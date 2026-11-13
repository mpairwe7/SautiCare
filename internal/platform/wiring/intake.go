// Package wiring composes the intake service from configuration and the
// operational store. intake-gw, ivr-controller, the M0/M1 demo and the
// end-to-end tests all build intake through BuildIntake, so every one of
// them runs the same checks: the signed triage bundle, the gazetteer in
// force, the screen-length gate, the catalogue/triage domain match, and the
// production refusal of placeholder content.
package wiring

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"sautihealth.com/sauti/internal/config"
	"sautihealth.com/sauti/internal/crypto"
	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/intake"
	"sautihealth.com/sauti/internal/location"
	"sautihealth.com/sauti/internal/platform/pg/opstore"
	"sautihealth.com/sauti/internal/triage"
)

// IntakeOptions are the inputs to BuildIntake.
type IntakeOptions struct {
	Config    config.Bundle
	Store     *opstore.Store
	Keys      crypto.KeyProvider
	Rand      io.Reader
	TriageDoc []byte
	TriageSig []byte
	// Production refuses placeholder Tool 1 wording, a placeholder
	// gazetteer, and a pending or dev-signed triage bundle.
	Production bool
}

// Intake is the composed intake context.
type Intake struct {
	Service   *intake.Service
	Incidents *incident.Service
	Machine   *intake.Machine
	Triage    *triage.Engine
	Gazetteer location.File
}

// ReadTriageBundle reads a rule bundle and its detached signature
// (<file>.sig) relative to root.
func ReadTriageBundle(root, rulesFile string) (doc, sig []byte, err error) {
	p := filepath.Join(root, filepath.FromSlash(rulesFile))
	if doc, err = os.ReadFile(p); err != nil {
		return nil, nil, err
	}
	if sig, err = os.ReadFile(p + ".sig"); err != nil {
		return nil, nil, err
	}
	return doc, sig, nil
}

// BuildIntake verifies and composes the intake service.
func BuildIntake(ctx context.Context, o IntakeOptions) (Intake, error) {
	cfg := o.Config
	trusted, err := triage.DecodeTrustedKeys(cfg.Triage.TrustedKeys)
	if err != nil {
		return Intake{}, err
	}
	engine, err := triage.Load(o.TriageDoc, o.TriageSig, trusted)
	if err != nil {
		return Intake{}, err
	}
	gaz, err := o.Store.Snapshot(ctx)
	if err != nil {
		return Intake{}, err
	}
	if o.Production {
		var problems []error
		for _, check := range []error{cfg.ProductionReady(), location.ProductionReady(gaz), engine.ProductionReady()} {
			if check != nil {
				problems = append(problems, check)
			}
		}
		if len(problems) > 0 {
			return Intake{}, fmt.Errorf("wiring: refusing production start: %w", errors.Join(problems...))
		}
	}
	machine, err := intake.NewMachine(cfg.Tool1, gaz, cfg.Intake.DispatchTriggerScreen)
	if err != nil {
		return Intake{}, err
	}
	limits := map[string]int{"default": cfg.Intake.DefaultScreenCharLimit}
	for network, l := range cfg.Intake.ScreenCharLimits {
		limits[network] = l
	}
	if err := machine.CheckScreens(limits); err != nil {
		return Intake{}, err
	}
	if err := intake.CheckTriageDomain(cfg.Tool1, engine.Values); err != nil {
		return Intake{}, err
	}
	ids := crypto.Identifiers{Env: crypto.NewEnvelope(o.Keys, o.Rand)}
	incidents := incident.NewService(o.Store, ids, o.Rand)
	svc, err := intake.New(intake.Deps{
		Incidents: incidents, Reader: o.Store, Steps: o.Store, Keys: ids, Machine: machine, Triage: engine,
		Gazetteer: gaz, Matcher: o.Store, ResumeWindow: cfg.Intake.ResumeWindow.Duration,
		MinSimilarity: cfg.Intake.FuzzyMatchMinSimilarity,
	})
	if err != nil {
		return Intake{}, err
	}
	return Intake{Service: svc, Incidents: incidents, Machine: machine, Triage: engine, Gazetteer: gaz}, nil
}

// Describe summarises what was built, for the start-up log line (no
// identifiers).
func (in Intake) Describe() string {
	return strings.Join([]string{"triage=" + in.Triage.LogicVersion(), "gazetteer=" + in.Gazetteer.Version}, " ")
}
