package forgejo

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// testRepo returns a fixture maitred-enabled repository
// (§forgejo/reconciliation/the-sweep).
func testRepo(fullName string) Repository {
	owner, name := splitRepo(fullName)
	return Repository{
		Name:     name,
		FullName: fullName,
		Owner:    owner,
		Topics:   []string{"maitred-enabled"},
		// The fixture repositories have the pull requests unit on, so
		// the sweep exercises the pull-request listing unless a test
		// restates the unit (§forgejo/reconciliation/the-sweep).
		HasPullRequests: true,
	}
}

// TestReconcile_CatchesMissedIssueOpened is the backstop case: an issue
// opened while the engine missed the webhook (no watermark) is caught by
// the sweep and dispatched (§forgejo/reconciliation/the-sweep).
func TestReconcile_CatchesMissedIssueOpened(t *testing.T) {
	eng, api, q, store := newTestEngine(t)
	api.repos = []Repository{testRepo("o/r")}
	issue := &Issue{Number: 7, State: "open", UpdatedAt: testUpdatedAt, Repository: "o/r", HTMLURL: "https://forge.example.com/o/r/issues/7"}
	api.issues[7] = issue
	api.openIssues["r"] = []Issue{*issue}

	eng.Reconcile()

	tasks := q.all()
	if len(tasks) != 1 {
		t.Fatalf("expected 1 dispatched task, got %d", len(tasks))
	}
	if !strings.Contains(tasks[0].Prompt, "action=implement") {
		t.Errorf("prompt = %q, want the implement action", tasks[0].Prompt)
	}
	w, err := store.Load(Key{Repo: "o/r", Kind: KindIssue, Number: 7})
	if err != nil {
		t.Fatalf("load watermark: %v", err)
	}
	if w == nil || w.Action != string(ActionImplement) {
		t.Errorf("watermark = %+v, want implement recorded", w)
	}
}

// TestReconcile_StaleWatermarkRedispatches: a watermark at an older
// revision than Forgejo's current state is reconciled — the delta is
// dispatched (§forgejo/reconciliation/the-sweep).
func TestReconcile_StaleWatermarkRedispatches(t *testing.T) {
	eng, api, q, store := newTestEngine(t)
	api.repos = []Repository{testRepo("o/r")}
	issue := &Issue{Number: 7, State: "open", UpdatedAt: testUpdatedAt, Repository: "o/r"}
	api.issues[7] = issue
	api.openIssues["r"] = []Issue{*issue}
	if err := store.Save(Key{Repo: "o/r", Kind: KindIssue, Number: 7}, Watermark{Action: string(ActionImplement), Revision: "2026-09-15T00:00:00Z"}); err != nil {
		t.Fatalf("save stale watermark: %v", err)
	}

	eng.Reconcile()

	if len(q.all()) != 1 {
		t.Fatalf("expected 1 re-dispatch at the newer revision, got %d", len(q.all()))
	}
}

// TestReconcile_FreshWatermarkImplementRefiresOnce: the sweep's
// implement row is state-governed — an open, unblocked, unlabelled
// issue with no connected pull request dispatches revision
// notwithstanding, so an attempt that died before opening a PR
// recovers instead of stranding at the watermark. The re-fire bound
// spaces it: a second sweep inside one reconcile interval holds off
// naming the bound, and once the interval is spent the same unchanged
// state dispatches again (§forgejo/reconciliation/the-state-is-the-watermark,
// §forgejo/decisions/loop-prevention).
func TestReconcile_FreshWatermarkImplementRefiresOnce(t *testing.T) {
	eng, api, q, store := newTestEngine(t)
	api.repos = []Repository{testRepo("o/r")}
	issue := &Issue{Number: 7, State: "open", UpdatedAt: testUpdatedAt, Repository: "o/r"}
	api.issues[7] = issue
	api.openIssues["r"] = []Issue{*issue}
	key := Key{Repo: "o/r", Kind: KindIssue, Number: 7}
	if err := store.Save(key, Watermark{Action: string(ActionImplement), Revision: issueRevision(issue)}); err != nil {
		t.Fatalf("save watermark: %v", err)
	}

	eng.Reconcile()

	if tasks := q.all(); len(tasks) != 1 {
		t.Fatalf("got %d tasks after the first sweep, want 1 (the state, not the revision, decides); all = %+v", len(tasks), tasks)
	}

	// A second sweep inside the interval the first one stamped is
	// bounded: the unchanged state waits the interval out.
	eng.Reconcile()
	if tasks := q.all(); len(tasks) != 1 {
		t.Fatalf("got %d tasks after the second sweep, want 1 (the re-fire is bounded); all = %+v", len(tasks), tasks)
	}

	// The interval the bound waits out is spent: the same unchanged
	// state dispatches again — the recovery pace a stranded attempt
	// was denied under the revision-keyed guard.
	eng.stampReFireAt(key, ActionImplement, issueRevision(issue), time.Now().Add(-eng.cfg.ReconcileInterval-time.Minute))
	eng.Reconcile()
	if tasks := q.all(); len(tasks) != 2 {
		t.Fatalf("got %d tasks after the interval is spent, want 2; all = %+v", len(tasks), tasks)
	}
}

// TestReconcile_RebaseRefiresAtUnmovedHeadBounded: the matching rebase
// row — an open PR the sweep finds not mergeable dispatches rebase
// whatever the watermark records at the unmoved head, and the re-fire
// bound spaces it to one dispatch per reconcile interval
// (§forgejo/reconciliation/the-state-is-the-watermark,
// §forgejo/decisions/actions).
func TestReconcile_RebaseRefiresAtUnmovedHeadBounded(t *testing.T) {
	eng, api, q, store := newTestEngine(t)
	api.repos = []Repository{testRepo("o/r")}
	pr := PullRequest{Number: 1, State: "open", Mergeable: false, HeadSHA: "sha-1"}
	api.pulls[1] = &pr
	api.openPulls["r"] = []PullRequest{pr}
	key := Key{Repo: "o/r", Kind: KindPR, Number: 1}
	if err := store.Save(key, Watermark{Action: string(ActionRebase), Revision: "sha-1"}); err != nil {
		t.Fatalf("save watermark: %v", err)
	}

	eng.Reconcile()

	tasks := q.all()
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks after the first sweep, want 1 (the unmoved head is the state that re-calls the rebase); all = %+v", len(tasks), tasks)
	}
	if !strings.Contains(tasks[0].Prompt, "action=rebase") {
		t.Errorf("prompt = %q, want the rebase action", tasks[0].Prompt)
	}

	eng.Reconcile()
	if tasks := q.all(); len(tasks) != 1 {
		t.Fatalf("got %d tasks after the second sweep, want 1 (the re-fire is bounded); all = %+v", len(tasks), tasks)
	}

	eng.stampReFireAt(key, ActionRebase, "sha-1", time.Now().Add(-eng.cfg.ReconcileInterval-time.Minute))
	eng.Reconcile()
	if tasks := q.all(); len(tasks) != 2 {
		t.Fatalf("got %d tasks after the interval is spent, want 2; all = %+v", len(tasks), tasks)
	}
}

