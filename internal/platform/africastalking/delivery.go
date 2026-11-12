package africastalking

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"regexp"
	"time"
)

// DeliveryReport is what Sauti keeps of a provider's delivery report: the
// message ID returned at sending, the status and a failure reason code.
// The phone number in the report is dropped.
type DeliveryReport struct {
	Provider      string
	MessageID     string
	Status        string // Sent, Submitted, Buffered, Rejected, Success, Failed, AbsentSubscriber, Expired
	FailureReason string // a code, e.g. AbsentSubscriber, UserInBlackList; empty unless failed
	Final         bool   // Success, Failed, Rejected, AbsentSubscriber, Expired
}

// ErrUnknownMessage is returned by a DeliveryReports port for a report about
// a message it never sent; the handler acknowledges it.
var ErrUnknownMessage = errors.New("africastalking: delivery report for an unknown message")

// DeliveryReports records reports (the operational store).
type DeliveryReports interface {
	RecordDeliveryReport(ctx context.Context, r DeliveryReport) error
}

// DeliveryReportConfig configures the handler.
type DeliveryReportConfig struct {
	Provider  string // "at"
	PathToken string // secret path segment, at least 24 characters
	MaxBody   int64
}

var (
	messageID     = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)
	reasonCode    = regexp.MustCompile(`^[A-Za-z]{1,64}$`)
	finalStatuses = map[string]bool{"Success": true, "Failed": true, "Rejected": true, "AbsentSubscriber": true, "Expired": true}
	knownStatuses = map[string]bool{"Sent": true, "Submitted": true, "Buffered": true, "Success": true, "Failed": true,
		"Rejected": true, "AbsentSubscriber": true, "Expired": true}
)

type deliveryHandler struct {
	cfg  DeliveryReportConfig
	port DeliveryReports
	log  *slog.Logger
}

// NewDeliveryReportHandler returns the delivery-report callback at
// POST /v1/sms/delivery/{provider}/{token}.
func NewDeliveryReportHandler(cfg DeliveryReportConfig, port DeliveryReports, log *slog.Logger) (http.Handler, error) {
	if len(cfg.PathToken) < 24 || cfg.Provider == "" || port == nil {
		return nil, errors.New("africastalking: delivery reports need a provider, a port and a path token of at least 24 characters")
	}
	if cfg.MaxBody <= 0 {
		cfg.MaxBody = 4 << 10
	}
	h := &deliveryHandler{cfg: cfg, port: port, log: log}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sms/delivery/"+cfg.Provider+"/{token}", h.serve)
	return mux, nil
}

func (h *deliveryHandler) serve(w http.ResponseWriter, r *http.Request) {
	if subtle.ConstantTimeCompare([]byte(r.PathValue("token")), []byte(h.cfg.PathToken)) != 1 {
		http.NotFound(w, r)
		return
	}
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/x-www-form-urlencoded" {
		http.Error(w, "form expected", http.StatusUnsupportedMediaType)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, h.cfg.MaxBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	d := DeliveryReport{Provider: h.cfg.Provider, MessageID: r.PostForm.Get("id"), Status: r.PostForm.Get("status"),
		FailureReason: r.PostForm.Get("failureReason")}
	if !messageID.MatchString(d.MessageID) || !knownStatuses[d.Status] || (d.FailureReason != "" && !reasonCode.MatchString(d.FailureReason)) {
		http.Error(w, "bad delivery report", http.StatusBadRequest)
		return
	}
	d.Final = finalStatuses[d.Status]
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	switch err := h.port.RecordDeliveryReport(ctx, d); {
	case errors.Is(err, ErrUnknownMessage):
		h.log.Warn("delivery report for an unknown message", "message", d.MessageID, "status", d.Status)
	case err != nil:
		h.log.Error("delivery report not recorded", "message", d.MessageID, "status", d.Status, "err", err)
		http.Error(w, "retry later", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
}
