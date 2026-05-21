// Package web provides an HTTP server for the maitred trigger dashboard.
// It serves the static UI and exposes REST API endpoints for trigger
// management (definitions, history, pause/resume, fire-now).
package web

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"maitred/pkg/engine"
	"maitred/pkg/trigger"
	"maitred/pkg/webhook"
)

//go:embed static
var staticFS embed.FS

// Server is the HTTP server for the maitred dashboard.
type Server struct {
	port         int
	log          *log.Logger
	engine       *engine.Engine
	version      string
	webhookProvs []webhook.ProviderConfig
	mux          *http.ServeMux
}

// New creates a new web server.
func New(port int, eng *engine.Engine, version string, webhookProvs ...[]webhook.ProviderConfig) *Server {
	var providers []webhook.ProviderConfig
	if len(webhookProvs) > 0 {
		providers = webhookProvs[0]
	}
	s := &Server{
		port:         port,
		log:          log.Default(),
		engine:       eng,
		version:      version,
		webhookProvs: providers,
		mux:          http.NewServeMux(),
	}
	s.registerRoutes()
	return s
}

// Start begins serving HTTP in a goroutine. Returns immediately.
func (s *Server) Start() error {
	addr := fmt.Sprintf(":%d", s.port)
	s.log.Printf("web dashboard listening on %s", addr)
	go func() {
		_ = http.ListenAndServe(addr, s.mux)
	}()
	return nil
}

// Stop gracefully shuts down the HTTP server.
func (s *Server) Stop() error {
	return nil
}

func (s *Server) registerRoutes() {
	// REST API endpoints
	s.mux.HandleFunc("/api/", s.handleAPI)

	// Static files and SPA fallback
	s.mux.HandleFunc("/", s.handleStatic)
}

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	// Serve static files from the embedded FS
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		path = "static/index.html"
	} else if !strings.HasPrefix(path, "static/") {
		path = "static/" + path
	}

	data, err := fs.ReadFile(staticFS, path)
	if err != nil {
		// SPA fallback: serve index.html for non-file routes
		if r.Method == http.MethodGet {
			index, err := fs.ReadFile(staticFS, "static/index.html")
			if err != nil {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write(index)
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	// Set content type based on file extension
	ext := filepath.Ext(path)
	switch ext {
	case ".html":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	case ".css":
		w.Header().Set("Content-Type", "text/css")
	case ".js":
		w.Header().Set("Content-Type", "application/javascript")
	case ".json":
		w.Header().Set("Content-Type", "application/json")
	case ".png":
		w.Header().Set("Content-Type", "image/png")
	case ".svg":
		w.Header().Set("Content-Type", "image/svg+xml")
	}

	w.Write(data)
}

func (s *Server) handleAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	path := strings.TrimPrefix(r.URL.Path, "/api/")

	switch {
	case path == "definitions":
		s.handleDefinitions(w, r)
	case path == "history":
		s.handleHistory(w, r)
	case path == "version":
		s.handleVersion(w, r)
	case path == "webhooks":
		s.handleWebhooks(w, r)
	case strings.HasPrefix(path, "triggers/"):
		s.handleTriggerAPI(w, r, path)
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func (s *Server) handleDefinitions(w http.ResponseWriter, r *http.Request) {
	defs := s.engine.Definitions()
	if defs == nil {
		defs = []trigger.TriggerDefinition{}
	}
	json.NewEncoder(w).Encode(defs)
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	history := s.engine.History()
	all := history.All()
	if all == nil {
		all = map[string][]engine.ExecutionRecord{}
	}
	json.NewEncoder(w).Encode(all)
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	w.Write([]byte(s.version))
}

func (s *Server) handleWebhooks(w http.ResponseWriter, r *http.Request) {
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

	result := make([]webhookInfo, len(s.webhookProvs))
	for i, p := range s.webhookProvs {
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

func (s *Server) handleTriggerAPI(w http.ResponseWriter, r *http.Request, path string) {
	// Parse path: triggers/{id} or triggers/{id}/pause or triggers/{id}/resume or triggers/{id}/fire
	parts := strings.Split(strings.TrimPrefix(path, "triggers/"), "/")
	if len(parts) < 1 {
		http.Error(w, "trigger ID required", http.StatusBadRequest)
		return
	}

	id := parts[0]

	// GET /triggers/{id} returns trigger status
	if len(parts) == 1 && r.Method == http.MethodGet {
		def := s.findDefinition(id)
		if def == nil {
			http.Error(w, "trigger not found", http.StatusNotFound)
			return
		}
		status := map[string]interface{}{
			"id":     id,
			"paused": s.engine.IsPaused(id),
			"def":    def,
		}
		json.NewEncoder(w).Encode(status)
		return
	}

	if len(parts) < 2 {
		http.Error(w, "action required (pause, resume, fire)", http.StatusBadRequest)
		return
	}

	action := parts[1]

	switch action {
	case "pause":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.engine.PauseTrigger(id)
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "paused", "id": id})

	case "resume":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.engine.ResumeTrigger(id)
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "resumed", "id": id})

	case "fire":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := s.engine.FireNow(id); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "fired", "id": id})

	default:
		http.Error(w, "unknown action: "+action, http.StatusBadRequest)
	}
}

func (s *Server) findDefinition(id string) *trigger.TriggerDefinition {
	for _, d := range s.engine.Definitions() {
		if d.ID == id {
			tmp := d
			return &tmp
		}
	}
	return nil
}

// serveFile serves a file from disk (for development, when not embedded).
func serveFile(dir, path string, w http.ResponseWriter, r *http.Request) {
	fullPath := filepath.Join(dir, path)
	if _, err := os.Stat(fullPath); err != nil {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, fullPath)
}
