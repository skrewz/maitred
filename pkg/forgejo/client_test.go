package forgejo

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Client must satisfy the API interface the engine depends on.
var _ API = (*Client)(nil)

// recordedRequest captures the fields of the most recent request seen by a
// test server.
type recordedRequest struct {
	method string
	path   string
	query  url.Values
	auth   string
}

// newTestServer starts an httptest server that records each request and
// serves the given handler.
func newTestServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *recordedRequest) {
	t.Helper()
	rec := &recordedRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.method = r.Method
		rec.path = r.URL.Path
		rec.query = r.URL.Query()
		rec.auth = r.Header.Get("Authorization")
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

// issueJSON returns a minimal Forgejo issue JSON object.
func issueJSON(number int, repoFullName string) map[string]any {
	return map[string]any{
		"number":     number,
		"title":      "issue " + strconv.Itoa(number),
		"state":      "open",
		"labels":     []map[string]any{{"name": "agentic-auto-merge"}},
		"updated_at": "2026-09-16T13:25:18Z",
		"html_url":   "https://forge.example.com/" + repoFullName + "/issues/" + strconv.Itoa(number),
		"repository": map[string]any{"full_name": repoFullName},
	}
}

func TestNew_TrimsTrailingSlash(t *testing.T) {
	srv, rec := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	})
	c := New(srv.URL+"/", "test-token", nil)
	if _, err := c.ListOpenIssues("acme", "maitred"); err != nil {
		t.Fatalf("ListOpenIssues: %v", err)
	}
	if rec.path != "/api/v1/repos/acme/maitred/issues" {
		t.Errorf("path = %q, want /api/v1/repos/acme/maitred/issues", rec.path)
	}
}

func TestListOrgRepositories(t *testing.T) {
	srv, rec := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{
				"id": 1, "name": "maitred", "full_name": "acme/maitred",
				"owner":    map[string]any{"login": "acme"},
				"html_url": "https://forge.example.com/acme/maitred",
				"topics":   []string{"maitred-enabled", "engine"},
			},
			{
				"id": 2, "name": "other", "full_name": "acme/other",
				"owner":    map[string]any{"login": "acme"},
				"html_url": "https://forge.example.com/acme/other",
				"topics":   []string{},
			},
		})
	})

	c := New(srv.URL, "test-token", nil)
	repos, err := c.ListOrgRepositories("acme")
	if err != nil {
		t.Fatalf("ListOrgRepositories: %v", err)
	}
	if rec.path != "/api/v1/orgs/acme/repos" {
		t.Errorf("path = %q, want /api/v1/orgs/acme/repos", rec.path)
	}
	if rec.auth != "Bearer test-token" {
		t.Errorf("Authorization = %q, want Bearer test-token", rec.auth)
	}
	if len(repos) != 2 {
		t.Fatalf("got %d repos, want 2", len(repos))
	}
	want := Repository{
		ID: 1, Name: "maitred", FullName: "acme/maitred", Owner: "acme",
		HTMLURL: "https://forge.example.com/acme/maitred",
		Topics:  []string{"maitred-enabled", "engine"},
	}
	if !reflect.DeepEqual(repos[0], want) {
		t.Errorf("repos[0] = %+v, want %+v", repos[0], want)
	}
}

func TestListOrgRepositories_Pagination(t *testing.T) {
	srv, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if r.URL.Query().Get("limit") != "50" {
			t.Errorf("limit = %q, want 50", r.URL.Query().Get("limit"))
		}
		n := 50
		if page == 3 {
			n = 10
		}
		repos := make([]map[string]any, n)
		for i := range repos {
			repos[i] = map[string]any{
				"name": "r" + strconv.Itoa(i), "full_name": "acme/r" + strconv.Itoa(i),
				"owner": map[string]any{"login": "acme"},
			}
		}
		_ = json.NewEncoder(w).Encode(repos)
	})

	c := New(srv.URL, "test-token", nil)
	repos, err := c.ListOrgRepositories("acme")
	if err != nil {
		t.Fatalf("ListOrgRepositories: %v", err)
	}
	if len(repos) != 110 {
		t.Fatalf("got %d repos, want 110 (3 pages)", len(repos))
	}
}