// TestReconcile_RevisionGatedRowConsultsNoStamp: the re-fire bound's
// stamp is consulted only by state-governed rows. A revision-gated row
// — the event path's implement row with nothing recorded in the
// watermark — dispatches whatever a fresh stamp records, so generalising
// the bound changes no behaviour on the gated rows
// (§forgejo/decisions/loop-prevention,
// §forgejo/reconciliation/the-state-is-the-watermark).
func TestReconcile_RevisionGatedRowConsultsNoStamp(t *testing.T) {
	eng, api, q, _ := newTestEngine(t)
	api.repos = []Repository{testRepo("o/r")}
	issue := &Issue{Number: 7, State: "open", UpdatedAt: testUpdatedAt, Repository: "o/r"}
	api.issues[7] = issue
	key := Key{Repo: "o/r", Kind: KindIssue, Number: 7}
	// A stamp taken moments ago at this revision — the shape a
	// state-governed row leaves behind. The gated row must ignore it.
	eng.stampReFireAt(key, ActionImplement, issueRevision(issue), time.Now())

	eng.HandleEvent(Event{Type: EventIssueOpened, Repo: "o/r", Kind: KindIssue, Number: 7, Sender: "alice"})

	if tasks := q.all(); len(tasks) != 1 {
		t.Fatalf("got %d tasks, want 1 (a revision-gated row consults no stamp); all = %+v", len(tasks), tasks)
	}
}

// TestReconcile_PullRequests reconciles open pull requests through the
// same decision function: the review fan-out, its review-following rows, or rebase
// (§forgejo/reconciliation/the-sweep).
func TestReconcile_PullRequests(t *testing.T) {
	cases := []struct {
		name      string
		pr        PullRequest
		reviews   []Review
		watermark *Watermark
		want      string // empty: no dispatch
	}{
		{
			name: "mergeable, no review",
			pr:   PullRequest{Number: 1, State: "open", Mergeable: true, HeadSHA: "sha-1"},
			want: "action=review",
		},
		{
			name:    "mergeable, has a review",
			pr:      PullRequest{Number: 2, State: "open", Mergeable: true, HeadSHA: "sha-2"},
			reviews: []Review{{Event: ReviewApproved, Author: "bob", SubmittedAt: testUpdatedAt}},
			want:    "action=review",
		},
		{
			name: "not mergeable",
			pr:   PullRequest{Number: 3, State: "open", Mergeable: false, HeadSHA: "sha-3"},
			want: "action=rebase",
		},
		{
			// The sweep's review row is watermark-exempt: no review at
			// head is the state that calls for asking again, revision
			// notwithstanding (§forgejo/reconciliation/watermark-exemptions).
			name:      "review already dispatched at this revision re-fires while no review has landed",
			pr:        PullRequest{Number: 4, State: "open", Mergeable: true, HeadSHA: "sha-4"},
			watermark: &Watermark{Action: string(ActionReview), Revision: "sha-4"},
			want:      "action=review",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng, api, q, store := newTestEngine(t)
			api.repos = []Repository{testRepo("o/r")}
			api.pulls[tc.pr.Number] = &tc.pr
			api.openPulls["r"] = []PullRequest{tc.pr}
			api.reviews[tc.pr.Number] = tc.reviews
			if tc.watermark != nil {
				if err := store.Save(Key{Repo: "o/r", Kind: KindPR, Number: tc.pr.Number}, *tc.watermark); err != nil {
					t.Fatalf("save watermark: %v", err)
				}
			}

			eng.Reconcile()

			tasks := q.all()
			if tc.want == "" {
				if len(tasks) != 0 {
					t.Fatalf("expected no dispatch, got %d", len(tasks))
				}
				return
			}
			if len(tasks) != 1 {
				t.Fatalf("expected 1 dispatched task, got %d", len(tasks))
			}
			if !strings.Contains(tasks[0].Prompt, tc.want) {
				t.Errorf("prompt = %q, want %q", tasks[0].Prompt, tc.want)
			}
		})
	}
}

