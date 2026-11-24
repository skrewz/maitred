package forgejo

import (
	"strings"
	"testing"
	"time"
)

// everyAction lists the actions the decision function can name
// (§forgejo/decisions/actions).
var everyAction = []Action{
	ActionImplement, ActionReassess, ActionReview, ActionFixFeedback,
	ActionMergeOrWait, ActionRebase, ActionDecompose, ActionWrapUp,
	ActionTeardownForest, ActionSync,
}

// TestActionDeclarations_Complete checks the declaration table carries
// exactly one declaration per action, each naming a non-empty input
// set and carrying a ready predicate and a digest
// (§forgejo/decisions/actions).
func TestActionDeclarations_Complete(t *testing.T) {
	if len(actionDecls) != len(everyAction) {
		t.Errorf("the table declares %d actions, want %d", len(actionDecls), len(everyAction))
	}
	for _, a := range everyAction {
		decl, ok := actionDecls[a]
		if !ok {
			t.Errorf("action %q carries no declaration", a)
			continue
		}
		if len(decl.inputs) == 0 {
			t.Errorf("action %q declares no input set", a)
		}
		if decl.ready == nil {
			t.Errorf("action %q carries no ready predicate", a)
		}
		if decl.digest == nil {
			t.Errorf("action %q carries no digest", a)
		}
	}
}

// TestActionDeclarations_FoldComponentsAreDeclaredByDecompose checks
// the actionable-state fold's component set is drawn from the
// `decompose` declaration rather than a list living beside the fold:
// foldComponents reads the declaration, and no other action declares
// components (§forgejo/decisions/actionable-state-digest).
func TestActionDeclarations_FoldComponentsAreDeclaredByDecompose(t *testing.T) {
	decl := declarationFor(ActionDecompose)
	if len(decl.components) == 0 {
		t.Fatal("the decompose declaration carries no component set")
	}
	for _, a := range everyAction {
		if a == ActionDecompose {
			continue
		}
		if declarationFor(a).components != nil {
			t.Errorf("action %q declares a component set; only decompose folds one", a)
		}
	}
	got := foldComponents()
	if len(got) != len(decl.components) {
		t.Fatalf("foldComponents returns %d components, the declaration names %d", len(got), len(decl.components))
	}
	for i, want := range decl.components {
		if got[i] != want {
			t.Errorf("component %d = %+v, want the declared %+v", i, got[i], want)
		}
	}
	// The declared keys are the fold's fact prefixes: root, member,
	// tracker, blocker, comment, in reporting order
	// (§forgejo/decisions/actionable-state-digest).
	wantKeys := []string{"root", "member", "tracker", "blocker", "comment"}
	if len(got) != len(wantKeys) {
		t.Fatalf("the declaration names %d components, want %d", len(got), len(wantKeys))
	}
	for i, key := range wantKeys {
		if got[i].key != key {
			t.Errorf("component %d key = %q, want %q", i, got[i].key, key)
		}
	}
}

// TestActionDeclarations_DigestsAreDeclared pins what each declaration's
// digest derives from: an issue-path action's digest is the fold of its
// own input set, and the pull-request and landing-set actions name an
// object revision — a head sha, the landing set's derived revision —
// until the next member of the chain re-keys them
// (s-git/maitred#260). (§forgejo/state/watermark, s-git/maitred#258,
// s-git/maitred#259.)
func TestActionDeclarations_DigestsAreDeclared(t *testing.T) {
	issue := trackerIssue()
	pr := openPR("sha-head")
	members := []SyncMember{syncMemberFixture(4, 40)}
	st := State{Issue: issue, PR: pr, SyncMembers: members, ReviewLenses: testLenses}
	for _, a := range []Action{ActionImplement, ActionReassess, ActionDecompose, ActionWrapUp, ActionTeardownForest} {
		if !digestFoldsInputs(a) {
			t.Errorf("digestFor(%s) does not fold the declaration's input set", a)
		}
		if got, want := digestFor(a, Event{}, st), hashInputFacts(declaredInputFacts(a, st)); got != want {
			t.Errorf("digestFor(%s) = %q, want the declared fold %q", a, got, want)
		}
	}
	for _, a := range []Action{ActionReview, ActionFixFeedback, ActionMergeOrWait, ActionRebase} {
		if digestFoldsInputs(a) {
			t.Errorf("digestFor(%s) folds a declared input set; the pull-request rows are the next member of this chain", a)
		}
		if got, want := digestFor(a, Event{}, st), pr.HeadSHA; got != want {
			t.Errorf("digestFor(%s) = %q, want the PR head sha %q", a, got, want)
		}
	}
	if got, want := digestFor(ActionSync, Event{}, st), syncSetRevision(issue, members, testLenses); got != want {
		t.Errorf("digestFor(sync) = %q, want the set revision %q", got, want)
	}
}