func TestListOpenIssues(t *testing.T) {
	srv, rec := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			issueJSON(48, "acme/maitred"),
		})
	})

	c := New(srv.URL, "test-token", nil)
	issues, err := c.ListOpenIssues("acme", "maitred")
	if err != nil {
		t.Fatalf("ListOpenIssues: %v", err)
	}
	if rec.path != "/api/v1/repos/acme/maitred/issues" {
		t.Errorf("path = %q, want /api/v1/repos/acme/maitred/issues", rec.path)
	}
	if got := rec.query.Get("state"); got != "open" {
		t.Errorf("state = %q, want open", got)
	}
	if len(issues) != 1 {
		t.Fatalf("got %d issues, want 1", len(issues))
	}
	want := Issue{
		Number:     48,
		Title:      "issue 48",
		State:      "open",
		Labels:     []string{"agentic-auto-merge"},
		UpdatedAt:  time.Date(2026, 9, 16, 13, 25, 18, 0, time.UTC),
		HTMLURL:    "https://forge.example.com/acme/maitred/issues/48",
		Repository: "acme/maitred",
	}
	if !reflect.DeepEqual(issues[0], want) {
		t.Errorf("issues[0] = %+v, want %+v", issues[0], want)
	}
}

func TestListOpenPullRequests(t *testing.T) {
	srv, rec := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{
				"number": 58, "title": "a PR", "state": "open",
				"merged": false, "mergeable": true,
				"head":       map[string]any{"sha": "abc123"},
				"labels":     []map[string]any{{"name": "ready"}},
				"updated_at": "2026-09-18T02:56:38Z",
				"html_url":   "https://forge.example.com/acme/maitred/pulls/58",
			},
		})
	})

	c := New(srv.URL, "test-token", nil)
	pulls, err := c.ListOpenPullRequests("acme", "maitred")
	if err != nil {
		t.Fatalf("ListOpenPullRequests: %v", err)
	}
	if rec.path != "/api/v1/repos/acme/maitred/pulls" {
		t.Errorf("path = %q, want /api/v1/repos/acme/maitred/pulls", rec.path)
	}
	if got := rec.query.Get("state"); got != "open" {
		t.Errorf("state = %q, want open", got)
	}
	if len(pulls) != 1 {
		t.Fatalf("got %d pulls, want 1", len(pulls))
	}
	if pulls[0].HeadSHA != "abc123" {
		t.Errorf("HeadSHA = %q, want abc123", pulls[0].HeadSHA)
	}
	if pulls[0].Labels[0] != "ready" {
		t.Errorf("Labels = %v, want [ready]", pulls[0].Labels)
	}
}

func TestGetIssue(t *testing.T) {
	srv, rec := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(issueJSON(48, "acme/maitred"))
	})

	c := New(srv.URL, "test-token", nil)
	issue, err := c.GetIssue("acme", "maitred", 48)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if rec.path != "/api/v1/repos/acme/maitred/issues/48" {
		t.Errorf("path = %q, want /api/v1/repos/acme/maitred/issues/48", rec.path)
	}
	if issue.Number != 48 || issue.State != "open" || issue.Repository != "acme/maitred" {
		t.Errorf("issue = %+v, want number 48, state open, repo acme/maitred", *issue)
	}
}

func TestGetIssue_IsPull(t *testing.T) {
	srv, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		issue := issueJSON(48, "acme/maitred")
		issue["is_pull"] = true
		_ = json.NewEncoder(w).Encode(issue)
	})

	c := New(srv.URL, "test-token", nil)
	issue, err := c.GetIssue("acme", "maitred", 48)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if !issue.IsPull {
		t.Errorf("IsPull = false, want true (is_pull in the payload)")
	}
}

func TestGetIssue_NotPull(t *testing.T) {
	srv, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(issueJSON(48, "acme/maitred"))
	})

	c := New(srv.URL, "test-token", nil)
	issue, err := c.GetIssue("acme", "maitred", 48)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if issue.IsPull {
		t.Errorf("IsPull = true, want false (no is_pull in the payload)")
	}
}

func TestGetIssue_NotFound(t *testing.T) {
	srv, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Issue not found","url":"https://forge.example.com/api/swagger"}`))
	})

	c := New(srv.URL, "test-token", nil)
	_, err := c.GetIssue("acme", "maitred", 999)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("want *APIError, got %T: %v", err, err)
	}
	if apiErr.StatusCode != http.StatusNotFound {
		t.Errorf("StatusCode = %d, want 404", apiErr.StatusCode)
	}
	if !strings.Contains(apiErr.Error(), "Issue not found") {
		t.Errorf("error = %q, want it to contain the API message", apiErr.Error())
	}
}

