package forgejo

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

// issueURL is the html_url the real API carries on every issue row;
// the fake fills it only on the listing, so forest fixtures state it —
// both the root and the blocks-carried tracker must show one
// (§forgejo/observability/the-outcomes-view).
func issueURL(number int) string {
	return "https://forge.example.com/o/out/issues/" + strconv.Itoa(number)
}

// outcomesRepo returns a fixture repository holding the outcomes role and
// nothing else (§forgejo/webhook/repository-roles,
// §forgejo/observability/the-outcomes-view).
func outcomesRepo(fullName string) Repository {
	owner, name := splitRepo(fullName)
	return Repository{
		Name:            name,
		FullName:        fullName,
		Owner:           owner,
		Topics:          []string{maitredOutcomesTopic},
		HasPullRequests: true,
	}
}

// outcomesRoot builds an open issue carrying the reserved ideation label.
func outcomesRoot(number int, title string) Issue {
	return Issue{
		Number:    number,
		Title:     title,
		State:     "open",
		Labels:    []string{LabelIdeation},
		UpdatedAt: testUpdatedAt,
		HTMLURL:   issueURL(number),
	}
}

// outcomesTracker builds an open issue carrying the reserved outcome label.
func outcomesTracker(number int, title string) Issue {
	return Issue{
		Number:    number,
		Title:     title,
		State:     "open",
		Labels:    []string{LabelOutcome},
		UpdatedAt: testUpdatedAt,
		HTMLURL:   issueURL(number),
	}
}

// TestForests_PairsRootWithTracker: a root and the outcome tracker it
// blocks are listed together, with the repository, the root's fields, and
// the tracker's fields all stated (§forgejo/observability/the-outcomes-view).
func TestForests_PairsRootWithTracker(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	api.repos = []Repository{outcomesRepo("o/out")}
	root := outcomesRoot(51, "UI for human review")
	tracker := outcomesTracker(71, "forest tracker")
	// The tracker is open in the same listing: it must pair into the
	// forest, never stand as a forest of its own.
	api.openIssues["out"] = []Issue{root, tracker}
	api.blocks[51] = []Issue{tracker}

	forests, err := eng.enumerateForests()
	if err != nil {
		t.Fatalf("enumerateForests: %v", err)
	}
	if len(forests) != 1 {
		t.Fatalf("forests = %d, want 1 (the tracker is no forest of its own)", len(forests))
	}
	f := forests[0]
	if f.Repository != "o/out" {
		t.Errorf("repository = %q, want o/out", f.Repository)
	}
	if f.Root.Number != 51 || f.Root.Title != "UI for human review" || f.Root.State != "open" {
		t.Errorf("root = %+v, want ideation issue 51 open", f.Root)
	}
	if !strings.HasSuffix(f.Root.HTMLURL, "/o/out/issues/51") {
		t.Errorf("root html_url = %q, want the root's URL", f.Root.HTMLURL)
	}
	if f.Tracker == nil {
		t.Fatal("tracker = nil, want the outcome issue the root blocks")
	}
	if f.Tracker.Number != 71 || f.Tracker.Title != "forest tracker" || f.Tracker.State != "open" {
		t.Errorf("tracker = %+v, want outcome issue 71 open", f.Tracker)
	}
	if !strings.HasSuffix(f.Tracker.HTMLURL, "/o/out/issues/71") {
		t.Errorf("tracker html_url = %q, want the tracker's URL", f.Tracker.HTMLURL)
	}
}

// TestForests_MissingTrackerStated: a root whose tracker is missing is
// listed with the absence stated rather than papered over
// (§forgejo/observability/the-outcomes-view).
func TestForests_MissingTrackerStated(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	api.repos = []Repository{outcomesRepo("o/out")}
	rootNoEdges := outcomesRoot(1, "blocks nothing")
	rootWrongEdge := outcomesRoot(2, "blocks a plain issue")
	plain := Issue{Number: 3, Title: "plain work item", State: "open", UpdatedAt: testUpdatedAt}
	api.openIssues["out"] = []Issue{rootNoEdges, rootWrongEdge, plain}
	api.blocks[2] = []Issue{plain}

	forests, err := eng.enumerateForests()
	if err != nil {
		t.Fatalf("enumerateForests: %v", err)
	}
	if len(forests) != 2 {
		t.Fatalf("forests = %d, want both roots listed", len(forests))
	}
	for _, f := range forests {
		if f.Tracker != nil {
			t.Errorf("root %d tracker = %+v, want the absence stated", f.Root.Number, f.Tracker)
		}
	}
}