// TestDeclaredDigests_FoldEveryDeclaredFact checks that no event fact
// folds into a digest the two paths must answer with one value: an event
// fact would make one state yield two digests
// (§forgejo/decisions/inputs,
// §forgejo/reconciliation/the-state-is-the-watermark, s-git/maitred#259).
// The companion claim — that a declaration naming a state fact no fold
// states is a mistake rather than a digest — is no longer this test's to
// make: validateFoldingDeclarations refuses it as the declaration table
// is built, so no test binary with such a gap ever reaches a
// table walk.
func TestDeclaredDigests_FoldEveryDeclaredFact(t *testing.T) {
	// One state, every path: the sweep's synthetic event and an issue's
	// activity events must reach one digest, whatever row brought them.
	st := State{ReviewLenses: testLenses, Issue: openIssue(), Blockers: []Issue{{Number: 3, State: "closed", Repository: "o/r"}}}
	for _, ev := range []Event{
		{Type: EventReconcile, Repo: "o/r", Kind: KindIssue, Number: 7},
		{Type: EventIssueOpened, Repo: "o/r", Kind: KindIssue, Number: 7, Sender: "alice"},
		{Type: EventIssueEdited, Repo: "o/r", Kind: KindIssue, Number: 7, Sender: "carol"},
		{Type: EventIssueCommented, Repo: "o/r", Kind: KindIssue, Number: 7, Sender: "dave"},
	} {
		for _, a := range []Action{ActionImplement, ActionReassess, ActionWrapUp} {
			if got, want := digestFor(a, ev, st), digestFor(a, Event{}, st); got != want {
				t.Errorf("digestFor(%s) under %s = %q, want the sweep's %q: an event fact entered a digest", a, ev.Type, got, want)
			}
		}
	}
}

