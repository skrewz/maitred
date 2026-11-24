package forgejo

import (
	"bytes"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"

	"maitred/pkg/queue"
)

// The fixtures below are one forest as the forge reports it: an ideation root
// and the tracker it blocks, members in a second repository (one closed, one
// claimed by a person), a grouping issue, and a pull request that delivers a
// member and draws nothing (§forgejo/renderer/identifiers-and-shapes,
// §forgejo/reconciliation/the-tracker-block).

// chartRootColour and chartTrackerColour are the two hexes a role label gives
// the engine: the root's `ideation` colour and the tracker's `outcome` colour.
// A forest whose rows carry neither cannot be drawn, because the engine invents
// no colour (§forgejo/renderer/classes-and-strokes).
var (
	chartRootColour    = map[string]string{LabelIdeation: "#5319e7"}
	chartTrackerColour = map[string]string{LabelOutcome: "#1d76db"}
)

// chartAt is the moment one tick stamps.
var chartAt = time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)

func chartRoot() Issue {
	return Issue{
		Number:       1,
		Title:        "render tracker mermaid in-engine",
		State:        "open",
		Labels:       []string{LabelIdeation},
		LabelColours: chartRootColour,
		Repository:   "o/out",
		UpdatedAt:    testUpdatedAt,
		HTMLURL:      "https://forge.example.com/o/out/issues/1",
	}
}

func chartTracker(body string) Issue {
	return Issue{
		Number:       2,
		Title:        "outcome tracker",
		State:        "open",
		Labels:       []string{LabelOutcome},
		LabelColours: chartTrackerColour,
		Repository:   "o/out",
		UpdatedAt:    testUpdatedAt,
		Body:         body,
		HTMLURL:      "https://forge.example.com/o/out/issues/2",
	}
}

// chartRow builds a member in the work repository. The blocks listing is the
// row's only seat in these fixtures, so it carries its own URL the way the
// forge's does.
func chartRow(number int, title string, labels []string, state string, closedAt time.Time) Issue {
	return Issue{
		Number:     number,
		Title:      title,
		State:      state,
		Labels:     labels,
		Repository: "o/work",
		UpdatedAt:  testUpdatedAt,
		ClosedAt:   closedAt,
		HTMLURL:    "https://forge.example.com/o/work/issues/" + strconv.Itoa(number),
	}
}

// chartFenced is a tracker body as an agent left it: outcome prose the engine
// must not touch, the region it does own with a stale chart in it, and a tail of
// prose after the region.
func chartFenced() string {
	return "## Outcome\n\nThe thing we said we would ship.\n\n" +
		stateHeading + "\n\n" + fenceBegin + "\n\nLast revisited: 2026-01-01 00:00 UTC\n\n```mermaid\nflowchart TD\n%% stale\n```\n\n" +
		fenceEnd + "\n\n## Notes\n\nprose a human owns\n"
}

// chartForestEngine wires the fixtures into an engine whose log a test can
// read.
func chartForestEngine(t *testing.T, trackerBody string) (*Engine, *fakeAPI, *bytes.Buffer) {
	eng, api, _, log := chartForestQueue(t, trackerBody)
	return eng, api, log
}

// chartForestQueue is the same wiring for the tests that also watch the
// queue — which is what the loop-prevention cases read, the write having
// landed once the tick drew it (§forgejo/decisions/loop-prevention).
func chartForestQueue(t *testing.T, trackerBody string) (*Engine, *fakeAPI, *fakeQueue, *bytes.Buffer) {
	t.Helper()
	eng, api, q, _ := newTestEngine(t)
	var log bytes.Buffer
	eng.log = slog.New(slog.NewJSONHandler(&log, &slog.HandlerOptions{Level: slog.LevelDebug}))
	chartWire(api, trackerBody)
	return eng, api, q, &log
}

// chartSyncBody is the grouping issue's body: the order its members land in,
// which the landing set's digest reads and a stamp would move
// (§forgejo/decisions/sync-set).
const chartSyncBody = "land 3, then 4, then 5, in that order"

// chartClosedAt is the moment the fixtures' closed rows carry — the forge's
// own close time, the only one a drawing may stamp (§forgejo/renderer/closed-members).
var chartClosedAt = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

// chartWire hangs the forest on the fake: root 1, tracker 2, members 3 to 5 (3
// open, 4 closed, 5 a person's), grouping issue 6, and the delivering pull
// request 7.
func chartWire(api *fakeAPI, trackerBody string) {
	root := chartRoot()
	tracker := chartTracker(trackerBody)
	closedAt := chartClosedAt
	member := chartRow(3, "the purge, one command", nil, "open", time.Time{})
	closed := chartRow(4, "tombstone and retention", nil, "closed", closedAt)
	human := chartRow(5, "revoke signing keys", []string{LabelHumanTask}, "open", time.Time{})
	sync := Issue{
		Number:     6,
		Title:      "set: 3, 4, 5 in order",
		State:      "open",
		Labels:     []string{LabelSync},
		Repository: "o/out",
		UpdatedAt:  testUpdatedAt,
		Body:       chartSyncBody,
		HTMLURL:    "https://forge.example.com/o/out/issues/6",
	}
	delivery := Issue{
		Number: 7, Title: "the purge", State: "open", Repository: "o/work",
		IsPull: true, HTMLURL: "https://forge.example.com/o/work/pulls/7",
	}

	api.repos = []Repository{testRepoBothRoles("o/out"), testRepo("o/work")}
	api.issues[root.Number] = &root
	api.issues[tracker.Number] = &tracker
	api.issues[member.Number] = &member
	api.issues[sync.Number] = &sync
	api.blocks[root.Number] = []Issue{tracker, member, closed, human, sync, delivery}
	api.blocks[member.Number] = []Issue{sync}
	api.blocks[closed.Number] = []Issue{member}
	api.blocks[human.Number] = []Issue{member}
	api.openIssues["out"] = []Issue{root, tracker, sync}
	api.openIssues["work"] = []Issue{member, human}
}

func chartStamp(at time.Time, loc *time.Location) string {
	return "Last revisited by maître d' engine: " + at.In(loc).Format(stampLayout)
}

// chartForests is the one forest the fixtures describe, as the outcomes view
// would report it (§forgejo/observability/the-outcomes-view).
func chartForests() []Forest {
	tracker := ForestIssue{Number: 2, Repository: "o/out", Title: "outcome tracker", State: "open"}
	return []Forest{{
		Repository: "o/out",
		Root:       ForestIssue{Number: 1, Repository: "o/out", Title: "render tracker mermaid in-engine", State: "open"},
		Tracker:    &tracker,
	}}
}

// TestTrackerBlock_WritesIntoFencePair: the block lands between the pair, and
// every byte outside it survives the write unchanged
// (§forgejo/reconciliation/the-tracker-block).
func TestTrackerBlock_WritesIntoFencePair(t *testing.T) {
	eng, api, log := chartForestEngine(t, chartFenced())
	if _, err := eng.writeTrackerBlock(chartForests()[0], chartAt); err != nil {
		t.Fatalf("writeTrackerBlock: %v", err)
	}
	// The engine's one mutation is logged when it lands, so a description
	// that changed at an odd hour can be attributed to the engine rather
	// than to an agent (§forgejo/observability/the-decision-log).
	if !strings.Contains(log.String(), "tracker current-state block written") || !strings.Contains(log.String(), "o/out#2") {
		t.Errorf("log = %s, want the write logged with the tracker", log.String())
	}

	written := mustWritten(t, api, "out", 2)
	head := "## Outcome\n\nThe thing we said we would ship.\n\n" + stateHeading + "\n\n" + fenceBegin + "\n\n"
	if !strings.HasPrefix(written, head) {
		t.Errorf("head of the body = %q, want the outcome prose and heading up to the opener unchanged", firstLines(written, 5))
	}
	tail := "\n" + fenceEnd + "\n\n## Notes\n\nprose a human owns\n"
	if !strings.HasSuffix(written, tail) {
		t.Errorf("tail of the body = %q, want the prose after the region unchanged", lastLines(written, 5))
	}
	region := regionOf(written)
	if !strings.HasPrefix(region, chartStamp(chartAt, time.UTC)+"\n\n```mermaid\nflowchart TD\n") {
		t.Errorf("the region = %q, want the stamp then the Mermaid fence", firstLines(region, 3))
	}
	if !strings.Contains(region, "%% forest out#1 rendered by maitred") {
		t.Error("the region carries no note naming the forest the drawing shows")
	}
	if strings.Contains(written, "stale") {
		t.Error("the stale chart survived the write")
	}
	if got := api.writeCount(); got != 1 {
		t.Errorf("wrote %d issues, want only the tracker", got)
	}
}

// TestTrackerBlock_StampIsStampedAsTheEngines: the stamp line the engine
// writes names the engine — `Last revisited by maître d' engine:` — so a reader
// of a fresh picture can say from the stamp alone, without logs or a diff,
// that it was the engine's write path that drew it; the plain `Last
// revisited:` line is left to other authors and appears nowhere in the
// region the engine writes (s-git/maitred#251,
// §forgejo/reconciliation/the-tracker-block).
func TestTrackerBlock_StampIsStampedAsTheEngines(t *testing.T) {
	eng, api, _ := chartForestEngine(t, chartFenced())
	if _, err := eng.writeTrackerBlock(chartForests()[0], chartAt); err != nil {
		t.Fatalf("writeTrackerBlock: %v", err)
	}
	region := regionOf(mustWritten(t, api, "out", 2))
	want := "Last revisited by maître d' engine: " + chartAt.In(time.UTC).Format(stampLayout)
	if !strings.HasPrefix(region, want+"\n") {
		t.Errorf("the region's stamp line = %q, want the engine's attributed stamp %q", firstLines(region, 1), want)
	}
	if strings.Contains(region, "Last revisited:") {
		t.Errorf("the region the engine wrote still carries the unattributed stamp:\n%s", firstLines(region, 3))
	}
}

// TestTrackerBlock_NoFencePairInsertsFirstHeading: a tracker whose description
// carries no pair gains the block as its first heading, the prose found in the
// body following it in the order it was in
// (§forgejo/reconciliation/the-tracker-block).
func TestTrackerBlock_NoFencePairInsertsFirstHeading(t *testing.T) {
	body := "## Outcome\n\nWe will ship the purge.\n\n## Notes\n\nnothing yet\n"
	eng, api, _ := chartForestEngine(t, body)
	if _, err := eng.writeTrackerBlock(chartForests()[0], chartAt); err != nil {
		t.Fatalf("writeTrackerBlock: %v", err)
	}
	written := mustWritten(t, api, "out", 2)
	head := stateHeading + "\n\n" + fenceBegin + "\n\n" + chartStamp(chartAt, time.UTC) + "\n\n```mermaid\n"
	if !strings.HasPrefix(written, head) {
		t.Errorf("head of the body = %q, want the block as the body's first heading", firstLines(written, 5))
	}
	if !strings.HasSuffix(written, "\n"+fenceEnd+"\n\n"+body) {
		t.Errorf("tail of the body = %q, want the body found after the region", lastLines(written, 6))
	}
}

// TestTrackerBlock_TwoFencePairsWriteNothingAndSaySo: which region belongs to
// this forest is not guessable, so the tracker is left alone and the sweep names
// it (§forgejo/reconciliation/the-tracker-block).
func TestTrackerBlock_TwoFencePairsWriteNothingAndSaySo(t *testing.T) {
	body := chartFenced() + "\n" + fenceBegin + "\n\nanother forest\n\n" + fenceEnd + "\n"
	eng, api, log := chartForestEngine(t, body)
	_, err := eng.writeTrackerBlock(chartForests()[0], chartAt)
	if !errors.Is(err, errFenceAmbiguous) {
		t.Fatalf("writeTrackerBlock = %v, want it to report the ambiguity", err)
	}
	if _, ok := api.writtenBody("out", 2); ok {
		t.Error("an ambiguous tracker was written")
	}
	eng.writeTrackerBlocks(chartForests(), chartAt)
	if !strings.Contains(log.String(), "ambiguous") || !strings.Contains(log.String(), "o/out#2") {
		t.Errorf("log = %s, want the refusal to name the ambiguous tracker", log.String())
	}
}

