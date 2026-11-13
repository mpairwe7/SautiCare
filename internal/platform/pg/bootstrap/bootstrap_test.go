package bootstrap

import (
	"context"
	"errors"
	"testing"
)

// The environment is checked before any database work (M7).
func TestRunRefusesNonDevEnvironmentsBeforeConnecting(t *testing.T) {
	for _, env := range []string{"production", "Production", "prod", ""} {
		if _, err := Run(context.Background(), nil, Options{Password: "x", Env: env}); !errors.Is(err, ErrProductionForbidden) {
			t.Errorf("Run(env=%q) = %v, want ErrProductionForbidden", env, err)
		}
	}
}
