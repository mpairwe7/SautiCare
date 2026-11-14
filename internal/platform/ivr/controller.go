// Package ivr is the voice channel adapter (Build Prompt §10.2): it turns
// Asterisk ARI events into intake.Service calls and plays recorded prompts.
// Answers come from the keypad only; nothing is recorded; a caller's number
// is never logged.
package ivr

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"sautihealth.com/sauti/internal/incident"
	"sautihealth.com/sauti/internal/intake"
	"sautihealth.com/sauti/internal/pii"
	"sautihealth.com/sauti/internal/platform/ari"
)

// Event types re-exported from the ARI client.
type Event = ari.Event

const (
	EventStasisStart        = ari.EventStasisStart
	EventDTMF               = ari.EventDTMF
	EventPlaybackStarted    = ari.EventPlaybackStarted
	EventPlaybackFinished   = ari.EventPlaybackFinished
	EventPlaybackContinuing = ari.EventPlaybackContinuing
	EventStasisEnd          = ari.EventStasisEnd
	EventHangupRequest      = ari.EventHangupRequest
	playbackFailed          = ari.PlaybackFailed
)

// ARI is the subset of the Asterisk client the controller uses.
type ARI interface {
	Answer(ctx context.Context, channelID string) error
	Play(ctx context.Context, channelID, playbackID string, sounds []string) error
	StopPlayback(ctx context.Context, playbackID string) error
	Continue(ctx context.Context, channelID, dialContext, extension string, priority int) error
	Hangup(ctx context.Context, channelID string) error
	ListChannels(ctx context.Context) ([]ari.Channel, error)
	SetReference(ctx context.Context, channelID, ref string) error
}

// Intake is the voice side of intake.Service.
type Intake interface {
	StartIVR(ctx context.Context, callID string, phone pii.Phone) (intake.IVRTurn, error)
	AnswerIVR(ctx context.Context, call intake.IVRCall, input string) (intake.IVRTurn, error)
	HandOver(ctx context.Context, call intake.IVRCall, reason string) error
}

// FallbackContext is the dialplan context that takes a call Sauti cannot
// serve: a recorded message, then the on-call coordinators
// (telephony/asterisk/extensions.conf; hard constraint 14).
const FallbackContext = "sauti-ivr-fallback"

// Options tune the call behaviour.
type Options struct {
	// NoInput is how long silence lasts, counted from the end of a prompt,
	// before the prompt is played again. After MaxReprompts a caller who
	// has answered a screen (or resumed past the first) goes to a
	// coordinator; a call silent from the first prompt ends, as a pocket
	// dial (ADR-0029 rule 2; resume brings the caller back).
	NoInput      time.Duration
	MaxReprompts int
	// MaxNoMatch is how many invalid answers in a row to one question hand
	// the caller to a coordinator (default 3).
	MaxNoMatch int
	// OpTimeout bounds each intake or ARI operation of a call (default
	// 3 s). A call whose operation runs out of time is handed to a
	// coordinator; other calls are not affected.
	OpTimeout time.Duration
	// PromptMax is the longest a prompt plays (default 30 s): a prompt
	// whose end is never reported is re-played PromptMax + NoInput after it
	// was requested.
	PromptMax time.Duration
	// App is the Stasis application the controller serves (default
	// sauti-ivr): Adopt hands over the calls parked in it.
	App string
	// OnEscalationLost is called when a hand-over escalation is still
	// refused after 10 minutes of retries (the lost-escalation metric).
	OnEscalationLost func()
}

// maxCallbackDigits bounds a typed callback number (E.164 is at most 15).
const maxCallbackDigits = 16

// evNoInput is the controller's own event: a call's no-input timer fired.
const evNoInput = "SautiNoInput"

// item is one event queued for a call's worker; gen identifies the timer
// that queued a no-input event, so a stale one is ignored.
type item struct {
	ev  Event
	gen int
}

// call is one voice call's state, owned by its worker goroutine.
type call struct {
	ch        string
	turn      intake.IVRTurn
	digits    string
	seq       int
	current   string // playback ID of the prompt last requested
	started   bool   // current has started: keys answer it from now on
	playing   bool   // current is playing: a key stops it (barge-in)
	hangupOn  string // playback after which to hang up
	reprompts int
	invalid   int // invalid answers in a row to the question shown
	timer     *time.Timer
	timerGen  int
	begun     bool
	ended     bool
}