// TestTrackerBlock_StampMovesWhileGraphStands: a second sweep over the same
// graph rewrites the stamp and nothing else
// (§forgejo/reconciliation/the-tracker-block, §forgejo/renderer/determinism).
func TestTrackerBlock_StampMovesWhileGraphStands(t *testing.T) {
	eng, api, _ := chartForestEngine(t, chartFenced())
	if _, err := eng.writeTrackerBlock(chartForests()[0], chartAt); err != nil {
		t.Fatalf("first writeTrackerBlock: %v", err)
	}
	first := mustWritten(t, api, "out", 2)

	later := chartAt.Add(time.Minute)
	if _, err := eng.writeTrackerBlock(chartForests()[0], later); err != nil {
		t.Fatalf("second writeTrackerBlock: %v", err)
	}
	second := mustWritten(t, api, "out", 2)

	if first == second {
		t.Fatal("the second sweep wrote the same bytes: the stamp never moved")
	}
	if got := strings.Replace(second, chartStamp(later, time.UTC), chartStamp(chartAt, time.UTC), 1); got != first {
		t.Error("the second sweep changed more than the stamp")
	}
}

// TestTrackerBlock_UnchangedBodyIsNotWritten: inside one minute over an
// unchanged graph the composed body is the body already there, and a sweep that
// changes nothing sends nothing — a tick must not bump a tracker for nothing
// (§forgejo/reconciliation/the-tracker-block).
func TestTrackerBlock_UnchangedBodyIsNotWritten(t *testing.T) {
	eng, api, log := chartForestEngine(t, chartFenced())
	eng.writeTrackerBlocks(chartForests(), chartAt)
	log.Reset()
	eng.writeTrackerBlocks(chartForests(), chartAt)

	if got := api.callCount("UpdateIssueBody:out/2"); got != 1 {
		t.Errorf("wrote the tracker %d times, want the second pass to send nothing", got)
	}
	// "Nothing written" says so, rather than reading as a forest the tick
	// never reached.
	if !strings.Contains(log.String(), "unchanged") || !strings.Contains(log.String(), "o/out#2") {
		t.Errorf("log = %s, want the converged tick to say it wrote nothing", log.String())
	}
}

// TestTrackerBlock_OnlyTheTrackerIsWritten: the grouping issue's body drives a
// landing decision through its digest, so a stamp written there would
// re-dispatch a merge; a member's body and the outcome prose are out of reach
// for the same reason (§forgejo/decisions/sync-set,
// §forgejo/decisions/actionable-state-digest).
func TestTrackerBlock_OnlyTheTrackerIsWritten(t *testing.T) {
	eng, api, _ := chartForestEngine(t, chartFenced())
	eng.writeTrackerBlocks(chartForests(), chartAt)

	if got := api.writeCount(); got != 1 {
		t.Fatalf("wrote %d issues, want one: the tracker", got)
	}
	for _, ref := range []struct {
		repo   string
		number int
	}{
		{"out", 1}, {"out", 6}, {"work", 3}, {"work", 5},
	} {
		if _, ok := api.writtenBody(ref.repo, ref.number); ok {
			t.Errorf("%s#%d was written; only a tracker's own description is the engine's", ref.repo, ref.number)
		}
	}
	if body := api.issues[6].Body; body != chartSyncBody {
		t.Errorf("the grouping issue's body = %q, want it byte-for-byte as found", body)
	}
}

// TestTrackerBlock_TrackerlessForestWritesNothing: a forest whose tracker is
// missing states its absence rather than being written to something else
// (§forgejo/observability/the-outcomes-view).
func TestTrackerBlock_TrackerlessForestWritesNothing(t *testing.T) {
	eng, api, log := chartForestEngine(t, chartFenced())
	forest := chartForests()[0]
	forest.Tracker = nil
	eng.writeTrackerBlocks([]Forest{forest}, chartAt)

	if got := api.writeCount(); got != 0 {
		t.Errorf("wrote %d issues, want none for a forest with no tracker", got)
	}
	if log.Len() != 0 {
		t.Errorf("log = %s, want a forest with no tracker reported as nothing", log.String())
	}
}

// TestTrackerBlock_RoleColourMissingWritesNothing: a root whose `ideation` label
// reports no colour cannot be drawn without inventing a hex, and the engine
// invents none (§forgejo/renderer/classes-and-strokes).
func TestTrackerBlock_RoleColourMissingWritesNothing(t *testing.T) {
	eng, api, log := chartForestEngine(t, chartFenced())
	root := chartRoot()
	root.LabelColours = nil
	api.issues[root.Number] = &root

	eng.writeTrackerBlocks(chartForests(), chartAt)
	if got := api.writeCount(); got != 0 {
		t.Errorf("wrote %d issues, want none: the strokes could not be read", got)
	}
	if !strings.Contains(log.String(), LabelIdeation) {
		t.Errorf("log = %s, want the refusal to name the label it could not read", log.String())
	}
}

// TestTrackerBlock_WriteFailureIsReported: a forge that refuses the write is
// named in the log rather than left as silence — the fault belongs to that
// forest alone (§forgejo/reconciliation/the-tracker-block).
func TestTrackerBlock_WriteFailureIsReported(t *testing.T) {
	eng, api, log := chartForestEngine(t, chartFenced())
	api.writeErr = &APIError{StatusCode: 403, Message: "must have Write Issues Unit"}

	eng.writeTrackerBlocks(chartForests(), chartAt)
	if !strings.Contains(log.String(), "403") || !strings.Contains(log.String(), "o/out#2") {
		t.Errorf("log = %s, want the refused write named with the tracker", log.String())
	}
	if got := api.callCount("UpdateIssueBody:out/2"); got != 1 {
		t.Errorf("made %d write attempts, want one", got)
	}
}

// TestTrackerBlock_AWriteOnlyFailureStillPublishesTheDrawing: the payload's
// picture is the drawing, not the splice. Where the drawing was made and only
// the write failed — the forge refuses the body, or the tracker read the splice
// needs comes back broken — the outcomes payload still carries this tick's
// chart under this tick's stamp while the tracker's region keeps the bytes of
// the tick before. A fence pair too ambiguous to splice into is the same case
// and is pinned by name in the payload's own table
// (§forgejo/reconciliation/the-tracker-block,
// §forgejo/observability/the-outcomes-view).
func TestTrackerBlock_AWriteOnlyFailureStillPublishesTheDrawing(t *testing.T) {
	cases := []struct {
		name string
		fail func(*fakeAPI)
	}{
		{
			name: "the forge refuses the body",
			fail: func(api *fakeAPI) {
				api.writeErr = &APIError{StatusCode: 403, Message: "must have Write Issues Unit"}
			},
		},
		{
			name: "the tracker read the splice needs fails",
			fail: func(api *fakeAPI) {
				api.getIssueErrOn = map[int]error{2: errors.New("issue not found")}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng, api, _ := chartForestEngine(t, chartFenced())
			tc.fail(api)
			eng.Reconcile()

			forests, err := eng.Forests()
			if err != nil || len(forests) != 1 {
				t.Fatalf("forests = %+v, %v", forests, err)
			}
			forest := forests[0]
			if forest.Chart == "" || forest.ChartRevisited == "" {
				t.Errorf("chart = %q under stamp %q, want the drawing this tick made even though the write failed",
					forest.Chart, forest.ChartRevisited)
			}
			if got := api.writeCount(); got != 0 {
				t.Errorf("wrote %d bodies, want none: the description keeps the bytes of the tick before", got)
			}
		})
	}
}

// TestTrackerBlock_TrackerOutsideRemitIsNotWritten: which repository holds a
// tracker comes from the root's blocking edges, not from configuration, so the
// engine's one mutation pays the admission test every other cross-repository hop
// pays before it reads or writes that repository — a tracker no maitred role
// admits is left alone, unread and unwritten
// (§forgejo/webhook/repository-roles,
// §forgejo/reconciliation/the-tracker-block). The forest is moved to the refused
// repository alone: the gather still sees the row in a repository a role names,
// so the only thing standing between this tick and a write outside the remit is
// the guard on the write itself, and the refusal's own wording is what the log
// must carry for that to be legible.
func TestTrackerBlock_TrackerOutsideRemitIsNotWritten(t *testing.T) {
	eng, api, log := chartForestEngine(t, chartFenced())
	forest := chartForests()[0]
	forest.Tracker.Repository = "o/archive"

	eng.writeTrackerBlocks([]Forest{forest}, chartAt)
	if got := api.writeCount(); got != 0 {
		t.Errorf("wrote %d issues, want none: no maitred role names o/archive", got)
	}
	if got := api.callCount("GetIssue:archive/2"); got != 0 {
		t.Errorf("read the tracker %d times, want it not read at all", got)
	}
	if !strings.Contains(log.String(), "tracker lives in a repository no maitred role names") {
		t.Errorf("log = %s, want the refusal to name the rule it refused on", log.String())
	}
	if !strings.Contains(log.String(), "o/archive") {
		t.Errorf("log = %s, want the refusal to name the repository it refused", log.String())
	}
}

// TestTrackerBlock_RowOutsideRemitDrawsNothing: a blocking edge into a repository
// no maitred role names must not put that row's title into a tracker body the
// outcomes view publishes, nor make the engine read that row's own edges
// (§forgejo/webhook/repository-roles, §forgejo/renderer/identifiers-and-shapes).
func TestTrackerBlock_RowOutsideRemitDrawsNothing(t *testing.T) {
	eng, api, _ := chartForestEngine(t, chartFenced())
	root := chartRoot()
	outside := Issue{
		Number: 99, Title: "another team's ticket", State: "open",
		Repository: "o/archive", UpdatedAt: testUpdatedAt,
		HTMLURL: "https://forge.example.com/o/archive/issues/99",
	}
	api.blocks[root.Number] = append(append([]Issue{}, api.blocks[root.Number]...), outside)

	if _, err := eng.writeTrackerBlock(chartForests()[0], chartAt); err != nil {
		t.Fatalf("writeTrackerBlock: %v", err)
	}
	region := regionOf(mustWritten(t, api, "out", 2))
	if strings.Contains(region, "i_archive_99") || strings.Contains(region, "another team's ticket") {
		t.Errorf("the source drew a row no maitred role admits:\n%s", region)
	}
	if got := api.callCount("IssueBlocks:archive/99"); got != 0 {
		t.Errorf("read the omitted row's edges %d times, want the row dropped before it is read", got)
	}
}

// TestTrackerBlock_UnknownScopeWritesNothing: an unknown scope fails closed —
// the remit test's failure is reported naming the failure, never read as a
// repository holding no role (§forgejo/webhook/the-scope-cache).
func TestTrackerBlock_UnknownScopeWritesNothing(t *testing.T) {
	eng, api, log := chartForestEngine(t, chartFenced())
	api.listReposErr = errors.New("the org listing failed")

	eng.writeTrackerBlocks(chartForests(), chartAt)
	if got := api.writeCount(); got != 0 {
		t.Errorf("wrote %d issues, want none while the scope is unknown", got)
	}
	if !strings.Contains(log.String(), "scope of o/work") {
		t.Errorf("log = %s, want the unknown scope named with its failure", log.String())
	}
}