// TestDeclaredDigests_MoveWithTheFactsThePredicatesRead checks the
// digest answers a state relation the forge delivers no event for — a
// blocker edge attached or removed — and answers the listings' order
// once, so the guard cannot be the reason an edge move goes unhandled
// (§forgejo/reconciliation/watermark-exemptions,
// §forgejo/decisions/actionable-state-digest, s-git/maitred#259).
func TestDeclaredDigests_MoveWithTheFactsThePredicatesRead(t *testing.T) {
	issue := openIssue()
	blocker := Issue{Number: 3, State: "open", Repository: "o/r"}
	closed := Issue{Number: 3, State: "closed", Repository: "o/r"}
	st := State{ReviewLenses: testLenses, Issue: issue, Blockers: []Issue{blocker}}
	stUnblocked := State{ReviewLenses: testLenses, Issue: issue, Blockers: []Issue{closed}}
	for _, a := range []Action{ActionImplement, ActionReassess, ActionWrapUp} {
		if digestFor(a, Event{}, st) == digestFor(a, Event{}, stUnblocked) {
			t.Errorf("digestFor(%s) does not move when a blocker's state moves", a)
		}
	}

	// Order-insensitive: the same set of blockers listed the other way.
	two := []Issue{{Number: 3, State: "open", Repository: "o/r"}, {Number: 4, State: "closed", Repository: "o/r"}}
	reversed := []Issue{two[1], two[0]}
	for _, a := range []Action{ActionImplement, ActionWrapUp} {
		if got, want := digestFor(a, Event{}, State{Issue: issue, Blockers: two}), digestFor(a, Event{}, State{Issue: issue, Blockers: reversed}); got != want {
			t.Errorf("digestFor(%s) = %q over one listing order, want the other's %q", a, got, want)
		}
	}
	// A label the forge lists the other way round is the same state.
	shuffled := openIssue()
	shuffled.Labels = []string{"enhancement", "bug"}
	twisted := openIssue()
	twisted.Labels = []string{"bug", "enhancement"}
	for _, a := range []Action{ActionImplement, ActionReassess} {
		if got, want := digestFor(a, Event{}, State{Issue: shuffled}), digestFor(a, Event{}, State{Issue: twisted}); got != want {
			t.Errorf("digestFor(%s) = %q over one label order, want the other's %q", a, got, want)
		}
	}

	// A connected pull request opening and closing is a fact the
	// implement predicate reads, so it must move the digest
	// (§forgejo/decisions/transition-table).
	connected := State{Issue: issue, ConnectedPRs: []PullRequest{{Number: 9, State: "open"}}}
	if digestFor(ActionImplement, Event{}, connected) == digestFor(ActionImplement, Event{}, State{Issue: issue}) {
		t.Error("digestFor(implement) does not move when a pull request connects")
	}

	// A teardown order over a forest whose members are all spared
	// trackers states no downstream fact at all.
	root := ideationIssue()
	root.Labels = append(root.Labels, LabelTeardownForest)
	teardown := State{Issue: root, Blocked: []Issue{*trackerIssue()}}
	if got := declaredInputFacts(ActionTeardownForest, teardown); len(got) == 0 {
		t.Error("a root blocking only a spared tracker states no fact at all; want the kind labels folded besides")
	}
	spared := State{Issue: root, Blocked: []Issue{*trackerIssue()}}
	withMember := State{Issue: root, Blocked: []Issue{*trackerIssue(), *openIssue()}}
	if digestFor(ActionTeardownForest, Event{}, spared) == digestFor(ActionTeardownForest, Event{}, withMember) {
		t.Error("digestFor(teardown-forest) does not move when a deletable member appears")
	}
}

// TestDeclaredDigests_LeaveTheTrackersWrittenRegionOut checks the one
// fact the engine moves by writing it is absent from the digest: a
// tracker's current-state block, rewritten by every wrap-up's draw,
// must not move the digest a wrap-up is watermarked on, whatever the
// tick does to the row's timestamp (§forgejo/reconciliation/the-tracker-block,
// §forgejo/decisions/loop-prevention, s-git/maitred#259).
func TestDeclaredDigests_LeaveTheTrackersWrittenRegionOut(t *testing.T) {
	tracker := trackerIssue()
	tracker.Body = "## Outcome\n\nThe thing we said we would ship.\n\n## Current state\n\n" +
		fenceBegin + "\n\nLast revisited: 2026-01-01 00:00 UTC\n\n```mermaid\nflowchart TD\n```\n\n" + fenceEnd
	written := trackerIssue()
	written.Body = strings.Replace(tracker.Body, "Last revisited: 2026-01-01 00:00 UTC", "Last revisited: 2026-01-02 00:00 UTC", 1)
	written.UpdatedAt = tracker.UpdatedAt.Add(time.Hour)
	for _, a := range []Action{ActionWrapUp, ActionReassess, ActionImplement} {
		if got, want := digestFor(a, Event{}, State{Issue: written}), digestFor(a, Event{}, State{Issue: tracker}); got != want {
			t.Errorf("digestFor(%s) = %q after the engine rewrote the tracker's region, want the unchanged %q", a, got, want)
		}
	}
	// The prose outside the region is the outcome's own, and moves it.
	edited := trackerIssue()
	edited.Body = strings.Replace(tracker.Body, "The thing we said we would ship.", "What we shipped, restated by a human.", 1)
	if digestFor(ActionWrapUp, Event{}, State{Issue: tracker}) == digestFor(ActionWrapUp, Event{}, State{Issue: edited}) {
		t.Error("digestFor(wrap-up) does not move when the tracker's prose outside the region is edited")
	}
}

