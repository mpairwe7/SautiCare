package main

import (
	"context"
	"embed"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"sautihealth.com/sauti/internal/config"
	"sautihealth.com/sauti/internal/location"
	"sautihealth.com/sauti/internal/pii"
)

//go:embed web
var webFS embed.FS

// startAttempts bounds the retries of a failed start.
const startAttempts = 3

// Phases of the app.
const (
	phaseStarting  = "starting"
	phaseReady     = "ready"
	phaseResetting = "resetting"
	phaseFailed    = "failed"
)

// app serves the UI and owns the current stack. Starting and resetting run
// in the background, so the UI answers at once (a Space's port must).
type app struct {
	ctx context.Context
	o   options
	log *slog.Logger
	cfg config.Bundle
	gaz location.File

	mu    sync.Mutex
	st    *stack
	phase string
	run   int // counts the runs that came up, so a browser sees a reset it missed
	wg    sync.WaitGroup

	start func(context.Context, options, io.Writer) (*stack, error) // startStack; a seam for tests
}

func newApp(ctx context.Context, o options, log *slog.Logger, cfg config.Bundle, gaz location.File) *app {
	return &app{ctx: ctx, o: o, log: log, cfg: cfg, gaz: gaz, phase: phaseStarting, start: startStack}
}

// boot starts a stack in the background (phase starting or resetting),
// retrying a failed start up to startAttempts times: a port picked free can
// be taken by another process before its service binds it.
func (a *app) boot() {
	a.wg.Go(func() {
		var (
			st  *stack
			err error
		)
		for n := 1; ; n++ {
			if st, err = a.start(a.ctx, a.o, os.Stderr); err == nil || n == startAttempts || a.ctx.Err() != nil {
				break
			}
			a.log.Warn("demo: start failed; retrying", "attempt", n, "err", err)
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		switch {
		case err != nil:
			// The detail stays in the log: the UI is public.
			a.log.Error("demo: the system did not start", "err", err)
			a.phase = phaseFailed
		case a.ctx.Err() != nil:
			st.stop()
		default:
			a.st, a.phase = st, phaseReady
			a.run++
			a.log.Info("demo ready")
		}
	})
}

// close waits for a start or reset under way, then erases the current stack.
func (a *app) close() {
	a.wg.Wait()
	a.mu.Lock()
	st := a.st
	a.st = nil
	a.mu.Unlock()
	st.stop()
}

// current returns the ready stack, or answers 503 with the phase.
func (a *app) current(w http.ResponseWriter) *stack {
	a.mu.Lock()
	st, phase := a.st, a.phase
	a.mu.Unlock()
	if st == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "The demo is " + phase + ".", "phase": phase})
	}
	return st
}

func (a *app) routes() http.Handler {
	static, err := fs.Sub(webFS, "web")
	if err != nil {
		panic(err) // the embedded tree is fixed at build time
	}
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(static))
	mux.HandleFunc("GET /api/state", a.state)
	mux.HandleFunc("GET /api/board", a.board)
	mux.HandleFunc("GET /api/sms", a.sms)
	mux.HandleFunc("POST /api/ussd", a.ussd)
	mux.HandleFunc("POST /api/audit", a.audit)
	mux.HandleFunc("POST /api/reset", a.reset)
	return headers(mux)
}

// headers: no third-party content, no inline script, no caching of state.
// frame-ancestors is left open: Hugging Face shows a Space in an iframe.
func headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'none'; object-src 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// readJSON accepts only a small JSON body: a cross-site form cannot send one.
func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "JSON expected"})
		return false
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return false
	}
	return true
}

func (a *app) state(w http.ResponseWriter, _ *http.Request) {
	a.mu.Lock()
	st, phase, run := a.st, a.phase, a.run
	a.mu.Unlock()
	out := map[string]any{"phase": phase, "run": run, "codes": map[string]string{"caller": callerCode, "responder": responderCode}}
	if st != nil {
		phones := make([]phone, len(st.phones))
		for i, p := range st.phones {
			phones[i] = p
			for _, wd := range a.gaz.Wards {
				if wd.ID == p.Ward {
					phones[i].Ward = wd.Name.EN
				}
			}
		}
		out["phones"], out["down"] = phones, st.down()
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *app) board(w http.ResponseWriter, r *http.Request) {
	st := a.current(w)
	if st == nil {
		return
	}
	b, err := st.readBoard(r.Context(), a.cfg, a.gaz)
	if err != nil {
		a.log.Error("demo: board", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "The board could not be read."})
		return
	}
	writeJSON(w, http.StatusOK, b)
}

