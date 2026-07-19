package webhook

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"maitred/pkg/engine"
	"maitred/pkg/queue"
	"maitred/pkg/state"
	"maitred/pkg/trigger"
)

// Handler routes incoming webhook requests to the appropriate trigger.
type Handler struct {
	log     *log.Logger
	engine  *engine.Engine
	st      *state.Store
	version string
	mux     *http.ServeMux
}

// NewHandler creates a new webhook handler with the given router.
func NewHandler(eng *engine.Engine, st *state.Store, version string, providers []ProviderConfig) *Handler {
	h := &Handler{
		log:     log.Default(),
		engine:  eng,
		st:      st,
		version: version,
		mux:     http.NewServeMux(),
	}

	h.registerRoutes(providers)
	return h
}

func (h *Handler) registerRoutes(providers []ProviderConfig) {
	// /v1/{provider}/{endpoint}
	h.mux.HandleFunc("/v1/", h.handleWebhook(providers))

	// /api/webhooks — list configured webhook endpoints
	h.mux.HandleFunc("/api/webhooks", h.handleListWebhooks(providers))
}

// ServeMux returns the underlying ServeMux so the server can mount it.
func (h *Handler) ServeMux() *http.ServeMux {
	return h.mux
}

func (h *Handler) handleWebhook(providers []ProviderConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Parse path: /v1/{provider}/{endpoint}
		path := strings.TrimPrefix(r.URL.Path, "/v1/")
		parts := strings.SplitN(path, "/", 2)
		if len(parts) < 2 {
			http.Error(w, "invalid path: /v1/{provider}/{endpoint}", http.StatusBadRequest)
			return
		}

		providerName := parts[0]
		endpointName := parts[1]

		// Find the provider config
		var provider *ProviderConfig
		for i := range providers {
			if providers[i].Provider == providerName {
				p := providers[i]
				provider = &p
				break
			}
		}
		if provider == nil {
			http.Error(w, "unknown provider", http.StatusNotFound)
			return
		}

		// Find the endpoint config
		ep := provider.FindEndpoint(endpointName)
		if ep == nil {
			http.Error(w, "unknown endpoint", http.StatusNotFound)
			return
		}

		// Find the trigger definition
		var def *trigger.TriggerDefinition
		for _, d := range h.engine.Definitions() {
			if d.ID == ep.TriggerID {
				tmp := d
				def = &tmp
				break
			}
		}
		if def == nil {
			http.Error(w, fmt.Sprintf("trigger %q not found", ep.TriggerID), http.StatusNotFound)
			return
		}

		// Read the webhook payload
		payloadBytes, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read body", http.StatusBadRequest)
			return
		}
		defer r.Body.Close()

		// Parse payload as JSON
		var payload map[string]interface{}
		if err := json.Unmarshal(payloadBytes, &payload); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}

		// Load previous state
		lastState, err := h.st.Load(ep.TriggerID)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			h.log.Printf("[webhook:%s] failed to load state: %v", ep.Name, err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		var lastRun time.Time
		if lastState != nil {
			lastRun = lastState.LastRun
		}

		// Check hold-off condition before evaluating prompt
		heldOff, err := def.ShouldHoldOff(payload, lastRun)
		if err != nil {
			h.log.Printf("[webhook:%s] failed to evaluate hold-off condition: %v", ep.Name, err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if heldOff {
			h.log.Printf("[webhook:%s] held off by condition for trigger %s", ep.Name, ep.TriggerID)
			w.WriteHeader(http.StatusNoContent)
			return
		}

		// Evaluate prompt template with .LastRun and .Payload
		prompt, err := def.EvalPromptTemplateWith(payload, lastRun)
		if err != nil {
			h.log.Printf("[webhook:%s] failed to evaluate prompt: %v", ep.Name, err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		// Create and dispatch task
		task := &queue.Task{
			ID:      fmt.Sprintf("task-%s-webhook-%d", ep.TriggerID, time.Now().UnixNano()),
			Prompt:  prompt,
			Tags:    def.Tags,
			Timeout: def.Timeout,
		}

		if err := h.engine.Queue().AddTask(task); err != nil {
			h.log.Printf("[webhook:%s] failed to dispatch task: %v", ep.Name, err)
			http.Error(w, "failed to dispatch task", http.StatusInternalServerError)
			return
		}

		h.log.Printf("[webhook:%s] dispatched task %s for trigger %s", ep.Name, task.ID, ep.TriggerID)

		// Update state
		if err := h.st.Save(ep.TriggerID, time.Now(), nil); err != nil {
			h.log.Printf("[webhook:%s] failed to save state: %v", ep.Name, err)
		}

		// Record in history
		h.engine.History().Append(ep.TriggerID, engine.ExecutionRecord{
			Timestamp: time.Now(),
			TaskID:    task.ID,
			Success:   true,
		})

		// Send response
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(ep.Response))
	}
}

func (h *Handler) handleListWebhooks(providers []ProviderConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		type endpointInfo struct {
			Name      string `json:"name"`
			TriggerID string `json:"trigger_id"`
		}

		type webhookInfo struct {
			Provider  string         `json:"provider"`
			Endpoints []endpointInfo `json:"endpoints"`
		}

		result := make([]webhookInfo, len(providers))
		for i, p := range providers {
			eps := make([]endpointInfo, len(p.Endpoints))
			for j, ep := range p.Endpoints {
				eps[j] = endpointInfo{
					Name:      ep.Name,
					TriggerID: ep.TriggerID,
				}
			}
			result[i] = webhookInfo{
				Provider:  p.Provider,
				Endpoints: eps,
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	}
}