// TestDeclaredDigests_CommentsAreReassessInputs pins the acceptance that
// comments stand among `reassess`'s declared inputs, that whose comments
// count is stated once — in the fold of that input — and that no issue
// timestamp stands in the digest at all, the key the comment rows used
// to turn on (§forgejo/decisions/actions, §forgejo/decisions/inputs,
// s-git/maitred#262).
func TestDeclaredDigests_CommentsAreReassessInputs(t *testing.T) {
	quiet := State{
		ReviewLenses:       testLenses,
		Issue:              openIssue(),
		EngineAccount:      "maitred",
		DecomposerIdentity: "decomposer",
	}
	counted := quiet
	counted.Comments = []Comment{{Author: "alice", Body: "and add this too"}}
	if digestFor(ActionReassess, Event{}, quiet) == digestFor(ActionReassess, Event{}, counted) {
		t.Error("a comment authored outside the engine leaves reassess's digest where it was")
	}
	for _, author := range []string{"maitred", "decomposer"} {
		excluded := quiet
		excluded.Comments = []Comment{{Author: author, Body: "my own report"}}
		if digestFor(ActionReassess, Event{}, quiet) != digestFor(ActionReassess, Event{}, excluded) {
			t.Errorf("a comment authored by %q moved reassess's digest, which the declaration excludes", author)
		}
	}
	// Both halves of the rule read together: an excluded author adds no
	// fact of its own beside one the fold counts.
	beside := quiet
	beside.Comments = []Comment{{Author: "alice", Body: "and add this too"}, {Author: "maitred", Body: "my own report"}}
	if digestFor(ActionReassess, Event{}, counted) != digestFor(ActionReassess, Event{}, beside) {
		t.Error("an excluded author's comment folded a fact beside a counted one")
	}
	// Order-insensitive, as every other fold.
	two := quiet
	two.Comments = []Comment{{Author: "alice", Body: "and add this too"}, {Author: "bob", Body: "not that"}}
	reversed := quiet
	reversed.Comments = []Comment{two.Comments[1], two.Comments[0]}
	if digestFor(ActionReassess, Event{}, two) != digestFor(ActionReassess, Event{}, reversed) {
		t.Error("reassess's digest answers one listing of comments with two digests")
	}
	// A comment the forge lists with no author at all — the shape of a
	// response without a user object — belongs to no excluded identity,
	// so it counts, as the forest fold's exclusion already treats it
	// (§forgejo/decisions/actionable-state-digest).
	authorless := quiet
	authorless.Comments = []Comment{{Body: "relayed from somewhere else"}}
	if digestFor(ActionReassess, Event{}, quiet) == digestFor(ActionReassess, Event{}, authorless) {
		t.Error("a comment with no author folded no fact, though no identity claims it")
	}
	// The moment an issue was last touched moves with a comment, a label,
	// an edit — anything at all — so it stands in no digest a dispatch is
	// keyed on.
	stamped := quiet
	late := *openIssue()
	late.UpdatedAt = testTime.Add(72 * time.Hour)
	stamped.Issue = &late
	if digestFor(ActionReassess, Event{}, quiet) != digestFor(ActionReassess, Event{}, stamped) {
		t.Error("reassess's digest moves with the issue's updated_at")
	}
	// The quoted revision names the comment an operator can go and read.
	got := quotedRevision(ActionReassess, issueEventBy(EventIssueCommented, "alice"), counted, digestFor(ActionReassess, Event{}, counted))
	if want := "comment alice " + digestRevision("and add this too"); !strings.Contains(got, want) {
		t.Errorf("quoted revision %q names no %q", got, want)
	}
}

