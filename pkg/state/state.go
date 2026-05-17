// Package state provides persistent storage for trigger execution state.
// Each trigger's last run time and arbitrary extra state (for variables
// injected into prompt templates) is stored as a JSON file keyed by
// trigger ID.
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// TriggerState holds the execution state for a single trigger.
type TriggerState struct {
	LastRun    time.Time            `json:"last_run"`
	ExtraState map[string]interface{} `json:"extra_state,omitempty"`
}

// Store manages persistent trigger state on disk. Each trigger's state is
// stored as a separate JSON file named {trigger_id}.json in the data directory.
type Store struct {
	dir string
	mu  sync.RWMutex
}

// NewStore creates a new state store in the given directory. The directory
// is created if it does not exist.
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create state directory %q: %w", dir, err)
	}

	return &Store{
		dir: dir,
	}, nil
}

// Save persists the state for a trigger.
func (s *Store) Save(triggerID string, lastRun time.Time, extraState map[string]interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	state := TriggerState{
		LastRun:    lastRun,
		ExtraState: extraState,
	}

	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("marshal state for %s: %w", triggerID, err)
	}

	path := filepath.Join(s.dir, triggerID+".json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write state for %s: %w", triggerID, err)
	}

	return nil
}

// Load retrieves the state for a trigger. Returns an error if the trigger
// has never been run.
func (s *Store) Load(triggerID string) (*TriggerState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	path := filepath.Join(s.dir, triggerID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read state for %s: %w", triggerID, err)
	}

	var state TriggerState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parse state for %s: %w", triggerID, err)
	}

	return &state, nil
}

// LoadAll retrieves the state for all known triggers. Returns an empty map
// (never nil) if no triggers have been run yet.
func (s *Store) LoadAll() (map[string]*TriggerState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("read state directory: %w", err)
	}

	result := make(map[string]*TriggerState)

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if len(entry.Name()) < 5 {
			continue
		}
		if entry.Name()[len(entry.Name())-5:] != ".json" {
			continue
		}

		triggerID := entry.Name()[:len(entry.Name())-5]
		path := filepath.Join(s.dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			// Skip unreadable files
			continue
		}

		var state TriggerState
		if err := json.Unmarshal(data, &state); err != nil {
			// Skip corrupt files
			continue
		}

		result[triggerID] = &state
	}

	return result, nil
}
