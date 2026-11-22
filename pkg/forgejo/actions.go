package forgejo

import (
	"fmt"
	"strings"
)

// Action declarations (§forgejo/decisions/actions).
//
// Every action the decision function can name carries exactly one
// declaration: the input set its predicate reads, the ready predicate
// over that set, and the digest a dispatch is watermarked on. The
// decision path reaches an action's inputs, predicate and digest
// through this table — the rows in decisions.go neither restate the
// digest beside a dispatch nor carry a private readiness rule the
// declaration does not name.
//
// The predicate answers the question "does this state call for this
// action", one answer whichever path asks it; how a ready row is
// paced (a revision-keyed guard, an exemption, the re-fire bound)
// belongs to the row, not the predicate (§forgejo/decisions/loop-prevention,
// §forgejo/reconciliation/the-state-is-the-watermark). The digest is
// the revision the watermark records under the action's name
// (§forgejo/state/watermark); which fact each action keys on today is
// what the declarations state, and a later change to that key edits
// exactly one line (§forgejo/decisions/transition-table).

// readyAnswer is the predicate's verdict: the state calls for the
// action (readyYes), does not (readyNo), or the predicate abstains and
// the asking row continues to its next question (readyNotApplicable).
type readyAnswer int

const (
	readyNo readyAnswer = iota
	readyYes
	readyNotApplicable
)

// actionDecl is the one declaration of one action.
type actionDecl struct {
	// inputs names every input fact the predicate reads, in the
	// vocabulary of §forgejo/decisions/inputs (state fields, event
	// fields, and the declared configuration inputs the engine passes
	// verbatim). It is the set the refusal of undeclared reads will
	// one day check against; today it is the action's declaration of
	// what it looks at, in one place.
	inputs []string
	// ready is the action's predicate. The returned Decision carries
	// the dispatch reason (readyYes) or the hold-off reason
	// (readyNo), cascade included. The predicate must not consult the
	// watermark: pacing is the row's.
	ready func(Event, State) (Decision, readyAnswer)
	// digest derives the revision a dispatch of this action is
	// watermarked on (§forgejo/state/watermark).
	digest func(Event, State) string
	// components is the fold's component set, declared by the action
	// whose dispatch the fold gates: the `decompose` declaration owns
	// it and the digest reads it from here, so no list lives beside
	// the fold (§forgejo/decisions/actionable-state-digest). Nil for
	// every other action.
	components []digestComponent
}

// actionDecls is the declaration table: one entry per action
// (§forgejo/decisions/actions). Filled in init rather than a composite
// literal so the predicates may themselves ask a declared digest
// without tripping the compiler's initialization cycle.
var actionDecls map[Action]actionDecl

