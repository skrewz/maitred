package forgejo

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// testRepoDisabled returns a fixture repository the engine must not act
// on: it holds no maitred role — it carries neither the maitred-enabled
// nor the maitred-outcomes-repo topic (§forgejo/webhook/repository-roles).
func testRepoDisabled(fullName string) Repository {
	owner, name := splitRepo(fullName)
	return Repository{
		Name:     name,
		FullName: fullName,
		Owner:    owner,
		Topics:   []string{"archive"},
	}
}

// newScopeEngine builds an engine whose org holds one maitred-enabled
// repository (o/r) and one that is deliberately outside the engine's
// remit (o/off).
func newScopeEngine(t *testing.T) (*Engine, *fakeAPI, *fakeQueue, *Store) {
	t.Helper()
	eng, api, q, store := newTestEngine(t)
	api.repos = []Repository{testRepo("o/r"), testRepoDisabled("o/off")}
	return eng, api, q, store
}

// TestHandleEvent_RepoWithoutRole_HoldsOff: the repository roles scope
// the event path as well as the sweep. An event for a repository
// holding no role holds off — naming the repository in the reason —
// without re-fetching, dispatching, or recording a watermark
// (§forgejo/webhook/the-pipeline).
func TestHandleEvent_RepoWithoutRole_HoldsOff(t *testing.T) {
	var log bytes.Buffer
	eng, api, q, store := newScopeEngineWithLogger(t, slog.New(slog.NewJSONHandler(&log, nil)))
	api.issues[7] = &Issue{Number: 7, State: "open", UpdatedAt: testUpdatedAt, Repository: "o/off"}

	eng.HandleEvent(Event{Type: EventIssueOpened, Repo: "o/off", Kind: KindIssue, Number: 7, Sender: "alice"})

	if tasks := q.all(); len(tasks) != 0 {
		t.Fatalf("got %d dispatched tasks for a repository outside the remit, want none", len(tasks))
	}
	if n := api.callCount("GetIssue"); n != 0 {
		t.Errorf("re-fetch happened for an un-enabled repository: %d GetIssue call(s)", n)
	}
	w, err := store.Load(Key{Repo: "o/off", Kind: KindIssue, Number: 7})
	if err != nil {
		t.Fatalf("load watermark: %v", err)
	}
	if w != nil {
		t.Errorf("watermark = %+v, want none", w)
	}
	entry := findEntry(t, parseLogLines(t, log.Bytes()), "decision")
	if dispatched, _ := entry["dispatched"].(bool); dispatched {
		t.Errorf("decision logged as dispatched: %v", entry)
	}
	reason, _ := entry["reason"].(string)
	if !strings.Contains(reason, "repo o/off holds no maitred role") {
		t.Errorf("hold-off reason = %q, want it to name the repository", reason)
	}
}

// newScopeEngineWithLogger is newScopeEngine over a JSON log, for
// asserting on the hold-off's structured decision entry.
func newScopeEngineWithLogger(t *testing.T, logger *slog.Logger) (*Engine, *fakeAPI, *fakeQueue, *Store) {
	t.Helper()
	api := newFakeAPI()
	api.repos = []Repository{testRepo("o/r"), testRepoDisabled("o/off")}
	q := &fakeQueue{}
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return NewEngine(api, store, testConfig(), q, logger), api, q, store
}

// TestHandleEvent_UnknownRepo_HoldsOff: a repository the engine has
// never seen fails closed — the scope test holds off rather than
// assuming the repository is enabled (§forgejo/webhook/the-scope-cache).
func TestHandleEvent_UnknownRepo_HoldsOff(t *testing.T) {
	eng, api, q, _ := newScopeEngine(t)
	api.issues[7] = testIssue(7, "open")

	eng.HandleEvent(Event{Type: EventIssueOpened, Repo: "o/unknown", Kind: KindIssue, Number: 7, Sender: "alice"})

	if tasks := q.all(); len(tasks) != 0 {
		t.Fatalf("got %d tasks for an unknown repository, want none", len(tasks))
	}
}

