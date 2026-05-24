package webhook_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"maitred/pkg/engine"
	"maitred/pkg/queue"
	"maitred/pkg/webhook"
)

type mockQueue struct {
	mu    sync.Mutex
	tasks []*queue.Task
	added atomic.Int32
}

func (m *mockQueue) AddTask(task *queue.Task) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tasks = append(m.tasks, task)
	m.added.Add(1)
	return nil
}

func (m *mockQueue) Tasks() []*queue.Task {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]*queue.Task, len(m.tasks))
	copy(result, m.tasks)
	return result
}

func (m *mockQueue) Count() int {
	return int(m.added.Load())
}

func setupTestEnv(t *testing.T, triggerYAML, webhookYAML string) (*engine.Engine, *mockQueue, []webhook.ProviderConfig) {
	t.Helper()

	// Create trigger dir
	triggerDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(triggerDir, "triggers.yaml"), []byte(triggerYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create webhook config dir
	webhookDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(webhookDir, "forgejo.yaml"), []byte(webhookYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	mq := &mockQueue{}
	eng, err := engine.New(engine.Config{
		TriggerDir: triggerDir,
		DataDir:    t.TempDir(),
		Queue:      mq,
	})
	if err != nil {
		t.Fatalf("unexpected error creating engine: %v", err)
	}
	if err := eng.Start(); err != nil {
		t.Fatalf("unexpected error starting engine: %v", err)
	}

	providers, err := webhook.LoadProviderConfigs(webhookDir)
	if err != nil {
		t.Fatalf("unexpected error loading webhook configs: %v", err)
	}

	return eng, mq, providers
}

// setupTestEnvNoStart creates the engine and providers without starting
// the engine (so periodic triggers don't fire). Useful for webhook-only tests.
func setupTestEnvNoStart(t *testing.T, triggerYAML, webhookYAML string) (*engine.Engine, *mockQueue, []webhook.ProviderConfig) {
	t.Helper()

	triggerDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(triggerDir, "triggers.yaml"), []byte(triggerYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	webhookDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(webhookDir, "forgejo.yaml"), []byte(webhookYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	mq := &mockQueue{}
	eng, err := engine.New(engine.Config{
		TriggerDir: triggerDir,
		DataDir:    t.TempDir(),
		Queue:      mq,
	})
	if err != nil {
		t.Fatalf("unexpected error creating engine: %v", err)
	}
	// Don't start the engine — only webhook-triggered tasks

	providers, err := webhook.LoadProviderConfigs(webhookDir)
	if err != nil {
		t.Fatalf("unexpected error loading webhook configs: %v", err)
	}

	return eng, mq, providers
}

func TestHandler_WebhookPost(t *testing.T) {
	triggerYAML := `
triggers:
  - id: "pr-review"
    type: periodic
    schedule: "@every 1h"
    prompt: "Review PR: {{ .Payload.pull_request.title }}"
`
	webhookYAML := `
endpoints:
  - name: "pull_request"
    trigger_id: "pr-review"
    response: '{"status": "submitted"}'
`

	eng, mq, providers := setupTestEnv(t, triggerYAML, webhookYAML)
	defer eng.Stop()

	st := eng.StateStore()
	handler := webhook.NewHandler(eng, st, "test", providers)

	payload := map[string]interface{}{
		"pull_request": map[string]interface{}{
			"title":  "Fix the bug",
			"number": 42,
		},
	}
	payloadBytes, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/v1/forgejo/pull_request", strings.NewReader(string(payloadBytes)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ServeMux().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d: %s", w.Code, w.Body.String())
	}

	if mq.Count() < 1 {
		t.Errorf("expected at least 1 task, got %d", mq.Count())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if resp["status"] != "submitted" {
		t.Errorf("expected status 'submitted', got %v", resp["status"])
	}
}

func TestHandler_WebhookPost_WithLastRun(t *testing.T) {
	// Test that .LastRun is available alongside .Payload
	triggerYAML := `
triggers:
  - id: "pr-review"
    type: periodic
    schedule: "@every 1h"
    prompt: "Review PR {{ .Payload.pull_request.title }} since {{ .LastRun }}"
`
	webhookYAML := `
endpoints:
  - name: "pull_request"
    trigger_id: "pr-review"
    response: '{"status": "submitted"}'
`

	eng, mq, providers := setupTestEnv(t, triggerYAML, webhookYAML)
	defer eng.Stop()

	st := eng.StateStore()
	handler := webhook.NewHandler(eng, st, "test", providers)

	// First call to establish state
	payload := map[string]interface{}{"pull_request": map[string]interface{}{"title": "First PR"}}
	payloadBytes, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/v1/forgejo/pull_request", strings.NewReader(string(payloadBytes)))
	w := httptest.NewRecorder()
	handler.ServeMux().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("first call failed: %d", w.Code)
	}

	// Second call — should have a LastRun from the first
	time.Sleep(10 * time.Millisecond)
	payload2 := map[string]interface{}{"pull_request": map[string]interface{}{"title": "Second PR"}}
	payloadBytes2, _ := json.Marshal(payload2)

	req2 := httptest.NewRequest(http.MethodPost, "/v1/forgejo/pull_request", strings.NewReader(string(payloadBytes2)))
	w2 := httptest.NewRecorder()
	handler.ServeMux().ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("second call failed: %d", w2.Code)
	}

	// 2 webhook calls = 2 tasks
	if mq.Count() != 2 {
		t.Fatalf("expected 2 tasks (2 webhooks), got %d", mq.Count())
	}

	// Check that the second webhook task (index 1) has both payload and lastRun
	task := mq.Tasks()[1]
	if !strings.Contains(task.Prompt, "Second PR") {
		t.Errorf("expected prompt to contain 'Second PR', got %q", task.Prompt)
	}
	if !strings.Contains(task.Prompt, "since") {
		t.Errorf("expected prompt to contain 'since', got %q", task.Prompt)
	}
	if !strings.Contains(task.Prompt, "T") {
		t.Errorf("expected prompt to contain timestamp (RFC3339), got %q", task.Prompt)
	}

	// Check the first task was dispatched
	task1 := mq.Tasks()[0]
	if !strings.Contains(task1.Prompt, "First PR") {
		t.Errorf("expected prompt to contain 'First PR', got %q", task1.Prompt)
	}
}

func TestHandler_WebhookInvalidMethod(t *testing.T) {
	triggerYAML := `
triggers:
  - id: "test"
    type: periodic
    schedule: "@every 1h"
    prompt: "test"
`
	webhookYAML := `
endpoints:
  - name: "ep"
    trigger_id: "test"
    response: '{"status": "submitted"}'
`

	eng, _, providers := setupTestEnv(t, triggerYAML, webhookYAML)
	defer eng.Stop()

	st := eng.StateStore()
	handler := webhook.NewHandler(eng, st, "test", providers)

	req := httptest.NewRequest(http.MethodGet, "/v1/forgejo/ep", nil)
	w := httptest.NewRecorder()
	handler.ServeMux().ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected status 405, got %d", w.Code)
	}
}

