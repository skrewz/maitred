package forgejo

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// Fixtures for the decision function tests
// (§forgejo/decisions/transition-table).

var (
	testTime  = time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	testTime2 = testTime.Add(time.Hour)
	testRev   = testTime.UTC().Format(time.RFC3339)
	testRev2  = testTime2.UTC().Format(time.RFC3339)
)

func openIssue() *Issue {
	return &Issue{
		Number:     7,
		Title:      "an issue",
		State:      "open",
		UpdatedAt:  testTime,
		HTMLURL:    "https://forge.example.com/o/r/issues/7",
		Repository: "o/r",
	}
}

func closedIssue() *Issue {
	issue := openIssue()
	issue.State = "closed"
	return issue
}

// labelledIssue is an open issue carrying the given labels
// (§forgejo/decisions/transition-table).
func labelledIssue(labels ...string) *Issue {
	issue := openIssue()
	issue.Labels = labels
	return issue
}

// trackerIssue returns an open issue carrying the reserved outcome
// label (§forgejo/reconciliation/watermark-exemptions).
func trackerIssue() *Issue {
	issue := openIssue()
	issue.Labels = append(issue.Labels, LabelOutcome)
	return issue
}

// ideationIssue returns an open issue carrying the reserved ideation
// label (§forgejo/reconciliation/watermark-exemptions).
func ideationIssue() *Issue {
	issue := openIssue()
	issue.Labels = append(issue.Labels, LabelIdeation)
	return issue
}

func openPR(sha string) *PullRequest {
	return &PullRequest{
		Number:    9,
		Title:     "a PR",
		State:     "open",
		Mergeable: true,
		HeadSHA:   sha,
		HTMLURL:   "https://forge.example.com/o/r/pulls/9",
	}
}

func issueEvent(typ EventType) Event {
	return Event{Type: typ, Repo: "o/r", Kind: KindIssue, Number: 7, Sender: "alice"}
}

func prEvent(typ EventType, sender string) Event {
	return Event{Type: typ, Repo: "o/r", Kind: KindPR, Number: 9, Sender: sender}
}

func review(event, author string, at time.Time) Review {
	return Review{Event: event, Author: author, CommitID: "abc", SubmittedAt: at}
}

// TestActionNames pins the action names: they form the enum the engine
// config maps to canned prompts (§forgejo/decisions/actions).
func TestActionNames(t *testing.T) {
	want := map[Action]string{
		ActionImplement:   "implement",
		ActionReassess:    "reassess",
		ActionReview:      "review",
		ActionReReview:    "re-review",
		ActionFixFeedback: "fix-feedback",
		ActionMergeOrWait: "merge-or-wait",
		ActionRebase:      "rebase",
		ActionDecompose:   "decompose",
		ActionWrapUp:      "wrap-up",
	}
	for action, name := range want {
		if string(action) != name {
			t.Errorf("action = %q, want %q", action, name)
		}
	}
}

