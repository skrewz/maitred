package forgejo

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Action declarations (§forgejo/decisions/actions).
//
// Every action the decision function can name carries exactly one
// declaration: the input set its predicate reads, the ready predicate
// over that set, and the digest a dispatch is watermarked on. The
// decision path reaches an action's predicate and digest through this
// table — the rows in decisions.go neither restate the digest beside a
// dispatch nor carry a private readiness rule the declaration does not
// name. The input set is recorded rather than consulted: the refusal
// of undeclared reads that will one day check a predicate against it
// is a later member of this chain (§forgejo/decisions/inputs).
//
// The predicate answers the question "does this state call for this
// action", one answer whichever path asks it; how a ready row is
// paced (a revision-keyed guard, an exemption, the re-fire bound)
// belongs to the row, not the predicate (§forgejo/decisions/loop-prevention,
// §forgejo/reconciliation/the-state-is-the-watermark). The digest is
// the revision the watermark records under the action's name
// (§forgejo/state/watermark); which fact each action keys on today is
// what the declarations state. An issue-path action's digest is now
// derived from its own declared input set, so re-keying one edits this
// table and nothing beside it; the pull-request actions still name the
// object revision their rows already keyed on, and re-keying them is
// the next member of this chain (s-git/maitred#260). The digest is also
// what the re-fire bound is a rate on and what the task's dedup key
// carries, so the guard, the pacing and the queue answer on one value
// (§forgejo/decisions/loop-prevention,
// §forgejo/webhook/the-pipeline,
// §forgejo/decisions/transition-table).

