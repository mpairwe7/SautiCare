package wiring

import (
	"regexp"
	"testing"

	"sautihealth.com/sauti/internal/config"
)

// Every dispatch fact carries the ranking version and the fingerprint of
// the gated configuration it ran under (Build Prompt §10.4: stamp the
// version on each incident), in the format the audit ledger accepts.
func TestDispatchLogicVersion(t *testing.T) {
	b := config.Bundle{Dispatch: config.Dispatch{RankingVersion: "default-2026-11-12"},
		Fingerprint: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}
	got, err := DispatchLogicVersion(b)
	if err != nil || got != "default-2026-11-12+sha256:0123456789ab" {
		t.Fatalf("got %q, %v", got, err)
	}
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}\+sha256:[0-9a-f]{12,64}$`).MatchString(got) {
		t.Fatalf("%q is not an approved identifier", got)
	}
	for _, bad := range []config.Bundle{
		{Dispatch: config.Dispatch{RankingVersion: "Default 1"}, Fingerprint: b.Fingerprint},
		{Dispatch: config.Dispatch{RankingVersion: "v1"}, Fingerprint: "md5:abc"},
	} {
		if _, err := DispatchLogicVersion(bad); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
}
