// Package queue provides an in-memory task queue and the TaskQueueProvider
// interface that any downstream queue system can implement to receive tasks
// from the maitred trigger engine.
package queue

import (
	"fmt"
	"sync"
)

// TaskQueueProvider is the interface that any queue system must implement
// to receive tasks from the trigger engine. This decouples the engine from
// any specific queue implementation (HTTP adapter, in-memory, etc.).
type TaskQueueProvider interface {
	AddTask(task *Task) error
}

// Task represents a unit of work to be executed by an agent on a queue system.
// This is the minimal intersection of task schemas across queue systems.
type Task struct {
	ID      string   `json:"id"`
	Prompt  string   `json:"prompt"`
	Tags    []string `json:"tags,omitempty"`
	Timeout int      `json:"timeout,omitempty"` // seconds, 0 = unlimited
	// Persona is the name of a persona to apply when the task is executed.
	// On hotelier, this resolves to a named set of environment variables
	// and file copies that are applied to the task's working directory.
	Persona string `json:"persona,omitempty"`
	// DedupKey is an optional deduplication key. A queue system that
	// supports it (hotelier) squelches a submission whose key matches a
	// still-pending task's key, so a re-dispatch of the same logical task
	// does not enqueue a duplicate. Empty disables de-duplication.
	DedupKey string `json:"dedup_key,omitempty"`
	// Title is an optional human-readable name for the work the task
	// represents. The queue system may display it; maitred does not
	// interpret it. Empty omits the field.
	Title string `json:"title,omitempty"`
	// URL is an optional reference URL for the work the task represents
	// (for example the issue or pull request that triggered it). maitred
	// does not interpret it. Empty omits the field.
	URL string `json:"url,omitempty"`
	// Priority is the tier the queue should serve the task at. It is the
	// downstream queue's own vocabulary, forwarded verbatim: ValidPriorities
	// names the tiers it may hold, and the queue answers 400 for any other
	// non-empty value (see the hotelier API, POST /api/tasks and
	// POST /api/tasks/{id}/priority in
	// https://github.com/skrewz/hotelier/blob/main/README.md; the server
	// validates the field against that vocabulary in handleCreateTask,
	// verified at
	// https://github.com/skrewz/hotelier/blob/17b4f6d1a135eb4bbad8a35e56f47ce376184d49/internal/server/server.go#L2023
	// as of this writing). Empty means nothing was decided: the field is
	// left off the wire body and the queue's own default tier applies,
	// so the payload an unset priority produces is the payload maitred
	// produced before the field existed. A trigger's task carries the tier
	// its definition declares, if any (§overview/scope); forgejoeng sets it
	// where the `vip` label asks for the teacher tier
	// (§forgejo/webhook/vip-priority).
	Priority string `json:"priority,omitempty"`
}

// The queue's priority tiers, highest first. The vocabulary is the queue's
// own and maitred forwards it verbatim (§overview/scope); it is named here so
// the one place that asks "is this a tier the queue will accept?" — trigger
// validation — has an authority to ask. hotelier recognises these three and
// serves a task whose priority is absent at the lowest of them (see
// https://github.com/skrewz/hotelier/blob/main/README.md and
// https://github.com/skrewz/hotelier/blob/main/pkg/queue/queue.go).
const (
	PriorityFirefighter = "firefighter"
	PriorityTeacher     = "teacher"
	PriorityOrangutan   = "orangutan"
)

// validPriorities lists the tiers in descending order, the order the queue
// serves them in.
var validPriorities = []string{PriorityFirefighter, PriorityTeacher, PriorityOrangutan}

// ValidPriorities returns the tiers the queue recognises, highest first. The
// slice is a copy, so a caller may sort or format it freely.
func ValidPriorities() []string {
	return append([]string(nil), validPriorities...)
}

// IsValidPriority reports whether priority names a tier the queue recognises.
// The match is exact, as the queue's own match is: an empty priority is not a
// tier but a refusal to name one — the field is left off the wire body and the
// queue's default applies — so it is not valid here either. Where empty means
// "say nothing" is the caller's business; Task.Priority and the trigger schema
// both treat it that way (§overview/scope).
func IsValidPriority(priority string) bool {
	for _, p := range validPriorities {
		if p == priority {
			return true
		}
	}
	return false
}

// String returns a human-readable representation of the task.
func (t *Task) String() string {
	if t.ID == "" {
		return "<no-id>"
	}
	return t.ID
}

// TaskQueue is an in-memory FIFO queue for tasks. It implements the
// TaskQueueProvider interface so it can be used directly as a destination
// for the trigger engine.
type TaskQueue struct {
	tasks   map[string]*Task
	ordered []*Task // maintains insertion order
	mu      sync.RWMutex
}

// NewTaskQueue creates a new empty task queue.
func NewTaskQueue() *TaskQueue {
	return &TaskQueue{
		tasks:   make(map[string]*Task),
		ordered: make([]*Task, 0),
	}
}

// AddTask inserts a task into the queue. It is an alias for Add and
// implements the engine.TaskQueueProvider interface.
func (q *TaskQueue) AddTask(task *Task) error {
	return q.Add(task)
}

// Add inserts a task into the queue. Returns an error if a task with the
// same ID already exists.
func (q *TaskQueue) Add(task *Task) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	if _, exists := q.tasks[task.ID]; exists {
		return fmt.Errorf("task %s already exists", task.ID)
	}

	q.tasks[task.ID] = task
	q.ordered = append(q.ordered, task)
	return nil
}

// Get returns a task by ID. The second return value indicates whether the
// task was found.
func (q *TaskQueue) Get(id string) (*Task, bool) {
	q.mu.RLock()
	defer q.mu.RUnlock()

	task, exists := q.tasks[id]
	return task, exists
}

// Remove deletes a task from the queue. Returns an error if the task does
// not exist.
func (q *TaskQueue) Remove(id string) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	if _, exists := q.tasks[id]; !exists {
		return fmt.Errorf("task %s not found", id)
	}

	delete(q.tasks, id)

	for i, task := range q.ordered {
		if task.ID == id {
			q.ordered = append(q.ordered[:i], q.ordered[i+1:]...)
			break
		}
	}

	return nil
}

// GetAllTasks returns all tasks in insertion order. Returns an empty slice
// (never nil) when the queue is empty.
func (q *TaskQueue) GetAllTasks() []*Task {
	q.mu.RLock()
	defer q.mu.RUnlock()

	result := make([]*Task, 0, len(q.tasks))
	for _, task := range q.ordered {
		result = append(result, task)
	}
	return result
}

// Count returns the number of tasks in the queue.
func (q *TaskQueue) Count() int {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return len(q.tasks)
}