// TestDecide covers every transition in the table, the idempotency
// (watermark) guard, and the sender guard
// (§forgejo/decisions/transition-table, §forgejo/decisions/loop-prevention).
func TestDecide(t *testing.T) {
	tests := []struct {
		name        string
		event       Event
		state       State
		watermark   *Watermark
		wantAction  Action
		wantCascade []Key
	}{
		{
			name:       "issue opened dispatches implement",
			event:      issueEvent(EventIssueOpened),
			state:      State{Issue: openIssue()},
			wantAction: ActionImplement,
		},
		{
			name:  "issue opened with an open blocker holds off",
			event: issueEvent(EventIssueOpened),
			state: State{Issue: openIssue(), Blockers: []Issue{{Number: 3, State: "open", Repository: "o/r"}}},
		},
		{
			name:       "issue opened with only closed blockers dispatches implement",
			event:      issueEvent(EventIssueOpened),
			state:      State{Issue: openIssue(), Blockers: []Issue{{Number: 3, State: "closed", Repository: "o/r"}}},
			wantAction: ActionImplement,
		},
		{
			name:  "issue opened with a connected PR holds off",
			event: issueEvent(EventIssueOpened),
			state: State{Issue: openIssue(), ConnectedPRs: []PullRequest{*openPR("abc")}},
		},
		{
			name:  "issue opened but closed on re-fetch holds off",
			event: issueEvent(EventIssueOpened),
			state: State{Issue: closedIssue()},
		},
		{
			name:  "issue opened carrying human-task holds off",
			event: issueEvent(EventIssueOpened),
			state: State{Issue: labelledIssue(LabelHumanTask)},
		},
		{
			name:  "issue opened carrying ideation holds off",
			event: issueEvent(EventIssueOpened),
			state: State{Issue: labelledIssue("ideation")},
		},
		{
			name:  "issue opened carrying outcome holds off",
			event: issueEvent(EventIssueOpened),
			state: State{Issue: labelledIssue("outcome")},
		},
		{
			name:  "reconciled issue carrying ideation holds off",
			event: issueEvent(EventReconcile),
			state: State{Issue: labelledIssue("ideation")},
		},
		{
			name:  "issue opened without re-fetched state holds off",
			event: issueEvent(EventIssueOpened),
		},
		{
			name:       "issue edited dispatches reassess",
			event:      issueEvent(EventIssueEdited),
			state:      State{Issue: openIssue()},
			wantAction: ActionReassess,
		},
		{
			name:       "issue labels changed dispatches reassess",
			event:      issueEvent(EventIssueLabelsChanged),
			state:      State{Issue: openIssue()},
			wantAction: ActionReassess,
		},
		{
			name:       "issue feedback comment dispatches reassess",
			event:      issueEvent(EventIssueCommented),
			state:      State{Issue: openIssue()},
			wantAction: ActionReassess,
		},
		{
			name:  "issue edited with an open blocker holds off",
			event: issueEvent(EventIssueEdited),
			state: State{Issue: openIssue(), Blockers: []Issue{{Number: 3, State: "open", Repository: "o/r"}}},
		},
		{
			name:  "issue labels changed with an open blocker holds off",
			event: issueEvent(EventIssueLabelsChanged),
			state: State{Issue: openIssue(), Blockers: []Issue{{Number: 3, State: "open", Repository: "o/r"}}},
		},
		{
			name:  "issue commented with an open blocker holds off",
			event: issueEvent(EventIssueCommented),
			state: State{Issue: openIssue(), Blockers: []Issue{{Number: 3, State: "open", Repository: "o/r"}}},
		},
		{
			name:       "issue edited with only closed blockers dispatches reassess",
			event:      issueEvent(EventIssueEdited),
			state:      State{Issue: openIssue(), Blockers: []Issue{{Number: 3, State: "closed", Repository: "o/r"}}},
			wantAction: ActionReassess,
		},
		{
			name:  "issue edited but closed on re-fetch holds off",
			event: issueEvent(EventIssueEdited),
			state: State{Issue: closedIssue()},
		},
		{
			name:  "issue edited carrying ideation holds off",
			event: issueEvent(EventIssueEdited),
			state: State{Issue: labelledIssue("ideation")},
		},
		{
			name:  "issue labels changed carrying outcome holds off",
			event: issueEvent(EventIssueLabelsChanged),
			state: State{Issue: labelledIssue("outcome")},
		},
		{
			name:  "issue commented carrying human-task holds off",
			event: issueEvent(EventIssueCommented),
			state: State{Issue: labelledIssue(LabelHumanTask)},
		},
		{
			name:       "issue edited carrying an ordinary label dispatches reassess",
			event:      issueEvent(EventIssueEdited),
			state:      State{Issue: labelledIssue("bug")},
			wantAction: ActionReassess,
		},
		{
			name:        "issue closed runs the unblock cascade",
			event:       issueEvent(EventIssueClosed),
			state:       State{Issue: closedIssue(), CascadeRoot: cascadeFixture()},
			wantCascade: []Key{{Repo: "o/r", Kind: KindIssue, Number: 2}},
		},
		{
			name:  "issue closed but open on re-fetch holds off without cascade",
			event: issueEvent(EventIssueClosed),
			state: State{Issue: openIssue(), CascadeRoot: cascadeFixture()},
		},
		{
			name:       "PR opened dispatches review",
			event:      prEvent(EventPROpened, "alice"),
			state:      State{PR: openPR("abc")},
			wantAction: ActionReview,
		},
		{
			name:       "PR opened not mergeable dispatches rebase",
			event:      prEvent(EventPROpened, "alice"),
			state:      State{PR: notMergeablePR("abc")},
			wantAction: ActionRebase,
		},
		{
			name:  "PR opened but closed on re-fetch holds off",
			event: prEvent(EventPROpened, "alice"),
			state: State{PR: closedPR("abc")},
		},
		{
			name:  "PR opened without re-fetched state holds off",
			event: prEvent(EventPROpened, "alice"),
		},
		{
			name:       "PR synced without prior review dispatches review",
			event:      prEvent(EventPRSynced, "alice"),
			state:      State{PR: openPR("abc")},
			wantAction: ActionReview,
		},
		{
			name:       "PR synced after a review dispatches re-review",
			event:      prEvent(EventPRSynced, "alice"),
			state:      State{PR: openPR("abc"), Reviews: []Review{review("COMMENT", "carol", testTime)}},
			wantAction: ActionReReview,
		},
		{
			name:       "PR synced not mergeable dispatches rebase",
			event:      prEvent(EventPRSynced, "alice"),
			state:      State{PR: notMergeablePR("abc")},
			wantAction: ActionRebase,
		},
		{
			name:  "PR synced pushed by the reviewing author holds off",
			event: prEvent(EventPRSynced, "carol"),
			state: State{PR: openPR("abc"), Reviews: []Review{review("APPROVED", "carol", testTime)}},
		},
		{
			name:       "PR synced pushed by the reviewing author of an older review dispatches re-review",
			event:      prEvent(EventPRSynced, "bob"),
			state:      State{PR: openPR("abc"), Reviews: []Review{review("APPROVED", "bob", testTime), review("COMMENT", "carol", testTime2)}},
			wantAction: ActionReReview,
		},
		{
			name:  "PR synced pushed by the latest reviewing author holds off",
			event: prEvent(EventPRSynced, "carol"),
			state: State{PR: openPR("abc"), Reviews: []Review{review("APPROVED", "bob", testTime), review("COMMENT", "carol", testTime2)}},
		},
		{
			name:       "PR synced pushed by the implementer after a review dispatches re-review",
			event:      prEvent(EventPRSynced, "alice"),
			state:      State{PR: openPR("abc"), Reviews: []Review{review(ReviewChangesRequested, "carol", testTime)}},
			wantAction: ActionReReview,
		},
		{
			name:       "PR review changes-requested dispatches fix-feedback",
			event:      prEvent(EventPRReviewed, "carol"),
			state:      State{PR: openPR("abc"), Reviews: []Review{review(ReviewChangesRequested, "carol", testTime)}},
			wantAction: ActionFixFeedback,
		},
		{
			name:       "PR review approved dispatches merge-or-wait",
			event:      prEvent(EventPRReviewed, "carol"),
			state:      State{PR: openPR("abc"), Reviews: []Review{review("APPROVED", "carol", testTime)}},
			wantAction: ActionMergeOrWait,
		},
		{
			name:  "PR review comment holds off",
			event: prEvent(EventPRReviewed, "carol"),
			state: State{PR: openPR("abc"), Reviews: []Review{review("COMMENT", "carol", testTime)}},
		},
		{
			name:  "PR review without reviews on re-fetch holds off",
			event: prEvent(EventPRReviewed, "carol"),
			state: State{PR: openPR("abc")},
		},
		{
			name:       "PR review latest review wins",
			event:      prEvent(EventPRReviewed, "carol"),
			state:      State{PR: openPR("abc"), Reviews: []Review{review(ReviewChangesRequested, "carol", testTime), review("APPROVED", "carol", testTime2)}},
			wantAction: ActionMergeOrWait,
		},
		{
			name:        "PR merged runs the unblock cascade",
			event:       prEvent(EventPRMerged, "alice"),
			state:       State{PR: mergedPR("abc"), CascadeRoot: cascadeFixture()},
			wantCascade: []Key{{Repo: "o/r", Kind: KindIssue, Number: 2}},
		},
		{
			name:  "PR merged but not merged on re-fetch holds off without cascade",
			event: prEvent(EventPRMerged, "alice"),
			state: State{PR: openPR("abc"), CascadeRoot: cascadeFixture()},
		},
		{
			name:  "PR closed without merging holds off",
			event: prEvent(EventPRClosed, "alice"),
			state: State{PR: closedPR("abc")},
		},
		{
			name:       "reconcile issue behaves like issue opened",
			event:      issueEvent(EventReconcile),
			state:      State{Issue: openIssue()},
			wantAction: ActionImplement,
		},
		{
			name:  "reconcile blocked issue holds off",
			event: issueEvent(EventReconcile),
			state: State{Issue: openIssue(), Blockers: []Issue{{Number: 3, State: "open", Repository: "o/r"}}},
		},
		{
			name:       "reconcile PR behaves like PR synced",
			event:      prEvent(EventReconcile, "maitred"),
			state:      State{PR: openPR("abc")},
			wantAction: ActionReview,
		},
		{
			name:       "reconcile PR standing at a comment review dispatches fix-feedback",
			event:      prEvent(EventReconcile, "maitred"),
			state:      State{PR: openPR("abc"), Reviews: []Review{review("COMMENT", "carol", testTime)}},
			wantAction: ActionFixFeedback,
		},
		{
			name:       "reconcile PR standing at a changes-requested review dispatches fix-feedback",
			event:      prEvent(EventReconcile, "maitred"),
			state:      State{PR: openPR("abc"), Reviews: []Review{review(ReviewChangesRequested, "carol", testTime)}},
			wantAction: ActionFixFeedback,
		},
		{
			name:       "reconcile PR standing at an approved review dispatches merge-or-wait",
			event:      prEvent(EventReconcile, "maitred"),
			state:      State{PR: openPR("abc"), Reviews: []Review{review("APPROVED", "carol", testTime)}},
			wantAction: ActionMergeOrWait,
		},
		{
			name:       "reconcile PR standing at a dismissed review dispatches re-review",
			event:      prEvent(EventReconcile, "maitred"),
			state:      State{PR: openPR("abc"), Reviews: []Review{review("DISMISSED", "carol", testTime)}},
			wantAction: ActionReReview,
		},
		{
			name:       "reconcile PR whose latest review predates the head dispatches re-review",
			event:      prEvent(EventReconcile, "maitred"),
			state:      State{PR: openPR("def"), Reviews: []Review{review(ReviewChangesRequested, "carol", testTime)}},
			wantAction: ActionReReview,
		},
		{
			name:      "implement already dispatched at the revision holds off",
			event:     issueEvent(EventIssueOpened),
			state:     State{Issue: openIssue()},
			watermark: &Watermark{Action: "implement", Revision: testRev},
		},
		{
			name:      "reassess already dispatched at the revision holds off",
			event:     issueEvent(EventIssueEdited),
			state:     State{Issue: openIssue()},
			watermark: &Watermark{Action: "reassess", Revision: testRev},
		},
		{
			name:      "review already dispatched at the revision holds off",
			event:     prEvent(EventPROpened, "alice"),
			state:     State{PR: openPR("abc")},
			watermark: &Watermark{Action: "review", Revision: "abc"},
		},
		{
			name:      "re-review already dispatched at the revision holds off",
			event:     prEvent(EventPRSynced, "alice"),
			state:     State{PR: openPR("abc"), Reviews: []Review{review("COMMENT", "carol", testTime)}},
			watermark: &Watermark{Action: "re-review", Revision: "abc"},
		},
		{
			name:      "fix-feedback already dispatched at the revision holds off",
			event:     prEvent(EventPRReviewed, "carol"),
			state:     State{PR: openPR("abc"), Reviews: []Review{review(ReviewChangesRequested, "carol", testTime)}},
			watermark: &Watermark{Action: "fix-feedback", Revision: "abc"},
		},
		{
			name:      "merge-or-wait already dispatched at the revision holds off",
			event:     prEvent(EventPRReviewed, "carol"),
			state:     State{PR: openPR("abc"), Reviews: []Review{review("APPROVED", "carol", testTime)}},
			watermark: &Watermark{Action: "merge-or-wait", Revision: "abc"},
		},
		{
			name:      "reconcile PR fix-feedback already dispatched at the revision holds off",
			event:     prEvent(EventReconcile, "maitred"),
			state:     State{PR: openPR("abc"), Reviews: []Review{review(ReviewChangesRequested, "carol", testTime)}},
			watermark: &Watermark{Action: "fix-feedback", Revision: "abc"},
		},
		{
			name:      "reconcile PR merge-or-wait already dispatched at the revision holds off",
			event:     prEvent(EventReconcile, "maitred"),
			state:     State{PR: openPR("abc"), Reviews: []Review{review("APPROVED", "carol", testTime)}},
			watermark: &Watermark{Action: "merge-or-wait", Revision: "abc"},
		},
		{
			name:      "reconcile PR standing at a comment review, fix-feedback already dispatched at the revision, holds off",
			event:     prEvent(EventReconcile, "maitred"),
			state:     State{PR: openPR("abc"), Reviews: []Review{review("COMMENT", "carol", testTime)}},
			watermark: &Watermark{Action: "fix-feedback", Revision: "abc"},
		},
		{
			name:      "rebase already dispatched at the revision holds off",
			event:     prEvent(EventPRSynced, "alice"),
			state:     State{PR: notMergeablePR("abc")},
			watermark: &Watermark{Action: "rebase", Revision: "abc"},
		},
		{
			name:       "a different revision dispatches",
			event:      issueEvent(EventIssueOpened),
			state:      State{Issue: openIssue()},
			watermark:  &Watermark{Action: "implement", Revision: testRev2},
			wantAction: ActionImplement,
		},
		{
			name:       "a different action dispatches",
			event:      issueEvent(EventIssueOpened),
			state:      State{Issue: openIssue()},
			watermark:  &Watermark{Action: "reassess", Revision: testRev},
			wantAction: ActionImplement,
		},
		{
			name:       "a review watermark does not block a re-review",
			event:      prEvent(EventPRSynced, "alice"),
			state:      State{PR: openPR("abc"), Reviews: []Review{review("COMMENT", "carol", testTime)}},
			watermark:  &Watermark{Action: "review", Revision: "abc"},
			wantAction: ActionReReview,
		},
		{
			name:  "an unknown event type holds off",
			event: Event{Type: EventType("bogus"), Repo: "o/r", Kind: KindIssue, Number: 7},
		},
		// Watermark exemptions: state is the watermark for the exempt
		// actions, revision notwithstanding
		// (§forgejo/reconciliation/watermark-exemptions).
		{
			name:       "reconcile open fully unblocked outcome tracker dispatches wrap-up",
			event:      issueEvent(EventReconcile),
			state:      State{Issue: trackerIssue()},
			wantAction: ActionWrapUp,
		},
		{
			name:       "reconcile re-fires wrap-up for a tracker already dispatched at an unchanged revision",
			event:      issueEvent(EventReconcile),
			state:      State{Issue: trackerIssue()},
			watermark:  &Watermark{Action: "wrap-up", Revision: testRev},
			wantAction: ActionWrapUp,
		},
		{
			name:  "reconcile outcome tracker with an open blocker holds off",
			event: issueEvent(EventReconcile),
			state: State{Issue: trackerIssue(), Blockers: []Issue{{Number: 3, State: "open", Repository: "o/r"}}},
		},
		{
			name:  "reconcile outcome tracker closed on re-fetch holds off",
			event: issueEvent(EventReconcile),
			state: State{Issue: func() *Issue { i := trackerIssue(); i.State = "closed"; return i }()},
		},
		{
			name:       "reconcile open ideation issue dispatches decompose",
			event:      issueEvent(EventReconcile),
			state:      State{Issue: ideationIssue()},
			wantAction: ActionDecompose,
		},
		{
			name:       "reconcile re-fires decompose for an ideation issue already dispatched at an unchanged revision",
			event:      issueEvent(EventReconcile),
			state:      State{Issue: ideationIssue()},
			watermark:  &Watermark{Action: "decompose", Revision: testRev},
			wantAction: ActionDecompose,
		},
		{
			name:  "reconcile ideation issue closed on re-fetch holds off",
			event: issueEvent(EventReconcile),
			state: State{Issue: func() *Issue { i := ideationIssue(); i.State = "closed"; return i }()},
		},
		{
			name:       "an ideation label takes precedence over an outcome label",
			event:      issueEvent(EventReconcile),
			state:      State{Issue: &Issue{Number: 7, State: "open", UpdatedAt: testTime, Repository: "o/r", Labels: []string{LabelOutcome, LabelIdeation}}},
			wantAction: ActionDecompose,
		},
		{
			name:      "reconcile unlabelled issue implement already dispatched at the revision holds off",
			event:     issueEvent(EventReconcile),
			state:     State{Issue: openIssue()},
			watermark: &Watermark{Action: "implement", Revision: testRev},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := Decide(tt.event, tt.state, tt.watermark)
			if tt.wantAction != "" {
				if d.HoldOff() {
					t.Fatalf("HoldOff() = true, want dispatch of %q (reason: %s)", tt.wantAction, d.Reason)
				}
				if d.Action != tt.wantAction {
					t.Errorf("Action = %q, want %q (reason: %s)", d.Action, tt.wantAction, d.Reason)
				}
			} else if !d.HoldOff() {
				t.Fatalf("HoldOff() = false with action %q, want hold-off (reason: %s)", d.Action, d.Reason)
			}
			if !reflect.DeepEqual(d.Cascade, tt.wantCascade) {
				t.Errorf("Cascade = %v, want %v", d.Cascade, tt.wantCascade)
			}
			if d.Reason == "" {
				t.Error("Reason is empty; every decision carries a reason")
			}
		})
	}
}