// worker runs one call: its events in order, one at a time, on its own
// goroutine, so a slow call never holds up another.
type worker struct {
	mu    sync.Mutex
	queue []item
	wake  chan struct{}
	call  *call
}

func (w *worker) push(it item) {
	w.mu.Lock()
	w.queue = append(w.queue, it)
	w.mu.Unlock()
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *worker) pop(ctx context.Context) (item, bool) {
	for {
		w.mu.Lock()
		if len(w.queue) > 0 {
			it := w.queue[0]
			w.queue = w.queue[1:]
			w.mu.Unlock()
			return it, true
		}
		w.mu.Unlock()
		select {
		case <-w.wake:
		case <-ctx.Done():
			return item{}, false
		}
	}
}

// Controller runs voice calls.
type Controller struct {
	ari     ARI
	in      Intake
	log     *slog.Logger
	opt     Options
	mu      sync.Mutex // guards workers and adopted only: no call's work runs under it
	workers map[string]*worker
	adopted map[string]bool // channels Adopt handed over that the last list still showed
	adoptMu sync.Mutex      // one Adopt at a time
	pending atomic.Int64    // events queued and not yet handled
	running sync.WaitGroup  // workers, so Run returns after their hand-overs
}

// New builds a controller.
func New(a ARI, in Intake, log *slog.Logger, opt Options) *Controller {
	if opt.NoInput <= 0 {
		opt.NoInput = 15 * time.Second
	}
	if opt.MaxReprompts < 0 {
		opt.MaxReprompts = 0
	}
	if opt.OpTimeout <= 0 {
		opt.OpTimeout = 3 * time.Second
	}
	if opt.MaxNoMatch <= 0 {
		opt.MaxNoMatch = 3
	}
	if opt.PromptMax <= 0 {
		opt.PromptMax = 30 * time.Second
	}
	if opt.App == "" {
		opt.App = "sauti-ivr"
	}
	return &Controller{ari: a, in: in, log: log, opt: opt, workers: map[string]*worker{}, adopted: map[string]bool{}}
}

// adoptGrace is how long after a (re)connect Adopt runs: a call whose
// StasisStart is already on the new stream gets its worker first and is
// served, not handed over.
const adoptGrace = time.Second

// Run streams ARI events into the controller until ctx ends. After every
// (re)connect it adopts the calls it lost; when ctx ends it waits for the
// live calls to be handed over.
func (c *Controller) Run(ctx context.Context, client *ari.Client) error {
	defer c.running.Wait()
	return client.Stream(ctx, func() {
		c.running.Go(func() {
			select {
			case <-ctx.Done():
				return
			case <-time.After(adoptGrace):
			}
			if err := c.Adopt(ctx); err != nil {
				c.log.Error("ivr adopt after (re)connect failed", "err", err)
			}
		})
	}, func(e ari.Event) { c.Handle(ctx, e) })
}

// Adopt hands every call parked in the controller's application with no
// worker to the fallback. Such a call lost its worker in a restart, or its
// events while the stream was down, and would otherwise hear silence
// (hard constraint 14).
func (c *Controller) Adopt(ctx context.Context) error {
	// One adoption at a time: two reconnects in quick succession must not
	// both hand the same orphan over (review R1).
	c.adoptMu.Lock()
	defer c.adoptMu.Unlock()
	octx, cancel := c.op(ctx)
	chans, err := c.ari.ListChannels(octx)
	cancel()
	if err != nil {
		return err
	}
	c.mu.Lock()
	listed := map[string]bool{}
	var orphans []string
	for _, ch := range chans {
		if ch.App != c.opt.App {
			continue
		}
		listed[ch.ID] = true
		// A channel already handed over is skipped while a list still shows
		// it; its StasisStart may also still be in flight.
		if c.workers[ch.ID] == nil && !c.adopted[ch.ID] {
			c.adopted[ch.ID] = true
			orphans = append(orphans, ch.ID)
		}
	}
	for ch := range c.adopted {
		if !listed[ch] {
			delete(c.adopted, ch) // it has left the application
		}
	}
	c.mu.Unlock()
	for _, ch := range orphans {
		c.log.Warn("ivr call found without a worker; handing over", "channel", ch)
		// The channel ID is the call ID: HandOver finds the incident from it.
		c.handOver(ctx, &call{ch: ch, turn: intake.IVRTurn{Call: intake.IVRCall{CallID: ch}}})
	}
	return nil
}