// TestQuotedRevision_NamesTheFactsBehindTheDigest checks the digest a
// reason quotes is stated in the words an operator can line up against
// the forge's history, the rule every other revision quote already
// follows (§forgejo/observability/the-decision-log, s-git/maitred#259).
func TestQuotedRevision_NamesTheFactsBehindTheDigest(t *testing.T) {
	st := State{ReviewLenses: testLenses, Issue: openIssue(), Blockers: []Issue{{Number: 3, State: "closed", Repository: "o/r"}}}
	ev := issueEvent(EventIssueOpened)
	got := quotedRevision(ActionImplement, ev, st, digestFor(ActionImplement, ev, st))
	for _, want := range []string{"state open", "updated ", "o/r#3 closed"} {
		if !strings.Contains(got, want) {
			t.Errorf("quoted revision %q names no %q", got, want)
		}
	}
	// An `outcome` tracker's fold states the facts its masked revision
	// hashes rather than the region the engine rewrites every tick, so
	// the wrap-up row that quotes its digest names what an operator can
	// place in the forge's history (§forgejo/reconciliation/the-tracker-block).
	tracker := State{Issue: trackerIssue()}
	got = quotedRevision(ActionWrapUp, Event{}, tracker, digestFor(ActionWrapUp, Event{}, tracker))
	for _, want := range []string{"state open", "label " + LabelOutcome, "body " + digestRevision(outsideFence(tracker.Issue.Body))} {
		if !strings.Contains(got, want) {
			t.Errorf("a tracker's quoted revision %q names no %q", got, want)
		}
	}
	// A revision the action does not fold — a pull request's head sha,
	// quoted by the row that keys on it — is quoted as what it is.
	if got := quotedRevision(ActionReview, prEvent(EventPRReviewed, "carol"), State{PR: openPR("abc")}, "abc"); got != "revision abc" {
		t.Errorf("quoted revision = %q, want the head sha quoted as a revision", got)
	}
}

// TestDeclaredDigests_AgreeWithTheLoggedRevision pins the claim the
// watermark and the log rest on together: the digest a dispatch
// records, the revision the log names for it, the dedup key's revision
// and the re-fire stamp are one derivation — the dispatched action's
// declared digest. A hold-off names no action to derive one from and
// names the object's own revision instead, which is what the log's
// reader can line up against the forge (§forgejo/state/watermark,
// §forgejo/webhook/the-pipeline, §forgejo/observability/the-decision-log,
// s-git/maitred#259). A re-key of one action's digest is an edit to
// that declaration and to this test, not to a second derivation.
func TestDeclaredDigests_AgreeWithTheLoggedRevision(t *testing.T) {
	for _, path := range []struct {
		// name is the write path whose state the case stands for.
		name    string
		ev      Event
		st      State
		actions []Action
	}{
		{
			name:    "the event pipeline's issue leg",
			ev:      Event{Type: EventIssueOpened, Repo: "o/r", Kind: KindIssue, Number: 7},
			st:      State{Issue: openIssue()},
			actions: []Action{ActionImplement, ActionReassess, ActionDecompose, ActionWrapUp, ActionTeardownForest},
		},
		{
			// The cascade's issue leg holds the issue and its blockers,
			// so a digest may not need more than that.
			name:    "the unblock cascade's issue leg",
			ev:      Event{Type: EventIssueOpened, Repo: "o/r", Kind: KindIssue, Number: 7},
			st:      State{Issue: trackerIssue(), Blockers: []Issue{{Number: 3, State: "closed", Repository: "o/r"}}},
			actions: []Action{ActionImplement, ActionWrapUp},
		},
		{
			name:    "the event pipeline's PR leg",
			ev:      Event{Type: EventPRReviewed, Repo: "o/r", Kind: KindPR, Number: 9},
			st:      State{PR: openPR("sha-head"), ReviewLenses: testLenses},
			actions: []Action{ActionReview, ActionFixFeedback, ActionMergeOrWait, ActionRebase},
		},
		{
			name:    "the sweep's sync row and the cascade's set leg",
			ev:      Event{Type: EventReconcile, Repo: "o/r", Kind: KindIssue, Number: 11},
			st:      State{Issue: syncIssueFixture(11), SyncMembers: []SyncMember{syncMemberFixture(4, 40)}, ReviewLenses: testLenses},
			actions: []Action{ActionSync},
		},
	} {
		t.Run(path.name, func(t *testing.T) {
			for _, a := range path.actions {
				d := dispatch(a, "the case stands for a dispatch")
				if got, want := loggedRevision(d, path.ev, path.st), digestFor(a, path.ev, path.st); got != want {
					t.Errorf("the log names %q for %s, the watermark records %q", got, a, want)
				}
			}
			// A hold-off names the object revision, the value a reader
			// of the log can place in the forge's history.
			if got, want := loggedRevision(holdOff("nothing to dispatch"), path.ev, path.st), revisionFor(path.ev, path.st); got != want {
				t.Errorf("a hold-off logged %q, want the object revision %q", got, want)
			}
		})
	}
}

