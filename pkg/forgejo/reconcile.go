package forgejo

import (
	"time"
)

// maitredEnabledTopic is the repository topic marking a repository as
// a work-item source (§forgejo/webhook/repository-roles).
const maitredEnabledTopic = "maitred-enabled"

// maitredOutcomesTopic is the repository topic marking a repository as
// an outcomes source (§forgejo/webhook/repository-roles).
const maitredOutcomesTopic = "maitred-outcomes-repo"

// Reconcile runs one reconciliation sweep
// (§forgejo/reconciliation/the-sweep): it enumerates the org's
// repositories holding at least one role — the union of the
// work-item and outcomes role sets — lists their open issues and pull
// requests (a repository whose pull requests unit is off is not asked
// for pull requests at all), and feeds each a synthetic reconcile event
// through the same pipeline as the event path — re-fetch, decide,
// dispatch, watermark, serialised on the watermark store's per-key lock.
// A sweep that starts while another is in flight is skipped
// (§forgejo/reconciliation/scheduling).
func (e *Engine) Reconcile() {
	if !e.sweeping.CompareAndSwap(false, true) {
		e.log.Info("reconciliation already in flight; skipping")
		return
	}
	defer e.sweeping.Store(false)

	repos, err := e.api.ListOrgRepositories(e.cfg.Org)
	if err != nil {
		e.log.Error("reconciliation: list repositories", "org", e.cfg.Org, "error", err)
		return
	}
	// The listing is the scope cache: the event path consults it instead
	// of fetching per delivery (§forgejo/webhook/the-scope-cache).
	e.refreshScopeCache(repos)
	for i := range repos {
		repo := &repos[i]
		// The sweep visits the union of both role sets, so a missed
		// delivery in an outcomes-only repository self-heals too
		// (§forgejo/reconciliation/the-sweep, §forgejo/webhook/repository-roles).
		if !rolesFromTopics(repo.Topics).any() {
			continue
		}
		issues, err := e.api.ListOpenIssues(repo.Owner, repo.Name)
		if err != nil {
			e.log.Error("reconciliation: list open issues", "repo", repo.FullName, "error", err)
		} else {
			for j := range issues {
				if issues[j].IsPull {
					continue // a pull request: the pull request list covers it
				}
				e.HandleEvent(Event{Type: EventReconcile, Repo: repo.FullName, Kind: KindIssue, Number: issues[j].Number})
			}
		}
		// A repository with the pull requests unit off refuses the pull
		// request endpoint outright (it answers 404) and cannot hold a
		// pull request, so the sweep does not ask it: the refusal is an
		// ordinary state, not a failed read
		// (§forgejo/reconciliation/the-sweep). The unit comes from the
		// listing the sweep already made; the client keeps reporting a
		// 404 as an error, so a renamed or deleted repository — whose
		// unit is on — still fails loudly (§forgejo/client/errors).
		if !repo.HasPullRequests {
			continue
		}
		pulls, err := e.api.ListOpenPullRequests(repo.Owner, repo.Name)
		if err != nil {
			e.log.Error("reconciliation: list open pull requests", "repo", repo.FullName, "error", err)
		} else {
			for j := range pulls {
				e.HandleEvent(Event{Type: EventReconcile, Repo: repo.FullName, Kind: KindPR, Number: pulls[j].Number})
			}
		}
	}
}

// Start begins the reconciliation sweep: it runs once immediately, then
// on every ReconcileInterval tick — and on every manual start this
// process signals through FireSweep
// (§forgejo/reconciliation/scheduling,
// §forgejo/observability/triggering-a-sweep).
func (e *Engine) Start() {
	e.schedMu.Lock()
	defer e.schedMu.Unlock()
	if e.stopCh != nil {
		return
	}
	e.stopCh = make(chan struct{})
	e.doneCh = make(chan struct{})
	e.startCh = make(chan struct{}, 1)
	go e.sweepLoop()
}

// Stop halts the reconciliation sweep and waits for the in-flight
// sweep, if any, to finish — including one started by hand, because
// the sweep loop owns every start (§forgejo/reconciliation/scheduling,
// §forgejo/observability/triggering-a-sweep). Every scheduling channel
// is read and written under schedMu here, as it is in Start and
// FireSweep. It is a no-op when the engine was never started.
func (e *Engine) Stop() {
	e.schedMu.Lock()
	if e.stopCh == nil {
		e.schedMu.Unlock()
		return
	}
	close(e.stopCh)
	done := e.doneCh
	e.schedMu.Unlock()
	<-done
	e.schedMu.Lock()
	e.stopCh, e.doneCh, e.startCh = nil, nil, nil
	e.schedMu.Unlock()
}

// sweepLoop runs the sweep once, then on every tick and on every
// manual start signal, until Stop. A manual start is a sweep start
// like any other: it runs the sweep the catalog holds today — the
// reconciliation sweep — and a future sweep joining the catalog
// (§forgejo/observability/triggering-a-sweep) grows its start here
// alone.
func (e *Engine) sweepLoop() {
	defer close(e.doneCh)
	e.Reconcile()
	t := time.NewTicker(e.cfg.ReconcileInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			e.Reconcile()
		case <-e.startCh:
			e.Reconcile()
		case <-e.stopCh:
			// A start accepted before Stop closed stopCh may still sit
			// buffered: run it rather than drop it, because Stop is
			// already waiting doneCh and so waits this sweep out
			// (§forgejo/observability/triggering-a-sweep: no start is
			// ever accepted that nothing would wait out).
			select {
			case <-e.startCh:
				e.Reconcile()
			default:
			}
			return
		}
	}
}

// hasTopic reports whether topics contains topic.
func hasTopic(topics []string, topic string) bool {
	for _, t := range topics {
		if t == topic {
			return true
		}
	}
	return false
}
