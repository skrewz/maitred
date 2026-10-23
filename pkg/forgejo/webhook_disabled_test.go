package forgejo

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// disabledLog builds a disabled-engine handler over a capturing logger.
func disabledLog(t *testing.T, reason string) (http.Handler, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	h := NewDisabledHandler(reason, slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			// Drop the timestamp so log lines compare stably.
			if a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	})))
	return h, &buf
}

// delivery posts one refused delivery to the route with the given
// delivery id.
func delivery(t *testing.T, h http.Handler, deliveryID string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(issuePayload("org/repo", 7, "opened"))
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, WebhookPath, bytes.NewReader(body))
	req.Header.Set(HeaderDelivery, deliveryID)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// A delivery that reaches the route while the engine is off is refused
// with 503 naming the config error verbatim (§forgejo/webhook/the-route).
func TestDisabledHandler_Answers503WithTheConfigError(t *testing.T) {
	const reason = `parse config: reconcile_interval: unknown field "reconcile_interval"`
	h, _ := disabledLog(t, reason)

	w := delivery(t, h, "d1")

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), reason) {
		t.Errorf("body %q does not carry the config error verbatim %q", w.Body.String(), reason)
	}
}

// Per-delivery silence is half the defect: every refused delivery leaves
// one log line naming path, delivery id, and reason.
func TestDisabledHandler_LogsEveryRefusedDelivery(t *testing.T) {
	const reason = "MAITRED_FORGEJO_TOKEN must be set"
	h, buf := disabledLog(t, reason)

	for _, id := range []string{"aaa-111", "bbb-222"} {
		if w := delivery(t, h, id); w.Code != http.StatusServiceUnavailable {
			t.Fatalf("expected 503, got %d", w.Code)
		}
	}

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected one log line per delivery, got %d: %q", len(lines), buf.String())
	}
	for i, id := range []string{"aaa-111", "bbb-222"} {
		line := lines[i]
		for _, want := range []string{WebhookPath, id, reason} {
			if !strings.Contains(line, want) {
				t.Errorf("line %d: missing %q in %q", i+1, want, line)
			}
		}
	}
}

// An empty reason never yields an empty body or a reasonless log line.
func TestDisabledHandler_EmptyReasonFallsBack(t *testing.T) {
	h, buf := disabledLog(t, "")

	w := delivery(t, h, "ccc-333")

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
	if strings.TrimSpace(w.Body.String()) == "" {
		t.Error("body is empty; the refusal must say something")
	}
	if !strings.Contains(buf.String(), "path=") {
		t.Errorf("log line %q does not name the path", buf.String())
	}
}

// The refusal does not depend on the method or a parseable body; the
// route serves nothing while the engine is off.
func TestDisabledHandler_RefusesWithoutReadingTheBody(t *testing.T) {
	h, _ := disabledLog(t, "config error")

	req := httptest.NewRequest(http.MethodGet, WebhookPath, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("GET: expected 503, got %d", w.Code)
	}

	req = httptest.NewRequest(http.MethodPost, WebhookPath, strings.NewReader("not json"))
	req.Header.Set(HeaderDelivery, "ddd-444")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("unparseable body: expected 503, got %d", w.Code)
	}
}

// A nil logger falls back to the default one, like the engine's handler.
func TestDisabledHandler_NilLogger(t *testing.T) {
	h := NewDisabledHandler("config error", nil)

	if w := delivery(t, h, "fff-666"); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
}
