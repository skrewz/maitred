package engine_test

import (
	"testing"
	"time"

	"maitred/pkg/engine"
)

func TestExecutionHistory_AppendAndLast(t *testing.T) {
	h := engine.NewExecutionHistory(10)

	now := time.Date(2025, 5, 17, 10, 0, 0, 0, time.UTC)
	h.Append("trigger-1", engine.ExecutionRecord{
		Timestamp: now,
		TaskID:    "task-abc",
		Success:   true,
	})

	last := h.Last("trigger-1")
	if last == nil {
		t.Fatal("expected last record")
	}
	if !last.Success {
		t.Error("expected success")
	}
	if last.TaskID != "task-abc" {
		t.Errorf("expected task-abc, got %s", last.TaskID)
	}
}

func TestExecutionHistory_Last_NotFound(t *testing.T) {
	h := engine.NewExecutionHistory(10)
	if h.Last("nonexistent") != nil {
		t.Error("expected nil for nonexistent trigger")
	}
}

func TestExecutionHistory_Recent(t *testing.T) {
	h := engine.NewExecutionHistory(10)

	now := time.Date(2025, 5, 17, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		h.Append("trigger-1", engine.ExecutionRecord{
			Timestamp: now.Add(time.Duration(i) * time.Hour),
			Success:   i%2 == 0,
		})
	}

	recent := h.Recent("trigger-1", 3)
	if len(recent) != 3 {
		t.Fatalf("expected 3 records, got %d", len(recent))
	}
	// Should be the last 3 (indices 2, 3, 4)
	if !recent[0].Success {
		t.Error("expected index 2 to be success")
	}
	if recent[1].Success {
		t.Error("expected index 3 to be failure")
	}
	if !recent[2].Success {
		t.Error("expected index 4 to be success")
	}
}

func TestExecutionHistory_Recent_Empty(t *testing.T) {
	h := engine.NewExecutionHistory(10)
	recent := h.Recent("nonexistent", 5)
	if recent == nil {
		t.Error("expected empty slice, got nil")
	}
	if len(recent) != 0 {
		t.Errorf("expected 0 records, got %d", len(recent))
	}
}

func TestExecutionHistory_Cap(t *testing.T) {
	h := engine.NewExecutionHistory(3)

	now := time.Date(2025, 5, 17, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 10; i++ {
		h.Append("trigger-1", engine.ExecutionRecord{
			Timestamp: now.Add(time.Duration(i) * time.Hour),
			Success:   true,
		})
	}

	recent := h.Recent("trigger-1", 100)
	if len(recent) != 3 {
		t.Fatalf("expected 3 records (cap), got %d", len(recent))
	}
}

func TestExecutionHistory_All(t *testing.T) {
	h := engine.NewExecutionHistory(10)

	h.Append("trigger-1", engine.ExecutionRecord{Timestamp: time.Now(), Success: true})
	h.Append("trigger-2", engine.ExecutionRecord{Timestamp: time.Now(), Success: false, Error: "boom"})

	all := h.All()
	if len(all) != 2 {
		t.Fatalf("expected 2 triggers, got %d", len(all))
	}
	if len(all["trigger-1"]) != 1 {
		t.Error("expected 1 record for trigger-1")
	}
	if len(all["trigger-2"]) != 1 {
		t.Error("expected 1 record for trigger-2")
	}
	if all["trigger-2"][0].Error != "boom" {
		t.Errorf("expected error 'boom', got %q", all["trigger-2"][0].Error)
	}
}

func TestExecutionHistory_DefaultMaxEntries(t *testing.T) {
	h := engine.NewExecutionHistory(0) // should default to 50
	h.Append("t", engine.ExecutionRecord{Success: true})
	if h.Last("t") == nil {
		t.Error("expected record to be stored with default max")
	}
}
