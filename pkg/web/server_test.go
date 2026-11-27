package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"maitred/pkg/forgejo"
	"maitred/pkg/queue"
)

// stubForgejoAPI is a minimal forgejo.API for the web server tests
// (§forgejo/observability/the-dashboard-view).
type stubForgejoAPI struct {
	issue *forgejo.Issue

	// issues, when set, answers a single-issue read by number — the
	// forge answers with the row it holds, and the tick's drawing reads
	// roots and trackers by number the way any other read does
	// (§forgejo/client/operations). Where the map holds no row the stub
	// falls back to issue, so the dispatch tests keep their fixture.
	issues map[int]*forgejo.Issue

	// reposCounts lists how often the stub has been asked for the
	// org's repositories, so a test can see a manually fired sweep
	// run (§forgejo/observability/triggering-a-sweep).
	reposCounts atomic.Int32

	// The outcomes-view fixtures: the organisation's repositories,
	// open issues per repository, and the blocks listings — set only
	// by the outcomes-endpoint tests, so every other test keeps the
	// single maitred-enabled repository below (§forgejo/observability/the-outcomes-view).
	repos        []forgejo.Repository
	openIssues   map[string][]forgejo.Issue
	closedIssues map[string][]forgejo.Issue
	blocks       map[int][]forgejo.Issue

	// reposErr, when set, fails only ListOrgRepositories — so a test
	// can break the outcomes view's scope listing without breaking
	// anything else it reads.
	reposErr error

	// issuesErr, when set, fails every ListOpenIssues — so a test can
	// make a sweep's forest enumeration fail while its own listing
	// lands (§forgejo/observability/the-outcomes-view).
	issuesErr error
}

// The stub's organisation holds the one maitred-enabled repository the
// tests dispatch in — the event path's scope test consults the listing
// (§forgejo/webhook/the-scope-cache) — unless a test restates the
// listing outright.
func (s *stubForgejoAPI) ListOrgRepositories(string) ([]forgejo.Repository, error) {
	s.reposCounts.Add(1)
	if s.reposErr != nil {
		return nil, s.reposErr
	}
	if s.repos != nil {
		return s.repos, nil
	}
	return []forgejo.Repository{{
		Name:     "r",
		FullName: "o/r",
		Owner:    "o",
		Topics:   []string{"maitred-enabled"},
	}}, nil
}

func (s *stubForgejoAPI) ListOpenIssues(_, repo string) ([]forgejo.Issue, error) {
	if s.issuesErr != nil {
		return nil, s.issuesErr
	}
	return s.openIssues[repo], nil
}

// ListAllIssues is the stateless listing: the outcomes view's walk
// reads every issue once, open and closed alike, and splits current
// from past on each row's own state (§forgejo/observability/the-outcomes-view).
func (s *stubForgejoAPI) ListAllIssues(_, repo string) ([]forgejo.Issue, error) {
	if s.issuesErr != nil {
		return nil, s.issuesErr
	}
	out := make([]forgejo.Issue, 0, len(s.openIssues[repo])+len(s.closedIssues[repo]))
	out = append(out, s.openIssues[repo]...)
	return append(out, s.closedIssues[repo]...), nil
}

func (s *stubForgejoAPI) ListOpenPullRequests(string, string) ([]forgejo.PullRequest, error) {
	return nil, nil
}

func (s *stubForgejoAPI) GetIssue(_, _ string, number int) (*forgejo.Issue, error) {
	if row, ok := s.issues[number]; ok {
		return row, nil
	}
	return s.issue, nil
}

func (s *stubForgejoAPI) GetPullRequest(string, string, int) (*forgejo.PullRequest, error) {
	return nil, nil
}

func (s *stubForgejoAPI) IssueBlocks(_, _ string, number int) ([]forgejo.Issue, error) {
	return s.blocks[number], nil
}

func (s *stubForgejoAPI) IssueDependencies(string, string, int) ([]forgejo.Issue, error) {
	return nil, nil
}

func (s *stubForgejoAPI) ListIssueComments(string, string, int) ([]forgejo.Comment, error) {
	return nil, nil
}

func (s *stubForgejoAPI) ListPullRequestReviews(string, string, int) ([]forgejo.Review, error) {
	return nil, nil
}

// UpdateIssueBody is the engine's one mutation. The web tests do not study the
// drawing it lands, so the stub answers and lands nothing
// (§forgejo/reconciliation/the-tracker-block).
func (s *stubForgejoAPI) UpdateIssueBody(string, string, int, string) error {
	return nil
}

