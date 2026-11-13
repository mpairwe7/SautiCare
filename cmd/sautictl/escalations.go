package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/platform/pg"
	"sautihealth.com/sauti/internal/platform/pg/opstore"
)

type escalationReason struct {
	Reason string `json:"reason"`
	Role   string `json:"role,omitempty"`
}

type escalationRow struct {
	Reference       string             `json:"reference"`
	Severity        string             `json:"severity"`
	Reasons         []escalationReason `json:"reasons"`
	OldestSeconds   int                `json:"oldest_seconds"`
	Rings           int                `json:"rings"`
	LastRingSeconds *int               `json:"last_ring_seconds"` // null: never rung
}

type taskRow struct {
	Reference  string `json:"reference"`
	Severity   string `json:"severity"`
	AgeSeconds int    `json:"age_seconds"`
}

// cmdEscalations lists, for the coordinator before the M5 console (F-08),
// the open incidents whose escalations nobody has acknowledged, every reason
// most urgent first, and the location call-back tasks. It prints references
// only: never a number or free text.
func cmdEscalations(ctx context.Context, args []string, out io.Writer) error {
	fs := flags("escalations")
	dsn := fs.String("dsn", os.Getenv("SAUTI_DSN"), "operational store DSN (op_readonly; default $SAUTI_DSN)")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dsn == "" {
		return errors.New("--dsn or SAUTI_DSN is required")
	}
	pool, err := pg.Connect(ctx, *dsn, "sautictl", 2)
	if err != nil {
		return err
	}
	defer pool.Close()
	store := opstore.New(pool)
	open, err := store.OpenEscalations(ctx)
	if err != nil {
		return err
	}
	callbacks, err := store.LocationCallbacks(ctx)
	if err != nil {
		return err
	}
	report := struct {
		Escalations []escalationRow `json:"escalations"`
		Tasks       []taskRow       `json:"tasks"`
	}{Escalations: []escalationRow{}, Tasks: []taskRow{}}
	for _, e := range open {
		row := escalationRow{Reference: e.Reference.Spoken(), Severity: severityName(e.Severity), OldestSeconds: int(e.OldestSeconds), Rings: e.Rings}
		for i, r := range e.Reasons {
			row.Reasons = append(row.Reasons, escalationReason{Reason: r, Role: e.Roles[i]})
		}
		slices.SortStableFunc(row.Reasons, func(a, b escalationReason) int {
			return cmp.Compare(incident.ReasonRank(a.Reason), incident.ReasonRank(b.Reason))
		})
		if e.LastRingSeconds >= 0 {
			s := int(e.LastRingSeconds)
			row.LastRingSeconds = &s
		}
		report.Escalations = append(report.Escalations, row)
	}
	for _, c := range callbacks {
		report.Tasks = append(report.Tasks, taskRow{Reference: c.Reference.Spoken(), Severity: severityName(c.Severity), AgeSeconds: int(c.AgeSeconds)})
	}
	if *asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "ESCALATIONS NOT ACKNOWLEDGED: %d\n", len(report.Escalations))
	for _, e := range report.Escalations {
		last := "never rung"
		if e.LastRingSeconds != nil {
			last = "last ring " + secs(*e.LastRingSeconds) + " ago"
		}
		reasons := make([]string, len(e.Reasons))
		for i, r := range e.Reasons {
			reasons[i] = r.Reason
			if r.Role != "" {
				reasons[i] += " (" + r.Role + ")"
			}
		}
		fmt.Fprintf(w, "%s\t%s\toldest %s\trings %d\t%s\t%s\n", e.Reference, e.Severity, secs(e.OldestSeconds), e.Rings, last,
			strings.Join(reasons, ", "))
	}
	fmt.Fprintf(w, "LOCATION CALL-BACK TASKS: %d\n", len(report.Tasks))
	for _, t := range report.Tasks {
		fmt.Fprintf(w, "%s\t%s\topen %s\n", t.Reference, t.Severity, secs(t.AgeSeconds))
	}
	return w.Flush()
}

func severityName(s string) string {
	if s == "" {
		return "unclassified"
	}
	return s
}

func secs(n int) string { return (time.Duration(n) * time.Second).String() }
