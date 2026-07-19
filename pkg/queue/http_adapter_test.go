package queue_test

import (
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"maitred/pkg/queue"
)

// mockServer returns an httptest.Server that captures POSTed JSON
// and returns the given status code with an optional body.
func mockServer(status int, body string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
}

func TestNewHTTPAdapter_RequiresEndpoint(t *testing.T) {
	_, err := queue.NewHTTPAdapter(queue.AdapterConfig{}, nil)
	if err == nil {
		t.Error("expected error for empty endpoint, got nil")
	}
}

func TestNewHTTPAdapter_MissingKey(t *testing.T) {
	tmpDir := t.TempDir()
	certPath := tmpDir + "/cert.pem"
	os.WriteFile(certPath, []byte("cert"), 0o644)

	_, err := queue.NewHTTPAdapter(queue.AdapterConfig{
		Endpoint: "http://example.com",
		MTLSCert: certPath,
		MTLSKey:  "",
	}, nil)
	if err == nil {
		t.Error("expected error when only mtls_cert is set, got nil")
	}
}

func TestNewHTTPAdapter_BadCertPath(t *testing.T) {
	_, err := queue.NewHTTPAdapter(queue.AdapterConfig{
		Endpoint: "http://example.com",
		MTLSCert: "/nonexistent/cert.pem",
		MTLSKey:  "/nonexistent/key.pem",
	}, nil)
	if err == nil {
		t.Error("expected error for nonexistent cert path, got nil")
	}
}

func TestNewHTTPAdapter_BadTemplate(t *testing.T) {
	_, err := queue.NewHTTPAdapter(queue.AdapterConfig{
		Endpoint:     "http://example.com",
		TaskTemplate: "{{ .Nonexistent }",
	}, nil)
	if err == nil {
		t.Error("expected error for invalid template, got nil")
	}
}

func TestHTTPAdapter_AddTask_Success(t *testing.T) {
	server := mockServer(201, `{"id":"remote-1","status":"PENDING"}`)
	defer server.Close()

	logger := log.New(os.Stdout, "", log.LstdFlags)
	adapter, err := queue.NewHTTPAdapter(queue.AdapterConfig{
		Endpoint: server.URL,
	}, logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	task := &queue.Task{
		ID:      "task-1",
		Prompt:  "do something",
		Tags:    []string{"business-default"},
		Timeout: 3600,
	}

	if err := adapter.AddTask(task); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHTTPAdapter_AddTask_FailsOnBadStatus(t *testing.T) {
	server := mockServer(500, `{"error":"internal"}`)
	defer server.Close()

	adapter, err := queue.NewHTTPAdapter(queue.AdapterConfig{
		Endpoint: server.URL,
	}, log.New(os.Stdout, "", log.LstdFlags))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	task := &queue.Task{ID: "task-1", Prompt: "do something"}
	if err := adapter.AddTask(task); err == nil {
		t.Error("expected error for 500 status, got nil")
	}
}

func TestHTTPAdapter_AddTask_InjectsTrackingID(t *testing.T) {
	var receivedBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		receivedBody = string(buf[:n])
		w.WriteHeader(201)
		w.Write([]byte(`{"id":"remote-1"}`))
	}))
	defer server.Close()

	adapter, err := queue.NewHTTPAdapter(queue.AdapterConfig{
		Endpoint: server.URL,
	}, log.New(os.Stdout, "", log.LstdFlags))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	task := &queue.Task{
		ID:     "task-1",
		Prompt: "research something",
	}

	if err := adapter.AddTask(task); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify tracking ID was injected into the prompt
	if !strings.Contains(receivedBody, "task-1") {
		t.Error("expected task ID in request body")
	}
	if !strings.Contains(receivedBody, "internal maître d' tracking ID") {
		t.Error("expected tracking ID annotation in prompt")
	}
}

func TestHTTPAdapter_AddTask_DefaultTemplate(t *testing.T) {
	var received map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dec := json.NewDecoder(r.Body)
		dec.Decode(&received)
		w.WriteHeader(201)
		w.Write([]byte(`{"id":"remote-1"}`))
	}))
	defer server.Close()

	adapter, err := queue.NewHTTPAdapter(queue.AdapterConfig{
		Endpoint: server.URL,
	}, log.New(os.Stdout, "", log.LstdFlags))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	task := &queue.Task{
		ID:      "task-1",
		Prompt:  "research models",
		Tags:    []string{"business-default", "frontend"},
		Timeout: 1800,
	}

	if err := adapter.AddTask(task); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify fields are present
	if _, ok := received["prompt"]; !ok {
		t.Error("expected 'prompt' in request body")
	}
	if _, ok := received["tags"]; !ok {
		t.Error("expected 'tags' in request body")
	}
	if _, ok := received["timeout"]; !ok {
		t.Error("expected 'timeout' in request body")
	}
	// Default template omits the ID (remote system generates it)
	if _, ok := received["id"]; ok {
		t.Error("default template should not include 'id'")
	}
}

func TestHTTPAdapter_AddTask_Persona(t *testing.T) {
	var received map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dec := json.NewDecoder(r.Body)
		dec.Decode(&received)
		w.WriteHeader(201)
		w.Write([]byte(`{"id":"remote-1"}`))
	}))
	defer server.Close()

	adapter, err := queue.NewHTTPAdapter(queue.AdapterConfig{
		Endpoint: server.URL,
	}, log.New(os.Stdout, "", log.LstdFlags))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	task := &queue.Task{
		ID:      "task-1",
		Prompt:  "implement a feature",
		Tags:    []string{"business-default"},
		Timeout: 3600,
		Persona: "s-autonomics-implementer",
	}

	if err := adapter.AddTask(task); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify persona is present in the request body
	if _, ok := received["persona"]; !ok {
		t.Error("expected 'persona' in request body")
	}
	if received["persona"] != "s-autonomics-implementer" {
		t.Errorf("expected persona 's-autonomics-implementer', got %v", received["persona"])
	}
}

