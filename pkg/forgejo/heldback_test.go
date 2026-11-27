package forgejo

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Fixtures and tests for the held-back view: the two waits the warning
// walk already asks, flattened across forests with the waited-on
// object's labels alongside
// (§forgejo/observability/the-held-back-view).

// heldBackForest loads api with a current forest of o/out — root 51,
// tracker 71 — carrying two members: a human task whose blockers are
// all closed and a landing member whose approved delivery has gone
// stale. Both waits the held-back view splits are therefore standing
// in the one forest (§forgejo/observability/the-held-back-view).
func heldBackForest(t *testing.T, api *fakeAPI) (human, delivery Issue, pr PullRequest) {
	t.Helper()
	human = warningsMember(80, LabelHumanTask)
	// The body is the canary declared below: prose the engine reads and
	// never serves (§forgejo/observability/the-held-back-view).
	human.Body = heldBackCanary
	landing := warningsMember(81, LabelAgenticAutoMerge)
	warningsForest(api, human)
	api.blocks[51] = append(api.blocks[51], landing)
	api.issues[landing.Number] = &landing
	closed := Issue{
		Number: 82, Title: "landed", State: "closed", Repository: "o/r",
		UpdatedAt: time.Now().Add(-90 * time.Minute),
	}
	api.deps[human.Number] = []Issue{closed}

	pr = *testPR(980, "open", true)
	pr.Body = "Fixes " + landing.Repository + "#" + strconv.Itoa(landing.Number)
	pr.Labels = []string{"enhancement"}
	api.openPulls["r"] = []PullRequest{pr}
	api.pulls[pr.Number] = &pr
	api.reviews[pr.Number] = []Review{{
		Event:       ReviewApproved,
		Author:      "bob",
		CommitID:    pr.HeadSHA,
		SubmittedAt: time.Now().Add(-2 * time.Hour),
	}}
	return human, landing, pr
}

// heldBackCanary is the sentence the fixtures leave in a walked
// object's body. Asserting its absence — rather than the absence of a
// `description` key — is what pins the rule that no row carries an
// object body: a row serving the prose under any other field name
// still shows it here (§forgejo/observability/the-held-back-view).
const heldBackCanary = "Waiting on the person the label names."

// TestHeldBack_RowsCarryLabels: each wait the walk records becomes a
// held-back row carrying the reference, kind and age the warning
// carries alongside the waited-on object's labels — and no object
// body, which the canary above names
// (§forgejo/observability/the-held-back-view,
// §forgejo/observability/the-warnings-a-forest-carries).
func TestHeldBack_RowsCarryLabels(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	human, _, pr := heldBackForest(t, api)

	forests, err := eng.enumerateForests()
	if err != nil {
		t.Fatalf("enumerateForests: %v", err)
	}
	held := forests[0].heldBack
	if len(held) != 2 {
		t.Fatalf("heldBack = %+v, want the human wait and the stalled delivery", held)
	}
	byNumber := map[int]HeldBackItem{}
	for _, h := range held {
		byNumber[h.Number] = h
	}
	h := byNumber[human.Number]
	if h.Kind != WarningHumanTaskBlocking {
		t.Errorf("member row kind = %q, want %q", h.Kind, WarningHumanTaskBlocking)
	}
	if h.Repository != "o/r" || h.Title == "" || h.HTMLURL == "" {
		t.Errorf("member row reference = %+v, want the member with its fields", h)
	}
	if !containsLabel(h.Labels, LabelHumanTask) {
		t.Errorf("member row labels = %v, want the labels the object carries", h.Labels)
	}
	if h.AgeSeconds < 90*60-60 || h.AgeSeconds > 90*60+60 {
		t.Errorf("member row age_seconds = %v, want the ninety minutes since the last blocker closed", h.AgeSeconds)
	}
	d := byNumber[pr.Number]
	if d.Kind != WarningStaleAgenticMerge {
		t.Errorf("delivery row kind = %q, want %q", d.Kind, WarningStaleAgenticMerge)
	}
	if d.Repository != "o/r" || d.Number != pr.Number {
		t.Errorf("delivery row reference = %s#%d, want the delivering PR o/r#%d", d.Repository, d.Number, pr.Number)
	}
	if !containsLabel(d.Labels, "enhancement") {
		t.Errorf("delivery row labels = %v, want the pull request's labels", d.Labels)
	}
	// No row carries the walked object's prose, under this name or any
	// other: encoding the rows and naming the canary is what makes a
	// future field that smuggles a body back fail here rather than only
	// at the port (§forgejo/observability/the-held-back-view).
	encoded, err := json.Marshal(held)
	if err != nil {
		t.Fatalf("marshal rows: %v", err)
	}
	if strings.Contains(string(encoded), heldBackCanary) {
		t.Errorf("rows %s, want the walked object's prose on no field of any row", encoded)
	}
	// The rows ride alongside the warnings rather than replacing them:
	// the forest payload's warning list is unchanged
	// (§forgejo/observability/the-warnings-a-forest-carries).
	if len(forests[0].Warnings) != 2 {
		t.Errorf("warnings = %+v, want both waits still stated as warnings", forests[0].Warnings)
	}
}