// TestReconcile_PRStandingAtReview: a PR whose last activity is a
// review — the latest review is a changes-requested, approved, or
// comment review submitted against the PR's current head — is
// reconciled so that the implementer is triggered, not the reviewer
// (§forgejo/decisions/transition-table).
func TestReconcile_PRStandingAtReview(t *testing.T) {
	cases := []struct {
		name      string
		review    Review
		labels    []string
		watermark *Watermark
		want      string // empty: no dispatch
	}{
		{
			name:   "changes-requested against the head dispatches fix-feedback",
			review: Review{Event: ReviewChangesRequested, Author: "bob", CommitID: "sha-1", SubmittedAt: testUpdatedAt},
			want:   "action=fix-feedback",
		},
		{
			name:   "approved against the head dispatches merge-or-wait",
			review: Review{Event: ReviewApproved, Author: "bob", CommitID: "sha-1", SubmittedAt: testUpdatedAt},
			labels: []string{LabelAgenticAutoMerge},
			want:   "action=merge-or-wait",
		},
		{
			name:   "comment against the head dispatches fix-feedback",
			review: Review{Event: ReviewComment, Author: "bob", CommitID: "sha-1", SubmittedAt: testUpdatedAt},
			want:   "action=fix-feedback",
		},
		{
			// The sweep's fix-feedback rows are watermark-exempt: an open,
			// mergeable PR standing at a changes-requested or comment review
			// at head is the state that calls for the implementer, revision
			// notwithstanding (§forgejo/reconciliation/watermark-exemptions).
			name:      "fix-feedback already dispatched at the revision re-fires while the changes-requested review stands at head",
			review:    Review{Event: ReviewChangesRequested, Author: "bob", CommitID: "sha-1", SubmittedAt: testUpdatedAt},
			watermark: &Watermark{Action: string(ActionFixFeedback), Revision: "sha-1"},
			want:      "action=fix-feedback",
		},
		{
			// The merge-or-wait row is watermark-exempt on every path once
			// the PR is opted in: the approved labelled PR at a frozen head
			// is the state that is its own watermark, sweep included
			// (s-git/maitred#197).
			name:      "merge-or-wait already dispatched at the revision re-fires while the labelled PR stands approved at head",
			review:    Review{Event: ReviewApproved, Author: "bob", CommitID: "sha-1", SubmittedAt: testUpdatedAt},
			labels:    []string{LabelAgenticAutoMerge},
			watermark: &Watermark{Action: string(ActionMergeOrWait), Revision: "sha-1"},
			want:      "action=merge-or-wait",
		},
		{
			// The merge opt-in is a precondition of the dispatch on
			// every path: an approved PR with no agentic-auto-merge
			// label waits on a human whatever the watermark records
			// (§forgejo/decisions/merge-gate, s-git/maitred#179).
			name:      "approved at the head without the opt-in waits on a human",
			review:    Review{Event: ReviewApproved, Author: "bob", CommitID: "sha-1", SubmittedAt: testUpdatedAt},
			watermark: &Watermark{Action: string(ActionMergeOrWait), Revision: "sha-1"},
			want:      "",
		},
		{
			name:      "comment review, fix-feedback already dispatched at the revision, re-fires",
			review:    Review{Event: ReviewComment, Author: "bob", CommitID: "sha-1", SubmittedAt: testUpdatedAt},
			watermark: &Watermark{Action: string(ActionFixFeedback), Revision: "sha-1"},
			want:      "action=fix-feedback",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng, api, q, store := newTestEngine(t)
			api.repos = []Repository{testRepo("o/r")}
			pr := PullRequest{Number: 1, State: "open", Mergeable: true, HeadSHA: "sha-1", Labels: tc.labels}
			api.pulls[1] = &pr
			api.openPulls["r"] = []PullRequest{pr}
			api.reviews[1] = []Review{tc.review}
			if tc.watermark != nil {
				if err := store.Save(Key{Repo: "o/r", Kind: KindPR, Number: 1}, *tc.watermark); err != nil {
					t.Fatalf("save watermark: %v", err)
				}
			}

			eng.Reconcile()

			tasks := q.all()
			if tc.want == "" {
				if len(tasks) != 0 {
					t.Fatalf("expected no dispatch, got %d", len(tasks))
				}
				return
			}
			if len(tasks) != 1 {
				t.Fatalf("expected 1 dispatched task, got %d", len(tasks))
			}
			if !strings.Contains(tasks[0].Prompt, tc.want) {
				t.Errorf("prompt = %q, want %q", tasks[0].Prompt, tc.want)
			}
		})
	}
}

// TestReconcile_SkipsDisabledRepositories enumerates only the
// maitred-enabled repositories (§forgejo/reconciliation/the-sweep).
func TestReconcile_SkipsDisabledRepositories(t *testing.T) {
	eng, api, q, _ := newTestEngine(t)
	api.repos = []Repository{
		testRepo("o/enabled"),
		{Name: "off", FullName: "o/off", Owner: "o", Topics: []string{"other"}},
	}
	issue := &Issue{Number: 7, State: "open", UpdatedAt: testUpdatedAt, Repository: "o/enabled"}
	api.issues[7] = issue
	api.openIssues["enabled"] = []Issue{*issue}

	eng.Reconcile()

	if n := api.callCount("ListOpenIssues:off"); n != 0 {
		t.Errorf("ListOpenIssues on the disabled repo called %d times, want 0", n)
	}
	if len(q.all()) != 1 {
		t.Fatalf("expected 1 dispatched task (the enabled repo's issue), got %d", len(q.all()))
	}
}

// TestReconcile_SkipsPullsListedAsIssues: the issue list includes pull
// requests (Forgejo represents them as issues); the sweep skips the ones
// marked as pull requests — the pull request list covers them
// (§forgejo/reconciliation/the-sweep).
func TestReconcile_SkipsPullsListedAsIssues(t *testing.T) {
	eng, api, q, _ := newTestEngine(t)
	api.repos = []Repository{testRepo("o/r")}
	api.openIssues["r"] = []Issue{{Number: 7, State: "open", UpdatedAt: testUpdatedAt, Repository: "o/r", IsPull: true}}
	api.openPulls["r"] = []PullRequest{{Number: 7, State: "open", Mergeable: true, HeadSHA: "sha-7"}}
	api.issues[7] = &Issue{Number: 7, State: "open", UpdatedAt: testUpdatedAt, Repository: "o/r", IsPull: true}
	api.pulls[7] = &PullRequest{Number: 7, State: "open", Mergeable: true, HeadSHA: "sha-7"}

	eng.Reconcile()

	if n := api.callCount("GetIssue:r/7"); n != 0 {
		t.Errorf("GetIssue for the PR called %d times, want 0 (it is reconciled as a PR)", n)
	}
	tasks := q.all()
	if len(tasks) != 1 {
		t.Fatalf("expected exactly 1 dispatched task (review for the PR), got %d", len(tasks))
	}
	if !strings.Contains(tasks[0].Prompt, "action=review") {
		t.Errorf("prompt = %q, want the review action", tasks[0].Prompt)
	}
}

// TestReconcile_SkipsPullsWhenUnitDisabled: a repository whose pull
// requests unit is off is never asked for its pull requests — the sweep
// reads the unit from the repository listing it already made — so the
// sweep logs no error and produces no pull-request decisions for it
// (§forgejo/reconciliation/the-sweep). A repository that refuses pull
// requests cannot hold one, so the listing is not a read that failed.
func TestReconcile_SkipsPullsWhenUnitDisabled(t *testing.T) {
	eng, api, q, _ := newTestEngine(t)
	off := testRepo("o/forests")
	off.HasPullRequests = false
	api.repos = []Repository{testRepo("o/r"), off}
	issue := &Issue{Number: 7, State: "open", UpdatedAt: testUpdatedAt, Repository: "o/forests"}
	api.issues[7] = issue
	api.openIssues["forests"] = []Issue{*issue}
	// A pull request that cannot exist in the unit-less repository, if
	// the sweep were to ask for it.
	api.openPulls["forests"] = []PullRequest{{Number: 3, State: "open", Mergeable: true, HeadSHA: "sha-3"}}
	api.pulls[3] = &PullRequest{Number: 3, State: "open", Mergeable: true, HeadSHA: "sha-3"}

	eng.Reconcile()

	if n := api.callCount("ListOpenPullRequests:forests"); n != 0 {
		t.Errorf("ListOpenPullRequests on the unit-less repo called %d times, want 0", n)
	}
	if n := api.callCount("GetIssue:forests/7"); n != 1 {
		t.Errorf("the unit-less repo's issue decided %d times, want 1 (issues are still read)", n)
	}
	tasks := q.all()
	if len(tasks) != 1 {
		t.Fatalf("expected only the issue's task, got %d: %v", len(tasks), tasks)
	}
	if strings.Contains(tasks[0].Prompt, "action=review") {
		t.Errorf("a pull-request decision was made for a repository with the unit off: %q", tasks[0].Prompt)
	}
}