// newForgejoEngine builds an engine over the given API stub, without
// handling any event.
func newForgejoEngine(t *testing.T, api *stubForgejoAPI) *forgejo.Engine {
	t.Helper()
	store, err := forgejo.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	cfg := &forgejo.Config{Org: "o", ReconcileInterval: time.Hour}
	cfg.Actions = make(map[string]forgejo.PromptConfig, len(forgejo.AllActions))
	for _, a := range forgejo.AllActions {
		cfg.Actions[string(a)] = forgejo.PromptConfig{
			Prompt:   string(a),
			Identity: "test",
			Timeout:  time.Hour,
		}
	}
	eng := forgejo.NewEngine(
		api,
		store, cfg, queue.NewTaskQueue(),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	// Stop waits the in-flight sweep out whatever a test started, so
	// no sweep goroutine outlives the temporary directories above.
	t.Cleanup(eng.Stop)
	return eng
}

// testForgejoEngine builds an engine that has made one dispatching
// decision, alongside the API stub it reads through
// (§forgejo/observability/the-dashboard-view).
func testForgejoEngine(t *testing.T) (*forgejo.Engine, *stubForgejoAPI) {
	t.Helper()
	api := &stubForgejoAPI{issue: &forgejo.Issue{
		Number:     7,
		State:      "open",
		UpdatedAt:  time.Date(2026, 9, 16, 13, 25, 18, 0, time.UTC),
		Repository: "o/r",
	}}
	eng := newForgejoEngine(t, api)
	eng.HandleEvent(forgejo.Event{Type: forgejo.EventIssueOpened, Repo: "o/r", Kind: forgejo.KindIssue, Number: 7, Sender: "alice"})
	return eng, api
}

// trackedJSON mirrors one tracked entry of the /api/forgejo response
// (§forgejo/observability/the-dashboard-view).
type trackedJSON struct {
	Repo      string `json:"repo"`
	Kind      string `json:"kind"`
	Number    int    `json:"number"`
	State     string `json:"state"`
	Watermark *struct {
		Action   string `json:"action"`
		Revision string `json:"revision"`
	} `json:"watermark"`
	Decision struct {
		Dispatched bool   `json:"dispatched"`
		Action     string `json:"action"`
		Reason     string `json:"reason"`
		Event      string `json:"event"`
	} `json:"decision"`
}

// forgejoViewJSON mirrors the /api/forgejo response shape: the tracked
// list, the sweep catalog, and the one engine-status field
// (§forgejo/observability/the-dashboard-view,
// §forgejo/observability/triggering-a-sweep).
type forgejoViewJSON struct {
	EngineStatus string          `json:"engine_status"`
	Items        []trackedJSON   `json:"items"`
	Sweeps       []forgejo.Sweep `json:"sweeps"`
}

func getForgejo(t *testing.T, srv *Server) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/forgejo", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// TestForgejoEndpoint: the endpoint serves the tracked issues/PRs with
// their state, watermark, and last decision
// (§forgejo/observability/the-dashboard-view).
func TestForgejoEndpoint(t *testing.T) {
	fjEng, _ := testForgejoEngine(t)
	srv := New(0, nil, "test", fjEng, forgejo.EngineStateWord(true, nil))
	rec := getForgejo(t, srv)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var view forgejoViewJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if view.EngineStatus != forgejo.EngineUpWord {
		t.Errorf("engine_status = %q, want %q", view.EngineStatus, forgejo.EngineUpWord)
	}
	tracked := view.Items
	if len(tracked) != 1 {
		t.Fatalf("tracked has %d entries, want 1", len(tracked))
	}
	e := tracked[0]
	if e.Repo != "o/r" || e.Kind != "issue" || e.Number != 7 {
		t.Errorf("key = %s/%s/%d, want o/r/issue/7", e.Repo, e.Kind, e.Number)
	}
	if e.State != "open" {
		t.Errorf("state = %q, want open", e.State)
	}
	if e.Watermark == nil || e.Watermark.Action != "implement" {
		t.Errorf("watermark = %+v, want action implement", e.Watermark)
	}
	if !e.Decision.Dispatched || e.Decision.Action != "implement" {
		t.Errorf("decision = %+v, want a dispatched implement", e.Decision)
	}
	if e.Decision.Reason == "" {
		t.Error("decision has no reason")
	}
	if e.Decision.Event != "issue_opened" {
		t.Errorf("decision event = %q, want issue_opened", e.Decision.Event)
	}
}

// TestForgejoEndpoint_Disabled: with no engine and no cause in hand,
// the endpoint serves an empty view naming the not-configured state
// (§forgejo/observability/the-dashboard-view).
func TestForgejoEndpoint_Disabled(t *testing.T) {
	srv := New(0, nil, "test", nil, forgejo.EngineStateWord(false, nil))
	rec := getForgejo(t, srv)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var view forgejoViewJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if view.EngineStatus != forgejo.CategoryNotConfigured.String() {
		t.Errorf("engine_status = %q, want %q", view.EngineStatus, forgejo.CategoryNotConfigured.String())
	}
	if len(view.Items) != 0 {
		t.Errorf("tracked has %d entries, want 0", len(view.Items))
	}
}

// TestForgejoEndpoint_EngineStates: the one engine-status field renders
// the engine's state in the category's words, keeps the two nil-engine
// states distinguishable, and never leaks the raw error text
// (§forgejo/observability/the-dashboard-view,
// §forgejo/observability/why-the-engine-is-off).
func TestForgejoEndpoint_EngineStates(t *testing.T) {
	constructionFailure := fmt.Errorf("new forgejo engine: %w", forgejo.ErrEnvironmentIncomplete)
	cases := []struct {
		name    string
		fjEng   *forgejo.Engine
		state   string
		want    string
		mustNot []string
	}{
		{
			name:  "engine healthy",
			fjEng: nil,
			state: forgejo.EngineStateWord(true, nil),
			want:  forgejo.EngineUpWord,
		},
		{
			name:  "nil because nothing is configured",
			fjEng: nil,
			state: forgejo.EngineStateWord(false, nil),
			want:  forgejo.CategoryNotConfigured.String(),
		},
		{
			name:    "nil because construction failed",
			fjEng:   nil,
			state:   forgejo.EngineStateWord(false, constructionFailure),
			want:    forgejo.CategoryCredentialsMissing.String(),
			mustNot: []string{forgejo.ErrEnvironmentIncomplete.Error(), constructionFailure.Error(), forgejo.URLEnvVar},
		},
	}
	statuses := map[string]string{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := New(0, nil, "test", tc.fjEng, tc.state)
			rec := getForgejo(t, srv)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			var view forgejoViewJSON
			if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if view.EngineStatus != tc.want {
				t.Errorf("engine_status = %q, want %q", view.EngineStatus, tc.want)
			}
			for _, leak := range tc.mustNot {
				if strings.Contains(rec.Body.String(), leak) {
					t.Errorf("payload leaks raw error text %q: %s", leak, rec.Body.String())
				}
			}
			statuses[tc.name] = view.EngineStatus
		})
	}
	if statuses["nil because nothing is configured"] == statuses["nil because construction failed"] {
		t.Error("a configured-but-broken engine must not read as a healthy one with nothing tracked")
	}
}

