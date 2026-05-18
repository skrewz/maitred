package engine_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"maitred/pkg/engine"
)

func TestEngine_PauseResume(t *testing.T) {
	dir := t.TempDir()

	configYAML := `
triggers:
  - id: "pause-test"
    type: periodic
    schedule: "@every 2s"
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

	// Pause before starting
	eng.PauseTrigger("pause-test")

	if err := eng.Start(); err != nil {
		t.Fatal(err)
	}

	// Wait for the initial run (happens before pause check in ticker loop)
	time.Sleep(500 * time.Millisecond)
	beforeCount := mq.Count()

	// Wait - no new tasks should be created while paused
	time.Sleep(4 * time.Second)
	afterCount := mq.Count()
	if afterCount != beforeCount {
		t.Errorf("expected no new tasks while paused (before=%d, after=%d)", beforeCount, afterCount)
	}

	// Resume
	eng.ResumeTrigger("pause-test")
	if eng.IsPaused("pause-test") {
		t.Error("expected trigger to be resumed")
	}

	// Wait for at least one new run after resume
	time.Sleep(3 * time.Second)
	resumedCount := mq.Count()
	if resumedCount <= afterCount {
		t.Errorf("expected new tasks after resume (before=%d, after=%d)", afterCount, resumedCount)
	}

	eng.Stop()
}

func TestEngine_FireNow(t *testing.T) {
	dir := t.TempDir()

	configYAML := `
triggers:
  - id: "fire-test"
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
		t.Fatal(err)
	}

	if err := eng.Start(); err != nil {
		t.Fatal(err)
	}

	// Fire now
	if err := eng.FireNow("fire-test"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if mq.Count() < 1 {
		t.Errorf("expected at least 1 task from FireNow, got %d", mq.Count())
	}

	eng.Stop()
}

func TestEngine_FireNow_NotFound(t *testing.T) {
	dir := t.TempDir()

	configYAML := `
triggers:
  - id: "exists"
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
		t.Fatal(err)
	}

	if err := eng.FireNow("nonexistent"); err == nil {
		t.Error("expected error for nonexistent trigger")
	}
}

func TestEngine_FireNow_Paused(t *testing.T) {
	dir := t.TempDir()

	configYAML := `
triggers:
  - id: "paused-test"
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
		t.Fatal(err)
	}

	eng.PauseTrigger("paused-test")
	if err := eng.FireNow("paused-test"); err == nil {
		t.Error("expected error for paused trigger")
	}
}

func TestEngine_NextFireTime(t *testing.T) {
	dir := t.TempDir()

	configYAML := `
triggers:
  - id: "firetime-test"
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
		t.Fatal(err)
	}

	if err := eng.Start(); err != nil {
		t.Fatal(err)
	}

	// Wait for first execution
	time.Sleep(1 * time.Second)

	next := eng.NextFireTime("firetime-test")
	if next.IsZero() {
		t.Error("expected non-zero next fire time")
	}

	// Pause and check
	eng.PauseTrigger("firetime-test")
	next = eng.NextFireTime("firetime-test")
	if !next.IsZero() {
		t.Error("expected zero next fire time for paused trigger")
	}

	eng.Stop()
}

func TestEngine_NextFireTime_NotFound(t *testing.T) {
	dir := t.TempDir()

	configYAML := `
triggers:
  - id: "exists"
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
		t.Fatal(err)
	}

	next := eng.NextFireTime("nonexistent")
	if !next.IsZero() {
		t.Error("expected zero next fire time for nonexistent trigger")
	}
}
