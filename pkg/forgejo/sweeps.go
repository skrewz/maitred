package forgejo

import "errors"

// ErrUnknownSweep names a sweep trigger for an id outside the engine's
// catalog (§forgejo/observability/triggering-a-sweep).
var ErrUnknownSweep = errors.New("unknown sweep")

// ErrSweepNotRunning names a sweep trigger on an engine whose sweep
// loop is not running: outside the loop no start has a lifecycle owner
// to wait it out at Stop (§forgejo/observability/triggering-a-sweep,
// §forgejo/reconciliation/scheduling).
var ErrSweepNotRunning = errors.New("sweep loop not running")

// SweepIDReconcile is the catalog id of the reconciliation sweep
// (§forgejo/reconciliation/the-sweep).
const SweepIDReconcile = "reconcile"

// Sweep is one sweep operation the engine offers for manual triggering
// (§forgejo/observability/triggering-a-sweep). The catalog is the
// whole set of sweeps: every surface that lets a human trigger a sweep
// reads it, so adding a sweep never means touching those surfaces.
type Sweep struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// Sweeps is the engine's catalog of sweep operations — the
// reconciliation sweep today
// (§forgejo/observability/triggering-a-sweep).
func (e *Engine) Sweeps() []Sweep {
	return []Sweep{{
		ID:    SweepIDReconcile,
		Title: "Reconciliation sweep",
	}}
}

// FireSweep starts the named sweep now rather than at the next tick,
// asynchronously: a sweep can make many API calls, so the trigger
// reports acceptance, not completion. The id is validated against the
// catalog — the one place the sweep set lives — and the start is
// handed to the sweep loop, the same owner that runs scheduled sweeps,
// so a manual start is a sweep start like any other: Reconcile's
// skip-if-in-flight rule and Stop's wait for the in-flight sweep both
// cover it unchanged (§forgejo/observability/triggering-a-sweep,
// §forgejo/reconciliation/scheduling). An id outside the catalog is
// refused and nothing runs; an engine whose sweep loop is not running
// is refused too.
func (e *Engine) FireSweep(id string) error {
	for _, s := range e.Sweeps() {
		if s.ID != id {
			continue
		}
		e.schedMu.Lock()
		defer e.schedMu.Unlock()
		if e.startCh == nil {
			return ErrSweepNotRunning
		}
		select {
		case e.startCh <- struct{}{}:
		default:
			// A manual start is already queued and a sweep is coming
			// anyway — dropping this signal loses nothing, the way a
			// start during an in-flight sweep is skipped.
		}
		e.log.Info("sweep triggered manually", "sweep", id)
		return nil
	}
	return ErrUnknownSweep
}
