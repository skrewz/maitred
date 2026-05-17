// Package engine provides the periodic trigger engine for maitre-d.
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

	"maitre-d/pkg/queue"
	"maitre-d/pkg/state"
	"maitre-d/pkg/trigger"
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
	cfg      Config
	defs  []trigger.TriggerDefinition
	st    *state.Store
	log   *log.Logger
	ctx   context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	mu       sync.RWMutex
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
		cfg:      cfg,
		defs: defs,
		st:   st,
		log:  log.Default(),
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
func (e *Engine) runTrigger(def trigger.TriggerDefinition) {
	defer e.wg.Done()

	sched, err := trigger.ParseSchedule(def.Schedule)
	if err != nil {
		e.log.Printf("[trigger:%s] invalid schedule: %v", def.ID, err)
		return
	}

	// Determine the interval for @every schedules
	interval := parseDuration(sched)
	if interval == 0 {
		e.log.Printf("[trigger:%s] could not parse interval from schedule %q, skipping", def.ID, sched)
		return
	}

	e.log.Printf("[trigger:%s] scheduling: %s (interval: %v)", def.ID, sched, interval)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Run once immediately on startup
	e.executeTrigger(def)

	for {
		select {
		case <-e.ctx.Done():
			return
		case <-ticker.C:
			e.executeTrigger(def)
		}
	}
}

// executeTrigger runs a single execution of a trigger: loads state,
// evaluates the prompt template, creates a task, dispatches it, and
// saves the updated state.
func (e *Engine) executeTrigger(def trigger.TriggerDefinition) {
	// Load previous state (first run has no state)
	lastState, err := e.st.Load(def.ID)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		e.log.Printf("[trigger:%s] failed to load state: %v", def.ID, err)
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
		return
	}

	e.log.Printf("[trigger:%s] dispatched task %s", def.ID, task.ID)

	// Save updated state
	if err := e.st.Save(def.ID, time.Now(), nil); err != nil {
		e.log.Printf("[trigger:%s] failed to save state: %v", def.ID, err)
	}
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


