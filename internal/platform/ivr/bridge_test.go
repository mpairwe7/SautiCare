package ivr

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/crypto"
	"sautihealth.com/sauti/internal/dispatch"
	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/pii"
	"sautihealth.com/sauti/internal/platform/ari"
	"sautihealth.com/sauti/internal/responder"
)

// bridgeARI is a fake Asterisk for the masked bridge.
type bridgeARI struct {
	mu        sync.Mutex
	endpoints []string
	callerIDs []string
	args      [][]string
	answered  []string
	plays     map[string][][]string
	pbs       map[string][]string
	hungUp    []string
	bridged   map[string][]string
	destroyed []string
	failDial  bool
	ops       []string          // bridge and caller-ID operations, in order
	callerID  map[string]string // channel → caller ID Sauti set
	continued []string
}

func (a *bridgeARI) Continue(_ context.Context, ch, dialContext, _ string, _ int) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.continued = append(a.continued, ch+"→"+dialContext)
	return nil
}
func (a *bridgeARI) SetCallerID(_ context.Context, ch, number string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.callerID[ch] = number
	a.ops = append(a.ops, "callerid:"+ch)
	return nil
}

func newBridgeARI() *bridgeARI {
	return &bridgeARI{plays: map[string][][]string{}, pbs: map[string][]string{}, bridged: map[string][]string{}, callerID: map[string]string{}}
}

func (a *bridgeARI) Originate(_ context.Context, endpoint, callerID string, args []string, _ time.Duration) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.failDial {
		return "", fmt.Errorf("%w: trunk refused", dispatch.ErrRejected)
	}
	a.endpoints, a.callerIDs, a.args = append(a.endpoints, endpoint), append(a.callerIDs, callerID), append(a.args, args)
	return fmt.Sprintf("leg-%d", len(a.endpoints)), nil
}
func (a *bridgeARI) Answer(_ context.Context, ch string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.answered = append(a.answered, ch)
	return nil
}
func (a *bridgeARI) Play(_ context.Context, ch, pb string, sounds []string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.plays[ch] = append(a.plays[ch], sounds)
	a.pbs[ch] = append(a.pbs[ch], pb)
	return nil
}
func (a *bridgeARI) StopPlayback(context.Context, string) error { return nil }
func (a *bridgeARI) Hangup(_ context.Context, ch string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.hungUp = append(a.hungUp, ch)
	return nil
}
func (a *bridgeARI) CreateBridge(_ context.Context, id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.bridged[id] = []string{}
	return nil
}
func (a *bridgeARI) AddToBridge(_ context.Context, id, ch string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.bridged[id] = append(a.bridged[id], ch)
	a.ops = append(a.ops, "bridge:"+ch)
	return nil
}
func (a *bridgeARI) DestroyBridge(_ context.Context, id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.destroyed = append(a.destroyed, id)
	return nil
}
func (a *bridgeARI) lastSounds(ch string) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	p := a.plays[ch]
	if len(p) == 0 {
		return nil
	}
	return p[len(p)-1]
}
func (a *bridgeARI) lastPlayback(ch string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.pbs[ch][len(a.pbs[ch])-1]
}
func (a *bridgeARI) wasHungUp(ch string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Contains(a.hungUp, ch)
}

const (
	callerNum    = "+256772000111" // the person who reported the emergency
	responderNum = "+256772000222" // the responder
	sautiNum     = "+256800100100" // Sauti's bridge number (placeholder)
)

// macOf is a stand-in keyed hash: one MAC per number.
func macOf(p string) []crypto.MAC { return []crypto.MAC{{KeyID: "k", Sum: []byte(p)}} }

type bridgeKeys struct{}

func (bridgeKeys) PhoneLookupMACs(_ context.Context, p pii.Phone) ([]crypto.MAC, error) {
	return macOf(p.E164()), nil
}

type bridgeStore struct {
	mu         sync.Mutex
	failLookup bool
	responders map[string]uuid.UUID // MAC sum → responder
	byResp     map[uuid.UUID][]dispatch.BridgeTarget
	byCaller   map[string][]dispatch.BridgeTarget // MAC sum → targets
	begun      []string                           // initiated_by
	answered   int
	finished   map[uuid.UUID]string
}

