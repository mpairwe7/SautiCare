package main

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"sautihealth.com/sauti/internal/audit"
	"sautihealth.com/sauti/internal/config"
	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/location"
	"sautihealth.com/sauti/internal/platform/devkeys"
	"sautihealth.com/sauti/internal/platform/pg/auditstore"
	"sautihealth.com/sauti/internal/platform/pg/opstore"
)

// boardSize is how many of the latest incidents the board shows.
const boardSize = 25

type board struct {
	Now       time.Time       `json:"now"`
	Suspended bool            `json:"suspended"`
	Incidents []boardIncident `json:"incidents"`
}

type boardIncident struct {
	ID          string            `json:"id"`
	Reference   string            `json:"reference"`
	Severity    string            `json:"severity"`
	State       string            `json:"state"`
	Category    string            `json:"category"`
	Ward        string            `json:"ward"`
	Landmark    string            `json:"landmark"`
	Location    string            `json:"location"`
	Escalated   bool              `json:"escalated"`
	Flags       []string          `json:"flags"`
	T           map[string]string `json:"t"`              // "T1".."T7" → RFC 3339
	Wake        *time.Time        `json:"wake,omitempty"` // the dispatch timer
	Events      []boardEvent      `json:"events"`
	Escalations []boardEscalation `json:"escalations"`
	Alerts      []boardAlert      `json:"alerts"`
	Notices     []boardAttempt    `json:"notices"`
	MergedInto  string            `json:"mergedInto,omitempty"`
	byAlert     map[string]int    // alert ID → index in Alerts
}

type boardEvent struct {
	Seq     int64     `json:"seq"`
	Type    string    `json:"type"`
	At      time.Time `json:"at"`
	Actor   string    `json:"actor"`
	Payload string    `json:"payload"`
}

type boardEscalation struct {
	At     time.Time `json:"at"`
	Reason string    `json:"reason"`
	Role   string    `json:"role,omitempty"`
}

type boardAlert struct {
	Responder   string         `json:"responder"`
	Role        string         `json:"role"`
	Ring        int            `json:"ring"`
	Wave        int            `json:"wave"`
	SentAt      time.Time      `json:"sentAt"`
	Outcome     string         `json:"outcome"`
	Reason      string         `json:"reason,omitempty"`
	RespondedAt *time.Time     `json:"respondedAt,omitempty"`
	Attempts    []boardAttempt `json:"attempts"`
}

type boardAttempt struct {
	Kind     string `json:"kind"` // an alert attempt's purpose, a caller notice's kind
	Channel  string `json:"channel"`
	Status   string `json:"status"`
	Delivery string `json:"delivery,omitempty"`
}

// numberLike matches a phone number written any way Sauti accepts one
// (+256…, 256…, 0…, or any +E.164, with or without spaces). Payloads are
// coded and never hold a number (internal/incident), but the board masks
// one again before display (rule 5: never reveal the caller's number).
var numberLike = regexp.MustCompile(`\+[0-9][0-9 ]{6,16}[0-9]|\b0[0-9]{9}\b|\b256[0-9]{9}\b`)

func maskNumbers(s string) string { return numberLike.ReplaceAllString(s, "[number]") }

// readBoard reads the latest incidents as op_readonly, which cannot read a
// caller's number or a responder's name or number at all.
func (st *stack) readBoard(ctx context.Context, cfg config.Bundle, gaz location.File) (board, error) {
	ro := opstore.New(st.ro)
	b := board{Now: time.Now().UTC(), Incidents: []boardIncident{}}
	if err := st.ro.QueryRow(ctx, "SELECT op.dispatch_suspended()").Scan(&b.Suspended); err != nil {
		return b, err
	}
	ids, err := ro.ListIncidentIDs(ctx) // uuidv7: oldest first
	if err != nil {
		return b, err
	}
	if len(ids) > boardSize {
		ids = ids[len(ids)-boardSize:]
	}
	slices.Reverse(ids)
	byID := map[string]*boardIncident{}
	keys := make([]string, 0, len(ids))
	for _, id := range ids {
		p, err := ro.Projection(ctx, id)
		if err != nil {
			return b, err
		}
		events, err := ro.Events(ctx, id)
		if err != nil {
			return b, err
		}
		b.Incidents = append(b.Incidents, project(p, events, cfg, gaz))
		keys = append(keys, id.String())
	}
	for i := range b.Incidents {
		byID[b.Incidents[i].ID] = &b.Incidents[i]
	}
	if err := st.readDispatch(ctx, keys, byID); err != nil {
		return b, err
	}
	return b, nil
}

