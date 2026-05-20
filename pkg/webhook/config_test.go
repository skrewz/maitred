package webhook_test

import (
	"os"
	"path/filepath"
	"testing"

	"maitred/pkg/webhook"
)

func TestLoadProviderConfigs(t *testing.T) {
	dir := t.TempDir()

	configYAML := `
endpoints:
  - name: "pull_request"
    trigger_id: "pr-review"
    response: '{"status": "submitted"}'
  - name: "push"
    trigger_id: "push-handler"
    response: '{"status": "submitted"}'
`
	if err := os.WriteFile(filepath.Join(dir, "forgejo.yaml"), []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	providers, err := webhook.LoadProviderConfigs(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(providers) != 1 {
		t.Fatalf("expected 1 provider, got %d", len(providers))
	}

	p := providers[0]
	if p.Provider != "forgejo" {
		t.Errorf("expected provider 'forgejo', got %q", p.Provider)
	}
	if len(p.Endpoints) != 2 {
		t.Fatalf("expected 2 endpoints, got %d", len(p.Endpoints))
	}

	if p.Endpoints[0].Name != "pull_request" {
		t.Errorf("expected endpoint name 'pull_request', got %q", p.Endpoints[0].Name)
	}
	if p.Endpoints[0].TriggerID != "pr-review" {
		t.Errorf("expected trigger_id 'pr-review', got %q", p.Endpoints[0].TriggerID)
	}
	if p.Endpoints[1].Name != "push" {
		t.Errorf("expected endpoint name 'push', got %q", p.Endpoints[1].Name)
	}
}

func TestLoadProviderConfigs_MultipleFiles(t *testing.T) {
	dir := t.TempDir()

	config1 := `
endpoints:
  - name: "ep1"
    trigger_id: "trig-1"
    response: '{"status": "submitted"}'
`
	config2 := `
endpoints:
  - name: "ep2"
    trigger_id: "trig-2"
    response: '{"status": "submitted"}'
`
	if err := os.WriteFile(filepath.Join(dir, "01-first.yaml"), []byte(config1), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "02-second.yml"), []byte(config2), 0o644); err != nil {
		t.Fatal(err)
	}

	providers, err := webhook.LoadProviderConfigs(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(providers) != 2 {
		t.Fatalf("expected 2 providers, got %d", len(providers))
	}

	if providers[0].Provider != "01-first" {
		t.Errorf("expected provider '01-first', got %q", providers[0].Provider)
	}
	if providers[1].Provider != "02-second" {
		t.Errorf("expected provider '02-second', got %q", providers[1].Provider)
	}
}

func TestLoadProviderConfigs_EmptyDir(t *testing.T) {
	dir := t.TempDir()

	providers, err := webhook.LoadProviderConfigs(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(providers) != 0 {
		t.Errorf("expected 0 providers, got %d", len(providers))
	}
}

func TestLoadProviderConfigs_NonExistentDir(t *testing.T) {
	_, err := webhook.LoadProviderConfigs("/nonexistent/dir/xyz123")
	if err == nil {
		t.Error("expected error for non-existent directory, got nil")
	}
}

func TestLoadProviderConfigs_InvalidYAML(t *testing.T) {
	dir := t.TempDir()

	invalidYAML := `
endpoints:
  - name: broken
    [not valid yaml
`
	if err := os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte(invalidYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := webhook.LoadProviderConfigs(dir)
	if err == nil {
		t.Error("expected error for invalid YAML, got nil")
	}
}

func TestLoadProviderConfigs_SkipNonYAML(t *testing.T) {
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("not yaml"), 0o644); err != nil {
		t.Fatal(err)
	}

	configYAML := `
endpoints:
  - name: "only-one"
    trigger_id: "trig-1"
    response: '{"status": "submitted"}'
`
	if err := os.WriteFile(filepath.Join(dir, "webhooks.yaml"), []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	providers, err := webhook.LoadProviderConfigs(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(providers) != 1 {
		t.Fatalf("expected 1 provider, got %d", len(providers))
	}
}

func TestLoadProviderConfigs_MissingName(t *testing.T) {
	dir := t.TempDir()

	configYAML := `
endpoints:
  - trigger_id: "trig-1"
    response: '{"status": "submitted"}'
`
	if err := os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := webhook.LoadProviderConfigs(dir)
	if err == nil {
		t.Error("expected error for missing endpoint name, got nil")
	}
}

func TestLoadProviderConfigs_MissingTriggerID(t *testing.T) {
	dir := t.TempDir()

	configYAML := `
endpoints:
  - name: "ep"
    response: '{"status": "submitted"}'
`
	if err := os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := webhook.LoadProviderConfigs(dir)
	if err == nil {
		t.Error("expected error for missing trigger_id, got nil")
	}
}

func TestLoadProviderConfigs_MissingResponse(t *testing.T) {
	dir := t.TempDir()

	configYAML := `
endpoints:
  - name: "ep"
    trigger_id: "trig-1"
`
	if err := os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := webhook.LoadProviderConfigs(dir)
	if err == nil {
		t.Error("expected error for missing response, got nil")
	}
}

func TestLoadProviderConfigs_ProviderNameUppercase(t *testing.T) {
	dir := t.TempDir()

	configYAML := `
endpoints:
  - name: "ep"
    trigger_id: "trig-1"
    response: '{"status": "submitted"}'
`
	// Filename with uppercase — should be rejected
	if err := os.WriteFile(filepath.Join(dir, "Forgejo.yaml"), []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := webhook.LoadProviderConfigs(dir)
	if err == nil {
		t.Error("expected error for uppercase provider name, got nil")
	}
}

func TestLoadProviderConfigs_ProviderNameValid(t *testing.T) {
	dir := t.TempDir()

	configYAML := `
endpoints:
  - name: "ep"
    trigger_id: "trig-1"
    response: '{"status": "submitted"}'
`
	// Filename with hyphens and numbers — should be accepted
	if err := os.WriteFile(filepath.Join(dir, "forgejo-v2.yaml"), []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	providers, err := webhook.LoadProviderConfigs(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(providers) != 1 {
		t.Fatalf("expected 1 provider, got %d", len(providers))
	}
	if providers[0].Provider != "forgejo-v2" {
		t.Errorf("expected provider 'forgejo-v2', got %q", providers[0].Provider)
	}
}

func TestLoadProviderConfigs_ProviderNameSpecialChars(t *testing.T) {
	dir := t.TempDir()

	configYAML := `
endpoints:
  - name: "ep"
    trigger_id: "trig-1"
    response: '{"status": "submitted"}'
`
	// Filename with underscore — should be rejected
	if err := os.WriteFile(filepath.Join(dir, "forgejo_v2.yaml"), []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := webhook.LoadProviderConfigs(dir)
	if err == nil {
		t.Error("expected error for underscore in provider name, got nil")
	}
}

func TestProviderConfig_FindEndpoint(t *testing.T) {
	cfg := webhook.ProviderConfig{
		Provider: "forgejo",
		Endpoints: []webhook.EndpointConfig{
			{Name: "pull_request", TriggerID: "pr-review", Response: `{"status": "submitted"}`},
			{Name: "push", TriggerID: "push-handler", Response: `{"status": "submitted"}`},
		},
	}

	ep := cfg.FindEndpoint("pull_request")
	if ep == nil {
		t.Fatal("expected endpoint to be found")
	}
	if ep.TriggerID != "pr-review" {
		t.Errorf("expected trigger_id 'pr-review', got %q", ep.TriggerID)
	}

	if cfg.FindEndpoint("nonexistent") != nil {
		t.Error("expected nil for nonexistent endpoint")
	}
}
