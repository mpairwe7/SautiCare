// Package sim holds simulators that drive the real adapter interfaces
// (Build Prompt §16), so scenarios run end to end without a telco. The USSD
// simulator speaks the Africa's Talking callback contract: form-encoded
// sessionId, serviceCode, phoneNumber, text ('*'-joined input so far) and
// networkCode, answered by "CON …" or "END …".
package sim

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"sautihealth.com/sauti/internal/platform/egress"
)

// Screen is a parsed USSD reply.
type Screen struct {
	Text string
	End  bool
}

// USSD simulates one aggregator route.
type USSD struct {
	endpoint, serviceCode, network string
	client                         *http.Client
}

// NewUSSD targets baseURL + "/v1/ussd/at/" + token. Its HTTP client goes
// through platform/egress and allows only baseURL's host (an unparseable
// baseURL allows none), so a redirect elsewhere is refused.
func NewUSSD(baseURL, token, serviceCode, network string) *USSD {
	var allow []string
	if u, err := url.Parse(baseURL); err == nil && u.Hostname() != "" {
		allow = []string{u.Hostname()}
	}
	return &USSD{
		endpoint:    strings.TrimRight(baseURL, "/") + "/v1/ussd/at/" + token,
		serviceCode: serviceCode, network: network,
		client: egress.NewClient(allow, 10*time.Second),
	}
}

// USSDSession is one dialled session.
type USSDSession struct {
	u      *USSD
	id     string
	phone  string
	inputs []string
	last   url.Values
}

// Dial starts a session from phone (E.164).
func (u *USSD) Dial(phone string) *USSDSession {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return &USSDSession{u: u, id: "ATUid_sim_" + hex.EncodeToString(b), phone: phone}
}

// ID returns the provider session ID.
func (s *USSDSession) ID() string { return s.id }

// Send submits input; the empty string on a fresh session is the first
// request. Later inputs accumulate as Africa's Talking does: "1", "1*2", …
func (s *USSDSession) Send(input string) (Screen, error) {
	if input != "" || len(s.inputs) > 0 {
		s.inputs = append(s.inputs, input)
	}
	s.last = url.Values{
		"sessionId":   {s.id},
		"serviceCode": {s.u.serviceCode},
		"phoneNumber": {s.phone},
		"text":        {strings.Join(s.inputs, "*")},
		"networkCode": {s.u.network},
	}
	return s.post(s.last)
}

// Retry re-sends the previous request byte for byte, as an aggregator
// retry would.
func (s *USSDSession) Retry() (Screen, error) {
	if s.last == nil {
		return Screen{}, fmt.Errorf("sim: nothing to retry")
	}
	return s.post(s.last)
}

func (s *USSDSession) post(form url.Values) (Screen, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, s.u.endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return Screen{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.u.client.Do(req)
	if err != nil {
		return Screen{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return Screen{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return Screen{}, fmt.Errorf("sim: status %d", resp.StatusCode)
	}
	switch b := string(body); {
	case strings.HasPrefix(b, "CON "):
		return Screen{Text: b[4:]}, nil
	case strings.HasPrefix(b, "END "):
		return Screen{Text: b[4:], End: true}, nil
	default:
		return Screen{}, fmt.Errorf("sim: reply is not a USSD screen")
	}
}
