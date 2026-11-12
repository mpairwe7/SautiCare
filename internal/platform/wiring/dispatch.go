package wiring

import (
	"fmt"
	"regexp"
	"strings"

	"sautihealth.com/sauti/internal/config"
)

var logicVersion = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}\+sha256:[0-9a-f]{12,64}$`)

// DispatchLogicVersion is the version stamped on every dispatch fact: the
// ranking version (decision 4) and the fingerprint of the gated
// configuration in force, e.g. "default-2026-11-12+sha256:0123456789ab".
func DispatchLogicVersion(b config.Bundle) (string, error) {
	hexDigits, ok := strings.CutPrefix(b.Fingerprint, "sha256:")
	if !ok || len(hexDigits) < 12 {
		return "", fmt.Errorf("wiring: configuration fingerprint %q is not sha256", b.Fingerprint)
	}
	v := b.Dispatch.RankingVersion + "+sha256:" + hexDigits[:12]
	if !logicVersion.MatchString(v) {
		return "", fmt.Errorf("wiring: dispatch logic version %q is not an approved identifier", v)
	}
	return v, nil
}
