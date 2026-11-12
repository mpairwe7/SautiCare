// Package config loads Sauti's versioned behavioural configuration: the
// values field data may move (Guide §19) and the parameters the protocol
// freezes during the study window (Build Prompt rule 3.11). Deployment
// settings (DSNs, addresses) come from environment variables instead.
//
// Every file under gated/ is covered by the amendment gate and contributes to
// Fingerprint, the dispatch-parameter version stamped on every incident.
package config

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrNotProductionReady reports placeholder content that must never run live.
var ErrNotProductionReady = errors.New("config: not production ready")

// Duration unmarshals from strings like "90s" or "10m".
type Duration struct{ time.Duration }

// UnmarshalText implements encoding.TextUnmarshaler.
func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	if err != nil {
		return fmt.Errorf("config: duration %q: %w", b, err)
	}
	d.Duration = v
	return nil
}

// Bundle is the complete loaded configuration.
type Bundle struct {
	ProtocolStatus  string
	ProtocolVersion string
	Study           Study
	Intake          Intake
	Dispatch        Dispatch
	Flags           Flags
	Screens         Screens
	Platform        Platform
	// Fingerprint is "sha256:<hex>" over every gated file (path and content).
	Fingerprint string
}

// Study holds the study-window switch that freezes gated configuration.
type Study struct {
	WindowActive bool `yaml:"study_window_active"`
}

type protocolFile struct {
	ProtocolStatus  string `yaml:"protocol_status"`
	ProtocolVersion string `yaml:"protocol_version"`
}

// Intake holds Tool 1 session parameters.
type Intake struct {
	ResumeWindow           Duration       `yaml:"resume_window"`
	DispatchTriggerScreen  int            `yaml:"dispatch_trigger_screen"`
	DefaultScreenCharLimit int            `yaml:"default_screen_char_limit"`
	ScreenCharLimits       map[string]int `yaml:"screen_char_limits"`
}

// Dispatch holds Build Prompt §10.4 parameters.
type Dispatch struct {
	Mode               string   `yaml:"mode"`
	RingStepMetres     int      `yaml:"ring_step_metres"`
	RingInterval       Duration `yaml:"ring_interval"`
	MaxRadiusMetres    int      `yaml:"max_radius_metres"`
	CascadeTimeout     Duration `yaml:"cascade_timeout"`
	RedParallelCount   int      `yaml:"red_parallel_count"`
	ForwardAckTimeout  Duration `yaml:"forward_ack_timeout"`
	VoiceFallbackAfter Duration `yaml:"voice_fallback_after"`
	RankingOrder       []string `yaml:"ranking_order"`
	RankingVersion     string   `yaml:"ranking_version"`
}

// Flags are the section 11 enhancements; all stay false during the study.
type Flags struct {
	AndroidApp   bool `yaml:"android_app"`
	CallerLink   bool `yaml:"caller_link"`
	SpeechAssist bool `yaml:"speech_assist"`
	AppAPI       bool `yaml:"app_api"`
}

// Screens is the caller-facing copy catalogue, keyed by language ("en", "lg").
type Screens struct {
	StartedTestMode map[string]string `yaml:"started_test_mode"`
	InternalError   map[string]string `yaml:"internal_error"`
}

// All returns every catalogue entry as name/lang/text triples.
func (s Screens) All() [][3]string {
	var out [][3]string
	for name, m := range map[string]map[string]string{"started_test_mode": s.StartedTestMode, "internal_error": s.InternalError} {
		for lang, text := range m {
			out = append(out, [3]string{name, lang, text})
		}
	}
	return out
}

// Platform holds ungated operational tuning.
type Platform struct {
	CheckpointInterval Duration `yaml:"checkpoint_interval"`
	OutboxBatchSize    int      `yaml:"outbox_batch_size"`
	RelayPollInterval  Duration `yaml:"relay_poll_interval"`
}

// placeholderMarkers appear in copy that has not been signed off.
var placeholderMarkers = []string{"PENDING-TRANSLATION", "XXX"}

// ProductionReady reports whether the bundle may run with SAUTI_ENV=production:
// the protocol content must be approved and no placeholder copy may remain.
func (b Bundle) ProductionReady() error {
	var problems []string
	if b.ProtocolStatus != "approved" {
		problems = append(problems, "protocol_status is "+b.ProtocolStatus)
	}
	// Intake falls back to English; without it a caller would get an empty
	// final screen and no incident reference.
	if strings.TrimSpace(b.Screens.StartedTestMode["en"]) == "" {
		problems = append(problems, "screen started_test_mode/en is missing")
	}
	for _, e := range b.Screens.All() {
		for _, m := range placeholderMarkers {
			if strings.Contains(e[2], m) {
				problems = append(problems, fmt.Sprintf("screen %s/%s contains placeholder %q", e[0], e[1], m))
			}
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w: %s", ErrNotProductionReady, strings.Join(problems, "; "))
	}
	return nil
}