// TestHandleEvent_RepoEnabled_Unchanged: an event for a repository
// holding the work-item role behaves exactly as before
// (§forgejo/webhook/the-pipeline).
func TestHandleEvent_RepoEnabled_Unchanged(t *testing.T) {
	eng, api, q, store := newScopeEngine(t)
	api.issues[7] = testIssue(7, "open")

	eng.HandleEvent(Event{Type: EventIssueOpened, Repo: "o/r", Kind: KindIssue, Number: 7, Sender: "alice"})

	tasks := q.all()
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks, want 1", len(tasks))
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

// TestHandleEvent_NoCascadeForRepoWithoutRole: the scope test precedes
// the re-fetch, so a repository holding no role yields no cascade
// dispatches either (§forgejo/webhook/the-pipeline).
func TestHandleEvent_NoCascadeForRepoWithoutRole(t *testing.T) {
	eng, api, q, _ := newScopeEngine(t)
	closed := &Issue{Number: 7, State: "closed", UpdatedAt: testUpdatedAt, Repository: "o/off"}
	api.issues[7] = closed
	unblocked := &Issue{Number: 8, State: "open", UpdatedAt: testUpdatedAt, Repository: "o/off"}
	api.issues[8] = unblocked
	api.blocks[7] = []Issue{*unblocked}
	api.deps[8] = []Issue{*closed}

	eng.HandleEvent(Event{Type: EventIssueClosed, Repo: "o/off", Kind: KindIssue, Number: 7, Sender: "alice"})

	if tasks := q.all(); len(tasks) != 0 {
		t.Fatalf("got %d cascade tasks for a repository outside the remit, want none", len(tasks))
	}
}

// TestHandleEvent_ScopeCacheNoPerDeliveryRoundTrip: the scope test must
// not add an API round-trip to every delivery — the enabled set is
// cached for one reconcile interval
// (§forgejo/webhook/the-scope-cache).
func TestHandleEvent_ScopeCacheNoPerDeliveryRoundTrip(t *testing.T) {
	eng, api, q, _ := newScopeEngine(t)
	api.issues[7] = testIssue(7, "open")
	api.issues[8] = testIssue(8, "open")

	eng.HandleEvent(Event{Type: EventIssueOpened, Repo: "o/r", Kind: KindIssue, Number: 7, Sender: "alice"})
	eng.HandleEvent(Event{Type: EventIssueOpened, Repo: "o/r", Kind: KindIssue, Number: 8, Sender: "alice"})

	if len(q.all()) != 2 {
		t.Fatalf("got %d tasks, want 2", len(q.all()))
	}
	if n := api.callCount("ListOrgRepositories"); n != 1 {
		t.Errorf("ListOrgRepositories called %d time(s), want one cached lookup for both deliveries", n)
	}
}

// TestHandleEvent_ScopeCacheRefreshedWhenStale: a repository that has
// just been excluded must not be acted on for longer than the cache's
// staleness bound — once the cache is older than one reconcile interval
// it is refreshed before the decision (§forgejo/webhook/the-scope-cache).
func TestHandleEvent_ScopeCacheRefreshedWhenStale(t *testing.T) {
	eng, api, q, _ := newScopeEngine(t)
	api.issues[7] = testIssue(7, "open")

	eng.HandleEvent(Event{Type: EventIssueOpened, Repo: "o/r", Kind: KindIssue, Number: 7, Sender: "alice"})
	if len(q.all()) != 1 {
		t.Fatalf("setup: got %d tasks, want 1", len(q.all()))
	}

	// The topic is removed, and the cache has aged past one interval.
	api.repos = []Repository{testRepoDisabled("o/r")}
	eng.scopeAt = time.Time{}
	api.issues[8] = testIssue(8, "open")

	eng.HandleEvent(Event{Type: EventIssueOpened, Repo: "o/r", Kind: KindIssue, Number: 8, Sender: "alice"})

	if len(q.all()) != 1 {
		t.Fatalf("got %d tasks after the repository was excluded, want the original 1", len(q.all()))
	}
}

// TestDispatchCascade_KeyWithoutRoleHeldOff: a cross-repo unblock
// cascade must not start an agent in a repository holding no role, and
// the hold-off is recorded in the decision log for the cascade key like
// any other decision (§forgejo/webhook/repository-roles,
// §forgejo/webhook/the-scope-cache).
func TestDispatchCascade_KeyWithoutRoleHeldOff(t *testing.T) {
	var log bytes.Buffer
	eng, api, q, _ := newScopeEngineWithLogger(t, slog.New(slog.NewJSONHandler(&log, nil)))
	closed := &Issue{Number: 7, State: "closed", UpdatedAt: testUpdatedAt, Repository: "o/r"}
	api.issues[7] = closed
	unblocked := &Issue{Number: 8, State: "open", UpdatedAt: testUpdatedAt, Repository: "o/off"}
	api.issues[8] = unblocked
	api.blocks[7] = []Issue{*unblocked}
	api.deps[8] = []Issue{*closed}

	eng.HandleEvent(Event{Type: EventIssueClosed, Repo: "o/r", Kind: KindIssue, Number: 7, Sender: "alice"})

	if tasks := q.all(); len(tasks) != 0 {
		t.Fatalf("got %d cascade tasks into an un-enabled repository, want none", len(tasks))
	}
	var rec map[string]any
	for _, e := range parseLogLines(t, log.Bytes()) {
		if e["msg"] == "decision" && e["key"] == "o/off/issue/8" {
			rec = e
		}
	}
	if rec == nil {
		t.Fatalf("cascade key has no decision-log entry, want its hold-off recorded")
	}
	if dispatched, _ := rec["dispatched"].(bool); dispatched {
		t.Errorf("cascade key logged as dispatched: %v", rec)
	}
	if reason, _ := rec["reason"].(string); !strings.Contains(reason, "repo o/off holds no maitred role") {
		t.Errorf("cascade hold-off reason = %q, want it to name the repository", reason)
	}
}

// TestHandleEvent_ListingFailure_HoldsOff: a repository listing that
// fails is a hold-off whose reason names the listing failure — not a
// "holds no maitred role" exclusion a transient API blip would masquerade
// as — with no re-fetch and no dispatch (§forgejo/webhook/the-scope-cache).
func TestHandleEvent_ListingFailure_HoldsOff(t *testing.T) {
	var log bytes.Buffer
	api := newFakeAPI()
	api.repos = []Repository{testRepo("o/r")}
	api.listReposErr = errors.New("boom")
	q := &fakeQueue{}
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	eng := NewEngine(api, store, testConfig(), q, slog.New(slog.NewJSONHandler(&log, nil)))
	api.issues[7] = testIssue(7, "open")

	eng.HandleEvent(Event{Type: EventIssueOpened, Repo: "o/r", Kind: KindIssue, Number: 7, Sender: "alice"})

	if tasks := q.all(); len(tasks) != 0 {
		t.Fatalf("got %d tasks after a failed listing, want none", len(tasks))
	}
	if n := api.callCount("GetIssue"); n != 0 {
		t.Errorf("re-fetch happened after a failed listing: %d GetIssue call(s)", n)
	}
	entry := findEntry(t, parseLogLines(t, log.Bytes()), "decision")
	if dispatched, _ := entry["dispatched"].(bool); dispatched {
		t.Errorf("decision logged as dispatched: %v", entry)
	}
	reason, _ := entry["reason"].(string)
	if !strings.Contains(reason, "scope of repo o/r unknown") || !strings.Contains(reason, "boom") {
		t.Errorf("hold-off reason = %q, want it to name the listing failure", reason)
	}
	if strings.Contains(reason, "holds no maitred role") {
		t.Errorf("failed listing reported as an exclusion: %q", reason)
	}
}

// TestHandleEvent_ListingFailureRetriesBackoff: a failed listing must
// not turn into one full org listing per delivery — while the failure
// is within the retry backoff, later deliveries hold off without
// re-attempting; once past it, one retry happens
// (§forgejo/webhook/the-scope-cache).
func TestHandleEvent_ListingFailureRetriesBackoff(t *testing.T) {
	api := newFakeAPI()
	api.repos = []Repository{testRepo("o/r")}
	api.listReposErr = errors.New("boom")
	q := &fakeQueue{}
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	eng := NewEngine(api, store, testConfig(), q, slog.New(slog.NewTextHandler(io.Discard, nil)))

	for n := 7; n <= 9; n++ {
		eng.HandleEvent(Event{Type: EventIssueOpened, Repo: "o/r", Kind: KindIssue, Number: n, Sender: "alice"})
	}
	if n := api.callCount("ListOrgRepositories"); n != 1 {
		t.Errorf("ListOrgRepositories called %d time(s) during the failure backoff, want 1", n)
	}
	if len(q.all()) != 0 {
		t.Errorf("got %d tasks during a failed listing, want none", len(q.all()))
	}

	// Past the backoff, the next delivery re-attempts the listing once.
	eng.scopeMu.Lock()
	eng.scopeFailedAt = time.Time{}
	eng.scopeMu.Unlock()
	eng.HandleEvent(Event{Type: EventIssueOpened, Repo: "o/r", Kind: KindIssue, Number: 10, Sender: "alice"})
	if n := api.callCount("ListOrgRepositories"); n != 2 {
		t.Errorf("ListOrgRepositories called %d time(s) past the backoff, want 2", n)
	}
}

// TestHandleEvent_ScopeCacheStaleSingleListing: deliveries that find
// the cache stale concurrently must share one listing — the refresh is
// single-flight, not one listing per waiting delivery
// (§forgejo/webhook/the-scope-cache).
func TestHandleEvent_ScopeCacheStaleSingleListing(t *testing.T) {
	eng, api, _, _ := newScopeEngine(t)
	for n := 7; n <= 9; n++ {
		api.issues[n] = testIssue(n, "open")
	}
	eng.scopeMu.Lock()
	eng.scopeAt = time.Time{} // force every delivery to find the cache stale
	eng.scopeMu.Unlock()
	release := make(chan struct{})
	api.blockListRepos = release
	api.listReposEntered = make(chan struct{})

	var wg sync.WaitGroup
	for n := 7; n <= 9; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			eng.HandleEvent(Event{Type: EventIssueOpened, Repo: "o/r", Kind: KindIssue, Number: n, Sender: "alice"})
		}(n)
	}
	<-api.listReposEntered // the first delivery is inside the listing
	time.Sleep(50 * time.Millisecond)
	close(release) // the others, having found the cache stale, re-check under the refresh lock
	wg.Wait()

	if n := api.callCount("ListOrgRepositories"); n != 1 {
		t.Errorf("ListOrgRepositories called %d time(s) for three concurrent stale deliveries, want 1", n)
	}
}