// TestDecide_ActivityBlockerHoldOffNamesBlockers pins the acceptance
// that activity on an issue with an open blocker holds off with a
// reason naming the open blockers — cross-repo included — and not the
// closed ones (§forgejo/decisions/transition-table).
func TestDecide_ActivityBlockerHoldOffNamesBlockers(t *testing.T) {
	state := State{
		Issue: openIssue(),
		Blockers: []Issue{
			{Number: 3, State: "open", Repository: "o/r"},
			{Number: 12, State: "open", Repository: "other/repo"},
			{Number: 5, State: "closed", Repository: "o/r"},
		},
	}
	for _, typ := range []EventType{EventIssueEdited, EventIssueLabelsChanged, EventIssueCommented} {
		d := Decide(issueEvent(typ), state, nil)
		if !d.HoldOff() {
			t.Fatalf("%s: dispatched %q, want hold-off", typ, d.Action)
		}
		for _, want := range []string{"o/r#3", "other/repo#12"} {
			if !strings.Contains(d.Reason, want) {
				t.Errorf("%s: reason %q does not name blocker %s", typ, d.Reason, want)
			}
		}
		if strings.Contains(d.Reason, "o/r#5") {
			t.Errorf("%s: reason %q names a closed blocker", typ, d.Reason)
		}
	}
}

