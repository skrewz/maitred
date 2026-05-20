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

	"gopkg.in/yaml.v3"

	"maitred/pkg/engine"
	"maitred/pkg/queue"
	"maitred/pkg/web"
	"maitred/pkg/webhook"
)

// Version is set at build time via ldflags.
var Version = "dev"

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	// CLI flags (override env vars)
	triggerDir := flag.String("trigger-dir", "", "directory containing trigger YAML files")
	dataDir := flag.String("data-dir", "", "directory for persistent trigger state")
	queueConfig := flag.String("queue-config", "", "path to queue adapter YAML config (enables HTTP queue adapter)")
	showVersion := flag.Bool("version", false, "print version and exit")
	showHealth := flag.Bool("health", false, "health check mode (exits 0 if config is valid)")
	webPort := flag.Int("web-port", 0, "port for the web dashboard (default from MAITRE_D_WEB_PORT)")
	apiPort := flag.Int("api-port", 0, "port for the webhook API (default from MAITRE_D_API_PORT)")
	webhookDirFlag := flag.String("webhook-dir", "", "directory containing webhook endpoint YAML files")
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
	queueConfigStr := defaultEnv("MAITRE_D_QUEUE_CONFIG", "")
	webPortStr := defaultEnv("MAITRE_D_WEB_PORT", "9090")
	apiPortStr := defaultEnv("MAITRE_D_API_PORT", "9091")
	webhookDirStr := defaultEnv("MAITRE_D_WEBHOOK_DIR", "config/webhook-endpoints.d")

	// CLI flags take precedence over env vars
	if *triggerDir != "" {
		triggerDirStr = *triggerDir
	}
	if *dataDir != "" {
		dataDirStr = *dataDir
	}
	if *queueConfig != "" {
		queueConfigStr = *queueConfig
	}
	if *webhookDirFlag != "" {
		webhookDirStr = *webhookDirFlag
	}

	// Determine web port: CLI flag > env var > default
	port := 9090
	if *webPort > 0 {
		port = *webPort
	} else if p, err := parsePort(webPortStr); err == nil {
		port = p
	}

	// Determine API port: CLI flag > env var > default
	apiPortVal := 0
	if *apiPort > 0 {
		apiPortVal = *apiPort
	} else if p, err := parsePort(apiPortStr); err == nil {
		apiPortVal = p
	}
	_ = apiPortVal // used below

	log.Printf("maitred %s starting", Version)
	log.Printf("  trigger dir:    %s", triggerDirStr)
	log.Printf("  data dir:       %s", dataDirStr)
	log.Printf("  queue config:   %s", queueConfigStr)
	log.Printf("  web port:       %d", port)
	log.Printf("  api port:       %d", apiPortVal)
	log.Printf("  webhook dir:    %s", webhookDirStr)

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

	log.Printf("resolved trigger dir:  %s", triggerDirStr)
	log.Printf("resolved data dir:     %s", dataDirStr)

	// Resolve webhook dir relative to working directory if not absolute
	if !filepath.IsAbs(webhookDirStr) {
		var err error
		webhookDirStr, err = filepath.Abs(webhookDirStr)
		if err != nil {
			log.Fatalf("resolve webhook dir: %v", err)
		}
	}
	log.Printf("resolved webhook dir:  %s", webhookDirStr)

	// Create the engine (loads and validates config)
	var qe queue.TaskQueueProvider

	if queueConfigStr != "" {
		// Load queue adapter config
		queueCfg, err := loadQueueConfig(queueConfigStr)
		if err != nil {
			log.Fatalf("load queue config: %v", err)
		}
		log.Printf("  queue endpoint: %s", queueCfg.Endpoint)
		if queueCfg.MTLSCert != "" && queueCfg.MTLSKey != "" {
			log.Printf("  queue mTLS:     enabled (cert: %s)", queueCfg.MTLSCert)
		}
		if queueCfg.TaskTemplate != "" {
			log.Printf("  queue template: custom")
		}

		// Create the HTTP adapter
		adapter, err := queue.NewHTTPAdapter(queueCfg, log.Default())
		if err != nil {
			log.Fatalf("create queue adapter: %v", err)
		}
		qe = adapter
		log.Printf("  queue mode:     HTTP adapter")
	} else {
		mq := queue.NewTaskQueue()
		qe = mq
		log.Printf("  queue mode:     in-memory")
	}

	eng, err := engine.New(engine.Config{
		TriggerDir: triggerDirStr,
		DataDir:    dataDirStr,
		Queue:      qe,
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
	webSrv := web.New(port, eng, Version)
	if err := webSrv.Start(); err != nil {
		log.Printf("web server error: %v (continuing without dashboard)", err)
	}

	// Load webhook endpoint configs
	webhookProviders, err := webhook.LoadProviderConfigs(webhookDirStr)
	if err != nil {
		log.Printf("webhook config error: %v (continuing without webhook API)", err)
		webhookProviders = nil
	} else {
		log.Printf("loaded %d webhook provider(s)", len(webhookProviders))
		for _, p := range webhookProviders {
			for _, ep := range p.Endpoints {
				log.Printf("  /v1/%s/%s → trigger %s", p.Provider, ep.Name, ep.TriggerID)
			}
		}
	}

	// Start the webhook API server if port is configured
	var webhookSrv *webhook.Server
	if apiPortVal > 0 && webhookProviders != nil {
		webhookSrv = webhook.New(apiPortVal, eng, eng.StateStore(), Version, webhookProviders)
		if err := webhookSrv.Start(); err != nil {
			log.Printf("webhook server error: %v (continuing without webhook API)", err)
			webhookSrv = nil
		}
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
	if webhookSrv != nil {
		webhookSrv.Stop()
	}
	webSrv.Stop()
	eng.Stop()
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

// loadQueueConfig reads and parses a queue adapter configuration file.
func loadQueueConfig(path string) (queue.AdapterConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return queue.AdapterConfig{}, fmt.Errorf("read queue config: %w", err)
	}

	var cfg queue.AdapterConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return queue.AdapterConfig{}, fmt.Errorf("parse queue config: %w", err)
	}

	return cfg, nil
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
