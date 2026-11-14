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

// ADR-0029 rule 3: the fallback tries the coordinators a bounded number of
// times, holding the caller between tries only when the dial can succeed
// later, then tells the caller a coordinator will call back. The escalation
// recorded with the hand-over is what guarantees the call-back.
func TestFallbackDialplanRetriesThenCloses(t *testing.T) {
	lines := section(dialplan(t), FallbackContext)
	joined := strings.Join(lines, "\n")
	for _, want := range []string{
		"Dial(${SAUTI_COORDINATOR_DIAL},30)",
		`GotoIf($["${DIALSTATUS}"="ANSWER"]?done)`,
		"GotoIf($[${TRIES}>=3]?closing)",
		`GotoIf($["${DIALSTATUS}"="BUSY"|"${DIALSTATUS}"="NOANSWER"|"${DIALSTATUS}"="CHANUNAVAIL"|"${DIALSTATUS}"="CONGESTION"]?hold:closing)`,
		"same => n(hold),Playback(sauti/fallback-hold)",
		"same => n,Goto(dial)",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("fallback lacks %s", want)
		}
	}
	dial := slices.IndexFunc(lines, func(l string) bool { return strings.Contains(l, "same => n(dial),Set(TRIES=$[${TRIES}+1])") })
	if dial < 0 || !slices.Contains(lines[:dial], "same => n,Set(TRIES=0)") {
		t.Errorf("the try counter is not set before the dial loop: %q", lines)
	}
	n := len(lines)
	if n < 2 || lines[n-2] != "same => n(closing),Playback(sauti/fallback-closing)" || lines[n-1] != "same => n(done),Hangup()" {
		t.Errorf("the fallback does not end with the closing message, then a hang-up: %q", lines)
	}
}

// A carrier that delivers the DID as +256… reaches the same priorities as
// one that delivers 256… or 0…, so even the fallback runs (F-13).
func TestInboundContextsMatchPlusPrefixedDIDs(t *testing.T) {
	conf := dialplan(t)
	for _, ctx := range []string{"sauti-ivr-inbound", "sauti-bridge-inbound"} {
		plain, plus := extension(section(conf, ctx), "_X."), extension(section(conf, ctx), "_+X.")
		if len(plain) == 0 || !slices.Equal(plain, plus) {
			t.Errorf("%s: _X. %q, _+X. %q", ctx, plain, plus)
		}
	}
}

// The bridge number falls back to a coordinator when its Stasis
// application is not there, like the toll-free line.
func TestBridgeInboundFallsBackOnStasisFailure(t *testing.T) {
	inbound := section(dialplan(t), "sauti-bridge-inbound")
	stasis := slices.IndexFunc(inbound, func(l string) bool { return strings.Contains(l, "Stasis(sauti-alerts,bridge)") })
	if stasis < 0 || stasis+1 >= len(inbound) ||
		!strings.Contains(inbound[stasis+1], `GotoIf($["${STASISSTATUS}"="FAILED"]?`+FallbackContext+`,s,1)`) {
		t.Fatalf("no fallback straight after Stasis: %q", inbound)
	}
}

// extension returns the applications of one extension's priorities, in
// order: its "exten => <pattern>,1,…" line and the "same =>" lines after it.
func extension(lines []string, pattern string) []string {
	var out []string
	for i, l := range lines {
		if rest, ok := strings.CutPrefix(l, "exten => "+pattern+",1,"); ok {
			out = append(out, rest)
			for _, s := range lines[i+1:] {
				app, ok := strings.CutPrefix(s, "same => n,")
				if !ok {
					break
				}
				out = append(out, app)
			}
			return out
		}
	}
	return out
}

// Rule 5: the caller's number never reaches a coordinator's phone. The
// fallback masks the caller ID itself, before it answers and before any
// dial, on every path into it: an IVR hand-over, the bridge's hand-over, and
// a Stasis failure where no Go code runs. Nothing after the mask writes the
// caller ID again.
func TestFallbackMasksTheCallerBeforeAnyDial(t *testing.T) {
	conf := dialplan(t)
	lines := section(conf, FallbackContext)
	num := slices.Index(lines, "same => n,Set(CALLERID(num)=${SAUTI_CALLER_ID})")
	name := slices.Index(lines, "same => n,Set(CALLERID(name)=SAUTI ${SAUTI_REF})")
	first := func(app string) int {
		return slices.IndexFunc(lines, func(l string) bool { return strings.Contains(l, app) })
	}
	answer, dial := first("Answer("), first("Dial(")
	if num < 0 || name < 0 || answer < 0 || dial < 0 || max(num, name) > min(answer, dial) {
		t.Fatalf("the caller ID is not masked before the answer and every dial: %q", lines)
	}
	for _, l := range lines[max(num, name)+1:] {
		if strings.Contains(l, "CALLERID") {
			t.Fatalf("the caller ID is written again after the mask: %q", l)
		}
	}
	if !slices.ContainsFunc(section(conf, "globals"), func(l string) bool { return strings.HasPrefix(l, "SAUTI_CALLER_ID=") }) {
		t.Fatal("[globals] does not define SAUTI_CALLER_ID")
	}
}
