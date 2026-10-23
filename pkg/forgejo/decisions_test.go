package forgejo

import (
	"fmt"
	"reflect"
	"strconv"
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

// authoredIssue is an open issue authored by the given login
// (§forgejo/decisions/loop-prevention).
func authoredIssue(author string) *Issue {
	issue := openIssue()
	issue.Author = author
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

// syncIssueFixture returns an open issue carrying the reserved sync
// label: a grouping issue whose members must land together
// (§forgejo/decisions/sync-set).
func syncIssueFixture(number int) *Issue {
	issue := openIssue()
	issue.Number = number
	issue.Labels = append(issue.Labels, LabelSync)
	return issue
}

// syncIssueWithLabel returns an open issue carrying the sync label
// alongside a second reserved kind label — a grouping issue the
// decomposer misauthorised, since one issue cannot claim two kinds
// (§forgejo/decisions/sync-set).
func syncIssueWithLabel(number int, other string) *Issue {
	issue := syncIssueFixture(number)
	issue.Labels = append(issue.Labels, other)
	return issue
}

// syncMemberFixture is one ready member of a landing set: an open
// issue carrying agentic-sync-merge whose connected PR is mergeable
// and approved at its head (§forgejo/decisions/sync-set).
func syncMemberFixture(number, prNumber int) SyncMember {
	m := SyncMember{
		Issue: Issue{
			Number:     number,
			State:      "open",
			Repository: "o/r",
			Labels:     []string{LabelAgenticSyncMerge},
			UpdatedAt:  testTime,
		},
	}
	m.PR = openPR("sha-" + strconv.Itoa(prNumber))
	m.PR.Number = prNumber
	m.Reviews = []Review{{Event: ReviewApproved, Author: "carol", CommitID: m.PR.HeadSHA, SubmittedAt: testTime}}
	return m
}

// closedSyncMemberFixture is a member whose work has landed: closed,
// which makes it ready whatever its labels (§forgejo/decisions/sync-set).
func closedSyncMemberFixture(number int) SyncMember {
	return SyncMember{Issue: Issue{
		Number:     number,
		State:      "closed",
		Repository: "o/r",
		Labels:     []string{LabelAgenticSyncMerge},
		UpdatedAt:  testTime,
	}}
}

// syncMemberConnected builds the connected issue of a PR under test
// when it is a member of a landing set: it carries agentic-sync-merge
// and points at the open sync issue it belongs to
// (§forgejo/decisions/sync-set).
func syncMemberConnected(number, syncNumber int) ConnectedIssue {
	c := connectedIssueOf(number, gateBlockerPR("o/r", 9, "open"))
	c.Issue.Labels = []string{LabelAgenticSyncMerge}
	set := syncIssueFixture(syncNumber)
	c.SyncSets = []Issue{*set}
	return c
}

// syncMemberWithoutSets is the connected issue of a PR under test
// whose membership is gone: it carries agentic-sync-merge but stands
// in no open set — the decomposer retired it, deleting rather than
// closing — so the stale label is no gate
// (§forgejo/decisions/sync-set).
func syncMemberWithoutSets(number int) ConnectedIssue {
	c := syncMemberConnected(number, 11)
	c.SyncSets = nil
	return c
}

// dualLabelledSyncMember is the connected issue of a PR under test
// carrying both merge opt-ins with no open set behind them: the
// authoring error the approval hold-off contains
// (§forgejo/decisions/sync-set).
func dualLabelledSyncMember(number int) ConnectedIssue {
	c := syncMemberWithoutSets(number)
	c.Issue.Labels = append(c.Issue.Labels, LabelAgenticAutoMerge)
	return c
}

// connectedIssue is the non-PR issue a PR under test blocks — the
// connected issue of the reject-pr test (§forgejo/decisions/actions).
func connectedIssue() *Issue {
	issue := openIssue()
	issue.Number = 4
	issue.Title = "connected issue"
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

// gateBlocker returns an issue blocker on a connected issue, for the
// merge gate (§forgejo/decisions/merge-gate).
func gateBlocker(repo string, number int, state string) Issue {
	return Issue{Number: number, State: state, Repository: repo}
}

// gateBlockerPR returns a pull-request blocker on a connected issue —
// a connected open PR, which is the PR's own entry when the numbers
// match (§forgejo/decisions/merge-gate).
func gateBlockerPR(repo string, number int, state string) Issue {
	b := gateBlocker(repo, number, state)
	b.IsPull = true
	return b
}

// connectedIssueOf builds one of the PR's connected issues, carrying
// its blockers, for the merge gate (§forgejo/decisions/merge-gate).
func connectedIssueOf(number int, blockers ...Issue) ConnectedIssue {
	return ConnectedIssue{
		Issue:    Issue{Number: number, State: "open", Repository: "o/r"},
		Blockers: blockers,
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
		ActionImplement:      "implement",
		ActionReassess:       "reassess",
		ActionReview:         "review",
		ActionReReview:       "re-review",
		ActionFixFeedback:    "fix-feedback",
		ActionMergeOrWait:    "merge-or-wait",
		ActionRebase:         "rebase",
		ActionDecompose:      "decompose",
		ActionWrapUp:         "wrap-up",
		ActionRejectPR:       "reject-pr",
		ActionTeardownForest: "teardown-forest",
		ActionSync:           "sync",
	}
	for action, name := range want {
		if string(action) != name {
			t.Errorf("action = %q, want %q", action, name)
		}
	}
	// The map and AllActions are the same enum seen two ways: an action
	// added to one and not the other is a name waiting to be wrong
	// (§forgejo/decisions/actions).
	if len(want) != len(AllActions) {
		t.Fatalf("the name map holds %d actions, AllActions holds %d", len(want), len(AllActions))
	}
	for _, a := range AllActions {
		if _, ok := want[a]; !ok {
			t.Errorf("AllActions holds %q, which the name map does not name", a)
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
		wantCascade []CascadeDispatch
		wantHolds   []CascadeHoldOff
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
			// The ideation branch is evaluated before the connected-PR
			// check: a root is decomposed whatever else hangs off it.
			name:       "an open ideation root with a connected open PR dispatches decompose",
			event:      issueEvent(EventIssueOpened),
			state:      State{Issue: ideationIssue(), ConnectedPRs: []PullRequest{*openPR("abc")}},
			wantAction: ActionDecompose,
		},
		{
			name:  "issue opened authored by the decomposer holds off",
			event: issueEvent(EventIssueOpened),
			state: State{Issue: authoredIssue("decomposer"), DecomposerIdentity: "decomposer"},
		},
		{
			name:       "issue opened authored by anyone else dispatches implement",
			event:      issueEvent(EventIssueOpened),
			state:      State{Issue: authoredIssue("alice"), DecomposerIdentity: "decomposer"},
			wantAction: ActionImplement,
		},
		{
			name:       "issue opened authored by the decomposer with the identity unset dispatches implement",
			event:      issueEvent(EventIssueOpened),
			state:      State{Issue: authoredIssue("decomposer")},
			wantAction: ActionImplement,
		},
		{
			name:       "issue opened authored by a differently cased login dispatches implement",
			event:      issueEvent(EventIssueOpened),
			state:      State{Issue: authoredIssue("Decomposer"), DecomposerIdentity: "decomposer"},
			wantAction: ActionImplement,
		},
		{
			name:  "issue opened authored by the decomposer with closed blockers holds off",
			event: issueEvent(EventIssueOpened),
			state: State{
				Issue:              authoredIssue("decomposer"),
				Blockers:           []Issue{{Number: 3, State: "closed", Repository: "o/r"}},
				DecomposerIdentity: "decomposer",
			},
		},
		{
			name:  "reconciled issue authored by the decomposer holds off",
			event: issueEvent(EventReconcile),
			state: State{Issue: authoredIssue("decomposer"), DecomposerIdentity: "decomposer"},
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
			name:       "issue edited carrying ideation dispatches decompose",
			event:      issueEvent(EventIssueEdited),
			state:      State{Issue: labelledIssue("ideation")},
			wantAction: ActionDecompose,
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
			wantCascade: []CascadeDispatch{{Key: Key{Repo: "o/r", Kind: KindIssue, Number: 2}, Action: ActionImplement}},
		},
		{
			name:        "issue closed newly unblocks an outcome tracker: the cascade emits wrap-up, never implement",
			event:       issueEvent(EventIssueClosed),
			state:       State{Issue: closedIssue(), CascadeRoot: trackerCascadeFixture(false)},
			wantCascade: []CascadeDispatch{{Key: Key{Repo: "o/r", Kind: KindIssue, Number: 2}, Action: ActionWrapUp}},
		},
		{
			name:  "issue closed but open on re-fetch holds off without cascade",
			event: issueEvent(EventIssueClosed),
			state: State{Issue: openIssue(), CascadeRoot: cascadeFixture()},
		},
		{
			name:      "issue closed leaves an outcome tracker blocked: the cascade records a hold-off naming the blockers",
			event:     issueEvent(EventIssueClosed),
			state:     State{Issue: closedIssue(), CascadeRoot: trackerCascadeFixture(true)},
			wantHolds: []CascadeHoldOff{{Key: Key{Repo: "o/r", Kind: KindIssue, Number: 2}, Issue: &Issue{Number: 2, State: "open", Repository: "o/r", Labels: []string{LabelOutcome}}, Reason: "issue 2 is an open outcome tracker still blocked by o/r#3; wrap-up held off"}},
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
			name:  "PR opened whose connected issue is blocked by an open ideation issue dispatches reject-pr",
			event: prEvent(EventPROpened, "alice"),
			state: State{
				PR: openPR("abc"),
				ConnectedIssues: []ConnectedIssue{{
					Issue:    *connectedIssue(),
					Blockers: []Issue{{Number: 1, State: "open", Repository: "o/r", Labels: []string{LabelIdeation}}},
				}},
			},
			wantAction: ActionRejectPR,
		},
		{
			// The test applies to every connected issue: a later
			// connected issue blocked by an open ideation issue rejects
			// whatever order the API returned the relationships in
			// (§forgejo/decisions/transition-table).
			name:  "PR opened whose second connected issue is blocked by an open ideation issue dispatches reject-pr",
			event: prEvent(EventPROpened, "alice"),
			state: State{
				PR: openPR("abc"),
				ConnectedIssues: []ConnectedIssue{
					{
						Issue:    *connectedIssue(),
						Blockers: []Issue{{Number: 1, State: "open", Repository: "o/r"}},
					},
					{
						Issue:    Issue{Number: 5, Title: "second connected issue", State: "open", Repository: "o/r"},
						Blockers: []Issue{{Number: 2, State: "open", Repository: "o/r", Labels: []string{LabelIdeation}}},
					},
				},
			},
			wantAction: ActionRejectPR,
		},
		{
			name:  "PR opened whose two connected issues have no open ideation blocker dispatches review",
			event: prEvent(EventPROpened, "alice"),
			state: State{
				PR: openPR("abc"),
				ConnectedIssues: []ConnectedIssue{
					{
						Issue:    *connectedIssue(),
						Blockers: []Issue{{Number: 1, State: "closed", Repository: "o/r", Labels: []string{LabelIdeation}}},
					},
					{
						Issue:    Issue{Number: 5, Title: "second connected issue", State: "open", Repository: "o/r"},
						Blockers: []Issue{{Number: 2, State: "open", Repository: "o/r"}},
					},
				},
			},
			wantAction: ActionReview,
		},
		{
			// Ordering: reject-pr is decided before review and rebase
			// (§forgejo/decisions/transition-table).
			name:  "PR opened not mergeable but blocked by an open ideation issue dispatches reject-pr",
			event: prEvent(EventPROpened, "alice"),
			state: State{
				PR: notMergeablePR("abc"),
				ConnectedIssues: []ConnectedIssue{{
					Issue:    *connectedIssue(),
					Blockers: []Issue{{Number: 1, State: "open", Repository: "o/r", Labels: []string{LabelIdeation}}},
				}},
			},
			wantAction: ActionRejectPR,
		},
		{
			// The connected issue's own state is not consulted: the gate
			// has not opened either way (§forgejo/decisions/transition-table).
			name:  "PR opened against a closed connected issue blocked by an open ideation issue dispatches reject-pr",
			event: prEvent(EventPROpened, "alice"),
			state: State{
				PR: openPR("abc"),
				ConnectedIssues: []ConnectedIssue{{
					Issue: func() Issue {
						issue := *connectedIssue()
						issue.State = "closed"
						return issue
					}(),
					Blockers: []Issue{{Number: 1, State: "open", Repository: "o/r", Labels: []string{LabelIdeation}}},
				}},
			},
			wantAction: ActionRejectPR,
		},
		{
			// The reserved name is matched exactly and case-sensitively
			// (§forgejo/decisions/transition-table).
			name:  "PR opened whose connected issue is blocked by an open issue labelled Ideation dispatches review",
			event: prEvent(EventPROpened, "alice"),
			state: State{
				PR: openPR("abc"),
				ConnectedIssues: []ConnectedIssue{{
					Issue:    *connectedIssue(),
					Blockers: []Issue{{Number: 1, State: "open", Repository: "o/r", Labels: []string{"Ideation"}}},
				}},
			},
			wantAction: ActionReview,
		},
		{
			name:  "PR opened whose connected issue is blocked only by a closed ideation issue dispatches review",
			event: prEvent(EventPROpened, "alice"),
			state: State{
				PR: openPR("abc"),
				ConnectedIssues: []ConnectedIssue{{
					Issue:    *connectedIssue(),
					Blockers: []Issue{{Number: 1, State: "closed", Repository: "o/r", Labels: []string{LabelIdeation}}},
				}},
			},
			wantAction: ActionReview,
		},
		{
			name:  "PR opened whose connected issue has an open blocker without the ideation label dispatches review",
			event: prEvent(EventPROpened, "alice"),
			state: State{
				PR: openPR("abc"),
				ConnectedIssues: []ConnectedIssue{{
					Issue:    *connectedIssue(),
					Blockers: []Issue{{Number: 1, State: "open", Repository: "o/r"}},
				}},
			},
			wantAction: ActionReview,
		},
		{
			name:  "PR opened with no connected issue dispatches review",
			event: prEvent(EventPROpened, "alice"),
			state: State{
				PR: openPR("abc"),
			},
			wantAction: ActionReview,
		},
		{
			name:  "reject-pr already dispatched at the revision holds off",
			event: prEvent(EventPROpened, "alice"),
			state: State{
				PR: openPR("abc"),
				ConnectedIssues: []ConnectedIssue{{
					Issue:    *connectedIssue(),
					Blockers: []Issue{{Number: 1, State: "open", Repository: "o/r", Labels: []string{LabelIdeation}}},
				}},
			},
			watermark: &Watermark{Action: string(ActionRejectPR), Revision: "abc"},
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
			// The trigger-review label is the explicit request for an
			// agentic review (§forgejo/decisions/label-triggered-review).
			name:       "PR synced carrying trigger-review without a review dispatches review",
			event:      prEvent(EventPRSynced, "alice"),
			state:      State{PR: labelledPR("abc", LabelTriggerReview)},
			wantAction: ActionReview,
		},
		{
			name:       "PR synced carrying trigger-review with a review dispatches re-review",
			event:      prEvent(EventPRSynced, "alice"),
			state:      State{PR: labelledPR("abc", LabelTriggerReview), Reviews: []Review{review("APPROVED", "carol", testTime)}},
			wantAction: ActionReReview,
		},
		{
			// The label is an explicit request, not the author's own
			// push: the reviewing-author sender guard does not
			// suppress a label-requested review
			// (§forgejo/decisions/label-triggered-review).
			name:       "PR synced carrying trigger-review pushed by the reviewing author dispatches re-review",
			event:      prEvent(EventPRSynced, "carol"),
			state:      State{PR: labelledPR("abc", LabelTriggerReview), Reviews: []Review{review("APPROVED", "carol", testTime)}},
			wantAction: ActionReReview,
		},
		{
			name:       "PR synced carrying a differently cased trigger-review label dispatches re-review",
			event:      prEvent(EventPRSynced, "carol"),
			state:      State{PR: labelledPR("abc", "Trigger-Review"), Reviews: []Review{review("APPROVED", "carol", testTime)}},
			wantAction: ActionReReview,
		},
		{
			// Keyed to the label's presence at the current revision:
			// the label merely present does not re-fire the review
			// (§forgejo/decisions/label-triggered-review,
			// §forgejo/decisions/loop-prevention).
			name:      "PR synced carrying trigger-review already dispatched at the revision holds off",
			event:     prEvent(EventPRSynced, "alice"),
			state:     State{PR: labelledPR("abc", LabelTriggerReview), Reviews: []Review{review("APPROVED", "carol", testTime)}},
			watermark: &Watermark{Action: string(ActionReReview), Revision: "abc"},
		},
		{
			// The label requests a review of mergeable work; a
			// conflicting PR is rebased first
			// (§forgejo/decisions/label-triggered-review).
			name:       "PR synced carrying trigger-review but not mergeable dispatches rebase",
			event:      prEvent(EventPRSynced, "alice"),
			state:      State{PR: labelledPRNotMergeable("abc", LabelTriggerReview)},
			wantAction: ActionRebase,
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
			// The reject-pr test precedes the review-following
			// decisions: a gate that closed after the PR opened must
			// not be walked through by an approving review
			// (§forgejo/decisions/transition-table).
			name:  "PR review approved whose connected issue is blocked by an open ideation issue dispatches reject-pr",
			event: prEvent(EventPRReviewed, "carol"),
			state: State{
				PR:      openPR("abc"),
				Reviews: []Review{review("APPROVED", "carol", testTime)},
				ConnectedIssues: []ConnectedIssue{{
					Issue:    *connectedIssue(),
					Blockers: []Issue{{Number: 1, State: "open", Repository: "o/r", Labels: []string{LabelIdeation}}},
				}},
			},
			wantAction: ActionRejectPR,
		},
		{
			name:  "PR review changes-requested whose connected issue is blocked by an open ideation issue dispatches reject-pr",
			event: prEvent(EventPRReviewed, "carol"),
			state: State{
				PR:      openPR("abc"),
				Reviews: []Review{review(ReviewChangesRequested, "carol", testTime)},
				ConnectedIssues: []ConnectedIssue{{
					Issue:    *connectedIssue(),
					Blockers: []Issue{{Number: 1, State: "open", Repository: "o/r", Labels: []string{LabelIdeation}}},
				}},
			},
			wantAction: ActionRejectPR,
		},
		{
			name:  "PR review approved whose connected issue is blocked only by a closed ideation issue dispatches merge-or-wait",
			event: prEvent(EventPRReviewed, "carol"),
			state: State{
				PR:      openPR("abc"),
				Reviews: []Review{review("APPROVED", "carol", testTime)},
				ConnectedIssues: []ConnectedIssue{{
					Issue:    *connectedIssue(),
					Blockers: []Issue{{Number: 1, State: "closed", Repository: "o/r", Labels: []string{LabelIdeation}}},
				}},
			},
			wantAction: ActionMergeOrWait,
		},
		{
			name:  "reject-pr already dispatched at the revision holds off on the review path",
			event: prEvent(EventPRReviewed, "carol"),
			state: State{
				PR:      openPR("abc"),
				Reviews: []Review{review("APPROVED", "carol", testTime)},
				ConnectedIssues: []ConnectedIssue{{
					Issue:    *connectedIssue(),
					Blockers: []Issue{{Number: 1, State: "open", Repository: "o/r", Labels: []string{LabelIdeation}}},
				}},
			},
			watermark: &Watermark{Action: string(ActionRejectPR), Revision: "abc"},
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
		// The merge gate: merge-or-wait only when no connected
		// issue has an open blocker (§forgejo/decisions/merge-gate).
		{
			name:  "PR review approved with a connected issue blocked by an open issue holds off",
			event: prEvent(EventPRReviewed, "carol"),
			state: State{
				PR: openPR("abc"), Reviews: []Review{review("APPROVED", "carol", testTime)},
				ConnectedIssues: []ConnectedIssue{connectedIssueOf(7, gateBlocker("o/r", 3, "closed"), gateBlocker("o/r", 4, "open"))},
			},
		},
		{
			name:  "PR review approved with a connected issue's blockers all closed dispatches merge-or-wait",
			event: prEvent(EventPRReviewed, "carol"),
			state: State{
				PR: openPR("abc"), Reviews: []Review{review("APPROVED", "carol", testTime)},
				ConnectedIssues: []ConnectedIssue{connectedIssueOf(7, gateBlocker("o/r", 3, "closed"))},
			},
			wantAction: ActionMergeOrWait,
		},
		{
			name:  "PR review approved where the only open blocker is the PR itself dispatches merge-or-wait",
			event: prEvent(EventPRReviewed, "carol"),
			state: State{
				PR: openPR("abc"), Reviews: []Review{review("APPROVED", "carol", testTime)},
				ConnectedIssues: []ConnectedIssue{connectedIssueOf(7, gateBlockerPR("o/r", 9, "open"))},
			},
			wantAction: ActionMergeOrWait,
		},
		{
			name:  "PR review approved with the second connected issue blocked holds off",
			event: prEvent(EventPRReviewed, "carol"),
			state: State{
				PR: openPR("abc"), Reviews: []Review{review("APPROVED", "carol", testTime)},
				ConnectedIssues: []ConnectedIssue{
					connectedIssueOf(7, gateBlocker("o/r", 3, "closed")),
					connectedIssueOf(8, gateBlocker("other/repo", 5, "open")),
				},
			},
		},
		{
			name:  "PR review changes-requested with a connected issue blocked still dispatches fix-feedback",
			event: prEvent(EventPRReviewed, "carol"),
			state: State{
				PR: openPR("abc"), Reviews: []Review{review(ReviewChangesRequested, "carol", testTime)},
				ConnectedIssues: []ConnectedIssue{connectedIssueOf(7, gateBlocker("o/r", 3, "open"))},
			},
			wantAction: ActionFixFeedback,
		},
		{
			name:  "PR synced with a connected issue blocked dispatches re-review — the push path decides no merge",
			event: prEvent(EventPRSynced, "alice"),
			state: State{
				PR: openPR("abc"), Reviews: []Review{review("COMMENT", "carol", testTime)},
				ConnectedIssues: []ConnectedIssue{connectedIssueOf(7, gateBlocker("o/r", 3, "open"))},
			},
			wantAction: ActionReReview,
		},
		{
			name:  "PR opened with a connected issue blocked dispatches review — the open path decides no merge",
			event: prEvent(EventPROpened, "alice"),
			state: State{
				PR:              openPR("abc"),
				ConnectedIssues: []ConnectedIssue{connectedIssueOf(7, gateBlocker("o/r", 3, "open"))},
			},
			wantAction: ActionReview,
		},
		// The sync set: a member PR's approval never merges on its
		// own — landing is the set's decision — and the approval
		// emits the set it belongs to for evaluation
		// (§forgejo/decisions/sync-set).
		{
			name:  "PR review approved on a sync-set member PR holds off and emits its sync set",
			event: prEvent(EventPRReviewed, "carol"),
			state: State{
				PR:              openPR("abc"),
				Reviews:         []Review{review("APPROVED", "carol", testTime)},
				ConnectedIssues: []ConnectedIssue{syncMemberConnected(7, 11)},
			},
			wantCascade: []CascadeDispatch{{Key: Key{Repo: "o/r", Kind: KindIssue, Number: 11}, Action: ActionSync}},
		},
		{
			name:  "reconcile PR approved at head on a sync-set member holds off and emits its sync set",
			event: prEvent(EventReconcile, "maitred"),
			state: State{
				PR:              openPR("abc"),
				Reviews:         []Review{review("APPROVED", "carol", testTime)},
				ConnectedIssues: []ConnectedIssue{syncMemberConnected(7, 11)},
			},
			wantCascade: []CascadeDispatch{{Key: Key{Repo: "o/r", Kind: KindIssue, Number: 11}, Action: ActionSync}},
		},
		{
			name:  "PR review changes-requested on a sync-set member still dispatches fix-feedback",
			event: prEvent(EventPRReviewed, "carol"),
			state: State{
				PR:              openPR("abc"),
				Reviews:         []Review{review(ReviewChangesRequested, "carol", testTime)},
				ConnectedIssues: []ConnectedIssue{syncMemberConnected(7, 11)},
			},
			wantAction: ActionFixFeedback,
		},
		{
			name:  "PR review approved on a member of no open sync set dispatches merge-or-wait — a retired set is no gate",
			event: prEvent(EventPRReviewed, "carol"),
			state: State{
				PR:              openPR("abc"),
				Reviews:         []Review{review("APPROVED", "carol", testTime)},
				ConnectedIssues: []ConnectedIssue{syncMemberWithoutSets(7)},
			},
			wantAction: ActionMergeOrWait,
		},
		{
			name:  "reconcile PR approved at head on a member of no open sync set dispatches merge-or-wait",
			event: prEvent(EventReconcile, "maitred"),
			state: State{
				PR:              openPR("abc"),
				Reviews:         []Review{review("APPROVED", "carol", testTime)},
				ConnectedIssues: []ConnectedIssue{syncMemberWithoutSets(7)},
			},
			wantAction: ActionMergeOrWait,
		},
		{
			name:  "PR review approved on a member carrying both merge opt-ins holds off with no open set — the error state is contained",
			event: prEvent(EventPRReviewed, "carol"),
			state: State{
				PR:              openPR("abc"),
				Reviews:         []Review{review("APPROVED", "carol", testTime)},
				ConnectedIssues: []ConnectedIssue{dualLabelledSyncMember(7)},
			},
		},
		{
			name:  "reconcile issue carrying the sync label with no members holds off — an empty set has nothing to land",
			event: issueEvent(EventReconcile),
			state: State{Issue: syncIssueFixture(7)},
		},
		{
			// The sweep is the set's only backstop, so the sync row
			// is tested first: a second kind claim is authoring the
			// engine names rather than resolves by label order
			// (§forgejo/decisions/sync-set).
			name:  "reconcile issue carrying sync alongside outcome holds off naming both kind labels",
			event: issueEvent(EventReconcile),
			state: State{
				Issue:       syncIssueWithLabel(7, LabelOutcome),
				SyncMembers: []SyncMember{syncMemberFixture(4, 40), closedSyncMemberFixture(5)},
				Blockers:    []Issue{{Number: 4, State: "closed", Repository: "o/r"}, {Number: 5, State: "closed", Repository: "o/r"}},
			},
		},
		{
			name:  "reconcile issue carrying sync alongside ideation holds off naming both kind labels",
			event: issueEvent(EventReconcile),
			state: State{
				Issue:    syncIssueWithLabel(7, LabelIdeation),
				Blockers: []Issue{{Number: 4, State: "closed", Repository: "o/r"}},
			},
		},
		{
			name:       "reconcile sync issue with every member closed or approved dispatches sync",
			event:      issueEvent(EventReconcile),
			state:      State{Issue: syncIssueFixture(7), SyncMembers: []SyncMember{syncMemberFixture(4, 40), closedSyncMemberFixture(5)}},
			wantAction: ActionSync,
		},
		{
			name:  "reconcile sync issue with an unlabelled open member holds off — the member awaits a human",
			event: issueEvent(EventReconcile),
			state: State{Issue: syncIssueFixture(7), SyncMembers: []SyncMember{
				syncMemberFixture(4, 40),
				{Issue: Issue{Number: 5, State: "open", Repository: "o/r", UpdatedAt: testTime}},
			}},
		},
		{
			name:  "reconcile sync issue with a member whose PR is not approved at head holds off",
			event: issueEvent(EventReconcile),
			state: State{Issue: syncIssueFixture(7), SyncMembers: []SyncMember{
				syncMemberFixture(4, 40),
				func() SyncMember {
					m := syncMemberFixture(5, 50)
					m.Reviews = []Review{{Event: ReviewChangesRequested, Author: "carol", CommitID: m.PR.HeadSHA, SubmittedAt: testTime}}
					return m
				}(),
			}},
		},
		{
			name:  "reconcile sync issue with a member whose PR has merge conflicts holds off",
			event: issueEvent(EventReconcile),
			state: State{Issue: syncIssueFixture(7), SyncMembers: []SyncMember{
				func() SyncMember {
					m := syncMemberFixture(4, 40)
					m.PR.Mergeable = false
					return m
				}(),
				closedSyncMemberFixture(5),
			}},
		},
		{
			name:  "reconcile sync issue already dispatched at the set revision holds off",
			event: issueEvent(EventReconcile),
			state: State{Issue: syncIssueFixture(7), SyncMembers: []SyncMember{syncMemberFixture(4, 40), closedSyncMemberFixture(5)}},
			watermark: &Watermark{Action: string(ActionSync), Revision: syncSetRevision([]SyncMember{
				closedSyncMemberFixture(5), syncMemberFixture(4, 40),
			})},
		},
		{
			name:       "reconcile sync issue whose set state moved re-dispatches over the watermark",
			event:      issueEvent(EventReconcile),
			state:      State{Issue: syncIssueFixture(7), SyncMembers: []SyncMember{syncMemberFixture(4, 40), syncMemberFixture(5, 50)}},
			watermark:  &Watermark{Action: string(ActionSync), Revision: syncSetRevision([]SyncMember{syncMemberFixture(4, 40), closedSyncMemberFixture(5)})},
			wantAction: ActionSync,
		},
		{
			name:  "issue opened carrying the sync label holds off — never an implement candidate",
			event: issueEvent(EventIssueOpened),
			state: State{Issue: syncIssueFixture(7)},
		},
		{
			name:        "PR merged runs the unblock cascade",
			event:       prEvent(EventPRMerged, "alice"),
			state:       State{PR: mergedPR("abc"), CascadeRoot: cascadeFixture()},
			wantCascade: []CascadeDispatch{{Key: Key{Repo: "o/r", Kind: KindIssue, Number: 2}, Action: ActionImplement}},
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
			name:  "reconcile PR standing at an approved review with a connected issue blocked holds off",
			event: prEvent(EventReconcile, "maitred"),
			state: State{
				PR: openPR("abc"), Reviews: []Review{review("APPROVED", "carol", testTime)},
				ConnectedIssues: []ConnectedIssue{connectedIssueOf(7, gateBlocker("o/r", 3, "open"))},
			},
		},
		{
			name:  "reconcile PR standing at a changes-requested review with a connected issue blocked still dispatches fix-feedback",
			event: prEvent(EventReconcile, "maitred"),
			state: State{
				PR: openPR("abc"), Reviews: []Review{review(ReviewChangesRequested, "carol", testTime)},
				ConnectedIssues: []ConnectedIssue{connectedIssueOf(7, gateBlocker("o/r", 3, "open"))},
			},
			wantAction: ActionFixFeedback,
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
			// The sweep applies the reject-pr test ahead of the whole
			// PR path, not only its review-following branches — the
			// gate is not left to whether the PR happens to stand at a
			// review (§forgejo/decisions/transition-table).
			name:  "reconcile PR whose connected issue is blocked by an open ideation issue dispatches reject-pr",
			event: prEvent(EventReconcile, "maitred"),
			state: State{
				PR: openPR("abc"),
				ConnectedIssues: []ConnectedIssue{{
					Issue:    *connectedIssue(),
					Blockers: []Issue{{Number: 1, State: "open", Repository: "o/r", Labels: []string{LabelIdeation}}},
				}},
			},
			wantAction: ActionRejectPR,
		},
		{
			// The label overrides the trigger, not the review's
			// content: a label-requested review that has landed and
			// approves at head decides on under the merge policy
			// (§forgejo/decisions/label-triggered-review).
			name:       "reconcile PR carrying trigger-review standing at an approved review dispatches merge-or-wait",
			event:      prEvent(EventReconcile, "alice"),
			state:      State{PR: labelledPR("abc", LabelTriggerReview), Reviews: []Review{review("APPROVED", "carol", testTime)}},
			wantAction: ActionMergeOrWait,
		},
		{
			name:  "reconcile PR standing at an approved review whose connected issue is blocked by an open ideation issue dispatches reject-pr",
			event: prEvent(EventReconcile, "maitred"),
			state: State{
				PR:      openPR("abc"),
				Reviews: []Review{review("APPROVED", "carol", testTime)},
				ConnectedIssues: []ConnectedIssue{{
					Issue:    *connectedIssue(),
					Blockers: []Issue{{Number: 1, State: "open", Repository: "o/r", Labels: []string{LabelIdeation}}},
				}},
			},
			wantAction: ActionRejectPR,
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
			name:       "PR commented decides as reconcile: approved at head dispatches merge-or-wait",
			event:      prEvent(EventPRCommented, "alice"),
			state:      State{PR: openPR("abc"), Reviews: []Review{review("APPROVED", "carol", testTime)}},
			wantAction: ActionMergeOrWait,
		},
		{
			name:       "PR commented decides as reconcile: changes-requested at head dispatches fix-feedback",
			event:      prEvent(EventPRCommented, "alice"),
			state:      State{PR: openPR("abc"), Reviews: []Review{review(ReviewChangesRequested, "carol", testTime)}},
			wantAction: ActionFixFeedback,
		},
		{
			name:       "PR commented decides as reconcile: comment review at head dispatches fix-feedback",
			event:      prEvent(EventPRCommented, "alice"),
			state:      State{PR: openPR("abc"), Reviews: []Review{review("COMMENT", "carol", testTime)}},
			wantAction: ActionFixFeedback,
		},
		{
			name:       "PR commented decides as reconcile: no review dispatches review",
			event:      prEvent(EventPRCommented, "alice"),
			state:      State{PR: openPR("abc")},
			wantAction: ActionReview,
		},
		{
			name:      "PR commented, merge-or-wait already dispatched at the revision, holds off",
			event:     prEvent(EventPRCommented, "alice"),
			state:     State{PR: openPR("abc"), Reviews: []Review{review("APPROVED", "carol", testTime)}},
			watermark: &Watermark{Action: "merge-or-wait", Revision: "abc"},
		},
		{
			name:  "PR commented but closed on re-fetch holds off",
			event: prEvent(EventPRCommented, "alice"),
			state: State{PR: closedPR("abc")},
		},
		{
			name:  "commented event on an issue kind holds off",
			event: issueEvent(EventPRCommented),
			state: State{Issue: openIssue()},
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
			name:  "reconcile outcome tracker with an open blocker and no ideation blocker holds off",
			event: issueEvent(EventReconcile),
			state: State{Issue: trackerIssue(), Blockers: []Issue{{Number: 3, State: "open", Repository: "o/r"}}},
		},
		// A commissioned forest stays under curation: closing the ideation issue
		// removes the root from the sweep's enumeration, so the tracker's own
		// row carries the curation until the forest is finished
		// (§forgejo/reconciliation/watermark-exemptions).
		{
			name:  "reconcile commissioned unfinished tracker dispatches decompose",
			event: issueEvent(EventReconcile),
			state: State{Issue: trackerIssue(), Blockers: []Issue{
				{Number: 3, State: "closed", Repository: "o/r", Labels: []string{LabelIdeation}},
				{Number: 8, State: "open", Repository: "o/r"},
			}},
			wantAction: ActionDecompose,
		},
		{
			name:  "reconcile re-fires decompose for a commissioned tracker already dispatched at an unchanged revision",
			event: issueEvent(EventReconcile),
			state: State{Issue: trackerIssue(), Blockers: []Issue{
				{Number: 3, State: "closed", Repository: "o/r", Labels: []string{LabelIdeation}},
				{Number: 8, State: "open", Repository: "o/r"},
			}},
			watermark:  &Watermark{Action: "decompose", Revision: testRev},
			wantAction: ActionDecompose,
		},
		{
			name:  "reconcile not-yet-commissioned tracker holds off naming its open root",
			event: issueEvent(EventReconcile),
			state: State{Issue: trackerIssue(), Blockers: []Issue{
				{Number: 3, State: "open", Repository: "o/r", Labels: []string{LabelIdeation}},
				{Number: 8, State: "open", Repository: "o/r"},
			}},
		},
		{
			name:  "reconcile fully unblocked tracker whose commissioning edge is closed dispatches wrap-up",
			event: issueEvent(EventReconcile),
			state: State{Issue: trackerIssue(), Blockers: []Issue{
				{Number: 3, State: "closed", Repository: "o/r", Labels: []string{LabelIdeation}},
				{Number: 8, State: "closed", Repository: "o/r"},
			}},
			wantAction: ActionWrapUp,
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
		// The event path dispatches `decompose` for an open ideation
		// root — the label, not the repository and not the title, is
		// what makes a root — but never implements or reassesses an
		// issue carrying a reserved label, and a comment is not a
		// revision of intent
		// (§forgejo/decisions/transition-table).
		{
			name:       "issue opened carrying the ideation label dispatches decompose",
			event:      issueEvent(EventIssueOpened),
			state:      State{Issue: ideationIssue()},
			wantAction: ActionDecompose,
		},
		{
			name:       "labels changed on an issue carrying the ideation label dispatches decompose",
			event:      issueEvent(EventIssueLabelsChanged),
			state:      State{Issue: ideationIssue()},
			wantAction: ActionDecompose,
		},
		{
			name:  "feedback comment on an issue carrying the ideation label holds off",
			event: issueEvent(EventIssueCommented),
			state: State{Issue: ideationIssue()},
		},
		{
			name:  "decompose already dispatched at the revision holds off on the event path",
			event: issueEvent(EventIssueOpened),
			state: State{Issue: ideationIssue()},
			watermark: &Watermark{
				Action:   string(ActionDecompose),
				Revision: testRev,
			},
		},
		{
			name:  "decompose already dispatched at an older revision re-fires on an edit at a new revision",
			event: issueEvent(EventIssueEdited),
			state: State{Issue: func() *Issue {
				i := ideationIssue()
				i.UpdatedAt = testTime2
				return i
			}()},
			watermark: &Watermark{
				Action:   string(ActionDecompose),
				Revision: testRev,
			},
			wantAction: ActionDecompose,
		},
		{
			name:       "a root is decomposed whatever blocks it",
			event:      issueEvent(EventIssueOpened),
			state:      State{Issue: ideationIssue(), Blockers: []Issue{{Number: 3, State: "open", Repository: "o/r"}}},
			wantAction: ActionDecompose,
		},
		{
			name:       "an ideation label takes precedence over human-task on the event path",
			event:      issueEvent(EventIssueLabelsChanged),
			state:      State{Issue: labelledIssue(LabelHumanTask, LabelIdeation)},
			wantAction: ActionDecompose,
		},
		{
			name:       "an ideation label takes precedence over outcome on the event path",
			event:      issueEvent(EventIssueOpened),
			state:      State{Issue: labelledIssue(LabelOutcome, LabelIdeation)},
			wantAction: ActionDecompose,
		},
		{
			name:  "an ideation root authored by the decomposer dispatches decompose",
			event: issueEvent(EventIssueOpened),
			state: State{Issue: func() *Issue {
				i := ideationIssue()
				i.Author = "decomposer"
				return i
			}(), DecomposerIdentity: "decomposer"},
			wantAction: ActionDecompose,
		},
		{
			name:       "issue opened carrying a differently cased ideation label dispatches decompose",
			event:      issueEvent(EventIssueOpened),
			state:      State{Issue: &Issue{Number: 7, State: "open", UpdatedAt: testTime, Repository: "o/r", Labels: []string{"Ideation"}}},
			wantAction: ActionDecompose,
		},
		{
			name:  "ideation issue closed on re-fetch holds off",
			event: issueEvent(EventIssueOpened),
			state: State{Issue: func() *Issue {
				i := ideationIssue()
				i.State = "closed"
				return i
			}()},
		},
		{
			name:  "edited ideation issue closed on re-fetch holds off",
			event: issueEvent(EventIssueEdited),
			state: State{Issue: func() *Issue {
				i := ideationIssue()
				i.State = "closed"
				return i
			}()},
		},
		{
			name:  "feedback comment on an issue carrying the outcome label holds off",
			event: issueEvent(EventIssueCommented),
			state: State{Issue: trackerIssue()},
		},
		{
			name:  "issue opened carrying the outcome label holds off",
			event: issueEvent(EventIssueOpened),
			state: State{Issue: trackerIssue()},
		},
		{
			// Reserved label names match case-insensitively: label
			// names are user-authored strings, and a miss must not
			// silently decide as implement
			// (§forgejo/reconciliation/watermark-exemptions).
			name:       "reconcile open issue carrying a differently cased ideation label dispatches decompose",
			event:      issueEvent(EventReconcile),
			state:      State{Issue: &Issue{Number: 7, State: "open", UpdatedAt: testTime, Repository: "o/r", Labels: []string{"Ideation"}}},
			wantAction: ActionDecompose,
		},
		{
			name:       "reconcile open tracker carrying an upper-cased outcome label dispatches wrap-up",
			event:      issueEvent(EventReconcile),
			state:      State{Issue: &Issue{Number: 7, State: "open", UpdatedAt: testTime, Repository: "o/r", Labels: []string{"OUTCOME"}}},
			wantAction: ActionWrapUp,
		},
		{
			name:  "issue opened carrying a differently cased reserved label holds off",
			event: issueEvent(EventIssueOpened),
			state: State{Issue: &Issue{Number: 7, State: "open", UpdatedAt: testTime, Repository: "o/r", Labels: []string{"Outcome"}}},
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
			if !reflect.DeepEqual(d.CascadeHoldOffs, tt.wantHolds) {
				t.Errorf("CascadeHoldOffs = %v, want %v", d.CascadeHoldOffs, tt.wantHolds)
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

// TestDecide_MergeGateHoldOffNamesBlockers pins the acceptance that an
// approved, mergeable PR whose connected issue has an open blocker
// dispatches no merge-or-wait — on the review event and on the sweep —
// with a reason naming the open blockers, cross-repo included, and not
// the closed ones or the PR itself (§forgejo/decisions/merge-gate).
func TestDecide_MergeGateHoldOffNamesBlockers(t *testing.T) {
	state := State{
		PR:      openPR("abc"),
		Reviews: []Review{review("APPROVED", "carol", testTime)},
		ConnectedIssues: []ConnectedIssue{connectedIssueOf(7,
			gateBlocker("o/r", 3, "open"),
			gateBlocker("other/repo", 12, "open"),
			gateBlocker("o/r", 5, "closed"),
			gateBlockerPR("o/r", 9, "open"), // the PR's own entry: not a blocker
		)},
	}
	for _, tt := range []struct {
		name  string
		event Event
	}{
		{"review event", prEvent(EventPRReviewed, "carol")},
		{"sweep", prEvent(EventReconcile, "maitred")},
	} {
		d := Decide(tt.event, state, nil)
		if d.Action == ActionMergeOrWait {
			t.Errorf("%s: dispatched merge-or-wait, want hold-off (reason %q)", tt.name, d.Reason)
		}
		if d.Dispatched {
			t.Errorf("%s: dispatched %q, want hold-off", tt.name, d.Action)
		}
		for _, want := range []string{"o/r#3", "other/repo#12"} {
			if !strings.Contains(d.Reason, want) {
				t.Errorf("%s: reason %q does not name blocker %s", tt.name, d.Reason, want)
			}
		}
		for _, not := range []string{"o/r#5", "o/r#9"} {
			if strings.Contains(d.Reason, not) {
				t.Errorf("%s: reason %q names a closed blocker or the PR itself (%s)", tt.name, d.Reason, not)
			}
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
	t.Run("an event-path hold-off for a reserved label names the label", func(t *testing.T) {
		d := Decide(issueEvent(EventIssueOpened), State{Issue: trackerIssue()}, nil)
		if !d.HoldOff() {
			t.Fatalf("expected hold-off, got dispatch of %q", d.Action)
		}
		if !strings.Contains(d.Reason, LabelOutcome) {
			t.Errorf("reason = %q, want it to name the reserved label", d.Reason)
		}
	})
}

// TestAlreadyDispatched pins the idempotency guard as a plain
// revision-keyed comparison on every path
// (§forgejo/decisions/loop-prevention). The watermark exemption is not
// implemented here: it is the sweep's exempt rows, which decide without
// consulting the watermark
// (§forgejo/reconciliation/watermark-exemptions).
func TestAlreadyDispatched(t *testing.T) {
	if alreadyDispatched(&Watermark{Action: "implement", Revision: testRev2}, ActionImplement, testRev) {
		t.Error("a different revision must not suppress implement")
	}
	if !alreadyDispatched(&Watermark{Action: "implement", Revision: testRev}, ActionImplement, testRev) {
		t.Error("implement at a matching revision must be suppressed")
	}
	if alreadyDispatched(&Watermark{Action: "wrap-up", Revision: testRev2}, ActionWrapUp, testRev) {
		t.Error("a different revision must not suppress wrap-up")
	}
	if !alreadyDispatched(&Watermark{Action: "wrap-up", Revision: testRev}, ActionWrapUp, testRev) {
		t.Error("the guard is revision-keyed on every path; the sweep's exempt rows never reach it")
	}
	if alreadyDispatched(nil, ActionImplement, testRev) {
		t.Error("a key with no watermark recorded must never be suppressed")
	}
}

// TestCascadeActionFitsState pins the cascade dispatch's re-check of
// its emission against the re-fetched issue: the state wins over the
// graph data — implement is denied to any reserved label, wrap-up
// only survives while the issue is an outcome tracker without
// ideation (§forgejo/decisions/unblock-cascade).
func TestCascadeActionFitsState(t *testing.T) {
	tests := []struct {
		name       string
		action     Action
		issue      *Issue
		wantOK     bool
		wantReason string
	}{
		{
			name:   "implement fits an ordinary issue",
			action: ActionImplement,
			issue:  &Issue{Number: 2, State: "open", Repository: "o/r"},
			wantOK: true,
		},
		{
			name:       "implement is denied to a tracker that gained the outcome label",
			action:     ActionImplement,
			issue:      &Issue{Number: 2, State: "open", Repository: "o/r", Labels: []string{LabelOutcome}},
			wantReason: "issue 2 carries the outcome label; never an implement candidate",
		},
		{
			name:       "implement is denied to an ideation root",
			action:     ActionImplement,
			issue:      &Issue{Number: 2, State: "open", Repository: "o/r", Labels: []string{LabelIdeation}},
			wantReason: "issue 2 carries the ideation label; never an implement candidate",
		},
		{
			name:   "wrap-up fits the tracker it was classified for",
			action: ActionWrapUp,
			issue:  &Issue{Number: 2, State: "open", Repository: "o/r", Labels: []string{LabelOutcome}},
			wantOK: true,
		},
		{
			name:       "wrap-up holds off when ideation arrived: decompose wins",
			action:     ActionWrapUp,
			issue:      &Issue{Number: 2, State: "open", Repository: "o/r", Labels: []string{LabelIdeation, LabelOutcome}},
			wantReason: "issue 2 carries the ideation label; decompose wins over wrap-up",
		},
		{
			name:       "wrap-up holds off when the outcome label is gone",
			action:     ActionWrapUp,
			issue:      &Issue{Number: 2, State: "open", Repository: "o/r"},
			wantReason: "issue 2 no longer carries the outcome label; nothing to wrap up",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, ok := cascadeActionFitsState(tt.action, tt.issue)
			if ok != tt.wantOK {
				t.Fatalf("fitsState = %v (reason %q), want %v", ok, d.Reason, tt.wantOK)
			}
			if !tt.wantOK && d.Reason != tt.wantReason {
				t.Errorf("reason = %q, want %q", d.Reason, tt.wantReason)
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

// labelledPR is an open mergeable PR carrying the given labels
// (§forgejo/decisions/label-triggered-review).
func labelledPR(sha string, labels ...string) *PullRequest {
	pr := openPR(sha)
	pr.Labels = labels
	return pr
}

// labelledPRNotMergeable is an open PR carrying the given labels with
// merge conflicts (§forgejo/decisions/label-triggered-review).
func labelledPRNotMergeable(sha string, labels ...string) *PullRequest {
	pr := notMergeablePR(sha)
	pr.Labels = labels
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

// trackerCascadeFixture is the tracker graph of the cascade's wrap-up
// rows: the closed issue 1 blocks the open outcome tracker 2, whose
// blockers are issue 1 (closed) and — when stillBlocked — issue 3
// (open) (§forgejo/decisions/unblock-cascade).
func trackerCascadeFixture(stillBlocked bool) *IssueNode {
	blockers := []IssueNode{{Issue: Issue{Number: 1, State: "closed", Repository: "o/r"}}}
	if stillBlocked {
		blockers = append(blockers, IssueNode{Issue: Issue{Number: 3, State: "open", Repository: "o/r"}})
	}
	return &IssueNode{
		Issue: Issue{Number: 1, State: "closed", Repository: "o/r"},
		Blocks: []IssueNode{
			{
				Issue:    Issue{Number: 2, State: "open", Repository: "o/r", Labels: []string{LabelOutcome}},
				Blockers: blockers,
			},
		},
	}
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
	pullNode := func(repo string, number int, state string) IssueNode {
		n := node(repo, number, state)
		n.Issue.IsPull = true
		return n
	}

	tests := []struct {
		name      string
		root      *IssueNode
		want      []CascadeDispatch
		wantHolds []CascadeHoldOff
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
			want: []CascadeDispatch{{Key: Key{Repo: "o/r", Kind: KindIssue, Number: 2}, Action: ActionImplement}},
		},
		{
			// A tracker whose last blocker just closed is a tracker whose
			// work is finished: the cascade emits wrap-up, never implement
			// (§forgejo/decisions/unblock-cascade).
			name: "a newly unblocked outcome tracker emits wrap-up",
			root: &IssueNode{
				Issue: Issue{Number: 1, State: "closed", Repository: "o/r"},
				Blocks: []IssueNode{
					{
						Issue:    Issue{Number: 2, State: "open", Repository: "o/r", Labels: []string{LabelOutcome}},
						Blockers: []IssueNode{node("o/r", 1, "closed")},
					},
				},
			},
			want: []CascadeDispatch{{Key: Key{Repo: "o/r", Kind: KindIssue, Number: 2}, Action: ActionWrapUp}},
		},
		{
			// decompose wins over wrap-up: an issue carrying both labels
			// takes the implement emission, which the reserved-label
			// insurance then holds off naming ideation
			// (§forgejo/decisions/unblock-cascade).
			name: "ideation outranks outcome in the cascade classification",
			root: &IssueNode{
				Issue: Issue{Number: 1, State: "closed", Repository: "o/r"},
				Blocks: []IssueNode{
					{
						Issue:    Issue{Number: 2, State: "open", Repository: "o/r", Labels: []string{LabelIdeation, LabelOutcome}},
						Blockers: []IssueNode{node("o/r", 1, "closed")},
					},
				},
			},
			want: []CascadeDispatch{{Key: Key{Repo: "o/r", Kind: KindIssue, Number: 2}, Action: ActionImplement}},
		},
		{
			// The still-blocked tracker is not silent: the hold-off names
			// which blockers remain open (§forgejo/decisions/unblock-cascade).
			name: "a still-blocked outcome tracker records a cascade hold-off naming its blockers",
			root: &IssueNode{
				Issue: Issue{Number: 1, State: "closed", Repository: "o/r"},
				Blocks: []IssueNode{
					{
						Issue: Issue{Number: 2, State: "open", Repository: "o/r", Labels: []string{LabelOutcome}},
						Blockers: []IssueNode{
							node("o/r", 1, "closed"),
							node("other/repo", 3, "open"),
						},
					},
				},
			},
			wantHolds: []CascadeHoldOff{{
				Key:    Key{Repo: "o/r", Kind: KindIssue, Number: 2},
				Issue:  &Issue{Number: 2, State: "open", Repository: "o/r", Labels: []string{LabelOutcome}},
				Reason: "issue 2 is an open outcome tracker still blocked by other/repo#3; wrap-up held off",
			}},
		},
		{
			// A tracker's work is delivered by its members, never by a pull
			// request of its own: the PR is emitted merge-or-wait and the
			// tracker is named as still held — the walk is never silent for a
			// tracker it does not emit for
			// (§forgejo/decisions/unblock-cascade).
			name: "a tracker held only by its connected open PR is named, not silent",
			root: &IssueNode{
				Issue: Issue{Number: 1, State: "closed", Repository: "o/r"},
				Blocks: []IssueNode{
					{
						Issue: Issue{Number: 2, State: "open", Repository: "o/r", Labels: []string{LabelOutcome}},
						Blockers: []IssueNode{
							node("o/r", 1, "closed"),
							pullNode("o/r", 9, "open"),
						},
					},
				},
			},
			want: []CascadeDispatch{{Key: Key{Repo: "o/r", Kind: KindPR, Number: 9}, Action: ActionMergeOrWait}},
			wantHolds: []CascadeHoldOff{{
				Key:    Key{Repo: "o/r", Kind: KindIssue, Number: 2},
				Issue:  &Issue{Number: 2, State: "open", Repository: "o/r", Labels: []string{LabelOutcome}},
				Reason: "issue 2 is an open outcome tracker still blocked by o/r#9; wrap-up held off",
			}},
		},
		{
			// #101 widened the commissioned forest to a tracker gated by a
			// closed root one hop away: the sweep curates it, so the hold-off
			// must not promise a wrap-up the state does not call for
			// (§forgejo/reconciliation/watermark-exemptions).
			name: "a tracker in a commissioned forest names the curation, not wrap-up",
			root: &IssueNode{
				Issue: Issue{Number: 1, State: "closed", Repository: "o/r", Labels: []string{LabelIdeation}},
				Blocks: []IssueNode{
					{
						Issue: Issue{Number: 2, State: "open", Repository: "o/r", Labels: []string{LabelOutcome}},
						Blockers: []IssueNode{
							{Issue: Issue{Number: 1, State: "closed", Repository: "o/r", Labels: []string{LabelIdeation}}},
							node("other/repo", 3, "open"),
						},
					},
				},
			},
			wantHolds: []CascadeHoldOff{{
				Key:    Key{Repo: "o/r", Kind: KindIssue, Number: 2},
				Issue:  &Issue{Number: 2, State: "open", Repository: "o/r", Labels: []string{LabelOutcome}},
				Reason: "issue 2 is an open outcome tracker commissioned by ideation 1 and still blocked by other/repo#3; the sweep curates the forest",
			}},
		},
		{
			// A blocker the engine cannot read as closed holds the tracker as
			// surely as an open one does: it is named, not dropped
			// (§forgejo/decisions/unblock-cascade).
			name: "a blocker in an unrecognised state still holds the tracker and is named",
			root: &IssueNode{
				Issue: Issue{Number: 1, State: "closed", Repository: "o/r"},
				Blocks: []IssueNode{
					{
						Issue: Issue{Number: 2, State: "open", Repository: "o/r", Labels: []string{LabelOutcome}},
						Blockers: []IssueNode{
							node("o/r", 1, "closed"),
							node("o/r", 3, "blocked"),
						},
					},
				},
			},
			wantHolds: []CascadeHoldOff{{
				Key:    Key{Repo: "o/r", Kind: KindIssue, Number: 2},
				Issue:  &Issue{Number: 2, State: "open", Repository: "o/r", Labels: []string{LabelOutcome}},
				Reason: "issue 2 is an open outcome tracker still blocked by o/r#3; wrap-up held off",
			}},
		},
		{
			// Closing the tracker roots its own cascade: its members are
			// closed, the walk cycles back to the visited root, and nothing
			// is emitted — termination is tested, not assumed
			// (§forgejo/decisions/unblock-cascade).
			name: "a closed tracker cascades to nothing",
			root: &IssueNode{
				Issue: Issue{Number: 1, State: "closed", Repository: "o/r", Labels: []string{LabelOutcome}},
				Blocks: []IssueNode{
					{
						Issue:  Issue{Number: 2, State: "closed", Repository: "o/r"},
						Blocks: []IssueNode{node("o/r", 1, "closed")},
					},
				},
			},
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
			want: []CascadeDispatch{{Key: Key{Repo: "other/repo", Kind: KindIssue, Number: 2}, Action: ActionImplement}},
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
			want: []CascadeDispatch{{Key: Key{Repo: "o/r", Kind: KindIssue, Number: 3}, Action: ActionImplement}},
		},
		{
			// A member's outbound edges point at the sync issue it
			// belongs to: the walk emits it as a sync candidate,
			// whatever its readiness — the set's own evaluation
			// decides (§forgejo/decisions/sync-set).
			name: "a closed issue blocking a sync issue emits the sync-set candidate, not implement",
			root: &IssueNode{
				Issue: Issue{Number: 1, State: "closed", Repository: "o/r"},
				Blocks: []IssueNode{
					{
						Issue:    Issue{Number: 11, State: "open", Repository: "o/r", Labels: []string{LabelSync}},
						Blockers: []IssueNode{node("o/r", 1, "closed")},
					},
				},
			},
			want: []CascadeDispatch{{Key: Key{Repo: "o/r", Kind: KindIssue, Number: 11}, Action: ActionSync}},
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
			want: []CascadeDispatch{{Key: Key{Repo: "o/r", Kind: KindIssue, Number: 4}, Action: ActionImplement}},
		},
		{
			// The walk is label-blind: labels are consulted only at
			// emission, and a human-task emission — classified implement —
			// is held off by the reserved-label insurance at dispatch
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
			want: []CascadeDispatch{{Key: Key{Repo: "o/r", Kind: KindIssue, Number: 2}, Action: ActionImplement}},
		},
		{
			// A closed issue-side blocker unblocks the connected
			// open PR, not the issue: the PR is the work
			// (§forgejo/decisions/unblock-cascade).
			name: "an issue whose remaining open blocker is its connected PR unblocks the PR",
			root: &IssueNode{
				Issue: Issue{Number: 1, State: "closed", Repository: "o/r"},
				Blocks: []IssueNode{
					{
						Issue: Issue{Number: 2, State: "open", Repository: "o/r"},
						Blockers: []IssueNode{
							node("o/r", 1, "closed"),
							pullNode("o/r", 9, "open"),
						},
					},
				},
			},
			want: []CascadeDispatch{{Key: Key{Repo: "o/r", Kind: KindPR, Number: 9}, Action: ActionMergeOrWait}},
		},
		{
			name: "a connected PR unblocked by two issues is unblocked once",
			root: &IssueNode{
				Issue: Issue{Number: 1, State: "closed", Repository: "o/r"},
				Blocks: []IssueNode{
					{
						Issue: Issue{Number: 2, State: "open", Repository: "o/r"},
						Blockers: []IssueNode{
							node("o/r", 1, "closed"),
							pullNode("o/r", 9, "open"),
						},
					},
					{
						Issue: Issue{Number: 3, State: "open", Repository: "o/r"},
						Blockers: []IssueNode{
							node("o/r", 1, "closed"),
							pullNode("o/r", 9, "open"),
						},
					},
				},
			},
			want: []CascadeDispatch{{Key: Key{Repo: "o/r", Kind: KindPR, Number: 9}, Action: ActionMergeOrWait}},
		},
		{
			name: "an issue still blocked on the issue side is not unblocked, connected PR included",
			root: &IssueNode{
				Issue: Issue{Number: 1, State: "closed", Repository: "o/r"},
				Blocks: []IssueNode{
					{
						Issue: Issue{Number: 2, State: "open", Repository: "o/r"},
						Blockers: []IssueNode{
							node("o/r", 1, "closed"),
							node("o/r", 3, "open"),
							pullNode("o/r", 9, "open"),
						},
					},
				},
			},
		},
		{
			name: "a cross-repo connected PR carries its own key",
			root: &IssueNode{
				Issue: Issue{Number: 1, State: "closed", Repository: "o/r"},
				Blocks: []IssueNode{
					{
						Issue: Issue{Number: 2, State: "open", Repository: "o/r"},
						Blockers: []IssueNode{
							node("o/r", 1, "closed"),
							pullNode("other/repo", 9, "open"),
						},
					},
				},
			},
			want: []CascadeDispatch{{Key: Key{Repo: "other/repo", Kind: KindPR, Number: 9}, Action: ActionMergeOrWait}},
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
			got, gotHolds := unblockCascade(tt.root)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("unblockCascade() = %v, want %v", got, tt.want)
			}
			if !reflect.DeepEqual(gotHolds, tt.wantHolds) {
				t.Errorf("unblockCascade() hold-offs = %v, want %v", gotHolds, tt.wantHolds)
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
			state:      State{Issue: labelledIssue(LabelOutcome)},
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

// TestDecide_DecomposerAuthorshipHoldOffNamesTheAuthorship pins the
// authorship hold-off's reason on the event path and on the sweep: the
// reason names the authorship that denied the action
// (§forgejo/decisions/loop-prevention).
func TestDecide_DecomposerAuthorshipHoldOffNamesTheAuthorship(t *testing.T) {
	tests := []struct {
		name  string
		event Event
		state State
	}{
		{
			name:  "issue opened",
			event: issueEvent(EventIssueOpened),
			state: State{Issue: authoredIssue("decomposer"), DecomposerIdentity: "decomposer"},
		},
		{
			name:  "reconcile — the sweep's synthetic current-state event",
			event: issueEvent(EventReconcile),
			state: State{Issue: authoredIssue("decomposer"), DecomposerIdentity: "decomposer"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := Decide(tt.event, tt.state, nil)
			if !d.HoldOff() {
				t.Fatalf("decision = %+v, want a hold-off", d)
			}
			if !strings.Contains(d.Reason, "authored by the decomposer") || !strings.Contains(d.Reason, "decomposer") {
				t.Errorf("hold-off reason = %q, want it to name the authorship and the identity", d.Reason)
			}
		})
	}
}

// TestDecide_CascadeUnblocksDecomposerAuthoredMember pins that the
// authorship rule does not narrow the unblock cascade: the ideation
// ticket closing unblocks the member the decomposer authored
// (§forgejo/decisions/loop-prevention, §forgejo/decisions/unblock-cascade).
func TestDecide_CascadeUnblocksDecomposerAuthoredMember(t *testing.T) {
	root := &IssueNode{
		Issue: Issue{Number: 3, State: "closed", Repository: "o/r"},
		Blocks: []IssueNode{
			{
				Issue:    Issue{Number: 7, State: "open", Repository: "o/r", Author: "decomposer"},
				Blockers: []IssueNode{{Issue: Issue{Number: 3, State: "closed", Repository: "o/r"}}},
			},
		},
	}
	d := Decide(issueEvent(EventIssueClosed), State{Issue: closedIssue(), CascadeRoot: root, DecomposerIdentity: "decomposer"}, nil)
	want := []CascadeDispatch{{Key: Key{Repo: "o/r", Kind: KindIssue, Number: 7}, Action: ActionImplement}}
	if !reflect.DeepEqual(d.Cascade, want) {
		t.Errorf("cascade = %v, want %v (the cascade is not narrowed by authorship)", d.Cascade, want)
	}
}

// TestDecide_IdeationRowsReasons pins the reasons of the ideation event
// rows: a comment on an open root holds off saying a comment is not a
// revision of intent, and an opened, edited, or re-labelled root
// dispatches decompose naming the root
// (§forgejo/decisions/transition-table).
func TestDecide_IdeationRowsReasons(t *testing.T) {
	t.Run("comment hold-off says a comment is not a revision of intent", func(t *testing.T) {
		d := Decide(issueEvent(EventIssueCommented), State{Issue: ideationIssue()}, nil)
		if !d.HoldOff() {
			t.Fatalf("dispatched %q, want a hold-off", d.Action)
		}
		if !strings.Contains(d.Reason, "revision of intent") {
			t.Errorf("reason = %q, want it to say a comment is not a revision of intent", d.Reason)
		}
	})
	for _, typ := range []EventType{EventIssueOpened, EventIssueEdited, EventIssueLabelsChanged} {
		d := Decide(issueEvent(typ), State{Issue: ideationIssue()}, nil)
		if d.HoldOff() || d.Action != ActionDecompose {
			t.Fatalf("%s: decision = %+v, want a dispatch of %q", typ, d, ActionDecompose)
		}
		if !strings.Contains(d.Reason, LabelIdeation) {
			t.Errorf("%s: reason = %q, want it to name the ideation label", typ, d.Reason)
		}
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
		{ActionRejectPR, workItem, true},
		{ActionImplement, outcomes, false},
		{ActionReassess, outcomes, false},
		{ActionReview, outcomes, false},
		{ActionRejectPR, outcomes, false},
		// The outcomes actions: decompose fires on the event path and
		// on the sweep, wrap-up on the sweep (#83, #84, #85).
		{Action("decompose"), outcomes, true},
		{Action("wrap-up"), outcomes, true},
		{Action("decompose"), workItem, false},
		{Action("wrap-up"), workItem, false},
		{ActionImplement, both, true},
		{Action("wrap-up"), both, true},
		{ActionSync, outcomes, true},
		{ActionSync, workItem, false},
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
		{ActionSync, "action sync is inadmissible for repo o/r: requires the maitred-outcomes-repo role"},
		{Action("frobnicate"), "action frobnicate is admitted by no maitred role"},
	}
	for _, tt := range tests {
		if got := holdOffActionInadmissible(tt.action, "o/r").Reason; got != tt.want {
			t.Errorf("holdOffActionInadmissible(%q) reason = %q, want %q", tt.action, got, tt.want)
		}
	}
}

// teardownRoot returns an open ideation root carrying the reserved
// teardown-forest label: the human has ordered the forest it commissioned
// to be deleted (§forgejo/decisions/forest-teardown).
// standingForest is the downstream edge that makes a root's forest
// stand: the emptiness test the teardown rows read
// (§forgejo/decisions/forest-teardown).
func standingForest() []Issue {
	return []Issue{{Number: 9, State: "open", Repository: "o/r"}}
}

func teardownRoot() *Issue {
	root := ideationIssue()
	root.Labels = append(root.Labels, LabelTeardownForest)
	return root
}

// TestDecide_ForestTeardownRows pins the teardown rows: the label on an
// open root dispatches teardown-forest where the root row would dispatch
// decompose, it decides nothing without `ideation`, and it never fires on
// a closed root (§forgejo/decisions/forest-teardown).
func TestDecide_ForestTeardownRows(t *testing.T) {
	for _, typ := range []EventType{EventIssueOpened, EventIssueEdited, EventIssueLabelsChanged} {
		e := issueEvent(typ)
		d := Decide(e, State{Issue: teardownRoot(), Blocked: standingForest()}, nil)
		if d.Action != ActionTeardownForest {
			t.Errorf("%s on a root carrying the label: action = %q, want %q (%s)",
				typ, d.Action, ActionTeardownForest, d.Reason)
		}
	}

	for _, tc := range []struct {
		name  string
		event Event
		issue *Issue
	}{
		{
			// Closing commissions the forest; no label un-happens it
			// (§forgejo/decisions/commissioning).
			name:  "a closed root carrying the label holds off",
			event: issueEvent(EventIssueLabelsChanged),
			issue: func() *Issue {
				root := teardownRoot()
				root.State = "closed"
				return root
			}(),
		},
		{
			// A modifier, not a kind label: it retires no work item.
			name:  "the label without ideation holds off",
			event: issueEvent(EventIssueLabelsChanged),
			issue: labelledIssue(LabelTeardownForest),
		},
		{
			// Kind labels are named first: what an issue is outweighs
			// what a label modifies (§forgejo/decisions/actions).
			name:  "the label beside a kind label names the kind label",
			event: issueEvent(EventIssueLabelsChanged),
			issue: labelledIssue(LabelHumanTask, LabelTeardownForest),
		},
		{
			name:  "a comment on a root under teardown is not a revision of intent",
			event: issueEvent(EventIssueCommented),
			issue: teardownRoot(),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := Decide(tc.event, State{Issue: tc.issue}, nil)
			if d.Action != "" {
				t.Errorf("action = %q, want the hold-off (%s)", d.Action, d.Reason)
			}
			if tc.name == "the label beside a kind label names the kind label" &&
				!strings.Contains(d.Reason, LabelHumanTask) {
				t.Errorf("reason = %q, want the kind label named first", d.Reason)
			}
		})
	}
}

// TestSyncSetRevision pins the set revision's properties: it depends
// on every member's issue revision and PR head sha, never on the
// order the members are enumerated in — so the sweep does not re-fire
// at an unchanged set state, and any member moving re-fires
// (§forgejo/decisions/sync-set, §forgejo/state/watermark).
func TestSyncSetRevision(t *testing.T) {
	members := []SyncMember{syncMemberFixture(4, 40), closedSyncMemberFixture(5)}
	reversed := []SyncMember{closedSyncMemberFixture(5), syncMemberFixture(4, 40)}
	if syncSetRevision(members) != syncSetRevision(reversed) {
		t.Error("set revision must not depend on member order")
	}

	pushed := []SyncMember{syncMemberFixture(4, 40), closedSyncMemberFixture(5)}
	pushed[0].PR.HeadSHA = "sha-new"
	if syncSetRevision(members) == syncSetRevision(pushed) {
		t.Error("a member PR head moving must change the set revision")
	}

	edited := []SyncMember{syncMemberFixture(4, 40), closedSyncMemberFixture(5)}
	edited[1].Issue.UpdatedAt = testTime2
	if syncSetRevision(members) == syncSetRevision(edited) {
		t.Error("a member issue's activity must change the set revision")
	}

	if syncSetRevision(nil) == syncSetRevision(members) {
		t.Error("an empty set must not share a revision with a populated one")
	}
}

// TestDecide_SyncSetApprovalHoldOffNamesTheSet pins the approval
// path's hold-off reason: the PR stands approved but its connected
// issue is a set member — landing is the set's decision, not the
// PR's (§forgejo/decisions/sync-set).
func TestDecide_SyncSetApprovalHoldOffNamesTheSet(t *testing.T) {
	tests := []struct {
		name  string
		event Event
		state State
	}{
		{
			name:  "review event",
			event: prEvent(EventPRReviewed, "carol"),
			state: State{
				PR:              openPR("abc"),
				Reviews:         []Review{review("APPROVED", "carol", testTime)},
				ConnectedIssues: []ConnectedIssue{syncMemberConnected(7, 11)},
			},
		},
		{
			name:  "sweep PR path standing at an approved head",
			event: prEvent(EventReconcile, "maitred"),
			state: State{
				PR:              openPR("abc"),
				Reviews:         []Review{review("APPROVED", "carol", testTime)},
				ConnectedIssues: []ConnectedIssue{syncMemberConnected(7, 11)},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := Decide(tt.event, tt.state, nil)
			if d.Action != "" || !d.HoldOff() {
				t.Fatalf("decision = %+v, want a hold-off dispatching nothing", d)
			}
			if !strings.Contains(d.Reason, "sync set") || !strings.Contains(d.Reason, "o/r#7") {
				t.Errorf("hold-off reason = %q, want it to name the sync set and the member o/r#7", d.Reason)
			}
		})
	}
}

// TestDecide_TeardownProtectsTheTrackerThroughTheRootsOwnEdge pins where
// the tracker's protection actually comes from: the order is pre-closure,
// so while a teardown runs the root is open, and an open root is an open
// blocker — which holds the tracker's `wrap-up` and curation rows off
// without a rule of its own. The label reaching a *closed* root changes
// nothing anywhere: a stale label must freeze neither the tracker's
// wrap-up nor its curation (§forgejo/decisions/forest-teardown).
func TestDecide_TeardownProtectsTheTrackerThroughTheRootsOwnEdge(t *testing.T) {
	member := func(state string) Issue {
		return Issue{Number: 11, State: state, Repository: "o/r"}
	}
	root := func(state string) Issue {
		r := teardownRoot()
		r.Number = 3
		r.State = state
		return *r
	}
	tracker := func() *Issue {
		tr := trackerIssue()
		return tr
	}

	for _, tc := range []struct {
		name       string
		blockers   []Issue
		wantAction Action
	}{
		{
			name:       "a labelled root still open holds the tracker's rows off",
			blockers:   []Issue{root("open"), member("closed")},
			wantAction: "",
		},
		{
			name:       "deleting members cannot empty the tracker while the root is open",
			blockers:   []Issue{root("open")},
			wantAction: "",
		},
		{
			name:       "a stale label on a closed root does not freeze wrap-up",
			blockers:   []Issue{root("closed")},
			wantAction: ActionWrapUp,
		},
		{
			name:       "a stale label on a closed root does not freeze curation",
			blockers:   []Issue{root("closed"), member("open")},
			wantAction: ActionDecompose,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := tracker()
			d := Decide(Event{Type: EventReconcile, Repo: "o/r", Kind: KindIssue, Number: tr.Number},
				State{Issue: tr, Blockers: tc.blockers}, nil)
			if d.Action != tc.wantAction {
				t.Errorf("action = %q, want %q (%s)", d.Action, tc.wantAction, d.Reason)
			}
		})
	}
}

// TestDecide_TeardownNamesTheLabelAndTheRoot states the reason: the
// decision log must say which root is being retired and why the state
// calls for it (§forgejo/decisions/forest-teardown).
func TestDecide_TeardownNamesTheLabelAndTheRoot(t *testing.T) {
	root := teardownRoot()
	d := Decide(issueEvent(EventIssueLabelsChanged), State{Issue: root, Blocked: standingForest()}, nil)
	if !strings.Contains(d.Reason, LabelTeardownForest) || !strings.Contains(d.Reason, LabelIdeation) {
		t.Errorf("reason = %q, want both reserved label names", d.Reason)
	}
	if !strings.Contains(d.Reason, fmt.Sprintf("%d", root.Number)) {
		t.Errorf("reason = %q, want the root's number", d.Reason)
	}
}

// TestDecide_TeardownRefiresOnTheSweepWhateverTheWatermark pins the
// sweep's exemption: the trigger is the forest still standing, so a
// teardown that died half way through re-fires until the graph is empty
// (§forgejo/reconciliation/watermark-exemptions).
func TestDecide_TeardownRefiresOnTheSweepWhateverTheWatermark(t *testing.T) {
	root := teardownRoot()
	w := &Watermark{Action: string(ActionTeardownForest), Revision: issueRevision(root)}
	d := Decide(Event{Type: EventReconcile, Repo: "o/r", Kind: KindIssue, Number: root.Number}, State{Issue: root, Blocked: standingForest()}, w)
	if d.Action != ActionTeardownForest {
		t.Errorf("action = %q, want %q (%s)", d.Action, ActionTeardownForest, d.Reason)
	}
	if strings.Contains(d.Reason, "already dispatched") {
		t.Errorf("reason = %q, want the state fact, not a revision guard", d.Reason)
	}
}

// TestDecide_TeardownDispatchesOnlyWhileTheForestStands pins the
// emptiness test: the engine can see whether the root blocks anything,
// so an empty forest — never decomposed, or already deleted — dispatches
// nothing. The order still stands, so the hold-off names the state fact
// rather than a revision, and any blocked issue, closed included, is a
// forest worth an agent's report (§forgejo/decisions/forest-teardown).
func TestDecide_TeardownDispatchesOnlyWhileTheForestStands(t *testing.T) {
	for _, tc := range []struct {
		name       string
		blocked    []Issue
		wantAction Action
	}{
		{"the root blocks nothing: there is no forest to delete", nil, ""},
		{
			"a closed member is still a member, and a report is owed",
			[]Issue{{Number: 9, State: "closed", Repository: "o/r"}},
			ActionTeardownForest,
		},
		{
			"an open member is the ordinary case",
			[]Issue{{Number: 9, State: "open", Repository: "o/r"}},
			ActionTeardownForest,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := teardownRoot()
			d := Decide(issueEvent(EventIssueLabelsChanged), State{Issue: root, Blocked: tc.blocked}, nil)
			if d.Action != tc.wantAction {
				t.Errorf("action = %q, want %q (%s)", d.Action, tc.wantAction, d.Reason)
			}
			if tc.wantAction == "" && !strings.Contains(d.Reason, "forest") {
				t.Errorf("reason = %q, want the state fact naming the empty forest", d.Reason)
			}
		})
	}
}

// TestDecide_EmptyForestStopsTheTeardownRefiring is the exemption's
// terminus: the sweep re-fires a teardown while the forest stands, and
// stops the moment it does not — so a completed teardown costs no further
// agent runs, and a label left standing on a root that never had a forest
// costs none at all (§forgejo/reconciliation/watermark-exemptions).
func TestDecide_EmptyForestStopsTheTeardownRefiring(t *testing.T) {
	root := teardownRoot()
	for i := 0; i < 2; i++ {
		d := Decide(Event{Type: EventReconcile, Repo: "o/r", Kind: KindIssue, Number: root.Number},
			State{Issue: root, Blocked: nil}, nil)
		if d.Action != "" {
			t.Fatalf("sweep %d dispatched %q (%s), want nothing: an empty forest is not a state that calls for the action",
				i+1, d.Action, d.Reason)
		}
	}
}

// TestDecide_SyncSetMemberEdgeCaseReasons: the hold-off reasons for
// the two readings of the member label that decide alone — the
// double opt-in names both labels; the member of no open set is not
// held off at all, the merge gate deciding on
// (§forgejo/decisions/sync-set).
func TestDecide_SyncSetMemberEdgeCaseReasons(t *testing.T) {
	t.Run("dual-labelled member names both labels", func(t *testing.T) {
		d := Decide(prEvent(EventPRReviewed, "carol"), State{
			PR:              openPR("abc"),
			Reviews:         []Review{review("APPROVED", "carol", testTime)},
			ConnectedIssues: []ConnectedIssue{dualLabelledSyncMember(7)},
		}, nil)
		if !d.HoldOff() {
			t.Fatalf("decision = %+v, want a hold-off", d)
		}
		for _, want := range []string{LabelAgenticSyncMerge, LabelAgenticAutoMerge, "o/r#7"} {
			if !strings.Contains(d.Reason, want) {
				t.Errorf("hold-off reason = %q, want it to name %q", d.Reason, want)
			}
		}
	})
	t.Run("member of no open set is not held off", func(t *testing.T) {
		d := Decide(prEvent(EventPRReviewed, "carol"), State{
			PR:              openPR("abc"),
			Reviews:         []Review{review("APPROVED", "carol", testTime)},
			ConnectedIssues: []ConnectedIssue{syncMemberWithoutSets(7)},
		}, nil)
		if d.Action != ActionMergeOrWait {
			t.Errorf("decision = %+v, want merge-or-wait: a stale member label is no gate", d)
		}
	})
}
