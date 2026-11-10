// Package queue provides an in-memory task queue and the TaskQueue interface
// that any downstream queue system can implement to receive tasks from the
// maitred trigger engine.
package queue

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"text/template"
	"time"
)

// maxReadResponseBytes bounds how much of the queue's response body the
// adapter reads at all. The body is read where it is needed — the response
// log, and the reason a failed enqueue carries — never buffered on a
// successful dispatch nobody is inspecting; the cap bounds a misconfigured
// endpoint that answers a POST with a large page.
const maxReadResponseBytes = 64 << 10

// maxErrorBodyBytes bounds how much of that body reaches an error value,
// and through it a log line written by a caller that never opted into
// response logging. The queue's own explanations are single sentences; a
// longer body is a page rather than a reason.
const maxErrorBodyBytes = 256

// AdapterConfig holds the configuration for the HTTP queue adapter.
type AdapterConfig struct {
	// Endpoint is the URL of the remote queue system.
	Endpoint string `yaml:"endpoint"`
	// MTLSCert is the path to the TLS client certificate.
	// When set together with MTLSSKey, mTLS authentication is used.
	MTLSCert string `yaml:"mtls_cert"`
	// MTLSSKey is the path to the TLS client private key.
	// Must be set together with MTLSCert.
	MTLSKey string `yaml:"mtls_key"`
	// TaskTemplate is a Go text/template string that produces the JSON body
	// sent to the remote queue system. Available fields:
	//   .ID        — task ID (UUID)
	//   .Prompt    — evaluated prompt string
	//   .Tags      — capability tag slice
	//   .Timeout   — task timeout in seconds
	//   .Persona   — persona name (optional, for hotelier persona support)
	//   .DedupKey  — dedup key (optional; a queue system with
	//                de-duplication squelches a submission matching a
	//                still-pending task's key)
	//   .Title     — human-readable task title (optional)
	//   .URL       — reference URL for the work (optional)
	//   .Priority  — queue tier the task should be served at (optional;
	//                forwarded verbatim, and the default template emits it
	//                only when set, so an unset priority leaves the body
	//                unchanged and the queue's own default tier applies)
	//
	// A built-in "json" function marshals values to JSON for safe embedding
	// inside the template. If empty, a default template is used that omits
	// the ID (the remote system is expected to generate its own).
	TaskTemplate string `yaml:"task_template"`
	// LogResponse controls whether the adapter logs the queue's response
	// body, up to maxReadResponseBytes, through the response logger.
	// Useful for debugging; a failed enqueue carries a shorter excerpt of
	// the body in its error whatever this is set to.
	LogResponse bool `yaml:"log_response"`
}

// DefaultAdapterConfig returns an AdapterConfig with sensible defaults.
func DefaultAdapterConfig() AdapterConfig {
	return AdapterConfig{
		Endpoint:     "",
		MTLSCert:     "",
		MTLSKey:      "",
		TaskTemplate: "", // empty → use built-in default
		LogResponse:  false,
	}
}

// TaskTemplateFuncs provides template functions for the task template.
var TaskTemplateFuncs = template.FuncMap{
	"json": func(v interface{}) string {
		data, err := json.Marshal(v)
		if err != nil {
			return "[]"
		}
		return string(data)
	},
}

// defaultTaskTemplate is used when no custom template is provided.
// The remote system is expected to auto-generate the task ID.
const defaultTaskTemplate = `{
	"prompt": {{ .Prompt | json }},
	"tags": {{ .Tags | json }},
	"timeout": {{ .Timeout }},
	"persona": {{ .Persona | json }},
	"dedup_key": {{ .DedupKey | json }}{{ if .Title }},
	"title": {{ .Title | json }}{{ end }}{{ if .URL }},
	"url": {{ .URL | json }}{{ end }}{{ if .Priority }},
	"priority": {{ .Priority | json }}{{ end }}
}`

// HTTPAdapter is a TaskQueueProvider that sends tasks to a remote
// queue system via HTTP POST. It uses mTLS when configured, and renders
// the task body from a configurable Go text/template.
type HTTPAdapter struct {
	cfg     AdapterConfig
	client  *http.Client
	tmpl    *template.Template
	log     *log.Logger
	respLog *log.Logger
}

