package forgejo

import (
	"errors"
	"time"
)

// The scope cache: the snapshot of which of the organisation's
// repositories carry the maitred-enabled topic, consulted by the event
// path so the scope test costs no API round-trip per delivery
// (§forgejo/webhook/the-scope-cache).

// errScopeUnavailable reports that the engine could not establish scope:
// the snapshot is older than one reconcile interval and the repository
// listing is failing, so a refresh is waiting out the retry backoff.
// The caller holds off with a reason naming the listing failure rather
// than "not maitred-enabled", so a transient API failure is not recorded
// as a deliberate exclusion (§forgejo/webhook/the-scope-cache).
var errScopeUnavailable = errors.New("repository listing unavailable")

// scopeRetryInterval bounds how often the event path re-attempts a
// repository listing after a failure. Stamping the attempt even on
// failure keeps an API degradation from costing one full org listing per
// delivery; the last-known-good snapshot is kept, but never acted on
// while stale — the test still fails closed.
const scopeRetryInterval = 30 * time.Second

// scopeRetry returns the failure backoff, never longer than one
// reconcile interval.
func (e *Engine) scopeRetry() time.Duration {
	if e.cfg.ReconcileInterval < scopeRetryInterval {
		return e.cfg.ReconcileInterval
	}
	return scopeRetryInterval
}

// refreshScopeCache replaces the scope cache with a snapshot of the
// given repository listing, recording the moment of the snapshot so the
// staleness bound can be applied and clearing any failure backoff.
func (e *Engine) refreshScopeCache(repos []Repository) {
	enabled := make(map[string]bool, len(repos))
	for i := range repos {
		enabled[repos[i].FullName] = hasTopic(repos[i].Topics, maitredEnabledTopic)
	}
	e.scopeMu.Lock()
	e.scope = enabled
	e.scopeAt = time.Now()
	e.scopeFailedAt = time.Time{}
	e.scopeMu.Unlock()
}

// scopeNeedsRefresh reports whether the event path should attempt a
// refresh now: the snapshot is older than one reconcile interval, and
// either no listing has failed or the last failed attempt is older than
// the retry interval.
func (e *Engine) scopeNeedsRefresh() bool {
	e.scopeMu.Lock()
	defer e.scopeMu.Unlock()
	if time.Since(e.scopeAt) <= e.cfg.ReconcileInterval {
		return false
	}
	return e.scopeFailedAt.IsZero() || time.Since(e.scopeFailedAt) >= e.scopeRetry()
}

// tryRefreshScopeCache lists the organisation's repositories once and,
// on success, replaces the cache. A failure keeps the last snapshot and
// stamps the attempt so the backoff applies; either way the caller acts
// only on the error, never on the stale snapshot.
func (e *Engine) tryRefreshScopeCache() error {
	repos, err := e.api.ListOrgRepositories(e.cfg.Org)
	if err != nil {
		e.scopeMu.Lock()
		e.scopeFailedAt = time.Now()
		e.scopeMu.Unlock()
		e.log.Warn("scope test: repository listing failed; keeping last snapshot",
			"org", e.cfg.Org, "error", err, "retry_in", e.scopeRetry())
		return err
	}
	e.refreshScopeCache(repos)
	return nil
}

// repoEnabled reports whether the repository carries the
// maitred-enabled topic (§forgejo/reconciliation/the-sweep), according
// to the scope cache (§forgejo/webhook/the-scope-cache). The cache is
// refreshed when it has never been taken or is older than one reconcile
// interval; concurrent deliveries finding it stale share one listing —
// they wait on refreshMu and re-check staleness under it. A repository
// absent from a fresh snapshot is not enabled: the test fails closed.
// A non-nil error means scope is unknown — the listing failed, or a
// failure is waiting out the retry backoff with the snapshot gone stale
// — and the caller must hold off naming the failure instead of acting
// on the boolean.
func (e *Engine) repoEnabled(repo string) (bool, error) {
	if e.scopeNeedsRefresh() {
		e.refreshMu.Lock()
		defer e.refreshMu.Unlock()
		if e.scopeNeedsRefresh() {
			if err := e.tryRefreshScopeCache(); err != nil {
				return false, err
			}
		}
	}
	e.scopeMu.Lock()
	enabled := e.scope[repo]
	stale := time.Since(e.scopeAt) > e.cfg.ReconcileInterval
	e.scopeMu.Unlock()
	if stale {
		// Only reachable inside the failure backoff window: a refresh
		// was needed but is not yet due to be re-attempted.
		return false, errScopeUnavailable
	}
	return enabled, nil
}