// TestForests_UnpairedOutcomeNotListed: an outcome issue no open root
// blocks is not a forest and is not listed
// (§forgejo/observability/the-outcomes-view).
func TestForests_UnpairedOutcomeNotListed(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	api.repos = []Repository{outcomesRepo("o/out")}
	api.openIssues["out"] = []Issue{outcomesTracker(71, "orphan tracker")}

	forests, err := eng.enumerateForests()
	if err != nil {
		t.Fatalf("enumerateForests: %v", err)
	}
	if len(forests) != 0 {
		t.Errorf("forests = %d, want none: no open root blocks the tracker", len(forests))
	}
}

// TestForests_OnlyOutcomesReposEnumerated: repositories the scope cache
// does not classify as outcomes repositories are not walked, and pull
// requests carrying the labels pair into nothing
// (§forgejo/webhook/repository-roles,
// §forgejo/observability/the-outcomes-view).
func TestForests_OnlyOutcomesReposEnumerated(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	api.repos = []Repository{testRepo("o/wi"), outcomesRepo("o/out")}
	root := outcomesRoot(51, "outcomes root")
	tracker := outcomesTracker(71, "outcomes tracker")
	api.openIssues["out"] = []Issue{root, tracker}
	api.blocks[51] = []Issue{tracker}
	// A work-item repository is never walked, even when it holds rows
	// that look like forests, and a pull request carrying the labels is
	// no root.
	api.openIssues["wi"] = []Issue{
		outcomesRoot(90, "not walked"),
		{Number: 91, Title: "a pull request", State: "open", Labels: []string{LabelIdeation}, IsPull: true},
	}
	api.blocks[90] = []Issue{outcomesTracker(92, "not walked")}
	api.blocks[91] = []Issue{outcomesTracker(93, "not walked")}

	forests, err := eng.enumerateForests()
	if err != nil {
		t.Fatalf("enumerateForests: %v", err)
	}
	if len(forests) != 1 {
		t.Fatalf("forests = %d, want only the outcomes repository's forest: %+v", len(forests), forests)
	}
	if forests[0].Repository != "o/out" {
		t.Errorf("repository = %q, want o/out", forests[0].Repository)
	}
	if n := api.callCount("ListOpenIssues:wi"); n != 0 {
		t.Errorf("the work-item repository was listed %d times: the view walks only outcomes repositories", n)
	}
}

// TestForests_OrderIsDeterministic: forests are listed in repository order
// so the view does not reshuffle between reads
// (§forgejo/observability/the-outcomes-view).
func TestForests_OrderIsDeterministic(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	api.repos = []Repository{outcomesRepo("o/zeta"), outcomesRepo("o/alpha")}
	api.openIssues["zeta"] = []Issue{outcomesRoot(9, "zeta root")}
	api.openIssues["alpha"] = []Issue{outcomesRoot(3, "alpha root")}

	forests, err := eng.enumerateForests()
	if err != nil {
		t.Fatalf("enumerateForests: %v", err)
	}
	if len(forests) != 2 || forests[0].Repository != "o/alpha" || forests[1].Repository != "o/zeta" {
		t.Fatalf("forests = %+v, want o/alpha then o/zeta", forests)
	}
}

// TestForests_ReadFailureIsError: a failed read is an error, never a
// silently short or empty list — the view fails closed the way the scope
// test does — and the error names the repository or issue whose read
// broke, so the record of a failed enumeration is not a bare status
// code (§forgejo/webhook/the-scope-cache,
// §forgejo/observability/the-outcomes-view).
func TestForests_ReadFailureIsError(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(api *fakeAPI)
		want   string
	}{
		{"org listing fails", func(api *fakeAPI) { api.listReposErr = errors.New("org listing down") }, "org listing down"},
		{"issue listing fails", func(api *fakeAPI) { api.listIssuesErr = map[string]error{"out": errors.New("listing down")} }, "list open issues of o/out"},
		{"blocks read fails", func(api *fakeAPI) { api.issueBlocksErr = errors.New("blocks read down") }, "issues o/out#51 blocks"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng, api, _, _ := newTestEngine(t)
			api.repos = []Repository{outcomesRepo("o/out")}
			root := outcomesRoot(51, "root")
			tracker := outcomesTracker(71, "tracker")
			api.openIssues["out"] = []Issue{root, tracker}
			api.blocks[51] = []Issue{tracker}
			tc.mutate(api)

			forests, err := eng.enumerateForests()
			if err == nil {
				t.Fatalf("enumerateForests = %+v, want an error: a failed read is not an empty forest set", forests)
			}
			if forests != nil {
				t.Errorf("forests = %+v, want nil alongside the error", forests)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q, want it to name the failed read (%s)", err.Error(), tc.want)
			}
		})
	}
}

