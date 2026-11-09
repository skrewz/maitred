package forgejo

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Action is a named action the decision function can dispatch. The
// action names form the enum the engine config maps to canned prompts
// (§forgejo/decisions/actions).
type Action string

// The actions the decision function dispatches
// (§forgejo/decisions/actions).
const (
	ActionImplement Action = "implement"
	ActionReassess  Action = "reassess"
	// ActionReview is the review fan-out's dispatch name — one
	// dispatch per configured lens (§forgejo/decisions/review-lenses).
	// It is no longer a configured action: the config lists lenses
	// under `reviews:` instead of the retired `review` and `re-review`
	// singletons (§forgejo/config/review-lenses). The role tables and
	// the decision log still read the fan-out as `review`.
	ActionReview      Action = "review"
	ActionFixFeedback Action = "fix-feedback"
	ActionMergeOrWait Action = "merge-or-wait"
	ActionRebase      Action = "rebase"
	// ActionDecompose and ActionWrapUp are the actions the sweep's
	// watermark-exempt rows dispatch, where the state is the
	// watermark. On the event path decompose has rows of its own,
	// under the ordinary revision-keyed guard — the exemption
	// belongs to the sweep's rows, not to the action
	// (§forgejo/reconciliation/watermark-exemptions).
	ActionDecompose Action = "decompose"
	ActionWrapUp    Action = "wrap-up"
	// ActionSync is the third meta-action on outcomes repos: it
	// dispatches against a sync issue — a grouping issue whose
	// members must land together — once every member is closed or
	// stands approved at head, mergeable, and opted in with
	// agentic-sync-merge (§forgejo/decisions/sync-set). Its
	// watermark is a revision derived from the whole set.
	ActionSync Action = "sync"
	// ActionTeardownForest deletes the forest an open ideation root
	// commissioned, on the human's order through the `teardown-forest`
	// label (§forgejo/decisions/forest-teardown). Its trigger is a state
	// relation like decompose's, so the sweep's row is watermark-exempt
	// while the event path keeps the revision-keyed guard
	// (§forgejo/reconciliation/watermark-exemptions).
	ActionTeardownForest Action = "teardown-forest"
)

// The Forgejo review states the decision function reacts to
// (§forgejo/client/operations).
const (
	ReviewApproved         = "APPROVED"
	ReviewChangesRequested = "REQUEST_CHANGES"
	ReviewComment          = "COMMENT"
	// ReviewDismissed is the state a review listing reports for a
	// review that stands dismissed — a voided verdict, never one the
	// lens gates count (§forgejo/decisions/review-lenses).
	ReviewDismissed = "DISMISSED"
)

// reviewIsDismissed reports whether a listed review stands dismissed,
// whichever way the listing says so — the boolean flag or the
// DISMISSED state (§forgejo/client/operations, §forgejo/decisions/review-lenses).
func reviewIsDismissed(r *Review) bool {
	return r.Dismissed || r.Event == ReviewDismissed
}

// LabelHumanTask marks a ticket claimed by a human: an issue carrying
// it is never an implement candidate anywhere in the engine
// (§forgejo/decisions/transition-table).
const LabelHumanTask = "human-task"

// neverWorkItemLabel returns the issue label that denies the
// implement and reassess actions, or "" when the issue may be an
// implement or reassess candidate. The list is a permanent taxonomy:
// each name says what the issue *is* — a root, a tracker, a grouping
// issue, a human claim — not what it is *not yet*. A transient gate
// is recognised by transientHoldOffLabel instead
// (§forgejo/decisions/transition-table,
// §forgejo/reconciliation/watermark-exemptions).
func neverWorkItemLabel(labels []string) string {
	issue := &Issue{Labels: labels}
	if label := reservedLabel(issue); label != "" {
		return label
	}
	if hasLabel(issue, LabelHumanTask) {
		return LabelHumanTask
	}
	return ""
}

// transientHoldOffLabel returns the label whose presence holds the
// issue off from implement and reassess *until the label is removed*,
// as opposed to neverWorkItemLabel's permanent taxonomy of kind
// claims: the decomposer opens the gate and closes it, and once the
// label is gone the issue is an ordinary work item on every path
// (§forgejo/decisions/transition-table, s-git/maitred#125). Like
// hasLabel, names match case-insensitively.
func transientHoldOffLabel(labels []string) string {
	issue := &Issue{Labels: labels}
	if hasLabel(issue, LabelForestSeeding) {
		return LabelForestSeeding
	}
	return ""
}

// EventType identifies a Forgejo notification the engine reacts to
// (§forgejo/decisions/inputs).
type EventType string

// The event types the decision function handles
// (§forgejo/decisions/transition-table).
const (
	EventIssueOpened        EventType = "issue_opened"
	EventIssueEdited        EventType = "issue_edited"
	EventIssueLabelsChanged EventType = "issue_labels_changed"
	EventIssueCommented     EventType = "issue_commented"
	EventIssueClosed        EventType = "issue_closed"
	EventPROpened           EventType = "pr_opened"
	EventPRSynced           EventType = "pr_synced"
	EventPRReviewed         EventType = "pr_reviewed"
	EventPRMerged           EventType = "pr_merged"
	EventPRClosed           EventType = "pr_closed"
	// EventPRCommented is a general comment on a pull request — the
	// PR-side analogue of EventIssueCommented. It decides as the
	// reconciliation sweep's PR path: the re-fetched PR state decides
	// the next action (§forgejo/decisions/transition-table).
	EventPRCommented EventType = "pr_commented"
	// EventReconcile is the synthetic current-state event the
	// reconciliation sweep feeds through the same decision function
	// (§forgejo/reconciliation): for an issue it decides like
	// EventIssueOpened unless a watermark-exempt row applies
	// (§forgejo/reconciliation/watermark-exemptions), for a pull
	// request like EventPRSynced — except when the last activity is a
	// review: changes-requested or comment dispatches fix-feedback,
	// approved dispatches merge-or-wait
	// (§forgejo/decisions/transition-table).
	EventReconcile EventType = "reconcile"
)

// Event is a Forgejo notification: what happened, to which object, and
// who caused it (§forgejo/decisions/inputs). The event is a
// notification only; the re-fetched state is the source of truth.
type Event struct {
	Type EventType
	// Repo is the "owner/repo" of the affected object.
	Repo string
	// Kind is KindIssue or KindPR.
	Kind Kind
	// Number is the issue or pull request number.
	Number int
	// Sender is the login of the user who caused the event.
	Sender string
}

// KeyOf returns the watermark key for the event's object
// (§forgejo/state/storage).
func (e Event) KeyOf() Key {
	return Key{Repo: e.Repo, Kind: e.Kind, Number: e.Number}
}

// State is the re-fetched Forgejo state for the event's object, plus
// the graph data the unblock cascade needs
// (§forgejo/decisions/inputs). The engine re-fetches it from Forgejo
// (the source of truth) before calling Decide.
type State struct {
	// Issue is the re-fetched issue, for issue events.
	Issue *Issue
	// PR is the re-fetched pull request, for PR events.
	PR *PullRequest
	// Reviews are the PR's reviews, for PR events.
	Reviews []Review
	// Blockers are the issues that block State.Issue (re-fetched via
	// issue-dependencies), each with its own state.
	Blockers []Issue
	// ConnectedPRs are the open pull requests connected to State.Issue.
	ConnectedPRs []PullRequest
	// ConnectedIssues are the non-PR issues a pull request blocks, each
	// with its own blockers: the input to the merge gate
	// (§forgejo/decisions/merge-gate) and to the landing-set read
	// (§forgejo/decisions/sync-set). Read for PR reviewed, PR commented,
	// the sweep's synthetic PR event, and the unblock cascade's PR leg —
	// every path that can dispatch a merge; empty when the PR's opening
	// recorded no dependency.
	ConnectedIssues []ConnectedIssue
	// SyncMembers are the blocking issues of the sync issue in
	// State.Issue, each with the state of its delivering PR: the sync
	// predicate's input, fetched when the issue under consideration
	// carries the sync label (§forgejo/decisions/sync-set).
	SyncMembers []SyncMember
	// SyncSets are the open sync issues the pull request under
	// consideration blocks **directly**: the shape whose predicate side
	// reads the PR as a member whose delivery is itself. Read on the
	// paths that assemble PR state for the merge gate, from the
	// grouping issue's own dependency rows — the edge is already in the
	// graph the assembly holds, so the read costs nothing
	// (§forgejo/decisions/sync-set, §forgejo/webhook/re-fetch).
	SyncSets []Issue
	// ImplementerIdentity is the configured login of the identity that
	// implements work and fixes review feedback
	// (§forgejo/config/implementer-identity). It is a declared
	// configuration input the engine passes verbatim from its config,
	// not fetched state; when empty the fix-feedback ownership gate is
	// inert (§forgejo/decisions/inputs, §forgejo/decisions/loop-prevention,
	// s-git/maitred#143).
	ImplementerIdentity string
	// DecomposerIdentity is the configured login of the identity that
	// composes forests (§forgejo/config/decomposer-identity). Like
	// ImplementerIdentity it is a declared configuration input the
	// engine passes verbatim from its config, not fetched state. It is
	// read by one row alone: a comment on an open `ideation` root is
	// held off only when its author is this identity, so the
	// decomposer's own clarifying question does not re-trigger itself
	// while any other author's comment re-cures the root
	// (§forgejo/decisions/loop-prevention, s-git/maitred#147). When
	// empty the guard is inert, like the ownership gate above.
	DecomposerIdentity string
	// ReviewLenses is the configured ordered list of review lenses
	// (§forgejo/config/review-lenses). Like ImplementerIdentity it is
	// a declared configuration input the engine passes verbatim from
	// its config, not fetched state. The review fan-out dispatches
	// exactly the lenses with no undismissed approval at the current
	// head, and the review-following gates (fix-feedback,
	// merge-or-wait, the sync-set member test) are decided per lens
	// (§forgejo/decisions/review-lenses).
	ReviewLenses []ReviewLens
	// CascadeRoot is the closed issue the unblock cascade walks from,
	// for close events.
	CascadeRoot *IssueNode
	// Blocked is what the issue under consideration blocks: its
	// downstream edges. Fetched only for a root under a teardown order,
	// where it is the emptiness test — an issue blocking nothing has no
	// forest to delete (§forgejo/decisions/forest-teardown).
	Blocked []Issue
}

// ConnectedIssue is one non-PR issue a pull request blocks, with the
// issues that block it: the input to the merge gate. The relationship
// is recorded on the blocker graph, never inferred from the PR body
// (§forgejo/webhook/re-fetch, §forgejo/decisions/merge-gate).
type ConnectedIssue struct {
	// Issue is the connected issue; its Repository may differ from
	// the PR's (cross-repo relationships).
	Issue Issue
	// Blockers are the issues that block Issue: one hop, no recursion,
	// each with its own state and repository.
	Blockers []Issue
	// SyncSets are the open sync issues this connected issue is a
	// member of — its outbound edges filtered to the grouping issues
	// carrying the sync label. Read only when the connected issue
	// carries agentic-sync-merge: the membership edges the approval
	// path emits the sync-set candidate against
	// (§forgejo/decisions/sync-set).
	SyncSets []Issue
}

