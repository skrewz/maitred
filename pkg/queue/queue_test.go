package queue_test

import (
	"encoding/json"
	"strings"
	"testing"

	"maitred/pkg/queue"
)

func TestTask_String(t *testing.T) {
	task := &queue.Task{
		ID:      "task-1",
		Prompt:  "test prompt",
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

func TestTask_Persona(t *testing.T) {
	task := &queue.Task{
		ID:      "task-1",
		Prompt:  "test prompt",
		Tags:    []string{"business-default"},
		Timeout: 3600,
		Persona: "s-autonomics-implementer",
	}

	if task.Persona != "s-autonomics-implementer" {
		t.Errorf("expected persona 's-autonomics-implementer', got %q", task.Persona)
	}

	// Empty persona should be zero-value
	taskNoPersona := &queue.Task{
		ID:     "task-2",
		Prompt: "no persona task",
	}
	if taskNoPersona.Persona != "" {
		t.Errorf("expected empty persona, got %q", taskNoPersona.Persona)
	}
}

func TestTask_TitleAndURL(t *testing.T) {
	q := queue.NewTaskQueue()
	task := &queue.Task{
		ID:     "task-1",
		Prompt: "do something",
		Title:  "Issue o/r#7: name the work",
		URL:    "https://forge.example.com/o/r/issues/7",
	}

	if err := q.Add(task); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The in-memory queue passes the fields through untouched.
	got, ok := q.Get("task-1")
	if !ok {
		t.Fatal("expected task to be found")
	}
	if got.Title != "Issue o/r#7: name the work" {
		t.Errorf("expected title passthrough, got %q", got.Title)
	}
	if got.URL != "https://forge.example.com/o/r/issues/7" {
		t.Errorf("expected url passthrough, got %q", got.URL)
	}

	// Empty title/URL are zero values.
	bare := &queue.Task{ID: "task-2", Prompt: "plain"}
	if bare.Title != "" || bare.URL != "" {
		t.Errorf("expected empty title/url, got %q/%q", bare.Title, bare.URL)
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

func TestTask_DedupKey_JSON(t *testing.T) {
	// Omitted when empty.
	b, err := json.Marshal(&queue.Task{ID: "task-1", Prompt: "p"})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(b), "dedup_key") {
		t.Errorf("dedup_key should be omitted when empty, got %s", b)
	}

	// Present when set.
	b, err = json.Marshal(&queue.Task{ID: "task-1", Prompt: "p", DedupKey: "k"})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(b), `"dedup_key":"k"`) {
		t.Errorf("expected dedup_key in JSON, got %s", b)
	}
}

func TestTask_TitleAndURL_JSON(t *testing.T) {
	// Omitted when empty.
	b, err := json.Marshal(&queue.Task{ID: "task-1", Prompt: "p"})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(b), "title") || strings.Contains(string(b), `"url"`) {
		t.Errorf("title/url should be omitted when empty, got %s", b)
	}

	// Present when set.
	b, err = json.Marshal(&queue.Task{ID: "task-1", Prompt: "p", Title: "t", URL: "https://example.com/x"})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(b), `"title":"t"`) || !strings.Contains(string(b), `"url":"https://example.com/x"`) {
		t.Errorf("expected title/url in JSON, got %s", b)
	}
}

func TestTask_Priority_JSON(t *testing.T) {
	// Omitted when empty: an unset priority must say nothing, so the queue
	// applies its own default tier.
	b, err := json.Marshal(&queue.Task{ID: "task-1", Prompt: "p"})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(b), "priority") {
		t.Errorf("priority should be omitted when empty, got %s", b)
	}

	// Present when set, verbatim.
	b, err = json.Marshal(&queue.Task{ID: "task-1", Prompt: "p", Priority: "teacher"})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(b), `"priority":"teacher"`) {
		t.Errorf("expected priority in JSON, got %s", b)
	}
}