// TestDecide_HoldOffReasons distinguishes the two hold-off families:
// revision-keyed actions hold off with "already dispatched at
// revision", watermark-exempt actions with the state fact that calls
// for no action (§forgejo/reconciliation/watermark-exemptions,
// §forgejo/observability/the-decision-log).
func TestDecide_HoldOffReasons(t *testing.T) {
	t.Run("a revision-keyed hold-off names the revision", func(t *testing.T) {
		d := Decide(issueEvent(EventReconcile), State{Issue: openIssue()}, &Watermark{Action: "implement", Revision: testRev})
		if !d.HoldOff() {
			t.Fatalf("expected hold-off, got dispatch of %q", d.Action)
		}
		if !strings.Contains(d.Reason, "already dispatched at revision") {
			t.Errorf("reason = %q, want it to name the already-dispatched revision", d.Reason)
		}
	})
	t.Run("a watermark-exempt hold-off names the state, not the revision", func(t *testing.T) {
		d := Decide(issueEvent(EventReconcile), State{Issue: trackerIssue(), Blockers: []Issue{{Number: 3, State: "open", Repository: "o/r"}}}, &Watermark{Action: "wrap-up", Revision: testRev})
		if !d.HoldOff() {
			t.Fatalf("expected hold-off, got dispatch of %q", d.Action)
		}
		if !strings.Contains(d.Reason, "blocked by 1 open issue") {
			t.Errorf("reason = %q, want it to name the open blockers", d.Reason)
		}
		if strings.Contains(d.Reason, "already dispatched") {
			t.Errorf("reason = %q, must not read as an already-dispatched hold-off", d.Reason)
		}
	})
}