func TestHandler_WebhookUnknownProvider(t *testing.T) {
	triggerYAML := `
triggers:
  - id: "test"
    type: periodic
    schedule: "@every 1h"
    prompt: "test"
`
	webhookYAML := `
endpoints:
  - name: "ep"
    trigger_id: "test"
    response: '{"status": "submitted"}'
`

	eng, _, providers := setupTestEnv(t, triggerYAML, webhookYAML)
	defer eng.Stop()

	st := eng.StateStore()
	handler := webhook.NewHandler(eng, st, "test", providers)

	req := httptest.NewRequest(http.MethodPost, "/v1/unknown/ep", strings.NewReader("{}"))
	w := httptest.NewRecorder()
	handler.ServeMux().ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", w.Code)
	}
}

func TestHandler_WebhookUnknownEndpoint(t *testing.T) {
	triggerYAML := `
triggers:
  - id: "test"
    type: periodic
    schedule: "@every 1h"
    prompt: "test"
`
	webhookYAML := `
endpoints:
  - name: "ep"
    trigger_id: "test"
    response: '{"status": "submitted"}'
`

	eng, _, providers := setupTestEnv(t, triggerYAML, webhookYAML)
	defer eng.Stop()

	st := eng.StateStore()
	handler := webhook.NewHandler(eng, st, "test", providers)

	req := httptest.NewRequest(http.MethodPost, "/v1/forgejo/unknown", strings.NewReader("{}"))
	w := httptest.NewRecorder()
	handler.ServeMux().ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", w.Code)
	}
}

