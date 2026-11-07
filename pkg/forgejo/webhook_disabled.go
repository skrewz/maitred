package forgejo

import (
	"fmt"
	"log/slog"
	"net/http"
)

// disabledHandler is the route's answer while the engine is off: the
// endpoint exists and cannot serve, so every delivery is refused with
// 503 and leaves one log line behind (§forgejo/webhook/the-route).
// Refusing with 404 — what the trigger handler's /v1/ fallback would
// make of it — would point an operator at the webhook configuration
// rather than at the engine's startup log, where the answer is.
//
// The body names the fault's category, never the raw error: its reader
// is Forgejo, which logs it and displays it in a delivery list people
// read, so a config error quoting a filesystem path has no business
// there. The full error keeps going to the log line, where the detail
// is still findable (§forgejo/observability/why-the-engine-is-off).
type disabledHandler struct {
	category Category
	cause    error
	log      *slog.Logger
}

// NewDisabledHandler creates the handler mounted at WebhookPath when
// the engine could not be constructed. category is the enum value the
// startup error classifies to (§forgejo/observability/why-the-engine-is-off);
// it is named in every response body. cause is the startup error
// verbatim (missing config, missing credentials, ...); it is named in
// every log line and never in a body. A nil cause with the zero
// category — a state that should never happen, but must never produce
// an empty refusal either — reads as the enum's not-configured value.
// logger may be nil, in which case the default logger is used.
func NewDisabledHandler(category Category, cause error, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &disabledHandler{category: category, cause: cause, log: logger}
}

// ServeHTTP refuses the delivery: one log line naming the path, the
// delivery id, the category, and the full error as reason, then 503
// with the category in the body. The body is never read — there is
// nothing to verify or map (§forgejo/webhook/the-route).
func (h *disabledHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	reason := h.category.String()
	if h.cause != nil {
		reason = h.cause.Error()
	}
	h.log.Error("refused forgejo delivery",
		"path", r.URL.Path,
		"delivery", r.Header.Get(HeaderDelivery),
		"category", h.category.String(),
		"reason", reason,
	)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusServiceUnavailable)
	fmt.Fprintf(w, "forgejo engine disabled: %s\n", h.category)
}
