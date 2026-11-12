package opstore

// SetVerifyHook installs a function VerifyProjections calls between reading
// an incident's events and its projection, so tests can race an append into
// exactly that gap. It returns a function restoring the previous hook.
func SetVerifyHook(f func()) (restore func()) {
	prev := verifyHook
	verifyHook = f
	return func() { verifyHook = prev }
}