func TestHandler_WebhookInvalidJSON(t *testing.T) {
	triggerYAML := `
triggers:
  - id: "test"
    type: periodic
    schedule: "@every 1h"
    prompt: "test"
`
	webhookYAML := `
endpoints:
  - name: "ep"
    trigger_id: "test"
    response: '{"status": "submitted"}'
`

	eng, _, providers := setupTestEnv(t, triggerYAML, webhookYAML)
	defer eng.Stop()

	st := eng.StateStore()
	handler := webhook.NewHandler(eng, st, "test", providers)

	req := httptest.NewRequest(http.MethodPost, "/v1/forgejo/ep", strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeMux().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", w.Code)
	}
}

func TestHandler_WebhookMissingTrigger(t *testing.T) {
	triggerYAML := `
triggers:
  - id: "exists"
    type: periodic
    schedule: "@every 1h"
    prompt: "test"
`
	webhookYAML := `
endpoints:
  - name: "ep"
    trigger_id: "nonexistent"
    response: '{"status": "submitted"}'
`

	eng, _, providers := setupTestEnv(t, triggerYAML, webhookYAML)
	defer eng.Stop()

	st := eng.StateStore()
	handler := webhook.NewHandler(eng, st, "test", providers)

	req := httptest.NewRequest(http.MethodPost, "/v1/forgejo/ep", strings.NewReader("{}"))
	w := httptest.NewRecorder()
	handler.ServeMux().ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", w.Code)
	}
}

func TestHandler_WebhookDispatchesTask(t *testing.T) {
	triggerYAML := `
triggers:
  - id: "pr-review"
    type: periodic
    schedule: "@every 1h"
    prompt: "Review PR: {{ .Payload.pull_request.title }}"
    tags:
      - "code-review"
    timeout: 1800
`
	webhookYAML := `
endpoints:
  - name: "pull_request"
    trigger_id: "pr-review"
    response: '{"status": "submitted"}'
`

	eng, mq, providers := setupTestEnvNoStart(t, triggerYAML, webhookYAML)
	defer eng.Stop()

	st := eng.StateStore()
	handler := webhook.NewHandler(eng, st, "test", providers)

	payload := map[string]interface{}{
		"pull_request": map[string]interface{}{
			"title": "Add webhook support",
		},
	}
	payloadBytes, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/v1/forgejo/pull_request", strings.NewReader(string(payloadBytes)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeMux().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	if mq.Count() != 1 {
		t.Fatalf("expected exactly 1 task, got %d", mq.Count())
	}

	task := mq.Tasks()[0]
	expectedPrompt := "Review PR: Add webhook support"
	if task.Prompt != expectedPrompt {
		t.Errorf("expected prompt %q, got %q", expectedPrompt, task.Prompt)
	}
	if task.Timeout != 1800 {
		t.Errorf("expected timeout 1800, got %d", task.Timeout)
	}
	if len(task.Tags) != 1 || task.Tags[0] != "code-review" {
		t.Errorf("unexpected tags: %v", task.Tags)
	}
}

