package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func repoConfig(t *testing.T) Bundle {
	t.Helper()
	b, err := Load(os.DirFS("../../config"))
	if err != nil {
		t.Fatalf("Load(repo config): %v", err)
	}
	return b
}

// The committed configuration must carry the decided values (spec §2, Build
// Prompt §10.4, §11) and the safe defaults for open decisions.
func TestLoadRepoConfigDefaults(t *testing.T) {
	b := repoConfig(t)
	checks := []struct {
		name string
		ok   bool
	}{
		{"resume window 10m (D4)", b.Intake.ResumeWindow.Duration == 10*time.Minute},
		{"dispatch trigger screen 7 (D5)", b.Intake.DispatchTriggerScreen == 7},
		{"default screen limit 160 (O7)", b.Intake.DefaultScreenCharLimit == 160},
		{"ring step 500 m", b.Dispatch.RingStepMetres == 500},
		{"ring interval 60s", b.Dispatch.RingInterval.Duration == 60*time.Second},
		{"max radius 2.5 km", b.Dispatch.MaxRadiusMetres == 2500},
		{"cascade timeout 90s", b.Dispatch.CascadeTimeout.Duration == 90*time.Second},
		{"red parallel 3", b.Dispatch.RedParallelCount == 3},
		{"forward ack 60s", b.Dispatch.ForwardAckTimeout.Duration == 60*time.Second},
		{"voice fallback 20s", b.Dispatch.VoiceFallbackAfter.Duration == 20*time.Second},
		{"protocol mode until amendment", b.Dispatch.Mode == "protocol"},
		{"decision 4 default order", strings.Join(b.Dispatch.RankingOrder, ",") == "zone_distance,response_history,least_recently_dispatched"},
		{"all section 11 flags off", !b.Flags.AndroidApp && !b.Flags.CallerLink && !b.Flags.SpeechAssist && !b.Flags.AppAPI},
		{"study window inactive", !b.Study.WindowActive},
		{"protocol pending", b.ProtocolStatus == "pending"},
		{"checkpoint interval 5m", b.Platform.CheckpointInterval.Duration == 5*time.Minute},
		{"fingerprint present", strings.HasPrefix(b.Fingerprint, "sha256:") && len(b.Fingerprint) == 71},
	}
	for _, c := range checks {
		if !c.ok {
			t.Errorf("%s: not satisfied (bundle %+v)", c.name, b)
		}
	}
}

func minimalFS() fstest.MapFS {
	return fstest.MapFS{
		"study.yaml":          {Data: []byte("study_window_active: false\n")},
		"platform.yaml":       {Data: []byte("checkpoint_interval: 5m\noutbox_batch_size: 10\nrelay_poll_interval: 1s\n")},
		"gated/protocol.yaml": {Data: []byte("protocol_status: approved\nprotocol_version: \"2.0\"\n")},
		"gated/intake.yaml":   {Data: []byte("resume_window: 10m\ndispatch_trigger_screen: 7\ndefault_screen_char_limit: 160\nscreen_char_limits: {}\n")},
		"gated/dispatch.yaml": {Data: []byte("mode: protocol\nring_step_metres: 500\nring_interval: 60s\nmax_radius_metres: 2500\ncascade_timeout: 90s\nred_parallel_count: 3\nforward_ack_timeout: 60s\nvoice_fallback_after: 20s\nranking_order: [zone_distance]\nranking_version: v1\n")},
		"gated/flags.yaml":    {Data: []byte("android_app: false\ncaller_link: false\nspeech_assist: false\napp_api: false\n")},
		"gated/screens.yaml":  {Data: []byte("started_test_mode: {en: \"Incident {ref}\", lg: \"Ekikolwa {ref}\"}\ninternal_error: {en: \"Call 0800 100 200\", lg: \"Kuba 0800 100 200\"}\n")},
	}
}

func TestLoadRejectsUnknownField(t *testing.T) {
	fs := minimalFS()
	fs["gated/flags.yaml"] = &fstest.MapFile{Data: []byte("android_app: false\ncaller_link: false\nspeech_assist: false\napp_api: false\nteleport: true\n")}
	if _, err := Load(fs); err == nil || !strings.Contains(err.Error(), "teleport") {
		t.Fatalf("Load with unknown key = %v, want error naming the key", err)
	}
}

func TestLoadRejectsMissingFile(t *testing.T) {
	fs := minimalFS()
	delete(fs, "gated/dispatch.yaml")
	if _, err := Load(fs); err == nil {
		t.Fatal("Load succeeded without dispatch.yaml")
	}
}

