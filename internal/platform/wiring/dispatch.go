package wiring

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"sautihealth.com/sauti/internal/config"
	"sautihealth.com/sauti/internal/dispatch"
	"sautihealth.com/sauti/internal/location"
)

var logicVersion = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}\+sha256:[0-9a-f]{12,64}$`)

// DispatchLogicVersion is the version stamped on every dispatch fact: the
// ranking version (decision 4) and the fingerprint of the gated
// configuration in force, e.g. "default-2026-11-12+sha256:0123456789ab".
func DispatchLogicVersion(b config.Bundle) (string, error) {
	hexDigits, ok := strings.CutPrefix(b.Fingerprint, "sha256:")
	if !ok || len(hexDigits) < 12 {
		return "", fmt.Errorf("wiring: configuration fingerprint %q is not sha256", b.Fingerprint)
	}
	v := b.Dispatch.RankingVersion + "+sha256:" + hexDigits[:12]
	if !logicVersion.MatchString(v) {
		return "", fmt.Errorf("wiring: dispatch logic version %q is not an approved identifier", v)
	}
	return v, nil
}

// DispatchParams converts the gated dispatch configuration into the
// planner's parameters; the maximum radius must be a whole number of ring
// steps (2.5 km / 500 m = ring 5).
func DispatchParams(d config.Dispatch) (dispatch.Params, error) {
	if d.RingStepMetres <= 0 || d.MaxRadiusMetres < d.RingStepMetres || d.MaxRadiusMetres%d.RingStepMetres != 0 {
		return dispatch.Params{}, errors.New("wiring: max_radius_metres must be a whole number of ring_step_metres")
	}
	p := dispatch.Params{Mode: d.Mode, RingInterval: d.RingInterval.Duration, MaxRing: d.MaxRadiusMetres / d.RingStepMetres,
		CascadeTimeout: d.CascadeTimeout.Duration, RedParallel: d.RedParallelCount, ForwardAckTimeout: d.ForwardAckTimeout.Duration,
		RedEscalationAfter: d.RedEscalationAfter.Duration}
	return p, p.Validate()
}

// DispatchStore is what dispatch-worker needs from the operational store
// (role op_dispatch): the dispatch port and the gazetteer in force.
type DispatchStore interface {
	dispatch.Store
	Snapshot(ctx context.Context) (location.File, error)
}

// DispatchOptions wires dispatch-worker's engine.
type DispatchOptions struct {
	Config     config.Bundle
	Store      DispatchStore
	Deliverer  dispatch.Deliverer
	Notifier   dispatch.Notifier
	Suspension dispatch.Suspension
}

// Dispatch is the wired engine and what it runs under.
type Dispatch struct {
	Engine       *dispatch.Engine
	Gazetteer    location.File
	Params       dispatch.Params
	LogicVersion string
}

// BuildDispatch validates the gated dispatch parameters and ranking order,
// reads the gazetteer in force (stages, wards, facilities) and builds the
// engine. Every refusal stops the worker before it alerts anyone.
func BuildDispatch(ctx context.Context, o DispatchOptions) (Dispatch, error) {
	params, err := DispatchParams(o.Config.Dispatch)
	if err != nil {
		return Dispatch{}, err
	}
	ranker, err := dispatch.NewRanker(o.Config.Dispatch.RankingOrder, o.Config.Dispatch.RankingVersion)
	if err != nil {
		return Dispatch{}, err
	}
	logic, err := DispatchLogicVersion(o.Config)
	if err != nil {
		return Dispatch{}, err
	}
	gaz, err := o.Store.Snapshot(ctx)
	if err != nil {
		return Dispatch{}, err
	}
	rule := dispatch.StoppingRule{Window: o.Config.Dispatch.StoppingRule.Window, MaxFailureRate: o.Config.Dispatch.StoppingRule.MaxFailureRate}
	engine, err := dispatch.NewEngine(dispatch.EngineConfig{Store: o.Store, Deliverer: o.Deliverer, Notifier: o.Notifier, Suspension: o.Suspension,
		Params: params, Ranker: ranker, LogicVersion: logic, Gazetteer: gaz, StoppingRule: rule})
	if err != nil {
		return Dispatch{}, err
	}
	return Dispatch{Engine: engine, Gazetteer: gaz, Params: params, LogicVersion: logic}, nil
}
