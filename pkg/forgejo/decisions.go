package forgejo

import (
	"fmt"
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
)

// The Forgejo review states the decision function reacts to
// (§forgejo/client/operations).
const (
	ReviewApproved         = "APPROVED"
	ReviewChangesRequested = "CHANGES_REQUESTED"
)

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
	// EventIssueOpened, for a pull request like EventPRSynced.
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
	// CascadeRoot is the closed issue the unblock cascade walks from,
	// for close events.
	CascadeRoot *IssueNode
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
	// Cascade lists the issues the unblock cascade now unblocks (close
	// events only); each warrants a dispatch of ActionImplement. The
	// engine applies each key's own watermark before dispatching, so
	// the same (action, revision) is not re-dispatched.
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
	case EventIssueOpened, EventReconcile:
		if e.Kind == KindPR {
			return decidePRSynced(e, s, w)
		}
		return decideIssueOpened(e, s, w)
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
// (§forgejo/decisions/transition-table).
func decideIssueActivity(e Event, s State, w *Watermark) Decision {
	issue := s.Issue
	if issue == nil {
		return holdOff("no issue state re-fetched")
	}
	if issue.State != "open" {
		return holdOff(fmt.Sprintf("issue %d is %q, not open", issue.Number, issue.State))
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

// decidePRReviewed handles a submitted review: the re-fetched latest
// review decides — changes-requested dispatches fix-feedback, approved
// dispatches merge-or-wait, anything else holds off
// (§forgejo/decisions/transition-table).
func decidePRReviewed(e Event, s State, w *Watermark) Decision {
	pr := s.PR
	if pr == nil || !prOpen(pr) {
		return holdOff("PR is not open")
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
// returns the issues that are now unblocked — open, with all their
// blockers closed — each exactly once. The walk is transitive (a
// blocked issue that is itself closed cascades onward) and terminates
// on cycles via a visited set
// (§forgejo/decisions/unblock-cascade).
func unblockCascade(root *IssueNode) []Key {
	if root == nil {
		return nil
	}
	var unblocked []Key // nil when nothing is unblocked
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
				if allClosed(b.Blockers) {
					unblocked = append(unblocked, k)
				}
			}
		}
	}
	walk(root)
	return unblocked
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
// action at revision — the idempotency guard: the same (event, state,
// revision) does not re-dispatch the same action
// (§forgejo/decisions/loop-prevention).
func alreadyDispatched(w *Watermark, action Action, revision string) bool {
	return w != nil && w.Action == string(action) && w.Revision == revision
}

func dispatch(action Action, reason string) Decision {
	return Decision{Dispatched: true, Action: action, Reason: reason}
}

func holdOff(reason string) Decision {
	return Decision{Reason: reason}
}