// TestTrackerBlock_DrawsTheGraphItGathers: the roles, the closed stamp, the work
// order and the exclusion of a delivery all reach the source — gathering the
// graph is the work the renderer never does for itself
// (§forgejo/renderer/identifiers-and-shapes, §forgejo/renderer/edge-classes).
func TestTrackerBlock_DrawsTheGraphItGathers(t *testing.T) {
	eng, api, _ := chartForestEngine(t, chartFenced())
	if _, err := eng.writeTrackerBlock(chartForests()[0], chartAt); err != nil {
		t.Fatalf("writeTrackerBlock: %v", err)
	}
	region := regionOf(mustWritten(t, api, "out", 2))

	wants := []string{
		"flowchart TD",
		`i_out_1{{`,                       // the root is a hexagon
		`i_out_2(["`,                      // the tracker is a stadium
		"⚑",                               // the person's claim carries its prefix
		"✅",                               // a closed member carries the mark
		"closed 2026-10-05 12:00 UTC",     // and the forge's own close time
		"(#3)",                            // the grouping issue names its members
		"i_work_4 ==> i_work_3",           // the work order the edges state
		"i_work_3 ==> i_out_6",            // and the work order into the grouping
		`stroke:#5319e7,stroke-width:2px`, // the root's hex is its label's
		`stroke:#1d76db,stroke-width:2px`, // the tracker's hex is its label's
		`stroke:#6b7a8c`,                  // the member class is the engine's
	}
	for _, want := range wants {
		if !strings.Contains(region, want) {
			t.Errorf("the source carries no %q:\n%s", want, region)
		}
	}
	if strings.Contains(region, "-.-") {
		t.Errorf("the ideation root's membership edges were drawn:\n%s", region)
	}
	if strings.Contains(region, "i_work_7") {
		t.Error("a delivering pull request was drawn as a member")
	}
}

// TestTrackerBlock_StampIsRenderedInTheConfiguredZone: the stamp and a closed
// member's line both answer to the configured zone
// (§forgejo/config/tracker-chart, §forgejo/renderer/closed-members).
func TestTrackerBlock_StampIsRenderedInTheConfiguredZone(t *testing.T) {
	sydney, err := time.LoadLocation("Australia/Sydney")
	if err != nil {
		t.Skip("no tzdata to load Australia/Sydney with")
	}
	eng, api, _ := chartForestEngine(t, chartFenced())
	eng.cfg.TrackerTimezone = "Australia/Sydney"
	if _, err := eng.writeTrackerBlock(chartForests()[0], chartAt); err != nil {
		t.Fatalf("writeTrackerBlock: %v", err)
	}
	region := regionOf(mustWritten(t, api, "out", 2))
	for _, want := range []string{chartStamp(chartAt, sydney), "closed 2026-10-05 23:00 AEDT"} {
		if !strings.Contains(region, want) {
			t.Errorf("the source carries no %q in zone %s:\n%s", want, sydney, region)
		}
	}
}

// TestTrackerBlock_CeilingCollapsesPastTheCeiling: the configured ceiling is the
// renderer's, so a forest too wide for it collapses to one node per repository
// and says so (§forgejo/renderer/collapsing, §forgejo/config/tracker-chart).
func TestTrackerBlock_CeilingCollapsesPastTheCeiling(t *testing.T) {
	eng, api, _ := chartForestEngine(t, chartFenced())
	eng.cfg.MermaidMaxSourceCharacters = 600
	if _, err := eng.writeTrackerBlock(chartForests()[0], chartAt); err != nil {
		t.Fatalf("writeTrackerBlock: %v", err)
	}
	region := regionOf(mustWritten(t, api, "out", 2))
	if !strings.Contains(region, "%% collapsed to one node per repository") {
		t.Errorf("the source carries no collapse note:\n%s", region)
	}
	if strings.Contains(region, "i_work_3") {
		t.Error("the source was drawn per issue past the ceiling it was given")
	}
}

// TestTrackerBlock_ForestWithoutTrackerRowIsRefused covers the seat where the
// enumerated forest and the rows it reaches disagree: the view names a tracker
// the graph cannot find, so nothing is drawn rather than a chart of a forest
// missing its gate (§forgejo/reconciliation/the-tracker-block).
func TestTrackerBlock_ForestWithoutTrackerRowIsRefused(t *testing.T) {
	eng, api, log := chartForestEngine(t, chartFenced())
	root := chartRoot()
	api.blocks[root.Number] = []Issue{chartRow(3, "the purge, one command", nil, "open", time.Time{})}

	eng.writeTrackerBlocks(chartForests(), chartAt)
	if got := api.writeCount(); got != 0 {
		t.Errorf("wrote %d issues, want none", got)
	}
	if !strings.Contains(log.String(), "reaches no tracker row") {
		t.Errorf("log = %s, want the missing tracker named", log.String())
	}
}

// TestTrackerBlock_FailedReadWritesNothing: the region is composed from the body
// found, so a read that fails must not send anything. This is the gather's own
// read failing — the root — which is the first read the tick makes
// (§forgejo/reconciliation/the-tracker-block).
func TestTrackerBlock_FailedReadWritesNothing(t *testing.T) {
	eng, api, log := chartForestEngine(t, chartFenced())
	api.getIssueErr = errors.New("forge is unreachable")

	eng.writeTrackerBlocks(chartForests(), chartAt)
	if got := api.writeCount(); got != 0 {
		t.Errorf("wrote %d issues, want none after a read failed", got)
	}
	if !strings.Contains(log.String(), "root read o/out#1") {
		t.Errorf("log = %s, want the failed root read named with the row it concerned", log.String())
	}
}

// TestTrackerBlock_TrackerReadFailureNamesTheTracker: the root and its rows read
// fine and the tracker alone does not — deleted, archived, or one 500 — so the
// branch that splices against a body the tick never got is the one reported, not
// a failure from anywhere in the forest (§forgejo/reconciliation/the-tracker-block).
func TestTrackerBlock_TrackerReadFailureNamesTheTracker(t *testing.T) {
	eng, api, log := chartForestEngine(t, chartFenced())
	api.getIssueErrOn = map[int]error{2: errors.New("issue not found")}

	eng.writeTrackerBlocks(chartForests(), chartAt)
	if got := api.writeCount(); got != 0 {
		t.Errorf("wrote %d issues, want none after the tracker read failed", got)
	}
	if !strings.Contains(log.String(), "tracker read o/out#2") {
		t.Errorf("log = %s, want the tracker's own read named as the failure", log.String())
	}
}

// TestTrackerBlock_SweepWritesTheBlock: the write belongs to the tick, so an
// engine that sweeps lands the drawing without anyone asking, and a tick writes
// each forest once (§forgejo/reconciliation/the-sweep).
func TestTrackerBlock_SweepWritesTheBlock(t *testing.T) {
	eng, api, _ := chartForestEngine(t, chartFenced())
	eng.Reconcile()

	body, ok := api.writtenBody("out", 2)
	if !ok {
		t.Fatal("the sweep wrote no tracker block")
	}
	if !strings.Contains(regionOf(body), "```mermaid\nflowchart TD") {
		t.Errorf("the sweep wrote no chart:\n%s", regionOf(body))
	}
	if got := api.writeCount(); got != 1 {
		t.Errorf("the sweep wrote %d issues, want the one tracker", got)
	}
	// The drawing is the tick's last step: nothing the walk reads may come
	// after the write, or a slow forge would stand between the sweep and its
	// first decision (§forgejo/reconciliation/the-sweep).
	if last := api.calls[len(api.calls)-1]; !strings.HasPrefix(last, "UpdateIssueBody") {
		t.Errorf("the sweep's last call was %s, want the tracker write to come after the walk", last)
	}

	// The forest has not moved, and a sweep that changes nothing writes
	// nothing. Whether the second tick lands in the same wall-clock minute
	// as the first is not what this pins: either it converges without
	// writing, or it rewrites the stamp and nothing else — the property is
	// that a still forest gains no new bytes beyond the stamp, whatever
	// minute the clock happens to be in
	// (§forgejo/reconciliation/the-tracker-block).
	first := body
	eng.Reconcile()
	switch writes := api.callCount("UpdateIssueBody:out/2"); writes {
	case 1:
		// The stamp had not moved: nothing was sent.
	case 2:
		second, _ := api.writtenBody("out", 2)
		if got := strings.Replace(second, stampLine(second), stampLine(first), 1); got != first {
			t.Error("the second sweep changed more than the stamp")
		}
	default:
		t.Errorf("two sweeps wrote the tracker %d times, want one write or a second that only moved the stamp", writes)
	}
}

// stampLine is the recency line a body carries, as written.
func stampLine(body string) string {
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), stampLabel) {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

// TestTrackerBlock_RedrawDispatchesNoCurationRun: on a forest whose edges have
// not moved, a sweep that redrew the tracker's block — the engine's own
// handwriting, logged naming the tracker — dispatches no curation run: the
// drawing moves no fact the digest gate decides on, so a still forest stays
// under one first-observation decompose however often it is redrawn
// (§forgejo/reconciliation/the-tracker-block,
// §forgejo/decisions/actionable-state-digest).
func TestTrackerBlock_RedrawDispatchesNoCurationRun(t *testing.T) {
	eng, api, q, log := chartForestQueue(t, chartFenced())
	eng.Reconcile()
	if _, ok := api.writtenBody("out", 2); !ok {
		t.Fatal("the first sweep wrote no tracker block")
	}
	if got := countAction(t, q, "action=decompose"); got != 1 {
		t.Fatalf("the first sweep dispatched %d curation runs, want the one first-observation decompose", got)
	}

	eng.Reconcile()
	// What the second sweep may write — nothing, or the stamp and nothing
	// else — is pinned by TestTrackerBlock_SweepWritesTheBlock above; what
	// this test pins is that whichever it is, the tick must not wake a
	// curator (§forgejo/reconciliation/the-tracker-block).
	if got := countAction(t, q, "action=decompose"); got != 1 {
		t.Errorf("two sweeps over an unmoved graph dispatched %d curation runs, want the one the first observation earned", got)
	}
	// The engine says which description it moved, so a stamp that advanced
	// at an odd hour is attributable to the tick rather than to an agent
	// (§forgejo/observability/the-decision-log).
	if !strings.Contains(log.String(), "tracker current-state block written") {
		t.Errorf("log = %s, want the engine's write named in the log", log.String())
	}
}

// countAction is how many dispatches the queue holds whose prompt carries the
// given action field.
func countAction(t *testing.T, q *fakeQueue, action string) int {
	t.Helper()
	n := 0
	for _, task := range q.all() {
		if strings.Contains(task.Prompt, action) {
			n++
		}
	}
	return n
}

// TestTrackerBlock_SweepRecordsThePayloadPicture: one tick draws once and
// feeds both sinks — the bytes spliced between the tracker's fences are
// the bytes the outcomes payload carries, under the same stamp, so the
// page and the description can never state two pictures for one graph
// (§forgejo/reconciliation/the-tracker-block,
// §forgejo/observability/the-outcomes-view).
func TestTrackerBlock_SweepRecordsThePayloadPicture(t *testing.T) {
	eng, api, _ := chartForestEngine(t, chartFenced())
	eng.Reconcile()

	forests, err := eng.Forests()
	if err != nil || len(forests) != 1 {
		t.Fatalf("forests = %+v, %v", forests, err)
	}
	forest := forests[0]
	if forest.Chart == "" || forest.ChartRevisited == "" {
		t.Fatalf("the sweep recorded chart %q under stamp %q, want the tick's drawing and its moment", forest.Chart, forest.ChartRevisited)
	}
	body, ok := api.writtenBody("out", 2)
	if !ok {
		t.Fatal("the sweep wrote no tracker block")
	}
	region := regionOf(body)
	if !strings.Contains(region, forest.Chart) {
		t.Errorf("the written region does not carry the payload's chart:\nregion:\n%s\nchart:\n%s", region, forest.Chart)
	}
	if want := stampLabel + " " + forest.ChartRevisited; !strings.Contains(region, want) {
		t.Errorf("the region states no %q: one drawing, one stamp, two sinks", want)
	}
}

// TestTrackerBlock_WriteDoesNotRefireAStateGovernedRow: the tick's write moves
// the tracker's updated_at, and that timestamp is what both the re-fire bound
// and the queue's dedup key are keyed on — so a redraw would hand the engine a
// revision it had never seen, the bound would find a revision moved, the dedup
// key would differ, and a second wrapper would queue while the first was still
// running. A tracker's revision masks the fenced region out, exactly as the
// digest masks a tracker's body out, so one forest's state is one dedup key
// however many times it was redrawn (§forgejo/decisions/loop-prevention,
// §forgejo/reconciliation/the-tracker-block).
func TestTrackerBlock_WriteDoesNotRefireAStateGovernedRow(t *testing.T) {
	eng, api, q, _ := chartForestQueue(t, chartFenced())
	eng.Reconcile()
	first := wrapUpTask(t, q)

	// A member's title moves the drawing and nothing else: the chart is the
	// only part of the tracker's body between the fences. The forge bumps
	// the tracker's updated_at for the write, as it does for any edit.
	api.mu.Lock()
	for i := range api.blocks[1] {
		if api.blocks[1][i].Number == 3 {
			api.blocks[1][i].Title = "the purge, two commands"
		}
	}
	api.issues[2].UpdatedAt = testUpdatedAt.Add(time.Hour)
	api.mu.Unlock()

	eng.Reconcile()
	second := wrapUpTask(t, q)

	if got := api.callCount("UpdateIssueBody:out/2"); got != 2 {
		t.Fatalf("the tracker was written %d times across two sweeps, want the redraw to land", got)
	}
	if first.DedupKey == second.DedupKey {
		return
	}
	t.Errorf("the two sweeps dispatched %q and %q: a redraw moved the revision the queue squelches on", first.DedupKey, second.DedupKey)
}

// TestTrackerBlock_OwnEditIsNotReadAsActivity: the delivery the write provokes
// is the engine's own handwriting, so it holds off before the re-fetch — an
// edit by any other author is an event like any other and is read
// (§forgejo/decisions/loop-prevention).
func TestTrackerBlock_OwnEditIsNotReadAsActivity(t *testing.T) {
	tests := []struct {
		name       string
		sender     string
		wantRead   bool
		wantReason string
	}{
		{"the engine's own edit is not read", "maitred-engine", false, "edited by maitred-engine"},
		{"another author's edit is read", "alice", true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eng, api, q, log := chartForestQueue(t, chartFenced())
			api.mu.Lock()
			api.calls = nil
			api.mu.Unlock()

			eng.HandleEvent(Event{Type: EventIssueEdited, Repo: "o/out", Kind: KindIssue, Number: 2, Sender: tt.sender})

			if got := api.callCount("GetIssue:out/2") != 0; got != tt.wantRead {
				t.Errorf("the delivery re-fetched the tracker: %v, want a read: %v", got, tt.wantRead)
			}
			if n := len(q.all()); n != 0 {
				t.Errorf("the delivery dispatched %d tasks, want none", n)
			}
			if tt.wantReason != "" && !strings.Contains(log.String(), tt.wantReason) {
				t.Errorf("log = %s, want the hold-off to name the engine's own edit", log.String())
			}
		})
	}
}