func (s *bridgeStore) ResponderByPhone(_ context.Context, macs []crypto.MAC) (uuid.UUID, bool, error) {
	if s.failLookup {
		return uuid.UUID{}, false, errors.New("database failing over")
	}
	id, ok := s.responders[string(macs[0].Sum)]
	return id, ok, nil
}
func (s *bridgeStore) BridgeTargetsForResponder(_ context.Context, id uuid.UUID) ([]dispatch.BridgeTarget, error) {
	return s.byResp[id], nil
}
func (s *bridgeStore) BridgeTargetsForCaller(_ context.Context, macs []crypto.MAC) ([]dispatch.BridgeTarget, error) {
	return s.byCaller[string(macs[0].Sum)], nil
}
func (s *bridgeStore) CallerContact(context.Context, uuid.UUID) (incident.SealedContact, bool, error) {
	return incident.SealedContact{Sealed: crypto.Sealed{Ciphertext: []byte(callerNum)}}, true, nil
}
func (s *bridgeStore) ResponderPhone(context.Context, uuid.UUID) (incident.SealedContact, error) {
	return incident.SealedContact{Sealed: crypto.Sealed{Ciphertext: []byte(responderNum)}}, nil
}
func (s *bridgeStore) BeginBridgeCall(_ context.Context, _ uuid.UUID, by string) (uuid.UUID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.begun = append(s.begun, by)
	return uuid.NewV7(), nil
}
func (s *bridgeStore) BridgeCallAnswered(context.Context, uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.answered++
	return nil
}
func (s *bridgeStore) FinishBridgeCall(_ context.Context, id uuid.UUID, result string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, done := s.finished[id]; !done {
		s.finished[id] = result
	}
	return nil
}
func (s *bridgeStore) results() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, r := range s.finished {
		out = append(out, r)
	}
	return out
}

func target(ref string, role responder.Role) dispatch.BridgeTarget {
	return dispatch.BridgeTarget{IncidentID: uuid.NewV7(), Reference: incident.Reference(ref), Role: role, ResponderID: uuid.NewV7()}
}

func bridgeRig(t *testing.T, st *bridgeStore, ring time.Duration) (*Bridge, *bridgeARI, *bytes.Buffer) {
	t.Helper()
	if st.finished == nil {
		st.finished = map[uuid.UUID]string{}
	}
	a := newBridgeARI()
	var logs bytes.Buffer
	b, err := NewBridge(BridgeConfig{ARI: a, Store: st, Keys: bridgeKeys{}, Opener: plainOpener{}, Dial: "PJSIP/{number}@sauti-trunk",
		CallerID: sautiNum, Ring: ring, NoAnswerAfter: ring + 10*time.Millisecond, Log: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))})
	if err != nil {
		t.Fatal(err)
	}
	return b, a, &logs
}

func dialIn(b *Bridge, ch, from string) {
	b.Handle(context.Background(), Event{Type: EventStasisStart, ChannelID: ch, CallerNumber: from, Args: []string{"bridge"}})
}

// noNumbers fails if a phone number reached the logs, in any common form.
func noNumbers(t *testing.T, logs *bytes.Buffer) {
	t.Helper()
	for _, n := range []string{callerNum, responderNum, "0772000111", "0772000222", "772000111", "772000222"} {
		if strings.Contains(logs.String(), n) {
			t.Fatalf("a phone number reached the logs: %s", logs.String())
		}
	}
}