// TestForests_ScopeBackoffFailsClosed: a stale snapshot with a failed
// listing waiting out the retry backoff is an error, never the empty
// list a caller could read as "no outcomes repositories". The event
// path asserts this fail-closed tail through repoRoles; the outcomes
// view asserts the same guarantee through the sweep tick's reader —
// the walk the view is built from — and through the view itself, so a
// future edit that drops the check fails instead of silently serving an
// empty forest list (§forgejo/webhook/the-scope-cache,
// §forgejo/observability/the-outcomes-view).
func TestForests_ScopeBackoffFailsClosed(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	api.repos = []Repository{outcomesRepo("o/out")}
	api.openIssues["out"] = []Issue{outcomesRoot(51, "root")}

	// A snapshot aged past one interval with a listing failure still
	// inside its retry backoff: a refresh is needed but not yet due,
	// so the snapshot on hand must not be acted on.
	eng.scopeMu.Lock()
	eng.scopeAt = time.Time{}
	eng.scopeFailedAt = time.Now()
	eng.scopeMu.Unlock()

	forests, err := eng.enumerateForests()
	if !errors.Is(err, errScopeUnavailable) {
		t.Fatalf("enumerateForests err = %v, want errScopeUnavailable: a stale snapshot must not be read", err)
	}
	if forests != nil {
		t.Errorf("forests = %+v, want nil alongside the error", forests)
	}
	if n := api.callCount("ListOpenIssues:out"); n != 0 {
		t.Errorf("the stale snapshot was acted on: %d issue listing(s) ran during the backoff", n)
	}

	// The same guarantee through the view: recording this failed walk
	// leaves the view answering the failure, not a set it cannot vouch
	// for.
	eng.recordForests()
	if _, err := eng.Forests(); !errors.Is(err, errScopeUnavailable) {
		t.Errorf("enumerateForests err = %v, want errScopeUnavailable: the view must speak for the failed enumeration", err)
	}
}

// TestForests_TrackerReadIsTheEngines: a blocked row carrying both
// ideation and outcome is another forest's root, not this one's
// tracker — isTracker's one reading, decompose winning over wrap-up
// here as in the cascade and the teardown. Pairing takes the first
// blocked row that reads as a tracker, skipping the root
// (§forgejo/decisions/unblock-cascade, §forgejo/decisions/forest-teardown,
// §forgejo/observability/the-outcomes-view).
func TestForests_TrackerReadIsTheEngines(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	api.repos = []Repository{outcomesRepo("o/out")}
	root := outcomesRoot(51, "root")
	both := outcomesRoot(9, "a forest's own root, mislabelled")
	both.Labels = []string{LabelIdeation, LabelOutcome}
	tracker := outcomesTracker(71, "forest tracker")
	api.openIssues["out"] = []Issue{root, tracker}
	api.blocks[51] = []Issue{both, tracker}

	forests, err := eng.enumerateForests()
	if err != nil {
		t.Fatalf("enumerateForests: %v", err)
	}
	if len(forests) != 1 {
		t.Fatalf("forests = %d, want 1", len(forests))
	}
	if forests[0].Tracker == nil || forests[0].Tracker.Number != 71 {
		t.Errorf("tracker = %+v, want issue 71: the row carrying ideation too is no tracker", forests[0].Tracker)
	}
}

// TestForests_CrossRepoTrackerStatesRepository: a tracker in another
// repository — cross-repo blockers are a standing case of the blocks
// read — states the repository it lives in, so the row is honest about
// a cross-repo gate instead of pointing the dashboard at the root's
// repository (§forgejo/client/operations,
// §forgejo/observability/the-outcomes-view).
func TestForests_CrossRepoTrackerStatesRepository(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	api.repos = []Repository{outcomesRepo("o/out")}
	root := outcomesRoot(51, "root")
	tracker := outcomesTracker(71, "cross-repo tracker")
	tracker.Repository = "o/elsewhere"
	api.openIssues["out"] = []Issue{root}
	api.blocks[51] = []Issue{tracker}

	forests, err := eng.enumerateForests()
	if err != nil {
		t.Fatalf("enumerateForests: %v", err)
	}
	if len(forests) != 1 || forests[0].Tracker == nil {
		t.Fatalf("forests = %+v, want root 51 paired with its tracker", forests)
	}
	if forests[0].Repository != "o/out" {
		t.Errorf("repository = %q, want the forest to name the repository it grew in", forests[0].Repository)
	}
	if forests[0].Tracker.Repository != "o/elsewhere" {
		t.Errorf("tracker repository = %q, want o/elsewhere: the tracker states where it lives", forests[0].Tracker.Repository)
	}
}

