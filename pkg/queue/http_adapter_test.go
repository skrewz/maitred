package queue_test

import (
	"bytes"
	"encoding/json"
	"io"
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

// TestHTTPAdapter_AddTask_FailureCarriesQueueReason: the queue explains a
// refused task in the response body, and a caller that did not opt into
// response logging only has the error to read — so the explanation must
// reach it, bounded and on one line whatever the queue answered with.
func TestHTTPAdapter_AddTask_FailureCarriesQueueReason(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{
			name:   "the queue names what it refused",
			status: 400,
			body:   "persona \"ghost\" not found\n",
			want:   "persona \"ghost\" not found",
		},
		{
			name:   "an unhealthy queue's page stays short and on one line",
			status: 500,
			body:   "<html>\n" + strings.Repeat("body ", 2000) + "\n</html>\n",
			want:   "<html> body",
		},
		{
			name:   "a queue that says nothing says nothing",
			status: 502,
			body:   "",
			want:   "no response body",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := mockServer(tc.status, tc.body)
			defer server.Close()

			adapter, err := queue.NewHTTPAdapter(queue.AdapterConfig{
				Endpoint: server.URL,
			}, log.New(os.Stdout, "", log.LstdFlags))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			err = adapter.AddTask(&queue.Task{ID: "task-1", Prompt: "p"})
			if err == nil {
				t.Fatal("expected error for failed status, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not carry %q", err.Error(), tc.want)
			}
			if strings.Contains(err.Error(), "\n") {
				t.Errorf("error spans lines, which a log line must not: %q", err.Error())
			}
			if len(err.Error()) > 512 {
				t.Errorf("error is %d bytes, want a bounded excerpt", len(err.Error()))
			}
		})
	}
}

func TestHTTPAdapter_AddTask_PassesPromptThroughUnmodified(t *testing.T) {
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

	// The prompt must reach the queue verbatim: no tracking ID annotation,
	// and no task ID anywhere in the default body (the remote system
	// generates its own; the ID lives in maitred's task log).
	if !strings.Contains(receivedBody, `"research something"`) {
		t.Errorf("expected prompt verbatim in request body, got %s", receivedBody)
	}
	if strings.Contains(receivedBody, "tracking ID") {
		t.Errorf("expected no tracking ID annotation in prompt, got %s", receivedBody)
	}
	if strings.Contains(receivedBody, "task-1") {
		t.Errorf("expected no task ID in default request body, got %s", receivedBody)
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

	// No tracking ID annotation is ever appended to the prompt.
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

// TestHTTPAdapter_AddTask_TitleAndURL: a task carrying a title and a
// reference URL sends both on the wire, so a queue system (hotelier) can
// name the work and link back to its origin.
func TestHTTPAdapter_AddTask_TitleAndURL(t *testing.T) {
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
		ID:     "task-1",
		Prompt: "implement the feature",
		Title:  "Issue o/r#7: add a title",
		URL:    "https://forge.example.com/o/r/issues/7",
	}
	if err := adapter.AddTask(task); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if received["title"] != "Issue o/r#7: add a title" {
		t.Errorf("expected title in request body, got %v", received["title"])
	}
	if received["url"] != "https://forge.example.com/o/r/issues/7" {
		t.Errorf("expected url in request body, got %v", received["url"])
	}
}

// TestHTTPAdapter_AddTask_NoTitleOrURL: a task without a title or URL
// produces the body it produces today — no "title"/"url" keys — so an
// older queue system sees an unchanged payload.
func TestHTTPAdapter_AddTask_NoTitleOrURL(t *testing.T) {
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
		Prompt:  "plain task",
		Tags:    []string{"business-default"},
		Timeout: 1800,
	}
	if err := adapter.AddTask(task); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := received["title"]; ok {
		t.Errorf("expected no 'title' key in request body, got %v", received["title"])
	}
	if _, ok := received["url"]; ok {
		t.Errorf("expected no 'url' key in request body, got %v", received["url"])
	}
	// The rest of the body is unchanged.
	if received["prompt"] != "plain task" {
		t.Errorf("expected prompt 'plain task', got %v", received["prompt"])
	}
}

// TestHTTPAdapter_AddTask_DedupKey: the default template sends the
// task's dedup key to the remote queue system, so a queue system with
// de-duplication (hotelier) can squelch a duplicate submission.
func TestHTTPAdapter_AddTask_DedupKey(t *testing.T) {
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
		ID:       "task-1",
		Prompt:   "p",
		DedupKey: "forgejoeng-o/r-issue-7-implement-2026-09-16T13:25:18Z",
	}
	if err := adapter.AddTask(task); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(receivedBody, `"dedup_key": "forgejoeng-o/r-issue-7-implement-2026-09-16T13:25:18Z"`) {
		t.Errorf("expected dedup_key in request body, got %s", receivedBody)
	}
}

// TestHTTPAdapter_AddTask_NoDedupKey: a task without a dedup key still
// sends an (empty) dedup_key — the load-bearing contract for
// trigger-based dispatches, which rely on the queue system treating an
// empty key as "no de-duplication". A template change that drops or
// re-renders the field would break that contract; this test pins it.
func TestHTTPAdapter_AddTask_NoDedupKey(t *testing.T) {
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
		Prompt:  "no dedup key task",
		Tags:    []string{"business-default"},
		Timeout: 1800,
	}

	if err := adapter.AddTask(task); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// dedup_key should be present and empty ("" marshals to "")
	if _, ok := received["dedup_key"]; !ok {
		t.Error("expected 'dedup_key' key in request body")
	}
	if received["dedup_key"] != "" {
		t.Errorf("expected empty dedup_key, got %v", received["dedup_key"])
	}
}

// TestHTTPAdapter_AddTask_NoPriority: a task carrying no priority produces
// the body maitred produces today — byte for byte, and with no "priority"
// key — so the queue's own default tier applies unchanged.
func TestHTTPAdapter_AddTask_NoPriority(t *testing.T) {
	var raw bytes.Buffer
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(&raw, r.Body)
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
		Prompt:  "plain task",
		Tags:    []string{"business-default"},
		Timeout: 1800,
	}
	if err := adapter.AddTask(task); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := "{\n\t\"prompt\": \"plain task\",\n\t\"tags\": [\"business-default\"],\n\t\"timeout\": 1800,\n\t\"persona\": \"\",\n\t\"dedup_key\": \"\"\n}"
	if raw.String() != want {
		t.Errorf("body mismatch\n got: %q\nwant: %q", raw.String(), want)
	}
}

// TestHTTPAdapter_AddTask_Priority: a task carrying a priority forwards it
// verbatim as "priority" on the task-creation request, so the queue serves
// the task above its default tier. An unknown level is the queue's call to
// reject (hotelier answers 400); maitred does not interpret the value.
func TestHTTPAdapter_AddTask_Priority(t *testing.T) {
	var raw bytes.Buffer
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(&raw, r.Body)
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
		ID:       "task-1",
		Prompt:   "vip task",
		Tags:     []string{"business-default"},
		Timeout:  1800,
		Priority: "teacher",
	}
	if err := adapter.AddTask(task); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := "{\n\t\"prompt\": \"vip task\",\n\t\"tags\": [\"business-default\"],\n\t\"timeout\": 1800,\n\t\"persona\": \"\",\n\t\"dedup_key\": \"\",\n\t\"priority\": \"teacher\"\n}"
	if raw.String() != want {
		t.Errorf("body mismatch\n got: %q\nwant: %q", raw.String(), want)
	}
}