// Build Prompt §10.5: the responder calls the bridge number; Sauti
// recognises them and their one assignment and calls the caller, showing
// Sauti's number; the answered caller joins the responder in a mixing bridge;
// when either hangs up the other is hung up and the call is logged. The
// caller's number reaches only the trunk (the dialled endpoint) — never the
// responder's leg, a log line or a payload (hard constraint 5).
func TestBridgeResponderToCaller(t *testing.T) {
	resp := uuid.NewV7()
	tg := target("1234567894", responder.RoleVHT)
	st := &bridgeStore{responders: map[string]uuid.UUID{responderNum: resp}, byResp: map[uuid.UUID][]dispatch.BridgeTarget{resp: {tg}}}
	b, a, logs := bridgeRig(t, st, 30*time.Second)
	dialIn(b, "in-1", responderNum)
	if len(a.endpoints) != 1 || a.endpoints[0] != "PJSIP/"+callerNum+"@sauti-trunk" || a.callerIDs[0] != sautiNum ||
		a.args[0][0] != "bridge-leg" || slices.ContainsFunc(a.args[0], func(s string) bool { return strings.Contains(s, "772") }) {
		t.Fatalf("dialled %v as %v with %v", a.endpoints, a.callerIDs, a.args)
	}
	if !slices.Contains(a.lastSounds("in-1"), "sauti/en/bridge-connecting") || !slices.Contains(a.lastSounds("in-1"), ari.RingbackTone) ||
		!slices.Equal(st.begun, []string{dispatch.BridgeByVHT}) {
		t.Fatalf("connecting %v begun %v", a.plays, st.begun)
	}
	b.Handle(context.Background(), Event{Type: EventStasisStart, ChannelID: "leg-1", Args: a.args[0]})
	var bridge string
	for id, chans := range a.bridged {
		if slices.Equal(chans, []string{"in-1", "leg-1"}) {
			bridge = id
		}
	}
	if bridge == "" || st.answered != 1 {
		t.Fatalf("bridged %v answered %d", a.bridged, st.answered)
	}
	// Review I2: the VHT's own caller ID became Sauti's number before they
	// joined the bridge, so the caller is never sent it as a connected line.
	if a.callerID["in-1"] != sautiNum || slices.Index(a.ops, "callerid:in-1") > slices.Index(a.ops, "bridge:in-1") {
		t.Fatalf("caller ID %q, operations %v", a.callerID["in-1"], a.ops)
	}
	b.Handle(context.Background(), Event{Type: EventStasisEnd, ChannelID: "in-1"})
	if !a.wasHungUp("leg-1") || !slices.Contains(a.destroyed, bridge) || !slices.Equal(st.results(), []string{dispatch.BridgeConnected}) {
		t.Fatalf("after the responder hung up: hung up %v destroyed %v results %v", a.hungUp, a.destroyed, st.results())
	}
	noNumbers(t, logs)
}

// Several open assignments: the responder keys the last 4 digits of the
// incident number; a wrong entry is asked again.
func TestBridgeSeveralAssignmentsAskTheLastFourDigits(t *testing.T) {
	resp := uuid.NewV7()
	st := &bridgeStore{responders: map[string]uuid.UUID{responderNum: resp},
		byResp: map[uuid.UUID][]dispatch.BridgeTarget{resp: {target("1234567894", responder.RoleTransporter), target("9876543210", responder.RoleTransporter)}}}
	b, a, logs := bridgeRig(t, st, 30*time.Second)
	dialIn(b, "in-1", responderNum)
	if len(a.endpoints) != 0 || !slices.Contains(a.lastSounds("in-1"), "sauti/en/bridge-last-four") {
		t.Fatalf("expected the last-4 prompt: %v %v", a.endpoints, a.plays)
	}
	for _, d := range "1111" {
		b.Handle(context.Background(), Event{Type: EventDTMF, ChannelID: "in-1", Digit: string(d)})
	}
	if len(a.endpoints) != 0 || !slices.Contains(a.lastSounds("in-1"), "sauti/en/bridge-last-four-again") {
		t.Fatalf("a wrong number was not asked again: %v %v", a.endpoints, a.plays)
	}
	for _, d := range "3210" {
		b.Handle(context.Background(), Event{Type: EventDTMF, ChannelID: "in-1", Digit: string(d)})
	}
	if len(a.endpoints) != 1 || !slices.Equal(st.begun, []string{dispatch.BridgeByTransporter}) {
		t.Fatalf("after 3210: dialled %v begun %v", a.endpoints, st.begun)
	}
	noNumbers(t, logs)
}

// The caller can call back the same way: recognised by the number they
// reported from, they reach the assigned responder (who sees Sauti's
// number); with both roles assigned they choose 1 (health worker) or 2
// (transport).
func TestBridgeCallerCallsBack(t *testing.T) {
	vht, tr := target("1234567894", responder.RoleVHT), target("1234567894", responder.RoleTransporter)
	st := &bridgeStore{byCaller: map[string][]dispatch.BridgeTarget{callerNum: {vht, tr}}}
	b, a, logs := bridgeRig(t, st, 30*time.Second)
	dialIn(b, "in-1", callerNum)
	if len(a.endpoints) != 0 || !slices.Contains(a.lastSounds("in-1"), "sauti/en/bridge-choose-role") {
		t.Fatalf("expected the role choice: %v %v", a.endpoints, a.plays)
	}
	b.Handle(context.Background(), Event{Type: EventDTMF, ChannelID: "in-1", Digit: "2"})
	if len(a.endpoints) != 1 || a.endpoints[0] != "PJSIP/"+responderNum+"@sauti-trunk" || a.callerIDs[0] != sautiNum ||
		!slices.Equal(st.begun, []string{dispatch.BridgeByCaller}) {
		t.Fatalf("caller's call-back dialled %v as %v begun %v", a.endpoints, a.callerIDs, st.begun)
	}
	noNumbers(t, logs)
}

