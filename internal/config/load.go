package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"slices"

	"go.yaml.in/yaml/v4"
)

// gatedFiles are the files under the amendment gate, relative to the config
// directory. Their bytes, with their paths, make up Bundle.Fingerprint.
var gatedFiles = []string{
	"gated/dispatch.yaml",
	"gated/flags.yaml",
	"gated/intake.yaml",
	"gated/protocol.yaml",
	"gated/screens.yaml",
	"gated/triage.yaml",
	"gated/tool1.yaml",
	"gated/sms.yaml",
	"gated/responder.yaml",
}

// separatelyVersioned are gated files with their own version, stamped where
// they are used rather than in Fingerprint: the gazetteer is loaded into the
// operational store and recorded in op.config_versions (kind gazetteer).
var separatelyVersioned = []string{
	"gated/gazetteer.yaml",
}

// checkGatedDir fails on any file under gated/ that Load would not use.
func checkGatedDir(fsys fs.FS) error {
	entries, err := fs.ReadDir(fsys, "gated")
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	for _, e := range entries {
		p := "gated/" + e.Name()
		if !slices.Contains(gatedFiles, p) && !slices.Contains(separatelyVersioned, p) {
			return fmt.Errorf("config: unknown gated file %s: add it to Load or to separatelyVersioned", p)
		}
	}
	return nil
}

// Load reads the configuration directory (fsys rooted at config/). Decoding
// is strict: an unknown key is an error, so a typo cannot silently disable a
// parameter.
func Load(fsys fs.FS) (Bundle, error) {
	if err := checkGatedDir(fsys); err != nil {
		return Bundle{}, err
	}
	var b Bundle
	var proto protocolFile
	targets := []struct {
		path string
		dst  any
	}{
		{"study.yaml", &b.Study},
		{"platform.yaml", &b.Platform},
		{"gated/protocol.yaml", &proto},
		{"gated/intake.yaml", &b.Intake},
		{"gated/dispatch.yaml", &b.Dispatch},
		{"gated/flags.yaml", &b.Flags},
		{"gated/screens.yaml", &b.Screens},
		{"gated/triage.yaml", &b.Triage},
		{"gated/tool1.yaml", &b.Tool1},
		{"gated/sms.yaml", &b.SMS},
		{"gated/responder.yaml", &b.Responder},
	}
	// Each file is read exactly once; the fingerprint covers the very bytes
	// that were decoded, so it always identifies the parameters in force.
	read := map[string][]byte{}
	for _, t := range targets {
		data, err := fs.ReadFile(fsys, t.path)
		if err != nil {
			return Bundle{}, fmt.Errorf("config: %w", err)
		}
		read[t.path] = data
		if err := decodeStrict(data, t.dst); err != nil {
			return Bundle{}, fmt.Errorf("config: %s: %w", t.path, err)
		}
	}
	h := sha256.New()
	for _, p := range slices.Sorted(slices.Values(gatedFiles)) {
		data, ok := read[p]
		if !ok {
			return Bundle{}, fmt.Errorf("config: gated file %s was not loaded", p)
		}
		sum := sha256.Sum256(data)
		h.Write([]byte(p))
		h.Write([]byte{0})
		h.Write(sum[:])
	}
	b.ProtocolStatus = proto.ProtocolStatus
	b.ProtocolVersion = proto.ProtocolVersion
	b.Fingerprint = "sha256:" + hex.EncodeToString(h.Sum(nil))
	if b.Intake.DedupWindow.Duration <= 0 || b.Intake.DedupMaxZoneDistance < 0 {
		return Bundle{}, errors.New("config: intake dedup_window must be positive and dedup_max_zone_distance not negative")
	}
	if b.Intake.AbandonAfter.Duration <= 0 {
		return Bundle{}, errors.New("config: intake abandon_after must be positive")
	}
	if s := b.Intake.FuzzyMatchMinSimilarity; s <= 0 || s >= 1 {
		return Bundle{}, fmt.Errorf("config: intake fuzzy_match_min_similarity %v must be between 0 and 1", s)
	}
	if err := b.Tool1.Validate(); err != nil {
		return Bundle{}, err
	}
	if err := b.SMS.Validate(); err != nil {
		return Bundle{}, fmt.Errorf("config: gated/sms.yaml: %w", err)
	}
	if err := b.Responder.Validate(); err != nil {
		return Bundle{}, fmt.Errorf("config: gated/responder.yaml: %w", err)
	}
	if b.Triage.RulesFile == "" || len(b.Triage.TrustedKeys) == 0 {
		return Bundle{}, errors.New("config: gated/triage.yaml needs rules_file and at least one trusted key")
	}
	return b, nil
}

func decodeStrict(data []byte, dst any) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return errors.New("empty file")
		}
		return err
	}
	return nil
}
