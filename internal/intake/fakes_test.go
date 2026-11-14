package intake

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"sync"
	"testing"
	"time"
	"uuid"

	"go.yaml.in/yaml/v4"

	"sautihealth.com/sauti/internal/crypto"
	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/location"
	"sautihealth.com/sauti/internal/pii"
	"sautihealth.com/sauti/internal/triage"
)

// memIncidents is an in-memory incident store that applies every event
// through incident.Apply, honours idempotency keys, and records sealed
// identifiers in the clear so tests can prove where they (do not) go.
type memIncidents struct {
	mu        sync.Mutex
	events    map[uuid.UUID][]incident.Event
	keys      map[string]incident.Event
	sessions  map[string]uuid.UUID // provider session → incident
	phones    map[uuid.UUID]string // incident → phone MAC key used at open
	idents    map[uuid.UUID]string // identifier id → plaintext
	identKeys map[string]uuid.UUID
	activity  map[uuid.UUID]time.Time
	now       time.Time
}

func newMemIncidents() *memIncidents {
	return &memIncidents{events: map[uuid.UUID][]incident.Event{}, keys: map[string]incident.Event{},
		sessions: map[string]uuid.UUID{}, phones: map[uuid.UUID]string{}, idents: map[uuid.UUID]string{},
		identKeys: map[string]uuid.UUID{}, activity: map[uuid.UUID]time.Time{}, now: time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)}
}

func phoneKey(p pii.Phone) string { return "mac:" + p.E164() }

func (m *memIncidents) appendLocked(id uuid.UUID, typ incident.EventType, payload json.RawMessage, idem, logic string) (incident.Event, error) {
	if e, ok := m.keys[id.String()+"|"+idem]; ok {
		return e, nil
	}
	evs := m.events[id]
	e := incident.Event{ID: uuid.NewV7(), IncidentID: id, Seq: int64(len(evs) + 1), Type: typ, RecordedAt: m.now,
		ActorKind: incident.ActorCaller, Payload: payload, IdemKey: idem, LogicVersion: logic}
	if _, err := incident.Fold(append(append([]incident.Event{}, evs...), e)); err != nil {
		return incident.Event{}, err
	}
	m.events[id] = append(evs, e)
	m.keys[id.String()+"|"+idem] = e
	m.activity[id] = m.now
	return e, nil
}

func (m *memIncidents) StartSession(_ context.Context, c incident.StartSession) (incident.Opened, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id, ok := m.sessions[string(c.Channel)+"|"+c.ProviderSessionID]; ok {
		p, _ := incident.Fold(m.events[id])
		return incident.Opened{IncidentID: id, Reference: p.Reference, Created: false}, nil
	}
	ref, err := incident.NewReference(rand.Reader)
	if err != nil {
		return incident.Opened{}, err
	}
	id := uuid.NewV7()
	payload, _ := json.Marshal(map[string]string{"reference": string(ref), "channel": string(c.Channel)})
	if _, err := m.appendLocked(id, incident.EvSessionStarted, payload, c.IdemKey, ""); err != nil {
		return incident.Opened{}, err
	}
	m.sessions[string(c.Channel)+"|"+c.ProviderSessionID] = id
	if !c.Phone.IsZero() {
		m.phones[id] = phoneKey(c.Phone)
	}
	return incident.Opened{IncidentID: id, Reference: ref, T1: m.now, Created: true}, nil
}

func (m *memIncidents) ResumeSession(_ context.Context, c incident.ResumeSession) (incident.Opened, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id, ok := m.sessions[string(c.Channel)+"|"+c.ProviderSessionID]; ok {
		return incident.Opened{IncidentID: id, Created: false}, nil
	}
	payload, _ := json.Marshal(map[string]string{"channel": string(c.Channel)})
	if _, err := m.appendLocked(c.IncidentID, incident.EvSessionResumed, payload, c.IdemKey, ""); err != nil {
		return incident.Opened{}, err
	}
	m.sessions[string(c.Channel)+"|"+c.ProviderSessionID] = c.IncidentID
	return incident.Opened{IncidentID: c.IncidentID, Created: true}, nil
}

func (m *memIncidents) Append(_ context.Context, a incident.AppendParams) (incident.Appended, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if a.IdemKey == "" {
		return incident.Appended{}, errors.New("no idempotency key")
	}
	e, err := m.appendLocked(a.IncidentID, a.Type, a.Payload, a.IdemKey, a.LogicVersion)
	return incident.Appended{Event: e}, err
}

// AppendBatch applies every event or none, like the store's transaction.
func (m *memIncidents) AppendBatch(_ context.Context, ps []incident.AppendParams) ([]incident.Appended, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	events, keys, activity := maps.Clone(m.events), maps.Clone(m.keys), maps.Clone(m.activity)
	out := make([]incident.Appended, 0, len(ps))
	for _, a := range ps {
		var e incident.Event
		err := errors.New("no idempotency key")
		if a.IdemKey != "" {
			e, err = m.appendLocked(a.IncidentID, a.Type, a.Payload, a.IdemKey, a.LogicVersion)
		}
		if err != nil {
			m.events, m.keys, m.activity = events, keys, activity
			return nil, err
		}
		out = append(out, incident.Appended{Event: e})
	}
	return out, nil
}

func (m *memIncidents) StoreIdentifier(_ context.Context, inc uuid.UUID, kind string, plaintext []byte, idem string) (uuid.UUID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := inc.String() + "|" + idem
	if id, ok := m.identKeys[k]; ok {
		return id, nil
	}
	id := uuid.NewV7()
	m.identKeys[k], m.idents[id] = id, string(plaintext)
	return id, nil
}

func (m *memIncidents) Projection(_ context.Context, id uuid.UUID) (incident.Projection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return incident.Fold(m.events[id])
}