func TestGetPullRequest(t *testing.T) {
	srv, rec := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"number": 58, "title": "a PR", "state": "closed",
			"merged": true, "mergeable": true,
			"head":       map[string]any{"sha": "4db2ef6"},
			"updated_at": "2026-09-18T02:56:38Z",
			"html_url":   "https://forge.example.com/acme/maitred/pulls/58",
		})
	})

	c := New(srv.URL, "test-token", nil)
	pr, err := c.GetPullRequest("acme", "maitred", 58)
	if err != nil {
		t.Fatalf("GetPullRequest: %v", err)
	}
	if rec.path != "/api/v1/repos/acme/maitred/pulls/58" {
		t.Errorf("path = %q, want /api/v1/repos/acme/maitred/pulls/58", rec.path)
	}
	if !pr.Merged || !pr.Mergeable || pr.State != "closed" || pr.HeadSHA != "4db2ef6" {
		t.Errorf("pr = %+v, want merged, mergeable, closed, head 4db2ef6", *pr)
	}
}

func TestIssueBlocks_CrossRepo(t *testing.T) {
	srv, rec := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			issueJSON(49, "acme/maitred"),
			issueJSON(7, "acme/other-repo"),
		})
	})

	c := New(srv.URL, "test-token", nil)
	blocked, err := c.IssueBlocks("acme", "maitred", 48)
	if err != nil {
		t.Fatalf("IssueBlocks: %v", err)
	}
	if rec.path != "/api/v1/repos/acme/maitred/issues/48/blocks" {
		t.Errorf("path = %q, want /api/v1/repos/acme/maitred/issues/48/blocks", rec.path)
	}
	if len(blocked) != 2 {
		t.Fatalf("got %d blocked issues, want 2", len(blocked))
	}
	if blocked[1].Repository != "acme/other-repo" {
		t.Errorf("blocked[1].Repository = %q, want acme/other-repo (cross-repo)", blocked[1].Repository)
	}
}

func TestIssueDependencies(t *testing.T) {
	srv, rec := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			issueJSON(47, "acme/maitred"),
		})
	})

	c := New(srv.URL, "test-token", nil)
	deps, err := c.IssueDependencies("acme", "maitred", 48)
	if err != nil {
		t.Fatalf("IssueDependencies: %v", err)
	}
	if rec.path != "/api/v1/repos/acme/maitred/issues/48/dependencies" {
		t.Errorf("path = %q, want /api/v1/repos/acme/maitred/issues/48/dependencies", rec.path)
	}
	if len(deps) != 1 || deps[0].Number != 47 {
		t.Errorf("deps = %+v, want one issue #47", deps)
	}
}

func TestListPullRequestReviews(t *testing.T) {
	srv, rec := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{
				"state":        "APPROVED",
				"user":         map[string]any{"login": "s-reviewer"},
				"commit_id":    "91161c7",
				"submitted_at": "2026-09-17T23:51:30Z",
			},
			{
				"state":        "CHANGES_REQUESTED",
				"user":         map[string]any{"login": "s-reviewer"},
				"commit_id":    "1f5401a",
				"submitted_at": "2026-09-17T20:00:00Z",
			},
		})
	})

	c := New(srv.URL, "test-token", nil)
	reviews, err := c.ListPullRequestReviews("acme", "maitred", 58)
	if err != nil {
		t.Fatalf("ListPullRequestReviews: %v", err)
	}
	if rec.path != "/api/v1/repos/acme/maitred/pulls/58/reviews" {
		t.Errorf("path = %q, want /api/v1/repos/acme/maitred/pulls/58/reviews", rec.path)
	}
	if len(reviews) != 2 {
		t.Fatalf("got %d reviews, want 2", len(reviews))
	}
	want := Review{
		Event:       "APPROVED",
		Author:      "s-reviewer",
		CommitID:    "91161c7",
		SubmittedAt: time.Date(2026, 9, 17, 23, 51, 30, 0, time.UTC),
	}
	if reviews[0] != want {
		t.Errorf("reviews[0] = %+v, want %+v", reviews[0], want)
	}
}

