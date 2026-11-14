package sim

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// FakeARI is an Asterisk REST Interface stand-in for tests: it serves the
// REST calls Sauti makes (answer, play, stop a playback, continue in the
// dialplan, hang up, read a channel: 404 once it has ended), records them,
// and pushes events (StasisStart,
// ChannelDtmfReceived, PlaybackStarted, PlaybackFinished, StasisEnd) over the
// /ari/events WebSocket. It answers 401 to wrong credentials and 404 to any
// endpoint Sauti must never use (recording, snooping, bridges).
//
// Playbacks follow Asterisk: each channel plays one prompt at a time and
// queues the rest. A prompt sends PlaybackStarted when it starts and
// PlaybackFinished when it ends: at once by default, after the duration set
// with SetPlaybackDuration, when stopped (DELETE /ari/playbacks/{id}), or
// when the test calls FinishPlayback.
type FakeARI struct {
	originated []url.Values
	srv        *httptest.Server
	user, pass string
	mu         sync.Mutex
	requests   []string
	plays      map[string][][]string
	hungUp     map[string]bool
	continued  map[string]string
	conns      []*websocket.Conn
	connected  chan struct{}
	duration   time.Duration
	active     map[string]string   // channel → playback playing
	queued     map[string][]string // channel → playbacks waiting
	playbackCh map[string]string   // playback → channel (until it finishes)
	bridges    map[string][]string // bridge → channels added
	bridgeQ    map[string]url.Values
	destroyed  map[string]bool
	callerIDs  map[string]string
	refs       map[string]string // channel → SAUTI_REF
	live       map[string]bool   // channels that exist: placed or started, not yet ended
	apps       map[string]string // channel → the Stasis application it is parked in
}