// SyncMember is one member of a landing set: the member issue and
// the state of the PR that delivers it — the input to the sync
// predicate, which asks of every open member that its PR be
// mergeable and approved at its head while the issue carries
// agentic-sync-merge (§forgejo/decisions/sync-set). A member with
// several open PRs is read through its first one, the same
// projection the unblock cascade uses.
type SyncMember struct {
	Issue   Issue
	PR      *PullRequest
	Reviews []Review
}

// IssueNode is one issue in the blocker graph, as re-fetched for the
// unblock cascade (§forgejo/decisions/unblock-cascade).
type IssueNode struct {
	// Issue is the node's issue; its Repository may differ from the
	// root's (cross-repo blockers).
	Issue Issue
	// Blocks are the issues this issue blocks.
	Blocks []IssueNode
	// Blockers are the issues that block this issue.
	Blockers []IssueNode
}

// CascadeDispatch is one emission of the unblock cascade: the key of
// a newly unblocked issue and the action its state calls for —
// implement, or wrap-up when the issue carries the outcome label: a
// tracker whose last blocker just closed is a tracker whose work is
// finished (§forgejo/decisions/unblock-cascade).
type CascadeDispatch struct {
	Key    Key
	Action Action
}

// CascadeHoldOff is one cascade outcome that dispatches nothing but
// belongs in the decision log: an open tracker the walk finds still
// blocked, its reason naming which blockers remain open
// (§forgejo/decisions/unblock-cascade). It carries the tracker as the
// graph showed it, so the record states the state and revision it was
// made at instead of blanking them (§forgejo/observability/the-decision-log,
// §forgejo/observability/the-dashboard-view).
type CascadeHoldOff struct {
	Key    Key
	Issue  *Issue
	Reason string
}

// Decision is the outcome of Decide: a dispatch (a named action for the
// event's own object) or a hold-off, always with a human-readable
// reason, plus the unblock-cascade dispatches for close events
// (§forgejo/decisions/the-decision-function).
type Decision struct {
	// Dispatched reports whether an action is dispatched for the
	// event's own object.
	Dispatched bool
	// Action is the named action to dispatch (meaningful when
	// Dispatched).
	Action Action
	// Reason is a human-readable explanation of the decision; it is
	// logged with every decision.
	Reason string
	// Cascade lists the objects the unblock cascade now unblocks (close
	// events only), each with the action its state calls for: newly
	// unblocked issues — implement, or wrap-up for a newly unblocked
	// outcome tracker — and the connected open PRs of issues whose
	// remaining open blockers are only those PRs, each warranting a
	// dispatch of ActionMergeOrWait
	// (§forgejo/decisions/unblock-cascade). The engine applies each
	// key's own watermark before dispatching, so the same (action,
	// revision) is not re-dispatched.
	Cascade []CascadeDispatch
	// CascadeHoldOffs lists the cascade's non-silent hold-offs (close
	// events only): an open tracker the walk found still blocked, its
	// reason naming the open blockers, recorded in the decision log so
	// the log says why wrap-up did not fire
	// (§forgejo/decisions/unblock-cascade).
	CascadeHoldOffs []CascadeHoldOff
	// Lenses lists the review fan-out's dispatches for the event's own
	// object, in config order: one entry per lens whose approval is
	// outstanding at the current head (§forgejo/decisions/review-lenses).
	// Meaningful when Dispatched with Action == ActionReview; the
	// cascade shape (Cascade) is the precedent for a multi-emission
	// decision.
	Lenses []LensDispatch
}

// LensDispatch is one emission of the review fan-out: the identity of
// the lens to dispatch and the reason its approval is outstanding at
// the head (§forgejo/decisions/review-lenses).
type LensDispatch struct {
	// Lens is the configured lens identity to dispatch.
	Lens string
	// Reason is a human-readable explanation for this lens's dispatch.
	Reason string
}

// HoldOff reports whether the decision dispatches nothing for the
// event's own object.
func (d Decision) HoldOff() bool {
	return !d.Dispatched
}

// Decide maps an event, the re-fetched state, and the watermark to a
// decision. It is pure: no I/O — the inputs are the re-fetched state
// (Forgejo is the source of truth) and the watermark
// (§forgejo/decisions/the-decision-function).
func Decide(e Event, s State, w *Watermark) Decision {
	switch e.Type {
	case EventIssueOpened:
		return decideIssueOpened(e, s, w)
	case EventReconcile:
		if e.Kind == KindPR {
			return decidePRReconcile(e, s, w)
		}
		return decideIssueReconcile(e, s, w)
	case EventIssueEdited, EventIssueLabelsChanged, EventIssueCommented:
		return decideIssueActivity(e, s, w)
	case EventIssueClosed:
		return decideIssueClosed(s)
	case EventPROpened:
		return decidePROpened(e, s, w)
	case EventPRSynced:
		return decidePRSynced(e, s, w)
	case EventPRReviewed:
		return decidePRReviewed(e, s, w)
	case EventPRMerged:
		return decidePRMerged(s)
	case EventPRClosed:
		return holdOff("PR closed without merging; nothing to dispatch")
	case EventPRCommented:
		// A comment on a PR is the PR-side analogue of a comment on an
		// issue: something was said, re-evaluate what happens next. The
		// PR equivalent of reassess is the reconcile logic — the
		// re-fetched PR state decides the next action
		// (§forgejo/decisions/transition-table).
		if e.Kind == KindPR {
			return decidePRReconcile(e, s, w)
		}
		return holdOff(fmt.Sprintf("PR-commented event on kind %q", e.Kind))
	default:
		return holdOff(fmt.Sprintf("unhandled event type %q", e.Type))
	}
}

// decideIssueReconcile handles the reconciliation sweep's synthetic
// current-state event for an issue. The watermark-exempt rows come
// first, and they are the exemption: unlike every other path, they
// decide without consulting the watermark, so an open ideation root
// dispatches decompose, an open outcome tracker of a commissioned forest
// dispatches decompose too, and an open, fully unblocked outcome tracker
// dispatches wrap-up, revision notwithstanding — state is the watermark
// (§forgejo/reconciliation/watermark-exemptions). These rows are the exempt
// set: the exemption is scoped to the sweep, and an exempt action reached
// from an event keeps the revision-keyed guard like any other action. A
// tracker holds off only when there is nothing to curate — not yet
// commissioned, so its root's row is the pulse, or gated by no root at
// all — and the reason names that fact, so the decision log reads
// "state calls for no action", never "already dispatched at this
// revision". An issue carrying neither reserved label decides as issue
// opened (§forgejo/decisions/transition-table).
func decideIssueReconcile(e Event, s State, w *Watermark) Decision {
	issue := s.Issue
	if issue == nil || issue.State != "open" {
		return decideIssueOpened(e, s, w) // the same hold-offs as the event path
	}
	if hasLabel(issue, LabelSync) {
		// The sync row is tested before the other kind rows. `sync`
		// is a kind claim like `ideation` and `outcome`, and the sweep
		// is a landing set's only backstop: letting label enumeration
		// order decide which of two kind claims wins would run the
		// wrong prompt against the grouping issue — wrap-up against a
		// set that never lands, or the watermark-exempt decompose
		// every interval. The conflict is named instead, the shape
		// already chosen for a member carrying both merge opt-ins
		// (§forgejo/decisions/sync-set).
		if other := otherKindLabel(issue); other != "" {
			return holdOff(fmt.Sprintf("issue %d is an open %s issue that also carries %s; two reserved kind labels claim the same issue and only the decomposer may retire one; nothing dispatched", issue.Number, LabelSync, other))
		}
		// The sweep is the backstop for landing sets: a set that
		// changed through a delivery-less move (an edge edit, a
		// human closing a member) is found here, evaluated under
		// the set-revision guard like every other sync row
		// (§forgejo/decisions/sync-set).
		return considerSyncIssue(issue, s.SyncMembers, s.ReviewLenses, w)
	}
	if hasLabel(issue, LabelIdeation) {
		if hasLabel(issue, LabelTeardownForest) {
			// The teardown row wins over the root's row: a forest being
			// deleted is never re-commissioned in the same breath
			// (§forgejo/decisions/forest-teardown). The forest still
			// standing is the state that calls for the deletion — which
			// is also what ends the re-firing: once the root blocks
			// nothing, the sweep stops asking.
			if len(s.Blocked) == 0 {
				return holdOff(fmt.Sprintf("issue %d is an open %s root carrying %s but blocking no issue: the state calls for no teardown", issue.Number, LabelIdeation, LabelTeardownForest))
			}
			return dispatch(ActionTeardownForest, fmt.Sprintf("issue %d is an open %s root carrying %s and still blocking %d issue(s); the state calls for teardown regardless of revision %s", issue.Number, LabelIdeation, LabelTeardownForest, len(s.Blocked), issueRevision(issue)))
		}
		// A root is decomposed even when a tracker's wrap-up would
		// also apply: decompose wins over wrap-up
		// (§forgejo/reconciliation/watermark-exemptions).
		return dispatch(ActionDecompose, fmt.Sprintf("issue %d is an open %s issue; the state calls for decompose regardless of revision %s", issue.Number, LabelIdeation, issueRevision(issue)))
	}
	if hasLabel(issue, LabelOutcome) {
		if open := openBlockers(s.Blockers); len(open) > 0 {
			// A commissioned forest stays under curation: closing the root removes
			// it from the sweep's enumeration of open issues, so the tracker's
			// own row carries the curation until the forest is finished
			// (§forgejo/reconciliation/watermark-exemptions).
			gate := ideationGate(s.Blockers)
			if gate == nil {
				return holdOff(fmt.Sprintf("issue %d is an open %s tracker blocked by %d open issue(s) and gated by no %s issue; state calls for no action", issue.Number, LabelOutcome, len(open), LabelIdeation))
			}
			if gate.State == "open" {
				return holdOff(fmt.Sprintf("issue %d is an open %s tracker whose %s root %d is still open; its own row carries the curation", issue.Number, LabelOutcome, LabelIdeation, gate.Number))
			}
			return dispatch(ActionDecompose, fmt.Sprintf("issue %d is an open %s tracker commissioned by %s %d and still blocked by %d open issue(s); the state calls for decompose regardless of revision %s", issue.Number, LabelOutcome, LabelIdeation, gate.Number, len(open), issueRevision(issue)))
		}
		return dispatch(ActionWrapUp, fmt.Sprintf("issue %d is an open, fully unblocked %s tracker; the state calls for wrap-up regardless of revision %s", issue.Number, LabelOutcome, issueRevision(issue)))
	}
	return decideIssueOpened(e, s, w)
}

