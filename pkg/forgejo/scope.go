package forgejo

import (
	"time"
)

// The scope cache: the snapshot of which of the organisation's
// repositories carry the maitred-enabled topic, consulted by the event
// path so the scope test costs no API round-trip per delivery
// (§forgejo/webhook/the-scope-cache).

// refreshScopeCache replaces the scope cache with a snapshot of the
// given repository listing, recording the moment of the snapshot so the
// staleness bound can be applied.
func (e *Engine) refreshScopeCache(repos []Repository) {
	enabled := make(map[string]bool, len(repos))
	for i := range repos {
		enabled[repos[i].FullName] = hasTopic(repos[i].Topics, maitredEnabledTopic)
	}
	e.scopeMu.Lock()
	e.scope = enabled
	e.scopeAt = time.Now()
	e.scopeMu.Unlock()
}

// repoEnabled reports whether the repository carries the
// maitred-enabled topic (§forgejo/reconciliation/the-sweep), according
// to the scope cache (§forgejo/webhook/the-scope-cache). The cache is
// refreshed when it has never been taken or is older than one reconcile
// interval; a repository absent from the snapshot is not enabled — the
// test fails closed.
func (e *Engine) repoEnabled(repo string) bool {
	e.scopeMu.Lock()
	stale := time.Since(e.scopeAt) > e.cfg.ReconcileInterval
	e.scopeMu.Unlock()
	if stale {
		repos, err := e.api.ListOrgRepositories(e.cfg.Org)
		if err != nil {
			e.log.Warn("scope test: list repositories", "org", e.cfg.Org, "error", err)
			return false
		}
		e.refreshScopeCache(repos)
	}
	e.scopeMu.Lock()
	defer e.scopeMu.Unlock()
	return e.scope[repo]
}
