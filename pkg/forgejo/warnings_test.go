package forgejo

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

// Fixtures for the forest warnings of the outcomes view
// (§forgejo/observability/the-outcomes-view). Every case is a forest of
// one outcomes repository (o/out, holding the root and its tracker) and
// one work repository (o/r, holding the member whose state the warning
// is about) — the cross-repository shape the walk must read where the
// member lives rather than where the root does.

// warningsForest loads api with a forest whose root is 51, whose tracker
// is 71, and whose single open member is member, and returns the member
// so the case can place its labels.
func warningsForest(api *fakeAPI, member Issue) {
	api.repos = []Repository{testRepo("o/r"), outcomesRepo("o/out")}
	root := outcomesRoot(51, "UI for human review")
	tracker := outcomesTracker(71, "forest tracker")
	api.openIssues["out"] = []Issue{root, tracker}
	api.blocks[51] = []Issue{tracker, member}
}

// warningsMember builds an open member of o/r carrying the given labels.
func warningsMember(number int, labels ...string) Issue {
	return Issue{
		Number:     number,
		Title:      "member " + strconv.Itoa(number),
		State:      "open",
		Labels:     labels,
		Repository: "o/r",
		UpdatedAt:  testUpdatedAt,
		HTMLURL:    issueURL(number),
	}
}

// approvedDelivery loads api with the open pull request delivering
// member: mergeable, approved at head by the configured lens
// threshold+ago, and naming the member on its body with the closing
// grammar the delivery relation is read from. It returns the PR.
func approvedDelivery(t *testing.T, api *fakeAPI, member Issue, approved time.Time) PullRequest {
	t.Helper()
	pr := *testPR(900+member.Number, "open", true)
	pr.Body = "Fixes " + member.Repository + "#" + strconv.Itoa(member.Number)
	api.openPulls["r"] = []PullRequest{pr}
	api.pulls[pr.Number] = &pr
	api.reviews[pr.Number] = []Review{{
		Event:       ReviewApproved,
		Author:      "bob",
		CommitID:    pr.HeadSHA,
		SubmittedAt: approved,
	}}
	return pr
}

// TestForestWarnings_StaleAgenticMergeWarns: an approved, mergeable,
// unmerged pull request delivering a member that opted in has been
// waiting on a human past the threshold, so the forest carries a stale
// agentic merge warning naming the pull request and how long it has
// stood (§forgejo/observability/the-outcomes-view).
func TestForestWarnings_StaleAgenticMergeWarns(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	member := warningsMember(12, LabelAgenticAutoMerge)
	warningsForest(api, member)
	pr := approvedDelivery(t, api, member, time.Now().Add(-2*time.Hour))

	forests, err := eng.enumerateForests()
	if err != nil {
		t.Fatalf("enumerateForests: %v", err)
	}
	warns := forests[0].Warnings
	if len(warns) != 1 {
		t.Fatalf("warnings = %d, want the one stale merge: %+v", len(warns), warns)
	}
	w := warns[0]
	if w.Kind != WarningStaleAgenticMerge {
		t.Errorf("kind = %q, want %q", w.Kind, WarningStaleAgenticMerge)
	}
	if w.Repository != "o/r" || w.Number != pr.Number {
		t.Errorf("reference = %s#%d, want the delivering PR o/r#%d", w.Repository, w.Number, pr.Number)
	}
	if !strings.HasSuffix(w.HTMLURL, "/o/r/pulls/"+strconv.Itoa(pr.Number)) {
		t.Errorf("html_url = %q, want the pull request's URL", w.HTMLURL)
	}
	if w.Title == "" {
		t.Error("title is empty; the row must link rather than construct")
	}
	if w.AgeSeconds < 2*3600 || w.AgeSeconds > 2*3600+60 {
		t.Errorf("age_seconds = %v, want the two hours since the approval", w.AgeSeconds)
	}
}

