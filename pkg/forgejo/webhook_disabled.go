package forgejo

import (
	"fmt"
	"log/slog"
	"net/http"
)

// disabledReasonFallback stands in when the engine came up without a
// handler and without an error to name — a state that should never
// happen, but must never produce an empty refusal either.
const disabledReasonFallback = "forgejo engine is not configured"

// disabledHandler is the route's answer while the engine is off: the
// endpoint exists and cannot serve, so every delivery is refused with
// 503 and leaves one log line behind (§forgejo/webhook/the-route).
// Refusing with 404 — what the trigger handler's /v1/ fallback would
// make of it — would point an operator at the webhook configuration
// rather than at the engine's startup log, where the answer is.
type disabledHandler struct {
	reason string
	log    *slog.Logger
}

// NewDisabledHandler creates the handler mounted at WebhookPath when
// the engine could not be constructed. reason is the startup error
// verbatim (missing config, missing credentials, ...); it is named in
// every response body and every log line. logger may be nil, in which
// case the default logger is used.
func NewDisabledHandler(reason string, logger *slog.Logger) http.Handler {
	if reason == "" {
		reason = disabledReasonFallback
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &disabledHandler{reason: reason, log: logger}
}

// ServeHTTP refuses the delivery: one log line naming the path, the
// delivery id, and the reason, then 503 with the reason in the body.
// The body is never read — there is nothing to verify or map
// (§forgejo/webhook/the-route).
func (h *disabledHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.log.Error("refused forgejo delivery",
		"path", r.URL.Path,
		"delivery", r.Header.Get(HeaderDelivery),
		"reason", h.reason,
	)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusServiceUnavailable)
	fmt.Fprintf(w, "forgejo engine disabled: %s\n", h.reason)
}
