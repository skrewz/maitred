package forgejo

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// TestSweepsCatalog: the engine declares its sweep operations as a
// catalog, and every entry is renderable by the dashboard: an id and a
// title (§forgejo/observability/triggering-a-sweep). The catalog holds
// the reconciliation sweep alone: startCh's hand-off carries no id, so
// sweepLoop answers every accepted id with that one sweep. A second
// entry is a change to that hand-off, not only to the catalog, and
// this test is where it stops being silent.
func TestSweepsCatalog(t *testing.T) {
	eng, _, _, _ := newTestEngine(t)
	sweeps := eng.Sweeps()
	if len(sweeps) != 1 {
		t.Errorf("catalog %v holds %d entries, but the manual-start hand-off carries no id and runs the reconciliation sweep for all of them: give startCh an id to carry and dispatch on it in sweepLoop", sweeps, len(sweeps))
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

// TestFireSweepThenStopWaitsTheStartOut: a start accepted while the
// loop cannot take it is waited out by Stop, not dropped — whichever
// arm the loop's select takes on the way out
// (§forgejo/observability/triggering-a-sweep: no start is ever
// accepted that nothing would wait out).
//
// The loop is parked inside a sweep — blocked in the org listing —
// when the start buffers and Stop closes stopCh, so releasing the
// block leaves the loop at a select with both arms ready, and Go
// picks between them at random: the startCh arm runs the start, and
// the stopCh arm must take it through the drain on its exit. The
// interleaving is therefore a coin toss per pass, so it is repeated
// until the toss has landed on stopCh as well; delete the drain and
// every stopCh toss fails the assertion.
func TestFireSweepThenStopWaitsTheStartOut(t *testing.T) {
	for pass := 0; pass < drainTosses; pass++ {
		eng, api, _, _ := newTestEngine(t)
		api.repos = []Repository{testRepo("o/r")}
		api.listReposEntered = make(chan struct{})
		api.blockListRepos = make(chan struct{})
		release := sync.OnceFunc(func() { close(api.blockListRepos) })
		t.Cleanup(release)

		eng.Start()
		select {
		case <-api.listReposEntered:
		case <-time.After(5 * time.Second):
			t.Fatalf("pass %d: the sweep never reached the org listing", pass)
		}

		// The buffer is the only place this start can sit: the loop is
		// inside the sweep and cannot read it.
		if err := eng.FireSweep(SweepIDReconcile); err != nil {
			t.Fatalf("pass %d: FireSweep: %v", pass, err)
		}

		stopped := make(chan struct{})
		go func() {
			eng.Stop()
			close(stopped)
		}()
		release()

		select {
		case <-stopped:
		case <-time.After(5 * time.Second):
			t.Fatalf("pass %d: Stop did not return", pass)
		}
		if n := api.callCount("ListOrgRepositories"); n < 2 {
			t.Errorf("pass %d: Stop returned after %d listing(s), want the parked sweep's and the accepted start's", pass, n)
		}
	}
}

// drainTosses is how many times
// TestFireSweepThenStopWaitsTheStartOut repeats the coin-toss
// interleaving: each pass lands on the stopCh arm — the one the drain
// exists for — with probability a half, so a drain that has been
// removed survives this many passes with probability 2^-drainTosses.
const drainTosses = 16

// TestFireSweepDuringStopIsRefused: once Stop has begun, a trigger is
// refused rather than accepted — a start buffered into a loop on its
// way out would be answered 202 for nothing
// (§forgejo/observability/triggering-a-sweep,
// §forgejo/reconciliation/scheduling). The engine is parked inside its
// start-time sweep, so Stop is waiting that sweep out while the
// trigger is fired: the interleaving the refusal exists for.
func TestFireSweepDuringStopIsRefused(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	api.repos = []Repository{testRepo("o/r")}
	api.listReposEntered = make(chan struct{})
	api.blockListRepos = make(chan struct{})
	release := sync.OnceFunc(func() { close(api.blockListRepos) })
	t.Cleanup(release)

	eng.Start()
	select {
	case <-api.listReposEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("the engine's start-time sweep never reached the org listing")
	}

	stopped := make(chan struct{})
	go func() {
		eng.Stop()
		close(stopped)
	}()

	// A trigger fired before Stop reached stopCh is accepted, and must
	// then be waited out; once stopCh is closed, triggers are refused.
	accepted := false
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := eng.FireSweep(SweepIDReconcile)
		if errors.Is(err, ErrSweepNotRunning) {
			break
		}
		if err != nil {
			t.Fatalf("FireSweep: %v", err)
		}
		accepted = true
		if time.Now().After(deadline) {
			t.Fatal("FireSweep never reported a loop that had begun stopping")
		}
		time.Sleep(10 * time.Millisecond)
	}
	duringStop := api.callCount("ListOrgRepositories")

	release()
	<-stopped

	n := api.callCount("ListOrgRepositories")
	if accepted {
		if n <= duringStop {
			t.Errorf("a start accepted while Stop was beginning was not waited out: %d listing(s), want more than %d", n, duringStop)
		}
	} else if n != duringStop {
		t.Errorf("a refused start ran anyway: %d listing(s), want %d", n, duringStop)
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
