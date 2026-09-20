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

// TestReconcile_FreshWatermarkNoRedispatch: an already-dispatched
// (action, revision) is not re-dispatched
// (§forgejo/reconciliation/idempotency).
func TestReconcile_FreshWatermarkNoRedispatch(t *testing.T) {
	eng, api, q, store := newTestEngine(t)
	api.repos = []Repository{testRepo("o/r")}
	issue := &Issue{Number: 7, State: "open", UpdatedAt: testUpdatedAt, Repository: "o/r"}
	api.issues[7] = issue
	api.openIssues["r"] = []Issue{*issue}
	if err := store.Save(Key{Repo: "o/r", Kind: KindIssue, Number: 7}, Watermark{Action: string(ActionImplement), Revision: issueRevision(issue)}); err != nil {
		t.Fatalf("save watermark: %v", err)
	}

	eng.Reconcile()

	if len(q.all()) != 0 {
		t.Fatalf("expected no re-dispatch, got %d", len(q.all()))
	}
}

// TestReconcile_PullRequests reconciles open pull requests through the
// same decision function: review, re-review, or rebase
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
			want:    "action=re-review",
		},
		{
			name: "not mergeable",
			pr:   PullRequest{Number: 3, State: "open", Mergeable: false, HeadSHA: "sha-3"},
			want: "action=rebase",
		},
		{
			name:      "already reviewed at this revision",
			pr:        PullRequest{Number: 4, State: "open", Mergeable: true, HeadSHA: "sha-4"},
			watermark: &Watermark{Action: string(ActionReview), Revision: "sha-4"},
			want:      "",
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
	api.err = errors.New("boom")

	eng.Reconcile()

	if len(q.all()) != 0 {
		t.Fatalf("expected no dispatch, got %d", len(q.all()))
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
