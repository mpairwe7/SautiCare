package sim

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	"github.com/coder/websocket"
)

// FakeARI is an Asterisk REST Interface stand-in for tests: it serves the
// REST calls Sauti makes (answer, play, hang up), records them, and pushes
// events (StasisStart, ChannelDtmfReceived, PlaybackFinished, StasisEnd) over
// the /ari/events WebSocket. It answers 401 to wrong credentials and 404 to
// any endpoint Sauti must never use (recording, snooping, bridges).
type FakeARI struct {
	srv        *httptest.Server
	user, pass string
	mu         sync.Mutex
	requests   []string
	plays      map[string][][]string
	hungUp     map[string]bool
	conns      []*websocket.Conn
	connected  chan struct{}
}

// NewFakeARI starts the fake.
func NewFakeARI(user, pass string) *FakeARI {
	f := &FakeARI{user: user, pass: pass, plays: map[string][][]string{}, hungUp: map[string]bool{}, connected: make(chan struct{}, 16)}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	return f
}

// URL is the base URL (http://host:port).
func (f *FakeARI) URL() string { return f.srv.URL }

// Close stops the fake.
func (f *FakeARI) Close() {
	f.mu.Lock()
	for _, c := range f.conns {
		_ = c.CloseNow()
	}
	f.mu.Unlock()
	f.srv.Close()
}

// Connected returns a channel that receives once per accepted event stream.
func (f *FakeARI) Connected() <-chan struct{} { return f.connected }

func (f *FakeARI) serve(w http.ResponseWriter, r *http.Request) {
	u, p, ok := r.BasicAuth()
	if !ok || u != f.user || p != f.pass {
		http.Error(w, "unauthorised", http.StatusUnauthorized)
		return
	}
	f.mu.Lock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	f.mu.Unlock()
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/ari/events":
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		f.mu.Lock()
		f.conns = append(f.conns, c)
		f.mu.Unlock()
		f.connected <- struct{}{}
		_, _, _ = c.Read(r.Context()) // hold until the client goes away
	case r.Method == http.MethodPost && len(parts) == 4 && parts[1] == "channels" && parts[3] == "answer":
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPost && len(parts) == 5 && parts[1] == "channels" && parts[3] == "play":
		ch, pb := parts[2], parts[4]
		var media []string
		for _, m := range strings.Split(r.URL.Query().Get("media"), ",") {
			media = append(media, strings.TrimPrefix(m, "sound:"))
		}
		f.mu.Lock()
		f.plays[ch] = append(f.plays[ch], media)
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		f.Emit(map[string]any{"type": "PlaybackFinished", "playback": map[string]string{"id": pb, "target_uri": "channel:" + ch}})
	case r.Method == http.MethodDelete && len(parts) == 3 && parts[1] == "channels":
		f.mu.Lock()
		f.hungUp[parts[2]] = true
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
		f.Emit(map[string]any{"type": "StasisEnd", "channel": map[string]any{"id": parts[2]}})
	default:
		http.NotFound(w, r)
	}
}

// Emit sends one event to every connected client.
func (f *FakeARI) Emit(ev map[string]any) {
	b, _ := json.Marshal(ev)
	f.mu.Lock()
	conns := append([]*websocket.Conn(nil), f.conns...)
	f.mu.Unlock()
	for _, c := range conns {
		_ = c.Write(context.Background(), websocket.MessageText, b)
	}
}

// StartCall delivers a call into the Stasis application. An empty caller
// number is a withheld number.
func (f *FakeARI) StartCall(channelID, callerNumber string) {
	f.Emit(map[string]any{"type": "StasisStart", "args": []string{},
		"channel": map[string]any{"id": channelID, "state": "Ring", "caller": map[string]string{"number": callerNumber}}})
}

// Press sends DTMF digits, one event each.
func (f *FakeARI) Press(channelID, digits string) {
	for _, d := range digits {
		f.Emit(map[string]any{"type": "ChannelDtmfReceived", "digit": string(d), "channel": map[string]any{"id": channelID}})
	}
}

// Hangup simulates the caller hanging up.
func (f *FakeARI) Hangup(channelID string) {
	f.Emit(map[string]any{"type": "StasisEnd", "channel": map[string]any{"id": channelID}})
}

// DropStreams closes every event stream (the client must reconnect).
func (f *FakeARI) DropStreams() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.conns {
		_ = c.Close(websocket.StatusGoingAway, "restart")
	}
	f.conns = nil
}

// Requests returns every REST request ("METHOD /path") so far.
func (f *FakeARI) Requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

// Plays returns the sound lists played on a channel, in order.
func (f *FakeARI) Plays(channelID string) [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.plays[channelID]...)
}

// HungUp reports whether Sauti hung the channel up.
func (f *FakeARI) HungUp(channelID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hungUp[channelID]
}