// TestForgejoEndpoint_Method: only GET is allowed.
func TestForgejoEndpoint_Method(t *testing.T) {
	srv := New(0, nil, "test", nil, forgejo.EngineStateWord(false, nil))
	req := httptest.NewRequest(http.MethodPost, "/api/forgejo", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

// TestForgejoEndpoint_SweepsCatalog: with the engine up the view
// carries the sweep catalog — the reconciliation sweep, ided and
// titled — and with no engine the catalog is empty
// (§forgejo/observability/triggering-a-sweep).
func TestForgejoEndpoint_SweepsCatalog(t *testing.T) {
	fjEng, _ := testForgejoEngine(t)
	rec := getForgejo(t, New(0, nil, "test", fjEng, forgejo.EngineStateWord(true, nil)))
	var view forgejoViewJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var found bool
	for _, s := range view.Sweeps {
		if s.ID == forgejo.SweepIDReconcile && s.Title != "" {
			found = true
		}
	}
	if !found {
		t.Errorf("sweeps = %+v, want the reconciliation sweep", view.Sweeps)
	}

	rec = getForgejo(t, New(0, nil, "test", nil, forgejo.EngineStateWord(false, nil)))
	view = forgejoViewJSON{}
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(view.Sweeps) != 0 {
		t.Errorf("sweeps = %+v, want empty without an engine", view.Sweeps)
	}
}

// TestSweepFire: POST /api/forgejo/sweeps/{id} with the engine up
// answers 202 Accepted and starts the sweep now rather than at the
// next tick (§forgejo/observability/triggering-a-sweep). The engine is
// started and its start-time sweep waited out first, so the listing
// this test observes can come only from the triggered start — the
// tick is an hour away.
func TestSweepFire(t *testing.T) {
	fjEng, api := testForgejoEngine(t)
	srv := New(0, nil, "test", fjEng, forgejo.EngineStateWord(true, nil))
	fjEng.Start()

	deadline := time.Now().Add(5 * time.Second)
	for api.reposCounts.Load() < 1 {
		if time.Now().After(deadline) {
			t.Fatal("the engine's start-time sweep did not run")
		}
		time.Sleep(10 * time.Millisecond)
	}
	before := api.reposCounts.Load()

	req := httptest.NewRequest(http.MethodPost, "/api/forgejo/sweeps/"+forgejo.SweepIDReconcile, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	// A fresh budget for the triggered start: the wait above may
	// have consumed the old deadline, and a failure here should not
	// read like a trigger regression on a slow machine.
	deadline = time.Now().Add(5 * time.Second)
	for api.reposCounts.Load() <= before {
		if time.Now().After(deadline) {
			t.Fatal("the sweep did not run after the trigger")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestSweepFire_StoppedEngine: an engine that exists but does not run
// its sweep loop has no lifecycle owner for the start; the endpoint
// answers 503 and nothing runs (§forgejo/observability/triggering-a-sweep,
// §forgejo/reconciliation/scheduling).
func TestSweepFire_StoppedEngine(t *testing.T) {
	fjEng, api := testForgejoEngine(t)
	srv := New(0, nil, "test", fjEng, forgejo.EngineStateWord(true, nil))
	before := api.reposCounts.Load()

	req := httptest.NewRequest(http.MethodPost, "/api/forgejo/sweeps/"+forgejo.SweepIDReconcile, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	time.Sleep(100 * time.Millisecond)
	if n := api.reposCounts.Load(); n != before {
		t.Errorf("stopped engine ran a sweep anyway: %d listings, want %d", n, before)
	}
}

// TestSweepFire_UnknownSweep: an id outside the catalog answers 404
// (§forgejo/observability/triggering-a-sweep).
func TestSweepFire_UnknownSweep(t *testing.T) {
	fjEng, _ := testForgejoEngine(t)
	srv := New(0, nil, "test", fjEng, forgejo.EngineStateWord(true, nil))
	req := httptest.NewRequest(http.MethodPost, "/api/forgejo/sweeps/no-such-sweep", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// TestSweepFire_NoEngine: with no engine up there is nothing to
// trigger; the endpoint answers 503 (§forgejo/observability/triggering-a-sweep).
func TestSweepFire_NoEngine(t *testing.T) {
	srv := New(0, nil, "test", nil, forgejo.EngineStateWord(false, nil))
	req := httptest.NewRequest(http.MethodPost, "/api/forgejo/sweeps/"+forgejo.SweepIDReconcile, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

// TestSweepFire_Method: only POST triggers; a GET answers 405
// (§forgejo/observability/triggering-a-sweep).
func TestSweepFire_Method(t *testing.T) {
	fjEng, _ := testForgejoEngine(t)
	srv := New(0, nil, "test", fjEng, forgejo.EngineStateWord(true, nil))
	req := httptest.NewRequest(http.MethodGet, "/api/forgejo/sweeps/"+forgejo.SweepIDReconcile, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

// TestServerStartStop: Start serves the dashboard and Stop closes the
// listener, so the dashboard stops answering sweep triggers before
// shutdown moves on to the engine whose wait those triggers could
// otherwise restart (§forgejo/observability/triggering-a-sweep).
func TestServerStartStop(t *testing.T) {
	// Port 0 lets the kernel pick the port and Start binds it before
	// returning, so the test neither hands a port number across a
	// close-then-rebind window nor polls for the socket to appear;
	// Addr reports what the bind was actually given.
	srv := New(0, nil, "test", nil, forgejo.EngineStateWord(false, nil))
	if err := srv.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer srv.Stop()

	_, port, err := net.SplitHostPort(srv.Addr())
	if err != nil {
		t.Fatalf("addr %q: %v", srv.Addr(), err)
	}
	base := fmt.Sprintf("http://127.0.0.1:%s/api/forgejo", port)

	res, err := http.Get(base)
	if err != nil {
		t.Fatalf("get %s: %v", base, err)
	}
	res.Body.Close()

	if err := srv.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if res, err := http.Get(base); err == nil {
		res.Body.Close()
		t.Error("the dashboard still serves after Stop")
	}
	// Stop is a no-op once the server has stopped, so a second call
	// reports nothing — which is what makes the deferred Stop at the
	// top of the test safe alongside the one just above.
	if err := srv.Stop(); err != nil {
		t.Errorf("second Stop: %v, want the no-op to report nothing", err)
	}
}

// outcomesStubAPI is the outcomes-view fixture: the organisation holds a
// work-item repository and an outcomes repository; the outcomes
// repository holds a root paired with its tracker, a root whose
// tracker is absent, and a finished forest whose tickets are both
// closed (§forgejo/observability/the-outcomes-view).
func outcomesStubAPI() *stubForgejoAPI {
	root := forgejo.Issue{
		Number: 51, Title: "UI for human review", State: "open",
		Labels:       []string{"ideation"},
		LabelColours: map[string]string{"ideation": "#5319e7"},
		Repository:   "o/out",
		HTMLURL:      "https://forge.example.com/o/out/issues/51",
	}
	tracker := forgejo.Issue{
		Number: 71, Title: "forest tracker", State: "open",
		Labels:       []string{"outcome"},
		LabelColours: map[string]string{"outcome": "#1d76db"},
		Repository:   "o/out",
		HTMLURL:      "https://forge.example.com/o/out/issues/71",
		Body: "## Outcome\n\nThe prose a human wrote.\n\n" +
			"<!-- maitred:forest:begin -->\n\nLast revisited: 2026-11-16 09:12 AEDT\n\n" +
			"```mermaid\nflowchart TD\n\tr51[root #51] --> t71[tracker #71]\n```\n\n" +
			"<!-- maitred:forest:end -->\n",
	}
	loneRoot := forgejo.Issue{
		Number: 52, Title: "root without a gate", State: "open",
		Labels:       []string{"ideation"},
		LabelColours: map[string]string{"ideation": "#5319e7"},
		Repository:   "o/out",
		HTMLURL:      "https://forge.example.com/o/out/issues/52",
	}
	// The finished forest: root and tracker both closed, so the walk
	// names it past rather than dropping it
	// (§forgejo/observability/the-outcomes-view).
	pastRoot := forgejo.Issue{
		Number: 53, Title: "a forest that finished", State: "closed",
		Labels:       []string{"ideation"},
		LabelColours: map[string]string{"ideation": "#5319e7"},
		Repository:   "o/out",
		HTMLURL:      "https://forge.example.com/o/out/issues/53",
	}
	pastTracker := forgejo.Issue{
		Number: 73, Title: "tracker closed with its forest", State: "closed",
		Labels:       []string{"outcome"},
		LabelColours: map[string]string{"outcome": "#1d76db"},
		Repository:   "o/out",
		HTMLURL:      "https://forge.example.com/o/out/issues/73",
	}
	// The forest's one member is a human task with no blockers of its
	// own, so the forest is waiting on a person and carries a warning
	// naming it (§forgejo/observability/the-outcomes-view). This member's
	// body is prose the engine reads and never serves: the held-back row
	// for it carries reference, labels and age alone
	// (§forgejo/observability/the-held-back-view).
	member := forgejo.Issue{
		Number: 90, Title: "hand the keys over", State: "open",
		Labels: []string{"human-task"}, Repository: "o/r",
		HTMLURL: "https://forge.example.com/o/r/issues/90",
		Body:    "The prose the person left for themselves.",
	}
	return &stubForgejoAPI{
		issue:  &forgejo.Issue{Number: 7, State: "open", Repository: "o/r"},
		issues: map[int]*forgejo.Issue{51: &root, 71: &tracker, 52: &loneRoot, 53: &pastRoot, 73: &pastTracker},
		repos: []forgejo.Repository{
			{Name: "r", FullName: "o/r", Owner: "o", Topics: []string{"maitred-enabled"}},
			{Name: "out", FullName: "o/out", Owner: "o", Topics: []string{"maitred-outcomes-repo"}},
		},
		openIssues:   map[string][]forgejo.Issue{"out": {root, tracker, loneRoot}},
		closedIssues: map[string][]forgejo.Issue{"out": {pastRoot, pastTracker}},
		blocks:       map[int][]forgejo.Issue{51: {tracker, member}, 53: {pastTracker}},
	}
}

// forestIssueJSON mirrors one issue row of a forest in the outcomes
// view: number, title, state, HTML URL, and the repository the issue
// lives in — stated per row, because a tracker may sit outside the
// repository the forest grew in (§forgejo/observability/the-outcomes-view).
type forestIssueJSON struct {
	Number     int    `json:"number"`
	Repository string `json:"repository"`
	Title      string `json:"title"`
	State      string `json:"state"`
	HTMLURL    string `json:"html_url"`
}

// forestJSON mirrors one forest of the outcomes view: the repository,
// the root, the tracker — a null tracker states its honest absence — and
// the picture the sweep tick drew from the forest's graph: its Mermaid
// source and the drawing's stamp, empty when the tick drew nothing
// (§forgejo/observability/the-outcomes-view).
type forestJSON struct {
	Repository     string           `json:"repository"`
	Root           forestIssueJSON  `json:"root"`
	Tracker        *forestIssueJSON `json:"tracker"`
	Chart          string           `json:"chart"`
	ChartRevisited string           `json:"chart_revisited"`
	Warnings       []forestWarnJSON `json:"warnings"`
	WarningsWalked bool             `json:"warnings_walked"`
}

// forestWarnJSON mirrors one warning of a forest: its reference, the
// kind naming why it warns, and the age of the condition in seconds
// (§forgejo/observability/the-outcomes-view).
type forestWarnJSON struct {
	Kind       string  `json:"kind"`
	Repository string  `json:"repository"`
	Number     int     `json:"number"`
	Title      string  `json:"title"`
	HTMLURL    string  `json:"html_url"`
	AgeSeconds float64 `json:"age_seconds"`
}

// outcomesViewJSON mirrors the /api/forgejo/outcomes response shape:
// the current forest list, the past forest list beside it, and the one
// engine-status field (§forgejo/observability/the-outcomes-view).
type outcomesViewJSON struct {
	EngineStatus string       `json:"engine_status"`
	Forests      []forestJSON `json:"forests"`
	PastForests  []forestJSON `json:"past_forests"`
}

func getOutcomes(t *testing.T, srv *Server) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/forgejo/outcomes", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// TestForgejoOutcomesEndpoint: the endpoint serves the forests the
// sweep tick recorded — a root and its tracker listed together, a root
// whose tracker is missing listed with the absence stated — alongside
// the engine-status field; serving the request itself reads no forge
// (§forgejo/observability/the-outcomes-view).
func TestForgejoOutcomesEndpoint(t *testing.T) {
	api := outcomesStubAPI()
	fjEng := newForgejoEngine(t, api)
	fjEng.Reconcile() // the sweep tick records the set the view serves
	srv := New(0, nil, "test", fjEng, forgejo.EngineStateWord(true, nil))
	rec := getOutcomes(t, srv)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var view outcomesViewJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if view.EngineStatus != forgejo.EngineUpWord {
		t.Errorf("engine_status = %q, want %q", view.EngineStatus, forgejo.EngineUpWord)
	}
	if len(view.Forests) != 2 {
		t.Fatalf("forests = %+v, want both roots of o/out listed", view.Forests)
	}
	var paired, lone *forestJSON
	for i := range view.Forests {
		switch view.Forests[i].Root.Number {
		case 51:
			paired = &view.Forests[i]
		case 52:
			lone = &view.Forests[i]
		default:
			t.Errorf("unexpected root %d listed", view.Forests[i].Root.Number)
		}
	}
	if paired == nil || lone == nil {
		t.Fatalf("forests = %+v, want roots 51 and 52", view.Forests)
	}
	if paired.Repository != "o/out" {
		t.Errorf("repository = %q, want o/out", paired.Repository)
	}
	if paired.Root.Title != "UI for human review" || paired.Root.State != "open" || paired.Root.HTMLURL == "" {
		t.Errorf("root = %+v, want ideation issue 51 with its fields", paired.Root)
	}
	if paired.Root.Repository != "o/out" || (paired.Tracker != nil && paired.Tracker.Repository != "o/out") {
		t.Errorf("rows = %+v, want each row to state the repository the issue lives in", paired)
	}
	if paired.Tracker == nil {
		t.Fatal("tracker = nil, want the outcome issue the root blocks")
	}
	if paired.Tracker.Number != 71 || paired.Tracker.Title != "forest tracker" || paired.Tracker.State != "open" || paired.Tracker.HTMLURL == "" {
		t.Errorf("tracker = %+v, want outcome issue 71 with its fields", paired.Tracker)
	}
	// The forest carries its picture: the Mermaid source the tick drew
	// from the forest's graph — not the stale chart its tracker's
	// description still claims — under the stamp of that drawing
	// (§forgejo/observability/the-outcomes-view).
	if !strings.Contains(paired.Chart, "flowchart TD") ||
		!strings.Contains(paired.Chart, "i_out_51") || !strings.Contains(paired.Chart, "i_out_71") {
		t.Errorf("chart = %q, want the graph's drawing of the root and its tracker", paired.Chart)
	}
	if strings.Contains(paired.Chart, "r51[root #51]") {
		t.Errorf("chart = %q, want the drawing, not the stale chart the description claims", paired.Chart)
	}
	if paired.ChartRevisited == "" {
		t.Error("chart_revisited is empty, want the stamp of the drawing this tick made")
	}
	// The forest waiting on a person says so: the warning is the member
	// itself, linked rather than described, and carries its kind
	// (§forgejo/observability/the-outcomes-view).
	if len(paired.Warnings) != 1 {
		t.Fatalf("warnings = %+v, want the one human task holding this forest", paired.Warnings)
	}
	warn := paired.Warnings[0]
	if warn.Kind != "human_task_blocking" {
		t.Errorf("kind = %q, want human_task_blocking", warn.Kind)
	}
	if warn.Repository != "o/r" || warn.Number != 90 || warn.Title != "hand the keys over" || warn.HTMLURL == "" {
		t.Errorf("warning = %+v, want the member o/r#90 with its fields", warn)
	}
	// A forest with no tracker publishes both fields empty — present in
	// the body, and stating nothing rather than omitting the subject.
	if lone.Chart != "" || lone.ChartRevisited != "" {
		t.Errorf("root 52 picture = %q/%q, want both empty with no tracker to read", lone.Chart, lone.ChartRevisited)
	}
	// A forest nothing is waiting on carries the honest empty, spelled
	// as a list rather than a null — and the flag saying the walk ran
	// to find nothing (§forgejo/observability/the-outcomes-view).
	if lone.Warnings == nil || len(lone.Warnings) != 0 {
		t.Errorf("root 52 warnings = %+v, want the empty list", lone.Warnings)
	}
	if !lone.WarningsWalked {
		t.Errorf("root 52 says its warnings were not walked, want the walk's empty answer")
	}
	if lone.Tracker != nil {
		t.Errorf("root 52 tracker = %+v, want the absence stated", lone.Tracker)
	}
	// The stated absence is spelled as a null in the body, not omitted:
	// one check inside the shape test, since root 52 already stands
	// here for the case (§forgejo/observability/the-outcomes-view).
	if !strings.Contains(rec.Body.String(), `"tracker":null`) {
		t.Errorf("body %s, want the absent tracker spelled as null", rec.Body.String())
	}
	// The trackerless root's picture fields are present and empty rather
	// than omitted, the same spelling discipline as the null tracker.
	if !strings.Contains(rec.Body.String(), `"chart":""`) {
		t.Errorf("body %s, want the absent tracker's picture spelled as empty fields", rec.Body.String())
	}
	// The open tracker is no forest of its own: only roots are listed.
	for _, f := range view.Forests {
		if f.Root.Number == 71 {
			t.Error("the tracker stands as a forest of its own")
		}
	}
	// The finished forest stands in the past list, not the current one:
	// both tickets closed, named by its root and tracker, and stating
	// its warnings as not walked rather than implying a finished forest
	// waits on no one (§forgejo/observability/the-outcomes-view).
	if len(view.PastForests) != 1 {
		t.Fatalf("past_forests = %+v, want the one finished forest", view.PastForests)
	}
	past := view.PastForests[0]
	if past.Root.Number != 53 || past.Root.State != "closed" {
		t.Errorf("past root = %+v, want closed ideation issue 53", past.Root)
	}
	if past.Tracker == nil || past.Tracker.Number != 73 || past.Tracker.State != "closed" {
		t.Errorf("past tracker = %+v, want closed outcome issue 73", past.Tracker)
	}
	if past.Warnings == nil || len(past.Warnings) != 0 || past.WarningsWalked {
		t.Errorf("past warnings = %+v (walked: %v), want the empty stated as not walked", past.Warnings, past.WarningsWalked)
	}
}

// TestForgejoOutcomesEndpoint_Disabled: with no engine and no cause in
// hand, the endpoint answers an empty list — never null — alongside the
// not-configured engine status (§forgejo/observability/the-outcomes-view).
func TestForgejoOutcomesEndpoint_Disabled(t *testing.T) {
	srv := New(0, nil, "test", nil, forgejo.EngineStateWord(false, nil))
	rec := getOutcomes(t, srv)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"forests":[]`) {
		t.Errorf("body %s, want an empty forests list, never null", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"past_forests":[]`) {
		t.Errorf("body %s, want an empty past_forests list, never null", rec.Body.String())
	}
	var view outcomesViewJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if view.EngineStatus != forgejo.CategoryNotConfigured.String() {
		t.Errorf("engine_status = %q, want %q", view.EngineStatus, forgejo.CategoryNotConfigured.String())
	}
	if len(view.Forests) != 0 || len(view.PastForests) != 0 {
		t.Errorf("forests = %d, past = %d, want neither list populated without an engine", len(view.Forests), len(view.PastForests))
	}
}

// TestForgejoOutcomesEndpoint_ReadFailure: a sweep whose forest
// enumeration fails takes the view down with it — 500, not an empty
// list a caller could read as "no forests", and not the earlier set
// served as if fresh — and the 500 carries a fixed sentence, not the
// read's raw error text: this port authenticates nothing, and the raw
// text names repositories and forge response bodies a stranger should
// not read (§forgejo/observability/the-outcomes-view,
// §forgejo/observability/why-the-engine-is-off).
func TestForgejoOutcomesEndpoint_ReadFailure(t *testing.T) {
	api := outcomesStubAPI()
	fjEng := newForgejoEngine(t, api)
	fjEng.Reconcile() // a set the view stood on, until the reads broke
	api.issuesErr = fmt.Errorf("issue listing down")
	fjEng.Reconcile() // the sweep's enumeration fails; the view says so
	srv := New(0, nil, "test", fjEng, forgejo.EngineStateWord(true, nil))
	rec := getOutcomes(t, srv)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if body := rec.Body.String(); strings.Contains(body, "issue listing down") || strings.Contains(body, "o/out") || !strings.Contains(body, "outcomes read failed") {
		t.Errorf("body %q, want the fixed sentence and no raw forge error text", body)
	}
}

// TestForgejoOutcomesEndpoint_NotYetEnumerated: an engine whose first
// sweep has not yet recorded a forest set answers 500, not an empty
// list — the honest empty is reserved for a view that read fine and
// found nothing (§forgejo/observability/the-outcomes-view).
func TestForgejoOutcomesEndpoint_NotYetEnumerated(t *testing.T) {
	fjEng := newForgejoEngine(t, outcomesStubAPI())
	srv := New(0, nil, "test", fjEng, forgejo.EngineStateWord(true, nil))
	rec := getOutcomes(t, srv)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "outcomes read failed") {
		t.Errorf("body %q, want the fixed sentence", rec.Body.String())
	}
}

// TestForgejoOutcomesEndpoint_Method: only GET is allowed.
func TestForgejoOutcomesEndpoint_Method(t *testing.T) {
	srv := New(0, nil, "test", nil, forgejo.EngineStateWord(false, nil))
	req := httptest.NewRequest(http.MethodPost, "/api/forgejo/outcomes", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

// heldBackItemJSON mirrors one held-back row: the warning's reference,
// kind and age alongside the labels the waited-on object carries
// (§forgejo/observability/the-held-back-view). No description field is
// mirrored because the row carries none — the test below pins that.
type heldBackItemJSON struct {
	Kind       string   `json:"kind"`
	Repository string   `json:"repository"`
	Number     int      `json:"number"`
	Title      string   `json:"title"`
	HTMLURL    string   `json:"html_url"`
	AgeSeconds float64  `json:"age_seconds"`
	Labels     []string `json:"labels"`
}

// heldBackViewJSON mirrors the /api/forgejo/held-back response shape:
// the two lists, spelled the same way, and the one engine-status
// field (§forgejo/observability/the-held-back-view).
type heldBackViewJSON struct {
	EngineStatus      string             `json:"engine_status"`
	HeldByHumans      []heldBackItemJSON `json:"held_by_humans"`
	StalledDeliveries []heldBackItemJSON `json:"stalled_deliveries"`
}

func getHeldBack(t *testing.T, srv *Server) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/forgejo/held-back", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// TestForgejoHeldBackEndpoint: the endpoint serves the waits the sweep
// tick recorded onto the forest snapshot — the human task the forest
// waits on, with its labels alongside the reference, kind and age the
// warning already carries — beside an honest empty stalled list and the
// engine-status field; serving the request reads no forge and discloses
// no object body (§forgejo/observability/the-held-back-view).
func TestForgejoHeldBackEndpoint(t *testing.T) {
	api := outcomesStubAPI()
	fjEng := newForgejoEngine(t, api)
	fjEng.Reconcile() // the sweep tick records the set the view serves
	srv := New(0, nil, "test", fjEng, forgejo.EngineStateWord(true, nil))
	rec := getHeldBack(t, srv)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var view heldBackViewJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if view.EngineStatus != forgejo.EngineUpWord {
		t.Errorf("engine_status = %q, want %q", view.EngineStatus, forgejo.EngineUpWord)
	}
	if len(view.HeldByHumans) != 1 {
		t.Fatalf("held_by_humans = %+v, want the one human task the walk found", view.HeldByHumans)
	}
	h := view.HeldByHumans[0]
	if h.Kind != "human_task_blocking" {
		t.Errorf("kind = %q, want human_task_blocking", h.Kind)
	}
	if h.Repository != "o/r" || h.Number != 90 || h.Title != "hand the keys over" || h.HTMLURL == "" {
		t.Errorf("row = %+v, want the member o/r#90 with its fields", h)
	}
	if len(h.Labels) != 1 || h.Labels[0] != "human-task" {
		t.Errorf("labels = %v, want the labels the object carries", h.Labels)
	}
	if h.AgeSeconds <= 0 {
		t.Errorf("age_seconds = %v, want the age of the wait", h.AgeSeconds)
	}
	// The wire carries no body of anything: the dashboard port asks no
	// credential, so author prose is not this view's to disclose — the
	// row's link is how a reader reaches it
	// (§forgejo/observability/the-held-back-view).
	if strings.Contains(rec.Body.String(), "description") {
		t.Errorf("body %s, want no description field anywhere", rec.Body.String())
	}
	// The stalled half reads as the empty list it is, never null.
	if view.StalledDeliveries == nil || len(view.StalledDeliveries) != 0 {
		t.Errorf("stalled_deliveries = %+v, want the empty list", view.StalledDeliveries)
	}
	if !strings.Contains(rec.Body.String(), `"stalled_deliveries":[]`) {
		t.Errorf("body %s, want the empty stalled list spelled as a list", rec.Body.String())
	}
}

// TestForgejoHeldBackEndpoint_States: the held-back view keeps the
// outcomes view's shape discipline — an engineless port answers two
// empty lists beside the not-configured word, a view that has not
// been enumerated yet answers 500 with the fixed sentence rather
// than an empty list, and a method other than GET answers 405
// (§forgejo/observability/the-held-back-view,
// §forgejo/observability/the-outcomes-view).
func TestForgejoHeldBackEndpoint_States(t *testing.T) {
	srv := New(0, nil, "test", nil, forgejo.EngineStateWord(false, nil))
	rec := getHeldBack(t, srv)
	if rec.Code != http.StatusOK {
		t.Fatalf("disabled: status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"held_by_humans":[]`) || !strings.Contains(rec.Body.String(), `"stalled_deliveries":[]`) {
		t.Errorf("body %s, want two empty lists, never null", rec.Body.String())
	}
	var view heldBackViewJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if view.EngineStatus != forgejo.CategoryNotConfigured.String() {
		t.Errorf("engine_status = %q, want %q", view.EngineStatus, forgejo.CategoryNotConfigured.String())
	}

	notYet := New(0, nil, "test", newForgejoEngine(t, outcomesStubAPI()), forgejo.EngineStateWord(true, nil))
	rec = getHeldBack(t, notYet)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("not yet enumerated: status = %d, want 500", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "held-back read failed") || strings.Contains(body, "o/out") {
		t.Errorf("body %q, want the fixed sentence and no raw detail", body)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/forgejo/held-back", nil)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

// TestForgejoOutcomesEndpoint_RecordedEmpty: an engine whose sweep
// enumerated no forests answers 200 with an empty list. This is the
// honest empty the view reserves for a read that succeeded and found
// nothing, which the no-engine case cannot stand in for because that
// one holds no engine at all (§forgejo/observability/the-outcomes-view).
func TestForgejoOutcomesEndpoint_RecordedEmpty(t *testing.T) {
	api := outcomesStubAPI()
	api.openIssues["out"] = nil
	api.closedIssues["out"] = nil
	fjEng := newForgejoEngine(t, api)
	fjEng.Reconcile() // the sweep tick records an empty set
	srv := New(0, nil, "test", fjEng, forgejo.EngineStateWord(true, nil))
	rec := getOutcomes(t, srv)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"forests":[]`) {
		t.Errorf("body %s, want an empty forests list, never null", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"past_forests":[]`) {
		t.Errorf("body %s, want an empty past_forests list, never null", rec.Body.String())
	}
}

// vendoredMermaidSHA256 is the digest of the runtime this tree
// carries: npm's released dist/mermaid.min.js for mermaid@12.3.0, the
// artifact the specification records alongside the file
// (§forgejo/observability/the-outcomes-view).
const vendoredMermaidSHA256 = "336df803d87a38b70a01e736e666c8f7601d2ce42f5e47c8fafe5d721074b9a3"

// TestVendoredMermaidRuntimeIsServed: the page's vendored runtime is
// served as JavaScript, and it is the artifact the specification names
// (§forgejo/observability/the-outcomes-view, s-git/maitred#191). The
// serve half earns its keep because the SPA fallback answers any
// unknown path with index.html at 200: this is the browser-free signal
// that the runtime never landed in the embed, where every chart would
// otherwise quietly fall back to text. The digest half is what makes
// the byte-identity promise a fact rather than a sentence — the file
// is 5.5 MB no one reads, so a stray re-save, an edit in the wrong
// window or a text filter must fail here.
func TestVendoredMermaidRuntimeIsServed(t *testing.T) {
	data, err := staticFS.ReadFile("static/mermaid.min.js")
	if err != nil {
		t.Fatalf("static/mermaid.min.js: %v", err)
	}
	if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != vendoredMermaidSHA256 {
		t.Errorf("embedded runtime is sha256 %s (%d bytes), want %s: the specification promises the file is upstream's released mermaid@12.3.0 dist/mermaid.min.js, byte for byte",
			hex.EncodeToString(sum[:]), len(data), vendoredMermaidSHA256)
	}

	srv := New(0, nil, "test", nil, forgejo.EngineStateWord(false, nil))
	req := httptest.NewRequest(http.MethodGet, "/mermaid.min.js", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/javascript") {
		t.Errorf("Content-Type = %q, want JavaScript: the SPA fallback answering HTML here would leave the page without a runtime", got)
	}
	if rec.Body.Len() != len(data) {
		t.Errorf("served %d bytes, want the embedded %d", rec.Body.Len(), len(data))
	}
}

// TestResponsesCarrySecurityHeaders: the fence against every
// origin but the server's rides on every response the port gives,
// not on the renderer alone and not on the file being served
// (§forgejo/observability/the-outcomes-view). It is worth a test
// because a CSP fails silently: a broken policy is dropped, and the
// page still works — unguarded — in every browser.
func TestResponsesCarrySecurityHeaders(t *testing.T) {
	srv := New(0, nil, "test", nil, forgejo.EngineStateWord(false, nil))
	for _, path := range []string{"/", "/outcomes", "/mermaid.min.js", "/api/version"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: status = %d, want 200", path, rec.Code)
		}
		csp := rec.Header().Get("Content-Security-Policy")
		for _, want := range []string{"default-src 'self'", "img-src 'self' data:", "object-src 'none'", "base-uri 'none'", "form-action 'none'"} {
			if !strings.Contains(csp, want) {
				t.Errorf("GET %s: CSP %q lacks %s", path, csp, want)
			}
		}
		if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("GET %s: X-Content-Type-Options = %q, want nosniff", path, got)
		}
	}
}
