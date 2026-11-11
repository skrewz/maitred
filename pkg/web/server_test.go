package web

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
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

	// reposCounts lists how often the stub has been asked for the
	// org's repositories, so a test can see a manually fired sweep
	// run (§forgejo/observability/triggering-a-sweep).
	reposCounts atomic.Int32
}

// The stub's organisation holds the one maitred-enabled repository the
// tests dispatch in — the event path's scope test consults the listing
// (§forgejo/webhook/the-scope-cache).
func (s *stubForgejoAPI) ListOrgRepositories(string) ([]forgejo.Repository, error) {
	s.reposCounts.Add(1)
	return []forgejo.Repository{{
		Name:     "r",
		FullName: "o/r",
		Owner:    "o",
		Topics:   []string{"maitred-enabled"},
	}}, nil
}

func (s *stubForgejoAPI) ListOpenIssues(string, string) ([]forgejo.Issue, error) {
	return nil, nil
}

func (s *stubForgejoAPI) ListOpenPullRequests(string, string) ([]forgejo.PullRequest, error) {
	return nil, nil
}

func (s *stubForgejoAPI) GetIssue(string, string, int) (*forgejo.Issue, error) {
	return s.issue, nil
}

func (s *stubForgejoAPI) GetPullRequest(string, string, int) (*forgejo.PullRequest, error) {
	return nil, nil
}

func (s *stubForgejoAPI) IssueBlocks(string, string, int) ([]forgejo.Issue, error) {
	return nil, nil
}

func (s *stubForgejoAPI) IssueDependencies(string, string, int) ([]forgejo.Issue, error) {
	return nil, nil
}

func (s *stubForgejoAPI) ListPullRequestReviews(string, string, int) ([]forgejo.Review, error) {
	return nil, nil
}

// testForgejoEngine builds an engine that has made one dispatching
// decision, alongside the API stub it reads through
// (§forgejo/observability/the-dashboard-view).
func testForgejoEngine(t *testing.T) (*forgejo.Engine, *stubForgejoAPI) {
	t.Helper()
	store, err := forgejo.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	cfg := &forgejo.Config{Org: "o", ReconcileInterval: time.Hour}
	cfg.Actions = make(map[string]forgejo.PromptConfig, len(forgejo.AllActions))
	for _, a := range forgejo.AllActions {
		cfg.Actions[string(a)] = forgejo.PromptConfig{
			Prompt:  string(a),
			Persona: "test",
			Timeout: time.Hour,
		}
	}
	api := &stubForgejoAPI{issue: &forgejo.Issue{
		Number:     7,
		State:      "open",
		UpdatedAt:  time.Date(2026, 9, 16, 13, 25, 18, 0, time.UTC),
		Repository: "o/r",
	}}
	eng := forgejo.NewEngine(
		api,
		store, cfg, queue.NewTaskQueue(),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	// Stop waits the in-flight sweep out whatever a test started, so
	// no sweep goroutine outlives the temporary directories above.
	t.Cleanup(eng.Stop)
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
// carries the sweep catalog — the reconciliation sweep, ided, titled,
// and described — and with no engine the catalog is empty
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
		if s.ID == forgejo.SweepIDReconcile && s.Title != "" && s.Description != "" {
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
