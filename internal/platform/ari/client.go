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
	"io"
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
	// EventChannelDestroyed: a channel ended, including a call Sauti placed
	// that was never answered (it never entered the application).
	EventChannelDestroyed = "ChannelDestroyed"
)

// RingbackTone is the ringing tone a caller hears while the other party's
// phone rings (the masked bridge). Play accepts it besides recordings.
const RingbackTone = "tone:ring"

// Event is one decoded ARI event. CallerNumber is the raw caller ID (an
// identifier: never log it).
type Event struct {
	Type         string
	ChannelID    string
	CallerNumber string
	Digit        string
	PlaybackID   string
	Args         []string // StasisStart: the application arguments of an originated call
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
	_, err := c.doBody(ctx, method, path, q)
	return err
}

func (c *Client) doBody(ctx context.Context, method, path string, q url.Values) ([]byte, error) {
	u := *c.base
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, method, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.cfg.User, c.cfg.Password)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ari: %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, fmt.Errorf("ari: %s %s: %w", method, path, err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("ari: %s %s: status %d", method, path, resp.StatusCode)
	}
	return body, nil
}

// Originate dials endpoint into this client's Stasis application with
// args, for an outbound alert call (M2 Part B); it returns the new
// channel's ID. When the callee answers, the channel's StasisStart carries
// args. timeout is how long it rings.
func (c *Client) Originate(ctx context.Context, endpoint, callerID string, args []string, timeout time.Duration) (string, error) {
	if endpoint == "" || len(endpoint) > 256 || strings.ContainsAny(endpoint, "\r\n&") {
		return "", errors.New("ari: invalid endpoint")
	}
	if err := ids(args...); err != nil {
		return "", err
	}
	q := url.Values{"endpoint": {endpoint}, "app": {c.cfg.App}, "appArgs": {strings.Join(args, ",")},
		"timeout": {strconv.Itoa(max(1, int(timeout.Seconds())))}}
	if callerID != "" {
		q.Set("callerId", callerID)
	}
	body, err := c.doBody(ctx, http.MethodPost, "/ari/channels", q)
	if err != nil {
		return "", err
	}
	var ch struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &ch); err != nil || !safeID.MatchString(ch.ID) {
		return "", errors.New("ari: originate returned no channel")
	}
	return ch.ID, nil
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
		switch {
		case s == RingbackTone:
			media[i] = s
		case strings.Contains(s, ":"):
			return fmt.Errorf("ari: only recordings and the ring tone are played, not %q", s)
		default:
			media[i] = "sound:" + s
		}
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

// SetCallerID sets the caller ID a channel presents. The masked bridge sets
// the calling party's to Sauti's number before it joins the bridge, so the
// other party is never sent the real number as a connected-line update
// (rule 5).
func (c *Client) SetCallerID(ctx context.Context, channelID, number string) error {
	if err := ids(channelID); err != nil {
		return err
	}
	if !callerIDNumber.MatchString(number) {
		return errors.New("ari: a caller ID is a phone number")
	}
	return c.do(ctx, http.MethodPost, "/ari/channels/"+channelID+"/variable", url.Values{"variable": {"CALLERID(all)"}, "value": {number}})
}

var callerIDNumber = regexp.MustCompile(`^\+?[0-9]{3,15}$`)

// CreateBridge creates a mixing bridge for the masked bridge (Build Prompt
// §10.5, ADR-0028): the two legs hear each other, and nothing is recorded —
// no recording option exists in this client.
func (c *Client) CreateBridge(ctx context.Context, bridgeID string) error {
	if err := ids(bridgeID); err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, "/ari/bridges/"+bridgeID, url.Values{"type": {"mixing"}})
}

// AddToBridge adds a channel to a bridge.
func (c *Client) AddToBridge(ctx context.Context, bridgeID, channelID string) error {
	if err := ids(bridgeID, channelID); err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, "/ari/bridges/"+bridgeID+"/addChannel", url.Values{"channel": {channelID}})
}

// DestroyBridge ends a bridge (its channels stay up until hung up).
func (c *Client) DestroyBridge(ctx context.Context, bridgeID string) error {
	if err := ids(bridgeID); err != nil {
		return err
	}
	return c.do(ctx, http.MethodDelete, "/ari/bridges/"+bridgeID, nil)
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
	Type    string   `json:"type"`
	Digit   string   `json:"digit"`
	Args    []string `json:"args"`
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
	e := Event{Type: r.Type, Digit: r.Digit, Args: r.Args}
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
