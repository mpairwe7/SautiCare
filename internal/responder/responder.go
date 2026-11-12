// Package responder holds the responder registry's domain rules (Build
// Prompt §9 Responders, §10.4, §10.11): who may be alerted. A responder is a
// community health worker (VHT member) or a transporter (boda-boda rider). To
// be eligible they must pass three gates (certified, insured, kit OK), be
// active, and have an open availability period (decision 5: 8 hours).
// Names and phone numbers are identifiers: sealed in the store, never in
// events or logs.
package responder

import (
	"errors"
	"fmt"
	"time"
	"uuid"
)

// Role is a responder's role; each role is searched on its own.
type Role string

const (
	RoleVHT         Role = "vht"
	RoleTransporter Role = "transporter"
)

// Roles lists the roles in search order.
func Roles() []Role { return []Role{RoleVHT, RoleTransporter} }

// ParseRole validates a role.
func ParseRole(s string) (Role, error) {
	switch Role(s) {
	case RoleVHT, RoleTransporter:
		return Role(s), nil
	}
	return "", fmt.Errorf("responder: unknown role %q", s)
}

// Statuses of a registry entry.
const (
	StatusActive    = "active"
	StatusSuspended = "suspended"
	StatusRetired   = "retired"
)

// AvailabilityTTL is how long "available" lasts (decision 5).
const AvailabilityTTL = 8 * time.Hour

// Period is a half-open availability period [From, Until).
type Period struct{ From, Until time.Time }

// Contains reports whether t is inside the period.
func (p Period) Contains(t time.Time) bool { return !t.Before(p.From) && t.Before(p.Until) }

// Responder is a registry entry as dispatch sees it (no identifiers).
type Responder struct {
	ID              uuid.UUID
	Role            Role
	Ward            string
	Stage           string // gazetteer stage ID: where the responder waits
	CertifiedAt     *time.Time
	InsuredUntil    *time.Time
	KitOK           bool
	ResearchConsent bool
	Status          string
	Available       *Period // the latest availability period, if any
}

// ErrGate reports a responder who may not be alerted.
var ErrGate = errors.New("responder: not eligible")

// Gates checks the three gates and the status at now.
func (r Responder) Gates(now time.Time) error {
	switch {
	case r.Status != StatusActive:
		return fmt.Errorf("%w: status %s", ErrGate, r.Status)
	case r.CertifiedAt == nil || r.CertifiedAt.After(now):
		return fmt.Errorf("%w: not certified", ErrGate)
	case r.InsuredUntil == nil || !now.Before(*r.InsuredUntil):
		return fmt.Errorf("%w: not insured", ErrGate)
	case !r.KitOK:
		return fmt.Errorf("%w: kit not ready", ErrGate)
	}
	return nil
}

// AvailableAt reports whether an availability period covers t.
func (r Responder) AvailableAt(t time.Time) bool {
	return r.Available != nil && r.Available.Contains(t)
}
