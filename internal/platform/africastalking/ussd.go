// Package africastalking adapts Africa's Talking USSD callbacks (transport
// only, Build Prompt §4) to the intake port. Africa's Talking does not sign
// callbacks, so three checks stand in (spec §7.12): a secret path token
// (compared in constant time; a wrong token looks like a missing route), a
// service-code allowlist, and the source-IP allowlist enforced by HAProxy
// and nftables in front of this handler.
package africastalking

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"mime"
	"net/http"
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
}

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
	h := &handler{cfg: cfg, codes: map[string]bool{}, port: port, log: log}
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

func (h *handler) reply(w http.ResponseWriter, sc intake.Screen) {
	prefix := "CON "
	if sc.End {
		prefix = "END "
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(prefix + sc.Text))
}

func (h *handler) serve(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
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
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	sessionID := r.PostForm.Get("sessionId")
	serviceCode := r.PostForm.Get("serviceCode")
	if !printable(sessionID, 128) {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if !h.codes[serviceCode] {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	phone, err := pii.ParsePhone(r.PostForm.Get("phoneNumber"))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	text := r.PostForm.Get("text")
	if len(text) > 1024 || strings.ContainsAny(text, "\r\n") {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), h.cfg.Budget)
	defer cancel()
	sc, err := h.port.HandleUSSD(ctx, intake.USSDRequest{
		Provider: h.cfg.Provider, SessionID: sessionID, ServiceCode: serviceCode,
		NetworkCode: r.PostForm.Get("networkCode"), Text: text, Phone: phone,
	})
	if err != nil {
		// Fail safe to a human: the caller is always answered, and given the
		// free voice line. The error is logged without input or number.
		h.log.Error("ussd intake failed", "provider", h.cfg.Provider, "session", sessionID, "err", err)
		sc = intake.Screen{Text: h.cfg.ErrorText, End: true}
	}
	h.reply(w, sc)
	h.log.Info("ussd", "provider", h.cfg.Provider, "session", sessionID,
		"network", r.PostForm.Get("networkCode"), "end", sc.End, "ms", time.Since(start).Milliseconds())
}