func TestHTTPAdapter_AddTask_NoPersona(t *testing.T) {
	var received map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dec := json.NewDecoder(r.Body)
		dec.Decode(&received)
		w.WriteHeader(201)
		w.Write([]byte(`{"id":"remote-1"}`))
	}))
	defer server.Close()

	adapter, err := queue.NewHTTPAdapter(queue.AdapterConfig{
		Endpoint: server.URL,
	}, log.New(os.Stdout, "", log.LstdFlags))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	task := &queue.Task{
		ID:      "task-1",
		Prompt:  "no persona task",
		Tags:    []string{"business-default"},
		Timeout: 1800,
	}

	if err := adapter.AddTask(task); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Persona should be null (empty string marshals to "" in JSON)
	if _, ok := received["persona"]; !ok {
		t.Error("expected 'persona' key in request body")
	}
	if received["persona"] != "" {
		t.Errorf("expected empty persona, got %v", received["persona"])
	}
}

func TestHTTPAdapter_AddTask_CustomTemplate(t *testing.T) {
	var received map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dec := json.NewDecoder(r.Body)
		dec.Decode(&received)
		w.WriteHeader(201)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	adapter, err := queue.NewHTTPAdapter(queue.AdapterConfig{
		Endpoint: server.URL,
		TaskTemplate: `{
			"task_id": "{{ .ID }}",
			"prompt": {{ .Prompt | json }},
			"source": "maitred"
		}`,
	}, log.New(os.Stdout, "", log.LstdFlags))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	task := &queue.Task{
		ID:      "task-1",
		Prompt:  "test prompt",
		Tags:    []string{"custom"},
		Timeout: 600,
	}

	if err := adapter.AddTask(task); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if received["task_id"] != "task-1" {
		t.Errorf("expected task_id 'task-1', got %v", received["task_id"])
	}
	if received["source"] != "maitred" {
		t.Errorf("expected source 'maitred', got %v", received["source"])
	}
}

func TestHTTPAdapter_AddTask_EmptyPrompt(t *testing.T) {
	var receivedBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		receivedBody = string(buf[:n])
		w.WriteHeader(201)
		w.Write([]byte(`{"id":"remote-1"}`))
	}))
	defer server.Close()

	adapter, err := queue.NewHTTPAdapter(queue.AdapterConfig{
		Endpoint: server.URL,
	}, log.New(os.Stdout, "", log.LstdFlags))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	task := &queue.Task{ID: "task-1", Prompt: ""}
	if err := adapter.AddTask(task); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Even with empty prompt, tracking ID should not be appended
	if strings.Contains(receivedBody, "internal maître d' tracking ID") {
		t.Error("expected no tracking ID annotation for empty prompt")
	}
}

func TestHTTPAdapter_AddTask_LogResponse(t *testing.T) {
	server := mockServer(201, `{"id":"remote-1"}`)
	defer server.Close()

	// Use a real logger that captures output
	logger := log.New(os.Stdout, "", log.LstdFlags)
	adapter, err := queue.NewHTTPAdapter(queue.AdapterConfig{
		Endpoint:    server.URL,
		LogResponse: true,
	}, logger)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	task := &queue.Task{ID: "task-1", Prompt: "test"}
	if err := adapter.AddTask(task); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// If we got here without panicking, the test passes
}

func TestHTTPAdapter_AddTask_NilLogger(t *testing.T) {
	server := mockServer(201, `{"id":"remote-1"}`)
	defer server.Close()

	adapter, err := queue.NewHTTPAdapter(queue.AdapterConfig{
		Endpoint: server.URL,
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	task := &queue.Task{ID: "task-1", Prompt: "test"}
	if err := adapter.AddTask(task); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDefaultAdapterConfig(t *testing.T) {
	cfg := queue.DefaultAdapterConfig()
	if cfg.Endpoint != "" {
		t.Errorf("expected empty endpoint, got %q", cfg.Endpoint)
	}
	if cfg.MTLSCert != "" {
		t.Error("expected empty mtls_cert")
	}
	if cfg.MTLSKey != "" {
		t.Error("expected empty mtls_key")
	}
	if cfg.TaskTemplate != "" {
		t.Error("expected empty task_template")
	}
	if cfg.LogResponse {
		t.Error("expected LogResponse false")
	}
}

func TestHTTPAdapter_AddTask_DropInReplacement(t *testing.T) {
	// Verify HTTPAdapter implements TaskQueueProvider interface
	var _ interface{ AddTask(task *queue.Task) error } = (*queue.HTTPAdapter)(nil)
}

func TestHTTPAdapter_AddTask_NoTags(t *testing.T) {
	var receivedBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		receivedBody = string(buf[:n])
		w.WriteHeader(201)
		w.Write([]byte(`{"id":"remote-1"}`))
	}))
	defer server.Close()

	adapter, err := queue.NewHTTPAdapter(queue.AdapterConfig{
		Endpoint: server.URL,
	}, log.New(os.Stdout, "", log.LstdFlags))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	task := &queue.Task{
		ID:      "task-1",
		Prompt:  "minimal task",
		Timeout: 0,
	}

	if err := adapter.AddTask(task); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Tags should be present in the JSON (as null for nil slices)
	// The default template renders nil slices as `null` via json.Marshal
	if !strings.Contains(receivedBody, `"tags"`) {
		t.Error("expected 'tags' key in request body")
	}
	if !strings.Contains(receivedBody, `"prompt"`) {
		t.Error("expected 'prompt' key in request body")
	}
}
