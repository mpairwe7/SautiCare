package incident

import (
	"regexp"
	"testing"
)

// The registry's order is the announcement order: the most urgent reason is
// spoken first (ADR-0029 rule 8), and an unknown reason ranks last.
func TestReasonRankOrdersTriageRuleErrorFirst(t *testing.T) {
	if got := ReasonRank(ReasonTriageRuleError); got != 0 {
		t.Fatalf("triage_rule_error rank = %d, want 0", got)
	}
	if ReasonRank(ReasonRedNoAcceptance) >= ReasonRank(ReasonNoEligibleResponder) {
		t.Fatal("red_no_acceptance must outrank no_eligible_responder")
	}
	if got := ReasonRank("not_a_reason"); got != len(EscalationReasons) {
		t.Fatalf("unknown rank = %d, want %d", got, len(EscalationReasons))
	}
	code := regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	seen := map[string]bool{}
	for _, r := range EscalationReasons {
		if !code.MatchString(r) || seen[r] {
			t.Fatalf("reason %q is not a unique code", r)
		}
		seen[r] = true
	}
	if len(EscalationReasons) != 17 {
		t.Fatalf("%d reasons, want 17", len(EscalationReasons))
	}
}
