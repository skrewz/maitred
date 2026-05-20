// Package engine provides the periodic trigger engine for maitred.
// It loads trigger definitions from YAML files, schedules them according
// to their cron/duration expressions, and dispatches tasks to a
// TaskQueueProvider on each execution.
package engine

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"maitred/pkg/queue"
	"maitred/pkg/state"
	"maitred/pkg/trigger"
)

// TaskQueueProvider is the interface that any queue system must implement
// to receive tasks from the trigger engine. This decouples the engine from
// any specific queue implementation (hotelier, custom HTTP, etc.).
type TaskQueueProvider interface {
	AddTask(task *queue.Task) error
}

// Config holds the configuration for the trigger engine.
type Config struct {
	// TriggerDir is the directory containing trigger YAML files (.d/ convention).
	TriggerDir string
	// DataDir is the directory for persistent trigger state.
	DataDir string
	// Queue is the destination for generated tasks.
	Queue TaskQueueProvider
}

// Engine manages the lifecycle of periodic triggers. It loads trigger
// definitions from YAML files, schedules them, and dispatches tasks to
// the configured queue on each execution.
type Engine struct {
	cfg       Config
	defs      []trigger.TriggerDefinition
	st        *state.Store
	history   *ExecutionHistory
	log       *log.Logger
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	triggerWg sync.WaitGroup
	mu        sync.RWMutex
	// paused tracks which trigger IDs are temporarily disabled
	paused map[string]struct{}
}

// New creates a new Engine with the given configuration. It loads trigger
// definitions from the config directory and initializes the state store.
func New(cfg Config) (*Engine, error) {
	defs, err := trigger.LoadTriggerDefinitions(cfg.TriggerDir)
	if err != nil {
		return nil, fmt.Errorf("load trigger definitions: %w", err)
	}

	st, err := state.NewStore(cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("create state store: %w", err)
	}

	return &Engine{
		cfg:     cfg,
		defs:    defs,
		st:      st,
		history: NewExecutionHistory(50),
		log:     log.Default(),
		paused:  make(map[string]struct{}),
	}, nil
}

// Definitions returns the loaded trigger definitions.
func (e *Engine) Definitions() []trigger.TriggerDefinition {
	e.mu.RLock()
	defer e.mu.RUnlock()
	result := make([]trigger.TriggerDefinition, len(e.defs))
	copy(result, e.defs)
	return result
}

// Start begins the trigger engine. All triggers are scheduled and begin
// executing on their configured intervals. Returns immediately; triggers
// run in background goroutines.
func (e *Engine) Start() error {
	e.ctx, e.cancel = context.WithCancel(context.Background())

	e.mu.RLock()
	defs := make([]trigger.TriggerDefinition, len(e.defs))
	copy(defs, e.defs)
	e.mu.RUnlock()

	for _, def := range defs {
		e.wg.Add(1)
		go e.runTrigger(def)
	}

	return nil
}

// Stop gracefully shuts down the engine. All running triggers are
// cancelled and the function blocks until all goroutines have exited.
func (e *Engine) Stop() {
	if e.cancel != nil {
		e.cancel()
	}
	e.wg.Wait()
}

// runTrigger executes a single trigger according to its schedule.
// It loops until the engine is stopped, scheduling each execution
// after the previous one completes (not overlapping).
// Supports both @every durations and cron expressions.
func (e *Engine) runTrigger(def trigger.TriggerDefinition) {
	defer e.wg.Done()

	interval := parseDuration(def.Schedule)
	if interval > 0 {
		// @every schedule — use a ticker
		e.log.Printf("[trigger:%s] scheduling: %s (interval: %v)", def.ID, def.Schedule, interval)
		e.runWithTicker(def, interval)
		return
	}

	// Cron schedule — use a cron runner
	c, err := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow).Parse(def.Schedule)
	if err != nil {
		e.log.Printf("[trigger:%s] invalid cron schedule: %v", def.ID, err)
		return
	}

	e.log.Printf("[trigger:%s] scheduling: %s", def.ID, def.Schedule)
	e.runWithCron(def, c)
}

// runWithTicker runs a trigger on a fixed interval using a ticker.
func (e *Engine) runWithTicker(def trigger.TriggerDefinition, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Run once immediately on startup
	e.executeTrigger(def)

	for {
		select {
		case <-e.ctx.Done():
			return
		case <-ticker.C:
			e.mu.RLock()
			_, isPaused := e.paused[def.ID]
			e.mu.RUnlock()
			if !isPaused {
				e.executeTrigger(def)
			}
		}
	}
}

// runWithCron runs a trigger on a cron schedule.
func (e *Engine) runWithCron(def trigger.TriggerDefinition, spec cron.Schedule) {
	// Run once immediately on startup
	e.executeTrigger(def)

	for {
		// Wait until the next scheduled time
		next := spec.Next(time.Now())
		if next.IsZero() {
			// Cron spec has no more future times (e.g., one-time schedule)
			return
		}

		select {
		case <-e.ctx.Done():
			return
		case <-time.After(time.Until(next)):
			e.mu.RLock()
			_, isPaused := e.paused[def.ID]
			e.mu.RUnlock()
			if !isPaused {
				e.executeTrigger(def)
			}
		}
	}
}

