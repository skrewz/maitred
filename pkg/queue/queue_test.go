package queue_test

import (
	"testing"

	"maitred/pkg/queue"
)

func TestTask_String(t *testing.T) {
	task := &queue.Task{
		ID:      "task-1",
		Prompt:  "test prompt",
		Repos:   []string{"~/repos/hotelier"},
		Tags:    []string{"business-default"},
		Timeout: 3600,
	}

	str := task.String()
	if str != "task-1" {
		t.Errorf("expected 'task-1', got %q", str)
	}
}

func TestTask_String_EmptyID(t *testing.T) {
	task := &queue.Task{
		Prompt: "test prompt",
	}

	str := task.String()
	if str != "<no-id>" {
		t.Errorf("expected '<no-id>', got %q", str)
	}
}

func TestTaskQueue_Add(t *testing.T) {
	q := queue.NewTaskQueue()
	task := &queue.Task{
		ID:     "task-1",
		Prompt: "do something",
		Tags:   []string{"business-default"},
	}

	if err := q.Add(task); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if q.Count() != 1 {
		t.Errorf("expected count 1, got %d", q.Count())
	}

	// Adding a duplicate should error
	if err := q.Add(task); err == nil {
		t.Error("expected error for duplicate task ID, got nil")
	}
}

func TestTaskQueue_Get(t *testing.T) {
	q := queue.NewTaskQueue()
	task := &queue.Task{
		ID:     "task-1",
		Prompt: "do something",
	}

	q.Add(task)

	got, ok := q.Get("task-1")
	if !ok {
		t.Fatal("expected task to exist")
	}
	if got.ID != "task-1" {
		t.Errorf("expected task-1, got %s", got.ID)
	}

	_, ok = q.Get("nonexistent")
	if ok {
		t.Error("expected task not found")
	}
}

func TestTaskQueue_Remove(t *testing.T) {
	q := queue.NewTaskQueue()
	task := &queue.Task{
		ID:     "task-1",
		Prompt: "do something",
	}

	q.Add(task)
	if err := q.Remove("task-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if q.Count() != 0 {
		t.Errorf("expected count 0, got %d", q.Count())
	}

	// Removing non-existent should error
	if err := q.Remove("nonexistent"); err == nil {
		t.Error("expected error for removing non-existent task")
	}
}

func TestTaskQueue_GetAllTasks(t *testing.T) {
	q := queue.NewTaskQueue()

	q.Add(&queue.Task{ID: "task-1", Prompt: "first"})
	q.Add(&queue.Task{ID: "task-2", Prompt: "second"})
	q.Add(&queue.Task{ID: "task-3", Prompt: "third"})

	tasks := q.GetAllTasks()
	if len(tasks) != 3 {
		t.Fatalf("expected 3 tasks, got %d", len(tasks))
	}

	// Verify order is preserved (insertion order)
	if tasks[0].ID != "task-1" || tasks[1].ID != "task-2" || tasks[2].ID != "task-3" {
		t.Errorf("tasks not in insertion order: %v", tasks)
	}
}

func TestTaskQueue_GetAllTasks_Empty(t *testing.T) {
	q := queue.NewTaskQueue()
	tasks := q.GetAllTasks()
	if tasks == nil {
		t.Error("expected empty slice, got nil")
	}
	if len(tasks) != 0 {
		t.Errorf("expected 0 tasks, got %d", len(tasks))
	}
}
