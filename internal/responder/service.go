package responder

import (
	"context"
	"errors"
	"fmt"
	"time"
	"uuid"

	"sautihealth.com/sauti/internal/crypto"
	"sautihealth.com/sauti/internal/pii"
)

// Gates are the registry facts a coordinator maintains (Build Prompt
// §10.11).
type Gates struct {
	CertifiedAt  *time.Time
	InsuredUntil *time.Time
	KitOK        bool
	Status       string
}

// RegisterParams is a sealed registry entry.
type RegisterParams struct {
	ID              uuid.UUID
	Role            Role
	Name, Phone     crypto.Sealed
	PhoneMAC        crypto.MAC
	Ward, Stage     string
	Gates           Gates
	ResearchConsent bool
}

// Store is the registry's persistence port (the operational store).
type Store interface {
	RegisterResponder(ctx context.Context, p RegisterParams) error
}

// Sealer seals identifiers (crypto.Identifiers).
type Sealer interface {
	SealPhone(ctx context.Context, p pii.Phone, aad crypto.AAD) (crypto.Sealed, error)
	PhoneMAC(ctx context.Context, p pii.Phone) (crypto.MAC, error)
	SealText(ctx context.Context, plaintext []byte, aad crypto.AAD) (crypto.Sealed, error)
}

// Service registers responders.
type Service struct {
	store  Store
	sealer Sealer
}

// NewService wires the service.
func NewService(s Store, sealer Sealer) *Service { return &Service{store: s, sealer: sealer} }

// RegisterCmd registers one responder (console; M5 screen, sautictl before).
type RegisterCmd struct {
	Role            Role
	Name            pii.PersonName
	Phone           pii.Phone
	Ward, Stage     string
	CertifiedAt     *time.Time
	InsuredUntil    *time.Time
	KitOK           bool
	ResearchConsent bool
}

// Registered is the new entry.
type Registered struct{ ID uuid.UUID }

// Register seals the name and number to the new row (AAD names the row and
// column) and stores the entry with a keyed phone hash for recognising the
// responder's own calls and USSD sessions.
func (s *Service) Register(ctx context.Context, c RegisterCmd) (Registered, error) {
	if _, err := ParseRole(string(c.Role)); err != nil {
		return Registered{}, err
	}
	if c.Phone.IsZero() || c.Name.Reveal() == "" || c.Ward == "" || c.Stage == "" {
		return Registered{}, errors.New("responder: name, phone, ward and stage are required")
	}
	id := uuid.NewV7()
	aad := func(col string) crypto.AAD {
		return crypto.AAD{Store: "operational", Table: "responders", Column: col, RowID: id.String()}
	}
	name, err := s.sealer.SealText(ctx, []byte(c.Name.Reveal()), aad("name"))
	if err != nil {
		return Registered{}, fmt.Errorf("responder: seal name: %w", err)
	}
	phone, err := s.sealer.SealPhone(ctx, c.Phone, aad("phone"))
	if err != nil {
		return Registered{}, fmt.Errorf("responder: seal phone: %w", err)
	}
	mac, err := s.sealer.PhoneMAC(ctx, c.Phone)
	if err != nil {
		return Registered{}, fmt.Errorf("responder: phone mac: %w", err)
	}
	err = s.store.RegisterResponder(ctx, RegisterParams{ID: id, Role: c.Role, Name: name, Phone: phone, PhoneMAC: mac,
		Ward: c.Ward, Stage: c.Stage, ResearchConsent: c.ResearchConsent,
		Gates: Gates{CertifiedAt: c.CertifiedAt, InsuredUntil: c.InsuredUntil, KitOK: c.KitOK, Status: StatusActive}})
	if err != nil {
		return Registered{}, err
	}
	return Registered{ID: id}, nil
}
