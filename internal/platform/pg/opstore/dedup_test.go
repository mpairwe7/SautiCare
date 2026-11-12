package opstore

import (
	"math"
	"testing"
	"time"
)

// The SQL prefilter only narrows the candidates; intake.DedupRule.Survivor
// applies the exact window. A fractional window must therefore round up: a
// truncated prefilter would drop a call Survivor accepts (15m500ms -> 900 s
// excludes a call 15m250ms away).
func TestPrefilterSecondsRoundsUp(t *testing.T) {
	for _, tc := range []struct {
		w    time.Duration
		want int32
	}{
		{15 * time.Minute, 900},
		{15*time.Minute + 500*time.Millisecond, 901},
		{time.Second, 1},
		{time.Nanosecond, 1},
		{1500 * time.Millisecond, 2},
	} {
		got, err := prefilterSeconds(tc.w)
		if err != nil || got != tc.want {
			t.Errorf("prefilterSeconds(%v) = %d, %v; want %d", tc.w, got, err, tc.want)
		}
	}
	if _, err := prefilterSeconds(time.Duration(math.MaxInt64)); err == nil {
		t.Error("a window beyond int32 seconds was accepted")
	}
}
