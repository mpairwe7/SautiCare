package auditstore

import "time"

// SetAppendTimeout replaces the per-call deadline of Append, so tests can
// observe it without waiting the production value. It returns a function
// restoring the previous value.
func SetAppendTimeout(d time.Duration) (restore func()) {
	prev := appendTimeout
	appendTimeout = d
	return func() { appendTimeout = prev }
}
