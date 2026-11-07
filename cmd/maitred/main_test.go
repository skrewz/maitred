package main

import (
	"bytes"
	"errors"
	"fmt"
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

	mountForgejoRoute(h, enabled, nil, discardLog())

	if w := serve(h, http.MethodPost, forgejo.WebhookPath); w.Code != http.StatusNoContent {
		t.Errorf("expected 204 from the engine handler, got %d", w.Code)
	}
	if !called {
		t.Error("the engine handler was not mounted")
	}
}

// With the engine disabled the route still exists and answers 503
// naming the category of the startup error — not the raw error, which
// may quote filesystem paths — and not 404 (§forgejo/webhook/the-route).
func TestMountForgejoRoute_EngineDisabledAnswers503(t *testing.T) {
	h := testHandler(t)
	cause := fmt.Errorf("parse forgejoeng config %q: %w", "/data/maitred/forgejoeng/config.yaml", forgejo.ErrConfigUnreadable)

	mountForgejoRoute(h, nil, cause, discardLog())

	w := serve(h, http.MethodPost, forgejo.WebhookPath)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d (body %q)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), forgejo.CategoryConfigUnreadable.String()) {
		t.Errorf("body %q does not name the category %q", w.Body.String(), forgejo.CategoryConfigUnreadable.String())
	}
	if strings.Contains(w.Body.String(), "/data/maitred") {
		t.Errorf("body %q leaks the raw startup error", w.Body.String())
	}
}

// The trigger fallback is untouched: an unconfigured provider's own
// route still 404s, and the forgejo refusal does not leak into it.
func TestMountForgejoRoute_TriggerFallbackUnchanged(t *testing.T) {
	h := testHandler(t)

	mountForgejoRoute(h, nil, errors.New("config error"), discardLog())

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

// writeForgejoConfig writes a minimal valid engine config — one entry
// per action the binary knows — into dir, so these tests track the
// action enum rather than the shipped sample config.
func writeForgejoConfig(t *testing.T, dir string) {
	t.Helper()
	var b strings.Builder
	b.WriteString("org: example-org\nreconcile_interval: 15m\nactions:\n")
	for _, a := range forgejo.AllActions {
		fmt.Fprintf(&b, "  %s:\n    prompt: \"do {{.IssueURL}}\"\n    persona: persona\n    timeout: 1h\n", a)
	}
	if err := os.WriteFile(filepath.Join(dir, "forgejoeng.yaml"), []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write engine config: %v", err)
	}
}

// TestNewForgejoEngineEnvironmentIncomplete checks that an engine the
// config enables but the environment cannot serve is reported as an
// incomplete environment rather than an unclassifiable error
// (§forgejo/observability/why-the-engine-is-off,
// §forgejo/webhook/configuration).
func TestNewForgejoEngineEnvironmentIncomplete(t *testing.T) {
	dir := t.TempDir()
	writeForgejoConfig(t, dir)
	t.Setenv(forgejo.ConfigEnvVar, dir)
	t.Setenv(forgejo.URLEnvVar, "")
	t.Setenv(forgejo.TokenEnvVar, "")
	t.Setenv(forgejo.SecretEnvVar, "")
	t.Setenv(forgejo.SecretFileEnvVar, "")

	_, _, err := newForgejoEngine(t.TempDir(), nil, nil)
	if !errors.Is(err, forgejo.ErrEnvironmentIncomplete) {
		t.Fatalf("err = %v, want one wrapping forgejo.ErrEnvironmentIncomplete", err)
	}
	_, category := forgejo.EngineStatus(dir, forgejo.ProcessEnv())
	if category != forgejo.CategoryCredentialsMissing {
		t.Errorf("category = %s, want %s — startup and the classifier must agree", category, forgejo.CategoryCredentialsMissing)
	}
}

// TestNewForgejoEngineUnreadableConfig checks that a config the process
// cannot read is reported with its config context intact
// (§forgejo/observability/why-the-engine-is-off).
func TestNewForgejoEngineUnreadableConfig(t *testing.T) {
	skipUnlessAccessDenied(t)
	dir := t.TempDir()
	writeForgejoConfig(t, dir)
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Skipf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	t.Setenv(forgejo.ConfigEnvVar, dir)

	_, _, err := newForgejoEngine(t.TempDir(), nil, nil)
	if !errors.Is(err, forgejo.ErrConfigUnreadable) {
		t.Fatalf("err = %v, want one wrapping forgejo.ErrConfigUnreadable", err)
	}
}

// TestNewForgejoEngineUnusableMTLSPair checks that the mTLS pair the
// environment names is a cause startup and the offline classifier agree
// on: a probe must not read healthy on a deployment whose client
// certificate cannot be built (§forgejo/client/configuration,
// §forgejo/observability/why-the-engine-is-off).
func TestNewForgejoEngineUnusableMTLSPair(t *testing.T) {
	for _, tc := range []struct {
		name string
		cert string
		key  string
		want error
	}{
		{"half a pair", filepath.Join(t.TempDir(), "client.crt"), "", forgejo.ErrEnvironmentIncomplete},
		{"unloadable pair", filepath.Join(t.TempDir(), "absent.crt"), filepath.Join(t.TempDir(), "absent.key"), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeForgejoConfig(t, dir)
			t.Setenv(forgejo.ConfigEnvVar, dir)
			t.Setenv(forgejo.URLEnvVar, "https://forgejo.example.com")
			t.Setenv(forgejo.TokenEnvVar, "a-token")
			t.Setenv(forgejo.SecretEnvVar, "a-secret")
			t.Setenv(forgejo.SecretFileEnvVar, "")
			t.Setenv(forgejo.ClientCertEnvVar, tc.cert)
			t.Setenv(forgejo.ClientKeyEnvVar, tc.key)

			_, _, startupErr := newForgejoEngine(t.TempDir(), nil, nil)
			if startupErr == nil {
				t.Fatal("newForgejoEngine: want an error from a deployment with no usable client certificate, got nil")
			}
			if tc.want != nil && !errors.Is(startupErr, tc.want) {
				t.Errorf("startup error = %v, want one wrapping %v", startupErr, tc.want)
			}
			cause, category := forgejo.EngineStatus(dir, forgejo.ProcessEnv())
			if cause == nil {
				t.Fatal("EngineStatus read a deployment startup refuses as sound")
			}
			if category != forgejo.CategoryOf(startupErr) {
				t.Errorf("EngineStatus says %s, the startup error classifies as %s: the two must agree",
					category, forgejo.CategoryOf(startupErr))
			}
		})
	}
}

