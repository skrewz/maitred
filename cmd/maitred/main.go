// Command maitred is the CLI entry point for the periodic trigger engine.
// It loads trigger definitions from a config directory, starts the engine,
// and runs until interrupted.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"maitred/pkg/engine"
	"maitred/pkg/queue"
	"maitred/pkg/web"
)

// Version is set at build time via ldflags.
var Version = "dev"

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	// CLI flags (override env vars)
	triggerDir := flag.String("trigger-dir", "", "directory containing trigger YAML files")
	dataDir := flag.String("data-dir", "", "directory for persistent trigger state")
	queueAddr := flag.String("queue-addr", "", "target queue system address (for future HTTP adapter)")
	showVersion := flag.Bool("version", false, "print version and exit")
	showHealth := flag.Bool("health", false, "health check mode (exits 0 if config is valid)")
	flag.Parse()

	if *showVersion {
		fmt.Println(Version)
		os.Exit(0)
	}

	if *showHealth {
		if err := healthCheck(*triggerDir, *dataDir); err != nil {
			fmt.Fprintf(os.Stderr, "unhealthy: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("healthy")
		os.Exit(0)
	}

	// Defaults — can be overridden via environment variables
	triggerDirStr := defaultEnv("MAITRE_D_TRIGGER_DIR", "config/triggers.d")
	dataDirStr := defaultEnv("MAITRE_D_DATA_DIR", "data")
	queueAddrStr := defaultEnv("MAITRE_D_QUEUE_ADDR", "http://localhost:8080")
	webPortStr := defaultEnv("MAITRE_D_WEB_PORT", "9090")

	// CLI flags take precedence over env vars
	if *triggerDir != "" {
		triggerDirStr = *triggerDir
	}
	if *dataDir != "" {
		dataDirStr = *dataDir
	}
	if *queueAddr != "" {
		queueAddrStr = *queueAddr
	}

	// Parse web port from env
	webPort := 9090
	if p, err := parsePort(webPortStr); err == nil {
		webPort = p
	}

	log.Printf("maitred %s starting", Version)
	log.Printf("  trigger dir: %s", triggerDirStr)
	log.Printf("  data dir:    %s", dataDirStr)
	log.Printf("  queue addr:  %s", queueAddrStr)
	log.Printf("  web port:    %d", webPort)

	// Resolve trigger dir relative to working directory if not absolute
	if !filepath.IsAbs(triggerDirStr) {
		var err error
		triggerDirStr, err = filepath.Abs(triggerDirStr)
		if err != nil {
			log.Fatalf("resolve trigger dir: %v", err)
		}
	}

	// Resolve data dir relative to working directory if not absolute
	if !filepath.IsAbs(dataDirStr) {
		var err error
		dataDirStr, err = filepath.Abs(dataDirStr)
		if err != nil {
			log.Fatalf("resolve data dir: %v", err)
		}
	}

	log.Printf("resolved trigger dir: %s", triggerDirStr)
	log.Printf("resolved data dir:    %s", dataDirStr)

	// Create the engine (loads and validates config)
	mq := queue.NewTaskQueue()
	eng, err := engine.New(engine.Config{
		TriggerDir: triggerDirStr,
		DataDir:    dataDirStr,
		Queue:      mq,
	})
	if err != nil {
		log.Fatalf("initialize engine: %v", err)
	}

	log.Printf("loaded %d trigger(s)", len(eng.Definitions()))
	for _, def := range eng.Definitions() {
		log.Printf("  %s: %s", def.ID, def.Schedule)
	}

	log.Printf("ready")

	// Start the web dashboard
	webSrv := web.New(webPort, eng, Version)
	if err := webSrv.Start(); err != nil {
		log.Printf("web server error: %v (continuing without dashboard)", err)
	}

	// Start the engine
	if err := eng.Start(); err != nil {
		log.Fatalf("start engine: %v", err)
	}

	// Wait for interrupt signal
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	log.Printf("shutting down")
	eng.Stop()
	webSrv.Stop()
	log.Printf("stopped")
}

// healthCheck validates that trigger and data directories are accessible
// and trigger definitions can be loaded. Used for container HEALTHCHECK.
func healthCheck(triggerDir, dataDir string) error {
	mq := queue.NewTaskQueue()
	_, err := engine.New(engine.Config{
		TriggerDir: triggerDir,
		DataDir:    dataDir,
		Queue:      mq,
	})
	return err
}

func defaultEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}

func parsePort(s string) (int, error) {
	var port int
	_, err := fmt.Sscanf(s, "%d", &port)
	if err != nil || port <= 0 || port > 65535 {
		return 0, fmt.Errorf("invalid port: %s", s)
	}
	return port, nil
}
