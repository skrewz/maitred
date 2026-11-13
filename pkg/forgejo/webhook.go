package forgejo

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"maitred/pkg/queue"
)

// WebhookPath is the route the engine's handler serves
// (§forgejo/webhook/the-route).
const WebhookPath = "/v1/forgejo/all_events"

// The Forgejo webhook headers (§forgejo/webhook/the-pipeline).
const (
	// HeaderSignature carries the hex-encoded HMAC-SHA256 of the raw
	// body, computed with the shared secret.
	HeaderSignature = "X-Forgejo-Signature"
	// HeaderEventType carries the fine-grained event type (e.g.
	// "issues", "pull_request_sync").
	HeaderEventType = "X-Forgejo-Event-Type"
	// HeaderDelivery carries Forgejo's delivery id, the handle for the
	// delivery's entry in Forgejo's recent-deliveries list. It is named
	// in the log line a refusal leaves behind
	// (§forgejo/webhook/the-route).
	HeaderDelivery = "X-Forgejo-Delivery"
)

// maxWebhookBodySize caps the webhook body (mirrors the trigger
// handler's limit).
const maxWebhookBodySize = 10 << 20

// Engine is the Forgejo engine's event path: it re-fetches the
// authoritative state of an event's object, decides, dispatches canned
// tasks, and records watermarks (§forgejo/webhook/the-pipeline). The
// reconciliation sweep feeds the same path with synthetic events
// (§forgejo/reconciliation/the-sweep).
type Engine struct {
	api   API
	store *Store
	cfg   *Config
	queue queue.TaskQueueProvider
	log   *slog.Logger

	// The in-memory decision log: the last decision per key
	// (§forgejo/observability/the-dashboard-view).
	decMu     sync.Mutex
	decisions map[Key]DecisionRecord

	// Reconciliation sweep scheduling state. startCh is the hand-off
	// for manual starts: FireSweep signals it and the sweep loop, the
	// one owner of sweep starts, runs the sweep
	// (§forgejo/reconciliation/scheduling,
	// §forgejo/observability/triggering-a-sweep). The signal carries no
	// id, so the loop runs the reconciliation sweep for every id the
	// catalog accepts — sound only while the catalog holds that one
	// entry alone, and a second entry must give this channel an id to
	// carry and dispatch on it in sweepLoop. schedMu guards the three
	// channels against a trigger racing a Stop.
	stopCh   chan struct{}
	doneCh   chan struct{}
	startCh  chan struct{}
	schedMu  sync.Mutex
	sweeping atomic.Bool

	// The scope cache: the roles each of the org's repositories holds,
	// refreshed at most once per reconcile interval
	// (§forgejo/webhook/the-scope-cache, §forgejo/webhook/repository-roles).
	scopeMu sync.Mutex
	scope   map[string]roles
	scopeAt time.Time
	// scopeFailedAt stamps the last failed listing attempt, bounding
	// the retry of a failing API to one listing per scopeRetryInterval.
	scopeFailedAt time.Time
	// refreshMu single-flights the event path's cache refresh: deliveries
	// finding the cache stale wait here and re-check staleness, so a
	// burst costs one listing rather than one each.
	refreshMu sync.Mutex

	// The re-fire bound: the last landed singleton dispatch per key and
	// action, and the moment it landed, so one unchanged
	// (action, revision) re-fires at most once per reconcile interval
	// whichever path reached it. Only state-governed rows consult the
	// stamp, and only the stamp their own action left; a revision-gated
	// row consults none (§forgejo/decisions/loop-prevention,
	// §forgejo/reconciliation/the-state-is-the-watermark).
	reFireMu sync.Mutex
	reFires  map[Key]reFireStamp
}

// NewEngine creates the event path over the given Forgejo API, watermark
// store, config, and queue provider.
func NewEngine(api API, store *Store, cfg *Config, q queue.TaskQueueProvider, logger *slog.Logger) *Engine {
	if logger == nil {
		logger = slog.Default()
	}
	return &Engine{
		api:       api,
		store:     store,
		cfg:       cfg,
		queue:     q,
		log:       logger,
		decisions: make(map[Key]DecisionRecord),
		reFires:   make(map[Key]reFireStamp),
	}
}

// HandleEvent processes one event end to end — scope test, re-fetch,
// decide, dispatch, watermark — under the key's lock, so concurrent
// events for the same key cannot race the watermark
// (§forgejo/webhook/serialisation). A repository holding no maitred
// role is outside the engine's remit: it holds off before the re-fetch
// (§forgejo/webhook/repository-roles) — except a close, which is inert
// for its own issue but reads through the graph to wake the unblock
// cascade (§forgejo/webhook/repository-roles,
// §forgejo/decisions/unblock-cascade). A re-fetch failure holds off: the
// reconciliation sweep re-derives from the source of truth
// (§forgejo/webhook/the-pipeline).
func (e *Engine) HandleEvent(ev Event) {
	key := ev.KeyOf()
	r, err := e.repoRoles(ev.Repo)
	if err != nil {
		// Scope unknown: the repository listing failed. Hold off with a
		// reason naming the failure, never as a deliberate exclusion
		// (§forgejo/webhook/the-scope-cache).
		e.recordDecision(key, ev, State{}, holdOffScopeUnknown(ev.Repo, err))
		return
	}
	if !r.any() {
		// The repository holds no role: outside the engine's remit, so
		// hold off before re-fetching, so an inert repository costs
		// nothing and dispatches nothing — cascade included
		// (§forgejo/webhook/repository-roles). The one exception is a
		// close: the closed issue itself stays inert, but the graph
		// may be read through the repository, so the unblock cascade
		// runs for what the close blocks
		// (§forgejo/webhook/repository-roles,
		// §forgejo/decisions/unblock-cascade).
		if ev.Kind == KindIssue && ev.Type == EventIssueClosed {
			e.handleCascadeOnlyClose(ev)
			return
		}
		e.recordDecision(key, ev, State{}, holdOffOutOfRemit(ev.Repo))
		return
	}
	e.store.WithKeyLock(key, func() {
		ev, st, err := e.refetch(ev)
		if err != nil {
			e.log.Error("re-fetch failed, holding off", "key", key.String(), "error", err)
			return
		}
		w, err := e.store.loadLocked(key)
		if err != nil {
			e.log.Warn("load watermark (continuing without)", "key", key.String(), "error", err)
			w = nil
		}
		d := Decide(ev, st, w)
		// Admissibility: an action fires only if the repository holding
		// the object under consideration carries the role admitting it.
		// Applied after the re-fetch deliberately — the action is
		// Decide's answer, not the event's
		// (§forgejo/webhook/repository-roles).
		if d.Dispatched && !admissibleInRoles(d.Action, r) {
			d = holdOffActionInadmissible(d.Action, ev.Repo)
		}
		d = e.boundReFire(key, ev, st, d)
		e.recordDecision(key, ev, st, d)
		if d.Dispatched {
			if len(d.Lenses) > 0 {
				// The review fan-out: one dispatch per outstanding lens,
				// each recorded in its own subject slot so a second
				// lens at the same revision is not held off by the first
				// (§forgejo/decisions/review-lenses, §forgejo/state/watermark).
				e.dispatchLenses(key, w, ev, st, d.Lenses)
			} else {
				if err := e.dispatch(ev, st, d.Action); err != nil {
					// No watermark: the reconciliation sweep retries.
					e.log.Error("dispatch failed", "key", key.String(), "action", string(d.Action), "error", err)
					return
				}
				if err := e.store.saveLocked(key, watermarkAt(w, string(d.Action), revisionFor(ev, st))); err != nil {
					e.log.Error("save watermark", "key", key.String(), "error", err)
				}
			}
		}
		e.emitCascade(d, key, ev.Sender)
	})
}

// emitCascade applies the unblock cascade's emissions: each dispatch
// goes through dispatchCascade (its own admission, re-check, and
// watermark), and each still-blocked tracker hold-off is recorded in
// the decision log like any other decision — carrying the tracker as
// the graph showed it and qualified by the roles of the repository
// holding its key, so it never promises an action those roles exclude
// (§forgejo/decisions/unblock-cascade,
// §forgejo/observability/the-decision-log,
// §forgejo/webhook/repository-roles).
func (e *Engine) emitCascade(d Decision, rootKey Key, sender string) {
	for _, cd := range d.Cascade {
		if err := e.dispatchCascade(cd, sender); err != nil {
			e.log.Error("cascade dispatch failed", "key", rootKey.String(), "cascade", cd.Key.String(), "error", err)
		}
	}
	for _, ch := range d.CascadeHoldOffs {
		cev := Event{Type: EventIssueOpened, Repo: ch.Key.Repo, Kind: ch.Key.Kind, Number: ch.Key.Number, Sender: sender}
		e.recordDecision(ch.Key, cev, State{Issue: ch.Issue}, holdOff(e.cascadeHoldOffReason(ch)))
	}
}