// FindResumable uses the fake clock: activity within window of m.now.
func (m *memIncidents) FindResumable(_ context.Context, macs []crypto.MAC, window time.Duration) (uuid.UUID, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var best uuid.UUID
	found := false
	for id, k := range m.phones {
		match := false
		for _, mac := range macs {
			match = match || string(mac.Sum) == k
		}
		p, _ := incident.Fold(m.events[id])
		if !match || p.State.Terminal() || (p.LastScreen >= 8 && p.T[2] != nil) || m.now.Sub(m.activity[id]) > window {
			continue
		}
		if !found || m.activity[id].After(m.activity[best]) {
			best, found = id, true
		}
	}
	return best, found, nil
}

func (m *memIncidents) IVRCallIncident(_ context.Context, callID string) (uuid.UUID, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.sessions[string(incident.ChannelIVR)+"|"+callID]
	return id, ok, nil
}

func (m *memIncidents) allEvents() []incident.Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []incident.Event
	for _, evs := range m.events {
		out = append(out, evs...)
	}
	return out
}

func (k fakeKeys) PhoneLookupMACs(_ context.Context, p pii.Phone) ([]crypto.MAC, error) {
	return []crypto.MAC{{KeyID: "k1", Sum: []byte(phoneKey(p))}}, nil
}

type fakeMatcher map[string]location.Match

func (f fakeMatcher) Match(_ context.Context, _, _, text string) (location.Match, error) {
	return f[text], nil
}

type fixedTriage triage.Result

func (f fixedTriage) Classify(triage.Inputs) triage.Result { return triage.Result(f) }

func repoTriage(t *testing.T) *triage.Engine {
	t.Helper()
	doc, _ := os.ReadFile("../../triage-rules/tool1.yaml")
	sig, _ := os.ReadFile("../../triage-rules/tool1.yaml.sig")
	raw, _ := os.ReadFile("../../config/gated/triage.yaml")
	var trust struct {
		TrustedKeys map[string]string `yaml:"trusted_keys"`
	}
	if err := yaml.Unmarshal(raw, &trust); err != nil {
		t.Fatal(err)
	}
	keys, err := triage.DecodeTrustedKeys(trust.TrustedKeys)
	if err != nil {
		t.Fatal(err)
	}
	e, err := triage.Load(doc, sig, keys)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func plainSum(s string) []byte { h := sha256.Sum256([]byte(s)); return h[:] }

// DispatchOrMerge applies the dedup rule over the in-memory incidents, then
// appends dispatch_ready (and the merge events) like the store adapter: all
// of them or none.
func (m *memIncidents) DispatchOrMerge(_ context.Context, r DispatchRequest) (out DispatchOutcome, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.keys[r.IncidentID.String()+"|"+r.IdemKey]; ok {
		p, _ := incident.Fold(m.events[r.IncidentID])
		if p.MergedInto != nil {
			return DispatchOutcome{Merged: true, Survivor: *p.MergedInto}, nil
		}
		return DispatchOutcome{}, nil
	}
	events, keys, activity := maps.Clone(m.events), maps.Clone(m.keys), maps.Clone(m.activity)
	defer func() {
		if err != nil {
			m.events, m.keys, m.activity = events, keys, activity
		}
	}()
	var cands []Candidate
	for id, evs := range m.events {
		p, err := incident.Fold(evs)
		if err != nil || p.T[2] == nil || p.Zone == nil || p.State.Terminal() {
			continue
		}
		cands = append(cands, Candidate{ID: id, Category: p.Category, Zone: *p.Zone, LocationQuality: p.LocationQuality,
			Severity: p.Severity, Escalated: p.Escalated, T1: *p.T[1], T2: *p.T[2]})
	}
	if _, err := m.appendLocked(r.IncidentID, incident.EvDispatchReady, r.Payload, r.IdemKey, ""); err != nil {
		return DispatchOutcome{}, err
	}
	survivor, ok := r.Rule.Survivor(r.Call(), cands)
	if !ok {
		return DispatchOutcome{}, nil
	}
	into, _ := json.Marshal(map[string]string{"into": survivor.ID.String()})
	if _, err := m.appendLocked(r.IncidentID, incident.EvMerged, into, r.IdemKey+":merge", ""); err != nil {
		return DispatchOutcome{}, err
	}
	abs, _ := json.Marshal(map[string]string{"from": r.IncidentID.String(), "t1": r.T1.UTC().Format("2006-01-02T15:04:05.999999Z07:00")})
	if _, err := m.appendLocked(survivor.ID, incident.EvMergeAbsorbed, abs, "dedup:absorb:"+r.IncidentID.String(), ""); err != nil {
		return DispatchOutcome{}, err
	}
	// No acknowledgement exists in memory: the call's newest escalation is
	// unacknowledged.
	unacked := ""
	for _, e := range m.events[r.IncidentID] {
		if e.Type == incident.EvEscalated {
			unacked, _, _ = incident.ParseEscalated(e.Payload)
		}
	}
	cons, err := Consequences(r.Call(), survivor, unacked)
	if err != nil {
		return DispatchOutcome{}, err
	}
	if cons.Raise != nil {
		if _, err := m.appendLocked(survivor.ID, incident.EvSeverityRaised, cons.Raise, "dedup:raise:"+r.IncidentID.String(), ""); err != nil {
			return DispatchOutcome{}, err
		}
	}
	if cons.Escalate != nil {
		if _, err := m.appendLocked(survivor.ID, incident.EvEscalated, cons.Escalate, "dedup:escalate:"+r.IncidentID.String(), ""); err != nil {
			return DispatchOutcome{}, err
		}
	}
	return DispatchOutcome{Merged: true, Survivor: survivor.ID}, nil
}
