// Package triage classifies severity from Tool 1 answers, deterministically
// and server side (Build Prompt §3 rule 3, §10.1; Engineering Guide §5).
//
// The rules are clinical content, authored and signed by the emergency
// medicine co-investigators, not code (Guide §14). A bundle is an ordered
// decision table whose conditions are CEL expressions over the four inputs;
// the first matching row gives the severity. A bundle loads only if its
// ML-DSA-65 signature verifies against a trusted key and every combination of
// its declared inputs reaches a row without error (totality). At run time an
// error, no match, or an input outside the declared domain gives red and is
// reported as a rule error, so the coordinator is alerted (§14 fallbacks).
package triage

import (
	"bytes"
	"crypto/mldsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	"cel.dev/cel-go/cel"
	"go.yaml.in/yaml/v4"

	"sautihealth.com/sauti/internal/crypto"
)

// Severities a row may give.
const (
	Red    = "red"
	Yellow = "yellow"
	Green  = "green"
)

// RuleErrorID is the RuleID of a fail-safe red classification.
const RuleErrorID = "rule_error"

const (
	maxBundleBytes = 64 << 10
	algorithm      = "ML-DSA-65"
	devKeyPrefix   = "dev-"
	celCostLimit   = 10_000
)

var (
	// ErrSignature reports a bundle whose signature is missing, malformed,
	// by an untrusted key or does not verify.
	ErrSignature = errors.New("triage: bundle signature not valid")
	// ErrBundle reports a bundle that is malformed or not total.
	ErrBundle = errors.New("triage: invalid rule bundle")
	// ErrNotProductionReady reports a pending or dev-signed bundle.
	ErrNotProductionReady = errors.New("triage: bundle not production ready")
)

var versionPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// inputNames are the CEL variables, in Domain order.
var inputNames = []string{"danger", "category", "age_band", "specific_danger"}

// Inputs are the Tool 1 answers that triage reads (screens 2-5).
type Inputs struct {
	Danger         string `yaml:"danger"`
	Category       string `yaml:"category"`
	AgeBand        string `yaml:"age_band"`
	SpecificDanger string `yaml:"specific_danger"`
}

func (in Inputs) values() []string {
	return []string{in.Danger, in.Category, in.AgeBand, in.SpecificDanger}
}

// Result is one classification.
type Result struct {
	Severity     string
	RuleID       string
	LogicVersion string
	RuleError    bool
}

type rule struct {
	ID       string `yaml:"id"`
	When     string `yaml:"when"`
	Severity string `yaml:"severity"`
}

type bundle struct {
	Version        string              `yaml:"version"`
	ProtocolStatus string              `yaml:"protocol_status"`
	Inputs         map[string][]string `yaml:"inputs"`
	Rules          []rule              `yaml:"rules"`
}

type sigFile struct {
	KeyID     string `json:"key_id"`
	Algorithm string `json:"algorithm"`
	Signature string `json:"signature"`
}

// Engine is a verified, total rule bundle.
type Engine struct {
	version, status, keyID, digest string
	domain                         [][]string
	rules                          []rule
	env                            *cel.Env
	programs                       []cel.Program
}

// Sign produces the detached signature file for a bundle (`sautictl triage
// sign`; in production the clinical sign-off key signs through OpenBao).
func Sign(sk *mldsa.PrivateKey, keyID string, doc []byte) ([]byte, error) {
	sig, err := crypto.SignWithContext(sk, crypto.TriageRulesContext, doc)
	if err != nil {
		return nil, err
	}
	out, err := json.MarshalIndent(sigFile{KeyID: keyID, Algorithm: algorithm, Signature: base64.StdEncoding.EncodeToString(sig)}, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// DecodeTrustedKeys decodes base64 ML-DSA-65 public keys by key ID.
func DecodeTrustedKeys(enc map[string]string) (map[string][]byte, error) {
	out := make(map[string][]byte, len(enc))
	for id, b64 := range enc {
		pub, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, fmt.Errorf("triage: trusted key %s: %w", id, err)
		}
		if _, err := mldsa.NewPublicKey(mldsa.MLDSA65(), pub); err != nil {
			return nil, fmt.Errorf("triage: trusted key %s: %w", id, err)
		}
		out[id] = pub
	}
	return out, nil
}

func verify(doc, sigDoc []byte, trusted map[string][]byte) (string, error) {
	var sf sigFile
	dec := json.NewDecoder(bytes.NewReader(sigDoc))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&sf); err != nil {
		return "", fmt.Errorf("%w: signature file: %w", ErrSignature, err)
	}
	if sf.Algorithm != algorithm {
		return "", fmt.Errorf("%w: algorithm %q", ErrSignature, sf.Algorithm)
	}
	pub, ok := trusted[sf.KeyID]
	if !ok {
		return "", fmt.Errorf("%w: key %q is not trusted", ErrSignature, sf.KeyID)
	}
	sig, err := base64.StdEncoding.DecodeString(sf.Signature)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrSignature, err)
	}
	if err := crypto.VerifyWithContext(pub, crypto.TriageRulesContext, doc, sig); err != nil {
		return "", fmt.Errorf("%w: %w", ErrSignature, err)
	}
	return sf.KeyID, nil
}

