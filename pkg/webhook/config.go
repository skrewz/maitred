// Package webhook provides webhook endpoint configuration loading and
// HTTP handling for incoming webhook events (e.g. from Forgejo).
//
// Webhook endpoints are defined in YAML files under a webhook-endpoints.d/
// directory. Each file becomes a named provider (filename sans extension).
// The route pattern is: /v1/{provider}/{endpoint_name}
package webhook

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// validProviderName matches provider names: only lowercase letters, digits, and hyphens.
var validProviderName = regexp.MustCompile(`^[a-z0-9-]+$`)

// EndpointConfig represents a single webhook endpoint definition from YAML.
type EndpointConfig struct {
	// Name is the endpoint name (becomes the path segment after /v1/{provider}/).
	Name string `yaml:"name"`
	// TriggerID is the trigger definition this endpoint fires.
	TriggerID string `yaml:"trigger_id"`
	// Response is the response template sent back to the webhook caller.
	Response string `yaml:"response"`
}

// ProviderConfig represents a webhook provider (one YAML file).
type ProviderConfig struct {
	// Provider is the provider name (derived from filename).
	Provider string `yaml:"-"`
	// Endpoints defines the webhook endpoints for this provider.
	Endpoints []EndpointConfig `yaml:"endpoints"`
}

// LoadProviderConfigs reads all .yaml and .yml files from the given
// directory and returns parsed provider configurations. Files are
// processed in sorted alphabetical order.
func LoadProviderConfigs(dir string) ([]ProviderConfig, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read webhook config directory %q: %w", dir, err)
	}

	var allConfigs []ProviderConfig

	var yamlFiles []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		lower := strings.ToLower(name)
		if strings.HasSuffix(lower, ".yaml") || strings.HasSuffix(lower, ".yml") {
			yamlFiles = append(yamlFiles, name)
		}
	}
	sort.Strings(yamlFiles)

	for _, name := range yamlFiles {
		path := filepath.Join(dir, name)
		cfg, err := loadProviderFile(path)
		if err != nil {
			return nil, fmt.Errorf("load %s: %w", path, err)
		}
		allConfigs = append(allConfigs, cfg)
	}

	return allConfigs, nil
}

// loadProviderFile parses a single YAML webhook provider file.
func loadProviderFile(path string) (ProviderConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ProviderConfig{}, fmt.Errorf("read file: %w", err)
	}

	var cfg ProviderConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return ProviderConfig{}, fmt.Errorf("parse YAML: %w", err)
	}

	// Derive provider name from filename (strip extension)
	cfg.Provider = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))

	// Validate provider name: only lowercase letters, digits, and hyphens
	if !validProviderName.MatchString(cfg.Provider) {
		return ProviderConfig{}, fmt.Errorf("provider name %q: only [a-z0-9-] allowed", cfg.Provider)
	}

	// Validate endpoints
	for i, ep := range cfg.Endpoints {
		if ep.Name == "" {
			return ProviderConfig{}, fmt.Errorf("endpoint %d: name is required", i)
		}
		if ep.TriggerID == "" {
			return ProviderConfig{}, fmt.Errorf("endpoint %q: trigger_id is required", ep.Name)
		}
		if ep.Response == "" {
			return ProviderConfig{}, fmt.Errorf("endpoint %q: response is required", ep.Name)
		}
	}

	return cfg, nil
}

// FindEndpoint looks up an endpoint config by provider and endpoint name.
func (pc *ProviderConfig) FindEndpoint(name string) *EndpointConfig {
	for i := range pc.Endpoints {
		if pc.Endpoints[i].Name == name {
			return &pc.Endpoints[i]
		}
	}
	return nil
}