// otherKindLabel returns the reserved kind label an issue carries
// besides `sync` — `ideation` first, since decompose wins over
// wrap-up wherever both apply — or "" when the issue claims exactly
// one kind (§forgejo/decisions/sync-set).
func otherKindLabel(issue *Issue) string {
	for _, label := range []string{LabelIdeation, LabelOutcome} {
		if hasLabel(issue, label) {
			return label
		}
	}
	return ""
}

// decideIssueOpened handles an issue that was opened (or the
// reconciliation sweep's synthetic current-state event): an open issue
// carrying the ideation label is a root and dispatches decompose — the
// label, not the repository and not the title, is what makes a root
// (§forgejo/decisions/transition-table); otherwise dispatch implement
// if the re-fetched issue is open, unblocked, and has no connected PR.
// Authorship is not a gate: what gates a forest member is the
// forest-seeding hold-off, and once the label is gone the member is
// an ordinary work item on every path — the sweep included, which is
// the cascade's backstop (s-git/maitred#135,
// §forgejo/decisions/transition-table).
func decideIssueOpened(e Event, s State, w *Watermark) Decision {
	issue := s.Issue
	if issue == nil {
		return holdOff("no issue state re-fetched")
	}
	if issue.State != "open" {
		return holdOff(fmt.Sprintf("issue %d is %q, not open", issue.Number, issue.State))
	}
	if hasLabel(issue, LabelIdeation) {
		return decideDecompose(e, issue, s, w)
	}
	if label := neverWorkItemLabel(issue.Labels); label != "" {
		return holdOffNeverImplement(issue.Number, label)
	}
	if label := transientHoldOffLabel(issue.Labels); label != "" {
		return holdOffForestSeeding(issue.Number, label)
	}
	if hasLabel(issue, LabelTeardownForest) {
		return holdOffTeardownWithoutRoot(issue.Number)
	}
	if open := openBlockers(s.Blockers); len(open) > 0 {
		return holdOff(fmt.Sprintf("issue %d is blocked by %d open issue(s)", issue.Number, len(open)))
	}
	if len(s.ConnectedPRs) > 0 {
		return holdOff(fmt.Sprintf("issue %d has %d connected open PR(s)", issue.Number, len(s.ConnectedPRs)))
	}
	rev := issueRevision(issue)
	if alreadyDispatched(w, ActionImplement, rev) {
		return holdOff(fmt.Sprintf("implement already dispatched at revision %s", rev))
	}
	return dispatch(ActionImplement, fmt.Sprintf("issue %d opened: open, unblocked, no connected PR", issue.Number))
}

// decideIssueActivity handles an edited issue, a label change, or a
// feedback comment: on an open issue carrying the ideation label the
// edit or label change is the revision of intent the decomposer
// re-cures against and dispatches decompose — attaching the label is
// the first trigger — and so is a comment, by the same guard: the
// comment bumps the root's updated_at, so it reads as a new revision
// (s-git/maitred#147). The row is author-scoped, not blanket: a comment
// authored by the configured decomposer identity holds off naming the
// authorship, so the decomposer's own question never re-triggers it
// (§forgejo/decisions/loop-prevention); otherwise dispatch reassess if
// the re-fetched issue is open and has no open blocker; activity on a
// blocked issue has nothing to reassess — the work is held by the
// blocker, so the decision holds off naming the open blockers.
func decideIssueActivity(e Event, s State, w *Watermark) Decision {
	issue := s.Issue
	if issue == nil {
		return holdOff("no issue state re-fetched")
	}
	if issue.State != "open" {
		return holdOff(fmt.Sprintf("issue %d is %q, not open", issue.Number, issue.State))
	}
	if hasLabel(issue, LabelIdeation) {
		// The comment row is guarded by authorship, not blanket: the
		// decomposer's own comment on its root holds off silently,
		// every other author's re-cures the forest under the ordinary
		// revision-keyed watermark guard (§forgejo/decisions/loop-prevention,
		// s-git/maitred#147). The fall-through is the root row in full:
		// a root that also carries a teardown order takes the teardown
		// row, exactly as an edit does — the order is the label, not
		// the event (§forgejo/decisions/forest-teardown).
		if e.Type == EventIssueCommented && s.DecomposerIdentity != "" && e.Sender == s.DecomposerIdentity {
			// The match is exact — no case folding, no substring —
			// the same rule the fix-feedback ownership gate applies
			// to the implementer identity
			// (§forgejo/config/decomposer-identity).
			return holdOff(fmt.Sprintf("issue %d is an open %s root; comment authored by the decomposer identity %s: not re-curing on the decomposer's own words", issue.Number, LabelIdeation, e.Sender))
		}
		return decideDecompose(e, issue, s, w)
	}
	if label := neverWorkItemLabel(issue.Labels); label != "" {
		return holdOffNeverReassess(issue.Number, label)
	}
	if label := transientHoldOffLabel(issue.Labels); label != "" {
		return holdOffForestSeeding(issue.Number, label)
	}
	if hasLabel(issue, LabelTeardownForest) {
		return holdOffTeardownWithoutRoot(issue.Number)
	}
	if open := openBlockers(s.Blockers); len(open) > 0 {
		return holdOff(fmt.Sprintf("issue %d is blocked by %s; not reassessing", issue.Number, blockerNames(open)))
	}
	rev := issueRevision(issue)
	if alreadyDispatched(w, ActionReassess, rev) {
		return holdOff(fmt.Sprintf("reassess already dispatched at revision %s", rev))
	}
	return dispatch(ActionReassess, fmt.Sprintf("issue %d: %s", issue.Number, e.Type))
}

// decideDecompose is the event path's root row: an open ideation issue
// dispatches decompose under the ordinary revision-keyed watermark
// guard — the watermark exemption is a property of the sweep's row,
// not of the action (§forgejo/reconciliation/watermark-exemptions). A
// root is decomposed whatever blocks it; a closed root holds off
// upstream, because closing it commissions the forest and the unblock
// cascade dispatches the forest members
// (§forgejo/decisions/transition-table).
func decideDecompose(e Event, issue *Issue, s State, w *Watermark) Decision {
	if hasLabel(issue, LabelTeardownForest) {
		return decideTeardown(issue, s, w)
	}
	rev := issueRevision(issue)
	if alreadyDispatched(w, ActionDecompose, rev) {
		return holdOff(fmt.Sprintf("decompose already dispatched at revision %s", rev))
	}
	reason := fmt.Sprintf("issue %d is an open %s root: the label makes the root", issue.Number, LabelIdeation)
	if e.Type == EventIssueCommented {
		// The author guard on the comment row is this row's whole loop
		// prevention, so the dispatch names the comment's author: when
		// the declared identity does not match the account that
		// actually posts — a stale declaration, a renamed persona —
		// the fail-open loop is legible in the decision log as a
		// stream of decompose dispatches authored by that account
		// (§forgejo/config/decomposer-identity,
		// §forgejo/decisions/loop-prevention, s-git/maitred#147).
		reason = fmt.Sprintf("issue %d is an open %s root: a comment by %s", issue.Number, LabelIdeation, e.Sender)
	}
	return dispatch(ActionDecompose, reason)
}

// decideTeardown is the event path's teardown row: an open root carrying
// the teardown label dispatches teardown-forest under the ordinary
// revision-keyed watermark guard, the exemption being a property of the
// sweep's row (§forgejo/reconciliation/watermark-exemptions,
// §forgejo/decisions/forest-teardown).
func decideTeardown(issue *Issue, s State, w *Watermark) Decision {
	// The engine can see whether there is anything to delete, so it does
	// not ask for an agent to find out: an empty forest dispatches
	// nothing, on the event path or the sweep, and the label standing
	// over it decides no work (§forgejo/decisions/forest-teardown).
	if len(s.Blocked) == 0 {
		return holdOff(fmt.Sprintf("issue %d carries %s but blocks no issue: its forest is empty, so there is nothing to delete", issue.Number, LabelTeardownForest))
	}
	rev := issueRevision(issue)
	if alreadyDispatched(w, ActionTeardownForest, rev) {
		return holdOff(fmt.Sprintf("teardown-forest already dispatched at revision %s", rev))
	}
	return dispatch(ActionTeardownForest, fmt.Sprintf("issue %d is an open %s root carrying %s: the forest it commissioned is to be deleted", issue.Number, LabelIdeation, LabelTeardownForest))
}

// holdOffTeardownWithoutRoot names the misapplied order: the label
// modifies the root row, so on an issue that is no root it retires
// nothing and must wake no work either. Kind labels are tested first:
// `outcome` and `human-task` say what an issue *is*, and this says what
// a label *modifies*, which is the weaker claim
// (§forgejo/decisions/forest-teardown, §forgejo/decisions/actions).
func holdOffTeardownWithoutRoot(number int) Decision {
	return holdOff(fmt.Sprintf("issue %d carries %s without %s: the label modifies the root row and retires no work item", number, LabelTeardownForest, LabelIdeation))
}

// decideIssueClosed runs the unblock cascade from the closed issue
// (§forgejo/decisions/unblock-cascade).
func decideIssueClosed(s State) Decision {
	if s.Issue == nil || s.Issue.State != "closed" {
		return holdOff("issue is not closed on re-fetch")
	}
	d := holdOff(fmt.Sprintf("issue %d closed; unblock cascade", s.Issue.Number))
	d.Cascade, d.CascadeHoldOffs = unblockCascade(s.CascadeRoot)
	return d
}

// decidePROpened handles an opened PR: dispatch review, or rebase when
// the re-fetched PR is not mergeable
// (§forgejo/decisions/transition-table).
func decidePROpened(e Event, s State, w *Watermark) Decision {
	pr := s.PR
	if pr == nil {
		return holdOff("no PR state re-fetched")
	}
	if !prOpen(pr) {
		return holdOff(fmt.Sprintf("PR %d is not open", pr.Number))
	}
	// Whether the PR's work is commissioned decides nothing here: the
	// open row dispatches review or rebase on the PR's own state, and a
	// forest whose gate has not opened is held off upstream, by the
	// seeding window (§forgejo/decisions/commissioning, s-git/maitred#151).
	if !pr.Mergeable {
		if alreadyDispatched(w, ActionRebase, pr.HeadSHA) {
			return holdOff(fmt.Sprintf("rebase already dispatched at revision %s", pr.HeadSHA))
		}
		return dispatch(ActionRebase, fmt.Sprintf("PR %d opened with merge conflicts", pr.Number))
	}
	return decideReviewFanOut(e, pr, s, w, false)
}

