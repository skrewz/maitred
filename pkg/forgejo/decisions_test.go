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
// (§forgejo/decisions/transition-table).
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

// prMemberFixture is a member of a landing set that blocks the
// grouping issue directly as a pull request: the PR is its own
// delivery, so the predicate holds it to the same test it asks of a
// member's delivering PR (§forgejo/decisions/sync-set).
func prMemberFixture(number int, mergeable bool, optedIn bool) SyncMember {
	m := SyncMember{Issue: Issue{
		Number:     number,
		State:      "open",
		IsPull:     true,
		Repository: "o/r",
		UpdatedAt:  testTime,
	}}
	if optedIn {
		m.Issue.Labels = []string{LabelAgenticSyncMerge}
	}
	m.PR = openPR("sha-" + strconv.Itoa(number))
	m.PR.Mergeable = mergeable
	m.Reviews = []Review{{Event: ReviewApproved, Author: "carol", CommitID: m.PR.HeadSHA, SubmittedAt: testTime}}
	return m
}

// closedPRMemberFixture is a direct PR member whose delivery landed:
// merged, which is ready whatever its labels
// (§forgejo/decisions/sync-set).
func closedPRMemberFixture(number int) SyncMember {
	m := prMemberFixture(number, true, true)
	m.Issue.State = "closed"
	return m
}

// directPRMemberConnected builds the graph a pull request sees when it
// blocks a grouping issue **directly**: the connected issue is the
// grouping issue itself — it carries `sync`, not the member label — and
// the PR under test stands in its blockers, which is what makes the row
// a member whose delivery is itself. The sets column is the membership
// the PR's own assembly records from that edge
// (§forgejo/decisions/sync-set, §forgejo/webhook/re-fetch).
func directPRMemberConnected(syncNumber int, withPRInBlockers bool) ConnectedIssue {
	set := syncIssueFixture(syncNumber)
	set.Repository = "o/outcomes"
	c := ConnectedIssue{
		Issue:    *set,
		Blockers: []Issue{{Number: 5, State: "closed", Repository: "o/r"}},
	}
	if withPRInBlockers {
		c.Blockers = append(c.Blockers, gateBlockerPR("o/r", 9, "open"))
	}
	return c
}

