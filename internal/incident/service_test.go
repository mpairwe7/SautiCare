package incident

import (
	"context"
	"crypto/rand"
	"errors"
	"testing"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/crypto"
	"sautihealth.com/sauti/internal/pii"
)

type fakeStore struct {
	takenFirst int
	opens      []OpenParams
	appends    []AppendParams
}

func (f *fakeStore) OpenIncident(_ context.Context, p OpenParams) (Opened, error) {
	f.opens = append(f.opens, p)
	if len(f.opens) <= f.takenFirst {
		return Opened{}, ErrReferenceTaken
	}
	return Opened{IncidentID: uuid.NewV7(), Reference: p.Reference, EventID: uuid.NewV7(), T1: time.Now(), Created: true}, nil
}

func (f *fakeStore) Append(_ context.Context, p AppendParams) (Appended, error) {
	f.appends = append(f.appends, p)
	return Appended{NewState: StateIntake}, nil
}

func (f *fakeStore) Events(context.Context, uuid.UUID) ([]Event, error) { return nil, nil }

type fakeSealer struct{ aads []crypto.AAD }

func (s *fakeSealer) SealPhone(_ context.Context, _ pii.Phone, aad crypto.AAD) (crypto.Sealed, error) {
	s.aads = append(s.aads, aad)
	return crypto.Sealed{KeyID: "k", Ciphertext: []byte{1}}, nil
}

func (s *fakeSealer) PhoneMAC(context.Context, pii.Phone) (crypto.MAC, error) {
	return crypto.MAC{KeyID: "k", Sum: make([]byte, 32)}, nil
}

func startCmd(t *testing.T) StartSession {
	t.Helper()
	p, err := pii.ParsePhone("0772123456")
	if err != nil {
		t.Fatal(err)
	}
	return StartSession{Channel: ChannelUSSDAT, ProviderSessionID: "ATUid_1", ServiceCode: "*384*1#", Phone: p, IdemKey: "ussd:at:ATUid_1:e3b0"}
}

func TestStartSessionRetriesReferenceCollision(t *testing.T) {
	st := &fakeStore{takenFirst: 2}
	svc := NewService(st, &fakeSealer{}, rand.Reader)
	got, err := svc.StartSession(context.Background(), startCmd(t))
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if len(st.opens) != 3 {
		t.Fatalf("OpenIncident called %d times, want 3", len(st.opens))
	}
	if st.opens[0].Reference == st.opens[2].Reference {
		t.Fatal("retry reused the colliding reference")
	}
	if got.Reference != st.opens[2].Reference || !DammValid(string(got.Reference)) {
		t.Fatalf("returned reference %q", got.Reference)
	}
}

func TestStartSessionGivesUpAfterFiveCollisions(t *testing.T) {
	st := &fakeStore{takenFirst: 100}
	svc := NewService(st, &fakeSealer{}, rand.Reader)
	if _, err := svc.StartSession(context.Background(), startCmd(t)); !errors.Is(err, ErrReferenceTaken) {
		t.Fatalf("err = %v, want ErrReferenceTaken", err)
	}
	if len(st.opens) != 5 {
		t.Fatalf("attempts = %d, want 5", len(st.opens))
	}
}

// The phone ciphertext is bound to the session row that stores it.
func TestStartSessionSealsPhoneWithSessionAAD(t *testing.T) {
	st := &fakeStore{}
	sealer := &fakeSealer{}
	svc := NewService(st, sealer, rand.Reader)
	if _, err := svc.StartSession(context.Background(), startCmd(t)); err != nil {
		t.Fatal(err)
	}
	if len(sealer.aads) != 1 {
		t.Fatalf("SealPhone calls = %d", len(sealer.aads))
	}
	want := crypto.AAD{Store: "operational", Table: "sessions", Column: "phone", RowID: st.opens[0].SessionID.String()}
	if sealer.aads[0] != want {
		t.Fatalf("AAD = %+v, want %+v", sealer.aads[0], want)
	}
}

// A withheld number (IVR) is unknown, not an error: dispatch proceeds on location.
func TestStartSessionAcceptsUnknownPhone(t *testing.T) {
	st := &fakeStore{}
	sealer := &fakeSealer{}
	cmd := startCmd(t)
	cmd.Phone = pii.Phone{}
	if _, err := NewService(st, sealer, rand.Reader).StartSession(context.Background(), cmd); err != nil {
		t.Fatal(err)
	}
	if len(sealer.aads) != 0 || st.opens[0].Phone.Ciphertext != nil {
		t.Fatal("sealed an unknown phone")
	}
}

func TestAppendRejectsUnknownEventType(t *testing.T) {
	st := &fakeStore{}
	svc := NewService(st, &fakeSealer{}, rand.Reader)
	_, err := svc.Append(context.Background(), AppendParams{IncidentID: uuid.NewV7(), Type: "made.up", IdemKey: "k"})
	if !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("err = %v", err)
	}
	if len(st.appends) != 0 {
		t.Fatal("unknown type reached the store")
	}
}
