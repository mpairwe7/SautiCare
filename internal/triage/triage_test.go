package triage

import (
	"crypto/mldsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"cel.dev/cel-go/cel"
	"go.yaml.in/yaml/v4"

	"sautihealth.com/sauti/internal/crypto"
)

const rules = `version: "tool1-test-1"
protocol_status: pending
inputs:
  danger: ["yes", "no", "unknown"]
  category: ["trauma", "obstetric", "acute_medical", "poisoning", "other"]
  age_band: ["under_5", "age_5_17", "age_18_59", "age_60_plus", "unknown"]
  specific_danger: ["yes", "no"]
rules:
  - id: red-danger
    when: 'danger == "yes" || specific_danger == "yes"'
    severity: red
  - id: yellow-danger-unknown
    when: 'danger == "unknown" && specific_danger == "no"'
    severity: yellow
  - id: yellow-time-critical
    when: 'category in ["poisoning"] && specific_danger == "no"'
    severity: yellow
  - id: green-default
    when: "true"
    severity: green
`

type keypair struct {
	id  string
	sk  *mldsa.PrivateKey
	pub []byte
}

func newKey(t *testing.T, id string) keypair {
	t.Helper()
	sk, err := mldsa.GenerateKey(mldsa.MLDSA65())
	if err != nil {
		t.Fatal(err)
	}
	return keypair{id: id, sk: sk, pub: sk.PublicKey().Bytes()}
}