// TestReconcile_RefreshesScopeCache: the sweep already has the org's
// repositories in hand, so it seeds the scope cache — an event for an
// enabled repository then needs no repository listing of its own
// (§forgejo/reconciliation/the-sweep).
func TestReconcile_RefreshesScopeCache(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	api.repos = []Repository{testRepo("o/r")}
	eng.Reconcile()

	if n := api.callCount("ListOrgRepositories"); n != 1 {
		t.Fatalf("setup: ListOrgRepositories called %d time(s), want 1", n)
	}
	api.issues[7] = testIssue(7, "open")
	eng.HandleEvent(Event{Type: EventIssueOpened, Repo: "o/r", Kind: KindIssue, Number: 7, Sender: "alice"})

	if n := api.callCount("ListOrgRepositories"); n != 1 {
		t.Errorf("ListOrgRepositories called %d time(s) after the sweep, want the sweep's cache to be reused", n)
	}
}

// TestHandleEvent_IgnoredEventNeedsNoScopeLookup: the scope test is a
// stage after filtering, so a delivery that is cheaply ignored does not
// trigger a repository listing (§forgejo/webhook/the-pipeline).
func TestHandleEvent_IgnoredEventNeedsNoScopeLookup(t *testing.T) {
	eng, api, _, _ := newScopeEngine(t)

	serve(t, NewHandler(eng, "s3cret", slog.New(slog.NewTextHandler(io.Discard, nil))),
		signedRequest(t, "s3cret", "push", "", issuePayload("o/off", 7, "created")))

	if n := api.callCount("ListOrgRepositories"); n != 0 {
		t.Errorf("ListOrgRepositories called %d time(s) for an ignored delivery, want 0", n)
	}
}

