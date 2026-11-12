package bootstrap

// SetAfterCreateHook installs a function Run calls right after it creates a
// database and before it installs that database's grants, so tests can fail
// a bootstrap in exactly that window. It returns a function restoring the
// previous hook.
func SetAfterCreateHook(f func(db string) error) (restore func()) {
	prev := afterCreate
	afterCreate = f
	return func() { afterCreate = prev }
}
