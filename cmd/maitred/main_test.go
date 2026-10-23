package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"maitred/pkg/engine"
	"maitred/pkg/forgejo"
	"maitred/pkg/queue"
	"maitred/pkg/webhook"
)

const triggerYAML = `
triggers:
  - id: "github-issues"
    type: periodic
    schedule: "@every 1h"
    prompt: "check github issues"
`

// testHandler builds the webhook API router over one configured trigger
// provider and a trigger the endpoint points at.
func testHandler(t *testing.T) *webhook.Handler {
	t.Helper()
	triggerDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(triggerDir, "triggers.yaml"), []byte(triggerYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	eng, err := engine.New(engine.Config{
		TriggerDir: triggerDir,
		DataDir:    t.TempDir(),
		Queue:      queue.NewTaskQueue(),
	})
	if err != nil {
		t.Fatalf("engine.New: %v", err)
	}
	t.Cleanup(eng.Stop)

	providers := []webhook.ProviderConfig{{
		Provider: "github",
		Endpoints: []webhook.EndpointConfig{{
			Name:      "issues",
			TriggerID: "github-issues",
			Response:  `{"status":"ok"}`,
		}},
	}}
	return webhook.NewHandler(eng, eng.StateStore(), "test", providers)
}

// discardLog is the logger the refusals are logged to in tests.
func discardLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// serve routes one request through the router and records the response.
func serve(h *webhook.Handler, method, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader("{}"))
	req.Header.Set(forgejo.HeaderDelivery, "11111111-2222-3333-4444-555555555555")
	w := httptest.NewRecorder()
	h.ServeMux().ServeHTTP(w, req)
	return w
}

// The engine handler wins over the trigger route when the engine is up
// (§forgejo/webhook/the-route).
func TestMountForgejoRoute_EngineEnabledKeepsTheOverride(t *testing.T) {
	h := testHandler(t)
	var called bool
	enabled := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	})

	mountForgejoRoute(h, enabled, "", discardLog())

	if w := serve(h, http.MethodPost, forgejo.WebhookPath); w.Code != http.StatusNoContent {
		t.Errorf("expected 204 from the engine handler, got %d", w.Code)
	}
	if !called {
		t.Error("the engine handler was not mounted")
	}
}

// With the engine disabled the route still exists and answers 503
// naming the startup error, not 404 (§forgejo/webhook/the-route).
func TestMountForgejoRoute_EngineDisabledAnswers503(t *testing.T) {
	h := testHandler(t)
	const reason = `parse forgejoeng config: no such file or directory`

	mountForgejoRoute(h, nil, reason, discardLog())

	w := serve(h, http.MethodPost, forgejo.WebhookPath)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d (body %q)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), reason) {
		t.Errorf("body %q does not name the config error %q", w.Body.String(), reason)
	}
}

// The trigger fallback is untouched: an unconfigured provider's own
// route still 404s, and the forgejo refusal does not leak into it.
func TestMountForgejoRoute_TriggerFallbackUnchanged(t *testing.T) {
	h := testHandler(t)

	mountForgejoRoute(h, nil, "config error", discardLog())

	w := serve(h, http.MethodPost, "/v1/gitlab/push")
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for an unconfigured trigger provider, got %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "config error") {
		t.Errorf("the forgejo refusal leaked into the trigger route: %q", w.Body.String())
	}

	if w := serve(h, http.MethodPost, "/v1/github/issues"); w.Code != http.StatusOK {
		t.Errorf("configured trigger route: expected 200, got %d", w.Code)
	}
}