// A number with no open assignment or incident hears so and is hung up
// once the prompt has played; nothing is dialled or logged.
func TestBridgeUnknownNumber(t *testing.T) {
	b, a, logs := bridgeRig(t, &bridgeStore{}, 30*time.Second)
	dialIn(b, "in-1", "+256772000999")
	if len(a.endpoints) != 0 || !slices.Contains(a.lastSounds("in-1"), "sauti/en/bridge-no-assignment") || a.wasHungUp("in-1") {
		t.Fatalf("unknown number: %v %v hung up %v", a.endpoints, a.plays, a.hungUp)
	}
	b.Handle(context.Background(), Event{Type: EventPlaybackFinished, ChannelID: "in-1", PlaybackID: a.lastPlayback("in-1")})
	if !a.wasHungUp("in-1") {
		t.Fatal("not hung up after the prompt")
	}
	noNumbers(t, logs)
}

// The other party does not answer within the ring time: the leg is ended,
// the responder hears so, and the call is logged "no_answer". A trunk that
// refuses the call is logged "failed".
func TestBridgeNoAnswerAndFailure(t *testing.T) {
	resp := uuid.NewV7()
	st := &bridgeStore{responders: map[string]uuid.UUID{responderNum: resp},
		byResp: map[uuid.UUID][]dispatch.BridgeTarget{resp: {target("1234567894", responder.RoleVHT)}}}
	b, a, _ := bridgeRig(t, st, 10*time.Millisecond)
	dialIn(b, "in-1", responderNum)
	deadline := time.Now().Add(2 * time.Second)
	// The no-answer timer logs the result, ends the leg, then tells the
	// responder: wait for its last step.
	for !slices.Contains(a.lastSounds("in-1"), "sauti/en/bridge-no-answer") && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !slices.Equal(st.results(), []string{dispatch.BridgeNoAnswer}) || !a.wasHungUp("leg-1") ||
		!slices.Contains(a.lastSounds("in-1"), "sauti/en/bridge-no-answer") {
		t.Fatalf("no answer: results %v hung up %v plays %v", st.results(), a.hungUp, a.plays)
	}

	st2 := &bridgeStore{responders: st.responders, byResp: st.byResp}
	b2, a2, _ := bridgeRig(t, st2, 30*time.Second)
	a2.failDial = true
	dialIn(b2, "in-2", responderNum)
	// Review I1: a call that cannot be placed goes to a coordinator.
	if !slices.Equal(st2.results(), []string{dispatch.BridgeFailed}) || !slices.Equal(a2.continued, []string{"in-2→" + FallbackContext}) {
		t.Fatalf("refused trunk: results %v continued %v", st2.results(), a2.continued)
	}
}

// Review I3: the other phone is off or unreachable — its leg is destroyed
// before any answer — and the call ends at once as no answer.
func TestBridgeEndsWhenTheOtherPhoneIsUnreachable(t *testing.T) {
	resp := uuid.NewV7()
	st := &bridgeStore{responders: map[string]uuid.UUID{responderNum: resp},
		byResp: map[uuid.UUID][]dispatch.BridgeTarget{resp: {target("1234567894", responder.RoleVHT)}}}
	b, a, _ := bridgeRig(t, st, 30*time.Second)
	dialIn(b, "in-1", responderNum)
	b.Handle(context.Background(), Event{Type: EventChannelDestroyed, ChannelID: "leg-1"})
	if !slices.Equal(st.results(), []string{dispatch.BridgeNoAnswer}) || !slices.Contains(a.lastSounds("in-1"), "sauti/en/bridge-no-answer") {
		t.Fatalf("unreachable phone: results %v plays %v", st.results(), a.plays)
	}
	if len(b.calls) != 1 || len(b.byID) != 0 { // only the inbound channel, until its prompt ends
		t.Fatalf("state left behind: calls %v byID %v", b.calls, b.byID)
	}
}

