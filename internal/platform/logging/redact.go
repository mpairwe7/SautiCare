// Package logging provides the structured log handler every Sauti process
// uses. It is the in-process layer of the two-layer "no PII in telemetry"
// control (spec §7.7); the OpenTelemetry collector masks again before storage.
package logging

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"strings"
)

const (
	redactedNumber = "[redacted-number]"
	redactedValue  = "[redacted]"
	// minDigits is the shortest digit run treated as a possible phone number:
	// a Ugandan national number without its trunk 0 has 9 digits.
	minDigits = 9
)

// numberRun matches a run of digits optionally separated by spaces or
// hyphens, with an optional leading '+'. Runs are redacted only when they
// contain at least minDigits digits.
var numberRun = regexp.MustCompile(`\+?\d[\d \-]*\d`)

var deniedKeys = map[string]bool{
	"phone": true, "msisdn": true, "phonenumber": true, "phone_number": true,
	"caller": true, "callback": true, "name": true, "full_name": true,
}

// NewHandler returns a JSON slog handler that redacts number-like runs in
// messages, string values, error values and nested structures, and blanks
// values of keys that name personal data.
func NewHandler(w io.Writer, level slog.Leveler) slog.Handler {
	return slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level:       level,
		ReplaceAttr: replaceAttr,
	})
}

// RedactString masks every digit run of minDigits or more digits.
func RedactString(s string) string {
	return numberRun.ReplaceAllStringFunc(s, func(m string) string {
		n := 0
		for i := range len(m) {
			if m[i] >= '0' && m[i] <= '9' {
				n++
			}
		}
		if n >= minDigits {
			return redactedNumber
		}
		return m
	})
}

func replaceAttr(groups []string, a slog.Attr) slog.Attr {
	if len(groups) == 0 && a.Key == slog.TimeKey {
		return a
	}
	if deniedKeys[strings.ToLower(a.Key)] {
		return slog.String(a.Key, redactedValue)
	}
	v := a.Value
	switch v.Kind() {
	case slog.KindString:
		return slog.String(a.Key, RedactString(v.String()))
	case slog.KindInt64:
		if digitCount(v.Int64()) >= minDigits {
			return slog.String(a.Key, redactedNumber)
		}
	case slog.KindUint64:
		if digitCountU(v.Uint64()) >= minDigits {
			return slog.String(a.Key, redactedNumber)
		}
	case slog.KindAny:
		if err, ok := v.Any().(error); ok {
			return slog.String(a.Key, RedactString(err.Error()))
		}
		return slog.Any(a.Key, redactAny(v.Any()))
	}
	return a
}

func digitCount(n int64) int {
	if n < 0 {
		n = -n
	}
	return digitCountU(uint64(n))
}

func digitCountU(n uint64) int {
	c := 1
	for n >= 10 {
		n /= 10
		c++
	}
	return c
}

// redactAny round-trips an arbitrary value through JSON, redacting strings
// and long numbers structurally so the emitted JSON stays valid.
func redactAny(x any) json.RawMessage {
	raw, err := json.Marshal(x)
	if err != nil {
		s, _ := json.Marshal(RedactString(fmt.Sprint(x)))
		return s
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	var tree any
	if err := dec.Decode(&tree); err != nil {
		s, _ := json.Marshal(redactedValue)
		return s
	}
	out, err := json.Marshal(walk(tree, ""))
	if err != nil {
		s, _ := json.Marshal(redactedValue)
		return s
	}
	return out
}

func walk(n any, key string) any {
	if deniedKeys[strings.ToLower(key)] {
		return redactedValue
	}
	switch t := n.(type) {
	case string:
		return RedactString(t)
	case json.Number:
		if len(strings.TrimLeft(t.String(), "-")) >= minDigits && !strings.ContainsAny(t.String(), ".eE") {
			return redactedNumber
		}
		return t
	case map[string]any:
		for k, v := range t {
			t[k] = walk(v, k)
		}
		return t
	case []any:
		for i, v := range t {
			t[i] = walk(v, "")
		}
		return t
	default:
		return t
	}
}
