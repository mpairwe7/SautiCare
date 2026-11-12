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
	idents     []IdentifierParams
}

func (f *fakeStore) StoreIdentifier(_ context.Context, p IdentifierParams) (uuid.UUID, error) {
	f.idents = append(f.idents, p)
	return p.ID, nil
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

func (s *fakeSealer) SealText(_ context.Context, b []byte, aad crypto.AAD) (crypto.Sealed, error) {
	s.aads = append(s.aads, aad)
	return crypto.Sealed{KeyID: "k", Ciphertext: append([]byte("ct:"), b...)}, nil
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

// A typed landmark or callback number is sealed with AAD naming its own row,
// stored outside every event payload, and referenced by that row's ID.
func TestStoreIdentifierSealsWithRowAAD(t *testing.T) {
	st, se := &fakeStore{}, &fakeSealer{}
	svc := NewService(st, se, rand.Reader)
	inc := uuid.NewV7()
	id, err := svc.StoreIdentifier(context.Background(), inc, IdentifierCallback, []byte("+256772123456"), "ussd:at:s1:s8")
	if err != nil {
		t.Fatal(err)
	}
	if len(st.idents) != 1 || len(se.aads) != 1 {
		t.Fatalf("stored %d, sealed %d", len(st.idents), len(se.aads))
	}
	p, aad := st.idents[0], se.aads[0]
	want := crypto.AAD{Store: "operational", Table: "incident_identifiers", Column: "ct", RowID: id.String()}
	if aad != want || p.ID != id || p.IncidentID != inc || p.Kind != IdentifierCallback || p.IdemKey != "ussd:at:s1:s8" {
		t.Fatalf("aad=%+v params=%+v", aad, p)
	}
	if _, err := svc.StoreIdentifier(context.Background(), inc, "address", []byte("x"), "k"); err == nil {
		t.Fatal("unknown identifier kind accepted")
	}
	if _, err := svc.StoreIdentifier(context.Background(), inc, IdentifierLandmarkText, []byte("x"), ""); err == nil {
		t.Fatal("identifier without an idempotency key accepted")
	}
}