// TestForestWarnings_SyncMergeMemberWarns: a landing-set member's
// delivery that has gone stale warns too — the issue names both landing
// labels, and which set's decision a merge belongs to is not the
// warning's question (§forgejo/observability/the-outcomes-view).
func TestForestWarnings_SyncMergeMemberWarns(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	member := warningsMember(13, LabelAgenticSyncMerge)
	warningsForest(api, member)
	approvedDelivery(t, api, member, time.Now().Add(-3*time.Hour))

	forests, err := eng.enumerateForests()
	if err != nil {
		t.Fatalf("enumerateForests: %v", err)
	}
	if len(forests[0].Warnings) != 1 || forests[0].Warnings[0].Kind != WarningStaleAgenticMerge {
		t.Fatalf("warnings = %+v, want the one stale merge of a landing-set member", forests[0].Warnings)
	}
}

// TestForestWarnings_FreshApprovalDoesNotWarn: the same shape approved a
// moment ago is not stale, so the threshold is the only thing separating
// the two cases (§forgejo/observability/the-outcomes-view).
func TestForestWarnings_FreshApprovalDoesNotWarn(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	member := warningsMember(14, LabelAgenticAutoMerge)
	warningsForest(api, member)
	approvedDelivery(t, api, member, time.Now())

	forests, err := eng.enumerateForests()
	if err != nil {
		t.Fatalf("enumerateForests: %v", err)
	}
	if len(forests[0].Warnings) != 0 {
		t.Fatalf("warnings = %+v, want none beside a moment-old approval", forests[0].Warnings)
	}
}

// TestForestWarnings_MergeGateHoldsOffDoesNotWarn: a delivery the merge
// gate would not fire at — here an unmergeable head — is waiting on the
// rebase, not on a human, and warns nothing
// (§forgejo/observability/the-outcomes-view).
func TestForestWarnings_MergeGateHoldsOffDoesNotWarn(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	member := warningsMember(15, LabelAgenticAutoMerge)
	warningsForest(api, member)
	pr := approvedDelivery(t, api, member, time.Now().Add(-3*time.Hour))
	pr.Mergeable = false
	api.pulls[pr.Number] = &pr

	forests, err := eng.enumerateForests()
	if err != nil {
		t.Fatalf("enumerateForests: %v", err)
	}
	if len(forests[0].Warnings) != 0 {
		t.Fatalf("warnings = %+v, want none for a head that cannot merge", forests[0].Warnings)
	}
}

// TestForestWarnings_ThresholdHonouredWhenConfigured: the bound is a
// config option, so a forest whose delivery has stood approved for ninety
// minutes is stale against a one-hour default and healthy against a two
// hour threshold (§forgejo/config/stale-merge-threshold,
// §forgejo/observability/the-outcomes-view).
func TestForestWarnings_ThresholdHonouredWhenConfigured(t *testing.T) {
	for _, tc := range []struct {
		name      string
		threshold time.Duration
		wantWarn  bool
	}{
		{name: "default threshold", threshold: 0, wantWarn: true},
		{name: "configured above the age", threshold: 2 * time.Hour, wantWarn: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eng, api, _, _ := newTestEngine(t)
			eng.cfg.StaleMergeThreshold = tc.threshold
			member := warningsMember(16, LabelAgenticAutoMerge)
			warningsForest(api, member)
			approvedDelivery(t, api, member, time.Now().Add(-90*time.Minute))

			forests, err := eng.enumerateForests()
			if err != nil {
				t.Fatalf("enumerateForests: %v", err)
			}
			got := len(forests[0].Warnings) > 0
			if got != tc.wantWarn {
				t.Fatalf("warnings = %+v, want warning: %v", forests[0].Warnings, tc.wantWarn)
			}
		})
	}
}