// TestForests_RepositorySpellingFollowsTheListing: the scope cache is
// keyed on lowercased full names, but the view names a repository as
// the forge's listing spells it, the way every other engine read names
// it (§forgejo/client/operations, §forgejo/webhook/the-scope-cache,
// §forgejo/observability/the-outcomes-view).
func TestForests_RepositorySpellingFollowsTheListing(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	api.repos = []Repository{outcomesRepo("o/Out")}
	root := outcomesRoot(51, "root")
	root.Repository = "o/Out"
	tracker := outcomesTracker(71, "tracker")
	tracker.Repository = "o/Out"
	api.openIssues["out"] = []Issue{root, tracker}
	api.blocks[51] = []Issue{tracker}

	forests, err := eng.enumerateForests()
	if err != nil {
		t.Fatalf("enumerateForests: %v", err)
	}
	if len(forests) != 1 {
		t.Fatalf("forests = %d, want 1", len(forests))
	}
	if forests[0].Repository != "o/Out" || forests[0].Root.Repository != "o/Out" {
		t.Errorf("repository = %q root.repository = %q, want the listing's spelling o/Out, not the scope cache's lowercase", forests[0].Repository, forests[0].Root.Repository)
	}
	if forests[0].Tracker == nil || forests[0].Tracker.Repository != "o/Out" {
		t.Errorf("tracker = %+v, want its repository spelled o/Out", forests[0].Tracker)
	}
}

// TestForests_ViewIsTheLastSweepsSet: the view is served from the
// snapshot the last sweep tick recorded, not from live reads — a
// request costs no forge call, and before any sweep has enumerated,
// the view speaks of its unread state rather than answering an empty
// list (§forgejo/observability/the-outcomes-view).
func TestForests_ViewIsTheLastSweepsSet(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	api.repos = []Repository{outcomesRepo("o/out")}
	root := outcomesRoot(51, "root")
	tracker := outcomesTracker(71, "tracker")
	api.openIssues["out"] = []Issue{root, tracker}
	api.blocks[51] = []Issue{tracker}

	if forests, err := eng.Forests(); !errors.Is(err, errForestsUnavailable) || forests != nil {
		t.Fatalf("Forests = %+v, %v, want errForestsUnavailable and nil before the first enumeration", forests, err)
	}

	eng.recordForests()
	readsBefore := api.callCount("")
	forests, err := eng.Forests()
	if err != nil {
		t.Fatalf("Forests after the sweep tick: %v", err)
	}
	if len(forests) != 1 || forests[0].Root.Number != 51 || forests[0].Tracker == nil || forests[0].Tracker.Number != 71 {
		t.Errorf("forests = %+v, want the recorded forest", forests)
	}
	if _, err := eng.Forests(); err != nil {
		t.Fatalf("Forests (second read): %v", err)
	}
	if n := api.callCount("") - readsBefore; n != 0 {
		t.Errorf("serving the view made %d API call(s), want 0: the request path reads no forge", n)
	}
}

// TestForests_FailedEnumerationSilencesTheView: when a sweep's
// enumeration fails, the view answers that failure — never the earlier
// set served as if fresh, and never an empty list — until a later sweep
// enumerates again. This is the scope cache's rule, applied to the
// forest snapshot (§forgejo/webhook/the-scope-cache,
// §forgejo/observability/the-outcomes-view).
func TestForests_FailedEnumerationSilencesTheView(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	api.repos = []Repository{outcomesRepo("o/out")}
	root := outcomesRoot(51, "root")
	tracker := outcomesTracker(71, "tracker")
	api.openIssues["out"] = []Issue{root, tracker}
	api.blocks[51] = []Issue{tracker}

	eng.recordForests()
	if _, err := eng.Forests(); err != nil {
		t.Fatalf("Forests after the first sweep: %v", err)
	}

	api.listIssuesErr = map[string]error{"out": errors.New("listing down")}
	eng.recordForests()
	if forests, err := eng.Forests(); err == nil || forests != nil {
		t.Errorf("Forests = %+v, %v, want the failed enumeration's error and nil, not the last set", forests, err)
	} else if !strings.Contains(err.Error(), "list open issues of o/out") {
		t.Errorf("error %q, want it to name the read that broke", err)
	}

	delete(api.listIssuesErr, "out")
	eng.recordForests()
	if forests, err := eng.Forests(); err != nil || len(forests) != 1 {
		t.Errorf("Forests = %+v, %v, want the view recovered by the next successful sweep", forests, err)
	}
}

