package forgejo

import (
	"fmt"
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
	// ActionDecompose and ActionWrapUp are the watermark-exempt
	// actions: their watermark is the state, not the revision
	// (§forgejo/reconciliation/watermark-exemptions).
	ActionDecompose Action = "decompose"
	ActionWrapUp    Action = "wrap-up"
	// ActionRejectPR closes a PR opened against unreleased work
	// (§forgejo/decisions/actions).
	ActionRejectPR Action = "reject-pr"
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
	// CascadeRoot is the closed issue the unblock cascade walks from,
	// for close events.
	CascadeRoot *IssueNode
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
	// Cascade lists the objects the unblock cascade now releases (close
	// events only): newly unblocked issues, each warranting a dispatch
	// of ActionImplement, and the connected open PRs of issues whose
	// remaining open blockers are only those PRs, each warranting a
	// dispatch of ActionMergeOrWait
	// (§forgejo/decisions/unblock-cascade). The engine applies each
	// key's own watermark before dispatching, so the same (action,
	// revision) is not re-dispatched.
	Cascade []Key
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
	default:
		return holdOff(fmt.Sprintf("unhandled event type %q", e.Type))
	}
}

// decideIssueReconcile handles the reconciliation sweep's synthetic
// current-state event for an issue. The watermark-exempt rows come
// first, and they are the exemption: unlike every other path, they
// decide without consulting the watermark, so an open ideation root
// dispatches decompose and an open, fully unblocked outcome tracker
// dispatches wrap-up, revision notwithstanding — state is the watermark
// (§forgejo/reconciliation/watermark-exemptions). These two rows are
// the exempt set: the exemption is scoped to the sweep, and an exempt
// action reached from an event keeps the revision-keyed guard like any
// other action. A blocked tracker holds off naming its open blockers, so
// the decision log reads "state calls for no action", never "already
// dispatched at this revision". An issue carrying neither reserved label
// decides as issue opened (§forgejo/decisions/transition-table).
func decideIssueReconcile(e Event, s State, w *Watermark) Decision {
	issue := s.Issue
	if issue == nil || issue.State != "open" {
		return decideIssueOpened(e, s, w) // the same hold-offs as the event path
	}
	if hasLabel(issue, LabelIdeation) {
		// A root is decomposed even when a tracker's wrap-up would
		// also apply: decompose wins over wrap-up
		// (§forgejo/reconciliation/watermark-exemptions).
		return dispatch(ActionDecompose, fmt.Sprintf("issue %d is an open %s issue; the state calls for decompose regardless of revision %s", issue.Number, LabelIdeation, issueRevision(issue)))
	}
	if hasLabel(issue, LabelOutcome) {
		if open := openBlockers(s.Blockers); len(open) > 0 {
			return holdOff(fmt.Sprintf("issue %d is an open %s tracker blocked by %d open issue(s); state calls for no action", issue.Number, LabelOutcome, len(open)))
		}
		return dispatch(ActionWrapUp, fmt.Sprintf("issue %d is an open, fully unblocked %s tracker; the state calls for wrap-up regardless of revision %s", issue.Number, LabelOutcome, issueRevision(issue)))
	}
	return decideIssueOpened(e, s, w)
}

