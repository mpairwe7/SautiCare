package opstore

import "time"

// SetVerifyHook installs a function VerifyProjections calls between reading
// an incident's events and its projection, so tests can race an append into
// exactly that gap. It returns a function restoring the previous hook.
func SetVerifyHook(f func()) (restore func()) {
	prev := verifyHook
	verifyHook = f
	return func() { verifyHook = prev }
}

// SetOpTimeout replaces the per-call deadline (opTimeout), so tests can
// observe it without waiting the production value. It applies to stores
// created afterwards, and returns a function restoring the previous value.
func SetOpTimeout(d time.Duration) (restore func()) {
	prev := opTimeout
	opTimeout = d
	return func() { opTimeout = prev }
}

// SetVerifyTimeout replaces the deadline of a whole verification run.
func SetVerifyTimeout(d time.Duration) (restore func()) {
	prev := verifyTimeout
	verifyTimeout = d
	return func() { verifyTimeout = prev }
}
