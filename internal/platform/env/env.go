// Package env reads process configuration from environment variables
// (12-factor; the MVP stack doc keeps services re-hostable on Kubernetes).
package env

import (
	"fmt"
	"os"
	"strings"
)

// Required returns the value of key or an error naming the missing variable.
func Required(key string) (string, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return "", fmt.Errorf("env: %s is required", key)
	}
	return v, nil
}

// Default returns the value of key, or def when unset.
func Default(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// List splits a comma-separated variable, dropping blanks.
func List(key string) []string {
	var out []string
	for _, p := range strings.Split(os.Getenv(key), ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Environment returns SAUTI_ENV, which must be exactly "dev", "test" or
// "production". Anything else — unset, misspelt, differently cased — is an
// error, so a live host can never fall back to development behaviour.
func Environment() (string, error) {
	v := os.Getenv("SAUTI_ENV")
	switch v {
	case "dev", "test", "production":
		return v, nil
	}
	return "", fmt.Errorf("env: SAUTI_ENV must be dev, test or production (got %q)", v)
}
