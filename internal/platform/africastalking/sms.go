package africastalking

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"sautihealth.com/sauti/internal/pii"
)

// SMS through Africa's Talking's messaging API (POST /version1/messaging,
// form username, to, message, from; API key in the apiKey header). The
// response's per-recipient statusCode says whether Africa's Talking took
// the message: 100 Processed, 101 Sent (handed to the operator), 102
// Queued. Delivery to the handset comes later as a delivery report.

// ErrSMSRejected reports a message the provider refused: nothing was sent
// (invalid number, blacklisted, insufficient balance, rejected by the
// operator gateway, bad credentials). Any other error is an unknown outcome.
var ErrSMSRejected = errors.New("africastalking: SMS rejected by the provider")

// SMSConfig configures the SMS client.
type SMSConfig struct {
	BaseURL  string       // https://api.africastalking.com (sandbox: https://api.sandbox.africastalking.com)
	Username string       // the Africa's Talking application username
	APIKey   string       // secret: OpenBao in production
	SenderID string       // optional alphanumeric sender ID (≤ 11 characters)
	HTTP     *http.Client // the egress-guarded client
}

// SMSClient sends one SMS at a time.
type SMSClient struct {
	endpoint string
	c        SMSConfig
}

var senderID = regexp.MustCompile(`^[A-Za-z0-9]{1,11}$`)

// NewSMSClient validates the configuration.
func NewSMSClient(c SMSConfig) (*SMSClient, error) {
	u, err := url.Parse(c.BaseURL)
	switch {
	case err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "":
		return nil, errors.New("africastalking: SMS base URL must be an http(s) URL")
	case c.Username == "" || c.APIKey == "":
		return nil, errors.New("africastalking: SMS needs a username and an API key")
	case c.HTTP == nil:
		return nil, errors.New("africastalking: SMS needs the egress-guarded HTTP client")
	case c.SenderID != "" && !senderID.MatchString(c.SenderID):
		return nil, errors.New("africastalking: sender ID must be 1-11 letters or digits")
	}
	return &SMSClient{endpoint: strings.TrimRight(c.BaseURL, "/") + "/version1/messaging", c: c}, nil
}

type smsResponse struct {
	SMSMessageData struct {
		Recipients []struct {
			StatusCode int    `json:"statusCode"`
			Status     string `json:"status"`
			MessageID  string `json:"messageId"`
		} `json:"Recipients"`
	} `json:"SMSMessageData"`
}

// Send sends text to one number and returns the provider's message ID,
// which delivery reports carry. Errors never contain the number or key.
func (s *SMSClient) Send(ctx context.Context, to pii.Phone, text string) (string, error) {
	if to.IsZero() {
		return "", fmt.Errorf("%w: no number", ErrSMSRejected)
	}
	form := url.Values{"username": {s.c.Username}, "to": {to.E164()}, "message": {text}}
	if s.c.SenderID != "" {
		form.Set("from", s.c.SenderID)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", errors.New("africastalking: SMS request could not be built")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("apiKey", s.c.APIKey)
	resp, err := s.c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("africastalking: SMS outcome unknown: %w", redactURL(err))
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return "", errors.New("africastalking: SMS outcome unknown: reading the response failed")
	}
	switch {
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		return "", fmt.Errorf("%w: HTTP %d", ErrSMSRejected, resp.StatusCode)
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return "", fmt.Errorf("africastalking: SMS outcome unknown: HTTP %d", resp.StatusCode)
	}
	var r smsResponse
	if err := json.Unmarshal(body, &r); err != nil || len(r.SMSMessageData.Recipients) != 1 {
		return "", errors.New("africastalking: SMS outcome unknown: unexpected response")
	}
	rc := r.SMSMessageData.Recipients[0]
	switch {
	case rc.StatusCode >= 100 && rc.StatusCode <= 102 && rc.MessageID != "" && rc.MessageID != "None":
		return rc.MessageID, nil
	case (rc.StatusCode >= 401 && rc.StatusCode <= 409) || rc.StatusCode == 502:
		return "", fmt.Errorf("%w: status %d %s", ErrSMSRejected, rc.StatusCode, codeOnly(rc.Status))
	default:
		return "", fmt.Errorf("africastalking: SMS outcome unknown: status %d %s", rc.StatusCode, codeOnly(rc.Status))
	}
}

var statusWord = regexp.MustCompile(`^[A-Za-z]{1,40}$`)

// codeOnly keeps a provider status word, nothing else (it reaches logs).
func codeOnly(s string) string {
	if statusWord.MatchString(s) {
		return s
	}
	return "?"
}

// redactURL drops the request URL from a transport error (it carries no
// secret today, but the form never reaches it and neither must anything else).
func redactURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s: %w", ue.Op, ue.Err)
	}
	return err
}
