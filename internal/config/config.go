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
	Triage          Triage
	Tool1           Tool1
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
	// FuzzyMatchMinSimilarity is the pg_trgm similarity a free-text landmark
	// needs to count as a match; below it the incident is located at ward
	// level and flagged for a location call-back (Build Prompt §12).
	FuzzyMatchMinSimilarity float64 `yaml:"fuzzy_match_min_similarity"`
	// AbandonAfter is how long an unfinished session may be silent before it
	// is flagged abandoned (and escalated after a danger yes, Guide §4).
	AbandonAfter Duration `yaml:"abandon_after"`
	// DedupWindow and DedupMaxZoneDistance merge calls about one emergency:
	// same category, zones at most this Chebyshev distance apart, first calls
	// within the window (Build Prompt §10.3).
	DedupWindow          Duration `yaml:"dedup_window"`
	DedupMaxZoneDistance int      `yaml:"dedup_max_zone_distance"`
}

// Dispatch holds Build Prompt §10.4 parameters.
type Dispatch struct {
	Mode               string       `yaml:"mode"`
	RingStepMetres     int          `yaml:"ring_step_metres"`
	RingInterval       Duration     `yaml:"ring_interval"`
	MaxRadiusMetres    int          `yaml:"max_radius_metres"`
	CascadeTimeout     Duration     `yaml:"cascade_timeout"`
	RedParallelCount   int          `yaml:"red_parallel_count"`
	ForwardAckTimeout  Duration     `yaml:"forward_ack_timeout"`
	VoiceFallbackAfter Duration     `yaml:"voice_fallback_after"`
	RedEscalationAfter Duration     `yaml:"red_escalation_after"`
	StoppingRule       StoppingRule `yaml:"stopping_rule"`
	RankingOrder       []string     `yaml:"ranking_order"`
	RankingVersion     string       `yaml:"ranking_version"`
}

// StoppingRule is the study's dispatch stopping rule (Guide §14): red
// dispatch failure above MaxFailureRate across Window consecutive red
// incidents suspends dispatch automatically (O6).
type StoppingRule struct {
	Window         int     `yaml:"window"`
	MaxFailureRate float64 `yaml:"max_failure_rate"`
}

// Triage holds the trust anchors for clinical rule bundles: who may sign
// them (ML-DSA-65 public keys, base64, by key ID) and which bundle runs.
// Changing either is a protocol change under the amendment gate.
type Triage struct {
	RulesFile   string            `yaml:"rules_file"`
	TrustedKeys map[string]string `yaml:"trusted_keys"`
}

// Flags are the section 11 enhancements; all stay false during the study.
type Flags struct {
	AndroidApp   bool `yaml:"android_app"`
	CallerLink   bool `yaml:"caller_link"`
	SpeechAssist bool `yaml:"speech_assist"`
	AppAPI       bool `yaml:"app_api"`
}

// Screens holds adapter-level caller copy, keyed by language ("en", "lg").
// InternalError is the END screen given when a request cannot be processed:
// it names the free voice line (fail safe to a human, rule 3.14).
type Screens struct {
	InternalError map[string]string `yaml:"internal_error"`
}

// All returns every catalogue entry as name/lang/text triples.
func (s Screens) All() [][3]string {
	var out [][3]string
	for lang, text := range s.InternalError {
		out = append(out, [3]string{"internal_error", lang, text})
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
// PENDING-MD-PI marks copy awaiting the Medical Director's and PI's sign-off
// (O6: the message callers get while dispatch is suspended).
var placeholderMarkers = []string{"PENDING-TRANSLATION", "PENDING-TOOL1", "PENDING-MD-PI", "XXX"}

// SuspensionReady reports whether dispatch may be suspended in production
// (O6): the message callers get while it is suspended must be approved by
// the Medical Director and the PI — no placeholder marker in any language.
func (b Bundle) SuspensionReady() error {
	var problems []string
	for _, lang := range b.Tool1.Languages {
		text := b.Tool1.Messages.DispatchSuspended[lang]
		if strings.TrimSpace(text) == "" {
			problems = append(problems, "messages.dispatch_suspended/"+lang+" is missing")
		}
		for _, m := range placeholderMarkers {
			if strings.Contains(text, m) {
				problems = append(problems, fmt.Sprintf("messages.dispatch_suspended/%s contains placeholder %q", lang, m))
			}
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w: %s", ErrNotProductionReady, strings.Join(problems, "; "))
	}
	return nil
}

// ProductionReady reports whether the bundle may run with SAUTI_ENV=production:
// the protocol content must be approved and no placeholder copy may remain.
func (b Bundle) ProductionReady() error {
	var problems []string
	if b.ProtocolStatus != "approved" {
		problems = append(problems, "protocol_status is "+b.ProtocolStatus)
	}
	// The adapter falls back to English; without it a failed request would
	// get an empty END screen and no voice line.
	if strings.TrimSpace(b.Screens.InternalError["en"]) == "" {
		problems = append(problems, "screen internal_error/en is missing")
	}
	if b.Tool1.Status != "approved" {
		problems = append(problems, "tool1 status is "+b.Tool1.Status)
	}
	for _, e := range append(b.Screens.All(), b.Tool1.Texts()...) {
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