// decidePRSynced handles new commits on a PR (or the reconciliation
// sweep's synthetic current-state event): dispatch rebase when the
// re-fetched PR is not mergeable, otherwise the review fan-out — the
// lenses with no undismissed approving review at the current head
// (§forgejo/decisions/review-lenses). A push invalidates every
// approval (stale-approval dismissal), so a sync re-fires the whole
// chain; a lens's own push does not re-fire that lens, while the
// other lenses' dispatches still go out
// (§forgejo/decisions/loop-prevention). A PR carrying the
// trigger-review label is the explicit request for an agentic review:
// the fan-out is asked for at the current revision whatever the
// per-lens sender guard would suppress — the label is a human
// request, not a lens's own push — while the revision-keyed
// per-subject watermark guard keeps the label merely present from
// re-firing (§forgejo/decisions/label-triggered-review).
// The sweep's fan-out row is watermark-exempt per lens: a lens that
// has reported nothing at the current head is a lens whose dispatch
// may have gone nowhere, and a revision-keyed watermark at a frozen
// head sha would strand the PR (§forgejo/reconciliation/watermark-exemptions).
// The exemption is keyed on the event, not the state: the same rows
// reached from the PR-synced event keep the revision-keyed guard.
func decidePRSynced(e Event, s State, w *Watermark) Decision {
	pr := s.PR
	if pr == nil {
		return holdOff("no PR state re-fetched")
	}
	if !prOpen(pr) {
		return holdOff(fmt.Sprintf("PR %d is not open", pr.Number))
	}
	if !pr.Mergeable {
		if alreadyDispatched(w, ActionRebase, pr.HeadSHA) {
			return holdOff(fmt.Sprintf("rebase already dispatched at revision %s", pr.HeadSHA))
		}
		return dispatch(ActionRebase, fmt.Sprintf("PR %d synced with merge conflicts", pr.Number))
	}
	return decideReviewFanOut(e, pr, s, w, carriesLabel(pr.Labels, LabelTriggerReview))
}

// decideReviewFanOut is the review dispatch predicate
// (§forgejo/decisions/review-lenses): the lenses to dispatch are the
// configured lenses with no undismissed approving review at the PR's
// current head. Each outstanding lens passes under its own guard —
// the subject-keyed watermark slot for its identity at this revision,
// exempt on the sweep while the lens has reported nothing at the head
// (§forgejo/reconciliation/watermark-exemptions) — and under the
// per-lens sender guard: a lens's own push does not re-fire that lens
// (§forgejo/decisions/loop-prevention). A label-requested fan-out
// overrides the sender guard alone; the revision-keyed per-subject
// guard still holds the label's mere presence from re-firing
// (§forgejo/decisions/label-triggered-review). When every lens is
// satisfied the decision holds off naming that fact — the
// review-following rows own the PR then; when the outstanding lenses
// are all suppressed, the hold-off names them.
func decideReviewFanOut(e Event, pr *PullRequest, s State, w *Watermark, labelRequested bool) Decision {
	var dispatched []LensDispatch
	var satisfied, suppressed []string
	for _, lens := range s.ReviewLenses {
		st := lensStandingAtHead(s.Reviews, lens, pr.HeadSHA)
		if st.approved && !labelRequested {
			satisfied = append(satisfied, lens.Identity)
			continue
		}
		if !labelRequested && lens.Identity == e.Sender && lensHasAnyReview(s.Reviews, lens) {
			// The per-lens sender guard, the per-lens shape of the
			// retired reviewing-author guard: a lens's own push does
			// not re-fire that lens, but the other lenses' dispatches
			// go on (§forgejo/decisions/loop-prevention). Like the
			// retired guard it speaks only when the lens has actually
			// reviewed — a push that no lens has looked at yet is
			// reviewed by everyone.
			suppressed = append(suppressed, fmt.Sprintf("%s (its own push at head %s)", lens.Identity, pr.HeadSHA))
			continue
		}
		exempt := onSweep(e) && !st.reported
		guarded := alreadyDispatchedSubject(w, lens.Identity, ActionReview, pr.HeadSHA)
		if !exempt && guarded {
			suppressed = append(suppressed, lens.Identity)
			continue
		}
		reason := fmt.Sprintf("lens %s has no undismissed approval at head %s", lens.Identity, pr.HeadSHA)
		if labelRequested && st.approved {
			// The label re-asks a lens that already approved at head:
			// an explicit human request outranks the standing approval
			// (§forgejo/decisions/label-triggered-review).
			reason = fmt.Sprintf("lens %s carries the %s label's explicit request over its approval at head %s", lens.Identity, LabelTriggerReview, pr.HeadSHA)
		}
		if exempt && guarded {
			// The stalled shape the exemption exists to recover: the
			// state fact carries past the recorded revision, and the
			// log says so rather than "already dispatched".
			reason += fmt.Sprintf("; the state calls for the lens regardless of revision %s", pr.HeadSHA)
		}
		dispatched = append(dispatched, LensDispatch{Lens: lens.Identity, Reason: reason})
	}
	if len(dispatched) > 0 {
		reasons := make([]string, len(dispatched))
		for i, ld := range dispatched {
			reasons[i] = ld.Reason
		}
		d := dispatch(ActionReview, fmt.Sprintf("PR %d at head %s: dispatching review lens(es) %s — %s", pr.Number, pr.HeadSHA, lensNames(dispatched), strings.Join(reasons, "; ")))
		d.Lenses = dispatched
		return d
	}
	if len(suppressed) > 0 {
		return holdOff(fmt.Sprintf("review already dispatched at revision %s for the outstanding lens(es) of PR %d: %s", pr.HeadSHA, pr.Number, strings.Join(suppressed, ", ")))
	}
	return holdOff(fmt.Sprintf("PR %d: every review lens (%s) holds an undismissed approval at head %s; the review-following rows decide", pr.Number, strings.Join(lensIdentities(s.ReviewLenses), ", "), pr.HeadSHA))
}

// lensStanding is one lens's standing against a revision.
type lensStanding struct {
	// reported is true when the lens has an undismissed review at the
	// revision.
	reported bool
	// approved is true when the lens's latest undismissed review at
	// the revision is an approval.
	approved bool
}

// lensStandingAtHead classifies one lens against head: its latest
// undismissed review submitted at head decides its standing — an
// approval the forge has dismissed does not count, and a later
// non-approval supersedes an earlier approval by the same lens
// (§forgejo/decisions/review-lenses).
func lensStandingAtHead(reviews []Review, lens ReviewLens, head string) lensStanding {
	var st lensStanding
	var latest *Review
	for i := range reviews {
		r := &reviews[i]
		if reviewIsDismissed(r) || r.Author != lens.Identity || r.CommitID != head {
			continue
		}
		st.reported = true
		if latest == nil || r.SubmittedAt.After(latest.SubmittedAt) {
			latest = r
		}
	}
	st.approved = latest != nil && latest.Event == ReviewApproved
	return st
}

// lensHasAnyReview reports whether the lens authored any undismissed
// review of the pull request, at any revision — the standing the
// per-lens sender guard reads (§forgejo/decisions/loop-prevention).
func lensHasAnyReview(reviews []Review, lens ReviewLens) bool {
	for i := range reviews {
		if !reviewIsDismissed(&reviews[i]) && reviews[i].Author == lens.Identity {
			return true
		}
	}
	return false
}

// allLensesApproved reports whether every configured lens holds an
// undismissed approval at head — the review gate the fix-feedback,
// merge-or-wait, and landing-set rows consult before dispatching past
// the chain (§forgejo/decisions/review-lenses).
func allLensesApproved(reviews []Review, lenses []ReviewLens, head string) bool {
	for _, lens := range lenses {
		if !lensStandingAtHead(reviews, lens, head).approved {
			return false
		}
	}
	return len(lenses) > 0
}

// nonApprovalAtHead reports whether some reviewer's latest undismissed
// review at head is not an approval, and separately whether one
// requests changes — the "the reports are not all approvals" half of
// the fix-feedback gate, split so the event path can act on a
// changes-requested alone while the sweep also reads comment reports
// (§forgejo/decisions/review-lenses, §forgejo/decisions/transition-table).
// It reads every author, not only the lenses, so a human's
// changes-requested at head keeps the fix loop the fix loop
// (§forgejo/decisions/loop-prevention). A reviewer whose approval is
// the latest of its own reviews at head clears its own earlier
// rejection, exactly as the retired single-reviewer rule did.
func nonApprovalAtHead(reviews []Review, head string) (nonApproval, changesRequested bool) {
	latest := map[string]*Review{}
	for i := range reviews {
		r := &reviews[i]
		if reviewIsDismissed(r) || r.CommitID != head {
			continue
		}
		if cur, ok := latest[r.Author]; !ok || r.SubmittedAt.After(cur.SubmittedAt) {
			latest[r.Author] = r
		}
	}
	for _, r := range latest {
		if r.Event != ReviewApproved {
			nonApproval = true
		}
		if r.Event == ReviewChangesRequested {
			changesRequested = true
		}
	}
	return nonApproval, changesRequested
}

func lensNames(ds []LensDispatch) string {
	names := make([]string, len(ds))
	for i, d := range ds {
		names[i] = d.Lens
	}
	return strings.Join(names, ", ")
}

func lensIdentities(lenses []ReviewLens) []string {
	ids := make([]string, len(lenses))
	for i, l := range lenses {
		ids[i] = l.Identity
	}
	return ids
}

// decidePRReconcile handles the reconciliation sweep's synthetic
// current-state event for a pull request: like PR synced, except that
// the sweep's re-fetch also reads the connected issues and their
// blockers, so the merge gate is fed wherever the sweep may dispatch a
// merge — and except when the chain has reported at the current head.
// The review-reading branch is per lens
// (§forgejo/decisions/review-lenses): when every lens holds an
// undismissed approval at head and no reviewer's latest undismissed
// review at head is a non-approval, the decision dispatches
// merge-or-wait — held off, naming the open blockers, when a
// connected issue has one (§forgejo/decisions/merge-gate); when every
// lens has reported at head and the reports are not all approvals, it
// dispatches fix-feedback — a comment report moves the PR along on
// the sweep, triggering the implementer, not the reviewer
// (§forgejo/decisions/transition-table). While any lens has not
// reported at head, fix-feedback holds off and the synced-like
// fan-out row dispatches the outstanding lenses instead
// (§forgejo/decisions/review-lenses).
// Both fix-feedback rows sit behind the ownership gate: a PR whose
// author is not the configured implementer identity holds off
// (§forgejo/config/implementer-identity, §forgejo/decisions/loop-prevention,
// s-git/maitred#143).
// The fix-feedback row is watermark-exempt on the sweep, and the
// approved row is exempt only when the PR opts into merging on its
// own with the agentic-auto-merge label: a PR standing at a fully
// reported chain at a frozen head sha is then re-fired whatever its
// watermark records (§forgejo/reconciliation/watermark-exemptions).
// A fix-feedback dispatch that died recovers on the next sweep the
// same way an approved auto-merge PR's merge does: the state — an
// open, mergeable PR whose chain reports feedback at head — is
// self-limiting, ending the moment the author pushes. Because the
// PR-commented event shares this function, the exemption tests the
// event, not the state: only the sweep is exempt.
func decidePRReconcile(e Event, s State, w *Watermark) Decision {
	pr := s.PR
	if pr != nil && prOpen(pr) && pr.Mergeable {
		if d, ok := decideChainReported(e, s, w, true); ok {
			return d
		}
	}
	return decidePRSynced(e, s, w)
}

