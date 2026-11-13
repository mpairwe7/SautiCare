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
	"testing/synctest"
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
	stopped   []string // playbacks stopped (a prompt cut by a key)
	failPlay  string   // a sound whose playback Asterisk refuses
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
	if a.failPlay != "" && slices.Contains(sounds, a.failPlay) {
		return errors.New("ari: playback refused")
	}
	a.plays[ch] = append(a.plays[ch], sounds)
	a.pbs[ch] = append(a.pbs[ch], pb)
	return nil
}
func (a *bridgeARI) StopPlayback(_ context.Context, pb string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stopped = append(a.stopped, pb)
	return nil
}
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
func (a *bridgeARI) DestroyBridge(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err // as an HTTP request on an expired context
	}
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

// heard counts the times sound was played on ch.
func (a *bridgeARI) heard(ch, sound string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for _, p := range a.plays[ch] {
		if slices.Contains(p, sound) {
			n++
		}
	}
	return n
}

func (a *bridgeARI) handedOver() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.continued)
}

func (a *bridgeARI) dialled() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.endpoints)
}

func (a *bridgeARI) stops() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.stopped)
}

const (
	callerNum      = "+256772000111" // the person who reported the emergency
	responderNum   = "+256772000222" // the responder
	transporterNum = "+256772000333" // another responder
	secondVHTNum   = "+256772000444" // a VHT on the caller's other incident
	sautiNum       = "+256800100100" // Sauti's bridge number (placeholder)
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
	slowFinish bool                 // the end of a call is logged only when its time runs out
	responders map[string]uuid.UUID // MAC sum → responder
	byResp     map[uuid.UUID][]dispatch.BridgeTarget
	byCaller   map[string][]dispatch.BridgeTarget // MAC sum → targets
	phones     map[uuid.UUID]string               // responder → number (default responderNum)
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
func (s *bridgeStore) ResponderPhone(_ context.Context, id uuid.UUID) (incident.SealedContact, error) {
	n, ok := s.phones[id]
	if !ok {
		n = responderNum
	}
	return incident.SealedContact{Sealed: crypto.Sealed{Ciphertext: []byte(n)}}, nil
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
func (s *bridgeStore) FinishBridgeCall(ctx context.Context, id uuid.UUID, result string) error {
	if s.slowFinish {
		<-ctx.Done()
		return ctx.Err()
	}
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

func press(b *Bridge, ch, keys string) {
	for _, k := range keys {
		b.Handle(context.Background(), Event{Type: EventDTMF, ChannelID: ch, Digit: string(k)})
	}
}

// noNumbers fails if a phone number reached the logs, in any common form.
func noNumbers(t *testing.T, logs *bytes.Buffer) {
	t.Helper()
	for _, n := range []string{callerNum, responderNum, transporterNum, secondVHTNum, "0772000111", "0772000222", "0772000333", "0772000444",
		"772000111", "772000222", "772000333", "772000444"} {
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
		a.args[0][0] != "bridge-leg" || slices.ContainsFunc(a.args[0], func(s string) bool { return strings.Contains(s, "772000") }) {
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
		byCaller: map[string][]dispatch.BridgeTarget{responderNum: {reported, ownVHT}},
		phones:   map[uuid.UUID]string{self: responderNum, reported.ResponderID: transporterNum}}
	b, a, logs := bridgeRig(t, st, 30*time.Second)
	dialIn(b, "in-1", responderNum)
	if len(a.endpoints) != 0 || !slices.Contains(a.lastSounds("in-1"), "sauti/en/bridge-choose-side") {
		t.Fatalf("expected the choice of party: %v %v", a.endpoints, a.plays)
	}
	b.Handle(context.Background(), Event{Type: EventDTMF, ChannelID: "in-1", Digit: "2"})
	// CodeRabbit PR #12: each responder has their own number, so dialling
	// the transporter is told apart from dialling the caller themselves.
	if len(a.endpoints) != 1 || a.endpoints[0] != "PJSIP/"+transporterNum+"@sauti-trunk" || strings.Contains(a.endpoints[0], responderNum) ||
		!slices.Equal(st.begun, []string{dispatch.BridgeByCaller}) {
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

// twoAssignments is a responder holding two open assignments: the bridge
// asks for the last 4 digits (3210 picks the second).
func twoAssignments() *bridgeStore {
	resp := uuid.NewV7()
	return &bridgeStore{responders: map[string]uuid.UUID{responderNum: resp},
		byResp: map[uuid.UUID][]dispatch.BridgeTarget{resp: {target("1234567894", responder.RoleTransporter), target("9876543210", responder.RoleTransporter)}}}
}

// CodeRabbit PR #12 (the ledger's deferred minor): a choice nobody answers
// cannot hold the call open. With no key — or only part of the last 4
// digits — for the no-input time (15 s from the end of the prompt, as on the
// toll-free line) the choice is asked again, and after three silent or wrong
// tries a coordinator takes the call (rule 14). A lost end-of-prompt event
// does not stop this: the wait armed when a prompt starts (the longest
// prompt, 30 s, plus the no-input time) still ends. A key the choice does not
// offer is not input and leaves the prompt playing. The end of the choice or
// of the call stops the time, and a closing prompt whose end never comes is
// still followed by the hang-up.
func TestBridgeChoiceWithNoKeyGoesToACoordinator(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const lastFour = "sauti/en/bridge-last-four"
		// Silence, and no end-of-prompt event ever arrives.
		b, a, _ := bridgeRig(t, twoAssignments(), 30*time.Second)
		if b.c.NoInput != 15*time.Second || b.c.PromptMax != 30*time.Second {
			t.Fatalf("defaults: no input %v, longest prompt %v", b.c.NoInput, b.c.PromptMax)
		}
		dialIn(b, "in-1", responderNum)
		// Each wait is 45 s from its prompt's start: asked again at 45 s and
		// 90 s, to a coordinator at 135 s.
		for _, step := range []struct {
			sleep time.Duration
			heard int
		}{{44 * time.Second, 1}, {2 * time.Second, 2}, {43 * time.Second, 2}, {2 * time.Second, 3}} {
			time.Sleep(step.sleep)
			synctest.Wait()
			if a.heard("in-1", lastFour) != step.heard {
				t.Fatalf("at %v: prompt heard %d times, want %d", time.Since(time.Unix(946684800, 0)), a.heard("in-1", lastFour), step.heard)
			}
		}
		time.Sleep(45 * time.Second)
		synctest.Wait()
		if !slices.Equal(a.handedOver(), []string{"in-1→" + FallbackContext}) || len(a.dialled()) != 0 {
			t.Fatalf("three silent tries: handed over %v dialled %v", a.handedOver(), a.dialled())
		}
		time.Sleep(10 * time.Minute)
		synctest.Wait()
		if a.heard("in-1", lastFour) != 3 || len(a.handedOver()) != 1 {
			t.Fatalf("the time ran on after the hand-over: plays %v handed over %v", a.plays, a.handedOver())
		}

		// Part of the digits, then silence: counted from the last key, the
		// choice is asked again with the digits cleared.
		b2, a2, _ := bridgeRig(t, twoAssignments(), 30*time.Second)
		dialIn(b2, "in-2", responderNum)
		press(b2, "in-2", "*#")
		if len(a2.stops()) != 0 {
			t.Fatalf("a key the choice does not offer cut the prompt: %v", a2.stops())
		}
		time.Sleep(10 * time.Second)
		b2.Handle(context.Background(), Event{Type: EventPlaybackFinished, ChannelID: "in-2", PlaybackID: a2.lastPlayback("in-2")})
		time.Sleep(10 * time.Second)
		press(b2, "in-2", "32")
		time.Sleep(14 * time.Second)
		synctest.Wait()
		if a2.heard("in-2", lastFour) != 1 {
			t.Fatalf("asked again 14 s after a key: %v", a2.plays)
		}
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if a2.heard("in-2", lastFour) != 2 {
			t.Fatalf("part of the digits, then silence: not asked again: %v", a2.plays)
		}
		press(b2, "in-2", "3210") // with "32" kept this would read 3232
		if len(a2.dialled()) != 1 {
			t.Fatalf("3210 after a silent try did not connect: %v", a2.plays)
		}
		time.Sleep(10 * time.Minute)
		synctest.Wait()
		if len(a2.handedOver()) != 0 || a2.heard("in-2", lastFour) != 2 {
			t.Fatalf("the time ran on after the choice: handed over %v plays %v", a2.handedOver(), a2.plays)
		}

		// The prompt's end arrives: the 15 s run from it, not from its start.
		b5, a5, _ := bridgeRig(t, twoAssignments(), 30*time.Second)
		dialIn(b5, "in-5", responderNum)
		time.Sleep(5 * time.Second)
		b5.Handle(context.Background(), Event{Type: EventPlaybackFinished, ChannelID: "in-5", PlaybackID: a5.lastPlayback("in-5")})
		time.Sleep(14 * time.Second)
		synctest.Wait()
		if a5.heard("in-5", lastFour) != 1 {
			t.Fatalf("asked again 14 s after the prompt ended: %v", a5.plays)
		}
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if a5.heard("in-5", lastFour) != 2 {
			t.Fatalf("not asked again 16 s after the prompt ended: %v", a5.plays)
		}

		// The caller hangs up mid-choice: the time stops with the call.
		b3, a3, _ := bridgeRig(t, twoAssignments(), 30*time.Second)
		dialIn(b3, "in-3", responderNum)
		b3.Handle(context.Background(), Event{Type: EventStasisEnd, ChannelID: "in-3"})
		time.Sleep(10 * time.Minute)
		synctest.Wait()
		if len(a3.handedOver()) != 0 || a3.heard("in-3", lastFour) != 1 {
			t.Fatalf("a call that ended was still served: handed over %v plays %v", a3.handedOver(), a3.plays)
		}

		// A closing prompt whose end never arrives: hung up all the same.
		b4, a4, _ := bridgeRig(t, &bridgeStore{}, 30*time.Second)
		dialIn(b4, "in-4", "+256772000999")
		time.Sleep(46 * time.Second)
		synctest.Wait()
		if !a4.wasHungUp("in-4") {
			t.Fatal("a closing prompt with no end event held the call open")
		}
	})
}

// CodeRabbit PR #12: a choice whose prompt Asterisk cannot play leaves the
// caller with no instructions, so a coordinator takes the call at once
// (rule 14) — not after the tries a wrong key would have.
func TestBridgePromptThatCannotPlayGoesToACoordinator(t *testing.T) {
	self := uuid.NewV7()
	helping := target("1234567894", responder.RoleVHT)
	helping.ResponderID = self
	for _, c := range []struct {
		name, sound, from, keys string
		store                   *bridgeStore
	}{
		{"last four", "sauti/en/bridge-last-four", responderNum, "", twoAssignments()},
		{"again", "sauti/en/bridge-last-four-again", responderNum, "1111", twoAssignments()},
		{"role", "sauti/en/bridge-choose-role", callerNum, "", &bridgeStore{byCaller: map[string][]dispatch.BridgeTarget{
			callerNum: {target("1234567894", responder.RoleVHT), target("1234567894", responder.RoleTransporter)}}}},
		{"side", "sauti/en/bridge-choose-side", responderNum, "", &bridgeStore{responders: map[string]uuid.UUID{responderNum: self},
			byResp:   map[uuid.UUID][]dispatch.BridgeTarget{self: {helping}},
			byCaller: map[string][]dispatch.BridgeTarget{responderNum: {target("5555555555", responder.RoleTransporter)}}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			b, a, _ := bridgeRig(t, c.store, 30*time.Second)
			a.failPlay = c.sound
			dialIn(b, "in-1", c.from)
			press(b, "in-1", c.keys)
			if !slices.Equal(a.handedOver(), []string{"in-1→" + FallbackContext}) || len(a.dialled()) != 0 {
				t.Fatalf("%s not played: handed over %v dialled %v", c.sound, a.handedOver(), a.dialled())
			}
		})
	}
}

// CodeRabbit PR #12: a caller with two open incidents can have two helpers
// of one role. The role then picks no single helper, so the caller keys the
// last 4 digits of the incident number, and the call is still the caller's
// (initiated_by "caller"). A role held by one helper connects at once; with
// one role among several helpers the role is not asked at all.
func TestBridgeCallerWithTwoHelpersOfARoleKeysTheIncident(t *testing.T) {
	vhtA, vhtB, trA := target("1234567894", responder.RoleVHT), target("9876543210", responder.RoleVHT), target("1234567894", responder.RoleTransporter)
	phones := map[uuid.UUID]string{vhtA.ResponderID: responderNum, vhtB.ResponderID: secondVHTNum, trA.ResponderID: transporterNum}

	st := &bridgeStore{byCaller: map[string][]dispatch.BridgeTarget{callerNum: {vhtA, vhtB, trA}}, phones: phones}
	b, a, logs := bridgeRig(t, st, 30*time.Second)
	dialIn(b, "in-1", callerNum)
	press(b, "in-1", "1")
	if len(a.dialled()) != 0 || !slices.Contains(a.lastSounds("in-1"), "sauti/en/bridge-last-four") {
		t.Fatalf("two health workers: expected the last-4 prompt: dialled %v plays %v", a.dialled(), a.plays)
	}
	press(b, "in-1", "3210")
	if !slices.Equal(a.dialled(), []string{"PJSIP/" + secondVHTNum + "@sauti-trunk"}) || !slices.Equal(st.begun, []string{dispatch.BridgeByCaller}) {
		t.Fatalf("3210: dialled %v begun %v", a.dialled(), st.begun)
	}

	st2 := &bridgeStore{byCaller: map[string][]dispatch.BridgeTarget{callerNum: {vhtA, vhtB, trA}}, phones: phones}
	b2, a2, _ := bridgeRig(t, st2, 30*time.Second)
	dialIn(b2, "in-2", callerNum)
	press(b2, "in-2", "2")
	if !slices.Equal(a2.dialled(), []string{"PJSIP/" + transporterNum + "@sauti-trunk"}) {
		t.Fatalf("one transporter: dialled %v", a2.dialled())
	}

	st3 := &bridgeStore{byCaller: map[string][]dispatch.BridgeTarget{callerNum: {vhtA, vhtB}}, phones: phones}
	b3, a3, _ := bridgeRig(t, st3, 30*time.Second)
	dialIn(b3, "in-3", callerNum)
	if a3.heard("in-3", "sauti/en/bridge-choose-role") != 0 || !slices.Contains(a3.lastSounds("in-3"), "sauti/en/bridge-last-four") {
		t.Fatalf("health workers only: expected the last-4 prompt, not the role: %v", a3.plays)
	}
	press(b3, "in-3", "7894")
	if !slices.Equal(a3.dialled(), []string{"PJSIP/" + responderNum + "@sauti-trunk"}) || !slices.Equal(st3.begun, []string{dispatch.BridgeByCaller}) {
		t.Fatalf("7894: dialled %v begun %v", a3.dialled(), st3.begun)
	}
	noNumbers(t, logs)
}

// CodeRabbit PR #12: a log write that uses up its time must not leave the
// mixing bridge behind; destroying it has a time of its own.
func TestBridgeIsDestroyedAfterASlowLogWrite(t *testing.T) {
	resp := uuid.NewV7()
	st := &bridgeStore{responders: map[string]uuid.UUID{responderNum: resp},
		byResp: map[uuid.UUID][]dispatch.BridgeTarget{resp: {target("1234567894", responder.RoleVHT)}}}
	b, a, logs := bridgeRig(t, st, 30*time.Second)
	b.c.OpTimeout = 20 * time.Millisecond
	dialIn(b, "in-1", responderNum)
	b.Handle(context.Background(), Event{Type: EventStasisStart, ChannelID: "leg-1", Args: a.args[0]})
	st.slowFinish = true
	b.Handle(context.Background(), Event{Type: EventStasisEnd, ChannelID: "in-1"})
	if len(a.bridged) != 1 {
		t.Fatalf("bridges %v", a.bridged)
	}
	for id := range a.bridged {
		if !slices.Contains(a.destroyed, id) {
			t.Fatalf("the bridge outlived a slow log write: destroyed %v", a.destroyed)
		}
	}
	if !strings.Contains(logs.String(), "bridge call end not logged") || !a.wasHungUp("leg-1") {
		t.Fatalf("logs %s hung up %v", logs.String(), a.hungUp)
	}
	noNumbers(t, logs)
}

// A bridge choice whose recording failed to play leaves the caller without
// instructions: a coordinator takes the call at once (F-03).
func TestAFailedBridgePromptGoesToACoordinator(t *testing.T) {
	b, a, logs := bridgeRig(t, twoAssignments(), 30*time.Second)
	dialIn(b, "in-1", responderNum)
	b.Handle(context.Background(), Event{Type: EventPlaybackFinished, ChannelID: "in-1", PlaybackID: a.lastPlayback("in-1"), PlaybackState: ari.PlaybackFailed})
	if !slices.Equal(a.handedOver(), []string{"in-1→" + FallbackContext}) || !strings.Contains(logs.String(), "bridge prompt not played; to a coordinator") {
		t.Fatalf("handed over %v, logs %s", a.handedOver(), logs.String())
	}
}
