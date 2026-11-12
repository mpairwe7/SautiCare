// Package ivr is the voice channel adapter (Build Prompt §10.2): it turns
// Asterisk ARI events into intake.Service calls and plays recorded prompts.
// Answers come from the keypad only; nothing is recorded; a caller's number
// is never logged.
package ivr

import (
	"context"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"sautihealth.com/sauti/internal/intake"
	"sautihealth.com/sauti/internal/pii"
	"sautihealth.com/sauti/internal/platform/ari"
)

// Event types re-exported from the ARI client.
type Event = ari.Event

const (
	EventStasisStart      = ari.EventStasisStart
	EventDTMF             = ari.EventDTMF
	EventPlaybackFinished = ari.EventPlaybackFinished
	EventStasisEnd        = ari.EventStasisEnd
	EventHangupRequest    = ari.EventHangupRequest
)

// ARI is the subset of the Asterisk client the controller uses.
type ARI interface {
	Answer(ctx context.Context, channelID string) error
	Play(ctx context.Context, channelID, playbackID string, sounds []string) error
	Hangup(ctx context.Context, channelID string) error
}

// Intake is the voice side of intake.Service.
type Intake interface {
	StartIVR(ctx context.Context, callID string, phone pii.Phone) (intake.IVRTurn, error)
	AnswerIVR(ctx context.Context, call intake.IVRCall, input string) (intake.IVRTurn, error)
}

// Options tune the call behaviour.
type Options struct {
	// NoInput is how long silence lasts before the prompt is played again;
	// after MaxReprompts the call ends (resume brings the caller back).
	NoInput      time.Duration
	MaxReprompts int
}

// maxCallbackDigits bounds a typed callback number (E.164 is at most 15).
const maxCallbackDigits = 16

type call struct {
	turn      intake.IVRTurn
	digits    string
	seq       int
	hangupOn  string // playback after which to hang up
	reprompts int
	timer     *time.Timer
}

// Controller runs voice calls.
type Controller struct {
	ari   ARI
	in    Intake
	log   *slog.Logger
	opt   Options
	mu    sync.Mutex
	calls map[string]*call
}

// New builds a controller.
func New(a ARI, in Intake, log *slog.Logger, opt Options) *Controller {
	if opt.NoInput <= 0 {
		opt.NoInput = 15 * time.Second
	}
	if opt.MaxReprompts < 0 {
		opt.MaxReprompts = 0
	}
	return &Controller{ari: a, in: in, log: log, opt: opt, calls: map[string]*call{}}
}

// Run streams ARI events into the controller until ctx ends.
func (c *Controller) Run(ctx context.Context, client *ari.Client) error {
	return client.Events(ctx, func(e ari.Event) { c.Handle(ctx, e) })
}

// Handle processes one event. Events arrive in order from one stream.
func (c *Controller) Handle(ctx context.Context, e Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch e.Type {
	case EventStasisStart:
		c.start(ctx, e)
	case EventDTMF:
		if st := c.calls[e.ChannelID]; st != nil && !st.turn.Prompt.End {
			c.dtmf(ctx, e.ChannelID, st, e.Digit)
		}
	case EventPlaybackFinished:
		if st := c.calls[e.ChannelID]; st != nil && st.hangupOn != "" && st.hangupOn == e.PlaybackID {
			c.hangup(ctx, e.ChannelID, st)
		}
	case EventStasisEnd, EventHangupRequest:
		if st := c.calls[e.ChannelID]; st != nil {
			c.stopTimer(st)
			delete(c.calls, e.ChannelID)
		}
	}
}

func (c *Controller) start(ctx context.Context, e Event) {
	ch := e.ChannelID
	if err := c.ari.Answer(ctx, ch); err != nil {
		c.log.Error("ivr answer failed", "channel", ch, "err", err)
		return
	}
	// A withheld or unparseable number is unknown: the caller is asked for a
	// callback number and dispatch proceeds on location (Build Prompt §10.2).
	phone, err := pii.ParsePhone(e.CallerNumber)
	if err != nil {
		phone = pii.Phone{}
	}
	st := &call{}
	c.calls[ch] = st
	turn, err := c.in.StartIVR(ctx, ch, phone)
	if err != nil {
		c.fail(ctx, ch, st, "", err)
		return
	}
	st.turn = turn
	c.play(ctx, ch, st, turn.Prompt)
}

func (c *Controller) dtmf(ctx context.Context, ch string, st *call, digit string) {
	if st.turn.Call.Shown == intake.KindCallback {
		switch {
		case digit == "#":
			input := st.digits
			st.digits = ""
			c.answer(ctx, ch, st, input)
		case st.digits == "" && digit == "1" && st.turn.Call.HasPhone:
			c.answer(ctx, ch, st, "1")
		case len(st.digits) < maxCallbackDigits:
			st.digits += digit
			c.resetTimer(ctx, ch, st)
		}
		return
	}
	c.answer(ctx, ch, st, digit)
}

func (c *Controller) answer(ctx context.Context, ch string, st *call, input string) {
	turn, err := c.in.AnswerIVR(ctx, st.turn.Call, input)
	if err != nil {
		c.fail(ctx, ch, st, st.turn.Prompt.Lang, err)
		return
	}
	st.turn, st.reprompts = turn, 0
	c.play(ctx, ch, st, turn.Prompt)
}

func (c *Controller) play(ctx context.Context, ch string, st *call, pr intake.Prompt) {
	c.playSounds(ctx, ch, st, Sounds(pr), pr.End)
}

func (c *Controller) playSounds(ctx context.Context, ch string, st *call, sounds []string, end bool) {
	st.seq++
	pb := ch + "-" + strconv.Itoa(st.seq)
	if end {
		st.hangupOn = pb
		c.stopTimer(st)
	}
	if err := c.ari.Play(ctx, ch, pb, sounds); err != nil {
		c.log.Error("ivr play failed", "channel", ch, "err", err)
	}
	if !end {
		c.resetTimer(ctx, ch, st)
	}
}

// fail plays the error message and ends the call; the incident and every
// answer given so far are already persisted, and the caller can redial.
func (c *Controller) fail(ctx context.Context, ch string, st *call, lang string, err error) {
	c.log.Error("ivr intake failed", "channel", ch, "err", err)
	c.playSounds(ctx, ch, st, InternalError(lang), true)
}

func (c *Controller) hangup(ctx context.Context, ch string, st *call) {
	c.stopTimer(st)
	if err := c.ari.Hangup(ctx, ch); err != nil {
		c.log.Error("ivr hangup failed", "channel", ch, "err", err)
	}
	delete(c.calls, ch)
}

func (c *Controller) stopTimer(st *call) {
	if st.timer != nil {
		st.timer.Stop()
		st.timer = nil
	}
}

func (c *Controller) resetTimer(ctx context.Context, ch string, st *call) {
	c.stopTimer(st)
	st.timer = time.AfterFunc(c.opt.NoInput, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.calls[ch] != st || st.turn.Prompt.End {
			return
		}
		if st.reprompts >= c.opt.MaxReprompts {
			c.hangup(context.WithoutCancel(ctx), ch, st)
			return
		}
		st.reprompts++
		st.digits = ""
		c.playSounds(context.WithoutCancel(ctx), ch, st, Sounds(st.turn.Prompt), false)
	})
}