func project(p incident.Projection, events []incident.Event, cfg config.Bundle, gaz location.File) boardIncident {
	in := boardIncident{ID: p.ID.String(), Reference: p.Reference.Spoken(), Severity: string(p.Severity), State: string(p.State),
		Category: label(cfg.Tool1.Category.Options, p.Category), Ward: p.Ward, Landmark: p.Landmark,
		Location: p.LocationQuality, Escalated: p.Escalated, Flags: p.Flags, T: map[string]string{},
		Events: []boardEvent{}, Escalations: []boardEscalation{}, Alerts: []boardAlert{}, Notices: []boardAttempt{},
		byAlert: map[string]int{}}
	for _, w := range gaz.Wards {
		if w.ID == p.Ward {
			in.Ward = w.Name.EN
		}
	}
	for _, l := range gaz.Landmarks {
		if l.ID == p.Landmark {
			in.Landmark = l.Name.EN
		}
	}
	if p.MergedInto != nil {
		in.MergedInto = p.MergedInto.String()
	}
	for i, t := range p.T {
		if t != nil && i > 0 {
			in.T["T"+string(rune('0'+i))] = t.UTC().Format(time.RFC3339Nano)
		}
	}
	for _, e := range events {
		in.Events = append(in.Events, boardEvent{Seq: e.Seq, Type: string(e.Type), At: e.RecordedAt.UTC(), Actor: e.ActorKind,
			Payload: maskNumbers(string(e.Payload))})
		if e.Type == incident.EvEscalated {
			var x struct{ Reason, Role string }
			_ = json.Unmarshal(e.Payload, &x)
			in.Escalations = append(in.Escalations, boardEscalation{At: e.RecordedAt.UTC(), Reason: x.Reason, Role: x.Role})
		}
	}
	return in
}

func label(opts []config.Option, value string) string {
	for _, o := range opts {
		if o.Value == value {
			return o.Label["en"]
		}
	}
	return value
}

// readDispatch adds each incident's alerts with their delivery attempts, its
// caller notices and its dispatch timer (tables op_readonly may read).
func (st *stack) readDispatch(ctx context.Context, ids []string, byID map[string]*boardIncident) error {
	var id, inc, resp string
	var a boardAlert
	rows, err := st.ro.Query(ctx, `SELECT id::text, incident_id::text, responder_id::text, role, ring, wave, sent_at, outcome,
	        coalesce(decline_reason, ''), responded_at
	   FROM op.alerts WHERE incident_id = ANY($1::uuid[]) ORDER BY sent_at, id`, ids)
	if err != nil {
		return err
	}
	if _, err := pgx.ForEachRow(rows, []any{&id, &inc, &resp, &a.Role, &a.Ring, &a.Wave, &a.SentAt, &a.Outcome, &a.Reason, &a.RespondedAt}, func() error {
		if in := byID[inc]; in != nil {
			a.Responder, a.Attempts = st.labels[resp], []boardAttempt{}
			in.byAlert[id] = len(in.Alerts)
			in.Alerts = append(in.Alerts, a)
		}
		return nil
	}); err != nil {
		return err
	}
	var t boardAttempt
	rows, err = st.ro.Query(ctx, `SELECT t.alert_id::text, a.incident_id::text, t.purpose, t.channel, t.status, coalesce(t.delivery_status, '')
	   FROM op.alert_attempts t JOIN op.alerts a ON a.id = t.alert_id
	  WHERE a.incident_id = ANY($1::uuid[]) ORDER BY t.claimed_at`, ids)
	if err != nil {
		return err
	}
	if _, err := pgx.ForEachRow(rows, []any{&id, &inc, &t.Kind, &t.Channel, &t.Status, &t.Delivery}, func() error {
		if in := byID[inc]; in != nil {
			if i, ok := in.byAlert[id]; ok {
				in.Alerts[i].Attempts = append(in.Alerts[i].Attempts, t)
			}
		}
		return nil
	}); err != nil {
		return err
	}
	rows, err = st.ro.Query(ctx, `SELECT incident_id::text, kind, channel, status, coalesce(delivery_status, '')
	   FROM op.notice_attempts WHERE incident_id = ANY($1::uuid[]) ORDER BY claimed_at`, ids)
	if err != nil {
		return err
	}
	if _, err := pgx.ForEachRow(rows, []any{&inc, &t.Kind, &t.Channel, &t.Status, &t.Delivery}, func() error {
		if in := byID[inc]; in != nil {
			in.Notices = append(in.Notices, t)
		}
		return nil
	}); err != nil {
		return err
	}
	// A merged or parked incident's timer is 'infinity': it has no wake-up to show.
	var due time.Time
	rows, err = st.ro.Query(ctx, `SELECT incident_id::text, due_at FROM op.dispatch_timers WHERE incident_id = ANY($1::uuid[]) AND isfinite(due_at)`, ids)
	if err != nil {
		return err
	}
	_, err = pgx.ForEachRow(rows, []any{&inc, &due}, func() error {
		if in := byID[inc]; in != nil {
			d := due
			in.Wake = &d
		}
		return nil
	})
	return err
}

