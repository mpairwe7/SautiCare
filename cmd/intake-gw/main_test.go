package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"sautihealth.com/sauti/internal/intake"
	"sautihealth.com/sauti/internal/platform/africastalking"
	"sautihealth.com/sauti/internal/platform/metrics"
	"sautihealth.com/sauti/internal/platform/pg/opstore"
)

type recorderFunc func(context.Context, opstore.DeliveryReport) error

func (f recorderFunc) RecordDeliveryReport(ctx context.Context, r opstore.DeliveryReport) error {
	return f(ctx, r)
}

// The provider's delivery report reaches the store with its codes only; a
// report for a message Sauti never sent is acknowledged as unknown.
func TestDeliveryReportBridge(t *testing.T) {
	var got opstore.DeliveryReport
	b := deliveryReports{recorderFunc(func(_ context.Context, r opstore.DeliveryReport) error { got = r; return nil })}
	if err := b.RecordDeliveryReport(context.Background(), africastalking.DeliveryReport{Provider: "at", MessageID: "ATXid_1",
		Status: "Failed", FailureReason: "AbsentSubscriber", Final: true}); err != nil {
		t.Fatal(err)
	}
	if got != (opstore.DeliveryReport{ProviderRef: "ATXid_1", Status: "Failed", FailureReason: "AbsentSubscriber", Final: true}) {
		t.Fatalf("stored %+v", got)
	}
	unknown := deliveryReports{recorderFunc(func(context.Context, opstore.DeliveryReport) error { return opstore.ErrUnknownDelivery })}
	if err := unknown.RecordDeliveryReport(context.Background(), africastalking.DeliveryReport{MessageID: "x", Status: "Success"}); !errors.Is(err, africastalking.ErrUnknownMessage) {
		t.Fatalf("unknown: %v", err)
	}
}

type portFunc func(context.Context, intake.USSDRequest) (intake.Screen, error)

func (f portFunc) HandleUSSD(ctx context.Context, r intake.USSDRequest) (intake.Screen, error) {
	return f(ctx, r)
}

// Callbacks on the responder service code reach the responder menu; every
// other accepted code reaches Tool 1.
func TestUSSDRouterSendsRespondersToTheirMenu(t *testing.T) {
	var got []string
	r := ussdRouter{
		tool1: portFunc(func(context.Context, intake.USSDRequest) (intake.Screen, error) {
			got = append(got, "tool1")
			return intake.Screen{}, nil
		}),
		responders: portFunc(func(context.Context, intake.USSDRequest) (intake.Screen, error) {
			got = append(got, "responder")
			return intake.Screen{}, nil
		}),
		codes: map[string]bool{"*384*2#": true},
	}
	for _, code := range []string{"*384*1#", "*384*2#"} {
		if _, err := r.HandleUSSD(context.Background(), intake.USSDRequest{ServiceCode: code}); err != nil {
			t.Fatal(err)
		}
	}
	if len(got) != 2 || got[0] != "tool1" || got[1] != "responder" {
		t.Fatalf("routed %v", got)
	}
}

// CodeRabbit PR #11 (4558029969): a service code in both lists would send
// Tool 1 callers to the responder menu, so it is refused at start.
func TestServiceCodesMustNotOverlap(t *testing.T) {
	codes, err := responderCodeSet([]string{"*384*1#"}, []string{"*384*2#"})
	if err != nil || !codes["*384*2#"] || codes["*384*1#"] {
		t.Fatalf("distinct codes: %v %v", codes, err)
	}
	if _, err := responderCodeSet([]string{"*384*1#", "*384*2#"}, []string{"*384*2#"}); err == nil || !strings.Contains(err.Error(), "*384*2#") {
		t.Fatalf("overlap accepted: %v", err)
	}
}

type escalationAgeFunc func(context.Context) (float64, int, error)

func (f escalationAgeFunc) OldestUnacknowledgedEscalation(ctx context.Context) (float64, int, error) {
	return f(ctx)
}

// Tier 3 (F-01): the leader exports how many incidents hold an
// unacknowledged escalation and the age of the oldest; a failed read leaves
// the last values and is reported.
func TestEscalationGaugesExportTheDeadManSignal(t *testing.T) {
	reg := metrics.NewRegistry()
	g := newEscalationGauges(reg)
	if err := g.update(context.Background(), escalationAgeFunc(func(context.Context) (float64, int, error) { return 420, 2, nil })); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	reg.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()
	if !strings.Contains(body, "sauti_escalations_unacknowledged 2") || !strings.Contains(body, "sauti_escalation_oldest_unacknowledged_seconds 420") {
		t.Fatalf("metrics:\n%s", body)
	}
	if err := g.update(context.Background(), escalationAgeFunc(func(context.Context) (float64, int, error) { return 0, 0, errors.New("db down") })); err == nil {
		t.Fatal("a failed read was not reported")
	}
}

