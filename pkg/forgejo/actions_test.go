package forgejo

import (
	"strings"
	"testing"
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

// TestActionDeclarations_DigestsMatchRowRevisions pins each declared
// digest to the revision the decision path guards and records at
// today — the digests are unchanged by the declarations, and a change
// to one is a deliberate edit to exactly one line of the table
// (§forgejo/state/watermark, s-git/maitred#258).
func TestActionDeclarations_DigestsMatchRowRevisions(t *testing.T) {
	issue := trackerIssue()
	pr := openPR("sha-head")
	members := []SyncMember{syncMemberFixture(4, 40)}
	st := State{Issue: issue, PR: pr, SyncMembers: members, ReviewLenses: testLenses}
	for _, a := range []Action{ActionImplement, ActionReassess, ActionDecompose, ActionWrapUp, ActionTeardownForest} {
		if got, want := digestFor(a, Event{}, st), issueRevision(issue); got != want {
			t.Errorf("digestFor(%s) = %q, want the issue revision %q", a, got, want)
		}
	}
	for _, a := range []Action{ActionReview, ActionFixFeedback, ActionMergeOrWait, ActionRebase} {
		if got, want := digestFor(a, Event{}, st), pr.HeadSHA; got != want {
			t.Errorf("digestFor(%s) = %q, want the PR head sha %q", a, got, want)
		}
	}
	if got, want := digestFor(ActionSync, Event{}, st), syncSetRevision(issue, members, testLenses); got != want {
		t.Errorf("digestFor(sync) = %q, want the set revision %q", got, want)
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

// TestDeclaredDigests_AgreeWithTheObjectRevision pins the claim the
// watermark rests on: the digest a dispatch is watermarked on and the
// revision the decision log names and the re-fire stamp is keyed on
// are two derivations of one fact, and for every action over every
// state shape a write path hands them they answer with the same value
// (§forgejo/state/watermark, §forgejo/webhook/the-pipeline,
// §forgejo/decisions/actions). A re-key of one action's digest is
// therefore an edit to that declaration, to revisionFor, to the two
// sections named above and to this test — not to one line.
func TestDeclaredDigests_AgreeWithTheObjectRevision(t *testing.T) {
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
			// The cascade's issue leg holds the issue and its blockers
			// and nothing else, so a digest may not need more than that.
			name:    "the unblock cascade's issue leg",
			ev:      Event{Type: EventIssueOpened, Repo: "o/r", Kind: KindIssue, Number: 7},
			st:      State{Issue: trackerIssue()},
			actions: []Action{ActionImplement, ActionWrapUp},
		},
		{
			name:    "the event pipeline's PR leg",
			ev:      Event{Type: EventPRReviewed, Repo: "o/r", Kind: KindPR, Number: 9},
			st:      State{PR: openPR("sha-head"), ReviewLenses: testLenses},
			actions: []Action{ActionReview, ActionFixFeedback, ActionMergeOrWait, ActionRebase},
		},
		{
			// Both sync writes — the sweep's row and dispatchSyncSet —
			// read the set the same way, so the set revision is what
			// both derivations answer.
			name:    "the sweep's sync row and the cascade's set leg",
			ev:      Event{Type: EventReconcile, Repo: "o/r", Kind: KindIssue, Number: 11},
			st:      State{Issue: syncIssueFixture(11), SyncMembers: []SyncMember{syncMemberFixture(4, 40)}, ReviewLenses: testLenses},
			actions: []Action{ActionSync},
		},
	} {
		t.Run(path.name, func(t *testing.T) {
			for _, a := range path.actions {
				digest, rev := digestFor(a, path.ev, path.st), revisionFor(path.ev, path.st)
				if digest != rev {
					t.Errorf("digestFor(%s) = %q, want the revision the log records, %q", a, digest, rev)
				}
			}
		})
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