// directPRMemberState is the state of a pull request that is itself a
// member of an open landing set: the grouping issue among its connected
// issues, and the set the assembly read off it
// (§forgejo/decisions/sync-set).
func directPRMemberState(pr *PullRequest, syncNumber int) State {
	set := syncIssueFixture(syncNumber)
	set.Repository = "o/outcomes"
	return State{
		ReviewLenses:    testLenses,
		PR:              pr,
		Reviews:         []Review{review("APPROVED", "carol", testTime)},
		ConnectedIssues: []ConnectedIssue{directPRMemberConnected(syncNumber, true)},
		SyncSets:        []Issue{*set},
	}
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
// connected issue the merge gate reads (§forgejo/decisions/merge-gate).
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

// openPRBy returns an open PR authored by the given login: the input
// to the fix-feedback ownership gate (§forgejo/decisions/loop-prevention,
// s-git/maitred#143).
func openPRBy(author, sha string) *PullRequest {
	pr := openPR(sha)
	pr.Author = author
	return pr
}

// testImplementerIdentity is the configured implementer identity used
// by the ownership-gate fixtures (§forgejo/config/implementer-identity).
const testImplementerIdentity = "s-autonomics-implementer"

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

// testLenses is the lens roster the fixtures default to: the single
// lens is carol, the reviewing author of the fixtures' reviews, so a
// fixture keeps the standing the retired single-reviewer rule gave it
// (§forgejo/decisions/review-lenses). Tests of the fan-out itself
// override the roster per case.
var testLenses = []ReviewLens{{Identity: "carol"}}

// lensWatermark returns a watermark whose subject slots record the
// fan-out dispatch of the named lenses at revision — the shape a
// dispatched review chain leaves behind (§forgejo/state/watermark).
func lensWatermark(revision string, lenses ...string) *Watermark {
	w := &Watermark{Subjects: map[string]Slot{}}
	for _, l := range lenses {
		w.Subjects[l] = Slot{Action: string(ActionReview), Revision: revision}
	}
	return w
}

// TestActionNames pins the action names: they form the enum the engine
// config maps to canned prompts (§forgejo/decisions/actions).
func TestActionNames(t *testing.T) {
	want := map[Action]string{
		ActionImplement:      "implement",
		ActionReassess:       "reassess",
		ActionFixFeedback:    "fix-feedback",
		ActionMergeOrWait:    "merge-or-wait",
		ActionRebase:         "rebase",
		ActionDecompose:      "decompose",
		ActionWrapUp:         "wrap-up",
		ActionTeardownForest: "teardown-forest",
		ActionSync:           "sync",
	}
	for action, name := range want {
		if string(action) != name {
			t.Errorf("action = %q, want %q", action, name)
		}
	}
	// The fan-out keeps the retired singleton's name in the log while
	// leaving the configured enum: review is dispatched per lens, never
	// configured as an action entry (§forgejo/decisions/review-lenses).
	if string(ActionReview) != "review" {
		t.Errorf("ActionReview = %q, want \"review\"", ActionReview)
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
			state:      State{ReviewLenses: testLenses, Issue: openIssue()},
			wantAction: ActionImplement,
		},
		{
			name:  "issue opened with an open blocker holds off",
			event: issueEvent(EventIssueOpened),
			state: State{ReviewLenses: testLenses, Issue: openIssue(), Blockers: []Issue{{Number: 3, State: "open", Repository: "o/r"}}},
		},
		{
			name:       "issue opened with only closed blockers dispatches implement",
			event:      issueEvent(EventIssueOpened),
			state:      State{ReviewLenses: testLenses, Issue: openIssue(), Blockers: []Issue{{Number: 3, State: "closed", Repository: "o/r"}}},
			wantAction: ActionImplement,
		},
		{
			name:  "issue opened with a connected PR holds off",
			event: issueEvent(EventIssueOpened),
			state: State{ReviewLenses: testLenses, Issue: openIssue(), ConnectedPRs: []PullRequest{*openPR("abc")}},
		},
		{
			name:  "issue opened but closed on re-fetch holds off",
			event: issueEvent(EventIssueOpened),
			state: State{ReviewLenses: testLenses, Issue: closedIssue()},
		},
		{
			name:  "issue opened carrying human-task holds off",
			event: issueEvent(EventIssueOpened),
			state: State{ReviewLenses: testLenses, Issue: labelledIssue(LabelHumanTask)},
		},
		{
			// The seeding hold-off: a forest member created by the
			// decomposer is not dispatchable until its blocking edges
			// are wired and the label is removed (#125).
			name:  "issue opened carrying forest-seeding holds off",
			event: issueEvent(EventIssueOpened),
			state: State{ReviewLenses: testLenses, Issue: labelledIssue(LabelForestSeeding)},
		},
		{
			name:  "reconciled issue carrying forest-seeding holds off",
			event: issueEvent(EventReconcile),
			state: State{ReviewLenses: testLenses, Issue: labelledIssue(LabelForestSeeding)},
		},
		{
			// The ideation branch is evaluated before the connected-PR
			// check: a root is decomposed whatever else hangs off it.
			name:       "an open ideation root with a connected open PR dispatches decompose",
			event:      issueEvent(EventIssueOpened),
			state:      State{ReviewLenses: testLenses, Issue: ideationIssue(), ConnectedPRs: []PullRequest{*openPR("abc")}},
			wantAction: ActionDecompose,
		},
		{
			// Authorship is no gate (s-git/maitred#135): a seed-less,
			// unblocked member the decomposer authored is an ordinary
			// work item on the opened path and on the sweep alike.
			name:       "issue opened authored by the decomposer dispatches implement",
			event:      issueEvent(EventIssueOpened),
			state:      State{ReviewLenses: testLenses, Issue: authoredIssue("decomposer")},
			wantAction: ActionImplement,
		},
		{
			name:       "issue opened authored by anyone else dispatches implement",
			event:      issueEvent(EventIssueOpened),
			state:      State{ReviewLenses: testLenses, Issue: authoredIssue("alice")},
			wantAction: ActionImplement,
		},
		{
			// The seeding hold-off is the gate, whatever the author:
			// the differently cased login is no longer a distinction
			// the engine reads at all.
			name:       "issue opened authored by a differently cased login dispatches implement",
			event:      issueEvent(EventIssueOpened),
			state:      State{ReviewLenses: testLenses, Issue: authoredIssue("Decomposer")},
			wantAction: ActionImplement,
		},
		{
			name:  "issue opened authored by the decomposer with closed blockers dispatches implement",
			event: issueEvent(EventIssueOpened),
			state: State{
				ReviewLenses: testLenses,
				Issue:        authoredIssue("decomposer"),
				Blockers:     []Issue{{Number: 3, State: "closed", Repository: "o/r"}},
			},
			wantAction: ActionImplement,
		},
		{
			// The sweep is the cascade's backstop: the reconciliation
			// row decides a mature decomposer-authored member like any
			// other work item (s-git/maitred#135).
			name:       "reconciled issue authored by the decomposer dispatches implement",
			event:      issueEvent(EventReconcile),
			state:      State{ReviewLenses: testLenses, Issue: authoredIssue("decomposer")},
			wantAction: ActionImplement,
		},
		{
			name:  "issue opened without re-fetched state holds off",
			event: issueEvent(EventIssueOpened),
		},
		{
			name:       "issue edited dispatches reassess",
			event:      issueEvent(EventIssueEdited),
			state:      State{ReviewLenses: testLenses, Issue: openIssue()},
			wantAction: ActionReassess,
		},
		{
			name:       "issue labels changed dispatches reassess",
			event:      issueEvent(EventIssueLabelsChanged),
			state:      State{ReviewLenses: testLenses, Issue: openIssue()},
			wantAction: ActionReassess,
		},
		{
			name:       "issue feedback comment dispatches reassess",
			event:      issueEvent(EventIssueCommented),
			state:      State{ReviewLenses: testLenses, Issue: openIssue()},
			wantAction: ActionReassess,
		},
		{
			name:  "issue edited with an open blocker holds off",
			event: issueEvent(EventIssueEdited),
			state: State{ReviewLenses: testLenses, Issue: openIssue(), Blockers: []Issue{{Number: 3, State: "open", Repository: "o/r"}}},
		},
		{
			name:  "issue labels changed with an open blocker holds off",
			event: issueEvent(EventIssueLabelsChanged),
			state: State{ReviewLenses: testLenses, Issue: openIssue(), Blockers: []Issue{{Number: 3, State: "open", Repository: "o/r"}}},
		},
		{
			name:  "issue commented with an open blocker holds off",
			event: issueEvent(EventIssueCommented),
			state: State{ReviewLenses: testLenses, Issue: openIssue(), Blockers: []Issue{{Number: 3, State: "open", Repository: "o/r"}}},
		},
		{
			name:       "issue edited with only closed blockers dispatches reassess",
			event:      issueEvent(EventIssueEdited),
			state:      State{ReviewLenses: testLenses, Issue: openIssue(), Blockers: []Issue{{Number: 3, State: "closed", Repository: "o/r"}}},
			wantAction: ActionReassess,
		},
		{
			name:  "issue edited but closed on re-fetch holds off",
			event: issueEvent(EventIssueEdited),
			state: State{ReviewLenses: testLenses, Issue: closedIssue()},
		},
		{
			name:       "issue edited carrying ideation dispatches decompose",
			event:      issueEvent(EventIssueEdited),
			state:      State{ReviewLenses: testLenses, Issue: labelledIssue("ideation")},
			wantAction: ActionDecompose,
		},
		{
			name:  "issue labels changed carrying outcome holds off",
			event: issueEvent(EventIssueLabelsChanged),
			state: State{ReviewLenses: testLenses, Issue: labelledIssue("outcome")},
		},
		{
			name:  "issue commented carrying human-task holds off",
			event: issueEvent(EventIssueCommented),
			state: State{ReviewLenses: testLenses, Issue: labelledIssue(LabelHumanTask)},
		},
		{
			name:  "issue labels changed carrying forest-seeding holds off — reassess is denied during seeding",
			event: issueEvent(EventIssueLabelsChanged),
			state: State{ReviewLenses: testLenses, Issue: labelledIssue(LabelForestSeeding)},
		},
		{
			name:       "issue edited carrying an ordinary label dispatches reassess",
			event:      issueEvent(EventIssueEdited),
			state:      State{ReviewLenses: testLenses, Issue: labelledIssue("bug")},
			wantAction: ActionReassess,
		},
		{
			name:        "issue closed runs the unblock cascade",
			event:       issueEvent(EventIssueClosed),
			state:       State{ReviewLenses: testLenses, Issue: closedIssue(), CascadeRoot: cascadeFixture()},
			wantCascade: []CascadeDispatch{{Key: Key{Repo: "o/r", Kind: KindIssue, Number: 2}, Action: ActionImplement}},
		},
		{
			name:        "issue closed newly unblocks an outcome tracker: the cascade emits wrap-up, never implement",
			event:       issueEvent(EventIssueClosed),
			state:       State{ReviewLenses: testLenses, Issue: closedIssue(), CascadeRoot: trackerCascadeFixture(false)},
			wantCascade: []CascadeDispatch{{Key: Key{Repo: "o/r", Kind: KindIssue, Number: 2}, Action: ActionWrapUp}},
		},
		{
			name:  "issue closed but open on re-fetch holds off without cascade",
			event: issueEvent(EventIssueClosed),
			state: State{ReviewLenses: testLenses, Issue: openIssue(), CascadeRoot: cascadeFixture()},
		},
		{
			name:      "issue closed leaves an outcome tracker blocked: the cascade records a hold-off naming the blockers",
			event:     issueEvent(EventIssueClosed),
			state:     State{ReviewLenses: testLenses, Issue: closedIssue(), CascadeRoot: trackerCascadeFixture(true)},
			wantHolds: []CascadeHoldOff{{Key: Key{Repo: "o/r", Kind: KindIssue, Number: 2}, Issue: &Issue{Number: 2, State: "open", Repository: "o/r", Labels: []string{LabelOutcome}}, Reason: "issue 2 is an open outcome tracker still blocked by o/r#3; wrap-up held off"}},
		},
		{
			name:       "PR opened dispatches review",
			event:      prEvent(EventPROpened, "alice"),
			state:      State{ReviewLenses: testLenses, PR: openPR("abc")},
			wantAction: ActionReview,
		},
		{
			name:       "PR opened not mergeable dispatches rebase",
			event:      prEvent(EventPROpened, "alice"),
			state:      State{ReviewLenses: testLenses, PR: notMergeablePR("abc")},
			wantAction: ActionRebase,
		},
		{
			// Nothing about a connected issue's blockers decides a PR's
			// opening: un-commissioned work is held off upstream, by the
			// seeding window, and the merge gate is where a blocked merge
			// is stopped (§forgejo/decisions/commissioning,
			// §forgejo/decisions/merge-gate, s-git/maitred#151).
			name:  "PR opened whose connected issue is blocked by an open ideation issue dispatches review",
			event: prEvent(EventPROpened, "alice"),
			state: State{
				ReviewLenses: testLenses,
				PR:           openPR("abc"),
				ConnectedIssues: []ConnectedIssue{{
					Issue:    *connectedIssue(),
					Blockers: []Issue{{Number: 1, State: "open", Repository: "o/r", Labels: []string{LabelIdeation}}},
				}},
			},
			wantAction: ActionReview,
		},
		{
			name:  "PR opened whose second connected issue is blocked by an open ideation issue dispatches review",
			event: prEvent(EventPROpened, "alice"),
			state: State{
				ReviewLenses: testLenses,
				PR:           openPR("abc"),
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
			wantAction: ActionReview,
		},
		{
			name:  "PR opened whose two connected issues have no open ideation blocker dispatches review",
			event: prEvent(EventPROpened, "alice"),
			state: State{
				ReviewLenses: testLenses,
				PR:           openPR("abc"),
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
			// The connected issue's own state decides nothing either: the
			// open row reads the PR alone.
			name:  "PR opened against a closed connected issue blocked by an open ideation issue dispatches review",
			event: prEvent(EventPROpened, "alice"),
			state: State{
				ReviewLenses: testLenses,
				PR:           openPR("abc"),
				ConnectedIssues: []ConnectedIssue{{
					Issue: func() Issue {
						issue := *connectedIssue()
						issue.State = "closed"
						return issue
					}(),
					Blockers: []Issue{{Number: 1, State: "open", Repository: "o/r", Labels: []string{LabelIdeation}}},
				}},
			},
			wantAction: ActionReview,
		},
		{
			// The reserved name is matched exactly and case-sensitively
			// (§forgejo/decisions/transition-table).
			name:  "PR opened whose connected issue is blocked by an open issue labelled Ideation dispatches review",
			event: prEvent(EventPROpened, "alice"),
			state: State{
				ReviewLenses: testLenses,
				PR:           openPR("abc"),
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
				ReviewLenses: testLenses,
				PR:           openPR("abc"),
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
				ReviewLenses: testLenses,
				PR:           openPR("abc"),
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
				ReviewLenses: testLenses,
				PR:           openPR("abc"),
			},
			wantAction: ActionReview,
		},
		{
			name:  "PR opened but closed on re-fetch holds off",
			event: prEvent(EventPROpened, "alice"),
			state: State{ReviewLenses: testLenses, PR: closedPR("abc")},
		},
		{
			name:  "PR opened without re-fetched state holds off",
			event: prEvent(EventPROpened, "alice"),
		},
		{
			name:       "PR synced without prior review dispatches review",
			event:      prEvent(EventPRSynced, "alice"),
			state:      State{ReviewLenses: testLenses, PR: openPR("abc")},
			wantAction: ActionReview,
		},
		{
			name:       "PR synced after a review re-fires the lenses without an approval at the new head",
			event:      prEvent(EventPRSynced, "alice"),
			state:      State{ReviewLenses: testLenses, PR: openPR("abc"), Reviews: []Review{review("COMMENT", "carol", testTime)}},
			wantAction: ActionReview,
		},
		{
			name:       "PR synced not mergeable dispatches rebase",
			event:      prEvent(EventPRSynced, "alice"),
			state:      State{ReviewLenses: testLenses, PR: notMergeablePR("abc")},
			wantAction: ActionRebase,
		},
		{
			name:  "PR synced pushed by the reviewing author holds off",
			event: prEvent(EventPRSynced, "carol"),
			state: State{ReviewLenses: testLenses, PR: openPR("abc"), Reviews: []Review{review("APPROVED", "carol", testTime)}},
		},
		{
			name:       "PR synced pushed by the reviewing author of an older review dispatches every lens — the push predates any lens standing at head",
			event:      prEvent(EventPRSynced, "bob"),
			state:      State{ReviewLenses: testLenses, PR: openPR("abc"), Reviews: []Review{review("APPROVED", "bob", testTime), review("COMMENT", "carol", testTime2)}},
			wantAction: ActionReview,
		},
		{
			name:  "PR synced pushed by the latest reviewing author holds off",
			event: prEvent(EventPRSynced, "carol"),
			state: State{ReviewLenses: testLenses, PR: openPR("abc"), Reviews: []Review{review("APPROVED", "bob", testTime), review("COMMENT", "carol", testTime2)}},
		},
		{
			name:       "PR synced pushed by the implementer after a review dispatches every lens",
			event:      prEvent(EventPRSynced, "alice"),
			state:      State{ReviewLenses: testLenses, PR: openPR("abc"), Reviews: []Review{review(ReviewChangesRequested, "carol", testTime)}},
			wantAction: ActionReview,
		},
		{
			// The trigger-review label is the explicit request for an
			// agentic review (§forgejo/decisions/label-triggered-review).
			name:       "PR synced carrying trigger-review without a review dispatches review",
			event:      prEvent(EventPRSynced, "alice"),
			state:      State{ReviewLenses: testLenses, PR: labelledPR("abc", LabelTriggerReview)},
			wantAction: ActionReview,
		},
		{
			name:       "PR synced carrying trigger-review with a review re-fires the lens",
			event:      prEvent(EventPRSynced, "alice"),
			state:      State{ReviewLenses: testLenses, PR: labelledPR("abc", LabelTriggerReview), Reviews: []Review{review("APPROVED", "carol", testTime)}},
			wantAction: ActionReview,
		},
		{
			// The label is an explicit request, not the author's own
			// push: the reviewing-author sender guard does not
			// suppress a label-requested review
			// (§forgejo/decisions/label-triggered-review).
			name:       "PR synced carrying trigger-review pushed by the reviewing author dispatches the lens review",
			event:      prEvent(EventPRSynced, "carol"),
			state:      State{ReviewLenses: testLenses, PR: labelledPR("abc", LabelTriggerReview), Reviews: []Review{review("APPROVED", "carol", testTime)}},
			wantAction: ActionReview,
		},
		{
			name:       "PR synced carrying a differently cased trigger-review label dispatches the fan-out at the current revision",
			event:      prEvent(EventPRSynced, "carol"),
			state:      State{ReviewLenses: testLenses, PR: labelledPR("abc", "Trigger-Review"), Reviews: []Review{review("APPROVED", "carol", testTime)}},
			wantAction: ActionReview,
		},
		{
			// Keyed to the label's presence at the current revision:
			// the label merely present does not re-fire the review
			// (§forgejo/decisions/label-triggered-review,
			// §forgejo/decisions/loop-prevention).
			name:      "PR synced carrying trigger-review already dispatched at the revision holds off",
			event:     prEvent(EventPRSynced, "alice"),
			state:     State{ReviewLenses: testLenses, PR: labelledPR("abc", LabelTriggerReview), Reviews: []Review{review("APPROVED", "carol", testTime)}},
			watermark: lensWatermark("abc", "carol"),
		},
		{
			// The label requests a review of mergeable work; a
			// conflicting PR is rebased first
			// (§forgejo/decisions/label-triggered-review).
			name:       "PR synced carrying trigger-review but not mergeable dispatches rebase",
			event:      prEvent(EventPRSynced, "alice"),
			state:      State{ReviewLenses: testLenses, PR: labelledPRNotMergeable("abc", LabelTriggerReview)},
			wantAction: ActionRebase,
		},
		{
			name:       "PR review changes-requested dispatches fix-feedback",
			event:      prEvent(EventPRReviewed, "carol"),
			state:      State{ReviewLenses: testLenses, PR: openPR("abc"), Reviews: []Review{review(ReviewChangesRequested, "carol", testTime)}},
			wantAction: ActionFixFeedback,
		},
		{
			// The ownership gate: fix-feedback is the implementer's
			// action, dispatched only when the PR's author is the
			// configured implementer identity — a human handling their
			// own PR must not be collided with
			// (§forgejo/decisions/loop-prevention, s-git/maitred#143).
			name:  "PR review changes-requested on a PR not authored by the implementer identity holds off",
			event: prEvent(EventPRReviewed, "carol"),
			state: State{
				ReviewLenses:        testLenses,
				PR:                  openPRBy("skrewz-bot", "abc"),
				Reviews:             []Review{review(ReviewChangesRequested, "carol", testTime)},
				ImplementerIdentity: testImplementerIdentity,
			},
		},
		{
			name:  "PR review changes-requested on a PR authored by the implementer identity dispatches fix-feedback",
			event: prEvent(EventPRReviewed, "carol"),
			state: State{
				ReviewLenses:        testLenses,
				PR:                  openPRBy(testImplementerIdentity, "abc"),
				Reviews:             []Review{review(ReviewChangesRequested, "carol", testTime)},
				ImplementerIdentity: testImplementerIdentity,
			},
			wantAction: ActionFixFeedback,
		},
		{
			// An unset identity keeps the gate inert: dispatch exactly
			// as before, whatever the author (§forgejo/config/implementer-identity).
			name:       "PR review changes-requested with no implementer identity configured dispatches fix-feedback whatever the author",
			event:      prEvent(EventPRReviewed, "carol"),
			state:      State{ReviewLenses: testLenses, PR: openPRBy("skrewz-bot", "abc"), Reviews: []Review{review(ReviewChangesRequested, "carol", testTime)}},
			wantAction: ActionFixFeedback,
		},
		{
			// A PR whose author is empty (no user in the API
			// response) reads as not owned when an identity is
			// configured: the fail-closed exact match, pinned at
			// the decision level so a future TrimSpace- or
			// EqualFold-style tweak to the comparison cannot
			// silently widen who gets dispatched
			// (§forgejo/client/operations, s-git/maitred#143).
			name:  "PR review changes-requested with an empty author and a configured implementer identity holds off",
			event: prEvent(EventPRReviewed, "carol"),
			state: State{
				ReviewLenses:        testLenses,
				PR:                  openPRBy("", "abc"),
				Reviews:             []Review{review(ReviewChangesRequested, "carol", testTime)},
				ImplementerIdentity: testImplementerIdentity,
			},
		},
		{
			// The gate covers fix-feedback alone: reviewing and
			// reporting on a human-owned PR remain intended
			// (s-git/maitred#143).
			name:  "PR review approved on a PR not authored by the implementer identity dispatches merge-or-wait",
			event: prEvent(EventPRReviewed, "carol"),
			state: State{
				ReviewLenses:        testLenses,
				PR:                  optedIntoMerge(openPRBy("skrewz-bot", "abc")),
				Reviews:             []Review{review("APPROVED", "carol", testTime)},
				ImplementerIdentity: testImplementerIdentity,
			},
			wantAction: ActionMergeOrWait,
		},
		{
			name:       "PR review approved dispatches merge-or-wait",
			event:      prEvent(EventPRReviewed, "carol"),
			state:      State{ReviewLenses: testLenses, PR: optedIntoMerge(openPR("abc")), Reviews: []Review{review("APPROVED", "carol", testTime)}},
			wantAction: ActionMergeOrWait,
		},
		{
			// A submitted review decides on the review's own content: what
			// stops an un-commissioned merge is the merge gate, named by
			// its open blockers, not a rejection decided ahead of the row
			// (§forgejo/decisions/merge-gate, s-git/maitred#151).
			name:  "PR review approved whose connected issue is blocked by an open ideation issue holds off at the merge gate",
			event: prEvent(EventPRReviewed, "carol"),
			state: State{
				ReviewLenses: testLenses,
				PR:           openPR("abc"),
				Reviews:      []Review{review("APPROVED", "carol", testTime)},
				ConnectedIssues: []ConnectedIssue{{
					Issue:    *connectedIssue(),
					Blockers: []Issue{{Number: 1, State: "open", Repository: "o/r", Labels: []string{LabelIdeation}}},
				}},
			},
		},
		{
			name:  "PR review changes-requested whose connected issue is blocked by an open ideation issue dispatches fix-feedback",
			event: prEvent(EventPRReviewed, "carol"),
			state: State{
				ReviewLenses: testLenses,
				PR:           openPR("abc"),
				Reviews:      []Review{review(ReviewChangesRequested, "carol", testTime)},
				ConnectedIssues: []ConnectedIssue{{
					Issue:    *connectedIssue(),
					Blockers: []Issue{{Number: 1, State: "open", Repository: "o/r", Labels: []string{LabelIdeation}}},
				}},
			},
			wantAction: ActionFixFeedback,
		},
		{
			name:  "PR review approved whose connected issue is blocked only by a closed ideation issue dispatches merge-or-wait",
			event: prEvent(EventPRReviewed, "carol"),
			state: State{
				ReviewLenses: testLenses,
				PR:           optedIntoMerge(openPR("abc")),
				Reviews:      []Review{review("APPROVED", "carol", testTime)},
				ConnectedIssues: []ConnectedIssue{{
					Issue:    *connectedIssue(),
					Blockers: []Issue{{Number: 1, State: "closed", Repository: "o/r", Labels: []string{LabelIdeation}}},
				}},
			},
			wantAction: ActionMergeOrWait,
		},
		{
			name:  "PR review comment holds off",
			event: prEvent(EventPRReviewed, "carol"),
			state: State{ReviewLenses: testLenses, PR: openPR("abc"), Reviews: []Review{review("COMMENT", "carol", testTime)}},
		},
		{
			name:  "PR review without reviews on re-fetch holds off",
			event: prEvent(EventPRReviewed, "carol"),
			state: State{ReviewLenses: testLenses, PR: openPR("abc")},
		},
		{
			name:       "PR review latest review wins",
			event:      prEvent(EventPRReviewed, "carol"),
			state:      State{ReviewLenses: testLenses, PR: optedIntoMerge(openPR("abc")), Reviews: []Review{review(ReviewChangesRequested, "carol", testTime), review("APPROVED", "carol", testTime2)}},
			wantAction: ActionMergeOrWait,
		},
		// The merge gate: merge-or-wait only when no connected
		// issue has an open blocker (§forgejo/decisions/merge-gate).
		{
			name:  "PR review approved with a connected issue blocked by an open issue holds off",
			event: prEvent(EventPRReviewed, "carol"),
			state: State{
				ReviewLenses: testLenses,
				PR:           openPR("abc"), Reviews: []Review{review("APPROVED", "carol", testTime)},
				ConnectedIssues: []ConnectedIssue{connectedIssueOf(7, gateBlocker("o/r", 3, "closed"), gateBlocker("o/r", 4, "open"))},
			},
		},
		{
			name:  "PR review approved with a connected issue's blockers all closed dispatches merge-or-wait",
			event: prEvent(EventPRReviewed, "carol"),
			state: State{
				ReviewLenses: testLenses,
				PR:           optedIntoMerge(openPR("abc")), Reviews: []Review{review("APPROVED", "carol", testTime)},
				ConnectedIssues: []ConnectedIssue{connectedIssueOf(7, gateBlocker("o/r", 3, "closed"))},
			},
			wantAction: ActionMergeOrWait,
		},
		{
			name:  "PR review approved where the only open blocker is the PR itself dispatches merge-or-wait",
			event: prEvent(EventPRReviewed, "carol"),
			state: State{
				ReviewLenses: testLenses,
				PR:           optedIntoMerge(openPR("abc")), Reviews: []Review{review("APPROVED", "carol", testTime)},
				ConnectedIssues: []ConnectedIssue{connectedIssueOf(7, gateBlockerPR("o/r", 9, "open"))},
			},
			wantAction: ActionMergeOrWait,
		},
		{
			name:  "PR review approved with the second connected issue blocked holds off",
			event: prEvent(EventPRReviewed, "carol"),
			state: State{
				ReviewLenses: testLenses,
				PR:           openPR("abc"), Reviews: []Review{review("APPROVED", "carol", testTime)},
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
				ReviewLenses: testLenses,
				PR:           openPR("abc"), Reviews: []Review{review(ReviewChangesRequested, "carol", testTime)},
				ConnectedIssues: []ConnectedIssue{connectedIssueOf(7, gateBlocker("o/r", 3, "open"))},
			},
			wantAction: ActionFixFeedback,
		},
		{
			name:  "PR synced with a connected issue blocked dispatches the fan-out — the push path decides no merge",
			event: prEvent(EventPRSynced, "alice"),
			state: State{
				ReviewLenses: testLenses,
				PR:           openPR("abc"), Reviews: []Review{review("COMMENT", "carol", testTime)},
				ConnectedIssues: []ConnectedIssue{connectedIssueOf(7, gateBlocker("o/r", 3, "open"))},
			},
			wantAction: ActionReview,
		},
		{
			name:  "PR opened with a connected issue blocked dispatches review — the open path decides no merge",
			event: prEvent(EventPROpened, "alice"),
			state: State{
				ReviewLenses:    testLenses,
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
				ReviewLenses:    testLenses,
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
				ReviewLenses:    testLenses,
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
				ReviewLenses:    testLenses,
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
				ReviewLenses:    testLenses,
				PR:              optedIntoMerge(openPR("abc")),
				Reviews:         []Review{review("APPROVED", "carol", testTime)},
				ConnectedIssues: []ConnectedIssue{syncMemberWithoutSets(7)},
			},
			wantAction: ActionMergeOrWait,
		},
		{
			name:  "reconcile PR approved at head on a member of no open sync set dispatches merge-or-wait",
			event: prEvent(EventReconcile, "maitred"),
			state: State{
				ReviewLenses:    testLenses,
				PR:              optedIntoMerge(openPR("abc")),
				Reviews:         []Review{review("APPROVED", "carol", testTime)},
				ConnectedIssues: []ConnectedIssue{syncMemberWithoutSets(7)},
			},
			wantAction: ActionMergeOrWait,
		},
		{
			name:  "PR review approved on a member carrying both merge opt-ins holds off with no open set — the error state is contained",
			event: prEvent(EventPRReviewed, "carol"),
			state: State{
				ReviewLenses:    testLenses,
				PR:              openPR("abc"),
				Reviews:         []Review{review("APPROVED", "carol", testTime)},
				ConnectedIssues: []ConnectedIssue{dualLabelledSyncMember(7)},
			},
		},
		{
			name:  "reconcile issue carrying the sync label with no members holds off — an empty set has nothing to land",
			event: issueEvent(EventReconcile),
			state: State{ReviewLenses: testLenses, Issue: syncIssueFixture(7)},
		},
		{
			// The sweep is the set's only backstop, so the sync row
			// is tested first: a second kind claim is authoring the
			// engine names rather than resolves by label order
			// (§forgejo/decisions/sync-set).
			name:  "reconcile issue carrying sync alongside outcome holds off naming both kind labels",
			event: issueEvent(EventReconcile),
			state: State{
				ReviewLenses: testLenses,
				Issue:        syncIssueWithLabel(7, LabelOutcome),
				SyncMembers:  []SyncMember{syncMemberFixture(4, 40), closedSyncMemberFixture(5)},
				Blockers:     []Issue{{Number: 4, State: "closed", Repository: "o/r"}, {Number: 5, State: "closed", Repository: "o/r"}},
			},
		},
		{
			name:  "reconcile issue carrying sync alongside ideation holds off naming both kind labels",
			event: issueEvent(EventReconcile),
			state: State{
				ReviewLenses: testLenses,
				Issue:        syncIssueWithLabel(7, LabelIdeation),
				Blockers:     []Issue{{Number: 4, State: "closed", Repository: "o/r"}},
			},
		},
		{
			name:       "reconcile sync issue with every member closed or approved dispatches sync",
			event:      issueEvent(EventReconcile),
			state:      State{ReviewLenses: testLenses, Issue: syncIssueFixture(7), SyncMembers: []SyncMember{syncMemberFixture(4, 40), closedSyncMemberFixture(5)}},
			wantAction: ActionSync,
		},
		{
			// A pull request blocking the grouping issue directly is a
			// member the graph put in front of the set: the predicate
			// reads its own state rather than deciding past it
			// (§forgejo/decisions/sync-set).
			name:       "reconcile sync issue whose direct PR member is approved at head dispatches sync",
			event:      issueEvent(EventReconcile),
			state:      State{ReviewLenses: testLenses, Issue: syncIssueFixture(7), SyncMembers: []SyncMember{prMemberFixture(9, true, true), closedSyncMemberFixture(5)}},
			wantAction: ActionSync,
		},
		{
			name:       "reconcile sync issue whose direct PR member has landed dispatches sync",
			event:      issueEvent(EventReconcile),
			state:      State{ReviewLenses: testLenses, Issue: syncIssueFixture(7), SyncMembers: []SyncMember{closedPRMemberFixture(9), closedSyncMemberFixture(5)}},
			wantAction: ActionSync,
		},
		{
			name:  "reconcile sync issue whose direct PR member is not mergeable holds off",
			event: issueEvent(EventReconcile),
			state: State{ReviewLenses: testLenses, Issue: syncIssueFixture(7), SyncMembers: []SyncMember{prMemberFixture(9, false, true), closedSyncMemberFixture(5)}},
		},
		{
			name:  "reconcile sync issue whose direct PR member carries no opt-in holds off",
			event: issueEvent(EventReconcile),
			state: State{ReviewLenses: testLenses, Issue: syncIssueFixture(7), SyncMembers: []SyncMember{prMemberFixture(9, true, false), closedSyncMemberFixture(5)}},
		},
		{
			name:  "reconcile sync issue with an unlabelled open member holds off — the member awaits a human",
			event: issueEvent(EventReconcile),
			state: State{ReviewLenses: testLenses, Issue: syncIssueFixture(7), SyncMembers: []SyncMember{
				syncMemberFixture(4, 40),
				{Issue: Issue{Number: 5, State: "open", Repository: "o/r", UpdatedAt: testTime}},
			}},
		},
		{
			name:  "reconcile sync issue with a member whose PR is not approved at head holds off",
			event: issueEvent(EventReconcile),
			state: State{ReviewLenses: testLenses, Issue: syncIssueFixture(7), SyncMembers: []SyncMember{
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
			state: State{ReviewLenses: testLenses, Issue: syncIssueFixture(7), SyncMembers: []SyncMember{
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
			state: State{ReviewLenses: testLenses, Issue: syncIssueFixture(7), SyncMembers: []SyncMember{syncMemberFixture(4, 40), closedSyncMemberFixture(5)}},
			watermark: &Watermark{Action: string(ActionSync), Revision: syncSetRevision(syncIssueFixture(7), []SyncMember{
				closedSyncMemberFixture(5), syncMemberFixture(4, 40),
			}, testLenses)},
		},
		{
			name:       "reconcile sync issue whose set state moved re-dispatches over the watermark",
			event:      issueEvent(EventReconcile),
			state:      State{ReviewLenses: testLenses, Issue: syncIssueFixture(7), SyncMembers: []SyncMember{syncMemberFixture(4, 40), syncMemberFixture(5, 50)}},
			watermark:  &Watermark{Action: string(ActionSync), Revision: syncSetRevision(syncIssueFixture(7), []SyncMember{syncMemberFixture(4, 40), closedSyncMemberFixture(5)}, testLenses)},
			wantAction: ActionSync,
		},
		{
			name:  "issue opened carrying the sync label holds off — never an implement candidate",
			event: issueEvent(EventIssueOpened),
			state: State{ReviewLenses: testLenses, Issue: syncIssueFixture(7)},
		},
		{
			name:        "PR merged runs the unblock cascade",
			event:       prEvent(EventPRMerged, "alice"),
			state:       State{ReviewLenses: testLenses, PR: mergedPR("abc"), CascadeRoot: cascadeFixture()},
			wantCascade: []CascadeDispatch{{Key: Key{Repo: "o/r", Kind: KindIssue, Number: 2}, Action: ActionImplement}},
		},
		{
			name:  "PR merged but not merged on re-fetch holds off without cascade",
			event: prEvent(EventPRMerged, "alice"),
			state: State{ReviewLenses: testLenses, PR: openPR("abc"), CascadeRoot: cascadeFixture()},
		},
		{
			name:  "PR closed without merging holds off",
			event: prEvent(EventPRClosed, "alice"),
			state: State{ReviewLenses: testLenses, PR: closedPR("abc")},
		},
		{
			name:       "reconcile issue behaves like issue opened",
			event:      issueEvent(EventReconcile),
			state:      State{ReviewLenses: testLenses, Issue: openIssue()},
			wantAction: ActionImplement,
		},
		{
			name:  "reconcile blocked issue holds off",
			event: issueEvent(EventReconcile),
			state: State{ReviewLenses: testLenses, Issue: openIssue(), Blockers: []Issue{{Number: 3, State: "open", Repository: "o/r"}}},
		},
		{
			name:       "reconcile PR behaves like PR synced",
			event:      prEvent(EventReconcile, "maitred"),
			state:      State{ReviewLenses: testLenses, PR: openPR("abc")},
			wantAction: ActionReview,
		},
		{
			name:       "reconcile PR standing at a comment review dispatches fix-feedback",
			event:      prEvent(EventReconcile, "maitred"),
			state:      State{ReviewLenses: testLenses, PR: openPR("abc"), Reviews: []Review{review("COMMENT", "carol", testTime)}},
			wantAction: ActionFixFeedback,
		},
		{
			// The sweep follows a changes-requested review into
			// fix-feedback only for a PR the implementer identity owns
			// (§forgejo/decisions/loop-prevention, s-git/maitred#143).
			name:  "reconcile PR standing at a changes-requested review on a PR not authored by the implementer identity holds off",
			event: prEvent(EventReconcile, "maitred"),
			state: State{
				ReviewLenses:        testLenses,
				PR:                  openPRBy("skrewz-bot", "abc"),
				Reviews:             []Review{review(ReviewChangesRequested, "carol", testTime)},
				ImplementerIdentity: testImplementerIdentity,
			},
		},
		{
			name:  "reconcile PR standing at a comment review on a PR not authored by the implementer identity holds off",
			event: prEvent(EventReconcile, "maitred"),
			state: State{
				ReviewLenses:        testLenses,
				PR:                  openPRBy("skrewz-bot", "abc"),
				Reviews:             []Review{review("COMMENT", "carol", testTime)},
				ImplementerIdentity: testImplementerIdentity,
			},
		},
		{
			name:  "reconcile PR standing at a comment review authored by the implementer identity dispatches fix-feedback",
			event: prEvent(EventReconcile, "maitred"),
			state: State{
				ReviewLenses:        testLenses,
				PR:                  openPRBy(testImplementerIdentity, "abc"),
				Reviews:             []Review{review("COMMENT", "carol", testTime)},
				ImplementerIdentity: testImplementerIdentity,
			},
			wantAction: ActionFixFeedback,
		},
		{
			// The sweep's other PR rows are untouched by the gate:
			// an approved human-owned PR still reaches
			// merge-or-wait, a synced one still reaches review
			// (s-git/maitred#143).
			name:  "reconcile PR standing at an approved review on a PR not authored by the implementer identity dispatches merge-or-wait",
			event: prEvent(EventReconcile, "maitred"),
			state: State{
				ReviewLenses:        testLenses,
				PR:                  optedIntoMerge(openPRBy("skrewz-bot", "abc")),
				Reviews:             []Review{review("APPROVED", "carol", testTime)},
				ImplementerIdentity: testImplementerIdentity,
			},
			wantAction: ActionMergeOrWait,
		},
		{
			name:       "PR synced on a PR not authored by the implementer identity dispatches review",
			event:      prEvent(EventPRSynced, "skrewz-bot"),
			state:      State{ReviewLenses: testLenses, PR: openPRBy("skrewz-bot", "abc"), ImplementerIdentity: testImplementerIdentity},
			wantAction: ActionReview,
		},
		{
			name:       "reconcile PR standing at a changes-requested review dispatches fix-feedback",
			event:      prEvent(EventReconcile, "maitred"),
			state:      State{ReviewLenses: testLenses, PR: openPR("abc"), Reviews: []Review{review(ReviewChangesRequested, "carol", testTime)}},
			wantAction: ActionFixFeedback,
		},
		{
			name:       "reconcile PR standing at an approved review dispatches merge-or-wait",
			event:      prEvent(EventReconcile, "maitred"),
			state:      State{ReviewLenses: testLenses, PR: optedIntoMerge(openPR("abc")), Reviews: []Review{review("APPROVED", "carol", testTime)}},
			wantAction: ActionMergeOrWait,
		},
		{
			name:  "reconcile PR standing at an approved review with a connected issue blocked holds off",
			event: prEvent(EventReconcile, "maitred"),
			state: State{
				ReviewLenses: testLenses,
				PR:           openPR("abc"), Reviews: []Review{review("APPROVED", "carol", testTime)},
				ConnectedIssues: []ConnectedIssue{connectedIssueOf(7, gateBlocker("o/r", 3, "open"))},
			},
		},
		{
			name:  "reconcile PR standing at a changes-requested review with a connected issue blocked still dispatches fix-feedback",
			event: prEvent(EventReconcile, "maitred"),
			state: State{
				ReviewLenses: testLenses,
				PR:           openPR("abc"), Reviews: []Review{review(ReviewChangesRequested, "carol", testTime)},
				ConnectedIssues: []ConnectedIssue{connectedIssueOf(7, gateBlocker("o/r", 3, "open"))},
			},
			wantAction: ActionFixFeedback,
		},
		{
			name:       "reconcile PR standing at a dismissed review dispatches that lens again — a dismissed approval is no approval",
			event:      prEvent(EventReconcile, "maitred"),
			state:      State{ReviewLenses: testLenses, PR: openPR("abc"), Reviews: []Review{review("DISMISSED", "carol", testTime)}},
			wantAction: ActionReview,
		},
		{
			name:       "reconcile PR whose reviews predate the head dispatches the fan-out at the new head",
			event:      prEvent(EventReconcile, "maitred"),
			state:      State{ReviewLenses: testLenses, PR: openPR("def"), Reviews: []Review{review(ReviewChangesRequested, "carol", testTime)}},
			wantAction: ActionReview,
		},
		{
			// The sweep's PR path decides as PR synced and as the
			// review-following rows do; a connected issue blocked by an
			// open ideation issue is the merge gate's business alone
			// (§forgejo/decisions/merge-gate, s-git/maitred#151).
			name:  "reconcile PR whose connected issue is blocked by an open ideation issue dispatches review",
			event: prEvent(EventReconcile, "maitred"),
			state: State{
				ReviewLenses: testLenses,
				PR:           openPR("abc"),
				ConnectedIssues: []ConnectedIssue{{
					Issue:    *connectedIssue(),
					Blockers: []Issue{{Number: 1, State: "open", Repository: "o/r", Labels: []string{LabelIdeation}}},
				}},
			},
			wantAction: ActionReview,
		},
		{
			// The label overrides the trigger, not the review's
			// content: a label-requested review that has landed and
			// approves at head decides on under the merge policy
			// (§forgejo/decisions/label-triggered-review).
			name:       "reconcile PR carrying trigger-review standing at an approved review dispatches merge-or-wait",
			event:      prEvent(EventReconcile, "alice"),
			state:      State{ReviewLenses: testLenses, PR: optedIntoMerge(labelledPR("abc", LabelTriggerReview)), Reviews: []Review{review("APPROVED", "carol", testTime)}},
			wantAction: ActionMergeOrWait,
		},
		{
			name:  "reconcile PR standing at an approved review whose connected issue is blocked by an open ideation issue holds off at the merge gate",
			event: prEvent(EventReconcile, "maitred"),
			state: State{
				ReviewLenses: testLenses,
				PR:           openPR("abc"),
				Reviews:      []Review{review("APPROVED", "carol", testTime)},
				ConnectedIssues: []ConnectedIssue{{
					Issue:    *connectedIssue(),
					Blockers: []Issue{{Number: 1, State: "open", Repository: "o/r", Labels: []string{LabelIdeation}}},
				}},
			},
		},
		{
			name:      "implement already dispatched at the revision holds off",
			event:     issueEvent(EventIssueOpened),
			state:     State{ReviewLenses: testLenses, Issue: openIssue()},
			watermark: &Watermark{Action: "implement", Revision: testRev},
		},
		{
			name:      "reassess already dispatched at the revision holds off",
			event:     issueEvent(EventIssueEdited),
			state:     State{ReviewLenses: testLenses, Issue: openIssue()},
			watermark: &Watermark{Action: "reassess", Revision: testRev},
		},
		{
			name:      "review already dispatched at the revision holds off",
			event:     prEvent(EventPROpened, "alice"),
			state:     State{ReviewLenses: testLenses, PR: openPR("abc")},
			watermark: lensWatermark("abc", "carol"),
		},
		{
			name:      "lens review already dispatched at the revision holds off",
			event:     prEvent(EventPRSynced, "alice"),
			state:     State{ReviewLenses: testLenses, PR: openPR("abc"), Reviews: []Review{review("COMMENT", "carol", testTime)}},
			watermark: lensWatermark("abc", "carol"),
		},
		{
			name:      "fix-feedback already dispatched at the revision holds off",
			event:     prEvent(EventPRReviewed, "carol"),
			state:     State{ReviewLenses: testLenses, PR: openPR("abc"), Reviews: []Review{review(ReviewChangesRequested, "carol", testTime)}},
			watermark: &Watermark{Action: "fix-feedback", Revision: "abc"},
		},
		{
			name:      "merge-or-wait already dispatched at the revision holds off",
			event:     prEvent(EventPRReviewed, "carol"),
			state:     State{ReviewLenses: testLenses, PR: openPR("abc"), Reviews: []Review{review("APPROVED", "carol", testTime)}},
			watermark: &Watermark{Action: "merge-or-wait", Revision: "abc"},
		},
		{
			name:       "reconcile PR fix-feedback already dispatched at the revision re-fires: the state at head is the watermark",
			event:      prEvent(EventReconcile, "maitred"),
			state:      State{ReviewLenses: testLenses, PR: openPR("abc"), Reviews: []Review{review(ReviewChangesRequested, "carol", testTime)}},
			watermark:  &Watermark{Action: "fix-feedback", Revision: "abc"},
			wantAction: ActionFixFeedback,
		},
		{
			name:      "reconcile PR merge-or-wait already dispatched at the revision holds off",
			event:     prEvent(EventReconcile, "maitred"),
			state:     State{ReviewLenses: testLenses, PR: openPR("abc"), Reviews: []Review{review("APPROVED", "carol", testTime)}},
			watermark: &Watermark{Action: "merge-or-wait", Revision: "abc"},
		},
		{
			name:       "reconcile PR standing at a comment review, fix-feedback already dispatched at the revision, re-fires",
			event:      prEvent(EventReconcile, "maitred"),
			state:      State{ReviewLenses: testLenses, PR: openPR("abc"), Reviews: []Review{review("COMMENT", "carol", testTime)}},
			watermark:  &Watermark{Action: "fix-feedback", Revision: "abc"},
			wantAction: ActionFixFeedback,
		},
		{
			name:       "PR commented decides as reconcile: approved at head dispatches merge-or-wait",
			event:      prEvent(EventPRCommented, "alice"),
			state:      State{ReviewLenses: testLenses, PR: optedIntoMerge(openPR("abc")), Reviews: []Review{review("APPROVED", "carol", testTime)}},
			wantAction: ActionMergeOrWait,
		},
		{
			name:       "PR commented decides as reconcile: changes-requested at head dispatches fix-feedback",
			event:      prEvent(EventPRCommented, "alice"),
			state:      State{ReviewLenses: testLenses, PR: openPR("abc"), Reviews: []Review{review(ReviewChangesRequested, "carol", testTime)}},
			wantAction: ActionFixFeedback,
		},
		{
			name:       "PR commented decides as reconcile: comment review at head dispatches fix-feedback",
			event:      prEvent(EventPRCommented, "alice"),
			state:      State{ReviewLenses: testLenses, PR: openPR("abc"), Reviews: []Review{review("COMMENT", "carol", testTime)}},
			wantAction: ActionFixFeedback,
		},
		{
			name:       "PR commented decides as reconcile: no review dispatches review",
			event:      prEvent(EventPRCommented, "alice"),
			state:      State{ReviewLenses: testLenses, PR: openPR("abc")},
			wantAction: ActionReview,
		},
		{
			name:      "PR commented, merge-or-wait already dispatched at the revision, holds off",
			event:     prEvent(EventPRCommented, "alice"),
			state:     State{ReviewLenses: testLenses, PR: openPR("abc"), Reviews: []Review{review("APPROVED", "carol", testTime)}},
			watermark: &Watermark{Action: "merge-or-wait", Revision: "abc"},
		},
		{
			name:  "PR commented but closed on re-fetch holds off",
			event: prEvent(EventPRCommented, "alice"),
			state: State{ReviewLenses: testLenses, PR: closedPR("abc")},
		},
		{
			name:  "commented event on an issue kind holds off",
			event: issueEvent(EventPRCommented),
			state: State{ReviewLenses: testLenses, Issue: openIssue()},
		},
		{
			name:      "rebase already dispatched at the revision holds off",
			event:     prEvent(EventPRSynced, "alice"),
			state:     State{ReviewLenses: testLenses, PR: notMergeablePR("abc")},
			watermark: &Watermark{Action: "rebase", Revision: "abc"},
		},
		{
			name:       "a different revision dispatches",
			event:      issueEvent(EventIssueOpened),
			state:      State{ReviewLenses: testLenses, Issue: openIssue()},
			watermark:  &Watermark{Action: "implement", Revision: testRev2},
			wantAction: ActionImplement,
		},
		{
			name:       "a different action dispatches",
			event:      issueEvent(EventIssueOpened),
			state:      State{ReviewLenses: testLenses, Issue: openIssue()},
			watermark:  &Watermark{Action: "reassess", Revision: testRev},
			wantAction: ActionImplement,
		},
		{
			name:       "a review watermark does not block the fan-out at a new revision",
			event:      prEvent(EventPRSynced, "alice"),
			state:      State{ReviewLenses: testLenses, PR: openPR("abc"), Reviews: []Review{review("COMMENT", "carol", testTime)}},
			watermark:  &Watermark{Action: "review", Revision: "abc"},
			wantAction: ActionReview,
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
			state:      State{ReviewLenses: testLenses, Issue: trackerIssue()},
			wantAction: ActionWrapUp,
		},
		{
			name:       "reconcile re-fires wrap-up for a tracker already dispatched at an unchanged revision",
			event:      issueEvent(EventReconcile),
			state:      State{ReviewLenses: testLenses, Issue: trackerIssue()},
			watermark:  &Watermark{Action: "wrap-up", Revision: testRev},
			wantAction: ActionWrapUp,
		},
		{
			name:  "reconcile outcome tracker with an open blocker and no ideation blocker holds off",
			event: issueEvent(EventReconcile),
			state: State{ReviewLenses: testLenses, Issue: trackerIssue(), Blockers: []Issue{{Number: 3, State: "open", Repository: "o/r"}}},
		},
		// A commissioned forest stays under curation: closing the ideation issue
		// removes the root from the sweep's enumeration, so the tracker's own
		// row carries the curation until the forest is finished
		// (§forgejo/reconciliation/watermark-exemptions).
		{
			name:  "reconcile commissioned unfinished tracker dispatches decompose",
			event: issueEvent(EventReconcile),
			state: State{ReviewLenses: testLenses, Issue: trackerIssue(), Blockers: []Issue{
				{Number: 3, State: "closed", Repository: "o/r", Labels: []string{LabelIdeation}},
				{Number: 8, State: "open", Repository: "o/r"},
			}},
			wantAction: ActionDecompose,
		},
		{
			name:  "reconcile re-fires decompose for a commissioned tracker already dispatched at an unchanged revision",
			event: issueEvent(EventReconcile),
			state: State{ReviewLenses: testLenses, Issue: trackerIssue(), Blockers: []Issue{
				{Number: 3, State: "closed", Repository: "o/r", Labels: []string{LabelIdeation}},
				{Number: 8, State: "open", Repository: "o/r"},
			}},
			watermark:  &Watermark{Action: "decompose", Revision: testRev},
			wantAction: ActionDecompose,
		},
		{
			name:  "reconcile not-yet-commissioned tracker holds off naming its open root",
			event: issueEvent(EventReconcile),
			state: State{ReviewLenses: testLenses, Issue: trackerIssue(), Blockers: []Issue{
				{Number: 3, State: "open", Repository: "o/r", Labels: []string{LabelIdeation}},
				{Number: 8, State: "open", Repository: "o/r"},
			}},
		},
		{
			name:  "reconcile fully unblocked tracker whose commissioning edge is closed dispatches wrap-up",
			event: issueEvent(EventReconcile),
			state: State{ReviewLenses: testLenses, Issue: trackerIssue(), Blockers: []Issue{
				{Number: 3, State: "closed", Repository: "o/r", Labels: []string{LabelIdeation}},
				{Number: 8, State: "closed", Repository: "o/r"},
			}},
			wantAction: ActionWrapUp,
		},
		{
			name:  "reconcile outcome tracker closed on re-fetch holds off",
			event: issueEvent(EventReconcile),
			state: State{ReviewLenses: testLenses, Issue: func() *Issue { i := trackerIssue(); i.State = "closed"; return i }()},
		},
		{
			name:       "reconcile open ideation issue dispatches decompose",
			event:      issueEvent(EventReconcile),
			state:      State{ReviewLenses: testLenses, Issue: ideationIssue()},
			wantAction: ActionDecompose,
		},
		{
			name:       "reconcile re-fires decompose for an ideation issue already dispatched at an unchanged revision",
			event:      issueEvent(EventReconcile),
			state:      State{ReviewLenses: testLenses, Issue: ideationIssue()},
			watermark:  &Watermark{Action: "decompose", Revision: testRev},
			wantAction: ActionDecompose,
		},
		{
			name:  "reconcile ideation issue closed on re-fetch holds off",
			event: issueEvent(EventReconcile),
			state: State{ReviewLenses: testLenses, Issue: func() *Issue { i := ideationIssue(); i.State = "closed"; return i }()},
		},
		{
			name:       "an ideation label takes precedence over an outcome label",
			event:      issueEvent(EventReconcile),
			state:      State{ReviewLenses: testLenses, Issue: &Issue{Number: 7, State: "open", UpdatedAt: testTime, Repository: "o/r", Labels: []string{LabelOutcome, LabelIdeation}}},
			wantAction: ActionDecompose,
		},
		{
			name:      "reconcile unlabelled issue implement already dispatched at the revision holds off",
			event:     issueEvent(EventReconcile),
			state:     State{ReviewLenses: testLenses, Issue: openIssue()},
			watermark: &Watermark{Action: "implement", Revision: testRev},
		},
		// The event path dispatches `decompose` for an open ideation
		// root — the label, not the repository and not the title, is
		// what makes a root — but never implements or reassesses an
		// issue carrying a reserved label
		// (§forgejo/decisions/transition-table).
		{
			name:       "issue opened carrying the ideation label dispatches decompose",
			event:      issueEvent(EventIssueOpened),
			state:      State{ReviewLenses: testLenses, Issue: ideationIssue()},
			wantAction: ActionDecompose,
		},
		{
			name:       "labels changed on an issue carrying the ideation label dispatches decompose",
			event:      issueEvent(EventIssueLabelsChanged),
			state:      State{ReviewLenses: testLenses, Issue: ideationIssue()},
			wantAction: ActionDecompose,
		},
		{
			// A comment authored by the decomposer itself on a root
			// holds off on authorship (s-git/maitred#147).
			name: "feedback comment on an issue carrying the ideation label by the decomposer holds off",
			event: Event{
				Type:   EventIssueCommented,
				Repo:   "o/r",
				Kind:   KindIssue,
				Number: 7,
				Sender: "decomposer",
			},
			state: State{ReviewLenses: testLenses, Issue: ideationIssue(), DecomposerIdentity: "decomposer"},
		},
		{
			name:  "decompose already dispatched at the revision holds off on the event path",
			event: issueEvent(EventIssueOpened),
			state: State{ReviewLenses: testLenses, Issue: ideationIssue()},
			watermark: &Watermark{
				Action:   string(ActionDecompose),
				Revision: testRev,
			},
		},
		{
			name:  "decompose already dispatched at an older revision re-fires on an edit at a new revision",
			event: issueEvent(EventIssueEdited),
			state: State{ReviewLenses: testLenses, Issue: func() *Issue {
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
			state:      State{ReviewLenses: testLenses, Issue: ideationIssue(), Blockers: []Issue{{Number: 3, State: "open", Repository: "o/r"}}},
			wantAction: ActionDecompose,
		},
		{
			name:       "an ideation label takes precedence over human-task on the event path",
			event:      issueEvent(EventIssueLabelsChanged),
			state:      State{ReviewLenses: testLenses, Issue: labelledIssue(LabelHumanTask, LabelIdeation)},
			wantAction: ActionDecompose,
		},
		{
			name:       "an ideation label takes precedence over outcome on the event path",
			event:      issueEvent(EventIssueOpened),
			state:      State{ReviewLenses: testLenses, Issue: labelledIssue(LabelOutcome, LabelIdeation)},
			wantAction: ActionDecompose,
		},
		{
			name:  "an ideation root authored by the decomposer dispatches decompose",
			event: issueEvent(EventIssueOpened),
			state: State{ReviewLenses: testLenses, Issue: func() *Issue {
				i := ideationIssue()
				i.Author = "decomposer"
				return i
			}()},
			wantAction: ActionDecompose,
		},
		{
			name:       "issue opened carrying a differently cased ideation label dispatches decompose",
			event:      issueEvent(EventIssueOpened),
			state:      State{ReviewLenses: testLenses, Issue: &Issue{Number: 7, State: "open", UpdatedAt: testTime, Repository: "o/r", Labels: []string{"Ideation"}}},
			wantAction: ActionDecompose,
		},
		{
			name:  "ideation issue closed on re-fetch holds off",
			event: issueEvent(EventIssueOpened),
			state: State{ReviewLenses: testLenses, Issue: func() *Issue {
				i := ideationIssue()
				i.State = "closed"
				return i
			}()},
		},
		{
			name:  "edited ideation issue closed on re-fetch holds off",
			event: issueEvent(EventIssueEdited),
			state: State{ReviewLenses: testLenses, Issue: func() *Issue {
				i := ideationIssue()
				i.State = "closed"
				return i
			}()},
		},
		{
			name:  "feedback comment on an issue carrying the outcome label holds off",
			event: issueEvent(EventIssueCommented),
			state: State{ReviewLenses: testLenses, Issue: trackerIssue()},
		},
		{
			name:  "issue opened carrying the outcome label holds off",
			event: issueEvent(EventIssueOpened),
			state: State{ReviewLenses: testLenses, Issue: trackerIssue()},
		},
		{
			// Reserved label names match case-insensitively: label
			// names are user-authored strings, and a miss must not
			// silently decide as implement
			// (§forgejo/reconciliation/watermark-exemptions).
			name:       "reconcile open issue carrying a differently cased ideation label dispatches decompose",
			event:      issueEvent(EventReconcile),
			state:      State{ReviewLenses: testLenses, Issue: &Issue{Number: 7, State: "open", UpdatedAt: testTime, Repository: "o/r", Labels: []string{"Ideation"}}},
			wantAction: ActionDecompose,
		},
		{
			name:       "reconcile open tracker carrying an upper-cased outcome label dispatches wrap-up",
			event:      issueEvent(EventReconcile),
			state:      State{ReviewLenses: testLenses, Issue: &Issue{Number: 7, State: "open", UpdatedAt: testTime, Repository: "o/r", Labels: []string{"OUTCOME"}}},
			wantAction: ActionWrapUp,
		},
		{
			name:  "issue opened carrying a differently cased reserved label holds off",
			event: issueEvent(EventIssueOpened),
			state: State{ReviewLenses: testLenses, Issue: &Issue{Number: 7, State: "open", UpdatedAt: testTime, Repository: "o/r", Labels: []string{"Outcome"}}},
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

// TestDecide_FixFeedbackOwnershipHoldOffNamesAuthor pins the
// acceptance that a fix-feedback transition on a PR whose author is
// not the configured implementer identity holds off with a reason
// naming both the PR's author and the identity
// (§forgejo/decisions/loop-prevention, s-git/maitred#143).
func TestDecide_FixFeedbackOwnershipHoldOffNamesAuthor(t *testing.T) {
	cases := []struct {
		name  string
		event Event
		state State
	}{
		{
			name:  "review submitted, changes requested",
			event: prEvent(EventPRReviewed, "carol"),
			state: State{ReviewLenses: testLenses, PR: openPRBy("skrewz-bot", "abc"), Reviews: []Review{review(ReviewChangesRequested, "carol", testTime)}, ImplementerIdentity: testImplementerIdentity},
		},
		{
			name:  "reconcile standing at a changes-requested review",
			event: prEvent(EventReconcile, "maitred"),
			state: State{ReviewLenses: testLenses, PR: openPRBy("skrewz-bot", "abc"), Reviews: []Review{review(ReviewChangesRequested, "carol", testTime)}, ImplementerIdentity: testImplementerIdentity},
		},
		{
			name:  "reconcile standing at a comment review",
			event: prEvent(EventReconcile, "maitred"),
			state: State{ReviewLenses: testLenses, PR: openPRBy("skrewz-bot", "abc"), Reviews: []Review{review("COMMENT", "carol", testTime)}, ImplementerIdentity: testImplementerIdentity},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := Decide(tc.event, tc.state, nil)
			if !d.HoldOff() {
				t.Fatalf("HoldOff() = false with action %q, want hold-off (reason: %s)", d.Action, d.Reason)
			}
			for _, want := range []string{"skrewz-bot", testImplementerIdentity} {
				if !strings.Contains(d.Reason, want) {
					t.Errorf("reason %q does not name %q", d.Reason, want)
				}
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
		ReviewLenses: testLenses,
		Issue:        openIssue(),
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
		ReviewLenses: testLenses,
		PR:           openPR("abc"),
		Reviews:      []Review{review("APPROVED", "carol", testTime)},
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
		d := Decide(issueEvent(EventReconcile), State{ReviewLenses: testLenses, Issue: openIssue()}, &Watermark{Action: "implement", Revision: testRev})
		if !d.HoldOff() {
			t.Fatalf("expected hold-off, got dispatch of %q", d.Action)
		}
		if !strings.Contains(d.Reason, "already dispatched at revision") {
			t.Errorf("reason = %q, want it to name the already-dispatched revision", d.Reason)
		}
	})
	t.Run("a watermark-exempt hold-off names the state, not the revision", func(t *testing.T) {
		d := Decide(issueEvent(EventReconcile), State{ReviewLenses: testLenses, Issue: trackerIssue(), Blockers: []Issue{{Number: 3, State: "open", Repository: "o/r"}}}, &Watermark{Action: "wrap-up", Revision: testRev})
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
		d := Decide(issueEvent(EventIssueOpened), State{ReviewLenses: testLenses, Issue: trackerIssue()}, nil)
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
			name:       "implement is denied to a newborn forest member carrying forest-seeding",
			action:     ActionImplement,
			issue:      &Issue{Number: 2, State: "open", Repository: "o/r", Labels: []string{LabelForestSeeding}},
			wantReason: "issue 2 carries forest-seeding; forest is seeding, not yet gated",
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

// optedIntoMerge adds the agentic-auto-merge opt-in to a PR fixture:
// the precondition every path that dispatches merge-or-wait now
// reads (§forgejo/decisions/merge-gate, s-git/maitred#179).
func optedIntoMerge(pr *PullRequest) *PullRequest {
	pr.Labels = append(pr.Labels, LabelAgenticAutoMerge)
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
			// A set stalled on a member awaiting a human is not ready
			// whatever its deliveries: the graph says so without a set
			// read, and a candidate that can only hold off costs the
			// whole set fetch for nothing
			// (§forgejo/decisions/sync-set).
			name: "a sync issue with an open member lacking the member label emits no candidate",
			root: &IssueNode{
				Issue: Issue{Number: 1, State: "closed", Repository: "o/r"},
				Blocks: []IssueNode{
					{
						Issue:    Issue{Number: 11, State: "open", Repository: "o/r", Labels: []string{LabelSync}},
						Blockers: []IssueNode{node("o/r", 1, "closed"), node("o/r", 3, "open")},
					},
				},
			},
		},
		{
			name: "a sync issue whose open members are all opted in emits the candidate",
			root: &IssueNode{
				Issue: Issue{Number: 1, State: "closed", Repository: "o/r"},
				Blocks: []IssueNode{
					{
						Issue:    Issue{Number: 11, State: "open", Repository: "o/r", Labels: []string{LabelSync}},
						Blockers: []IssueNode{node("o/r", 1, "closed"), {Issue: Issue{Number: 3, State: "open", Repository: "o/r", Labels: []string{LabelAgenticSyncMerge}}}},
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
			state:      State{ReviewLenses: testLenses, Issue: labelledIssue(LabelHumanTask)},
			wantReason: "never an implement candidate",
		},
		{
			name:       "activity: reassess denied by the label",
			event:      issueEvent(EventIssueCommented),
			state:      State{ReviewLenses: testLenses, Issue: labelledIssue(LabelOutcome)},
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

// TestDecide_ForestSeedingHoldOff pins the seeding-window hold-off: an
// issue carrying the transient forest-seeding label holds off on every
// path — opened, activity, and the sweep — with a reason naming the
// seeding window rather than a never- claim, and the kind and claim
// labels are still named first when carried alongside
// (§forgejo/decisions/transition-table, s-git/maitred#125).
func TestDecide_ForestSeedingHoldOff(t *testing.T) {
	tests := []struct {
		name       string
		event      Event
		state      State
		wantReason string
	}{
		{
			name:       "opened: implement denied while seeding",
			event:      issueEvent(EventIssueOpened),
			state:      State{ReviewLenses: testLenses, Issue: labelledIssue(LabelForestSeeding)},
			wantReason: "issue 7 carries forest-seeding; forest is seeding, not yet gated",
		},
		{
			name:       "activity: reassess denied while seeding",
			event:      issueEvent(EventIssueLabelsChanged),
			state:      State{ReviewLenses: testLenses, Issue: labelledIssue(LabelForestSeeding)},
			wantReason: "issue 7 carries forest-seeding; forest is seeding, not yet gated",
		},
		{
			name:       "sweep: the reconcile fall-through holds off too",
			event:      issueEvent(EventReconcile),
			state:      State{ReviewLenses: testLenses, Issue: labelledIssue(LabelForestSeeding)},
			wantReason: "issue 7 carries forest-seeding; forest is seeding, not yet gated",
		},
		{
			// The seeding label matches case-insensitively, as every
			// reserved name does.
			name:       "a label created as Forest-Seeding holds off too",
			event:      issueEvent(EventIssueOpened),
			state:      State{ReviewLenses: testLenses, Issue: labelledIssue("Forest-Seeding")},
			wantReason: "issue 7 carries forest-seeding; forest is seeding, not yet gated",
		},
		{
			// A transient hold-off does not outrank what an issue *is*:
			// the kind or claim label is named first.
			name:       "outcome carried alongside forest-seeding is named first",
			event:      issueEvent(EventIssueCommented),
			state:      State{ReviewLenses: testLenses, Issue: labelledIssue(LabelForestSeeding, LabelOutcome)},
			wantReason: "issue 7 carries the outcome label; never a reassess candidate",
		},
		{
			name:       "human-task carried alongside forest-seeding is named first",
			event:      issueEvent(EventIssueOpened),
			state:      State{ReviewLenses: testLenses, Issue: labelledIssue(LabelForestSeeding, LabelHumanTask)},
			wantReason: "issue 7 carries the human-task label; never an implement candidate",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := Decide(tt.event, tt.state, nil)
			if !d.HoldOff() {
				t.Fatalf("decision = %+v, want a hold-off", d)
			}
			if d.Reason != tt.wantReason {
				t.Errorf("hold-off reason = %q, want %q", d.Reason, tt.wantReason)
			}
		})
	}
}

// TestDecide_DecomposerAuthoredMemberIsAnOrdinaryWorkItem pins the
// retired authorship guard: on the event path and on the sweep, an
// open, unblocked member the decomposer authored — seed-less, no
// connected PR — dispatches implement; the sweep is the cascade's
// backstop (s-git/maitred#135, §forgejo/decisions/transition-table).
func TestDecide_DecomposerAuthoredMemberIsAnOrdinaryWorkItem(t *testing.T) {
	tests := []struct {
		name  string
		event Event
	}{
		{name: "issue opened", event: issueEvent(EventIssueOpened)},
		{name: "reconcile — the sweep's synthetic current-state event", event: issueEvent(EventReconcile)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := Decide(tt.event, State{ReviewLenses: testLenses, Issue: authoredIssue("decomposer")}, nil)
			if !d.Dispatched || d.Action != ActionImplement {
				t.Errorf("decision = %+v, want a dispatch of %v", d, ActionImplement)
			}
		})
	}
}

// TestDecide_CascadeUnblocksDecomposerAuthoredMember pins that
// seeding, not authorship, gates a member: the ideation ticket closing
// unblocks the member the decomposer authored exactly as before
// (§forgejo/decisions/transition-table, §forgejo/decisions/unblock-cascade).
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
	d := Decide(issueEvent(EventIssueClosed), State{ReviewLenses: testLenses, Issue: closedIssue(), CascadeRoot: root}, nil)
	want := []CascadeDispatch{{Key: Key{Repo: "o/r", Kind: KindIssue, Number: 7}, Action: ActionImplement}}
	if !reflect.DeepEqual(d.Cascade, want) {
		t.Errorf("cascade = %v, want %v (the cascade is not narrowed by authorship)", d.Cascade, want)
	}
}

// TestDecide_IdeationRowsReasons pins the reasons of the ideation event
// rows: an opened, edited, re-labelled root — and a root a non-decomposer
// commented on — dispatches decompose naming the root
// (§forgejo/decisions/transition-table, s-git/maitred#147).
func TestDecide_IdeationRowsReasons(t *testing.T) {
	for _, typ := range []EventType{EventIssueOpened, EventIssueEdited, EventIssueLabelsChanged, EventIssueCommented} {
		d := Decide(issueEvent(typ), State{ReviewLenses: testLenses, Issue: ideationIssue()}, nil)
		if d.HoldOff() || d.Action != ActionDecompose {
			t.Fatalf("%s: decision = %+v, want a dispatch of %q", typ, d, ActionDecompose)
		}
		if !strings.Contains(d.Reason, LabelIdeation) {
			t.Errorf("%s: reason = %q, want it to name the ideation label", typ, d.Reason)
		}
	}
}

// TestDecide_IdeationCommentAuthorGuard pins the author-scoped comment
// row at an open ideation root: a comment authored by anyone other than
// the configured decomposer identity dispatches decompose under the
// ordinary revision-keyed watermark guard, while the decomposer's own
// comment holds off naming the authorship — the guard that keeps the
// decomposer's clarifying question from re-triggering itself. An unset
// identity leaves the guard inert, the same idiom as the fix-feedback
// ownership gate (§forgejo/decisions/loop-prevention,
// §forgejo/config/decomposer-identity, s-git/maitred#147).
func TestDecide_IdeationCommentAuthorGuard(t *testing.T) {
	root := ideationIssue()
	st := State{ReviewLenses: testLenses, Issue: root, DecomposerIdentity: "decomposer"}
	byDecomposer := func(issue *Issue) Event {
		return Event{Type: EventIssueCommented, Repo: "o/r", Kind: KindIssue, Number: issue.Number, Sender: "decomposer"}
	}

	t.Run("a comment by any other author dispatches decompose", func(t *testing.T) {
		d := Decide(issueEvent(EventIssueCommented), st, nil)
		if d.HoldOff() || d.Action != ActionDecompose {
			t.Fatalf("decision = %+v, want a dispatch of %q", d, ActionDecompose)
		}
		// The author guard is this row's whole loop prevention, so the
		// dispatch reason names the comment's author: a stream of
		// decompose dispatches authored by the account the operator
		// meant to declare — but did not, so the guard fails open — is
		// legible in the decision log
		// (§forgejo/config/decomposer-identity).
		if !strings.Contains(d.Reason, "alice") {
			t.Errorf("reason = %q, want it to name the comment's author", d.Reason)
		}
	})

	t.Run("the decomposer's own comment holds off naming the authorship", func(t *testing.T) {
		d := Decide(byDecomposer(root), st, nil)
		if !d.HoldOff() {
			t.Fatalf("dispatched %q, want a hold-off", d.Action)
		}
		if !strings.Contains(d.Reason, "decomposer") {
			t.Errorf("reason = %q, want it to name the decomposer identity as the comment's author", d.Reason)
		}
	})

	t.Run("the human's comment keeps the revision-keyed watermark guard", func(t *testing.T) {
		w := &Watermark{Action: string(ActionDecompose), Revision: issueRevision(root)}
		d := Decide(issueEvent(EventIssueCommented), st, w)
		if !d.HoldOff() || !strings.Contains(d.Reason, "already dispatched") {
			t.Fatalf("decision = %+v, want the revision-keyed hold-off", d)
		}
	})

	t.Run("an unset identity leaves the guard inert", func(t *testing.T) {
		d := Decide(byDecomposer(root), State{ReviewLenses: testLenses, Issue: root}, nil)
		if d.HoldOff() || d.Action != ActionDecompose {
			t.Fatalf("decision = %+v, want a dispatch of %q with no identity configured", d, ActionDecompose)
		}
	})

	t.Run("a comment by the decomposer on a root under teardown also holds off", func(t *testing.T) {
		d := Decide(byDecomposer(teardownRoot()), State{ReviewLenses: testLenses, Issue: teardownRoot(), DecomposerIdentity: "decomposer"}, nil)
		if !d.HoldOff() {
			t.Fatalf("dispatched %q, want the authorship hold-off even under a teardown order", d.Action)
		}
	})
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
		{ActionRebase, workItem, true},
		{ActionFixFeedback, workItem, true},
		{ActionMergeOrWait, workItem, true},
		{ActionImplement, outcomes, false},
		{ActionReassess, outcomes, false},
		{ActionReview, outcomes, false},
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
		d := Decide(e, State{ReviewLenses: testLenses, Issue: teardownRoot(), Blocked: standingForest()}, nil)
		if d.Action != ActionTeardownForest {
			t.Errorf("%s on a root carrying the label: action = %q, want %q (%s)",
				typ, d.Action, ActionTeardownForest, d.Reason)
		}
	}

	// The author-guarded comment row falls through to the root rows
	// like an edit, so a human comment on a root under a teardown
	// order takes the teardown row rather than holding off: the order
	// is the label, not the event, and the row wins over the root's
	// decompose row exactly as it does for an edit
	// (§forgejo/decisions/forest-teardown, s-git/maitred#147).
	t.Run("a human comment on a root under teardown takes the teardown row, as an edit does", func(t *testing.T) {
		d := Decide(issueEvent(EventIssueCommented), State{ReviewLenses: testLenses, Issue: teardownRoot(), Blocked: standingForest(), DecomposerIdentity: "decomposer"}, nil)
		if d.Action != ActionTeardownForest {
			t.Errorf("action = %q, want %q (%s)", d.Action, ActionTeardownForest, d.Reason)
		}
	})

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
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := Decide(tc.event, State{ReviewLenses: testLenses, Issue: tc.issue}, nil)
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
// on what the sync predicate reads about every member — state, member
// label, PR open and mergeable, approval at head — and never on the
// order the members are enumerated in, so the sweep does not re-fire at
// an unchanged set state, any member's readiness moving re-fires, and
// an edit that cannot move readiness does not
// (§forgejo/decisions/sync-set, §forgejo/state/watermark).
func TestSyncSetRevision(t *testing.T) {
	setIssue := syncIssueFixture(7)
	members := []SyncMember{syncMemberFixture(4, 40), closedSyncMemberFixture(5)}
	reversed := []SyncMember{closedSyncMemberFixture(5), syncMemberFixture(4, 40)}
	if syncSetRevision(setIssue, members, testLenses) != syncSetRevision(setIssue, reversed, testLenses) {
		t.Error("set revision must not depend on member order")
	}

	pushed := []SyncMember{syncMemberFixture(4, 40), closedSyncMemberFixture(5)}
	pushed[0].PR.HeadSHA = "sha-new"
	if syncSetRevision(setIssue, members, testLenses) == syncSetRevision(setIssue, pushed, testLenses) {
		t.Error("a member PR head moving must change the set revision")
	}

	edited := []SyncMember{syncMemberFixture(4, 40), closedSyncMemberFixture(5)}
	edited[1].Issue.UpdatedAt = testTime2
	if syncSetRevision(setIssue, members, testLenses) != syncSetRevision(setIssue, edited, testLenses) {
		t.Error("an edit that cannot move readiness must not change the set revision: the digest records what the predicate reads, not the members' activity")
	}

	unlabelled := []SyncMember{syncMemberFixture(4, 40), closedSyncMemberFixture(5)}
	unlabelled[0].Issue.Labels = nil
	if syncSetRevision(setIssue, members, testLenses) == syncSetRevision(setIssue, unlabelled, testLenses) {
		t.Error("a member losing the member label must change the set revision")
	}

	staleApproval := []SyncMember{syncMemberFixture(4, 40), closedSyncMemberFixture(5)}
	staleApproval[0].Reviews = []Review{{Event: ReviewApproved, Author: "carol", CommitID: "sha-before-the-push", SubmittedAt: testTime}}
	if syncSetRevision(setIssue, members, testLenses) == syncSetRevision(setIssue, staleApproval, testLenses) {
		t.Error("a member PR no longer approved at head must change the set revision")
	}

	conflicted := []SyncMember{syncMemberFixture(4, 40), closedSyncMemberFixture(5)}
	conflicted[0].PR.Mergeable = false
	if syncSetRevision(setIssue, members, testLenses) == syncSetRevision(setIssue, conflicted, testLenses) {
		t.Error("a member PR that stopped being mergeable must change the set revision")
	}

	if syncSetRevision(setIssue, nil, testLenses) == syncSetRevision(setIssue, members, testLenses) {
		t.Error("an empty set must not share a revision with a populated one")
	}

	// The grouping issue's **body** is in the digest: a ready set whose
	// landing run reported an error is re-dispatched when the body — the
	// order the prompt must honour — is restated, which is the human's
	// only lever on such a report
	// (§forgejo/decisions/sync-set, §forgejo/reconciliation/watermark-exemptions).
	restated := syncIssueFixture(7)
	restated.Body = "Land 4, then 5."
	if syncSetRevision(setIssue, members, testLenses) == syncSetRevision(restated, members, testLenses) {
		t.Error("restating the grouping issue's body must change the set revision: a ready set that never landed must not be a fixed point")
	}
	// The grouping issue's activity is not: its updated_at moves on a
	// comment, a title fix, or an unrelated label toggle, and the
	// members were narrowed off exactly that signal — the lever is the
	// restatement, not the touch (§forgejo/decisions/sync-set).
	touched := syncIssueFixture(7)
	touched.UpdatedAt = testTime2
	if syncSetRevision(setIssue, members, testLenses) != syncSetRevision(touched, members, testLenses) {
		t.Error("a touch of the grouping issue that restates nothing must not change the set revision: the landing prompt would re-run on activity")
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
				ReviewLenses:    testLenses,
				PR:              openPR("abc"),
				Reviews:         []Review{review("APPROVED", "carol", testTime)},
				ConnectedIssues: []ConnectedIssue{syncMemberConnected(7, 11)},
			},
		},
		{
			name:  "sweep PR path standing at an approved head",
			event: prEvent(EventReconcile, "maitred"),
			state: State{
				ReviewLenses:    testLenses,
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

// TestDecide_SyncSetApprovalHoldOffDirectPRMember pins the approval
// hold-off on the shape whose predicate side reads the row: a pull
// request blocking a grouping issue directly is a member whose delivery
// is itself, so its approval never merges it — the set's decision lands
// it, and the set is emitted from the hold-off whatever path the
// approval arrived on. The connected issue carries `sync`, not the
// member label, so this is the one membership the approval side cannot
// read off a connected issue's labels (§forgejo/decisions/sync-set).
func TestDecide_SyncSetApprovalHoldOffDirectPRMember(t *testing.T) {
	for _, tc := range []struct {
		name  string
		event Event
		state State
	}{
		{
			name:  "review event",
			event: prEvent(EventPRReviewed, "carol"),
			state: directPRMemberState(openPR("abc"), 11),
		},
		{
			name:  "sweep PR path standing at an approved head",
			event: prEvent(EventReconcile, "maitred"),
			state: directPRMemberState(openPR("abc"), 11),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := Decide(tc.event, tc.state, nil)
			if d.Action != "" || !d.HoldOff() {
				t.Fatalf("decision = %+v, want a hold-off dispatching nothing", d)
			}
			if !strings.Contains(d.Reason, "sync set") || !strings.Contains(d.Reason, "o/outcomes#11") {
				t.Errorf("hold-off reason = %q, want it to name the sync set and the grouping issue o/outcomes#11", d.Reason)
			}
			want := CascadeDispatch{
				Key:    Key{Repo: "o/outcomes", Kind: KindIssue, Number: 11},
				Action: ActionSync,
			}
			if len(d.Cascade) != 1 || d.Cascade[0] != want {
				t.Errorf("cascade = %+v, want the set emitted: %+v", d.Cascade, want)
			}
		})
	}
}

// TestDecide_SyncSetApprovalHoldOffDirectPRMemberDualLabel: a direct PR
// member carrying both merge opt-ins is the same authoring error the
// member issue's dual label is, and the PR's own labels are the only
// place it can be named from on this path — the connected issue carries
// the kind label alone (§forgejo/decisions/sync-set).
func TestDecide_SyncSetApprovalHoldOffDirectPRMemberDualLabel(t *testing.T) {
	pr := openPR("abc")
	pr.Labels = []string{LabelAgenticSyncMerge, LabelAgenticAutoMerge}
	d := Decide(prEvent(EventPRReviewed, "carol"), directPRMemberState(pr, 11), nil)
	if d.Action != "" || !d.HoldOff() {
		t.Fatalf("decision = %+v, want a hold-off dispatching nothing", d)
	}
	if !strings.Contains(d.Reason, LabelAgenticSyncMerge) || !strings.Contains(d.Reason, LabelAgenticAutoMerge) {
		t.Errorf("hold-off reason = %q, want both opt-ins named", d.Reason)
	}
}

// TestDecide_SyncSetApprovalDirectMembershipNeedsThePRsOwnEdge pins the
// gate's condition: it is the PR's own entry in the grouping issue's
// blockers that makes it a member, not the presence of a sync issue
// among the connected issues. A grouping issue the PR does not block
// holds nothing over it, so the merge gate decides on
// (§forgejo/decisions/sync-set).
func TestDecide_SyncSetApprovalDirectMembershipNeedsThePRsOwnEdge(t *testing.T) {
	state := directPRMemberState(optedIntoMerge(openPR("abc")), 11)
	state.ConnectedIssues = []ConnectedIssue{directPRMemberConnected(11, false)}
	state.SyncSets = nil
	d := Decide(prEvent(EventPRReviewed, "carol"), state, nil)
	if d.Action != ActionMergeOrWait {
		t.Errorf("action = %q, want %q (%s)", d.Action, ActionMergeOrWait, d.Reason)
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
				State{ReviewLenses: testLenses, Issue: tr, Blockers: tc.blockers}, nil)
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
	d := Decide(issueEvent(EventIssueLabelsChanged), State{ReviewLenses: testLenses, Issue: root, Blocked: standingForest()}, nil)
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
	d := Decide(Event{Type: EventReconcile, Repo: "o/r", Kind: KindIssue, Number: root.Number}, State{ReviewLenses: testLenses, Issue: root, Blocked: standingForest()}, w)
	if d.Action != ActionTeardownForest {
		t.Errorf("action = %q, want %q (%s)", d.Action, ActionTeardownForest, d.Reason)
	}
	if strings.Contains(d.Reason, "already dispatched") {
		t.Errorf("reason = %q, want the state fact, not a revision guard", d.Reason)
	}
}

// TestDecide_TeardownLabelRemovalIsTheRootRowResuming pins the
// switch's other transition: an open ideation root that no longer
// carries teardown-forest is the root row, not a third state needing
// explanation. It dispatches decompose arriving as a labels-changed
// event and as a sweep row, the reason names the root, and a watermark
// already holding a teardown-forest key for the revision stops neither:
// the label states what to do now and carries no history
// (§forgejo/decisions/forest-teardown, §forgejo/decisions/transition-table).
func TestDecide_TeardownLabelRemovalIsTheRootRowResuming(t *testing.T) {
	root := ideationIssue()
	w := &Watermark{Action: string(ActionTeardownForest), Revision: issueRevision(root)}
	for _, tc := range []struct {
		name  string
		event Event
	}{
		{"a labels-changed event dropping the label is the root row", issueEvent(EventIssueLabelsChanged)},
		{"the sweep row resumes whatever the watermark holds", Event{Type: EventReconcile, Repo: "o/r", Kind: KindIssue, Number: root.Number}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := Decide(tc.event, State{ReviewLenses: testLenses, Issue: root, Blocked: standingForest()}, w)
			if d.Action != ActionDecompose {
				t.Errorf("action = %q, want %q (%s)", d.Action, ActionDecompose, d.Reason)
			}
			if !strings.Contains(d.Reason, fmt.Sprintf("%d", root.Number)) {
				t.Errorf("reason = %q, want the root named by number", d.Reason)
			}
		})
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
			d := Decide(issueEvent(EventIssueLabelsChanged), State{ReviewLenses: testLenses, Issue: root, Blocked: tc.blocked}, nil)
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
			State{ReviewLenses: testLenses, Issue: root, Blocked: nil}, nil)
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
			ReviewLenses:    testLenses,
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
			ReviewLenses:    testLenses,
			PR:              optedIntoMerge(openPR("abc")),
			Reviews:         []Review{review("APPROVED", "carol", testTime)},
			ConnectedIssues: []ConnectedIssue{syncMemberWithoutSets(7)},
		}, nil)
		if d.Action != ActionMergeOrWait {
			t.Errorf("decision = %+v, want merge-or-wait: a stale member label is no gate", d)
		}
	})
}

// TestDecide_SweepAutoMergeRowIsWatermarkExempt: the sweep's
// merge-or-wait row is watermark-exempt when the state calls for it —
// the PR is open, its latest review approves at head, no connected issue
// has an open blocker, and it carries the agentic-auto-merge
// label. The label is the opt-in and the precondition of the
// dispatch on every path: an approved PR without it waits on a
// human, its hold-off naming the missing label and the connected
// issues (§forgejo/reconciliation/watermark-exemptions,
// §forgejo/decisions/merge-gate, s-git/maitred#179).
func TestDecide_SweepAutoMergeRowIsWatermarkExempt(t *testing.T) {
	approvedAtHead := func(labels ...string) State {
		return State{
			ReviewLenses: testLenses,
			PR:           labelledPR("abc", labels...),
			Reviews:      []Review{review("APPROVED", "carol", testTime)},
		}
	}
	watermark := &Watermark{Action: string(ActionMergeOrWait), Revision: "abc"}

	t.Run("sweep re-fires the labelled PR at the recorded revision", func(t *testing.T) {
		d := Decide(prEvent(EventReconcile, ""), approvedAtHead(LabelAgenticAutoMerge), watermark)
		if d.Action != ActionMergeOrWait {
			t.Fatalf("decision = %+v, want merge-or-wait: an approved auto-merge PR at a frozen head is not a fixed point", d)
		}
		if strings.Contains(d.Reason, "already dispatched") {
			t.Errorf("reason = %q, want the state that calls for the action, not the watermark", d.Reason)
		}
		if !strings.Contains(d.Reason, "regardless of revision abc") {
			t.Errorf("reason = %q, want it to name the revision the state overrides", d.Reason)
		}
	})

	t.Run("sweep holds off a PR without the opt-in", func(t *testing.T) {
		d := Decide(prEvent(EventReconcile, ""), approvedAtHead(), watermark)
		if !d.HoldOff() {
			t.Fatalf("decision = %+v, want a hold-off: auto-merge is an opt-in", d)
		}
		if !strings.Contains(d.Reason, LabelAgenticAutoMerge) {
			t.Errorf("reason = %q, want it to name the missing opt-in, not merely the watermark", d.Reason)
		}
	})

	t.Run("sweep holds off the labelled PR with a connected open blocker", func(t *testing.T) {
		s := approvedAtHead(LabelAgenticAutoMerge)
		s.ConnectedIssues = []ConnectedIssue{connectedIssueOf(7, gateBlocker("o/r", 3, "open"))}
		d := Decide(prEvent(EventReconcile, ""), s, watermark)
		if !d.HoldOff() {
			t.Fatalf("decision = %+v, want a hold-off: the merge gate holds on every path", d)
		}
		if !strings.Contains(d.Reason, "o/r#3") {
			t.Errorf("reason = %q, want it to name the open blocker", d.Reason)
		}
		if strings.Contains(d.Reason, "already dispatched") {
			t.Errorf("reason = %q, want the state that calls for no action, not the watermark", d.Reason)
		}
	})

	t.Run("sweep holds off the labelled PR that is already merged", func(t *testing.T) {
		pr := labelledPR("abc", LabelAgenticAutoMerge)
		pr.State, pr.Merged = "closed", true
		d := Decide(prEvent(EventReconcile, ""), State{
			ReviewLenses: testLenses,
			PR:           pr,
			Reviews:      []Review{review("APPROVED", "carol", testTime)},
		}, watermark)
		if !d.HoldOff() {
			t.Fatalf("decision = %+v, want a hold-off: the re-fire ends where the PR does", d)
		}
		if !strings.Contains(d.Reason, "not open") {
			t.Errorf("reason = %q, want it to name the PR's state", d.Reason)
		}
	})

	t.Run("event path stays revision-keyed for the labelled PR", func(t *testing.T) {
		for _, ev := range []Event{prEvent(EventPRReviewed, "carol"), prEvent(EventPRCommented, "alice")} {
			d := Decide(ev, approvedAtHead(LabelAgenticAutoMerge), watermark)
			if !d.HoldOff() {
				t.Errorf("event %q: decision = %+v, want a hold-off: the exemption is the sweep's row alone", ev.Type, d)
			}
			if !strings.Contains(d.Reason, "already dispatched at revision abc") {
				t.Errorf("event %q: reason = %q, want the revision-keyed hold-off", ev.Type, d.Reason)
			}
		}
	})
}

// TestDecide_SweepReviewRowsAreWatermarkExempt: the sweep's rows for
// the lens fan-out and fix-feedback are watermark-exempt — the state
// is their watermark, so a dispatch that went nowhere recovers on the
// next sweep while the head stands still. Each exempt state is
// self-limiting: a lens reporting at head ends that lens' exemption, a push
// ends fix-feedback, and the sweep enumerates open pull requests only.
// The exemption is the sweep's rows alone: the same states reached
// from an event (pr_synced, pr_reviewed, pr_commented) keep the
// revision-keyed guard, so a push or a review never re-fires on its own
// (§forgejo/reconciliation/watermark-exemptions,
// §forgejo/decisions/loop-prevention).
func TestDecide_SweepReviewRowsAreWatermarkExempt(t *testing.T) {
	oldReview := func(event string) Review {
		return Review{Event: event, Author: "carol", CommitID: "old", SubmittedAt: testTime}
	}

	t.Run("sweep re-fires review for a PR with no reviews at the recorded revision", func(t *testing.T) {
		d := Decide(prEvent(EventReconcile, ""), State{ReviewLenses: testLenses, PR: openPR("abc")}, lensWatermark("abc", "carol"))
		if d.Action != ActionReview {
			t.Fatalf("decision = %+v, want review: the review the sweep asked for has still not happened", d)
		}
		if strings.Contains(d.Reason, "already dispatched") {
			t.Errorf("reason = %q, want the state that calls for the action, not the watermark", d.Reason)
		}
		if !strings.Contains(d.Reason, "regardless of revision abc") {
			t.Errorf("reason = %q, want it to name the revision the state overrides", d.Reason)
		}
	})

	t.Run("sweep re-fires the lens review when the latest review stands at an older revision", func(t *testing.T) {
		d := Decide(prEvent(EventReconcile, ""), State{ReviewLenses: testLenses, PR: openPR("abc"), Reviews: []Review{oldReview(ReviewApproved)}}, lensWatermark("abc", "carol"))
		if d.Action != ActionReview {
			t.Fatalf("decision = %+v, want review: the head carries work no reviewer has looked at", d)
		}
		if strings.Contains(d.Reason, "already dispatched") {
			t.Errorf("reason = %q, want the state that calls for the action, not the watermark", d.Reason)
		}
		if !strings.Contains(d.Reason, "regardless of revision abc") {
			t.Errorf("reason = %q, want it to name the revision the state overrides", d.Reason)
		}
	})

	t.Run("sweep re-fires fix-feedback at a changes-requested review standing at head", func(t *testing.T) {
		d := Decide(prEvent(EventReconcile, ""), State{ReviewLenses: testLenses, PR: openPR("abc"), Reviews: []Review{review(ReviewChangesRequested, "carol", testTime)}}, &Watermark{Action: string(ActionFixFeedback), Revision: "abc"})
		if d.Action != ActionFixFeedback {
			t.Fatalf("decision = %+v, want fix-feedback: feedback was asked for and the head has not moved since", d)
		}
		if strings.Contains(d.Reason, "already dispatched") {
			t.Errorf("reason = %q, want the state that calls for the action, not the watermark", d.Reason)
		}
		if !strings.Contains(d.Reason, "regardless of revision abc") {
			t.Errorf("reason = %q, want it to name the revision the state overrides", d.Reason)
		}
	})

	t.Run("sweep re-fires fix-feedback at a comment review standing at head", func(t *testing.T) {
		d := Decide(prEvent(EventReconcile, ""), State{ReviewLenses: testLenses, PR: openPR("abc"), Reviews: []Review{review(ReviewComment, "carol", testTime)}}, &Watermark{Action: string(ActionFixFeedback), Revision: "abc"})
		if d.Action != ActionFixFeedback {
			t.Fatalf("decision = %+v, want fix-feedback: the sweep moves the PR along by triggering the implementer", d)
		}
		if strings.Contains(d.Reason, "already dispatched") {
			t.Errorf("reason = %q, want the state that calls for the action, not the watermark", d.Reason)
		}
	})

	t.Run("the trigger-review label is not needed to ask again on the sweep", func(t *testing.T) {
		d := Decide(prEvent(EventReconcile, ""), State{ReviewLenses: testLenses, PR: labelledPR("abc", LabelTriggerReview)}, lensWatermark("abc", "carol"))
		if d.Action != ActionReview {
			t.Fatalf("decision = %+v, want review: a labelled hold-off at the recorded revision would leave the PR stranded like an unlabelled one", d)
		}
		if strings.Contains(d.Reason, "already dispatched") {
			t.Errorf("reason = %q, want the state that calls for the action, not the watermark", d.Reason)
		}
	})

	// Green controls: the exemption is exactly the three sweep rows.

	t.Run("an approving review at head still belongs to merge-or-wait and none of the three fires", func(t *testing.T) {
		d := Decide(prEvent(EventReconcile, ""), State{ReviewLenses: testLenses, PR: openPR("abc"), Reviews: []Review{review(ReviewApproved, "carol", testTime)}}, &Watermark{Action: string(ActionMergeOrWait), Revision: "abc"})
		if d.Dispatched {
			t.Fatalf("decision = %+v, want the merge-or-wait hold-off: an approved PR without the opt-in waits on a human", d)
		}
		if d.Action == ActionReview || d.Action == ActionFixFeedback {
			t.Errorf("decision = %+v, want no review-side action: a review at head ends the review rows", d)
		}
		if !strings.Contains(d.Reason, LabelAgenticAutoMerge) {
			t.Errorf("reason = %q, want the merge-side hold-off naming the missing opt-in", d.Reason)
		}
	})

	t.Run("a PR that is not mergeable still decides rebase, revision-keyed", func(t *testing.T) {
		d := Decide(prEvent(EventReconcile, ""), State{ReviewLenses: testLenses, PR: notMergeablePR("abc")}, &Watermark{Action: string(ActionRebase), Revision: "abc"})
		if !d.HoldOff() {
			t.Fatalf("decision = %+v, want a hold-off: rebase is real work and stays revision-keyed", d)
		}
		if !strings.Contains(d.Reason, "rebase already dispatched at revision abc") {
			t.Errorf("reason = %q, want the revision-keyed hold-off", d.Reason)
		}
	})

	t.Run("a PR whose connected issue is blocked by an open ideation issue still dispatches the exempt review row", func(t *testing.T) {
		d := Decide(prEvent(EventReconcile, ""), State{
			ReviewLenses: testLenses,
			PR:           openPR("abc"),
			ConnectedIssues: []ConnectedIssue{{
				Issue:    *connectedIssue(),
				Blockers: []Issue{{Number: 1, State: "open", Repository: "o/r", Labels: []string{LabelIdeation}}},
			}},
		}, lensWatermark("abc", "carol"))
		if d.Action != ActionReview {
			t.Fatalf("decision = %+v, want review: nothing about a connected issue's blockers decides ahead of the exempt rows (s-git/maitred#151)", d)
		}
	})

	t.Run("a closed PR holds off: the re-fire ends where the PR does", func(t *testing.T) {
		d := Decide(prEvent(EventReconcile, ""), State{ReviewLenses: testLenses, PR: closedPR("abc")}, lensWatermark("abc", "carol"))
		if !d.HoldOff() {
			t.Fatalf("decision = %+v, want a hold-off: the sweep enumerates open pull requests only", d)
		}
		if !strings.Contains(d.Reason, "not open") {
			t.Errorf("reason = %q, want it to name the PR's state", d.Reason)
		}
	})

	t.Run("event paths keep the revision-keyed guard", func(t *testing.T) {
		cases := []struct {
			name      string
			event     Event
			state     State
			watermark *Watermark
		}{
			{"pr_synced with no review at a recorded revision", prEvent(EventPRSynced, "alice"), State{ReviewLenses: testLenses, PR: openPR("abc")}, lensWatermark("abc", "carol")},
			{"pr_synced with a review at an older revision", prEvent(EventPRSynced, "alice"), State{ReviewLenses: testLenses, PR: openPR("abc"), Reviews: []Review{oldReview(ReviewApproved)}}, lensWatermark("abc", "carol")},
			{"pr_synced carrying trigger-review with no review", prEvent(EventPRSynced, "alice"), State{ReviewLenses: testLenses, PR: labelledPR("abc", LabelTriggerReview)}, lensWatermark("abc", "carol")},
			{"pr_reviewed requesting changes at head", prEvent(EventPRReviewed, "carol"), State{ReviewLenses: testLenses, PR: openPR("abc"), Reviews: []Review{review(ReviewChangesRequested, "carol", testTime)}}, &Watermark{Action: string(ActionFixFeedback), Revision: "abc"}},
			{"pr_commented standing at a changes-requested review at head", prEvent(EventPRCommented, "alice"), State{ReviewLenses: testLenses, PR: openPR("abc"), Reviews: []Review{review(ReviewChangesRequested, "carol", testTime)}}, &Watermark{Action: string(ActionFixFeedback), Revision: "abc"}},
		}
		for _, tt := range cases {
			d := Decide(tt.event, tt.state, tt.watermark)
			if !d.HoldOff() {
				t.Errorf("%s: decision = %+v, want a hold-off: the exemption is the sweep's rows alone", tt.name, d)
			}
			if !strings.Contains(d.Reason, "already dispatched at revision abc") {
				t.Errorf("%s: reason = %q, want the revision-keyed hold-off", tt.name, d.Reason)
			}
		}
	})
}

// TestDecide_FanOutReasonFlagsNeverSeenLensIdentity: a subject slot
// at head with no review by the lens's login anywhere on the PR is
// the shape a mistyped identity produces — the lens can never report,
// so the sweep re-fires it forever. The reason says what the state
// shows so an operator can tell that from a dispatch still
// mid-flight (§forgejo/config/review-lenses).
func TestDecide_FanOutReasonFlagsNeverSeenLensIdentity(t *testing.T) {
	t.Run("the sweep reason names a lens identity never seen on the PR", func(t *testing.T) {
		d := Decide(prEvent(EventReconcile, ""), State{ReviewLenses: testLenses, PR: openPR("abc")}, lensWatermark("abc", "carol"))
		if d.Action != ActionReview {
			t.Fatalf("decision = %+v, want the exempt sweep re-fire", d)
		}
		if !strings.Contains(d.Reason, "no review by carol has been seen on this PR at all") {
			t.Errorf("reason = %q, want it to name the identity the PR has never seen", d.Reason)
		}
	})

	t.Run("a lens whose earlier reviews simply went stale keeps the plain reason", func(t *testing.T) {
		d := Decide(prEvent(EventReconcile, ""), State{
			ReviewLenses: testLenses,
			PR:           openPR("abc"),
			Reviews:      []Review{{Event: ReviewApproved, Author: "carol", CommitID: "old", SubmittedAt: testTime}},
		}, lensWatermark("abc", "carol"))
		if d.Action != ActionReview {
			t.Fatalf("decision = %+v, want the exempt sweep re-fire", d)
		}
		if strings.Contains(d.Reason, "has been seen on this PR at all") {
			t.Errorf("reason = %q, want no identity warning: the login has reviewed, its approval is merely stale", d.Reason)
		}
	})
}

// TestDecide_ReviewLenses pins the dispatch predicate end to end on
// the pure decision function: the lenses to dispatch are the
// configured lenses with no undismissed approving review at the
// current head — all outstanding at first, partial as verdicts land,
// the whole chain again after a push invalidated the approvals, and
// nothing once every lens approves at head (§forgejo/decisions/review-lenses).
func TestDecide_ReviewLenses(t *testing.T) {
	lenses := []ReviewLens{{Identity: "lens-min"}, {Identity: "lens-maint"}, {Identity: "lens-sec"}}
	pr := optedIntoMerge(openPR("head-1"))
	at := func(lens, event, sha string, minute int) Review {
		return Review{
			Event:       event,
			Author:      lens,
			CommitID:    sha,
			SubmittedAt: testTime.Add(time.Duration(minute) * time.Minute),
		}
	}
	stateWith := func(reviews ...Review) State {
		return State{ReviewLenses: lenses, PR: pr, Reviews: reviews}
	}

	t.Run("all outstanding: every lens dispatches in order", func(t *testing.T) {
		d := Decide(prEvent(EventPROpened, "alice"), stateWith(), nil)
		if d.Action != ActionReview || len(d.Lenses) != 3 {
			t.Fatalf("decision = %+v, want a fan-out over all three lenses", d)
		}
		if d.Lenses[0].Lens != "lens-min" || d.Lenses[2].Lens != "lens-sec" {
			t.Errorf("fan-out = %+v, want config order", d.Lenses)
		}
	})

	t.Run("partial: mid-flight lenses hold off, named as outstanding", func(t *testing.T) {
		// All three were dispatched at the head; lens-min approved.
		// The other two are mid-flight — their subject slots keep
		// the fan-out from duplicating them, and the hold-off names
		// them (§forgejo/decisions/review-lenses).
		st := stateWith(at("lens-min", ReviewApproved, "head-1", 1))
		d := Decide(prEvent(EventPRReviewed, "lens-min"), st, lensWatermark("head-1", "lens-min", "lens-maint", "lens-sec"))
		if d.Dispatched {
			t.Fatalf("decision = %+v, want a hold-off: the outstanding lenses are mid-flight", d)
		}
		if !strings.Contains(d.Reason, "lens-maint") || !strings.Contains(d.Reason, "lens-sec") {
			t.Errorf("reason = %q, want the outstanding lenses named", d.Reason)
		}
		// Without the slots — a dispatch that went nowhere — the
		// sweep re-fires exactly those two.
		d = Decide(prEvent(EventReconcile, ""), st, nil)
		if d.Action != ActionReview || len(d.Lenses) != 2 || d.Lenses[0].Lens != "lens-maint" || d.Lenses[1].Lens != "lens-sec" {
			t.Fatalf("sweep decision = %+v, want lens-maint and lens-sec re-dispatched", d)
		}
	})

	t.Run("stale after push: the whole chain re-fires", func(t *testing.T) {
		// Approvals stand at the pre-push head; the push dismissed
		// them, so no lens holds an undismissed approval at head.
		st := State{ReviewLenses: lenses, PR: openPR("head-2"), Reviews: []Review{
			at("lens-min", ReviewApproved, "head-1", 1),
			at("lens-maint", ReviewApproved, "head-1", 2),
			at("lens-sec", ReviewApproved, "head-1", 3),
		}}
		d := Decide(prEvent(EventPRSynced, "implementer-bot"), st, lensWatermark("head-1", "lens-min", "lens-maint", "lens-sec"))
		if d.Action != ActionReview || len(d.Lenses) != 3 {
			t.Fatalf("decision = %+v, want all three lenses re-dispatched at the new head", d)
		}
	})

	t.Run("a dismissed approval is no approval", func(t *testing.T) {
		st := stateWith(
			Review{Event: ReviewApproved, Author: "lens-min", CommitID: "head-1", SubmittedAt: testTime, Dismissed: true},
			at("lens-maint", ReviewApproved, "head-1", 2),
			at("lens-sec", ReviewApproved, "head-1", 3),
		)
		d := Decide(prEvent(EventReconcile, ""), st, nil)
		if d.Action != ActionReview || len(d.Lenses) != 1 || d.Lenses[0].Lens != "lens-min" {
			t.Fatalf("decision = %+v, want only the dismissed lens re-dispatched", d)
		}
	})

	t.Run("all approved: merge-or-wait decides, the fan-out holds off", func(t *testing.T) {
		st := stateWith(
			at("lens-min", ReviewApproved, "head-1", 1),
			at("lens-maint", ReviewApproved, "head-1", 2),
			at("lens-sec", ReviewApproved, "head-1", 3),
		)
		d := Decide(prEvent(EventPRReviewed, "lens-sec"), st, nil)
		if d.Action != ActionMergeOrWait {
			t.Fatalf("decision = %+v, want merge-or-wait once every lens approves at head", d)
		}
	})

	t.Run("fix-feedback waits for every lens and names the outstanding", func(t *testing.T) {
		// lens-min rejected, lens-maint approved, lens-sec silent:
		// the round is unfinished, so no fix dispatches yet and the
		// hold-off names who still owes a report.
		st := stateWith(
			at("lens-min", ReviewChangesRequested, "head-1", 1),
			at("lens-maint", ReviewApproved, "head-1", 2),
		)
		d := Decide(prEvent(EventPRReviewed, "lens-maint"), st, lensWatermark("head-1", "lens-maint"))
		if d.Action != ActionReview || len(d.Lenses) != 2 || d.Lenses[0].Lens != "lens-min" || d.Lenses[1].Lens != "lens-sec" {
			t.Fatalf("decision = %+v, want the unreported lenses dispatched, not fix-feedback", d)
		}
		if strings.Contains(d.Reason, "fix") {
			t.Errorf("reason = %q, must not dispatch fix-feedback while the round is open", d.Reason)
		}
		// Once every lens has reported and the reports are not all
		// approvals, the fix fires.
		st.Reviews = append(st.Reviews, at("lens-sec", ReviewChangesRequested, "head-1", 3))
		d = Decide(prEvent(EventPRReviewed, "lens-sec"), st, lensWatermark("head-1", "lens-min", "lens-maint", "lens-sec"))
		if d.Action != ActionFixFeedback {
			t.Fatalf("decision = %+v, want fix-feedback once the round closed with rejections", d)
		}
		if !strings.Contains(d.Reason, "lens-min") || !strings.Contains(d.Reason, "lens-sec") {
			t.Errorf("reason = %q, want the lenses without an approval named", d.Reason)
		}
	})

	t.Run("a lens's own push does not re-fire that lens", func(t *testing.T) {
		st := stateWith(at("lens-min", ReviewApproved, "head-0", 1))
		d := Decide(prEvent(EventPRSynced, "lens-min"), st, nil)
		if d.Action != ActionReview {
			t.Fatalf("decision = %+v, want the fan-out", d)
		}
		for _, ld := range d.Lenses {
			if ld.Lens == "lens-min" {
				t.Errorf("the pushing lens was re-fired: %+v", d.Lenses)
			}
		}
		if len(d.Lenses) != 2 {
			t.Errorf("fan-out = %+v, want the other two lenses", d.Lenses)
		}
	})
}

// blockedTracker is the tracker's membership edge out of the root: the
// tracker stands by the order's own rule, so this edge is a standing edge
// that says nothing about whether a forest remains
// (§forgejo/decisions/forest-teardown, s-git/maitred#173).
func blockedTracker() Issue {
	return Issue{Number: 30, State: "open", Labels: []string{LabelOutcome}, Repository: "o/r"}
}

// TestDecide_TeardownSparesTheTrackerInCount pins the exclusion: the
// engine counts what the order can delete, and a tracker is never
// deleted — so a root whose only downstream edge reaches its tracker
// holds off on the event path and on the sweep, naming the state fact,
// while a root still blocking a work item dispatches on both paths
// (§forgejo/decisions/forest-teardown,
// §forgejo/reconciliation/watermark-exemptions, s-git/maitred#173).
func TestDecide_TeardownSparesTheTrackerInCount(t *testing.T) {
	sweep := func(root *Issue, blocked []Issue, w *Watermark) Decision {
		return Decide(Event{Type: EventReconcile, Repo: "o/r", Kind: KindIssue, Number: root.Number},
			State{Issue: root, Blocked: blocked}, w)
	}

	t.Run("a root blocking only its tracker holds off on both paths", func(t *testing.T) {
		root := teardownRoot()
		d := Decide(issueEvent(EventIssueLabelsChanged), State{Issue: root, Blocked: []Issue{blockedTracker()}}, nil)
		if d.Action != "" {
			t.Errorf("event path dispatched %q (%s), want the hold-off", d.Action, d.Reason)
		}
		if !strings.Contains(d.Reason, LabelOutcome) {
			t.Errorf("reason = %q, want the spared tracker named", d.Reason)
		}
		w := &Watermark{Action: string(ActionTeardownForest), Revision: issueRevision(root)}
		sd := sweep(root, []Issue{blockedTracker()}, w)
		if sd.Action != "" {
			t.Errorf("sweep dispatched %q (%s), want the hold-off whatever the watermark", sd.Action, sd.Reason)
		}
		if strings.Contains(sd.Reason, "already dispatched") {
			t.Errorf("reason = %q, want the state fact, not a revision guard", sd.Reason)
		}
		if !strings.Contains(sd.Reason, LabelOutcome) {
			t.Errorf("reason = %q, want the spared tracker named", sd.Reason)
		}
	})

	t.Run("a root blocking a tracker and a work item still dispatches on both paths", func(t *testing.T) {
		root := teardownRoot()
		blocked := []Issue{blockedTracker(), {Number: 9, State: "open", Repository: "o/r"}}
		d := Decide(issueEvent(EventIssueLabelsChanged), State{Issue: root, Blocked: blocked}, nil)
		if d.Action != ActionTeardownForest {
			t.Errorf("event path action = %q, want %q (%s)", d.Action, ActionTeardownForest, d.Reason)
		}
		sd := sweep(root, blocked, nil)
		if sd.Action != ActionTeardownForest {
			t.Errorf("sweep action = %q, want %q (%s)", sd.Action, ActionTeardownForest, sd.Reason)
		}
	})

	// The `ideation` carve-out in isTracker is what makes a downstream
	// issue carrying both reserved labels a member the order would
	// delete rather than a tracker it spares — the graph the one-hop
	// membership rule invites, a root blocking another root
	// (§forgejo/decisions/forest-teardown, s-git/maitred#173).
	t.Run("a downstream issue carrying both reserved labels is a member, not a tracker", func(t *testing.T) {
		root := teardownRoot()
		blocked := []Issue{{Number: 12, State: "open", Labels: []string{LabelOutcome, LabelIdeation}, Repository: "o/r"}}
		d := Decide(issueEvent(EventIssueLabelsChanged), State{Issue: root, Blocked: blocked}, nil)
		if d.Action != ActionTeardownForest {
			t.Errorf("event path action = %q, want %q (%s)", d.Action, ActionTeardownForest, d.Reason)
		}
		sd := sweep(root, blocked, nil)
		if sd.Action != ActionTeardownForest {
			t.Errorf("sweep action = %q, want %q (%s)", sd.Action, ActionTeardownForest, sd.Reason)
		}
	})
}

// TestDecide_ApprovedPRWithoutMergeOptInHoldsOff: the
// agentic-auto-merge label is a precondition of merge-or-wait on
// every path that can dispatch it — the review event, PR commented
// (which decides as the sweep's PR path), the sweep's approved row,
// and the cascade's PR leg, which takes the review-event row on the
// PR's own re-fetch (§forgejo/decisions/unblock-cascade). An
// approved, mergeable PR with no opt-in holds off, its reason naming
// the missing label and the connected issue; the labelled PR still
// dispatches, and the sync-set hold-off still sits ahead of this row
// whatever it decides (§forgejo/decisions/merge-gate,
// s-git/maitred#179).
func TestDecide_ApprovedPRWithoutMergeOptInHoldsOff(t *testing.T) {
	state := func(labels ...string) State {
		return State{
			ReviewLenses:    testLenses,
			PR:              labelledPR("abc", labels...),
			Reviews:         []Review{review(ReviewApproved, "carol", testTime)},
			ConnectedIssues: []ConnectedIssue{connectedIssueOf(7)},
		}
	}
	paths := []struct {
		name string
		ev   Event
	}{
		{"the review event", prEvent(EventPRReviewed, "carol")},
		{"the PR-commented event", prEvent(EventPRCommented, "alice")},
		{"the sweep's approved row", prEvent(EventReconcile, "maitred")},
	}
	for _, p := range paths {
		t.Run("no opt-in holds off on "+p.name, func(t *testing.T) {
			d := Decide(p.ev, state(), nil)
			if !d.HoldOff() {
				t.Fatalf("decision = %+v, want a hold-off: an approved PR with no merge opt-in waits on a human", d)
			}
			if !strings.Contains(d.Reason, LabelAgenticAutoMerge) {
				t.Errorf("reason = %q, want it to name the missing opt-in label", d.Reason)
			}
			if !strings.Contains(d.Reason, "o/r#7") {
				t.Errorf("reason = %q, want it to name the connected issue", d.Reason)
			}
		})
		t.Run("the opt-in still dispatches on "+p.name, func(t *testing.T) {
			d := Decide(p.ev, state(LabelAgenticAutoMerge), nil)
			if d.Action != ActionMergeOrWait {
				t.Fatalf("decision = %+v, want merge-or-wait: the labelled PR is unchanged", d)
			}
		})
	}
	t.Run("the sync-set hold-off sits ahead of the opt-in row", func(t *testing.T) {
		for _, p := range paths {
			s := state()
			s.ConnectedIssues = []ConnectedIssue{syncMemberConnected(7, 11)}
			d := Decide(p.ev, s, nil)
			if !d.HoldOff() {
				t.Fatalf("%s: decision = %+v, want the set's hold-off", p.name, d)
			}
			if !strings.Contains(d.Reason, "sync set") {
				t.Errorf("%s: reason = %q, want it to name the sync set, not this row", p.name, d.Reason)
			}
			if strings.Contains(d.Reason, LabelAgenticAutoMerge) {
				t.Errorf("%s: reason = %q, want the set's decision, not the missing opt-in", p.name, d.Reason)
			}
		}
	})
	t.Run("a PR connected to no issue names that in the hold-off", func(t *testing.T) {
		s := state()
		s.ConnectedIssues = nil
		d := Decide(prEvent(EventPRReviewed, "carol"), s, nil)
		if !d.HoldOff() {
			t.Fatalf("decision = %+v, want a hold-off", d)
		}
		if !strings.Contains(d.Reason, LabelAgenticAutoMerge) {
			t.Errorf("reason = %q, want it to name the missing opt-in label", d.Reason)
		}
		if !strings.Contains(d.Reason, "connected issues: none") {
			t.Errorf("reason = %q, want it to say the PR is connected to no issue", d.Reason)
		}
	})
}
