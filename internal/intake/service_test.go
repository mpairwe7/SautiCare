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
	mu sync.Mutex
	m  map[string]Screen
}

func key(p, s string, h [32]byte) string { return p + "|" + s + "|" + string(h[:]) }

func (m *memSteps) Lookup(_ context.Context, p, s string, h [32]byte) (Screen, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sc, ok := m.m[key(p, s, h)]
	return sc, ok, nil
}

func (m *memSteps) Save(_ context.Context, p, s string, h [32]byte, sc Screen, _ uuid.UUID) (Screen, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.m == nil {
		m.m = map[string]Screen{}
	}
	if old, ok := m.m[key(p, s, h)]; ok {
		return old, nil
	}
	m.m[key(p, s, h)] = sc
	return sc, nil
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
	svc := NewService(st, &memSteps{}, repoScreens(t), "en")
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
	svc := NewService(st, steps, repoScreens(t), "en")
	first, _ := svc.HandleUSSD(context.Background(), request(""))
	again, err := svc.HandleUSSD(context.Background(), request(""))
	if err != nil || again != first {
		t.Fatalf("replay = %+v (%v), want %+v", again, err, first)
	}
	if len(st.calls) != 1 {
		t.Fatalf("StartSession called %d times, want 1", len(st.calls))
	}
	h := sha256.Sum256([]byte(""))
	if _, ok, _ := steps.Lookup(context.Background(), "at", "ATUid_1", h); !ok {
		t.Fatal("step not stored under sha256(text)")
	}
}

func TestUCCRouteMapsToItsChannel(t *testing.T) {
	ref, _ := incident.NewReference(rand.Reader)
	st := &fakeStarter{ref: ref}
	r := request("")
	r.Provider = "ucc"
	if _, err := NewService(st, &memSteps{}, repoScreens(t), "en").HandleUSSD(context.Background(), r); err != nil {
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
	svc := NewService(&fakeStarter{ref: ref}, &memSteps{}, repoScreens(t), "lg")
	sc, err := svc.HandleUSSD(context.Background(), request(""))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sc.Text, ref.Spoken()) {
		t.Fatalf("screen %q lacks the reference %s", sc.Text, ref.Spoken())
	}
}