// handleCascadeOnlyClose is the close path of a repository holding no
// maitred role. The roles decide what the engine acts on, not which
// repositories the graph may be read through: the closed issue itself
// is inert — nothing is acted on for it, no re-fetch, no watermark —
// but the engine asks what it blocks and runs the ordinary unblock
// cascade for it. Every emission stays judged by the roles of the
// repository holding its key, so a cascade woken by an inert close can
// only act where the roles admit it. The extra read is restricted to
// closes: no other event in a role-less repository gets past the
// zero-cost hold-off. A failed graph read is a hold-off the sweep
// backstops, like any re-fetch failure
// (§forgejo/webhook/repository-roles,
// §forgejo/decisions/unblock-cascade).
func (e *Engine) handleCascadeOnlyClose(ev Event) {
	key := ev.KeyOf()
	// The root's own fields are never consulted by the walk — each
	// visited issue is judged by its own dependency rows, so a root
	// that is not in fact closed emits nothing — only its identity
	// and its downstream edges matter here (§forgejo/decisions/unblock-cascade).
	root, err := e.graphNode(&Issue{Number: ev.Number, Repository: ev.Repo, State: "closed"})
	if err != nil {
		e.log.Error("graph read failed, holding off", "key", key.String(), "error", err)
		return
	}
	if len(root.Blocks) == 0 {
		// Inert and unrelated: the remit-only hold-off, unchanged.
		e.recordDecision(key, ev, State{}, holdOffOutOfRemit(ev.Repo))
		return
	}
	// Inert, but in the graph: the hold-off names the cascade it woke.
	e.recordDecision(key, ev, State{}, holdOffOutOfRemitWakesCascade(ev.Repo, ev.Number))
	var d Decision
	d.Cascade, d.CascadeHoldOffs = unblockCascade(root)
	e.emitCascade(d, key, ev.Sender)
}

// refetch re-fetches the authoritative state for ev from Forgejo
// (§forgejo/webhook/re-fetch). A pull_request event with action closed
// is refined by the re-fetch: a merged PR is a PR-merged event, an
// unmerged one stays a PR-closed event.
func (e *Engine) refetch(ev Event) (Event, State, error) {
	owner, name := splitRepo(ev.Repo)
	switch ev.Kind {
	case KindIssue:
		issue, err := e.api.GetIssue(owner, name, ev.Number)
		if err != nil {
			return ev, State{}, err
		}
		st := State{Issue: issue, DecomposerIdentity: e.cfg.DecomposerIdentity, ReviewLenses: e.cfg.Reviews}
		switch ev.Type {
		case EventIssueOpened, EventIssueEdited, EventIssueLabelsChanged,
			EventIssueCommented, EventReconcile:
			// The opened path needs the blockers for the implement
			// condition and the activity path for the reassess
			// condition: an open blocker holds off both
			// (§forgejo/decisions/transition-table, §forgejo/webhook/re-fetch).
			blockers, err := e.api.IssueDependencies(owner, name, ev.Number)
			if err != nil {
				return ev, State{}, err
			}
			st.Blockers = blockers
			for i := range blockers {
				if blockers[i].IsPull && blockers[i].State == "open" {
					st.ConnectedPRs = append(st.ConnectedPRs, pullRequestFromIssue(blockers[i]))
				}
			}
			// A root under a teardown order is the one issue whose
			// *downstream* edges are read, because the order is only
			// actionable while the forest stands: whether the root blocks
			// anything is the emptiness test, and asking an agent to
			// discover it would be the waste the test exists to avoid
			// (§forgejo/decisions/forest-teardown, §forgejo/webhook/re-fetch).
			if hasLabel(issue, LabelIdeation) && hasLabel(issue, LabelTeardownForest) {
				downstream, err := e.api.IssueBlocks(owner, name, ev.Number)
				if err != nil {
					return ev, State{}, err
				}
				st.Blocked = downstream
			}
			if ev.Type == EventReconcile && hasLabel(issue, LabelSync) {
				// The sweep's backstop row for landing sets: the sync
				// predicate reads each member's delivering PR, so the
				// blockers re-fetched above are projected onto the set
				// members, each with its PR state
				// (§forgejo/decisions/sync-set).
				members, err := e.syncMembers(blockers)
				if err != nil {
					return ev, State{}, err
				}
				st.SyncMembers = members
			}
		case EventIssueClosed:
			root, err := e.graphNode(issue)
			if err != nil {
				return ev, State{}, err
			}
			st.CascadeRoot = root
		}
		return ev, st, nil
	case KindPR:
		pr, err := e.api.GetPullRequest(owner, name, ev.Number)
		if err != nil {
			return ev, State{}, err
		}
		// The implementer identity rides on the state verbatim from the
		// config: the fix-feedback ownership gate reads it alongside
		// the PR's author (§forgejo/config/implementer-identity,
		// §forgejo/decisions/inputs, s-git/maitred#143). The lens list
		// rides the same way: the review fan-out and the per-lens
		// gates read the operator's declaration, never an inferred
		// roster (§forgejo/config/review-lenses).
		st := State{PR: pr, ImplementerIdentity: e.cfg.ImplementerIdentity, ReviewLenses: e.cfg.Reviews}
		switch ev.Type {
		case EventPRReviewed, EventPRCommented, EventReconcile:
			// The one hop performed wherever a merge may be dispatched —
			// the PR reviewed event, PR commented (which decides as the
			// sweep's PR path), and the sweep's synthetic current-state
			// event: every issue connected to the PR — those its body
			// auto-closes and those its blocking edges name, the two
			// relations that can carry a delivery — each with its
			// blockers, read from that issue's own repository, no
			// recursion. Every entry is resolved: the merge gate is not
			// left to API ordering. A failure to resolve any connected
			// issue is a re-fetch failure — a hold-off naming the
			// failure, never the path's default
			// (§forgejo/webhook/re-fetch,
			// §forgejo/decisions/merge-gate).
			if err := e.fetchPRSetState(owner, name, ev.Number, &st); err != nil {
				return ev, State{}, err
			}
			fallthrough
		case EventPROpened, EventPRSynced:
			reviews, err := e.api.ListPullRequestReviews(owner, name, ev.Number)
			if err != nil {
				return ev, State{}, err
			}
			st.Reviews = reviews
		case EventPRClosed:
			if pr.Merged {
				ev.Type = EventPRMerged
				blocks, err := e.api.IssueBlocks(owner, name, ev.Number)
				if err != nil {
					return ev, State{}, err
				}
				for i := range blocks {
					if !blocks[i].IsPull {
						root, err := e.graphNode(&blocks[i])
						if err != nil {
							return ev, State{}, err
						}
						st.CascadeRoot = root
						break
					}
				}
			}
		}
		return ev, st, nil
	}
	return ev, State{}, fmt.Errorf("unknown kind %q", ev.Kind)
}

