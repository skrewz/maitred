package forgejo

import "errors"

// ErrUnknownSweep names a sweep trigger for an id outside the engine's
// catalog (§forgejo/observability/triggering-a-sweep).
var ErrUnknownSweep = errors.New("unknown sweep")

// SweepIDReconcile is the catalog id of the reconciliation sweep
// (§forgejo/reconciliation/the-sweep).
const SweepIDReconcile = "reconcile"

// Sweep is one sweep operation the engine offers for manual triggering
// (§forgejo/observability/triggering-a-sweep). The catalog is the
// whole set of sweeps: every surface that lets a human trigger a sweep
// reads it, so adding a sweep never means touching those surfaces.
type Sweep struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

// Sweeps is the engine's catalog of sweep operations — the
// reconciliation sweep today
// (§forgejo/observability/triggering-a-sweep).
func (e *Engine) Sweeps() []Sweep {
	return []Sweep{{
		ID:    SweepIDReconcile,
		Title: "Reconciliation sweep",
		// The description is user-facing dashboard text; the
		// specification it summarises is referenced in this comment,
		// not in the string (§forgejo/reconciliation/the-sweep).
		Description: "Re-enumerates the organisation's open issues and pull requests and reconciles each through the decision pipeline.",
	}}
}

// FireSweep starts the named sweep now rather than at the next tick,
// asynchronously: a sweep can make many API calls, so the trigger
// reports acceptance, not completion. A manual start is a sweep start
// like any other — Reconcile's skip-if-in-flight rule applies
// unchanged (§forgejo/observability/triggering-a-sweep,
// §forgejo/reconciliation/scheduling). An id outside the catalog is
// refused and nothing runs.
func (e *Engine) FireSweep(id string) error {
	if id != SweepIDReconcile {
		return ErrUnknownSweep
	}
	e.log.Info("sweep triggered manually", "sweep", id)
	go e.Reconcile()
	return nil
}