// TestHeldBack_PastForestContributesNothing: a forest whose tickets
// are both closed contributes no held-back rows, as it contributes no
// walked warnings — and the view spells its silence as two empty
// lists rather than nulls (§forgejo/observability/the-held-back-view,
// §forgejo/observability/the-outcomes-view).
func TestHeldBack_PastForestContributesNothing(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	human, _, _ := heldBackForest(t, api)
	// Close both of the forest's tickets: the walk stops warning for
	// it, and the view stops naming its waits.
	root := api.openIssues["out"][0]
	root.State = "closed"
	tracker := api.openIssues["out"][1]
	tracker.State = "closed"
	api.openIssues["out"] = []Issue{root, tracker}
	api.blocks[51] = []Issue{tracker, human}

	forests, err := eng.enumerateForests()
	if err != nil {
		t.Fatalf("enumerateForests: %v", err)
	}
	if forests[0].heldBack != nil {
		t.Errorf("heldBack = %+v, want nothing recorded for a past forest", forests[0].heldBack)
	}

	eng.recordForests()
	humans, stalled, err := eng.HeldBackSplit()
	if err != nil {
		t.Fatalf("HeldBackSplit: %v", err)
	}
	if humans == nil || len(humans) != 0 {
		t.Errorf("held_by_humans = %+v, want the honest empty", humans)
	}
	if stalled == nil || len(stalled) != 0 {
		t.Errorf("stalled_deliveries = %+v, want the honest empty", stalled)
	}
}

// TestHeldBack_OneObjectHeldBackTwiceIsOneRow: two forests whose
// members include the same waited-on object hold the organisation back
// once, so the flattened view states the wait once
// (§forgejo/observability/the-held-back-view).
func TestHeldBack_OneObjectHeldBackTwiceIsOneRow(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	human, _, _ := heldBackForest(t, api)
	// A second forest — another root and tracker — blocks the same
	// human task the first forest's root does.
	root2 := outcomesRoot(55, "sibling root")
	tracker2 := outcomesTracker(75, "sibling tracker")
	api.openIssues["out"] = append(api.openIssues["out"], root2, tracker2)
	api.blocks[root2.Number] = []Issue{tracker2, human}
	api.issues[root2.Number] = &root2
	api.issues[tracker2.Number] = &tracker2

	eng.recordForests()
	humans, stalled, err := eng.HeldBackSplit()
	if err != nil {
		t.Fatalf("HeldBackSplit: %v", err)
	}
	if len(humans) != 1 {
		t.Errorf("held_by_humans = %+v, want the shared member stated once", humans)
	}
	if len(stalled) != 1 {
		t.Errorf("stalled_deliveries = %+v, want the one stalled delivery", stalled)
	}
}

// TestHeldBack_ViewIsTheLastSweepsSet: the view serves the set the
// sweep tick recorded under the forest snapshot's own discipline —
// unavailable before the first enumeration, free of forge reads once
// recorded, and stale rather than served-as-fresh once the set has
// aged past one reconcile interval
// (§forgejo/observability/the-held-back-view,
// §forgejo/observability/the-outcomes-view).
func TestHeldBack_ViewIsTheLastSweepsSet(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	heldBackForest(t, api)

	if humans, stalled, err := eng.HeldBackSplit(); !errors.Is(err, errForestsUnavailable) || humans != nil || stalled != nil {
		t.Fatalf("HeldBackSplit = %+v, %+v, %v, want errForestsUnavailable and nils before the first enumeration", humans, stalled, err)
	}

	eng.Reconcile()
	humans, stalled, err := eng.HeldBackSplit()
	if err != nil {
		t.Fatalf("HeldBackSplit after Reconcile: %v", err)
	}
	if len(humans) != 1 || humans[0].Kind != WarningHumanTaskBlocking {
		t.Errorf("held_by_humans = %+v, want the one human wait", humans)
	}
	if len(stalled) != 1 || stalled[0].Kind != WarningStaleAgenticMerge {
		t.Errorf("stalled_deliveries = %+v, want the one stalled delivery", stalled)
	}
	readsBefore := api.callCount("")
	if _, _, err := eng.HeldBackSplit(); err != nil {
		t.Fatalf("HeldBackSplit (second read): %v", err)
	}
	if n := api.callCount("") - readsBefore; n != 0 {
		t.Errorf("serving the view made %d API call(s), want 0: the request path reads no forge", n)
	}

	eng.forestsMu.Lock()
	eng.forestsAt = time.Now().Add(-eng.cfg.ReconcileInterval - time.Minute)
	eng.forestsMu.Unlock()
	if humans, stalled, err := eng.HeldBackSplit(); !errors.Is(err, errForestsStale) || humans != nil || stalled != nil {
		t.Errorf("HeldBackSplit = %+v, %+v, %v, want errForestsStale and nils on an aged set", humans, stalled, err)
	}
}

// containsLabel reports whether names holds label.
func containsLabel(names []string, label string) bool {
	for _, n := range names {
		if n == label {
			return true
		}
	}
	return false
}
