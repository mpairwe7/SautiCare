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
}

// Load reads the configuration directory (fsys rooted at config/). Decoding
// is strict: an unknown key is an error, so a typo cannot silently disable a
// parameter.
func Load(fsys fs.FS) (Bundle, error) {
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
	}
	h := sha256.New()
	for _, t := range targets {
		data, err := fs.ReadFile(fsys, t.path)
		if err != nil {
			return Bundle{}, fmt.Errorf("config: %w", err)
		}
		if err := decodeStrict(data, t.dst); err != nil {
			return Bundle{}, fmt.Errorf("config: %s: %w", t.path, err)
		}
	}
	for _, p := range slices.Sorted(slices.Values(gatedFiles)) {
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return Bundle{}, fmt.Errorf("config: %w", err)
		}
		sum := sha256.Sum256(data)
		h.Write([]byte(p))
		h.Write([]byte{0})
		h.Write(sum[:])
	}
	b.ProtocolStatus = proto.ProtocolStatus
	b.ProtocolVersion = proto.ProtocolVersion
	b.Fingerprint = "sha256:" + hex.EncodeToString(h.Sum(nil))
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