func TestFingerprintStableAndSensitive(t *testing.T) {
	a, err := Load(minimalFS())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Load(minimalFS())
	if a.Fingerprint != b.Fingerprint {
		t.Fatal("fingerprint not stable")
	}
	fs := minimalFS()
	fs["gated/dispatch.yaml"] = &fstest.MapFile{Data: []byte(strings.Replace(string(fs["gated/dispatch.yaml"].Data), "90s", "91s", 1))}
	c, err := Load(fs)
	if err != nil {
		t.Fatal(err)
	}
	if c.Fingerprint == a.Fingerprint {
		t.Fatal("fingerprint unchanged after a gated parameter changed")
	}
	// Ungated platform tuning must not change the dispatch-parameter version.
	fs2 := minimalFS()
	fs2["platform.yaml"] = &fstest.MapFile{Data: []byte("checkpoint_interval: 1m\noutbox_batch_size: 10\nrelay_poll_interval: 1s\n")}
	d, _ := Load(fs2)
	if d.Fingerprint != a.Fingerprint {
		t.Fatal("ungated change altered the fingerprint")
	}
}

func TestProductionReadyFailsWhilePending(t *testing.T) {
	if err := repoConfig(t).ProductionReady(); !errors.Is(err, ErrNotProductionReady) {
		t.Fatalf("repo config ProductionReady = %v, want ErrNotProductionReady", err)
	}
	ok, err := Load(minimalFS())
	if err != nil {
		t.Fatal(err)
	}
	if err := ok.ProductionReady(); err != nil {
		t.Fatalf("approved, fully translated bundle not ready: %v", err)
	}
	fs := minimalFS()
	fs["gated/screens.yaml"] = &fstest.MapFile{Data: []byte("started_test_mode: {en: \"Incident {ref}\", lg: \"PENDING-TRANSLATION\"}\ninternal_error: {en: \"Call 0800-XXX-XXX\", lg: \"x\"}\n")}
	pending, _ := Load(fs)
	if err := pending.ProductionReady(); !errors.Is(err, ErrNotProductionReady) {
		t.Fatalf("placeholder copy accepted for production: %v", err)
	}
}

// CodeRabbit PR #2: intake falls back to the English start screen; without
// it the caller would get an empty END screen and no incident reference.
func TestProductionReadyRequiresEnglishStartScreen(t *testing.T) {
	fs := minimalFS()
	fs["gated/screens.yaml"] = &fstest.MapFile{Data: []byte("started_test_mode: {lg: \"Ekikolwa {ref}\"}\ninternal_error: {en: \"Call 0800 100 200\", lg: \"Kuba 0800 100 200\"}\n")}
	b, err := Load(fs)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.ProductionReady(); !errors.Is(err, ErrNotProductionReady) || !strings.Contains(err.Error(), "started_test_mode/en") {
		t.Fatalf("ProductionReady without an English start screen = %v", err)
	}
}

// changingFS returns different content for a path on every read, to prove
// Load fingerprints exactly the bytes it decoded (CodeRabbit PR #2).
type changingFS struct {
	fstest.MapFS
	path  string
	reads int
}

func (c *changingFS) ReadFile(name string) ([]byte, error) {
	if name == c.path {
		c.reads++
		return []byte(strings.Replace(string(c.MapFS[name].Data), "90s", fmt.Sprintf("%ds", 90+c.reads), 1)), nil
	}
	return c.MapFS.ReadFile(name)
}

func (c *changingFS) Open(name string) (fs.File, error) { return c.MapFS.Open(name) }

func TestFingerprintCoversTheDecodedBytes(t *testing.T) {
	c := &changingFS{MapFS: minimalFS(), path: "gated/dispatch.yaml"}
	b, err := Load(c)
	if err != nil {
		t.Fatal(err)
	}
	// Recompute the fingerprint from a stable FS holding what was decoded.
	stable := minimalFS()
	stable["gated/dispatch.yaml"] = &fstest.MapFile{Data: []byte(strings.Replace(string(stable["gated/dispatch.yaml"].Data),
		"90s", fmt.Sprintf("%ds", int(b.Dispatch.CascadeTimeout.Seconds())), 1))}
	want, err := Load(stable)
	if err != nil {
		t.Fatal(err)
	}
	if b.Fingerprint != want.Fingerprint {
		t.Fatalf("fingerprint does not match the decoded content (reads=%d, cascade=%v)", c.reads, b.Dispatch.CascadeTimeout)
	}
}

// Every file under gated/ is either part of the fingerprint or declared as
// separately versioned; an unknown gated file would otherwise be frozen by
// the amendment gate yet silently unused.
func TestLoadRejectsUnknownGatedFile(t *testing.T) {
	fs := minimalFS()
	fs["gated/extra.yaml"] = &fstest.MapFile{Data: []byte("x: 1\n")}
	if _, err := Load(fs); err == nil || !strings.Contains(err.Error(), "gated/extra.yaml") {
		t.Fatalf("unknown gated file: err = %v", err)
	}
	fs = minimalFS()
	fs["gated/gazetteer.yaml"] = &fstest.MapFile{Data: []byte("version: v\n")}
	if _, err := Load(fs); err != nil {
		t.Fatalf("separately versioned gated file refused: %v", err)
	}
}
