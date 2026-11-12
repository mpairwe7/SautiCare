// Package ari is Sauti's minimal Asterisk REST Interface client (ADR-0023):
// answer a channel, play and stop prompts, hand a call back to the dialplan,
// hang up, and receive the Stasis event stream. It deliberately has no recording, snooping or bridging call: the
// IVR records nothing (Build Prompt §10.2). REST goes through the egress
// allowlist (the configured Asterisk host only); events arrive over one
// WebSocket that reconnects with backoff.
package ari

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"sautihealth.com/sauti/internal/platform/egress"
)

// Event types Sauti handles.
const (
	EventStasisStart      = "StasisStart"
	EventDTMF             = "ChannelDtmfReceived"
	EventPlaybackStarted  = "PlaybackStarted"
	EventPlaybackFinished = "PlaybackFinished"
	EventStasisEnd        = "StasisEnd"
	EventHangupRequest    = "ChannelHangupRequest"
)

// Event is one decoded ARI event. CallerNumber is the raw caller ID (an
// identifier: never log it).
type Event struct {
	Type         string
	ChannelID    string
	CallerNumber string
	Digit        string
	PlaybackID   string
}

// Config configures the client.
type Config struct {
	BaseURL  string // e.g. http://127.0.0.1:8088 (Asterisk's HTTP server, never public)
	User     string
	Password string
	App      string // Stasis application name
	Timeout  time.Duration
}

// Client talks to one Asterisk.
type Client struct {
	cfg       Config
	base      *url.URL
	http      *http.Client
	backoff   time.Duration
	connected atomic.Bool
}

// IDs from Asterisk are used in URL paths: allow only their shape.
var safeID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// New validates cfg and builds a client.
func New(cfg Config) (*Client, error) {
	u, err := url.Parse(cfg.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("ari: base URL %q must be http(s)://host[:port]", cfg.BaseURL)
	}
	if cfg.User == "" || cfg.Password == "" || cfg.App == "" {
		return nil, errors.New("ari: user, password and app are required")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Second
	}
	return &Client{cfg: cfg, base: u, http: egress.NewClient([]string{u.Hostname()}, cfg.Timeout), backoff: time.Second}, nil
}

func (c *Client) do(ctx context.Context, method, path string, q url.Values) error {
	u := *c.base
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, method, u.String(), nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(c.cfg.User, c.cfg.Password)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("ari: %s %s: %w", method, path, err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("ari: %s %s: status %d", method, path, resp.StatusCode)
	}
	return nil
}

func ids(xs ...string) error {
	for _, x := range xs {
		if !safeID.MatchString(x) {
			return fmt.Errorf("ari: invalid identifier %q", x)
		}
	}
	return nil
}

// Answer answers a ringing channel.
func (c *Client) Answer(ctx context.Context, channelID string) error {
	if err := ids(channelID); err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, "/ari/channels/"+channelID+"/answer", nil)
}

// Play plays the sounds in order (one playback); the caller may press keys
// during it (barge-in).
func (c *Client) Play(ctx context.Context, channelID, playbackID string, sounds []string) error {
	if err := ids(channelID, playbackID); err != nil {
		return err
	}
	if len(sounds) == 0 {
		return errors.New("ari: nothing to play")
	}
	media := make([]string, len(sounds))
	for i, s := range sounds {
		media[i] = "sound:" + s
	}
	return c.do(ctx, http.MethodPost, "/ari/channels/"+channelID+"/play/"+playbackID, url.Values{"media": {strings.Join(media, ",")}})
}

// StopPlayback stops a prompt that is playing (the caller pressed a key:
// barge-in), or removes it from the channel's queue if it has not started.
// Asterisk then sends PlaybackFinished for it.
func (c *Client) StopPlayback(ctx context.Context, playbackID string) error {
	if err := ids(playbackID); err != nil {
		return err
	}
	return c.do(ctx, http.MethodDelete, "/ari/playbacks/"+playbackID, nil)
}

// Continue hands the channel back to the dialplan at context, extension,
// priority: it leaves the Stasis application (StasisEnd follows). Sauti uses
// it to pass a call it cannot serve to the fallback that rings a
// coordinator (hard constraint 14).
func (c *Client) Continue(ctx context.Context, channelID, dialContext, extension string, priority int) error {
	if err := ids(channelID, dialContext, extension); err != nil {
		return err
	}
	if priority < 1 {
		return fmt.Errorf("ari: priority %d", priority)
	}
	return c.do(ctx, http.MethodPost, "/ari/channels/"+channelID+"/continue",
		url.Values{"context": {dialContext}, "extension": {extension}, "priority": {strconv.Itoa(priority)}})
}

// EventsConnected reports whether the event stream is up: without it no
// call can be served (ivr-controller readiness).
func (c *Client) EventsConnected() bool { return c.connected.Load() }

// Hangup ends the channel.
func (c *Client) Hangup(ctx context.Context, channelID string) error {
	if err := ids(channelID); err != nil {
		return err
	}
	return c.do(ctx, http.MethodDelete, "/ari/channels/"+channelID, nil)
}

type rawEvent struct {
	Type    string `json:"type"`
	Digit   string `json:"digit"`
	Channel *struct {
		ID     string `json:"id"`
		Caller struct {
			Number string `json:"number"`
		} `json:"caller"`
	} `json:"channel"`
	Playback *struct {
		ID        string `json:"id"`
		TargetURI string `json:"target_uri"`
	} `json:"playback"`
}

func decodeEvent(b []byte) (Event, error) {
	var r rawEvent
	if err := json.Unmarshal(b, &r); err != nil {
		return Event{}, err
	}
	e := Event{Type: r.Type, Digit: r.Digit}
	if r.Channel != nil {
		e.ChannelID, e.CallerNumber = r.Channel.ID, r.Channel.Caller.Number
	}
	if r.Playback != nil {
		e.PlaybackID = r.Playback.ID
		if e.ChannelID == "" {
			e.ChannelID = strings.TrimPrefix(r.Playback.TargetURI, "channel:")
		}
	}
	return e, nil
}

// Events streams events to handle until ctx ends, reconnecting with
// exponential backoff (up to 30 s) when the stream drops.
func (c *Client) Events(ctx context.Context, handle func(Event)) error {
	u := *c.base
	u.Scheme = map[string]string{"http": "ws", "https": "wss"}[c.base.Scheme]
	u.Path = strings.TrimRight(u.Path, "/") + "/ari/events"
	u.RawQuery = url.Values{"app": {c.cfg.App}}.Encode()
	hdr := http.Header{}
	hdr.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(c.cfg.User+":"+c.cfg.Password)))
	wait := c.backoff
	for {
		conn, resp, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{HTTPClient: c.http, HTTPHeader: hdr})
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		if err == nil {
			wait = c.backoff
			c.connected.Store(true)
			for {
				_, msg, rerr := conn.Read(ctx)
				if rerr != nil {
					break
				}
				if e, derr := decodeEvent(msg); derr == nil && e.Type != "" {
					handle(e)
				}
			}
			c.connected.Store(false)
			_ = conn.CloseNow()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		wait = min(wait*2, 30*time.Second)
	}
}
