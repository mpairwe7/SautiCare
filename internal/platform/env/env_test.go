package env

import "testing"

// SAUTI_ENV must fail closed: an unset, misspelt or differently-cased value
// on a live host must not silently run as development (review finding M7).
func TestEnvironmentAcceptsOnlyKnownValues(t *testing.T) {
	for _, ok := range []string{"dev", "test", "production"} {
		t.Setenv("SAUTI_ENV", ok)
		if got, err := Environment(); err != nil || got != ok {
			t.Errorf("Environment(%q) = %q, %v", ok, got, err)
		}
	}
	for _, bad := range []string{"", "prod", "Production", "PRODUCTION", "development", " dev"} {
		t.Setenv("SAUTI_ENV", bad)
		if _, err := Environment(); err == nil {
			t.Errorf("Environment(%q) accepted", bad)
		}
	}
}