func init() {
	actionDecls = map[Action]actionDecl{
		ActionImplement: {
			inputs: []string{
				"issue.state", "issue.labels", "issue.updated_at",
				"blockers.states", "connected PRs",
			},
			ready:  implementReady,
			digest: issueDigest,
		},
		ActionReassess: {
			inputs: []string{
				"issue.state", "issue.labels", "issue.updated_at",
				"blockers.states", "event.type",
			},
			ready:  reassessReady,
			digest: issueDigest,
		},
		ActionReview: {
			inputs: []string{
				"PR.state", "PR.mergeable", "PR.head sha", "PR.labels",
				"reviews", "config.review lenses", "event.type", "event.sender",
			},
			ready:  reviewReady,
			digest: prHeadDigest,
		},
		ActionFixFeedback: {
			inputs: []string{
				"PR.state", "PR.mergeable", "PR.head sha", "PR.author",
				"reviews", "config.review lenses", "config.implementer identity",
				"event.type",
			},
			ready:  fixFeedbackReady,
			digest: prHeadDigest,
		},
		ActionMergeOrWait: {
			inputs: []string{
				"PR.state", "PR.mergeable", "PR.head sha", "reviews",
				"connected issues", "connected issues' blockers",
				"connected issues' sync sets", "sync sets",
				"config.review lenses", "event.kind", "event.repo", "event.number",
			},
			ready:  mergeOrWaitReady,
			digest: prHeadDigest,
		},
		ActionRebase: {
			inputs: []string{"PR.state", "PR.mergeable", "PR.head sha", "event.type"},
			ready:  rebaseReady,
			digest: prHeadDigest,
		},
		ActionDecompose: {
			inputs: []string{
				"issue.state", "issue.labels", "issue.body",
				"blockers.states", "forest digest gate",
				"event.type", "event.sender",
			},
			ready:      decomposeReady,
			digest:     issueDigest,
			components: digestComponentSet,
		},
		ActionWrapUp: {
			inputs: []string{
				"issue.state", "issue.labels", "issue.updated_at",
				"blockers.states",
			},
			ready:  wrapUpReady,
			digest: issueDigest,
		},
		ActionTeardownForest: {
			inputs: []string{
				"issue.state", "issue.labels", "issue.updated_at",
				"blocked downstream edges",
			},
			ready:  teardownForestReady,
			digest: issueDigest,
		},
		ActionSync: {
			inputs: []string{
				"issue.state", "issue.labels", "issue.body", "sync members",
				"members' PRs", "members' PR reviews", "config.review lenses",
			},
			ready:  syncReady,
			digest: syncSetDigest,
		},
	}
}

// digestComponent is one component of a forest's actionable state:
// its key — the first part of each folded fact of that component, so
// the fold groups by it for free — and the phrase the dispatch reason
// uses to name it, so a run firing at an unexpected time can be
// diagnosed (§forgejo/decisions/actionable-state-digest,
// s-git/maitred#209). The component set is declared with the
// `decompose` action below, in reporting order; key and phrase travel
// together so no component can gain a key without a name.
type digestComponent struct {
	key    string
	phrase string
}

// digestComponentSet is the `decompose` fold's component set: the
// components of a forest's actionable state, in reporting order; key
// and phrase travel together so no component can gain a key without a
// name (§forgejo/decisions/actionable-state-digest, s-git/maitred#209).
var digestComponentSet = []digestComponent{
	{"root", "the root's state, labels or body"},
	{"member", "a member's state or kind/landing labels"},
	{"tracker", "a tracker's state"},
	{"blocker", "a blocker's state"},
	{"comment", "a comment authored outside the engine"},
}

// declarationFor returns an action's declaration, panicking on an
// undeclared action: every caller passes a constant the decision
// function dispatches, so a miss here is a programming error, not a
// runtime state (§forgejo/decisions/actions).
func declarationFor(a Action) actionDecl {
	decl, ok := actionDecls[a]
	if !ok {
		panic("forgejo: action declaration: undeclared action " + string(a))
	}
	return decl
}

// readyTo asks an action's declared predicate whether the state calls
// for it. The Decision carries the reason either answer owes the
// decision log (§forgejo/observability/the-decision-log).
func readyTo(a Action, e Event, s State) (Decision, readyAnswer) {
	return declarationFor(a).ready(e, s)
}

// digestFor derives the revision a dispatch of the action is
// watermarked on, through the action's declaration
// (§forgejo/state/watermark).
func digestFor(a Action, e Event, s State) string {
	return declarationFor(a).digest(e, s)
}

// foldComponents is the actionable-state fold's component set, drawn
// from the `decompose` declaration rather than a list beside the fold
// (§forgejo/decisions/actionable-state-digest).
func foldComponents() []digestComponent {
	return declarationFor(ActionDecompose).components
}

// The digest derivations the declarations bind to. issueDigest is an
// issue row's activity revision — a tracker's with the engine-owned
// region masked out (§forgejo/reconciliation/the-tracker-block);
// prHeadDigest is the pull request's head sha; and the sync action's
// digest is the whole landing set's revision. The sync leg of the
// unblock cascade reads the set before it dispatches
// (dispatchSyncSet), so no declared digest is ever derived over a
// state whose members are absent. A later member of this chain
// re-keys these onto the digest of the declared input set itself
// (s-git/maitred#258 scope note).
func issueDigest(_ Event, s State) string {
	if s.Issue == nil {
		return ""
	}
	return issueRevision(s.Issue)
}