// TestReconcile_PRListingErrorStillFailsWhenUnitEnabled: the unit check
// is a skip, not an exception carved out of the error path — where the
// repository has the pull requests unit, a failing pull-request listing
// (a 404 from a renamed or deleted repository included) is still an
// error, logged and carried on from, never read as "none"
// (§forgejo/reconciliation/failure-handling, §forgejo/client/errors).
func TestReconcile_PRListingErrorStillFailsWhenUnitEnabled(t *testing.T) {
	eng, api, q, _ := newTestEngine(t)
	api.repos = []Repository{testRepo("o/bad"), testRepo("o/good")}
	api.listPullsErr["bad"] = &APIError{StatusCode: 404, Message: "The target couldn't be found."}
	api.openPulls["good"] = []PullRequest{{Number: 1, State: "open", Mergeable: true, HeadSHA: "sha-1"}}
	api.pulls[1] = &PullRequest{Number: 1, State: "open", Mergeable: true, HeadSHA: "sha-1"}

	eng.Reconcile()

	if n := api.callCount("ListOpenPullRequests:bad"); n != 1 {
		t.Errorf("ListOpenPullRequests on the unit-enabled repo called %d times, want 1 (the error path is unchanged)", n)
	}
	if len(q.all()) != 1 {
		t.Fatalf("expected the good repo's pull request dispatched, got %d tasks", len(q.all()))
	}
}

// TestReconcile_RepoListingErrorContinues: a failure listing one
// repository's issues is logged and the sweep continues with the
// remaining repositories (§forgejo/reconciliation/failure-handling).
func TestReconcile_RepoListingErrorContinues(t *testing.T) {
	eng, api, q, _ := newTestEngine(t)
	api.repos = []Repository{testRepo("o/bad"), testRepo("o/good")}
	api.listIssuesErr["bad"] = errors.New("boom")
	issue := &Issue{Number: 7, State: "open", UpdatedAt: testUpdatedAt, Repository: "o/good"}
	api.issues[7] = issue
	api.openIssues["good"] = []Issue{*issue}

	eng.Reconcile()

	if len(q.all()) != 1 {
		t.Fatalf("expected the good repo's issue dispatched, got %d tasks", len(q.all()))
	}
}

// TestReconcile_PRListingErrorContinues: a failure listing one
// repository's pull requests is logged, and the sweep continues with the
// remaining repositories (§forgejo/reconciliation/failure-handling).
func TestReconcile_PRListingErrorContinues(t *testing.T) {
	eng, api, q, _ := newTestEngine(t)
	api.repos = []Repository{testRepo("o/bad"), testRepo("o/good")}
	api.listPullsErr["bad"] = errors.New("boom")
	api.openPulls["good"] = []PullRequest{{Number: 1, State: "open", Mergeable: true, HeadSHA: "sha-1"}}
	api.pulls[1] = &PullRequest{Number: 1, State: "open", Mergeable: true, HeadSHA: "sha-1"}

	eng.Reconcile()

	if len(q.all()) != 1 {
		t.Fatalf("expected the good repo's pull request dispatched, got %d tasks", len(q.all()))
	}
}

// TestReconcile_ListOrgRepositoriesError: a failure enumerating the org's
// repositories ends the sweep (logged); nothing is dispatched
// (§forgejo/reconciliation/failure-handling).
func TestReconcile_ListOrgRepositoriesError(t *testing.T) {
	eng, api, q, _ := newTestEngine(t)
	api.listReposErr = errors.New("boom")

	eng.Reconcile()

	if len(q.all()) != 0 {
		t.Fatalf("expected no dispatch, got %d", len(q.all()))
	}
}

// TestReconcile_WatermarkExemptRefiresAtUnchangedRevision: the sweep
// re-fires the watermark-exempt actions whenever the state calls for
// them, an unchanged updated_at notwithstanding — the dependency edge
// that unblocks the work emits no delivery and moves no revision
// (§forgejo/reconciliation/watermark-exemptions).
func TestReconcile_WatermarkExemptRefiresAtUnchangedRevision(t *testing.T) {
	cases := []struct {
		name   string
		labels []string
		action Action
	}{
		{"fully unblocked outcome tracker re-fires wrap-up", []string{LabelOutcome}, ActionWrapUp},
		{"open ideation issue re-fires decompose", []string{LabelIdeation}, ActionDecompose},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng, api, q, store := newTestEngine(t)
			api.repos = []Repository{testRepoBothRoles("o/r")}
			issue := &Issue{Number: 7, State: "open", UpdatedAt: testUpdatedAt, Repository: "o/r", Labels: tc.labels}
			api.issues[7] = issue
			api.openIssues["r"] = []Issue{*issue}
			key := Key{Repo: "o/r", Kind: KindIssue, Number: 7}
			if err := store.Save(key, Watermark{Action: string(tc.action), Revision: issueRevision(issue)}); err != nil {
				t.Fatalf("save watermark: %v", err)
			}

			eng.Reconcile()

			tasks := q.all()
			if len(tasks) != 1 {
				t.Fatalf("expected the exempt action re-fired at the unchanged revision, got %d tasks", len(tasks))
			}
			if !strings.Contains(tasks[0].Prompt, "action="+string(tc.action)) {
				t.Errorf("prompt = %q, want the %s action", tasks[0].Prompt, tc.action)
			}
		})
	}
}