// NewHTTPAdapter creates a new HTTP queue adapter.
//
// The adapter builds an HTTP client with optional mTLS from the config,
// parses the task template, and returns the ready-to-use adapter.
//
// Returns an error if the config is invalid, TLS certs cannot be loaded,
// or the template cannot be parsed.
func NewHTTPAdapter(cfg AdapterConfig, logger *log.Logger) (*HTTPAdapter, error) {
	if cfg.Endpoint == "" {
		return nil, fmt.Errorf("queue adapter: endpoint is required")
	}

	// Build HTTP client with optional mTLS
	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	if cfg.MTLSCert != "" && cfg.MTLSKey != "" {
		cert, err := tls.LoadX509KeyPair(cfg.MTLSCert, cfg.MTLSKey)
		if err != nil {
			return nil, fmt.Errorf("queue adapter: load mTLS cert/key: %w", err)
		}
		client.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{
				Certificates: []tls.Certificate{cert},
				RootCAs:      nil, // trust system CAs
			},
		}
	} else if cfg.MTLSCert != "" || cfg.MTLSKey != "" {
		return nil, fmt.Errorf("queue adapter: both mtls_cert and mtls_key must be set together")
	}

	// Resolve task template
	tmplStr := cfg.TaskTemplate
	if tmplStr == "" {
		tmplStr = defaultTaskTemplate
	}

	tmpl, err := template.New("task").Funcs(TaskTemplateFuncs).Parse(tmplStr)
	if err != nil {
		return nil, fmt.Errorf("queue adapter: parse task template: %w", err)
	}

	var respLog *log.Logger
	if cfg.LogResponse {
		respLog = log.New(os.Stdout, "[maitred:queue-response] ", log.LstdFlags|log.Lmicroseconds)
	}

	// Use a discard logger if nil is provided
	logLogger := logger
	if logLogger == nil {
		logLogger = log.New(os.Stdout, "", log.LstdFlags)
	}

	return &HTTPAdapter{
		cfg:     cfg,
		client:  client,
		tmpl:    tmpl,
		log:     logLogger,
		respLog: respLog,
	}, nil
}

// AddTask sends a task to the remote queue system via HTTP POST.
// It renders the task template with the task's fields and POSTs the
// resulting JSON to the configured endpoint.
func (a *HTTPAdapter) AddTask(task *Task) error {
	// The prompt is passed through verbatim. The task ID is not injected
	// into the prompt text: prompt-template expansion in the downstream
	// agent consumes the whole message (so an appended suffix would be
	// silently discarded and could pollute positional-argument parsing).
	// The task ID remains available in maitred's task log and in the
	// structured payload's ID field.

	// Render the task template
	var buf bytes.Buffer
	data := struct {
		ID       string
		Prompt   string
		Tags     []string
		Timeout  int
		Persona  string
		DedupKey string
		Title    string
		URL      string
		Priority string
	}{
		ID:       task.ID,
		Prompt:   task.Prompt,
		Tags:     task.Tags,
		Timeout:  task.Timeout,
		Persona:  task.Persona,
		DedupKey: task.DedupKey,
		Title:    task.Title,
		URL:      task.URL,
		Priority: task.Priority,
	}

	if err := a.tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("queue adapter: render template: %w", err)
	}

	// POST to the remote endpoint
	req, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		a.cfg.Endpoint,
		&buf,
	)
	if err != nil {
		return fmt.Errorf("queue adapter: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("queue adapter: POST %s: %w", a.cfg.Endpoint, err)
	}
	defer resp.Body.Close()

	// The body is read only where something is made of it: the response
	// log when it is on, and the reason a failed request carries.
	var (
		body    []byte
		readErr error
	)
	if a.cfg.LogResponse || failedStatus(resp.StatusCode) {
		body, readErr = io.ReadAll(io.LimitReader(resp.Body, maxReadResponseBytes))
	}

	if a.cfg.LogResponse {
		a.respLog.Printf("endpoint=%s status=%d body=%s",
			a.cfg.Endpoint, resp.StatusCode, string(body))
	}

	if failedStatus(resp.StatusCode) {
		return fmt.Errorf("queue adapter: unexpected status %d from %s: %s",
			resp.StatusCode, a.cfg.Endpoint, failureReason(body, readErr))
	}

	a.log.Printf("[queue:adapter] task %s dispatched to %s (status: %d)",
		task.ID, a.cfg.Endpoint, resp.StatusCode)

	return nil
}

// failedStatus reports whether a response status means the queue did not
// accept the task.
func failedStatus(status int) bool {
	return status < 200 || status >= 300
}

// failureReason is why the queue said no, as far as the adapter can say it.
// The queue writes its own explanation into the body — a refused persona, a
// malformed task — so a failed enqueue without it leaves the operator a
// status code and an endpoint. The excerpt is short, and whitespace — the
// newlines of an HTML error page included — is folded to spaces so the log
// line the caller writes stays one line; the full body is the response
// log's, under log_response.
func failureReason(body []byte, readErr error) string {
	switch {
	case readErr != nil:
		// A body that could not be read is itself worth naming, rather
		// than quietly reporting an empty one.
		return fmt.Sprintf("response body unreadable: %v", readErr)
	case strings.TrimSpace(string(body)) == "":
		return "no response body"
	default:
		if len(body) > maxErrorBodyBytes {
			body = body[:maxErrorBodyBytes]
		}
		return strings.Join(strings.Fields(string(body)), " ")
	}
}