// TestForestWarnings_HumanTaskWithOpenBlockerDoesNotWarn: a member the
// human cannot start yet — a blocker still stands — is not the forest
// waiting on a person and warns nothing
// (§forgejo/observability/the-outcomes-view).
func TestForestWarnings_HumanTaskWithOpenBlockerDoesNotWarn(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	member := warningsMember(17, LabelHumanTask)
	warningsForest(api, member)
	open := Issue{
		Number: 18, Title: "still going", State: "open", Repository: "o/r",
		UpdatedAt: testUpdatedAt,
	}
	api.blocks[51] = append(api.blocks[51], open)
	api.deps[member.Number] = []Issue{
		{Number: 51, State: "open", Repository: "o/out", Labels: []string{LabelIdeation}},
		open,
	}

	forests, err := eng.enumerateForests()
	if err != nil {
		t.Fatalf("enumerateForests: %v", err)
	}
	if len(forests[0].Warnings) != 0 {
		t.Fatalf("warnings = %+v, want none while a blocker is open", forests[0].Warnings)
	}
}

// TestForestWarnings_HumanTaskEveryBlockerClosedWarns: the same member
// with every work-order blocker closed is the forest waiting on a
// person, so it warns with the wait measured from the last closure — and
// the root's membership edge, open as long as the forest is, does not
// hold the warning back (§forgejo/decisions/commissioning,
// §forgejo/observability/the-outcomes-view).
func TestForestWarnings_HumanTaskEveryBlockerClosedWarns(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	member := warningsMember(19, LabelHumanTask)
	warningsForest(api, member)
	closed := Issue{
		Number: 20, Title: "landed", State: "closed", Repository: "o/r",
		UpdatedAt: time.Now().Add(-90 * time.Minute),
	}
	api.blocks[51] = append(api.blocks[51], closed)
	api.deps[member.Number] = []Issue{
		{Number: 51, State: "open", Repository: "o/out", Labels: []string{LabelIdeation}},
		closed,
	}

	forests, err := eng.enumerateForests()
	if err != nil {
		t.Fatalf("enumerateForests: %v", err)
	}
	warns := forests[0].Warnings
	if len(warns) != 1 {
		t.Fatalf("warnings = %d, want the one human task: %+v", len(warns), warns)
	}
	w := warns[0]
	if w.Kind != WarningHumanTaskBlocking {
		t.Errorf("kind = %q, want %q", w.Kind, WarningHumanTaskBlocking)
	}
	if w.Repository != "o/r" || w.Number != member.Number {
		t.Errorf("reference = %s#%d, want the human task %s#%d", w.Repository, w.Number, member.Repository, member.Number)
	}
	if !strings.HasSuffix(w.HTMLURL, "/o/out/issues/19") {
		t.Errorf("html_url = %q, want the member's URL", w.HTMLURL)
	}
	if w.AgeSeconds < 90*60-60 || w.AgeSeconds > 90*60+60 {
		t.Errorf("age_seconds = %v, want the ninety minutes since the last blocker closed", w.AgeSeconds)
	}
}

// TestForestWarnings_HealthyForestWarnsNothing: a forest whose member is
// an ordinary work item warns nothing, and states that honestly as an
// empty list rather than a null the UI would have to guard
// (§forgejo/observability/the-outcomes-view).
func TestForestWarnings_HealthyForestWarnsNothing(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	member := warningsMember(21, LabelAgenticAutoMerge)
	warningsForest(api, member)
	api.deps[member.Number] = []Issue{
		{Number: 51, State: "open", Repository: "o/out", Labels: []string{LabelIdeation}},
	}

	forests, err := eng.enumerateForests()
	if err != nil {
		t.Fatalf("enumerateForests: %v", err)
	}
	if forests[0].Warnings == nil {
		t.Fatal("warnings = nil; the honest empty is an empty list")
	}
	if len(forests[0].Warnings) != 0 {
		t.Fatalf("warnings = %+v, want none from a member nothing is waiting on", forests[0].Warnings)
	}
}