// decideChainReported is the review-reading cluster's shared branch:
// the gate the lens reports at head impose on fix-feedback and
// merge-or-wait (§forgejo/decisions/review-lenses). mergeFires is
// every lens approved at head with no reviewer's latest undismissed
// review at head a non-approval; fixFires is every lens reported at
// head with the reports not all approvals — on the sweep a comment
// report counts (the sweep triggers the implementer, not the
// reviewer), while the pure review-event path waits for a
// changes-requested (§forgejo/decisions/transition-table). ok is
// false when the branch does not apply — some lens has still not
// reported at head — and the caller falls through to the fan-out
// row, which names the outstanding lenses in its reason.
func decideChainReported(e Event, s State, w *Watermark, sweepComments bool) (Decision, bool) {
	pr := s.PR
	gate := prReviewGate(s.Reviews, s.ReviewLenses, pr.HeadSHA)
	if !gate.allReported {
		return Decision{}, false
	}
	if gate.mergeFires() {
		if d, ok := syncMemberSetHoldOff(pr, s); ok {
			return d, true
		}
		if open := mergeGateOpenBlockers(e, s); len(open) > 0 {
			return holdOff(fmt.Sprintf("PR %d stands at approving reviews from every lens, but a connected issue is blocked by %s; not merging", pr.Number, blockerNames(open))), true
		}
		// The sweep's exempt row. An agentic-auto-merge PR sits at a
		// frozen head sha once the chain approves, so a revision-keyed
		// watermark makes it a fixed point the sweep can never fire on
		// and a labelled PR that failed to merge never recovers. The
		// state is therefore its watermark: the row re-fires until the
		// PR closes, and stops the moment it does, because the sweep
		// enumerates open pull requests only. The opt-in label is the
		// whole of it — an approved PR without it waits on a human,
		// revision-keyed as ever — and the exemption belongs to this
		// row alone: the same state reached from an event (the review
		// event, a PR comment) keeps the guard below, so a fully
		// approved chain never re-fires merge on its own
		// (§forgejo/reconciliation/watermark-exemptions,
		// §forgejo/decisions/loop-prevention).
		if onSweep(e) && carriesLabel(pr.Labels, LabelAgenticAutoMerge) {
			return dispatch(ActionMergeOrWait, fmt.Sprintf("PR %d stands at approving reviews from every lens and carries %s; the state calls for merge-or-wait regardless of revision %s", pr.Number, LabelAgenticAutoMerge, pr.HeadSHA)), true
		}
		if alreadyDispatched(w, ActionMergeOrWait, pr.HeadSHA) {
			return holdOff(fmt.Sprintf("merge-or-wait already dispatched at revision %s", pr.HeadSHA)), true
		}
		return dispatch(ActionMergeOrWait, fmt.Sprintf("PR %d: every lens (%s) holds an undismissed approval at head %s", pr.Number, strings.Join(lensIdentities(s.ReviewLenses), ", "), pr.HeadSHA)), true
	}
	if gate.fixFires(sweepComments) {
		if d, held := holdOffPRNotOwned(pr, s.ImplementerIdentity); held {
			return d, true
		}
		if !onSweep(e) && alreadyDispatched(w, ActionFixFeedback, pr.HeadSHA) {
			return holdOff(fmt.Sprintf("fix-feedback already dispatched at revision %s", pr.HeadSHA)), true
		}
		return dispatchOnState(w, onSweep(e), ActionFixFeedback, pr.HeadSHA, fmt.Sprintf("PR %d stands at reviews at head %s whose reports are not all approvals (lenses without an approval: %s)", pr.Number, pr.HeadSHA, strings.Join(gate.outstanding, ", "))), true
	}
	return Decision{}, false
}

// reviewGate is the state the review-following rows read: how the
// lens chain and the other reviewers stand against the PR's head
// (§forgejo/decisions/review-lenses).
type reviewGate struct {
	// allReported is true when every configured lens has an
	// undismissed review at head — the chain has had its say.
	allReported bool
	// allApproved is true when every lens holds an undismissed
	// approval at head.
	allApproved bool
	// nonApproval is true when some reviewer's latest undismissed
	// review at head is not an approval.
	nonApproval bool
	// changesRequested is true when some reviewer's latest
	// undismissed review at head requests changes.
	changesRequested bool
	// outstanding names the lenses without an undismissed approval
	// at head.
	outstanding []string
}

func (g reviewGate) mergeFires() bool { return g.allApproved && !g.nonApproval }

// fixFires reports when the reports call for a fix: every lens has
// reported and the reports are not all approvals. sweepComments
// widens "not all approvals" to any non-approval (a comment report
// counts, the sweep triggering the implementer); the event path acts
// on a changes-requested alone, so a comment-only chain holds off
// until a verdict or the sweep (§forgejo/decisions/transition-table).
func (g reviewGate) fixFires(sweepComments bool) bool {
	if !g.allReported {
		return false
	}
	if sweepComments {
		return g.nonApproval
	}
	return g.changesRequested
}

// prReviewGate computes the gate from the PR's reviews, the
// configured lenses, and the head (§forgejo/decisions/review-lenses).
func prReviewGate(reviews []Review, lenses []ReviewLens, head string) reviewGate {
	g := reviewGate{allReported: len(lenses) > 0, allApproved: len(lenses) > 0}
	for _, lens := range lenses {
		st := lensStandingAtHead(reviews, lens, head)
		if !st.reported {
			g.allReported = false
		}
		if !st.approved {
			g.allApproved = false
			g.outstanding = append(g.outstanding, lens.Identity)
		}
	}
	g.nonApproval, g.changesRequested = nonApprovalAtHead(reviews, head)
	return g
}

// onSweep reports whether the event is the reconciliation sweep's
// synthetic current-state event — the path the watermark exemption
// belongs to. The sweep's PR row is shared with the PR-commented
// event, which decides the same state under the ordinary
// revision-keyed guard, so an exempt row on that path is keyed on the
// event, not on the function that reads the state
// (§forgejo/reconciliation/watermark-exemptions).
func onSweep(e Event) bool {
	return e.Type == EventReconcile
}

// holdOffPRNotOwned is the fix-feedback ownership gate: the implementer
// is dispatched to fix review feedback only on PRs it owns — a PR
// authored by anyone else is that author's to fix, and dispatching the
// implementer onto it collides with a human handling their own review
// feedback (s-git/maitred#143, s-git/s-puppet#118). The gate covers
// fix-feedback alone: reviewing, reporting on, and merging a
// human-owned PR remain intended. When the implementer identity is
// unset the gate is inert — no identity is declared, so the dispatch
// proceeds as before (§forgejo/config/implementer-identity,
// §forgejo/decisions/loop-prevention). The match is exact — no case
// folding, no substring — mirroring the retired authorship guard.
func holdOffPRNotOwned(pr *PullRequest, identity string) (Decision, bool) {
	if identity == "" || pr.Author == identity {
		return Decision{}, false
	}
	return holdOff(fmt.Sprintf("PR %d is authored by %s, not the implementer identity %s; fix-feedback is theirs to fix, not the implementer's", pr.Number, pr.Author, identity)), true
}

// dispatchOnState dispatches an exempt sweep row: the state is the
// watermark, so the row never consults the guard. When the watermark
// does happen to already record the action at this revision — the
// stalled shape the exemption exists to recover — the reason names
// the revision the state carries past it, so the decision log keeps
// stating the state fact and never "already dispatched at this
// revision" (§forgejo/reconciliation/watermark-exemptions,
// §forgejo/observability/the-decision-log). On a guarded row
// (exempt false) it is a plain dispatch.
func dispatchOnState(w *Watermark, exempt bool, action Action, revision, reason string) Decision {
	if exempt && alreadyDispatched(w, action, revision) {
		reason = fmt.Sprintf("%s; the state calls for %s regardless of revision %s", reason, action, revision)
	}
	return dispatch(action, reason)
}

// decidePRReviewed handles a submitted review: the re-fetched lens
// chain at head decides (§forgejo/decisions/review-lenses). When the
// chain has reported — every lens holds an undismissed review at the
// current head — a non-approval anywhere dispatches fix-feedback
// behind the ownership gate (a PR authored by anyone but the
// configured implementer identity holds off: the human owns that PR
// and handles the feedback themselves —
// §forgejo/config/implementer-identity, §forgejo/decisions/loop-prevention,
// s-git/maitred#143), and approvals across the board dispatch
// merge-or-wait only when the merge gate lets it through, no
// connected issue having an open blocker (§forgejo/decisions/merge-gate).
// A comment-only chain holds off here — the sweep is the row that
// turns comment reports into fix-feedback — and while any lens has
// not reported at head the decision falls through to the fan-out row
// so the outstanding lenses are dispatched, the hold-off naming them
// (§forgejo/decisions/review-lenses).
func decidePRReviewed(e Event, s State, w *Watermark) Decision {
	pr := s.PR
	if pr == nil || !prOpen(pr) {
		return holdOff("PR is not open")
	}
	// A submitted review decides on the review chain's content. Work
	// whose forest has not been commissioned cannot be walked through
	// to a merge by approving reviews: the merge gate holds every
	// such merge off by naming its open blockers
	// (§forgejo/decisions/merge-gate).
	if len(s.Reviews) == 0 {
		return holdOff("no reviews on re-fetch")
	}
	if d, ok := decideChainReported(e, s, w, false); ok {
		return d
	}
	// A fully reported chain that dispatches no merge or fix — the
	// comment-only report the event path deliberately leaves to the
	// sweep — is not an unreported lens, and the fan-out row would
	// ask it again (§forgejo/decisions/transition-table).
	for _, lens := range s.ReviewLenses {
		if !lensStandingAtHead(s.Reviews, lens, pr.HeadSHA).reported {
			// The chain has not finished reporting at head: this
			// review is one lens's verdict, and dispatching the lenses
			// still owed is what advances the chain
			// (§forgejo/decisions/review-lenses). The subject-keyed
			// guards keep a lens already dispatched at this head from
			// being duplicated, and the per-lens sender guard keeps the
			// submitting lens — the event's sender — from being re-fired
			// by its own verdict reaching the fan-out row
			// (§forgejo/decisions/loop-prevention).
			return decidePRSynced(e, s, w)
		}
	}
	return holdOff(fmt.Sprintf("PR %d: every review lens has reported at head %s and the reports do not all approve; the fix waits for a changes-requested or the sweep", pr.Number, pr.HeadSHA))
}