// Review R4: a former leader does not keep exporting the last values it
// read: when its term ends the gauges go to 0 and the leader gauge to 0, so
// the alert sees which instance exports and never a stale age.
func TestEscalationGaugesResetWhenTheTermEnds(t *testing.T) {
	reg := metrics.NewRegistry()
	g := newEscalationGauges(reg)
	if err := g.update(context.Background(), escalationAgeFunc(func(context.Context) (float64, int, error) { return 420, 2, nil })); err != nil {
		t.Fatal(err)
	}
	scrape := func() string {
		rec := httptest.NewRecorder()
		reg.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		return rec.Body.String()
	}
	if body := scrape(); !hasSample(body, "sauti_escalation_gauges_leader 1") {
		t.Fatalf("leading:\n%s", body)
	}
	g.reset()
	body := scrape()
	for _, want := range []string{"sauti_escalations_unacknowledged 0", "sauti_escalation_oldest_unacknowledged_seconds 0", "sauti_escalation_gauges_leader 0"} {
		if !strings.Contains(body, want) {
			t.Fatalf("after the term, %q missing:\n%s", want, body)
		}
	}
}

// hasSample reports whether the exposition holds this exact sample line; the
// leader gauge's HELP text begins with "1 while", so a substring would match it.
func hasSample(body, line string) bool { return slices.Contains(strings.Split(body, "\n"), line) }

// V-01 (EO-1): tier 3 is the leader term's gauge loop. Its test reads what
// /metrics serves: the audit's mutant M3 (the update call removed), a loop
// that is never started, or one that reads once and freezes, leaves no
// signal or a stale one, and fails here. The dispatch watchdog starts with
// the term.
func TestTheLeaderTermExportsTheGauges(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reg := metrics.NewRegistry()
		scrape := func() string {
			rec := httptest.NewRecorder()
			reg.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
			return rec.Body.String()
		}
		ages, reads := []float64{700, 715}, 0
		store := escalationAgeFunc(func(context.Context) (float64, int, error) {
			age := ages[min(reads, len(ages)-1)]
			reads++
			return age, 2, nil
		})
		watching := false
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() {
			done <- leaderTerm(ctx, term{store: store, gauges: newEscalationGauges(reg), every: sweepEvery,
				watchdog: func(ctx context.Context) error { watching = true; <-ctx.Done(); return ctx.Err() },
				sweeper:  func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() },
				onErr:    func(err error) { t.Errorf("gauge read reported %v", err) }})
		}()
		synctest.Wait()
		body := scrape()
		for _, want := range []string{"sauti_escalation_gauges_leader 1", "sauti_escalations_unacknowledged 2",
			"sauti_escalation_oldest_unacknowledged_seconds 700"} {
			if !hasSample(body, want) {
				t.Fatalf("leading, %q missing:\n%s", want, body)
			}
		}
		if !watching {
			t.Fatal("the dispatch watchdog did not start with the term")
		}
		time.Sleep(sweepEvery)
		synctest.Wait()
		if body := scrape(); !hasSample(body, "sauti_escalation_oldest_unacknowledged_seconds 715") {
			t.Fatalf("after one tick the age is not read again (a frozen signal):\n%s", body)
		}
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("the term returned %v, want the sweeper's result", err)
		}
		synctest.Wait()
		if body := scrape(); !hasSample(body, "sauti_escalation_gauges_leader 0") {
			t.Fatalf("after the term:\n%s", body)
		}
	})
}

// PR #14 review: a read that fails leaves no value looking fresh. The leader
// gauge drops to 0 until a read succeeds again, so the alert on it fires
// while the age cannot be read.
func TestAFailedReadMarksTheGaugesStale(t *testing.T) {
	reg := metrics.NewRegistry()
	g := newEscalationGauges(reg)
	ok := escalationAgeFunc(func(context.Context) (float64, int, error) { return 30, 1, nil })
	if err := g.update(context.Background(), ok); err != nil {
		t.Fatal(err)
	}
	if err := g.update(context.Background(), escalationAgeFunc(func(context.Context) (float64, int, error) { return 0, 0, errors.New("db down") })); err == nil {
		t.Fatal("the failed read was not reported")
	}
	rec := httptest.NewRecorder()
	reg.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(rec.Body.String(), "sauti_escalation_gauges_leader 0") {
		t.Fatalf("after a failed read:\n%s", rec.Body.String())
	}
}
