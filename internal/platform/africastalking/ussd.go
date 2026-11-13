// Package africastalking adapts Africa's Talking USSD callbacks (transport
// only, Build Prompt §4) to the intake port. Africa's Talking does not sign
// callbacks, so three checks stand in (spec §7.12): a secret path token
// (compared in constant time; a wrong token looks like a missing route), a
// service-code allowlist, and the source-IP allowlist enforced by HAProxy
// and nftables in front of this handler.
//
// Logs never carry the provider's sessionId, which is free text (a name
// passes every check): the "session" attribute is a keyed correlation value
// instead (see correlation). networkCode is accepted only as MCC+MNC.
package africastalking

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"

	"sautihealth.com/sauti/internal/intake"
	"sautihealth.com/sauti/internal/pii"
)

// USSDPort is the intake service.
type USSDPort interface {
	HandleUSSD(ctx context.Context, r intake.USSDRequest) (intake.Screen, error)
}

// USSDConfig configures the handler.
type USSDConfig struct {
	Provider     string   // "at"
	PathToken    string   // secret path segment, at least 24 characters
	ServiceCodes []string // accepted serviceCode values
	MaxBody      int64    // request body limit in bytes
	ErrorText    string   // END text on internal error: gives the free voice line
	Budget       time.Duration
}

type handler struct {
	cfg   USSDConfig
	codes map[string]bool
	port  USSDPort
	log   *slog.Logger
	// corrKey keys the logged session correlation value. It is drawn when
	// the handler is built and never leaves the process.
	corrKey []byte
}

// networkCode is MCC+MNC (3 + 2 or 3 digits).
var networkCode = regexp.MustCompile(`^[0-9]{5,6}$`)

// NewUSSDHandler returns the callback handler at POST /v1/ussd/{provider}/{token}.
func NewUSSDHandler(cfg USSDConfig, port USSDPort, log *slog.Logger) (http.Handler, error) {
	if len(cfg.PathToken) < 24 {
		return nil, errors.New("africastalking: path token must be at least 24 characters")
	}
	if len(cfg.ServiceCodes) == 0 || cfg.ErrorText == "" {
		return nil, errors.New("africastalking: service codes and error text are required")
	}
	if cfg.MaxBody <= 0 {
		cfg.MaxBody = 8 << 10
	}
	if cfg.Budget <= 0 {
		cfg.Budget = 800 * time.Millisecond // spec §7.5: USSD p99 under 1 s
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("africastalking: correlation key: %w", err)
	}
	h := &handler{cfg: cfg, codes: map[string]bool{}, port: port, log: log, corrKey: key}
	for _, c := range cfg.ServiceCodes {
		h.codes[c] = true
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/ussd/"+cfg.Provider+"/{token}", h.serve)
	return mux, nil
}

func printable(s string, max int) bool {
	if s == "" || len(s) > max {
		return false
	}
	for _, r := range s {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

// correlation is what the logs carry under "session" in place of sessionID:
// HMAC-SHA-256 under the handler's random key, hex-truncated to 16
// characters. It correlates the lines of one session within one process
// lifetime only (a restart or another replica draws another key), and
// without the key a candidate sessionId cannot be confirmed by hashing it
// (CodeRabbit PR #4). An absent sessionID stays "".
func (h *handler) correlation(sessionID string) string {
	if sessionID == "" {
		return ""
	}
	m := hmac.New(sha256.New, h.corrKey)
	m.Write([]byte(sessionID))
	return hex.EncodeToString(m.Sum(nil))[:16]
}

func (h *handler) reply(w http.ResponseWriter, sc intake.Screen) {
	prefix := "CON "
	if sc.End {
		prefix = "END "
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(prefix + sc.Text))
}

// fail answers an authenticated request that cannot be processed: the caller
// still gets an END screen with the free voice line (rule 3.14; review I4).
// The reason is logged without any input or number.
func (h *handler) fail(w http.ResponseWriter, sessionID, reason string) {
	h.log.Warn("ussd request answered with voice line", "provider", h.cfg.Provider, "session", h.correlation(sessionID), "reason", reason)
	h.reply(w, intake.Screen{Text: h.cfg.ErrorText, End: true})
}

func (h *handler) serve(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	// Transport-level rejections only: wrong route token, wrong media type,
	// oversize body, unknown service code. Everything after authentication
	// is answered with a screen.
	if subtle.ConstantTimeCompare([]byte(r.PathValue("token")), []byte(h.cfg.PathToken)) != 1 {
		http.NotFound(w, r)
		return
	}
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/x-www-form-urlencoded" {
		w.WriteHeader(http.StatusUnsupportedMediaType)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, h.cfg.MaxBody)
	if err := r.ParseForm(); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		h.fail(w, "", "malformed form encoding")
		return
	}
	sessionID := r.PostForm.Get("sessionId")
	serviceCode := r.PostForm.Get("serviceCode")
	if !h.codes[serviceCode] {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if !printable(sessionID, 128) {
		h.fail(w, "", "missing or invalid sessionId")
		return
	}
	text := r.PostForm.Get("text")
	if len(text) > 1024 || strings.ContainsAny(text, "\r\n") {
		h.fail(w, sessionID, "invalid text")
		return
	}
	session := h.correlation(sessionID)
	// An invalid network code is dropped (treated as absent), never logged.
	network := r.PostForm.Get("networkCode")
	if !networkCode.MatchString(network) {
		network = ""
	}
	// An unparseable or withheld number never blocks a report: the session
	// proceeds with an unknown phone and dispatch proceeds on location.
	phone, err := pii.ParsePhone(r.PostForm.Get("phoneNumber"))
	if err != nil {
		h.log.Warn("ussd caller number unparseable; proceeding as unknown", "provider", h.cfg.Provider, "session", session)
		phone = pii.Phone{}
	}

	ctx, cancel := context.WithTimeout(r.Context(), h.cfg.Budget)
	defer cancel()
	sc, err := h.port.HandleUSSD(ctx, intake.USSDRequest{
		Provider: h.cfg.Provider, SessionID: sessionID, ServiceCode: serviceCode,
		NetworkCode: network, Text: text, Phone: phone,
	})
	if err != nil {
		// Fail safe to a human: the caller is always answered, and given the
		// free voice line. The error is logged without input or number.
		h.log.Error("ussd intake failed", "provider", h.cfg.Provider, "session", session, "err", err)
		sc = intake.Screen{Text: h.cfg.ErrorText, End: true}
	}
	h.reply(w, sc)
	h.log.Info("ussd", "provider", h.cfg.Provider, "session", session,
		"network", network, "end", sc.End, "ms", time.Since(start).Milliseconds())
}