// TestReconcile_ConcurrentEventNoDoubleDispatch: a concurrent event for
// the same key is in flight (holding the per-key lock in its re-fetch)
// when the sweep runs; exactly one dispatch happens
// (§forgejo/reconciliation/idempotency, §forgejo/state/concurrency).
func TestReconcile_ConcurrentEventNoDoubleDispatch(t *testing.T) {
	eng, api, q, _ := newTestEngine(t)
	api.repos = []Repository{testRepo("o/r")}
	issue := &Issue{Number: 7, State: "open", UpdatedAt: testUpdatedAt, Repository: "o/r"}
	api.issues[7] = issue
	api.openIssues["r"] = []Issue{*issue}
	release := make(chan struct{})
	entered := make(chan struct{})
	api.blockGetIssue = release
	api.getIssueEntered = entered

	done := make(chan struct{})
	go func() {
		eng.HandleEvent(Event{Type: EventIssueOpened, Repo: "o/r", Kind: KindIssue, Number: 7, Sender: "alice"})
		close(done)
	}()
	<-entered // the event path is inside its re-fetch, holding the key lock

	reconciled := make(chan struct{})
	go func() {
		eng.Reconcile()
		close(reconciled)
	}()
	// Wait for the sweep to have listed the issues: it is now at the
	// per-key lock (or past it, if the event path already finished).
	deadline := time.Now().Add(2 * time.Second)
	for api.callCount("ListOpenIssues:r") == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the sweep did not reach ListOpenIssues")
		}
		time.Sleep(time.Millisecond)
	}
	close(release)
	select {
	case <-reconciled:
	case <-time.After(5 * time.Second):
		t.Fatal("the sweep did not finish")
	}
	<-done

	if n := len(q.all()); n != 1 {
		t.Fatalf("expected exactly 1 dispatch, got %d", n)
	}
}

// TestReconcile_OverlappingSweepSkipped: a sweep that starts while
// another is in flight is skipped
// (§forgejo/reconciliation/scheduling).
func TestReconcile_OverlappingSweepSkipped(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	api.repos = []Repository{testRepo("o/r")}
	api.openIssues["r"] = []Issue{{Number: 7, State: "open", UpdatedAt: testUpdatedAt, Repository: "o/r"}}
	release := make(chan struct{})
	entered := make(chan struct{})
	api.blockGetIssue = release
	api.getIssueEntered = entered

	go eng.Reconcile() // first sweep: blocks in GetIssue, in flight
	<-entered

	returned := make(chan struct{})
	go func() {
		eng.Reconcile() // second sweep: must skip, not wait for the first
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("the second sweep did not skip the in-flight one")
	}
	close(release)
}

// TestEngine_StartStop_Sweeps: the engine runs the sweep once on start,
// then on every interval tick, and Stop halts it
// (§forgejo/reconciliation/scheduling).
func TestEngine_StartStop_Sweeps(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	api.repos = []Repository{testRepo("o/r")}
	eng.cfg.ReconcileInterval = 10 * time.Millisecond

	eng.Start()
	deadline := time.Now().Add(2 * time.Second)
	for api.callCount("ListOrgRepositories:") < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("expected at least two sweeps (start + tick), got %d", api.callCount("ListOrgRepositories:"))
		}
		time.Sleep(5 * time.Millisecond)
	}
	eng.Stop()
	eng.Stop() // idempotent
}

// TestEngine_StopWithoutStart is a no-op
// (§forgejo/reconciliation/scheduling).
func TestEngine_StopWithoutStart(t *testing.T) {
	eng, _, _, _ := newTestEngine(t)
	eng.Stop() // must not panic
}

// TestReconcile_VisitsOutcomesOnlyRepo: the sweep enumerates the union
// of both role sets, so an outcomes-only repository is reconciled too —
// a missed delivery there self-heals — even though implement is
// inadmissible for its objects and nothing dispatches
// (§forgejo/reconciliation/the-sweep, §forgejo/webhook/repository-roles).
func TestReconcile_VisitsOutcomesOnlyRepo(t *testing.T) {
	eng, api, q, _ := newTestEngine(t)
	api.repos = []Repository{testRepoOutcomes("o/out")}
	issue := &Issue{Number: 7, State: "open", UpdatedAt: testUpdatedAt, Repository: "o/out"}
	api.issues[7] = issue
	api.openIssues["out"] = []Issue{*issue}

	eng.Reconcile()

	if n := api.callCount("GetIssue"); n != 1 {
		t.Errorf("GetIssue called %d time(s), want 1: the sweep must visit an outcomes-only repository", n)
	}
	if tasks := q.all(); len(tasks) != 0 {
		t.Fatalf("got %d dispatched tasks from an outcomes-only repository, want none", len(tasks))
	}
}

// TestReconcile_VisitsBothRoleSets: one listing feeds both roles — a
// mixed org is swept in full: the enabled repository dispatches as
// ever, the outcomes-only one is visited and held off
// (§forgejo/reconciliation/the-sweep).
func TestReconcile_VisitsBothRoleSets(t *testing.T) {
	eng, api, q, _ := newTestEngine(t)
	api.repos = []Repository{testRepo("o/r"), testRepoOutcomes("o/out")}
	enabled := &Issue{Number: 7, State: "open", UpdatedAt: testUpdatedAt, Repository: "o/r"}
	outcomes := &Issue{Number: 8, State: "open", UpdatedAt: testUpdatedAt, Repository: "o/out"}
	api.issues[7] = enabled
	api.issues[8] = outcomes
	api.openIssues["r"] = []Issue{*enabled}
	api.openIssues["out"] = []Issue{*outcomes}

	eng.Reconcile()

	tasks := q.all()
	if len(tasks) != 1 {
		t.Fatalf("got %d dispatched tasks, want the single implement from the enabled repository", len(tasks))
	}
	if n := api.callCount("GetIssue"); n != 2 {
		t.Errorf("GetIssue called %d time(s), want 2: both repositories must be visited", n)
	}
	if n := api.callCount("ListOrgRepositories"); n != 1 {
		t.Errorf("ListOrgRepositories called %d time(s), want one listing feeding both roles", n)
	}
}

// TestReconcile_DecomposerAuthoredIssueDispatches: the sweep is the
// cascade's backstop — an open, unblocked member the decomposer
// authored, carrying no forest-seeding label, is dispatched by the
// sweep like any other work item: authorship is not a gate, the
// seeding label is (s-git/maitred#135,
// §forgejo/decisions/transition-table, §forgejo/reconciliation/the-sweep).
func TestReconcile_DecomposerAuthoredIssueDispatches(t *testing.T) {
	eng, api, q, store := newTestEngine(t)
	api.repos = []Repository{testRepo("o/r")}
	member := &Issue{Number: 7, State: "open", Author: "decomposer", UpdatedAt: testUpdatedAt, Repository: "o/r"}
	api.issues[7] = member
	api.openIssues["r"] = []Issue{*member}

	eng.Reconcile()

	tasks := q.all()
	if len(tasks) != 1 {
		t.Fatalf("got %d sweep tasks for an unblocked decomposer-authored member, want 1 (implement)", len(tasks))
	}
	if !strings.Contains(tasks[0].Prompt, "action=implement") {
		t.Errorf("prompt = %q, want the implement action", tasks[0].Prompt)
	}
	w, err := store.Load(Key{Repo: "o/r", Kind: KindIssue, Number: 7})
	if err != nil {
		t.Fatalf("load watermark: %v", err)
	}
	if w == nil {
		t.Errorf("watermark = nil, want the dispatched implement revision")
	}
}