// NewFakeARI starts the fake.
func NewFakeARI(user, pass string) *FakeARI {
	f := &FakeARI{user: user, pass: pass, plays: map[string][][]string{}, hungUp: map[string]bool{}, continued: map[string]string{},
		connected: make(chan struct{}, 16), active: map[string]string{}, queued: map[string][]string{}, playbackCh: map[string]string{},
		bridges: map[string][]string{}, bridgeQ: map[string]url.Values{}, destroyed: map[string]bool{}, callerIDs: map[string]string{}, refs: map[string]string{}, live: map[string]bool{}, apps: map[string]string{}}
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

// SetPlaybackDuration sets how long each prompt plays: 0 (the default)
// finishes it as soon as it starts, a positive duration finishes it after
// that long, and a negative one never (until stopped or FinishPlayback).
func (f *FakeARI) SetPlaybackDuration(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.duration = d
}

type event = map[string]any

func playbackEvent(typ, pb, ch string) event {
	return event{"type": typ, "playback": map[string]string{"id": pb, "target_uri": "channel:" + ch}}
}

// startLocked starts the channel's next queued prompt if none is playing
// and returns the events to send, in order.
func (f *FakeARI) startLocked(ch string) []event {
	if f.active[ch] != "" || len(f.queued[ch]) == 0 {
		return nil
	}
	pb := f.queued[ch][0]
	f.queued[ch] = f.queued[ch][1:]
	f.active[ch] = pb
	evs := []event{playbackEvent("PlaybackStarted", pb, ch)}
	switch d := f.duration; {
	case d == 0:
		evs = append(evs, f.finishLocked(pb)...)
	case d > 0:
		time.AfterFunc(d, func() { f.FinishPlayback(pb) })
	}
	return evs
}

// finishLocked ends a playing or queued prompt and returns the events to
// send (none if it is unknown or already finished).
func (f *FakeARI) finishLocked(pb string) []event {
	ch, ok := f.playbackCh[pb]
	if !ok {
		return nil
	}
	delete(f.playbackCh, pb)
	if f.active[ch] == pb {
		f.active[ch] = ""
	} else {
		f.queued[ch] = slices.DeleteFunc(f.queued[ch], func(q string) bool { return q == pb })
	}
	return append([]event{playbackEvent("PlaybackFinished", pb, ch)}, f.startLocked(ch)...)
}

func (f *FakeARI) emitAll(evs []event) {
	for _, e := range evs {
		f.Emit(e)
	}
}

// FinishPlayback ends a prompt as if it had played to the end.
func (f *FakeARI) FinishPlayback(playbackID string) {
	f.mu.Lock()
	evs := f.finishLocked(playbackID)
	f.mu.Unlock()
	f.emitAll(evs)
}

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
	case r.Method == http.MethodPost && len(parts) == 2 && parts[1] == "channels":
		f.mu.Lock()
		f.originated = append(f.originated, r.URL.Query())
		id := fmt.Sprintf("out-%d", len(f.originated))
		f.live[id], f.apps[id] = true, r.URL.Query().Get("app")
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":%q}`, id)
		return
	case r.Method == http.MethodGet && len(parts) == 2 && parts[1] == "channels":
		type dialplan struct {
			AppName string `json:"app_name"`
			AppData string `json:"app_data"`
		}
		type channel struct {
			ID       string   `json:"id"`
			Dialplan dialplan `json:"dialplan"`
		}
		f.mu.Lock()
		list := []channel{}
		for id := range f.live {
			ch := channel{ID: id}
			if app := f.apps[id]; app != "" {
				ch.Dialplan = dialplan{AppName: "Stasis", AppData: app}
			}
			list = append(list, ch)
		}
		f.mu.Unlock()
		slices.SortFunc(list, func(a, b channel) int { return strings.Compare(a.ID, b.ID) })
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(list)
	case r.Method == http.MethodGet && len(parts) == 3 && parts[1] == "channels":
		f.mu.Lock()
		live := f.live[parts[2]]
		f.mu.Unlock()
		if !live {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"state":"Up"}`)) // Sauti reads only the status
	case r.Method == http.MethodPost && len(parts) == 4 && parts[1] == "channels" && parts[3] == "answer":
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPost && len(parts) == 4 && parts[1] == "channels" && parts[3] == "continue":
		q := r.URL.Query()
		f.mu.Lock()
		f.continued[parts[2]] = q.Get("context") + "," + q.Get("extension") + "," + q.Get("priority")
		delete(f.apps, parts[2]) // still live, now in the dialplan, not the application
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
		f.Emit(event{"type": "StasisEnd", "channel": map[string]any{"id": parts[2]}})
	case r.Method == http.MethodPost && len(parts) == 5 && parts[1] == "channels" && parts[3] == "play":
		ch, pb := parts[2], parts[4]
		var media []string
		for _, m := range strings.Split(r.URL.Query().Get("media"), ",") {
			media = append(media, strings.TrimPrefix(m, "sound:"))
		}
		f.mu.Lock()
		f.playbackCh[pb] = ch
		f.queued[ch] = append(f.queued[ch], pb)
		evs := f.startLocked(ch)
		f.mu.Unlock()
		// Events first, then the record a test waits on: by the time a test
		// sees the play, its PlaybackStarted is already on the stream.
		f.emitAll(evs)
		f.mu.Lock()
		f.plays[ch] = append(f.plays[ch], media)
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	case r.Method == http.MethodDelete && len(parts) == 3 && parts[1] == "playbacks":
		f.mu.Lock()
		evs := f.finishLocked(parts[2])
		f.mu.Unlock()
		if evs == nil {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		f.emitAll(evs)
	case r.Method == http.MethodPost && len(parts) == 4 && parts[1] == "channels" && parts[3] == "variable":
		switch r.URL.Query().Get("variable") {
		case "CALLERID(all)":
			f.mu.Lock()
			f.callerIDs[parts[2]] = r.URL.Query().Get("value")
			f.mu.Unlock()
		case "SAUTI_REF":
			f.mu.Lock()
			f.refs[parts[2]] = r.URL.Query().Get("value")
			f.mu.Unlock()
		}
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPost && len(parts) == 3 && parts[1] == "bridges":
		f.mu.Lock()
		f.bridges[parts[2]] = []string{}
		f.bridgeQ[parts[2]] = r.URL.Query()
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodPost && len(parts) == 4 && parts[1] == "bridges" && parts[3] == "addChannel":
		f.mu.Lock()
		_, ok := f.bridges[parts[2]]
		if ok {
			f.bridges[parts[2]] = append(f.bridges[parts[2]], r.URL.Query().Get("channel"))
		}
		f.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodDelete && len(parts) == 3 && parts[1] == "bridges":
		f.mu.Lock()
		f.destroyed[parts[2]] = true
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodDelete && len(parts) == 3 && parts[1] == "channels":
		f.mu.Lock()
		f.hungUp[parts[2]] = true
		delete(f.live, parts[2])
		for pb, ch := range f.playbackCh {
			if ch == parts[2] {
				delete(f.playbackCh, pb)
			}
		}
		delete(f.active, parts[2])
		delete(f.queued, parts[2])
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
		f.Emit(event{"type": "StasisEnd", "channel": map[string]any{"id": parts[2]}})
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

// CallIn is a call that entered the application with arguments (the
// dialplan's Stasis(app,args...)), from callerNumber.
func (f *FakeARI) CallIn(channelID, callerNumber string, args ...string) {
	f.Emit(map[string]any{"type": "StasisStart", "args": args,
		"channel": map[string]any{"id": channelID, "state": "Ring", "caller": map[string]string{"number": callerNumber}}})
}

// StartCall delivers a call into the IVR's Stasis application (sauti-ivr).
// An empty caller number is a withheld number.
func (f *FakeARI) StartCall(channelID, callerNumber string) {
	f.setLive(channelID, true)
	f.mu.Lock()
	f.apps[channelID] = "sauti-ivr"
	f.mu.Unlock()
	f.Emit(event{"type": "StasisStart", "args": []string{},
		"channel": map[string]any{"id": channelID, "state": "Ring", "caller": map[string]string{"number": callerNumber}}})
}

// Press sends DTMF digits, one event each.
func (f *FakeARI) Press(channelID, digits string) {
	for _, d := range digits {
		f.Emit(event{"type": "ChannelDtmfReceived", "digit": string(d), "channel": map[string]any{"id": channelID}})
	}
}

// Hangup simulates the far end hanging up: the channel ends, and its
// StasisEnd goes to the streams connected now (none: the event is lost).
func (f *FakeARI) Hangup(channelID string) {
	f.setLive(channelID, false)
	f.Emit(event{"type": "StasisEnd", "channel": map[string]any{"id": channelID}})
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

// Playing returns the playback a channel is playing ("" if none).
func (f *FakeARI) Playing(channelID string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.active[channelID]
}

// CallerID returns the caller ID Sauti set on a channel ("" if none).
func (f *FakeARI) CallerID(channelID string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.callerIDs[channelID]
}

// Reference returns the SAUTI_REF Sauti set on a channel ("" if none).
func (f *FakeARI) Reference(channelID string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.refs[channelID]
}

// Bridged returns the channels added to a bridge, in order.
func (f *FakeARI) Bridged(bridgeID string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.bridges[bridgeID])
}

// BridgeQuery returns the query a bridge was created with.
func (f *FakeARI) BridgeQuery(bridgeID string) url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bridgeQ[bridgeID]
}

// BridgeDestroyed reports whether Sauti destroyed the bridge.
func (f *FakeARI) BridgeDestroyed(bridgeID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.destroyed[bridgeID]
}

// HungUp reports whether Sauti hung the channel up.
func (f *FakeARI) HungUp(channelID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hungUp[channelID]
}

// Continued returns where Sauti sent the channel in the dialplan
// ("context,extension,priority"), or "".
func (f *FakeARI) Continued(channelID string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.continued[channelID]
}

// Originated returns the originate requests' query parameters, in order.
func (f *FakeARI) Originated() []url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.originated)
}

// Answered emits the StasisStart of an originated call the callee answered,
// with the application arguments it was originated with.
func (f *FakeARI) Answered(channelID string, args ...string) {
	f.setLive(channelID, true)
	f.Emit(map[string]any{"type": "StasisStart", "args": args, "channel": map[string]any{"id": channelID}})
}

func (f *FakeARI) setLive(channelID string, live bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if live {
		f.live[channelID] = true
	} else {
		delete(f.live, channelID)
	}
}
