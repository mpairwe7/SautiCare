package auditstore

import "time"

// SetCallTimeout replaces the per-call deadline of every ledger call, so
// tests can observe it without waiting the production value. It applies to
// stores created afterwards, and returns a function restoring the previous
// value.
func SetCallTimeout(d time.Duration) (restore func()) {
	prev := callTimeout
	callTimeout = d
	return func() { callTimeout = prev }
}
