package engine

import (
	"sync"
	"time"
)

// ExecutionRecord captures the result of a single trigger firing.
type ExecutionRecord struct {
	Timestamp time.Time `json:"timestamp"`
	TaskID    string    `json:"task_id,omitempty"`
	Success   bool      `json:"success"`
	Error     string    `json:"error,omitempty"`
}

// ExecutionHistory maintains a rolling log of recent trigger firings.
// Each trigger has its own history, capped at maxEntries.
type ExecutionHistory struct {
	mu         sync.RWMutex
	entries    map[string][]ExecutionRecord // triggerID -> records
	maxEntries int
}

// NewExecutionHistory creates a new history with the given max entries per trigger.
func NewExecutionHistory(maxEntries int) *ExecutionHistory {
	if maxEntries <= 0 {
		maxEntries = 50
	}
	return &ExecutionHistory{
		entries:    make(map[string][]ExecutionRecord),
		maxEntries: maxEntries,
	}
}

// Append adds a new execution record for a trigger, discarding oldest entries
// if the cap is exceeded.
func (h *ExecutionHistory) Append(triggerID string, record ExecutionRecord) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.entries[triggerID] = append(h.entries[triggerID], record)
	if len(h.entries[triggerID]) > h.maxEntries {
		h.entries[triggerID] = h.entries[triggerID][len(h.entries[triggerID])-h.maxEntries:]
	}
}

// Last returns the most recent execution record for a trigger, or nil if none.
func (h *ExecutionHistory) Last(triggerID string) *ExecutionRecord {
	h.mu.RLock()
	defer h.mu.RUnlock()

	records := h.entries[triggerID]
	if len(records) == 0 {
		return nil
	}
	return &records[len(records)-1]
}

// Recent returns the last n execution records for a trigger.
func (h *ExecutionHistory) Recent(triggerID string, n int) []ExecutionRecord {
	h.mu.RLock()
	defer h.mu.RUnlock()

	records := h.entries[triggerID]
	if len(records) == 0 {
		return []ExecutionRecord{}
	}
	if n > len(records) {
		n = len(records)
	}
	result := make([]ExecutionRecord, n)
	copy(result, records[len(records)-n:])
	return result
}

// All returns all execution records for all triggers.
func (h *ExecutionHistory) All() map[string][]ExecutionRecord {
	h.mu.RLock()
	defer h.mu.RUnlock()

	result := make(map[string][]ExecutionRecord, len(h.entries))
	for k, v := range h.entries {
		result[k] = v
	}
	return result
}