// decidePRMerged runs the unblock cascade for the connected issue a
// merged PR auto-closes (§forgejo/decisions/unblock-cascade).
func decidePRMerged(s State) Decision {
	if s.PR == nil || !s.PR.Merged {
		return holdOff("PR is not merged on re-fetch")
	}
	d := holdOff(fmt.Sprintf("PR %d merged; unblock cascade for the connected issue", s.PR.Number))
	d.Cascade, d.CascadeHoldOffs = unblockCascade(s.CascadeRoot)
	return d
}

// unblockCascade walks the blocker graph from the closed issue and
// returns the dispatches for the objects it now unblocks — open
// issues with all their issue-side blockers closed (dispatched
// implement, or wrap-up when the issue carries the outcome label)
// and, for issues whose remaining open blockers are only their
// connected open PRs, those PRs (dispatched merge-or-wait) — each
// exactly once, plus the hold-offs of the open outcome trackers the
// walk finds still blocked: every tracker it visits without emitting
// for it is named, so no tracker is passed over in silence. The
// dispatch's action is classified at
// emission from the node's labels (the walk itself stays
// label-blind); a newly unblocked tracker dispatches wrap-up, never
// implement. The walk is transitive (a blocked issue that is itself
// closed cascades onward) and terminates on cycles via a visited set
// (§forgejo/decisions/unblock-cascade).
func unblockCascade(root *IssueNode) ([]CascadeDispatch, []CascadeHoldOff) {
	if root == nil {
		return nil, nil
	}
	var (
		emissions []CascadeDispatch
		holds     []CascadeHoldOff
	)
	seenPR := map[Key]bool{}
	visited := map[Key]bool{
		{Repo: root.Issue.Repository, Kind: KindIssue, Number: root.Issue.Number}: true,
	}
	var walk func(n *IssueNode)
	walk = func(n *IssueNode) {
		for i := range n.Blocks {
			b := &n.Blocks[i]
			k := Key{Repo: b.Issue.Repository, Kind: KindIssue, Number: b.Issue.Number}
			if visited[k] {
				continue
			}
			visited[k] = true
			switch b.Issue.State {
			case "closed":
				walk(b) // transitive: a closed issue cascades onward
			case "open":
				// An open sync issue the walk finds is a grouping
				// issue a member's outbound edge points at: emit it
				// as a sync-set candidate when the graph in hand
				// cannot show the set stalled on a member awaiting a
				// human — the set's own re-fetched evaluation decides,
				// and an unready one stays silent
				// (§forgejo/decisions/sync-set).
				if hasLabel(&b.Issue, LabelSync) {
					if syncSetCouldBeReady(b.Blockers) {
						emissions = append(emissions, CascadeDispatch{Key: k, Action: ActionSync})
					}
					continue
				}
				// An issue whose remaining open blockers are its
				// connected open PRs is not an implement candidate —
				// the work exists as a PR, and the closure that
				// removed the last issue-side blocker unblocks that
				// PR instead (§forgejo/decisions/unblock-cascade).
				var emitted bool
				if prs := openPullBlockers(b.Blockers); len(prs) > 0 && allClosed(issueBlockers(b.Blockers)) {
					for _, p := range prs {
						pk := Key{Repo: p.Repository, Kind: KindPR, Number: p.Number}
						if seenPR[pk] {
							continue // unblocked once, even by several issues
						}
						seenPR[pk] = true
						emissions = append(emissions, CascadeDispatch{Key: pk, Action: ActionMergeOrWait})
					}
				} else if allClosed(b.Blockers) {
					emitted = true
					emissions = append(emissions, CascadeDispatch{Key: k, Action: cascadeAction(&b.Issue)})
				}
				// A tracker the walk does not emit for is not silent
				// either: the hold-off names what still holds it — an
				// open issue blocker, or the open PR whose merge will
				// fire its wrap-up (§forgejo/decisions/unblock-cascade).
				if !emitted {
					if h, ok := trackerStillBlocked(&b.Issue, b.Blockers); ok {
						holds = append(holds, h)
					}
				}
			}
		}
	}
	walk(root)
	return emissions, holds
}

// syncSetCouldBeReady is the cascade walk's cheap necessary condition
// for a landing set. A set is blocked by construction, so the walk
// would otherwise pay the whole set read — the grouping issue, its
// dependency rows, and per open member its dependencies, its PR and
// that PR's reviews — for a set that cannot be ready, on every
// closure that cascades through any member while the set is authoring.
// What the graph already holds rules out one such case exactly: an
// open blocker carrying no agentic-sync-merge is a member awaiting a
// human, and the predicate holds off on that fact alone, so emitting
// would only buy an evaluation saying "not ready". The remaining
// not-ready readings cannot be told from graph data — a member's
// delivering PR hangs off the member, one hop deeper than the walk
// goes — so they stay the set's own read to make, the re-check in the
// dispatcher the authority and the sweep the backstop, unchanged
// (§forgejo/decisions/sync-set, §forgejo/decisions/unblock-cascade).
func syncSetCouldBeReady(blockers []IssueNode) bool {
	for i := range blockers {
		b := &blockers[i]
		if b.Issue.State != "open" {
			continue // landed, or finished by a human
		}
		if b.Issue.IsPull {
			// A PR row is a member whose delivery is itself: whether it
			// stands approved at head, mergeable and opted in is a fact
			// the graph does not carry, so the predicate's read of the
			// PR decides it (§forgejo/decisions/sync-set).
			continue
		}
		if !hasLabel(&b.Issue, LabelAgenticSyncMerge) {
			return false // a member awaiting a human: the set cannot land
		}
	}
	return true
}

// cascadeAction classifies a newly unblocked cascade issue at
// emission: an issue carrying the outcome label is a tracker whose
// members are all finished, so it warrants wrap-up; everything else
// warrants implement. An issue carrying ideation as well does not
// warrant wrap-up — decompose wins over wrap-up — so it takes the
// implement emission, which the reserved-label insurance then holds
// off naming ideation (§forgejo/decisions/unblock-cascade).
func cascadeAction(issue *Issue) Action {
	if hasLabel(issue, LabelOutcome) && !hasLabel(issue, LabelIdeation) {
		return ActionWrapUp
	}
	return ActionImplement
}

// trackerStillBlocked is the cascade hold-off for an open outcome
// tracker the walk finds still blocked: the reason names the blockers
// holding it (cross-repo included) so the decision log says why
// wrap-up did not fire when one of several blockers closed. A blocker
// holds the tracker unless its state is closed — one the engine cannot
// read as closed holds as surely as an open one does, and is named
// rather than dropped. An ideation-carrying issue is not this hold-off's
// subject: decompose wins over wrap-up and the sweep's rows decide it.
// A tracker whose ideation root is closed is a commissioned forest,
// which the sweep curates, so the reason says that instead of
// promising a wrap-up the state does not call for
// (§forgejo/decisions/unblock-cascade,
// §forgejo/reconciliation/watermark-exemptions).
func trackerStillBlocked(issue *Issue, blockers []IssueNode) (CascadeHoldOff, bool) {
	if !hasLabel(issue, LabelOutcome) || hasLabel(issue, LabelIdeation) || allClosed(blockers) {
		return CascadeHoldOff{}, false
	}
	var (
		open  []Issue
		every = make([]Issue, 0, len(blockers))
	)
	for i := range blockers {
		every = append(every, blockers[i].Issue)
		if blockers[i].Issue.State != "closed" {
			open = append(open, blockers[i].Issue)
		}
	}
	reason := fmt.Sprintf("issue %d is an open %s tracker still blocked by %s", issue.Number, LabelOutcome, blockerNames(open))
	if gate := ideationGate(every); gate != nil && gate.State != "open" {
		reason = fmt.Sprintf("issue %d is an open %s tracker commissioned by %s %d and still blocked by %s; the sweep curates the forest",
			issue.Number, LabelOutcome, LabelIdeation, gate.Number, blockerNames(open))
	} else {
		reason += "; wrap-up held off"
	}
	return CascadeHoldOff{Key: issueKey(issue), Issue: issue, Reason: reason}, true
}

// cascadeActionFitsState re-checks a cascade emission against the
// re-fetched issue — the state wins over the graph data. An implement
// emission is covered by the reserved-label insurance: a tracker is
// never an implement candidate, the cascade included, and a newborn
// forest member carries its transient gate into the walk, which
// holds off there too (§forgejo/decisions/unblock-cascade,
// s-git/maitred#125). A wrap-up emission demands the issue still be
// an outcome tracker without ideation; otherwise the sweep decides
// the issue afresh (§forgejo/decisions/unblock-cascade).
func cascadeActionFitsState(action Action, issue *Issue) (Decision, bool) {
	if action == ActionWrapUp {
		switch {
		case hasLabel(issue, LabelIdeation):
			return holdOff(fmt.Sprintf("issue %d carries the %s label; decompose wins over wrap-up", issue.Number, LabelIdeation)), false
		case !hasLabel(issue, LabelOutcome):
			return holdOff(fmt.Sprintf("issue %d no longer carries the %s label; nothing to wrap up", issue.Number, LabelOutcome)), false
		}
		return Decision{}, true
	}
	if label := neverWorkItemLabel(issue.Labels); label != "" {
		return holdOffNeverImplement(issue.Number, label), false
	}
	if label := transientHoldOffLabel(issue.Labels); label != "" {
		return holdOffForestSeeding(issue.Number, label), false
	}
	return Decision{}, true
}

// issueBlockers returns the non-pull-request entries of a blocker
// list — the issue-side blockers the cascade tests
// (§forgejo/decisions/unblock-cascade).
func issueBlockers(blockers []IssueNode) []IssueNode {
	var issues []IssueNode
	for i := range blockers {
		if !blockers[i].Issue.IsPull {
			issues = append(issues, blockers[i])
		}
	}
	return issues
}

// openPullBlockers returns the open pull-request entries of a
// blocker list — an issue's connected open PRs, seen through the
// dependency rows that make the PR a blocker of the issue it delivers
// (§forgejo/decisions/unblock-cascade).
func openPullBlockers(blockers []IssueNode) []Issue {
	var pulls []Issue
	for i := range blockers {
		if blockers[i].Issue.IsPull && blockers[i].Issue.State == "open" {
			pulls = append(pulls, blockers[i].Issue)
		}
	}
	return pulls
}

// allClosed reports whether every blocker is closed; no blockers is
// unblocked.
func allClosed(blockers []IssueNode) bool {
	for i := range blockers {
		if blockers[i].Issue.State != "closed" {
			return false
		}
	}
	return true
}