// TestReconcile_ForestSeedingMemberHoldsOff: the companion of the
// sweep's backstop — while the seeding label stands the member holds
// off from the sweep too: the label, not the author, is the gate
// (§forgejo/decisions/transition-table, s-git/maitred#125).
func TestReconcile_ForestSeedingMemberHoldsOff(t *testing.T) {
	eng, api, q, store := newTestEngine(t)
	api.repos = []Repository{testRepo("o/r")}
	member := &Issue{Number: 7, State: "open", Author: "decomposer", UpdatedAt: testUpdatedAt, Repository: "o/r", Labels: []string{LabelForestSeeding}}
	api.issues[7] = member
	api.openIssues["r"] = []Issue{*member}

	eng.Reconcile()

	if tasks := q.all(); len(tasks) != 0 {
		t.Fatalf("got %d sweep tasks for a forest-seeding member, want none", len(tasks))
	}
	w, err := store.Load(Key{Repo: "o/r", Kind: KindIssue, Number: 7})
	if err != nil {
		t.Fatalf("load watermark: %v", err)
	}
	if w != nil {
		t.Errorf("watermark = %+v, want none", w)
	}
}

// TestReconcile_OtherAuthorIssueDispatches: the sweep still dispatches
// an unblocked issue authored by anyone else; authorship is read by
// nothing at all (§forgejo/decisions/loop-prevention,
// §forgejo/reconciliation/the-sweep).
func TestReconcile_OtherAuthorIssueDispatches(t *testing.T) {
	eng, api, q, _ := newTestEngine(t)
	api.repos = []Repository{testRepo("o/r")}
	issue := &Issue{Number: 7, State: "open", Author: "alice", UpdatedAt: testUpdatedAt, Repository: "o/r"}
	api.issues[7] = issue
	api.openIssues["r"] = []Issue{*issue}

	eng.Reconcile()

	if tasks := q.all(); len(tasks) != 1 {
		t.Fatalf("got %d sweep tasks, want 1 (implement)", len(tasks))
	}
}

// TestReconcile_ReviewRowsRefireAtUnchangedRevision: the sweep's rows
// for the lens fan-out and fix-feedback are watermark-exempt — the
// state is the watermark, so a dispatch the queue lost, a task that
// died, or a reviewer that finished without posting recovers on the
// next sweep while the head stands frozen at the recorded revision
// (§forgejo/reconciliation/watermark-exemptions).
func TestReconcile_ReviewRowsRefireAtUnchangedRevision(t *testing.T) {
	cases := []struct {
		name      string
		reviews   []Review
		watermark Watermark
		want      string
	}{
		{
			name:      "no reviews at all re-fires review",
			watermark: Watermark{Subjects: map[string]Slot{"lens": {Action: string(ActionReview), Revision: "sha-1"}}},
			want:      "action=review",
		},
		{
			name:      "latest review at an older revision re-fires the lens review",
			reviews:   []Review{{Event: ReviewApproved, Author: "bob", CommitID: "sha-0", SubmittedAt: testUpdatedAt}},
			watermark: Watermark{Subjects: map[string]Slot{"lens": {Action: string(ActionReview), Revision: "sha-1"}}},
			want:      "action=review",
		},
		{
			name:      "changes-requested at head re-fires fix-feedback",
			reviews:   []Review{{Event: ReviewChangesRequested, Author: "bob", CommitID: "sha-1", SubmittedAt: testUpdatedAt}},
			watermark: Watermark{Action: string(ActionFixFeedback), Revision: "sha-1"},
			want:      "action=fix-feedback",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng, api, q, store := newTestEngine(t)
			api.repos = []Repository{testRepo("o/r")}
			pr := PullRequest{Number: 1, State: "open", Mergeable: true, HeadSHA: "sha-1"}
			api.pulls[1] = &pr
			api.openPulls["r"] = []PullRequest{pr}
			api.reviews[1] = tc.reviews
			if err := store.Save(Key{Repo: "o/r", Kind: KindPR, Number: 1}, tc.watermark); err != nil {
				t.Fatalf("save watermark: %v", err)
			}

			eng.Reconcile()

			tasks := q.all()
			if len(tasks) != 1 {
				t.Fatalf("expected the exempt row to re-fire at the recorded revision, got %d tasks", len(tasks))
			}
			if !strings.Contains(tasks[0].Prompt, tc.want) {
				t.Errorf("prompt = %q, want %q", tasks[0].Prompt, tc.want)
			}
		})
	}
}