// testRepoOutcomes returns a fixture repository holding only the
// outcomes role: it carries the maitred-outcomes-repo topic and not
// maitred-enabled (§forgejo/webhook/repository-roles).
func testRepoOutcomes(fullName string) Repository {
	owner, name := splitRepo(fullName)
	return Repository{
		Name:     name,
		FullName: fullName,
		Owner:    owner,
		Topics:   []string{"maitred-outcomes-repo"},
	}
}

// testRepoBothRoles returns a fixture repository holding both roles
// (§forgejo/webhook/repository-roles).
func testRepoBothRoles(fullName string) Repository {
	owner, name := splitRepo(fullName)
	return Repository{
		Name:     name,
		FullName: fullName,
		Owner:    owner,
		Topics:   []string{"maitred-enabled", "maitred-outcomes-repo"},
	}
}

// TestHandleEvent_OutcomesOnlyRepo_ImplementInadmissible: an
// outcomes-only repository is admitted — its objects are considered, so
// the re-fetch happens — but implement is inadmissible there: the
// decision is a hold-off naming the action and the required role, with
// no dispatch and no watermark (§forgejo/webhook/repository-roles).
func TestHandleEvent_OutcomesOnlyRepo_ImplementInadmissible(t *testing.T) {
	var log bytes.Buffer
	api := newFakeAPI()
	api.repos = []Repository{testRepo("o/r"), testRepoOutcomes("o/out")}
	q := &fakeQueue{}
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	eng := NewEngine(api, store, testConfig(), q, slog.New(slog.NewJSONHandler(&log, nil)))
	api.issues[7] = &Issue{Number: 7, State: "open", UpdatedAt: testUpdatedAt, Repository: "o/out"}

	eng.HandleEvent(Event{Type: EventIssueOpened, Repo: "o/out", Kind: KindIssue, Number: 7, Sender: "alice"})

	if tasks := q.all(); len(tasks) != 0 {
		t.Fatalf("got %d dispatched tasks for an outcomes-only repository, want none", len(tasks))
	}
	if n := api.callCount("GetIssue"); n != 1 {
		t.Errorf("GetIssue called %d time(s), want 1: the delivery must be admitted and re-fetched", n)
	}
	w, err := store.Load(Key{Repo: "o/out", Kind: KindIssue, Number: 7})
	if err != nil {
		t.Fatalf("load watermark: %v", err)
	}
	if w != nil {
		t.Errorf("watermark = %+v, want none", w)
	}
	entry := findEntry(t, parseLogLines(t, log.Bytes()), "decision")
	if dispatched, _ := entry["dispatched"].(bool); dispatched {
		t.Errorf("decision logged as dispatched: %v", entry)
	}
	reason, _ := entry["reason"].(string)
	if !strings.Contains(reason, "implement is inadmissible") || !strings.Contains(reason, "maitred-enabled") {
		t.Errorf("hold-off reason = %q, want it to name the action and the required role's topic", reason)
	}
}

