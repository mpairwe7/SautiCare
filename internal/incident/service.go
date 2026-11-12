package incident

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/crypto"
	"sautihealth.com/sauti/internal/pii"
)

// Store is the persistence port of the incident context. The operational
// adapter (internal/platform/pg/opstore) implements it on top of the
// database-enforced single write path.
type Store interface {
	OpenIncident(ctx context.Context, p OpenParams) (Opened, error)
	Append(ctx context.Context, p AppendParams) (Appended, error)
	Events(ctx context.Context, id uuid.UUID) ([]Event, error)
	// StoreIdentifier stores a sealed identifier once per (incident,
	// IdemKey) and returns the stored row's ID (the existing one on a retry).
	StoreIdentifier(ctx context.Context, p IdentifierParams) (uuid.UUID, error)
}

// Identifier kinds from screen answers (Build Prompt §9 "callback number",
// "free-text landmark"). They are sealed, never put in an event payload.
const (
	IdentifierCallback     = "callback"
	IdentifierLandmarkText = "landmark_text"
)

// IdentifierParams is one sealed screen-answer identifier.
type IdentifierParams struct {
	ID         uuid.UUID
	IncidentID uuid.UUID
	Kind       string
	Sealed     crypto.Sealed
	IdemKey    string
}

// PhoneSealer encrypts and hashes phone numbers (crypto.Identifiers).
type PhoneSealer interface {
	SealPhone(ctx context.Context, p pii.Phone, aad crypto.AAD) (crypto.Sealed, error)
	PhoneMAC(ctx context.Context, p pii.Phone) (crypto.MAC, error)
	SealText(ctx context.Context, plaintext []byte, aad crypto.AAD) (crypto.Sealed, error)
}

// OpenParams creates an incident, its session and its session.started event.
type OpenParams struct {
	Reference         Reference
	Channel           Channel
	SessionID         uuid.UUID
	ProviderSessionID string
	NetworkCode       string
	IdemKey           string
	Phone             crypto.Sealed // zero when the number is unknown
	PhoneMAC          crypto.MAC
}

// Opened is the result of OpenIncident. Created is false when the session
// already existed (a retried first callback) and the existing incident is
// returned unchanged.
type Opened struct {
	IncidentID uuid.UUID
	Reference  Reference
	EventID    uuid.UUID
	T1         time.Time
	Created    bool
}

// AppendParams appends one event. ExpectedVersion 0 means "any version".
type AppendParams struct {
	IncidentID      uuid.UUID
	ExpectedVersion int64
	Type            EventType
	Payload         json.RawMessage
	ActorKind       string
	StaffID         *uuid.UUID
	IdemKey         string
	LogicVersion    string
}

// Appended is the stored event. Duplicate is true when IdemKey had already
// been used, in which case the original event is returned.
type Appended struct {
	Event     Event
	NewState  State
	Duplicate bool
}

const maxReferenceAttempts = 5

// Service is the incident application service.
type Service struct {
	store  Store
	sealer PhoneSealer
	rand   io.Reader
}

// NewService wires the service. rand must be crypto/rand.Reader in production.
func NewService(s Store, ps PhoneSealer, rand io.Reader) *Service {
	return &Service{store: s, sealer: ps, rand: rand}
}

// StartSession opens an incident for a new session (Build Prompt §10.1: the
// incident and its reference exist from screen 1). The phone number is sealed
// to the session row; T1 is set by the database.
func (s *Service) StartSession(ctx context.Context, cmd StartSession) (Opened, error) {
	sessionID := uuid.NewV7()
	params := OpenParams{
		Channel:           cmd.Channel,
		SessionID:         sessionID,
		ProviderSessionID: cmd.ProviderSessionID,
		NetworkCode:       cmd.NetworkCode,
		IdemKey:           cmd.IdemKey,
	}
	if !cmd.Phone.IsZero() {
		aad := crypto.AAD{Store: "operational", Table: "sessions", Column: "phone", RowID: sessionID.String()}
		sealed, err := s.sealer.SealPhone(ctx, cmd.Phone, aad)
		if err != nil {
			return Opened{}, fmt.Errorf("incident: seal phone: %w", err)
		}
		mac, err := s.sealer.PhoneMAC(ctx, cmd.Phone)
		if err != nil {
			return Opened{}, fmt.Errorf("incident: phone mac: %w", err)
		}
		params.Phone, params.PhoneMAC = sealed, mac
	}
	var lastErr error
	for range maxReferenceAttempts {
		ref, err := NewReference(s.rand)
		if err != nil {
			return Opened{}, err
		}
		params.Reference = ref
		opened, err := s.store.OpenIncident(ctx, params)
		if errors.Is(err, ErrReferenceTaken) {
			lastErr = err
			continue
		}
		return opened, err
	}
	return Opened{}, fmt.Errorf("incident: %d reference collisions: %w", maxReferenceAttempts, lastErr)
}

// Append validates the event type and appends through the store.
func (s *Service) Append(ctx context.Context, p AppendParams) (Appended, error) {
	if !p.Type.Known() || p.Type == EvSessionStarted {
		return Appended{}, fmt.Errorf("%w: event type %q", ErrIllegalTransition, p.Type)
	}
	if p.IdemKey == "" {
		return Appended{}, errors.New("incident: append requires an idempotency key")
	}
	if len(p.Payload) == 0 {
		p.Payload = json.RawMessage("{}")
	}
	return s.store.Append(ctx, p)
}

// StoreIdentifier seals a screen-answer identifier (a callback number or a
// typed landmark) with AAD naming its own row and stores it outside every
// event payload; the screen.answered event references the returned ID. The
// call is idempotent on (incident, idemKey).
func (s *Service) StoreIdentifier(ctx context.Context, incidentID uuid.UUID, kind string, plaintext []byte, idemKey string) (uuid.UUID, error) {
	if kind != IdentifierCallback && kind != IdentifierLandmarkText {
		return uuid.UUID{}, fmt.Errorf("incident: unknown identifier kind %q", kind)
	}
	if idemKey == "" {
		return uuid.UUID{}, errors.New("incident: identifier requires an idempotency key")
	}
	id := uuid.NewV7()
	aad := crypto.AAD{Store: "operational", Table: "incident_identifiers", Column: "ct", RowID: id.String()}
	sealed, err := s.sealer.SealText(ctx, plaintext, aad)
	if err != nil {
		return uuid.UUID{}, fmt.Errorf("incident: seal identifier: %w", err)
	}
	return s.store.StoreIdentifier(ctx, IdentifierParams{ID: id, IncidentID: incidentID, Kind: kind, Sealed: sealed, IdemKey: idemKey})
}