func prHeadDigest(_ Event, s State) string {
	if s.PR == nil {
		return ""
	}
	return s.PR.HeadSHA
}

func syncSetDigest(_ Event, s State) string {
	if s.Issue == nil {
		return ""
	}
	return syncSetRevision(s.Issue, s.SyncMembers, s.ReviewLenses)
}

// The predicates, one per action, written over the re-fetched state
// alone (§forgejo/decisions/inputs). The rows in decisions.go reach
// them through the declaration table; a row's object-shape checks (no
// state re-fetched, the wrong kind reached) and its watermark pacing
// stay with the row.

// implementReady is implement's predicate: the open issue is a work
// item — no reserved or transient label, no teardown order standing
// over a root row — with no open blocker and no connected open PR
// (§forgejo/decisions/actions, §forgejo/decisions/transition-table).
func implementReady(_ Event, s State) (Decision, readyAnswer) {
	issue := s.Issue
	if issue == nil {
		return holdOff("no issue state re-fetched"), readyNo
	}
	if issue.State != "open" {
		return holdOff(fmt.Sprintf("issue %d is %q, not open", issue.Number, issue.State)), readyNo
	}
	if label := neverWorkItemLabel(issue.Labels); label != "" {
		return holdOffNeverImplement(issue.Number, label), readyNo
	}
	if label := transientHoldOffLabel(issue.Labels); label != "" {
		return holdOffForestSeeding(issue.Number, label), readyNo
	}
	if hasLabel(issue, LabelTeardownForest) {
		return holdOffTeardownWithoutRoot(issue.Number), readyNo
	}
	if open := openBlockers(s.Blockers); len(open) > 0 {
		return holdOff(fmt.Sprintf("issue %d is blocked by %d open issue(s)", issue.Number, len(open))), readyNo
	}
	if len(s.ConnectedPRs) > 0 {
		return holdOff(fmt.Sprintf("issue %d has %d connected open PR(s)", issue.Number, len(s.ConnectedPRs))), readyNo
	}
	return dispatch(ActionImplement, fmt.Sprintf("issue %d opened: open, unblocked, no connected PR", issue.Number)), readyYes
}

// reassessReady is reassess's predicate: an open issue with no open
// blocker carried activity — the event row it is reached from is the
// edited/labelled/commented row; the firing rule itself is unchanged
// (§forgejo/decisions/actions, s-git/maitred#262 stands).
func reassessReady(e Event, s State) (Decision, readyAnswer) {
	issue := s.Issue
	if issue == nil {
		return holdOff("no issue state re-fetched"), readyNo
	}
	if issue.State != "open" {
		return holdOff(fmt.Sprintf("issue %d is %q, not open", issue.Number, issue.State)), readyNo
	}
	if label := neverWorkItemLabel(issue.Labels); label != "" {
		return holdOffNeverReassess(issue.Number, label), readyNo
	}
	if label := transientHoldOffLabel(issue.Labels); label != "" {
		return holdOffForestSeeding(issue.Number, label), readyNo
	}
	if hasLabel(issue, LabelTeardownForest) {
		return holdOffTeardownWithoutRoot(issue.Number), readyNo
	}
	if open := openBlockers(s.Blockers); len(open) > 0 {
		return holdOff(fmt.Sprintf("issue %d is blocked by %s; not reassessing", issue.Number, issueRefs(open))), readyNo
	}
	return dispatch(ActionReassess, fmt.Sprintf("issue %d: %s", issue.Number, e.Type)), readyYes
}