// TestHandleEvent_OutcomesOnlyRepo_ReviewInadmissible: the PR actions
// belong to the work-item role, so a PR in an outcomes-only repository
// is admitted but never reviewed (§forgejo/webhook/repository-roles).
func TestHandleEvent_OutcomesOnlyRepo_ReviewInadmissible(t *testing.T) {
	var log bytes.Buffer
	api := newFakeAPI()
	api.repos = []Repository{testRepoOutcomes("o/out")}
	q := &fakeQueue{}
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	eng := NewEngine(api, store, testConfig(), q, slog.New(slog.NewJSONHandler(&log, nil)))
	api.pulls[9] = &PullRequest{Number: 9, State: "open", Mergeable: true, HeadSHA: "abc"}

	eng.HandleEvent(Event{Type: EventPROpened, Repo: "o/out", Kind: KindPR, Number: 9, Sender: "alice"})

	if tasks := q.all(); len(tasks) != 0 {
		t.Fatalf("got %d dispatched tasks for an outcomes-only repository, want none", len(tasks))
	}
	if n := api.callCount("GetPullRequest"); n != 1 {
		t.Errorf("GetPullRequest called %d time(s), want 1: the delivery must be admitted and re-fetched", n)
	}
	entry := findEntry(t, parseLogLines(t, log.Bytes()), "decision")
	reason, _ := entry["reason"].(string)
	if !strings.Contains(reason, "review is inadmissible") || !strings.Contains(reason, "maitred-enabled") {
		t.Errorf("hold-off reason = %q, want it to name the action and maitred-enabled", reason)
	}
}

// TestHandleEvent_BothRolesRepo_Unchanged: a repository holding both
// roles admits its work items exactly as an enabled one — admission is
// by any role, admissibility by the object's role table
// (§forgejo/webhook/repository-roles).
func TestHandleEvent_BothRolesRepo_Unchanged(t *testing.T) {
	api := newFakeAPI()
	api.repos = []Repository{testRepoBothRoles("o/both")}
	q := &fakeQueue{}
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	eng := NewEngine(api, store, testConfig(), q, slog.New(slog.NewTextHandler(io.Discard, nil)))
	api.issues[7] = &Issue{Number: 7, State: "open", UpdatedAt: testUpdatedAt, Repository: "o/both"}

	eng.HandleEvent(Event{Type: EventIssueOpened, Repo: "o/both", Kind: KindIssue, Number: 7, Sender: "alice"})

	if tasks := q.all(); len(tasks) != 1 {
		t.Fatalf("got %d tasks, want 1: a both-roles repository is a work-item source too", len(tasks))
	}
	w, err := store.Load(Key{Repo: "o/both", Kind: KindIssue, Number: 7})
	if err != nil {
		t.Fatalf("load watermark: %v", err)
	}
	if w == nil || w.Action != string(ActionImplement) {
		t.Errorf("watermark = %+v, want implement recorded", w)
	}
}