// TestAlreadyDispatched pins the idempotency guard: revision-keyed
// actions are suppressed at a matching revision, watermark-exempt
// actions never are (§forgejo/decisions/loop-prevention,
// §forgejo/reconciliation/watermark-exemptions).
func TestAlreadyDispatched(t *testing.T) {
	w := &Watermark{Action: "wrap-up", Revision: testRev}
	if alreadyDispatched(&Watermark{Action: "implement", Revision: testRev2}, ActionImplement, testRev) {
		t.Error("a different revision must not suppress implement")
	}
	if !alreadyDispatched(&Watermark{Action: "implement", Revision: testRev}, ActionImplement, testRev) {
		t.Error("implement at a matching revision must be suppressed")
	}
	if alreadyDispatched(w, ActionWrapUp, testRev) {
		t.Error("a watermark-exempt action must never be suppressed by the watermark")
	}
}

// TestEvent_KeyOf checks the watermark key derived from an event
// (§forgejo/state/storage).
func TestEvent_KeyOf(t *testing.T) {
	tests := []struct {
		event Event
		want  Key
	}{
		{issueEvent(EventIssueOpened), Key{Repo: "o/r", Kind: KindIssue, Number: 7}},
		{prEvent(EventPRSynced, "alice"), Key{Repo: "o/r", Kind: KindPR, Number: 9}},
	}
	for _, tt := range tests {
		if got := tt.event.KeyOf(); got != tt.want {
			t.Errorf("KeyOf() = %v, want %v", got, tt.want)
		}
	}
}