// PauseTrigger temporarily disables a trigger by ID. It will not fire
// until ResumeTrigger is called or the engine is restarted.
func (e *Engine) PauseTrigger(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.paused[id] = struct{}{}
	e.log.Printf("[trigger:%s] paused", id)
}

// ResumeTrigger re-enables a previously paused trigger.
func (e *Engine) ResumeTrigger(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.paused, id)
	e.log.Printf("[trigger:%s] resumed", id)
}

// IsPaused returns whether a trigger is currently paused.
func (e *Engine) IsPaused(id string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	_, ok := e.paused[id]
	return ok
}

// FireNow manually fires a trigger immediately, bypassing the schedule.
func (e *Engine) FireNow(id string) error {
	e.mu.RLock()
	_, isPaused := e.paused[id]
	var def *trigger.TriggerDefinition
	for _, d := range e.defs {
		if d.ID == id {
			tmp := d
			def = &tmp
			break
		}
	}
	e.mu.RUnlock()

	if def == nil {
		return fmt.Errorf("trigger %s not found", id)
	}
	if isPaused {
		return fmt.Errorf("trigger %s is paused", id)
	}

	e.executeTrigger(*def)
	return nil
}

// executeTrigger runs a single execution of a trigger: loads state,
// evaluates the prompt template, creates a task, dispatches it, and
// saves the updated state. Records the execution in the history.
func (e *Engine) executeTrigger(def trigger.TriggerDefinition) {
	now := time.Now()

	// Load previous state (first run has no state)
	lastState, err := e.st.Load(def.ID)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		e.log.Printf("[trigger:%s] failed to load state: %v", def.ID, err)
		e.history.Append(def.ID, ExecutionRecord{
			Timestamp: now,
			Success:   false,
			Error:     err.Error(),
		})
		return
	}

	var lastRun time.Time
	if lastState != nil {
		lastRun = lastState.LastRun
	}

	// Evaluate prompt template
	prompt, err := def.EvalPromptTemplate(lastRun)
	if err != nil {
		e.log.Printf("[trigger:%s] failed to evaluate prompt: %v", def.ID, err)
		e.history.Append(def.ID, ExecutionRecord{
			Timestamp: now,
			Success:   false,
			Error:     err.Error(),
		})
		return
	}

	// Create task
	task := &queue.Task{
		ID:      fmt.Sprintf("task-%s-%d", def.ID, time.Now().UnixNano()),
		Prompt:  prompt,
		Repos:   def.Repos,
		Tags:    def.Tags,
		Timeout: def.Timeout,
	}

	// Dispatch to queue
	if err := e.cfg.Queue.AddTask(task); err != nil {
		e.log.Printf("[trigger:%s] failed to dispatch task: %v", def.ID, err)
		e.history.Append(def.ID, ExecutionRecord{
			Timestamp: now,
			TaskID:    task.ID,
			Success:   false,
			Error:     err.Error(),
		})
		return
	}

	e.log.Printf("[trigger:%s] dispatched task %s", def.ID, task.ID)
	e.history.Append(def.ID, ExecutionRecord{
		Timestamp: now,
		TaskID:    task.ID,
		Success:   true,
	})

	// Save updated state
	if err := e.st.Save(def.ID, now, nil); err != nil {
		e.log.Printf("[trigger:%s] failed to save state: %v", def.ID, err)
	}
}

// History returns a reference to the execution history.
// Callers should treat it as read-only.
func (e *Engine) History() *ExecutionHistory {
	return e.history
}

// Queue returns the configured TaskQueueProvider.
func (e *Engine) Queue() TaskQueueProvider {
	return e.cfg.Queue
}

// StateStore returns the engine's state store.
func (e *Engine) StateStore() *state.Store {
	return e.st
}

// NextFireTime calculates the next scheduled fire time for a trigger.
// Returns the zero time if the trigger is paused or has an invalid schedule.
func (e *Engine) NextFireTime(id string) time.Time {
	e.mu.RLock()
	if _, paused := e.paused[id]; paused {
		e.mu.RUnlock()
		return time.Time{}
	}
	var def *trigger.TriggerDefinition
	for _, d := range e.defs {
		if d.ID == id {
			tmp := d
			def = &tmp
			break
		}
	}
	e.mu.RUnlock()

	if def == nil {
		return time.Time{}
	}

	interval := parseDuration(def.Schedule)
	if interval > 0 {
		// @every schedule
		lastState, err := e.st.Load(id)
		if err != nil {
			return time.Time{}
		}
		return lastState.LastRun.Add(interval)
	}

	// Cron schedule
	c, err := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow).Parse(def.Schedule)
	if err != nil {
		return time.Time{}
	}

	// Next fire time is the next cron occurrence after now
	next := c.Next(time.Now())
	if next.IsZero() {
		return time.Time{}
	}
	return next
}

// parseDuration extracts a time.Duration from a schedule string.
// Returns 0 if the schedule is not @every format.
func parseDuration(sched string) time.Duration {
	const prefix = "@every "
	if len(sched) >= len(prefix) && sched[:len(prefix)] == prefix {
		dur, err := time.ParseDuration(sched[len(prefix):])
		if err == nil {
			return dur
		}
	}
	return 0
}
