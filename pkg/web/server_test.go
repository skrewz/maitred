package web

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"maitred/pkg/forgejo"
	"maitred/pkg/queue"
)

// stubForgejoAPI is a minimal forgejo.API for the web server tests
// (§forgejo/observability/the-dashboard-view).
type stubForgejoAPI struct {
	issue *forgejo.Issue
}

func (s *stubForgejoAPI) ListOrgRepositories(string) ([]forgejo.Repository, error) {
	return nil, nil
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

// testForgejoEngine builds an engine that has made one dispatching decision
// (§forgejo/observability/the-dashboard-view).
func testForgejoEngine(t *testing.T) *forgejo.Engine {
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
	eng := forgejo.NewEngine(
		&stubForgejoAPI{issue: &forgejo.Issue{
			Number:     7,
			State:      "open",
			UpdatedAt:  time.Date(2026, 9, 16, 13, 25, 18, 0, time.UTC),
			Repository: "o/r",
		}},
		store, cfg, queue.NewTaskQueue(),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	eng.HandleEvent(forgejo.Event{Type: forgejo.EventIssueOpened, Repo: "o/r", Kind: forgejo.KindIssue, Number: 7, Sender: "alice"})
	return eng
}

// trackedJSON mirrors the /api/forgejo response shape
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
	srv := New(0, nil, "test", testForgejoEngine(t))
	rec := getForgejo(t, srv)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var tracked []trackedJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &tracked); err != nil {
		t.Fatalf("decode: %v", err)
	}
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

// TestForgejoEndpoint_Disabled: without the engine the endpoint serves an
// empty view (§forgejo/observability/the-dashboard-view).
func TestForgejoEndpoint_Disabled(t *testing.T) {
	srv := New(0, nil, "test", nil)
	rec := getForgejo(t, srv)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var tracked []trackedJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &tracked); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(tracked) != 0 {
		t.Errorf("tracked has %d entries, want 0", len(tracked))
	}
}

// TestForgejoEndpoint_Method: only GET is allowed.
func TestForgejoEndpoint_Method(t *testing.T) {
	srv := New(0, nil, "test", nil)
	req := httptest.NewRequest(http.MethodPost, "/api/forgejo", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}
