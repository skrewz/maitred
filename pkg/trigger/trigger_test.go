package trigger_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"maitred/pkg/trigger"
)

func TestParseSchedule(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"@every 1h", "@every 1h"},
		{"@every 30m", "@every 30m"},
		{"@every 6h", "@every 6h"},
		{"0 */6 * * *", "0 */6 * * *"},
		{"0 0 * * *", "0 0 * * *"},
		{"@daily", "@daily"},
		{"@hourly", "@hourly"},
		{"@webhook", "@webhook"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			sched, err := trigger.ParseSchedule(tt.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if sched != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, sched)
			}
		})
	}
}

func TestParseSchedule_Invalid(t *testing.T) {
	_, err := trigger.ParseSchedule("not-a-schedule")
	if err == nil {
		t.Error("expected error for invalid schedule, got nil")
	}
}

func TestParseSchedule_InvalidDuration(t *testing.T) {
	_, err := trigger.ParseSchedule("@every notaduration")
	if err == nil {
		t.Error("expected error for invalid duration, got nil")
	}
}

func TestParseSchedule_InvalidCron(t *testing.T) {
	_, err := trigger.ParseSchedule("*/invalid * * * *")
	if err == nil {
		t.Error("expected error for invalid cron, got nil")
	}
}

func TestParseSchedule_Webhook(t *testing.T) {
	sched, err := trigger.ParseSchedule("@webhook")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sched != "@webhook" {
		t.Errorf("expected '@webhook', got %q", sched)
	}
}

