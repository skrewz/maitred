package forgejo

import (
	"bytes"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
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

// outcomesRoot builds an open issue carrying the reserved ideation label,
// in the colour a real forge reports for it — the drawing's stroke comes
// from the label, so the outcomes fixtures carry one too
// (§forgejo/renderer/classes-and-strokes).
func outcomesRoot(number int, title string) Issue {
	return Issue{
		Number:       number,
		Title:        title,
		State:        "open",
		Labels:       []string{LabelIdeation},
		LabelColours: chartRootColour,
		UpdatedAt:    testUpdatedAt,
		HTMLURL:      issueURL(number),
	}
}

// outcomesTracker builds an open issue carrying the reserved outcome
// label, in the colour a real forge reports for it
// (§forgejo/renderer/classes-and-strokes).
func outcomesTracker(number int, title string) Issue {
	return Issue{
		Number:       number,
		Title:        title,
		State:        "open",
		Labels:       []string{LabelOutcome},
		LabelColours: chartTrackerColour,
		UpdatedAt:    testUpdatedAt,
		HTMLURL:      issueURL(number),
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
	stageForest(api, root, tracker)

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
	stageForest(api, root, tracker)
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
	if n := api.callCount("ListAllIssues:wi"); n != 0 {
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
		{"issue listing fails", func(api *fakeAPI) { api.allIssuesErr = map[string]error{"out": errors.New("listing down")} }, "list issues of o/out"},
		{"blocks read fails", func(api *fakeAPI) { api.issueBlocksErr = errors.New("blocks read down") }, "issues o/out#51 blocks"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng, api, _, _ := newTestEngine(t)
			api.repos = []Repository{outcomesRepo("o/out")}
			root := outcomesRoot(51, "root")
			tracker := outcomesTracker(71, "tracker")
			stageForest(api, root, tracker)
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

// TestForests_DeletedRootIsSkipped: a forest root the forge answers not
// found for — deleted by teardown-forest, its row still standing in the
// listing the walk already made — names no forest and skips without
// failing the enumeration, while a failure that is not the forge's
// definitive "no such issue" still fails the whole set
// (§forgejo/observability/the-outcomes-view,
// §forgejo/decisions/forest-teardown). The view survives the deletion of
// one forest rather than the whole outcomes view going dark on every
// poll (s-git/maitred#268).
func TestForests_DeletedRootIsSkipped(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	api.repos = []Repository{outcomesRepo("o/out")}
	gone := outcomesRoot(89, "deleted root")
	goneTracker := outcomesTracker(90, "deleted tracker")
	live := outcomesRoot(84, "live root")
	liveTracker := outcomesTracker(109, "live tracker")
	api.openIssues["out"] = []Issue{gone, goneTracker, live, liveTracker}
	api.blocks[84] = []Issue{liveTracker}
	// The forge still hands 89's row on the listing the walk made; the
	// blocks read of the deleted root answers 404.
	api.issueBlocksErrOn = map[int]error{89: &APIError{StatusCode: 404, Message: "The target couldn't be found."}}

	forests, err := eng.enumerateForests()
	if err != nil {
		t.Fatalf("enumerateForests: %v: a deleted root is the lifecycle's ordinary answer, not a failed read", err)
	}
	if len(forests) != 1 || forests[0].Root.Number != 84 {
		t.Fatalf("forests = %+v, want only the live forest: the deleted root skips, the rest survive", forests)
	}

	// A failure that is not a 404 still fails the enumeration whole —
	// the skip belongs to the forge's definitive answer alone.
	api.issueBlocksErrOn = map[int]error{89: errors.New("blocks read down")}
	forests, err = eng.enumerateForests()
	if err == nil || forests != nil {
		t.Fatalf("forests = %+v, err = %v, want the enumeration to fail closed on a broken read", forests, err)
	}
	if !strings.Contains(err.Error(), "blocks read down") {
		t.Errorf("error %q, want it to name the failed read", err.Error())
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
	if n := api.callCount("ListAllIssues:out"); n != 0 {
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
// it — and a tracker row that lists no repository lives where the root
// does, the digest's rule for the same row, so one forest never spells
// one repository two ways
// (§forgejo/client/operations, §forgejo/webhook/the-scope-cache,
// §forgejo/observability/the-outcomes-view).
func TestForests_RepositorySpellingFollowsTheListing(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	api.repos = []Repository{outcomesRepo("o/Out")}
	root := outcomesRoot(51, "root")
	root.Repository = "o/Out"
	tracker := outcomesTracker(71, "tracker")
	tracker.Repository = "o/Out"
	api.openIssues["out"] = []Issue{root, tracker}
	blocksRow := tracker
	blocksRow.Repository = "" // the blocks listing may omit the field
	api.blocks[51] = []Issue{blocksRow}

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
		t.Errorf("tracker = %+v, want its repository spelled o/Out: a blocks row listing none lives where the root does, not at the scope cache's lowercased key", forests[0].Tracker)
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
	stageForest(api, root, tracker)

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
	stageForest(api, root, tracker)

	eng.recordForests()
	if _, err := eng.Forests(); err != nil {
		t.Fatalf("Forests after the first sweep: %v", err)
	}

	api.allIssuesErr = map[string]error{"out": errors.New("listing down")}
	eng.recordForests()
	if forests, err := eng.Forests(); err == nil || forests != nil {
		t.Errorf("Forests = %+v, %v, want the failed enumeration's error and nil, not the last set", forests, err)
	} else if !strings.Contains(err.Error(), "list issues of o/out") {
		t.Errorf("error %q, want it to name the read that broke", err)
	}

	delete(api.allIssuesErr, "out")
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
	stageForest(api, root, tracker)

	eng.Reconcile()
	if _, err := eng.Forests(); err != nil {
		t.Fatalf("Forests after the first sweep: %v", err)
	}

	// The forge becomes unreachable: every later sweep fails at the
	// organisation listing, so nothing records a set and nothing marks
	// the view failed.
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

	// A sweep that reaches the step that records the set again
	// refreshes the stamp.
	api.listReposErr = nil
	eng.Reconcile()
	if forests, err := eng.Forests(); err != nil || len(forests) != 1 {
		t.Errorf("Forests = %+v, %v, want a successful sweep to recover the view", forests, err)
	}
}

// TestForests_RecordLandsBeforeTheWalk: the tick records the forest
// set at its head, before the walk dispatches any event, so the
// pipeline's pace can never delay the recording the view ages itself
// against (§forgejo/reconciliation/the-sweep,
// §forgejo/observability/the-outcomes-view).
func TestForests_RecordLandsBeforeTheWalk(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	api.repos = []Repository{outcomesRepo("o/out")}
	root := outcomesRoot(51, "root")
	tracker := outcomesTracker(71, "tracker")
	api.openIssues["out"] = []Issue{root, tracker}
	api.blocks[51] = []Issue{tracker}
	api.issues[51] = &root
	api.issues[71] = &tracker

	eng.Reconcile()

	api.mu.Lock()
	defer api.mu.Unlock()
	blocks, walk := -1, -1
	for i, c := range api.calls {
		if blocks < 0 && strings.HasPrefix(c, "IssueBlocks:") {
			blocks = i
		}
		if walk < 0 && strings.HasPrefix(c, "GetIssue:") {
			walk = i
		}
		if blocks >= 0 && walk >= 0 {
			break
		}
	}
	if blocks < 0 {
		t.Fatal("the sweep made no blocks read: the tick never enumerated the forests")
	}
	if walk < 0 {
		t.Fatal("the sweep made no pipeline re-fetch: the walk never ran, so the order of the two says nothing")
	}
	if blocks > walk {
		t.Errorf("the tick's blocks read (%d) came after the walk's first pipeline read (%d): the recording lands after the walk it must not age against", blocks, walk)
	}
}

// TestForests_ViewServesBeforeTheFirstWalkFinishes: at process start
// the view must not wait for the first sweep's walk — the pipeline's
// re-fetches — before it has a set to serve, and a walk that runs past
// a tick's due must not delay that tick either. Holding the walk inside
// its first re-fetch witnesses both without a clock standing in for
// either: the set the tick's head recorded is served while the sweep's
// slowest part is still running, and the tick that fell due during the
// hold fires on release, as §the-sweep states
// (§forgejo/observability/the-outcomes-view,
// §forgejo/reconciliation/the-sweep, §forgejo/reconciliation/scheduling).
func TestForests_ViewServesBeforeTheFirstWalkFinishes(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	eng.cfg.ReconcileInterval = 500 * time.Millisecond
	api.repos = []Repository{outcomesRepo("o/out"), testRepo("o/r")}
	root := outcomesRoot(51, "root")
	tracker := outcomesTracker(71, "tracker")
	api.openIssues["out"] = []Issue{root, tracker}
	api.blocks[51] = []Issue{tracker}
	api.issues[51] = &root
	api.issues[71] = &tracker
	work := Issue{Number: 7, State: "open", UpdatedAt: testUpdatedAt, Repository: "o/r", HTMLURL: "https://forge.example.com/o/r/issues/7"}
	api.openIssues["r"] = []Issue{work}
	api.issues[7] = &work
	release := make(chan struct{})
	entered := make(chan struct{})
	api.blockGetIssue = release
	api.getIssueEntered = entered
	releaseOnce := sync.OnceFunc(func() { close(release) })

	eng.Start()
	// Stop waits the in-flight sweep out, so the walk is released first
	// — the deferred calls run last-declared first.
	defer eng.Stop()
	defer releaseOnce()

	// Once the walk is inside a re-fetch, the tick's head has been paid
	// for: the recording landed before the event was dispatched.
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the first sweep never reached a pipeline re-fetch")
	}
	forests, err := eng.Forests()
	if err != nil {
		t.Fatalf("Forests while the first sweep's walk is held in its re-fetch = %v, want the set the tick's head recorded before the walk dispatched anything: %+v", err, forests)
	}
	if len(forests) != 1 || forests[0].Root.Number != 51 {
		t.Errorf("forests = %+v, want the forest the tick recorded", forests)
	}

	// The walk is now held past the first tick's due, so the tick waits
	// unread in the ticker's buffer while the loop is inside the sweep.
	// Releasing it must start the second sweep at once. The witness is
	// the work issue's second re-fetch: a sweep re-fetches each object
	// once, so only a second sweep turns that count into two — unlike
	// the organisation listing, which the event path's own scope refresh
	// can spend while a walk overruns an interval. The bound is half an
	// interval, where the schedule this rules out waits a whole interval
	// on the walk's finish (§forgejo/reconciliation/the-sweep).
	time.Sleep(eng.cfg.ReconcileInterval + eng.cfg.ReconcileInterval/2)
	released := time.Now()
	releaseOnce()
	deadline := released.Add(eng.cfg.ReconcileInterval / 2)
	for api.callCount("GetIssue:r/7") < 2 {
		if time.Now().After(deadline) {
			t.Fatal("the second sweep waited a whole interval after the first one finished rather than starting on the tick that fell due during its walk (§forgejo/reconciliation/the-sweep)")
		}
		time.Sleep(2 * time.Millisecond)
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

// The picture the payload carries: the tick's own drawing of the forest's
// graph, recorded onto the set the view serves. The tracker's description
// is out of the picture's chain of command, ragged or not
// (§forgejo/observability/the-outcomes-view,
// §forgejo/reconciliation/the-tracker-block).

// forestChartSource is the Mermaid source a tracker's description may
// claim between its fences. The payload publishes none of what the
// description claims: wherever a test says "stale", this is the chart the
// description holds and the payload must not publish.
const forestChartSource = "flowchart TD\n\tr51[root #51] --> t71[tracker #71]"

// mermaidBlock is that source as a fenced Mermaid block.
func mermaidBlock() string {
	return "```mermaid\n" + forestChartSource + "\n```"
}

// currentStateBlock builds a tracker body whose current-state region is
// delimited by the fence pair the engine writes, holding the stamp line
// and the given fenced block, with prose outside the pair
// (§forgejo/reconciliation/the-tracker-block).
func currentStateBlock(stamp, block string) string {
	region := fenceBegin + "\n\n## Current state\n\nLast revisited: " + stamp + "\n\n" + block + "\n\n" + fenceEnd
	return "## Outcome\n\nThe prose a human wrote.\n\n" + region + "\n\nA footnote outside the pair.\n"
}

// stageForest stages one forest in the outcomes fixtures: the root and its
// tracker in the listing and readable as themselves, and the pairing in
// the root's blocks read. The payload's picture is drawn from the graph
// these rows trace, never read out of a description
// (§forgejo/observability/the-outcomes-view, §forgejo/client/operations).
func stageForest(api *fakeAPI, root, tracker Issue) {
	api.openIssues["out"] = []Issue{root, tracker}
	api.blocks[root.Number] = []Issue{tracker}
	api.issues[root.Number] = &root
	api.issues[tracker.Number] = &tracker
}

// TestForests_PictureTravelsAcrossTheTick: the set a tick records at its head
// carries each forest's picture pair across from the set it replaces, so the
// walk never leaves the view answering an empty picture; step 5 replaces every
// pair, so a carried pair is never the view's last word, and a forest the head
// records for the first time carries the honest empty until the tick draws it,
// and a forest whose tracker is absent carries it empty from the head, having
// nothing to draw a picture for
// (§forgejo/observability/the-outcomes-view,
// §forgejo/reconciliation/the-tracker-block,
// §forgejo/reconciliation/the-sweep).
func TestForests_PictureTravelsAcrossTheTick(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	chartWire(api, chartFenced())

	// Step 2 of the first tick: there is no earlier set to carry a pair from,
	// so the pair is the honest empty.
	first, err := eng.recordForests()
	if err != nil || len(first) != 1 {
		t.Fatalf("forests = %+v, %v", first, err)
	}
	if first[0].Chart != "" || first[0].ChartRevisited != "" {
		t.Errorf("a forest recorded for the first time carries chart %q under stamp %q, want both fields empty",
			first[0].Chart, first[0].ChartRevisited)
	}

	// Step 5 draws it, and the pair becomes the view's answer.
	eng.writeTrackerBlocks(first, chartAt)
	drawn, err := eng.Forests()
	if err != nil || len(drawn) != 1 {
		t.Fatalf("forests = %+v, %v", drawn, err)
	}
	if drawn[0].Chart == "" || drawn[0].ChartRevisited == "" {
		t.Fatalf("the tick recorded chart %q under stamp %q, want the drawing and its moment", drawn[0].Chart, drawn[0].ChartRevisited)
	}
	want := drawn[0]

	// A second forest joins the organisation: the next head records it, and
	// nothing carries a pair across for it.
	rootTwo := chartRoot()
	rootTwo.Number = 8
	rootTwo.HTMLURL = "https://forge.example.com/o/out/issues/8"
	trackerTwo := chartTracker(chartFenced())
	trackerTwo.Number = 9
	trackerTwo.HTMLURL = "https://forge.example.com/o/out/issues/9"
	api.mu.Lock()
	api.issues[rootTwo.Number] = &rootTwo
	api.issues[trackerTwo.Number] = &trackerTwo
	api.blocks[rootTwo.Number] = []Issue{trackerTwo}
	api.openIssues["out"] = append(api.openIssues["out"], rootTwo, trackerTwo)
	api.mu.Unlock()

	// Step 2 of the next tick on its own: the walk has not run and nothing has
	// been drawn, yet the view still answers the forest it knew with the pair
	// it had — the same chart under the same stamp, never a blank and never
	// half a pair — and the forest it has never seen with the empty.
	second, err := eng.recordForests()
	if err != nil || len(second) != 2 {
		t.Fatalf("forests = %+v, %v", second, err)
	}
	held, err := eng.Forests()
	if err != nil || len(held) != 2 {
		t.Fatalf("forests = %+v, %v", held, err)
	}
	for _, forest := range held {
		switch forest.Root.Number {
		case want.Root.Number:
			if forest.Chart != want.Chart || forest.ChartRevisited != want.ChartRevisited {
				t.Errorf("between the tick's head and its drawing the view answers chart %q under stamp %q, want the pair it held: chart %q under stamp %q",
					forest.Chart, forest.ChartRevisited, want.Chart, want.ChartRevisited)
			}
		case rootTwo.Number:
			if forest.Chart != "" || forest.ChartRevisited != "" {
				t.Errorf("a forest recorded for the first time carries chart %q under stamp %q, want both fields empty",
					forest.Chart, forest.ChartRevisited)
			}
		default:
			t.Errorf("unexpected forest %s#%d", forest.Repository, forest.Root.Number)
		}
	}

	// Step 5 of that tick replaces every pair: the carried one is overwritten
	// by this tick's drawing under this tick's stamp, and the new forest's
	// empty gives way to its own picture.
	eng.writeTrackerBlocks(second, chartAt.Add(time.Hour))
	now, err := eng.Forests()
	if err != nil || len(now) != 2 {
		t.Fatalf("forests = %+v, %v", now, err)
	}
	for _, forest := range now {
		if forest.Chart == "" {
			t.Errorf("forest %s#%d carries no chart after the tick drew it", forest.Repository, forest.Root.Number)
		}
		if forest.ChartRevisited == "" || forest.ChartRevisited == want.ChartRevisited {
			t.Errorf("forest %s#%d carries stamp %q, want this tick's drawing moment rather than the carried one",
				forest.Repository, forest.Root.Number, forest.ChartRevisited)
		}
	}

	// A tracker unblocked is a forest whose tracker is absent, which the view
	// states with both fields empty (§forgejo/observability/the-outcomes-view):
	// a picture has no tracker to be drawn for, so none carries across.
	api.mu.Lock()
	api.blocks[want.Root.Number] = nil
	api.mu.Unlock()
	lost, err := eng.recordForests()
	if err != nil || len(lost) != 2 {
		t.Fatalf("forests = %+v, %v", lost, err)
	}
	absent := 0
	for _, forest := range lost {
		if forest.Tracker == nil {
			absent++
			if forest.Chart != "" || forest.ChartRevisited != "" {
				t.Errorf("a forest with no tracker carries chart %q under stamp %q, want both fields empty", forest.Chart, forest.ChartRevisited)
			}
		}
	}
	if absent != 1 {
		t.Fatalf("%d forests carry no tracker, want exactly 1: at zero the case above asserts nothing at all", absent)
	}
}

// TestForests_ChartIsDrawnFromTheGraph: one table of tracker
// descriptions — well-formed but stale, ragged in every way a picture has
// ever refused to be read off a description, and absent outright, the chart
// never written to its tracker — and every row carries the same payload
// picture: the source the tick drew from the forest's graph, stamped at the
// moment that drawing was made. What a description claims about the graph
// moves no field (§forgejo/observability/the-outcomes-view,
// §forgejo/reconciliation/the-tracker-block).
func TestForests_ChartIsDrawnFromTheGraph(t *testing.T) {
	const stamp = "2026-11-16 09:12 AEDT"
	cases := []struct {
		name string
		body string
	}{
		{
			name: "a well-formed region whose chart and stamp have gone stale",
			body: currentStateBlock(stamp, "```mermaid\nflowchart TD\n%% stale\n```"),
		},
		{
			// The case a picture taken from a description would have left
			// empty for the whole of a young forest's life: nobody has
			// written its tracker a chart yet.
			name: "no fence pair — the chart was never written to the tracker",
			body: "## Outcome\n\nNothing was ever delimited here.\n",
		},
		{
			name: "an empty description",
			body: "",
		},
		{
			name: "two fence pairs are ambiguous",
			body: currentStateBlock(stamp, mermaidBlock()) + "\n" +
				currentStateBlock("2025-01-01 00:00 AEDT", mermaidBlock()),
		},
		{
			name: "the end marker sits before the begin marker",
			body: fenceEnd + "\n```mermaid\n" + forestChartSource + "\n```\n" + fenceBegin,
		},
		{
			name: "the pair holds prose but no Mermaid fence",
			body: currentStateBlock(stamp, "The graph would go here, but none was written."),
		},
		{
			name: "the pair holds two Mermaid blocks",
			body: currentStateBlock(stamp, mermaidBlock()+"\n\n"+mermaidBlock()),
		},
		{
			name: "the Mermaid fence is never closed",
			body: currentStateBlock(stamp, "```mermaid\n"+forestChartSource),
		},
		{
			name: "the region holds no stamp line",
			body: fenceBegin + "\n\n" + mermaidBlock() + "\n\n" + fenceEnd,
		},
		{
			name: "a stamp line carrying no value",
			body: currentStateBlock("", mermaidBlock()),
		},
		{
			name: "the region holds two stamp lines",
			body: currentStateBlock(stamp+"\n\nLast revisited: 2025-01-01 00:00 AEDT", mermaidBlock()),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng, api, _, _ := newTestEngine(t)
			chartWire(api, tc.body)
			start := time.Now()
			eng.Reconcile()

			forests, err := eng.Forests()
			if err != nil {
				t.Fatalf("Forests: %v", err)
			}
			if len(forests) != 1 {
				t.Fatalf("forests = %+v, want the one forest", forests)
			}
			forest := forests[0]
			want, err := eng.renderForestChart(forest)
			if err != nil {
				t.Fatalf("renderForestChart: %v", err)
			}
			if forest.Chart != want {
				t.Errorf("chart = %q, want the tick's drawing of the graph %q: what the description claims moves no field", forest.Chart, want)
			}
			stamped, err := time.Parse(stampLayout, forest.ChartRevisited)
			if err != nil {
				t.Errorf("chart_revisited = %q, want the render moment in the %s layout: %v", forest.ChartRevisited, stampLayout, err)
			} else if stamped.Before(start.Add(-time.Minute)) || stamped.After(time.Now().Add(time.Minute)) {
				t.Errorf("chart_revisited = %q, want a stamp of this tick's drawing moment", forest.ChartRevisited)
			}
		})
	}
}

// TestForests_AbsentTrackerPublishesNoPictureAndIsNotRead: a forest whose
// tracker is absent publishes both picture fields empty and reads no
// description of an issue that is not there — through the enumeration and
// through a full sweep tick, since the tick draws no picture for a forest
// with nothing to draw it on
// (§forgejo/observability/the-outcomes-view).
func TestForests_AbsentTrackerPublishesNoPictureAndIsNotRead(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	api.repos = []Repository{outcomesRepo("o/out")}
	root := outcomesRoot(51, "root blocks nothing")
	api.openIssues["out"] = []Issue{root}

	forests, err := eng.enumerateForests()
	if err != nil {
		t.Fatalf("enumerateForests: %v", err)
	}
	if len(forests) != 1 || forests[0].Tracker != nil {
		t.Fatalf("forests = %+v, want the root listed with its tracker absent", forests)
	}
	if forests[0].Chart != "" || forests[0].ChartRevisited != "" {
		t.Errorf("chart = %q chart_revisited = %q, want both empty with no tracker to draw", forests[0].Chart, forests[0].ChartRevisited)
	}
	if n := api.callCount("GetIssue:"); n != 0 {
		t.Errorf("the walk made %d issue read(s), want none: a forest with no tracker reads no body", n)
	}

	eng.Reconcile()
	forests, err = eng.Forests()
	if err != nil {
		t.Fatalf("Forests after the sweep: %v", err)
	}
	if len(forests) != 1 || forests[0].Chart != "" || forests[0].ChartRevisited != "" {
		t.Errorf("forests = %+v, want the trackerless forest recorded with both picture fields empty", forests)
	}
}

// TestForests_ChartIgnoresWhatTheTrackersBodyClaims: a cross-repository
// tracker whose description carries a decoy picture still brings the
// payload the drawing the graph supports — the row the blocks read
// returned is no longer the picture's source, wherever it lives
// (§forgejo/client/operations, §forgejo/observability/the-outcomes-view,
// §forgejo/webhook/repository-roles).
func TestForests_ChartIgnoresWhatTheTrackersBodyClaims(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	api.repos = []Repository{outcomesRepo("o/out"), testRepo("o/elsewhere")}
	root := outcomesRoot(51, "root")
	api.issues[51] = &root
	tracker := outcomesTracker(71, "cross-repo tracker")
	tracker.Repository = "o/elsewhere"
	tracker.Body = currentStateBlock("2026-11-16 09:12 AEDT",
		"```mermaid\nflowchart TD\n\tdecoy[a chart that lies]\n```")
	api.openIssues["out"] = []Issue{root}
	api.blocks[51] = []Issue{tracker}

	eng.Reconcile()
	forests, err := eng.Forests()
	if err != nil {
		t.Fatalf("Forests: %v", err)
	}
	if len(forests) != 1 || forests[0].Tracker == nil || forests[0].Tracker.Repository != "o/elsewhere" {
		t.Fatalf("forests = %+v, want the cross-repo tracker paired", forests)
	}
	forest := forests[0]
	if strings.Contains(forest.Chart, "a chart that lies") {
		t.Errorf("chart = %q, want the graph's drawing, not the decoy the description claims", forest.Chart)
	}
	want, err := eng.renderForestChart(forest)
	if err != nil {
		t.Fatalf("renderForestChart: %v", err)
	}
	if forest.Chart != want {
		t.Errorf("chart = %q, want the tick's drawing of the graph %q", forest.Chart, want)
	}
	if forest.ChartRevisited == "" {
		t.Error("chart_revisited is empty, want the stamp of the drawing that was made")
	}
}

// TestForests_PicturePaysTheAdmissionTest: the picture is the tick's
// drawing, so the one admission test the drawing pays governs both of
// its sinks — a tracker in a repository a maitred role names brings its
// payload picture, one no role names stays paired with the picture
// withheld, so a repository outside the remit authors nothing the
// unauthenticated view publishes as the engine's current state; the
// refusal is Debug, the level every other cross-repository hop uses for
// this sentence (§forgejo/webhook/repository-roles,
// §forgejo/reconciliation/the-tracker-block,
// §forgejo/observability/the-outcomes-view).
func TestForests_PicturePaysTheAdmissionTest(t *testing.T) {
	var log bytes.Buffer
	eng, api, _, _ := newEngineWithLogger(t, slog.New(slog.NewJSONHandler(&log,
		&slog.HandlerOptions{Level: slog.LevelDebug})))
	api.repos = []Repository{outcomesRepo("o/out"), testRepo("o/elsewhere")}
	root := outcomesRoot(51, "root")
	admitted := outcomesTracker(71, "tracker in an admitted repository")
	admitted.Repository = "o/elsewhere"
	rootTwo := outcomesRoot(52, "second root")
	refused := outcomesTracker(72, "tracker in an unadmitted repository")
	refused.Repository = "o/archive"
	api.openIssues["out"] = []Issue{root, rootTwo}
	api.issues[51] = &root
	api.issues[52] = &rootTwo
	api.blocks[51] = []Issue{admitted}
	api.blocks[52] = []Issue{refused}

	eng.Reconcile()
	forests, err := eng.Forests()
	if err != nil {
		t.Fatalf("Forests: %v", err)
	}
	if len(forests) != 2 {
		t.Fatalf("forests = %d, want 2", len(forests))
	}
	for _, f := range forests {
		if f.Tracker == nil {
			t.Fatalf("forest %+v has no tracker: both roots pair their tracker", f)
		}
		switch f.Tracker.Number {
		case 71:
			want, err := eng.renderForestChart(f)
			if err != nil {
				t.Fatalf("renderForestChart: %v", err)
			}
			if f.Chart != want || f.ChartRevisited == "" {
				t.Errorf("admitted tracker published chart %q stamp %q, want the tick's drawing %q under its stamp", f.Chart, f.ChartRevisited, want)
			}
		case 72:
			if f.Chart != "" || f.ChartRevisited != "" {
				t.Errorf("unadmitted tracker published chart %q stamp %q, want both empty: a repository no role admits authors no picture", f.Chart, f.ChartRevisited)
			}
		default:
			t.Errorf("unexpected tracker %d", f.Tracker.Number)
		}
	}
	line := findEntry(t, parseLogLines(t, log.Bytes()),
		"reconciliation: tracker lives in a repository no maitred role names; nothing written")
	if line["tracker"] != "o/archive#72" {
		t.Errorf("tracker = %v, want o/archive#72: the refusal names the repository it refused", line["tracker"])
	}
	if n := api.callCount("UpdateIssueBody:archive/72"); n != 0 {
		t.Errorf("the tick wrote %d bodies to the refused repository, want none", n)
	}

	// The revocation case: a role withdrawn from the repository holding a
	// picture that tick already drew. The head's carry asks the admission test
	// as well as step 5's drawing, so step 2 of the next tick — before any
	// drawing — is enough to empty both fields
	// (§forgejo/webhook/repository-roles,
	// §forgejo/observability/the-outcomes-view).
	api.mu.Lock()
	api.repos = []Repository{outcomesRepo("o/out"), testRepoDisabled("o/elsewhere")}
	api.mu.Unlock()
	eng.scopeAt = time.Time{} // the withdrawal must be seen, not the cached remit
	if _, err := eng.recordForests(); err != nil {
		t.Fatalf("recordForests: %v", err)
	}
	revoked, err := eng.Forests()
	if err != nil || len(revoked) != 2 {
		t.Fatalf("forests = %+v, %v", revoked, err)
	}
	carried := 0
	for _, f := range revoked {
		if f.Tracker == nil {
			t.Fatalf("forest %+v has no tracker: both roots still pair their tracker", f)
		}
		switch f.Tracker.Repository {
		case "o/elsewhere":
			carried++
			if f.Chart != "" || f.ChartRevisited != "" {
				t.Errorf("a withdrawn remit left chart %q under stamp %q in the view, want both empty at the tick's head", f.Chart, f.ChartRevisited)
			}
			if n := findEntry(t, parseLogLines(t, log.Bytes()),
				"outcomes: nothing carried; the picture's repository is not admitted"); n["tracker"] != "o/elsewhere#71" {
				t.Errorf("tracker = %v, want o/elsewhere#71: the drop names the picture it dropped", n["tracker"])
			}
		case "o/archive":
			// The forest whose picture was refused on both ticks: nothing new
			// to say about it here, only that it is still listed.
		default:
			t.Errorf("unexpected tracker in %s: the fixture pairs one tracker in each repository", f.Tracker.Repository)
		}
	}
	if carried != 1 {
		t.Fatalf("%d forests carry a picture out of o/elsewhere, want exactly 1: at zero the revocation case above asserts nothing at all", carried)
	}

	// The re-pairing case: a forest's tracker is forge data, so the pair the
	// carry moves can have been drawn from a repository the tracker no longer
	// stands in. Asking only where the pair is travelling to asks a question
	// the drawing never answers — and answers it trivially when the new tracker
	// shares the root's repository — so the drawing's own repository pays the
	// test too, and a role withdrawn from it empties both fields at the head
	// even though the destination is admitted (§forgejo/webhook/repository-roles,
	// §forgejo/observability/the-outcomes-view).
	api.mu.Lock()
	legacy := outcomesTracker(74, "tracker in a repository with a role of its own")
	legacy.Repository = "o/legacy"
	api.repos = []Repository{outcomesRepo("o/out"), testRepoDisabled("o/elsewhere"), testRepo("o/legacy")}
	api.issues[74] = &legacy
	api.blocks[51] = []Issue{legacy}
	api.openIssues["out"] = []Issue{root, rootTwo}
	api.mu.Unlock()
	eng.scopeAt = time.Time{}
	if _, err := eng.recordForests(); err != nil {
		t.Fatalf("recordForests: %v", err)
	}
	eng.writeTrackerBlocks(mustForests(t, eng), chartAt.Add(time.Hour))
	parked, err := eng.Forests()
	if err != nil || len(parked) != 2 {
		t.Fatalf("forests = %+v, %v", parked, err)
	}
	var kept Forest
	for _, f := range parked {
		if f.Root.Number == 51 {
			kept = f
		}
	}
	if kept.Tracker == nil || kept.Tracker.Repository != "o/legacy" || kept.Chart == "" {
		t.Fatalf("root 51 = %+v, want its picture drawn while o/legacy still held a role", kept)
	}

	// The tracker moves into the root's own repository — a second tracker-
	// reading row takes the pairing — and the role in the repository the
	// drawing came from is withdrawn in the same breath.
	api.mu.Lock()
	api.repos = []Repository{outcomesRepo("o/out"), testRepoDisabled("o/elsewhere"), testRepoDisabled("o/legacy")}
	home := outcomesTracker(75, "tracker in the root's own repository")
	home.Repository = "o/out"
	api.openIssues["out"] = []Issue{root, rootTwo, home}
	api.issues[75] = &home
	api.blocks[51] = []Issue{home}
	api.mu.Unlock()
	eng.scopeAt = time.Time{}
	written := log.Len()
	if _, err := eng.recordForests(); err != nil {
		t.Fatalf("recordForests: %v", err)
	}
	moved, err := eng.Forests()
	if err != nil || len(moved) != 2 {
		t.Fatalf("forests = %+v, %v", moved, err)
	}
	paired := 0
	for _, f := range moved {
		if f.Root.Number != 51 {
			continue
		}
		paired++
		if f.Tracker == nil || f.Tracker.Repository != "o/out" {
			t.Fatalf("root 51 = %+v, want its tracker re-paired into o/out", f)
		}
		if f.Chart != "" || f.ChartRevisited != "" {
			t.Errorf("a picture drawn from a withdrawn repository travelled to an admitted one carrying chart %q under stamp %q, want both fields empty",
				f.Chart, f.ChartRevisited)
		}
		if line := findEntry(t, parseLogLines(t, log.Bytes()[written:]),
			"outcomes: nothing carried; the picture's repository is not admitted"); line["drawn"] != "o/legacy" {
			t.Errorf("drawn = %v, want o/legacy: the drop names the repository the drawing came from", line["drawn"])
		}
	}
	if paired != 1 {
		t.Fatalf("%d forests carry root 51, want exactly 1: at zero the re-pairing case above asserts nothing at all", paired)
	}

	// The mirrored re-pairing: the destination is refused while the origin is
	// admitted. Drawing the pair fresh from o/legacy, the tracker is then
	// re-paired into o/archive — a repository no role names — while the role
	// the drawing came from stands. Asking the two ends and keeping only the
	// second answer would let the origin's admission overwrite the refused
	// destination and publish the pair into an unadmitted repository; both
	// answers must count, so the head empties both fields and the Debug line
	// names the refused destination
	// (§forgejo/webhook/repository-roles,
	// §forgejo/observability/the-outcomes-view).
	api.mu.Lock()
	api.repos = []Repository{outcomesRepo("o/out"), testRepoDisabled("o/elsewhere"), testRepo("o/legacy")}
	api.blocks[51] = []Issue{legacy}
	api.openIssues["out"] = []Issue{root, rootTwo}
	api.mu.Unlock()
	eng.scopeAt = time.Time{}
	if _, err := eng.recordForests(); err != nil {
		t.Fatalf("recordForests: %v", err)
	}
	eng.writeTrackerBlocks(mustForests(t, eng), chartAt.Add(2*time.Hour))
	drawn, err := eng.Forests()
	if err != nil {
		t.Fatalf("Forests: %v", err)
	}
	for _, f := range drawn {
		if f.Root.Number == 51 && (f.Tracker == nil || f.Tracker.Repository != "o/legacy" || f.Chart == "") {
			t.Fatalf("root 51 = %+v, want its picture drawn from admitted o/legacy before the mirroring", f)
		}
	}
	api.mu.Lock()
	archive := outcomesTracker(76, "tracker in an unadmitted repository")
	archive.Repository = "o/archive"
	api.issues[76] = &archive
	api.blocks[51] = []Issue{archive}
	api.mu.Unlock()
	eng.scopeAt = time.Time{}
	written = log.Len()
	if _, err := eng.recordForests(); err != nil {
		t.Fatalf("recordForests: %v", err)
	}
	mirrored, err := eng.Forests()
	if err != nil || len(mirrored) != 2 {
		t.Fatalf("forests = %+v, %v", mirrored, err)
	}
	refusedInto := 0
	for _, f := range mirrored {
		if f.Root.Number != 51 {
			continue
		}
		refusedInto++
		if f.Tracker == nil || f.Tracker.Repository != "o/archive" {
			t.Fatalf("root 51 = %+v, want its tracker re-paired into o/archive", f)
		}
		if f.Chart != "" || f.ChartRevisited != "" {
			t.Errorf("a picture travelled into a refused repository carrying chart %q under stamp %q, want both fields empty: the destination's refusal is not advisory",
				f.Chart, f.ChartRevisited)
		}
		if line := findEntry(t, parseLogLines(t, log.Bytes()[written:]),
			"outcomes: nothing carried; the picture's repository is not admitted"); line["tracker"] != "o/archive#76" {
			t.Errorf("tracker = %v, want o/archive#76: the drop names the repository it refused", line["tracker"])
		}
	}
	if refusedInto != 1 {
		t.Fatalf("%d forests carry root 51, want exactly 1: at zero the mirrored case above asserts nothing at all", refusedInto)
	}
}

// mustForests is the view's set, failed loudly.
func mustForests(t *testing.T, eng *Engine) []Forest {
	t.Helper()
	forests, err := eng.Forests()
	if err != nil {
		t.Fatalf("Forests: %v", err)
	}
	return forests
}

// TestForests_CarryFailsClosedOnAnUnknownScope: the carry's admission test
// is a read, and a read can break. Where the remit cannot be answered the
// pair is dropped — a drawing whose remit is unknown is no evidence the
// unauthenticated view may publish — and the log names the failed read at a
// level an operator reads, rather than asserting a refusal the remit never
// made (§forgejo/observability/the-outcomes-view,
// §forgejo/webhook/the-scope-cache, §forgejo/webhook/repository-roles).
func TestForests_CarryFailsClosedOnAnUnknownScope(t *testing.T) {
	var log bytes.Buffer
	eng, api, _, _ := newEngineWithLogger(t, slog.New(slog.NewJSONHandler(&log,
		&slog.HandlerOptions{Level: slog.LevelInfo})))
	api.repos = []Repository{outcomesRepo("o/out"), testRepo("o/elsewhere")}
	root := outcomesRoot(51, "root")
	tracker := outcomesTracker(71, "tracker in an admitted repository")
	tracker.Repository = "o/elsewhere"
	api.openIssues["out"] = []Issue{root}
	api.issues[51] = &root
	api.blocks[51] = []Issue{tracker}

	eng.Reconcile()
	drawn, err := eng.Forests()
	if err != nil || len(drawn) != 1 {
		t.Fatalf("forests = %+v, %v", drawn, err)
	}
	if drawn[0].Chart == "" {
		t.Fatal("the first tick drew no picture into the payload")
	}
	// The set a tick's head publishes: Forests hands out the snapshot itself,
	// so the carry is handed a copy of it, and a fresh enumeration carries no
	// picture of its own — the carry is what fills it before step 5 draws.
	next := append([]Forest{}, drawn...)
	next[0].Chart, next[0].ChartRevisited = "", ""

	// The listing breaks between the walk that enumerated this set and the
	// carry asking it — a forge too flaky to answer the scope by the time the
	// walk finished. Nothing is published from this set, so no reader sees the
	// empty the carry writes into it.
	api.mu.Lock()
	api.listReposErr = errors.New("org listing down")
	api.mu.Unlock()
	eng.scopeAt = time.Time{} // the carry must find the listing, not a cached remit
	eng.carryDrawnPictures(next)

	if len(next) != 1 || next[0].Chart != "" || next[0].ChartRevisited != "" {
		t.Errorf("carried set = %+v, want the forest listed with both picture fields empty", next)
	}
	line := findEntry(t, parseLogLines(t, log.Bytes()),
		"outcomes: cannot tell whether the picture's repository is admitted; nothing carried")
	if msg, _ := line["error"].(string); !strings.Contains(msg, "org listing down") {
		t.Errorf("error = %v, want the failed read it stands for: a refusal is a different fact",
			line["error"])
	}
	if line["tracker"] != "o/elsewhere#71" {
		t.Errorf("tracker = %v, want o/elsewhere#71: the drop names the picture it dropped", line["tracker"])
	}
}

// TestForests_DrawFailureEmptiesThePicture: the view never dresses a
// fresh stamp over a remembered chart. The tick that cannot draw a
// forest — here, a root whose role label lost its readable colour —
// states both picture fields empty, names the failure in the log, and
// leaves the forest itself listed
// (§forgejo/observability/the-outcomes-view,
// §forgejo/reconciliation/the-tracker-block).
func TestForests_DrawFailureEmptiesThePicture(t *testing.T) {
	eng, api, log := chartForestEngine(t, chartFenced())
	eng.Reconcile()
	forests, err := eng.Forests()
	if err != nil || len(forests) != 1 {
		t.Fatalf("forests = %+v, %v", forests, err)
	}
	if forests[0].Chart == "" {
		t.Fatal("the first tick drew no picture into the payload")
	}

	// The root's role label loses its readable colour: the drawing the
	// next tick attempts cannot be made, and inventing a hex is not on
	// the table.
	api.mu.Lock()
	api.issues[1].LabelColours = nil
	api.mu.Unlock()
	eng.Reconcile()

	forests, err = eng.Forests()
	if err != nil {
		t.Fatalf("Forests after the failed draw: %v", err)
	}
	if forests[0].Chart != "" || forests[0].ChartRevisited != "" {
		t.Errorf("chart = %q stamp = %q, want both empty for a tick that drew nothing — never yesterday's chart under today's stamp",
			forests[0].Chart, forests[0].ChartRevisited)
	}
	line := findEntry(t, parseLogLines(t, log.Bytes()),
		"reconciliation: tracker current-state block not written")
	if line["tracker"] != "o/out#2" {
		t.Errorf("the failure line names %v, want o/out#2: the failure is one forest's own", line["tracker"])
	}
}

// TestForests_ChartStampIsRenderedInTheConfiguredZone: the payload's
// recency is the drawing moment spelled in the configured zone, the way
// the block's stamp is — a CET rendering reads as CET, and the view
// interprets nothing (§forgejo/config/tracker-chart,
// §forgejo/observability/the-outcomes-view).
func TestForests_ChartStampIsRenderedInTheConfiguredZone(t *testing.T) {
	sydney, err := time.LoadLocation("Australia/Sydney")
	if err != nil {
		t.Skip("no tzdata to load Australia/Sydney with")
	}
	eng, api, _, _ := newTestEngine(t)
	chartWire(api, chartFenced())
	eng.cfg.TrackerTimezone = "Australia/Sydney"
	eng.Reconcile()

	forests, err := eng.Forests()
	if err != nil {
		t.Fatalf("Forests: %v", err)
	}
	if len(forests) != 1 || forests[0].ChartRevisited == "" {
		t.Fatalf("forests = %+v, want the drawn forest carrying its stamp", forests)
	}
	stamped, err := time.ParseInLocation(stampLayout, forests[0].ChartRevisited, sydney)
	if err != nil {
		t.Fatalf("chart_revisited = %q: %v", forests[0].ChartRevisited, err)
	}
	if stamped.UTC().Format(stampLayout) == forests[0].ChartRevisited {
		t.Errorf("chart_revisited = %q, want the drawing moment spelled in %s rather than as UTC", forests[0].ChartRevisited, sydney)
	}
}

// --- current versus past (s-git/maitred#266) ---

// TestForests_ClosedRootOpenTrackerIsCurrent: a forest whose ideation root
// closed while its tracker stayed open is still a current forest — current
// means the root OR the tracker exists and is open, not the root alone —
// and the walk finds it in the same stateless listing it pays for an
// open root, with the same one blocks read per forest
// (§forgejo/observability/the-outcomes-view).
func TestForests_ClosedRootOpenTrackerIsCurrent(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	api.repos = []Repository{outcomesRepo("o/out")}
	root := outcomesRoot(51, "the idea, filed and finished")
	root.State = "closed"
	tracker := outcomesTracker(71, "the work still in flight")
	api.openIssues["out"] = []Issue{tracker}
	api.closedIssues["out"] = []Issue{root}
	api.blocks[51] = []Issue{tracker}

	forests, err := eng.enumerateForests()
	if err != nil {
		t.Fatalf("enumerateForests: %v", err)
	}
	if len(forests) != 1 {
		t.Fatalf("forests = %d, want the closed root's forest enumerated", len(forests))
	}
	f := forests[0]
	if f.Root.Number != 51 || f.Root.State != "closed" {
		t.Errorf("root = %+v, want closed ideation issue 51", f.Root)
	}
	if f.Tracker == nil || f.Tracker.Number != 71 || f.Tracker.State != "open" {
		t.Errorf("tracker = %+v, want open outcome issue 71", f.Tracker)
	}
	// The walk paid one stateless listing for the repository and the
	// same one blocks read per forest row the open walk pays
	// (§forgejo/observability/the-outcomes-view).
	if n := api.callCount("ListAllIssues:out"); n != 1 {
		t.Errorf("the walk made %d listings of o/out, want 1", n)
	}
	if n := api.callCount("IssueBlocks:out/51"); n != 1 {
		t.Errorf("the walk made %d blocks reads of the root, want 1", n)
	}

	// Both rows open the door: the forest is current, not past.
	if _, err := eng.recordForests(); err != nil {
		t.Fatalf("recordForests: %v", err)
	}
	current, err := eng.Forests()
	if err != nil || len(current) != 1 || current[0].Root.Number != 51 {
		t.Errorf("Forests = %+v, %v, want the root-or-tracker-open forest", current, err)
	}
	if !current[0].WarningsWalked {
		t.Errorf("a current row says its warnings were not walked, want the walk's answer: %+v", current[0].Warnings)
	}
	_, past, err := eng.ForestSplit()
	if err != nil || len(past) != 0 {
		t.Errorf("ForestSplit's past half = %+v, %v, want none: an open tracker makes this forest current", past, err)
	}
}

// TestForests_BothClosedIsPast: a forest whose root and tracker are both
// closed leaves the current list for the past one, named by its ideation
// and tracker tickets with their closed states, and states its warnings
// as not walked without paying the warning walk's reads
// (§forgejo/observability/the-outcomes-view).
func TestForests_BothClosedIsPast(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	api.repos = []Repository{outcomesRepo("o/out")}
	root := outcomesRoot(51, "a forest that finished")
	root.State = "closed"
	tracker := outcomesTracker(71, "closed with its forest")
	tracker.State = "closed"
	// One member outlived its forest — an open landing-set row. A
	// warning walk over this fixture would read the repository's pull
	// requests, which is what makes the zero count below mean what it
	// says: the past row states its warnings not walked rather than
	// paying that read (§forgejo/observability/the-warnings-a-forest-carries).
	member := Issue{
		Number: 91, Title: "approved, never merged", State: "open",
		Labels: []string{LabelAgenticAutoMerge}, HTMLURL: issueURL(91),
	}
	api.closedIssues["out"] = []Issue{root, tracker}
	api.openIssues["out"] = []Issue{member}
	api.blocks[51] = []Issue{tracker, member}

	if _, err := eng.recordForests(); err != nil {
		t.Fatalf("recordForests: %v", err)
	}
	current, err := eng.Forests()
	if err != nil {
		t.Fatalf("Forests: %v", err)
	}
	if len(current) != 0 {
		t.Errorf("Forests = %+v, want empty: neither ticket is open", current)
	}
	_, past, err := eng.ForestSplit()
	if err != nil {
		t.Fatalf("ForestSplit: %v", err)
	}
	if len(past) != 1 {
		t.Fatalf("the split's past half = %d, want the finished forest", len(past))
	}
	f := past[0]
	if f.Root.State != "closed" || f.Tracker == nil || f.Tracker.State != "closed" {
		t.Errorf("past row = %+v, want both tickets closed", f)
	}
	if len(f.Warnings) != 0 || f.WarningsWalked {
		t.Errorf("past warnings = %+v (walked: %v), want the empty stated as not walked", f.Warnings, f.WarningsWalked)
	}
	// A finished forest pays no warning-walk reads: the walk this
	// fixture would act on — listing the repository's open pull
	// requests for the surviving member — never runs for a past row
	// (§forgejo/observability/the-warnings-a-forest-carries).
	if n := api.callCount("ListOpenPullRequests"); n != 0 {
		t.Errorf("the past walk made %d pull-request listings, want 0", n)
	}
}

// TestForests_PastRowStatesAnAbsentTracker: a closed root that blocks no
// tracker is a past forest whose gate is stated absent — the same honest
// absence the current rows state — and it carries no picture, having been
// seen only after it was done (§forgejo/observability/the-outcomes-view).
func TestForests_PastRowStatesAnAbsentTracker(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	api.repos = []Repository{outcomesRepo("o/out")}
	root := outcomesRoot(51, "an idea that closed untracked")
	root.State = "closed"
	api.closedIssues["out"] = []Issue{root}

	if _, err := eng.recordForests(); err != nil {
		t.Fatalf("recordForests: %v", err)
	}
	_, past, err := eng.ForestSplit()
	if err != nil {
		t.Fatalf("ForestSplit: %v", err)
	}
	if len(past) != 1 {
		t.Fatalf("the split's past half = %d, want the closed root with its absence stated", len(past))
	}
	if past[0].Tracker != nil {
		t.Errorf("past tracker = %+v, want the absence stated", past[0].Tracker)
	}
	if past[0].Chart != "" || past[0].ChartRevisited != "" {
		t.Errorf("a past forest never seen while current carries chart %q under stamp %q, want both fields empty",
			past[0].Chart, past[0].ChartRevisited)
	}
	if current, err := eng.Forests(); err != nil || len(current) != 0 {
		t.Errorf("Forests = %+v, %v, want empty", current, err)
	}
}

// TestForests_PastPictureIsFrozen: a forest's picture pair freezes on the
// tick it was last current. Step 5 addresses the current forests alone —
// the engine does not edit a closed tracker's history — so the past row
// carries the last drawing made while the forest lived, under the stamp
// belonging to that drawing, and no later tick writes that tracker or
// redraws that pair (§forgejo/observability/the-outcomes-view,
// §forgejo/reconciliation/the-tracker-block).
func TestForests_PastPictureIsFrozen(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	chartWire(api, chartFenced())

	first, err := eng.recordForests()
	if err != nil || len(first) != 1 {
		t.Fatalf("forests = %+v, %v", first, err)
	}
	eng.writeTrackerBlocks(first, chartAt)
	drawn, err := eng.Forests()
	if err != nil || len(drawn) != 1 || drawn[0].Chart == "" {
		t.Fatalf("Forests = %+v, %v, want the tick's drawing", drawn, err)
	}
	wantChart, wantStamp := drawn[0].Chart, drawn[0].ChartRevisited
	writesBefore := api.callCount("UpdateIssueBody")
	trackerReadsBefore := api.callCount("GetIssue:out/2")

	// The forest finishes: root and tracker both close — with the
	// closed_at a real forge reports, so the graph the mutation
	// below would redraw is one the renderer accepts rather than a
	// refusal doing the pin's work (§forgejo/renderer/closed-members).
	api.mu.Lock()
	root := *api.issues[1]
	root.State = "closed"
	root.ClosedAt = chartClosedAt
	tracker := *api.issues[2]
	tracker.State = "closed"
	tracker.ClosedAt = chartClosedAt
	api.issues[1] = &root
	api.issues[2] = &tracker
	api.openIssues["out"] = nil
	api.closedIssues["out"] = []Issue{root, tracker}
	// The pairing reads the tracker's row from the blocks listing, so
	// that row is where the tracker's closed state has to show.
	api.blocks[1] = []Issue{tracker}
	api.mu.Unlock()

	second, err := eng.recordForests()
	if err != nil || len(second) != 1 {
		t.Fatalf("forests = %+v, %v", second, err)
	}
	eng.writeTrackerBlocks(second, chartAt.Add(time.Hour))

	_, past, err := eng.ForestSplit()
	if err != nil || len(past) != 1 {
		t.Fatalf("the split's past half = %+v, %v, want the finished forest", past, err)
	}
	if past[0].Chart != wantChart || past[0].ChartRevisited != wantStamp {
		t.Errorf("the past row carries chart %q under stamp %q, want the frozen pair %q under %q",
			past[0].Chart, past[0].ChartRevisited, wantChart, wantStamp)
	}
	if current, err := eng.Forests(); err != nil || len(current) != 0 {
		t.Errorf("Forests = %+v, %v, want empty once the forest is past", current, err)
	}
	// Step 5 never reaches for the closed tracker at all — not one
	// read, not one write. The read pin is what makes this mean the
	// skip rather than a write the forge would have coalesced away:
	// with the skip removed the tick reads the tracker to compare a
	// body the newer stamp would have changed, so both counts move
	// (§forgejo/reconciliation/the-tracker-block).
	if n := api.callCount("GetIssue:out/2"); n != trackerReadsBefore {
		t.Errorf("the tick read the closed tracker %d times after the forest closed, want 0", n-trackerReadsBefore)
	}
	if n := api.callCount("UpdateIssueBody"); n != writesBefore {
		t.Errorf("the tick made %d tracker writes after the forest closed, want 0", n-writesBefore)
	}
}

// TestForests_ListingRowOnTwoPagesEnumeratesOnce: the stateless listing is
// the forge's paged answer, and paging a repository whose ordering shifted
// mid-walk can hand one row on two pages. The walk keeps each issue number
// once, so the straddling root is one forest, counted once, and pays its
// one blocks read once rather than once per reading
// (§forgejo/observability/the-outcomes-view).
func TestForests_ListingRowOnTwoPagesEnumeratesOnce(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	api.repos = []Repository{outcomesRepo("o/out")}
	root := outcomesRoot(51, "a root that straddles a page boundary")
	tracker := outcomesTracker(71, "the work it gates")
	// Naming the root in both fixtures is the straddle: the stateless
	// listing answers from both, so issue 51 arrives twice the way two
	// pages would hand it over
	// (§forgejo/observability/the-outcomes-view).
	api.openIssues["out"] = []Issue{root}
	api.closedIssues["out"] = []Issue{root, tracker}
	api.blocks[51] = []Issue{tracker}

	if _, err := eng.recordForests(); err != nil {
		t.Fatalf("recordForests: %v", err)
	}
	current, past, err := eng.ForestSplit()
	if err != nil {
		t.Fatalf("ForestSplit: %v", err)
	}
	if len(current) != 1 || len(past) != 0 {
		t.Errorf("the split counted %d current and %d past, want one forest on one tab: %+v",
			len(current), len(past), current)
	}
	if n := api.callCount("IssueBlocks:out/51"); n != 1 {
		t.Errorf("the duplicated row made %d blocks reads of the root, want 1", n)
	}
}

// TestForests_ForestSplitIsOnePicture: the split answers both halves
// from one snapshot — disjoint, together naming the recorded set, and
// both halves nil when the view cannot be vouched for — the promise a
// pair of separate reads could break when a sweep lands between them
// (§forgejo/observability/the-outcomes-view).
func TestForests_ForestSplitIsOnePicture(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	api.repos = []Repository{outcomesRepo("o/out")}
	live := outcomesRoot(51, "still ideating")
	liveTracker := outcomesTracker(71, "in flight")
	stageForest(api, live, liveTracker)
	done := outcomesRoot(52, "a forest that finished")
	done.State = "closed"
	doneTracker := outcomesTracker(72, "closed with its forest")
	doneTracker.State = "closed"
	api.closedIssues["out"] = []Issue{done, doneTracker}
	api.blocks[52] = []Issue{doneTracker}

	if current, past, err := eng.ForestSplit(); current != nil || past != nil || !errors.Is(err, errForestsUnavailable) {
		t.Errorf("ForestSplit before any sweep = %+v, %+v, %v, want both halves nil with the absence stated", current, past, err)
	}
	if _, err := eng.recordForests(); err != nil {
		t.Fatalf("recordForests: %v", err)
	}
	current, past, err := eng.ForestSplit()
	if err != nil {
		t.Fatalf("ForestSplit: %v", err)
	}
	if len(current) != 1 || current[0].Root.Number != 51 {
		t.Errorf("current half = %+v, want the living forest", current)
	}
	if len(past) != 1 || past[0].Root.Number != 52 {
		t.Errorf("past half = %+v, want the finished forest", past)
	}
}