// reviewReady is the review fan-out's predicate, the one the
// specification states whole: the lenses to consider are the
// configured lenses with no undismissed approving review at the PR's
// current head (§forgejo/decisions/review-lenses). When at least one
// lens is outstanding the fan-out row composes the per-lens dispatch
// and applies each lens's pacing; when every lens is satisfied the
// review-following rows own the PR, and that is the hold-off named.
func reviewReady(_ Event, s State) (Decision, readyAnswer) {
	pr := s.PR
	if pr == nil {
		return holdOff("no PR state re-fetched"), readyNo
	}
	head := digestFor(ActionReview, Event{}, s)
	for _, lens := range s.ReviewLenses {
		if !lensStandingAtHead(s.Reviews, lens, head).approved {
			return dispatch(ActionReview, fmt.Sprintf("lens %s has no undismissed approval at head %s", lens.Identity, head)), readyYes
		}
	}
	return holdOff(fmt.Sprintf("PR %d: every review lens (%s) holds an undismissed approval at head %s; the review-following rows decide", pr.Number, strings.Join(lensIdentities(s.ReviewLenses), ", "), head)), readyNo
}

// rebaseReady is rebase's predicate: an open pull request the forge
// reports not mergeable (§forgejo/decisions/transition-table). The
// row that dispatched it names the delivery: an open event, or new
// commits on a PR that already existed. Mergeability is the row's
// entry condition rather than a fact the predicate restates: both
// rows ask rebase only from a not-mergeable branch.
func rebaseReady(e Event, s State) (Decision, readyAnswer) {
	pr := s.PR
	if pr == nil {
		return holdOff("no PR state re-fetched"), readyNo
	}
	if !prOpen(pr) {
		return holdOff(fmt.Sprintf("PR %d is not open", pr.Number)), readyNo
	}
	reason := fmt.Sprintf("PR %d synced with merge conflicts", pr.Number)
	if e.Type == EventPROpened {
		reason = fmt.Sprintf("PR %d opened with merge conflicts", pr.Number)
	}
	return dispatch(ActionRebase, reason), readyYes
}

// fixFeedbackReady is fix-feedback's predicate: the chain has
// reported at head and the reports are not all approvals — a comment
// report counts on the sweep and the PR-commented path, while the
// pure review-event path waits for a changes-requested — and the PR's
// author is the configured implementer identity, the ownership gate
// being this row's whole loop prevention
// (§forgejo/decisions/review-lenses, §forgejo/config/implementer-identity,
// s-git/maitred#143).
func fixFeedbackReady(e Event, s State) (Decision, readyAnswer) {
	pr := s.PR
	if pr == nil {
		return holdOff("no PR state re-fetched"), readyNo
	}
	gate := prReviewGate(s.Reviews, s.ReviewLenses, digestFor(ActionFixFeedback, e, s))
	if !gate.allReported || !gate.fixFires(e.Type != EventPRReviewed) {
		return Decision{}, readyNotApplicable
	}
	if d, held := holdOffPRNotOwned(pr, s.ImplementerIdentity); held {
		return d, readyNo
	}
	return dispatch(ActionFixFeedback, fmt.Sprintf("PR %d stands at reviews at head %s whose reports are not all approvals (lenses without an approval: %s)",
		pr.Number, pr.HeadSHA, strings.Join(gate.outstanding, ", "))), readyYes
}

