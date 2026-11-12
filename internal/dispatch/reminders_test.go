package dispatch

import (
	"context"
	"errors"
	"testing"
	"time"
	"uuid"
)

type claimReminders struct {
	got    []Reminder
	within time.Duration
}

func (c *claimReminders) ClaimAvailabilityReminders(_ context.Context, within time.Duration, _ int) ([]Reminder, error) {
	c.within = within
	return c.got, nil
}

type remindSender struct {
	sent []uuid.UUID
	fail uuid.UUID
}

func (s *remindSender) Remind(_ context.Context, r Reminder) (string, error) {
	s.sent = append(s.sent, r.ResponderID)
	if r.ResponderID == s.fail {
		return "", errors.New("provider down")
	}
	return "ATXid_r", nil
}

// Review focus 4: every claimed reminder is sent (a failure does not stop
// the rest; the claim means it is never sent twice).
func TestRemindersSendEveryClaimedPeriod(t *testing.T) {
	a, b, c := uuid.NewV7(), uuid.NewV7(), uuid.NewV7()
	store := &claimReminders{got: []Reminder{{ResponderID: a}, {ResponderID: b}, {ResponderID: c}}}
	sender := &remindSender{fail: b}
	var failed int
	n, err := Reminders{Store: store, Sender: sender}.RunOnce(context.Background(), func(error) { failed++ })
	if err != nil || n != 3 || len(sender.sent) != 3 || failed != 1 || store.within != ReminderBefore {
		t.Fatalf("n=%d err=%v sent=%v failed=%d within=%v", n, err, sender.sent, failed, store.within)
	}
}