// ideationGate returns the issue gating an issue's commissioning: the first
// blocker carrying the reserved ideation label, or nil when no blocker
// carries it. A tracker is gated one hop away because it is a member like
// any other and carries the membership edge from its root; the engine never
// walks a graph to find the gate. A closed gate is a commissioned forest; an
// open one is not (§forgejo/decisions/commissioning,
// §forgejo/reconciliation/watermark-exemptions).
func ideationGate(blockers []Issue) *Issue {
	for i := range blockers {
		if blockers[i].IsPull {
			continue // a pull request is a delivery vehicle, never a root
		}
		if hasLabel(&blockers[i], LabelIdeation) {
			return &blockers[i]
		}
	}
	return nil
}

// openBlockers returns the blockers that are still open.
func openBlockers(blockers []Issue) []Issue {
	var open []Issue
	for i := range blockers {
		if blockers[i].State == "open" {
			open = append(open, blockers[i])
		}
	}
	return open
}

// syncMemberSetHoldOff is the approval path's sync-set hold-off,
// consulted by every path about to dispatch merge-or-wait, ahead of
// the merge gate: when the PR is a member of an open landing set —
// through a connected issue carrying agentic-sync-merge, or because it
// blocks a grouping issue **directly** and is its own delivery —
// landing is the set's decision, not the individual PR's, and
// merge-or-wait must never merge it. The hold-off
// is silent (an unready set is not an event worth a comment) and
// emits the members' open sync sets as sync dispatches against the
// grouping issues, so the last approval that completes a set
// triggers it without waiting for the sweep
// (§forgejo/decisions/sync-set). Two readings of the label decide
// alone: a member also carrying agentic-auto-merge is the authoring
// error the hold-off exists to contain and holds off whatever sets
// exist, naming both labels; a member of no open set — its set
// retired by the decomposer, which deletes rather than closes — has
// no set whose decision to wait for, and holding off would strand an
// approved deliverable with no backstop, so its stale label is no
// gate and ok is false, letting the caller's merge gate decide on.
// ok is likewise false when no connected issue is a member at all.
func syncMemberSetHoldOff(pr *PullRequest, s State) (Decision, bool) {
	var members, dualLabelled, directSets []string
	var emissions []CascadeDispatch
	seen := map[Key]bool{}
	emit := func(set *Issue) {
		k := issueKey(set)
		if seen[k] {
			return // one emission per grouping issue, even from several members
		}
		seen[k] = true
		emissions = append(emissions, CascadeDispatch{Key: k, Action: ActionSync})
	}
	// The PR's own membership: it stands in a grouping issue's blockers,
	// so the row is the member and its delivery is itself — the shape the
	// predicate already reads, held off here on the same terms
	// (§forgejo/decisions/sync-set).
	direct := len(s.SyncSets) > 0
	for i := range s.SyncSets {
		set := &s.SyncSets[i]
		directSets = append(directSets, fmt.Sprintf("%s#%d", set.Repository, set.Number))
		emit(set)
	}
	for i := range s.ConnectedIssues {
		c := &s.ConnectedIssues[i]
		if !hasLabel(&c.Issue, LabelAgenticSyncMerge) {
			continue
		}
		ref := fmt.Sprintf("%s#%d", c.Issue.Repository, c.Issue.Number)
		members = append(members, ref)
		if hasLabel(&c.Issue, LabelAgenticAutoMerge) {
			dualLabelled = append(dualLabelled, ref)
		}
		for j := range c.SyncSets {
			emit(&c.SyncSets[j])
		}
	}
	if len(members) == 0 && !direct {
		return Decision{}, false
	}
	if len(emissions) == 0 && len(dualLabelled) == 0 {
		// A member of no open set: the set was retired or its
		// membership edge removed. The label is stale authoring, not
		// a gate — fall through to the caller's merge gate rather
		// than hold off with nothing left to wait for
		// (§forgejo/decisions/sync-set).
		return Decision{}, false
	}
	reason := fmt.Sprintf("PR %d stands at an approved review, but its connected issue %s is a member of a sync set; landing is the set's decision, not the PR's", pr.Number, strings.Join(members, ", "))
	if direct {
		who := fmt.Sprintf("it is a direct member of the open sync set grouped by %s", strings.Join(directSets, ", "))
		if len(members) > 0 {
			reason = fmt.Sprintf("PR %d stands at an approved review, but its connected issue %s is a member of a sync set and %s; landing is the set's decision, not the PR's", pr.Number, strings.Join(members, ", "), who)
		} else {
			reason = fmt.Sprintf("PR %d stands at an approved review, but %s; landing is the set's decision, not the PR's", pr.Number, who)
		}
	}
	if len(dualLabelled) > 0 {
		reason = fmt.Sprintf("PR %d stands at an approved review, but its connected issue %s carries both %s and %s; a member is opted into the set, never into merging on its own — the double label is an authoring error the decomposer must retire", pr.Number, strings.Join(dualLabelled, ", "), LabelAgenticSyncMerge, LabelAgenticAutoMerge)
	} else if direct && carriesLabel(pr.Labels, LabelAgenticAutoMerge) {
		// A direct member's opt-ins sit on the PR itself: the connected
		// issue carries the kind label alone, so this is the only place
		// the double label can be named (§forgejo/decisions/sync-set).
		reason = fmt.Sprintf("PR %d stands at an approved review, but it is a direct member of the sync set grouped by %s and carries both %s and %s; a member is opted into the set, never into merging on its own — the double label is an authoring error the decomposer must retire", pr.Number, strings.Join(directSets, ", "), LabelAgenticSyncMerge, LabelAgenticAutoMerge)
	}
	d := holdOff(reason)
	d.Cascade = emissions
	return d, true
}

// considerSyncIssue is the sync predicate, one function called from
// every direction the set state moved from: the sweep's row, the
// cascade walk's emission, and the approval path's emission re-checked
// on re-fetch. An open issue carrying the sync label dispatches sync
// once every blocking issue is closed — landed, or finished by a
// human — or stands with its delivering PR approved at head and
// mergeable while carrying agentic-sync-merge. "Approved at head" is
// the lens gate: every configured lens must hold an undismissed
// approval at the PR's head (§forgejo/decisions/review-lenses). A
// pull request that blocks the grouping issue directly is such a
// blocking issue too, and is held to that same test through its own
// state: the set does not land past a delivery the graph places in
// front of it. The dispatch is recorded against a revision derived
// from the whole set, so the sweep does not re-fire at an unchanged
// set state (§forgejo/decisions/sync-set).
func considerSyncIssue(issue *Issue, members []SyncMember, lenses []ReviewLens, w *Watermark) Decision {
	if len(members) == 0 {
		return holdOff(fmt.Sprintf("issue %d is an open %s issue with no blocking issues; an empty set has nothing to land", issue.Number, LabelSync))
	}
	var notReady []string
	for i := range members {
		m := &members[i]
		if m.Issue.State != "open" {
			continue // a closed member's work has landed, or a human finished it
		}
		ref := fmt.Sprintf("%s#%d", m.Issue.Repository, m.Issue.Number)
		if m.Issue.IsPull {
			ref = fmt.Sprintf("%s PR #%d", m.Issue.Repository, m.Issue.Number)
		}
		switch {
		case !hasLabel(&m.Issue, LabelAgenticSyncMerge):
			notReady = append(notReady, fmt.Sprintf("%s is open without the %s label: a member awaiting a human must be closed before the set lands", ref, LabelAgenticSyncMerge))
		case m.PR == nil || !prOpen(m.PR) || !m.PR.Mergeable:
			if m.Issue.IsPull {
				// A PR member is its own delivery: what is not ready is
				// the PR itself, not some PR hanging off an issue
				// (§forgejo/decisions/sync-set).
				notReady = append(notReady, fmt.Sprintf("%s is not an open, mergeable pull request", ref))
			} else {
				notReady = append(notReady, fmt.Sprintf("%s has no mergeable open PR", ref))
			}
		default:
			if !allLensesApproved(m.Reviews, lenses, m.PR.HeadSHA) {
				notReady = append(notReady, fmt.Sprintf("%s is not approved at head %s", ref, m.PR.HeadSHA))
			}
		}
	}
	if len(notReady) > 0 {
		// An unready set is not an event worth a comment: the hold-off
		// is silent, at home only in the decision log
		// (§forgejo/decisions/sync-set, §forgejo/observability/the-decision-log).
		return holdOff(fmt.Sprintf("sync issue %d held off: %s", issue.Number, strings.Join(notReady, "; ")))
	}
	rev := syncSetRevision(issue, members, lenses)
	if alreadyDispatched(w, ActionSync, rev) {
		return holdOff(fmt.Sprintf("sync already dispatched at set revision %s", rev))
	}
	return dispatch(ActionSync, fmt.Sprintf("issue %d is an open %s issue: all %d member(s) closed or approved, mergeable, and opted in; the state calls for sync at set revision %s", issue.Number, LabelSync, len(members), rev))
}

// syncSetRevision derives the set revision the sync dispatch's
// watermark records: what the predicate reads about each member — its
// state, whether it carries the member label, and its delivering PR's
// head sha, open and mergeable state, and approval at that head —
// ordered by key so the API's enumeration order cannot change the
// digest, plus the grouping issue's **body** (§forgejo/decisions/sync-set,
// §forgejo/state/watermark).
// The members' updated_at is deliberately absent: it records any edit
// to any member — a comment, a title fix, an unrelated label toggle —
// and a digest that moved with it would re-dispatch the landing
// prompt on an unchanged predicate result, most noisily for a set
// stalled on a member awaiting a human. Nothing is lost by narrowing
// the digest: the sweep re-evaluates the set every interval whatever
// it says, so a change that does move readiness still re-fires within
// one interval, and one that does not stops re-firing at all.
//
// The grouping issue's body is in the digest precisely because the
// object under consideration is not that noise: it is the set, whose
// body names the order the landing prompt must honour. A ready set
// whose landing run reported an error — a joint conflict between
// individually-mergeable members, a repository the agent cannot merge
// in — is otherwise a fixed point of the digest: every later sweep
// reads the same readiness, finds the watermark already there, and
// dispatches nothing. Restating the order on the grouping issue is the
// human's lever on that report, and the row §reconciliation calls the
// set's backstop has to be able to answer it
// (§forgejo/reconciliation/watermark-exemptions). The digest takes the
// body itself and not the grouping issue's updated_at, which moves on a
// comment, a title fix or an unrelated label toggle: for the members the
// same narrowing was argued as "activity is not readiness", and the
// lever this section names is a restatement, not a touch.
func syncSetRevision(issue *Issue, members []SyncMember, lenses []ReviewLens) string {
	lines := make([]string, 0, len(members)+1)
	lines = append(lines, fmt.Sprintf("set %s#%d body=%s", issue.Repository, issue.Number, bodyRevision(issue)))
	for i := range members {
		m := &members[i]
		line := fmt.Sprintf("%s#%d state=%s %s=%t", m.Issue.Repository, m.Issue.Number, m.Issue.State, LabelAgenticSyncMerge, hasLabel(&m.Issue, LabelAgenticSyncMerge))
		if m.PR != nil {
			approvedAtHead := allLensesApproved(m.Reviews, lenses, m.PR.HeadSHA)
			line += fmt.Sprintf(" pr=%d open=%t mergeable=%t head=%s approved-at-head=%t", m.PR.Number, prOpen(m.PR), m.PR.Mergeable, m.PR.HeadSHA, approvedAtHead)
		}
		lines = append(lines, line)
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])
}

