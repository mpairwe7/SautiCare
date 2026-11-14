package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"sautihealth.com/sauti/internal/pii"
)

// smsSink fakes Africa's Talking's messaging API (POST /version1/messaging,
// the contract internal/platform/africastalking/sms.go speaks) and keeps
// every message for the phones on screen. When report is set, each message
// is followed by a "Success" delivery report, as the provider would send.
type smsSink struct {
	apiKey string
	report func(messageID string)

	mu   sync.Mutex
	seq  int
	msgs []sms
}

type sms struct {
	to   string    // E.164; never leaves the demo process
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

// maxMessages bounds the sink's memory on a public demo.
const maxMessages = 2000

func (s *smsSink) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/version1/messaging" {
		http.NotFound(w, r)
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("apiKey")), []byte(s.apiKey)) != 1 {
		http.Error(w, "The supplied authentication is invalid", http.StatusUnauthorized)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	to, err := pii.ParsePhone(r.PostForm.Get("to"))
	text := r.PostForm.Get("message")
	if err != nil || text == "" {
		http.Error(w, "bad message", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.seq++
	id := fmt.Sprintf("ATXid_demo_%d", s.seq)
	s.msgs = append(s.msgs, sms{to: to.E164(), Text: text, At: time.Now().UTC()})
	if len(s.msgs) > maxMessages {
		s.msgs = s.msgs[len(s.msgs)-maxMessages:]
	}
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"SMSMessageData": map[string]any{
		"Message": "Sent to 1/1 Total Cost: UGX 0.0000",
		"Recipients": []map[string]any{{"statusCode": 101, "status": "Success", "cost": "UGX 0.0000",
			"messageId": id}},
	}})
	if s.report != nil {
		go s.report(id)
	}
}

// inbox returns the messages sent to an E.164 number, oldest first.
func (s *smsSink) inbox(e164 string) []sms {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []sms{}
	for _, m := range s.msgs {
		if m.to == e164 {
			out = append(out, m)
		}
	}
	return out
}

// deliveryReporter posts a "Success" delivery report for a message to
// intake-gw's delivery-report callback a moment after it is sent (after
// dispatch-worker has stored the message ID), until ctx ends.
func deliveryReporter(ctx context.Context, client *http.Client, callback string) func(string) {
	return func(id string) {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
		form := url.Values{"id": {id}, "status": {"Success"}}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, callback, strings.NewReader(form.Encode()))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if resp, err := client.Do(req); err == nil {
			_ = resp.Body.Close()
		}
	}
}
