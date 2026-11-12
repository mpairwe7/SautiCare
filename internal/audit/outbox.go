package audit

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"
	"uuid"
)

// ErrPoisonRow reports an outbox row that does not match the audit payload
// contract. The relay stops on it rather than skipping it or copying unknown
// fields into the ledger (runbook: docs/runbooks/outbox-poison-row.md).
var ErrPoisonRow = errors.New("audit: poison outbox row")

// TopicAudit is the outbox topic relayed into the ledger.
const TopicAudit = "audit"

// outboxPayload is exactly what op.enqueue_audit writes. Decoding is strict.
type outboxPayload struct {
	EntryID       string  `json:"entry_id"`
	EventID       string  `json:"event_id"`
	OccurredAt    string  `json:"occurred_at"`
	EventType     string  `json:"event_type"`
	IncidentID    *string `json:"incident_id"`
	StaffID       *string `json:"staff_id"`
	LogicVersion  *string `json:"logic_version"`
	PayloadSHA256 string  `json:"payload_sha256"`
}

func optUUID(s *string) (*uuid.UUID, error) {
	if s == nil {
		return nil, nil
	}
	u, err := uuid.Parse(*s)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// ParseOutboxPayload decodes an audit outbox row into an append request.
func ParseOutboxPayload(r OutboxRow) (AppendRequest, error) {
	poison := func(why string, args ...any) (AppendRequest, error) {
		return AppendRequest{}, fmt.Errorf("%w: row %s: %s", ErrPoisonRow, r.ID, fmt.Sprintf(why, args...))
	}
	dec := json.NewDecoder(bytes.NewReader(r.Payload))
	dec.DisallowUnknownFields()
	var p outboxPayload
	if err := dec.Decode(&p); err != nil {
		return poison("decode: %v", err)
	}
	entryID, err := uuid.Parse(p.EntryID)
	if err != nil || entryID != r.ID {
		return poison("entry_id does not match the outbox row")
	}
	at, err := time.Parse(time.RFC3339Nano, p.OccurredAt)
	if err != nil {
		return poison("occurred_at: %v", err)
	}
	if p.EventType == "" {
		return poison("missing event_type")
	}
	sum, err := hex.DecodeString(p.PayloadSHA256)
	if err != nil || len(sum) != 32 {
		return poison("payload_sha256 must be 32 hex bytes")
	}
	inc, err := optUUID(p.IncidentID)
	if err != nil {
		return poison("incident_id: %v", err)
	}
	staff, err := optUUID(p.StaffID)
	if err != nil {
		return poison("staff_id: %v", err)
	}
	req := AppendRequest{EntryID: entryID, OccurredAt: at.UTC(), EventType: p.EventType, IncidentID: inc, StaffID: staff}
	if p.LogicVersion != nil {
		req.LogicVersion = *p.LogicVersion
	}
	copy(req.PayloadSHA256[:], sum)
	return req, nil
}

// RelayMetrics receives relay measurements. nil means "do not record".
type RelayMetrics interface {
	Relayed(n int)
	Lag(d time.Duration)
	Poison()
}

// Relay moves audit outbox rows into the ledger, at least once and in order.
type Relay struct {
	outbox    Outbox
	ledger    Ledger
	log       *slog.Logger
	metrics   RelayMetrics
	batchSize int
	now       func() time.Time
}

// NewRelay returns a relay. metrics may be nil.
func NewRelay(o Outbox, l Ledger, log *slog.Logger, m RelayMetrics, batchSize int) *Relay {
	if batchSize <= 0 {
		batchSize = 100
	}
	return &Relay{outbox: o, ledger: l, log: log, metrics: m, batchSize: batchSize, now: time.Now}
}

// RunOnce relays one batch and returns how many rows it delivered. On any
// error the batch is aborted: rows stay pending and are redelivered later,
// which the ledger absorbs idempotently.
func (r *Relay) RunOnce(ctx context.Context) (int, error) {
	batch, err := r.outbox.Claim(ctx, TopicAudit, r.batchSize)
	if err != nil {
		return 0, fmt.Errorf("relay: claim: %w", err)
	}
	rows := batch.Rows()
	if len(rows) == 0 {
		if r.metrics != nil {
			r.metrics.Lag(0)
		}
		return 0, batch.Complete(ctx, nil)
	}
	delivered := make([]uuid.UUID, 0, len(rows))
	for i, row := range rows {
		req, err := ParseOutboxPayload(row)
		if err != nil {
			batch.Abort(ctx)
			if r.metrics != nil {
				r.metrics.Poison()
			}
			r.log.Error("audit relay stopped on poison row", "outbox_id", row.ID.String(), "err", err)
			return 0, err
		}
		if i == 0 && r.metrics != nil {
			r.metrics.Lag(r.now().Sub(req.OccurredAt))
		}
		if _, err := r.ledger.Append(ctx, req); err != nil {
			batch.Abort(ctx)
			return 0, fmt.Errorf("relay: ledger append: %w", err)
		}
		delivered = append(delivered, row.ID)
	}
	if err := batch.Complete(ctx, delivered); err != nil {
		return 0, fmt.Errorf("relay: complete: %w", err)
	}
	if r.metrics != nil {
		r.metrics.Relayed(len(delivered))
	}
	return len(delivered), nil
}

// Run relays until ctx ends: immediately again after a full batch, after
// poll otherwise, backing off up to 30 s while the ledger is unavailable.
func (r *Relay) Run(ctx context.Context, poll time.Duration) error {
	backoff := poll
	for {
		n, err := r.RunOnce(ctx)
		wait := poll
		switch {
		case errors.Is(err, ErrPoisonRow):
			return err // needs a human: see the poison-row runbook
		case err != nil:
			r.log.Warn("audit relay retrying", "err", err, "backoff", backoff.String())
			wait = backoff
			backoff = min(backoff*2, 30*time.Second)
		case n == r.batchSize:
			wait = 0
			backoff = poll
		default:
			backoff = poll
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}