// wrapUpTask is the queue's most recent wrap-up dispatch.
func wrapUpTask(t *testing.T, q *fakeQueue) *queue.Task {
	t.Helper()
	var last *queue.Task
	for _, task := range q.all() {
		if strings.Contains(task.Prompt, "action=wrap-up") {
			last = task
		}
	}
	if last == nil {
		t.Fatal("the queue holds no wrap-up dispatch")
	}
	return last
}

// TestTrackerBlock_TrackerIsReadAtTheMomentItIsWritten: the description is
// spliced against the body as it stands when the block is composed, not
// against one read before a forest's worth of graph reads — otherwise a human
// editing the prose outside the pair during the gather would have that edit
// overwritten by the write that follows it
// (§forgejo/reconciliation/the-tracker-block).
func TestTrackerBlock_TrackerIsReadAtTheMomentItIsWritten(t *testing.T) {
	eng, api, _ := chartForestEngine(t, chartFenced())
	if _, err := eng.writeTrackerBlock(chartForests()[0], chartAt); err != nil {
		t.Fatalf("writeTrackerBlock: %v", err)
	}

	trackerRead, lastEdgeRead, write := -1, -1, -1
	for i, call := range api.calls {
		switch {
		case strings.HasPrefix(call, "GetIssue:out/2"):
			trackerRead = i
		case strings.HasPrefix(call, "IssueBlocks:"):
			lastEdgeRead = i
		case strings.HasPrefix(call, "UpdateIssueBody:"):
			write = i
		}
	}
	switch {
	case trackerRead < 0 || write < 0:
		t.Fatalf("the tick made no tracker read or no write: %v", api.calls)
	case trackerRead < lastEdgeRead:
		t.Errorf("the tracker was read before the graph it is drawn from (%v)", api.calls)
	case write != trackerRead+1:
		t.Errorf("the write at %d did not follow the read at %d directly: %v", write, trackerRead, api.calls)
	}
}

// TestTrackerBlock_BlocksReadFailureWritesNothing: an edge read that fails is the
// forest's own fault, reported and skipped — a chart of half a graph is a chart
// that guessed (§forgejo/reconciliation/the-tracker-block).
func TestTrackerBlock_BlocksReadFailureWritesNothing(t *testing.T) {
	eng, api, log := chartForestEngine(t, chartFenced())
	api.issueBlocksErr = errors.New("forge refused the blocks read")

	eng.writeTrackerBlocks(chartForests(), chartAt)
	if got := api.writeCount(); got != 0 {
		t.Errorf("wrote %d issues, want none after the edge read failed", got)
	}
	if !strings.Contains(log.String(), "issues o/out#1 blocks") {
		t.Errorf("log = %s, want the failed read named with the row it concerned", log.String())
	}
}

// TestTrackerBlock_RowNamingNoRepositoryStillDraws: a listing that reports no
// repository for a row keeps the address the read was made from, so the node is
// drawn rather than the graph refusing a row that names no issue
// (§forgejo/client/operations, §forgejo/renderer/identifiers-and-shapes).
func TestTrackerBlock_RowNamingNoRepositoryStillDraws(t *testing.T) {
	eng, api, _ := chartForestEngine(t, chartFenced())
	root := chartRoot()
	rows := api.blocks[root.Number]
	for i := range rows {
		rows[i].Repository = ""
	}

	if _, err := eng.writeTrackerBlock(chartForests()[0], chartAt); err != nil {
		t.Fatalf("writeTrackerBlock: %v", err)
	}
	region := regionOf(mustWritten(t, api, "out", 2))
	// Every row resolves to the repository the read was made from, which is
	// where a listing that names no repository puts them all.
	for _, want := range []string{`i_out_2(["`, `i_out_3["`} {
		if !strings.Contains(region, want) {
			t.Errorf("the source carries no %q:\n%s", want, region)
		}
	}
}

// TestTrackerBlock_UnloadableZoneFallsBackToUTC is the defensive half: load
// refuses an unloadable zone (§forgejo/config/validation), so the sweep should
// never meet one — and if a config built by hand carries one anyway, the stamp
// says UTC rather than a zone nobody configured.
func TestTrackerBlock_UnloadableZoneFallsBackToUTC(t *testing.T) {
	cfg := testConfig()
	cfg.TrackerTimezone = "Mars/Olympus"
	if got := cfg.ChartLocation(); got != time.UTC {
		t.Errorf("ChartLocation = %s, want UTC", got)
	}
}

// TestTrackerRevision_IgnoresTheFencedRegion: the region the engine writes is
// not a fact the engine reacts to — a redraw leaves a tracker's revision where
// it was, prose outside the pair moves it, and an issue the engine never
// writes still answers with its updated_at (§forgejo/state/watermark,
// §forgejo/decisions/loop-prevention).
func TestTrackerRevision_IgnoresTheFencedRegion(t *testing.T) {
	before := chartTracker("prose\n\n" + fenceBegin + "\nthe old chart\n\n" + fenceEnd + "\n")
	after := chartTracker("prose\n\n" + fenceBegin + "\nthe new chart\n\n" + fenceEnd + "\n")
	after.UpdatedAt = before.UpdatedAt.Add(time.Hour)
	if issueRevision(&before) != issueRevision(&after) {
		t.Error("a redraw moved the tracker's revision")
	}

	drifted := chartTracker("prose a human rewrote\n\n" + fenceBegin + "\nthe new chart\n\n" + fenceEnd + "\n")
	if issueRevision(&before) == issueRevision(&drifted) {
		t.Error("prose outside the pair left the tracker's revision where it was")
	}

	root := chartRoot()
	if got, want := issueRevision(&root), root.UpdatedAt.UTC().Format(time.RFC3339); got != want {
		t.Errorf("an issue the engine never writes reports revision %q, want its updated_at %q", got, want)
	}
}

// TestTrackerBlock_BoundReFireNamesTheFolds: the line a bounded wrap-up re-fire
// leaves is the one an operator reads when a tracker seems not to wake an agent,
// and a tracker's revision is a digest — so that third quote names the facts
// behind it, as the two rows that dispatch do (§forgejo/state/watermark,
// §forgejo/observability/the-decision-log).
func TestTrackerBlock_BoundReFireNamesTheFolds(t *testing.T) {
	eng, _, log := chartForestEngine(t, chartFenced())
	eng.Reconcile()
	eng.Reconcile()

	reason := lastReasonFor(t, log, "o/out/issue/2")
	if !strings.Contains(reason, "re-fire is bounded") {
		t.Fatalf("reason %q, want the second sweep's bounded wrap-up re-fire", reason)
	}
	for _, want := range []string{"state open", "label " + LabelOutcome, "body "} {
		if !strings.Contains(reason, want) {
			t.Errorf("reason %q names no %q: the revision was quoted as a bare digest", reason, want)
		}
	}
}