// TestReconcile_ApprovedAutoMergePRRefires: the sweep's merge-or-wait
// row is watermark-exempt for an open, approved-at-head PR whose work is
// opted into merging with no connected open blocker — the state is the
// watermark, so a PR that stalled at a frozen head sha recovers on the
// next sweep instead of sitting forever at "already dispatched at
// revision". The opt-in is read from the pull request or from the issue
// its body names as the one it delivers; without it the merge opt-in is
// the precondition that
// answers first: an approved PR with no agentic-auto-merge label on
// either seat waits on a human whatever the watermark records, so the
// revision-keyed guard never decides it (§forgejo/decisions/merge-gate,
// s-git/maitred#179; §forgejo/reconciliation/watermark-exemptions).
func TestReconcile_ApprovedAutoMergePRRefires(t *testing.T) {
	cases := []struct {
		name           string
		body           string
		labels         []string
		connectedLabel []string
		want           string
	}{
		{"auto-merge PR re-fires merge-or-wait", "", []string{LabelAgenticAutoMerge}, nil, "action=merge-or-wait"},
		{"PR whose delivered issue is opted in re-fires merge-or-wait", "Fixes o/r#4", nil, []string{LabelAgenticAutoMerge}, "action=merge-or-wait"},
		{"unlabelled PR approved at the head waits on a human", "", nil, nil, ""},
		{
			// The connected issue the body never claims to deliver — the
			// blocking-edge leg alone reaches it — is another work item's
			// authoring, and cannot opt this PR into merging
			// (§forgejo/decisions/merge-gate).
			name:           "PR connected to an opted-in issue it does not deliver waits on a human",
			connectedLabel: []string{LabelAgenticAutoMerge},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng, api, q, store := newTestEngine(t)
			api.repos = []Repository{testRepo("o/r")}
			pr := PullRequest{Number: 1, State: "open", Mergeable: true, HeadSHA: "sha-1", Body: tc.body, Labels: tc.labels}
			api.pulls[1] = &pr
			api.openPulls["r"] = []PullRequest{pr}
			api.reviews[1] = []Review{{Event: ReviewApproved, Author: "bob", CommitID: "sha-1", SubmittedAt: testUpdatedAt}}
			if tc.connectedLabel != nil {
				// The work item the PR delivers, opted into merging while
				// the PR itself carries no label — the shape that left a
				// fully approved PR in limbo until an operator labelled it
				// by hand (§forgejo/decisions/merge-gate). Where the body
				// names it as the delivery, that opt-in is the PR's own.
				api.blocks[1] = []Issue{{Number: 4, Repository: "o/r", State: "open", Title: "delivered issue", Labels: tc.connectedLabel}}
			}
			if err := store.Save(Key{Repo: "o/r", Kind: KindPR, Number: 1}, Watermark{Action: string(ActionMergeOrWait), Revision: "sha-1"}); err != nil {
				t.Fatalf("save watermark: %v", err)
			}

			eng.Reconcile()

			tasks := q.all()
			if tc.want == "" {
				if len(tasks) != 0 {
					t.Fatalf("expected no dispatch without the opt-in, got %d", len(tasks))
				}
				return
			}
			if len(tasks) != 1 {
				t.Fatalf("expected the exempt row to re-fire, got %d tasks", len(tasks))
			}
			if !strings.Contains(tasks[0].Prompt, tc.want) {
				t.Errorf("prompt = %q, want %q", tasks[0].Prompt, tc.want)
			}
		})
	}
}

// The digest gate's sweep fixtures (§forgejo/decisions/actionable-state-digest,
// s-git/maitred#209): a quiet forest — an open ideation root #1, its
// commissioned tracker #2 still blocked by the open root, an open member #4
// claimed as a human task, and blocker #9 standing open on the member.
// Nothing in the forest dispatches anything but the root's gated
// decompose row.

func quietForest(t *testing.T) (*Engine, *fakeAPI, *fakeQueue, *Store) {
	t.Helper()
	eng, api, q, store := newTestEngine(t)
	// The forest stands in an outcomes repository: decompose is
	// admitted only where the outcomes role stands
	// (§forgejo/webhook/repository-roles).
	api.repos = []Repository{{Name: "r", FullName: "o/r", Owner: "o", Topics: []string{"maitred-enabled", maitredOutcomesTopic}, HasPullRequests: true}}
	root := &Issue{Number: 1, State: "open", UpdatedAt: testUpdatedAt, Repository: "o/r", Labels: []string{LabelIdeation}}
	tracker := &Issue{Number: 2, State: "open", UpdatedAt: testUpdatedAt, Repository: "o/r", Labels: []string{LabelOutcome}}
	member := &Issue{Number: 4, State: "open", UpdatedAt: testUpdatedAt, Repository: "o/r", Labels: []string{LabelHumanTask}}
	blocker := Issue{Number: 9, State: "open", Repository: "o/r"}
	api.issues[1], api.issues[2], api.issues[4] = root, tracker, member
	api.blocks[1] = []Issue{*tracker, *member}
	api.deps[2] = []Issue{*root, *member}
	api.deps[4] = []Issue{blocker}
	api.openIssues["r"] = []Issue{*root, *tracker, *member}
	return eng, api, q, store
}

// TestReconcile_DecomposeDigestGate_QuietForest: two consecutive sweeps
// over a quiet forest cost one digest computation each and no second
// dispatch — the first observation commissions the run, the unchanged
// second buys silence; and the refresh leaves the forest slot holding
// the digest and its parts (§forgejo/reconciliation/watermark-exemptions,
// §forgejo/state/forest-digest, s-git/maitred#209).
func TestReconcile_DecomposeDigestGate_QuietForest(t *testing.T) {
	eng, api, q, store := quietForest(t)

	eng.Reconcile()
	tasks := q.all()
	if len(tasks) != 1 {
		t.Fatalf("first sweep dispatched %d tasks, want the one first-observation decompose", len(tasks))
	}
	if !strings.Contains(tasks[0].Prompt, "action=decompose") {
		t.Errorf("prompt = %q, want the decompose action", tasks[0].Prompt)
	}
	slot, err := store.Load(ForestKey("o/r", 1))
	if err != nil {
		t.Fatalf("load forest slot: %v", err)
	}
	if slot == nil || slot.Digest == "" || len(slot.DigestParts) == 0 {
		t.Fatalf("forest slot = %+v, want the refreshed digest and its parts", slot)
	}
	readsAfterOne := api.callCount("IssueBlocks:r/1")

	eng.Reconcile()
	if got := len(q.all()); got != 1 {
		t.Fatalf("quiet forest dispatched %d tasks across two sweeps, want 1", got)
	}
	if got := api.callCount("IssueBlocks:r/1"); got != readsAfterOne+1 {
		t.Errorf("second sweep cost %d forest reads, want one digest computation (%d total)", got-readsAfterOne, readsAfterOne+1)
	}
}