// Review I1 (rule 14): a database that fails during recognition, or a
// bridge not wired in this process, hands the call to a coordinator — it
// never plays "nothing to connect" or hangs up.
func TestBridgeFailuresGoToACoordinator(t *testing.T) {
	st := &bridgeStore{failLookup: true}
	b, a, _ := bridgeRig(t, st, 30*time.Second)
	dialIn(b, "in-1", responderNum)
	if !slices.Equal(a.continued, []string{"in-1→" + FallbackContext}) || slices.Contains(a.lastSounds("in-1"), "sauti/en/bridge-no-assignment") {
		t.Fatalf("database failure: continued %v plays %v", a.continued, a.plays)
	}
	unwired := newBridgeARI()
	Outbound{ARI: unwired}.Handle(context.Background(), Event{Type: EventStasisStart, ChannelID: "in-2", Args: []string{"bridge"}})
	if !slices.Equal(unwired.continued, []string{"in-2→" + FallbackContext}) || slices.Contains(unwired.hungUp, "in-2") {
		t.Fatalf("unwired bridge: continued %v hung up %v", unwired.continued, unwired.hungUp)
	}
}

// Review I4: a responder who is also a caller (they reported an emergency
// from their own phone) is asked which party to reach, and never reaches
// themselves.
func TestBridgeResponderWhoIsAlsoACaller(t *testing.T) {
	self := uuid.NewV7()
	helping := target("1234567894", responder.RoleVHT)
	helping.ResponderID = self
	reported := target("5555555555", responder.RoleTransporter) // the transporter coming to them
	ownVHT := target("5555555555", responder.RoleVHT)           // they also hold the VHT role on their own report
	ownVHT.ResponderID = self
	st := &bridgeStore{responders: map[string]uuid.UUID{responderNum: self},
		byResp:   map[uuid.UUID][]dispatch.BridgeTarget{self: {helping, ownVHT}},
		byCaller: map[string][]dispatch.BridgeTarget{responderNum: {reported, ownVHT}}}
	b, a, logs := bridgeRig(t, st, 30*time.Second)
	dialIn(b, "in-1", responderNum)
	if len(a.endpoints) != 0 || !slices.Contains(a.lastSounds("in-1"), "sauti/en/bridge-choose-side") {
		t.Fatalf("expected the choice of party: %v %v", a.endpoints, a.plays)
	}
	b.Handle(context.Background(), Event{Type: EventDTMF, ChannelID: "in-1", Digit: "2"})
	if len(a.endpoints) != 1 || a.endpoints[0] != "PJSIP/"+responderNum+"@sauti-trunk" || !slices.Equal(st.begun, []string{dispatch.BridgeByCaller}) {
		t.Fatalf("their own helper: dialled %v begun %v", a.endpoints, st.begun)
	}
	noNumbers(t, logs)
}

// The outbound router hands each event to each call kind once: the bridge,
// registered for two kinds, must not see a key twice.
func TestOutboundRoutesBridgeEventsOnce(t *testing.T) {
	resp := uuid.NewV7()
	st := &bridgeStore{responders: map[string]uuid.UUID{responderNum: resp},
		byResp: map[uuid.UUID][]dispatch.BridgeTarget{resp: {target("1234567894", responder.RoleVHT), target("9876543210", responder.RoleVHT)}}}
	b, a, _ := bridgeRig(t, st, 30*time.Second)
	o := Outbound{ARI: a, Bridge: b}
	o.Handle(context.Background(), Event{Type: EventStasisStart, ChannelID: "in-1", CallerNumber: responderNum, Args: []string{"bridge"}})
	for _, d := range "32" {
		o.Handle(context.Background(), Event{Type: EventDTMF, ChannelID: "in-1", Digit: string(d)})
	}
	if len(a.endpoints) != 0 {
		t.Fatalf("two keys dialled already (each seen twice): %v", a.endpoints)
	}
	for _, d := range "10" {
		o.Handle(context.Background(), Event{Type: EventDTMF, ChannelID: "in-1", Digit: string(d)})
	}
	if len(a.endpoints) != 1 {
		t.Fatalf("3210 did not connect: %v", a.endpoints)
	}
}
