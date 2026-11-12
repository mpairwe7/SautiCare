package incident

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// Diff describes how two projections differ, or returns "" if they are
// equal. Timestamps compare as instants. It is used to prove that the
// database projection equals Fold of its events (`sautictl
// verify-projections`, spec §6.2).
func Diff(a, b Projection) string {
	var d []string
	add := func(field string, x, y any) { d = append(d, fmt.Sprintf("%s: %v != %v", field, x, y)) }
	if a.ID != b.ID {
		add("id", a.ID, b.ID)
	}
	if a.Reference != b.Reference {
		add("reference", a.Reference, b.Reference)
	}
	if a.State != b.State {
		add("state", a.State, b.State)
	}
	if a.Version != b.Version {
		add("version", a.Version, b.Version)
	}
	if a.Channel != b.Channel {
		add("channel", a.Channel, b.Channel)
	}
	if a.Severity != b.Severity {
		add("severity", a.Severity, b.Severity)
	}
	if a.LogicVersion != b.LogicVersion {
		add("logic_version", a.LogicVersion, b.LogicVersion)
	}
	for i := 1; i <= 7; i++ {
		if !sameInstant(a.T[i], b.T[i]) {
			add(fmt.Sprintf("T%d", i), fmtTime(a.T[i]), fmtTime(b.T[i]))
		}
	}
	if a.Escalated != b.Escalated {
		add("escalated", a.Escalated, b.Escalated)
	}
	if !slices.Equal(a.Flags, b.Flags) && !(len(a.Flags) == 0 && len(b.Flags) == 0) {
		add("flags", a.Flags, b.Flags)
	}
	if (a.MergedInto == nil) != (b.MergedInto == nil) || (a.MergedInto != nil && *a.MergedInto != *b.MergedInto) {
		add("merged_into", a.MergedInto, b.MergedInto)
	}
	return strings.Join(d, "; ")
}

func sameInstant(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

func fmtTime(t *time.Time) string {
	if t == nil {
		return "<nil>"
	}
	return t.UTC().Format(time.RFC3339Nano)
}
