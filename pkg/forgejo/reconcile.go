package forgejo

import (
	"time"
)

// maitredEnabledTopic is the repository topic marking a repository as
// tracked by the engine (§forgejo/reconciliation/the-sweep).
const maitredEnabledTopic = "maitred-enabled"

// Reconcile runs one reconciliation sweep
// (§forgejo/reconciliation/the-sweep): it enumerates the org's
// maitred-enabled repositories, lists their open issues and pull
// requests, and feeds each a synthetic reconcile event through the same
// pipeline as the event path — re-fetch, decide, dispatch, watermark,
// serialised on the watermark store's per-key lock. A sweep that starts
// while another is in flight is skipped
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
	for i := range repos {
		repo := &repos[i]
		if !hasTopic(repo.Topics, maitredEnabledTopic) {
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
// on every ReconcileInterval tick
// (§forgejo/reconciliation/scheduling).
func (e *Engine) Start() {
	if e.stopCh != nil {
		return
	}
	e.stopCh = make(chan struct{})
	e.doneCh = make(chan struct{})
	go e.sweepLoop()
}

// Stop halts the reconciliation sweep and waits for the in-flight sweep,
// if any, to finish. It is a no-op when the engine was never started
// (§forgejo/reconciliation/scheduling).
func (e *Engine) Stop() {
	if e.stopCh == nil {
		return
	}
	close(e.stopCh)
	<-e.doneCh
	e.stopCh = nil
	e.doneCh = nil
}

// sweepLoop runs the sweep once, then on every tick, until Stop.
func (e *Engine) sweepLoop() {
	defer close(e.doneCh)
	e.Reconcile()
	t := time.NewTicker(e.cfg.ReconcileInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			e.Reconcile()
		case <-e.stopCh:
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