// mergeOrWaitReady is merge-or-wait's predicate: every lens holds an
// undismissed approval at head and no reviewer's latest undismissed
// review at head is a non-approval; the PR is no member of an open
// landing set; no connected issue has an open blocker — the merge
// gate; and the issue its body closes carries the merge opt-in, the
// precondition every path asks before dispatching
// (§forgejo/decisions/review-lenses, §forgejo/decisions/sync-set,
// §forgejo/decisions/merge-gate, s-git/maitred#179, s-git/maitred#228).
// The landing-set hold-off carries the cascade's sync-set emissions,
// which is why the predicate answers with a full Decision.
func mergeOrWaitReady(e Event, s State) (Decision, readyAnswer) {
	pr := s.PR
	if pr == nil {
		return holdOff("no PR state re-fetched"), readyNo
	}
	gate := prReviewGate(s.Reviews, s.ReviewLenses, digestFor(ActionMergeOrWait, e, s))
	if !gate.mergeFires() {
		return Decision{}, readyNotApplicable
	}
	if d, ok := syncMemberSetHoldOff(pr, e.Repo, s); ok {
		return d, readyNo
	}
	if open := mergeGateOpenBlockers(e, s); len(open) > 0 {
		return holdOff(fmt.Sprintf("PR %d stands at approving reviews from every lens, but a connected issue is blocked by %s; not merging", pr.Number, issueRefs(open))), readyNo
	}
	ref, optedIn := mergeOptedIn(pr, e.Repo, s.ConnectedIssues)
	if !optedIn {
		return holdOff(fmt.Sprintf("PR %d stands at approving reviews from every lens but no issue its body closes carries the %s label; an approved PR with no merge opt-in waits on a human (connected issues: %s)", pr.Number, LabelAgenticAutoMerge, connectedIssueNames(s))), readyNo
	}
	return dispatch(ActionMergeOrWait, fmt.Sprintf("PR %d stands at approving reviews from every lens and %s; the state calls for merge-or-wait regardless of revision %s",
		pr.Number, issueOptInClause(ref), digestFor(ActionMergeOrWait, e, s))), readyYes
}

// decomposeReady is decompose's predicate, the digest gate's
// comparison wherever the gate is armed, and the root's standing
// wherever it is not — the event path's kind-conflicting root, the
// only case the sweep never reaches, keeps its revision-keyed pacing
// in the row rather than in the predicate
// (§forgejo/decisions/actionable-state-digest,
// §forgejo/reconciliation/watermark-exemptions, s-git/maitred#209).
// A teardown order stands above the root's row, so under the label
// decompose answers "not ready" — the teardown declaration's row
// decides that root (§forgejo/decisions/forest-teardown).
func decomposeReady(e Event, s State) (Decision, readyAnswer) {
	issue := s.Issue
	if issue == nil {
		return holdOff("no issue state re-fetched"), readyNo
	}
	if hasLabel(issue, LabelTeardownForest) {
		return holdOff(fmt.Sprintf("issue %d is an open %s root carrying %s: the teardown order stands above the root's row", issue.Number, LabelIdeation, LabelTeardownForest)), readyNo
	}
	if hasLabel(issue, LabelIdeation) {
		if onSweep(e) {
			return digestGateAnswer(s.ForestGate, fmt.Sprintf("issue %d is an open %s issue", issue.Number, LabelIdeation),
				holdOff(fmt.Sprintf("issue %d is an open %s issue: the digest gate assembled no observation of its forest; nothing dispatches on a missing digest", issue.Number, LabelIdeation)))
		}
		subject := fmt.Sprintf("issue %d is an open %s root: the label makes the root", issue.Number, LabelIdeation)
		if e.Type == EventIssueCommented {
			// The author guard on the comment row is this row's whole
			// loop prevention, so the dispatch names the comment's
			// author: when the declared identity does not match the
			// account that actually posts — a stale declaration, a
			// renamed persona — the fail-open loop is legible in the
			// decision log as a stream of decompose dispatches
			// authored by that account
			// (§forgejo/config/decomposer-identity,
			// §forgejo/decisions/loop-prevention, s-git/maitred#147).
			subject = fmt.Sprintf("issue %d is an open %s root: a comment by %s", issue.Number, LabelIdeation, e.Sender)
		}
		// Unarmed, the root's standing is the answer and the row
		// paces the re-fire with the revision-keyed guard.
		return digestGateAnswer(s.ForestGate, subject, dispatch(ActionDecompose, subject))
	}
	if isTracker(issue) {
		gate := ideationGate(s.Blockers)
		if gate == nil {
			return holdOff(fmt.Sprintf("issue %d is an open %s tracker blocked by %d open issue(s) and gated by no %s issue; state calls for no action", issue.Number, LabelOutcome, len(openBlockers(s.Blockers)), LabelIdeation)), readyNo
		}
		if gate.State == "open" {
			return holdOff(fmt.Sprintf("issue %d is an open %s tracker whose %s root %d is still open; its own row carries the curation", issue.Number, LabelOutcome, LabelIdeation, gate.Number)), readyNo
		}
		// A commissioned tracker carries its forest's curation under
		// the same gate as the root's row: one forest, one answer,
		// whichever row asks (§forgejo/decisions/actionable-state-digest).
		return digestGateAnswer(s.ForestGate, fmt.Sprintf("issue %d is an open %s tracker commissioned by %s %d and still blocked by %d open issue(s)", issue.Number, LabelOutcome, LabelIdeation, gate.Number, len(openBlockers(s.Blockers))),
			holdOff(fmt.Sprintf("issue %d is an open %s tracker whose forest the digest gate assembled no observation of: nothing dispatches on a missing digest", issue.Number, LabelOutcome)))
	}
	return holdOff(fmt.Sprintf("issue %d is neither an open %s root nor an %s tracker: no row asks decompose of it", issue.Number, LabelIdeation, LabelOutcome)), readyNo
}