// sms returns every on-screen phone's inbox, keyed by phone.
func (a *app) sms(w http.ResponseWriter, _ *http.Request) {
	st := a.current(w)
	if st == nil {
		return
	}
	out := map[string][]sms{}
	for _, p := range st.phones {
		out[p.Handle] = st.sink.inbox(p.e164)
	}
	writeJSON(w, http.StatusOK, out)
}

type ussdRequest struct {
	Phone   string `json:"phone"`   // a phone's handle; a new session needs it and Code
	Code    string `json:"code"`    // the dialled service code
	Session string `json:"session"` // an open session's token
	Input   string `json:"input"`
}

// maxInput is longer than any Tool 1 answer (a 20-letter place, a number).
const maxInput = 64

// ussd dials a service code from an on-screen phone, or sends one reply on
// an open session, through intake-gw's Africa's Talking callback.
func (a *app) ussd(w http.ResponseWriter, r *http.Request) {
	st := a.current(w)
	if st == nil {
		return
	}
	var q ussdRequest
	if !readJSON(w, r, &q) {
		return
	}
	if len(q.Input) > maxInput || !utf8.ValidString(q.Input) || strings.IndexFunc(q.Input, unicode.IsControl) >= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "That reply is too long or holds characters a phone cannot send."})
		return
	}
	if !syntheticOnly(q.Input, st.phones) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "This is a public demo: never type a real phone number. Choose \"This number\" instead."})
		return
	}
	var (
		tok  = q.Session
		sess *session
	)
	if tok == "" {
		p, ok := st.phone(q.Phone)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown phone"})
			return
		}
		route := st.routes[q.Code]
		if route == nil {
			// What a handset shows for a code no network serves.
			writeJSON(w, http.StatusOK, map[string]any{"text": "Connection problem or invalid MMI code.", "end": true})
			return
		}
		var err error
		if tok, sess, err = st.dial(p, route); err != nil {
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": err.Error()})
			return
		}
		q.Input = ""
	} else if sess = st.session(tok); sess == nil {
		writeJSON(w, http.StatusGone, map[string]any{"text": "Session ended.", "end": true})
		return
	}
	sc, err := sess.send(q.Input)
	if err != nil {
		a.log.Warn("demo: USSD callback failed", "err", err)
		st.hangUp(tok)
		writeJSON(w, http.StatusOK, map[string]any{"text": "Connection problem or invalid MMI code.", "end": true})
		return
	}
	if sc.End {
		st.hangUp(tok)
		tok = ""
	}
	writeJSON(w, http.StatusOK, map[string]any{"session": tok, "text": sc.Text, "end": sc.End})
}

// syntheticOnly refuses a reply holding a phone number (seven digits or
// more) unless it is one of the demo's own synthetic numbers: on a public
// page, a typed call-back number must never be a real person's.
func syntheticOnly(input string, phones []phone) bool {
	digits := 0
	for _, c := range input {
		if c >= '0' && c <= '9' {
			digits++
		}
	}
	if digits < 7 {
		return true
	}
	p, err := pii.ParsePhone(input)
	if err != nil {
		return false
	}
	for _, s := range phones {
		if s.e164 == p.E164() {
			return true
		}
	}
	return false
}

func (a *app) audit(w http.ResponseWriter, r *http.Request) {
	st := a.current(w)
	if st == nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	res, err := st.verifyLedger(ctx)
	if err != nil {
		a.log.Error("demo: verify ledger", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "The verification could not run."})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// reset erases the current run and starts a new one in the background.
func (a *app) reset(w http.ResponseWriter, r *http.Request) {
	var none struct{}
	if !readJSON(w, r, &none) {
		return
	}
	a.mu.Lock()
	if a.phase == phaseStarting || a.phase == phaseResetting {
		a.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{"error": "The demo is already " + a.phase + "."})
		return
	}
	old := a.st
	a.st, a.phase = nil, phaseResetting
	a.mu.Unlock()
	a.log.Info("demo: reset requested")
	a.wg.Go(func() {
		old.stop()
		if a.ctx.Err() == nil {
			a.boot()
		}
	})
	writeJSON(w, http.StatusAccepted, map[string]string{"phase": phaseResetting})
}