// Load verifies, parses, compiles and totality-checks a bundle.
func Load(doc, sigDoc []byte, trusted map[string][]byte) (*Engine, error) {
	if len(doc) > maxBundleBytes {
		return nil, fmt.Errorf("%w: %d bytes", ErrBundle, len(doc))
	}
	keyID, err := verify(doc, sigDoc, trusted)
	if err != nil {
		return nil, err
	}
	var b bundle
	dec := yaml.NewDecoder(bytes.NewReader(doc))
	dec.KnownFields(true)
	if err := dec.Decode(&b); err != nil {
		if errors.Is(err, io.EOF) {
			err = errors.New("empty")
		}
		return nil, fmt.Errorf("%w: %w", ErrBundle, err)
	}
	bad := func(format string, a ...any) (*Engine, error) {
		return nil, fmt.Errorf("%w: "+format, append([]any{ErrBundle}, a...)...)
	}
	// The logic version "<version>+sha256:<hex>" must pass the ledger's and
	// the write path's format check, so the version is a lower-case code.
	if !versionPattern.MatchString(b.Version) {
		return bad("version %q must match %s", b.Version, versionPattern)
	}
	if b.ProtocolStatus != "pending" && b.ProtocolStatus != "approved" {
		return bad("protocol_status %q", b.ProtocolStatus)
	}
	sum := sha256.Sum256(doc)
	e := &Engine{version: b.Version, status: b.ProtocolStatus, keyID: keyID, digest: hex.EncodeToString(sum[:])}
	if len(b.Inputs) != len(inputNames) {
		return bad("inputs must declare exactly %v", inputNames)
	}
	var opts []cel.EnvOption
	for _, name := range inputNames {
		vals, ok := b.Inputs[name]
		if !ok || len(vals) == 0 {
			return bad("input %s needs at least one value", name)
		}
		for i, v := range vals {
			if v == "" || slices.Contains(vals[:i], v) {
				return bad("input %s: empty or duplicate value %q", name, v)
			}
		}
		e.domain = append(e.domain, vals)
		opts = append(opts, cel.Variable(name, cel.StringType))
	}
	if e.env, err = cel.NewEnv(opts...); err != nil {
		return nil, err
	}
	if len(b.Rules) == 0 {
		return bad("no rules")
	}
	seen := map[string]bool{}
	for _, r := range b.Rules {
		if r.ID == "" || r.ID == RuleErrorID || seen[r.ID] {
			return bad("rule id %q empty, reserved or duplicate", r.ID)
		}
		seen[r.ID] = true
		if r.Severity != Red && r.Severity != Yellow && r.Severity != Green {
			return bad("rule %s: severity %q", r.ID, r.Severity)
		}
		p, err := e.compile(r.When)
		if err != nil {
			return bad("rule %s: %v", r.ID, err)
		}
		e.rules = append(e.rules, r)
		e.programs = append(e.programs, p)
	}
	for _, in := range e.Domain() {
		if r := e.Classify(in); r.RuleError {
			return bad("not total: %+v reaches no row without error", in)
		}
	}
	return e, nil
}

func (e *Engine) compile(expr string) (cel.Program, error) {
	ast, iss := e.env.Compile(expr)
	if iss.Err() != nil {
		return nil, iss.Err()
	}
	if ast.OutputType() != cel.BoolType {
		return nil, fmt.Errorf("condition %q is %v, want bool", expr, ast.OutputType())
	}
	return e.env.Program(ast, cel.CostLimit(celCostLimit))
}

// Domain returns every combination of the declared input values.
func (e *Engine) Domain() []Inputs {
	out := []Inputs{{}}
	for i := range inputNames {
		var next []Inputs
		for _, partial := range out {
			for _, v := range e.domain[i] {
				in := partial
				switch i {
				case 0:
					in.Danger = v
				case 1:
					in.Category = v
				case 2:
					in.AgeBand = v
				case 3:
					in.SpecificDanger = v
				}
				next = append(next, in)
			}
		}
		out = next
	}
	return out
}

// Classify returns the severity for in. It never fails: an input outside the
// declared domain, an evaluation error or no matching row gives red with
// RuleError set (Build Prompt §14: "Triage rule error: treat as red and alert
// the coordinator").
func (e *Engine) Classify(in Inputs) (res Result) {
	fail := Result{Severity: Red, RuleID: RuleErrorID, LogicVersion: e.LogicVersion(), RuleError: true}
	defer func() {
		if recover() != nil {
			res = fail
		}
	}()
	vals := in.values()
	vars := make(map[string]any, len(inputNames))
	for i, name := range inputNames {
		if !slices.Contains(e.domain[i], vals[i]) {
			return fail
		}
		vars[name] = vals[i]
	}
	for i, p := range e.programs {
		out, _, err := p.Eval(vars)
		if err != nil {
			return fail
		}
		match, ok := out.Value().(bool)
		if !ok {
			return fail
		}
		if match {
			return Result{Severity: e.rules[i].Severity, RuleID: e.rules[i].ID, LogicVersion: e.LogicVersion()}
		}
	}
	return fail
}

// LogicVersion identifies the exact bundle: "<version>+sha256:<12 hex>". It is
// stamped on every incident.classified event.
func (e *Engine) LogicVersion() string { return e.version + "+sha256:" + e.digest[:12] }

// DevSigned reports a bundle signed by a development key.
func (e *Engine) DevSigned() bool { return strings.HasPrefix(e.keyID, devKeyPrefix) }

// Values returns the declared values of one input (for cross-checking the
// Tool 1 catalogue against the bundle).
func (e *Engine) Values(input string) []string {
	if i := slices.Index(inputNames, input); i >= 0 {
		return slices.Clone(e.domain[i])
	}
	return nil
}

// ProductionReady reports whether the bundle may run with SAUTI_ENV=production:
// approved protocol content signed by a non-development key.
func (e *Engine) ProductionReady() error {
	switch {
	case e.status != "approved":
		return fmt.Errorf("%w: protocol_status %s", ErrNotProductionReady, e.status)
	case e.DevSigned():
		return fmt.Errorf("%w: signed by development key %s", ErrNotProductionReady, e.keyID)
	}
	return nil
}
