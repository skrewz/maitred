// Command maitre-d is the CLI entry point for the periodic trigger engine.
// It loads trigger definitions from a config directory, starts the engine,
// and runs until interrupted.
package main

import (
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
)

// Version is set at build time via ldflags.
var Version = "dev"

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	// Defaults — can be overridden via environment variables
	triggerDir := defaultEnv("MAITRE_D_TRIGGER_DIR", "config/triggers.d")
	dataDir := defaultEnv("MAITRE_D_DATA_DIR", "data")
	queueAddr := defaultEnv("MAITRE_D_QUEUE_ADDR", "http://localhost:8080")

	log.Printf("maitre-d %s starting", Version)
	log.Printf("  trigger dir: %s", triggerDir)
	log.Printf("  data dir:    %s", dataDir)
	log.Printf("  queue addr:  %s", queueAddr)

	// Resolve trigger dir relative to working directory if not absolute
	if !filepath.IsAbs(triggerDir) {
		var err error
		triggerDir, err = filepath.Abs(triggerDir)
		if err != nil {
			log.Fatalf("resolve trigger dir: %v", err)
		}
	}

	// Resolve data dir relative to working directory if not absolute
	if !filepath.IsAbs(dataDir) {
		var err error
		dataDir, err = filepath.Abs(dataDir)
		if err != nil {
			log.Fatalf("resolve data dir: %v", err)
		}
	}

	log.Printf("resolved trigger dir: %s", triggerDir)
	log.Printf("resolved data dir:    %s", dataDir)

	log.Printf("ready")

	// Wait for interrupt signal
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	log.Printf("shutting down")
}

func defaultEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}
