package forgejo

import (
	"errors"
	"sort"
	"strings"
	"time"
)

// The scope cache: the snapshot of the roles the organisation's
// repositories hold, consulted by the event path so the scope test
// costs no API round-trip per delivery
// (§forgejo/webhook/the-scope-cache, §forgejo/webhook/repository-roles).

// roles are the repository roles held by topic
// (§forgejo/webhook/repository-roles): workItems is the
// maitred-enabled role, outcomes the maitred-outcomes-repo role. A
// repository may hold both or neither.
type roles struct {
	workItems bool
	outcomes  bool
}

// any reports whether the repository holds at least one role — the
// admission test (§forgejo/webhook/repository-roles).
func (r roles) any() bool { return r.workItems || r.outcomes }

// scopeEntry is what the cache remembers of one repository: the roles
// its topics carry and whether its pull requests unit is on, so a reader
// that must ask the unit asks the listing the sweep already made rather
// than paying a listing of its own (§forgejo/reconciliation/the-sweep,
// §forgejo/webhook/the-scope-cache).
type scopeEntry struct {
	roles        roles
	pullRequests bool
}

// rolesFromTopics derives a repository's roles from its topics
// (§forgejo/webhook/repository-roles). The derivation is written once
// here and shared by the scope cache and the sweep filter
// (§forgejo/reconciliation/the-sweep), so a third role means extending
// this function only — never two divergent admission tests.
func rolesFromTopics(topics []string) roles {
	return roles{
		workItems: hasTopic(topics, maitredEnabledTopic),
		outcomes:  hasTopic(topics, maitredOutcomesTopic),
	}
}

// errScopeUnavailable reports that the engine could not establish scope:
// the snapshot is older than one reconcile interval and the repository
// listing is failing, so a refresh is waiting out the retry backoff.
// The caller holds off with a reason naming the listing failure rather
// than a missing role, so a transient API failure is not recorded as a
// deliberate exclusion (§forgejo/webhook/the-scope-cache).
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
// given repository listing — one listing feeds both roles
// (§forgejo/webhook/repository-roles) — recording the moment of the
// snapshot so the staleness bound can be applied and clearing any
// failure backoff. The cache is keyed on the lowercased full name, so
// the lookup matches repository names case-insensitively — the way the
// forge addresses a repository and the way the closing-reference
// grammar matches one (§forgejo/webhook/re-fetch).
func (e *Engine) refreshScopeCache(repos []Repository) {
	scoped := make(map[string]scopeEntry, len(repos))
	for i := range repos {
		scoped[strings.ToLower(repos[i].FullName)] = scopeEntry{
			roles:        rolesFromTopics(repos[i].Topics),
			pullRequests: repos[i].HasPullRequests,
		}
	}
	e.scopeMu.Lock()
	e.scope = scoped
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

// freshScope returns the scope snapshot, refreshing the cache first
// when it is older than one reconcile interval: the whole fresh-scope
// read every scope-cache reader shares, so the event path's role lookup
// and the outcomes view's enumeration can never disagree about
// freshness, no reader can act on a stale snapshot, and the
// failure-backoff rule lives in exactly one place
// (§forgejo/webhook/the-scope-cache). Concurrent readers finding the
// cache stale single-flight on refreshMu and re-check staleness under
// it, so a burst costs one listing rather than one each. The returned
// map is the snapshot itself: refreshScopeCache replaces it whole and
// never mutates a published snapshot in place, so it stays safe to read
// after the lock is released. A non-nil error means scope is unknown —
// the listing failed, or a failure is waiting out the retry backoff
// with the snapshot gone stale — the latter answering
// errScopeUnavailable; the caller acts only on the error, never on an
// empty result misread as "no repositories".
func (e *Engine) freshScope() (map[string]scopeEntry, error) {
	if e.scopeNeedsRefresh() {
		e.refreshMu.Lock()
		defer e.refreshMu.Unlock()
		if e.scopeNeedsRefresh() {
			if err := e.tryRefreshScopeCache(); err != nil {
				return nil, err
			}
		}
	}
	e.scopeMu.Lock()
	defer e.scopeMu.Unlock()
	if time.Since(e.scopeAt) > e.cfg.ReconcileInterval {
		// Only reachable inside the failure backoff window: a refresh
		// was needed but is not yet due to be re-attempted.
		return nil, errScopeUnavailable
	}
	return e.scope, nil
}

// repoRoles reports the roles the repository holds
// (§forgejo/webhook/repository-roles), according to the scope cache
// (§forgejo/webhook/the-scope-cache). The name is matched
// case-insensitively: the cache is keyed on lowercased full names, so a
// caller holding a reference's spelling, a listing's or any other
// casing of the same repository gets the same answer. Freshness and the
// fail-closed rule are freshScope's. A repository absent from a fresh
// snapshot holds no role: the test fails closed. A non-nil error means
// scope is unknown, and the caller must hold off naming the failure
// instead of acting on the roles.
func (e *Engine) repoRoles(repo string) (roles, error) {
	scope, err := e.freshScope()
	if err != nil {
		return roles{}, err
	}
	return scope[strings.ToLower(repo)].roles, nil
}

// repoHasPullRequests reports whether the repository has the pull
// requests unit on, according to the same snapshot repoRoles reads, so a
// reader never asks an endpoint the repository refuses
// (§forgejo/reconciliation/the-sweep, §forgejo/webhook/the-scope-cache).
// The name is matched case-insensitively as repoRoles matches it. A
// repository absent from a fresh snapshot answers false, the way it
// holds no role. The outcomes walk tests the remit before asking, so
// absence there names a repository the listing left out; syncMembers
// asks without testing it, and so reads an out-of-remit repository as
// delivering nothing — the same conservative answer a delivery it
// cannot read gives, which no sync predicate can fire on
// (§forgejo/decisions/sync-set).
func (e *Engine) repoHasPullRequests(repo string) (bool, error) {
	scope, err := e.freshScope()
	if err != nil {
		return false, err
	}
	return scope[strings.ToLower(repo)].pullRequests, nil
}

// outcomesRepositories reports the full names of the repositories the
// scope cache classifies as outcomes repositories
// (§forgejo/webhook/repository-roles, §forgejo/observability/the-outcomes-view),
// sorted so a reader's order is deterministic across calls. Like
// repoRoles it goes through freshScope, so a non-nil error means scope
// is unknown and the caller must not read an empty list as "no
// outcomes repositories".
func (e *Engine) outcomesRepositories() ([]string, error) {
	scope, err := e.freshScope()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(scope))
	for name, entry := range scope {
		if entry.roles.outcomes {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}