// decideIssueOpened handles an issue that was opened (or the
// reconciliation sweep's synthetic current-state event): dispatch
// implement if the re-fetched issue is open, unblocked, and has no
// connected PR (§forgejo/decisions/transition-table).
func decideIssueOpened(e Event, s State, w *Watermark) Decision {
	issue := s.Issue
	if issue == nil {
		return holdOff("no issue state re-fetched")
	}
	if issue.State != "open" {
		return holdOff(fmt.Sprintf("issue %d is %q, not open", issue.Number, issue.State))
	}
	if label := neverWorkItemLabel(issue.Labels); label != "" {
		return holdOffNeverImplement(issue.Number, label)
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
// feedback comment: dispatch reassess if the re-fetched issue is open
// and has no open blocker; activity on a blocked issue has nothing to
// reassess — the work is held by the blocker, so the decision holds
// off naming the open blockers (§forgejo/decisions/transition-table).
func decideIssueActivity(e Event, s State, w *Watermark) Decision {
	issue := s.Issue
	if issue == nil {
		return holdOff("no issue state re-fetched")
	}
	if issue.State != "open" {
		return holdOff(fmt.Sprintf("issue %d is %q, not open", issue.Number, issue.State))
	}
	if label := neverWorkItemLabel(issue.Labels); label != "" {
		return holdOffNeverReassess(issue.Number, label)
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

// decideIssueClosed runs the unblock cascade from the closed issue
// (§forgejo/decisions/unblock-cascade).
func decideIssueClosed(s State) Decision {
	if s.Issue == nil || s.Issue.State != "closed" {
		return holdOff("issue is not closed on re-fetch")
	}
	d := holdOff(fmt.Sprintf("issue %d closed; unblock cascade", s.Issue.Number))
	d.Cascade = unblockCascade(s.CascadeRoot)
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

// decideRejectPR tests whether a PR is work started before its release
// gate, ahead of the review, rebase, fix and merge decisions: one of
// its connected issues — every non-PR issue the PR blocks is tested,
// so the decision does not depend on the order the relationships are
// returned in — blocked by an **open** issue carrying the reserved
// ideation label (§forgejo/decisions/actions). It is consulted by the
// PR-opened, PR-reviewed and reconcile paths, the paths that can
// dispatch review, re-review, fix-feedback or merge-or-wait; the PR
// synced event decides without it (§forgejo/decisions/transition-table).
// A PR whose connected issues have no such blocker — or with no
// connected issue at all, because the PR's opening recorded no
// dependency — is not rejected: a *closed* ideation blocker means the
// release gate has opened, and an unlabelled blocker is ordinary work,
// both decided on as review or rebase. A connected issue's **own**
// state is not consulted: the gate is the blocker's, so a PR opened
// against a closed connected issue still blocked by an open ideation
// issue is rejected. When the test does not apply, ok is false and the
// calling path decides on (§forgejo/decisions/transition-table).
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
			return dispatch(ActionRejectPR, fmt.Sprintf("PR %d is connected to issue %d, blocked by open ideation issue %d: work started before its release gate", pr.Number, c.Issue.Number, b.Number)), true
		}
	}
	return Decision{}, false
}

// decidePRSynced handles new commits on a PR (or the reconciliation
// sweep's synthetic current-state event): dispatch rebase when the
// re-fetched PR is not mergeable, re-review when it was already
// reviewed, review otherwise. A commit pushed by the PR's reviewing
// author does not re-review the reviewer's own push
// (§forgejo/decisions/loop-prevention).
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
	d.Cascade = unblockCascade(s.CascadeRoot)
	return d
}

// unblockCascade walks the blocker graph from the closed issue and
// returns the objects it now releases — open issues with all their
// issue-side blockers closed (dispatched implement) and, for issues
// whose remaining open blockers are only their connected open PRs, those
// PRs (dispatched merge-or-wait) — each exactly once. The walk is
// transitive (a blocked issue that is itself closed cascades onward) and
// terminates on cycles via a visited set
// (§forgejo/decisions/unblock-cascade).
func unblockCascade(root *IssueNode) []Key {
	if root == nil {
		return nil
	}
	var unblocked []Key // nil when nothing is unblocked
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
				// An issue whose remaining open blockers are its
				// connected open PRs is not an implement candidate —
				// the work exists as a PR, and the closure that
				// removed the last issue-side blocker releases that
				// PR instead (§forgejo/decisions/unblock-cascade).
				if prs := openPullBlockers(b.Blockers); len(prs) > 0 {
					if allClosed(issueBlockers(b.Blockers)) {
						for _, p := range prs {
							pk := Key{Repo: p.Repository, Kind: KindPR, Number: p.Number}
							if seenPR[pk] {
								continue // released once, even by several issues
							}
							seenPR[pk] = true
							unblocked = append(unblocked, pk)
						}
					}
				} else if allClosed(b.Blockers) {
					unblocked = append(unblocked, k)
				}
			}
		}
	}
	walk(root)
	return unblocked
}

// issueBlockers returns the non-pull-request entries of a blocker
// list — the issue-side blockers the cascade and the PR release test
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
	for _, l := range issue.Labels {
		if strings.EqualFold(l, label) {
			return true
		}
	}
	return false
}

// reservedLabel returns the reserved watermark-exempt label the issue
// carries, or the empty string when it carries none. Ideation is
// reported first: the decompose-wins precedence applies to the hold-off
// reason too (§forgejo/reconciliation/watermark-exemptions).
func reservedLabel(issue *Issue) string {
	switch {
	case hasLabel(issue, LabelIdeation):
		return LabelIdeation
	case hasLabel(issue, LabelOutcome):
		return LabelOutcome
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
// (§forgejo/webhook/repository-roles). The outcomes actions are
// reserved vocabulary here: the outcomes chain (#83, #84, #85)
// introduces their constants and dispatches them.
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
		"decompose": true,
		"wrap-up":   true,
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

// holdOffScopeUnknown is the scope test's failure hold-off: the
// repository listing failed, so the engine cannot tell whether the
// repository is in its remit. The reason names the listing failure,
// never the topic, so a transient API failure does not read in the
// decision log as a deliberate exclusion
// (§forgejo/webhook/the-scope-cache, §forgejo/observability/the-decision-log).
func holdOffScopeUnknown(repo string, err error) Decision {
	return holdOff(fmt.Sprintf("scope of repo %s unknown: repository listing failed: %v", repo, err))
}