// digestGateAnswer is the digest gate's comparison, the answer every
// gated decompose row reaches through the declaration: unchanged
// digests hold off, a moved digest dispatches naming the component
// that moved, and a forest the store has never observed dispatches as
// a first observation. gateUnarmed is what the asker answers when the
// gate carries no observation at all — a hold-off on the sweep's
// gated rows (through the pipeline it cannot happen), the root's
// standing on the event path (§forgejo/decisions/actionable-state-digest,
// §forgejo/reconciliation/watermark-exemptions, s-git/maitred#209).
func digestGateAnswer(g *ForestDigestGate, subject string, gateUnarmed Decision) (Decision, readyAnswer) {
	if unarmedDigestGate(g) {
		return gateUnarmed, answerOf(gateUnarmed)
	}
	if g.Current == g.Stored {
		return holdOff(fmt.Sprintf("%s: the forest's actionable-state digest is unchanged since it was last observed: nothing a decompose run could act on has moved", subject)), readyNo
	}
	if g.Stored == "" {
		return dispatch(ActionDecompose, fmt.Sprintf("%s: the forest's actionable-state digest is observed for the first time; the state calls for decompose", subject)), readyYes
	}
	return dispatch(ActionDecompose, fmt.Sprintf("%s: the forest's actionable-state digest moved (%s); the state calls for decompose", subject, strings.Join(g.movedComponents(), ", "))), readyYes
}

// unarmedDigestGate reports whether the gate carries no observation:
// a nil gate is an unarmed one, an empty current digest an assembly
// that observed nothing
// (§forgejo/decisions/actionable-state-digest).
func unarmedDigestGate(g *ForestDigestGate) bool {
	return g == nil || g.Current == ""
}

// answerOf reads a hold-off-or-dispatch Decision back as the
// predicate's answer.
func answerOf(d Decision) readyAnswer {
	if d.Dispatched {
		return readyYes
	}
	return readyNo
}

// wrapUpReady is wrap-up's predicate: an open outcome tracker with no
// open blocker — on the sweep the state is the watermark, which is
// why the reason names the revision it dispatches past
// (§forgejo/reconciliation/watermark-exemptions).
func wrapUpReady(_ Event, s State) (Decision, readyAnswer) {
	issue := s.Issue
	if issue == nil {
		return holdOff("no issue state re-fetched"), readyNo
	}
	if issue.State != "open" {
		return holdOff(fmt.Sprintf("issue %d is %q, not open", issue.Number, issue.State)), readyNo
	}
	if !isTracker(issue) {
		return holdOff(fmt.Sprintf("issue %d is not an open %s tracker: nothing to wrap up", issue.Number, LabelOutcome)), readyNo
	}
	if open := openBlockers(s.Blockers); len(open) > 0 {
		return holdOff(fmt.Sprintf("issue %d is an open %s tracker still blocked by %d open issue(s); the curation row asks first", issue.Number, LabelOutcome, len(open))), readyNo
	}
	return dispatch(ActionWrapUp, fmt.Sprintf("issue %d is an open, fully unblocked %s tracker; the state calls for wrap-up regardless of revision %s",
		issue.Number, LabelOutcome, revisionFacts(issue, digestFor(ActionWrapUp, Event{}, s)))), readyYes
}

