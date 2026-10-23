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
	ActionImplement   Action = "implement"
	ActionReassess    Action = "reassess"
	ActionReview      Action = "review"
	ActionReReview    Action = "re-review"
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
	// ActionRejectPR closes a PR opened against work that is not yet
	// commissioned (§forgejo/decisions/actions).
	ActionRejectPR Action = "reject-pr"
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
)

// LabelHumanTask marks a ticket claimed by a human: an issue carrying
// it is never an implement candidate anywhere in the engine
// (§forgejo/decisions/transition-table).
const LabelHumanTask = "human-task"

// neverWorkItemLabel returns the issue label that denies the
// implement and reassess actions, or "" when the issue may be an
// implement or reassess candidate. Label names match
// case-insensitively, and ideation is reported first: the
// decompose-wins precedence applies to the hold-off reason too
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
	// with its own blockers: the input both to the reject-pr test
	// (§forgejo/decisions/actions) and to the merge gate
	// (§forgejo/decisions/merge-gate). Read for PR opened, PR reviewed,
	// and the sweep's synthetic PR event; empty when the PR's opening
	// recorded no dependency.
	ConnectedIssues []ConnectedIssue
	// SyncMembers are the blocking issues of the sync issue in
	// State.Issue, each with the state of its delivering PR: the sync
	// predicate's input, fetched when the issue under consideration
	// carries the sync label (§forgejo/decisions/sync-set).
	SyncMembers []SyncMember
	// DecomposerIdentity is the configured login of the identity that
	// performs decomposition (§forgejo/config/decomposer-identity). It is
	// a declared configuration input the engine passes verbatim from its
	// config, not fetched state; when empty the authorship rule is inert
	// (§forgejo/decisions/inputs).
	DecomposerIdentity string
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
// issues that block it: the input to the reject-pr test and to the
// merge gate. The relationship is recorded on the blocker graph, never
// inferred from the PR body (§forgejo/webhook/re-fetch,
// §forgejo/decisions/actions, §forgejo/decisions/merge-gate).
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
		return considerSyncIssue(issue, s.SyncMembers, w)
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
// An issue authored by the configured decomposer identity is never an
// implement candidate on either path: it waits for the unblock cascade
// alone (§forgejo/decisions/transition-table,
// §forgejo/decisions/loop-prevention).
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
	if hasLabel(issue, LabelTeardownForest) {
		return holdOffTeardownWithoutRoot(issue.Number)
	}
	if decomposerAuthored(issue, s.DecomposerIdentity) {
		return holdOffDecomposerAuthored(issue.Number, issue.Author)
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
// the first trigger — while a comment is not a revision of intent and
// holds off (§forgejo/decisions/transition-table); otherwise dispatch
// reassess if the re-fetched issue is open and has no open blocker;
// activity on a blocked issue has nothing to reassess — the work is
// held by the blocker, so the decision holds off naming the open
// blockers.
func decideIssueActivity(e Event, s State, w *Watermark) Decision {
	issue := s.Issue
	if issue == nil {
		return holdOff("no issue state re-fetched")
	}
	if issue.State != "open" {
		return holdOff(fmt.Sprintf("issue %d is %q, not open", issue.Number, issue.State))
	}
	if hasLabel(issue, LabelIdeation) {
		if e.Type == EventIssueCommented {
			return holdOff(fmt.Sprintf("issue %d is an open %s root; a comment is not a revision of intent", issue.Number, LabelIdeation))
		}
		return decideDecompose(e, issue, s, w)
	}
	if label := neverWorkItemLabel(issue.Labels); label != "" {
		return holdOffNeverReassess(issue.Number, label)
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
	return dispatch(ActionDecompose, fmt.Sprintf("issue %d is an open %s root: the label makes the root", issue.Number, LabelIdeation))
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
	// The reject-pr test precedes review and rebase: reviewing work
	// that must not exist wastes a reviewer and produces a diff nobody
	// asked for (§forgejo/decisions/transition-table).
	if d, rejected := decideRejectPR(pr, s, w); rejected {
		return d
	}
	if !pr.Mergeable {
		if alreadyDispatched(w, ActionRebase, pr.HeadSHA) {
			return holdOff(fmt.Sprintf("rebase already dispatched at revision %s", pr.HeadSHA))
		}
		return dispatch(ActionRebase, fmt.Sprintf("PR %d opened with merge conflicts", pr.Number))
	}
	if alreadyDispatched(w, ActionReview, pr.HeadSHA) {
		return holdOff(fmt.Sprintf("review already dispatched at revision %s", pr.HeadSHA))
	}
	return dispatch(ActionReview, fmt.Sprintf("PR %d opened", pr.Number))
}

// decideRejectPR tests whether a PR is work started before its forest was
// commissioned, ahead of the review, rebase, fix and merge decisions: one of
// its connected issues — every non-PR issue the PR blocks is tested,
// so the decision does not depend on the order the relationships are
// returned in — blocked by an **open** issue carrying the reserved
// ideation label (§forgejo/decisions/commissioning,
// §forgejo/decisions/actions). It is consulted by the PR-opened,
// PR-reviewed and reconcile paths, the paths that can dispatch review,
// re-review, fix-feedback or merge-or-wait; the PR synced event decides
// without it (§forgejo/decisions/transition-table). A PR whose
// connected issues have no such blocker — or with no connected issue at
// all, because its opening recorded no dependency — is not rejected: a
// *closed* ideation blocker means the forest is commissioned, and an
// unlabelled blocker is ordinary work, both decided on as review or
// rebase. A connected issue's **own** state is not consulted: the gate
// is the blocker's, so a PR opened against a closed connected issue
// still blocked by an open ideation issue is rejected. When the test
// does not apply, ok is false and the calling path decides on
// (§forgejo/decisions/transition-table).
func decideRejectPR(pr *PullRequest, s State, w *Watermark) (Decision, bool) {
	if len(s.ConnectedIssues) == 0 {
		return Decision{}, false
	}
	for i := range s.ConnectedIssues {
		c := &s.ConnectedIssues[i]
		for j := range c.Blockers {
			b := &c.Blockers[j]
			if b.State != "open" {
				continue
			}
			labeled := false
			for _, l := range b.Labels {
				if l == LabelIdeation {
					labeled = true
					break
				}
			}
			if !labeled {
				continue
			}
			if alreadyDispatched(w, ActionRejectPR, pr.HeadSHA) {
				return holdOff(fmt.Sprintf("reject-pr already dispatched at revision %s", pr.HeadSHA)), true
			}
			return dispatch(ActionRejectPR, fmt.Sprintf("PR %d is connected to issue %d, blocked by open ideation issue %d: work started before its forest was commissioned", pr.Number, c.Issue.Number, b.Number)), true
		}
	}
	return Decision{}, false
}

// decidePRSynced handles new commits on a PR (or the reconciliation
// sweep's synthetic current-state event): dispatch rebase when the
// re-fetched PR is not mergeable, re-review when it was already
// reviewed, review otherwise. A commit pushed by the PR's reviewing
// author does not re-review the reviewer's own push
// (§forgejo/decisions/loop-prevention). A PR carrying the
// trigger-review label is the explicit request for an agentic review:
// the review is dispatched for the current revision whatever the
// review state would otherwise produce — the reviewing-author sender
// guard does not suppress it, since the label is a human request, not
// the author's own push — while the revision-keyed watermark guard
// keeps the label merely present from re-firing
// (§forgejo/decisions/label-triggered-review).
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
	action := ActionReview
	if len(s.Reviews) > 0 {
		action = ActionReReview
	}
	if carriesLabel(pr.Labels, LabelTriggerReview) {
		if alreadyDispatched(w, action, pr.HeadSHA) {
			return holdOff(fmt.Sprintf("%s already dispatched at revision %s", action, pr.HeadSHA))
		}
		return dispatch(action, fmt.Sprintf("PR %d carries the %s label: explicit request for a review at head %s", pr.Number, LabelTriggerReview, pr.HeadSHA))
	}
	if alreadyDispatched(w, action, pr.HeadSHA) {
		return holdOff(fmt.Sprintf("%s already dispatched at revision %s", action, pr.HeadSHA))
	}
	if latest := latestReview(s.Reviews); latest != nil && latest.Author == e.Sender {
		return holdOff(fmt.Sprintf("PR %d commit pushed by the reviewing author %s; not re-reviewing the reviewer's own push", pr.Number, e.Sender))
	}
	return dispatch(action, fmt.Sprintf("PR %d synced (head %s)", pr.Number, pr.HeadSHA))
}

// decidePRReconcile handles the reconciliation sweep's synthetic
// current-state event for a pull request: like PR synced, except that
// the reject-pr test is applied first — the sweep's re-fetch performs
// the same one hop, so a blocked PR is rejected rather than reviewed
// or merge-or-waited by the sweep — and except when the last activity
// is a review — the latest review is a changes-requested, approved, or
// comment review submitted against the PR's current head (no commit
// has been pushed since) — in which case the decision follows the
// latest review: changes-requested and comment dispatch fix-feedback,
// approved dispatches merge-or-wait — held off, naming the open
// blockers, when a connected issue has one
// (§forgejo/decisions/merge-gate). A comment review holds off on the
// event path, but the sweep moves the PR along by triggering the
// implementer, not the reviewer (§forgejo/decisions/transition-table).
func decidePRReconcile(e Event, s State, w *Watermark) Decision {
	pr := s.PR
	if pr != nil && prOpen(pr) {
		// The reject-pr test precedes the whole PR path, review-
		// following and synced-like alike
		// (§forgejo/decisions/transition-table).
		if d, rejected := decideRejectPR(pr, s, w); rejected {
			return d
		}
	}
	if pr != nil && prOpen(pr) && pr.Mergeable {
		if latest := latestReview(s.Reviews); latest != nil && latest.CommitID == pr.HeadSHA {
			switch latest.Event {
			case ReviewChangesRequested:
				if alreadyDispatched(w, ActionFixFeedback, pr.HeadSHA) {
					return holdOff(fmt.Sprintf("fix-feedback already dispatched at revision %s", pr.HeadSHA))
				}
				return dispatch(ActionFixFeedback, fmt.Sprintf("PR %d stands at a changes-requested review by %s", pr.Number, latest.Author))
			case ReviewComment:
				if alreadyDispatched(w, ActionFixFeedback, pr.HeadSHA) {
					return holdOff(fmt.Sprintf("fix-feedback already dispatched at revision %s", pr.HeadSHA))
				}
				return dispatch(ActionFixFeedback, fmt.Sprintf("PR %d stands at a comment review by %s", pr.Number, latest.Author))
			case ReviewApproved:
				if d, ok := syncMemberSetHoldOff(pr, s); ok {
					return d
				}
				if open := mergeGateOpenBlockers(e, s); len(open) > 0 {
					return holdOff(fmt.Sprintf("PR %d stands at an approved review, but a connected issue is blocked by %s; not merging", pr.Number, blockerNames(open)))
				}
				if alreadyDispatched(w, ActionMergeOrWait, pr.HeadSHA) {
					return holdOff(fmt.Sprintf("merge-or-wait already dispatched at revision %s", pr.HeadSHA))
				}
				return dispatch(ActionMergeOrWait, fmt.Sprintf("PR %d stands at an approved review by %s", pr.Number, latest.Author))
			}
		}
	}
	return decidePRSynced(e, s, w)
}

// decidePRReviewed handles a submitted review: the reject-pr test is
// applied first, so a gate that closed after the PR opened cannot be
// walked through by the review-following decisions; then the
// re-fetched latest review decides — changes-requested dispatches
// fix-feedback, approved dispatches merge-or-wait only when the merge
// gate lets it through, no connected issue having an open blocker
// (§forgejo/decisions/merge-gate), anything else holds off
// (§forgejo/decisions/transition-table).
func decidePRReviewed(e Event, s State, w *Watermark) Decision {
	pr := s.PR
	if pr == nil || !prOpen(pr) {
		return holdOff("PR is not open")
	}
	// The reject-pr test precedes the review-following decisions:
	// merging work that must not exist is the outcome the gate exists
	// to prevent (§forgejo/decisions/transition-table).
	if d, rejected := decideRejectPR(pr, s, w); rejected {
		return d
	}
	latest := latestReview(s.Reviews)
	if latest == nil {
		return holdOff("no reviews on re-fetch")
	}
	switch latest.Event {
	case ReviewChangesRequested:
		if alreadyDispatched(w, ActionFixFeedback, pr.HeadSHA) {
			return holdOff(fmt.Sprintf("fix-feedback already dispatched at revision %s", pr.HeadSHA))
		}
		return dispatch(ActionFixFeedback, fmt.Sprintf("PR %d: review by %s requests changes", pr.Number, latest.Author))
	case ReviewApproved:
		if d, ok := syncMemberSetHoldOff(pr, s); ok {
			return d
		}
		if open := mergeGateOpenBlockers(e, s); len(open) > 0 {
			return holdOff(fmt.Sprintf("PR %d stands at an approved review, but a connected issue is blocked by %s; not merging", pr.Number, blockerNames(open)))
		}
		if alreadyDispatched(w, ActionMergeOrWait, pr.HeadSHA) {
			return holdOff(fmt.Sprintf("merge-or-wait already dispatched at revision %s", pr.HeadSHA))
		}
		return dispatch(ActionMergeOrWait, fmt.Sprintf("PR %d: review by %s approves", pr.Number, latest.Author))
	default:
		return holdOff(fmt.Sprintf("PR %d: latest review is %q; no action", pr.Number, latest.Event))
	}
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
				// as a sync-set candidate whatever its readiness —
				// the set's own re-fetched evaluation decides, and
				// an unready one stays silent
				// (§forgejo/decisions/sync-set).
				if hasLabel(&b.Issue, LabelSync) {
					emissions = append(emissions, CascadeDispatch{Key: k, Action: ActionSync})
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
// never an implement candidate, the cascade included. A wrap-up
// emission demands the issue still be an outcome tracker without
// ideation; otherwise the sweep decides the issue afresh
// (§forgejo/decisions/unblock-cascade).
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
	return Decision{}, true
}

// issueBlockers returns the non-pull-request entries of a blocker
// list — the issue-side blockers the cascade and the reject-pr test
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
// the merge gate: when any connected issue of the PR is a member of
// an open landing set, landing is the set's decision, not the
// individual PR's — merge-or-wait must never merge it. The hold-off
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
	var members, dualLabelled []string
	var emissions []CascadeDispatch
	seen := map[Key]bool{}
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
			set := &c.SyncSets[j]
			k := issueKey(set)
			if seen[k] {
				continue // one emission per grouping issue, even from several members
			}
			seen[k] = true
			emissions = append(emissions, CascadeDispatch{Key: k, Action: ActionSync})
		}
	}
	if len(members) == 0 {
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
	if len(dualLabelled) > 0 {
		reason = fmt.Sprintf("PR %d stands at an approved review, but its connected issue %s carries both %s and %s; a member is opted into the set, never into merging on its own — the double label is an authoring error the decomposer must retire", pr.Number, strings.Join(dualLabelled, ", "), LabelAgenticSyncMerge, LabelAgenticAutoMerge)
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
// mergeable while carrying agentic-sync-merge. The dispatch is
// recorded against a revision derived from the whole set, so the
// sweep does not re-fire at an unchanged set state
// (§forgejo/decisions/sync-set).
func considerSyncIssue(issue *Issue, members []SyncMember, w *Watermark) Decision {
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
		switch {
		case !hasLabel(&m.Issue, LabelAgenticSyncMerge):
			notReady = append(notReady, fmt.Sprintf("%s is open without the %s label: a member awaiting a human must be closed before the set lands", ref, LabelAgenticSyncMerge))
		case m.PR == nil || !prOpen(m.PR) || !m.PR.Mergeable:
			notReady = append(notReady, fmt.Sprintf("%s has no mergeable open PR", ref))
		default:
			if latest := latestReview(m.Reviews); latest == nil || latest.Event != ReviewApproved || latest.CommitID != m.PR.HeadSHA {
				notReady = append(notReady, fmt.Sprintf("%s's PR %d is not approved at head %s", ref, m.PR.Number, m.PR.HeadSHA))
			}
		}
	}
	if len(notReady) > 0 {
		// An unready set is not an event worth a comment: the hold-off
		// is silent, at home only in the decision log
		// (§forgejo/decisions/sync-set, §forgejo/observability/the-decision-log).
		return holdOff(fmt.Sprintf("sync issue %d held off: %s", issue.Number, strings.Join(notReady, "; ")))
	}
	rev := syncSetRevision(members)
	if alreadyDispatched(w, ActionSync, rev) {
		return holdOff(fmt.Sprintf("sync already dispatched at set revision %s", rev))
	}
	return dispatch(ActionSync, fmt.Sprintf("issue %d is an open %s issue: all %d member(s) closed or approved, mergeable, and opted in; the state calls for sync at set revision %s", issue.Number, LabelSync, len(members), rev))
}

// syncSetRevision derives the set revision the sync dispatch's
// watermark records: the members keyed by repository and number, each
// at its issue revision and PR head sha, ordered by key so the API's
// enumeration order cannot change the digest
// (§forgejo/decisions/sync-set, §forgejo/state/watermark).
func syncSetRevision(members []SyncMember) string {
	lines := make([]string, 0, len(members))
	for i := range members {
		m := &members[i]
		line := fmt.Sprintf("%s#%d@%s", m.Issue.Repository, m.Issue.Number, issueRevision(&m.Issue))
		if m.PR != nil {
			line += "@" + m.PR.HeadSHA
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

// latestReview returns the PR's most recently submitted review, or nil
// when it has none.
func latestReview(reviews []Review) *Review {
	var latest *Review
	for i := range reviews {
		if latest == nil || reviews[i].SubmittedAt.After(latest.SubmittedAt) {
			latest = &reviews[i]
		}
	}
	return latest
}

// issueRevision is the issue's activity revision: its updated_at in
// RFC 3339 (§forgejo/state/watermark).
func issueRevision(issue *Issue) string {
	return issue.UpdatedAt.UTC().Format(time.RFC3339)
}

// alreadyDispatched reports whether the watermark already records
// action at revision — the idempotency guard, revision-keyed on every
// path: the same (event, state, revision) does not re-dispatch the same
// action (§forgejo/decisions/loop-prevention). The watermark exemption
// is not implemented here: it is scoped to the sweep, whose exempt rows
// in decideIssueReconcile simply never consult the watermark
// (§forgejo/reconciliation/watermark-exemptions). Keying the bypass on
// the action instead of the path would hand every future event row for
// those actions a suppression-free path by accident.
func alreadyDispatched(w *Watermark, action Action, revision string) bool {
	return w != nil && w.Action == string(action) && w.Revision == revision
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
		ActionReReview:    true,
		ActionFixFeedback: true,
		ActionMergeOrWait: true,
		ActionRebase:      true,
		ActionRejectPR:    true,
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
// insurance hold-offs (§forgejo/decisions/transition-table).
func holdOffNeverImplement(number int, label string) Decision {
	return holdOff(fmt.Sprintf("issue %d carries the %s label; never an implement candidate", number, label))
}

func holdOffNeverReassess(number int, label string) Decision {
	return holdOff(fmt.Sprintf("issue %d carries the %s label; never a reassess candidate", number, label))
}

// decomposerAuthored reports whether the issue was opened by the
// configured decomposer identity. The match is exact — no case folding,
// no substring — and an unset identity (or an issue with no author) never
// matches, which keeps the rule inert
// (§forgejo/config/decomposer-identity, §forgejo/decisions/loop-prevention).
func decomposerAuthored(issue *Issue, identity string) bool {
	return identity != "" && issue.Author == identity
}

// holdOffDecomposerAuthored is the authorship hold-off: the issue was
// authored by the configured decomposer identity, so it is a forest
// member that the unblock cascade unblocks, never the event path or the
// sweep (§forgejo/decisions/loop-prevention).
func holdOffDecomposerAuthored(number int, author string) Decision {
	return holdOff(fmt.Sprintf("issue %d is authored by the decomposer %s; held for the unblock cascade, not dispatched as an implement candidate", number, author))
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