// captureStdout runs fn with os.Stdout redirected to a pipe and returns
// what fn printed. The health branch's notes go to stdout, so the tests
// that pin them have to read the process's own output.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()
	fn()
	_ = w.Close()
	os.Stdout = old
	out := <-done
	_ = r.Close()
	return out
}

// healthTriggerDirs builds the directories healthCheck probes: one valid
// trigger and a fresh data directory.
func healthTriggerDirs(t *testing.T) (string, string) {
	t.Helper()
	triggerDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(triggerDir, "triggers.yaml"), []byte(triggerYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	return triggerDir, t.TempDir()
}

// writeForgejoConfigVariant writes an engine config covering the binary's
// action set except for the action named omit, plus the extra lines —
// enough to name an action the binary does not know.
func writeForgejoConfigVariant(t *testing.T, dir, omit, extra string) {
	t.Helper()
	var b strings.Builder
	b.WriteString("org: example-org\nreconcile_interval: 15m\nactions:\n")
	for _, a := range forgejo.AllActions {
		if string(a) == omit {
			continue
		}
		fmt.Fprintf(&b, "  %s:\n    prompt: \"do {{.IssueURL}}\"\n    persona: persona\n    timeout: 1h\n", a)
	}
	b.WriteString(extra)
	if err := os.WriteFile(filepath.Join(dir, "forgejoeng.yaml"), []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write engine config: %v", err)
	}
}

// TestHealthCheckForgejoEngine pins the health check's Forgejo states:
// nothing configured stays healthy and says so, a configured engine the
// environment cannot serve, or a config the binary disagrees with, fails
// the check with the category in its text — offline, without opening the
// store (§forgejo/observability/why-the-engine-is-off, §overview/scope).
func TestHealthCheckForgejoEngine(t *testing.T) {
	// setForgejoEnv isolates one case's Forgejo environment: the config
	// location and every credential variable the probe reads.
	setForgejoEnv := func(t *testing.T, configPath string, creds bool) {
		t.Helper()
		t.Setenv(forgejo.ConfigEnvVar, configPath)
		url, token, secret := "", "", ""
		if creds {
			url, token, secret = "https://forgejo.example.com", "a-token", "a-secret"
		}
		t.Setenv(forgejo.URLEnvVar, url)
		t.Setenv(forgejo.TokenEnvVar, token)
		t.Setenv(forgejo.SecretEnvVar, secret)
		t.Setenv(forgejo.SecretFileEnvVar, "")
		t.Setenv(forgejo.ClientCertEnvVar, "")
		t.Setenv(forgejo.ClientKeyEnvVar, "")
	}

	t.Run("no config at all is healthy and says the engine is not configured", func(t *testing.T) {
		setForgejoEnv(t, filepath.Join(t.TempDir(), "forgejoeng.yaml"), false)
		triggerDir, dataDir := healthTriggerDirs(t)
		var err error
		out := captureStdout(t, func() { err = healthCheck(triggerDir, dataDir) })
		if err != nil {
			t.Errorf("healthCheck with no forgejo config = %v, want healthy: not configured is not a fault", err)
		}
		if !strings.Contains(out, forgejo.CategoryNotConfigured.String()) {
			t.Errorf("healthy output %q must say the engine is not configured", out)
		}
		if _, statErr := os.Stat(filepath.Join(dataDir, "forgejoeng")); statErr == nil {
			t.Error("the health check must not create the engine store: it answers offline")
		}
	})

	t.Run("configured and complete is healthy without a not-configured note", func(t *testing.T) {
		dir := t.TempDir()
		writeForgejoConfig(t, dir)
		setForgejoEnv(t, dir, true)
		triggerDir, dataDir := healthTriggerDirs(t)
		var err error
		out := captureStdout(t, func() { err = healthCheck(triggerDir, dataDir) })
		if err != nil {
			t.Errorf("healthCheck with a working deployment = %v, want healthy", err)
		}
		if strings.Contains(out, forgejo.CategoryNotConfigured.String()) {
			t.Errorf("a running engine must not be reported as %q: the zero value names an engine that was never configured", forgejo.CategoryNotConfigured)
		}
	})

	t.Run("config omits an action the binary knows is unhealthy naming the category", func(t *testing.T) {
		dir := t.TempDir()
		writeForgejoConfigVariant(t, dir, "rebase", "")
		setForgejoEnv(t, dir, true)
		triggerDir, dataDir := healthTriggerDirs(t)
		err := healthCheck(triggerDir, dataDir)
		if !errors.Is(err, forgejo.ErrActionSetMismatch) {
			t.Fatalf("healthCheck = %v, want one wrapping forgejo.ErrActionSetMismatch", err)
		}
		if !strings.Contains(err.Error(), forgejo.CategoryConfigBinaryMismatch.String()) {
			t.Errorf("error text %q must name the category %q", err, forgejo.CategoryConfigBinaryMismatch)
		}
	})

	t.Run("config names an action the binary does not know is unhealthy naming the category", func(t *testing.T) {
		dir := t.TempDir()
		writeForgejoConfigVariant(t, dir, "", "  bogus:\n    prompt: x\n    persona: p\n    timeout: 1h\n")
		setForgejoEnv(t, dir, true)
		triggerDir, dataDir := healthTriggerDirs(t)
		err := healthCheck(triggerDir, dataDir)
		if !errors.Is(err, forgejo.ErrActionSetMismatch) {
			t.Fatalf("healthCheck = %v, want one wrapping forgejo.ErrActionSetMismatch", err)
		}
		if !strings.Contains(err.Error(), forgejo.CategoryConfigBinaryMismatch.String()) {
			t.Errorf("error text %q must name the category %q", err, forgejo.CategoryConfigBinaryMismatch)
		}
	})

	t.Run("config present with credentials missing is unhealthy naming the category", func(t *testing.T) {
		dir := t.TempDir()
		writeForgejoConfig(t, dir)
		setForgejoEnv(t, dir, false)
		triggerDir, dataDir := healthTriggerDirs(t)
		err := healthCheck(triggerDir, dataDir)
		if !errors.Is(err, forgejo.ErrEnvironmentIncomplete) {
			t.Fatalf("healthCheck = %v, want one wrapping forgejo.ErrEnvironmentIncomplete", err)
		}
		if !strings.Contains(err.Error(), forgejo.CategoryCredentialsMissing.String()) {
			t.Errorf("error text %q must name the category %q", err, forgejo.CategoryCredentialsMissing)
		}
	})

	t.Run("config present but unparseable is unhealthy naming the category", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "forgejoeng.yaml"), []byte("org: [unclosed"), 0o644); err != nil {
			t.Fatal(err)
		}
		setForgejoEnv(t, dir, true)
		triggerDir, dataDir := healthTriggerDirs(t)
		err := healthCheck(triggerDir, dataDir)
		if !errors.Is(err, forgejo.ErrConfigUnreadable) {
			t.Fatalf("healthCheck = %v, want one wrapping forgejo.ErrConfigUnreadable", err)
		}
		if !strings.Contains(err.Error(), forgejo.CategoryConfigUnreadable.String()) {
			t.Errorf("error text %q must name the category %q", err, forgejo.CategoryConfigUnreadable)
		}
	})
}