// TestReconcile_DecomposeDigestGate_CommissionedForest pins the sweep's
// commissioned-tracker row — the row that carries the curation once a
// forest's root has closed, which is the steady state the gate exists
// to make quiet: two sweeps over a commissioned quiet forest cost one
// digest computation each and no second dispatch, and the slot is
// keyed by the closed root the tracker's row fetched, not by the
// tracker whose row armed the gate
// (§forgejo/reconciliation/watermark-exemptions,
// §forgejo/state/forest-digest, s-git/maitred#209).
func TestReconcile_DecomposeDigestGate_CommissionedForest(t *testing.T) {
	eng, api, q, store := quietForest(t)
	// The forest is commissioned: closing the root removes the root's
	// own sweep row, so the tracker's gated row is the forest's pulse
	// (§forgejo/decisions/transition-table).
	api.mu.Lock()
	api.issues[1].State = "closed"
	api.deps[2][0].State = "closed" // the value copy of the root the tracker's dependencies carry
	api.openIssues["r"] = []Issue{*api.issues[2], *api.issues[4]}
	api.mu.Unlock()

	eng.Reconcile()
	tasks := q.all()
	if len(tasks) != 1 {
		t.Fatalf("first sweep dispatched %d tasks, want the one first-observation decompose from the tracker's row", len(tasks))
	}
	if !strings.Contains(tasks[0].Prompt, "action=decompose") {
		t.Errorf("prompt = %q, want the decompose action", tasks[0].Prompt)
	}
	// The slot is keyed by the root the row fetched whole, not by the
	// tracker the sweep walked: the tracker's row and the root's row
	// must ask the same forest's question
	// (§forgejo/state/forest-digest).
	slot, err := store.Load(ForestKey("o/r", 1))
	if err != nil {
		t.Fatalf("load forest slot: %v", err)
	}
	if slot == nil || slot.Digest == "" || len(slot.DigestParts) == 0 {
		t.Fatalf("root's forest slot = %+v, want the refreshed digest and its parts", slot)
	}
	if byTracker, err := store.Load(ForestKey("o/r", 2)); err != nil {
		t.Fatalf("load tracker forest slot: %v", err)
	} else if byTracker != nil {
		t.Errorf("tracker-keyed forest slot = %+v, want the digest stored under the root only", byTracker)
	}
	readsAfterOne := api.callCount("IssueBlocks:r/1")

	eng.Reconcile()
	if got := len(q.all()); got != 1 {
		t.Fatalf("commissioned quiet forest dispatched %d tasks across two sweeps, want 1", got)
	}
	if got := api.callCount("IssueBlocks:r/1"); got != readsAfterOne+1 {
		t.Errorf("second sweep cost %d forest reads, want one digest computation (%d total)", got-readsAfterOne, readsAfterOne+1)
	}
}

// TestReconcile_DecomposeDigestGate_MovedDigest: an actionable change
// between sweeps dispatches the decompose again — a blocker of a member
// closing is exactly the move the gate exists to catch
// (§forgejo/decisions/actionable-state-digest, s-git/maitred#209).
func TestReconcile_DecomposeDigestGate_MovedDigest(t *testing.T) {
	eng, api, q, store := quietForest(t)
	eng.Reconcile()

	api.mu.Lock()
	api.deps[4][0].State = "closed"
	api.mu.Unlock()

	eng.Reconcile()
	tasks := q.all()
	if len(tasks) != 2 {
		t.Fatalf("dispatched %d tasks, want a second decompose once the blocker closed", len(tasks))
	}
	if !strings.Contains(tasks[1].Prompt, "action=decompose") {
		t.Errorf("prompt = %q, want the decompose action", tasks[1].Prompt)
	}
	// The successful dispatch refreshes the slot: the next sweep's
	// comparison runs against what this one observed.
	slot, err := store.Load(ForestKey("o/r", 1))
	if err != nil {
		t.Fatalf("load forest slot: %v", err)
	}
	if slot == nil || slot.Digest == "" || len(slot.DigestParts) == 0 {
		t.Fatalf("forest slot = %+v, want the refreshed digest and its parts", slot)
	}
}

// TestReconcile_DecomposeDigestGate_FailedDispatchKeepsTheSlot pins the
// refresh's failure clause: a dispatch that never landed leaves the
// stale slot, so the next sweep re-observes the moved digest and
// retries the run the queue never got — the forest must not go quiet
// on a run that never reached the queue
// (§forgejo/state/forest-digest, §forgejo/webhook/the-pipeline,
// s-git/maitred#209).
func TestReconcile_DecomposeDigestGate_FailedDispatchKeepsTheSlot(t *testing.T) {
	eng, api, q, store := quietForest(t)
	eng.Reconcile()
	if got := len(q.all()); got != 1 {
		t.Fatalf("first sweep dispatched %d tasks, want the one first-observation decompose", got)
	}
	slot, err := store.Load(ForestKey("o/r", 1))
	if err != nil {
		t.Fatalf("load forest slot: %v", err)
	}
	if slot == nil || slot.Digest == "" || len(slot.DigestParts) == 0 {
		t.Fatalf("forest slot = %+v, want the refreshed digest and its parts", slot)
	}

	// The digest moves while the queue is down: the sweep dispatches
	// nothing and the slot stays exactly as the landed sweep left it.
	api.mu.Lock()
	api.deps[4][0].State = "closed"
	api.mu.Unlock()
	q.err = errors.New("queue down")
	eng.Reconcile()
	if got := len(q.all()); got != 1 {
		t.Fatalf("failing sweep landed %d tasks, want the first only", got)
	}
	after, err := store.Load(ForestKey("o/r", 1))
	if err != nil {
		t.Fatalf("load forest slot: %v", err)
	}
	if after.Digest != slot.Digest || len(after.DigestParts) != len(slot.DigestParts) {
		t.Errorf("failed dispatch moved the forest slot from %+v to %+v, want it byte-identical", slot, after)
	}

	// With the queue back, the next sweep sees the still-unequal
	// digests, lands the run the failure owed, and refreshes.
	q.err = nil
	eng.Reconcile()
	tasks := q.all()
	if len(tasks) != 2 {
		t.Fatalf("recovery sweep landed %d tasks, want the retried decompose", len(tasks))
	}
	if !strings.Contains(tasks[1].Prompt, "action=decompose") {
		t.Errorf("prompt = %q, want the decompose action", tasks[1].Prompt)
	}
	after, err = store.Load(ForestKey("o/r", 1))
	if err != nil {
		t.Fatalf("load forest slot: %v", err)
	}
	if after.Digest == slot.Digest {
		t.Errorf("recovery sweep left the stale digest %q, want the refreshed one", after.Digest)
	}
}

// TestReconcile_DecomposeDigestGate_FailedReadIsAHoldOff: a failed
// digest read fails the re-fetch — a loud hold-off the next sweep
// backstops, never a dispatch from a partial digest
// (§forgejo/decisions/actionable-state-digest, s-git/maitred#209).
func TestReconcile_DecomposeDigestGate_FailedReadIsAHoldOff(t *testing.T) {
	eng, api, q, _ := quietForest(t)
	api.issueBlocksErr = errors.New("blocks listing down")
	eng.Reconcile()
	if got := len(q.all()); got != 0 {
		t.Fatalf("dispatched %d tasks on a failed digest read, want none", got)
	}
}