// connectedIssues fetches the one hop the merge gate and the landing-set
// read share: the issues connected to a pull request, each carrying the
// issues that block it. Two relations connect them, and both are read.
// The issues the pull request's body **auto-closes**, resolved through
// the forge's own closing-keyword grammar, are the delivery relation the
// authoring contract writes: a member delivers through the pull request
// whose body closes it, and no blocking edge between the two is recorded
// any more (§forgejo/decisions/sync-set, s-git/maitred#171). The issues
// the pull request **blocks** are still read, because that edge carries
// the shape a grouping issue's own PR member stands in. A read of one
// relation alone leaves a path blind to the delivery the other sees: the
// approval path that could not see a member's set merged its delivery
// through merge-or-wait and left the set to the next sweep
// (s-git/maitred#180). An issue reachable through either relation is
// read the same way — its blockers, in its own repository (cross-repo
// aware), one hop per connected issue, no recursion — and the rows are
// deduplicated by repository and number, the blocking-edge rows first,
// so an issue reachable both ways is read and counted once. A failed
// read fails the whole re-fetch (§forgejo/webhook/re-fetch,
// §forgejo/decisions/merge-gate) — but a reference whose issue answers
// "not found" is a definitive answer, not a failed read: the forge
// itself silently ignores a reference whose target does not exist, so a
// dangling reference — a deleted issue, a mistyped number, an
// illustrative one in prose, a repository the token cannot see — is
// skipped like a reference naming a pull request, and only a genuine
// read failure holds the re-fetch off. A reference into a repository
// other than the pull request's pays the same admission test every
// other cross-repository read pays (§forgejo/webhook/repository-roles):
// outside the engine's remit it connects nothing, and an unknown scope
// fails the re-fetch. A reference whose repository names a path segment
// is refused before any URL is built, and the reads this loop makes
// are capped (maxAutoCloseRefs), charged beside the paid read they
// bound: a reference that costs no read — deduped against an edge
// row, refused as a path segment, turned away by the remit test —
// spends none of the budget and cannot truncate the read. A body
// that asks for more reads than the cap pays cannot have its
// relations read completely, and a partial answer presented as a
// complete one is the fail-open #180 exists to close: the re-fetch
// fails — a hold-off naming the failure, never a merge. The grammar
// itself stays uncapped, a faithful mirror for the predicate's pure
// in-memory read. Every drop — a skip, a refusal, the cap's
// overflow — leaves a debug line naming the resolved reference, so a
// dropped relation is visible rather than silent. A pull request is
// never a connected issue: it is a delivery, so a reference naming
// one is skipped.
func (e *Engine) connectedIssues(owner, name string, number int, body string) ([]ConnectedIssue, error) {
	prRepo := owner + "/" + name
	blocks, err := e.api.IssueBlocks(owner, name, number)
	if err != nil {
		return nil, err
	}
	var connected []ConnectedIssue
	seen := make(map[Key]bool)
	for i := range blocks {
		if blocks[i].IsPull {
			continue // only issues connect a PR to the graph this way
		}
		co, cn := splitRepo(blocks[i].Repository)
		blockers, err := e.api.IssueDependencies(co, cn, blocks[i].Number)
		if err != nil {
			return nil, err
		}
		seen[connectedKey(blocks[i].Repository, blocks[i].Number)] = true
		connected = append(connected, ConnectedIssue{Issue: blocks[i], Blockers: blockers})
	}
	resolved := 0
	for _, ref := range autoCloseRefs(body, prRepo) {
		if !validRepoRef(ref.repo) {
			// A reference whose repository names a path segment — "." or
			// "..", which the grammar's character class admits — names
			// no repository; it is refused here, before any URL is built
			// (§forgejo/webhook/re-fetch).
			e.logSkippedRef(prRepo, number, ref, "whose repository names a path segment")
			continue
		}
		if seen[ref.key()] {
			continue // the blocking-edge row already carries this issue
		}
		if !strings.EqualFold(ref.repo, prRepo) {
			// A cross-repository reference directs the engine's token
			// into a repository the PR author does not control, so it
			// pays the same admission test every other cross-repository
			// read pays (§forgejo/webhook/repository-roles): a reference
			// naming a repository outside the engine's remit connects
			// nothing, and an unknown scope fails the re-fetch rather
			// than reading on.
			r, err := e.repoRoles(ref.repo)
			if err != nil {
				return nil, err
			}
			if !r.any() {
				e.logSkippedRef(prRepo, number, ref, "into a repository outside the engine's remit")
				continue
			}
		}
		if resolved == maxAutoCloseRefs {
			// A reference every refusal and test has admitted — so one
			// that wants a read the budget can no longer pay — means the
			// relations of this body cannot be read completely. The
			// rows collected so far may be missing the delivery, and a
			// partial answer presented as a complete one is the
			// fail-open #180 exists to close: the set hold-off would
			// read it as not-a-member and merge-or-wait would land the
			// delivery out of turn, the survivor set chosen by the body
			// being read. The re-fetch fails — a hold-off naming the
			// failure, never a merge (§forgejo/webhook/re-fetch).
			e.logSkippedRef(prRepo, number, ref, "beyond the reference cap; the read is incomplete and the re-fetch fails")
			return nil, fmt.Errorf("connected issues of %s#%d: the body names more than %d closing references that cost a read, so the connected-issues read is incomplete", prRepo, number, maxAutoCloseRefs)
		}
		resolved++
		ro, rn := splitRepo(ref.repo)
		issue, err := e.api.GetIssue(ro, rn, ref.number)
		if err != nil {
			if isNotFound(err) {
				// Dangling reference: the forge ignores it too. The same
				// answer arrives from a repository the token cannot see,
				// so the line names the resolved reference — identifiers
				// only, never the body (§forgejo/webhook/re-fetch).
				e.logSkippedRef(prRepo, number, ref, "whose issue answers not found")
				continue
			}
			return nil, err
		}
		if issue.IsPull {
			e.logSkippedRef(prRepo, number, ref, "naming a pull request")
			continue // a pull request delivers; it never sits as a connected issue
		}
		if issue.Repository == "" {
			// The single-issue endpoint names no repository; the reference does.
			issue.Repository = ref.repo
		}
		blockers, err := e.api.IssueDependencies(ro, rn, ref.number)
		if err != nil {
			return nil, err
		}
		connected = append(connected, ConnectedIssue{Issue: *issue, Blockers: blockers})
	}
	return connected, nil
}

// issueRef is an issue a pull request body names: the repository the
// reference resolved to — the pull request's own for a bare #N — and the
// number it carried.
type issueRef struct {
	repo   string
	number int
}

// key is the reference's identity, compared case-insensitively on the
// repository — the reference grammar matches a repository name that way
// — and exactly on the number.
func (r issueRef) key() Key {
	return connectedKey(r.repo, r.number)
}

// logSkippedRef records one dropped closing reference — identifiers
// only, never the body — in one place, so the wording a reader greps
// for when a relation went missing cannot drift between its sites
// (§forgejo/webhook/re-fetch).
func (e *Engine) logSkippedRef(prRepo string, prNumber int, ref issueRef, why string) {
	e.log.Debug("connected issues: skipping a closing reference "+why,
		"pr", prRepo+"#"+strconv.Itoa(prNumber),
		"reference", ref.repo+"#"+strconv.Itoa(ref.number))
}

// connectedKey is the dedup key of a connected issue, matching a
// reference's identity so the same issue reached through either relation
// is read once (§forgejo/webhook/re-fetch).
func connectedKey(repo string, number int) Key {
	return Key{Repo: strings.ToLower(repo), Kind: KindIssue, Number: number}
}

// isNotFound reports whether err is the forge's definitive "no such
// object" answer — an HTTP 404 — as opposed to a failed read. The forge
// ignores a linked reference whose target does not exist, so a read
// told an issue is not found has succeeded in learning there is nothing
// there (§forgejo/webhook/re-fetch).
func isNotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound
}

// maxAutoCloseRefs caps the number of references connectedIssues
// resolves. The authoring contract needs one or two; the cap bounds
// the per-reference fan-out the approval path pays (one issue read and
// one blockers row per resolved reference) against a body written to
// cost the engine the delivery (§forgejo/webhook/re-fetch). It is
// charged where that fan-out happens — beside the issue read — so a
// reference the loop drops before reaching the API spends none of the
// budget, and eight means eight reads rather than eight references
// the body happened to name first. A reference that survives every
// refusal and test but meets the cap marks the read as incomplete,
// and the re-fetch fails on it rather than answering with the rows
// it happened to collect: which references survive must never be
// decided by the body being read. The cap belongs to that resolving
// loop, not to the grammar: the predicate's read of the same body is
// pure in-memory matching that costs nothing per reference, and #180
// asks nothing of it.
const maxAutoCloseRefs = 8

// autoCloseRefs lists the issues a pull request body auto-closes, in
// order of first appearance and without duplicates — the grammar read,
// with no decision about what the engine can afford to resolve: every
// distinct reference the body names is returned, and what to do with
// the ninth is its caller's, taken where the cost is paid. The grammar
// is the one prAutoClosesIssue applies and the forge itself honours:
// the default closing keywords, a bare #N resolved in the pull
// request's own repository and an owner/repo#N in the repository it
// names (https://forgejo.org/docs/latest/user/linked-references/,
// §forgejo/decisions/sync-set). Go's RE2 has no lookahead, so the
// pattern consumes the character that terminates a match; the scan
// rewinds to that character's start the way the forge's scanner rewinds
// over the whitespace its match ate
// (https://codeberg.org/forgejo/forgejo/src/branch/main/modules/references/references.go),
// so two references separated by nothing but a newline both resolve.
// A reference's repository carries its canonical identity, lowercased:
// the grammar matches repository names case-insensitively, the dedup
// key (issueRef.key) lowercases, and the scope lookup matches
// case-insensitively, so one casing decides the identity everywhere.
// A qualified reference whose repository names a path segment — "." or
// "..", the grammar's class admits dots — is carried like any other;
// connectedIssues refuses it before any URL is built.
func autoCloseRefs(body, prRepo string) []issueRef {
	var refs []issueRef
	seen := make(map[Key]bool)
	for pos := 0; pos < len(body); {
		m := closeKeywordRef.FindStringSubmatchIndex(body[pos:])
		if m == nil {
			break
		}
		match := pos
		if m[0] == 0 && pos > 0 && !isRefLead(body[pos]) {
			// The pattern's ^ alternative matches at the start of every
			// rewound slice, but the forge reads a reference's lead
			// against the whole message: no whitespace or bracket
			// before the keyword means no reference here. Step one byte
			// and let the next scan find the real lead, if any.
			pos++
			continue
		}
		// m[6] bounds the start of the terminator the match consumed —
		// one character, or the empty end of the body. The terminator
		// group always participates (its $ alternative matches empty at
		// the end) and always starts at least one byte past the match
		// start (the keyword and #N sit between), so either way the
		// cursor advances.
		pos = match + m[6]
		number, err := strconv.Atoi(body[match+m[4] : match+m[5]])
		if err != nil {
			continue
		}
		repo := prRepo
		if m[2] >= 0 {
			repo = body[match+m[2] : match+m[3]]
		}
		ref := issueRef{repo: strings.ToLower(repo), number: number}
		if seen[ref.key()] {
			continue
		}
		seen[ref.key()] = true
		refs = append(refs, ref)
	}
	return refs
}

// validRepoRef reports whether a reference's repository is a plain
// owner/repo pair. The grammar's character class admits dots, and the
// resolved string reaches the API URL, so "." and ".." segments are
// refused (§forgejo/webhook/re-fetch).
func validRepoRef(s string) bool {
	owner, name := splitRepo(s)
	if owner == "" || name == "" || strings.ContainsRune(name, '/') {
		return false
	}
	return owner != "." && owner != ".." && name != "." && name != ".."
}