func sign(t *testing.T, k keypair, doc string) []byte {
	t.Helper()
	sig, err := Sign(k.sk, k.id, []byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	return sig
}

func load(t *testing.T, doc string) *Engine {
	t.Helper()
	k := newKey(t, "clinical-test")
	e, err := Load([]byte(doc), sign(t, k, doc), map[string][]byte{k.id: k.pub})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// The Build Prompt §10.1 / Engineering Guide §5 rule, written by hand as an
// independent oracle for the rule table.
func oracle(in Inputs) string {
	switch {
	case in.Danger == "yes" || in.SpecificDanger == "yes":
		return "red"
	case in.Danger == "unknown" && in.SpecificDanger == "no":
		return "yellow"
	case in.Category == "poisoning" && in.SpecificDanger == "no":
		return "yellow"
	}
	return "green"
}

func TestScenario3PoisoningScreen5NoIsYellow(t *testing.T) {
	r := load(t, rules).Classify(Inputs{Danger: "no", Category: "poisoning", AgeBand: "age_18_59", SpecificDanger: "no"})
	if r.Severity != "yellow" || r.RuleError || r.RuleID != "yellow-time-critical" {
		t.Fatalf("scenario 3 = %+v, want yellow by yellow-time-critical", r)
	}
}

func TestRuleTableFromBuildPrompt(t *testing.T) {
	e := load(t, rules)
	n := 0
	for _, in := range e.Domain() {
		n++
		if got := e.Classify(in); got.Severity != oracle(in) || got.RuleError {
			t.Errorf("%+v = %+v, want %s", in, got, oracle(in))
		}
	}
	if n != 3*5*5*2 {
		t.Fatalf("domain has %d combinations, want 150", n)
	}
}

func TestLoadRejectsBadSignature(t *testing.T) {
	k := newKey(t, "clinical-test")
	sig := sign(t, k, rules)
	tampered := strings.Replace(rules, `category in ["poisoning"]`, `category in []`, 1)
	if _, err := Load([]byte(tampered), sig, map[string][]byte{k.id: k.pub}); !errors.Is(err, ErrSignature) {
		t.Fatalf("tampered bundle: err = %v, want ErrSignature", err)
	}
	if _, err := Load([]byte(rules), []byte("{}"), map[string][]byte{k.id: k.pub}); !errors.Is(err, ErrSignature) {
		t.Fatalf("empty signature file: err = %v", err)
	}
	// A checkpoint signature over the same bytes is not a rules signature.
	cp, _ := crypto.SignMLDSA65(k.sk, []byte(rules))
	forged, _ := json.Marshal(sigFile{KeyID: k.id, Algorithm: "ML-DSA-65", Signature: base64.StdEncoding.EncodeToString(cp)})
	if _, err := Load([]byte(rules), forged, map[string][]byte{k.id: k.pub}); !errors.Is(err, ErrSignature) {
		t.Fatalf("checkpoint-context signature accepted: %v", err)
	}
}

func TestLoadRejectsUntrustedKey(t *testing.T) {
	signer, other := newKey(t, "clinical-a"), newKey(t, "clinical-b")
	if _, err := Load([]byte(rules), sign(t, signer, rules), map[string][]byte{other.id: other.pub}); !errors.Is(err, ErrSignature) {
		t.Fatalf("untrusted key ID: err = %v", err)
	}
	// Same key ID, different key: still refused.
	if _, err := Load([]byte(rules), sign(t, signer, rules), map[string][]byte{signer.id: other.pub}); !errors.Is(err, ErrSignature) {
		t.Fatalf("wrong public key under a trusted ID: err = %v", err)
	}
}

// Every combination of the declared inputs must reach a row: a table with a
// gap (here, no default row) is refused at load, before it can run.
func TestLoadRejectsNonTotalTable(t *testing.T) {
	gap := strings.Replace(rules, "  - id: green-default\n    when: \"true\"\n    severity: green\n", "", 1)
	for name, doc := range map[string]string{
		"gap":                gap,
		"bad severity":       strings.Replace(rules, "severity: green", "severity: blue", 1),
		"not boolean":        strings.Replace(rules, `when: "true"`, `when: '"true"'`, 1),
		"unknown input":      strings.Replace(rules, `when: "true"`, `when: 'colour == "x"'`, 1),
		"duplicate id":       strings.Replace(rules, "id: green-default", "id: red-danger", 1),
		"empty domain":       strings.Replace(rules, `specific_danger: ["yes", "no"]`, `specific_danger: []`, 1),
		"no version":         strings.Replace(rules, `version: "tool1-test-1"`, `version: ""`, 1),
		"version not a code": strings.Replace(rules, `version: "tool1-test-1"`, `version: "Tool1-v1"`, 1),
		"unknown key":        strings.Replace(rules, "protocol_status: pending", "protocol_status: pending\nauthor: x", 1),
		"bad status":         strings.Replace(rules, "protocol_status: pending", "protocol_status: draft", 1),
		"missing input":      strings.Replace(rules, "  age_band: [\"under_5\", \"age_5_17\", \"age_18_59\", \"age_60_plus\", \"unknown\"]\n", "", 1),
		"syntax":             strings.Replace(rules, `when: "true"`, `when: "danger =="`, 1),
	} {
		k := newKey(t, "clinical-test")
		if _, err := Load([]byte(doc), sign(t, k, doc), map[string][]byte{k.id: k.pub}); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
}

// CodeRabbit PR #6: a bundle that fits maxBundleBytes can still declare a
// domain whose expansion exhausts memory before the totality check runs. Its
// size is checked first: every input answers a single-digit menu screen.
func TestLoadRejectsOversizedDomain(t *testing.T) {
	ten := `["v0", "v1", "v2", "v3", "v4", "v5", "v6", "v7", "v8", "v9"]`
	doc := rules
	for _, in := range []string{
		`danger: ["yes", "no", "unknown"]`,
		`category: ["trauma", "obstetric", "acute_medical", "poisoning", "other"]`,
		`age_band: ["under_5", "age_5_17", "age_18_59", "age_60_plus", "unknown"]`,
		`specific_danger: ["yes", "no"]`,
	} {
		doc = strings.Replace(doc, in, in[:strings.Index(in, "[")]+ten, 1)
	}
	k := newKey(t, "clinical-test")
	_, err := Load([]byte(doc), sign(t, k, doc), map[string][]byte{k.id: k.pub})
	if !errors.Is(err, ErrBundle) || !strings.Contains(err.Error(), "domain") {
		t.Fatalf("10^4 combinations: err = %v, want ErrBundle naming the domain", err)
	}
}

// Fail safe (Build Prompt §14): a rule that errors at run time, or an input
// outside the declared domain, gives red and is marked as a rule error so the
// coordinator is alerted.
func TestRuntimeErrorIsRed(t *testing.T) {
	e := load(t, rules)
	for _, in := range []Inputs{
		{Danger: "no", Category: "snakebite", AgeBand: "age_18_59", SpecificDanger: "no"},
		{Danger: "", Category: "trauma", AgeBand: "age_18_59", SpecificDanger: "no"},
	} {
		if r := e.Classify(in); r.Severity != "red" || !r.RuleError {
			t.Errorf("%+v = %+v, want red rule error", in, r)
		}
	}
	// A row that errors when evaluated (division by zero) also yields red.
	divide := strings.Replace(rules, `when: 'danger == "unknown" && specific_danger == "no"'`,
		`when: 'danger == "unknown" && 1 / (size(category) - size(category)) == 0'`, 1)
	k := newKey(t, "clinical-test")
	if _, err := Load([]byte(divide), sign(t, k, divide), map[string][]byte{k.id: k.pub}); err == nil {
		t.Fatal("a row that errors on part of the domain loaded (totality check)")
	}
	e.programs[1] = mustCompile(t, e, `1 / (size(category) - size(category)) == 0`)
	if r := e.Classify(Inputs{Danger: "unknown", Category: "trauma", AgeBand: "age_18_59", SpecificDanger: "no"}); r.Severity != "red" || !r.RuleError {
		t.Fatalf("runtime evaluation error = %+v, want red rule error", r)
	}
}

func TestLogicVersionAndDevSigned(t *testing.T) {
	e := load(t, rules)
	if !strings.HasPrefix(e.LogicVersion(), "tool1-test-1+sha256:") || len(e.LogicVersion()) != len("tool1-test-1+sha256:")+12 {
		t.Fatalf("LogicVersion = %q", e.LogicVersion())
	}
	if e.DevSigned() || e.ProductionReady() == nil {
		t.Fatalf("clinical-signed pending bundle: DevSigned=%v ProductionReady=%v", e.DevSigned(), e.ProductionReady())
	}
	k := newKey(t, "dev-triage")
	approved := strings.Replace(rules, "protocol_status: pending", "protocol_status: approved", 1)
	d, err := Load([]byte(approved), sign(t, k, approved), map[string][]byte{k.id: k.pub})
	if err != nil {
		t.Fatal(err)
	}
	if !d.DevSigned() || d.ProductionReady() == nil {
		t.Fatal("a dev-signed bundle must never be production ready")
	}
}

type scenario struct {
	Name     string `yaml:"name"`
	Inputs   Inputs `yaml:"inputs"`
	Severity string `yaml:"severity"`
}

// The committed bundle is signed by a key trusted in config/gated/triage.yaml,
// and passes every clinical scenario in triage-rules/scenarios.yaml.
func TestRepositoryBundleVerifies(t *testing.T) {
	doc, err := os.ReadFile("../../triage-rules/tool1.yaml")
	if err != nil {
		t.Fatal(err)
	}
	sig, err := os.ReadFile("../../triage-rules/tool1.yaml.sig")
	if err != nil {
		t.Fatal(err)
	}
	trustDoc, err := os.ReadFile("../../config/gated/triage.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var trust struct {
		TrustedKeys map[string]string `yaml:"trusted_keys"`
	}
	if err := yaml.Unmarshal(trustDoc, &trust); err != nil {
		t.Fatal(err)
	}
	keys, err := DecodeTrustedKeys(trust.TrustedKeys)
	if err != nil {
		t.Fatal(err)
	}
	e, err := Load(doc, sig, keys)
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range e.Domain() {
		if got := e.Classify(in); got.Severity != oracle(in) {
			t.Errorf("repository bundle: %+v = %s, want %s", in, got.Severity, oracle(in))
		}
	}
	raw, err := os.ReadFile("../../triage-rules/scenarios.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var cases []scenario
	if err := yaml.Unmarshal(raw, &cases); err != nil || len(cases) == 0 {
		t.Fatalf("scenarios: %v (%d)", err, len(cases))
	}
	for _, c := range cases {
		if got := e.Classify(c.Inputs); got.Severity != c.Severity || got.RuleError {
			t.Errorf("%s: %+v, want %s", c.Name, got, c.Severity)
		}
	}
}

func mustCompile(t *testing.T, e *Engine, expr string) cel.Program {
	t.Helper()
	p, err := e.compile(expr)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
