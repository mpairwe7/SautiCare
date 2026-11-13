package dispatch

import (
	"errors"
	"math"
)

// StoppingRule is the study's stopping rule computed from system data
// (Guide §14): red dispatch failure above MaxFailureRate across Window
// consecutive red incidents suspends dispatch automatically (O6). A failure
// is a red incident with no acceptance before its closure or its escalation
// for lack of one.
type StoppingRule struct {
	Window         int
	MaxFailureRate float64
}

// Validate refuses a rule that could never, or would always, fire. A NaN
// rate compares false both ways, so it is refused by name (YAML decodes
// ".nan" as a float).
func (r StoppingRule) Validate() error {
	if r.Window < 1 || math.IsNaN(r.MaxFailureRate) || r.MaxFailureRate <= 0 || r.MaxFailureRate >= 1 {
		return errors.New("dispatch: stopping rule needs a window of at least 1 and a rate strictly between 0 and 1")
	}
	return nil
}

// Breached judges the most recent settled red incidents, newest first (true
// = an acceptance came in time). Fewer than Window outcomes are not judged.
func (r StoppingRule) Breached(newestFirst []bool) (bool, int) {
	if len(newestFirst) < r.Window {
		return false, 0
	}
	failures := 0
	for _, ok := range newestFirst[:r.Window] {
		if !ok {
			failures++
		}
	}
	// failures/window > rate, compared as failures > rate·window.
	return float64(failures) > r.MaxFailureRate*float64(r.Window), failures
}
