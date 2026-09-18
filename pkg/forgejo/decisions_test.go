package forgejo

import (
	"reflect"
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
			name:  "issue edited but closed on re-fetch holds off",
			event: issueEvent(EventIssueEdited),
			state: State{Issue: closedIssue()},
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
			state:      State{PR: openPR("abc"), Reviews: []Review{review("CHANGES_REQUESTED", "carol", testTime)}},
			wantAction: ActionReReview,
		},
		{
			name:       "PR review changes-requested dispatches fix-feedback",
			event:      prEvent(EventPRReviewed, "carol"),
			state:      State{PR: openPR("abc"), Reviews: []Review{review("CHANGES_REQUESTED", "carol", testTime)}},
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
			state:      State{PR: openPR("abc"), Reviews: []Review{review("CHANGES_REQUESTED", "carol", testTime), review("APPROVED", "carol", testTime2)}},
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
			state:      State{PR: openPR("abc"), Reviews: []Review{review("COMMENT", "carol", testTime)}},
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
			state:     State{PR: openPR("abc"), Reviews: []Review{review("CHANGES_REQUESTED", "carol", testTime)}},
			watermark: &Watermark{Action: "fix-feedback", Revision: "abc"},
		},
		{
			name:      "merge-or-wait already dispatched at the revision holds off",
			event:     prEvent(EventPRReviewed, "carol"),
			state:     State{PR: openPR("abc"), Reviews: []Review{review("APPROVED", "carol", testTime)}},
			watermark: &Watermark{Action: "merge-or-wait", Revision: "abc"},
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
