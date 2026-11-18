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
// so the case can place its labels. The member is registered where the
// connected-issues read resolves a closing reference — the way the forge
// answers one that names an issue that exists (§forgejo/webhook/re-fetch).
func warningsForest(api *fakeAPI, member Issue) {
	api.repos = []Repository{testRepo("o/r"), outcomesRepo("o/out")}
	root := outcomesRoot(51, "UI for human review")
	tracker := outcomesTracker(71, "forest tracker")
	api.openIssues["out"] = []Issue{root, tracker}
	api.blocks[51] = []Issue{tracker, member}
	api.issues[member.Number] = &member
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
	_, name := splitRepo(member.Repository)
	api.openPulls[name] = []PullRequest{pr}
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
// delivery that has gone stale warns when the member stands in no open
// set — a label the decomposer left behind gates nothing, and the merge
// gate fires at the head whatever the label says
// (§forgejo/decisions/sync-set, §forgejo/observability/the-outcomes-view).
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

// TestForestWarnings_OwedVerdictDoesNotWarn: a lens has reported nothing
// at head, so the chain owes its verdict, the wait is the reviewers',
// and the ⚠️ that asserts a person is the wait says nothing
// (§forgejo/observability/the-outcomes-view).
func TestForestWarnings_OwedVerdictDoesNotWarn(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	member := warningsMember(35, LabelAgenticAutoMerge)
	warningsForest(api, member)
	pr := approvedDelivery(t, api, member, time.Now().Add(-3*time.Hour))
	api.reviews[pr.Number] = nil // the lens has reported nothing at the head

	forests, err := eng.enumerateForests()
	if err != nil {
		t.Fatalf("enumerateForests: %v", err)
	}
	if len(forests[0].Warnings) != 0 {
		t.Fatalf("warnings = %+v, want none while the chain owes its verdict", forests[0].Warnings)
	}
}

// TestForestWarnings_UndatedApprovalDoesNotWarn: an approval the chain
// stands on that carries no moment says the wait cannot be dated, and
// the walk says nothing rather than borrowing another object's
// timestamp (§forgejo/observability/the-outcomes-view).
func TestForestWarnings_UndatedApprovalDoesNotWarn(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	member := warningsMember(36, LabelAgenticAutoMerge)
	warningsForest(api, member)
	pr := approvedDelivery(t, api, member, time.Now().Add(-3*time.Hour))
	api.reviews[pr.Number] = []Review{{
		Event:    ReviewApproved,
		Author:   "bob",
		CommitID: pr.HeadSHA,
		// SubmittedAt is the zero time: a verdict no clock dates.
	}}

	forests, err := eng.enumerateForests()
	if err != nil {
		t.Fatalf("enumerateForests: %v", err)
	}
	if len(forests[0].Warnings) != 0 {
		t.Fatalf("warnings = %+v, want none beside a verdict no clock dates", forests[0].Warnings)
	}
}

// TestForestWarnings_ThresholdHonouredWhenConfigured: the bound is a
// config option, so a forest whose delivery has stood approved for ninety
// minutes is stale against a one-hour default and healthy against a two
// hour threshold (§forgejo/config/validation,
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
		{"membership read", func(f *fakeAPI) { f.issueBlocksErrOn = map[int]error{22: errWarnFixture} }},
		{"connected-issues read", func(f *fakeAPI) { f.issueBlocksErrOn = map[int]error{922: errWarnFixture} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eng, api, _, _ := newTestEngine(t)
			member := warningsMember(22, LabelAgenticAutoMerge, LabelAgenticSyncMerge, LabelHumanTask)
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

// TestForestWarnings_ConnectedEdgeBlockerHoldsOffDoesNotWarn: the gate
// asks the blockers of every issue the delivery connects — those its body
// names and those its blocking edges name — so an open blocker of an issue
// the delivery merely blocks holds the ⚠️ back too, whichever direction
// the relation ran through (§forgejo/decisions/merge-gate,
// §forgejo/webhook/re-fetch).
func TestForestWarnings_ConnectedEdgeBlockerHoldsOffDoesNotWarn(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	member := warningsMember(40, LabelAgenticAutoMerge)
	warningsForest(api, member)
	pr := approvedDelivery(t, api, member, time.Now().Add(-3*time.Hour))
	connected := Issue{Number: 41, Title: "edge", State: "open", Repository: "o/r", UpdatedAt: testUpdatedAt}
	api.blocks[pr.Number] = []Issue{connected}
	api.deps[41] = []Issue{{
		Number: 42, Title: "the edge's work", State: "open", Repository: "o/r",
		UpdatedAt: testUpdatedAt,
	}}

	forests, err := eng.enumerateForests()
	if err != nil {
		t.Fatalf("enumerateForests: %v", err)
	}
	if len(forests[0].Warnings) != 0 {
		t.Fatalf("warnings = %+v, want none while an issue the delivery blocks has work open", forests[0].Warnings)
	}
}

// TestForestWarnings_DirectSetMembershipHoldsOffDoesNotWarn: a delivery
// that blocks the grouping issue **directly** is a member whose delivery is
// itself, and the gate holds such a set's delivery off (§decisions/sync-set
// reads the row both ways). The grouping issue's only open blocker is the
// delivery itself, which the gate excludes, so it is the set membership —
// read from the row the connected-issues read already holds — that keeps
// the ⚠️ from claiming a person is the wait (§forgejo/decisions/sync-set).
func TestForestWarnings_DirectSetMembershipHoldsOffDoesNotWarn(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	member := warningsMember(43, LabelAgenticAutoMerge)
	warningsForest(api, member)
	pr := approvedDelivery(t, api, member, time.Now().Add(-3*time.Hour))
	set := Issue{Number: 44, Title: "grouping", State: "open", Repository: "o/r", Labels: []string{LabelSync}}
	api.blocks[pr.Number] = []Issue{set}
	api.deps[44] = []Issue{{
		Number: pr.Number, Title: "pr", State: "open", Repository: "o/r", IsPull: true,
		UpdatedAt: testUpdatedAt,
	}}

	forests, err := eng.enumerateForests()
	if err != nil {
		t.Fatalf("enumerateForests: %v", err)
	}
	if len(forests[0].Warnings) != 0 {
		t.Fatalf("warnings = %+v, want none while the delivery is a member of an open set", forests[0].Warnings)
	}
}

// TestForestWarnings_SiblingOutOfRemitHoldsOffDoesNotWarn: a co-delivered
// sibling in a repository no role names cannot be read, and what the walk
// may not read it does not assert — the sibling holds the warning back, and
// the repository costs no read, because a body reference must not direct
// the engine's token wherever the author pointed it
// (§forgejo/webhook/repository-roles, §forgejo/webhook/re-fetch,
// §forgejo/observability/the-warnings-a-forest-carries).
func TestForestWarnings_SiblingOutOfRemitHoldsOffDoesNotWarn(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	member := warningsMember(45, LabelAgenticAutoMerge)
	sibling := warningsMember(46, LabelAgenticAutoMerge)
	sibling.Repository = "archive/r"
	warningsForest(api, member)
	api.repos = append(api.repos, testRepoDisabled("archive/r"))
	pr := approvedDelivery(t, api, member, time.Now().Add(-3*time.Hour))
	pr.Body += " and Fixes " + sibling.Repository + "#" + strconv.Itoa(sibling.Number)
	api.openPulls["r"] = []PullRequest{pr}
	api.pulls[pr.Number] = &pr
	api.deps[sibling.Number] = []Issue{{
		Number: 47, State: "closed", Repository: "archive/r", UpdatedAt: testUpdatedAt,
	}}

	forests, err := eng.enumerateForests()
	if err != nil {
		t.Fatalf("enumerateForests: %v", err)
	}
	if len(forests[0].Warnings) != 0 {
		t.Fatalf("warnings = %+v, want none from a sibling the remit will not let the walk read", forests[0].Warnings)
	}
	for _, op := range []string{"GetIssue:archive", "IssueDependencies:archive"} {
		if n := api.callCount(op); n != 0 {
			t.Errorf("%s paid %d time(s); an out-of-remit reference must cost no read", op, n)
		}
	}
}

// TestForestWarnings_PathSegmentReferenceHoldsOffDoesNotWarn: the closing
// grammar's character class admits dots, so a body can name `../x#5`. The
// walk refuses the shape before any URL is built, as the resolving loop
// does, rather than leaning on what the scope cache happens to hold
// (§forgejo/webhook/re-fetch).
func TestForestWarnings_PathSegmentReferenceHoldsOffDoesNotWarn(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	member := warningsMember(48, LabelAgenticAutoMerge)
	warningsForest(api, member)
	pr := approvedDelivery(t, api, member, time.Now().Add(-3*time.Hour))
	pr.Body += " and Fixes ../x#5"
	api.openPulls["r"] = []PullRequest{pr}
	api.pulls[pr.Number] = &pr

	forests, err := eng.enumerateForests()
	if err != nil {
		t.Fatalf("enumerateForests: %v", err)
	}
	if len(forests[0].Warnings) != 0 {
		t.Fatalf("warnings = %+v, want none from a reference that names no repository", forests[0].Warnings)
	}
	if n := api.callCount("GetIssue:x"); n != 0 {
		t.Errorf("GetIssue:x paid %d time(s); the reference names a path segment", n)
	}
}

// TestForestWarnings_BeyondTheReadCapHoldsOffDoesNotWarn: the body is the
// author's, so it must not choose how many reads the sweep pays. A body
// naming more references than the resolving budget pays costs no read at
// all and asserts no wait — and above all does not fail the view, which is
// what the re-fetch does when it has a merge to protect
// (§forgejo/webhook/re-fetch, §forgejo/observability/the-warnings-a-forest-carries).
func TestForestWarnings_BeyondTheReadCapHoldsOffDoesNotWarn(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	member := warningsMember(49, LabelAgenticAutoMerge)
	warningsForest(api, member)
	pr := approvedDelivery(t, api, member, time.Now().Add(-3*time.Hour))
	for i := 0; i <= maxAutoCloseRefs; i++ {
		pr.Body += " Fixes o/r#" + strconv.Itoa(200+i)
	}
	api.openPulls["r"] = []PullRequest{pr}
	api.pulls[pr.Number] = &pr

	forests, err := eng.enumerateForests()
	if err != nil {
		t.Fatalf("enumerateForests: %v", err)
	}
	if len(forests[0].Warnings) != 0 {
		t.Fatalf("warnings = %+v, want none from a body the read budget cannot finish", forests[0].Warnings)
	}
	if n := api.callCount("GetIssue"); n != 0 {
		t.Errorf("GetIssue paid %d time(s); a body beyond the cap must cost no resolving read", n)
	}
}

// TestForestWarnings_DanglingReferenceIsNotAnOutage: a closing reference
// whose issue is not there is the answer the forge itself ignores, so it
// connects nothing here as it does for the gate — it must not answer 500
// for every forest in the organisation until somebody edits the body
// (§forgejo/webhook/re-fetch, §forgejo/observability/the-outcomes-view).
func TestForestWarnings_DanglingReferenceIsNotAnOutage(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	member := warningsMember(50, LabelAgenticAutoMerge)
	warningsForest(api, member)
	pr := approvedDelivery(t, api, member, time.Now().Add(-3*time.Hour))
	pr.Body += " and Fixes o/r#9999999"
	api.openPulls["r"] = []PullRequest{pr}
	api.pulls[pr.Number] = &pr

	forests, err := eng.enumerateForests()
	if err != nil {
		t.Fatalf("enumerateForests: %v; a reference the forge ignores must not fail the view", err)
	}
	if len(forests[0].Warnings) != 1 || forests[0].Warnings[0].Kind != WarningStaleAgenticMerge {
		t.Fatalf("warnings = %+v, want the stale ⚠️ the member's shape carries", forests[0].Warnings)
	}
	if n := api.callCount("IssueDependencies:r/50"); n != 1 {
		t.Errorf("the member's blockers read paid %d time(s), want 1: the dangling reference must not skip the wait it names", n)
	}
}

// TestForestWarnings_MergeGateBlockerHoldsOffDoesNotWarn: the merge gate
// holds an approved head off on an open blocker of the issue it delivers,
// so the forest waits on that work and not on a person — and the ⚠️ that
// asserts a person is the wait says nothing (§forgejo/decisions/merge-gate,
// §forgejo/observability/the-warnings-a-forest-carries).
func TestForestWarnings_MergeGateBlockerHoldsOffDoesNotWarn(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	member := warningsMember(26, LabelAgenticAutoMerge)
	warningsForest(api, member)
	approvedDelivery(t, api, member, time.Now().Add(-3*time.Hour))
	api.deps[member.Number] = []Issue{{
		Number: 27, Title: "waiting on that", State: "open", Repository: "o/r",
		UpdatedAt: testUpdatedAt,
	}}

	forests, err := eng.enumerateForests()
	if err != nil {
		t.Fatalf("enumerateForests: %v", err)
	}
	if len(forests[0].Warnings) != 0 {
		t.Fatalf("warnings = %+v, want none while the merge gate waits on an open blocker", forests[0].Warnings)
	}
}

// TestForestWarnings_CoDeliveredSiblingHoldsOffDoesNotWarn: one pull
// request delivering two members is one wait, and the gate asks the
// blockers of every issue its body names, so a sibling with work still
// open holds the stale ⚠️ back — the gate waits on that work, not on a
// person (§forgejo/decisions/merge-gate,
// §forgejo/observability/the-warnings-a-forest-carries).
func TestForestWarnings_CoDeliveredSiblingHoldsOffDoesNotWarn(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	member := warningsMember(37, LabelAgenticAutoMerge)
	sibling := warningsMember(38, LabelAgenticAutoMerge)
	warningsForest(api, member) // the walk visits only the member
	pr := approvedDelivery(t, api, member, time.Now().Add(-3*time.Hour))
	pr.Body += " and Fixes " + sibling.Repository + "#" + strconv.Itoa(sibling.Number)
	api.issues[sibling.Number] = &sibling
	api.openPulls["r"] = []PullRequest{pr}
	api.pulls[pr.Number] = &pr
	api.deps[sibling.Number] = []Issue{{
		Number: 39, Title: "the sibling's work", State: "open", Repository: "o/r",
		UpdatedAt: testUpdatedAt,
	}}

	forests, err := eng.enumerateForests()
	if err != nil {
		t.Fatalf("enumerateForests: %v", err)
	}
	if len(forests[0].Warnings) != 0 {
		t.Fatalf("warnings = %+v, want none while a co-delivered sibling's work is open", forests[0].Warnings)
	}
}

// TestForestWarnings_SyncSetMemberHoldsOffDoesNotWarn: a landing-set
// member whose set has not landed waits on the set's other members, never
// on a person, so the approval path's hold-off holds the warning back too
// (§forgejo/decisions/sync-set, §forgejo/observability/the-warnings-a-forest-carries).
func TestForestWarnings_SyncSetMemberHoldsOffDoesNotWarn(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	member := warningsMember(28, LabelAgenticSyncMerge)
	warningsForest(api, member)
	approvedDelivery(t, api, member, time.Now().Add(-3*time.Hour))
	api.blocks[member.Number] = []Issue{{
		Number: 29, Title: "landing set", State: "open", Repository: "o/r",
		Labels:    []string{LabelSync},
		UpdatedAt: testUpdatedAt,
	}}

	forests, err := eng.enumerateForests()
	if err != nil {
		t.Fatalf("enumerateForests: %v", err)
	}
	if len(forests[0].Warnings) != 0 {
		t.Fatalf("warnings = %+v, want none while the set's landing is the wait", forests[0].Warnings)
	}
}

// TestForestWarnings_HumanTaskDeliveryIsNotABlocker: a human who has
// raised their pull request and is waiting on review is the forest
// waiting on a person — the member's own delivery is a delivery and never
// a work order, the way every other blocker read in the engine reads it
// (§forgejo/decisions/merge-gate, §forgejo/observability/the-outcomes-view).
func TestForestWarnings_HumanTaskDeliveryIsNotABlocker(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	member := warningsMember(30, LabelHumanTask, LabelAgenticAutoMerge)
	warningsForest(api, member)
	api.deps[member.Number] = []Issue{{
		Number: 930, Title: "pr 930", State: "open", Repository: "o/r", IsPull: true,
		UpdatedAt: testUpdatedAt,
	}}

	forests, err := eng.enumerateForests()
	if err != nil {
		t.Fatalf("enumerateForests: %v", err)
	}
	warns := forests[0].Warnings
	if len(warns) != 1 || warns[0].Kind != WarningHumanTaskBlocking || warns[0].Number != member.Number {
		t.Fatalf("warnings = %+v, want the human task %d its own delivery does not hold back", warns, member.Number)
	}
}

// TestForestWarnings_OutOfRemitRowIsReadNowhere: a blocking-edge row may
// name any repository the author had in mind, and the walk pays the scope
// test before it reads there — a row outside the engine's remit costs no
// read and states no warning, or an authored edge would point the
// engine's token at a repository no role names and publish what it found
// on an unauthenticated port (§forgejo/webhook/repository-roles,
// §forgejo/webhook/re-fetch, §forgejo/observability/the-outcomes-view).
func TestForestWarnings_OutOfRemitRowIsReadNowhere(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	member := warningsMember(31, LabelAgenticAutoMerge, LabelHumanTask)
	member.Repository = "archive/r"
	warningsForest(api, member)
	api.repos = append(api.repos, testRepoDisabled("archive/r"))
	approvedDelivery(t, api, member, time.Now().Add(-3*time.Hour))
	api.deps[member.Number] = []Issue{{
		Number: 32, State: "closed", Repository: "archive/r", UpdatedAt: testUpdatedAt,
	}}

	forests, err := eng.enumerateForests()
	if err != nil {
		t.Fatalf("enumerateForests: %v", err)
	}
	if len(forests[0].Warnings) != 0 {
		t.Fatalf("warnings = %+v, want none from a repository outside the remit", forests[0].Warnings)
	}
	for _, op := range []string{"ListOpenPullRequests:r", "GetPullRequest:r", "IssueDependencies:r"} {
		if n := api.callCount(op); n != 0 {
			t.Errorf("%s paid %d time(s); an out-of-remit row must cost no read", op, n)
		}
	}
}

// TestForestWarnings_PullRequestsUnitOffDeliversNothing: a repository
// that refuses the pull-request endpoint cannot hold a delivery, so the
// walk does not ask it — an ordinary state the sweep already reads this
// way, not an enumeration failure that would take every forest with it
// (§forgejo/reconciliation/the-sweep,
// §forgejo/observability/the-warnings-a-forest-carries).
func TestForestWarnings_PullRequestsUnitOffDeliversNothing(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	member := warningsMember(33, LabelAgenticAutoMerge)
	member.Repository = "o/noswift"
	warningsForest(api, member)
	unitOff := testRepo("o/noswift")
	unitOff.HasPullRequests = false
	api.repos = append(api.repos, unitOff)
	api.deps[member.Number] = []Issue{{
		Number: 34, State: "closed", Repository: "o/noswift", UpdatedAt: testUpdatedAt,
	}}
	approvedDelivery(t, api, member, time.Now().Add(-3*time.Hour))

	forests, err := eng.enumerateForests()
	if err != nil {
		t.Fatalf("enumerateForests: %v", err)
	}
	if len(forests[0].Warnings) != 0 {
		t.Fatalf("warnings = %+v, want none from a repository that holds no pull request", forests[0].Warnings)
	}
	if n := api.callCount("ListOpenPullRequests:noswift"); n != 0 {
		t.Errorf("ListOpenPullRequests:noswift paid %d time(s); the unit is off", n)
	}
}

// TestForestWarnings_WarningsArePerReference: the payload's order is the
// walk's, and the walk walks one blocks listing, so one state enumerated
// twice answers alike; and a reference is warned on once, since one pull
// request delivering two members is one wait and two marks for it would
// read as two (§forgejo/observability/the-outcomes-view).
func TestForestWarnings_WarningsArePerReference(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	first := warningsMember(23, LabelHumanTask, LabelAgenticAutoMerge)
	second := warningsMember(24, LabelAgenticAutoMerge)
	api.repos = []Repository{testRepo("o/r"), outcomesRepo("o/out")}
	root := outcomesRoot(51, "UI for human review")
	tracker := outcomesTracker(71, "forest tracker")
	api.openIssues["out"] = []Issue{root, tracker}
	api.blocks[51] = []Issue{tracker, second, first}
	api.deps[first.Number] = nil
	api.deps[second.Number] = nil
	// One pull request delivering both members: the delivery relation
	// both members read (§forgejo/decisions/sync-set).
	pr := *testPR(900, "open", true)
	pr.Body = "Fixes " + first.Repository + "#" + strconv.Itoa(first.Number) +
		" and Fixes " + second.Repository + "#" + strconv.Itoa(second.Number)
	api.openPulls["r"] = []PullRequest{pr}
	api.pulls[pr.Number] = &pr
	api.reviews[pr.Number] = []Review{{
		Event: ReviewApproved, Author: "bob", CommitID: pr.HeadSHA,
		SubmittedAt: time.Now().Add(-3 * time.Hour),
	}}

	warns, err := eng.forestWarnings(&root, "o/out", api.blocks[51])
	if err != nil {
		t.Fatalf("forestWarnings: %v", err)
	}
	if len(warns) != 2 {
		t.Fatalf("warnings = %+v, want the human task and the one delivery it shares with the other member", warns)
	}
	if warns[0].Number != pr.Number || warns[0].Kind != WarningStaleAgenticMerge {
		t.Errorf("warnings[0] = %+v, want the delivering PR %d once, in the listing's order", warns[0], pr.Number)
	}
	if warns[1].Number != first.Number || warns[1].Kind != WarningHumanTaskBlocking {
		t.Errorf("warnings[1] = %+v, want the human task %d beside it", warns[1], first.Number)
	}
	again, err := eng.forestWarnings(&root, "o/out", api.blocks[51])
	if err != nil {
		t.Fatalf("forestWarnings: %v", err)
	}
	if len(again) != len(warns) {
		t.Fatalf("second enumeration = %+v, want %d warnings", again, len(warns))
	}
	for i := range warns {
		if again[i].Kind != warns[i].Kind || again[i].Repository != warns[i].Repository || again[i].Number != warns[i].Number {
			t.Errorf("second enumeration's warnings[%d] = %+v, want the first's %+v", i, again[i], warns[i])
		}
	}
}
