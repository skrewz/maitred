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
	"log"
	"net/http"
	"os"
	"text/template"
	"time"
)

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
	//
	// A built-in "json" function marshals values to JSON for safe embedding
	// inside the template. If empty, a default template is used that omits
	// the ID (the remote system is expected to generate its own).
	TaskTemplate string `yaml:"task_template"`
	// LogResponse controls whether the adapter logs the full response body
	// from the remote queue system. Useful for debugging.
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
	"timeout": {{ .Timeout }}
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
	// Inject internal tracking ID into the prompt
	prompt := task.Prompt
	if prompt != "" {
		prompt = prompt + "\n(internal maître d' tracking ID: " + task.ID + ")"
	}

	// Render the task template
	var buf bytes.Buffer
	data := struct {
		ID      string
		Prompt  string
		Tags    []string
		Timeout int
	}{
		ID:      task.ID,
		Prompt:  prompt,
		Tags:    task.Tags,
		Timeout: task.Timeout,
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

	if a.cfg.LogResponse {
		var bodyBuf bytes.Buffer
		_, _ = bodyBuf.ReadFrom(resp.Body)
		a.respLog.Printf("endpoint=%s status=%d body=%s",
			a.cfg.Endpoint, resp.StatusCode, bodyBuf.String())
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("queue adapter: unexpected status %d from %s", resp.StatusCode, a.cfg.Endpoint)
	}

	a.log.Printf("[queue:adapter] task %s dispatched to %s (status: %d)",
		task.ID, a.cfg.Endpoint, resp.StatusCode)

	return nil
}
