package forgejo

import (
	"bytes"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// testRepoDisabled returns a fixture repository the engine must not act
// on: it does not carry the maitred-enabled topic
// (§forgejo/webhook/the-scope-cache).
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

// TestHandleEvent_RepoNotEnabled_HoldsOff: the maitred-enabled topic
// scopes the event path as well as the sweep. An event for a repository
// without the topic holds off — naming the repository in the reason —
// without re-fetching, dispatching, or recording a watermark
// (§forgejo/webhook/the-pipeline).
func TestHandleEvent_RepoNotEnabled_HoldsOff(t *testing.T) {
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
	if !strings.Contains(reason, "repo o/off is not maitred-enabled") {
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
// carrying the topic behaves exactly as before (§forgejo/webhook/the-pipeline).
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

// TestHandleEvent_NoCascadeForUnenabledRepo: the scope test precedes the
// re-fetch, so an un-enabled repository yields no cascade dispatches
// either (§forgejo/webhook/the-pipeline).
func TestHandleEvent_NoCascadeForUnenabledRepo(t *testing.T) {
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

// TestDispatchCascade_UnenabledRepoHeldOff: a cross-repo unblock
// cascade must not start an agent in a repository the sweep deliberately
// skips (§forgejo/webhook/the-scope-cache).
func TestDispatchCascade_UnenabledRepoHeldOff(t *testing.T) {
	eng, api, q, _ := newScopeEngine(t)
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
