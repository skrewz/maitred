package forgejo

import (
	"errors"
	"testing"
	"time"
)

// TestSweepsCatalog: the engine declares its sweep operations as a
// catalog — the reconciliation sweep today — and every entry is
// renderable by the dashboard: an id and a title
// (§forgejo/observability/triggering-a-sweep).
func TestSweepsCatalog(t *testing.T) {
	eng, _, _, _ := newTestEngine(t)
	sweeps := eng.Sweeps()
	if len(sweeps) == 0 {
		t.Fatal("the engine offers no sweep operations")
	}
	var found bool
	for _, s := range sweeps {
		if s.ID == "" || s.Title == "" {
			t.Errorf("sweep %+v has an empty field", s)
		}
		if s.ID == SweepIDReconcile {
			found = true
		}
	}
	if !found {
		t.Errorf("catalog %v does not name the reconciliation sweep %q", sweeps, SweepIDReconcile)
	}
}

// TestFireSweepStartsSweep: FireSweep starts the named sweep now rather
// than at the next tick, and does so asynchronously — the call returns
// while the sweep runs (§forgejo/observability/triggering-a-sweep).
// The observation belongs to the manual start alone: the engine is
// started first, its start-time sweep is waited out, and only then is
// the trigger fired — with the tick hours away, a listing beyond the
// start sweep's can come from nothing else.
func TestFireSweepStartsSweep(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	api.repos = []Repository{testRepo("o/r")}
	eng.Start()
	t.Cleanup(eng.Stop)

	// Wait for the start-time sweep to have listed the organisation
	// and to have released the in-flight flag.
	deadline := time.Now().Add(5 * time.Second)
	for api.callCount("ListOrgRepositories") < 1 || eng.sweeping.Load() {
		if time.Now().After(deadline) {
			t.Fatal("the engine's start-time sweep did not run and finish")
		}
		time.Sleep(10 * time.Millisecond)
	}

	before := api.callCount("ListOrgRepositories")
	if err := eng.FireSweep(SweepIDReconcile); err != nil {
		t.Fatalf("FireSweep: %v", err)
	}

	// The triggered sweep lists the org's repositories as its first
	// act; poll briefly because the loop runs it asynchronously.
	deadline = time.Now().Add(5 * time.Second)
	for api.callCount("ListOrgRepositories") <= before {
		if time.Now().After(deadline) {
			t.Fatal("FireSweep did not start a sweep that lists the org's repositories")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestFireSweep_UnknownID: an id outside the catalog is refused —
// nothing runs (§forgejo/observability/triggering-a-sweep).
func TestFireSweep_UnknownID(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	before := api.callCount("ListOrgRepositories")

	err := eng.FireSweep("no-such-sweep")
	if err == nil {
		t.Fatal("FireSweep accepted an id outside the catalog")
	}
	if !errors.Is(err, ErrUnknownSweep) {
		t.Errorf("FireSweep error = %v, want it to match ErrUnknownSweep", err)
	}
	if n := api.callCount("ListOrgRepositories"); n != before {
		t.Errorf("unknown sweep ran anyway: %d listings, want %d", n, before)
	}
}

// TestFireSweep_StoppedEngine: an engine whose sweep loop is not
// running refuses the trigger — outside the loop a start has no
// lifecycle owner — and nothing runs. Firing and then waiting the
// sweep out with Stop is the contract this pins
// (§forgejo/observability/triggering-a-sweep,
// §forgejo/reconciliation/scheduling).
func TestFireSweep_StoppedEngine(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	api.repos = []Repository{testRepo("o/r")}
	eng.Start()
	eng.Stop()

	before := api.callCount("ListOrgRepositories")
	err := eng.FireSweep(SweepIDReconcile)
	if err == nil {
		t.Fatal("FireSweep accepted a trigger on an engine whose sweep loop is stopped")
	}
	if !errors.Is(err, ErrSweepNotRunning) {
		t.Errorf("FireSweep error = %v, want it to match ErrSweepNotRunning", err)
	}
	if n := api.callCount("ListOrgRepositories"); n != before {
		t.Errorf("stopped engine ran a sweep anyway: %d listings, want %d", n, before)
	}
}