// TestDispatchCascade_OutcomesRootEnabledMember: a cascade rooted at a
// closed issue in an outcomes-only repository dispatches implement for
// a newly unblocked member in an enabled repository — admissibility is
// judged by the repository holding the object under consideration,
// never the repository the event arrived from
// (§forgejo/webhook/repository-roles).
func TestDispatchCascade_OutcomesRootEnabledMember(t *testing.T) {
	api := newFakeAPI()
	api.repos = []Repository{testRepo("o/r"), testRepoOutcomes("o/out")}
	q := &fakeQueue{}
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	eng := NewEngine(api, store, testConfig(), q, slog.New(slog.NewTextHandler(io.Discard, nil)))
	closed := &Issue{Number: 7, State: "closed", UpdatedAt: testUpdatedAt, Repository: "o/out"}
	api.issues[7] = closed
	member := &Issue{Number: 8, State: "open", UpdatedAt: testUpdatedAt, Repository: "o/r"}
	api.issues[8] = member
	api.blocks[7] = []Issue{*member}
	api.deps[8] = []Issue{*closed}

	eng.HandleEvent(Event{Type: EventIssueClosed, Repo: "o/out", Kind: KindIssue, Number: 7, Sender: "alice"})

	tasks := q.all()
	if len(tasks) != 1 {
		t.Fatalf("got %d cascade tasks, want 1 into the enabled repository", len(tasks))
	}
	if !strings.Contains(tasks[0].Prompt, "action=implement") {
		t.Errorf("prompt = %q, want the implement action", tasks[0].Prompt)
	}
	w, err := store.Load(Key{Repo: "o/r", Kind: KindIssue, Number: 8})
	if err != nil {
		t.Fatalf("load watermark: %v", err)
	}
	if w == nil || w.Action != string(ActionImplement) {
		t.Errorf("watermark = %+v, want implement recorded for the member", w)
	}
}

// TestDispatchCascade_OutcomesMemberHeldOff: a cascade key in an
// outcomes-only repository is not an implement dispatch — the hold-off
// names the action and the required role, and is recorded in the
// decision log for the cascade key
// (§forgejo/webhook/repository-roles).
func TestDispatchCascade_OutcomesMemberHeldOff(t *testing.T) {
	var log bytes.Buffer
	api := newFakeAPI()
	api.repos = []Repository{testRepo("o/r"), testRepoOutcomes("o/out")}
	q := &fakeQueue{}
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	eng := NewEngine(api, store, testConfig(), q, slog.New(slog.NewJSONHandler(&log, nil)))
	closed := &Issue{Number: 7, State: "closed", UpdatedAt: testUpdatedAt, Repository: "o/r"}
	api.issues[7] = closed
	member := &Issue{Number: 8, State: "open", UpdatedAt: testUpdatedAt, Repository: "o/out"}
	api.issues[8] = member
	api.blocks[7] = []Issue{*member}
	api.deps[8] = []Issue{*closed}

	eng.HandleEvent(Event{Type: EventIssueClosed, Repo: "o/r", Kind: KindIssue, Number: 7, Sender: "alice"})

	if tasks := q.all(); len(tasks) != 0 {
		t.Fatalf("got %d cascade tasks into an outcomes-only repository, want none", len(tasks))
	}
	var rec map[string]any
	for _, e := range parseLogLines(t, log.Bytes()) {
		if e["msg"] == "decision" && e["key"] == "o/out/issue/8" {
			rec = e
		}
	}
	if rec == nil {
		t.Fatalf("cascade key has no decision-log entry, want its hold-off recorded")
	}
	reason, _ := rec["reason"].(string)
	if !strings.Contains(reason, "implement is inadmissible") || !strings.Contains(reason, "maitred-enabled") {
		t.Errorf("cascade hold-off reason = %q, want it to name the action and the required role's topic", reason)
	}
}