type auditResult struct {
	OK             bool   `json:"ok"`
	Entries        int64  `json:"entries"`
	HeadSeq        int64  `json:"headSeq"`
	CheckpointSeq  int64  `json:"checkpointSeq"` // signed by this verification; -1 when the ledger is empty
	CheckpointsOK  int    `json:"checkpointsOk"`
	CheckpointsBad int    `json:"checkpointsBad"`
	WitnessesOK    int    `json:"witnessesOk"`
	WitnessesBad   int    `json:"witnessesBad"`
	FirstBadSeq    int64  `json:"firstBadSeq"`
	Problem        string `json:"problem,omitempty"`
	Events         int64  `json:"events"`
	Pending        int64  `json:"pending"` // outbox rows not yet relayed
	Projections    int    `json:"projections"`
	Drift          int    `json:"drift"`
}

// verifyLedger signs a checkpoint of the ledger head, as `sautictl
// checkpoint` does (anchored on the witness, so a rewritten history is
// refused, never signed), then runs `sautictl verify-ledger` with the
// witness and `verify-projections`: the same functions, in process.
func (st *stack) verifyLedger(ctx context.Context) (auditResult, error) {
	st.audit.Lock()
	defer st.audit.Unlock()
	res := auditResult{CheckpointSeq: -1}
	witness := devkeys.NewFSWitness(st.witnessDir)
	reader, ledger := auditstore.New(st.auditRead), auditstore.New(st.auditWrite)
	seq, _, err := ledger.Head(ctx)
	if err != nil {
		return res, err
	}
	if seq > 0 {
		cper, _, err := audit.NewAnchoredCheckpointer(ledger, st.keys.Signer(), witness, origin, time.Now)
		if err == nil {
			var cp audit.Checkpoint
			if cp, err = cper.Checkpoint(ctx); err == nil {
				res.CheckpointSeq = cp.Seq
			}
		}
		if err != nil && !errors.Is(err, audit.ErrChainDiverged) {
			return res, err
		}
		if err != nil {
			res.Problem = "the ledger no longer holds the witnessed history; no checkpoint signed"
		}
	}
	notes, err := witness.ReadAll()
	if err != nil {
		return res, err
	}
	var cps []audit.Checkpoint
	for _, n := range notes {
		cp, err := audit.ParseWitnessNote(n)
		if err != nil {
			return res, err
		}
		cps = append(cps, cp)
	}
	rep, err := audit.Verify(ctx, reader, st.keys.Signer().PublicKey(), cps)
	if err != nil {
		return res, err
	}
	res.Entries, res.HeadSeq, res.FirstBadSeq = rep.Entries, rep.HeadSeq, rep.FirstBadSeq
	res.CheckpointsOK, res.CheckpointsBad, res.WitnessesOK, res.WitnessesBad = rep.CheckpointsOK, rep.CheckpointsBad, rep.WitnessesOK, rep.WitnessesBad
	if rep.Problem != "" {
		res.Problem = rep.Problem
	}
	checked, drift, err := opstore.VerifyProjections(ctx, opstore.New(st.ro))
	if err != nil {
		return res, err
	}
	res.Projections, res.Drift = checked, len(drift)
	if err := st.ro.QueryRow(ctx, `SELECT (SELECT count(*) FROM op.incident_events), (SELECT count(*) FROM op.outbox WHERE delivered_at IS NULL)`).
		Scan(&res.Events, &res.Pending); err != nil {
		return res, err
	}
	res.OK = rep.OK() && res.Problem == "" && len(drift) == 0
	return res, nil
}