func notMergeablePR(sha string) *PullRequest {
	pr := openPR(sha)
	pr.Mergeable = false
	return pr
}

func closedPR(sha string) *PullRequest {
	pr := openPR(sha)
	pr.State = "closed"
	return pr
}

func mergedPR(sha string) *PullRequest {
	pr := openPR(sha)
	pr.State = "closed"
	pr.Merged = true
	return pr
}

// cascadeFixture is a small blocker graph: the closed issue 1 blocks the
// open issue 2 (whose only blocker is issue 1) and the open issue 4
// (which is also blocked by the still-open issue 3).
func cascadeFixture() *IssueNode {
	root := IssueNode{
		Issue: Issue{Number: 1, State: "closed", Repository: "o/r"},
		Blocks: []IssueNode{
			{
				Issue: Issue{Number: 2, State: "open", Repository: "o/r"},
				Blockers: []IssueNode{
					{Issue: Issue{Number: 1, State: "closed", Repository: "o/r"}},
				},
			},
			{
				Issue: Issue{Number: 4, State: "open", Repository: "o/r"},
				Blockers: []IssueNode{
					{Issue: Issue{Number: 1, State: "closed", Repository: "o/r"}},
					{Issue: Issue{Number: 3, State: "open", Repository: "o/r"}},
				},
			},
		},
	}
	return &root
}