// readyAnswer is the predicate's verdict: the state calls for the
// action (readyYes), does not (readyNo), or the predicate abstains and
// the asking row continues to its next question (readyNotApplicable).
// Only the review-reading rows have such a next question; the rest ask
// through readyAtRow, which records an abstain as the wiring fault it
// would be.
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
	// digestFoldsInputs records that the digest is the fold of this
	// declaration's own input set, which is what lets a reason line
	// name the facts behind the digest it quotes
	// (§forgejo/state/watermark, §forgejo/observability/the-decision-log).
	digestFoldsInputs bool
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
			ready:             implementReady,
			digest:            declaredDigestFor(ActionImplement),
			digestFoldsInputs: true,
		},
		// Comments stand among reassess's inputs, and this declaration is
		// the one place whose comments count is stated: a comment authored
		// by neither the account engine_account names nor the configured
		// decomposer identity, the test commentCountsAsInput applies
		// (§forgejo/config/engine-account, §forgejo/config/decomposer-identity,
		// §forgejo/decisions/inputs, s-git/maitred#262). The digest of
		// these facts is what fires the row, the description among them so
		// an edit that rewords what the engine is asked to do moves it
		// (s-git/maitred#262).
		ActionReassess: {
			inputs: []string{
				"issue.state", "issue.labels", "issue.body", "issue.comments",
				"blockers.states", "event.type", "event.sender",
			},
			ready:             reassessReady,
			digest:            declaredDigestFor(ActionReassess),
			digestFoldsInputs: true,
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
			ready:             decomposeReady,
			digest:            declaredDigestFor(ActionDecompose),
			digestFoldsInputs: true,
			components:        digestComponentSet,
		},
		ActionWrapUp: {
			inputs: []string{
				"issue.state", "issue.labels", "issue.updated_at",
				"blockers.states",
			},
			ready:             wrapUpReady,
			digest:            declaredDigestFor(ActionWrapUp),
			digestFoldsInputs: true,
		},
		ActionTeardownForest: {
			inputs: []string{
				"issue.state", "issue.labels", "issue.updated_at",
				"blocked downstream edges",
			},
			ready:             teardownForestReady,
			digest:            declaredDigestFor(ActionTeardownForest),
			digestFoldsInputs: true,
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
	validateFoldingDeclarations()
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

// readyAtRow asks an action's predicate from a row that has no next
// question after it, so the predicate's answer is that row's whole
// decision: readyYes is the dispatch the row paces, readyNo the
// hold-off it records. Abstention belongs to the rows that do continue
// — the review-reading cluster's fall-through to the fan-out
// (decideChainReported) — so an abstain reaching one of these rows is
// a misrouted ask, and is named in the decision log rather than
// recorded as the silent nothing
// (§forgejo/observability/the-decision-log, §forgejo/decisions/actions).
// ok reports whether the state calls for the action.
func readyAtRow(a Action, e Event, s State) (Decision, bool) {
	d, ans := readyTo(a, e, s)
	switch ans {
	case readyYes:
		return d, true
	case readyNo:
		return d, false
	}
	return holdOff(fmt.Sprintf("the %s declaration abstains only where the asking row has a next question to fall through to; this row has none, so asking it here is a wiring fault", a)), false
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

// The digest derivations the declarations bind to. declaredDigestFor is
// the digest of an issue-path action's declared input set; prHeadDigest
// is the pull request's head sha; and the sync action's digest is the
// whole landing set's revision. The sync leg of the unblock cascade
// reads the set before it dispatches (dispatchSyncSet), so no declared
// digest is ever derived over a state whose members are absent. The
// pull-request actions key on an object revision — a head sha — and
// re-keying them onto the fold of their declared inputs is the next
// member of this chain (s-git/maitred#260).

// declaredDigestFor is the digest derivation an issue-path declaration
// binds to: the digest of that action's declared input set, the hex
// SHA-256 over exactly the state facts its declaration names, so the
// same state yields a byte-identical digest whatever order the listings
// arrive in and a change to any fact the predicate reads moves it
// (§forgejo/state/watermark, §forgejo/decisions/inputs,
// s-git/maitred#259).
//
// No event fact stands in it, and no issue timestamp stands in it
// whole. The event names which row fired, and the sweep and the event
// path must reach one digest for one state — two answers is the split
// this chain exists to close
// (§forgejo/reconciliation/the-state-is-the-watermark). The one fact
// the engine moves by writing it, an outcome tracker's current-state
// region, is folded out: the body folds as what stands outside the
// fence the engine owns, and the row's own timestamp as the masked
// revision (§forgejo/reconciliation/the-tracker-block).
//
// A fact a re-keyed declaration names and no fold covers would fold as
// nothing, silently widening the digest's blind spots, so
// validateFoldingDeclarations refuses it when the declaration table is
// built (§forgejo/decisions/inputs).
func declaredDigestFor(a Action) func(Event, State) string {
	return func(_ Event, s State) string {
		facts := declaredInputFacts(a, s)
		if len(facts) == 0 {
			// An action reached with none of its declared state present
			// folds to nothing, and the revision-keyed guard is inert for
			// a revision that is nothing — the same answer an issue that
			// was never fetched gave before this digest existed
			// (§forgejo/state/watermark).
			return ""
		}
		return hashInputFacts(facts)
	}
}

// declaredInputFacts is one declared digest's input, in the words a
// reason line quotes them: the facts the action's declaration names,
// each stated so a human can line the quote up against the forge's
// history (§forgejo/observability/the-decision-log,
// §forgejo/state/watermark).
func declaredInputFacts(a Action, s State) []string {
	return inputFacts(a, s, nil)
}

// quotedInputFacts is the same set as a decision line carries it, with
// the bound an input declares applied: a reason stays the length of the
// delivery it describes rather than the age of the work item
// (§forgejo/observability/the-decision-log).
func quotedInputFacts(a Action, s State) []string {
	return inputFacts(a, s, declaredInputQuotes)
}

// inputFacts collects one declaration's folded facts once for both
// readers: the digest folds every one of them, the quote takes them
// through the bound named in quotes (nil for the digest itself). A fact
// an earlier input already stated is collected once, so two folds that
// name the same fact move one digest and quote it once.
func inputFacts(a Action, s State, quotes map[string]func([]string) []string) []string {
	var facts []string
	seen := map[string]bool{}
	for _, name := range declarationFor(a).inputs {
		if strings.HasPrefix(name, "event.") {
			// An event fact names which row fired, never part of the
			// state whose digest both paths must answer with, so it
			// folds nowhere (§forgejo/decisions/inputs,
			// §forgejo/reconciliation/the-state-is-the-watermark). The
			// rows still read them — reassess names the event type in
			// its reason, the root's comment row names the sender — they
			// simply cannot enter a digest both paths answer with.
			continue
		}
		foldFact, ok := declaredInputFolds[name]
		if !ok {
			panic(fmt.Sprintf("forgejo: %s declares %s, which no digest fold states", a, name))
		}
		var folded []string
		for _, fact := range foldFact(s) {
			if seen[fact] {
				continue
			}
			seen[fact] = true
			folded = append(folded, fact)
		}
		if bound, ok := quotes[name]; ok {
			folded = bound(folded)
		}
		facts = append(facts, folded...)
	}
	return facts
}

// declaredInputQuotes bounds how one declared input's folded facts reach
// a reason line. Comments are the only input whose facts grow with a
// work item's age — labels, blockers and connected pull requests are
// trimmed to the taxonomy the forge enforces, a description folds to one
// hex — so the digest keeps folding every counted comment while the
// quote names the newest, the one the delivery was about, beside how
// many the fold counted (§forgejo/observability/the-decision-log,
// s-git/maitred#262). An input that needs no bound stands nowhere here.
var declaredInputQuotes = map[string]func([]string) []string{
	"issue.comments": quoteCountedComments,
}

// quoteCountedComments is that bound. The forge lists a thread oldest
// first, so the fact last folded is the comment just written; a thread
// of one quotes as that comment alone.
func quoteCountedComments(facts []string) []string {
	if len(facts) < 2 {
		return facts
	}
	return []string{fmt.Sprintf("%s (the newest of %d counted comments)", facts[len(facts)-1], len(facts))}
}

// validateFoldingDeclarations refuses a declaration whose digest is the
// fold of its own inputs while naming a state fact no fold states. A
// silent gap there would widen the digest's blind spots — a fact the
// predicate reads could move while the digest stands still — so the
// table is checked once as it is built, where a mistake takes the
// process down at startup rather than in the middle of serving a
// delivery (§forgejo/decisions/inputs).
func validateFoldingDeclarations() {
	for a, decl := range actionDecls {
		if decl.digestFoldsInputs {
			declaredInputFacts(a, State{})
		}
	}
}

// digestFoldsInputs reports whether an action's digest is the fold of
// its declaration's input set, the case in which a reason quoting it
// names the facts behind it (§forgejo/state/watermark).
func digestFoldsInputs(a Action) bool {
	return declarationFor(a).digestFoldsInputs
}

// declaredInputFolds maps each state fact a declaration may name to how
// its digest states it. Each fold states the fact the action's own
// predicate reads, so no fact can be folded that no predicate asks —
// and, conversely, no fact the predicate asks of can stand still while
// the digest moves (§forgejo/decisions/inputs).
var declaredInputFolds = map[string]func(State) []string{
	"issue.state": func(s State) []string {
		if s.Issue == nil {
			return nil
		}
		return []string{"state " + s.Issue.State}
	},
	"issue.labels": func(s State) []string {
		if s.Issue == nil {
			return nil
		}
		facts := make([]string, 0, len(s.Issue.Labels))
		for _, label := range s.Issue.Labels {
			facts = append(facts, "label "+label)
		}
		return facts
	},
	"issue.body": func(s State) []string {
		if s.Issue == nil {
			return nil
		}
		// What stands outside the fence the engine owns: the
		// current-state region a wrap-up rewrites every tick folds
		// nowhere (§forgejo/reconciliation/the-tracker-block).
		return []string{"body " + digestRevision(outsideFence(s.Issue.Body))}
	},
	"issue.updated_at": func(s State) []string {
		if s.Issue == nil {
			return nil
		}
		// An ordinary row states its activity timestamp, one fact among
		// the set rather than the cursor the whole decision turned on.
		// An outcome tracker's write of its own current-state region
		// moves that timestamp, so the tracker states instead the facts
		// its masked revision folds — the same facts, readable, and none
		// of them one the engine moves by writing it
		// (§forgejo/state/watermark,
		// §forgejo/reconciliation/the-tracker-block).
		if isTracker(s.Issue) {
			return trackerFacts(s.Issue)
		}
		return []string{"updated " + s.Issue.UpdatedAt.UTC().Format(time.RFC3339)}
	},
	"issue.comments": func(s State) []string {
		// The comments are carried as the forge lists them; which of
		// them count is commentCountsAsInput's one statement, applied
		// here and nowhere else, so an excluded author's comment moves
		// no digest while every other author's moves it
		// (§forgejo/config/engine-account, §forgejo/config/decomposer-identity,
		// s-git/maitred#262). A body is quoted as the hex its text
		// digests to, the way an issue's own body is: what an operator
		// can place in the forge's history is the comment beside it.
		var facts []string
		for _, c := range s.Comments {
			if !commentCountsAsInput(c.Author, s.EngineAccount, s.DecomposerIdentity) {
				continue
			}
			facts = append(facts, "comment "+c.Author+" "+digestRevision(c.Body))
		}
		return facts
	},
	"blockers.states": func(s State) []string {
		facts := make([]string, 0, len(s.Blockers))
		for i := range s.Blockers {
			facts = append(facts, blockerRef(s, &s.Blockers[i])+" "+s.Blockers[i].State)
		}
		return facts
	},
	"connected PRs": func(s State) []string {
		// A connected pull request is one of the issue's blockers, so it
		// stands where the issue does and is keyed there
		// (§forgejo/webhook/re-fetch, §forgejo/decisions/transition-table).
		repo := ""
		if s.Issue != nil {
			repo = s.Issue.Repository
		}
		facts := make([]string, 0, len(s.ConnectedPRs))
		for i := range s.ConnectedPRs {
			facts = append(facts, forestRef(repo, s.ConnectedPRs[i].Number)+" "+s.ConnectedPRs[i].State)
		}
		return facts
	},
	"blocked downstream edges": func(s State) []string {
		// The teardown predicate's read is countDeletableMembers: the
		// spared tracker says nothing about whether a forest remains, so
		// it states no fact either (§forgejo/decisions/forest-teardown).
		var facts []string
		for i := range s.Blocked {
			if isTracker(&s.Blocked[i]) {
				continue
			}
			facts = append(facts, blockerRef(s, &s.Blocked[i])+" "+s.Blocked[i].State)
		}
		return facts
	},
	"forest digest gate": func(s State) []string {
		if s.ForestGate == nil {
			return []string{"forest gate unarmed"}
		}
		return []string{"forest " + forestRef(s.ForestGate.RootRepo, s.ForestGate.RootNumber) + " " + s.ForestGate.Current}
	},
}

// blockerRef is a blocker's identity, which the dependency listing
// states as its own repository and, where it states none, means the
// repository of the issue it blocks
// (§forgejo/decisions/actionable-state-digest).
func blockerRef(s State, blocker *Issue) string {
	repo := blocker.Repository
	if repo == "" && s.Issue != nil {
		repo = s.Issue.Repository
	}
	return forestRef(repo, blocker.Number)
}

// hashInputFacts is the package's one length-prefixed hash: the hex
// SHA-256 over the facts in sorted order, each length-prefixed so no
// fact's text can be mistaken for another's structure. The sort is what
// makes the digest order-insensitive; the prefix is what makes the fold
// injective. Both the declared digest and a tracker's masked revision
// run through it, so no derivation can be re-encoded half-way
// (§forgejo/state/watermark). The forest fold's hashFacts is separate
// because it owes a sub-digest per tagged component, which no plain
// fact list can express (§forgejo/decisions/actionable-state-digest).
func hashInputFacts(facts []string) string {
	encoded := append([]string(nil), facts...)
	sort.Strings(encoded)
	h := sha256.New()
	for _, fact := range encoded {
		fmt.Fprintf(h, "%d:%s\n", len(fact), fact)
	}
	return hex.EncodeToString(h.Sum(nil))
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
// edited/labelled/commented row. A comment counts only when
// commentCountsAsInput does not exclude its author: the engine's own
// report, or the decomposer's own question, on an ordinary work item is
// no input, so the row holds off naming whose authorship it read rather
// than re-firing at the re-fire bound's pace for as long as the issue
// stands (§forgejo/decisions/actions,
// §forgejo/decisions/loop-prevention, s-git/maitred#262). No timestamp
// is asked of: what moves this row is a fact it declares.
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
	// The authorship is asked last: every hold-off above it already
	// names a state the row has nothing to do with, and this one names
	// the delivery.
	if e.Type == EventIssueCommented && !commentCountsAsInput(e.Sender, s.EngineAccount, s.DecomposerIdentity) {
		return holdOff(fmt.Sprintf("issue %d: comment authored by %s, whose comments are no input; nothing reassessed", issue.Number, e.Sender)), readyNo
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
// A teardown order stands above the root's row, so a root carrying
// the order answers "not ready" — the teardown declaration's row
// decides that forest (§forgejo/decisions/forest-teardown). The order
// is asked of the root's labels because the root's row is the one that
// tests it; the tracker's row asks no question of an order, which
// §decisions/actions does not give it.
func decomposeReady(e Event, s State) (Decision, readyAnswer) {
	issue := s.Issue
	if issue == nil {
		return holdOff("no issue state re-fetched"), readyNo
	}
	if hasLabel(issue, LabelIdeation) {
		if hasLabel(issue, LabelTeardownForest) {
			return holdOff(fmt.Sprintf("issue %d is an open %s root carrying %s: the teardown order stands above the root's row", issue.Number, LabelIdeation, LabelTeardownForest)), readyNo
		}
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
		// Unarmed, the root's standing is the answer and the re-fire
		// bound paces the row, one attempt per interval.
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
// open blocker (§forgejo/decisions/actions,
// §forgejo/reconciliation/watermark-exemptions).
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
	return dispatch(ActionWrapUp, fmt.Sprintf("issue %d is an open, fully unblocked %s tracker; the state calls for wrap-up",
		issue.Number, LabelOutcome)), readyYes
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
	return dispatch(ActionTeardownForest, fmt.Sprintf("issue %d is an open %s root carrying %s and still blocking %d issue(s) the order would delete; the forest it commissioned is to be deleted",
		issue.Number, LabelIdeation, LabelTeardownForest, remaining)), readyYes
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
