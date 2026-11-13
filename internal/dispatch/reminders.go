package dispatch

import (
	"context"
	"time"
)

// ReminderBefore is how long before an availability period ends its
// responder is reminded (decision 5: 8-hour periods; review focus 4).
const ReminderBefore = 30 * time.Minute

// ReminderStore claims due reminders, each period once (op_dispatch).
type ReminderStore interface {
	ClaimAvailabilityReminders(ctx context.Context, within time.Duration, limit int) ([]Reminder, error)
}

// ReminderSender sends one reminder (the SMS channel).
type ReminderSender interface {
	Remind(ctx context.Context, r Reminder) (string, error)
}

// Reminders sends the availability-expiry reminders. A claim comes before
// the send, so a reminder is never sent twice (a crash in between loses it:
// it is a convenience, not a dispatch step).
type Reminders struct {
	Store  ReminderStore
	Sender ReminderSender
}

// RunOnce claims and sends the due reminders; a failed send is reported and
// the rest still go out.
func (r Reminders) RunOnce(ctx context.Context, onErr func(error)) (int, error) {
	due, err := r.Store.ClaimAvailabilityReminders(ctx, ReminderBefore, 100)
	if err != nil {
		return 0, err
	}
	for _, d := range due {
		if _, err := r.Sender.Remind(ctx, d); err != nil && onErr != nil {
			onErr(err)
		}
	}
	return len(due), nil
}

// Run sends reminders every interval until ctx ends (under leadership).
func (r Reminders) Run(ctx context.Context, every time.Duration, onErr func(error)) error {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if _, err := r.RunOnce(ctx, onErr); err != nil && ctx.Err() == nil && onErr != nil {
			onErr(err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}