func TestHandler_WebhookPreservesPayloadStructure(t *testing.T) {
	// Test that nested payload fields are accessible
	triggerYAML := `
triggers:
  - id: "pr-review"
    type: periodic
    schedule: "@every 1h"
    prompt: "PR #{{ .Payload.pull_request.number }}: {{ .Payload.pull_request.title }} by {{ .Payload.sender.login }}"
`
	webhookYAML := `
endpoints:
  - name: "pull_request"
    trigger_id: "pr-review"
    response: '{"status": "submitted"}'
`

	eng, mq, providers := setupTestEnvNoStart(t, triggerYAML, webhookYAML)
	defer eng.Stop()

	st := eng.StateStore()
	handler := webhook.NewHandler(eng, st, "test", providers)

	payload := map[string]interface{}{
		"pull_request": map[string]interface{}{
			"number": 123,
			"title":  "Fix critical bug",
		},
		"sender": map[string]interface{}{
			"login": "developer1",
		},
	}
	payloadBytes, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/v1/forgejo/pull_request", strings.NewReader(string(payloadBytes)))
	w := httptest.NewRecorder()
	handler.ServeMux().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	task := mq.Tasks()[0]
	expected := "PR #123: Fix critical bug by developer1"
	if task.Prompt != expected {
		t.Errorf("expected %q, got %q", expected, task.Prompt)
	}
}