// isRefLead reports whether b can stand at the start of a closing
// reference: whitespace or an opening bracket, the forge's leading
// context (https://forgejo.org/docs/latest/user/linked-references/).
func isRefLead(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\f' || b == '(' || b == '['
}

// fetchPRSetState assembles the state the approval path's sync-set
// hold-off and the merge gate read for a pull request: its connected
// issues — those its body auto-closes and those its blocking edges name
// — each with its blockers, and the landing-set membership, the open
// sync issues it belongs to, read through a connected member's
// membership edges or, where the PR stands in a grouping issue's own
// blockers, through that edge. Both paths that can dispatch
// merge-or-wait for a PR build this state here, the event's re-fetch and
// the unblock cascade's PR leg, so the hold-off cannot see membership on
// one and be blind to it on the other: a member of an open set never
// merges on its own, whichever direction the approval arrived from. The
// body the references are read from is the pull request's own, already
// fetched, so a delivery that names no issue costs no read beyond the
// edge hop it always did (§forgejo/webhook/re-fetch,
// §forgejo/decisions/sync-set, §forgejo/decisions/unblock-cascade).
func (e *Engine) fetchPRSetState(owner, name string, number int, st *State) error {
	connected, err := e.connectedIssues(owner, name, number, st.PR.Body)
	if err != nil {
		return err
	}
	st.ConnectedIssues = connected
	return e.fetchSyncSets(owner, name, number, st)
}

// fetchSyncSets reads the landing-set membership of the pull request in
// st, from the connected issues the assembly already holds. Two edges
// make a PR a member. A connected issue carrying agentic-sync-merge is
// the ordinary one: the hop to the open sync issues it blocks is taken
// only for members, so the ordinary PR path costs what it always did. A
// connected issue carrying `sync` whose blockers include this PR is the
// direct one — the shape §decisions/sync-set reads as a member whose
// delivery is itself — and it needs no read at all: the edge is already
// in the dependency rows the merge gate fetched, and a predicate that
// sees the row while the approval path stays blind to it would land a
// set's delivery through merge-or-wait, which is the outcome the labels
// table forbids (§forgejo/webhook/re-fetch, §forgejo/decisions/sync-set).
func (e *Engine) fetchSyncSets(owner, name string, number int, st *State) error {
	prRepo := owner + "/" + name
	for i := range st.ConnectedIssues {
		c := &st.ConnectedIssues[i]
		if hasLabel(&c.Issue, LabelAgenticSyncMerge) {
			co, cn := splitRepo(c.Issue.Repository)
			blocks, err := e.api.IssueBlocks(co, cn, c.Issue.Number)
			if err != nil {
				return err
			}
			for j := range blocks {
				b := blocks[j]
				if b.IsPull || b.State != "open" || !hasLabel(&b, LabelSync) {
					continue // only open grouping issues are membership edges
				}
				c.SyncSets = append(c.SyncSets, b)
			}
			continue
		}
		if c.Issue.State == "open" && hasLabel(&c.Issue, LabelSync) && blocksPull(c.Blockers, prRepo, number) {
			st.SyncSets = append(st.SyncSets, c.Issue)
		}
	}
	return nil
}

// blocksPull reports whether the blocker rows include the pull request
// under consideration — the entry that makes it a member of the issue
// whose blockers are being read (§forgejo/decisions/sync-set).
func blocksPull(blockers []Issue, repo string, number int) bool {
	for i := range blockers {
		b := &blockers[i]
		if b.IsPull && b.State == "open" && b.Number == number && b.Repository == repo {
			return true
		}
	}
	return false
}

// syncMembers projects the blocking issues of a sync issue onto the
// members of a landing set, attaching to every open member the state
// of its delivering PR — mergeability and reviews — the sync
// predicate reads. A member delivers its change through the pull
// request that auto-closes it: an open pull request of the member's
// repository whose body carries an auto-close reference to the
// member, the same pull-request-side relation the forge honours when
// a merged delivery closes the issue. No blocking edge between a
// member and its delivery is consulted or expected — implementers
// record none, the graph's edges mean *cannot proceed while open*,
// not *delivers*, and a predicate gated on an edge nobody writes
// strands every set in silence (§forgejo/decisions/sync-set,
// s-git/maitred#171). Where several open pull requests auto-close a
// member the first in the repository's listing is its delivery, the
// one-delivery-per-member projection; the repository's listing is
// read once per evaluation whatever the set's shape. An open member
// without agentic-sync-merge is read no further: it holds the set off
// on the label alone whatever its PR says.
//
// A pull request that blocks the grouping issue **directly** is a
// member whose delivery is itself. It is read through its own state
// rather than dropped: nothing in the vocabulary forbids the edge, and
// a predicate that never saw the row would dispatch a set whose
// unmerged delivery the graph had put in front of it. The test the
// row is held to is the one the predicate already asks of a member's
// delivering PR — open, mergeable, approved at head, opted in
// (§forgejo/decisions/sync-set).
func (e *Engine) syncMembers(blockers []Issue) ([]SyncMember, error) {
	var members []SyncMember
	listing := make(map[string][]PullRequest)
	for i := range blockers {
		b := blockers[i]
		m := SyncMember{Issue: b}
		owner, name := splitRepo(b.Repository)
		if b.IsPull {
			if b.State == "open" {
				pr, err := e.api.GetPullRequest(owner, name, b.Number)
				if err != nil {
					return nil, err
				}
				if prOpen(pr) {
					reviews, err := e.api.ListPullRequestReviews(owner, name, b.Number)
					if err != nil {
						return nil, err
					}
					m.PR, m.Reviews = pr, reviews
				}
			}
			members = append(members, m)
			continue
		}
		if b.State == "open" && hasLabel(&b, LabelAgenticSyncMerge) {
			pulls, ok := listing[b.Repository]
			if !ok {
				var err error
				pulls, err = e.api.ListOpenPullRequests(owner, name)
				if err != nil {
					return nil, err
				}
				listing[b.Repository] = pulls
			}
			for j := range pulls {
				if !prAutoClosesIssue(pulls[j].Body, b.Repository, b.Repository, b.Number) {
					continue
				}
				// The listing is the lead; the single read is the
				// authoritative state the predicate and the revision
				// digest, as everywhere else (§forgejo/client/operations).
				pr, err := e.api.GetPullRequest(owner, name, pulls[j].Number)
				if err != nil {
					return nil, err
				}
				if !prOpen(pr) {
					continue // closed or merged between the listing and the read
				}
				reviews, err := e.api.ListPullRequestReviews(owner, name, pulls[j].Number)
				if err != nil {
					return nil, err
				}
				m.PR, m.Reviews = pr, reviews
				break
			}
		}
		members = append(members, m)
	}
	return members, nil
}

// closeKeywordRef mirrors the forge's own reading of an auto-close
// reference in a pull request body: one of the default closing
// keywords, starting at whitespace or a bracket, adjacent to the
// reference (a colon and exactly one space allowed between), then #N
// in the pull request's own repository or owner/repo#N across
// repositories, terminated by a non-word character or the end
// (https://forgejo.org/docs/latest/user/linked-references/ and
// https://codeberg.org/forgejo/forgejo/src/branch/main/models/references/references.go).
// The terminator is captured so autoCloseRefs can rewind to it: Go's
// RE2 has no lookahead, and without the rewind a reference terminated
// by nothing but a newline would eat the next reference's leading
// whitespace and the pair would resolve to one. The engine mirrors the
// forge's **default** keyword set: an instance that reconfigures the
// keywords reads differently than the predicate does — a known
// limitation of a read-only client (§forgejo/decisions/sync-set).
var closeKeywordRef = regexp.MustCompile(
	`(?i)(?:\s|^|\(|\[)(?:closed|closes|close|fixed|fixes|fix|resolved|resolves|resolve):? (?:([0-9a-zA-Z-_.]+/[0-9a-zA-Z-_.]+))?#([0-9]+)([^0-9A-Za-z]|$)`)

// prAutoClosesIssue reports whether the pull request body auto-closes
// the given issue — the relation the forge honours by closing the
// issue when the pull request merges, the relation the sync predicate
// reads a member's delivering PR through, and the relation the merge
// opt-in's issue seat is read from: the label means the pull requests
// that implement an issue may auto-merge, and implementing it is
// carrying a closing reference to it
// (§forgejo/decisions/sync-set, §forgejo/decisions/merge-gate,
// s-git/maitred#171). A bare #N
// resolves in the pull request's own repository; a qualified
// owner/repo#N in the repository it names. The resolution itself lives
// in autoCloseRefs, so the predicate's read and the approval path's
// read cannot drift apart on how the grammar resolves a reference.
func prAutoClosesIssue(body, prRepo, issueRepo string, issueNumber int) bool {
	for _, ref := range autoCloseRefs(body, prRepo) {
		if ref.number == issueNumber && strings.EqualFold(ref.repo, issueRepo) {
			return true
		}
	}
	return false
}

// graphNode builds the blocker-graph node for issue: its blocks, each
// open block with its blockers, each closed block followed recursively —
// the shape the unblock cascade walks. A visited set terminates cycles
// (§forgejo/webhook/re-fetch).
func (e *Engine) graphNode(issue *Issue) (*IssueNode, error) {
	visited := map[Key]bool{issueKey(issue): true}
	return e.fillNode(issue, visited)
}

func (e *Engine) fillNode(issue *Issue, visited map[Key]bool) (*IssueNode, error) {
	node := &IssueNode{Issue: *issue}
	owner, name := splitRepo(issue.Repository)
	blocks, err := e.api.IssueBlocks(owner, name, issue.Number)
	if err != nil {
		return nil, err
	}
	node.Blocks = make([]IssueNode, len(blocks))
	for i := range blocks {
		b := blocks[i]
		node.Blocks[i].Issue = b
		bo, bn := splitRepo(b.Repository)
		switch b.State {
		case "open":
			blockers, err := e.api.IssueDependencies(bo, bn, b.Number)
			if err != nil {
				return nil, err
			}
			node.Blocks[i].Blockers = issueNodes(blockers)
		case "closed":
			k := issueKey(&b)
			if visited[k] {
				continue // cycle: leave the node as a leaf
			}
			visited[k] = true
			child, err := e.fillNode(&b, visited)
			if err != nil {
				return nil, err
			}
			node.Blocks[i] = *child
		}
	}
	return node, nil
}

// urlArguments counts the URL placeholders a prompt template references
// and how many of them this dispatch fills. The count is textual on
// purpose: what matters is whether the prompt asks for a URL at all and
// whether any arrived — not which action asked, which would make the
// engine duplicate the decision table's kinds
// (§forgejo/decisions/transition-table).
//
// A prompt that asks for a URL and receives none renders as a command
// with no argument: the persona is handed "/issue-implementer " and Pi
// raises nothing over it, which is the silent half of #121. Failing the
// dispatch is the loud alternative, and unlike a load-time rule it costs
// no deployment whose config is not yet fixed (§forgejo/config/canned-prompts).
func urlArguments(prompt string, p Placeholders) (referenced, filled int) {
	for _, u := range []struct {
		field string
		value string
	}{
		{".IssueURL", p.IssueURL},
		{".PRURL", p.PRURL},
	} {
		// Matched on the field name rather than one spelling of the
		// reference, so "{{ .IssueURL }}" counts as readily as "{{.IssueURL}}".
		if strings.Contains(prompt, u.field) {
			referenced++
			if u.value != "" {
				filled++
			}
		}
	}
	return referenced, filled
}

// The caps on a dispatched task's title (§forgejo/webhook/the-pipeline):
// the subject — the issue or PR's own title — is truncated so a long one
// cannot produce an unreadable row, and the composed line is capped on
// top of that in case the reference and the verb push it out too.
const (
	maxTaskSubjectRunes = 80
	maxTaskTitleRunes   = 120
)

// trimRunes caps s at n runes, marking an elision with an ellipsis so a
// truncated title reads as truncated.
func trimRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}

// taskSubject is the title of the object the state carries — the pull
// request where one is present, the issue otherwise, so a dispatch that
// carries both names the object its URL points at
// (§forgejo/webhook/the-pipeline).
func taskSubject(st State) string {
	switch {
	case st.PR != nil:
		return st.PR.Title
	case st.Issue != nil:
		return st.Issue.Title
	}
	return ""
}

// taskURL is the dispatch's reference link: the pull request's HTML URL
// when the state carries one, the issue's otherwise, and empty where
// neither is there — the engine invents no link. The URL-placeholder
// refusal above guards the prompt, not this field, and is unaffected
// (§forgejo/config/canned-prompts).
func taskURL(st State) string {
	switch {
	case st.PR != nil:
		return st.PR.HTMLURL
	case st.Issue != nil:
		return st.Issue.HTMLURL
	}
	return ""
}

// taskTitle composes the dispatched task's human-readable name — the
// dispatch's verb, the subject's title where the state carries one,
// and the `repo#number` reference — in the one helper every dispatch
// path goes through, so a queue system's row reads the same whichever
// object or lens dispatched, e.g. "Review: Make everything awesome
// and pink (o/r#1337)" (§forgejo/webhook/the-pipeline). The verb
// names the dispatch: the action's own verb for a singleton, the
// action with its subject for a fan-out ("Review as lens-a"), so the
// title budget lives in exactly one function.
func taskTitle(verb string, ev Event, st State) string {
	ref := fmt.Sprintf("%s#%d", ev.Repo, ev.Number)
	if subject := taskSubject(st); subject != "" {
		// The subject takes what is left of the line's budget once the
		// verb and the reference are paid for — never more than its own
		// cap. An elision may eat the subject but never the reference.
		overhead := utf8.RuneCountInString(verb) + utf8.RuneCountInString(ref) + utf8.RuneCountInString(": ()") + 1
		// Below six runes a truncated subject would read as an ellipsis
		// and nothing else, so such a line drops it and keeps the
		// action-and-reference form.
		if budget := min(maxTaskSubjectRunes, maxTaskTitleRunes-overhead); budget >= 6 {
			return fmt.Sprintf("%s: %s (%s)", verb, trimRunes(subject, budget), ref)
		}
	}
	return trimRunes(fmt.Sprintf("%s (%s)", verb, ref), maxTaskTitleRunes)
}

// dispatchVerb is the title verb of one dispatch: the action's verb,
// capitalised, with the fan-out's subject named alongside it where
// there is one — "Implement" for a singleton, "Review as lens-a" for
// a lens (§forgejo/webhook/the-pipeline).
func dispatchVerb(action Action, subject string) string {
	verb := string(action)
	if r := []rune(verb); len(r) > 0 {
		verb = string(unicode.ToUpper(r[0])) + string(r[1:])
	}
	if subject != "" {
		verb += " as " + subject
	}
	return verb
}

// dispatch fills the action's canned prompt and enqueues the task
// through dispatchPrompt with no subject — the singleton shape
// (§forgejo/webhook/the-pipeline). The landed dispatch is stamped for the
// re-fire bound naming the action it landed for: only a state-governed
// row consults a stamp, and only its own action's stamp, so a dispatch
// can pace the same action's re-fire and never another's
// (§forgejo/decisions/loop-prevention,
// §forgejo/reconciliation/the-state-is-the-watermark).
func (e *Engine) dispatch(ev Event, st State, action Action) error {
	pc, err := e.cfg.PromptFor(action)
	if err != nil {
		return err
	}
	rev := revisionFor(ev, st)
	if err := e.dispatchPrompt(ev, st, *pc, "", action, rev); err != nil {
		return err
	}
	// Stamp the landed dispatch for the re-fire bound, naming the action
	// that landed. Only a state-governed row consults the stamp, so a
	// gated dispatch's stamp is inert for the gated rows themselves; it
	// can only pace the same action's state-governed re-fire at this
	// revision, which is what keeps one revision one dispatch when an
	// event and the sweep arrive at it together
	// (§forgejo/decisions/loop-prevention,
	// §forgejo/reconciliation/the-state-is-the-watermark).
	e.stampReFire(ev.KeyOf(), action, rev)
	return nil
}

// reFireStamp is one landed dispatch the engine remembers for the
// re-fire bound: the action that landed, the revision it landed at, and
// when. The action is part of the stamp because the bound has always
// paced one unchanged `(action, revision)`: a dispatch of one action
// must not hold a different action's state-governed row off, and a
// hold-off naming a re-fire that never happened would be a lie
// (§forgejo/decisions/loop-prevention).
type reFireStamp struct {
	action   Action
	revision string
	at       time.Time
}

// boundReFire applies the re-fire bound to Decide's answer: a
// state-governed dispatch the engine landed at this revision inside one
// reconcile interval becomes a hold-off naming the bound, whichever
// path brought the event (§forgejo/decisions/loop-prevention,
// §forgejo/reconciliation/the-state-is-the-watermark). The stamp it
// consults is the one this action's own dispatch left, so an unrelated
// dispatch at the same revision bounds nothing here. A revision-gated
// decision consults nothing: the bound exists to pace the rows the
// watermark does not gate. The bound is the engine's own stamp rather
// than the watermark, so a store that cannot be read narrows the re-fire
// instead of switching the bound off; a revision with no stamp is a
// first dispatch, which is never touched.
func (e *Engine) boundReFire(key Key, ev Event, st State, d Decision) Decision {
	if !d.Dispatched || !d.StateGoverned {
		return d
	}
	rev := revisionFor(ev, st)
	if e.reFiredRecently(key, d.Action, rev) {
		return holdOffReFireBound(d.Action, rev, e.cfg.ReconcileInterval)
	}
	return d
}

// reFiredRecently reports whether the stamp for key still bounds a
// re-fire of action at revision: another action's dispatch at the same
// revision, an interval spent, or a revision the object has moved past
// bounds nothing, so the bound is a rate on one `(action, revision)` and
// never a hold-off on a dispatch the engine has not made
// (§forgejo/decisions/loop-prevention).
func (e *Engine) reFiredRecently(key Key, action Action, revision string) bool {
	e.reFireMu.Lock()
	defer e.reFireMu.Unlock()
	last, ok := e.reFires[key]
	return ok && last.action == action && last.revision == revision && time.Since(last.at) < e.cfg.ReconcileInterval
}

// stampReFire records a landed singleton dispatch for the bound. A
// revision the object has moved past is a new revision, so the stamp is
// replaced rather than kept: the bound is one per unchanged
// `(action, revision)`, never a hold-off on a dispatch the engine has not
// made.
func (e *Engine) stampReFire(key Key, action Action, revision string) {
	e.stampReFireAt(key, action, revision, time.Now())
}

// stampReFireAt records the stamp at a given moment; the tests
// that need an interval spent use it rather than waiting one out.
func (e *Engine) stampReFireAt(key Key, action Action, revision string, at time.Time) {
	e.reFireMu.Lock()
	defer e.reFireMu.Unlock()
	e.reFires[key] = reFireStamp{action: action, revision: revision, at: at}
}

// dispatchPrompt is the one dispatch pipeline: it fills the canned
// prompt, refuses an invocation that names no object, and enqueues
// the task with the dedup key of the dispatch identity — the key's
// repo, kind, and number, the action, the subject where the dispatch
// names one (a fan-out lens's identity), and the revision — so a
// queue system with de-duplication (hotelier's dedup_key) squelches a
// re-dispatch of the same (action, subject, revision) while the first
// task is still pending (§forgejo/webhook/the-pipeline,
// §forgejo/decisions/review-lenses). The subject is what keeps one
// lens's task from squelching a sibling's at the same revision; the
// singleton path carries none. The key carries the repo verbatim
// (slash retained — it is opaque to the queue system), so two
// distinct repos never produce the same key. The task ID, by
// contrast, keeps its "-"-replaced repo: there the ambiguity is only
// a traceability concern. The prompt contract, the URL-placeholder
// refusal, and the task shape all live here, so the singleton and
// the fan-out cannot drift apart (§forgejo/config/canned-prompts).
func (e *Engine) dispatchPrompt(ev Event, st State, pc PromptConfig, subject string, action Action, rev string) error {
	p := Placeholders{
		Repo:   ev.Repo,
		Kind:   string(ev.Kind),
		Number: ev.Number,
		Sender: ev.Sender,
		Action: string(action),
	}
	if st.Issue != nil {
		p.IssueURL = st.Issue.HTMLURL
	}
	if st.PR != nil {
		p.PRURL = st.PR.HTMLURL
	}
	prompt, err := pc.Render(p)
	if err != nil {
		return err
	}
	if referenced, filled := urlArguments(pc.Prompt, p); referenced > 0 && filled == 0 {
		return fmt.Errorf("dispatch %s for %s: the canned prompt references a URL placeholder but the state carries no URL — refusing to enqueue an invocation that names no object", dispatchVerb(action, subject), ev.KeyOf())
	}
	identity := string(action)
	if subject != "" {
		identity += "-" + subject
	}
	task := &queue.Task{
		ID:       fmt.Sprintf("forgejoeng-%s-%s-%d-%s-%d", strings.ReplaceAll(ev.Repo, "/", "-"), ev.Kind, ev.Number, identity, time.Now().UnixNano()),
		Prompt:   prompt,
		Persona:  pc.Identity,
		Timeout:  int(pc.Timeout.Seconds()),
		DedupKey: fmt.Sprintf("forgejoeng-%s-%s-%d-%s-%s", ev.Repo, ev.Kind, ev.Number, identity, rev),
		Title:    taskTitle(dispatchVerb(action, subject), ev, st),
		URL:      taskURL(st),
		// The tier is decided here, at the one dispatch site, from the
		// labels read now — never from anything cached between
		// schedulings (§forgejo/webhook/vip-priority).
		Priority: e.taskPriority(st),
	}
	if err := e.queue.AddTask(task); err != nil {
		// The tier is an ask, not a precondition: a vip dispatch whose
		// enqueue fails is the ordinary failure every path already
		// handles — nothing recorded, the sweep retrying
		// (§forgejo/webhook/vip-priority).
		return fmt.Errorf("enqueue %s for %s: %w", dispatchVerb(action, subject), ev.KeyOf(), err)
	}
	e.log.Info("dispatched", "key", ev.KeyOf().String(), "task", task.ID)
	return nil
}

// dispatchLenses dispatches the review fan-out's emissions for the
// event's own object: one task per lens, each carrying its own dedup
// key, then one watermark write recording a subject slot per lens that
// made it onto the queue — the main slot and the other lenses' slots
// are preserved, so a fan-out neither erases a fix-feedback dispatch
// nor a mid-flight sibling lens (§forgejo/decisions/review-lenses,
// §forgejo/state/watermark). A lens whose enqueue failed records no
// slot and the sweep retries it, exactly as a failed singleton dispatch
// retries.
func (e *Engine) dispatchLenses(key Key, w *Watermark, ev Event, st State, lenses []LensDispatch) {
	rev := revisionFor(ev, st)
	slots := w.subjectSlots()
	if slots == nil {
		slots = map[string]Slot{}
	}
	dispatched := false
	for _, ld := range lenses {
		lens, ok := findLens(e.cfg.Reviews, ld.Lens)
		if !ok {
			// The decision emitted a lens the config does not carry —
			// impossible while Decide and the engine read the same
			// list; loud in the log if the two ever diverge.
			e.log.Error("review lens not configured", "key", key.String(), "lens", ld.Lens)
			continue
		}
		if err := e.dispatchLens(ev, st, lens, rev); err != nil {
			e.log.Error("dispatch failed", "key", key.String(), "action", string(ActionReview), "lens", ld.Lens, "error", err)
			continue
		}
		slots[ld.Lens] = Slot{Action: string(ActionReview), Revision: rev}
		dispatched = true
	}
	if !dispatched {
		return
	}
	next := watermarkKeepSlots(w, slots)
	if err := e.store.saveLocked(key, next); err != nil {
		e.log.Error("save watermark", "key", key.String(), "error", err)
	}
}

// findLens looks a configured lens up by identity.
func findLens(lenses []ReviewLens, identity string) (ReviewLens, bool) {
	for _, l := range lenses {
		if l.Identity == identity {
			return l, true
		}
	}
	return ReviewLens{}, false
}

// watermarkAt returns a watermark whose main slot records action at
// revision, keeping the subject slots a previous fan-out wrote
// (§forgejo/state/watermark).
func watermarkAt(prev *Watermark, action, revision string) Watermark {
	return Watermark{Action: action, Revision: revision, Subjects: prev.subjectSlots()}
}

// watermarkKeepSlots returns a watermark keeping prev's main slot and
// adding/overwriting the given subject slots — the fan-out's write.
func watermarkKeepSlots(prev *Watermark, slots map[string]Slot) Watermark {
	next := Watermark{Subjects: prev.subjectSlots()}
	if prev != nil {
		next.Action = prev.Action
		next.Revision = prev.Revision
	}
	if next.Subjects == nil {
		next.Subjects = map[string]Slot{}
	}
	for k, v := range slots {
		next.Subjects[k] = v
	}
	return next
}

// dispatchLens dispatches one review lens through the same
// dispatchPrompt as the singleton paths, with the lens identity as
// the subject: three review tasks over one head are three tasks, not
// one squelched trio, while a re-dispatch of the same (lens,
// revision) still squelches while pending
// (§forgejo/webhook/the-pipeline, §forgejo/decisions/review-lenses).
func (e *Engine) dispatchLens(ev Event, st State, lens ReviewLens, rev string) error {
	pc := PromptConfig{Prompt: lens.Prompt, Identity: lens.Identity, Timeout: lens.Timeout}
	return e.dispatchPrompt(ev, st, pc, lens.Identity, ActionReview, rev)
}

// cascadeHoldOffReason qualifies a cascade hold-off with the roles of
// the repository holding its key. An emission names the action and the
// role it requires when the roles deny it; a hold-off must not promise
// an action those roles exclude either, so where they deny wrap-up the
// reason says so in the same wording — the hold-off helpers keep the
// two paths saying the same thing. The scope cache makes the test free
// (§forgejo/webhook/repository-roles, §forgejo/decisions/unblock-cascade).
func (e *Engine) cascadeHoldOffReason(ch CascadeHoldOff) string {
	r, err := e.repoRoles(ch.Key.Repo)
	switch {
	case err != nil:
		return ch.Reason + "; " + holdOffScopeUnknown(ch.Key.Repo, err).Reason
	case !r.any():
		return ch.Reason + "; " + holdOffOutOfRemit(ch.Key.Repo).Reason
	case !admissibleInRoles(ActionWrapUp, r):
		return ch.Reason + "; " + holdOffActionInadmissible(ActionWrapUp, ch.Key.Repo).Reason
	}
	return ch.Reason // the roles admit wrap-up: nothing to qualify
}

// dispatchCascade dispatches the entry's action for one key of the
// unblock cascade, applying the key's own watermark first, so an
// already-dispatched (action, revision) is not re-dispatched
// (§forgejo/decisions/unblock-cascade). A sync-set candidate — whose
// emission's action is sync — is dispatched through dispatchSyncSet.
// A newly unblocked PR key — whose
// emission's action is merge-or-wait — is dispatched through
// dispatchCascadePR. Admissibility is judged by the roles of the
// repository holding the key, never the repository whose closure
// rooted the cascade: implement requires the work-item role and
// wrap-up the outcomes role, so a cascade rooted at a work-item
// issue can emit wrap-up into an outcomes repository
// (§forgejo/webhook/repository-roles). An issue carrying a reserved
// label is never an implement candidate, cascade included
// (§forgejo/decisions/transition-table); the re-fetched issue
// re-checks the emission — the state wins over the graph data
// (§forgejo/decisions/unblock-cascade).
func (e *Engine) dispatchCascade(cd CascadeDispatch, sender string) error {
	if cd.Action == ActionSync {
		return e.dispatchSyncSet(cd, sender)
	}
	k := cd.Key
	if k.Kind == KindPR {
		return e.dispatchCascadePR(k, sender)
	}
	r, err := e.repoRoles(k.Repo)
	if err != nil || !r.any() {
		// A cascade key is tested the same way as the event path, and its
		// hold-off is recorded in the decision log like any other
		// decision, wherever it is made
		// (§forgejo/webhook/repository-roles,
		// §forgejo/observability/the-decision-log).
		held := holdOffOutOfRemit(k.Repo)
		if err != nil {
			held = holdOffScopeUnknown(k.Repo, err)
		}
		ev := Event{Type: EventIssueOpened, Repo: k.Repo, Kind: k.Kind, Number: k.Number, Sender: sender}
		e.recordDecision(k, ev, State{}, held)
		return nil
	}
	if !admissibleInRoles(cd.Action, r) {
		ev := Event{Type: EventIssueOpened, Repo: k.Repo, Kind: k.Kind, Number: k.Number, Sender: sender}
		e.recordDecision(k, ev, State{}, holdOffActionInadmissible(cd.Action, k.Repo))
		return nil
	}
	owner, name := splitRepo(k.Repo)
	issue, err := e.api.GetIssue(owner, name, k.Number)
	if err != nil {
		return err
	}
	if issue.State != "open" {
		return nil // closed in the meantime: nothing to dispatch
	}
	if d, ok := cascadeActionFitsState(cd.Action, issue); !ok {
		ev := Event{Type: EventIssueOpened, Repo: k.Repo, Kind: k.Kind, Number: k.Number, Sender: sender}
		e.recordDecision(k, ev, State{Issue: issue}, d)
		return nil
	}
	w, err := e.store.Load(k)
	if err != nil {
		e.log.Warn("load watermark (continuing without)", "key", k.String(), "error", err)
		w = nil
	}
	rev := issueRevision(issue)
	if alreadyDispatched(w, cd.Action, rev) {
		return nil
	}
	ev := Event{Type: EventIssueOpened, Repo: k.Repo, Kind: KindIssue, Number: k.Number, Sender: sender}
	// The vip walk reads the issue's blocker rows for a labelled tracker
	// (§forgejo/webhook/vip-priority), and this is the one path that
	// dispatches an issue without holding them — the cascade carries
	// keys, not state — so the leg pays the one read the decision made
	// elsewhere. A lost read costs the tier and not the dispatch: the
	// task goes out at the queue's default with a warn line naming it
	// (§forgejo/webhook/vip-priority).
	st := State{Issue: issue}
	blockers, err := e.api.IssueDependencies(owner, name, k.Number)
	if err != nil {
		e.log.Warn("vip walk: issue-dependencies read failed; scheduling at the queue's default tier",
			"key", k.String(), "error", err)
	} else {
		st.Blockers = blockers
	}
	if err := e.dispatch(ev, st, cd.Action); err != nil {
		return err
	}
	return e.store.Save(k, Watermark{Action: string(cd.Action), Revision: rev})
}

// dispatchCascadePR dispatches the unblock cascade for a newly unblocked PR
// key (§forgejo/decisions/unblock-cascade): the closure that rooted
// the cascade closed the last issue-side blocker of the PR's connected
// issue, so the merge the gate held off now fires — merge-or-wait and
// nothing else. Admission and admissibility are judged by the roles of
// the repository holding the PR, never the repository whose closure
// rooted the cascade (§forgejo/webhook/repository-roles), and the
// re-fetched PR state decides: only a decision of merge-or-wait
// dispatches — a PR not standing at an approved head, a PR whose gate
// still sees an open blocker on re-fetch, a PR carrying no
// agentic-auto-merge opt-in on either seat, and a PR whose connected issue is a
// member of an open landing set all dispatch nothing, each recorded
// in the decision log with the sweep as the backstop
// (§forgejo/decisions/merge-gate, §forgejo/decisions/sync-set). The PR
// key's own watermark applies before dispatching, the exempt merge row
// of an opted-in PR excepted, and that row is paced here by the re-fire
// bound (§forgejo/decisions/loop-prevention). The leg can be woken by a
// close in a repository holding no maitred role —
// handleCascadeOnlyClose reads through the graph on purpose — but it is
// admitted and judged by the roles of the PR's own repository and
// re-fetches the PR, so such a close can repeat a dispatch the bound
// already covers, never a merge the gate would refuse
// (§forgejo/reconciliation/watermark-exemptions); the set's own
// decision is not emitted from here — the walk follows the closed
// node's edges only — and stays the sweep's row to make.
func (e *Engine) dispatchCascadePR(k Key, sender string) error {
	ev := Event{Type: EventPRReviewed, Repo: k.Repo, Kind: KindPR, Number: k.Number, Sender: sender}
	r, err := e.repoRoles(k.Repo)
	if err != nil || !r.any() {
		held := holdOffOutOfRemit(k.Repo)
		if err != nil {
			held = holdOffScopeUnknown(k.Repo, err)
		}
		e.recordDecision(k, ev, State{}, held)
		return nil
	}
	if !admissibleInRoles(ActionMergeOrWait, r) {
		e.recordDecision(k, ev, State{}, holdOffActionInadmissible(ActionMergeOrWait, k.Repo))
		return nil
	}
	owner, name := splitRepo(k.Repo)
	pr, err := e.api.GetPullRequest(owner, name, k.Number)
	if err != nil {
		return err
	}
	if !prOpen(pr) {
		return nil // closed or merged in the meantime: nothing to dispatch
	}
	reviews, err := e.api.ListPullRequestReviews(owner, name, k.Number)
	if err != nil {
		return err
	}
	// The cascade's PR leg decides on the same state the event path
	// assembles — connected issues and their landing-set membership
	// included. Without the membership hop the hold-off would read an
	// empty SyncSets and a member of an open set would merge here, on
	// the one path the labels table forbids it on
	// (§forgejo/decisions/sync-set, §forgejo/decisions/unblock-cascade).
	st := State{PR: pr, Reviews: reviews, ImplementerIdentity: e.cfg.ImplementerIdentity, ReviewLenses: e.cfg.Reviews}
	if err := e.fetchPRSetState(owner, name, k.Number, &st); err != nil {
		return err
	}
	// Insurance against the re-fetched state, through the same pure
	// decision and the same gate the event path takes, under this PR
	// key's lock exactly as the event path takes it: the watermark read,
	// the bound's check and the dispatch that stamps it are one critical
	// section. It is reached while the cascade root's lock is held, so
	// locks go root first and target second — the one nesting
	// §forgejo/state/concurrency permits — and the PR is re-fetched above
	// the lock, so no walk re-enters a key this leg holds
	// (§forgejo/state/concurrency, §forgejo/decisions/unblock-cascade).
	var d Decision
	var dispErr error
	e.store.WithKeyLock(k, func() {
		w, err := e.store.loadLocked(k)
		if err != nil {
			e.log.Warn("load watermark (continuing without)", "key", k.String(), "error", err)
			w = nil
		}
		d = e.boundReFire(k, ev, st, Decide(ev, st, w))
		if d.Action == ActionMergeOrWait {
			// The dispatch carries the assembled state, not the bare
			// pull request: the delivered issue's labels are read from
			// the connected-issue rows this path already fetched, so
			// the merge the gate now releases is judged on the same
			// carriers the event path judges, with no read the decision
			// did not already pay (§forgejo/webhook/vip-priority).
			// taskURL, taskTitle and revisionFor read st.PR, which the
			// assembled state carries unchanged, so nothing but the
			// tier can differ.
			if dispErr = e.dispatch(ev, st, ActionMergeOrWait); dispErr == nil {
				dispErr = e.store.saveLocked(k, watermarkAt(w, string(ActionMergeOrWait), pr.HeadSHA))
			}
			return
		}
		// A fork of merge-or-wait — a landing-set member, or a re-fire the
		// bound has caught — is recorded like the sibling cascade
		// dispatchers record theirs: this path has no backstop of its
		// own and the decision log is the engine's only observability, so
		// it must leave a line naming why, not only a queue that stayed
		// empty (§forgejo/decisions/unblock-cascade,
		// §forgejo/observability/the-decision-log).
		e.recordDecision(k, ev, st, d)
	})
	return dispErr
}

// dispatchSyncSet dispatches the sync-set candidate the cascade walk
// or the approval path emitted: the grouping issue's own watermark
// applies at the set revision, and the re-fetched set state is
// decided through the same pure row the sweep takes — an unready set
// stays silent, its hold-off at home only in the decision log and
// nothing enqueued (§forgejo/decisions/sync-set). Admissibility is
// judged by the roles of the repository holding the grouping issue:
// sync requires the outcomes role, so a candidate emitted from a
// work-item repository lands its decision in the outcomes repository
// the set belongs to (§forgejo/webhook/repository-roles).
func (e *Engine) dispatchSyncSet(cd CascadeDispatch, sender string) error {
	k := cd.Key
	ev := Event{Type: EventReconcile, Repo: k.Repo, Kind: KindIssue, Number: k.Number, Sender: sender}
	r, err := e.repoRoles(k.Repo)
	if err != nil || !r.any() {
		held := holdOffOutOfRemit(k.Repo)
		if err != nil {
			held = holdOffScopeUnknown(k.Repo, err)
		}
		e.recordDecision(k, ev, State{}, held)
		return nil
	}
	if !admissibleInRoles(ActionSync, r) {
		e.recordDecision(k, ev, State{}, holdOffActionInadmissible(ActionSync, k.Repo))
		return nil
	}
	owner, name := splitRepo(k.Repo)
	issue, err := e.api.GetIssue(owner, name, k.Number)
	if err != nil {
		return err
	}
	if issue.State != "open" {
		return nil // closed in the meantime: the set resolved itself
	}
	if !hasLabel(issue, LabelSync) {
		// No longer a sync issue: only the decomposer retires sets,
		// and it deletes rather than closes — nothing to dispatch.
		return nil
	}
	blockers, err := e.api.IssueDependencies(owner, name, k.Number)
	if err != nil {
		return err
	}
	members, err := e.syncMembers(blockers)
	if err != nil {
		return err
	}
	w, err := e.store.Load(k)
	if err != nil {
		e.log.Warn("load watermark (continuing without)", "key", k.String(), "error", err)
		w = nil
	}
	st := State{Issue: issue, Blockers: blockers, SyncMembers: members, ReviewLenses: e.cfg.Reviews}
	d := Decide(ev, st, w)
	if !d.Dispatched {
		// Unready, or already dispatched at this set state: silent on
		// the wire, one line in the decision log
		// (§forgejo/decisions/sync-set).
		e.recordDecision(k, ev, st, d)
		return nil
	}
	if d.Action != ActionSync {
		// The candidate decided a different row — a mislabelled
		// grouping issue (sync alongside outcome or ideation) takes
		// the exempt branches first. The sibling cascade guard holds
		// the same line: record the fork and enqueue nothing, so the
		// queue and the decision log cannot disagree — the grouping
		// issue's own sweep row decides its non-sync label
		// (§forgejo/decisions/sync-set).
		forked := holdOff(fmt.Sprintf("sync-set candidate against %s decided %s, not sync; nothing enqueued — the grouping issue's own row decides its other labels", k.String(), d.Action))
		e.recordDecision(k, ev, st, forked)
		return nil
	}
	if err := e.dispatch(ev, st, ActionSync); err != nil {
		return err
	}
	return e.store.Save(k, watermarkAt(w, string(ActionSync), syncSetRevision(issue, members, e.cfg.Reviews)))
}

// revisionFor is the revision a dispatch's watermark records: the PR
// head sha for pull requests, the issue's updated_at for issues, and
// the derived set revision for a landing set — the state of the whole
// set, not of the grouping issue alone
// (§forgejo/webhook/the-pipeline, §forgejo/decisions/sync-set).
func revisionFor(ev Event, st State) string {
	// The set revision describes a landing set, so it takes
	// precedence only on the path that attached members to the
	// object under consideration: the sweep's row for an open issue
	// carrying the sync label. Tying the precedence to that label —
	// not merely to members having been attached — keeps a future
	// re-fetch that shares the field from silently re-typing a PR
	// row's watermark (§forgejo/decisions/sync-set).
	if ev.Kind == KindIssue && st.Issue != nil && hasLabel(st.Issue, LabelSync) && len(st.SyncMembers) > 0 {
		return syncSetRevision(st.Issue, st.SyncMembers, st.ReviewLenses)
	}
	if ev.Kind == KindPR && st.PR != nil {
		return st.PR.HeadSHA
	}
	if st.Issue != nil {
		return issueRevision(st.Issue)
	}
	return ""
}

// splitRepo splits an "owner/repo" full name.
func splitRepo(full string) (owner, name string) {
	i := strings.IndexByte(full, '/')
	if i < 0 {
		return full, ""
	}
	return full[:i], full[i+1:]
}

// issueKey is the watermark key of an issue.
func issueKey(issue *Issue) Key {
	return Key{Repo: issue.Repository, Kind: KindIssue, Number: issue.Number}
}

// pullRequestFromIssue projects a blocker that is a pull request onto
// the slim PR type (the fields the decision function needs).
func pullRequestFromIssue(issue Issue) PullRequest {
	return PullRequest{
		Number:  issue.Number,
		Title:   issue.Title,
		State:   issue.State,
		HTMLURL: issue.HTMLURL,
	}
}

// issueNodes projects issues onto blocker-graph nodes.
func issueNodes(issues []Issue) []IssueNode {
	nodes := make([]IssueNode, len(issues))
	for i := range issues {
		nodes[i].Issue = issues[i]
	}
	return nodes
}

// Handler serves the organisation's all-events webhook at WebhookPath
// (§forgejo/webhook/the-route).
type Handler struct {
	engine *Engine
	secret []byte
	log    *slog.Logger
}

// NewHandler creates the webhook handler for the engine, verifying
// deliveries against the shared secret.
func NewHandler(engine *Engine, secret string, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{engine: engine, secret: []byte(secret), log: logger}
}

// ServeHTTP implements the pipeline (§forgejo/webhook/the-pipeline):
// verify, filter, re-fetch-then-decide (in the engine), acknowledge.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxWebhookBodySize)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	if !verifySignature(h.secret, body, r.Header.Get(HeaderSignature)) {
		h.log.Error("rejected delivery with invalid signature")
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	var p wireEvent
	if err := json.Unmarshal(body, &p); err != nil {
		// A signed but unparseable body is not an event we can act on;
		// acknowledge and move on.
		h.log.Warn("unparseable payload", "error", err)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	ev, ok := mapEvent(r.Header.Get(HeaderEventType), p)
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	h.engine.HandleEvent(ev)
	w.WriteHeader(http.StatusNoContent)
}

// verifySignature reports whether sig is the hex-encoded HMAC-SHA256 of
// body under secret — the format Forgejo sends in X-Forgejo-Signature
// (§forgejo/webhook/the-pipeline).
func verifySignature(secret, body []byte, sig string) bool {
	if len(secret) == 0 || sig == "" {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))
	return subtle.ConstantTimeCompare([]byte(want), []byte(sig)) == 1
}