// predicateAgreement is one scenario in which a row asks its action's
// declaration: the predicate's answer must be the answer the row
// gives — a ready row dispatches with the predicate's reason, a
// not-ready row holds off with it — so no row states the readiness
// beside the declaration a second time (§forgejo/decisions/actions).
type predicateAgreement struct {
	name   string
	action Action
	event  Event
	state  State
	ready  bool
}

func TestPredicates_AgreeWithRows(t *testing.T) {
	// A root whose forest's digest moved; the stored slot names the
	// component that moved (§forgejo/decisions/actionable-state-digest).
	rootMovedGate := &ForestDigestGate{
		RootRepo: "o/r", RootNumber: 7,
		Current: "cur", CurrentParts: map[string]string{"comment": "c2"},
		Stored: "old", StoredParts: map[string]string{"comment": "c1"},
	}
	rootStill := &ForestDigestGate{
		RootRepo: "o/r", RootNumber: 7,
		Current: "same", CurrentParts: map[string]string{"root": "r1"},
		Stored: "same", StoredParts: map[string]string{"root": "r1"},
	}
	notMergeable := notMergeablePR("sha-x")
	approvedPR := closesIssues(openPR("abc"), "o/r#4")
	optedInState := func() State {
		return optedIntoMerge(State{
			ReviewLenses: testLenses,
			PR:           approvedPR,
			Reviews:      []Review{review(ReviewApproved, "carol", testTime)},
		})
	}
	feedbackState := State{
		ReviewLenses:        testLenses,
		PR:                  openPRBy("bot", "abc"),
		Reviews:             []Review{review(ReviewChangesRequested, "carol", testTime)},
		ImplementerIdentity: "bot",
	}

	for _, tt := range []predicateAgreement{
		{
			name: "implement on a quiet open issue", action: ActionImplement,
			event: issueEvent(EventIssueOpened), state: State{Issue: openIssue()}, ready: true,
		},
		{
			name: "implement on a blocked issue", action: ActionImplement,
			event: issueEvent(EventIssueOpened),
			state: State{Issue: openIssue(), Blockers: []Issue{{Number: 3, State: "open", Repository: "o/r"}}},
		},
		{
			name: "reassess on an edited issue", action: ActionReassess,
			event: issueEvent(EventIssueEdited), state: State{Issue: openIssue()}, ready: true,
		},
		{
			name: "decompose on an event-path root, gate unarmed", action: ActionDecompose,
			event: issueEvent(EventIssueEdited), state: State{Issue: ideationIssue()}, ready: true,
		},
		{
			name: "decompose on a sweep root whose digest moved", action: ActionDecompose,
			event: issueEvent(EventReconcile), state: State{Issue: ideationIssue(), ForestGate: rootMovedGate}, ready: true,
		},
		{
			name: "decompose on a sweep root whose digest is unchanged", action: ActionDecompose,
			event: issueEvent(EventReconcile), state: State{Issue: ideationIssue(), ForestGate: rootStill},
		},
		{
			name: "teardown on a sweep root with a forest standing", action: ActionTeardownForest,
			event: issueEvent(EventReconcile),
			state: State{Issue: labelledIssue(LabelIdeation, LabelTeardownForest), Blocked: []Issue{{Number: 8, State: "open", Repository: "o/r"}}},
			ready: true,
		},
		{
			name: "teardown on a sweep root whose forest is gone", action: ActionTeardownForest,
			event: issueEvent(EventReconcile),
			state: State{Issue: labelledIssue(LabelIdeation, LabelTeardownForest), Blocked: []Issue{{Number: 9, Repository: "o/r", Labels: []string{LabelOutcome}}}},
		},
		{
			name: "wrap-up on a fully unblocked tracker", action: ActionWrapUp,
			event: issueEvent(EventReconcile), state: State{Issue: trackerIssue()}, ready: true,
		},
		{
			name: "rebase on a synced PR with conflicts", action: ActionRebase,
			event: prEvent(EventPRSynced, "alice"), state: State{PR: notMergeable, ReviewLenses: testLenses}, ready: true,
		},
		{
			name: "review with every lens approved at head", action: ActionReview,
			event: prEvent(EventPRSynced, "alice"),
			state: State{PR: openPR("abc"), Reviews: []Review{review(ReviewApproved, "carol", testTime)}, ReviewLenses: testLenses},
		},
		{
			name: "merge-or-wait on an approved opted-in PR", action: ActionMergeOrWait,
			event: prEvent(EventPRReviewed, "carol"), state: optedInState(), ready: true,
		},
		{
			name: "fix-feedback behind a changes-requested on an owned PR", action: ActionFixFeedback,
			event: prEvent(EventPRReviewed, "carol"), state: feedbackState, ready: true,
		},
		{
			name: "sync on a ready landing set", action: ActionSync,
			event: issueEvent(EventReconcile),
			state: State{
				Issue: syncIssueFixture(7), SyncMembers: []SyncMember{syncMemberFixture(4, 40), closedSyncMemberFixture(5)},
				ReviewLenses: testLenses,
			},
			ready: true,
		},
		{
			name: "sync on a set stalled on a member awaiting a human", action: ActionSync,
			event: issueEvent(EventReconcile),
			state: State{
				Issue: syncIssueFixture(7),
				SyncMembers: []SyncMember{{Issue: Issue{
					Number: 4, State: "open", Repository: "o/r", UpdatedAt: testTime,
				}}},
				ReviewLenses: testLenses,
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d, ans := readyTo(tt.action, tt.event, tt.state)
			if (ans == readyYes) != tt.ready {
				t.Fatalf("predicate answered %v, want ready=%v (reason %q)", ans, tt.ready, d.Reason)
			}
			got := Decide(tt.event, tt.state, nil)
			if tt.ready {
				if !got.Dispatched || got.Action != tt.action {
					t.Fatalf("Decide dispatched %q (dispatched=%v, reason %q), want %q", got.Action, got.Dispatched, got.Reason, tt.action)
				}
				if got.Reason != d.Reason {
					t.Errorf("Decide reason %q differs from the declaration's %q", got.Reason, d.Reason)
				}
			} else {
				if got.Dispatched {
					t.Fatalf("Decide dispatched %q, want a hold-off", got.Action)
				}
				if got.Reason != d.Reason {
					t.Errorf("Decide hold-off %q differs from the declaration's %q", got.Reason, d.Reason)
				}
			}
		})
	}
}