// TestForests_SweepTickRecordsTheView: Reconcile records the forest
// set on its tick, so the endpoint's data is the sweep's — no API
// request enumerates anything itself (§forgejo/reconciliation/the-sweep,
// §forgejo/observability/the-outcomes-view).
func TestForests_SweepTickRecordsTheView(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	api.repos = []Repository{outcomesRepo("o/out")}
	root := outcomesRoot(51, "root")
	tracker := outcomesTracker(71, "tracker")
	api.openIssues["out"] = []Issue{root, tracker}
	api.blocks[51] = []Issue{tracker}
	api.issues[51] = &root
	api.issues[71] = &tracker

	eng.Reconcile()

	forests, err := eng.Forests()
	if err != nil {
		t.Fatalf("Forests after Reconcile: %v", err)
	}
	if len(forests) != 1 || forests[0].Root.Number != 51 || forests[0].Tracker == nil || forests[0].Tracker.Number != 71 {
		t.Errorf("forests = %+v, want the sweep to have recorded the paired forest", forests)
	}
}

// TestForests_SnapshotAgesOutFailsClosed: a forge that becomes
// unreachable makes the sweep return at its first step, so no
// enumeration is recorded at all — and the set recorded before the
// outage must stop being served as fresh once it is older than one
// reconcile interval. The bound is the view's own, applied when the set
// is read, exactly as the scope cache bounds its snapshot
// (§forgejo/webhook/the-scope-cache,
// §forgejo/observability/the-outcomes-view,
// §forgejo/reconciliation/the-sweep).
func TestForests_SnapshotAgesOutFailsClosed(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	api.repos = []Repository{outcomesRepo("o/out")}
	root := outcomesRoot(51, "root")
	tracker := outcomesTracker(71, "tracker")
	api.openIssues["out"] = []Issue{root, tracker}
	api.blocks[51] = []Issue{tracker}

	eng.Reconcile()
	if _, err := eng.Forests(); err != nil {
		t.Fatalf("Forests after the first sweep: %v", err)
	}

	// The forge becomes unreachable: every later sweep fails at the
	// organisation listing and never reaches step 4, so nothing
	// records a set and nothing marks the view failed.
	api.listReposErr = errors.New("forge unreachable")
	eng.Reconcile()
	if _, err := eng.Forests(); err != nil {
		t.Errorf("Forests during the outage = %v, want the set still served while it is younger than one interval", err)
	}

	// Past one interval with nothing recorded, the engine can no
	// longer vouch for the set it holds.
	eng.forestsMu.Lock()
	eng.forestsAt = time.Now().Add(-eng.cfg.ReconcileInterval - time.Minute)
	eng.forestsMu.Unlock()

	forests, err := eng.Forests()
	if !errors.Is(err, errForestsStale) {
		t.Errorf("Forests err = %v, want errForestsStale: an aged set is never served as fresh", err)
	}
	if forests != nil {
		t.Errorf("forests = %+v, want nil alongside the stale error", forests)
	}

	// A sweep that reaches its fourth step again refreshes the stamp.
	api.listReposErr = nil
	eng.Reconcile()
	if forests, err := eng.Forests(); err != nil || len(forests) != 1 {
		t.Errorf("Forests = %+v, %v, want a successful sweep to recover the view", forests, err)
	}
}

// TestForests_RecordedEmptyIsHonest: an engine whose enumeration found
// no forests answers the empty set, not the "not yet enumerated"
// failure. Whether anything was recorded is carried by the moment it
// was recorded, not by whether the set came back non-empty, so an
// engine whose forests have all finished — or whose repositories do not
// yet hold the outcomes role — is never mistaken for one whose reads
// have not landed (§forgejo/observability/the-outcomes-view).
func TestForests_RecordedEmptyIsHonest(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	api.repos = []Repository{outcomesRepo("o/out")}
	api.openIssues["out"] = []Issue{outcomesTracker(71, "tracker no root blocks")}

	if _, err := eng.Forests(); !errors.Is(err, errForestsUnavailable) {
		t.Fatalf("Forests before the first enumeration = %v, want errForestsUnavailable", err)
	}

	eng.recordForests()
	forests, err := eng.Forests()
	if err != nil {
		t.Fatalf("Forests after an empty enumeration = %v, want the honest empty rather than a failure", err)
	}
	if forests == nil {
		t.Error("forests = nil, want an empty non-nil list: the view answers [] and never null")
	}
	if len(forests) != 0 {
		t.Errorf("forests = %+v, want none", forests)
	}
}