func TestServerError(t *testing.T) {
	srv, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"boom"}`))
	})

	c := New(srv.URL, "test-token", nil)
	_, err := c.ListOpenIssues("acme", "maitred")
	if err == nil {
		t.Fatal("want error, got nil")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("want *APIError, got %T: %v", err, err)
	}
	if apiErr.StatusCode != http.StatusInternalServerError || apiErr.Message != "boom" {
		t.Errorf("apiErr = %+v, want 500 boom", apiErr)
	}
}

func TestError_NonJSONBody(t *testing.T) {
	srv, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`<html>bad gateway</html>`))
	})

	c := New(srv.URL, "test-token", nil)
	_, err := c.GetIssue("acme", "maitred", 1)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("want *APIError, got %T: %v", err, err)
	}
	if apiErr.StatusCode != http.StatusBadGateway {
		t.Errorf("StatusCode = %d, want 502", apiErr.StatusCode)
	}
	if !strings.Contains(apiErr.Error(), "bad gateway") {
		t.Errorf("error = %q, want it to contain the raw body", apiErr.Error())
	}
}

func TestAuthHeader_AllOperations(t *testing.T) {
	ops := []func(c *Client) error{
		func(c *Client) error { _, err := c.ListOrgRepositories("acme"); return err },
		func(c *Client) error { _, err := c.ListOpenIssues("acme", "maitred"); return err },
		func(c *Client) error { _, err := c.ListOpenPullRequests("acme", "maitred"); return err },
		func(c *Client) error { _, err := c.GetIssue("acme", "maitred", 1); return err },
		func(c *Client) error { _, err := c.GetPullRequest("acme", "maitred", 1); return err },
		func(c *Client) error { _, err := c.IssueBlocks("acme", "maitred", 1); return err },
		func(c *Client) error { _, err := c.IssueDependencies("acme", "maitred", 1); return err },
		func(c *Client) error { _, err := c.ListPullRequestReviews("acme", "maitred", 1); return err },
	}
	for i, op := range ops {
		srv, rec := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
			// Single-object endpoints (issues/N, pulls/N) take an object;
			// list endpoints take an array.
			if strings.HasSuffix(r.URL.Path, "/blocks") ||
				strings.HasSuffix(r.URL.Path, "/dependencies") ||
				strings.HasSuffix(r.URL.Path, "/reviews") ||
				strings.HasSuffix(r.URL.Path, "/repos") ||
				strings.HasSuffix(r.URL.Path, "/issues") ||
				strings.HasSuffix(r.URL.Path, "/pulls") {
				_, _ = w.Write([]byte(`[]`))
				return
			}
			_, _ = w.Write([]byte(`{}`))
		})
		c := New(srv.URL, "secret-token", nil)
		if err := op(c); err != nil {
			t.Fatalf("op %d: %v", i, err)
		}
		if rec.auth != "Bearer secret-token" {
			t.Errorf("op %d: Authorization = %q, want Bearer secret-token", i, rec.auth)
		}
		if rec.method != http.MethodGet {
			t.Errorf("op %d: method = %q, want GET (read-only client)", i, rec.method)
		}
	}
}

func TestAllOperations_SurfaceAPIError(t *testing.T) {
	srv, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"message":"unavailable"}`))
	})
	c := New(srv.URL, "test-token", nil)

	ops := map[string]func() error{
		"ListOrgRepositories":    func() error { _, err := c.ListOrgRepositories("acme"); return err },
		"ListOpenIssues":         func() error { _, err := c.ListOpenIssues("acme", "maitred"); return err },
		"ListOpenPullRequests":   func() error { _, err := c.ListOpenPullRequests("acme", "maitred"); return err },
		"GetIssue":               func() error { _, err := c.GetIssue("acme", "maitred", 1); return err },
		"GetPullRequest":         func() error { _, err := c.GetPullRequest("acme", "maitred", 1); return err },
		"IssueBlocks":            func() error { _, err := c.IssueBlocks("acme", "maitred", 1); return err },
		"IssueDependencies":      func() error { _, err := c.IssueDependencies("acme", "maitred", 1); return err },
		"ListPullRequestReviews": func() error { _, err := c.ListPullRequestReviews("acme", "maitred", 1); return err },
	}
	for name, op := range ops {
		err := op()
		if err == nil {
			t.Errorf("%s: want error, got nil", name)
			continue
		}
		apiErr, ok := err.(*APIError)
		if !ok {
			t.Errorf("%s: want *APIError, got %T: %v", name, err, err)
			continue
		}
		if apiErr.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s: StatusCode = %d, want 503", name, apiErr.StatusCode)
		}
	}
}

func TestListOpenIssues_Empty(t *testing.T) {
	srv, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	})

	c := New(srv.URL, "test-token", nil)
	issues, err := c.ListOpenIssues("acme", "maitred")
	if err != nil {
		t.Fatalf("ListOpenIssues: %v", err)
	}
	if issues == nil || len(issues) != 0 {
		t.Errorf("issues = %v, want empty non-nil slice", issues)
	}
}

func TestIssue_NoRepositoryField(t *testing.T) {
	// Single-issue responses for the list endpoints omit nothing, but be
	// defensive: a missing repository object must not panic.
	srv, _ := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"number": 1, "title": "t", "state": "open",
			"updated_at": "2026-09-16T13:25:18Z",
		})
	})

	c := New(srv.URL, "test-token", nil)
	issue, err := c.GetIssue("acme", "maitred", 1)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if issue.Repository != "" {
		t.Errorf("Repository = %q, want empty", issue.Repository)
	}
	if !reflect.DeepEqual(issue.Labels, []string(nil)) {
		t.Errorf("Labels = %v, want nil", issue.Labels)
	}
}