// TestSpliceStateBlock covers the region rules directly, including the two
// ragged shapes a hand-authored tracker can have
// (§forgejo/reconciliation/the-tracker-block).
func TestSpliceStateBlock(t *testing.T) {
	block := "STAMP\n"
	tests := []struct {
		name    string
		body    string
		want    string
		wantErr bool
	}{
		{
			name: "no pair inserts the heading",
			body: "prose\n",
			want: stateHeading + "\n\n" + fenceBegin + "\n\n" + block + "\n" + fenceEnd + "\n\nprose\n",
		},
		{
			name: "no pair and no prose at all",
			body: "",
			want: stateHeading + "\n\n" + fenceBegin + "\n\n" + block + "\n" + fenceEnd + "\n",
		},
		{
			name: "one pair replaces only its region",
			body: "before" + fenceBegin + "\n\nold\n\n" + fenceEnd + "after",
			want: "before" + fenceBegin + "\n\n" + block + "\n" + fenceEnd + "after",
		},
		{
			name:    "two pairs are ambiguous",
			body:    fenceBegin + "\n\none\n\n" + fenceEnd + "\n" + fenceBegin + "\n\ntwo\n\n" + fenceEnd,
			wantErr: true,
		},
		{
			name:    "a closer with no opener is ambiguous",
			body:    "stray" + fenceEnd + "\n",
			wantErr: true,
		},
		{
			name:    "an opener with no closer is ambiguous",
			body:    "stray" + fenceBegin + "\n",
			wantErr: true,
		},
		{
			name:    "a closer standing before its opener is ambiguous",
			body:    fenceEnd + "\n" + fenceBegin + "\n" + fenceEnd + "\n",
			wantErr: true,
		},
		{
			// One of each, in the wrong order: the count guard lets this
			// through, so the order guard is the only thing standing between
			// the splice and a region that runs backwards.
			name:    "one closer then one opener is ambiguous",
			body:    "before" + fenceEnd + "between" + fenceBegin + "after",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := spliceStateBlock(tt.body, block)
			if tt.wantErr {
				if !errors.Is(err, errFenceAmbiguous) {
					t.Fatalf("spliceStateBlock = %v, want the ambiguity reported", err)
				}
				if got != tt.body {
					t.Errorf("an ambiguous body came back changed: %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("spliceStateBlock: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// regionOf is the bytes between one tracker's fence pair, stripped of the blank
// lines that set them off from the fences.
func regionOf(body string) string {
	start := strings.Index(body, fenceBegin)
	end := strings.Index(body, fenceEnd)
	if start < 0 || end < start {
		return ""
	}
	return strings.Trim(body[start+len(fenceBegin):end], "\n")
}

func mustWritten(t *testing.T, api *fakeAPI, repo string, number int) string {
	t.Helper()
	body, ok := api.writtenBody(repo, number)
	if !ok {
		t.Fatalf("no write landed on %s/%d", repo, number)
	}
	return body
}

func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

func lastLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// The wrap-up path (s-git/maitred#232): a forest whose tracker is finished
// leaves behind the picture of its completion — the event's redraw draws it,
// the same render function, the same graph, landed as part of handling the
// event (s-git/maitred#274). The tests below drive the same fixtures the tick's
// own write uses, and a test pins that the two paths share one renderer.

// wrapUpWire closes the members chartWire left open and hangs the tracker's
// gate on its dependencies, as the graph stands the moment a wrap-up fires:
// every member closed, every close stamped
// (§forgejo/decisions/transition-table).
func wrapUpWire(api *fakeAPI) {
	for _, number := range []int{3, 5} {
		if row := api.issues[number]; row != nil {
			row.State = "closed"
			row.ClosedAt = chartClosedAt
		}
		for i := range api.blocks[1] {
			if api.blocks[1][i].Number == number {
				api.blocks[1][i].State = "closed"
				api.blocks[1][i].ClosedAt = chartClosedAt
			}
		}
	}
	gate := *api.issues[1] // the root stands closed: an open root would hold the tracker off
	gate.State = "closed"
	api.deps[2] = []Issue{gate}
	// The member's blockers name both edges reaching it: the root's membership
	// edge, which is what makes it a member of this forest, and the tracker
	// whose work it delivers (§forgejo/decisions/commissioning). The close that
	// wakes the cascade is read through these rows by the redraw
	// (§forgejo/webhook/the-pipeline, s-git/maitred#274).
	api.deps[3] = []Issue{gate, *api.issues[2]}
}

// wrapUpQueuedAt is where a wrap-up dispatch stands in the fake's ordered log,
// or -1 when none queued: the queue records into that log, so whether a write
// came before a dispatch is one index in it (s-git/maitred#232).
func wrapUpQueuedAt(api *fakeAPI) int {
	return callIndex(api, "AddTask:Wrap-up")
}

// callIndex is where the first API call carrying prefix stands in the fake's
// ordered log, or -1 when no call did.
func callIndex(api *fakeAPI, prefix string) int {
	api.mu.Lock()
	defer api.mu.Unlock()
	for i, call := range api.calls {
		if strings.HasPrefix(call, prefix) {
			return i
		}
	}
	return -1
}

// wantEveryMemberClosed asserts the region draws every member struck through
// with its close stamp (§forgejo/renderer/closed-members).
func wantEveryMemberClosed(t *testing.T, region string) {
	t.Helper()
	for _, ref := range []string{"i_work_3", "i_work_4", "i_work_5"} {
		if !strings.Contains(region, ref) {
			t.Errorf("the drawing holds no %s:\n%s", ref, region)
		}
	}
	if got := strings.Count(region, "✅"); got != 3 {
		t.Errorf("the drawing marks %d nodes closed, want the three members:\n%s", got, region)
	}
	if got := strings.Count(region, "text-decoration:line-through"); got != 3 {
		t.Errorf("the drawing strikes through %d labels, want the three members", got)
	}
	if got := strings.Count(region, "closed "+chartClosedAt.Format("2006-01-02 15:04 MST")); got != 3 {
		t.Errorf("the drawing stamps %d close times, want the three members", got)
	}
}

// TestWrapUpDrawsTheFinishedForestOnTheSweep: the sweep's wrap-up row queues
// its task behind exactly one current-state write, and the drawing it follows
// shows every member closed (§forgejo/reconciliation/the-tracker-block,
// §forgejo/reconciliation/watermark-exemptions, s-git/maitred#232).
func TestWrapUpDrawsTheFinishedForestOnTheSweep(t *testing.T) {
	eng, api, q, _ := chartForestQueue(t, chartFenced())
	wrapUpWire(api)
	eng.Reconcile()

	wrapUpTask(t, q)
	if got := api.writeCount(); got != 1 {
		t.Fatalf("the sweep wrote %d issues, want the one write its own pass makes", got)
	}
	// The drawing is the tick's last step, not the dispatch's errand: the
	// write follows the task it describes rather than preceding it
	// (§forgejo/reconciliation/the-sweep, s-git/maitred#274).
	wrote, queued := callIndex(api, "UpdateIssueBody:out/2"), wrapUpQueuedAt(api)
	if queued < 0 {
		t.Fatal("the queue recorded no wrap-up dispatch")
	}
	if wrote < queued {
		t.Errorf("the write at %d came before the wrap-up dispatch queued at %d: the tick draws after the walk it decided on", wrote, queued)
	}
	wantEveryMemberClosed(t, regionOf(mustWritten(t, api, "out", 2)))
}

// TestWrapUpDrawsTheFinishedForestOnTheCascade: the unblock cascade fires the
// moment the last member closes, ahead of any sweep — so this is the path a
// stale chart survived on, and the one the redraw takes over: the close that
// woke the wrap-up draws the finished forest itself, in the block and in the
// payload (§forgejo/decisions/unblock-cascade, s-git/maitred#232, s-git/maitred#274).
func TestWrapUpDrawsTheFinishedForestOnTheCascade(t *testing.T) {
	eng, api, q, _ := chartForestQueue(t, chartFenced())
	wrapUpWire(api)
	// The member's close is the cascade's last: it blocks the tracker, so its
	// closure newly unblocks it.
	api.blocks[3] = []Issue{*api.issues[2]}
	eng.HandleEvent(Event{Type: EventIssueClosed, Repo: "o/work", Kind: KindIssue, Number: 3, Sender: "alice"})

	wrapUpTask(t, q)
	if got := api.writeCount(); got != 1 {
		t.Fatalf("the cascade wrote %d issues, want the one write the event's redraw makes", got)
	}
	// The drawing is the pipeline's tail: the dispatch it follows is the one
	// the same event woke (§forgejo/webhook/the-pipeline, s-git/maitred#274).
	wrote, queued := callIndex(api, "UpdateIssueBody:out/2"), wrapUpQueuedAt(api)
	if queued < 0 {
		t.Fatal("the queue recorded no wrap-up dispatch")
	}
	if wrote < queued {
		t.Errorf("the write at %d came before the wrap-up dispatch queued at %d: the redraw is the event's last step", wrote, queued)
	}
	wantEveryMemberClosed(t, regionOf(mustWritten(t, api, "out", 2)))
}

// TestWrapUpDispatchSharesTheSweepRenderer: the picture between the fences is
// the sweep's own render function, byte for byte — the wrap-up path composes
// no drawing of its own, and the tick's write finds the body it landed
// current (§forgejo/renderer/the-contract, s-git/maitred#232).
func TestWrapUpDispatchSharesTheSweepRenderer(t *testing.T) {
	eng, api, q, _ := chartForestQueue(t, chartFenced())
	wrapUpWire(api)
	api.blocks[3] = []Issue{*api.issues[2]}
	eng.HandleEvent(Event{Type: EventIssueClosed, Repo: "o/work", Kind: KindIssue, Number: 3, Sender: "alice"})
	wrapUpTask(t, q)

	source, err := eng.renderForestChart(chartForests()[0])
	if err != nil {
		t.Fatalf("renderForestChart: %v", err)
	}
	if region := regionOf(mustWritten(t, api, "out", 2)); !strings.Contains(region, strings.TrimRight(source, "\n")) {
		t.Error("the picture the dispatch drew is not the sweep's render function's own bytes")
	}
	drawn := mustWritten(t, api, "out", 2)
	// The tick's own write, over the same graph, is the same drawing: one
	// render function, whichever path calls it. Whether the two stamps fall
	// inside one minute of the wall clock is not what this pins — the
	// property is that a drawn forest gains no bytes beyond the stamp.
	if _, err := eng.writeTrackerBlock(chartForests()[0], time.Now()); err != nil {
		t.Fatalf("writeTrackerBlock: %v", err)
	}
	switch writes := api.callCount("UpdateIssueBody:out/2"); writes {
	case 1:
		// The stamp had not moved: the tick found the body current.
	case 2:
		second := mustWritten(t, api, "out", 2)
		if got := strings.Replace(second, stampLine(second), stampLine(drawn), 1); got != drawn {
			t.Error("the tick's write over a body the dispatch drew changed more than the stamp")
		}
	default:
		t.Errorf("the forest was written %d times, want one write or a second that only moved the stamp", writes)
	}
}

// TestWrapUpDrawFailureStillDispatches: the chart is UX; a forge that refuses
// the write is logged naming the tracker, and the wrap-up dispatches anyway
// (s-git/maitred#232).
func TestWrapUpDrawFailureStillDispatches(t *testing.T) {
	eng, api, q, log := chartForestQueue(t, chartFenced())
	wrapUpWire(api)
	api.blocks[3] = []Issue{*api.issues[2]}
	api.writeErr = &APIError{StatusCode: 403, Message: "must have Write Issues Unit"}
	eng.HandleEvent(Event{Type: EventIssueClosed, Repo: "o/work", Kind: KindIssue, Number: 3, Sender: "alice"})

	wrapUpTask(t, q)
	if !strings.Contains(log.String(), "redraw: tracker current-state block not written") ||
		!strings.Contains(log.String(), "o/out#2") || !strings.Contains(log.String(), "403") {
		t.Errorf("log = %s, want the refused write named with the tracker it failed on", log.String())
	}
}

// TestWrapUpAmbiguousFencesLeftUntouched: a description carrying two fence
// pairs belongs to no guessable region, so the dispatch leaves it exactly as
// found and the refusal surfaces through the log — as it does on the sweep's
// own write — while the wrap-up still queues (§forgejo/reconciliation/the-tracker-block,
// s-git/maitred#232).
func TestWrapUpAmbiguousFencesLeftUntouched(t *testing.T) {
	body := chartFenced() + "\n" + fenceBegin + "\n\nanother forest\n\n" + fenceEnd + "\n"
	eng, api, q, log := chartForestQueue(t, body)
	wrapUpWire(api)
	api.blocks[3] = []Issue{*api.issues[2]}
	eng.HandleEvent(Event{Type: EventIssueClosed, Repo: "o/work", Kind: KindIssue, Number: 3, Sender: "alice"})

	wrapUpTask(t, q)
	if got := api.writeCount(); got != 0 {
		t.Errorf("wrote %d issues, want none: the ambiguous tracker stays as found", got)
	}
	if !strings.Contains(log.String(), "ambiguous") || !strings.Contains(log.String(), "o/out#2") {
		t.Errorf("log = %s, want the refusal to name the ambiguous tracker", log.String())
	}
}

// TestWrapUpDrawWritesTheTrackerAlone: the draw addresses the forest's
// tracker and nothing else — the grouping issue's body drives a landing
// decision through its digest and stays byte-for-byte as found
// (§forgejo/decisions/sync-set, s-git/maitred#232).
func TestWrapUpDrawWritesTheTrackerAlone(t *testing.T) {
	eng, api, q, _ := chartForestQueue(t, chartFenced())
	wrapUpWire(api)
	api.blocks[3] = []Issue{*api.issues[2]}
	eng.HandleEvent(Event{Type: EventIssueClosed, Repo: "o/work", Kind: KindIssue, Number: 3, Sender: "alice"})
	wrapUpTask(t, q)

	if got := api.writeCount(); got != 1 {
		t.Fatalf("wrote %d issues, want one: the tracker", got)
	}
	for _, ref := range []struct {
		repo   string
		number int
	}{
		{"out", 1}, {"out", 6}, {"work", 3}, {"work", 5},
	} {
		if _, ok := api.writtenBody(ref.repo, ref.number); ok {
			t.Errorf("%s#%d was written; the wrap-up draw addresses the tracker alone", ref.repo, ref.number)
		}
	}
	if body := api.issues[6].Body; body != chartSyncBody {
		t.Errorf("the grouping issue's body = %q, want it byte-for-byte as found", body)
	}
}

// TestWrapUpDrawMovesOnlyTheStampOnACurrentBlock: a tracker whose block the
// tick already drew current gains, from the dispatch's draw, a moved
// `Last revisited by maître d' engine:` stamp and nothing else
// (§forgejo/reconciliation/the-tracker-block, s-git/maitred#232).
func TestWrapUpDrawMovesOnlyTheStampOnACurrentBlock(t *testing.T) {
	eng, api, q, _ := chartForestQueue(t, chartFenced())
	wrapUpWire(api)
	api.blocks[3] = []Issue{*api.issues[2]}
	// The block is current but for an hour-old stamp, which is what the
	// dispatch's own reading of the clock will move: the draw cannot be
	// accused of redrawing anything but the stamp.
	if _, err := eng.writeTrackerBlock(chartForests()[0], time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("writeTrackerBlock: %v", err)
	}
	first := mustWritten(t, api, "out", 2)

	eng.HandleEvent(Event{Type: EventIssueClosed, Repo: "o/work", Kind: KindIssue, Number: 3, Sender: "alice"})
	wrapUpTask(t, q)

	second := mustWritten(t, api, "out", 2)
	if first == second {
		t.Fatal("the dispatch's draw changed nothing at all: the stamp never moved")
	}
	if got := strings.Replace(second, stampLine(second), stampLine(first), 1); got != first {
		t.Error("the dispatch's draw moved more than the stamp")
	}
}

// The redraw: an admissible event that lands on an object of a forest draws that
// forest as part of handling the event, in both sinks, instead of leaving the
// picture to the next tick's step 5 (s-git/maitred#274,
// §forgejo/reconciliation/the-tracker-block, §forgejo/webhook/the-pipeline).
// Membership is the relation every other reader of the graph applies, so the
// tests below hang membership edges on the fixtures rather than restating a rule.

// redrawWire hangs the membership edges an event on the member reads: its
// blockers name the root that makes it a member and the tracker that gates its
// work (§forgejo/decisions/commissioning).
func redrawWire(api *fakeAPI) {
	api.deps[3] = []Issue{*api.issues[1], *api.issues[2]}
}

// redrawSeed records the forest set the outcomes view serves, so a test can see
// the payload's picture pair move from the empty the head records to the drawing
// an event made (§forgejo/observability/the-outcomes-view).
func redrawSeed(t *testing.T, eng *Engine) {
	t.Helper()
	if _, err := eng.recordForests(); err != nil {
		t.Fatalf("recordForests: %v", err)
	}
}

// fencedChart is the Mermaid source a block's region carries — the bytes the
// payload's chart field is supposed to hold beside it
// (§forgejo/observability/the-outcomes-view).
func fencedChart(t *testing.T, region string) string {
	t.Helper()
	const open = "```mermaid\n"
	start := strings.Index(region, open)
	if start < 0 {
		t.Fatalf("the region holds no fenced Mermaid block:\n%s", region)
	}
	rest := region[start+len(open):]
	end := strings.Index(rest, "```")
	if end < 0 {
		t.Fatalf("the region's Mermaid fence never closes:\n%s", region)
	}
	return rest[:end]
}

// TestRedrawOnAnEventLandsBothSinks: an event on a member draws its forest
// within the event, and the one drawing lands in the tracker's block and the
// outcomes payload together — the same bytes, the same stamp, neither sink a
// tick behind the other (s-git/maitred#274, §forgejo/reconciliation/the-tracker-block).
func TestRedrawOnAnEventLandsBothSinks(t *testing.T) {
	eng, api, _, _ := chartForestQueue(t, chartFenced())
	redrawWire(api)
	redrawSeed(t, eng)
	if got := mustForests(t, eng)[0]; got.Chart != "" || got.ChartRevisited != "" {
		t.Fatal("the recorded set already carries a picture, so this test would prove nothing")
	}

	eng.HandleEvent(Event{Type: EventIssueEdited, Repo: "o/work", Kind: KindIssue, Number: 3, Sender: "alice"})

	body := mustWritten(t, api, "out", 2)
	region := regionOf(body)
	if !strings.Contains(region, "i_work_3") {
		t.Errorf("the block the event landed draws no member:\n%s", region)
	}
	after := mustForests(t, eng)[0]
	if after.Chart != fencedChart(t, region) {
		t.Errorf("the payload's chart is not the bytes the block carries:\npayload %q\nblock %q", after.Chart, fencedChart(t, region))
	}
	if stamp := strings.TrimPrefix(stampLine(body), stampLabel+" "); after.ChartRevisited != stamp {
		t.Errorf("the payload's stamp %q is not the block's %q: the two sinks state two drawings",
			after.ChartRevisited, stampLine(body))
	}
}

// TestRedrawOnTheRootsOwnEvent: the root is the forest, so an event on it needs
// no membership edge to read and draws the forest all the same
// (§forgejo/decisions/commissioning, s-git/maitred#274).
func TestRedrawOnTheRootsOwnEvent(t *testing.T) {
	eng, api, _, _ := chartForestQueue(t, chartFenced())
	redrawSeed(t, eng)

	eng.HandleEvent(Event{Type: EventIssueCommented, Repo: "o/out", Kind: KindIssue, Number: 1, Sender: "alice"})

	// The re-fetch reads the root's blockers for the decision it makes; the
	// redraw asks for no read of its own, the root being the forest
	// (§forgejo/webhook/re-fetch).
	if got := api.callCount("IssueDependencies:out/1"); got != 1 {
		t.Errorf("the root's blockers were read %d times, want the one read the re-fetch made", got)
	}
	body := mustWritten(t, api, "out", 2)
	if after := mustForests(t, eng)[0]; after.Chart != fencedChart(t, regionOf(body)) {
		t.Error("an event on the root left the payload's picture behind the block")
	}
}

// TestRedrawDrawsEveryForestTheObjectBelongsTo: membership is a relation, and
// two roots' membership edges can reach one row. Both forests are drawn, and
// each draws its own tracker — the second forest is a whole forest in the second
// repository, not a variant of the first (§forgejo/decisions/commissioning).
func TestRedrawDrawsEveryForestTheObjectBelongsTo(t *testing.T) {
	eng, api, _, _ := chartForestQueue(t, chartFenced())
	root := chartRoot()
	root.Number, root.Title = 8, "the second forest"
	tracker := chartTracker(chartFenced())
	tracker.Number, tracker.Title, tracker.Repository = 9, "outcome tracker of the second forest", "o/work"
	member := *api.issues[3]
	api.issues[8], api.issues[9] = &root, &tracker
	api.blocks[8] = []Issue{tracker, member}
	api.deps[3] = []Issue{root, *api.issues[1], tracker, *api.issues[2]}
	api.openIssues["work"] = append(api.openIssues["work"], tracker)
	redrawSeed(t, eng)

	eng.HandleEvent(Event{Type: EventIssueEdited, Repo: "o/work", Kind: KindIssue, Number: 3, Sender: "alice"})

	for _, ref := range []struct {
		repo   string
		number int
	}{{"out", 2}, {"work", 9}} {
		if _, ok := api.writtenBody(ref.repo, ref.number); !ok {
			t.Errorf("%s#%d was never drawn: the redraw drew one forest where the object belongs to two", ref.repo, ref.number)
		}
	}
	// Each forest's pair is its own: the one forest's drawing lands on its own
	// row and leaves its sibling's picture as the tick drew it
	// (§forgejo/observability/the-outcomes-view).
	for _, forest := range mustForests(t, eng) {
		if forest.Chart == "" {
			t.Errorf("forest %s#%d carries no picture: one forest's redraw moved only its own pair", forest.Repository, forest.Root.Number)
		}
	}
}

// TestRedrawClosesTheForestItWasDispatchedFrom: a member's close wakes the
// cascade's wrap-up and redraws the finished forest in the same event, so the
// picture the forest leaves behind is the completion the task describes and the
// outcomes view answers it without waiting for a tick (§forgejo/decisions/unblock-cascade,
// s-git/maitred#232, s-git/maitred#274).
func TestRedrawClosesTheForestItWasDispatchedFrom(t *testing.T) {
	eng, api, q, _ := chartForestQueue(t, chartFenced())
	wrapUpWire(api)
	api.blocks[3] = []Issue{*api.issues[2]}
	redrawSeed(t, eng)

	eng.HandleEvent(Event{Type: EventIssueClosed, Repo: "o/work", Kind: KindIssue, Number: 3, Sender: "alice"})

	wrapUpTask(t, q)
	body := mustWritten(t, api, "out", 2)
	wantEveryMemberClosed(t, regionOf(body))
	if after := mustForests(t, eng)[0]; after.Chart != fencedChart(t, regionOf(body)) {
		t.Error("the finished forest's picture never left the last tick")
	}
}

// TestRedrawDrawsTheForestTheCascadeReaches: the cascade walks the graph
// outward, past the forests the event's object belongs to — a closed member
// blocking a second forest's tracker directly newly unblocks that tracker,
// and its forest has no membership edge to the closing row. The wrap-up the
// cascade dispatches is the sign that forest's picture moved, so the drawing
// step reaches it too, in the same event and after the dispatch it follows
// (§forgejo/decisions/unblock-cascade, §forgejo/webhook/the-pipeline,
// s-git/maitred#232, s-git/maitred#274).
func TestRedrawDrawsTheForestTheCascadeReaches(t *testing.T) {
	eng, api, q, log := chartForestQueue(t, chartFenced())
	wrapUpWire(api)
	secondForestWire(api)
	// The closing member blocks its own tracker and the second forest's
	// tracker directly — the edge whose closure newly unblocks that tracker.
	api.blocks[3] = []Issue{*api.issues[2], *api.issues[9]}
	redrawSeed(t, eng)

	eng.HandleEvent(Event{Type: EventIssueClosed, Repo: "o/work", Kind: KindIssue, Number: 3, Sender: "alice"})

	var forSecond bool
	for _, task := range q.all() {
		if strings.Contains(task.Prompt, "action=wrap-up repo=o/out kind=issue number=9") {
			forSecond = true
		}
	}
	if !forSecond {
		t.Fatalf("the cascade queued no wrap-up for the second forest's tracker: %v", q.all())
	}
	// The second forest's block moves although the closing row belongs to no
	// forest of its own; the drawing follows the dispatch it describes, the
	// pipeline's tail step for both forests the event reached.
	body := mustWritten(t, api, "out", 9)
	if region := regionOf(body); !strings.Contains(region, "i_out_9") || !strings.Contains(region, "i_out_8") {
		t.Errorf("the second forest's block draws neither its tracker nor its root:\n%s", region)
	}
	if wrote, queued := callIndex(api, "UpdateIssueBody:out/9"), wrapUpQueuedAt(api); queued >= 0 && wrote < queued {
		t.Errorf("the write at %d came before a wrap-up dispatch queued at %d: the redraw is the event's last step", wrote, queued)
	}
	// The forest the event's own membership named is drawn once, not once per
	// road that named it.
	if got := api.writeCount(); got != 2 {
		t.Errorf("wrote %d issues, want the two forests' trackers: one drawing per forest the event reached", got)
	}
	// The payload refuses to invent the row the head record never held: the
	// closed root's forest arrives with the next tick's enumeration, its block
	// none the worse for landing now (§forgejo/observability/the-outcomes-view).
	if !strings.Contains(log.String(), "holds no row to record this drawing onto") || !strings.Contains(log.String(), "o/out#8") {
		t.Errorf("log = %s, want the refuse-to-invent line naming the forest the enumeration never held", log.String())
	}
}

// TestRedrawDrawsTheForestAnOutOfRemitCloseWakes: a close in a repository
// holding no maitred role is inert for its own issue, but it wakes the
// cascade through the graph, and the cascade's wrap-up dispatch draws — the
// drawing step belongs to that path too, judged by the roles of the
// repositories the forest actually stands in
// (§forgejo/webhook/repository-roles, §forgejo/decisions/unblock-cascade,
// s-git/maitred#232).
func TestRedrawDrawsTheForestAnOutOfRemitCloseWakes(t *testing.T) {
	eng, api, q, _ := chartForestQueue(t, chartFenced())
	root, tracker := secondForestWire(api)
	// The closing issue stands in a repository no maitred role names, and it
	// blocks the second forest's tracker directly.
	inert := Issue{Number: 11, Title: "a stranger's ticket", State: "closed", Repository: "o/off", ClosedAt: chartClosedAt, UpdatedAt: testUpdatedAt}
	api.issues[11] = &inert
	api.blocks[11] = []Issue{tracker}
	api.deps[9] = []Issue{root, inert}

	eng.HandleEvent(Event{Type: EventIssueClosed, Repo: "o/off", Kind: KindIssue, Number: 11, Sender: "alice"})

	if tasks := q.all(); len(tasks) != 1 || !strings.Contains(tasks[0].Prompt, "action=wrap-up repo=o/out kind=issue number=9") {
		t.Fatalf("got %v, want the one wrap-up the inert close woke", tasks)
	}
	body := mustWritten(t, api, "out", 9)
	if region := regionOf(body); !strings.Contains(region, "i_out_9") {
		t.Errorf("the forest an inert close woke drew no tracker:\n%s", region)
	}
}

// TestRedrawDrawsNothingForAForestWhollyInTheInertRepository: the inert close
// runs because the repository the event arrived in holds no maitred role, so
// that repository admits nothing to the drawing — a forest standing wholly
// inside it is no forest this path may write to, and its tracker's body stays
// as its own forge left it (§forgejo/webhook/repository-roles,
// §forgejo/decisions/unblock-cascade, s-git/maitred#232).
func TestRedrawDrawsNothingForAForestWhollyInTheInertRepository(t *testing.T) {
	eng, api, q, log := chartForestQueue(t, chartFenced())
	root := chartRoot()
	root.Number, root.Title, root.Repository = 12, "a stranger's forest", "o/off"
	root.State, root.ClosedAt = "closed", chartClosedAt
	tracker := chartTracker(chartFenced())
	tracker.Number, tracker.Title, tracker.Repository = 13, "stranger's outcome tracker", "o/off"
	closing := Issue{
		Number: 11, Title: "a stranger's ticket", State: "closed",
		Repository: "o/off", ClosedAt: chartClosedAt, UpdatedAt: testUpdatedAt,
	}
	api.issues[11], api.issues[12], api.issues[13] = &closing, &root, &tracker
	api.blocks[11] = []Issue{tracker}
	api.blocks[12] = []Issue{tracker}
	api.deps[13] = []Issue{root, closing}

	eng.HandleEvent(Event{Type: EventIssueClosed, Repo: "o/off", Kind: KindIssue, Number: 11, Sender: "alice"})

	// The cascade still woke — the graph is read through an inert repository —
	// but the emission holds off there, and so does its drawing.
	if tasks := q.all(); len(tasks) != 0 {
		t.Fatalf("queued %v, want nothing: no role names the repository the close arrived in", tasks)
	}
	if got := api.writeCount(); got != 0 {
		t.Errorf("wrote %d issues, want none: a repository this path established holds no role admits no forest's block to it", got)
	}
	if body := api.issues[13].Body; body != chartFenced() {
		t.Error("the inert repository's tracker body changed: a chart was spliced in from outside the remit")
	}
	// The walk of the closed issue's downstream edges pays its own read of the
	// tracker; the drawing step pays none beside it — the admission test says
	// no before any request of that repository is made.
	if n := api.callCount("IssueDependencies:off/13"); n != 1 {
		t.Errorf("the inert tracker's rows were read %d times, want the cascade walk's one read and none from the drawing", n)
	}
	if !strings.Contains(log.String(), "no maitred role names") || !strings.Contains(log.String(), "o/off#13") {
		t.Errorf("log = %s, want the drawing's silence named with the tracker it refused", log.String())
	}
}

// TestRedrawNamesTheCascadeReadItCouldNotMake: the pairing read a cascade
// `wrap-up` pays is the redraw's one read whose failure no other test
// breaks — the line an operator reads must name the tracker whose rows went
// unread, the next emission may not be dropped with it, and a tracker in a
// role-less repository is refused before its rows are asked for at all
// (§forgejo/webhook/repository-roles, §forgejo/observability/the-decision-log,
// s-git/maitred#232).
func TestRedrawNamesTheCascadeReadItCouldNotMake(t *testing.T) {
	eng, api, log := chartForestEngine(t, chartFenced())
	secondForestWire(api) // root 8 gates tracker 9, both in the outcomes repository
	wrapUpWire(api)       // tracker 2 is gated by root 1
	api.issueDepsErrOn = map[int]error{9: errors.New("the tracker's rows unread")}
	d := Decision{Cascade: []CascadeDispatch{
		{Key: Key{Repo: "o/out", Kind: KindIssue, Number: 9}, Action: ActionWrapUp},
		{Key: Key{Repo: "o/out", Kind: KindIssue, Number: 2}, Action: ActionWrapUp},
		{Key: Key{Repo: "o/off", Kind: KindIssue, Number: 13}, Action: ActionWrapUp},
	}}

	roots := eng.cascadeRoots(d, "o/out", nil)

	if len(roots) != 1 || roots[0].Number != 1 {
		t.Errorf("the cascade named %v, want root 1 alone: one tracker's unread rows cost that forest alone, and no role-less repository names a root", roots)
	}
	if !strings.Contains(log.String(), "the tracker's rows unread") || !strings.Contains(log.String(), "o/out#9") {
		t.Errorf("log = %s, want the failed read named with the tracker it broke on", log.String())
	}
	if n := api.callCount("IssueDependencies:off/13"); n != 0 {
		t.Errorf("the role-less tracker's rows were read %d times, want 0: the admission test is paid before the request", n)
	}
	if !strings.Contains(log.String(), "o/off#13") {
		t.Errorf("log = %s, want the refused emission named with the tracker it refused", log.String())
	}
}

// TestRedrawNamesTheCascadeScopeItCouldNotAsk: the admission test this road
// pays is a scope lookup, and a lookup that fails is a forest drawn from an
// answer nobody has — nothing is read of it and the line names the scope
// failure the sweep is the backstop for
// (§forgejo/webhook/the-scope-cache, §forgejo/webhook/repository-roles).
func TestRedrawNamesTheCascadeScopeItCouldNotAsk(t *testing.T) {
	eng, api, log := chartForestEngine(t, chartFenced())
	api.listReposErr = errors.New("the org listing failed")
	d := Decision{Cascade: []CascadeDispatch{
		{Key: Key{Repo: "o/off", Kind: KindIssue, Number: 13}, Action: ActionWrapUp},
	}}

	if roots := eng.cascadeRoots(d, "", nil); len(roots) != 0 {
		t.Errorf("the cascade named %v, want nothing while the scope is unknown", roots)
	}
	if n := api.callCount("IssueDependencies:off/13"); n != 0 {
		t.Errorf("the tracker's rows were read %d times, want none: an unknown scope is not an admission", n)
	}
	if !strings.Contains(log.String(), "the org listing failed") || !strings.Contains(log.String(), "o/off#13") {
		t.Errorf("log = %s, want the refused read to name the scope failure and the tracker it concerns", log.String())
	}
}

// secondForestWire hangs a whole second forest on the fixtures: a closed root
// 8 gating an open tracker 9, both in the outcomes repository — a forest no
// membership edge of the first forest's objects reaches
// (§forgejo/decisions/commissioning).
func secondForestWire(api *fakeAPI) (Issue, Issue) {
	root := chartRoot()
	root.Number, root.Title = 8, "the second forest"
	root.State, root.ClosedAt = "closed", chartClosedAt
	tracker := chartTracker(chartFenced())
	tracker.Number, tracker.Title = 9, "outcome tracker of the second forest"
	api.issues[8], api.issues[9] = &root, &tracker
	api.blocks[8] = []Issue{tracker}
	api.deps[9] = []Issue{root, *api.issues[3]}
	return root, tracker
}

// TestRedrawDrawsNothingOnAnObjectNoEdgeReaches: a repository's role admits the
// read, it does not make a forest — an object no membership edge reaches and
// whose own labels name no root is not in a forest, and nothing is drawn for it
// (§forgejo/webhook/repository-roles, §forgejo/decisions/commissioning).
func TestRedrawDrawsNothingOnAnObjectNoEdgeReaches(t *testing.T) {
	eng, api, _, _ := chartForestQueue(t, chartFenced())
	api.deps[3] = []Issue{*api.issues[2]} // gated by its tracker, reached by no root
	redrawSeed(t, eng)

	eng.HandleEvent(Event{Type: EventIssueEdited, Repo: "o/work", Kind: KindIssue, Number: 3, Sender: "alice"})

	if got := api.writeCount(); got != 0 {
		t.Errorf("wrote %d issues, want none: no membership edge, no forest, no drawing", got)
	}
	if got := mustForests(t, eng)[0]; got.Chart != "" || got.ChartRevisited != "" {
		t.Errorf("the payload carries a picture %q drawn for a forest that does not hold the object", got.Chart)
	}
}

// TestRedrawSkipsTheSweepOwnEventAndFinishedForests: the two events that draw
// nothing for a rule rather than a guess — the tick's synthetic row, whose tick
// draws every current forest at step 5, and an event on a forest whose tickets
// are both closed, a history the engine does not edit
// (§forgejo/reconciliation/the-sweep, §forgejo/observability/the-outcomes-view).
func TestRedrawSkipsTheSweepOwnEventAndFinishedForests(t *testing.T) {
	eng, api, _, _ := chartForestQueue(t, chartFenced())
	redrawWire(api)
	redrawSeed(t, eng)

	eng.HandleEvent(Event{Type: EventReconcile, Repo: "o/work", Kind: KindIssue, Number: 3})

	if got := api.writeCount(); got != 0 {
		t.Errorf("the sweep's synthetic event drew %d trackers, want none: step 5 draws that tick's forests", got)
	}

	bothClosed := *api.issues[1]
	bothClosed.State, bothClosed.ClosedAt = "closed", chartClosedAt
	api.issues[1] = &bothClosed
	tracker := *api.issues[2]
	tracker.State, tracker.ClosedAt = "closed", chartClosedAt
	api.issues[2] = &tracker
	api.blocks[1][0] = tracker // the pairing reads the finished tracker as finished
	api.deps[3] = []Issue{bothClosed, tracker}

	eng.HandleEvent(Event{Type: EventIssueEdited, Repo: "o/work", Kind: KindIssue, Number: 3, Sender: "alice"})

	if got := api.writeCount(); got != 0 {
		t.Errorf("a finished forest was drawn %d times, want none: a finished forest is history", got)
	}
}

// TestRedrawDrawsNothingForAPullRequest: a delivery is drawn no node and no
// edge, so no event on one can move a picture — what a merge delivers arrives
// as the issue events it causes (§forgejo/renderer/edge-classes).
func TestRedrawDrawsNothingForAPullRequest(t *testing.T) {
	eng, api, _, _ := chartForestQueue(t, chartFenced())
	redrawWire(api)
	delivery := Issue{Number: 7, Title: "the purge", State: "open", Repository: "o/work", IsPull: true}
	pr := pullRequestFromIssue(delivery)
	api.pulls[7] = &pr

	eng.HandleEvent(Event{Type: EventPRCommented, Repo: "o/work", Kind: KindPR, Number: 7, Sender: "carol"})

	if got := api.writeCount(); got != 0 {
		t.Errorf("a pull request's comment drew %d trackers, want none: no drawing gives a delivery a node", got)
	}
}

// TestRedrawNamesTheMembershipReadItCouldNotMake: the read that names the
// forests an event concerns can break, and a broken read is not the graph
// saying there are none — the line an operator reads names the failure, the
// sweep being the backstop (§forgejo/observability/the-decision-log,
// §forgejo/webhook/re-fetch).
func TestRedrawNamesTheMembershipReadItCouldNotMake(t *testing.T) {
	eng, api, log := chartForestEngine(t, chartFenced())
	redrawSeed(t, eng)
	api.issueDepsErrOn = map[int]error{3: errors.New("the blocker read failed")}

	eng.HandleEvent(Event{Type: EventIssueClosed, Repo: "o/work", Kind: KindIssue, Number: 3, Sender: "alice"})

	if !strings.Contains(log.String(), "the blocker read failed") {
		t.Errorf("log = %s, want the failed membership read named", log.String())
	}
	if got := api.writeCount(); got != 0 {
		t.Errorf("wrote %d issues on a graph nobody read, want none", got)
	}
}

// TestRedrawUnknownScopeFailsClosed: an unknown scope is a failure, not a
// no-role answer — the redraw reads nothing of a repository it cannot place and
// names the failure, the tick's TestTrackerBlock_UnknownScopeWritesNothing being
// the twin (§forgejo/webhook/the-scope-cache, §forgejo/webhook/repository-roles).
func TestRedrawUnknownScopeFailsClosed(t *testing.T) {
	eng, api, log := chartForestEngine(t, chartFenced())
	api.listReposErr = errors.New("the org listing failed")
	root := chartRoot()
	root.Repository = "o/work" // a second repository, so the answer is a scope lookup

	eng.redrawForest(&root, "o/out", time.Now())

	if got := api.writeCount(); got != 0 {
		t.Errorf("wrote %d issues, want none while the scope is unknown", got)
	}
	if got := api.callCount("IssueBlocks:work/1"); got != 0 {
		t.Errorf("read the root's rows %d times, want none: the admission test comes before the read", got)
	}
	if !strings.Contains(log.String(), "the org listing failed") {
		t.Errorf("log = %s, want the refused draw to name the scope failure", log.String())
	}
}

// TestRedrawSkipsARootNoRoleNames: a root an edge reached stands in whichever
// repository the edge named, and a repository no maitred role names is read
// nothing of — its rows, its titles and its charts stay where they are, and the
// event's own decision is none the worse (§forgejo/webhook/repository-roles).
func TestRedrawSkipsARootNoRoleNames(t *testing.T) {
	eng, api, log := chartForestEngine(t, chartFenced())
	root := *api.issues[1]
	root.Repository = "o/archive" // a repository carrying no maitred topic at all
	api.deps[3] = []Issue{root, *api.issues[2]}

	eng.HandleEvent(Event{Type: EventIssueEdited, Repo: "o/work", Kind: KindIssue, Number: 3, Sender: "alice"})

	if got := api.writeCount(); got != 0 {
		t.Errorf("wrote %d issues, want none: a root no role names is drawn nowhere", got)
	}
	if got := api.callCount("IssueBlocks:archive/1"); got != 0 {
		t.Errorf("read the unadmitted repository's rows %d times, want none: the test comes before the read", got)
	}
	if !strings.Contains(log.String(), "no maitred role names") || !strings.Contains(log.String(), "o/archive#1") {
		t.Errorf("log = %s, want the skipped root named with the rule that skipped it", log.String())
	}
}

// TestRedrawOfAFinishedForestDrawsNothing: a forest whose tickets are both
// closed is history, and the engine does not edit history — the pair both sinks
// hold stands frozen under the stamp of the drawing that ended the forest
// (§forgejo/observability/the-outcomes-view).
func TestRedrawOfAFinishedForestDrawsNothing(t *testing.T) {
	eng, api, log := chartForestEngine(t, chartFenced())
	root := *api.issues[1]
	root.State, root.ClosedAt = "closed", chartClosedAt
	tracker := *api.issues[2]
	tracker.State, tracker.ClosedAt = "closed", chartClosedAt
	api.issues[1], api.issues[2] = &root, &tracker
	api.blocks[1][0] = tracker // the pairing reads the finished tracker as finished

	eng.redrawForest(&root, "o/out", time.Now())
	if got := api.writeCount(); got != 0 {
		t.Errorf("wrote %d issues, want none: the forest is finished", got)
	}
	if !strings.Contains(log.String(), "finished forest") {
		t.Errorf("log = %s, want the finished forest named as the reason nothing was drawn", log.String())
	}
}

// TestRedrawLeavesTheStandingPictureOnAFailedDraw: a drawing that broke dresses
// no new stamp over the picture both sinks hold — the pair is never written
// apart, and a remembered chart never meets a stamp it did not earn
// (§forgejo/observability/the-outcomes-view).
func TestRedrawLeavesTheStandingPictureOnAFailedDraw(t *testing.T) {
	eng, api, _, _ := chartForestQueue(t, chartFenced())
	redrawWire(api)
	redrawSeed(t, eng)
	eng.writeTrackerBlocks(mustForests(t, eng), chartAt)
	before := mustForests(t, eng)[0]
	if before.Chart == "" {
		t.Fatal("the tick drew no picture, so this test would prove nothing")
	}
	api.getIssueErrOn = map[int]error{1: errors.New("the root read failed")}

	eng.HandleEvent(Event{Type: EventIssueEdited, Repo: "o/work", Kind: KindIssue, Number: 3, Sender: "alice"})

	after := mustForests(t, eng)[0]
	if after.Chart != before.Chart || after.ChartRevisited != before.ChartRevisited {
		t.Errorf("the failed draw moved the pair from %q/%q to %q/%q", before.Chart, before.ChartRevisited, after.Chart, after.ChartRevisited)
	}
	if got := api.writeCount(); got != 1 {
		t.Errorf("wrote %d issues, want only the tick's own write", got)
	}
}

// TestRedrawRecordsTheChartTheWriteRefused: an ambiguous fence pair is a
// refusal to write, not a refusal to draw, so the block stands as its deployment
// left it while the payload carries the drawing — as it does on the tick's own
// write (§forgejo/observability/the-outcomes-view, s-git/maitred#232).
func TestRedrawRecordsTheChartTheWriteRefused(t *testing.T) {
	body := chartFenced() + "\n" + fenceBegin + "\n\nanother forest\n\n" + fenceEnd + "\n"
	eng, api, _, _ := chartForestQueue(t, body)
	redrawWire(api)
	redrawSeed(t, eng)

	eng.HandleEvent(Event{Type: EventIssueEdited, Repo: "o/work", Kind: KindIssue, Number: 3, Sender: "alice"})

	if got := api.writeCount(); got != 0 {
		t.Errorf("wrote %d issues, want none: the ambiguous tracker stays as found", got)
	}
	if after := mustForests(t, eng)[0]; after.Chart == "" {
		t.Error("the payload carries no picture: a refused write was taken for a failed drawing")
	}
}

// TestRedrawOverAnUnchangedForestWritesNothing: the redraw reads the live graph
// rather than asking what the event kind could have changed, and an event that
// moved no row composes the bytes already there — so what it does not write is
// the answer to "did this change the picture?", whatever minute the clock is in
// (§forgejo/reconciliation/the-tracker-block, s-git/maitred#274).
func TestRedrawOverAnUnchangedForestWritesNothing(t *testing.T) {
	eng, api, _, _ := chartForestQueue(t, chartFenced())
	redrawWire(api)
	redrawSeed(t, eng)

	eng.HandleEvent(Event{Type: EventIssueEdited, Repo: "o/work", Kind: KindIssue, Number: 3, Sender: "alice"})
	first := mustWritten(t, api, "out", 2)
	eng.HandleEvent(Event{Type: EventIssueCommented, Repo: "o/work", Kind: KindIssue, Number: 3, Sender: "bob"})

	switch writes := api.callCount("UpdateIssueBody:out/2"); writes {
	case 1:
		// The stamp had not moved: the redraw found the body current.
	case 2:
		second := mustWritten(t, api, "out", 2)
		if got := strings.Replace(second, stampLine(second), stampLine(first), 1); got != first {
			t.Error("the second event changed more than the stamp over an unchanged graph")
		}
	default:
		t.Errorf("two events over an unchanged forest wrote the tracker %d times, want one write or a second that only moved the stamp", writes)
	}
}

// TestRedrawNamesWhichAbsenceItFound: the redraw reaching no forest does it two
// ways that are not the same fact — a read that broke, which the sweep is the
// backstop for, and a graph whose root gates no tracker, which is the forest
// being what it is. The read that broke says so where an operator reads; the
// absence says it quietly (§forgejo/observability/the-decision-log,
// §forgejo/webhook/re-fetch).
func TestRedrawNamesWhichAbsenceItFound(t *testing.T) {
	tests := []struct {
		name   string
		wire   func(api *fakeAPI)
		expect string
	}{
		{
			name: "a pairing read that failed",
			wire: func(api *fakeAPI) {
				api.issueBlocksErrOn = map[int]error{1: errors.New("the root's rows unread")}
			},
			expect: "the root's rows unread",
		},
		{
			name: "a root gating no tracker",
			wire: func(api *fakeAPI) {
				member := chartRow(3, "the purge, one command", nil, "open", time.Time{})
				api.blocks[1] = []Issue{member}
			},
			expect: "the root gates no tracker",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eng, api, log := chartForestEngine(t, chartFenced())
			tt.wire(api)
			root := chartRoot()

			eng.redrawForest(&root, "o/out", time.Now())

			if got := api.writeCount(); got != 0 {
				t.Errorf("wrote %d issues, want none: no tracker is no region to draw into", got)
			}
			if !strings.Contains(log.String(), tt.expect) || !strings.Contains(log.String(), "o/out#1") {
				t.Errorf("log = %s, want %q named with the root it concerns", log.String(), tt.expect)
			}
		})
	}
}

// TestRedrawDrawsAForestTheViewDoesNotHoldYet: the set the outcomes view serves
// is what an enumeration vouches for, and a redraw invents no row for a forest no
// enumeration named — the block lands now, the payload carries the forest from
// the tick's head (§forgejo/observability/the-outcomes-view).
func TestRedrawDrawsAForestTheViewDoesNotHoldYet(t *testing.T) {
	eng, api, _, _ := chartForestQueue(t, chartFenced())
	redrawWire(api)

	eng.HandleEvent(Event{Type: EventIssueEdited, Repo: "o/work", Kind: KindIssue, Number: 3, Sender: "alice"})

	mustWritten(t, api, "out", 2)
	if _, err := eng.Forests(); !errors.Is(err, errForestsUnavailable) {
		t.Errorf("Forests = %v, want the view still saying nothing was enumerated: the redraw invented no row", err)
	}
}
