package intake

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
	"uuid"

	"sautihealth.com/sauti/internal/config"
	"sautihealth.com/sauti/internal/crypto"
	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/pii"
)

type fakeStarter struct {
	calls []incident.StartSession
	ref   incident.Reference
}

func (f *fakeStarter) StartSession(_ context.Context, c incident.StartSession) (incident.Opened, error) {
	f.calls = append(f.calls, c)
	return incident.Opened{IncidentID: uuid.NewV7(), Reference: f.ref, T1: time.Now(), Created: len(f.calls) == 1}, nil
}

type memSteps struct {
	mu    sync.Mutex
	m     map[string]Step
	order map[string][]Step
}

func key(p, s string, mac crypto.MAC) string { return p + "|" + s + "|" + string(mac.Sum) }

func (m *memSteps) Lookup(_ context.Context, p, s string, macs []crypto.MAC) (Step, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, mac := range macs {
		if st, ok := m.m[key(p, s, mac)]; ok {
			return st, true, nil
		}
	}
	return Step{}, false, nil
}

func (m *memSteps) Latest(_ context.Context, p, s string) (Step, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var best Step
	found := false
	for _, st := range m.order[p+"|"+s] {
		if !found || st.ConsumedLen > best.ConsumedLen {
			best, found = st, true
		}
	}
	return best, found, nil
}

func (m *memSteps) Save(_ context.Context, p, s string, st Step) (Screen, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.m == nil {
		m.m, m.order = map[string]Step{}, map[string][]Step{}
	}
	if old, ok := m.m[key(p, s, st.MAC)]; ok {
		return old.Screen, nil
	}
	m.m[key(p, s, st.MAC)] = st
	m.order[p+"|"+s] = append(m.order[p+"|"+s], st)
	return st.Screen, nil
}

// fakeKeys is a keyed hash stand-in: deterministic, but never sha256(msg).
type fakeKeys struct{}

func (fakeKeys) StepMAC(_ context.Context, msg []byte) (crypto.MAC, error) {
	sum := sha256.Sum256(append([]byte("test-key|"), msg...))
	return crypto.MAC{KeyID: "k1", Sum: sum[:]}, nil
}

func (k fakeKeys) StepLookupMACs(ctx context.Context, msg []byte) ([]crypto.MAC, error) {
	m, err := k.StepMAC(ctx, msg)
	return []crypto.MAC{m}, err
}

func repoScreens(t *testing.T) config.Screens {
	t.Helper()
	b, err := config.Load(os.DirFS("../../config"))
	if err != nil {
		t.Fatal(err)
	}
	return b.Screens
}

func request(text string) USSDRequest {
	p, _ := pii.ParsePhone("0772123456")
	return USSDRequest{Provider: "at", SessionID: "ATUid_1", ServiceCode: "*384*1#", NetworkCode: "64110", Text: text, Phone: p}
}

func TestHandleUSSDStartsSessionOnEmptyText(t *testing.T) {
	ref, _ := incident.NewReference(rand.Reader)
	st := &fakeStarter{ref: ref}
	svc := NewService(st, &memSteps{}, fakeKeys{}, repoScreens(t), "en")
	sc, err := svc.HandleUSSD(context.Background(), request(""))
	if err != nil {
		t.Fatal(err)
	}
	if !sc.End || !strings.Contains(sc.Text, ref.Spoken()) {
		t.Fatalf("screen = %+v, want END containing %q", sc, ref.Spoken())
	}
	if len(st.calls) != 1 {
		t.Fatalf("StartSession calls = %d", len(st.calls))
	}
	c := st.calls[0]
	if c.Channel != incident.ChannelUSSDAT || c.ProviderSessionID != "ATUid_1" || c.Phone.E164() != "+256772123456" || c.IdemKey == "" {
		t.Fatalf("StartSession command = %+v", c)
	}
}

// A retried callback (same session, same input) must return the stored
// screen without touching the incident again (Build Prompt §7 idempotency).
func TestHandleUSSDReplaysStoredScreen(t *testing.T) {
	ref, _ := incident.NewReference(rand.Reader)
	st := &fakeStarter{ref: ref}
	steps := &memSteps{}
	svc := NewService(st, steps, fakeKeys{}, repoScreens(t), "en")
	first, _ := svc.HandleUSSD(context.Background(), request(""))
	again, err := svc.HandleUSSD(context.Background(), request(""))
	if err != nil || again != first {
		t.Fatalf("replay = %+v (%v), want %+v", again, err, first)
	}
	if len(st.calls) != 1 {
		t.Fatalf("StartSession called %d times, want 1", len(st.calls))
	}
	mac, _ := fakeKeys{}.StepMAC(context.Background(), StepMessage("at", "ATUid_1", ""))
	if _, ok, _ := steps.Lookup(context.Background(), "at", "ATUid_1", []crypto.MAC{mac}); !ok {
		t.Fatal("step not stored under its keyed MAC")
	}
	plain := sha256.Sum256([]byte(""))
	if _, ok, _ := steps.Lookup(context.Background(), "at", "ATUid_1", []crypto.MAC{{KeyID: "k1", Sum: plain[:]}}); ok {
		t.Fatal("step stored under the plain sha256 of its input")
	}
}

// Step messages are length-prefixed, so no choice of provider, session and
// text can collide with another.
func TestStepMessageIsUnambiguous(t *testing.T) {
	if string(StepMessage("at", "s1*2", "3")) == string(StepMessage("at", "s1", "2*3")) ||
		string(StepMessage("a", "t", "")) == string(StepMessage("at", "", "")) {
		t.Fatal("step messages collide")
	}
}

func TestUCCRouteMapsToItsChannel(t *testing.T) {
	ref, _ := incident.NewReference(rand.Reader)
	st := &fakeStarter{ref: ref}
	r := request("")
	r.Provider = "ucc"
	if _, err := NewService(st, &memSteps{}, fakeKeys{}, repoScreens(t), "en").HandleUSSD(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if st.calls[0].Channel != incident.ChannelUSSDUCC {
		t.Fatalf("channel = %s", st.calls[0].Channel)
	}
}

// The build fails if any screen exceeds the operator character limit in
// either language (Build Prompt §8, §16).
func TestScreensFitCharLimit(t *testing.T) {
	b, err := config.Load(os.DirFS("../../config"))
	if err != nil {
		t.Fatal(err)
	}
	ref, _ := incident.NewReference(rand.Reader)
	for _, e := range b.Screens.All() {
		rendered := Render(e[2], ref)
		if n := utf8.RuneCountInString(rendered); n > b.Intake.DefaultScreenCharLimit {
			t.Errorf("screen %s/%s is %d characters, limit %d", e[0], e[1], n, b.Intake.DefaultScreenCharLimit)
		}
	}
}

// CodeRabbit PR #2: a language whose template lacks {ref} (the Luganda
// placeholder) must fall back to English so the caller still gets the
// incident reference.
func TestHandleUSSDAlwaysCarriesTheReference(t *testing.T) {
	ref, _ := incident.NewReference(rand.Reader)
	svc := NewService(&fakeStarter{ref: ref}, &memSteps{}, fakeKeys{}, repoScreens(t), "lg")
	sc, err := svc.HandleUSSD(context.Background(), request(""))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sc.Text, ref.Spoken()) {
		t.Fatalf("screen %q lacks the reference %s", sc.Text, ref.Spoken())
	}
}
