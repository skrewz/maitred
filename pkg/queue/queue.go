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
