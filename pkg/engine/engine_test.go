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

func TestEngine_NextFireTime_CronTZ(t *testing.T) {
	dir := t.TempDir()

	// Schedule that fires at 9:00 every day
	configYAML := `
triggers:
  - id: "cron-tz-test"
    type: periodic
    schedule: "0 9 * * *"
    prompt: "daily at 9"
`
	if err := os.WriteFile(filepath.Join(dir, "triggers.yaml"), []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	// Use UTC — the next fire should be at 09:00 UTC
	utcLoc, err := time.LoadLocation("UTC")
	if err != nil {
		t.Fatalf("unexpected error loading UTC: %v", err)
	}

	eng, err := engine.New(engine.Config{
		TriggerDir:   dir,
		DataDir:      t.TempDir(),
		Queue:        &mockQueue{},
		CronLocation: utcLoc,
	})
	if err != nil {
		t.Fatalf("unexpected error creating engine: %v", err)
	}

	next := eng.NextFireTime("cron-tz-test")
	if next.IsZero() {
		t.Fatal("expected non-zero next fire time")
	}

	// The next fire should be at 09:00:00 in UTC
	if next.Hour() != 9 || next.Minute() != 0 || next.Second() != 0 {
		t.Errorf("expected next fire at 09:00:00 UTC, got %02d:%02d:%02d (loc: %s)", next.Hour(), next.Minute(), next.Second(), next.Location().String())
	}

	// It should be in the future (or very close to now if it's almost 9 AM UTC)
	if next.Before(time.Now()) {
		t.Errorf("expected next fire to be in the future, got %v", next)
	}
}

func TestEngine_NextFireTime_CronTZ_CrossTimezone(t *testing.T) {
	// Compare NextFireTime across two fixed-offset timezones to prove
	// the schedule is actually evaluated in the configured timezone.
	// If the engine ignored CronLocation and always used local time,
	// both engines would return the same UTC instant.
	//
	// We use UTC and Asia/Tokyo (UTC+9, no DST) so the UTC-hours of
	// the fire times are deterministic regardless of when the test runs.
	dir := t.TempDir()

	configYAML := `
triggers:
  - id: "cron-tz-test"
    type: periodic
    schedule: "0 9 * * *"
    prompt: "daily at 9"
`
	if err := os.WriteFile(filepath.Join(dir, "triggers.yaml"), []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	utcLoc, err := time.LoadLocation("UTC")
	if err != nil {
		t.Fatalf("unexpected error loading UTC: %v", err)
	}
	tokyoLoc, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatalf("unexpected error loading Asia/Tokyo: %v", err)
	}

	engUTC, err := engine.New(engine.Config{
		TriggerDir:   dir,
		DataDir:      t.TempDir(),
		Queue:        &mockQueue{},
		CronLocation: utcLoc,
	})
	if err != nil {
		t.Fatalf("unexpected error creating UTC engine: %v", err)
	}

	engTokyo, err := engine.New(engine.Config{
		TriggerDir:   dir,
		DataDir:      t.TempDir(),
		Queue:        &mockQueue{},
		CronLocation: tokyoLoc,
	})
	if err != nil {
		t.Fatalf("unexpected error creating Tokyo engine: %v", err)
	}

	nextUTC := engUTC.NextFireTime("cron-tz-test")
	nextTokyo := engTokyo.NextFireTime("cron-tz-test")

	if nextUTC.IsZero() || nextTokyo.IsZero() {
		t.Fatal("expected non-zero next fire times")
	}

	// 9 AM UTC → UTC hour is 9
	if nextUTC.UTC().Hour() != 9 {
		t.Errorf("UTC engine: expected UTC hour 9, got %d (%s)", nextUTC.UTC().Hour(), nextUTC.UTC().Format(time.RFC3339))
	}

	// 9 AM JST (UTC+9) → UTC hour is 0
	if nextTokyo.UTC().Hour() != 0 {
		t.Errorf("Tokyo engine: expected UTC hour 0 (9 AM JST = midnight UTC), got %d (%s)", nextTokyo.UTC().Hour(), nextTokyo.UTC().Format(time.RFC3339))
	}
}

func TestEngine_NextFireTime_DefaultUsesLocal(t *testing.T) {
	dir := t.TempDir()

	configYAML := `
triggers:
  - id: "default-local"
    type: periodic
    schedule: "0 12 * * *"
    prompt: "noon"
`
	if err := os.WriteFile(filepath.Join(dir, "triggers.yaml"), []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	// No CronLocation set — should use system local time
	eng, err := engine.New(engine.Config{
		TriggerDir: dir,
		DataDir:    t.TempDir(),
		Queue:      &mockQueue{},
	})
	if err != nil {
		t.Fatalf("unexpected error creating engine: %v", err)
	}

	next := eng.NextFireTime("default-local")
	if next.IsZero() {
		t.Fatal("expected non-zero next fire time")
	}

	// Should be at 12:00:00 in local time
	if next.Hour() != 12 || next.Minute() != 0 || next.Second() != 0 {
		t.Errorf("expected next fire at 12:00:00 local, got %02d:%02d:%02d (loc: %s)", next.Hour(), next.Minute(), next.Second(), next.Location().String())
	}
}

func TestEngine_HoldOffCondition_PreventsDispatch(t *testing.T) {
	dir := t.TempDir()

	// Hold off if LastRun is zero (never run before). After the first
	// non-hold-off fire, state is saved and subsequent fires should proceed.
	// But since the condition always holds off on zero LastRun, the trigger
	// will never fire at all — proving the hold-off works.
	configYAML := `
triggers:
  - id: "always-holdoff"
    type: periodic
    schedule: "@every 500ms"
    hold-off-condition: "{{ eq .LastRun \"0001-01-01T00:00:00Z\" }}"
    prompt: "should never fire"
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

	// Wait for several scheduled intervals
	time.Sleep(2 * time.Second)
	eng.Stop()

	// Trigger should never have dispatched a task
	if mq.Count() != 0 {
		t.Errorf("expected 0 tasks (always held off), got %d", mq.Count())
	}

	// State should not have been persisted (hold-off skips state save)
	st, err := state.NewStore(dataDir)
	if err != nil {
		t.Fatalf("unexpected error creating state store: %v", err)
	}
	_, err = st.Load("always-holdoff")
	if err == nil {
		t.Error("expected no state for always-held-off trigger")
	}
}

func TestEngine_HoldOffCondition_FiresWhenFalse(t *testing.T) {
	dir := t.TempDir()

	// Hold off condition that is always false — trigger should fire normally
	configYAML := `
triggers:
  - id: "never-holdoff"
    type: periodic
    schedule: "@every 500ms"
    hold-off-condition: "false"
    prompt: "always fires"
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

	time.Sleep(1500 * time.Millisecond)
	eng.Stop()

	// Trigger should have fired at least once
	if mq.Count() < 1 {
		t.Errorf("expected at least 1 task, got %d", mq.Count())
	}
}

func TestEngine_PersonaPassedThrough(t *testing.T) {
	dir := t.TempDir()

	configYAML := `
triggers:
  - id: "persona-test"
    type: periodic
    schedule: "@every 500ms"
    persona: s-issue-implementer
    prompt: "implement a feature"
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
	if len(tasks) < 1 {
		t.Fatalf("expected at least 1 task, got %d", len(tasks))
	}

	if tasks[0].Persona != "s-issue-implementer" {
		t.Errorf("expected persona 's-issue-implementer', got %q", tasks[0].Persona)
	}
}
