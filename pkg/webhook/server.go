// Package webhook provides an HTTP server for receiving webhook events
// (e.g. from Forgejo) and dispatching them to maitred triggers.
//
// The server listens on a separate port from the web dashboard and
// routes requests to /v1/{provider}/{endpoint} where {provider} is
// derived from the webhook config YAML filename and {endpoint} is
// defined in that file.
package webhook

import (
	"fmt"
	"log"
	"net/http"

	"maitred/pkg/engine"
	"maitred/pkg/state"
)

// Server is the HTTP server for the maitred webhook API.
type Server struct {
	port       int
	log        *log.Logger
	handler    *Handler
	mux        *http.ServeMux
	httpServer *http.Server
}

// New creates a new webhook server.
func New(port int, eng *engine.Engine, st *state.Store, version string, providers []ProviderConfig) *Server {
	mux := http.NewServeMux()

	handler := NewHandler(eng, st, version, providers)
	mux.Handle("/", handler.ServeMux())

	s := &Server{
		port:    port,
		log:     log.Default(),
		handler: handler,
		mux:     mux,
	}
	return s
}

// Start begins serving HTTP in a goroutine. Returns immediately.
func (s *Server) Start() error {
	addr := fmt.Sprintf(":%d", s.port)
	s.log.Printf("webhook API listening on %s", addr)

	s.httpServer = &http.Server{
		Addr:    addr,
		Handler: s.mux,
	}

	go func() {
		_ = s.httpServer.ListenAndServe()
	}()

	return nil
}

// Stop gracefully shuts down the HTTP server.
func (s *Server) Stop() error {
	if s.httpServer != nil {
		return s.httpServer.Close()
	}
	return nil
}