func TestHandler_WebhookEmptyPayload(t *testing.T) {
	// Test with an empty JSON object
	triggerYAML := `
triggers:
  - id: "test"
    type: periodic
    schedule: "@every 1h"
    prompt: "Process {{ .Payload.event_type }}"
`
	webhookYAML := `
endpoints:
  - name: "ep"
    trigger_id: "test"
    response: '{"status": "submitted"}'
`

	eng, mq, providers := setupTestEnvNoStart(t, triggerYAML, webhookYAML)
	defer eng.Stop()

	st := eng.StateStore()
	handler := webhook.NewHandler(eng, st, "test", providers)

	req := httptest.NewRequest(http.MethodPost, "/v1/forgejo/ep", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeMux().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	if mq.Count() != 1 {
		t.Fatalf("expected 1 task, got %d", mq.Count())
	}

	// The prompt should contain "Process " with an empty event_type
	task := mq.Tasks()[0]
	if !strings.Contains(task.Prompt, "Process ") {
		t.Errorf("expected prompt to contain 'Process ', got %q", task.Prompt)
	}
}

func TestHandler_WebhookStatePersists(t *testing.T) {
	// Test that state is persisted after webhook execution
	triggerYAML := `
triggers:
  - id: "test"
    type: periodic
    schedule: "@every 1h"
    prompt: "test"
`
	webhookYAML := `
endpoints:
  - name: "ep"
    trigger_id: "test"
    response: '{"status": "submitted"}'
`

	eng, _, providers := setupTestEnvNoStart(t, triggerYAML, webhookYAML)
	defer eng.Stop()

	st := eng.StateStore()
	handler := webhook.NewHandler(eng, st, "test", providers)

	// Fire webhook
	req := httptest.NewRequest(http.MethodPost, "/v1/forgejo/ep", strings.NewReader("{}"))
	w := httptest.NewRecorder()
	handler.ServeMux().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	// Check state was persisted
	s, err := st.Load("test")
	if err != nil {
		t.Fatalf("expected state to exist: %v", err)
	}
	if s.LastRun.IsZero() {
		t.Error("expected lastRun to be set")
	}
}

func TestHandler_WebhookHistoryRecorded(t *testing.T) {
	// Test that history is recorded after webhook execution
	triggerYAML := `
triggers:
  - id: "test"
    type: periodic
    schedule: "@every 1h"
    prompt: "test"
`
	webhookYAML := `
endpoints:
  - name: "ep"
    trigger_id: "test"
    response: '{"status": "submitted"}'
`

	eng, _, providers := setupTestEnvNoStart(t, triggerYAML, webhookYAML)
	defer eng.Stop()

	st := eng.StateStore()
	handler := webhook.NewHandler(eng, st, "test", providers)

	req := httptest.NewRequest(http.MethodPost, "/v1/forgejo/ep", strings.NewReader("{}"))
	w := httptest.NewRecorder()
	handler.ServeMux().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	// Check history
	history := eng.History()
	records := history.All()
	recs, ok := records["test"]
	if !ok || len(recs) == 0 {
		t.Fatal("expected history record for test trigger")
	}
	if !recs[0].Success {
		t.Error("expected history record to be successful")
	}
	if recs[0].TaskID == "" {
		t.Error("expected history record to have task ID")
	}
}

func TestHandler_ListWebhooks(t *testing.T) {
	triggerYAML := `
triggers:
  - id: "test"
    type: periodic
    schedule: "@every 1h"
    prompt: "test"
`
	webhookYAML := `
endpoints:
  - name: "ep1"
    trigger_id: "test"
    response: '{"status": "submitted"}'
  - name: "ep2"
    trigger_id: "test"
    response: '{"status": "submitted"}'
`

	eng, _, providers := setupTestEnv(t, triggerYAML, webhookYAML)
	defer eng.Stop()

	st := eng.StateStore()
	handler := webhook.NewHandler(eng, st, "test", providers)

	req := httptest.NewRequest(http.MethodGet, "/api/webhooks", nil)
	w := httptest.NewRecorder()
	handler.ServeMux().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var result []struct {
		Provider  string `json:"provider"`
		Endpoints []struct {
			Name      string `json:"name"`
			TriggerID string `json:"trigger_id"`
		} `json:"endpoints"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if len(result) != 1 {
		t.Fatalf("expected 1 provider, got %d", len(result))
	}
	if result[0].Provider != "forgejo" {
		t.Errorf("expected provider 'forgejo', got %q", result[0].Provider)
	}
	if len(result[0].Endpoints) != 2 {
		t.Errorf("expected 2 endpoints, got %d", len(result[0].Endpoints))
	}
}

func TestHandler_ListWebhooksInvalidMethod(t *testing.T) {
	webhookDir := t.TempDir()
	webhookYAML := `
endpoints:
  - name: "ep"
    trigger_id: "test"
    response: '{"status": "submitted"}'
`
	if err := os.WriteFile(filepath.Join(webhookDir, "test.yaml"), []byte(webhookYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	triggerDir := t.TempDir()
	configYAML := `
triggers:
  - id: "test"
    type: periodic
    schedule: "@every 1h"
    prompt: "test"
`
	if err := os.WriteFile(filepath.Join(triggerDir, "triggers.yaml"), []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	eng, err := engine.New(engine.Config{
		TriggerDir: triggerDir,
		DataDir:    t.TempDir(),
		Queue:      &mockQueue{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.Start(); err != nil {
		t.Fatal(err)
	}
	defer eng.Stop()

	providers, _ := webhook.LoadProviderConfigs(webhookDir)
	st := eng.StateStore()
	handler := webhook.NewHandler(eng, st, "test", providers)

	req := httptest.NewRequest(http.MethodPost, "/api/webhooks", nil)
	w := httptest.NewRecorder()
	handler.ServeMux().ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected status 405, got %d", w.Code)
	}
}

func TestHandler_WebhookInvalidPath(t *testing.T) {
	triggerYAML := `
triggers:
  - id: "test"
    type: periodic
    schedule: "@every 1h"
    prompt: "test"
`
	webhookYAML := `
endpoints:
  - name: "ep"
    trigger_id: "test"
    response: '{"status": "submitted"}'
`

	eng, _, providers := setupTestEnv(t, triggerYAML, webhookYAML)
	defer eng.Stop()

	st := eng.StateStore()
	handler := webhook.NewHandler(eng, st, "test", providers)

	// Missing endpoint name
	req := httptest.NewRequest(http.MethodPost, "/v1/forgejo", strings.NewReader("{}"))
	w := httptest.NewRecorder()
	handler.ServeMux().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", w.Code)
	}
}

func TestHandler_WebhookMultipleProviders(t *testing.T) {
	// Test with two different provider files
	triggerYAML := `
triggers:
  - id: "forgejo-trigger"
    type: periodic
    schedule: "@every 1h"
    prompt: "Forgejo: {{ .Payload.action }}"
  - id: "github-trigger"
    type: periodic
    schedule: "@every 1h"
    prompt: "GitHub: {{ .Payload.action }}"
`

	// Create two webhook config files
	webhookDir := t.TempDir()

	forgejoYAML := `
endpoints:
  - name: "events"
    trigger_id: "forgejo-trigger"
    response: '{"status": "submitted"}'
`
	githubYAML := `
endpoints:
  - name: "push"
    trigger_id: "github-trigger"
    response: '{"status": "submitted"}'
`
	if err := os.WriteFile(filepath.Join(webhookDir, "forgejo.yaml"), []byte(forgejoYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(webhookDir, "github.yaml"), []byte(githubYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	eng, mq, providers := setupTestEnvNoStart(t, triggerYAML, "")
	// Reload with the actual webhook dir
	providers, _ = webhook.LoadProviderConfigs(webhookDir)
	defer eng.Stop()

	st := eng.StateStore()
	handler := webhook.NewHandler(eng, st, "test", providers)

	// Fire forgejo webhook
	forgejoPayload := map[string]interface{}{"action": "opened"}
	payloadBytes, _ := json.Marshal(forgejoPayload)

	req := httptest.NewRequest(http.MethodPost, "/v1/forgejo/events", strings.NewReader(string(payloadBytes)))
	w := httptest.NewRecorder()
	handler.ServeMux().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("forgejo webhook failed: %d", w.Code)
	}

	// Fire github webhook
	githubPayload := map[string]interface{}{"action": "push"}
	payloadBytes2, _ := json.Marshal(githubPayload)

	req2 := httptest.NewRequest(http.MethodPost, "/v1/github/push", strings.NewReader(string(payloadBytes2)))
	w2 := httptest.NewRecorder()
	handler.ServeMux().ServeHTTP(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("github webhook failed: %d", w2.Code)
	}

	if mq.Count() != 2 {
		t.Fatalf("expected 2 tasks, got %d", mq.Count())
	}

	// Check that both triggers fired correctly
	foundForgejo := false
	foundGitHub := false
	for _, task := range mq.Tasks() {
		if strings.HasPrefix(task.Prompt, "Forgejo:") {
			foundForgejo = true
		}
		if strings.HasPrefix(task.Prompt, "GitHub:") {
			foundGitHub = true
		}
	}
	if !foundForgejo {
		t.Error("expected to find forgejo task")
	}
	if !foundGitHub {
		t.Error("expected to find github task")
	}
}

func TestHandler_WebhookResponseTemplate(t *testing.T) {
	// Test that the configured response template is returned
	triggerYAML := `
triggers:
  - id: "test"
    type: periodic
    schedule: "@every 1h"
    prompt: "test"
`
	webhookYAML := `
endpoints:
  - name: "ep"
    trigger_id: "test"
    response: '{"status": "submitted", "received_at": "now"}'
`

	eng, _, providers := setupTestEnvNoStart(t, triggerYAML, webhookYAML)
	defer eng.Stop()

	st := eng.StateStore()
	handler := webhook.NewHandler(eng, st, "test", providers)

	req := httptest.NewRequest(http.MethodPost, "/v1/forgejo/ep", strings.NewReader("{}"))
	w := httptest.NewRecorder()
	handler.ServeMux().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if resp["status"] != "submitted" {
		t.Errorf("expected status 'submitted', got %v", resp["status"])
	}
	if resp["received_at"] != "now" {
		t.Errorf("expected received_at 'now', got %v", resp["received_at"])
	}
}

func TestHandler_WebhookWithStateStore(t *testing.T) {
	// Verify the state store passed to the handler is the same one used by the engine
	triggerYAML := `
triggers:
  - id: "test"
    type: periodic
    schedule: "@every 1h"
    prompt: "test"
`
	webhookYAML := `
endpoints:
  - name: "ep"
    trigger_id: "test"
    response: '{"status": "submitted"}'
`

	eng, _, providers := setupTestEnvNoStart(t, triggerYAML, webhookYAML)
	defer eng.Stop()

	st := eng.StateStore()
	handler := webhook.NewHandler(eng, st, "test", providers)

	// Fire webhook
	req := httptest.NewRequest(http.MethodPost, "/v1/forgejo/ep", strings.NewReader("{}"))
	w := httptest.NewRecorder()
	handler.ServeMux().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	// Load state directly from the store (not via engine)
	s, err := st.Load("test")
	if err != nil {
		t.Fatalf("expected state to exist: %v", err)
	}
	if s.LastRun.IsZero() {
		t.Error("expected lastRun to be set")
	}
}
