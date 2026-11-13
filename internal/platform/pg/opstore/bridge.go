package opstore

import (
	"context"
	"fmt"
	"uuid"

	"github.com/jackc/pgx/v5/pgtype"

	"sautihealth.com/sauti/internal/crypto"
	"sautihealth.com/sauti/internal/dispatch"
	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/platform/pg/opstore/opdb"
	"sautihealth.com/sauti/internal/responder"
)

// The masked bridge (op_ivr; Build Prompt §10.5, ADR-0028): who a caller of
// the bridge number may be connected to, and the log of bridge calls.

// BridgeTargetsForResponder lists the open assignments a responder holds.
func (s *Store) BridgeTargetsForResponder(ctx context.Context, responderID uuid.UUID) ([]dispatch.BridgeTarget, error) {
	rows, err := s.q.BridgeTargetsForResponder(ctx, pgID(responderID))
	if err != nil {
		return nil, fmt.Errorf("opstore: bridge targets: %w", err)
	}
	out := make([]dispatch.BridgeTarget, 0, len(rows))
	for _, r := range rows {
		out = append(out, bridgeTarget(r.IncidentID, r.Reference, r.Role, r.ResponderID))
	}
	return out, nil
}

// BridgeTargetsForCaller lists the responders assigned to the open incidents
// reported from a number (its lookup MACs).
func (s *Store) BridgeTargetsForCaller(ctx context.Context, macs []crypto.MAC) ([]dispatch.BridgeTarget, error) {
	sums := make([][]byte, len(macs))
	for i, m := range macs {
		sums[i] = m.Sum
	}
	rows, err := s.q.BridgeTargetsForCaller(ctx, sums)
	if err != nil {
		return nil, fmt.Errorf("opstore: caller bridge targets: %w", err)
	}
	out := make([]dispatch.BridgeTarget, 0, len(rows))
	for _, r := range rows {
		out = append(out, bridgeTarget(r.IncidentID, r.Reference, r.Role, r.ResponderID))
	}
	return out, nil
}

func bridgeTarget(inc pgtype.UUID, ref, role string, resp pgtype.UUID) dispatch.BridgeTarget {
	return dispatch.BridgeTarget{IncidentID: uuid.UUID(inc.Bytes), Reference: incident.Reference(ref), Role: responder.Role(role),
		ResponderID: uuid.UUID(resp.Bytes)}
}

// BeginBridgeCall logs a bridge call's start (an open assignment is needed).
func (s *Store) BeginBridgeCall(ctx context.Context, incidentID uuid.UUID, initiatedBy string) (uuid.UUID, error) {
	id, err := s.q.BeginBridgeCall(ctx, opdb.BeginBridgeCallParams{IncidentID: pgID(incidentID), InitiatedBy: initiatedBy})
	if err != nil {
		return uuid.UUID{}, mapErr(err)
	}
	return uuid.UUID(id.Bytes), nil
}

// BridgeCallAnswered logs the answer of the bridge call's other leg.
func (s *Store) BridgeCallAnswered(ctx context.Context, id uuid.UUID) error {
	return mapErr(s.q.BridgeCallAnswered(ctx, pgID(id)))
}

// FinishBridgeCall logs a bridge call's end and result, once.
func (s *Store) FinishBridgeCall(ctx context.Context, id uuid.UUID, result string) error {
	_, err := s.q.FinishBridgeCall(ctx, opdb.FinishBridgeCallParams{ID: pgID(id), Result: result})
	return mapErr(err)
}

// DispatchSurge reports the surge flag (op_readonly, op_console, op_dispatch).
func (s *Store) DispatchSurge(ctx context.Context) (dispatch.Surge, error) {
	r, err := s.q.DispatchSurge(ctx)
	if err != nil {
		return dispatch.Surge{}, fmt.Errorf("opstore: surge: %w", err)
	}
	return dispatch.Surge{Waiting: int(r.Waiting), Available: int(r.Available), Surge: r.Surge}, nil
}
