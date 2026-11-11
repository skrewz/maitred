package forgejo

import (
	"errors"
	"testing"
	"time"
)

// TestSweepsCatalog: the engine declares its sweep operations as a
// catalog — the reconciliation sweep today — and every entry is
// renderable by the dashboard: an id, a title, and a description
// (§forgejo/observability/triggering-a-sweep).
func TestSweepsCatalog(t *testing.T) {
	eng, _, _, _ := newTestEngine(t)
	sweeps := eng.Sweeps()
	if len(sweeps) == 0 {
		t.Fatal("the engine offers no sweep operations")
	}
	var found bool
	for _, s := range sweeps {
		if s.ID == "" || s.Title == "" || s.Description == "" {
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
func TestFireSweepStartsSweep(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	api.repos = []Repository{testRepo("o/r")}

	before := api.callCount("ListOrgRepositories")
	if err := eng.FireSweep(SweepIDReconcile); err != nil {
		t.Fatalf("FireSweep: %v", err)
	}

	// The sweep lists the org's repositories as its first act; poll
	// briefly because it runs in a goroutine.
	deadline := time.Now().Add(5 * time.Second)
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
