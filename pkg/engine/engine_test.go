package engine_test

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"maitred/pkg/engine"
	"maitred/pkg/queue"
	"maitred/pkg/state"
)

// mockQueue wraps a TaskQueue to track Add calls.
type mockQueue struct {
	mu    sync.Mutex
	tasks []*queue.Task
	added atomic.Int32
}

func (m *mockQueue) AddTask(task *queue.Task) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tasks = append(m.tasks, task)
	m.added.Add(1)
	return nil
}

func (m *mockQueue) Tasks() []*queue.Task {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]*queue.Task, len(m.tasks))
	copy(result, m.tasks)
	return result
}

func (m *mockQueue) Count() int {
	return int(m.added.Load())
}

func TestEngine_StartStop(t *testing.T) {
	dir := t.TempDir()

	// Create a trigger config
	configYAML := `
triggers:
  - id: "test-trigger"
    type: periodic
    schedule: "@every 500ms"
    prompt: "test prompt"
`
	if err := os.WriteFile(filepath.Join(dir, "triggers.yaml"), []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	dataDir := t.TempDir()
	mq := &mockQueue{}

	eng, err := engine.New(engine.Config{
		TriggerDir: dir,
		DataDir:    dataDir,
		Queue:      mq,
	})
	if err != nil {
		t.Fatalf("unexpected error creating engine: %v", err)
	}

	// Start the engine
	if err := eng.Start(); err != nil {
		t.Fatalf("unexpected error starting engine: %v", err)
	}

	// Let it run briefly
	time.Sleep(600 * time.Millisecond)

	// Stop the engine
	eng.Stop()

	// Engine should have processed the trigger at least once
	if mq.Count() < 1 {
		t.Errorf("expected at least 1 task, got %d", mq.Count())
	}
}

func TestEngine_TriggerProducesTask(t *testing.T) {
	dir := t.TempDir()

	configYAML := `
triggers:
  - id: "model-research"
    type: periodic
    schedule: "@every 500ms"
    prompt: "Research new models since {{ .LastRun }}"
    tags:
      - "business-default"
    timeout: 3600
`
	if err := os.WriteFile(filepath.Join(dir, "triggers.yaml"), []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	dataDir := t.TempDir()
	mq := &mockQueue{}

	eng, err := engine.New(engine.Config{
		TriggerDir: dir,
		DataDir:    dataDir,
		Queue:      mq,
	})
	if err != nil {
		t.Fatalf("unexpected error creating engine: %v", err)
	}

	if err := eng.Start(); err != nil {
		t.Fatalf("unexpected error starting engine: %v", err)
	}

	// Wait for at least one execution
	time.Sleep(1 * time.Second)
	eng.Stop()

	tasks := mq.Tasks()
	if len(tasks) < 1 {
		t.Fatalf("expected at least 1 task, got %d", len(tasks))
	}

	// Check task content
	task := tasks[0]
	if task.Prompt == "" {
		t.Error("expected prompt to be set")
	}
	if task.Timeout != 3600 {
		t.Errorf("expected timeout 3600, got %d", task.Timeout)
	}
	if len(task.Tags) != 1 || task.Tags[0] != "business-default" {
		t.Errorf("unexpected tags: %v", task.Tags)
	}
}

func TestEngine_StatePersists(t *testing.T) {
	dir := t.TempDir()

	configYAML := `
triggers:
  - id: "state-test"
    type: periodic
    schedule: "@every 500ms"
    prompt: "test"
`
	if err := os.WriteFile(filepath.Join(dir, "triggers.yaml"), []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	dataDir := t.TempDir()
	mq := &mockQueue{}

	eng, err := engine.New(engine.Config{
		TriggerDir: dir,
		DataDir:    dataDir,
		Queue:      mq,
	})
	if err != nil {
		t.Fatalf("unexpected error creating engine: %v", err)
	}

	if err := eng.Start(); err != nil {
		t.Fatalf("unexpected error starting engine: %v", err)
	}

	time.Sleep(1 * time.Second)
	eng.Stop()

	// Check that state was persisted
	st, err := state.NewStore(dataDir)
	if err != nil {
		t.Fatalf("unexpected error creating state store: %v", err)
	}

	s, err := st.Load("state-test")
	if err != nil {
		t.Fatalf("expected state to exist for 'state-test': %v", err)
	}

	if s.LastRun.IsZero() {
		t.Error("expected lastRun to be set")
	}
}

func TestEngine_MultipleTriggers(t *testing.T) {
	dir := t.TempDir()

	configYAML := `
triggers:
  - id: "trigger-1"
    type: periodic
    schedule: "@every 500ms"
    prompt: "first"
  - id: "trigger-2"
    type: periodic
    schedule: "@every 500ms"
    prompt: "second"
`
	if err := os.WriteFile(filepath.Join(dir, "triggers.yaml"), []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	dataDir := t.TempDir()
	mq := &mockQueue{}

	eng, err := engine.New(engine.Config{
		TriggerDir: dir,
		DataDir:    dataDir,
		Queue:      mq,
	})
	if err != nil {
		t.Fatalf("unexpected error creating engine: %v", err)
	}

	if err := eng.Start(); err != nil {
		t.Fatalf("unexpected error starting engine: %v", err)
	}

	time.Sleep(1 * time.Second)
	eng.Stop()

	tasks := mq.Tasks()
	if len(tasks) < 2 {
		t.Fatalf("expected at least 2 tasks (one per trigger), got %d", len(tasks))
	}

	// Both prompts should appear
	foundFirst := false
	foundSecond := false
	for _, task := range tasks {
		if task.Prompt == "first" {
			foundFirst = true
		}
		if task.Prompt == "second" {
			foundSecond = true
		}
	}

	if !foundFirst {
		t.Error("expected to find 'first' prompt task")
	}
	if !foundSecond {
		t.Error("expected to find 'second' prompt task")
	}
}

func TestEngine_FailedTriggerDoesNotCrash(t *testing.T) {
	dir := t.TempDir()

	configYAML := `
triggers:
  - id: "failing-trigger"
    type: periodic
    schedule: "@every 500ms"
    prompt: "test"
`
	if err := os.WriteFile(filepath.Join(dir, "triggers.yaml"), []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	dataDir := t.TempDir()

	// Queue that fails on every call
	failingQueue := &failingQueue{}

	eng, err := engine.New(engine.Config{
		TriggerDir: dir,
		DataDir:    dataDir,
		Queue:      failingQueue,
	})
	if err != nil {
		t.Fatalf("unexpected error creating engine: %v", err)
	}

	if err := eng.Start(); err != nil {
		t.Fatalf("unexpected error starting engine: %v", err)
	}

	// Engine should survive the failing queue
	time.Sleep(1 * time.Second)
	eng.Stop()

	// If we got here without panicking, the test passes
}

type failingQueue struct{}

func (f *failingQueue) AddTask(task *queue.Task) error {
	return os.ErrInvalid
}

func TestEngine_LoadTriggersFromMultipleFiles(t *testing.T) {
	dir := t.TempDir()

	config1 := `
triggers:
  - id: "multi-1"
    type: periodic
    schedule: "@every 500ms"
    prompt: "from file 1"
`
	config2 := `
triggers:
  - id: "multi-2"
    type: periodic
    schedule: "@every 500ms"
    prompt: "from file 2"
`
	if err := os.WriteFile(filepath.Join(dir, "01-base.yaml"), []byte(config1), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "02-extra.yaml"), []byte(config2), 0o644); err != nil {
		t.Fatal(err)
	}

	dataDir := t.TempDir()
	mq := &mockQueue{}

	eng, err := engine.New(engine.Config{
		TriggerDir: dir,
		DataDir:    dataDir,
		Queue:      mq,
	})
	if err != nil {
		t.Fatalf("unexpected error creating engine: %v", err)
	}

	if err := eng.Start(); err != nil {
		t.Fatalf("unexpected error starting engine: %v", err)
	}

	time.Sleep(1 * time.Second)
	eng.Stop()

	tasks := mq.Tasks()
	if len(tasks) < 2 {
		t.Fatalf("expected at least 2 tasks, got %d", len(tasks))
	}
}

func TestEngine_DefinitionsAreLoaded(t *testing.T) {
	dir := t.TempDir()

	configYAML := `
triggers:
  - id: "count-test"
    type: periodic
    schedule: "@every 500ms"
    prompt: "test"
`
	if err := os.WriteFile(filepath.Join(dir, "triggers.yaml"), []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	dataDir := t.TempDir()
	mq := &mockQueue{}

	eng, err := engine.New(engine.Config{
		TriggerDir: dir,
		DataDir:    dataDir,
		Queue:      mq,
	})
	if err != nil {
		t.Fatalf("unexpected error creating engine: %v", err)
	}

	defs := eng.Definitions()
	if len(defs) != 1 {
		t.Fatalf("expected 1 definition, got %d", len(defs))
	}
	if defs[0].ID != "count-test" {
		t.Errorf("expected ID 'count-test', got %q", defs[0].ID)
	}
}

func TestEngine_New_NoTriggers(t *testing.T) {
	dir := t.TempDir()
	mq := &mockQueue{}

	eng, err := engine.New(engine.Config{
		TriggerDir: dir,
		DataDir:    t.TempDir(),
		Queue:      mq,
	})
	if err != nil {
		t.Fatalf("unexpected error creating engine with no triggers: %v", err)
	}

	if eng == nil {
		t.Fatal("expected engine to be created")
	}
}

func TestEngine_New_InvalidTriggerDir(t *testing.T) {
	mq := &mockQueue{}
	_, err := engine.New(engine.Config{
		TriggerDir: "/nonexistent/dir/xyz123",
		DataDir:    t.TempDir(),
		Queue:      mq,
	})
	if err == nil {
		t.Error("expected error for invalid trigger directory, got nil")
	}
}

func TestEngine_New_InvalidDataDir(t *testing.T) {
	dir := t.TempDir()
	configYAML := `
triggers:
  - id: "test"
    type: periodic
    schedule: "@every 1h"
    prompt: "test"
`
	if err := os.WriteFile(filepath.Join(dir, "triggers.yaml"), []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := engine.New(engine.Config{
		TriggerDir: dir,
		DataDir:    "/nonexistent/deeply/nested/invalid/dir/xyz123",
		Queue:      &mockQueue{},
	})
	if err == nil {
		t.Error("expected error for invalid data directory, got nil")
	}
}

func TestEngine_StartStop_Idempotent(t *testing.T) {
	dir := t.TempDir()

	configYAML := `
triggers:
  - id: "test"
    type: periodic
    schedule: "@every 500ms"
    prompt: "test"
`
	if err := os.WriteFile(filepath.Join(dir, "triggers.yaml"), []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	mq := &mockQueue{}
	eng, err := engine.New(engine.Config{
		TriggerDir: dir,
		DataDir:    t.TempDir(),
		Queue:      mq,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Start, stop, start again
	if err := eng.Start(); err != nil {
		t.Fatalf("first start: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	eng.Stop()

	// Start again — should not panic or error
	if err := eng.Start(); err != nil {
		t.Fatalf("second start: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	eng.Stop()
}

func TestEngine_Queue(t *testing.T) {
	dir := t.TempDir()
	configYAML := `
triggers:
  - id: "test"
    type: periodic
    schedule: "@every 1h"
    prompt: "test"
`
	if err := os.WriteFile(filepath.Join(dir, "triggers.yaml"), []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	mq := &mockQueue{}
	eng, err := engine.New(engine.Config{
		TriggerDir: dir,
		DataDir:    t.TempDir(),
		Queue:      mq,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	q := eng.Queue()
	if q == nil {
		t.Fatal("expected non-nil queue")
	}
	if q != mq {
		t.Error("expected Queue() to return the configured queue")
	}
}

func TestEngine_StateStore(t *testing.T) {
	dir := t.TempDir()
	configYAML := `
triggers:
  - id: "test"
    type: periodic
    schedule: "@every 1h"
    prompt: "test"
`
	if err := os.WriteFile(filepath.Join(dir, "triggers.yaml"), []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	eng, err := engine.New(engine.Config{
		TriggerDir: dir,
		DataDir:    t.TempDir(),
		Queue:      &mockQueue{},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	st := eng.StateStore()
	if st == nil {
		t.Fatal("expected non-nil state store")
	}
}

func TestEngine_WebhookTriggerDoesNotFire(t *testing.T) {
	dir := t.TempDir()

	configYAML := `
triggers:
  - id: "webhook-only"
    type: periodic
    schedule: "@webhook"
    prompt: "webhook event"
  - id: "periodic-trigger"
    type: periodic
    schedule: "@every 2s"
    prompt: "periodic"
`
	if err := os.WriteFile(filepath.Join(dir, "triggers.yaml"), []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	dataDir := t.TempDir()
	mq := &mockQueue{}

	eng, err := engine.New(engine.Config{
		TriggerDir: dir,
		DataDir:    dataDir,
		Queue:      mq,
	})
	if err != nil {
		t.Fatalf("unexpected error creating engine: %v", err)
	}

	if err := eng.Start(); err != nil {
		t.Fatalf("unexpected error starting engine: %v", err)
	}

	// Wait for periodic trigger to fire once on schedule
	time.Sleep(2500 * time.Millisecond)
	eng.Stop()

	// Only the periodic trigger should have produced a task
	tasks := mq.Tasks()
	if len(tasks) != 1 {
		t.Fatalf("expected exactly 1 task from periodic trigger, got %d", len(tasks))
	}
	if tasks[0].Prompt != "periodic" {
		t.Errorf("expected prompt 'periodic', got %q", tasks[0].Prompt)
	}

	// Verify only the periodic trigger's state was persisted
	st, err := state.NewStore(dataDir)
	if err != nil {
		t.Fatalf("unexpected error creating state store: %v", err)
	}

	// Periodic trigger should have state
	_, err = st.Load("periodic-trigger")
	if err != nil {
		t.Error("expected state for periodic-trigger")
	}

	// Webhook-only trigger should NOT have state (never executed)
	_, err = st.Load("webhook-only")
	if err == nil {
		t.Error("expected no state for webhook-only trigger")
	}
}

func TestEngine_DefaultNoImmediateFire(t *testing.T) {
	dir := t.TempDir()

	configYAML := `
triggers:
  - id: "no-startup-test"
    type: periodic
    schedule: "@every 500ms"
    prompt: "test prompt"
`
	if err := os.WriteFile(filepath.Join(dir, "triggers.yaml"), []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	dataDir := t.TempDir()
	mq := &mockQueue{}

	eng, err := engine.New(engine.Config{
		TriggerDir: dir,
		DataDir:    dataDir,
		Queue:      mq,
	})
	if err != nil {
		t.Fatalf("unexpected error creating engine: %v", err)
	}

	if err := eng.Start(); err != nil {
		t.Fatalf("unexpected error starting engine: %v", err)
	}

	// Wait briefly — no tasks should have been dispatched yet
	time.Sleep(150 * time.Millisecond)

	if mq.Count() > 0 {
		t.Errorf("expected 0 tasks, got %d", mq.Count())
	}

	// Wait for the first scheduled interval to pass
	time.Sleep(600 * time.Millisecond)
	eng.Stop()

	// Now the trigger should have fired at least once on schedule
	if mq.Count() < 1 {
		t.Errorf("expected at least 1 task after first interval, got %d", mq.Count())
	}
}

func TestEngine_NextFireTime_Webhook(t *testing.T) {
	dir := t.TempDir()

	configYAML := `
triggers:
  - id: "webhook-trigger"
    type: periodic
    schedule: "@webhook"
    prompt: "test"
`
	if err := os.WriteFile(filepath.Join(dir, "triggers.yaml"), []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	eng, err := engine.New(engine.Config{
		TriggerDir: dir,
		DataDir:    t.TempDir(),
		Queue:      &mockQueue{},
	})
	if err != nil {
		t.Fatal(err)
	}

	next := eng.NextFireTime("webhook-trigger")
	if !next.IsZero() {
		t.Errorf("expected zero next fire time for @webhook trigger, got %v", next)
	}
}
