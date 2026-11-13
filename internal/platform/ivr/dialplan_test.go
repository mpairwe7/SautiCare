package ivr

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const dialplanPath = "../../../telephony/asterisk/extensions.conf"

func dialplan(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(dialplanPath)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// section returns the lines of one [context], comments dropped.
func section(conf, name string) []string {
	var out []string
	in := false
	for _, line := range strings.Split(conf, "\n") {
		line = strings.TrimSpace(line)
		if i := strings.Index(line, ";"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if strings.HasPrefix(line, "[") {
			in = line == "["+name+"]"
			continue
		}
		if in && line != "" {
			out = append(out, line)
		}
	}
	return out
}

// I4 (hard constraint 14): a voice call never ends in a bare hang-up
// because Sauti cannot serve it. If the Stasis application is not there
// (ivr-controller down, ARI disconnected), the dialplan goes to the fallback
// context, which the controller also uses for a call it cannot serve: a
// recorded message, then the on-call coordinators. Nothing is recorded.
func TestDialplanFallsBackToACoordinator(t *testing.T) {
	conf := dialplan(t)
	inbound := section(conf, "sauti-ivr-inbound")
	stasis := slices.IndexFunc(inbound, func(l string) bool { return strings.Contains(l, "Stasis(sauti-ivr)") })
	if stasis < 0 || stasis+1 >= len(inbound) ||
		!strings.Contains(inbound[stasis+1], `GotoIf($["${STASISSTATUS}"="FAILED"]?`+FallbackContext+`,s,1)`) {
		t.Fatalf("no fallback straight after Stasis: %q", inbound)
	}
	fallback := section(conf, FallbackContext)
	joined := strings.Join(fallback, "\n")
	if !strings.HasPrefix(joined, "exten => s,1,") || !strings.Contains(joined, "Playback(sauti/fallback)") ||
		!strings.Contains(joined, "Dial(${SAUTI_COORDINATOR_DIAL}") {
		t.Fatalf("fallback context = %q", fallback)
	}
	globals := strings.Join(section(conf, "globals"), "\n")
	if !strings.Contains(globals, "SAUTI_COORDINATOR_DIAL=") || regexp.MustCompile(`\+?256\d{9}|0[37]\d{8}`).MatchString(globals) {
		t.Fatalf("globals must define a placeholder coordinator dial string and no number: %q", globals)
	}
	for _, banned := range []string{"Record(", "MixMonitor(", "Monitor(", "ChanSpy("} {
		if strings.Contains(strings.Join(section(conf, "sauti-ivr-inbound"), "\n")+joined, banned) {
			t.Errorf("dialplan uses %s", banned)
		}
	}
}

// Every sound the dialplan plays is in the recording catalogue.
func TestDialplanPlaysOnlyCataloguedSounds(t *testing.T) {
	want := requiredSounds(t)
	for _, m := range regexp.MustCompile(`Playback\(([^)]+)\)`).FindAllStringSubmatch(dialplan(t), -1) {
		for _, s := range strings.Split(m[1], "&") {
			if !slices.Contains(want, s) {
				t.Errorf("dialplan plays %q, which is not in the catalogue", s)
			}
		}
	}
}