// Handle queues one event for its call and returns at once. Events of one
// call are handled in order by that call's worker; a StasisStart starts the
// worker, and events of an unknown call are dropped.
func (c *Controller) Handle(ctx context.Context, e Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	w := c.workers[e.ChannelID]
	if w == nil {
		if e.Type != EventStasisStart || c.adopted[e.ChannelID] {
			return
		}
		w = &worker{wake: make(chan struct{}, 1), call: &call{ch: e.ChannelID}}
		c.workers[e.ChannelID] = w
		c.running.Add(1)
		go c.run(ctx, w)
	}
	c.pending.Add(1)
	w.push(item{ev: e})
}

// enqueue queues a no-input event for a call still running.
func (c *Controller) enqueue(ch string, it item) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if w := c.workers[ch]; w != nil {
		c.pending.Add(1)
		w.push(it)
	}
}

func (c *Controller) run(ctx context.Context, w *worker) {
	defer c.running.Done()
	st := w.call
	for !st.ended {
		it, ok := w.pop(ctx)
		if !ok {
			// Stopping with the call live: a coordinator takes it. Each step
			// has its own OpTimeout, outliving ctx (c.op).
			c.handOver(ctx, st)
			break
		}
		c.step(ctx, st, it)
		c.pending.Add(-1)
	}
	c.stopTimer(st)
	c.mu.Lock()
	if c.workers[st.ch] == w {
		delete(c.workers, st.ch)
	}
	w.mu.Lock()
	c.pending.Add(-int64(len(w.queue)))
	w.queue = nil
	w.mu.Unlock()
	c.mu.Unlock()
}

// op is the context of one intake or ARI operation: its own deadline, and
// not cancelled with the event stream, so an operation under way finishes
// or fails cleanly.
func (c *Controller) op(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), c.opt.OpTimeout)
}

func (c *Controller) step(ctx context.Context, st *call, it item) {
	e := it.ev
	switch e.Type {
	case EventStasisStart:
		if !st.begun { // a repeated StasisStart never opens a second session
			st.begun = true
			c.start(ctx, st, e)
		}
	case EventDTMF:
		c.dtmf(ctx, st, e.Digit)
	case EventPlaybackStarted, EventPlaybackContinuing:
		if e.PlaybackID == st.current {
			st.started, st.playing = true, true
			// The backstop runs per recording, from its start: a long
			// sequence (the reference, then a message) is never cut off.
			c.startTimer(st, c.opt.PromptMax+c.opt.NoInput)
		}
	case EventPlaybackFinished:
		if e.PlaybackID != st.current {
			return // a prompt stopped or replaced earlier
		}
		st.started, st.playing = true, false
		switch {
		case st.hangupOn == e.PlaybackID:
			c.hangup(ctx, st)
		case e.PlaybackState == ari.PlaybackFailed:
			// The caller heard nothing: a failure, not silence (F-03).
			c.log.Error("ivr prompt failed to play", "channel", st.ch, "playback", e.PlaybackID)
			c.handOver(ctx, st)
		case !st.turn.Prompt.End:
			c.startTimer(st, c.opt.NoInput) // silence is counted from the end of the prompt
		}
	case evNoInput:
		switch {
		case it.gen != st.timerGen:
		case st.turn.Prompt.End:
			c.silentHangup(ctx, st) // the closing prompt's end was never reported
		default:
			c.noInput(ctx, st)
		}
	case EventStasisEnd, EventHangupRequest:
		st.ended = true
	}
}

func (c *Controller) start(ctx context.Context, st *call, e Event) {
	octx, cancel := c.op(ctx)
	defer cancel()
	if err := c.ari.Answer(octx, st.ch); err != nil {
		c.log.Error("ivr answer failed", "channel", st.ch, "err", err)
		c.handOver(ctx, st)
		return
	}
	// A withheld or unparseable number is unknown: the caller is asked for a
	// callback number and dispatch proceeds on location (Build Prompt §10.2).
	phone, err := pii.ParsePhone(e.CallerNumber)
	if err != nil {
		phone = pii.Phone{}
	}
	turn, err := c.in.StartIVR(octx, st.ch, phone)
	if err != nil {
		c.log.Error("ivr intake failed", "channel", st.ch, "err", err)
		c.handOver(ctx, st)
		return
	}
	st.turn = turn
	c.play(ctx, st, Sounds(turn.Prompt), turn.Prompt.End)
}

