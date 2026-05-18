package state_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"maitred/pkg/state"
)

func TestStore_SaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	s, err := state.NewStore(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	lastRun := time.Date(2025, 5, 17, 10, 0, 0, 0, time.UTC)
	if err := s.Save("trigger-1", lastRun, nil); err != nil {
		t.Fatalf("unexpected error saving: %v", err)
	}

	loaded, err := s.Load("trigger-1")
	if err != nil {
		t.Fatalf("unexpected error loading: %v", err)
	}

	if !loaded.LastRun.Equal(lastRun) {
		t.Errorf("expected lastRun %v, got %v", lastRun, loaded.LastRun)
	}
}

func TestStore_Load_NotFound(t *testing.T) {
	dir := t.TempDir()
	s, err := state.NewStore(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = s.Load("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent trigger, got nil")
	}
}

func TestStore_Load_AllTriggers(t *testing.T) {
	dir := t.TempDir()
	s, err := state.NewStore(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	now := time.Date(2025, 5, 17, 10, 0, 0, 0, time.UTC)
	s.Save("trigger-1", now, map[string]interface{}{"key": "value"})
	s.Save("trigger-2", now.Add(time.Hour), nil)

	all, err := s.LoadAll()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(all) != 2 {
		t.Fatalf("expected 2 triggers, got %d", len(all))
	}

	// Check trigger-1 has extra state
	if v, ok := all["trigger-1"].ExtraState["key"]; !ok || v != "value" {
		t.Errorf("expected extraState key=value for trigger-1, got %v", all["trigger-1"].ExtraState)
	}

	// Check trigger-2 has no extra state
	if all["trigger-2"].ExtraState != nil {
		t.Errorf("expected nil extraState for trigger-2, got %v", all["trigger-2"].ExtraState)
	}
}

func TestStore_LoadAll_Empty(t *testing.T) {
	dir := t.TempDir()
	s, err := state.NewStore(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	all, err := s.LoadAll()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if all == nil {
		t.Error("expected empty map, got nil")
	}
	if len(all) != 0 {
		t.Errorf("expected 0 triggers, got %d", len(all))
	}
}

func TestStore_Save_Overwrite(t *testing.T) {
	dir := t.TempDir()
	s, err := state.NewStore(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	now1 := time.Date(2025, 5, 17, 10, 0, 0, 0, time.UTC)
	s.Save("trigger-1", now1, nil)

	now2 := time.Date(2025, 5, 17, 12, 0, 0, 0, time.UTC)
	s.Save("trigger-1", now2, map[string]interface{}{"count": 42})

	loaded, err := s.Load("trigger-1")
	if err != nil {
		t.Fatalf("unexpected error loading: %v", err)
	}

	if !loaded.LastRun.Equal(now2) {
		t.Errorf("expected lastRun %v, got %v", now2, loaded.LastRun)
	}
	if loaded.ExtraState["count"].(float64) != 42 {
		t.Errorf("expected count 42, got %v", loaded.ExtraState["count"])
	}
}

func TestStore_DataDir_Created(t *testing.T) {
	parentDir := t.TempDir()
	dataDir := filepath.Join(parentDir, "subdir", "data")

	_, err := state.NewStore(dataDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The directory should have been created
	info, err := os.Stat(dataDir)
	if err != nil {
		t.Fatalf("data directory not created: %v", err)
	}
	if !info.IsDir() {
		t.Error("data path is not a directory")
	}
}

func TestStore_Save_InvalidDir(t *testing.T) {
	_, err := state.NewStore("/nonexistent/deeply/nested/dir/that/cannot/exist/xyz123")
	if err == nil {
		t.Error("expected error for invalid directory, got nil")
	}
}