// TestDecomposeReady_TeardownOrderIsAskedOfTheRoot pins the scope of
// the order label inside the decompose declaration: a root carrying
// the order is the teardown declaration's forest, while a tracker
// carrying it is not a root and keeps the curation row it had before
// the declaration existed — asking the order of the tracker would
// lose that dispatch and misdescribe the issue it holds off on
// (§forgejo/decisions/forest-teardown, §forgejo/decisions/actions).
func TestDecomposeReady_TeardownOrderIsAskedOfTheRoot(t *testing.T) {
	moved := &ForestDigestGate{
		RootRepo: "o/r", RootNumber: 3,
		Current: "cur", CurrentParts: map[string]string{"root": "r2"},
		Stored: "old", StoredParts: map[string]string{"root": "r1"},
	}
	blocked := []Issue{
		{Number: 3, State: "closed", Repository: "o/r", Labels: []string{LabelIdeation}},
		{Number: 4, State: "open", Repository: "o/r"},
	}

	t.Run("a root under the order is the teardown row's", func(t *testing.T) {
		e := issueEvent(EventReconcile)
		st := State{Issue: labelledIssue(LabelIdeation, LabelTeardownForest), Blockers: blocked, ForestGate: moved}
		d, ans := readyTo(ActionDecompose, e, st)
		if ans != readyNo {
			t.Fatalf("predicate answered %v (reason %q), want a hold-off", ans, d.Reason)
		}
		want := "issue 7 is an open ideation root carrying teardown-forest: the teardown order stands above the root's row"
		if d.Reason != want {
			t.Errorf("hold-off = %q, want %q", d.Reason, want)
		}
	})

	t.Run("a tracker carrying the order keeps its curation row", func(t *testing.T) {
		e := issueEvent(EventReconcile)
		st := State{Issue: labelledIssue(LabelOutcome, LabelTeardownForest), Blockers: blocked, ForestGate: moved}
		d, ans := readyTo(ActionDecompose, e, st)
		if ans != readyYes {
			t.Fatalf("predicate answered %v (reason %q), want the curation dispatch its row made", ans, d.Reason)
		}
		if got := Decide(e, st, nil); !got.Dispatched || got.Action != ActionDecompose || got.Reason != d.Reason {
			t.Errorf("Decide gave %q dispatched=%v reason %q, want the declaration's dispatch %q",
				got.Action, got.Dispatched, got.Reason, d.Reason)
		}
	})
}

