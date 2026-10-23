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
	"strings"
	"sync"
	"sync/atomic"
	"time"

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

	// Reconciliation sweep scheduling state
	// (§forgejo/reconciliation/scheduling).
	stopCh   chan struct{}
	doneCh   chan struct{}
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
	}
}

// HandleEvent processes one event end to end — scope test, re-fetch,
// decide, dispatch, watermark — under the key's lock, so concurrent
// events for the same key cannot race the watermark
// (§forgejo/webhook/serialisation). A repository holding no maitred
// role is outside the engine's remit: it holds off before the re-fetch
// (§forgejo/webhook/repository-roles). A re-fetch failure holds off: the
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
		// (§forgejo/webhook/repository-roles).
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
		e.recordDecision(key, ev, st, d)
		if d.Dispatched {
			if err := e.dispatch(ev, st, d.Action); err != nil {
				// No watermark: the reconciliation sweep retries.
				e.log.Error("dispatch failed", "key", key.String(), "action", string(d.Action), "error", err)
				return
			}
			if err := e.store.saveLocked(key, Watermark{Action: string(d.Action), Revision: revisionFor(ev, st)}); err != nil {
				e.log.Error("save watermark", "key", key.String(), "error", err)
			}
		}
		for _, cd := range d.Cascade {
			if err := e.dispatchCascade(cd, ev.Sender); err != nil {
				e.log.Error("cascade dispatch failed", "key", key.String(), "cascade", cd.Key.String(), "error", err)
			}
		}
		for _, ch := range d.CascadeHoldOffs {
			// A still-blocked tracker is not silent: the hold-off belongs
			// to the decision log like any other decision, carries the
			// tracker as the graph showed it (so its state and revision are
			// its own), and is qualified by the roles of the repository
			// holding the key, so it never promises an action those roles
			// exclude (§forgejo/decisions/unblock-cascade,
			// §forgejo/observability/the-decision-log,
			// §forgejo/webhook/repository-roles).
			cev := Event{Type: EventIssueOpened, Repo: ch.Key.Repo, Kind: ch.Key.Kind, Number: ch.Key.Number, Sender: ev.Sender}
			e.recordDecision(ch.Key, cev, State{Issue: ch.Issue}, holdOff(e.cascadeHoldOffReason(ch)))
		}
	})
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
		st := State{Issue: issue, DecomposerIdentity: e.cfg.DecomposerIdentity}
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
		st := State{PR: pr}
		switch ev.Type {
		case EventPROpened, EventPRReviewed, EventPRCommented, EventReconcile:
			// The one hop performed wherever the reject-pr test and the
			// merge gate apply (PR opened, PR reviewed, PR commented —
			// which decides as the sweep's PR path — and the sweep's
			// synthetic current-state event): every non-PR issue the
			// PR blocks — relationships recorded on the blocker graph
			// rather than inferred from the PR body — and each issue's
			// blockers, read from that issue's own repository, no
			// recursion. Every non-PR entry is resolved: neither test is
			// left to API ordering. A failure to resolve any connected
			// issue is a re-fetch failure — a hold-off naming the
			// failure, never the path's default (§forgejo/webhook/re-
			// fetch, §forgejo/decisions/actions,
			// §forgejo/decisions/merge-gate).
			if err := e.fetchPRSetState(owner, name, ev.Number, &st); err != nil {
				return ev, State{}, err
			}
			fallthrough
		case EventPRSynced:
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

// connectedIssues fetches the one hop shared by the reject-pr test and
// the merge gate: every non-PR issue the PR blocks — its connected
// issues — each carrying the issues that block it. The hop is read in
// the connected issue's own repository (cross-repo aware), one hop per
// connected issue, no recursion; a failed read fails the whole re-fetch
// (§forgejo/webhook/re-fetch, §forgejo/decisions/actions,
// §forgejo/decisions/merge-gate).
func (e *Engine) connectedIssues(owner, name string, number int) ([]ConnectedIssue, error) {
	blocks, err := e.api.IssueBlocks(owner, name, number)
	if err != nil {
		return nil, err
	}
	var connected []ConnectedIssue
	for i := range blocks {
		if blocks[i].IsPull {
			continue // only issues connect a PR to the graph this way
		}
		co, cn := splitRepo(blocks[i].Repository)
		blockers, err := e.api.IssueDependencies(co, cn, blocks[i].Number)
		if err != nil {
			return nil, err
		}
		connected = append(connected, ConnectedIssue{Issue: blocks[i], Blockers: blockers})
	}
	return connected, nil
}

// fetchPRSetState assembles the state the approval path's sync-set
// hold-off and the merge gate read for a pull request: every non-PR
// issue the PR blocks, each with its blockers, and the landing-set
// membership of each connected issue carrying agentic-sync-merge — the
// open sync issues it blocks. Both paths that can dispatch
// merge-or-wait for a PR build this state here, the event's re-fetch
// and the unblock cascade's PR leg, so the hold-off cannot see
// membership on one and be blind to it on the other: a member of an
// open set never merges on its own, whichever direction the approval
// arrived from (§forgejo/webhook/re-fetch, §forgejo/decisions/sync-set,
// §forgejo/decisions/unblock-cascade).
func (e *Engine) fetchPRSetState(owner, name string, number int, st *State) error {
	connected, err := e.connectedIssues(owner, name, number)
	if err != nil {
		return err
	}
	st.ConnectedIssues = connected
	return e.fetchSyncSets(st)
}

// fetchSyncSets reads the membership edges of every connected issue
// that is a landing-set member (it carries agentic-sync-merge): the
// open sync issues it blocks. The hop is taken only for members, so
// the ordinary PR path costs what it always did
// (§forgejo/webhook/re-fetch, §forgejo/decisions/sync-set).
func (e *Engine) fetchSyncSets(st *State) error {
	for i := range st.ConnectedIssues {
		c := &st.ConnectedIssues[i]
		if !hasLabel(&c.Issue, LabelAgenticSyncMerge) {
			continue
		}
		owner, name := splitRepo(c.Issue.Repository)
		blocks, err := e.api.IssueBlocks(owner, name, c.Issue.Number)
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
	}
	return nil
}

// syncMembers projects the blocking issues of a sync issue onto the
// members of a landing set, attaching to every open member the state
// of its delivering PR — mergeability and reviews — the sync
// predicate reads. A member's PR is seen through the member's own
// dependency rows (the PR is a blocker of the issue it delivers), the
// same projection the unblock cascade uses; where a member has
// several open PRs the first is read (§forgejo/decisions/sync-set).
func (e *Engine) syncMembers(blockers []Issue) ([]SyncMember, error) {
	var members []SyncMember
	for i := range blockers {
		b := blockers[i]
		if b.IsPull {
			continue // a set groups issues; a direct PR member is a graph error
		}
		m := SyncMember{Issue: b}
		if b.State == "open" {
			owner, name := splitRepo(b.Repository)
			deps, err := e.api.IssueDependencies(owner, name, b.Number)
			if err != nil {
				return nil, err
			}
			for _, p := range openPullBlockers(issueNodes(deps)) {
				po, pn := splitRepo(p.Repository)
				pr, err := e.api.GetPullRequest(po, pn, p.Number)
				if err != nil {
					return nil, err
				}
				if !prOpen(pr) {
					continue
				}
				reviews, err := e.api.ListPullRequestReviews(po, pn, p.Number)
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

// dispatch fills the action's canned prompt and enqueues the task, with
// the dedup key of the dispatch identity — the key's repo, kind, and
// number, the action, and the revision — so a queue system with
// de-duplication (hotelier's dedup_key) squelches a re-dispatch of the
// same (action, revision) while the first task is still pending
// (§forgejo/webhook/the-pipeline). The key carries the repo verbatim
// (slash retained — it is opaque to the queue system), so two distinct
// repos never produce the same key. The task ID, by contrast, keeps its
// "-"-replaced repo: there the ambiguity is only a traceability concern.
func (e *Engine) dispatch(ev Event, st State, action Action) error {
	pc, err := e.cfg.PromptFor(action)
	if err != nil {
		return err
	}
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
	rev := revisionFor(ev, st)
	task := &queue.Task{
		ID:       fmt.Sprintf("forgejoeng-%s-%s-%d-%s-%d", strings.ReplaceAll(ev.Repo, "/", "-"), ev.Kind, ev.Number, action, time.Now().UnixNano()),
		Prompt:   prompt,
		Persona:  pc.Persona,
		Timeout:  int(pc.Timeout.Seconds()),
		DedupKey: fmt.Sprintf("forgejoeng-%s-%s-%d-%s-%s", ev.Repo, ev.Kind, ev.Number, action, rev),
	}
	if err := e.queue.AddTask(task); err != nil {
		return fmt.Errorf("enqueue %s for %s: %w", action, ev.KeyOf(), err)
	}
	e.log.Info("dispatched", "key", ev.KeyOf().String(), "task", task.ID)
	return nil
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
	if err := e.dispatch(ev, State{Issue: issue}, cd.Action); err != nil {
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
// still sees an open blocker on re-fetch, and a PR whose connected
// issue is a member of an open landing set all dispatch nothing, each
// recorded in the decision log with the sweep as the backstop
// (§forgejo/decisions/merge-gate, §forgejo/decisions/sync-set). The PR
// key's own watermark applies before dispatching; the set's own
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
	st := State{PR: pr, Reviews: reviews}
	if err := e.fetchPRSetState(owner, name, k.Number, &st); err != nil {
		return err
	}
	w, err := e.store.Load(k)
	if err != nil {
		e.log.Warn("load watermark (continuing without)", "key", k.String(), "error", err)
		w = nil
	}
	// Insurance against the re-fetched state, through the same pure
	// decision and the same gate the event path takes.
	d := Decide(ev, st, w)
	if d.Action != ActionMergeOrWait {
		// A fork of merge-or-wait is recorded like the sibling cascade
		// dispatchers record theirs: this path has no backstop of its
		// own and the decision log is the engine's only observability,
		// so a member held off because it belongs to a landing set must
		// leave a line naming why, not only a queue that stayed empty
		// (§forgejo/decisions/unblock-cascade,
		// §forgejo/observability/the-decision-log).
		e.recordDecision(k, ev, st, d)
		return nil
	}
	if err := e.dispatch(ev, State{PR: pr}, ActionMergeOrWait); err != nil {
		return err
	}
	return e.store.Save(k, Watermark{Action: string(ActionMergeOrWait), Revision: pr.HeadSHA})
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
	st := State{Issue: issue, Blockers: blockers, SyncMembers: members, DecomposerIdentity: e.cfg.DecomposerIdentity}
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
	return e.store.Save(k, Watermark{Action: string(ActionSync), Revision: syncSetRevision(members)})
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
		return syncSetRevision(st.SyncMembers)
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