// teardownForestReady is teardown-forest's predicate: an open
// ideation root carrying the order, still blocking at least one issue
// the order would delete — a spared tracker is not one — because a
// forest with nothing left to delete dispatches nothing, on either
// path (§forgejo/decisions/forest-teardown, s-git/maitred#173). The
// order label is the row's entry condition, both rows testing it
// before asking, so the predicate does not restate it.
func teardownForestReady(e Event, s State) (Decision, readyAnswer) {
	issue := s.Issue
	if issue == nil {
		return holdOff("no issue state re-fetched"), readyNo
	}
	if issue.State != "open" {
		return holdOff(fmt.Sprintf("issue %d is %q, not open", issue.Number, issue.State)), readyNo
	}
	if !hasLabel(issue, LabelIdeation) {
		return holdOffTeardownWithoutRoot(issue.Number), readyNo
	}
	remaining := countDeletableMembers(s.Blocked)
	if remaining == 0 {
		if onSweep(e) {
			return holdOff(fmt.Sprintf("issue %d is an open %s root carrying %s but blocking no issue other than an %s tracker, which the order spares: its forest has nothing left to delete", issue.Number, LabelIdeation, LabelTeardownForest, LabelOutcome)), readyNo
		}
		return holdOff(fmt.Sprintf("issue %d carries %s but blocks no issue other than an %s tracker, which the order spares: its forest has nothing left to delete", issue.Number, LabelTeardownForest, LabelOutcome)), readyNo
	}
	if onSweep(e) {
		return dispatch(ActionTeardownForest, fmt.Sprintf("issue %d is an open %s root carrying %s and still blocking %d issue(s) the order would delete; the state calls for teardown regardless of revision %s",
			issue.Number, LabelIdeation, LabelTeardownForest, remaining, digestFor(ActionTeardownForest, e, s))), readyYes
	}
	return dispatch(ActionTeardownForest, fmt.Sprintf("issue %d is an open %s root carrying %s: the forest it commissioned is to be deleted", issue.Number, LabelIdeation, LabelTeardownForest)), readyYes
}

// syncReady is the landing-set predicate: an open sync issue
// dispatches sync once every blocking issue is closed — landed, or
// finished by a human — or stands with its delivering PR approved at
// head and mergeable while carrying agentic-sync-merge; a pull
// request that blocks the grouping issue directly is held to the same
// test through its own state (§forgejo/decisions/sync-set). An unready
// set holds off silently — the hold-off is at home only in the
// decision log.
func syncReady(_ Event, s State) (Decision, readyAnswer) {
	issue := s.Issue
	if issue == nil {
		return holdOff("no issue state re-fetched"), readyNo
	}
	members := s.SyncMembers
	if len(members) == 0 {
		return holdOff(fmt.Sprintf("issue %d is an open %s issue with no blocking issues; an empty set has nothing to land", issue.Number, LabelSync)), readyNo
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
			if !allLensesApproved(m.Reviews, s.ReviewLenses, m.PR.HeadSHA) {
				notReady = append(notReady, fmt.Sprintf("%s is not approved at head %s", ref, m.PR.HeadSHA))
			}
		}
	}
	if len(notReady) > 0 {
		return holdOff(fmt.Sprintf("sync issue %d held off: %s", issue.Number, strings.Join(notReady, "; "))), readyNo
	}
	return dispatch(ActionSync, fmt.Sprintf("issue %d is an open %s issue: all %d member(s) closed or approved, mergeable, and opted in; the state calls for sync at set revision %s",
		issue.Number, LabelSync, len(members), digestFor(ActionSync, Event{}, s))), readyYes
}