// TestUnblockCascade covers the cascade's graph walking: cross-repo
// blockers, transitivity, and termination without double-dispatching
// (§forgejo/decisions/unblock-cascade).
func TestUnblockCascade(t *testing.T) {
	node := func(repo string, number int, state string) IssueNode {
		return IssueNode{Issue: Issue{Number: number, State: state, Repository: repo}}
	}

	tests := []struct {
		name string
		root *IssueNode
		want []Key
	}{
		{
			name: "nil root yields no cascade",
		},
		{
			name: "closed issue with no blocks yields no cascade",
			root: &IssueNode{Issue: Issue{Number: 1, State: "closed", Repository: "o/r"}},
		},
		{
			name: "a closed issue with all blockers closed is unblocked",
			root: &IssueNode{
				Issue: Issue{Number: 1, State: "closed", Repository: "o/r"},
				Blocks: []IssueNode{
					{
						Issue:    Issue{Number: 2, State: "open", Repository: "o/r"},
						Blockers: []IssueNode{node("o/r", 1, "closed")},
					},
				},
			},
			want: []Key{{Repo: "o/r", Kind: KindIssue, Number: 2}},
		},
		{
			name: "cross-repo blockers are followed",
			root: &IssueNode{
				Issue: Issue{Number: 1, State: "closed", Repository: "o/r"},
				Blocks: []IssueNode{
					{
						Issue:    Issue{Number: 2, State: "open", Repository: "other/repo"},
						Blockers: []IssueNode{node("o/r", 1, "closed")},
					},
				},
			},
			want: []Key{{Repo: "other/repo", Kind: KindIssue, Number: 2}},
		},
		{
			name: "the cascade is transitive through closed issues",
			root: &IssueNode{
				Issue: Issue{Number: 1, State: "closed", Repository: "o/r"},
				Blocks: []IssueNode{
					{
						Issue: Issue{Number: 2, State: "closed", Repository: "o/r"},
						Blocks: []IssueNode{
							{
								Issue:    Issue{Number: 3, State: "open", Repository: "o/r"},
								Blockers: []IssueNode{node("o/r", 2, "closed")},
							},
						},
					},
				},
			},
			want: []Key{{Repo: "o/r", Kind: KindIssue, Number: 3}},
		},
		{
			name: "an issue with an open blocker is not unblocked",
			root: &IssueNode{
				Issue: Issue{Number: 1, State: "closed", Repository: "o/r"},
				Blocks: []IssueNode{
					{
						Issue: Issue{Number: 2, State: "open", Repository: "o/r"},
						Blockers: []IssueNode{
							node("o/r", 1, "closed"),
							node("o/r", 3, "open"),
						},
					},
				},
			},
		},
		{
			name: "a diamond unblocks the shared issue exactly once",
			root: &IssueNode{
				Issue: Issue{Number: 1, State: "closed", Repository: "o/r"},
				Blocks: []IssueNode{
					{
						Issue: Issue{Number: 2, State: "closed", Repository: "o/r"},
						Blocks: []IssueNode{
							{
								Issue: Issue{Number: 4, State: "open", Repository: "o/r"},
								Blockers: []IssueNode{
									node("o/r", 2, "closed"),
									node("o/r", 3, "closed"),
								},
							},
						},
					},
					{
						Issue: Issue{Number: 3, State: "closed", Repository: "o/r"},
						Blocks: []IssueNode{
							{
								Issue: Issue{Number: 4, State: "open", Repository: "o/r"},
								Blockers: []IssueNode{
									node("o/r", 2, "closed"),
									node("o/r", 3, "closed"),
								},
							},
						},
					},
				},
			},
			want: []Key{{Repo: "o/r", Kind: KindIssue, Number: 4}},
		},
		{
			// The walk is label-blind: what is finally dispatched for a
			// labelled member is the cascade dispatch's call
			// (§forgejo/decisions/unblock-cascade).
			name: "a reserved label does not blind the walk",
			root: &IssueNode{
				Issue: Issue{Number: 1, State: "closed", Repository: "o/r"},
				Blocks: []IssueNode{
					{
						Issue:    Issue{Number: 2, State: "open", Repository: "o/r", Labels: []string{LabelHumanTask}},
						Blockers: []IssueNode{node("o/r", 1, "closed")},
					},
				},
			},
			want: []Key{{Repo: "o/r", Kind: KindIssue, Number: 2}},
		},
		{
			name: "a cycle terminates",
			root: &IssueNode{
				Issue: Issue{Number: 1, State: "closed", Repository: "o/r"},
				Blocks: []IssueNode{
					{
						Issue: Issue{Number: 2, State: "closed", Repository: "o/r"},
						Blocks: []IssueNode{
							node("o/r", 1, "closed"),
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := unblockCascade(tt.root); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("unblockCascade() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestDecide_ReservedLabelHoldOffsNameTheLabel pins that the label
// insurance hold-offs name the reserved label that denied the action
// (§forgejo/decisions/transition-table).
func TestDecide_ReservedLabelHoldOffsNameTheLabel(t *testing.T) {
	tests := []struct {
		name       string
		event      Event
		state      State
		wantReason string
	}{
		{
			name:       "opened: implement denied by the label",
			event:      issueEvent(EventIssueOpened),
			state:      State{Issue: labelledIssue(LabelHumanTask)},
			wantReason: "never an implement candidate",
		},
		{
			name:       "activity: reassess denied by the label",
			event:      issueEvent(EventIssueCommented),
			state:      State{Issue: labelledIssue("ideation")},
			wantReason: "never a reassess candidate",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := Decide(tt.event, tt.state, nil)
			if !d.HoldOff() {
				t.Fatalf("decision = %+v, want a hold-off", d)
			}
			label := tt.state.Issue.Labels[0]
			if !strings.Contains(d.Reason, label) || !strings.Contains(d.Reason, tt.wantReason) {
				t.Errorf("hold-off reason = %q, want it to name %q and %q", d.Reason, label, tt.wantReason)
			}
		})
	}
}

// TestAdmissibleInRoles pins the action-to-role table: the work-item
// actions are admitted by the maitred-enabled role, the outcomes
// actions by the maitred-outcomes-repo role, and an unknown action is
// inadmissible anywhere (§forgejo/webhook/repository-roles).
func TestAdmissibleInRoles(t *testing.T) {
	workItem := roles{workItems: true}
	outcomes := roles{outcomes: true}
	both := roles{workItems: true, outcomes: true}
	tests := []struct {
		action Action
		r      roles
		want   bool
	}{
		{ActionImplement, workItem, true},
		{ActionReassess, workItem, true},
		{ActionReview, workItem, true},
		{ActionReReview, workItem, true},
		{ActionRebase, workItem, true},
		{ActionFixFeedback, workItem, true},
		{ActionMergeOrWait, workItem, true},
		{ActionImplement, outcomes, false},
		{ActionReassess, outcomes, false},
		{ActionReview, outcomes, false},
		// The outcomes actions are reserved vocabulary until the
		// outcomes chain dispatches them (#83, #84, #85).
		{Action("decompose"), outcomes, true},
		{Action("wrap-up"), outcomes, true},
		{Action("decompose"), workItem, false},
		{Action("wrap-up"), workItem, false},
		{ActionImplement, both, true},
		{Action("wrap-up"), both, true},
		{Action("frobnicate"), both, false},
		{Action("frobnicate"), roles{}, false},
		{ActionImplement, roles{}, false},
	}
	for _, tt := range tests {
		if got := admissibleInRoles(tt.action, tt.r); got != tt.want {
			t.Errorf("admissibleInRoles(%q, %+v) = %v, want %v", tt.action, tt.r, got, tt.want)
		}
	}
}

// TestHoldOffActionInadmissibleNamesTheRole pins the hold-off wording:
// a known action names the topic of the role that would admit it; an
// action no role admits states exactly that rather than pointing at a
// role admissibleInRoles has just denied — the fail-closed case
// (§forgejo/webhook/repository-roles).
func TestHoldOffActionInadmissibleNamesTheRole(t *testing.T) {
	tests := []struct {
		action Action
		want   string
	}{
		{ActionImplement, "action implement is inadmissible for repo o/r: requires the maitred-enabled role"},
		{ActionReview, "action review is inadmissible for repo o/r: requires the maitred-enabled role"},
		{Action("wrap-up"), "action wrap-up is inadmissible for repo o/r: requires the maitred-outcomes-repo role"},
		{Action("frobnicate"), "action frobnicate is admitted by no maitred role"},
	}
	for _, tt := range tests {
		if got := holdOffActionInadmissible(tt.action, "o/r").Reason; got != tt.want {
			t.Errorf("holdOffActionInadmissible(%q) reason = %q, want %q", tt.action, got, tt.want)
		}
	}
}