// wireEvent is the slim payload the engine reads from a Forgejo
// all-events delivery (§forgejo/webhook/event-mapping).
type wireEvent struct {
	Action     string `json:"action"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	Issue struct {
		Number int `json:"number"`
	} `json:"issue"`
	PullRequest struct {
		Number int `json:"number"`
	} `json:"pull_request"`
	Sender struct {
		Login string `json:"login"`
	} `json:"sender"`
}

// mapEvent maps a delivery (the X-Forgejo-Event-Type header and the
// payload) to a decision event, or (Event{}, false) when the delivery is
// cheaply ignored (§forgejo/webhook/event-mapping). A pull_request
// delivery with action closed is mapped to EventPRClosed; the re-fetch
// refines it to EventPRMerged when the PR is merged
// (§forgejo/webhook/re-fetch).
func mapEvent(eventType string, p wireEvent) (Event, bool) {
	repo := p.Repository.FullName
	sender := p.Sender.Login
	issue := func(typ EventType) (Event, bool) {
		if repo == "" || p.Issue.Number == 0 {
			return Event{}, false
		}
		return Event{Type: typ, Repo: repo, Kind: KindIssue, Number: p.Issue.Number, Sender: sender}, true
	}
	pr := func(typ EventType) (Event, bool) {
		if repo == "" || p.PullRequest.Number == 0 {
			return Event{}, false
		}
		return Event{Type: typ, Repo: repo, Kind: KindPR, Number: p.PullRequest.Number, Sender: sender}, true
	}

	switch eventType {
	case "issues":
		switch p.Action {
		case "opened":
			return issue(EventIssueOpened)
		case "edited":
			return issue(EventIssueEdited)
		case "closed":
			return issue(EventIssueClosed)
		}
	case "issue_label":
		if p.Action == "label_updated" || p.Action == "label_cleared" {
			return issue(EventIssueLabelsChanged)
		}
	case "issue_comment":
		if p.Action == "created" {
			return issue(EventIssueCommented)
		}
	case "pull_request":
		switch p.Action {
		case "opened":
			return pr(EventPROpened)
		case "closed":
			return pr(EventPRClosed)
		}
	case "pull_request_sync":
		if p.Action == "synchronized" {
			return pr(EventPRSynced)
		}
	case "pull_request_review_approved", "pull_request_review_rejected", "pull_request_review_comment":
		return pr(EventPRReviewed)
	case "pull_request_comment":
		// General comments on a PR arrive as pull_request_comment,
		// distinct from issue_comment (issues only) and
		// pull_request_review_comment (inline review comments).
		if p.Action == "created" {
			return pr(EventPRCommented)
		}
	}
	return Event{}, false
}