// TestForestWarnings_FailedReadFailsTheEnumeration: a warning read that
// fails fails the whole enumeration, so the view answers 500 rather than
// presenting a forest waiting on a person as a healthy one — the same
// fail-closed rule the enumeration already applies to its own reads
// (§forgejo/observability/the-outcomes-view).
func TestForestWarnings_FailedReadFailsTheEnumeration(t *testing.T) {
	for _, tc := range []struct {
		name string
		fail func(*fakeAPI)
	}{
		{"pull request listing", func(f *fakeAPI) { f.listPullsErr = map[string]error{"r": errWarnFixture} }},
		{"blocker read", func(f *fakeAPI) { f.issueDepsErr = errWarnFixture }},
		{"pull request read", func(f *fakeAPI) { f.pullErr = errWarnFixture }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eng, api, _, _ := newTestEngine(t)
			member := warningsMember(22, LabelAgenticAutoMerge, LabelHumanTask)
			warningsForest(api, member)
			approvedDelivery(t, api, member, time.Now().Add(-3*time.Hour))
			api.deps[member.Number] = []Issue{{Number: 51, State: "open", Repository: "o/out"}}
			tc.fail(api)

			forests, err := eng.enumerateForests()
			if err == nil {
				t.Fatalf("enumerateForests = %+v, want the failed %s read to fail it", forests, tc.name)
			}
		})
	}
}

var errWarnFixture = &APIError{StatusCode: 500, Message: "read failed"}

// TestForestWarnings_OrderIsDeterministic: the payload's order is the
// walk's, not the forge listing's, so two enumerations of one state
// answer alike (§forgejo/observability/the-outcomes-view).
func TestForestWarnings_OrderIsDeterministic(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	first := warningsMember(23, LabelHumanTask)
	second := warningsMember(24, LabelAgenticAutoMerge)
	api.repos = []Repository{testRepo("o/r"), outcomesRepo("o/out")}
	root := outcomesRoot(51, "UI for human review")
	tracker := outcomesTracker(71, "forest tracker")
	api.openIssues["out"] = []Issue{root, tracker}
	api.blocks[51] = []Issue{tracker, second, first}
	api.deps[first.Number] = nil
	approvedDelivery(t, api, second, time.Now().Add(-3*time.Hour))

	warns, err := eng.forestWarnings(&root, "o/out", api.blocks[51])
	if err != nil {
		t.Fatalf("forestWarnings: %v", err)
	}
	if len(warns) != 2 {
		t.Fatalf("warnings = %+v, want one of each kind", warns)
	}
	if warns[0].Number != first.Number || warns[0].Kind != WarningHumanTaskBlocking {
		t.Errorf("warnings[0] = %+v, want the human task 23 first", warns[0])
	}
	if warns[1].Number != 900+second.Number || warns[1].Kind != WarningStaleAgenticMerge {
		t.Errorf("warnings[1] = %+v, want its delivering PR 9%d second", warns[1], second.Number)
	}
	for _, w := range warns {
		if w.Kind == "" {
			t.Errorf("warning %+v carries no kind", w)
		}
	}
}

// TestForestWarnings_ApprovalWithoutTimestampAgesFromThePR: where the
// review read carries no timestamp for the verdict that satisfied the
// chain, the wait is dated from the pull request's own last activity
// rather than from the epoch, so the age the payload states is one a
// human can act on (§forgejo/observability/the-warnings-a-forest-carries).
func TestForestWarnings_ApprovalWithoutTimestampAgesFromThePR(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	member := warningsMember(25, LabelAgenticAutoMerge)
	warningsForest(api, member)
	pr := approvedDelivery(t, api, member, time.Time{})
	pr.UpdatedAt = time.Now().Add(-3 * time.Hour)
	api.pulls[pr.Number] = &pr

	forests, err := eng.enumerateForests()
	if err != nil {
		t.Fatalf("enumerateForests: %v", err)
	}
	warns := forests[0].Warnings
	if len(warns) != 1 {
		t.Fatalf("warnings = %+v, want the stale merge the undated approval leaves standing", warns)
	}
	if warns[0].AgeSeconds < 3*3600-60 || warns[0].AgeSeconds > 3*3600+60 {
		t.Errorf("age_seconds = %v, want the three hours since the pull request last moved", warns[0].AgeSeconds)
	}
}
