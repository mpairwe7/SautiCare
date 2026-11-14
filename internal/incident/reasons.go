package incident

import "slices"

// Escalation reasons: the fixed codes an incident.escalated event carries
// (hard constraint 3). Each has a recording in both languages and a row in
// docs/runbooks/no-eligible-responders.md; a test ties the three together.
const (
	ReasonTriageRuleError            = "triage_rule_error"
	ReasonRedNoAcceptance            = "red_no_acceptance"
	ReasonDispatchStalled            = "dispatch_stalled"
	ReasonDispatchError              = "dispatch_error"
	ReasonDispatchNotStarted         = "dispatch_not_started"
	ReasonVoiceHandedOver            = "voice_handed_over"
	ReasonAbandonedDangerYes         = "abandoned_after_danger_yes"
	ReasonAbandonedRedBeforeDispatch = "abandoned_red_before_dispatch"
	ReasonOutOfAreaAfterDanger       = "out_of_area_after_danger"
	ReasonNoEligibleResponder        = "no_eligible_responder"
	ReasonNoResponderNearby          = "no_responder_nearby"
	ReasonBridgeUnanswered           = "bridge_unanswered"
	ReasonMergedCallEscalated        = "merged_call_escalated"
	ReasonForwardedToFacility        = "forwarded_to_facility"
	ReasonNoFacilityAcknowledged     = "no_facility_acknowledged"
	ReasonDispatchSuspended          = "dispatch_suspended"
	ReasonAssignedFollowUp           = "assigned_follow_up"
)

// EscalationReasons lists every reason in announcement order, most urgent
// first. The order is PENDING the PI.
var EscalationReasons = []string{
	ReasonTriageRuleError, ReasonRedNoAcceptance, ReasonDispatchStalled, ReasonDispatchError,
	ReasonDispatchNotStarted, ReasonVoiceHandedOver, ReasonAbandonedDangerYes, ReasonAbandonedRedBeforeDispatch,
	ReasonOutOfAreaAfterDanger, ReasonNoEligibleResponder, ReasonNoResponderNearby, ReasonBridgeUnanswered,
	ReasonMergedCallEscalated, ReasonForwardedToFacility, ReasonNoFacilityAcknowledged, ReasonDispatchSuspended,
	ReasonAssignedFollowUp,
}

// ReasonRank is the reason's place in EscalationReasons, or
// len(EscalationReasons) for an unknown reason.
func ReasonRank(reason string) int {
	if i := slices.Index(EscalationReasons, reason); i >= 0 {
		return i
	}
	return len(EscalationReasons)
}