func (c *Controller) dtmf(ctx context.Context, st *call, digit string) {
	if st.turn.Prompt.End || !st.started {
		// Before the current prompt has begun, a key belongs to the one
		// before it (still being stopped, or queued ahead): ARI plays
		// prompts in order, so taking it would answer a question the
		// caller has not heard.
		return
	}
	if st.playing {
		c.stopPlayback(ctx, st) // barge-in: the first key ends the prompt
	}
	c.stopTimer(st)
	if st.turn.Call.Shown == intake.KindCallback {
		switch {
		case digit == "#":
			input := st.digits
			st.digits = ""
			c.answer(ctx, st, input)
		case st.digits == "" && digit == "1" && st.turn.Call.HasPhone:
			c.answer(ctx, st, "1")
		case len(st.digits) < maxCallbackDigits:
			st.digits += digit
			if !st.playing {
				c.startTimer(st, c.opt.NoInput)
			}
		}
		return
	}
	c.answer(ctx, st, digit)
}

func (c *Controller) stopPlayback(ctx context.Context, st *call) {
	st.playing = false
	octx, cancel := c.op(ctx)
	defer cancel()
	if err := c.ari.StopPlayback(octx, st.current); err != nil {
		c.log.Warn("ivr stop playback failed", "channel", st.ch, "err", err)
	}
}

func (c *Controller) answer(ctx context.Context, st *call, input string) {
	octx, cancel := c.op(ctx)
	defer cancel()
	turn, err := c.in.AnswerIVR(octx, st.turn.Call, input)
	if err != nil {
		c.log.Error("ivr intake failed", "channel", st.ch, "err", err)
		c.handOver(ctx, st)
		return
	}
	// The same question back with a notice is a no-match.
	noMatch := turn.Prompt.Notice != "" && turn.Prompt.Kind == st.turn.Call.Shown
	st.turn, st.reprompts, st.digits = turn, 0, ""
	if !noMatch {
		st.invalid = 0
	} else if st.invalid++; st.invalid >= c.opt.MaxNoMatch {
		c.handOver(ctx, st)
		return
	}
	c.play(ctx, st, Sounds(turn.Prompt), turn.Prompt.End)
}

func (c *Controller) noInput(ctx context.Context, st *call) {
	if st.reprompts >= c.opt.MaxReprompts {
		// Past the first screen the caller has engaged (answered it, or
		// resumed a report): silence is not a pocket dial.
		if st.turn.Call.Shown != intake.KindLanguage {
			c.handOver(ctx, st)
			return
		}
		c.silentHangup(ctx, st)
		return
	}
	st.reprompts++
	st.digits = ""
	c.play(ctx, st, Sounds(st.turn.Prompt), false)
}

// play requests a prompt. Keys are taken once it has started; the no-input
// timer starts when it finishes. A closing prompt that cannot be played is
// followed by an immediate hang-up (its PlaybackFinished would never come);
// a question that cannot be played hands the caller to a coordinator.
func (c *Controller) play(ctx context.Context, st *call, sounds []string, end bool) {
	st.seq++
	pb := st.ch + "-" + strconv.Itoa(st.seq)
	st.current, st.started, st.playing = pb, false, false
	c.stopTimer(st)
	if end {
		st.hangupOn = pb
	}
	octx, cancel := c.op(ctx)
	defer cancel()
	if err := c.ari.Play(octx, st.ch, pb, sounds); errors.Is(err, ari.ErrNotInStasis) {
		c.log.Warn("ivr call already left the application", "channel", st.ch)
		c.stopTimer(st)
		st.ended = true
		return
	} else if err != nil {
		c.log.Error("ivr play failed", "channel", st.ch, "err", err)
		if end {
			c.hangup(ctx, st)
			return
		}
		c.handOver(ctx, st)
		return
	}
	// The backstop: if the prompt's end is never reported, silence is still
	// counted (PlaybackFinished re-arms the timer at NoInput).
	c.startTimer(st, c.opt.PromptMax+c.opt.NoInput)
}

