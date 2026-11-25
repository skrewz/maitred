package forgejo

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
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
// name. The input set is consulted as well as recorded: an action the
// enum names without a declaration cannot dispatch, and a predicate
// reading a fact its own declaration does not name is refused
// (§forgejo/decisions/inputs, §forgejo/decisions/actions).
//
// The predicate answers the question "does this state call for this
// action", one answer whichever path asks it; how a ready row is
// paced (the digest it declares, and the re-fire bound that
// spaces a re-fire of it)
// belongs to the row, not the predicate (§forgejo/decisions/loop-prevention,
// §forgejo/reconciliation/the-state-is-the-watermark). The digest is
// the revision the watermark records under the action's name
// (§forgejo/state/watermark), and every declaration's is derived from
// the input set it names — the issue-path actions took that first
// (s-git/maitred#259), the pull-request actions at s-git/maitred#260 —
// so re-keying an action edits this table and nothing beside it. The
// digest is also what the re-fire bound is a rate on and what the task's
// dedup key carries, so the guard, the pacing and the queue answer on one value
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
	// verbatim). It is the set the refusal of undeclared reads checks
	// the predicate against, and so the action's declaration of what it
	// looks at, in one place.
	inputs []string
	// ready is the action's predicate. The returned Decision carries
	// the dispatch reason (readyYes) or the hold-off reason
	// (readyNo), cascade included. The predicate must not consult the
	// watermark: pacing is the row's.
	ready func(Event, State) (Decision, readyAnswer)
	// digest derives the revision a dispatch of this action is
	// watermarked on (§forgejo/state/watermark).
	digest func(Event, State) string
	// digestSubject derives the digest one subject of this action's
	// fan-out is paced on: the fold of the declared inputs that one
	// subject's dispatch reads, so a sibling's move cannot re-fire it
	// (§forgejo/state/watermark, §forgejo/decisions/review-lenses).
	// Only the review fan-out declares one.
	digestSubject func(Event, State, string) string
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
			digest: declaredDigestFor(ActionImplement),
		},
		// Comments stand among reassess's inputs, and this declaration is
		// the one place whose comments count is stated: a comment authored
		// by neither the account engine_account names nor the configured
		// decomposer identity, the test commentCountsAsInput applies
		// (§forgejo/config/engine-account, §forgejo/config/decomposer-identity,
		// §forgejo/decisions/inputs, s-git/maitred#262). The digest of
		// these facts is what fires the row, the description among them so
		// an edit that rewords what the engine is asked to do moves it
		// (s-git/maitred#262). The two identities the count answers to are
		// declared like any other input and fold nowhere, as an event fact
		// does: a fold that stated the operator's file would move every
		// row the day it was edited (§forgejo/decisions/inputs).
		ActionReassess: {
			inputs: []string{
				"issue.state", "issue.labels", "issue.body", "issue.comments",
				"blockers.states",
				"config.decomposer identity", "config.engine account",
				"event.type", "event.sender",
			},
			ready:  reassessReady,
			digest: declaredDigestFor(ActionReassess),
		},
		ActionReview: {
			inputs: []string{
				"PR.state", "PR.mergeable", "PR.head sha", "PR.labels",
				"reviews", "config.review lenses", "event.type", "event.sender",
			},
			ready:         reviewReady,
			digest:        declaredDigestFor(ActionReview),
			digestSubject: declaredSubjectDigestFor(ActionReview),
		},
		ActionFixFeedback: {
			inputs: []string{
				"PR.state", "PR.mergeable", "PR.head sha", "PR.author",
				"reviews", "config.review lenses", "config.implementer identity",
				"event.type",
			},
			ready:  fixFeedbackReady,
			digest: declaredDigestFor(ActionFixFeedback),
		},
		ActionMergeOrWait: {
			inputs: []string{
				"PR.state", "PR.mergeable", "PR.head sha", "reviews",
				"connected issues", "connected issues' blockers",
				"connected issues' sync sets", "sync sets",
				"config.review lenses", "event.kind", "event.repo", "event.number",
			},
			ready:  mergeOrWaitReady,
			digest: declaredDigestFor(ActionMergeOrWait),
		},
		ActionRebase: {
			inputs: []string{"PR.state", "PR.mergeable", "PR.head sha", "event.type"},
			ready:  rebaseReady,
			digest: declaredDigestFor(ActionRebase),
		},
		ActionDecompose: {
			inputs: []string{
				"issue.state", "issue.labels", "issue.body",
				"blockers.states", "forest digest gate",
				"event.type", "event.sender",
			},
			ready:      decomposeReady,
			digest:     declaredDigestFor(ActionDecompose),
			components: digestComponentSet,
		},
		ActionWrapUp: {
			inputs: []string{
				"issue.state", "issue.labels", "issue.updated_at",
				"blockers.states",
			},
			ready:  wrapUpReady,
			digest: declaredDigestFor(ActionWrapUp),
		},
		ActionTeardownForest: {
			inputs: []string{
				"issue.state", "issue.labels", "issue.updated_at",
				"blocked downstream edges",
			},
			ready:  teardownForestReady,
			digest: declaredDigestFor(ActionTeardownForest),
		},
		ActionSync: {
			inputs: []string{
				"issue.state", "issue.sync label", "issue.body", "sync members",
				"members' PRs", "members' PR reviews", "config.review lenses",
			},
			ready:  syncReady,
			digest: declaredDigestFor(ActionSync),
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

// digestForSubject derives the revision one subject of a fan-out is
// paced on, through the same declaration: the fold of the declared inputs
// that subject's own dispatch reads. An action with no fan-out has no
// subject digest, which is a programming error rather than a state
// (§forgejo/state/watermark, §forgejo/decisions/review-lenses).
func digestForSubject(a Action, subject string, e Event, s State) string {
	decl := declarationFor(a)
	if decl.digestSubject == nil {
		panic("forgejo: subject digest: " + string(a) + " declares no fan-out digest")
	}
	return decl.digestSubject(e, s, subject)
}

// foldComponents is the actionable-state fold's component set, drawn
// from the `decompose` declaration rather than a list beside the fold
// (§forgejo/decisions/actionable-state-digest).
func foldComponents() []digestComponent {
	return declarationFor(ActionDecompose).components
}

// The digest derivations the declarations bind to: declaredDigestFor
// is the digest of an action's declared input set, which every
// declaration binds to — the issue-path actions took it first
// (s-git/maitred#259), the pull-request actions here
// (s-git/maitred#260). The sync leg of the unblock cascade reads the
// set before it dispatches (dispatchSyncSet), so no declared digest is
// ever derived over a state whose members are absent.

// declaredDigestFor is the digest derivation a declaration binds to: the
// digest of that action's declared input set, the hex SHA-256 over
// exactly the state facts its declaration names, so the same state yields
// a byte-identical digest whatever order the listings arrive in and a
// change to any fact the predicate reads moves it
// (§forgejo/state/watermark, §forgejo/decisions/inputs,
// s-git/maitred#259).
//
// No event fact stands in it, and no object timestamp stands in it
// whole. The event names which row fired, and the sweep and the event
// path must reach one digest for one state — two answers is the split
// this chain exists to close
// (§forgejo/reconciliation/the-state-is-the-watermark). The one fact
// the engine moves by writing it, an outcome tracker's current-state
// region, is folded out: the body folds as what stands outside the
// fence the engine owns, and the row's own timestamp as the masked
// revision (§forgejo/reconciliation/the-tracker-block).
//
// A fact a declaration names and no fold covers would fold as nothing,
// silently widening the digest's blind spots, so
// validateFoldingDeclarations refuses it when the declaration table is
// built (§forgejo/decisions/inputs).
func declaredDigestFor(a Action) func(Event, State) string {
	return func(_ Event, s State) string {
		return digestFacts(declaredInputFacts(a, "", s))
	}
}

// declaredSubjectDigestFor is the derivation one subject of a fan-out is
// paced on: the same fold of the same declared inputs, read from that
// subject's side. A fan-out's subjects share the object but not its
// standing against it — the review lenses each owe their own report at
// head — and a digest that folded a sibling's reviews would move when the
// sibling reported and re-dispatch a lens whose own dispatch was already
// in flight. Scoping the fold to the subject is what keeps one lens's
// dispatch one dispatch (§forgejo/decisions/review-lenses,
// §forgejo/decisions/loop-prevention).
func declaredSubjectDigestFor(a Action) func(Event, State, string) string {
	return func(_ Event, s State, subject string) string {
		return digestFacts(declaredInputFacts(a, subject, s))
	}
}

// digestFacts is a declared digest's answer over its facts: nothing for
// an action reached with none of its declared state present, which makes
// the guard inert for a revision that is nothing — the same answer an
// object that was never fetched gave before declared digests existed
// (§forgejo/state/watermark).
func digestFacts(facts []string) string {
	if len(facts) == 0 {
		return ""
	}
	return hashInputFacts(facts)
}

// declaredInputFacts is one declared digest's input, in the words a
// reason line quotes them: the facts the action's declaration names,
// each stated so a human can line the quote up against the forge's
// history (§forgejo/observability/the-decision-log,
// §forgejo/state/watermark). subject is the fan-out subject whose
// dispatch the digest paces, empty for a singleton row. The digest
// folds every fact; a quote of them bounds the thread
// (§forgejo/observability/the-decision-log). A fact an earlier input
// already stated is collected once, so two folds that name the same
// fact move one digest and quote it once.

// foldsNowhere names the prefixes of declared names no digest fold
// states, each for its own reason. An event fact names which row
// fired, never part of the state whose digest both paths must answer
// with (§forgejo/reconciliation/the-state-is-the-watermark), and a
// configuration fact states the operator's choice, so a fold that
// stated it would move every row the day it was edited
// (§forgejo/decisions/inputs). The rows still read both kinds —
// reassess names the event type in its reason and the identities whose
// comments count — and declare them like any other fact; the fold
// simply passes them by.
var foldsNowhere = []string{"event.", "config."}

func declaredInputFacts(a Action, subject string, s State) []string {
	var facts []string
	seen := map[string]bool{}
	add := func(fact string) {
		if seen[fact] {
			return
		}
		seen[fact] = true
		facts = append(facts, fact)
	}
	for _, name := range declarationFor(a).inputs {
		if slices.ContainsFunc(foldsNowhere, func(prefix string) bool {
			return strings.HasPrefix(name, prefix)
		}) {
			continue
		}
		foldFact, ok := declaredInputFolds[name]
		if !ok {
			panic(fmt.Sprintf("forgejo: %s declares %s, which no digest fold states", a, name))
		}
		for _, fact := range foldFact(subject, s) {
			add(fact)
		}
	}
	return facts
}

// commentFactPrefix opens every fact the issue.comments fold states, so
// the one place that bounds the thread for a reason line recognises it.
const commentFactPrefix = "comment "

// quoteThread bounds a thread's facts for the reason line that quotes
// them. Comments are the only input whose facts grow with a work item's
// age — labels, blockers and connected pull requests are trimmed to the
// taxonomy the forge enforces, a description folds to one hex — so a
// quote carries the newest counted comment, the one the delivery was
// about, and lets the count the fold states say how many there were
// (§forgejo/observability/the-decision-log, s-git/maitred#262). The
// newest is the last fact the fold produced, the listing being in the
// order the forge answers with; were that order other, the line would
// name another comment of the same thread rather than the one asked for,
// the decision itself being folded from every one. The bound is the
// quote's alone: a comment the thread lost still moves the digest.
func quoteThread(facts []string) []string {
	kept := make([]string, 0, len(facts))
	newest := ""
	for _, fact := range facts {
		if strings.HasPrefix(fact, commentFactPrefix) {
			newest = fact
			continue
		}
		kept = append(kept, fact)
	}
	if newest != "" {
		kept = append(kept, newest)
	}
	return kept
}

// validateFoldingDeclarations refuses a declaration whose digest is the
// fold of its own inputs while naming a state fact no fold states. A
// silent gap there would widen the digest's blind spots — a fact the
// predicate reads could move while the digest stands still — so the
// table is checked once as it is built, where a mistake takes the
// process down at startup rather than in the middle of serving a
// delivery (§forgejo/decisions/inputs).
func validateFoldingDeclarations() {
	for a := range actionDecls {
		declaredInputFacts(a, "", State{})
		if declarationFor(a).digestSubject != nil {
			declaredInputFacts(a, "subject", State{})
		}
	}
}

// declaredInputFolds maps each state fact a declaration may name to how
// its digest states it. Each fold states the fact the action's own
// predicate reads, so no fact can be folded that no predicate asks —
// and, conversely, no fact the predicate asks of can stand still while
// the digest moves (§forgejo/decisions/inputs). subject is the fan-out
// subject whose dispatch the digest paces; only the review fold reads it,
// because only a fan-out's subject reads a standing of its own.
var declaredInputFolds = map[string]func(subject string, s State) []string{
	"issue.state": func(_ string, s State) []string {
		if s.Issue == nil {
			return nil
		}
		return []string{"state " + s.Issue.State}
	},
	"issue.labels": func(_ string, s State) []string {
		if s.Issue == nil {
			return nil
		}
		facts := make([]string, 0, len(s.Issue.Labels))
		for _, label := range s.Issue.Labels {
			facts = append(facts, "label "+label)
		}
		return facts
	},
	"issue.body": func(_ string, s State) []string {
		if s.Issue == nil {
			return nil
		}
		// What stands outside the fence the engine owns: the
		// current-state region a wrap-up rewrites every tick folds
		// nowhere (§forgejo/reconciliation/the-tracker-block).
		return []string{"body " + digestRevision(outsideFence(s.Issue.Body))}
	},
	"issue.sync label": func(_ string, s State) []string {
		if s.Issue == nil {
			return nil
		}
		// The landing row exists only while the grouping issue carries
		// the `sync` label, and every path asks that presence before
		// the predicate is reached — the sweep's scan, the cascade's
		// candidate and the approval path's re-check
		// (§forgejo/decisions/sync-set). No other label of a grouping
		// issue decides anything the row reads, so the presence is the
		// whole of the label fact (§forgejo/decisions/inputs).
		return []string{fmt.Sprintf("sync label %t", hasLabel(s.Issue, LabelSync))}
	},
	"issue.updated_at": func(_ string, s State) []string {
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
	"issue.comments": func(_ string, s State) []string {
		// One comment, one fact: the login that authored it and the hex
		// its body digests to, the way an issue's own description is
		// stated. Whose comments reach the fold is commentCountsAsInput's
		// to say, and the declaration above it (s-git/maitred#262).
		//
		// A thread is append-only, and its information is multiplicity:
		// every other input is a set of current facts, where stating one
		// twice says no more, while a comment repeating an earlier one's
		// text word for word is still a delivery that asked for this row.
		// So the fold states how many it counted beside what it counted,
		// and the deduplication that keeps two inputs from stating one
		// fact twice cannot swallow the repeat
		// (§forgejo/decisions/actions, s-git/maitred#262).
		var facts []string
		counted := 0
		for _, c := range s.Comments {
			if !commentCountsAsInput(c.Author, s.EngineAccount, s.DecomposerIdentity) {
				continue
			}
			counted++
			facts = append(facts, commentFactPrefix+c.Author+" "+digestRevision(c.Body))
		}
		if counted > 0 {
			facts = append(facts, fmt.Sprintf("counted comments %d", counted))
		}
		return facts
	},
	"blockers.states": func(_ string, s State) []string {
		facts := make([]string, 0, len(s.Blockers))
		for i := range s.Blockers {
			facts = append(facts, blockerRef(s, &s.Blockers[i])+" "+s.Blockers[i].State)
		}
		return facts
	},
	"connected PRs": func(_ string, s State) []string {
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
	"blocked downstream edges": func(_ string, s State) []string {
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
	"forest digest gate": func(_ string, s State) []string {
		if s.ForestGate == nil {
			return []string{"forest gate unarmed"}
		}
		return []string{"forest " + forestRef(s.ForestGate.RootRepo, s.ForestGate.RootNumber) + " " + s.ForestGate.Current}
	},
	// The pull-request folds: the facts the pull-request rows read, each
	// stated as the row reads it, the head sha among them as the
	// declaration names it (s-git/maitred#260).
	"PR.state": func(_ string, s State) []string {
		if s.PR == nil {
			return nil
		}
		return []string{"pr state " + s.PR.State}
	},
	"PR.mergeable": func(_ string, s State) []string {
		if s.PR == nil {
			return nil
		}
		return []string{fmt.Sprintf("pr mergeable %t", s.PR.Mergeable)}
	},
	"PR.head sha": func(_ string, s State) []string {
		if s.PR == nil {
			return nil
		}
		return []string{"pr head " + s.PR.HeadSHA}
	},
	"PR.labels": func(_ string, s State) []string {
		if s.PR == nil {
			return nil
		}
		// The one pull-request label any predicate reads is the
		// fan-out's `trigger-review` request
		// (§forgejo/decisions/label-triggered-review). Folding the
		// set whole would let a toggle of a label no predicate reads
		// move the digest, and the re-fire bound — which remembers one
		// digest per (action, subject) — would pace nothing: an
		// alternating label would be an alternating digest, evading
		// the bound entirely (§forgejo/decisions/loop-prevention).
		// The label's presence is the fact the row asks
		// (§forgejo/decisions/inputs).
		return []string{fmt.Sprintf("pr label %s %t", LabelTriggerReview, carriesLabel(s.PR.Labels, LabelTriggerReview))}
	},
	"PR.author": func(_ string, s State) []string {
		if s.PR == nil {
			return nil
		}
		return []string{"pr author " + s.PR.Author}
	},
	"reviews": func(subject string, s State) []string {
		facts := make([]string, 0, len(s.Reviews))
		for i := range s.Reviews {
			r := &s.Reviews[i]
			if reviewIsDismissed(r) {
				continue // a voided verdict states no standing
			}
			if subject != "" && r.Author != subject {
				continue // a sibling's report is no subject's fact
			}
			facts = append(facts, fmt.Sprintf("review %s %s at %s", r.Author, r.Event, r.CommitID))
		}
		return facts
	},
	"config.review lenses": func(_ string, s State) []string {
		facts := make([]string, 0, len(s.ReviewLenses))
		for _, lens := range s.ReviewLenses {
			facts = append(facts, "lens "+lens.Identity)
		}
		return facts
	},
	"config.implementer identity": func(_ string, s State) []string {
		if s.ImplementerIdentity == "" {
			return []string{"implementer identity unset"}
		}
		return []string{"implementer " + s.ImplementerIdentity}
	},
	"connected issues": func(_ string, s State) []string {
		// The merge gate reads two labels of a connected work item
		// beside its identity and state: the merging opt-in on the
		// issue its body closes (§forgejo/decisions/merge-gate,
		// s-git/maitred#228) and the membership opt-in whose set holds
		// an approved PR off the merge
		// (§forgejo/decisions/sync-set). Folding the whole label set
		// would let a toggle of a label no predicate reads alternate
		// the digest between two values, and the re-fire bound — which
		// remembers one digest per `(action, subject)` — would pace
		// nothing (§forgejo/decisions/loop-prevention). The two
		// presences are the facts the row asks
		// (§forgejo/decisions/inputs).
		var facts []string
		for i := range s.ConnectedIssues {
			c := &s.ConnectedIssues[i]
			ref := forestRef(c.Issue.Repository, c.Issue.Number)
			facts = append(facts,
				"issue "+ref+" "+c.Issue.State,
				fmt.Sprintf("issue %s label %s %t", ref, LabelAgenticAutoMerge, hasLabel(&c.Issue, LabelAgenticAutoMerge)),
				fmt.Sprintf("issue %s label %s %t", ref, LabelAgenticSyncMerge, hasLabel(&c.Issue, LabelAgenticSyncMerge)))
		}
		return facts
	},
	"connected issues' blockers": func(_ string, s State) []string {
		var facts []string
		for i := range s.ConnectedIssues {
			for j := range s.ConnectedIssues[i].Blockers {
				b := &s.ConnectedIssues[i].Blockers[j]
				facts = append(facts, forestRef(b.Repository, b.Number)+" "+b.State)
			}
		}
		return facts
	},
	"connected issues' sync sets": func(_ string, s State) []string {
		var facts []string
		for i := range s.ConnectedIssues {
			for j := range s.ConnectedIssues[i].SyncSets {
				facts = append(facts, syncSetFact(&s.ConnectedIssues[i].SyncSets[j]))
			}
		}
		return facts
	},
	"sync sets": func(_ string, s State) []string {
		facts := make([]string, 0, len(s.SyncSets))
		for i := range s.SyncSets {
			facts = append(facts, syncSetFact(&s.SyncSets[i]))
		}
		return facts
	},
	"sync members": func(_ string, s State) []string {
		// The landing-set predicate reads two facts of a member: its
		// state, and whether it carries the membership opt-in
		// (§forgejo/decisions/sync-set). Folding the member's whole
		// label set would let a toggle that moves no readiness re-run
		// the landing prompt: the same noise §forgejo/decisions/sync-set
		// argued updated_at out of the fold for (s-git/maitred#260).
		var facts []string
		for i := range s.SyncMembers {
			m := &s.SyncMembers[i]
			facts = append(facts, fmt.Sprintf("member %s %s %s %t",
				forestRef(m.Issue.Repository, m.Issue.Number), m.Issue.State,
				LabelAgenticSyncMerge, carriesLabel(m.Issue.Labels, LabelAgenticSyncMerge)))
		}
		return facts
	},
	"members' PRs": func(_ string, s State) []string {
		var facts []string
		for i := range s.SyncMembers {
			m := &s.SyncMembers[i]
			if m.PR == nil {
				facts = append(facts, fmt.Sprintf("%s delivers nothing", forestRef(m.Issue.Repository, m.Issue.Number)))
				continue
			}
			facts = append(facts, fmt.Sprintf("member pr %d state %s mergeable %t head %s",
				m.PR.Number, m.PR.State, m.PR.Mergeable, m.PR.HeadSHA))
		}
		return facts
	},
	"members' PR reviews": func(_ string, s State) []string {
		// The member test's read is the chain's standing at the member's
		// head, not the review listing whole: a set re-fires when a member
		// becomes approved at head, and holds while it does not
		// (§forgejo/decisions/sync-set).
		var facts []string
		for i := range s.SyncMembers {
			m := &s.SyncMembers[i]
			if m.PR == nil {
				continue
			}
			facts = append(facts, fmt.Sprintf("member %s approved at head %t",
				forestRef(m.Issue.Repository, m.Issue.Number), allLensesApproved(m.Reviews, s.ReviewLenses, m.PR.HeadSHA)))
		}
		return facts
	},
}

// syncSetFact states a grouping issue as a landing-set fact: the set
// stands or falls with its own state.
func syncSetFact(set *Issue) string {
	return "sync set " + forestRef(set.Repository, set.Number) + " " + set.State
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
// edited/labelled/commented row, and what moves it is a fact the
// declaration names. commentCountsAsInput says whose comments reach that
// row: the engine's own report, or the decomposer's own question, on an
// ordinary work item holds off naming the authorship it read rather than
// re-firing at the re-fire bound's pace for as long as the issue stands
// (§forgejo/decisions/loop-prevention, s-git/maitred#262).
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
// specification states whole: the lenses owed a dispatch are the
// configured lenses that have reported nothing at the PR's current head
// (§forgejo/decisions/review-lenses). A lens whose report landed at head
// is not owed another — the standing is an input this action declares,
// and a lens that reported feedback stands for the review-following rows
// to decide, not for a second reading of the same head. When at least one
// lens is owed the fan-out row composes the per-lens dispatch and applies
// each lens's pacing; when every lens has reported the PR belongs to the
// review-following rows, and that is the hold-off named.
func reviewReady(_ Event, s State) (Decision, readyAnswer) {
	pr := s.PR
	if pr == nil {
		return holdOff("no PR state re-fetched"), readyNo
	}
	head := s.PR.HeadSHA
	var owed []string
	for _, lens := range s.ReviewLenses {
		if !lensStandingAtHead(s.Reviews, lens, head).reported {
			owed = append(owed, lens.Identity)
		}
	}
	if len(owed) > 0 {
		return dispatch(ActionReview, fmt.Sprintf("lens %s has no undismissed report at head %s", strings.Join(owed, ", "), head)), readyYes
	}
	return holdOff(fmt.Sprintf("PR %d: every review lens (%s) has an undismissed report at head %s; the review-following rows decide", pr.Number, strings.Join(lensIdentities(s.ReviewLenses), ", "), head)), readyNo
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
	gate := prReviewGate(s.Reviews, s.ReviewLenses, pr.HeadSHA)
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
	gate := prReviewGate(s.Reviews, s.ReviewLenses, pr.HeadSHA)
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
	return dispatch(ActionMergeOrWait, fmt.Sprintf("PR %d stands at approving reviews from every lens and %s",
		pr.Number, issueOptInClause(ref))), readyYes
}

// decomposeReady is decompose's predicate, the digest gate's
// comparison wherever the gate is armed, and the root's standing
// wherever it is not — the event path's kind-conflicting root, the
// only case the sweep never reaches, keeps its revision-keyed pacing
// in the row rather than in the predicate
// (§forgejo/decisions/actionable-state-digest,
// §forgejo/reconciliation/the-state-is-the-watermark, s-git/maitred#209).
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
		// A commissioned tracker carries its forest's curation under the
		// same gate as the root's row: one forest, one answer, whichever
		// row asks. How many of the forest's members still stand is no
		// input here — the tracker's own open state is what keeps the
		// forest under curation (§forgejo/decisions/actions, s-git/maitred#279).
		return digestGateAnswer(s.ForestGate, fmt.Sprintf("issue %d is an open %s tracker commissioned by %s %d", issue.Number, LabelOutcome, LabelIdeation, gate.Number),
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
// §forgejo/reconciliation/the-state-is-the-watermark, s-git/maitred#209).
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
// §forgejo/reconciliation/the-state-is-the-watermark).
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
	return dispatch(ActionSync, fmt.Sprintf("issue %d is an open %s issue: all %d member(s) closed or approved, mergeable, and opted in; the state calls for sync at the set's declared digest %s",
		issue.Number, LabelSync, len(members), digestFor(ActionSync, Event{}, s))), readyYes
}