// TestReadyAtRow_NamesAnAbstainAsAFault checks that a row with no next
// question records an abstain as the wiring fault it is, rather than
// the silent nothing the decision log forbids: the chain predicates are
// the only abstaining ones, and the rows that ask them continue
// (§forgejo/observability/the-decision-log, §forgejo/decisions/actions).
func TestReadyAtRow_NamesAnAbstainAsAFault(t *testing.T) {
	// No lens has reported at head, so fix-feedback's predicate abstains
	// and the review-reading cluster falls through to the fan-out
	// elsewhere.
	d, ok := readyAtRow(ActionFixFeedback, prEvent(EventPRReviewed, "carol"), State{
		PR:           openPR("abc"),
		ReviewLenses: testLenses,
	})
	if ok {
		t.Error("readyAtRow reports the action ready on an abstain, want a hold-off")
	}
	if d.Dispatched {
		t.Errorf("readyAtRow dispatched %q, want a hold-off", d.Action)
	}
	if !strings.Contains(d.Reason, string(ActionFixFeedback)) || !strings.Contains(d.Reason, "wiring fault") {
		t.Errorf("hold-off reason = %q, want it to name the abstaining ask as a wiring fault", d.Reason)
	}
}

// TestDeclarations_InputsNameTheVocabularyOfInputs spot-checks that
// the declared sets speak the shared vocabulary of the two state
// families the specification names — state facts and the declared
// configuration inputs — rather than inventing per-action names
// (§forgejo/decisions/inputs).
func TestDeclarations_InputsNameTheVocabularyOfInputs(t *testing.T) {
	owns := func(a Action, want ...string) {
		t.Helper()
		have := map[string]bool{}
		for _, in := range declarationFor(a).inputs {
			have[in] = true
		}
		for _, in := range want {
			if !have[in] {
				t.Errorf("action %q does not declare input %q", a, in)
			}
		}
	}
	owns(ActionFixFeedback, "PR.author", "config.implementer identity")
	owns(ActionDecompose, "forest digest gate", "event.sender")
	owns(ActionMergeOrWait, "connected issues", "config.review lenses")
	owns(ActionReview, "reviews", "PR.head sha")
	owns(ActionTeardownForest, "blocked downstream edges")
	// The implementation reaches every declared fact through the
	// State fields the predicates read: a digest function must not
	// consult nothing at all.
	for _, a := range everyAction {
		if all := declarationFor(a).inputs; len(all) == 0 || strings.TrimSpace(strings.Join(all, " ")) == "" {
			t.Errorf("action %q declares an empty input set", a)
		}
	}
}