// handOver passes a call Sauti cannot serve to the dialplan's fallback (a
// recorded message, then the on-call coordinators; hard constraint 14). The
// incident and every answer given so far are already persisted, and a
// redial resumes them. The escalation is recorded first, so the coordinator
// line rings even if the transfer fails; one the store refuses is retried in
// the background, never holding the caller back. The fallback masks the
// caller ID: the coordinator sees Sauti's number and "SAUTI" with the
// reference, never the caller's. If the transfer fails, the call is hung up
// rather than left in silence.
func (c *Controller) handOver(ctx context.Context, st *call) {
	c.stopTimer(st)
	hctx, hcancel := c.op(ctx)
	if err := c.in.HandOver(hctx, st.turn.Call, incident.ReasonVoiceHandedOver); err != nil {
		c.log.Error("ivr hand-over escalation failed; retrying", "channel", st.ch, "err", err)
		go c.retryHandOver(context.WithoutCancel(ctx), st.ch, st.turn.Call)
	}
	hcancel()
	c.present(ctx, st)
	octx, cancel := c.op(ctx)
	defer cancel()
	if err := c.ari.Continue(octx, st.ch, FallbackContext, "s", 1); errors.Is(err, ari.ErrNotInStasis) {
		// Another actor has already moved the call on (a second hand-over,
		// the dialplan) or it has ended: never hang up a caller put through.
		c.log.Warn("ivr call already left the application; not hung up", "channel", st.ch)
		st.ended = true
		return
	} else if err != nil {
		c.log.Error("ivr hand-over to the fallback failed; hanging up", "channel", st.ch, "err", err)
		c.hangup(ctx, st)
		return
	}
	c.log.Warn("ivr call handed to the coordinator fallback", "channel", st.ch)
	st.ended = true
}

// retryHandOver records a refused hand-over escalation, every 5 s for up to
// 10 minutes; its key (the call) keeps it to one event. A durable retry is
// not possible: the store that refused it is the only place it could go,
// and a database outage pages through P0 alerting.
func (c *Controller) retryHandOver(ctx context.Context, ch string, call intake.IVRCall) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	until := time.Now().Add(10 * time.Minute) // a local timer, not a recorded time
	for range t.C {
		octx, cancel := c.op(ctx)
		err := c.in.HandOver(octx, call, incident.ReasonVoiceHandedOver)
		cancel()
		if err == nil {
			return
		}
		if !time.Now().Before(until) {
			// IDs only: the runbook recovers it from the call ID.
			c.log.Error("ivr hand-over escalation lost", "call", ch, "incident", call.IncidentID, "err", err)
			if c.opt.OnEscalationLost != nil {
				c.opt.OnEscalationLost()
			}
			return
		}
	}
}

// present gives the fallback the reference to show the coordinator after
// "SAUTI". A failure is logged and the hand-over goes ahead (the coordinator
// then sees "SAUTI" alone): reaching a person comes first (hard constraint
// 14). The caller ID is the dialplan's to mask, never this request's.
func (c *Controller) present(ctx context.Context, st *call) {
	ref := st.turn.Call.Reference
	if ref == "" {
		return
	}
	octx, cancel := c.op(ctx)
	defer cancel()
	if err := c.ari.SetReference(octx, st.ch, ref.Spoken()); err != nil {
		c.log.Error("ivr hand-over reference not set", "channel", st.ch, "err", err)
	}
}

// silentHangup ends a call that has sent no event for a while. Its silence
// may mean another instance (whose newer stream took the application) or
// the dialplan has taken it: a call no longer in this application is left
// alone, never hung up on a coordinator (review R1). If Asterisk cannot be
// asked, the call is hung up as before.
func (c *Controller) silentHangup(ctx context.Context, st *call) {
	octx, cancel := c.op(ctx)
	chans, err := c.ari.ListChannels(octx)
	cancel()
	if err == nil && !slices.ContainsFunc(chans, func(ch ari.Channel) bool { return ch.ID == st.ch && ch.App == c.opt.App }) {
		c.log.Warn("ivr call already left the application; not hung up", "channel", st.ch)
		c.stopTimer(st)
		st.ended = true
		return
	}
	c.hangup(ctx, st)
}

func (c *Controller) hangup(ctx context.Context, st *call) {
	c.stopTimer(st)
	octx, cancel := c.op(ctx)
	defer cancel()
	if err := c.ari.Hangup(octx, st.ch); err != nil {
		c.log.Error("ivr hangup failed", "channel", st.ch, "err", err)
	}
	st.ended = true
}

func (c *Controller) stopTimer(st *call) {
	if st.timer != nil {
		st.timer.Stop()
		st.timer = nil
	}
	st.timerGen++
}

// startTimer arms the no-input timer; when it fires it queues an event to
// the call's own worker, never touching the call from the timer goroutine.
func (c *Controller) startTimer(st *call, after time.Duration) {
	c.stopTimer(st)
	gen, ch := st.timerGen, st.ch
	st.timer = time.AfterFunc(after, func() {
		c.enqueue(ch, item{ev: Event{Type: evNoInput, ChannelID: ch}, gen: gen})
	})
}
