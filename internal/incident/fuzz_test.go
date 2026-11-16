package incident

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"
	"uuid"
)

// fuzzTypes are the event types of FixtureLifecycle, plus merge_absorbed and
// severity_raised, the other event that sets a severity; fuzzPayloads holds a
// valid payload for each type whose payload carries no severity.
var fuzzTypes, fuzzPayloads = func() ([]EventType, map[EventType]json.RawMessage) {
	var types []EventType
	payloads := map[EventType]json.RawMessage{}
	for _, e := range FixtureLifecycle() {
		types = append(types, e.Type)
		payloads[e.Type] = e.Payload
	}
	return append(types, EvMergeAbsorbed, EvSeverityRaised), payloads
}()

// fuzzSeverities are the three severities and two the fold must refuse.
var fuzzSeverities = []Severity{SevGreen, SevYellow, SevRed, "", "purple"}

// fuzzSources are the two calls a fuzzed history may merge in.
var fuzzSources = [2]uuid.UUID{
	uuid.MustParse("0195f3a2-7c1e-7d4b-9a10-0000000000d1"),
	uuid.MustParse("0195f3a2-7c1e-7d4b-9a10-0000000000d2"),
}

// fuzzEvent decodes two bytes into the event at seq: the first picks the
// type, the second the severity (mod 5) and the merged call (div 5, mod 2).
func fuzzEvent(seq int64, kind, arg byte) Event {
	typ := fuzzTypes[int(kind)%len(fuzzTypes)]
	sev, src := fuzzSeverities[int(arg)%len(fuzzSeverities)], fuzzSources[int(arg)/len(fuzzSeverities)%2]
	payload := string(fuzzPayloads[typ])
	switch typ {
	case EvClassified:
		payload = fmt.Sprintf(`{"severity":%q}`, sev)
	case EvMergeAbsorbed:
		payload = fmt.Sprintf(`{"from":%q,"t1":%q}`, src, t0.Format(time.RFC3339Nano))
	case EvSeverityRaised:
		payload = fmt.Sprintf(`{"severity":%q,"from":%q}`, sev, src)
	}
	// Not ev(): its random ID makes the coverage of one input differ between
	// runs, and the fuzzer then stalls minimizing inputs it cannot reproduce.
	e := Event{IncidentID: fixtureID, Seq: seq, Type: typ, RecordedAt: t0.Add(time.Duration(seq) * time.Second),
		ActorKind: ActorSystem, Payload: json.RawMessage(payload), IdemKey: string(typ)}
	if typ == EvClassified {
		e.LogicVersion = fixtureLogic
	}
	return e
}

// severityNeverFalls applies the history data decodes to a started incident
// and fails if any accepted event lowers the severity or stores one that is
// not a severity, or a refused event changes it.
func severityNeverFalls(t *testing.T, data []byte) {
	p, err := Apply(Projection{}, fuzzEvent(1, 0, 0)) // session.started
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(data); i += 2 {
		e := fuzzEvent(p.Version+1, data[i], data[i+1])
		next, err := Apply(p, e)
		if err != nil {
			if next.Severity != p.Severity {
				t.Fatalf("refused %s %s changed the severity %q to %q", e.Type, e.Payload, p.Severity, next.Severity)
			}
			continue
		}
		if next.Severity.Rank() < p.Severity.Rank() || (next.Severity != "" && next.Severity.Rank() == 0) {
			t.Fatalf("%s %s took the severity %q to %q (history % x)", e.Type, e.Payload, p.Severity, next.Severity, data[:i+2])
		}
		p = next
	}
}

// FuzzFoldSeverityNeverFalls: whatever the history, no accepted event
// lowers the severity (hard constraint 3; evaluation audit V-04, ES-1).
func FuzzFoldSeverityNeverFalls(f *testing.F) {
	const classified, absorb, raise = 2, 10, 11 // indices in fuzzTypes
	const green, yellow, red, empty, purple = 0, 1, 2, 3, 4
	if fuzzTypes[classified] != EvClassified || fuzzTypes[absorb] != EvMergeAbsorbed || fuzzTypes[raise] != EvSeverityRaised {
		f.Fatalf("the seeds' type indices no longer match fuzzTypes %v", fuzzTypes)
	}
	for _, seed := range [][]byte{
		{absorb, 0, raise, red, classified, yellow}, // a classification below a raise
		{absorb, 0, raise, yellow, classified, green},
		{classified, red, absorb, 0, raise, yellow}, // a raise below a classification
		{absorb, 0, raise, red, raise, yellow},      // a raise below a raise
		{classified, purple}, {classified, empty},   // not a severity
		{absorb, 0, raise, purple + 5}, // the other call, not a severity
	} {
		f.Add(seed)
	}
	f.Fuzz(severityNeverFalls)
}

// The fuzz property over 20 000 generated histories in every unit run.
func TestFoldSeverityNeverFallsProperty(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	data := make([]byte, 32)
	for range 20_000 {
		n := r.IntN(len(data) + 1)
		for i := range n {
			data[i] = byte(r.Uint32())
		}
		severityNeverFalls(t, data[:n])
	}
}