// mergeGateOpenBlockers is the merge gate's shared test, consulted by
// every path that dispatches merge-or-wait: the open blockers of the
// PR's connected issues, which must be none
// (§forgejo/decisions/merge-gate). The PR's own entry in a connected
// issue's blocker list is not
// a blocker — the PR is that issue's delivery vehicle (the
// issue-blocks record that connects them makes the PR one of the
// issue's blockers) — so it is excluded; every other open blocker
// counts, cross-repo included.
func mergeGateOpenBlockers(ev Event, s State) []Issue {
	var open []Issue
	for i := range s.ConnectedIssues {
		for j := range s.ConnectedIssues[i].Blockers {
			b := s.ConnectedIssues[i].Blockers[j]
			if b.State != "open" {
				continue
			}
			if b.IsPull && b.Number == ev.Number && b.Repository == ev.Repo {
				continue // the PR itself
			}
			open = append(open, b)
		}
	}
	return open
}

// blockerNames renders blockers as a comma-separated list of
// "repo#number" references, so a hold-off reason can name them —
// cross-repo blockers included (§forgejo/decisions/transition-table).
func blockerNames(blockers []Issue) string {
	names := make([]string, len(blockers))
	for i := range blockers {
		names[i] = fmt.Sprintf("%s#%d", blockers[i].Repository, blockers[i].Number)
	}
	return strings.Join(names, ", ")
}

// prOpen reports whether the re-fetched PR is open (not closed, not
// merged).
func prOpen(pr *PullRequest) bool {
	return pr.State == "open" && !pr.Merged
}

// issueRevision is the issue's activity revision: its updated_at in
// RFC 3339 (§forgejo/state/watermark).
func issueRevision(issue *Issue) string {
	return issue.UpdatedAt.UTC().Format(time.RFC3339)
}

// bodyRevision is the grouping issue's body as the set revision digests
// it: the text the landing prompt must honour, not the moment it was
// last touched — a comment or a label toggle moves updated_at and says
// nothing about the order (§forgejo/decisions/sync-set).
func bodyRevision(issue *Issue) string {
	sum := sha256.Sum256([]byte(issue.Body))
	return hex.EncodeToString(sum[:])
}

// alreadyDispatched reports whether the watermark already records
// action at revision — the idempotency guard, revision-keyed on every
// path: the same (event, state, revision) does not re-dispatch the same
// action (§forgejo/decisions/loop-prevention). The watermark exemption
// is not implemented here: it is scoped to the sweep, whose exempt rows
// in decideIssueReconcile, the sweep's per-lens fan-out exemption in
// decideReviewFanOut, and the sweep's fix-feedback and labelled
// merge-or-wait rows in decidePRReconcile simply never consult the
// watermark (§forgejo/reconciliation/watermark-exemptions). Keying the
// bypass on the action instead of the path would hand every future
// event row for those actions a suppression-free path by accident.
func alreadyDispatched(w *Watermark, action Action, revision string) bool {
	return w != nil && w.Action == string(action) && w.Revision == revision
}

// alreadyDispatchedSubject is the subject-keyed half of the same
// guard: whether the watermark's slot for one fan-out subject — a
// review lens's identity — already records action at revision, so two
// lenses dispatched at the same revision do not hold each other off
// and a lens mid-flight is not duplicated (§forgejo/state/watermark,
// §forgejo/decisions/review-lenses).
func alreadyDispatchedSubject(w *Watermark, subject string, action Action, revision string) bool {
	s, ok := w.subjectSlot(subject)
	return ok && s.Action == string(action) && s.Revision == revision
}

// hasLabel reports whether the issue carries the label. Label names are
// user-authored strings, so the reserved names match
// case-insensitively: a label created as "Ideation" must not silently
// decide as implement (§forgejo/reconciliation/watermark-exemptions).
func hasLabel(issue *Issue, label string) bool {
	return carriesLabel(issue.Labels, label)
}

// carriesLabel reports whether the label list carries the label, matching
// case-insensitively as hasLabel does — the same rule applies to the
// labels on a pull request, so a label created as "Trigger-Review" must
// not silently decide as no request (§forgejo/decisions/label-triggered-review).
func carriesLabel(labels []string, label string) bool {
	for _, l := range labels {
		if strings.EqualFold(l, label) {
			return true
		}
	}
	return false
}

// reservedLabel returns the reserved kind label the issue carries —
// the label that makes it something other than a work item — or the
// empty string when it carries none. Ideation is reported first: the
// decompose-wins precedence applies to the hold-off reason too
// (§forgejo/reconciliation/watermark-exemptions, §forgejo/decisions/sync-set).
func reservedLabel(issue *Issue) string {
	switch {
	case hasLabel(issue, LabelIdeation):
		return LabelIdeation
	case hasLabel(issue, LabelOutcome):
		return LabelOutcome
	case hasLabel(issue, LabelSync):
		return LabelSync
	default:
		return ""
	}
}

func dispatch(action Action, reason string) Decision {
	return Decision{Dispatched: true, Action: action, Reason: reason}
}

func holdOff(reason string) Decision {
	return Decision{Reason: reason}
}

// holdOffOutOfRemit is the admission hold-off: the repository holds no
// maitred role, so it is outside the engine's remit on either path
// (§forgejo/webhook/repository-roles, §forgejo/webhook/the-scope-cache).
func holdOffOutOfRemit(repo string) Decision {
	return holdOff(fmt.Sprintf("repo %s holds no maitred role (neither %s nor %s)", repo, maitredEnabledTopic, maitredOutcomesTopic))
}

// holdOffOutOfRemitWakesCascade is the admission hold-off of a close
// in a role-less repository whose issue sits in the blocker graph: the
// repository stays outside the engine's remit — nothing is acted on for
// the closed issue — but the graph may be read through it, and the
// reason names the unblock cascade the close woke. The two wordings
// distinguish inert-and-unrelated (the remit-only reason) from
// inert-but-in-the-graph (§forgejo/webhook/repository-roles,
// §forgejo/decisions/unblock-cascade).
func holdOffOutOfRemitWakesCascade(repo string, number int) Decision {
	return holdOff(fmt.Sprintf("%s; close of issue %d is in the blocker graph: unblock cascade woken", holdOffOutOfRemit(repo).Reason, number))
}

// workItemActions are the actions the maitred-enabled role grants
// admissibility to; outcomesActions are those the
// maitred-outcomes-repo role grants it to
// (§forgejo/webhook/repository-roles). decompose fires on the event
// path and on the sweep (#84); wrap-up fires on the sweep (#83, #85)
// and in the unblock cascade (#85).
var (
	workItemActions = map[Action]bool{
		ActionImplement:   true,
		ActionReassess:    true,
		ActionReview:      true,
		ActionFixFeedback: true,
		ActionMergeOrWait: true,
		ActionRebase:      true,
	}
	outcomesActions = map[Action]bool{
		ActionDecompose:      true,
		ActionWrapUp:         true,
		ActionTeardownForest: true,
		ActionSync:           true,
	}
)

// admissibleInRoles reports whether the repository roles of the
// repository holding the object under consideration admit the action
// (§forgejo/webhook/repository-roles). An action no role admits — an
// unknown one — is inadmissible: the test fails closed.
func admissibleInRoles(a Action, r roles) bool {
	return (r.outcomes && outcomesActions[a]) || (r.workItems && workItemActions[a])
}

// roleTopicFor names the topic of a role admitting the action, for the
// hold-off reason (§forgejo/webhook/repository-roles). An action no
// table admits — an unknown one — has no such topic and returns the
// empty string, so its hold-off never names a role that
// admissibleInRoles has just denied.
func roleTopicFor(a Action) string {
	if outcomesActions[a] {
		return maitredOutcomesTopic
	}
	if workItemActions[a] {
		return maitredEnabledTopic
	}
	return ""
}

// holdOffActionInadmissible is the admissibility hold-off: the action
// may not fire for an object in this repository — the roles of the
// repository holding the object, never the repository the event
// arrived from, deny it (§forgejo/webhook/repository-roles). An action
// no role admits names that plainly, rather than a role that does not
// admit it.
func holdOffActionInadmissible(a Action, repo string) Decision {
	if topic := roleTopicFor(a); topic != "" {
		return holdOff(fmt.Sprintf("action %s is inadmissible for repo %s: requires the %s role", a, repo, topic))
	}
	return holdOff(fmt.Sprintf("action %s is admitted by no maitred role", a))
}

// holdOffNeverImplement and holdOffNeverReassess are the reserved-label
// insurance hold-offs: the permanent taxonomy, where the label says
// what the issue *is* (§forgejo/decisions/transition-table).
func holdOffNeverImplement(number int, label string) Decision {
	return holdOff(fmt.Sprintf("issue %d carries the %s label; never an implement candidate", number, label))
}

func holdOffNeverReassess(number int, label string) Decision {
	return holdOff(fmt.Sprintf("issue %d carries the %s label; never a reassess candidate", number, label))
}

// holdOffForestSeeding names the transient gate: the member exists,
// but the forest's gate — its blocking edges — is not yet wired, so
// neither implement nor reassess may dispatch until the decomposer
// removes the label. The reason names the seeding lifecycle, not a
// never- claim: the hold-off is a window the decomposer opens and
// closes, not a property of the issue. The reason names the label that
// was hit, so the two stay tied together as the list grows
// (§forgejo/decisions/transition-table, s-git/maitred#125).
func holdOffForestSeeding(number int, label string) Decision {
	return holdOff(fmt.Sprintf("issue %d carries %s; forest is seeding, not yet gated", number, label))
}

// holdOffScopeUnknown is the scope test's failure hold-off: the
// repository listing failed, so the engine cannot tell whether the
// repository is in its remit. The reason names the listing failure,
// never the topic, so a transient API failure does not read in the
// decision log as a deliberate exclusion
// (§forgejo/webhook/the-scope-cache, §forgejo/observability/the-decision-log).
func holdOffScopeUnknown(repo string, err error) Decision {
	return holdOff(fmt.Sprintf("scope of repo %s unknown: repository listing failed: %v", repo, err))
}
