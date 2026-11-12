package incident

import "errors"

var (
	// ErrIllegalTransition: the event is not allowed in the incident's state,
	// or its type is unknown.
	ErrIllegalTransition = errors.New("incident: illegal transition")
	// ErrSeverityLowered: a classification tried to lower severity. No rule
	// and no model may lower a severity (hard constraint 3).
	ErrSeverityLowered = errors.New("incident: severity may not be lowered")
	// ErrEmptyHistory: Fold was given no events.
	ErrEmptyHistory = errors.New("incident: empty event history")
	// ErrSequenceGap: events are not consecutive from seq 1.
	ErrSequenceGap = errors.New("incident: event sequence gap")
	// ErrVersionConflict: the expected version did not match (optimistic lock).
	ErrVersionConflict = errors.New("incident: version conflict")
	// ErrNotFound: no such incident.
	ErrNotFound = errors.New("incident: not found")
	// ErrForbidden reports an action on something that is not the actor's,
	// such as a responder answering another responder's alert.
	ErrForbidden = errors.New("incident: not permitted")
	// ErrReferenceTaken: the random reference collided with an existing one.
	ErrReferenceTaken = errors.New("incident: reference already taken")
)