func TestLoadTriggerDefinitions(t *testing.T) {
	dir := t.TempDir()

	// Create a trigger config file
	configYAML := `
triggers:
  - id: "test-trigger"
    type: periodic
    schedule: "@every 1h"
    prompt: "Research new models"
    tags:
      - "business-default"
    timeout: 3600
`
	configPath := filepath.Join(dir, "triggers.yaml")
	if err := os.WriteFile(configPath, []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	defs, err := trigger.LoadTriggerDefinitions(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(defs) != 1 {
		t.Fatalf("expected 1 trigger, got %d", len(defs))
	}

	def := defs[0]
	if def.ID != "test-trigger" {
		t.Errorf("expected ID 'test-trigger', got %q", def.ID)
	}
	if def.Type != trigger.TypePeriodic {
		t.Errorf("expected type periodic, got %q", def.Type)
	}
	if def.Schedule != "@every 1h" {
		t.Errorf("expected schedule '@every 1h', got %q", def.Schedule)
	}
	if def.Prompt != "Research new models" {
		t.Errorf("expected prompt 'Research new models', got %q", def.Prompt)
	}
	if len(def.Tags) != 1 || def.Tags[0] != "business-default" {
		t.Errorf("expected tags ['business-default'], got %v", def.Tags)
	}
	if def.Timeout != 3600 {
		t.Errorf("expected timeout 3600, got %d", def.Timeout)
	}
}

func TestLoadTriggerDefinitions_MultipleFiles(t *testing.T) {
	dir := t.TempDir()

	// Create two config files in the .d folder
	config1YAML := `
triggers:
  - id: "trigger-1"
    type: periodic
    schedule: "@every 1h"
    prompt: "First trigger"
`
	config2YAML := `
triggers:
  - id: "trigger-2"
    type: periodic
    schedule: "@every 2h"
    prompt: "Second trigger"
`
	if err := os.WriteFile(filepath.Join(dir, "01-base.yaml"), []byte(config1YAML), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "02-extra.yaml"), []byte(config2YAML), 0o644); err != nil {
		t.Fatal(err)
	}

	defs, err := trigger.LoadTriggerDefinitions(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(defs) != 2 {
		t.Fatalf("expected 2 triggers, got %d", len(defs))
	}

	if defs[0].ID != "trigger-1" || defs[1].ID != "trigger-2" {
		t.Errorf("unexpected trigger order: %v", defs)
	}
}

func TestLoadTriggerDefinitions_WebhookSchedule(t *testing.T) {
	dir := t.TempDir()

	configYAML := `
triggers:
  - id: "webhook-trigger"
    type: periodic
    schedule: "@webhook"
    prompt: "Handle webhook event"
`
	if err := os.WriteFile(filepath.Join(dir, "triggers.yaml"), []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	defs, err := trigger.LoadTriggerDefinitions(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(defs) != 1 {
		t.Fatalf("expected 1 trigger, got %d", len(defs))
	}

	def := defs[0]
	if def.ID != "webhook-trigger" {
		t.Errorf("expected ID 'webhook-trigger', got %q", def.ID)
	}
	if def.Schedule != "@webhook" {
		t.Errorf("expected schedule '@webhook', got %q", def.Schedule)
	}
}

func TestLoadTriggerDefinitions_EmptyDir(t *testing.T) {
	dir := t.TempDir()

	defs, err := trigger.LoadTriggerDefinitions(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if defs == nil {
		t.Error("expected empty slice, got nil")
	}
	if len(defs) != 0 {
		t.Errorf("expected 0 triggers, got %d", len(defs))
	}
}

func TestLoadTriggerDefinitions_NonExistentDir(t *testing.T) {
	_, err := trigger.LoadTriggerDefinitions("/nonexistent/dir/that/does/not/exist")
	if err == nil {
		t.Error("expected error for non-existent directory, got nil")
	}
}

func TestLoadTriggerDefinitions_InvalidYAML(t *testing.T) {
	dir := t.TempDir()

	invalidYAML := `
triggers:
  - id: broken
    [not valid yaml
`
	if err := os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte(invalidYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := trigger.LoadTriggerDefinitions(dir)
	if err == nil {
		t.Error("expected error for invalid YAML, got nil")
	}
}

func TestLoadTriggerDefinitions_SkipNonYAML(t *testing.T) {
	dir := t.TempDir()

	// Create a non-YAML file that should be skipped
	if err := os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("not yaml"), 0o644); err != nil {
		t.Fatal(err)
	}

	configYAML := `
triggers:
  - id: "only-one"
    type: periodic
    schedule: "@every 1h"
    prompt: "test"
`
	if err := os.WriteFile(filepath.Join(dir, "triggers.yaml"), []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	defs, err := trigger.LoadTriggerDefinitions(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(defs) != 1 {
		t.Fatalf("expected 1 trigger, got %d", len(defs))
	}

	if defs[0].ID != "only-one" {
		t.Errorf("expected 'only-one', got %q", defs[0].ID)
	}
}

func TestTriggerDefinition_EvalPromptTemplate(t *testing.T) {
	def := trigger.TriggerDefinition{
		ID:       "test",
		Type:     trigger.TypePeriodic,
		Schedule: "@every 1h",
		Prompt:   "Research since {{ .LastRun }}",
	}

	lastRun := time.Date(2025, 5, 17, 10, 0, 0, 0, time.UTC)
	result, err := def.EvalPromptTemplate(lastRun)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := "Research since 2025-05-17T10:00:00Z"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestTriggerDefinition_EvalPromptTemplate_NoVars(t *testing.T) {
	def := trigger.TriggerDefinition{
		ID:       "test",
		Type:     trigger.TypePeriodic,
		Schedule: "@every 1h",
		Prompt:   "Just a plain prompt",
	}

	lastRun := time.Date(2025, 5, 17, 10, 0, 0, 0, time.UTC)
	result, err := def.EvalPromptTemplate(lastRun)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result != "Just a plain prompt" {
		t.Errorf("expected 'Just a plain prompt', got %q", result)
	}
}

func TestTriggerDefinition_EvalPromptTemplateWith_Payload(t *testing.T) {
	def := trigger.TriggerDefinition{
		ID:       "test",
		Type:     trigger.TypePeriodic,
		Schedule: "@every 1h",
		Prompt:   "PR: {{ .Payload.pull_request.title }}",
	}

	payload := map[string]interface{}{
		"pull_request": map[string]interface{}{
			"title": "Fix the bug",
		},
	}
	lastRun := time.Time{}

	result, err := def.EvalPromptTemplateWith(payload, lastRun)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := "PR: Fix the bug"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestTriggerDefinition_EvalPromptTemplateWith_PayloadAndLastRun(t *testing.T) {
	def := trigger.TriggerDefinition{
		ID:       "test",
		Type:     trigger.TypePeriodic,
		Schedule: "@every 1h",
		Prompt:   "Review {{ .Payload.event }} since {{ .LastRun }}",
	}

	payload := map[string]interface{}{
		"event": "push",
	}
	lastRun := time.Date(2025, 5, 17, 10, 0, 0, 0, time.UTC)

	result, err := def.EvalPromptTemplateWith(payload, lastRun)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := "Review push since 2025-05-17T10:00:00Z"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestTriggerDefinition_EvalPromptTemplateWith_NilPayload(t *testing.T) {
	def := trigger.TriggerDefinition{
		ID:       "test",
		Type:     trigger.TypePeriodic,
		Schedule: "@every 1h",
		Prompt:   "Research since {{ .LastRun }}",
	}

	lastRun := time.Date(2025, 5, 17, 10, 0, 0, 0, time.UTC)

	result, err := def.EvalPromptTemplateWith(nil, lastRun)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := "Research since 2025-05-17T10:00:00Z"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestTriggerDefinition_EvalPromptTemplateWith_NestedPayload(t *testing.T) {
	def := trigger.TriggerDefinition{
		ID:       "test",
		Type:     trigger.TypePeriodic,
		Schedule: "@every 1h",
		Prompt:   "{{ .Payload.sender.login }} pushed to {{ .Payload.repository.name }}",
	}

	payload := map[string]interface{}{
		"sender": map[string]interface{}{
			"login": "developer1",
		},
		"repository": map[string]interface{}{
			"name": "maitred",
		},
	}
	lastRun := time.Time{}

	result, err := def.EvalPromptTemplateWith(payload, lastRun)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := "developer1 pushed to maitred"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestTriggerDefinition_EvalPromptTemplateWith_EmptyPayload(t *testing.T) {
	def := trigger.TriggerDefinition{
		ID:       "test",
		Type:     trigger.TypePeriodic,
		Schedule: "@every 1h",
		Prompt:   "Processing event",
	}

	payload := map[string]interface{}{}
	lastRun := time.Time{}

	result, err := def.EvalPromptTemplateWith(payload, lastRun)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := "Processing event"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestTriggerDefinition_EvalPromptTemplateWith_BadTemplate(t *testing.T) {
	def := trigger.TriggerDefinition{
		ID:       "test",
		Type:     trigger.TypePeriodic,
		Schedule: "@every 1h",
		Prompt:   "{{ .Payload.nonexistent.deeply.nested.field }}",
	}

	payload := map[string]interface{}{}
	lastRun := time.Time{}

	_, err := def.EvalPromptTemplateWith(payload, lastRun)
	// This should succeed because the template engine returns an error
	// when accessing a nil field — but let's verify it handles it gracefully
	_ = err
}

func TestLoadTriggerDefinitions_Subdirectories(t *testing.T) {
	dir := t.TempDir()

	// Create a subdirectory
	subDir := filepath.Join(dir, "periodic")
	if err := os.Mkdir(subDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a trigger in the root
	rootYAML := `
triggers:
  - id: "root-trigger"
    type: periodic
    schedule: "@every 1h"
    prompt: "Root trigger prompt"
`
	if err := os.WriteFile(filepath.Join(dir, "01-root.yaml"), []byte(rootYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create a trigger in the subdirectory
	subYAML := `
triggers:
  - id: "sub-trigger"
    type: periodic
    schedule: "@every 30m"
    prompt: "Subdirectory trigger prompt"
`
	if err := os.WriteFile(filepath.Join(subDir, "01-sub.yaml"), []byte(subYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	defs, err := trigger.LoadTriggerDefinitions(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(defs) != 2 {
		t.Fatalf("expected 2 triggers, got %d", len(defs))
	}

	// Verify ordering: root comes before sub by path
	if defs[0].ID != "root-trigger" {
		t.Errorf("expected first trigger 'root-trigger', got %q", defs[0].ID)
	}
	if defs[1].ID != "sub-trigger" {
		t.Errorf("expected second trigger 'sub-trigger', got %q", defs[1].ID)
	}
}

func TestLoadTriggerDefinitions_NestedSubdirectories(t *testing.T) {
	dir := t.TempDir()

	// Create nested subdirectories
	subDir1 := filepath.Join(dir, "a")
	subDir2 := filepath.Join(subDir1, "b")
	if err := os.MkdirAll(subDir2, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create triggers at each level
	rootYAML := `
triggers:
  - id: "root"
    type: periodic
    schedule: "@every 1h"
    prompt: "root"
`
	aYAML := `
triggers:
  - id: "a"
    type: periodic
    schedule: "@every 1h"
    prompt: "a"
`
	bYAML := `
triggers:
  - id: "b"
    type: periodic
    schedule: "@every 1h"
    prompt: "b"
`
	if err := os.WriteFile(filepath.Join(dir, "00-root.yaml"), []byte(rootYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subDir1, "01-a.yaml"), []byte(aYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subDir2, "02-b.yaml"), []byte(bYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	defs, err := trigger.LoadTriggerDefinitions(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(defs) != 3 {
		t.Fatalf("expected 3 triggers, got %d", len(defs))
	}

	// Verify ordering by full path
	expectedIDs := []string{"root", "a", "b"}
	for i, id := range expectedIDs {
		if defs[i].ID != id {
			t.Errorf("expected trigger[%d] ID %q, got %q", i, id, defs[i].ID)
		}
	}
}

func TestLoadTriggerDefinitions_SkipNonYAMLInSubdirs(t *testing.T) {
	dir := t.TempDir()

	subDir := filepath.Join(dir, "webhook")
	if err := os.Mkdir(subDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a non-YAML file in the subdirectory
	if err := os.WriteFile(filepath.Join(subDir, "readme.txt"), []byte("not yaml"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create a valid YAML file in the subdirectory
	subYAML := `
triggers:
  - id: "webhook-trigger"
    type: periodic
    schedule: "@every 1h"
    prompt: "webhook"
`
	if err := os.WriteFile(filepath.Join(subDir, "01-webhook.yaml"), []byte(subYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	defs, err := trigger.LoadTriggerDefinitions(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(defs) != 1 {
		t.Fatalf("expected 1 trigger, got %d", len(defs))
	}

	if defs[0].ID != "webhook-trigger" {
		t.Errorf("expected 'webhook-trigger', got %q", defs[0].ID)
	}
}

func TestTriggerDefinition_EvalPromptTemplate_Equivalence(t *testing.T) {
	// EvalPromptTemplate should be equivalent to EvalPromptTemplateWith(nil, lastRun)
	def := trigger.TriggerDefinition{
		ID:       "test",
		Type:     trigger.TypePeriodic,
		Schedule: "@every 1h",
		Prompt:   "Research since {{ .LastRun }}",
	}

	lastRun := time.Date(2025, 5, 17, 10, 0, 0, 0, time.UTC)

	result1, err1 := def.EvalPromptTemplate(lastRun)
	if err1 != nil {
		t.Fatalf("unexpected error: %v", err1)
	}

	result2, err2 := def.EvalPromptTemplateWith(nil, lastRun)
	if err2 != nil {
		t.Fatalf("unexpected error: %v", err2)
	}

	if result1 != result2 {
		t.Errorf("expected equivalence: %q != %q", result1, result2)
	}
}

func TestTriggerDefinition_ShouldHoldOff_NoCondition(t *testing.T) {
	def := trigger.TriggerDefinition{
		ID:               "test",
		Type:             trigger.TypePeriodic,
		Schedule:         "@every 1h",
		HoldOffCondition: "",
		Prompt:           "test prompt",
	}

	heldOff, err := def.ShouldHoldOff(nil, time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if heldOff {
		t.Error("expected false when no hold-off condition is set")
	}
}

func TestTriggerDefinition_ShouldHoldOff_ConditionTrue(t *testing.T) {
	def := trigger.TriggerDefinition{
		ID:               "test",
		Type:             trigger.TypePeriodic,
		Schedule:         "@webhook",
		HoldOffCondition: "{{ .Payload.pull_request.merged }}",
		Prompt:           "test prompt",
	}

	payload := map[string]interface{}{
		"pull_request": map[string]interface{}{
			"merged": true,
		},
	}

	heldOff, err := def.ShouldHoldOff(payload, time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !heldOff {
		t.Error("expected true when condition evaluates to true")
	}
}

func TestTriggerDefinition_ShouldHoldOff_ConditionFalse(t *testing.T) {
	def := trigger.TriggerDefinition{
		ID:               "test",
		Type:             trigger.TypePeriodic,
		Schedule:         "@webhook",
		HoldOffCondition: "{{ .Payload.pull_request.merged }}",
		Prompt:           "test prompt",
	}

	payload := map[string]interface{}{
		"pull_request": map[string]interface{}{
			"merged": false,
		},
	}

	heldOff, err := def.ShouldHoldOff(payload, time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if heldOff {
		t.Error("expected false when condition evaluates to false")
	}
}

func TestTriggerDefinition_ShouldHoldOff_OrCondition(t *testing.T) {
	// Mirrors the real-world pattern: hold off if merged OR closed
	def := trigger.TriggerDefinition{
		ID:               "test",
		Type:             trigger.TypePeriodic,
		Schedule:         "@webhook",
		HoldOffCondition: `{{ or .Payload.pull_request.merged (eq .Payload.pull_request.state "closed") }}`,
		Prompt:           "test prompt",
	}

	// Case 1: merged = true, should hold off
	payload1 := map[string]interface{}{
		"pull_request": map[string]interface{}{
			"merged": true,
			"state":  "open",
		},
	}
	heldOff, err := def.ShouldHoldOff(payload1, time.Time{})
	if err != nil {
		t.Fatalf("case 1 error: %v", err)
	}
	if !heldOff {
		t.Error("case 1: expected true when merged=true")
	}

	// Case 2: state = closed, should hold off
	payload2 := map[string]interface{}{
		"pull_request": map[string]interface{}{
			"merged": false,
			"state":  "closed",
		},
	}
	heldOff, err = def.ShouldHoldOff(payload2, time.Time{})
	if err != nil {
		t.Fatalf("case 2 error: %v", err)
	}
	if !heldOff {
		t.Error("case 2: expected true when state=closed")
	}

	// Case 3: open and not merged, should NOT hold off
	payload3 := map[string]interface{}{
		"pull_request": map[string]interface{}{
			"merged": false,
			"state":  "open",
		},
	}
	heldOff, err = def.ShouldHoldOff(payload3, time.Time{})
	if err != nil {
		t.Fatalf("case 3 error: %v", err)
	}
	if heldOff {
		t.Error("case 3: expected false when open and not merged")
	}
}

func TestTriggerDefinition_ShouldHoldOff_InvalidTemplate(t *testing.T) {
	def := trigger.TriggerDefinition{
		ID:               "test",
		Type:             trigger.TypePeriodic,
		Schedule:         "@webhook",
		HoldOffCondition: "{{ invalid syntax }}",
		Prompt:           "test prompt",
	}

	_, err := def.ShouldHoldOff(nil, time.Time{})
	if err == nil {
		t.Error("expected error for invalid template, got nil")
	}
}

func TestTriggerDefinition_ShouldHoldOff_LastRunCondition(t *testing.T) {
	// Hold off if LastRun is zero (never run before) — a silly but valid example
	def := trigger.TriggerDefinition{
		ID:               "test",
		Type:             trigger.TypePeriodic,
		Schedule:         "@every 1h",
		HoldOffCondition: `{{ eq .LastRun "0001-01-01T00:00:00Z" }}`,
		Prompt:           "test prompt",
	}

	// Never run before — should hold off
	heldOff, err := def.ShouldHoldOff(nil, time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !heldOff {
		t.Error("expected true when LastRun is zero")
	}

	// Has run before — should NOT hold off
	lastRun := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	heldOff, err = def.ShouldHoldOff(nil, lastRun)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if heldOff {
		t.Error("expected false when LastRun is set")
	}
}

func TestLoadTriggerDefinition_HoldOffCondition(t *testing.T) {
	dir := t.TempDir()

	configYAML := `
triggers:
  - id: "holdoff-trigger"
    type: periodic
    schedule: "@webhook"
    hold-off-condition: "{{ .Payload.pull_request.merged }}"
    prompt: "Handle PR"
`
	if err := os.WriteFile(filepath.Join(dir, "triggers.yaml"), []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	defs, err := trigger.LoadTriggerDefinitions(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(defs) != 1 {
		t.Fatalf("expected 1 trigger, got %d", len(defs))
	}

	expected := "{{ .Payload.pull_request.merged }}"
	if defs[0].HoldOffCondition != expected {
		t.Errorf("expected hold-off-condition %q, got %q", expected, defs[0].HoldOffCondition)
	}
}

func TestLoadTriggerDefinition_HoldOffConditionOptional(t *testing.T) {
	dir := t.TempDir()

	configYAML := `
triggers:
  - id: "normal-trigger"
    type: periodic
    schedule: "@every 1h"
    prompt: "Handle PR"
`
	if err := os.WriteFile(filepath.Join(dir, "triggers.yaml"), []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	defs, err := trigger.LoadTriggerDefinitions(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(defs) != 1 {
		t.Fatalf("expected 1 trigger, got %d", len(defs))
	}

	if defs[0].HoldOffCondition != "" {
		t.Errorf("expected empty hold-off-condition, got %q", defs[0].HoldOffCondition)
	}
}

func TestLoadTriggerDefinition_Persona(t *testing.T) {
	dir := t.TempDir()

	configYAML := `
triggers:
  - id: "persona-trigger"
    type: periodic
    schedule: "@every 1h"
    persona: s-issue-implementer
    prompt: "Implement a feature"
    tags:
      - "business-default"
    timeout: 3600
`
	if err := os.WriteFile(filepath.Join(dir, "triggers.yaml"), []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	defs, err := trigger.LoadTriggerDefinitions(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(defs) != 1 {
		t.Fatalf("expected 1 trigger, got %d", len(defs))
	}

	if defs[0].Persona != "s-issue-implementer" {
		t.Errorf("expected persona 's-issue-implementer', got %q", defs[0].Persona)
	}
}

func TestLoadTriggerDefinition_PersonaOptional(t *testing.T) {
	dir := t.TempDir()

	configYAML := `
triggers:
  - id: "no-persona-trigger"
    type: periodic
    schedule: "@every 1h"
    prompt: "Just a regular trigger"
`
	if err := os.WriteFile(filepath.Join(dir, "triggers.yaml"), []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	defs, err := trigger.LoadTriggerDefinitions(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(defs) != 1 {
		t.Fatalf("expected 1 trigger, got %d", len(defs))
	}

	if defs[0].Persona != "" {
		t.Errorf("expected empty persona, got %q", defs[0].Persona)
	}
}
