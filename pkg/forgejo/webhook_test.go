package forgejo

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"maitred/pkg/queue"
)

// testConfig returns a config with a canned prompt for every action,
// rendering the placeholders so tests can assert on the rendered prompt.
func testConfig() *Config {
	cfg := &Config{Org: "o", ReconcileInterval: 15 * time.Minute, DecomposerIdentity: "decomposer"}
	cfg.Actions = make(map[string]PromptConfig, len(AllActions))
	for _, a := range AllActions {
		cfg.Actions[string(a)] = PromptConfig{
			Prompt:  "action={{.Action}} repo={{.Repo}} kind={{.Kind}} number={{.Number}} issue={{.IssueURL}} pr={{.PRURL}} sender={{.Sender}}",
			Persona: "persona-" + string(a),
			Timeout: 2 * time.Hour,
		}
	}
	cfg.Reviews = []ReviewLens{{
		// One lens, bob — the reviewing author of the fixtures'
		// reviews — so engine-level fixtures keep the standing the
		// retired single-reviewer rows gave them
		// (§forgejo/config/review-lenses).
		Identity: "bob",
		Prompt:   "action={{.Action}} repo={{.Repo}} kind={{.Kind}} number={{.Number}} issue={{.IssueURL}} pr={{.PRURL}} sender={{.Sender}}",
		Persona:  "persona-review",
		Timeout:  1 * time.Hour,
	}}
	return cfg
}

// testUpdatedAt is the fixture issues' activity revision.
var testUpdatedAt = time.Date(2026, 9, 16, 13, 25, 18, 0, time.UTC)

// testIssue returns a fixture issue in repo o/r.
func testIssue(number int, state string) *Issue {
	return &Issue{
		Number:     number,
		Title:      "issue " + strconv.Itoa(number),
		State:      state,
		Labels:     []string{LabelAgenticAutoMerge},
		UpdatedAt:  testUpdatedAt,
		HTMLURL:    "https://forge.example.com/o/r/issues/" + strconv.Itoa(number),
		Repository: "o/r",
	}
}

// testPR returns a fixture pull request in repo o/r. The engine's
// fixture PRs are agent-authored deliverables opted into automatic
// merging, exactly as testIssue's fixture issues are: the
// agentic-auto-merge label is the precondition every merge-or-wait
// path reads, so a fixture that stands at an approved head merges
// unless the test clears the label
// (§forgejo/decisions/merge-gate, s-git/maitred#179).
func testPR(number int, state string, mergeable bool) *PullRequest {
	return &PullRequest{
		Number:    number,
		Title:     "pr " + strconv.Itoa(number),
		State:     state,
		HeadSHA:   "sha-" + strconv.Itoa(number),
		Mergeable: mergeable,
		UpdatedAt: testUpdatedAt,
		Labels:    []string{LabelAgenticAutoMerge},
		HTMLURL:   "https://forge.example.com/o/r/pulls/" + strconv.Itoa(number),
	}
}

// authoredPR is testPR with the PR's author set — the field the
// fix-feedback ownership gate reads alongside the configured
// implementer identity (§forgejo/config/implementer-identity).
func authoredPR(number int, state string, mergeable bool, author string) *PullRequest {
	pr := testPR(number, state, mergeable)
	pr.Author = author
	return pr
}

// setImplementerIdentity declares the implementer identity on the
// engine's config, engaging the fix-feedback ownership gate
// (§forgejo/config/implementer-identity).
func setImplementerIdentity(cfg *Config) {
	cfg.ImplementerIdentity = testImplementerIdentity
}

// fakeAPI is a fake Forgejo API for the webhook tests
// (§forgejo/client/operations).
type fakeAPI struct {
	mu      sync.Mutex
	issues  map[int]*Issue
	pulls   map[int]*PullRequest
	reviews map[int][]Review
	blocks  map[int][]Issue
	deps    map[int][]Issue
	err     error
	calls   []string

	// Reconciliation fixtures, keyed by repository name
	// (§forgejo/reconciliation/the-sweep).
	repos         []Repository
	openIssues    map[string][]Issue
	openPulls     map[string][]PullRequest
	listIssuesErr map[string]error
	listPullsErr  map[string]error

	// listReposErr, when set, fails only ListOrgRepositories — so a
	// test can break the scope listing without breaking the object
	// re-fetches, and vice versa (§forgejo/webhook/the-scope-cache).
	listReposErr error

	// depsErr, when set, fails only IssueDependencies — so a test can
	// break the blockers re-fetch without breaking the object fetches
	// (§forgejo/webhook/re-fetch).
	depsErr error

	// issueBlocksErr, when set, fails only IssueBlocks — so a test can
	// break the connected-issue resolution without breaking the other
	// re-fetches (§forgejo/webhook/re-fetch).
	issueBlocksErr error

	// issueDepsErr, when set, fails only IssueDependencies — so a test
	// can break the blocker resolution of the connected issue alone
	// (§forgejo/webhook/re-fetch).
	issueDepsErr error

	// pullErr, when set, fails only GetPullRequest — so a test can
	// break the cascade's PR re-fetch without breaking the event's
	// reads (§forgejo/decisions/unblock-cascade).
	pullErr error

	// getIssueErr, when set, fails only GetIssue — so a test can break
	// the resolution of an issue the pull request's body auto-closes
	// without breaking the object re-fetches
	// (§forgejo/webhook/re-fetch, s-git/maitred#180).
	getIssueErr error

	// GetIssue blocking hooks for the concurrency tests: GetIssue
	// closes getIssueEntered (once) on entry, then waits on
	// blockGetIssue when it is set.
	blockGetIssue   chan struct{}
	getIssueEntered chan struct{}

	// ListOrgRepositories blocking hooks, mirroring the GetIssue ones,
	// for the scope-cache single-flight test.
	blockListRepos   chan struct{}
	listReposEntered chan struct{}
}

func newFakeAPI() *fakeAPI {
	return &fakeAPI{
		issues:        map[int]*Issue{},
		pulls:         map[int]*PullRequest{},
		reviews:       map[int][]Review{},
		blocks:        map[int][]Issue{},
		deps:          map[int][]Issue{},
		openIssues:    map[string][]Issue{},
		openPulls:     map[string][]PullRequest{},
		listIssuesErr: map[string]error{},
		listPullsErr:  map[string]error{},
		// The org's repositories default to the one the fixtures live
		// in, maitred-enabled, so the event path's scope test passes
		// unless a test restates the listing (§forgejo/webhook/the-scope-cache).
		repos: []Repository{testRepo("o/r")},
	}
}

func (f *fakeAPI) record(op string) {
	f.mu.Lock()
	f.calls = append(f.calls, op)
	f.mu.Unlock()
}

func (f *fakeAPI) callCount(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// withIssueURL fills the html_url a test left blank. The real API always
// carries one, and the engine refuses a dispatch whose prompt would name
// no object (§forgejo/config/canned-prompts), so the fakes answer the way
// Forgejo does.
func withIssueURL(owner, repo string, i Issue) Issue {
	if i.HTMLURL == "" {
		i.HTMLURL = "https://forge.example.com/" + owner + "/" + repo + "/issues/" + strconv.Itoa(i.Number)
	}
	return i
}

// withPullURL fills the html_url a test left blank on a pull request.
func withPullURL(owner, repo string, pr PullRequest) PullRequest {
	if pr.HTMLURL == "" {
		pr.HTMLURL = "https://forge.example.com/" + owner + "/" + repo + "/pulls/" + strconv.Itoa(pr.Number)
	}
	return pr
}

func (f *fakeAPI) GetIssue(owner, repo string, number int) (*Issue, error) {
	f.record("GetIssue:" + repo + "/" + strconv.Itoa(number))
	if f.getIssueEntered != nil {
		select {
		case <-f.getIssueEntered:
		default:
			close(f.getIssueEntered)
		}
	}
	if f.blockGetIssue != nil {
		<-f.blockGetIssue
	}
	if f.getIssueErr != nil {
		return nil, f.getIssueErr
	}
	if f.err != nil {
		return nil, f.err
	}
	issue, ok := f.issues[number]
	if !ok {
		return nil, &APIError{StatusCode: http.StatusNotFound, Message: "issue not found"}
	}
	filled := withIssueURL(owner, repo, *issue)
	return &filled, nil
}

func (f *fakeAPI) GetPullRequest(owner, repo string, number int) (*PullRequest, error) {
	f.record("GetPullRequest:" + repo + "/" + strconv.Itoa(number))
	if f.err != nil {
		return nil, f.err
	}
	if f.pullErr != nil {
		return nil, f.pullErr
	}
	pr, ok := f.pulls[number]
	if !ok {
		return nil, &APIError{StatusCode: http.StatusNotFound, Message: "pull request not found"}
	}
	filled := withPullURL(owner, repo, *pr)
	return &filled, nil
}

func (f *fakeAPI) ListOrgRepositories(org string) ([]Repository, error) {
	f.record("ListOrgRepositories:" + org)
	if f.listReposEntered != nil {
		select {
		case <-f.listReposEntered:
		default:
			close(f.listReposEntered)
		}
	}
	if f.blockListRepos != nil {
		<-f.blockListRepos
	}
	// Deliberately not consulting f.err: the global error models a
	// failing object re-fetch, which must leave the scope listing
	// healthy so scope-tested deliveries still reach refetch.
	if f.listReposErr != nil {
		return nil, f.listReposErr
	}
	return f.repos, nil
}

func (f *fakeAPI) ListOpenIssues(owner, repo string) ([]Issue, error) {
	f.record("ListOpenIssues:" + repo)
	if f.err != nil {
		return nil, f.err
	}
	if err, ok := f.listIssuesErr[repo]; ok {
		return nil, err
	}
	out := make([]Issue, 0, len(f.openIssues[repo]))
	for _, i := range f.openIssues[repo] {
		out = append(out, withIssueURL(owner, repo, i))
	}
	return out, nil
}

func (f *fakeAPI) ListOpenPullRequests(owner, repo string) ([]PullRequest, error) {
	f.record("ListOpenPullRequests:" + repo)
	if f.err != nil {
		return nil, f.err
	}
	if err, ok := f.listPullsErr[repo]; ok {
		return nil, err
	}
	out := make([]PullRequest, 0, len(f.openPulls[repo]))
	for _, pr := range f.openPulls[repo] {
		out = append(out, withPullURL(owner, repo, pr))
	}
	return out, nil
}

func (f *fakeAPI) ListPullRequestReviews(owner, repo string, number int) ([]Review, error) {
	f.record("ListPullRequestReviews:" + repo + "/" + strconv.Itoa(number))
	if f.err != nil {
		return nil, f.err
	}
	return f.reviews[number], nil
}

func (f *fakeAPI) IssueBlocks(owner, repo string, number int) ([]Issue, error) {
	f.record("IssueBlocks:" + repo + "/" + strconv.Itoa(number))
	if f.issueBlocksErr != nil {
		return nil, f.issueBlocksErr
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.blocks[number], nil
}

func (f *fakeAPI) IssueDependencies(owner, repo string, number int) ([]Issue, error) {
	f.record("IssueDependencies:" + repo + "/" + strconv.Itoa(number))
	if f.issueDepsErr != nil {
		return nil, f.issueDepsErr
	}
	if f.err != nil {
		return nil, f.err
	}
	if f.depsErr != nil {
		return nil, f.depsErr
	}
	return f.deps[number], nil
}

// fakeQueue is a fake queue provider for the webhook tests.
type fakeQueue struct {
	mu    sync.Mutex
	tasks []*queue.Task
	err   error
}

func (q *fakeQueue) AddTask(task *queue.Task) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.err != nil {
		return q.err
	}
	q.tasks = append(q.tasks, task)
	return nil
}

func (q *fakeQueue) all() []*queue.Task {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]*queue.Task, len(q.tasks))
	copy(out, q.tasks)
	return out
}

// newTestEngine builds an engine over a fresh fake API, queue, and store.
func newTestEngine(t *testing.T) (*Engine, *fakeAPI, *fakeQueue, *Store) {
	t.Helper()
	api := newFakeAPI()
	q := &fakeQueue{}
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	eng := NewEngine(api, store, testConfig(), q, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return eng, api, q, store
}

// sign computes the hex HMAC-SHA256 of body under secret, the format
// Forgejo sends in X-Forgejo-Signature.
func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// issuePayload builds a minimal all-events payload for an issue event.
func issuePayload(repo string, number int, action string) map[string]any {
	return map[string]any{
		"action":     action,
		"repository": map[string]any{"full_name": repo},
		"issue":      map[string]any{"number": number},
		"sender":     map[string]any{"login": "alice"},
	}
}

// prPayload builds a minimal all-events payload for a pull request
// event, sent by carol (neither the PR author nor the reviewing author
// bob).
func prPayload(repo string, number int, action string) map[string]any {
	return map[string]any{
		"action":       action,
		"repository":   map[string]any{"full_name": repo},
		"pull_request": map[string]any{"number": number},
		"sender":       map[string]any{"login": "carol"},
	}
}

// signedRequest builds a signed POST for the webhook route.
func signedRequest(t *testing.T, secret, eventType, action string, payload map[string]any) *http.Request {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, WebhookPath, bytes.NewReader(body))
	if eventType != "" {
		req.Header.Set(HeaderEventType, eventType)
	}
	req.Header.Set(HeaderSignature, sign(secret, body))
	return req
}

// serve runs one delivery through the handler and returns the recorder.
func serve(t *testing.T, h *Handler, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHandler_AcceptsValidSignature(t *testing.T) {
	eng, api, q, _ := newTestEngine(t)
	h := NewHandler(eng, "s3cret", nil)
	api.issues[7] = testIssue(7, "open")

	rec := serve(t, h, signedRequest(t, "s3cret", "issues", "opened", issuePayload("o/r", 7, "opened")))
	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rec.Code)
	}
	if len(q.all()) != 1 {
		t.Errorf("got %d tasks, want 1", len(q.all()))
	}
}

func TestHandler_RejectsInvalidSignature(t *testing.T) {
	eng, api, q, _ := newTestEngine(t)
	h := NewHandler(eng, "s3cret", nil)
	api.issues[7] = testIssue(7, "open")

	rec := serve(t, h, signedRequest(t, "wrong-secret", "issues", "opened", issuePayload("o/r", 7, "opened")))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	if len(q.all()) != 0 {
		t.Errorf("task dispatched despite invalid signature")
	}
	if len(api.calls) != 0 {
		t.Errorf("client called despite invalid signature: %v", api.calls)
	}
}

func TestHandler_RejectsMissingSignature(t *testing.T) {
	eng, _, q, _ := newTestEngine(t)
	h := NewHandler(eng, "s3cret", nil)

	body, _ := json.Marshal(issuePayload("o/r", 7, "opened"))
	req := httptest.NewRequest(http.MethodPost, WebhookPath, bytes.NewReader(body))
	req.Header.Set(HeaderEventType, "issues")
	rec := serve(t, h, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	if len(q.all()) != 0 {
		t.Errorf("task dispatched despite missing signature")
	}
}

func TestHandler_RejectsNonPOST(t *testing.T) {
	eng, _, _, _ := newTestEngine(t)
	h := NewHandler(eng, "s3cret", nil)

	req := httptest.NewRequest(http.MethodGet, WebhookPath, nil)
	rec := serve(t, h, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

func TestHandler_RejectsOversizedBody(t *testing.T) {
	eng, _, _, _ := newTestEngine(t)
	h := NewHandler(eng, "s3cret", nil)

	body := make([]byte, maxWebhookBodySize+1)
	req := httptest.NewRequest(http.MethodPost, WebhookPath, bytes.NewReader(body))
	req.Header.Set(HeaderSignature, sign("s3cret", body))
	req.Header.Set(HeaderEventType, "issues")
	rec := serve(t, h, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", rec.Code)
	}
}

func TestHandler_IgnoresUnmappedEvents(t *testing.T) {
	ignored := []struct {
		eventType string
		action    string
	}{
		{"push", ""},
		{"create", ""},
		{"delete", ""},
		{"fork", ""},
		{"release", ""},
		{"wiki", ""},
		{"package", ""},
		{"workflow_run", ""},
		{"issue_assign", "created"},
		{"issue_milestone", "created"},
		{"issue_comment", "edited"},
		{"issues", "reopened"},
		{"issues", "deleted"},
		{"pull_request", "reopened"},
		{"pull_request", "edited"},
		{"pull_request_comment", "deleted"},
		{"pull_request_assign", "created"},
		{"pull_request_label", "labeled"},
		{"pull_request_milestone", "created"},
		{"pull_request_review_request", "created"},
		{"", "opened"}, // missing event type header
	}

	for _, tc := range ignored {
		t.Run(tc.eventType+"/"+tc.action, func(t *testing.T) {
			eng, api, q, _ := newTestEngine(t)
			h := NewHandler(eng, "s3cret", nil)
			api.issues[7] = testIssue(7, "open")

			var payload map[string]any
			if strings.HasPrefix(tc.eventType, "pull_request") || tc.eventType == "" {
				payload = prPayload("o/r", 9, tc.action)
			} else {
				payload = issuePayload("o/r", 7, tc.action)
			}
			rec := serve(t, h, signedRequest(t, "s3cret", tc.eventType, tc.action, payload))
			if rec.Code != http.StatusNoContent {
				t.Errorf("status = %d, want 204", rec.Code)
			}
			if len(api.calls) != 0 {
				t.Errorf("client called for ignored event: %v", api.calls)
			}
			if len(q.all()) != 0 {
				t.Errorf("task dispatched for ignored event")
			}
		})
	}
}

func TestHandler_MapsAndDecides(t *testing.T) {
	cases := []struct {
		name      string
		eventType string
		action    string
		payload   map[string]any
		setup     func(api *fakeAPI)
		// configure adjusts the engine's config for the case;
		// nil keeps testConfig's defaults.
		configure func(cfg *Config)
		want      Action // empty: no dispatch
	}{
		{
			name:      "issue opened, unblocked",
			eventType: "issues", action: "opened",
			payload: issuePayload("o/r", 7, "opened"),
			setup:   func(api *fakeAPI) { api.issues[7] = testIssue(7, "open") },
			want:    ActionImplement,
		},
		{
			name:      "issue edited",
			eventType: "issues", action: "edited",
			payload: issuePayload("o/r", 7, "edited"),
			setup:   func(api *fakeAPI) { api.issues[7] = testIssue(7, "open") },
			want:    ActionReassess,
		},
		{
			name:      "issue labels changed",
			eventType: "issue_label", action: "label_updated",
			payload: issuePayload("o/r", 7, "label_updated"),
			setup:   func(api *fakeAPI) { api.issues[7] = testIssue(7, "open") },
			want:    ActionReassess,
		},
		{
			name:      "issue commented",
			eventType: "issue_comment", action: "created",
			payload: issuePayload("o/r", 7, "created"),
			setup:   func(api *fakeAPI) { api.issues[7] = testIssue(7, "open") },
			want:    ActionReassess,
		},
		{
			name:      "issue opened, open blocker",
			eventType: "issues", action: "opened",
			payload: issuePayload("o/r", 7, "opened"),
			setup: func(api *fakeAPI) {
				api.issues[7] = testIssue(7, "open")
				api.deps[7] = []Issue{*testIssue(10, "open")}
			},
			want: "",
		},
		{
			name:      "issue edited, open blocker",
			eventType: "issues", action: "edited",
			payload: issuePayload("o/r", 7, "edited"),
			setup: func(api *fakeAPI) {
				api.issues[7] = testIssue(7, "open")
				api.deps[7] = []Issue{*testIssue(10, "open")}
			},
			want: "",
		},
		{
			name:      "issue labels changed, closed blockers only",
			eventType: "issue_label", action: "label_updated",
			payload: issuePayload("o/r", 7, "label_updated"),
			setup: func(api *fakeAPI) {
				api.issues[7] = testIssue(7, "open")
				api.deps[7] = []Issue{*testIssue(10, "closed")}
			},
			want: ActionReassess,
		},
		{
			name:      "PR opened, mergeable",
			eventType: "pull_request", action: "opened",
			payload: prPayload("o/r", 9, "opened"),
			setup:   func(api *fakeAPI) { api.pulls[9] = testPR(9, "open", true) },
			want:    ActionReview,
		},
		{
			name:      "PR opened, not mergeable",
			eventType: "pull_request", action: "opened",
			payload: prPayload("o/r", 9, "opened"),
			setup:   func(api *fakeAPI) { api.pulls[9] = testPR(9, "open", false) },
			want:    ActionRebase,
		},
		{
			// Nothing on the open path decides a merge, so nothing there
			// reads the graph: a PR opened against work whose forest is not
			// yet commissioned is reviewed like any other, and the merge
			// gate is where it stops (§forgejo/decisions/commissioning,
			// §forgejo/decisions/merge-gate, s-git/maitred#151).
			name:      "PR opened, connected issue blocked by an open ideation issue",
			eventType: "pull_request", action: "opened",
			payload: prPayload("o/r", 9, "opened"),
			setup: func(api *fakeAPI) {
				api.pulls[9] = testPR(9, "open", true)
				api.blocks[9] = []Issue{*testIssue(4, "open")}
				ideation := testIssue(1, "open")
				ideation.Labels = []string{LabelIdeation}
				api.deps[4] = []Issue{*ideation}
			},
			want: ActionReview,
		},
		{
			name:      "PR opened, not mergeable and blocked by an open ideation issue",
			eventType: "pull_request", action: "opened",
			payload: prPayload("o/r", 9, "opened"),
			setup: func(api *fakeAPI) {
				api.pulls[9] = testPR(9, "open", false)
				api.blocks[9] = []Issue{*testIssue(4, "open")}
				ideation := testIssue(1, "open")
				ideation.Labels = []string{LabelIdeation}
				api.deps[4] = []Issue{*ideation}
			},
			want: ActionRebase,
		},
		{
			name:      "PR opened, connected issue blocked only by a closed ideation issue",
			eventType: "pull_request", action: "opened",
			payload: prPayload("o/r", 9, "opened"),
			setup: func(api *fakeAPI) {
				api.pulls[9] = testPR(9, "open", true)
				api.blocks[9] = []Issue{*testIssue(4, "open")}
				ideation := testIssue(1, "closed")
				ideation.Labels = []string{LabelIdeation}
				api.deps[4] = []Issue{*ideation}
			},
			want: ActionReview,
		},
		{
			name:      "PR synced, no reviews",
			eventType: "pull_request_sync", action: "synchronized",
			payload: prPayload("o/r", 9, "synchronized"),
			setup:   func(api *fakeAPI) { api.pulls[9] = testPR(9, "open", true) },
			want:    ActionReview,
		},
		{
			name:      "PR synced, reviews exist",
			eventType: "pull_request_sync", action: "synchronized",
			payload: prPayload("o/r", 9, "synchronized"),
			// The sync re-fires only the lenses whose approval the push
			// invalidated; here the head moved and bob's approval
			// stands at the old revision
			// (§forgejo/decisions/review-lenses).
			setup: func(api *fakeAPI) {
				api.pulls[9] = testPR(9, "open", true)
				api.reviews[9] = []Review{{Author: "bob", Event: ReviewApproved, CommitID: "sha-old"}}
			},
			want: ActionReview,
		},
		{
			name:      "PR synced by the reviewing author, hold off",
			eventType: "pull_request_sync", action: "synchronized",
			payload: func() map[string]any {
				p := prPayload("o/r", 9, "synchronized")
				p["sender"] = map[string]any{"login": "bob"}
				return p
			}(),
			setup: func(api *fakeAPI) {
				api.pulls[9] = testPR(9, "open", true)
				api.reviews[9] = []Review{{Author: "bob", Event: ReviewApproved, CommitID: "sha-9"}}
			},
			want: "",
		},
		{
			name:      "PR reviewed, approved",
			eventType: "pull_request_review_approved", action: "approved",
			payload: prPayload("o/r", 9, "approved"),
			setup: func(api *fakeAPI) {
				api.pulls[9] = testPR(9, "open", true)
				api.reviews[9] = []Review{{Author: "bob", Event: ReviewApproved, CommitID: "sha-9"}}
			},
			want: ActionMergeOrWait,
		},
		{
			// The gate that stops this merge is the merge gate: the PR
			// stays open, nothing is closed, and the hold-off names the
			// blocker (§forgejo/decisions/merge-gate, s-git/maitred#151).
			name:      "PR reviewed, approved but the connected issue is blocked by an open ideation issue",
			eventType: "pull_request_review_approved", action: "approved",
			payload: prPayload("o/r", 9, "approved"),
			setup: func(api *fakeAPI) {
				api.pulls[9] = testPR(9, "open", true)
				api.reviews[9] = []Review{{Author: "bob", Event: ReviewApproved, CommitID: "sha-9"}}
				api.blocks[9] = []Issue{*testIssue(4, "open")}
				ideation := testIssue(1, "open")
				ideation.Labels = []string{LabelIdeation}
				api.deps[4] = []Issue{*ideation}
			},
			want: "",
		},
		{
			name:      "PR reviewed, approved, connected issue blocked only by a closed ideation issue",
			eventType: "pull_request_review_approved", action: "approved",
			payload: prPayload("o/r", 9, "approved"),
			setup: func(api *fakeAPI) {
				api.pulls[9] = testPR(9, "open", true)
				api.reviews[9] = []Review{{Author: "bob", Event: ReviewApproved, CommitID: "sha-9"}}
				api.blocks[9] = []Issue{*testIssue(4, "open")}
				ideation := testIssue(1, "closed")
				ideation.Labels = []string{LabelIdeation}
				api.deps[4] = []Issue{*ideation}
			},
			want: ActionMergeOrWait,
		},
		{
			name:      "PR reviewed, rejected",
			eventType: "pull_request_review_rejected", action: "rejected",
			payload: prPayload("o/r", 9, "rejected"),
			setup: func(api *fakeAPI) {
				api.pulls[9] = testPR(9, "open", true)
				api.reviews[9] = []Review{{Author: "bob", Event: ReviewChangesRequested, CommitID: "sha-9"}}
			},
			want: ActionFixFeedback,
		},
		{
			// The ownership gate's wiring at engine level: config
			// → state → decision → (no) dispatch. The fixtures
			// above leave the identity unset, so their fix-feedback
			// rows pass on the gate's inert branch; these rows pin
			// that a dropped or unparsed identity cannot silently
			// restore the churn this gate exists to stop
			// (§forgejo/config/implementer-identity,
			// §forgejo/decisions/loop-prevention).
			name:      "PR reviewed, rejected on a PR not authored by the configured implementer identity",
			eventType: "pull_request_review_rejected", action: "rejected",
			payload:   prPayload("o/r", 9, "rejected"),
			configure: setImplementerIdentity,
			setup: func(api *fakeAPI) {
				api.pulls[9] = authoredPR(9, "open", true, "dave")
				api.reviews[9] = []Review{{Author: "bob", Event: ReviewChangesRequested, CommitID: "sha-9"}}
			},
			want: "",
		},
		{
			name:      "PR reviewed, rejected on a PR authored by the configured implementer identity",
			eventType: "pull_request_review_rejected", action: "rejected",
			payload:   prPayload("o/r", 9, "rejected"),
			configure: setImplementerIdentity,
			setup: func(api *fakeAPI) {
				api.pulls[9] = authoredPR(9, "open", true, testImplementerIdentity)
				api.reviews[9] = []Review{{Author: "bob", Event: ReviewChangesRequested, CommitID: "sha-9"}}
			},
			want: ActionFixFeedback,
		},
		{
			name:      "PR reviewed, comment, hold off",
			eventType: "pull_request_review_comment", action: "created",
			payload: prPayload("o/r", 9, "created"),
			setup: func(api *fakeAPI) {
				api.pulls[9] = testPR(9, "open", true)
				api.reviews[9] = []Review{{Author: "bob", Event: "COMMENT", CommitID: "sha-9"}}
			},
			want: "",
		},
		{
			name:      "PR closed, not merged",
			eventType: "pull_request", action: "closed",
			payload: prPayload("o/r", 9, "closed"),
			setup:   func(api *fakeAPI) { api.pulls[9] = testPR(9, "closed", true) },
			want:    "",
		},
		{
			// A comment on a PR decides as the reconcile sweep does
			// (§forgejo/decisions/transition-table).
			name:      "PR commented, standing at an approved review",
			eventType: "pull_request_comment", action: "created",
			payload: prPayload("o/r", 9, "created"),
			setup: func(api *fakeAPI) {
				api.pulls[9] = testPR(9, "open", true)
				api.reviews[9] = []Review{{Author: "bob", Event: ReviewApproved, CommitID: "sha-9", SubmittedAt: testUpdatedAt}}
			},
			want: ActionMergeOrWait,
		},
		{
			name:      "PR commented, standing at a changes-requested review",
			eventType: "pull_request_comment", action: "created",
			payload: prPayload("o/r", 9, "created"),
			setup: func(api *fakeAPI) {
				api.pulls[9] = testPR(9, "open", true)
				api.reviews[9] = []Review{{Author: "bob", Event: ReviewChangesRequested, CommitID: "sha-9", SubmittedAt: testUpdatedAt}}
			},
			want: ActionFixFeedback,
		},
		{
			// The same wiring on the comment path, which decides
			// as the reconcile sweep does
			// (§forgejo/decisions/transition-table).
			name:      "PR commented, standing at a changes-requested review on a PR not authored by the configured implementer identity",
			eventType: "pull_request_comment", action: "created",
			payload:   prPayload("o/r", 9, "created"),
			configure: setImplementerIdentity,
			setup: func(api *fakeAPI) {
				api.pulls[9] = authoredPR(9, "open", true, "dave")
				api.reviews[9] = []Review{{Author: "bob", Event: ReviewChangesRequested, CommitID: "sha-9", SubmittedAt: testUpdatedAt}}
			},
			want: "",
		},
		{
			name:      "PR commented, standing at a comment review",
			eventType: "pull_request_comment", action: "created",
			payload: prPayload("o/r", 9, "created"),
			setup: func(api *fakeAPI) {
				api.pulls[9] = testPR(9, "open", true)
				api.reviews[9] = []Review{{Author: "bob", Event: ReviewComment, CommitID: "sha-9", SubmittedAt: testUpdatedAt}}
			},
			want: ActionFixFeedback,
		},
		{
			name:      "PR commented, no reviews",
			eventType: "pull_request_comment", action: "created",
			payload: prPayload("o/r", 9, "created"),
			setup:   func(api *fakeAPI) { api.pulls[9] = testPR(9, "open", true) },
			want:    ActionReview,
		},
		{
			name:      "PR commented, not mergeable",
			eventType: "pull_request_comment", action: "created",
			payload: prPayload("o/r", 9, "created"),
			setup:   func(api *fakeAPI) { api.pulls[9] = testPR(9, "open", false) },
			want:    ActionRebase,
		},
		{
			name:      "PR commented, closed on re-fetch, hold off",
			eventType: "pull_request_comment", action: "created",
			payload: prPayload("o/r", 9, "created"),
			setup:   func(api *fakeAPI) { api.pulls[9] = testPR(9, "closed", true) },
			want:    "",
		},
		{
			name:      "PR commented, latest review predates the head",
			eventType: "pull_request_comment", action: "created",
			payload: prPayload("o/r", 9, "created"),
			setup: func(api *fakeAPI) {
				api.pulls[9] = testPR(9, "open", true)
				api.reviews[9] = []Review{{Author: "bob", Event: ReviewComment, CommitID: "stale-sha", SubmittedAt: testUpdatedAt}}
			},
			want: ActionReview,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng, api, q, _ := newTestEngine(t)
			h := NewHandler(eng, "s3cret", nil)
			if tc.configure != nil {
				tc.configure(eng.cfg)
			}
			tc.setup(api)

			rec := serve(t, h, signedRequest(t, "s3cret", tc.eventType, tc.action, tc.payload))
			if rec.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want 204", rec.Code)
			}
			tasks := q.all()
			if tc.want == "" {
				if len(tasks) != 0 {
					t.Fatalf("got %d tasks, want none: %+v", len(tasks), tasks)
				}
				return
			}
			if len(tasks) != 1 {
				t.Fatalf("got %d tasks, want 1", len(tasks))
			}
			if !strings.Contains(tasks[0].Prompt, "action="+string(tc.want)) {
				t.Errorf("prompt = %q, want it to carry action %s", tasks[0].Prompt, tc.want)
			}
		})
	}
}

// TestHandler_IssueActivity_FetchesBlockers pins that the activity
// path fetches the issue's blockers — the re-fetch the blocked-issue
// hold-off needs — exactly once per delivery (§forgejo/webhook/re-fetch,
// §forgejo/decisions/transition-table).
func TestHandler_IssueActivity_FetchesBlockers(t *testing.T) {
	for _, tc := range []struct {
		eventType string
		action    string
	}{
		{"issues", "edited"},
		{"issue_label", "label_updated"},
		{"issue_comment", "created"},
	} {
		eng, api, q, _ := newTestEngine(t)
		h := NewHandler(eng, "s3cret", nil)
		api.issues[7] = testIssue(7, "open")

		serve(t, h, signedRequest(t, "s3cret", tc.eventType, tc.action, issuePayload("o/r", 7, tc.action)))
		if n := api.callCount("IssueDependencies:r/7"); n != 1 {
			t.Errorf("%s/%s: IssueDependencies called %d times, want 1", tc.eventType, tc.action, n)
		}
		if len(q.all()) != 1 {
			t.Errorf("%s/%s: unblocked issue dispatched %d tasks, want 1 reassess", tc.eventType, tc.action, len(q.all()))
		}
	}
}

// TestHandler_IssueActivity_BlockersFetchFailureHoldsOff pins that a
// failing blockers read never reaches the decision as "no blockers":
// the re-fetch fails, so nothing dispatches and no watermark is
// recorded; the reconciliation sweep re-derives
// (§forgejo/webhook/the-pipeline, §forgejo/webhook/re-fetch).
func TestHandler_IssueActivity_BlockersFetchFailureHoldsOff(t *testing.T) {
	eng, api, q, store := newTestEngine(t)
	h := NewHandler(eng, "s3cret", nil)
	api.issues[7] = testIssue(7, "open")
	api.depsErr = errors.New("dependencies endpoint down")

	serve(t, h, signedRequest(t, "s3cret", "issue_comment", "created", issuePayload("o/r", 7, "created")))
	if n := api.callCount("IssueDependencies:r/7"); n == 0 {
		t.Error("the failing blockers read was never reached")
	}
	if tasks := q.all(); len(tasks) != 0 {
		t.Errorf("dispatched %d tasks after the blockers read failed: %+v", len(tasks), tasks)
	}
	w, err := store.Load(Key{Repo: "o/r", Kind: KindIssue, Number: 7})
	if err != nil || w != nil {
		t.Errorf("watermark recorded after the hold-off: %v, %v", w, err)
	}
}

// TestHandler_PROpened_BlockedConnectedIssueDispatchesReview pins the
// retired rejection's absence end to end: a PR opened against a work
// item that is blocked by an open ideation issue is not closed by the
// engine — the client holds no close-a-PR verb at all — it is reviewed
// as any other PR would be, and the decision log records the review
// (§forgejo/decisions/transition-table, s-git/maitred#151).
func TestHandler_PROpened_BlockedConnectedIssueDispatchesReview(t *testing.T) {
	eng, api, q, store := newTestEngine(t)
	h := NewHandler(eng, "s3cret", nil)
	api.pulls[9] = testPR(9, "open", true)
	api.blocks[9] = []Issue{*testIssue(4, "open")}
	ideation := testIssue(1, "open")
	ideation.Labels = []string{LabelIdeation}
	api.deps[4] = []Issue{*ideation}

	serve(t, h, signedRequest(t, "s3cret", "pull_request", "opened", prPayload("o/r", 9, "opened")))
	if len(q.all()) != 1 || !strings.Contains(q.all()[0].Prompt, "action="+string(ActionReview)) {
		t.Fatalf("want one review dispatch, got %+v", q.all())
	}
	w, err := store.Load(Key{Repo: "o/r", Kind: KindPR, Number: 9})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if w == nil || w.Subjects["bob"] != (Slot{Action: string(ActionReview), Revision: "sha-9"}) {
		t.Errorf("watermark = %+v, want a subject slot for the lens review at the head revision", w)
	}
}

// TestHandler_PROpened_DoesNotFetchConnectedIssues: the open path
// dispatches no merge, so it grows no gate read — the hop it used to
// perform served only the retired rejection
// (§forgejo/webhook/re-fetch, s-git/maitred#151).
func TestHandler_PROpened_DoesNotFetchConnectedIssues(t *testing.T) {
	eng, api, _, _ := newTestEngine(t)
	h := NewHandler(eng, "s3cret", nil)
	api.pulls[9] = testPR(9, "open", true)
	api.blocks[9] = []Issue{*testIssue(4, "open")}
	ideation := testIssue(1, "open")
	ideation.Labels = []string{LabelIdeation}
	api.deps[4] = []Issue{*ideation}

	serve(t, h, signedRequest(t, "s3cret", "pull_request", "opened", prPayload("o/r", 9, "opened")))
	if n := api.callCount("IssueBlocks"); n != 0 {
		t.Errorf("IssueBlocks called %d time(s) on PR opened, want 0: the gate hop is PR reviewed, PR commented and the sweep, not an open", n)
	}
	if n := api.callCount("IssueDependencies"); n != 0 {
		t.Errorf("IssueDependencies called %d time(s) on PR opened, want 0: one hop per connected issue belongs to the paths that may merge", n)
	}
}

// TestHandler_PRReviewed_ResolvesEveryConnectedIssue pins the re-fetch
// surface when a PR blocks several non-PR issues: every non-PR entry is
// resolved with its blockers — one hop each, once per issue — so the
// merge gate's decision cannot depend on the order the API returned the
// relationships in (§forgejo/webhook/re-fetch).
func TestHandler_PRReviewed_ResolvesEveryConnectedIssue(t *testing.T) {
	eng, api, q, _, log := mergeGateFixtures(t)
	h := NewHandler(eng, "s3cret", nil)
	first := testIssue(4, "open")
	second := testIssue(5, "open")
	second.Repository = "o/outcomes"
	api.blocks[9] = []Issue{*first, *second}
	api.deps[4] = []Issue{*testIssue(1, "open")} // ordinary unlabelled blocker
	ideation := testIssue(2, "open")
	ideation.Repository = "o/outcomes"
	ideation.Labels = []string{LabelIdeation}
	api.deps[5] = []Issue{*ideation}

	serve(t, h, signedRequest(t, "s3cret", "pull_request_review_approved", "approved", prPayload("o/r", 9, "approved")))
	if len(q.all()) != 0 {
		t.Fatalf("want no dispatch — a connected issue has an open blocker, so the merge gate holds off, got %+v", q.all())
	}
	if reason := reasonFor(t, log, "o/r/pr/9"); !strings.Contains(reason, "o/outcomes#2") {
		t.Errorf("reason %q does not name the open blocker o/outcomes#2: every connected issue is resolved, not just the first", reason)
	}
	if n := api.callCount("IssueBlocks"); n != 1 {
		t.Errorf("IssueBlocks called %d time(s), want 1", n)
	}
	if n := api.callCount("IssueDependencies:r/4"); n != 1 {
		t.Errorf("IssueDependencies on the first connected issue called %d time(s), want 1", n)
	}
	if n := api.callCount("IssueDependencies:outcomes/5"); n != 1 {
		t.Errorf("IssueDependencies on the second connected issue called %d time(s), want 1", n)
	}
	if n := api.callCount("IssueDependencies:outcomes/2"); n != 0 {
		t.Errorf("IssueDependencies on a connected issue's blocker called %d time(s), want 0: one hop per connected issue, no recursion", n)
	}
}

func TestHandler_PRSynced_DoesNotFetchConnectedIssue(t *testing.T) {
	eng, api, q, _ := newTestEngine(t)
	h := NewHandler(eng, "s3cret", nil)
	api.pulls[9] = testPR(9, "open", true)

	serve(t, h, signedRequest(t, "s3cret", "pull_request_sync", "synchronized", prPayload("o/r", 9, "synchronized")))
	if len(q.all()) != 1 || !strings.Contains(q.all()[0].Prompt, "action="+string(ActionReview)) {
		t.Fatalf("want one review dispatch, got %+v", q.all())
	}
	if n := api.callCount("IssueBlocks"); n != 0 {
		t.Errorf("IssueBlocks called %d time(s) on PR sync, want 0: the gate hop is PR reviewed, PR commented and the sweep, not a sync", n)
	}
}

// TestHandler_PRReviewed_FetchesConnectedIssueOneHop pins the fetch
// surface of the review path: a PR-reviewed delivery performs the gate
// hop — one IssueBlocks, one IssueDependencies per connected issue, no
// recursion — so the merge gate is fed where it applies
// (§forgejo/webhook/re-fetch, §forgejo/decisions/merge-gate).
func TestHandler_PRReviewed_FetchesConnectedIssueOneHop(t *testing.T) {
	eng, api, q, _ := newTestEngine(t)
	h := NewHandler(eng, "s3cret", nil)
	api.pulls[9] = testPR(9, "open", true)
	api.reviews[9] = []Review{{Author: "bob", Event: ReviewApproved, CommitID: "sha-9"}}
	api.blocks[9] = []Issue{*testIssue(4, "open")}
	ideation := testIssue(1, "open")
	ideation.Labels = []string{LabelIdeation}
	api.deps[4] = []Issue{*ideation}

	serve(t, h, signedRequest(t, "s3cret", "pull_request_review_approved", "approved", prPayload("o/r", 9, "approved")))
	if len(q.all()) != 0 {
		t.Fatalf("want no dispatch — the merge gate holds the approved merge off, got %+v", q.all())
	}
	if n := api.callCount("IssueBlocks"); n != 1 {
		t.Errorf("IssueBlocks called %d time(s), want 1 (one hop, no recursion)", n)
	}
	if n := api.callCount("IssueDependencies:r/4"); n != 1 {
		t.Errorf("IssueDependencies on the connected issue called %d time(s), want 1", n)
	}
	if n := api.callCount("IssueDependencies:r/1"); n != 0 {
		t.Errorf("IssueDependencies on the blocker called %d time(s), want 0: one hop, no recursion", n)
	}
}

// TestHandler_PRReviewed_ConnectedIssueFetchFailureHoldsOff pins the
// fail-closed hold-off on the review path: a failed hop read holds the
// PR open naming the failure, never defaulting into the
// review-following decisions (§forgejo/webhook/re-fetch).
func TestHandler_PRReviewed_ConnectedIssueFetchFailureHoldsOff(t *testing.T) {
	eng, api, q, store := newTestEngine(t)
	h := NewHandler(eng, "s3cret", nil)
	api.pulls[9] = testPR(9, "open", true)
	api.reviews[9] = []Review{{Author: "bob", Event: ReviewApproved, CommitID: "sha-9"}}
	api.issueBlocksErr = errors.New("blocks unavailable")

	serve(t, h, signedRequest(t, "s3cret", "pull_request_review_approved", "approved", prPayload("o/r", 9, "approved")))
	if n := api.callCount("IssueBlocks"); n != 1 {
		t.Errorf("IssueBlocks called %d time(s), want 1: the delivery must reach the hop for this test to test it", n)
	}
	if len(q.all()) != 0 {
		t.Errorf("task dispatched despite failed connected-issue re-fetch: %+v", q.all())
	}
	w, err := store.Load(Key{Repo: "o/r", Kind: KindPR, Number: 9})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if w != nil {
		t.Errorf("watermark recorded after failed re-fetch: %+v", w)
	}
}

// TestHandler_PRReviewed_ConnectedIssueBlocksFetchFailureHoldsOff pins
// the same fail-closed hold-off when it is the connected issue's
// *blockers* whose read fails: the merge gate never reads "no blockers"
// off a failed listing (§forgejo/webhook/re-fetch,
// §forgejo/decisions/merge-gate).
func TestHandler_PRReviewed_ConnectedIssueBlocksFetchFailureHoldsOff(t *testing.T) {
	eng, api, q, store := newTestEngine(t)
	h := NewHandler(eng, "s3cret", nil)
	api.pulls[9] = testPR(9, "open", true)
	api.reviews[9] = []Review{{Author: "bob", Event: ReviewApproved, CommitID: "sha-9"}}
	api.blocks[9] = []Issue{*testIssue(4, "open")}
	api.issueDepsErr = errors.New("dependencies unavailable")

	serve(t, h, signedRequest(t, "s3cret", "pull_request_review_approved", "approved", prPayload("o/r", 9, "approved")))
	if n := api.callCount("IssueDependencies"); n != 1 {
		t.Errorf("IssueDependencies called %d time(s), want 1: the delivery must reach the blocker resolution for this test to test it", n)
	}
	if len(q.all()) != 0 {
		t.Errorf("task dispatched despite failed blocker re-fetch: %+v", q.all())
	}
	w, err := store.Load(Key{Repo: "o/r", Kind: KindPR, Number: 9})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if w != nil {
		t.Errorf("watermark recorded after failed re-fetch: %+v", w)
	}
}

// TestHandler_ReconcilePR_FetchesConnectedIssueOneHop pins the sweep
// half of the hop: the sweep's synthetic current-state event for a PR
// performs the same one hop, so the merge gate is fed before the sweep's
// review-following and synced-like branches
// (§forgejo/webhook/re-fetch, §forgejo/decisions/transition-table).
func TestHandler_ReconcilePR_FetchesConnectedIssueOneHop(t *testing.T) {
	eng, api, q, _ := newTestEngine(t)
	api.repos = []Repository{testRepo("o/r")}
	api.pulls[9] = testPR(9, "open", true)
	api.reviews[9] = []Review{{Author: "bob", Event: ReviewApproved, CommitID: testPR(9, "open", true).HeadSHA}}
	api.blocks[9] = []Issue{*testIssue(4, "open")}
	ideation := testIssue(1, "open")
	ideation.Labels = []string{LabelIdeation}
	api.deps[4] = []Issue{*ideation}

	eng.HandleEvent(Event{Type: EventReconcile, Repo: "o/r", Kind: KindPR, Number: 9, Sender: "maitred"})
	if len(q.all()) != 0 {
		t.Fatalf("want no dispatch — the merge gate holds the sweep's merge off, got %+v", q.all())
	}
	if n := api.callCount("IssueBlocks"); n != 1 {
		t.Errorf("IssueBlocks called %d time(s), want 1 (one hop, no recursion)", n)
	}
	if n := api.callCount("IssueDependencies:r/4"); n != 1 {
		t.Errorf("IssueDependencies on the connected issue called %d time(s), want 1", n)
	}
}

// TestHandler_PRLabelDelivery_IgnoredWithoutFetching pins that a label
// delivery stays cheaply ignored even with a connected issue blocked by
// an open ideation issue: it must not reach the decision, no re-fetch,
// no dispatch (§forgejo/webhook/event-mapping).
func TestHandler_PRLabelDelivery_IgnoredWithoutFetching(t *testing.T) {
	eng, api, q, _ := newTestEngine(t)
	h := NewHandler(eng, "s3cret", nil)
	api.pulls[9] = testPR(9, "open", true)
	api.blocks[9] = []Issue{*testIssue(4, "open")}
	ideation := testIssue(1, "open")
	ideation.Labels = []string{LabelIdeation}
	api.deps[4] = []Issue{*ideation}

	rec := serve(t, h, signedRequest(t, "s3cret", "pull_request_label", "labeled", prPayload("o/r", 9, "labeled")))
	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rec.Code)
	}
	if len(api.calls) != 0 {
		t.Errorf("client called for ignored label delivery: %v", api.calls)
	}
	if len(q.all()) != 0 {
		t.Errorf("task dispatched for ignored label delivery")
	}
}

func TestHandler_RefetchWins(t *testing.T) {
	eng, api, q, _ := newTestEngine(t)
	h := NewHandler(eng, "s3cret", nil)
	// The payload says opened, but the re-fetched issue is closed.
	api.issues[7] = testIssue(7, "closed")

	rec := serve(t, h, signedRequest(t, "s3cret", "issues", "opened", issuePayload("o/r", 7, "opened")))
	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rec.Code)
	}
	if len(q.all()) != 0 {
		t.Errorf("dispatched despite the re-fetched issue being closed")
	}
}

func TestHandler_RefetchFailure_HoldsOff(t *testing.T) {
	eng, api, q, store := newTestEngine(t)
	h := NewHandler(eng, "s3cret", nil)
	// f.err fails the object re-fetch only; the scope listing stays
	// healthy so the delivery actually reaches the re-fetch stage
	// rather than holding off at the scope test.
	api.err = errors.New("boom")

	rec := serve(t, h, signedRequest(t, "s3cret", "issues", "opened", issuePayload("o/r", 7, "opened")))
	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rec.Code)
	}
	if n := api.callCount("GetIssue"); n != 1 {
		t.Errorf("GetIssue called %d time(s), want 1: the delivery must reach the re-fetch for this test to test it", n)
	}
	if len(q.all()) != 0 {
		t.Errorf("task dispatched despite failed re-fetch")
	}
	w, err := store.Load(Key{Repo: "o/r", Kind: KindIssue, Number: 7})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if w != nil {
		t.Errorf("watermark recorded after failed re-fetch: %+v", w)
	}
}

func TestHandler_WatermarkAndIdempotency(t *testing.T) {
	eng, api, q, store := newTestEngine(t)
	h := NewHandler(eng, "s3cret", nil)
	api.issues[7] = testIssue(7, "open")

	rec := serve(t, h, signedRequest(t, "s3cret", "issues", "opened", issuePayload("o/r", 7, "opened")))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("first delivery: status = %d, want 204", rec.Code)
	}
	if len(q.all()) != 1 {
		t.Fatalf("got %d tasks, want 1", len(q.all()))
	}
	w, err := store.Load(Key{Repo: "o/r", Kind: KindIssue, Number: 7})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if w == nil || w.Action != string(ActionImplement) || w.Revision != testUpdatedAt.UTC().Format(time.RFC3339) {
		t.Errorf("watermark = %+v, want implement @ %s", w, testUpdatedAt.UTC().Format(time.RFC3339))
	}

	// A second identical delivery holds off.
	rec = serve(t, h, signedRequest(t, "s3cret", "issues", "opened", issuePayload("o/r", 7, "opened")))
	if rec.Code != http.StatusNoContent {
		t.Errorf("second delivery: status = %d, want 204", rec.Code)
	}
	if len(q.all()) != 1 {
		t.Errorf("got %d tasks, want 1 (idempotent)", len(q.all()))
	}
}

// TestHandler_PRMerged_NoOwnWatermark: a merged PR dispatches nothing
// for its own object (only the cascade), so no watermark is recorded
// for the PR key.
func TestHandler_PRMerged_NoOwnWatermark(t *testing.T) {
	eng, api, q, store := newTestEngine(t)
	h := NewHandler(eng, "s3cret", nil)
	api.pulls[9] = testPR(9, "closed", true)
	api.pulls[9].Merged = true
	api.blocks[9] = []Issue{*testIssue(7, "closed")}

	rec := serve(t, h, signedRequest(t, "s3cret", "pull_request", "closed", prPayload("o/r", 9, "closed")))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if len(q.all()) != 0 {
		t.Errorf("got %d tasks, want none for the PR's own object", len(q.all()))
	}
	w, err := store.Load(Key{Repo: "o/r", Kind: KindPR, Number: 9})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if w != nil {
		t.Errorf("watermark = %+v, want none (a hold-off records nothing)", w)
	}
}

func TestHandler_TaskFields(t *testing.T) {
	eng, api, q, _ := newTestEngine(t)
	h := NewHandler(eng, "s3cret", nil)
	api.issues[7] = testIssue(7, "open")

	rec := serve(t, h, signedRequest(t, "s3cret", "issues", "opened", issuePayload("o/r", 7, "opened")))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	tasks := q.all()
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks, want 1", len(tasks))
	}
	task := tasks[0]
	wantPrompt := "action=implement repo=o/r kind=issue number=7 issue=https://forge.example.com/o/r/issues/7 pr= sender=alice"
	if task.Prompt != wantPrompt {
		t.Errorf("prompt = %q, want %q", task.Prompt, wantPrompt)
	}
	if task.Persona != "persona-implement" {
		t.Errorf("persona = %q, want persona-implement", task.Persona)
	}
	if task.Timeout != int((2 * time.Hour).Seconds()) {
		t.Errorf("timeout = %d, want %d", task.Timeout, int((2 * time.Hour).Seconds()))
	}
	if !strings.HasPrefix(task.ID, "forgejoeng-") {
		t.Errorf("task ID = %q, want the forgejoeng- prefix", task.ID)
	}
}

// TestHandler_TaskDedupKey: a dispatch carries the dedup key of its
// dispatch identity — the key's repo, kind, and number, the action, and
// the revision — so a queue system with de-duplication can squelch a
// re-dispatch of the same (action, revision) while the first task is
// still pending (§forgejo/webhook/the-pipeline).
func TestHandler_TaskDedupKey(t *testing.T) {
	eng, api, q, _ := newTestEngine(t)
	h := NewHandler(eng, "s3cret", nil)
	api.issues[7] = testIssue(7, "open")

	rec := serve(t, h, signedRequest(t, "s3cret", "issues", "opened", issuePayload("o/r", 7, "opened")))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	tasks := q.all()
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks, want 1", len(tasks))
	}
	want := "forgejoeng-o/r-issue-7-implement-" + testUpdatedAt.UTC().Format(time.RFC3339)
	if tasks[0].DedupKey != want {
		t.Errorf("dedup key = %q, want %q", tasks[0].DedupKey, want)
	}
}

// TestDispatch_DedupKeyStable: re-dispatching the same (action,
// revision) yields the same dedup key, and a different revision yields
// a different one (§forgejo/webhook/the-pipeline).
func TestDispatch_DedupKeyStable(t *testing.T) {
	eng, api, q, _ := newTestEngine(t)
	api.issues[7] = testIssue(7, "open")
	ev := Event{Type: EventIssueOpened, Repo: "o/r", Kind: KindIssue, Number: 7, Sender: "alice"}
	_, st, err := eng.refetch(ev)
	if err != nil {
		t.Fatalf("refetch: %v", err)
	}
	if err := eng.dispatch(ev, st, ActionImplement); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if err := eng.dispatch(ev, st, ActionImplement); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	tasks := q.all()
	if len(tasks) != 2 {
		t.Fatalf("got %d tasks, want 2", len(tasks))
	}
	if tasks[0].DedupKey == "" || tasks[0].DedupKey != tasks[1].DedupKey {
		t.Errorf("dedup keys = %q, %q; want equal and non-empty", tasks[0].DedupKey, tasks[1].DedupKey)
	}

	// A new revision (the issue was edited) is a different dispatch
	// identity.
	api.issues[7].UpdatedAt = testUpdatedAt.Add(time.Hour)
	_, st2, err := eng.refetch(ev)
	if err != nil {
		t.Fatalf("refetch: %v", err)
	}
	if err := eng.dispatch(ev, st2, ActionImplement); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	tasks = q.all()
	if tasks[2].DedupKey == tasks[0].DedupKey {
		t.Errorf("dedup key = %q after a revision change, want a different key", tasks[2].DedupKey)
	}
}

// TestDispatch_DedupKeyPR: a PR dispatch's dedup key carries the head
// sha, so a synced PR (a new head) is a different dispatch identity
// (§forgejo/webhook/the-pipeline).
func TestDispatch_DedupKeyPR(t *testing.T) {
	eng, api, q, _ := newTestEngine(t)
	api.pulls[9] = testPR(9, "open", true)
	ev := Event{Type: EventPRSynced, Repo: "o/r", Kind: KindPR, Number: 9, Sender: "alice"}
	_, st, err := eng.refetch(ev)
	if err != nil {
		t.Fatalf("refetch: %v", err)
	}
	if err := eng.dispatchLens(ev, st, eng.cfg.Reviews[0], revisionFor(ev, st)); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	tasks := q.all()
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks, want 1", len(tasks))
	}
	// The key carries the lens alongside the action and the revision:
	// the fan-out's tasks are three tasks over one head, not one
	// squelched trio (§forgejo/decisions/review-lenses).
	want := "forgejoeng-o/r-pr-9-review-bob-sha-9"
	if tasks[0].DedupKey != want {
		t.Errorf("dedup key = %q, want %q", tasks[0].DedupKey, want)
	}
}

// TestDispatch_LensDedupKeysDistinct: the fan-out over a three-lens
// chain produces three distinct dedup keys — the queue squelches a
// re-dispatch of one lens, never of the chain
// (§forgejo/webhook/the-pipeline, §forgejo/decisions/review-lenses).
func TestDispatch_LensDedupKeysDistinct(t *testing.T) {
	eng, api, q, _ := newTestEngine(t)
	eng.cfg.Reviews = []ReviewLens{
		{Identity: "lens-a", Prompt: "/pr-review-a {{.PRURL}}", Persona: "p", Timeout: time.Hour},
		{Identity: "lens-b", Prompt: "/pr-review-b {{.PRURL}}", Persona: "p", Timeout: time.Hour},
		{Identity: "lens-c", Prompt: "/pr-review-c {{.PRURL}}", Persona: "p", Timeout: time.Hour},
	}
	api.pulls[9] = testPR(9, "open", true)
	ev := Event{Type: EventPROpened, Repo: "o/r", Kind: KindPR, Number: 9, Sender: "alice"}
	_, st, err := eng.refetch(ev)
	if err != nil {
		t.Fatalf("refetch: %v", err)
	}
	seen := map[string]bool{}
	for _, lens := range eng.cfg.Reviews {
		if err := eng.dispatchLens(ev, st, lens, revisionFor(ev, st)); err != nil {
			t.Fatalf("dispatchLens(%s): %v", lens.Identity, err)
		}
		key := q.all()[len(q.all())-1].DedupKey
		if seen[key] {
			t.Fatalf("lens %s repeats dedup key %q", lens.Identity, key)
		}
		seen[key] = true
	}
}

// TestDispatch_DedupKeyDistinctRepos: repos that would encode
// identically under the task ID's "-" replacement (a-b/c and a/b-c)
// still produce distinct dedup keys — the key carries the repo
// verbatim (§forgejo/webhook/the-pipeline).
func TestDispatch_DedupKeyDistinctRepos(t *testing.T) {
	eng, api, q, _ := newTestEngine(t)
	api.issues[7] = testIssue(7, "open")
	for _, repo := range []string{"a-b/c", "a/b-c"} {
		ev := Event{Type: EventIssueOpened, Repo: repo, Kind: KindIssue, Number: 7, Sender: "alice"}
		_, st, err := eng.refetch(ev)
		if err != nil {
			t.Fatalf("refetch: %v", err)
		}
		if err := eng.dispatch(ev, st, ActionImplement); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
	}
	tasks := q.all()
	if len(tasks) != 2 {
		t.Fatalf("got %d tasks, want 2", len(tasks))
	}
	if tasks[0].DedupKey == tasks[1].DedupKey {
		t.Errorf("dedup keys = %q for both a-b/c and a/b-c, want distinct", tasks[0].DedupKey)
	}
}

// TestDispatch_TitleAndRefURL: a dispatch names its work. The title reads
// as the action's verb, the subject's issue or PR title, and the
// `repo#number` reference; the URL is the pull request's when the state
// carries one and the issue's otherwise — empty where the state carries
// neither, since the engine invents no link. A long subject is capped so
// it cannot produce an unreadable row (§forgejo/webhook/the-pipeline).
func TestDispatch_TitleAndRefURL(t *testing.T) {
	long := strings.Repeat("x", maxTaskSubjectRunes+40)

	cases := []struct {
		name   string
		prompt *PromptConfig
		ev     Event
		st     State
		action Action
		lens   bool
		want   string
		url    string
	}{
		{
			name:   "issue dispatch",
			ev:     Event{Type: EventIssueOpened, Repo: "o/r", Kind: KindIssue, Number: 7, Sender: "alice"},
			st:     State{Issue: testIssue(7, "open")},
			action: ActionImplement,
			want:   "Implement: issue 7 (o/r#7)",
			url:    "https://forge.example.com/o/r/issues/7",
		},
		{
			name:   "lens review dispatch",
			ev:     Event{Type: EventPRSynced, Repo: "o/r", Kind: KindPR, Number: 9, Sender: "alice"},
			st:     State{PR: testPR(9, "open", true)},
			action: ActionReview,
			lens:   true,
			want:   "Review as bob: pr 9 (o/r#9)",
			url:    "https://forge.example.com/o/r/pulls/9",
		},
		{
			// A pull request alongside its issue is named by the pull
			// request: title and URL name the same object.
			name:   "pull request dispatch with its issue",
			ev:     Event{Type: EventPRSynced, Repo: "o/r", Kind: KindPR, Number: 9, Sender: "alice"},
			st:     State{Issue: testIssue(7, "open"), PR: testPR(9, "open", true)},
			action: ActionMergeOrWait,
			want:   "Merge-or-wait: pr 9 (o/r#9)",
			url:    "https://forge.example.com/o/r/pulls/9",
		},
		{
			// The state carries neither object, so the title names only
			// the action and the reference and no URL is invented. A
			// prompt naming no URL placeholder keeps dispatching
			// (§forgejo/config/canned-prompts).
			name:   "state with no object",
			prompt: &PromptConfig{Prompt: "/housekeeping {{.Repo}}", Persona: "sweeper", Timeout: time.Hour},
			ev:     Event{Type: EventIssueOpened, Repo: "o/r", Kind: KindIssue, Number: 7, Sender: "alice"},
			st:     State{},
			action: ActionWrapUp,
			want:   "Wrap-up (o/r#7)",
		},
		{
			name:   "long subject is capped",
			ev:     Event{Type: EventIssueOpened, Repo: "o/r", Kind: KindIssue, Number: 7, Sender: "alice"},
			st:     State{Issue: &Issue{Number: 7, Title: long, State: "open", HTMLURL: "https://forge.example.com/o/r/issues/7"}},
			action: ActionImplement,
			want:   "Implement: " + strings.Repeat("x", maxTaskSubjectRunes-1) + "… (o/r#7)",
			url:    "https://forge.example.com/o/r/issues/7",
		},
		{
			// A reference so long that no subject fits the line leaves
			// the action-and-reference form, capped.
			name:   "reference leaving no room for a subject",
			ev:     Event{Type: EventIssueOpened, Repo: strings.Repeat("r", maxTaskTitleRunes), Kind: KindIssue, Number: 7, Sender: "alice"},
			st:     State{Issue: testIssue(7, "open")},
			action: ActionImplement,
			want:   "Implement (" + strings.Repeat("r", maxTaskTitleRunes-len("Implement (")-1) + "…",
			url:    "https://forge.example.com/o/r/issues/7",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng, _, q, _ := newTestEngine(t)
			if tc.prompt != nil {
				eng.cfg.Actions[string(tc.action)] = *tc.prompt
			}
			if tc.lens {
				if err := eng.dispatchLens(tc.ev, tc.st, eng.cfg.Reviews[0], revisionFor(tc.ev, tc.st)); err != nil {
					t.Fatalf("dispatchLens: %v", err)
				}
			} else if err := eng.dispatch(tc.ev, tc.st, tc.action); err != nil {
				t.Fatalf("dispatch: %v", err)
			}
			tasks := q.all()
			if len(tasks) != 1 {
				t.Fatalf("got %d tasks, want 1", len(tasks))
			}
			if tasks[0].Title != tc.want {
				t.Errorf("title = %q, want %q", tasks[0].Title, tc.want)
			}
			if tasks[0].URL != tc.url {
				t.Errorf("url = %q, want %q", tasks[0].URL, tc.url)
			}
		})
	}
}

// TestDispatch_WithoutURLArgument: a canned prompt that asks for a URL the
// state cannot fill renders as a command naming no object — "/issue-
// implementer " — and Pi reports nothing over one, so the persona would
// simply start without its parameter (#121). The dispatch fails loudly
// instead and enqueues nothing (§forgejo/config/canned-prompts,
// §forgejo/webhook/the-pipeline).
func TestDispatch_WithoutURLArgument(t *testing.T) {
	issuePrompt := PromptConfig{Prompt: "/issue-implementer {{.IssueURL}}", Persona: "implementer", Timeout: time.Hour}
	prPrompt := PromptConfig{Prompt: "/pr-reviewer {{.PRURL}}", Persona: "reviewer", Timeout: time.Hour}

	cases := []struct {
		name       string
		prompt     PromptConfig
		ev         Event
		st         State
		action     Action
		wantErr    bool
		wantSuffix string
	}{
		{
			name:       "issue path with its issue",
			prompt:     issuePrompt,
			ev:         Event{Type: EventIssueOpened, Repo: "o/r", Kind: KindIssue, Number: 7, Sender: "alice"},
			st:         State{Issue: testIssue(7, "open")},
			action:     ActionImplement,
			wantSuffix: "https://forge.example.com/o/r/issues/7",
		},
		{
			name:   "issue path with no issue",
			prompt: issuePrompt,
			ev:     Event{Type: EventIssueOpened, Repo: "o/r", Kind: KindIssue, Number: 7, Sender: "alice"},
			st:     State{},
			action: ActionImplement, wantErr: true,
		},
		{
			name:       "PR path with its pull request",
			prompt:     prPrompt,
			ev:         Event{Type: EventPRSynced, Repo: "o/r", Kind: KindPR, Number: 9, Sender: "alice"},
			st:         State{PR: testPR(9, "open", true)},
			action:     ActionReview,
			wantSuffix: "https://forge.example.com/o/r/pulls/9",
		},
		{
			name:   "PR path with no pull request",
			prompt: prPrompt,
			ev:     Event{Type: EventPRSynced, Repo: "o/r", Kind: KindPR, Number: 9, Sender: "alice"},
			st:     State{},
			action: ActionReview, wantErr: true,
		},
		{
			// A prompt that asks for no URL is the deployment's choice;
			// the guard only refuses prompts that ask and get nothing.
			name:   "prompt naming no URL",
			prompt: PromptConfig{Prompt: "/housekeeping {{.Repo}}", Persona: "sweeper", Timeout: time.Hour},
			ev:     Event{Type: EventIssueOpened, Repo: "o/r", Kind: KindIssue, Number: 7, Sender: "alice"},
			st:     State{},
			action: ActionImplement,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng, _, q, _ := newTestEngine(t)
			eng.cfg.Actions[string(tc.action)] = tc.prompt
			err := eng.dispatch(tc.ev, tc.st, tc.action)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("dispatch succeeded, want it to refuse an invocation with no object")
				}
				if len(q.all()) != 0 {
					t.Errorf("dispatch enqueued %d task(s) despite naming no object", len(q.all()))
				}
				return
			}
			if err != nil {
				t.Fatalf("dispatch: %v", err)
			}
			tasks := q.all()
			if len(tasks) != 1 {
				t.Fatalf("got %d tasks, want 1", len(tasks))
			}
			if tc.wantSuffix == "" {
				return
			}
			if !strings.HasSuffix(tasks[0].Prompt, tc.wantSuffix) {
				t.Errorf("prompt = %q, want the invocation with its URL", tasks[0].Prompt)
			}
		})
	}
}

// TestHandler_IssueClosed_Cascade: a closed issue unblocks its open
// blocks whose blockers are all closed, and the cascade dispatches
// implement for them (§forgejo/decisions/unblock-cascade).
func TestHandler_IssueClosed_Cascade(t *testing.T) {
	eng, api, q, store := newTestEngine(t)
	h := NewHandler(eng, "s3cret", nil)
	// Issue 7 (closed) blocks issue 10 (open); 10's only blocker is 7.
	api.issues[7] = testIssue(7, "closed")
	api.issues[10] = testIssue(10, "open")
	api.blocks[7] = []Issue{*testIssue(10, "open")}
	api.deps[10] = []Issue{*testIssue(7, "closed")}

	rec := serve(t, h, signedRequest(t, "s3cret", "issues", "closed", issuePayload("o/r", 7, "closed")))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	tasks := q.all()
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks, want 1 (implement for issue 10)", len(tasks))
	}
	if !strings.Contains(tasks[0].Prompt, "kind=issue number=10") {
		t.Errorf("prompt = %q, want implement for issue 10", tasks[0].Prompt)
	}
	// The cascade dispatch records the cascade key's watermark.
	w, err := store.Load(Key{Repo: "o/r", Kind: KindIssue, Number: 10})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if w == nil || w.Action != string(ActionImplement) {
		t.Errorf("cascade watermark = %+v, want implement", w)
	}
}

// TestHandler_IssueClosed_CascadeWatermarkHolds: a cascade key that
// already has an implement watermark at the current revision is not
// re-dispatched.
func TestHandler_IssueClosed_CascadeWatermarkHolds(t *testing.T) {
	eng, api, q, store := newTestEngine(t)
	h := NewHandler(eng, "s3cret", nil)
	api.issues[7] = testIssue(7, "closed")
	api.issues[10] = testIssue(10, "open")
	api.blocks[7] = []Issue{*testIssue(10, "open")}
	api.deps[10] = []Issue{*testIssue(7, "closed")}
	if err := store.Save(Key{Repo: "o/r", Kind: KindIssue, Number: 10},
		Watermark{Action: string(ActionImplement), Revision: testUpdatedAt.UTC().Format(time.RFC3339)}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	rec := serve(t, h, signedRequest(t, "s3cret", "issues", "closed", issuePayload("o/r", 7, "closed")))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if len(q.all()) != 0 {
		t.Errorf("got %d tasks, want none (already dispatched)", len(q.all()))
	}
}

// TestCascadeHoldOffReason_QualifiesByTheKeyRepositoryRoles: a cascade
// hold-off is qualified by the roles of the repository holding its key,
// so it never promises wrap-up where those roles exclude it — and never
// names a role a failed listing merely failed to establish
// (§forgejo/webhook/repository-roles, §forgejo/decisions/unblock-cascade).
func TestCascadeHoldOffReason_QualifiesByTheKeyRepositoryRoles(t *testing.T) {
	base := "issue 2 is an open outcome tracker still blocked by other/repo#3; wrap-up held off"
	tests := []struct {
		name    string
		repo    string
		repos   []Repository
		listErr error
		want    []string
		deny    string
	}{
		{
			name:  "the key repository admits wrap-up: nothing qualifies the reason",
			repo:  "o/outcomes",
			repos: []Repository{testRepo("o/r"), testRepoOutcomes("o/outcomes")},
			deny:  "inadmissible",
		},
		{
			name:  "the key repository holds only the work-item role: the action and its role are named",
			repo:  "o/r",
			repos: []Repository{testRepo("o/r")},
			want:  []string{string(ActionWrapUp), maitredOutcomesTopic},
		},
		{
			name: "the key repository holds no role: the exclusion is named",
			repo: "o/off",
			repos: []Repository{
				testRepo("o/r"),
				{Owner: "o", Name: "off", FullName: "o/off"},
			},
			want: []string{"holds no maitred role"},
		},
		{
			name:    "a failed listing names the failure, never an exclusion",
			repo:    "o/r",
			listErr: errors.New("boom"),
			want:    []string{"scope of repo o/r unknown", "boom"},
			deny:    "holds no maitred role",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eng, api, _, _ := newTestEngine(t)
			api.repos = tt.repos
			api.listReposErr = tt.listErr
			got := eng.cascadeHoldOffReason(CascadeHoldOff{
				Key:    Key{Repo: tt.repo, Kind: KindIssue, Number: 2},
				Reason: base,
			})
			if !strings.HasPrefix(got, base) {
				t.Errorf("reason %q does not start from the decision's own reason", got)
			}
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Errorf("reason %q does not name %q", got, want)
				}
			}
			if tt.deny != "" && strings.Contains(got, tt.deny) {
				t.Errorf("reason %q names %q, want it to stay silent on the roles", got, tt.deny)
			}
		})
	}
}

// TestHandler_IssueClosed_CascadeWrapUpInadmissibleHoldsOff: the cascade
// classifies an outcome tracker as wrap-up, and wrap-up is
// inadmissible in a repository holding only the work-item role: the
// hold-off names the action and the required role
// (§forgejo/webhook/repository-roles, §forgejo/decisions/unblock-cascade).
func TestHandler_IssueClosed_CascadeWrapUpInadmissibleHoldsOff(t *testing.T) {
	eng, api, q, _ := newTestEngine(t)
	h := NewHandler(eng, "s3cret", nil)
	api.issues[7] = testIssue(7, "closed")
	tracker := testIssue(10, "open")
	tracker.Labels = append(tracker.Labels, LabelOutcome)
	api.issues[10] = tracker
	api.blocks[7] = []Issue{*tracker}
	api.deps[10] = []Issue{*testIssue(7, "closed")}

	rec := serve(t, h, signedRequest(t, "s3cret", "issues", "closed", issuePayload("o/r", 7, "closed")))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if tasks := q.all(); len(tasks) != 0 {
		t.Errorf("got %d tasks, want none (wrap-up is inadmissible in a work-item repository)", len(tasks))
	}
	d := decisionFor(t, eng, Key{Repo: "o/r", Kind: KindIssue, Number: 10})
	if d.Dispatched {
		t.Errorf("decision log records a dispatch of %q, want a hold-off", d.Action)
	}
	for _, want := range []string{string(ActionWrapUp), maitredOutcomesTopic} {
		if !strings.Contains(d.Reason, want) {
			t.Errorf("reason %q does not name %q", d.Reason, want)
		}
	}
}

// TestHandler_PRMerged_Cascade: a merged PR's connected issue is the
// cascade root; its open blocks whose blockers are all closed are
// dispatched.
func TestHandler_PRMerged_Cascade(t *testing.T) {
	eng, api, q, _ := newTestEngine(t)
	h := NewHandler(eng, "s3cret", nil)
	// PR 9 (merged) blocks issue 7 (closed); 7 blocks issue 10 (open),
	// whose only blocker is 7.
	api.pulls[9] = testPR(9, "closed", true)
	api.pulls[9].Merged = true
	api.blocks[9] = []Issue{*testIssue(7, "closed")}
	api.issues[7] = testIssue(7, "closed")
	api.issues[10] = testIssue(10, "open")
	api.blocks[7] = []Issue{*testIssue(10, "open")}
	api.deps[10] = []Issue{*testIssue(7, "closed")}

	rec := serve(t, h, signedRequest(t, "s3cret", "pull_request", "closed", prPayload("o/r", 9, "closed")))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	tasks := q.all()
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks, want 1 (implement for issue 10)", len(tasks))
	}
	if !strings.Contains(tasks[0].Prompt, "kind=issue number=10") {
		t.Errorf("prompt = %q, want implement for issue 10", tasks[0].Prompt)
	}
}

// TestHandler_PRMerged_ConnectedIssueStillOpen: the cascade walks the
// connected issue's blocks; a block still blocked by the open connected
// issue is not dispatched.
func TestHandler_PRMerged_ConnectedIssueStillOpen(t *testing.T) {
	eng, api, q, _ := newTestEngine(t)
	h := NewHandler(eng, "s3cret", nil)
	api.pulls[9] = testPR(9, "closed", true)
	api.pulls[9].Merged = true
	api.blocks[9] = []Issue{*testIssue(7, "open")}
	api.issues[7] = testIssue(7, "open")
	api.issues[10] = testIssue(10, "open")
	api.blocks[7] = []Issue{*testIssue(10, "open")}
	api.deps[10] = []Issue{*testIssue(7, "open")}

	rec := serve(t, h, signedRequest(t, "s3cret", "pull_request", "closed", prPayload("o/r", 9, "closed")))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if len(q.all()) != 0 {
		t.Errorf("got %d tasks, want none (issue 10 still blocked)", len(q.all()))
	}
}

func TestHandler_CrossRepoCascade(t *testing.T) {
	eng, api, q, _ := newTestEngine(t)
	h := NewHandler(eng, "s3cret", nil)
	// Issue 7 in o/r (closed) blocks issue 10 in o/other (open); both
	// repositories are maitred-enabled, so the cascade may dispatch in
	// either (§forgejo/webhook/the-scope-cache).
	api.repos = append(api.repos, testRepo("o/other"))
	api.issues[7] = testIssue(7, "closed")
	cross := testIssue(10, "open")
	cross.Repository = "o/other"
	cross.HTMLURL = "https://forge.example.com/o/other/issues/10"
	api.issues[10] = cross
	api.blocks[7] = []Issue{*cross}
	crossBlocker := testIssue(7, "closed")
	api.deps[10] = []Issue{*crossBlocker}

	rec := serve(t, h, signedRequest(t, "s3cret", "issues", "closed", issuePayload("o/r", 7, "closed")))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	tasks := q.all()
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks, want 1", len(tasks))
	}
	if !strings.Contains(tasks[0].Prompt, "repo=o/other kind=issue number=10") {
		t.Errorf("prompt = %q, want implement for o/other issue 10", tasks[0].Prompt)
	}
}

// decisionFor returns the recorded decision for one key
// (§forgejo/observability/the-decision-log).
func decisionFor(t *testing.T, eng *Engine, k Key) DecisionRecord {
	t.Helper()
	for _, tr := range eng.Tracked() {
		if tr.Key == k {
			return tr.Decision
		}
	}
	t.Fatalf("no decision recorded for %s", k.String())
	return DecisionRecord{}
}

// trackerFixtureCrossRepo returns an open outcome tracker in repo,
// blocked only by the closed issue 7 of o/r
// (§forgejo/decisions/unblock-cascade).
func trackerFixtureCrossRepo(repo string) *Issue {
	tracker := testIssue(10, "open")
	tracker.Labels = append(tracker.Labels, LabelOutcome)
	tracker.Repository = repo
	tracker.HTMLURL = "https://forge.example.com/" + repo + "/issues/10"
	return tracker
}

// TestHandler_IssueClosed_CascadeWrapUpAcrossRoleBoundary: a cascade
// rooted at an issue in a work-item repository newly unblocks an
// outcome tracker in an outcomes repository — the emission is
// wrap-up, admissible where the key lives, and the tracker key's
// watermark records wrap-up (§forgejo/decisions/unblock-cascade,
// §forgejo/webhook/repository-roles).
func TestHandler_IssueClosed_CascadeWrapUpAcrossRoleBoundary(t *testing.T) {
	eng, api, q, store := newTestEngine(t)
	h := NewHandler(eng, "s3cret", nil)
	api.repos = append(api.repos, testRepoOutcomes("o/outcomes"))
	api.issues[7] = testIssue(7, "closed")
	tracker := trackerFixtureCrossRepo("o/outcomes")
	api.issues[10] = tracker
	api.blocks[7] = []Issue{*tracker}
	api.deps[10] = []Issue{*testIssue(7, "closed")}

	rec := serve(t, h, signedRequest(t, "s3cret", "issues", "closed", issuePayload("o/r", 7, "closed")))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	tasks := q.all()
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks, want 1 (wrap-up for tracker 10)", len(tasks))
	}
	if !strings.Contains(tasks[0].Prompt, "action=wrap-up repo=o/outcomes kind=issue number=10") {
		t.Errorf("prompt = %q, want the wrap-up dispatch for o/outcomes issue 10", tasks[0].Prompt)
	}
	w, err := store.Load(Key{Repo: "o/outcomes", Kind: KindIssue, Number: 10})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if w == nil || w.Action != string(ActionWrapUp) {
		t.Errorf("tracker watermark = %+v, want wrap-up", w)
	}
}

// TestHandler_IssueClosed_CascadeWrapUpWatermarkHolds: the cascade's
// wrap-up emission is guarded by the key's own revision-keyed
// watermark — unlike the sweep's exempt row, a cascade does not
// re-fire wrap-up at an already-dispatched revision
// (§forgejo/reconciliation/watermark-exemptions).
func TestHandler_IssueClosed_CascadeWrapUpWatermarkHolds(t *testing.T) {
	eng, api, q, store := newTestEngine(t)
	h := NewHandler(eng, "s3cret", nil)
	api.repos = append(api.repos, testRepoOutcomes("o/outcomes"))
	api.issues[7] = testIssue(7, "closed")
	tracker := trackerFixtureCrossRepo("o/outcomes")
	api.issues[10] = tracker
	api.blocks[7] = []Issue{*tracker}
	api.deps[10] = []Issue{*testIssue(7, "closed")}
	if err := store.Save(Key{Repo: "o/outcomes", Kind: KindIssue, Number: 10},
		Watermark{Action: string(ActionWrapUp), Revision: testUpdatedAt.UTC().Format(time.RFC3339)}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	rec := serve(t, h, signedRequest(t, "s3cret", "issues", "closed", issuePayload("o/r", 7, "closed")))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if n := len(q.all()); n != 0 {
		t.Errorf("got %d tasks, want none (wrap-up already dispatched at the revision)", n)
	}
}

// TestHandler_IssueClosed_CascadeBlockedTrackerNamesBlockers: closing
// one of a tracker's blockers records a cascade hold-off naming the
// blockers that remain open, and dispatches nothing. The record carries
// the tracker's own state and revision — an unrelated close event must
// not blank the tracker's dashboard row — and names that wrap-up is
// inadmissible in a repository holding only the work-item role
// (§forgejo/decisions/unblock-cascade, §forgejo/observability/the-decision-log,
// §forgejo/webhook/repository-roles).
func TestHandler_IssueClosed_CascadeBlockedTrackerNamesBlockers(t *testing.T) {
	eng, api, q, _ := newTestEngine(t)
	h := NewHandler(eng, "s3cret", nil)
	api.issues[7] = testIssue(7, "closed")
	api.issues[11] = testIssue(11, "open")
	tracker := trackerFixtureCrossRepo("o/r")
	api.issues[10] = tracker
	api.blocks[7] = []Issue{*tracker}
	api.deps[10] = []Issue{*testIssue(7, "closed"), *testIssue(11, "open")}

	rec := serve(t, h, signedRequest(t, "s3cret", "issues", "closed", issuePayload("o/r", 7, "closed")))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if n := len(q.all()); n != 0 {
		t.Errorf("got %d tasks, want none (the tracker is still blocked)", n)
	}
	d := decisionFor(t, eng, Key{Repo: "o/r", Kind: KindIssue, Number: 10})
	if d.Dispatched {
		t.Fatalf("decision log records a dispatch of %q, want a hold-off", d.Action)
	}
	for _, want := range []string{LabelOutcome, "o/r#11"} {
		if !strings.Contains(d.Reason, want) {
			t.Errorf("reason %q does not name %q", d.Reason, want)
		}
	}
	// The tracker's own state and revision ride the record: the hold-off
	// must not downgrade the dashboard row of an object it did not decide
	// (§forgejo/observability/the-dashboard-view).
	if d.State != "open" {
		t.Errorf("record state = %q, want open (the tracker's own state)", d.State)
	}
	if want := testUpdatedAt.UTC().Format(time.RFC3339); d.Revision != want {
		t.Errorf("record revision = %q, want %s (the tracker's own revision)", d.Revision, want)
	}
	// o/r holds only the work-item role, so the record must not promise a
	// wrap-up those roles exclude.
	for _, want := range []string{string(ActionWrapUp), maitredOutcomesTopic} {
		if !strings.Contains(d.Reason, want) {
			t.Errorf("reason %q does not name the inadmissible %q", d.Reason, want)
		}
	}
}

// TestHandler_IssueClosed_CascadeBlockedTrackerAdmissibleReason: the same
// hold-off for a tracker in an outcomes repository names only its open
// blockers — the roles admit wrap-up there, so nothing qualifies the
// reason (§forgejo/webhook/repository-roles, §forgejo/decisions/unblock-cascade).
func TestHandler_IssueClosed_CascadeBlockedTrackerAdmissibleReason(t *testing.T) {
	eng, api, q, _ := newTestEngine(t)
	h := NewHandler(eng, "s3cret", nil)
	api.repos = append(api.repos, testRepoOutcomes("o/outcomes"))
	api.issues[7] = testIssue(7, "closed")
	tracker := trackerFixtureCrossRepo("o/outcomes")
	api.issues[10] = tracker
	blocker := testIssue(11, "open")
	blocker.Repository = "o/outcomes"
	api.blocks[7] = []Issue{*tracker}
	api.deps[10] = []Issue{*testIssue(7, "closed"), *blocker}

	rec := serve(t, h, signedRequest(t, "s3cret", "issues", "closed", issuePayload("o/r", 7, "closed")))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if n := len(q.all()); n != 0 {
		t.Errorf("got %d tasks, want none (the tracker is still blocked)", n)
	}
	d := decisionFor(t, eng, Key{Repo: "o/outcomes", Kind: KindIssue, Number: 10})
	if d.Dispatched {
		t.Fatalf("decision log records a dispatch of %q, want a hold-off", d.Action)
	}
	if !strings.Contains(d.Reason, "o/outcomes#11") {
		t.Errorf("reason %q does not name the open blocker o/outcomes#11", d.Reason)
	}
	if strings.Contains(d.Reason, "inadmissible") {
		t.Errorf("reason %q qualifies an action the roles admit", d.Reason)
	}
}

// TestHandler_ClosedTracker_CascadeEmitsNothing: closing the tracker
// roots its own cascade — its members are closed, the walk cycles
// back to the visited root, and nothing is emitted. Termination is
// tested, not assumed (§forgejo/decisions/unblock-cascade).
func TestHandler_ClosedTracker_CascadeEmitsNothing(t *testing.T) {
	eng, api, q, _ := newTestEngine(t)
	h := NewHandler(eng, "s3cret", nil)
	tracker := trackerFixtureCrossRepo("o/r")
	tracker.State = "closed"
	api.issues[10] = tracker
	api.issues[8] = testIssue(8, "closed")
	api.blocks[10] = []Issue{*testIssue(8, "closed")}
	api.blocks[8] = []Issue{*tracker}

	rec := serve(t, h, signedRequest(t, "s3cret", "issues", "closed", issuePayload("o/r", 10, "closed")))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if n := len(q.all()); n != 0 {
		t.Errorf("got %d tasks, want none (a closed tracker cascades to nothing)", n)
	}
}

// TestHandler_IssueClosed_CascadeTrackerNeverGetsImplement: a
// tracker the graph mislabels (the outcome label arrived after the
// graph walk) still never receives implement — the re-fetched state
// wins and the reserved-label insurance holds the emission off
// (§forgejo/decisions/transition-table, §forgejo/decisions/unblock-cascade).
func TestHandler_IssueClosed_CascadeTrackerNeverGetsImplement(t *testing.T) {
	eng, api, q, store := newTestEngine(t)
	h := NewHandler(eng, "s3cret", nil)
	api.issues[7] = testIssue(7, "closed")
	tracker := trackerFixtureCrossRepo("o/r")
	api.issues[10] = tracker
	// The graph node carries no labels: the walk classifies it as
	// implement; the re-fetch says otherwise.
	api.blocks[7] = []Issue{{Number: 10, State: "open", Repository: "o/r"}}
	api.deps[10] = []Issue{*testIssue(7, "closed")}

	rec := serve(t, h, signedRequest(t, "s3cret", "issues", "closed", issuePayload("o/r", 7, "closed")))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if n := len(q.all()); n != 0 {
		t.Errorf("got %d tasks, want none (implement is never dispatched for a tracker)", n)
	}
	w, err := store.Load(Key{Repo: "o/r", Kind: KindIssue, Number: 10})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if w != nil {
		t.Errorf("watermark = %+v, want none (nothing was dispatched)", w)
	}
	d := decisionFor(t, eng, Key{Repo: "o/r", Kind: KindIssue, Number: 10})
	if !strings.Contains(d.Reason, LabelOutcome) {
		t.Errorf("reason %q does not name the %s label", d.Reason, LabelOutcome)
	}
}

// TestHandler_IssueClosed_CascadeWrapUpLabelGoneHoldsOff: a wrap-up
// emission whose re-fetched issue no longer carries the outcome label
// holds off saying so — the state wins over the graph data, and the
// sweep decides the issue afresh (§forgejo/decisions/unblock-cascade).
func TestHandler_IssueClosed_CascadeWrapUpLabelGoneHoldsOff(t *testing.T) {
	eng, api, q, _ := newTestEngine(t)
	h := NewHandler(eng, "s3cret", nil)
	api.repos = append(api.repos, testRepoOutcomes("o/outcomes"))
	api.issues[7] = testIssue(7, "closed")
	tracker := trackerFixtureCrossRepo("o/outcomes")
	api.blocks[7] = []Issue{*tracker}
	// The label was removed before the dispatch: the re-fetch reads a
	// plain issue.
	plain := testIssue(10, "open")
	plain.Repository = "o/outcomes"
	api.issues[10] = plain
	api.deps[10] = []Issue{*testIssue(7, "closed")}

	rec := serve(t, h, signedRequest(t, "s3cret", "issues", "closed", issuePayload("o/r", 7, "closed")))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if n := len(q.all()); n != 0 {
		t.Errorf("got %d tasks, want none (the state no longer calls for wrap-up)", n)
	}
	d := decisionFor(t, eng, Key{Repo: "o/outcomes", Kind: KindIssue, Number: 10})
	if !strings.Contains(d.Reason, LabelOutcome) || !strings.Contains(d.Reason, "no longer carries") {
		t.Errorf("reason %q does not state that the outcome label is gone", d.Reason)
	}
}

// TestHandler_IssueOpened_ConnectedPR_HoldsOff: an opened issue that
// already has a connected open PR is held off (the PR path owns it);
// this also covers the PR projection of a pull-request blocker.
func TestHandler_IssueOpened_ConnectedPR_HoldsOff(t *testing.T) {
	eng, api, q, _ := newTestEngine(t)
	h := NewHandler(eng, "s3cret", nil)
	api.issues[7] = testIssue(7, "open")
	prBlocker := testIssue(9, "open")
	prBlocker.IsPull = true
	api.deps[7] = []Issue{*prBlocker}

	rec := serve(t, h, signedRequest(t, "s3cret", "issues", "opened", issuePayload("o/r", 7, "opened")))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if n := len(q.all()); n != 0 {
		t.Errorf("got %d tasks, want 0 (connected open PR)", n)
	}
}

// TestHandler_Cascade_Transitive: the cascade walks through closed
// blockers — closing 7 (which blocks 8, already closed, which blocks
// 9) unblocks 9.
func TestHandler_Cascade_Transitive(t *testing.T) {
	eng, api, q, _ := newTestEngine(t)
	h := NewHandler(eng, "s3cret", nil)
	api.issues[7] = testIssue(7, "closed")
	api.issues[8] = testIssue(8, "closed")
	api.issues[9] = testIssue(9, "open")
	api.blocks[7] = []Issue{*testIssue(8, "closed")}
	api.blocks[8] = []Issue{*testIssue(9, "open")}
	api.deps[9] = []Issue{*testIssue(8, "closed")}

	rec := serve(t, h, signedRequest(t, "s3cret", "issues", "closed", issuePayload("o/r", 7, "closed")))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	tasks := q.all()
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks, want 1", len(tasks))
	}
	if !strings.Contains(tasks[0].Prompt, "number=9") {
		t.Errorf("prompt = %q, want implement for issue 9", tasks[0].Prompt)
	}
}

// TestHandler_Cascade_Cycle: a cycle in the blocker graph terminates
// (visited set) and dispatches nothing.
func TestHandler_Cascade_Cycle(t *testing.T) {
	eng, api, q, _ := newTestEngine(t)
	h := NewHandler(eng, "s3cret", nil)
	api.issues[7] = testIssue(7, "closed")
	api.issues[8] = testIssue(8, "closed")
	api.blocks[7] = []Issue{*testIssue(8, "closed")}
	api.blocks[8] = []Issue{*testIssue(7, "closed")}

	rec := serve(t, h, signedRequest(t, "s3cret", "issues", "closed", issuePayload("o/r", 7, "closed")))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if n := len(q.all()); n != 0 {
		t.Errorf("got %d tasks, want 0 (cycle)", n)
	}
}

// TestHandler_Cascade_MissingIssue: a blocked issue that no longer
// exists in the API is skipped (the re-fetch error is logged).
func TestHandler_Cascade_MissingIssue(t *testing.T) {
	eng, api, q, _ := newTestEngine(t)
	h := NewHandler(eng, "s3cret", nil)
	api.issues[7] = testIssue(7, "closed")
	api.blocks[7] = []Issue{*testIssue(8, "open")} // 8 is absent from the API

	rec := serve(t, h, signedRequest(t, "s3cret", "issues", "closed", issuePayload("o/r", 7, "closed")))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if n := len(q.all()); n != 0 {
		t.Errorf("got %d tasks, want 0 (missing issue)", n)
	}
}

// TestHandler_Cascade_ClosedInMeantime: a block that is closed by the
// time the cascade re-fetches it is not dispatched.
func TestHandler_Cascade_ClosedInMeantime(t *testing.T) {
	eng, api, q, _ := newTestEngine(t)
	h := NewHandler(eng, "s3cret", nil)
	api.issues[7] = testIssue(7, "closed")
	api.issues[8] = testIssue(8, "closed") // the graph says open, the re-fetch says closed
	api.blocks[7] = []Issue{*testIssue(8, "open")}

	rec := serve(t, h, signedRequest(t, "s3cret", "issues", "closed", issuePayload("o/r", 7, "closed")))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if n := len(q.all()); n != 0 {
		t.Errorf("got %d tasks, want 0 (closed in the meantime)", n)
	}
}

// TestHandler_CorruptWatermark_Continues: a corrupt on-disk watermark
// is treated as lost (logged, continued without) and re-saved after a
// successful dispatch (§forgejo/state/lossiness).
func TestHandler_CorruptWatermark_Continues(t *testing.T) {
	eng, api, q, store := newTestEngine(t)
	h := NewHandler(eng, "s3cret", nil)
	api.issues[7] = testIssue(7, "open")
	key := Key{Repo: "o/r", Kind: KindIssue, Number: 7}
	if err := os.WriteFile(filepath.Join(store.dir, key.fileName()), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write corrupt watermark: %v", err)
	}

	rec := serve(t, h, signedRequest(t, "s3cret", "issues", "opened", issuePayload("o/r", 7, "opened")))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if n := len(q.all()); n != 1 {
		t.Fatalf("got %d tasks, want 1", n)
	}
	w, err := store.Load(key)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if w == nil || w.Action != string(ActionImplement) {
		t.Errorf("watermark = %+v, want implement re-saved", w)
	}
}

// TestNewEngine_DefaultLogger: a nil logger falls back to the default.
func TestNewEngine_DefaultLogger(t *testing.T) {
	eng := NewEngine(&fakeAPI{}, nil, testConfig(), nil, nil)
	if eng == nil || eng.log == nil {
		t.Fatal("NewEngine: want engine with default logger")
	}
}

func TestSplitRepo(t *testing.T) {
	if o, n := splitRepo("noslash"); o != "noslash" || n != "" {
		t.Errorf("splitRepo(noslash) = (%q, %q), want (noslash, \"\")", o, n)
	}
	if o, n := splitRepo("o/r"); o != "o" || n != "r" {
		t.Errorf("splitRepo(o/r) = (%q, %q), want (o, r)", o, n)
	}
}

func TestHandler_QueueFailure_HoldsOff(t *testing.T) {
	eng, api, q, store := newTestEngine(t)
	h := NewHandler(eng, "s3cret", nil)
	api.issues[7] = testIssue(7, "open")
	q.err = errors.New("queue down")

	rec := serve(t, h, signedRequest(t, "s3cret", "issues", "opened", issuePayload("o/r", 7, "opened")))
	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rec.Code)
	}
	w, err := store.Load(Key{Repo: "o/r", Kind: KindIssue, Number: 7})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if w != nil {
		t.Errorf("watermark recorded despite failed dispatch: %+v", w)
	}
}

// TestHandler_Concurrent_SameKey: concurrent deliveries for the same key
// are serialised on the watermark store's per-key lock, so an
// idempotent event dispatches exactly once.
func TestHandler_Concurrent_SameKey(t *testing.T) {
	eng, api, q, _ := newTestEngine(t)
	h := NewHandler(eng, "s3cret", nil)
	api.issues[7] = testIssue(7, "open")

	const n = 10
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			serve(t, h, signedRequest(t, "s3cret", "issues", "opened", issuePayload("o/r", 7, "opened")))
		}()
	}
	wg.Wait()

	if got := len(q.all()); got != 1 {
		t.Errorf("got %d tasks, want 1 (serialised on the key lock)", got)
	}
}

// TestHandler_Concurrent_DifferentKeys: events for different keys
// proceed in parallel.
func TestHandler_Concurrent_DifferentKeys(t *testing.T) {
	eng, api, q, _ := newTestEngine(t)
	h := NewHandler(eng, "s3cret", nil)
	api.issues[7] = testIssue(7, "open")
	api.issues[8] = testIssue(8, "open")

	var wg sync.WaitGroup
	for _, n := range []int{7, 8} {
		wg.Add(1)
		go func(number int) {
			defer wg.Done()
			serve(t, h, signedRequest(t, "s3cret", "issues", "opened", issuePayload("o/r", number, "opened")))
		}(n)
	}
	wg.Wait()

	if got := len(q.all()); got != 2 {
		t.Errorf("got %d tasks, want 2", got)
	}
}

func TestVerifySignature(t *testing.T) {
	body := []byte(`{"action":"opened"}`)
	valid := sign("s3cret", body)

	if !verifySignature([]byte("s3cret"), body, valid) {
		t.Error("valid signature rejected")
	}
	if verifySignature([]byte("wrong"), body, valid) {
		t.Error("wrong secret accepted")
	}
	if verifySignature([]byte("s3cret"), body, "") {
		t.Error("empty signature accepted")
	}
	if verifySignature(nil, body, valid) {
		t.Error("nil secret accepted")
	}
	if verifySignature([]byte("s3cret"), body, "not-hex") {
		t.Error("non-hex signature accepted")
	}
}

func TestMapEvent(t *testing.T) {
	cases := []struct {
		eventType string
		action    string
		payload   map[string]any
		want      Event
		ok        bool
	}{
		{
			"issues", "opened", issuePayload("o/r", 7, "opened"),
			Event{Type: EventIssueOpened, Repo: "o/r", Kind: KindIssue, Number: 7, Sender: "alice"},
			true,
		},
		{
			"issues", "edited", issuePayload("o/r", 7, "edited"),
			Event{Type: EventIssueEdited, Repo: "o/r", Kind: KindIssue, Number: 7, Sender: "alice"},
			true,
		},
		{
			"issues", "closed", issuePayload("o/r", 7, "closed"),
			Event{Type: EventIssueClosed, Repo: "o/r", Kind: KindIssue, Number: 7, Sender: "alice"},
			true,
		},
		{
			"issue_label", "label_updated", issuePayload("o/r", 7, "label_updated"),
			Event{Type: EventIssueLabelsChanged, Repo: "o/r", Kind: KindIssue, Number: 7, Sender: "alice"},
			true,
		},
		{
			"issue_label", "label_cleared", issuePayload("o/r", 7, "label_cleared"),
			Event{Type: EventIssueLabelsChanged, Repo: "o/r", Kind: KindIssue, Number: 7, Sender: "alice"},
			true,
		},
		{
			"issue_comment", "created", issuePayload("o/r", 7, "created"),
			Event{Type: EventIssueCommented, Repo: "o/r", Kind: KindIssue, Number: 7, Sender: "alice"},
			true,
		},
		{
			"pull_request", "opened", prPayload("o/r", 9, "opened"),
			Event{Type: EventPROpened, Repo: "o/r", Kind: KindPR, Number: 9, Sender: "carol"},
			true,
		},
		{
			"pull_request", "closed", prPayload("o/r", 9, "closed"),
			Event{Type: EventPRClosed, Repo: "o/r", Kind: KindPR, Number: 9, Sender: "carol"},
			true,
		},
		{
			"pull_request_sync", "synchronized", prPayload("o/r", 9, "synchronized"),
			Event{Type: EventPRSynced, Repo: "o/r", Kind: KindPR, Number: 9, Sender: "carol"},
			true,
		},
		{
			"pull_request_review_approved", "approved", prPayload("o/r", 9, "approved"),
			Event{Type: EventPRReviewed, Repo: "o/r", Kind: KindPR, Number: 9, Sender: "carol"},
			true,
		},
		{
			"pull_request_review_rejected", "rejected", prPayload("o/r", 9, "rejected"),
			Event{Type: EventPRReviewed, Repo: "o/r", Kind: KindPR, Number: 9, Sender: "carol"},
			true,
		},
		{
			"pull_request_review_comment", "created", prPayload("o/r", 9, "created"),
			Event{Type: EventPRReviewed, Repo: "o/r", Kind: KindPR, Number: 9, Sender: "carol"},
			true,
		},
		{
			"pull_request_comment", "created", prPayload("o/r", 9, "created"),
			Event{Type: EventPRCommented, Repo: "o/r", Kind: KindPR, Number: 9, Sender: "carol"},
			true,
		},
		{"push", "", map[string]any{}, Event{}, false},
		{"issues", "reopened", issuePayload("o/r", 7, "reopened"), Event{}, false},
		{"pull_request", "edited", prPayload("o/r", 9, "edited"), Event{}, false},
		{"unknown", "", map[string]any{}, Event{}, false},
	}

	for _, tc := range cases {
		t.Run(fmt.Sprintf("%s/%s", tc.eventType, tc.action), func(t *testing.T) {
			ev, ok := mapEvent(tc.eventType, wireEventFromPayload(t, tc.payload))
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v (event %+v)", ok, tc.ok, ev)
			}
			if tc.ok && ev != tc.want {
				t.Errorf("event = %+v, want %+v", ev, tc.want)
			}
		})
	}
}

func wireEventFromPayload(t *testing.T, payload map[string]any) wireEvent {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var p wireEvent
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return p
}

// TestDispatchCascade_ReservedLabelHeldOff: a newly unblocked issue
// carrying a reserved label (here human-task) is never an implement
// dispatch — the cascade hold-off names the label and is recorded in
// the decision log for the cascade key
// (§forgejo/decisions/unblock-cascade, §forgejo/decisions/transition-table).
func TestDispatchCascade_ReservedLabelHeldOff(t *testing.T) {
	var log bytes.Buffer
	api := newFakeAPI()
	api.repos = []Repository{testRepo("o/r")}
	q := &fakeQueue{}
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	eng := NewEngine(api, store, testConfig(), q, slog.New(slog.NewJSONHandler(&log, nil)))
	closed := &Issue{Number: 7, State: "closed", UpdatedAt: testUpdatedAt, Repository: "o/r"}
	api.issues[7] = closed
	unblocked := &Issue{Number: 8, State: "open", UpdatedAt: testUpdatedAt, Repository: "o/r", Labels: []string{LabelHumanTask}}
	api.issues[8] = unblocked
	api.blocks[7] = []Issue{*unblocked}
	api.deps[8] = []Issue{*closed}

	eng.HandleEvent(Event{Type: EventIssueClosed, Repo: "o/r", Kind: KindIssue, Number: 7, Sender: "alice"})

	if tasks := q.all(); len(tasks) != 0 {
		t.Fatalf("got %d cascade tasks for a human-ticketed issue, want none", len(tasks))
	}
	var rec map[string]any
	for _, e := range parseLogLines(t, log.Bytes()) {
		if e["msg"] == "decision" && e["key"] == "o/r/issue/8" {
			rec = e
		}
	}
	if rec == nil {
		t.Fatalf("cascade key has no decision-log entry, want its hold-off recorded")
	}
	reason, _ := rec["reason"].(string)
	if !strings.Contains(reason, "human-task") || !strings.Contains(reason, "never an implement candidate") {
		t.Errorf("cascade hold-off reason = %q, want it to name the label and the denial", reason)
	}
	w, err := store.Load(Key{Repo: "o/r", Kind: KindIssue, Number: 8})
	if err != nil {
		t.Fatalf("load watermark: %v", err)
	}
	if w != nil {
		t.Errorf("watermark = %+v, want none", w)
	}
}

// --- The merge gate (§forgejo/decisions/merge-gate) -------------------------------------------

// mergeGateFixtures wires PR 9 (open, mergeable, approved at head) whose
// connected issue 7 it blocks; issue 7 is blocked by open issue 3 and
// by PR 9 itself (the delivery record). It returns the pieces a merge
// gate test needs.
func mergeGateFixtures(t *testing.T) (*Engine, *fakeAPI, *fakeQueue, *Store, *bytes.Buffer) {
	t.Helper()
	var log bytes.Buffer
	api := newFakeAPI()
	api.repos = []Repository{testRepo("o/r")}
	q := &fakeQueue{}
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	eng := NewEngine(api, store, testConfig(), q, slog.New(slog.NewJSONHandler(&log, nil)))
	api.pulls[9] = testPR(9, "open", true)
	api.reviews[9] = []Review{{Author: "bob", Event: ReviewApproved, CommitID: "sha-9"}}
	api.blocks[9] = []Issue{*testIssue(7, "open")}
	prSelf := testIssue(9, "open")
	prSelf.IsPull = true
	api.deps[7] = []Issue{*testIssue(3, "open"), *prSelf}
	return eng, api, q, store, &log
}

func reasonFor(t *testing.T, log *bytes.Buffer, key string) string {
	t.Helper()
	for _, e := range parseLogLines(t, log.Bytes()) {
		if e["msg"] == "decision" && e["key"] == key {
			reason, _ := e["reason"].(string)
			return reason
		}
	}
	return ""
}

// TestHandler_PRReviewed_ConnectedIssueBlockerHoldsOffMerge: an
// approved, mergeable PR whose connected issue has an open blocker
// dispatches no merge-or-wait on the review-event path; the hold-off
// names the open blockers and no watermark is recorded
// (§forgejo/decisions/merge-gate).
func TestHandler_PRReviewed_ConnectedIssueBlockerHoldsOffMerge(t *testing.T) {
	eng, api, q, store, log := mergeGateFixtures(t)
	h := NewHandler(eng, "s3cret", nil)

	rec := serve(t, h, signedRequest(t, "s3cret", "pull_request_review_approved", "", prPayload("o/r", 9, "")))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if tasks := q.all(); len(tasks) != 0 {
		t.Fatalf("got %d tasks, want none (connected issue blocked); first = %+v", len(tasks), tasks)
	}
	reason := reasonFor(t, log, "o/r/pr/9")
	if !strings.Contains(reason, "o/r#3") {
		t.Errorf("decision-log reason %q does not name the open blocker o/r#3", reason)
	}
	if n := api.callCount("IssueBlocks:r/9"); n != 1 {
		t.Errorf("IssueBlocks on the PR = %d, want 1", n)
	}
	if n := api.callCount("IssueDependencies:r/7"); n != 1 {
		t.Errorf("IssueDependencies on the connected issue = %d, want 1", n)
	}
	w, err := store.Load(Key{Repo: "o/r", Kind: KindPR, Number: 9})
	if err != nil {
		t.Fatalf("load watermark: %v", err)
	}
	if w != nil {
		t.Errorf("watermark = %+v, want none", w)
	}
}

// TestHandler_PRReviewed_ConnectedIssueSelfEntryMerges: the PR's own
// entry among the connected issue's blockers is not a blocker — with
// every other blocker closed the approved PR dispatches merge-or-wait
// (§forgejo/decisions/merge-gate).
func TestHandler_PRReviewed_ConnectedIssueSelfEntryMerges(t *testing.T) {
	eng, api, q, store, _ := mergeGateFixtures(t)
	prSelf := testIssue(9, "open")
	prSelf.IsPull = true
	api.deps[7] = []Issue{*testIssue(3, "closed"), *prSelf}
	h := NewHandler(eng, "s3cret", nil)

	rec := serve(t, h, signedRequest(t, "s3cret", "pull_request_review_approved", "", prPayload("o/r", 9, "")))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	tasks := q.all()
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks, want 1 (merge-or-wait despite the self entry)", len(tasks))
	}
	if !strings.Contains(tasks[0].Prompt, "action=merge-or-wait") {
		t.Errorf("prompt = %q, want merge-or-wait", tasks[0].Prompt)
	}
	w, err := store.Load(Key{Repo: "o/r", Kind: KindPR, Number: 9})
	if err != nil {
		t.Fatalf("load watermark: %v", err)
	}
	if w == nil || w.Action != string(ActionMergeOrWait) || w.Revision != "sha-9" {
		t.Errorf("watermark = %+v, want merge-or-wait at sha-9", w)
	}
}

// TestHandler_PRReviewed_MergeGateFetchFailureHoldsOff: a failed
// dependency read on the merge path is a hold-off — never a silent
// "no blockers" (§forgejo/webhook/re-fetch, §forgejo/decisions/merge-gate).
func TestHandler_PRReviewed_MergeGateFetchFailureHoldsOff(t *testing.T) {
	eng, api, q, store, _ := mergeGateFixtures(t)
	api.depsErr = errors.New("dependency listing failed")
	h := NewHandler(eng, "s3cret", nil)

	rec := serve(t, h, signedRequest(t, "s3cret", "pull_request_review_approved", "", prPayload("o/r", 9, "")))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if tasks := q.all(); len(tasks) != 0 {
		t.Fatalf("got %d tasks, want none (failed gate read holds off)", len(tasks))
	}
	w, _ := store.Load(Key{Repo: "o/r", Kind: KindPR, Number: 9})
	if w != nil {
		t.Errorf("watermark = %+v, want none", w)
	}
}

// TestHandler_PRReviewed_ConnectedIssueInAnotherRepo: the gate's hop
// reads the dependency rows in the connected issue's own repository,
// never the event's (§forgejo/webhook/re-fetch).
func TestHandler_PRReviewed_ConnectedIssueInAnotherRepo(t *testing.T) {
	eng, api, q, _, log := mergeGateFixtures(t)
	cross := testIssue(7, "open")
	cross.Repository = "o/other"
	api.blocks[9] = []Issue{*cross}

	rec := serve(t, NewHandler(eng, "s3cret", nil),
		signedRequest(t, "s3cret", "pull_request_review_approved", "", prPayload("o/r", 9, "")))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if tasks := q.all(); len(tasks) != 0 {
		t.Fatalf("got %d tasks, want none (connected issue blocked)", len(tasks))
	}
	if n := api.callCount("IssueDependencies:other/7"); n != 1 {
		t.Errorf("IssueDependencies on the cross-repo connected issue = %d, want 1 (read in its own repo)", n)
	}
	if reason := reasonFor(t, log, "o/r/pr/9"); !strings.Contains(reason, "o/r#3") {
		t.Errorf("reason %q does not name the open blocker", reason)
	}
}

// TestHandler_PRSynced_DoesNotFetchConnectedIssues: the push path
// dispatches no merge, so it grows no gate read
// (§forgejo/webhook/re-fetch). The open path reads no graph either, for
// the same reason — it dispatches no merge — and is pinned by
// TestHandler_PROpened_DoesNotFetchConnectedIssues
// (§forgejo/decisions/merge-gate, s-git/maitred#151).
func TestHandler_PRSynced_DoesNotFetchConnectedIssues(t *testing.T) {
	eng, api, q, _, _ := mergeGateFixtures(t)
	// The push moved the head past the approval: the fan-out re-fires
	// the lens whose approval the push invalidated
	// (§forgejo/decisions/review-lenses).
	api.reviews[9] = []Review{{Author: "bob", Event: ReviewApproved, CommitID: "sha-before-the-push"}}
	serve(t, NewHandler(eng, "s3cret", nil),
		signedRequest(t, "s3cret", "pull_request_sync", "synchronized", prPayload("o/r", 9, "synchronized")))
	if tasks := q.all(); len(tasks) != 1 {
		t.Fatalf("got %d tasks, want 1 (lens review re-fire)", len(tasks))
	}
	if n := api.callCount("IssueBlocks"); n != 0 {
		t.Errorf("IssueBlocks calls = %d, want 0 (no gate read on the push path)", n)
	}
}

// TestHandler_ReconcilePR_ConnectedIssueBlockerHoldsOffMerge: the sweep
// path holds off the merge at an approved head when a connected issue
// has an open blocker (§forgejo/decisions/merge-gate).
func TestHandler_ReconcilePR_ConnectedIssueBlockerHoldsOffMerge(t *testing.T) {
	eng, api, q, store, log := mergeGateFixtures(t)

	eng.HandleEvent(Event{Type: EventReconcile, Repo: "o/r", Kind: KindPR, Number: 9})

	if tasks := q.all(); len(tasks) != 0 {
		t.Fatalf("got %d tasks, want none (sweep gate holds off); first = %+v", len(tasks), tasks)
	}
	if reason := reasonFor(t, log, "o/r/pr/9"); !strings.Contains(reason, "o/r#3") {
		t.Errorf("decision-log reason %q does not name the open blocker o/r#3", reason)
	}
	w, _ := store.Load(Key{Repo: "o/r", Kind: KindPR, Number: 9})
	if w != nil {
		t.Errorf("watermark = %+v, want none", w)
	}
	if n := api.callCount("IssueBlocks:r/9"); n != 1 {
		t.Errorf("IssueBlocks on the PR = %d, want 1 (the sweep reads the gate too)", n)
	}
}

// --- The unblock cascade unblocks a held PR (§forgejo/decisions/unblock-cascade) ---------------

// cascadePRFixtures: closing issue 7 unblocks issue 10, whose only
// remaining open blocker is its connected open PR 9.
func cascadePRFixtures(t *testing.T) (*Engine, *fakeAPI, *fakeQueue, *Store, *bytes.Buffer) {
	t.Helper()
	var log bytes.Buffer
	api := newFakeAPI()
	api.repos = []Repository{testRepo("o/r")}
	q := &fakeQueue{}
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	eng := NewEngine(api, store, testConfig(), q, slog.New(slog.NewJSONHandler(&log, nil)))
	api.issues[7] = testIssue(7, "closed")
	api.issues[10] = testIssue(10, "open")
	api.blocks[7] = []Issue{*testIssue(10, "open")}
	prSelf := testIssue(9, "open")
	prSelf.IsPull = true
	api.deps[10] = []Issue{*testIssue(7, "closed"), *prSelf}
	api.pulls[9] = testPR(9, "open", true)
	api.reviews[9] = []Review{{Author: "bob", Event: ReviewApproved, CommitID: "sha-9"}}
	api.blocks[9] = []Issue{*testIssue(10, "open")}
	return eng, api, q, store, &log
}

// TestHandler_IssueClosed_CascadeUnblocksConnectedPR: the closure of
// the last issue-side blocker moves the connected PR — one
// merge-or-wait dispatch, no implement for the issue, and the PR key's
// watermark recorded (§forgejo/decisions/unblock-cascade).
func TestHandler_IssueClosed_CascadeUnblocksConnectedPR(t *testing.T) {
	eng, _, q, store, _ := cascadePRFixtures(t)
	h := NewHandler(eng, "s3cret", nil)

	rec := serve(t, h, signedRequest(t, "s3cret", "issues", "closed", issuePayload("o/r", 7, "closed")))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	tasks := q.all()
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks, want 1 (merge-or-wait for PR 9); all = %+v", len(tasks), tasks)
	}
	if !strings.Contains(tasks[0].Prompt, "action=merge-or-wait repo=o/r kind=pr number=9") {
		t.Errorf("prompt = %q, want merge-or-wait for o/r PR 9", tasks[0].Prompt)
	}
	w, err := store.Load(Key{Repo: "o/r", Kind: KindPR, Number: 9})
	if err != nil {
		t.Fatalf("load watermark: %v", err)
	}
	if w == nil || w.Action != string(ActionMergeOrWait) || w.Revision != "sha-9" {
		t.Errorf("PR watermark = %+v, want merge-or-wait at sha-9", w)
	}
}

// TestHandler_IssueClosed_CascadePRNotApprovedStaysSilent: a cascaded
// PR that does not stand at an approved head dispatches nothing but
// merge-or-wait — here nothing at all; the sweep is the backstop
// (§forgejo/decisions/unblock-cascade).
func TestHandler_IssueClosed_CascadePRNotApprovedStaysSilent(t *testing.T) {
	eng, api, q, store, _ := cascadePRFixtures(t)
	api.reviews[9] = []Review{{Author: "bob", Event: ReviewChangesRequested, CommitID: "sha-9"}}
	h := NewHandler(eng, "s3cret", nil)

	rec := serve(t, h, signedRequest(t, "s3cret", "issues", "closed", issuePayload("o/r", 7, "closed")))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if tasks := q.all(); len(tasks) != 0 {
		t.Fatalf("got %d tasks, want none (the cascade dispatches nothing but merge-or-wait)", len(tasks))
	}
	w, _ := store.Load(Key{Repo: "o/r", Kind: KindPR, Number: 9})
	if w != nil {
		t.Errorf("watermark = %+v, want none", w)
	}
}

// TestHandler_IssueClosed_CascadePRWithoutOptInHoldsOff:
// the agentic-auto-merge label is a precondition of the cascade's PR
// leg too — the PR's own re-fetch decides it, asking the predicate
// that reads both seats — and an approved PR with no merge opt-in on
// either seat holds off, the fork recorded in the decision log naming
// the missing label and the connected issue. Nothing is
// dispatched, no watermark is recorded, and the hold-off has no
// backstop of its own: the next sweep is (§forgejo/decisions/unblock-cascade,
// §forgejo/decisions/merge-gate, s-git/maitred#179).
func TestHandler_IssueClosed_CascadePRWithoutOptInHoldsOff(t *testing.T) {
	eng, api, q, store, log := cascadePRFixtures(t)
	api.pulls[9].Labels = nil
	// The delivered issue is the other seat the predicate reads, so
	// the no-opt-in shape is empty on both: the blocking-edge stub the
	// re-fetch carries is where the issue's labels are read from.
	noOptIn := testIssue(10, "open")
	noOptIn.Labels = nil
	api.issues[10] = noOptIn
	api.blocks[9] = []Issue{*noOptIn}
	h := NewHandler(eng, "s3cret", nil)

	rec := serve(t, h, signedRequest(t, "s3cret", "issues", "closed", issuePayload("o/r", 7, "closed")))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if tasks := q.all(); len(tasks) != 0 {
		t.Fatalf("got %d tasks, want none (an approved PR with no opt-in waits on a human)", len(tasks))
	}
	w, _ := store.Load(Key{Repo: "o/r", Kind: KindPR, Number: 9})
	if w != nil {
		t.Errorf("watermark = %+v, want none", w)
	}
	reason := reasonFor(t, log, "o/r/pr/9")
	if !strings.Contains(reason, LabelAgenticAutoMerge) || !strings.Contains(reason, "o/r#10") {
		t.Errorf("decision-log reason %q does not name the missing opt-in and the connected issue o/r#10", reason)
	}
}

// TestHandler_IssueClosed_CascadePRClosedInMeantime: a cascaded PR
// closed or merged between the closure and the re-fetch is silent
// (§forgejo/decisions/unblock-cascade).
func TestHandler_IssueClosed_CascadePRClosedInMeantime(t *testing.T) {
	eng, api, q, _, _ := cascadePRFixtures(t)
	api.pulls[9] = testPR(9, "closed", true)
	h := NewHandler(eng, "s3cret", nil)

	rec := serve(t, h, signedRequest(t, "s3cret", "issues", "closed", issuePayload("o/r", 7, "closed")))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if tasks := q.all(); len(tasks) != 0 {
		t.Fatalf("got %d tasks, want none (PR closed in the meantime)", len(tasks))
	}
}

// TestHandler_IssueClosed_CascadePRInadmissibleInOutcomesRepo: the
// cascaded PR key's admissibility is judged by the roles of the
// repository holding the PR; merge-or-wait needs the work-item role,
// and the hold-off is recorded in the decision log
// (§forgejo/webhook/repository-roles).
func TestHandler_IssueClosed_CascadePRInadmissibleInOutcomesRepo(t *testing.T) {
	eng, api, q, store, log := cascadePRFixtures(t)
	api.repos = []Repository{testRepo("o/r"), testRepoOutcomes("o/other")}
	prCross := testIssue(9, "open")
	prCross.IsPull = true
	prCross.Repository = "o/other"
	api.deps[10] = []Issue{*testIssue(7, "closed"), *prCross}

	eng.HandleEvent(Event{Type: EventIssueClosed, Repo: "o/r", Kind: KindIssue, Number: 7, Sender: "alice"})

	if tasks := q.all(); len(tasks) != 0 {
		t.Fatalf("got %d tasks, want none (merge-or-wait is inadmissible in an outcomes repo)", len(tasks))
	}
	if reason := reasonFor(t, log, "o/other/pr/9"); !strings.Contains(reason, "inadmissible") {
		t.Errorf("cascade hold-off reason %q does not name the inadmissibility", reason)
	}
	w, _ := store.Load(Key{Repo: "o/other", Kind: KindPR, Number: 9})
	if w != nil {
		t.Errorf("watermark = %+v, want none", w)
	}
}

// TestHandler_IssueClosed_CascadePRFetchFailure: a failed PR re-fetch
// on the cascade dispatch is a logged dispatch failure — nothing
// dispatched, no watermark, the sweep retries. It mirrors the issue
// key's failure handling (§forgejo/decisions/unblock-cascade).
func TestHandler_IssueClosed_CascadePRFetchFailure(t *testing.T) {
	eng, api, q, store, _ := cascadePRFixtures(t)
	api.pullErr = errors.New("PR fetch failed")

	eng.HandleEvent(Event{Type: EventIssueClosed, Repo: "o/r", Kind: KindIssue, Number: 7, Sender: "alice"})

	if tasks := q.all(); len(tasks) != 0 {
		t.Fatalf("got %d tasks, want none (failed PR fetch)", len(tasks))
	}
	w, _ := store.Load(Key{Repo: "o/r", Kind: KindPR, Number: 9})
	if w != nil {
		t.Errorf("watermark = %+v, want none", w)
	}
}

// TestHandler_IssueClosed_CascadePRWatermarkHolds: a newly unblocked PR whose
// watermark already records merge-or-wait at the current head is not
// re-dispatched (§forgejo/decisions/unblock-cascade,
// §forgejo/decisions/loop-prevention).
func TestHandler_IssueClosed_CascadePRWatermarkHolds(t *testing.T) {
	eng, _, q, store, _ := cascadePRFixtures(t)
	if err := store.Save(Key{Repo: "o/r", Kind: KindPR, Number: 9},
		Watermark{Action: string(ActionMergeOrWait), Revision: "sha-9"}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	eng.HandleEvent(Event{Type: EventIssueClosed, Repo: "o/r", Kind: KindIssue, Number: 7, Sender: "alice"})

	if tasks := q.all(); len(tasks) != 0 {
		t.Fatalf("got %d tasks, want none (already dispatched at the head)", len(tasks))
	}
}

// TestHandler_IssueClosed_CascadePROutOfRemit: a newly unblocked PR in a
// repository holding no maitred role is a recorded hold-off on the
// cascade key — admission is tested the same way as for issue keys
// (§forgejo/webhook/repository-roles, §forgejo/observability/the-decision-log).
func TestHandler_IssueClosed_CascadePROutOfRemit(t *testing.T) {
	eng, api, q, store, log := cascadePRFixtures(t)
	api.repos = []Repository{testRepo("o/r"), testRepoDisabled("o/other")}
	prCross := testIssue(9, "open")
	prCross.IsPull = true
	prCross.Repository = "o/other"
	api.deps[10] = []Issue{*testIssue(7, "closed"), *prCross}

	eng.HandleEvent(Event{Type: EventIssueClosed, Repo: "o/r", Kind: KindIssue, Number: 7, Sender: "alice"})

	if tasks := q.all(); len(tasks) != 0 {
		t.Fatalf("got %d tasks, want none (repo holds no role)", len(tasks))
	}
	if reason := reasonFor(t, log, "o/other/pr/9"); !strings.Contains(reason, "holds no maitred role") {
		t.Errorf("cascade hold-off reason %q does not name the missing role", reason)
	}
	w, _ := store.Load(Key{Repo: "o/other", Kind: KindPR, Number: 9})
	if w != nil {
		t.Errorf("watermark = %+v, want none", w)
	}
}

// TestHandler_IssueOpened_DecomposerAuthoredImplements: an open,
// unblocked issue authored by the decomposer, carrying no
// forest-seeding label and no connected PR, is an implement candidate
// on the opened path like any other work item: authorship is no gate —
// the seeding label is (s-git/maitred#135,
// §forgejo/decisions/transition-table).
func TestHandler_IssueOpened_DecomposerAuthoredImplements(t *testing.T) {
	eng, api, q, store := newTestEngine(t)
	member := testIssue(7, "open")
	member.Author = "decomposer"
	api.issues[7] = member

	rec := serve(t, NewHandler(eng, "s3cret", nil), signedRequest(t, "s3cret", "issues", "opened", issuePayload("o/r", 7, "opened")))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	tasks := q.all()
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks for an unblocked decomposer-authored member, want 1 (implement)", len(tasks))
	}
	if !strings.Contains(tasks[0].Prompt, "action=implement") {
		t.Errorf("prompt = %q, want the implement action", tasks[0].Prompt)
	}
	w, err := store.Load(Key{Repo: "o/r", Kind: KindIssue, Number: 7})
	if err != nil {
		t.Fatalf("load watermark: %v", err)
	}
	if w == nil {
		t.Errorf("watermark = nil, want the dispatched implement revision")
	}
}

// TestHandler_IssueOpened_ForestSeedingHoldsOff: the companion of the
// gate — a newborn member the decomposer authored, still carrying
// forest-seeding, holds off while its blocking edges are being wired:
// the re-fetch sees no blockers yet, and the label — not the author —
// keeps implement from dispatching vacuously
// (§forgejo/decisions/transition-table, s-git/maitred#125).
func TestHandler_IssueOpened_ForestSeedingHoldsOff(t *testing.T) {
	var log bytes.Buffer
	eng, api, q, store := newEngineWithLogger(t, slog.New(slog.NewJSONHandler(&log, nil)))
	member := testIssue(7, "open")
	member.Author = "decomposer"
	member.Labels = []string{LabelForestSeeding}
	api.issues[7] = member

	rec := serve(t, NewHandler(eng, "s3cret", nil), signedRequest(t, "s3cret", "issues", "opened", issuePayload("o/r", 7, "opened")))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if tasks := q.all(); len(tasks) != 0 {
		t.Fatalf("got %d tasks for a forest-seeding member, want none", len(tasks))
	}
	entry := findEntry(t, parseLogLines(t, log.Bytes()), "decision")
	reason, _ := entry["reason"].(string)
	if !strings.Contains(reason, "forest-seeding") {
		t.Errorf("hold-off reason = %q, want it to name the seeding window", reason)
	}
	w, err := store.Load(Key{Repo: "o/r", Kind: KindIssue, Number: 7})
	if err != nil {
		t.Fatalf("load watermark: %v", err)
	}
	if w != nil {
		t.Errorf("watermark = %+v, want none", w)
	}
}

// TestHandler_IssueOpened_OtherAuthorImplements: an issue authored by
// anyone else, with no blockers and no connected PR, is an implement
// candidate as before; authorship is not read at all
// (§forgejo/decisions/transition-table, s-git/maitred#135).
func TestHandler_IssueOpened_OtherAuthorImplements(t *testing.T) {
	eng, api, q, _ := newTestEngine(t)
	member := testIssue(7, "open")
	member.Author = "alice"
	api.issues[7] = member

	rec := serve(t, NewHandler(eng, "s3cret", nil), signedRequest(t, "s3cret", "issues", "opened", issuePayload("o/r", 7, "opened")))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	tasks := q.all()
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks, want 1 (implement for issue 7)", len(tasks))
	}
	if !strings.Contains(tasks[0].Prompt, "action=implement") {
		t.Errorf("prompt = %q, want the implement action", tasks[0].Prompt)
	}
}

// TestHandler_IssueClosed_CascadeUnblocksDecomposerAuthoredMember: the
// cascade is un-narrowed, not widened — the ideation
// ticket closing dispatches implement for the member the decomposer
// authored (§forgejo/decisions/transition-table,
// §forgejo/decisions/unblock-cascade).
func TestHandler_IssueClosed_CascadeUnblocksDecomposerAuthoredMember(t *testing.T) {
	eng, api, q, store := newTestEngine(t)
	// Issue 7 (the ideation ticket, closed) blocks issue 10 (the member,
	// authored by the decomposer); 10's only blocker is 7.
	api.issues[7] = testIssue(7, "closed")
	member := testIssue(10, "open")
	member.Author = "decomposer"
	api.issues[10] = member
	api.blocks[7] = []Issue{*member}
	api.deps[10] = []Issue{*testIssue(7, "closed")}

	rec := serve(t, NewHandler(eng, "s3cret", nil), signedRequest(t, "s3cret", "issues", "closed", issuePayload("o/r", 7, "closed")))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	tasks := q.all()
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks, want 1 (implement for issue 10 from the cascade)", len(tasks))
	}
	if !strings.Contains(tasks[0].Prompt, "kind=issue number=10") {
		t.Errorf("prompt = %q, want implement for issue 10", tasks[0].Prompt)
	}
	w, err := store.Load(Key{Repo: "o/r", Kind: KindIssue, Number: 10})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if w == nil || w.Action != string(ActionImplement) {
		t.Errorf("cascade watermark = %+v, want implement", w)
	}
}

// newOutcomesEngine builds an engine whose org holds one
// outcomes-role repository, o/out, alongside the usual work-item o/r
// (§forgejo/webhook/repository-roles).
func newOutcomesEngine(t *testing.T) (*Engine, *fakeAPI, *fakeQueue, *Store) {
	t.Helper()
	eng, api, q, store := newTestEngine(t)
	api.repos = []Repository{testRepo("o/r"), testRepoOutcomes("o/out")}
	return eng, api, q, store
}

// TestHandler_IdeationEventPath_DispatchesDecompose: opening, editing,
// or changing the labels of an open ideation issue in a repository with
// the outcomes role dispatches decompose and records the watermark; a
// comment authored by anyone other than the configured decomposer
// identity dispatches decompose the same way
// (§forgejo/decisions/transition-table, s-git/maitred#147).
func TestHandler_IdeationEventPath_DispatchesDecompose(t *testing.T) {
	for _, typ := range []EventType{EventIssueOpened, EventIssueEdited, EventIssueLabelsChanged, EventIssueCommented} {
		t.Run(string(typ), func(t *testing.T) {
			eng, api, q, store := newOutcomesEngine(t)
			issue := &Issue{Number: 7, State: "open", UpdatedAt: testUpdatedAt, Repository: "o/out", Labels: []string{LabelIdeation}}
			api.issues[7] = issue

			eng.HandleEvent(Event{Type: typ, Repo: "o/out", Kind: KindIssue, Number: 7, Sender: "alice"})

			tasks := q.all()
			if len(tasks) != 1 {
				t.Fatalf("got %d tasks, want 1 decompose dispatch", len(tasks))
			}
			if !strings.Contains(tasks[0].Prompt, "action="+string(ActionDecompose)) {
				t.Errorf("prompt = %q, want the decompose action", tasks[0].Prompt)
			}
			w, err := store.Load(Key{Repo: "o/out", Kind: KindIssue, Number: 7})
			if err != nil {
				t.Fatalf("load watermark: %v", err)
			}
			if w == nil || w.Action != string(ActionDecompose) {
				t.Errorf("watermark = %+v, want decompose at the current revision", w)
			}
		})
	}
}

// TestHandler_IdeationComment_HoldsOff: a comment created on an open
// root by the configured decomposer identity is inert: no dispatch, no
// watermark, and the reason names the authorship — the author-scoped
// guard that keeps the decomposer's own clarifying question from
// re-triggering it (§forgejo/decisions/loop-prevention,
// s-git/maitred#147).
func TestHandler_IdeationComment_HoldsOff(t *testing.T) {
	var log bytes.Buffer
	eng, api, q, store := newOutcomesEngine(t)
	eng.log = slog.New(slog.NewJSONHandler(&log, nil))
	issue := &Issue{Number: 7, State: "open", UpdatedAt: testUpdatedAt, Repository: "o/out", Labels: []string{LabelIdeation}}
	api.issues[7] = issue

	eng.HandleEvent(Event{Type: EventIssueCommented, Repo: "o/out", Kind: KindIssue, Number: 7, Sender: "decomposer"})

	if tasks := q.all(); len(tasks) != 0 {
		t.Fatalf("got %d tasks for the decomposer's own comment on a root, want none", len(tasks))
	}
	w, err := store.Load(Key{Repo: "o/out", Kind: KindIssue, Number: 7})
	if err != nil {
		t.Fatalf("load watermark: %v", err)
	}
	if w != nil {
		t.Errorf("watermark = %+v, want none", w)
	}
	reason, _ := findEntry(t, parseLogLines(t, log.Bytes()), "decision")["reason"].(string)
	if !strings.Contains(reason, "decomposer") {
		t.Errorf("decision-log reason = %q, want it to name the decomposer identity as the comment's author", reason)
	}
}

// TestHandler_IdeationInWorkItemRepo_DecomposeInadmissible: an open
// ideation issue in a repository holding only the work-item role is
// admitted and re-fetched, but decompose is inadmissible there: no
// dispatch, no watermark, and the hold-off names the action and the
// required role (§forgejo/webhook/repository-roles).
func TestHandler_IdeationInWorkItemRepo_DecomposeInadmissible(t *testing.T) {
	var log bytes.Buffer
	eng, api, q, store := newTestEngine(t)
	eng.log = slog.New(slog.NewJSONHandler(&log, nil))
	issue := &Issue{Number: 7, State: "open", UpdatedAt: testUpdatedAt, Repository: "o/r", Labels: []string{LabelIdeation}}
	api.issues[7] = issue

	eng.HandleEvent(Event{Type: EventIssueOpened, Repo: "o/r", Kind: KindIssue, Number: 7, Sender: "alice"})

	if tasks := q.all(); len(tasks) != 0 {
		t.Fatalf("got %d tasks in a work-item-only repository, want none", len(tasks))
	}
	w, err := store.Load(Key{Repo: "o/r", Kind: KindIssue, Number: 7})
	if err != nil {
		t.Fatalf("load watermark: %v", err)
	}
	if w != nil {
		t.Errorf("watermark = %+v, want none", w)
	}
	reason, _ := findEntry(t, parseLogLines(t, log.Bytes()), "decision")["reason"].(string)
	if !strings.Contains(reason, "decompose is inadmissible") || !strings.Contains(reason, "maitred-outcomes-repo") {
		t.Errorf("hold-off reason = %q, want it to name the action and the required role's topic", reason)
	}
}

// TestHandler_Decompose_NoPrompt_HoldsOffWithoutWatermark: with no
// canned prompt configured for decompose the action cannot fire, and
// the failure says so: nothing is enqueued and no watermark is recorded,
// so the sweep retries (§forgejo/config/canned-prompts,
// §forgejo/webhook/the-pipeline).
func TestHandler_Decompose_NoPrompt_HoldsOffWithoutWatermark(t *testing.T) {
	eng, api, q, store := newOutcomesEngine(t)
	cfg := testConfig()
	delete(cfg.Actions, string(ActionDecompose))
	eng.cfg = cfg
	issue := &Issue{Number: 7, State: "open", UpdatedAt: testUpdatedAt, Repository: "o/out", Labels: []string{LabelIdeation}}
	api.issues[7] = issue

	eng.HandleEvent(Event{Type: EventIssueOpened, Repo: "o/out", Kind: KindIssue, Number: 7, Sender: "alice"})

	if tasks := q.all(); len(tasks) != 0 {
		t.Fatalf("got %d tasks with no decompose prompt configured, want none", len(tasks))
	}
	w, err := store.Load(Key{Repo: "o/out", Kind: KindIssue, Number: 7})
	if err != nil {
		t.Fatalf("load watermark: %v", err)
	}
	if w != nil {
		t.Errorf("watermark = %+v, want none: a dispatch that cannot fire must not mark itself done", w)
	}
}

// --- Sync set: landing a group of changes together (#99) ---

// syncSetFixtures wires a minimal landing set into the fake API:
// member 4 (o/r, agentic-sync-merge) delivered by PR 9, member 5
// closed, grouped by sync issue 11 in the outcomes repository
// o/outcomes (§forgejo/decisions/sync-set).
func syncSetFixtures(api *fakeAPI) {
	member := testIssue(4, "open")
	member.Labels = append(member.Labels, LabelAgenticSyncMerge)
	closedMember := testIssue(5, "closed")
	syncIssue := testIssue(11, "open")
	syncIssue.Labels = append(syncIssue.Labels, LabelSync)
	syncIssue.Repository = "o/outcomes"
	pr := testPR(9, "open", true)
	// The delivery names the member it auto-closes: the predicate
	// resolves it from the pull-request side, not through a blocking
	// edge (§forgejo/decisions/sync-set, s-git/maitred#171).
	pr.Body = "The delivery for member 4.\n\nFixes #4"
	prAsIssue := testIssue(9, "open")
	prAsIssue.IsPull = true
	api.issues[4] = member
	api.issues[5] = closedMember
	api.issues[11] = syncIssue
	api.pulls[9] = pr
	api.openPulls["r"] = []PullRequest{*pr}
	api.reviews[9] = []Review{{Event: ReviewApproved, Author: "bob", CommitID: pr.HeadSHA, SubmittedAt: testUpdatedAt}}
	api.blocks[9] = []Issue{*member}
	api.blocks[4] = []Issue{*syncIssue}
	api.deps[4] = []Issue{*prAsIssue}
	api.deps[5] = nil
	api.deps[11] = []Issue{*member, *closedMember}
}

func syncOutcomesRepos() []Repository {
	return []Repository{testRepo("o/r"), testRepoOutcomes("o/outcomes")}
}

// TestHandler_PRReviewed_SyncMemberHoldsOffAndDispatchesTheSet: the
// final approval in a set never merges the member PR; it dispatches
// sync against the grouping issue without waiting for a sweep, and a
// second delivery at an unchanged set state is suppressed by the
// watermark (§forgejo/decisions/sync-set).
func TestHandler_PRReviewed_SyncMemberHoldsOffAndDispatchesTheSet(t *testing.T) {
	eng, api, q, store := newTestEngine(t)
	h := NewHandler(eng, "s3cret", nil)
	api.repos = syncOutcomesRepos()
	syncSetFixtures(api)

	req := signedRequest(t, "s3cret", "pull_request_review_approved", "approved", prPayload("o/r", 9, "approved"))
	rec := serve(t, h, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	tasks := q.all()
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks, want 1 (sync for 11; the member PR must not merge)", len(tasks))
	}
	if !strings.Contains(tasks[0].Prompt, "action=sync repo=o/outcomes kind=issue number=11") {
		t.Errorf("prompt = %q, want the sync dispatch for o/outcomes issue 11", tasks[0].Prompt)
	}
	prKey := Key{Repo: "o/r", Kind: KindPR, Number: 9}
	w, err := store.Load(prKey)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if w != nil {
		t.Errorf("PR watermark = %+v, want none: the approval held off", w)
	}
	syncKey := Key{Repo: "o/outcomes", Kind: KindIssue, Number: 11}
	w, err = store.Load(syncKey)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if w == nil || w.Action != string(ActionSync) || w.Revision == "" {
		t.Fatalf("sync watermark = %+v, want sync at the set revision", w)
	}

	// The same approval delivered again: the set state is unchanged,
	// the watermark suppresses the second dispatch.
	serve(t, h, req)
	if tasks := q.all(); len(tasks) != 1 {
		t.Errorf("got %d tasks after the repeat delivery, want 1 (watermark suppression)", len(tasks))
	}
}

// TestHandler_PRReviewed_SyncSetNotReadyStaysSilent: a set with any
// member not ready dispatches nothing and comments nowhere — an
// unlabelled open member stalls the set (§forgejo/decisions/sync-set).
func TestHandler_PRReviewed_SyncSetNotReadyStaysSilent(t *testing.T) {
	eng, api, q, store := newTestEngine(t)
	h := NewHandler(eng, "s3cret", nil)
	api.repos = syncOutcomesRepos()
	syncSetFixtures(api)
	stray := testIssue(5, "open")
	stray.Labels = nil // an unlabelled member is a member awaiting a human
	api.issues[5] = stray
	member := api.issues[4]
	api.deps[11] = []Issue{*member, *stray}

	rec := serve(t, h, signedRequest(t, "s3cret", "pull_request_review_approved", "approved", prPayload("o/r", 9, "approved")))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if tasks := q.all(); len(tasks) != 0 {
		t.Errorf("got %d tasks, want none: an unready set dispatches nothing", len(tasks))
	}
	w, err := store.Load(Key{Repo: "o/outcomes", Kind: KindIssue, Number: 11})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if w != nil {
		t.Errorf("sync watermark = %+v, want none: nothing was dispatched", w)
	}
}

// autoCloseSetFixtures wires the shape s-git/maitred#180 reports: the
// member delivers through the pull request whose body auto-closes it and
// **no blocking edge** connects the member and its delivery — the edge
// §decisions/sync-set retired on the member side (s-git/maitred#171).
// The pull request therefore blocks nothing, so the approval path finds
// the landing set through the reference the body carries, exactly as the
// predicate does, rather than through an edge no implementer records
// (§forgejo/decisions/sync-set, §forgejo/webhook/re-fetch).
func autoCloseSetFixtures(t *testing.T) (*Engine, *fakeAPI, *fakeQueue, *Store, *bytes.Buffer) {
	t.Helper()
	api := newFakeAPI()
	q := &fakeQueue{}
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	var log bytes.Buffer
	eng := NewEngine(api, store, testConfig(), q, slog.New(slog.NewJSONHandler(&log, nil)))
	api.repos = syncOutcomesRepos()

	member := testIssue(4, "open")
	member.Labels = []string{LabelAgenticSyncMerge} // opted into the set, never into merging alone
	closedMember := testIssue(5, "closed")
	syncIssue := testIssue(11, "open")
	syncIssue.Labels = []string{LabelSync}
	syncIssue.Repository = "o/outcomes"
	pr := testPR(9, "open", true)
	pr.Body = "Deliver member 4.\n\nCloses #4"
	api.issues[4] = member
	api.issues[5] = closedMember
	api.issues[11] = syncIssue
	api.pulls[9] = pr
	api.openPulls["r"] = []PullRequest{*pr}
	api.reviews[9] = []Review{{Event: ReviewApproved, Author: "bob", CommitID: pr.HeadSHA, SubmittedAt: testUpdatedAt}}
	api.blocks[4] = []Issue{*syncIssue} // the member's membership edge
	api.deps[11] = []Issue{*member, *closedMember}
	return eng, api, q, store, &log
}

// TestHandler_PRReviewed_AutoCloseReferenceFindsTheSet: the reported
// shape, end to end and with no sweep in sight. An open member carrying
// agentic-sync-merge blocks an open sync issue whose other member is
// closed, and the delivery is an open pull request whose body reads
// Closes #N — the member records no blocking edge, so the old read saw
// nothing and the approval merged the delivery on its own. The approval
// now holds merge-or-wait off and enqueues sync against the grouping
// issue at the set revision (§forgejo/decisions/sync-set,
// §forgejo/webhook/re-fetch, s-git/maitred#180).
func TestHandler_PRReviewed_AutoCloseReferenceFindsTheSet(t *testing.T) {
	eng, _, q, store, log := autoCloseSetFixtures(t)
	h := NewHandler(eng, "s3cret", nil)

	rec := serve(t, h, signedRequest(t, "s3cret", "pull_request_review_approved", "approved", prPayload("o/r", 9, "approved")))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	tasks := q.all()
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks, want the one sync dispatch for 11 (the member PR must not merge): %+v", len(tasks), tasks)
	}
	if !strings.Contains(tasks[0].Prompt, "action=sync repo=o/outcomes kind=issue number=11") {
		t.Errorf("prompt = %q, want the sync dispatch for o/outcomes issue 11", tasks[0].Prompt)
	}
	if strings.Contains(tasks[0].Prompt, string(ActionMergeOrWait)) {
		t.Errorf("prompt = %q, want no merge-or-wait: the set's decision lands its delivery", tasks[0].Prompt)
	}
	w, err := store.Load(Key{Repo: "o/r", Kind: KindPR, Number: 9})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if w != nil {
		t.Errorf("PR watermark = %+v, want none: the approval held off", w)
	}
	w, err = store.Load(Key{Repo: "o/outcomes", Kind: KindIssue, Number: 11})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if w == nil || w.Action != string(ActionSync) || w.Revision == "" {
		t.Fatalf("sync watermark = %+v, want sync at the set revision", w)
	}
	if reason := reasonFor(t, log, "o/r/pr/9"); !strings.Contains(reason, "sync set") {
		t.Errorf("approval hold-off %q does not name the landing set it is waiting for", reason)
	}
}

// TestHandler_PRReviewed_AutoCloseReferenceUnreadySetDispatchesNothing:
// the mirror case. The same delivery, the same reference, and one member
// of the set standing unapproved: nothing dispatches, the queue stays
// empty and the PR stays unmerged — the hold-off fires on membership,
// and the set's own re-fetch is what decides it is not ready
// (§forgejo/decisions/sync-set, §forgejo/webhook/re-fetch).
func TestHandler_PRReviewed_AutoCloseReferenceUnreadySetDispatchesNothing(t *testing.T) {
	eng, api, q, store, log := autoCloseSetFixtures(t)
	stray := testIssue(12, "open")
	stray.Labels = []string{LabelAgenticSyncMerge}
	strayPR := testPR(13, "open", true)
	strayPR.Body = "Deliver member 12.\n\nFixes #12"
	api.issues[12] = stray
	api.pulls[13] = strayPR
	api.openPulls["r"] = append(api.openPulls["r"], *strayPR)
	api.reviews[13] = []Review{{Event: ReviewComment, Author: "carol", CommitID: strayPR.HeadSHA, SubmittedAt: testUpdatedAt}}
	api.deps[11] = append(api.deps[11], *stray)
	h := NewHandler(eng, "s3cret", nil)

	rec := serve(t, h, signedRequest(t, "s3cret", "pull_request_review_approved", "approved", prPayload("o/r", 9, "approved")))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if tasks := q.all(); len(tasks) != 0 {
		t.Errorf("got %d tasks, want none: an unready set dispatches nothing and merges nothing: %+v", len(tasks), tasks)
	}
	w, err := store.Load(Key{Repo: "o/r", Kind: KindPR, Number: 9})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if w != nil {
		t.Errorf("PR watermark = %+v, want none: the approval held off", w)
	}
	if reason := reasonFor(t, log, "o/r/pr/9"); !strings.Contains(reason, "sync set") {
		t.Errorf("approval hold-off %q does not name the landing set it is waiting for", reason)
	}
}

// TestHandler_PRReviewed_AutoCloseReferenceBlockerHoldsOffMerge: the
// union decides the merge gate too. A pull request whose body closes an
// issue with an open blocker is held off by name even though it blocks
// nothing on the graph — one relation for the delivery, read the same
// way by both the gate and the set hold-off
// (§forgejo/decisions/merge-gate, §forgejo/webhook/re-fetch).
func TestHandler_PRReviewed_AutoCloseReferenceBlockerHoldsOffMerge(t *testing.T) {
	eng, api, q, store, log := mergeGateFixtures(t)
	api.blocks[9] = nil // the PR blocks nothing: only the body connects it
	api.issues[7] = testIssue(7, "open")
	api.pulls[9].Body = "Delivers issue 7.\n\nCloses #7"
	h := NewHandler(eng, "s3cret", nil)

	serve(t, h, signedRequest(t, "s3cret", "pull_request_review_approved", "approved", prPayload("o/r", 9, "approved")))
	if tasks := q.all(); len(tasks) != 0 {
		t.Fatalf("got %d tasks, want none: the issue the body closes has an open blocker: %+v", len(tasks), tasks)
	}
	if reason := reasonFor(t, log, "o/r/pr/9"); !strings.Contains(reason, "o/r#3") {
		t.Errorf("reason %q does not name the open blocker o/r#3 of the issue the body closes", reason)
	}
	w, err := store.Load(Key{Repo: "o/r", Kind: KindPR, Number: 9})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if w != nil {
		t.Errorf("watermark = %+v, want none: the merge gate held off", w)
	}
}

// TestHandler_PRReviewed_AutoCloseReferenceFetchFailureHoldsOff: the
// extra read fails closed like every other hop — a body whose reference
// cannot be resolved is a re-fetch failure named in the log, never
// "nothing connected" and never the path's default
// (§forgejo/webhook/re-fetch).
func TestHandler_PRReviewed_AutoCloseReferenceFetchFailureHoldsOff(t *testing.T) {
	eng, api, q, store := newTestEngine(t)
	h := NewHandler(eng, "s3cret", nil)
	api.pulls[9] = testPR(9, "open", true)
	api.pulls[9].Body = "Delivers issue 4.\n\nFixes #4"
	api.reviews[9] = []Review{{Author: "bob", Event: ReviewApproved, CommitID: "sha-9"}}
	api.getIssueErr = errors.New("issue unavailable")

	serve(t, h, signedRequest(t, "s3cret", "pull_request_review_approved", "approved", prPayload("o/r", 9, "approved")))
	if n := api.callCount("GetIssue"); n != 1 {
		t.Errorf("GetIssue called %d time(s), want 1: the delivery must reach the reference read for this test to test it", n)
	}
	if tasks := q.all(); len(tasks) != 0 {
		t.Errorf("task dispatched despite failed reference read: %+v", tasks)
	}
	w, err := store.Load(Key{Repo: "o/r", Kind: KindPR, Number: 9})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if w != nil {
		t.Errorf("watermark recorded after failed re-fetch: %+v", w)
	}
}

// TestConnectedIssuesUnionsTheTwoRelations pins the read itself: the
// issues the body auto-closes and the issues the blocking edges name are
// one list, each carrying its blockers, deduplicated when an issue is
// reachable both ways; a reference naming a pull request is not a
// connected issue, a dangling reference is skipped as the forge skips
// it, and a body carrying no reference costs no read at all
// (§forgejo/webhook/re-fetch, §forgejo/decisions/sync-set).
func TestConnectedIssuesUnionsTheTwoRelations(t *testing.T) {
	blocked := testIssue(7, "open")
	member := testIssue(4, "open")
	member.Labels = []string{LabelAgenticSyncMerge}
	outcomesIssue := testIssue(11, "open")
	outcomesIssue.Repository = "o/outcomes"
	prAsIssue := testIssue(9, "open")
	prAsIssue.IsPull = true

	cases := []struct {
		name      string
		blocks    []Issue
		body      string
		repos     []Repository
		want      []Key
		wantGet   int
		wantCalls []string
	}{
		{
			name:    "blocking edge alone",
			blocks:  []Issue{*blocked},
			body:    "No references here, not even #4.",
			want:    []Key{{Repo: "o/r", Kind: KindIssue, Number: 7}},
			wantGet: 0,
		},
		{
			name:    "auto-close reference alone",
			body:    "Delivers the member.\n\nCloses #4",
			want:    []Key{{Repo: "o/r", Kind: KindIssue, Number: 4}},
			wantGet: 1,
		},
		{
			name:   "both relations, one row each",
			blocks: []Issue{*blocked},
			body:   "Delivers the member and issue 7 too.\n\nFixes #4\nFixes #7",
			want: []Key{
				{Repo: "o/r", Kind: KindIssue, Number: 7},
				{Repo: "o/r", Kind: KindIssue, Number: 4},
			},
			wantGet: 1,
		},
		{
			name:    "an issue reachable both ways is read once",
			blocks:  []Issue{*member},
			body:    "Delivers the member.\n\nFixes #4\nAnd again, fixes #4.",
			want:    []Key{{Repo: "o/r", Kind: KindIssue, Number: 4}},
			wantGet: 0,
		},
		{
			name:   "references on consecutive lines, no shared row",
			blocks: nil,
			body:   "Delivers both.\n\nCloses #4\nCloses #7",
			want: []Key{
				{Repo: "o/r", Kind: KindIssue, Number: 4},
				{Repo: "o/r", Kind: KindIssue, Number: 7},
			},
			wantGet: 2,
		},
		{
			name:    "cross-repository reference",
			body:    "Also lands o/outcomes#11.\n\nCloses o/outcomes#11",
			repos:   syncOutcomesRepos(),
			want:    []Key{{Repo: "o/outcomes", Kind: KindIssue, Number: 11}},
			wantGet: 1,
			// Pin the resolved target, not the fixture: the read must be
			// aimed at the repository the reference resolved to
			// (§forgejo/webhook/re-fetch, s-git/maitred#185).
			wantCalls: []string{"GetIssue:outcomes/11", "IssueDependencies:outcomes/11"},
		},
		{
			// The grammar matches repository names case-insensitively and
			// so must the remit test: a mixed-case spelling of a repository
			// inside the remit resolves like its canonical casing, not a
			// silent out-of-remit skip (s-git/maitred#185 review 1304).
			name:      "a cross-repository reference in a different casing",
			body:      "Also lands O/Outcomes#11.\n\nCloses O/Outcomes#11",
			repos:     syncOutcomesRepos(),
			want:      []Key{{Repo: "o/outcomes", Kind: KindIssue, Number: 11}},
			wantGet:   1,
			wantCalls: []string{"GetIssue:outcomes/11", "IssueDependencies:outcomes/11"},
		},
		{
			name:    "a reference into a repository outside the remit reads nothing",
			body:    "Probe.\n\nCloses o/elsewhere#5",
			want:    nil,
			wantGet: 0,
		},
		{
			name:    "a reference naming a path segment resolves nowhere",
			body:    "Probe.\n\nCloses ../x#5",
			want:    nil,
			wantGet: 0,
		},
		{
			name:    "a reference to a pull request is not a connected issue",
			body:    "Follows the shape of #8.\n\nFixes #9.",
			want:    nil,
			wantGet: 1,
		},
		{
			name:   "a dangling reference is skipped, the rest of the body still reads",
			blocks: []Issue{*blocked},
			body:   "Delivers the member and issue 7.\n\nFixes #4 and Fixes #99.",
			want: []Key{
				{Repo: "o/r", Kind: KindIssue, Number: 7},
				{Repo: "o/r", Kind: KindIssue, Number: 4},
			},
			wantGet: 2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng, api, _, _ := newTestEngine(t)
			api.issues[4] = member
			api.issues[7] = blocked
			api.issues[9] = prAsIssue
			api.issues[11] = outcomesIssue
			api.deps[4] = []Issue{*testIssue(3, "open")}
			api.deps[7] = []Issue{*testIssue(6, "closed")}
			api.blocks[9] = tc.blocks
			if tc.repos != nil {
				api.repos = tc.repos
			}

			got, err := eng.connectedIssues("o", "r", 9, tc.body)
			if err != nil {
				t.Fatalf("connectedIssues: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d connected issues, want %d: %+v", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i].Issue.Repository != tc.want[i].Repo || got[i].Issue.Number != tc.want[i].Number {
					t.Errorf("row %d = %s#%d, want %s#%d", i, got[i].Issue.Repository, got[i].Issue.Number, tc.want[i].Repo, tc.want[i].Number)
				}
			}
			if n := api.callCount("GetIssue"); n != tc.wantGet {
				t.Errorf("GetIssue called %d time(s), want %d", n, tc.wantGet)
			}
			for _, prefix := range tc.wantCalls {
				if n := api.callCount(prefix); n != 1 {
					t.Errorf("%s called %d time(s), want 1", prefix, n)
				}
			}
			// One hop per connected issue, no recursion — the referenced
			// issue's blockers are read, its blockers' blockers are not.
			if n := api.callCount("IssueDependencies:r/3"); n != 0 {
				t.Errorf("IssueDependencies on a blocker called %d time(s), want 0", n)
			}
		})
	}
}

// TestConnectedIssuesReferencedIssueReadFailure: a referenced issue whose
// read — the issue itself or its blockers — fails fails the whole read,
// so the caller holds off naming the failure rather than deciding on an
// empty graph (§forgejo/webhook/re-fetch).
func TestConnectedIssuesReferencedIssueReadFailure(t *testing.T) {
	t.Run("issue read", func(t *testing.T) {
		eng, api, _, _ := newTestEngine(t)
		api.getIssueErr = errors.New("issue unavailable")
		if _, err := eng.connectedIssues("o", "r", 9, "Fixes #4"); err == nil {
			t.Error("connectedIssues succeeded, want the failed issue read reported")
		}
	})
	t.Run("blockers read", func(t *testing.T) {
		eng, api, _, _ := newTestEngine(t)
		api.issues[4] = testIssue(4, "open")
		api.issueDepsErr = errors.New("dependencies unavailable")
		if _, err := eng.connectedIssues("o", "r", 9, "Fixes #4"); err == nil {
			t.Error("connectedIssues succeeded, want the failed blockers read reported")
		}
	})
	t.Run("not found is not a failed read", func(t *testing.T) {
		// A 404 is the forge's definitive "no such issue" answer, which
		// the forge itself ignores for a linked reference — it must not
		// hold the re-fetch off the way an unavailable read does.
		eng, api, _, _ := newTestEngine(t)
		api.getIssueErr = &APIError{StatusCode: http.StatusNotFound, Message: "issue not found"}
		got, err := eng.connectedIssues("o", "r", 9, "Fixes #4")
		if err != nil {
			t.Fatalf("connectedIssues: a not-found answer failed the read: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("got %d connected issues, want none", len(got))
		}
	})
	t.Run("scope read", func(t *testing.T) {
		// A cross-repository reference pays the admission test the rest
		// of the file pays, and an unknown scope fails the re-fetch
		// rather than reading on (§forgejo/webhook/repository-roles).
		eng, api, _, _ := newTestEngine(t)
		api.issues[11] = testIssue(11, "open")
		api.listReposErr = errors.New("repository listing unavailable")
		if _, err := eng.connectedIssues("o", "r", 9, "Fixes o/outcomes#11"); err == nil {
			t.Error("connectedIssues succeeded, want the failed scope read reported")
		}
		if n := api.callCount("GetIssue"); n != 0 {
			t.Errorf("GetIssue called %d time(s) with scope unknown, want 0", n)
		}
	})
}

// TestConnectedIssuesSkipsAreLogged: a skipped reference leaves a trace
// naming the resolved repository and number — identifiers only, never
// the body — so a hold-off or a merge whose relation was dropped is
// visible in the log rather than only in the grammar
// (§forgejo/webhook/re-fetch, s-git/maitred#185).
func TestConnectedIssuesSkipsAreLogged(t *testing.T) {
	api := newFakeAPI()
	q := &fakeQueue{}
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	var log bytes.Buffer
	eng := NewEngine(api, store, testConfig(), q, slog.New(slog.NewJSONHandler(&log, &slog.HandlerOptions{Level: slog.LevelDebug})))
	prAsIssue := testIssue(9, "open")
	prAsIssue.IsPull = true
	api.issues[9] = prAsIssue

	if _, err := eng.connectedIssues("o", "r", 5, "Fixes #99.\n\nFixes #9.\n\nCloses ../x#5"); err != nil {
		t.Fatalf("connectedIssues: %v", err)
	}
	out := log.String()
	for _, want := range []string{"skipping a closing reference whose issue answers not found", "o/r#99", "skipping a closing reference naming a pull request", "o/r#9", "skipping a closing reference whose repository names a path segment", "../x#5"} {
		if !strings.Contains(out, want) {
			t.Errorf("log %q does not contain %q", out, want)
		}
	}
}

// TestConnectedIssuesReferenceCap: the per-reference fan-out is
// bounded and a body that exceeds the bound cannot be read completely,
// so the read fails rather than answering with the rows it happened to
// collect: the first reference the loop can no longer pay for leaves a
// debug line and the re-fetch errors — a hold-off naming the failure,
// never a partial set presented as a complete one (§forgejo/webhook/re-fetch,
// s-git/maitred#185 review 1365).
func TestConnectedIssuesReferenceCap(t *testing.T) {
	api := newFakeAPI()
	q := &fakeQueue{}
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	var log bytes.Buffer
	eng := NewEngine(api, store, testConfig(), q, slog.New(slog.NewJSONHandler(&log, &slog.HandlerOptions{Level: slog.LevelDebug})))
	body := "Delivers a great many things."
	overflow := 20 + maxAutoCloseRefs
	for n := 20; n < 20+maxAutoCloseRefs+2; n++ {
		api.issues[n] = testIssue(n, "open")
		body += "\n\nFixes #" + strconv.Itoa(n)
	}
	got, err := eng.connectedIssues("o", "r", 9, body)
	if err == nil {
		t.Fatalf("connectedIssues returned %d rows and no error, want the re-fetch to fail: a truncated read is not a complete one", len(got))
	}
	if got != nil {
		t.Errorf("connectedIssues returned %d rows beside the error, want none", len(got))
	}
	if n := api.callCount("GetIssue"); n != maxAutoCloseRefs {
		t.Errorf("GetIssue called %d time(s), want the cap %d", n, maxAutoCloseRefs)
	}
	out := log.String()
	for _, want := range []string{"skipping a closing reference beyond the reference cap", "o/r#" + strconv.Itoa(overflow)} {
		if !strings.Contains(out, want) {
			t.Errorf("log %q does not contain %q", out, want)
		}
	}
}

// TestConnectedIssuesCapOverflowFailsTheReFetch: the adversarial body
// review 1365 reproduced — disposable references placed first exhaust
// the budget and the real delivery reference would be dropped, which
// under the previous shape answered "these are the connected issues"
// and let merge-or-wait land the member's delivery out of turn, the
// deliberate form of the fail-open #180 exists to close. The read
// fails closed instead: the failure mode is a hold-off, never a merge,
// whichever body writes it (§forgejo/webhook/re-fetch).
func TestConnectedIssuesCapOverflowFailsTheReFetch(t *testing.T) {
	api := newFakeAPI()
	q := &fakeQueue{}
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	var log bytes.Buffer
	eng := NewEngine(api, store, testConfig(), q, slog.New(slog.NewJSONHandler(&log, &slog.HandlerOptions{Level: slog.LevelDebug})))

	// Eight dangling references — each pays a GetIssue that answers 404
	// and spends the budget — then the genuine delivery reference, a
	// landing-set member the edges do not carry.
	body := "Delivers one member behind a wall of noise."
	for n := 9001; n < 9001+maxAutoCloseRefs; n++ {
		body += "\n\nCloses #" + strconv.Itoa(n)
	}
	api.issues[100] = testIssue(100, "open")
	body += "\n\nCloses #100"

	got, err := eng.connectedIssues("o", "r", 9, body)
	if err == nil {
		t.Fatalf("connectedIssues returned %d rows and no error, want the re-fetch to fail: the survivor set of a truncated read must not be chosen by the body that is being read", len(got))
	}
	if n := api.callCount("GetIssue"); n != maxAutoCloseRefs {
		t.Errorf("GetIssue called %d time(s), want the cap %d", n, maxAutoCloseRefs)
	}
	if out := log.String(); !strings.Contains(out, "skipping a closing reference beyond the reference cap") || !strings.Contains(out, "o/r#100") {
		t.Errorf("log %q does not name the dropped reference o/r#100", out)
	}
}

// TestConnectedIssuesCapChargesOnlyPaidReferences: the budget buys API
// calls, so a reference that costs nothing — one a blocking-edge row
// already carries, or one refused before any URL is built — must not
// spend it. Otherwise a body whose first references all arrive as edges
// resolves nothing new and drops the first genuinely-new reference as
// "beyond the reference cap": the delivery reads as disconnected and
// merge-or-wait lands it out of turn, the fail-open #180 exists to fix
// (§forgejo/webhook/re-fetch, s-git/maitred#185 review 1338).
func TestConnectedIssuesCapChargesOnlyPaidReferences(t *testing.T) {
	api := newFakeAPI()
	q := &fakeQueue{}
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	var log bytes.Buffer
	eng := NewEngine(api, store, testConfig(), q, slog.New(slog.NewJSONHandler(&log, &slog.HandlerOptions{Level: slog.LevelDebug})))

	// Eight blocking-edge rows, the same eight named in the body, and
	// one reference the edges do not carry.
	var edges []Issue
	body := "Delivers eight members and one more."
	for n := 20; n < 20+maxAutoCloseRefs; n++ {
		api.issues[n] = testIssue(n, "open")
		edges = append(edges, *testIssue(n, "open"))
		body += "\n\nFixes #" + strconv.Itoa(n)
	}
	api.issues[100] = testIssue(100, "open")
	body += "\n\nFixes #100"
	api.blocks[9] = edges

	got, err := eng.connectedIssues("o", "r", 9, body)
	if err != nil {
		t.Fatalf("connectedIssues: %v", err)
	}
	if len(got) != maxAutoCloseRefs+1 {
		t.Fatalf("got %d connected issues, want the %d edge rows plus the new reference: %+v", len(got), maxAutoCloseRefs, got)
	}
	last := got[len(got)-1]
	if last.Issue.Number != 100 {
		t.Errorf("last row = %s#%d, want o/r#100: the new reference must be resolved, not dropped by the cap", last.Issue.Repository, last.Issue.Number)
	}
	// The eight references dedup against the edge rows and pay nothing;
	// only the new reference costs a read.
	if n := api.callCount("GetIssue"); n != 1 {
		t.Errorf("GetIssue called %d time(s), want 1", n)
	}
	if out := log.String(); strings.Contains(out, "beyond the reference cap") {
		t.Errorf("log %q records a cap truncation, want none: no reference here costs a read", out)
	}
}

// TestHandler_Reconcile_SyncIssueDispatchesTheSet: the sweep is the
// backstop — reconciling an open sync issue whose members are all
// closed or approved-and-opted-in dispatches sync, recorded against
// the set revision; a second sweep at the unchanged set state is
// suppressed (§forgejo/decisions/sync-set, §forgejo/reconciliation).
func TestHandler_Reconcile_SyncIssueDispatchesTheSet(t *testing.T) {
	eng, api, q, store := newTestEngine(t)
	api.repos = syncOutcomesRepos()
	syncSetFixtures(api)

	eng.HandleEvent(Event{Type: EventReconcile, Repo: "o/outcomes", Kind: KindIssue, Number: 11, Sender: "maitred"})
	tasks := q.all()
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks, want 1 (sync for the sweep's sync issue)", len(tasks))
	}
	if !strings.Contains(tasks[0].Prompt, "action=sync repo=o/outcomes kind=issue number=11") {
		t.Errorf("prompt = %q, want the sync dispatch for o/outcomes issue 11", tasks[0].Prompt)
	}
	w, err := store.Load(Key{Repo: "o/outcomes", Kind: KindIssue, Number: 11})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if w == nil || w.Action != string(ActionSync) {
		t.Fatalf("sync watermark = %+v, want sync", w)
	}
	if w.Revision == issueRevision(api.issues[11]) {
		t.Error("sync watermark must record the set revision, not the grouping issue's own revision")
	}

	eng.HandleEvent(Event{Type: EventReconcile, Repo: "o/outcomes", Kind: KindIssue, Number: 11, Sender: "maitred"})
	if tasks := q.all(); len(tasks) != 1 {
		t.Errorf("got %d tasks after the second sweep, want 1 (unchanged set state)", len(tasks))
	}

	// A member PR moving changes the set revision: the sweep re-fires.
	api.pulls[9].HeadSHA = "sha-9-v2"
	api.reviews[9] = []Review{{Event: ReviewApproved, Author: "bob", CommitID: "sha-9-v2", SubmittedAt: testUpdatedAt}}
	eng.HandleEvent(Event{Type: EventReconcile, Repo: "o/outcomes", Kind: KindIssue, Number: 11, Sender: "maitred"})
	if tasks := q.all(); len(tasks) != 2 {
		t.Errorf("got %d tasks after the set state moved, want 2 (the set revision re-fires the dispatch)", len(tasks))
	}
}

// TestHandler_Reconcile_SyncIssueReadsItsDirectPRMember: a pull request
// that blocks the grouping issue directly is a member of the set. While
// it is not opted in the sweep dispatches nothing — the engine does not
// land a set past an unmerged delivery the graph placed in front of it —
// and once the PR is opted in, approved at head and mergeable the same
// sweep dispatches (§forgejo/decisions/sync-set).
func TestHandler_Reconcile_SyncIssueReadsItsDirectPRMember(t *testing.T) {
	eng, api, q, store := newTestEngine(t)
	api.repos = syncOutcomesRepos()
	syncSetFixtures(api)
	prAsIssue := testIssue(9, "open")
	prAsIssue.IsPull = true
	prAsIssue.Labels = nil // opted out: the set cannot land past it
	api.deps[11] = []Issue{*testIssue(5, "closed"), *prAsIssue}

	eng.HandleEvent(Event{Type: EventReconcile, Repo: "o/outcomes", Kind: KindIssue, Number: 11, Sender: "maitred"})
	if tasks := q.all(); len(tasks) != 0 {
		t.Fatalf("got %d tasks, want none: an open PR member without the opt-in holds the set off", len(tasks))
	}
	w, err := store.Load(Key{Repo: "o/outcomes", Kind: KindIssue, Number: 11})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if w != nil {
		t.Errorf("sync watermark = %+v, want none: nothing was dispatched", w)
	}
	if n := api.callCount("GetPullRequest:r/9"); n == 0 {
		t.Error("the direct PR member's state was never read: the predicate must see the row the graph put in front of it")
	}

	prAsIssue.Labels = []string{LabelAgenticSyncMerge}
	api.deps[11] = []Issue{*testIssue(5, "closed"), *prAsIssue}
	eng.HandleEvent(Event{Type: EventReconcile, Repo: "o/outcomes", Kind: KindIssue, Number: 11, Sender: "maitred"})
	tasks := q.all()
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks, want the sync dispatch once the PR member is opted in", len(tasks))
	}
	if !strings.Contains(tasks[0].Prompt, "action=sync repo=o/outcomes kind=issue number=11") {
		t.Errorf("prompt = %q, want the sync dispatch for o/outcomes issue 11", tasks[0].Prompt)
	}
}

// TestHandler_IssueClosed_CascadeEmitsSyncSetCandidate: closing a
// member routes its outbound edge into the grouping issue — the
// cascade emits the sync-set candidate and the set's evaluation
// dispatches (§forgejo/decisions/sync-set,
// §forgejo/decisions/unblock-cascade).
func TestHandler_IssueClosed_CascadeEmitsSyncSetCandidate(t *testing.T) {
	eng, api, q, _ := newTestEngine(t)
	h := NewHandler(eng, "s3cret", nil)
	api.repos = syncOutcomesRepos()
	syncSetFixtures(api)
	api.issues[7] = testIssue(7, "closed")
	api.blocks[7] = []Issue{*api.issues[11]}

	rec := serve(t, h, signedRequest(t, "s3cret", "issues", "closed", issuePayload("o/r", 7, "closed")))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	tasks := q.all()
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks, want 1 (the sync-set candidate from the walk)", len(tasks))
	}
	if !strings.Contains(tasks[0].Prompt, "action=sync repo=o/outcomes kind=issue number=11") {
		t.Errorf("prompt = %q, want the sync dispatch for o/outcomes issue 11", tasks[0].Prompt)
	}
	// The gate holds the set read back only where the graph shows the
	// set cannot be ready: here its members are opted in, so the set
	// is read and decided as before
	// (§forgejo/decisions/sync-set).
	if n := api.callCount("GetIssue:outcomes/11"); n == 0 {
		t.Error("the set was never read: the walk's gate must not hold back a set whose members are all opted in")
	}
}

// cascadePRSetFixtures: the cascade's PR leg inside a landing set.
// Closing issue 7 unblocks the labelled member issue 10 — its only
// remaining open blocker is its connected PR 9 — and issue 10 is a
// member of the open sync issue 11 in the outcomes repository, whose
// other member (issue 5) stands open without the member label, so the
// set is not ready. The membership edge is blocks[10]: delete it and
// the set reads as retired (§forgejo/decisions/sync-set,
// §forgejo/decisions/unblock-cascade).
func cascadePRSetFixtures(t *testing.T) (*Engine, *fakeAPI, *fakeQueue, *Store, *bytes.Buffer) {
	t.Helper()
	eng, api, q, store, log := cascadePRFixtures(t)
	api.repos = syncOutcomesRepos()
	syncSetFixtures(api)
	member := api.issues[10]
	// A member is opted into the set, never into merging on its own.
	member.Labels = []string{LabelAgenticSyncMerge}
	api.issues[5] = testIssue(5, "open") // open, unlabelled: a member awaiting a human
	api.deps[11] = []Issue{*member, *api.issues[5]}
	api.blocks[9] = []Issue{*member} // PR 9 delivers issue 10, not fixture member 4
	// The delivery names the member it auto-closes. Fixture member 4
	// stands outside this set here, so the body names issue 10: a body
	// still reading "Fixes #4" would connect 4 to this PR through the
	// reference the approval path now reads as well
	// (§forgejo/webhook/re-fetch).
	api.pulls[9].Body = "Delivers issue 10.\n\nCloses #10"
	api.blocks[10] = []Issue{*api.issues[11]}
	return eng, api, q, store, log
}

// TestHandler_IssueClosed_CascadePRLegNeverMergesSetMember: the merge
// the unblock cascade fires is the third path to merge-or-wait, and a
// member of an open landing set must not be merged by it — closing the
// last issue-side blocker of a labelled member whose set is not ready
// dispatches nothing, records no PR watermark, and leaves the hold-off
// in the decision log naming the set (§forgejo/decisions/sync-set,
// §forgejo/decisions/unblock-cascade).
func TestHandler_IssueClosed_CascadePRLegNeverMergesSetMember(t *testing.T) {
	eng, _, q, store, log := cascadePRSetFixtures(t)
	h := NewHandler(eng, "s3cret", nil)

	rec := serve(t, h, signedRequest(t, "s3cret", "issues", "closed", issuePayload("o/r", 7, "closed")))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if tasks := q.all(); len(tasks) != 0 {
		t.Fatalf("got %d tasks, want none: an open set lands its member PR, not the cascade %+v", len(tasks), tasks)
	}
	w, err := store.Load(Key{Repo: "o/r", Kind: KindPR, Number: 9})
	if err != nil {
		t.Fatalf("load watermark: %v", err)
	}
	if w != nil {
		t.Errorf("PR watermark = %+v, want none: the cascade's PR leg held off", w)
	}
	if reason := reasonFor(t, log, "o/r/pr/9"); !strings.Contains(reason, "sync set") {
		t.Errorf("cascade PR hold-off %q does not name the landing set it is waiting for", reason)
	}
}

// TestHandler_IssueClosed_CascadePRLegFindsTheSetThroughTheBody: the
// third path to merge-or-wait reads the delivery the same way. Closing
// the last issue-side blocker unblocks the member's PR — and that PR
// records no blocking edge anywhere, being connected to its member only
// by the reference in its body. The cascade's PR leg still sees the open
// set, holds the merge off and emits the set instead
// (§forgejo/decisions/unblock-cascade, §forgejo/webhook/re-fetch,
// s-git/maitred#180).
func TestHandler_IssueClosed_CascadePRLegFindsTheSetThroughTheBody(t *testing.T) {
	eng, api, q, store, log := cascadePRSetFixtures(t)
	api.blocks[9] = nil // the PR blocks nothing: the body carries the delivery
	h := NewHandler(eng, "s3cret", nil)

	rec := serve(t, h, signedRequest(t, "s3cret", "issues", "closed", issuePayload("o/r", 7, "closed")))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if tasks := q.all(); len(tasks) != 0 {
		t.Fatalf("got %d tasks, want none: an open set lands its member PR, not the cascade: %+v", len(tasks), tasks)
	}
	w, err := store.Load(Key{Repo: "o/r", Kind: KindPR, Number: 9})
	if err != nil {
		t.Fatalf("load watermark: %v", err)
	}
	if w != nil {
		t.Errorf("PR watermark = %+v, want none: the cascade's PR leg held off", w)
	}
	if reason := reasonFor(t, log, "o/r/pr/9"); !strings.Contains(reason, "sync set") {
		t.Errorf("cascade PR hold-off %q does not name the landing set it is waiting for", reason)
	}
}

// TestHandler_IssueClosed_CascadePRLegMergesRetiredSetMember: the
// other reading of the member label, pinned where it is reachable.
// With the set retired — the decomposer deletes rather than closes, so
// the membership edge is gone — there is no set whose decision to wait
// for, and the cascade's PR leg merges the member's approved PR like
// any other newly unblocked one (§forgejo/decisions/sync-set,
// §forgejo/decisions/unblock-cascade).
func TestHandler_IssueClosed_CascadePRLegMergesRetiredSetMember(t *testing.T) {
	eng, api, q, store, _ := cascadePRSetFixtures(t)
	delete(api.blocks, 10) // the set was retired: no membership edge left
	h := NewHandler(eng, "s3cret", nil)

	rec := serve(t, h, signedRequest(t, "s3cret", "issues", "closed", issuePayload("o/r", 7, "closed")))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	tasks := q.all()
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks, want 1 (merge-or-wait for the orphaned member's PR); all = %+v", len(tasks), tasks)
	}
	if !strings.Contains(tasks[0].Prompt, "action=merge-or-wait repo=o/r kind=pr number=9") {
		t.Errorf("prompt = %q, want merge-or-wait for o/r PR 9", tasks[0].Prompt)
	}
	w, err := store.Load(Key{Repo: "o/r", Kind: KindPR, Number: 9})
	if err != nil {
		t.Fatalf("load watermark: %v", err)
	}
	if w == nil || w.Action != string(ActionMergeOrWait) || w.Revision != "sha-9" {
		t.Errorf("PR watermark = %+v, want merge-or-wait at sha-9", w)
	}
}

// TestHandler_Reconcile_SyncMemberReadsItsFirstOpenPR: the
// one-delivery-per-member authoring contract — where a member
// nonetheless stands with several open pull requests auto-closing it,
// the set reads it through the first one in the repository listing and
// the second does not hold the set
// (§forgejo/decisions/sync-set, §forgejo/webhook/re-fetch).
func TestHandler_Reconcile_SyncMemberReadsItsFirstOpenPR(t *testing.T) {
	eng, api, q, store := newTestEngine(t)
	api.repos = syncOutcomesRepos()
	syncSetFixtures(api)
	first := api.pulls[9]              // open, mergeable, approved at head, auto-closes member 4
	second := testPR(12, "open", true) // open and mergeable, but no reviews at all
	second.Body = "Another delivery.\n\nFixes #4"
	first.Body = "The delivery.\n\nFixes #4"
	api.pulls[12] = second
	api.openPulls["r"] = []PullRequest{*first, *second}

	eng.HandleEvent(Event{Type: EventReconcile, Repo: "o/outcomes", Kind: KindIssue, Number: 11, Sender: "maitred"})
	tasks := q.all()
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks, want 1: the member is ready through its first open PR", len(tasks))
	}
	if !strings.Contains(tasks[0].Prompt, "action=sync repo=o/outcomes kind=issue number=11") {
		t.Errorf("prompt = %q, want the sync dispatch for o/outcomes issue 11", tasks[0].Prompt)
	}
	w, err := store.Load(Key{Repo: "o/outcomes", Kind: KindIssue, Number: 11})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if w == nil || w.Action != string(ActionSync) {
		t.Errorf("sync watermark = %+v, want sync at the set revision", w)
	}
}

// TestHandler_Reconcile_SyncSetFindsMemberPRsThroughAutoCloseReferences:
// the shape the live sets stand in (s-git/maitred#171). Each member
// delivers through an open, mergeable, approved-at-head pull request
// that auto-closes the member in its body — the pull-request-side
// relation the forge honours when the delivery merges — and no
// PR-to-issue blocking edge exists anywhere: no open pull request
// stands in either member's dependency rows, and the predicate reads
// neither the members' dependencies nor the pull requests' blocks.
// The set dispatches; strip the references from the bodies and the
// same set reads as not ready and holds off silently, at no new
// watermark (§forgejo/decisions/sync-set).
func TestHandler_Reconcile_SyncSetFindsMemberPRsThroughAutoCloseReferences(t *testing.T) {
	eng, api, q, store := newTestEngine(t)
	api.repos = syncOutcomesRepos()
	memberA := testIssue(4, "open")
	memberA.Labels = append(memberA.Labels, LabelAgenticSyncMerge)
	memberB := testIssue(6, "open")
	memberB.Labels = append(memberB.Labels, LabelAgenticSyncMerge)
	set := testIssue(11, "open")
	set.Labels = append(set.Labels, LabelSync)
	set.Repository = "o/outcomes"
	api.issues[4] = memberA
	api.issues[6] = memberB
	api.issues[11] = set
	api.deps[11] = []Issue{*memberA, *memberB}
	// No PR-to-issue blocking edge: neither member's dependency rows
	// hold an open pull request.
	api.deps[4] = nil
	api.deps[6] = nil
	prA := testPR(9, "open", true)
	prA.Body = "Retires the widget.\n\nFixes #4"
	prB := testPR(12, "open", true)
	prB.Body = "Wires the gadget.\n\nFixes o/r#6"
	api.pulls[9] = prA
	api.pulls[12] = prB
	api.openPulls["r"] = []PullRequest{*prA, *prB}
	api.reviews[9] = []Review{{Event: ReviewApproved, Author: "bob", CommitID: prA.HeadSHA, SubmittedAt: testUpdatedAt}}
	api.reviews[12] = []Review{{Event: ReviewApproved, Author: "bob", CommitID: prB.HeadSHA, SubmittedAt: testUpdatedAt}}

	eng.HandleEvent(Event{Type: EventReconcile, Repo: "o/outcomes", Kind: KindIssue, Number: 11, Sender: "maitred"})
	tasks := q.all()
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks, want the sync dispatch — the predicate must find the deliveries through the auto-close references, not through an edge nobody records: %+v", len(tasks), tasks)
	}
	if !strings.Contains(tasks[0].Prompt, "action=sync repo=o/outcomes kind=issue number=11") {
		t.Errorf("prompt = %q, want the sync dispatch for o/outcomes issue 11", tasks[0].Prompt)
	}
	w, err := store.Load(Key{Repo: "o/outcomes", Kind: KindIssue, Number: 11})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if w == nil || w.Action != string(ActionSync) {
		t.Fatalf("sync watermark = %+v, want sync", w)
	}
	// The read the retired edge would have required is never performed:
	// the member dependency rows are not consulted for a pull request.
	if n := api.callCount("IssueDependencies:r/4"); n != 0 {
		t.Errorf("IssueDependencies on member 4 called %d times, want 0: the predicate reads the relation from the pull-request side", n)
	}
	if n := api.callCount("IssueDependencies:r/6"); n != 0 {
		t.Errorf("IssueDependencies on member 6 called %d times, want 0", n)
	}

	// An unchanged set does not re-fire.
	eng.HandleEvent(Event{Type: EventReconcile, Repo: "o/outcomes", Kind: KindIssue, Number: 11, Sender: "maitred"})
	if tasks := q.all(); len(tasks) != 1 {
		t.Errorf("got %d tasks after the second sweep, want 1", len(tasks))
	}

	// Without the references the predicate no longer sees the
	// deliveries: the set holds off silently.
	bareA := testPR(9, "open", true)
	bareB := testPR(12, "open", true)
	api.openPulls["r"] = []PullRequest{*bareA, *bareB}
	eng.HandleEvent(Event{Type: EventReconcile, Repo: "o/outcomes", Kind: KindIssue, Number: 11, Sender: "maitred"})
	if tasks := q.all(); len(tasks) != 1 {
		t.Errorf("got %d tasks after the references went away, want 1: a member with no auto-closing pull request has no delivery", len(tasks))
	}
}

// TestPRAutoClosesIssue pins the matcher against the forge's own
// reference semantics: the default closing keywords, keyword and
// reference adjacent (a colon and a space allowed between them), the
// #N form resolved in the pull request's own repository and the
// owner/repo#N form resolved across repositories
// (https://forgejo.org/docs/latest/user/linked-references/,
// §forgejo/decisions/sync-set).
func TestPRAutoClosesIssue(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"bare same-repo reference", "Summary.\n\nFixes #4", true},
		{"qualified same-repo reference", "Summary.\n\nFixes o/r#4", true},
		{"keyword closes", "closes #4", true},
		{"keyword closed", "closed #4", true},
		{"keyword close", "close #4", true},
		{"keyword fix", "fix #4", true},
		{"keyword fixed", "fixed #4", true},
		{"keyword resolve", "resolve #4", true},
		{"keyword resolves", "resolves #4", true},
		{"keyword resolved", "resolved #4", true},
		{"case insensitive", "FIXES #4", true},
		{"colon form", "Fixes: #4", true},
		{"reference later in body", "See #9 for context.\n\nThis branch fixes #4 in passing.", true},
		{"reference on the next line", "Fixes #5\nFixes #4", true},
		{"other issue", "Fixes #5", false},
		{"other repository", "Fixes o/other#4", false},
		{"no keyword", "Delivers #4", false},
		{"keyword not adjacent", "Fixes the issue #4", false},
		{"non-closing keyword", "reopens #4", false},
		{"number prefix only", "Fixes #40", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := prAutoClosesIssue(tc.body, "o/r", "o/r", 4); got != tc.want {
				t.Errorf("prAutoClosesIssue(%q, o/r, o/r, 4) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

// TestHandler_IssueClosed_SyncCandidateOnMislabelledIssueDispatchesNothing:
// a grouping issue misauthorised with the sync label alongside
// outcome claims two kinds, and the sweep's row names that conflict
// instead of letting label enumeration order pick a prompt — the
// sweep being the set's only backstop. The sibling cascade guard
// holds here too: nothing is enqueued and no watermark is recorded
// against sync, so the queue and the decision log cannot disagree
// (§forgejo/decisions/sync-set, §forgejo/decisions/unblock-cascade).
func TestHandler_IssueClosed_SyncCandidateOnMislabelledIssueDispatchesNothing(t *testing.T) {
	eng, api, q, store := newTestEngine(t)
	h := NewHandler(eng, "s3cret", nil)
	api.repos = syncOutcomesRepos()
	syncSetFixtures(api)
	api.issues[11].Labels = append(api.issues[11].Labels, LabelOutcome) // sync + outcome: authoring error
	api.issues[4] = testIssue(4, "closed")                              // fully unblocked: the outcome branch dispatches
	api.deps[11] = []Issue{*api.issues[4], *api.issues[5]}
	api.issues[7] = testIssue(7, "closed")
	api.blocks[7] = []Issue{*api.issues[11]}

	rec := serve(t, h, signedRequest(t, "s3cret", "issues", "closed", issuePayload("o/r", 7, "closed")))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if tasks := q.all(); len(tasks) != 0 {
		t.Errorf("got %d tasks, want none: a grouping issue claiming two kinds dispatches neither %s nor %s",
			len(tasks), ActionWrapUp, ActionSync)
	}
	w, err := store.Load(Key{Repo: "o/outcomes", Kind: KindIssue, Number: 11})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if w != nil {
		t.Errorf("watermark = %+v, want none: a forked candidate must not mark sync done", w)
	}
}

// TestHandler_Reconcile_MislabelledGroupingIssueDispatchesNothing:
// the sweep is the set's only backstop, and the reproduction the
// review describes is this row — a grouping issue carrying sync
// alongside outcome must never take the outcome branch, whose
// watermark would record wrap-up against a set that then never
// lands. The kind conflict is named and nothing is dispatched
// (§forgejo/decisions/sync-set, §forgejo/reconciliation).
func TestHandler_Reconcile_MislabelledGroupingIssueDispatchesNothing(t *testing.T) {
	eng, api, q, store := newTestEngine(t)
	api.repos = syncOutcomesRepos()
	syncSetFixtures(api)
	api.issues[11].Labels = append(api.issues[11].Labels, LabelOutcome) // sync + outcome: authoring error
	api.issues[4] = testIssue(4, "closed")                              // every member closed: the outcome branch would dispatch
	api.deps[11] = []Issue{*api.issues[4], *api.issues[5]}

	eng.HandleEvent(Event{Type: EventReconcile, Repo: "o/outcomes", Kind: KindIssue, Number: 11, Sender: "maitred"})
	if tasks := q.all(); len(tasks) != 0 {
		t.Fatalf("got %d tasks, want none: the sync row is tested before the kind it conflicts with, not after it", len(tasks))
	}
	w, err := store.Load(Key{Repo: "o/outcomes", Kind: KindIssue, Number: 11})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if w != nil && w.Action == string(ActionWrapUp) {
		t.Errorf("watermark = %+v, want none naming wrap-up: the grouping issue's set would never land", w)
	}
}

// TestHandler_IssueClosed_CascadeSkipsSetStalledOnAHuman: a sync
// issue is a permanent neighbour of its members' forests while the
// set is authoring, so the walk must not pay the set read — the
// grouping issue, its dependency rows, and per open member its
// dependencies, PR and reviews — on every closure. An open member
// without the member label is a member awaiting a human, which the
// graph states for free: no candidate, no set read, and the sweep
// stays the backstop (§forgejo/decisions/sync-set,
// §forgejo/decisions/unblock-cascade).
func TestHandler_IssueClosed_CascadeSkipsSetStalledOnAHuman(t *testing.T) {
	eng, api, q, store := newTestEngine(t)
	h := NewHandler(eng, "s3cret", nil)
	api.repos = syncOutcomesRepos()
	syncSetFixtures(api)
	api.issues[5] = testIssue(5, "open") // open, unlabelled: a member awaiting a human
	api.deps[11] = []Issue{*api.issues[4], *api.issues[5]}
	api.issues[7] = testIssue(7, "closed")
	api.blocks[7] = []Issue{*api.issues[11]}

	rec := serve(t, h, signedRequest(t, "s3cret", "issues", "closed", issuePayload("o/r", 7, "closed")))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if tasks := q.all(); len(tasks) != 0 {
		t.Fatalf("got %d tasks, want none: a set stalled on a human is not ready", len(tasks))
	}
	// The fake records each call as "<op>:<repo name>/<number>".
	for _, op := range []string{"GetIssue:outcomes/11", "GetPullRequest:r/9", "ListPullRequestReviews:r/9"} {
		if n := api.callCount(op); n != 0 {
			t.Errorf("%s called %d time(s): a set the graph already shows stalled on a member awaiting a human must not be read", op, n)
		}
	}
	w, err := store.Load(Key{Repo: "o/outcomes", Kind: KindIssue, Number: 11})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if w != nil {
		t.Errorf("sync watermark = %+v, want none: nothing was dispatched", w)
	}
}

// TestHandler_Reconcile_SyncIssueInadmissibleWithoutOutcomesRole: a
// sync issue in a repository holding no outcomes role is not
// dispatched — sync is admissible by the outcomes role of the
// repository holding the grouping issue alone
// (§forgejo/webhook/repository-roles).
func TestHandler_Reconcile_SyncIssueInadmissibleWithoutOutcomesRole(t *testing.T) {
	eng, api, q, _ := newTestEngine(t)
	syncSetFixtures(api)
	api.issues[11].Repository = "o/r" // a work-item repository, no outcomes role

	eng.HandleEvent(Event{Type: EventReconcile, Repo: "o/r", Kind: KindIssue, Number: 11, Sender: "maitred"})
	if tasks := q.all(); len(tasks) != 0 {
		t.Errorf("got %d tasks, want none: sync is inadmissible without the outcomes role", len(tasks))
	}
}

// TestHandler_Sync_NoPrompt_HoldsOffWithoutWatermark: with no canned
// prompt configured for sync the action cannot fire, and the failure
// says so: nothing is enqueued and no watermark is recorded, so the
// sweep retries — the activation shape decompose already takes
// (§forgejo/config/canned-prompts, §forgejo/webhook/the-pipeline).
func TestHandler_Sync_NoPrompt_HoldsOffWithoutWatermark(t *testing.T) {
	eng, api, q, store := newTestEngine(t)
	cfg := testConfig()
	delete(cfg.Actions, string(ActionSync))
	eng.cfg = cfg
	api.repos = syncOutcomesRepos()
	syncSetFixtures(api)

	eng.HandleEvent(Event{Type: EventReconcile, Repo: "o/outcomes", Kind: KindIssue, Number: 11, Sender: "maitred"})
	if tasks := q.all(); len(tasks) != 0 {
		t.Fatalf("got %d tasks with no sync prompt configured, want none", len(tasks))
	}
	w, err := store.Load(Key{Repo: "o/outcomes", Kind: KindIssue, Number: 11})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if w != nil {
		t.Errorf("watermark = %+v, want none: a dispatch that cannot fire must not mark itself done", w)
	}
}

// directPRSetFixtures wires the shape whose predicate side syncMembers
// reads: the pull request blocks the grouping issue **directly**, so the
// PR is a member whose delivery is itself — the set's last open blocker,
// opted in, mergeable and approved at head, beside a closed member 5
// (§forgejo/decisions/sync-set).
func directPRSetFixtures(t *testing.T) (*Engine, *fakeAPI, *fakeQueue, *Store, *bytes.Buffer) {
	t.Helper()
	api := newFakeAPI()
	q := &fakeQueue{}
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	var log bytes.Buffer
	eng := NewEngine(api, store, testConfig(), q, slog.New(slog.NewJSONHandler(&log, nil)))
	api.repos = syncOutcomesRepos()

	syncIssue := testIssue(11, "open")
	syncIssue.Labels = []string{LabelSync}
	syncIssue.Repository = "o/outcomes"
	closedMember := testIssue(5, "closed")
	pr := testPR(9, "open", true)
	pr.Labels = []string{LabelAgenticSyncMerge}
	prAsIssue := testIssue(9, "open")
	prAsIssue.IsPull = true
	prAsIssue.Labels = []string{LabelAgenticSyncMerge}
	api.issues[11] = syncIssue
	api.issues[5] = closedMember
	api.pulls[9] = pr
	api.reviews[9] = []Review{{Event: ReviewApproved, Author: "bob", CommitID: pr.HeadSHA, SubmittedAt: testUpdatedAt}}
	api.blocks[9] = []Issue{*syncIssue} // the PR blocks the grouping issue itself
	api.deps[11] = []Issue{*closedMember, *prAsIssue}
	return eng, api, q, store, &log
}

// TestHandler_PRReviewed_DirectPRMemberHoldsOffAndDispatchesTheSet: the
// membership the row reads has to be visible to the approval side too.
// A pull request blocking a grouping issue directly is a member whose
// delivery is itself, so its approval never merges it: merge-or-wait is
// the outcome the labels table forbids, and the set's own decision lands
// the delivery instead (§forgejo/decisions/sync-set).
func TestHandler_PRReviewed_DirectPRMemberHoldsOffAndDispatchesTheSet(t *testing.T) {
	eng, _, q, store, log := directPRSetFixtures(t)
	h := NewHandler(eng, "s3cret", nil)

	rec := serve(t, h, signedRequest(t, "s3cret", "pull_request_review_approved", "approved", prPayload("o/r", 9, "approved")))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	tasks := q.all()
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks, want the one sync dispatch for 11 (the member PR must not merge): %+v", len(tasks), tasks)
	}
	if !strings.Contains(tasks[0].Prompt, "action=sync repo=o/outcomes kind=issue number=11") {
		t.Errorf("prompt = %q, want the sync dispatch for o/outcomes issue 11", tasks[0].Prompt)
	}
	if strings.Contains(tasks[0].Prompt, string(ActionMergeOrWait)) {
		t.Errorf("prompt = %q, want no merge-or-wait: a landing-set delivery is the set's to land", tasks[0].Prompt)
	}
	w, err := store.Load(Key{Repo: "o/r", Kind: KindPR, Number: 9})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if w != nil {
		t.Errorf("PR watermark = %+v, want none: the approval held off", w)
	}
	if reason := reasonFor(t, log, "o/r/pr/9"); !strings.Contains(reason, "sync set") {
		t.Errorf("approval hold-off %q does not name the landing set it is waiting for", reason)
	}
}

// TestHandler_PRReviewed_DirectPRMemberOfUnreadySetDispatchesNothing:
// the hold-off is the same whatever the set's readiness — an unready set
// must not have its member merged underneath it, and nothing dispatches
// until the set stands ready (§forgejo/decisions/sync-set).
func TestHandler_PRReviewed_DirectPRMemberOfUnreadySetDispatchesNothing(t *testing.T) {
	eng, api, q, store, log := directPRSetFixtures(t)
	stray := testIssue(12, "open") // open, unlabelled: a member awaiting a human
	api.issues[12] = stray
	api.deps[11] = append(api.deps[11], *stray)
	h := NewHandler(eng, "s3cret", nil)

	rec := serve(t, h, signedRequest(t, "s3cret", "pull_request_review_approved", "approved", prPayload("o/r", 9, "approved")))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if tasks := q.all(); len(tasks) != 0 {
		t.Errorf("got %d tasks, want none: an unready set dispatches nothing and merges nothing %+v", len(tasks), tasks)
	}
	w, err := store.Load(Key{Repo: "o/r", Kind: KindPR, Number: 9})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if w != nil {
		t.Errorf("PR watermark = %+v, want none: the approval held off", w)
	}
	if reason := reasonFor(t, log, "o/r/pr/9"); !strings.Contains(reason, "sync set") {
		t.Errorf("approval hold-off %q does not name the landing set it is waiting for", reason)
	}
}

// TestHandler_PRReviewed_DirectPRMemberOfRetiredSetMerges: the other
// reading of the direct edge, pinned beside it. With the grouping issue
// closed — the set landed or retired — there is no set whose decision to
// wait for, and the approval decides on through the merge gate like any
// other PR (§forgejo/decisions/sync-set).
func TestHandler_PRReviewed_DirectPRMemberOfRetiredSetMerges(t *testing.T) {
	eng, api, q, store, _ := directPRSetFixtures(t)
	api.issues[11].State = "closed"
	api.blocks[9] = []Issue{*api.issues[11]}
	// The retirement leaves the delivery's set membership stale, not
	// gating — but merging on its own is still opt-in, so the human's
	// lever on the orphaned delivery is the agentic-auto-merge label
	// (§forgejo/decisions/sync-set, §forgejo/decisions/merge-gate,
	// s-git/maitred#179).
	api.pulls[9].Labels = append(api.pulls[9].Labels, LabelAgenticAutoMerge)
	h := NewHandler(eng, "s3cret", nil)

	rec := serve(t, h, signedRequest(t, "s3cret", "pull_request_review_approved", "approved", prPayload("o/r", 9, "approved")))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	tasks := q.all()
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks, want 1 (merge-or-wait for the retired member's PR); all = %+v", len(tasks), tasks)
	}
	if !strings.Contains(tasks[0].Prompt, "action=merge-or-wait repo=o/r kind=pr number=9") {
		t.Errorf("prompt = %q, want merge-or-wait for o/r PR 9", tasks[0].Prompt)
	}
	w, err := store.Load(Key{Repo: "o/r", Kind: KindPR, Number: 9})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if w == nil || w.Action != string(ActionMergeOrWait) || w.Revision != "sha-9" {
		t.Errorf("PR watermark = %+v, want merge-or-wait at sha-9", w)
	}
}

// --- Forest teardown (§forgejo/decisions/forest-teardown) --------------

// teardownRootIssue returns an open root in the given repository carrying
// both reserved labels (§forgejo/decisions/forest-teardown).
func teardownRootIssue(repo string, number int) *Issue {
	return &Issue{
		Number: number, State: "open", UpdatedAt: testUpdatedAt,
		Repository: repo, Labels: []string{LabelIdeation, LabelTeardownForest},
		HTMLURL: "https://forge.example.com/" + repo + "/issues/" + strconv.Itoa(number),
	}
}

// forestMember is the issue a teardown root blocks: a standing forest,
// which is what makes the teardown dispatchable at all
// (§forgejo/decisions/forest-teardown).
func forestMember(repo string, number int) Issue {
	return Issue{Number: number, State: "open", Repository: repo}
}

// forestTracker is the root's tracker: a member by the root's membership
// edge, and the one member the teardown order spares — so its edge keeps
// standing after every deletable member is gone
// (§forgejo/decisions/forest-teardown, s-git/maitred#173).
func forestTracker(repo string, number int) Issue {
	return Issue{Number: number, State: "open", Repository: repo, Labels: []string{LabelOutcome}}
}

// TestHandler_TeardownEventPath_DispatchesTeardown: opening, editing or
// labelling an open root that carries the teardown label dispatches
// teardown-forest and records the watermark — the teardown row wins over
// the root's decompose row (§forgejo/decisions/forest-teardown).
func TestHandler_TeardownEventPath_DispatchesTeardown(t *testing.T) {
	for _, typ := range []EventType{EventIssueOpened, EventIssueEdited, EventIssueLabelsChanged} {
		t.Run(string(typ), func(t *testing.T) {
			eng, api, q, store := newOutcomesEngine(t)
			api.issues[7] = teardownRootIssue("o/out", 7)
			api.blocks[7] = []Issue{forestMember("o/out", 8)}

			eng.HandleEvent(Event{Type: typ, Repo: "o/out", Kind: KindIssue, Number: 7, Sender: "alice"})

			tasks := q.all()
			if len(tasks) != 1 {
				t.Fatalf("got %d tasks, want the one teardown dispatch: %v", len(tasks), tasks)
			}
			if !strings.Contains(tasks[0].Prompt, "action="+string(ActionTeardownForest)) {
				t.Errorf("prompt = %q, want the teardown-forest action", tasks[0].Prompt)
			}
			w, err := store.Load(Key{Repo: "o/out", Kind: KindIssue, Number: 7})
			if err != nil {
				t.Fatalf("load watermark: %v", err)
			}
			if w == nil || w.Action != string(ActionTeardownForest) {
				t.Errorf("watermark = %+v, want teardown-forest at the current revision", w)
			}
		})
	}
}

// TestHandler_TeardownSweepRefiresWhileTheForestStands pins the sweep's
// exemption at the engine: while the label stands on an open root the
// sweep dispatches again, so a teardown that died half way through is
// retried rather than remembered as done
// (§forgejo/reconciliation/watermark-exemptions).
func TestHandler_TeardownSweepRefiresWhileTheForestStands(t *testing.T) {
	eng, api, q, _ := newOutcomesEngine(t)
	api.issues[7] = teardownRootIssue("o/out", 7)
	api.blocks[7] = []Issue{forestMember("o/out", 8)}

	for i := 0; i < 2; i++ {
		eng.HandleEvent(Event{Type: EventReconcile, Repo: "o/out", Kind: KindIssue, Number: 7, Sender: "maitred"})
	}
	tasks := q.all()
	if len(tasks) != 2 {
		t.Fatalf("got %d tasks across two sweeps, want 2: %v", len(tasks), tasks)
	}
	for _, task := range tasks {
		if !strings.Contains(task.Prompt, "action="+string(ActionTeardownForest)) {
			t.Errorf("prompt = %q, want the teardown-forest action", task.Prompt)
		}
	}
}

// TestHandler_TeardownOutsideTheOutcomesRole pins the role boundary: the
// label on a root in a repository holding no outcomes role dispatches
// nothing, because teardown is the decomposer's work and the decomposer
// has no remit there (§forgejo/webhook/repository-roles).
func TestHandler_TeardownOutsideTheOutcomesRole(t *testing.T) {
	eng, api, q, store := newTestEngine(t)
	api.issues[7] = teardownRootIssue("o/r", 7)
	api.blocks[7] = []Issue{forestMember("o/r", 8)}

	eng.HandleEvent(Event{Type: EventIssueLabelsChanged, Repo: "o/r", Kind: KindIssue, Number: 7, Sender: "alice"})

	if tasks := q.all(); len(tasks) != 0 {
		t.Fatalf("got %d tasks, want none outside the outcomes role: %v", len(tasks), tasks)
	}
	w, err := store.Load(Key{Repo: "o/r", Kind: KindIssue, Number: 7})
	if err != nil {
		t.Fatalf("load watermark: %v", err)
	}
	if w != nil {
		t.Errorf("watermark = %+v, want none: a hold-off marks nothing done", w)
	}
}

// TestHandler_TeardownEmptyForestCostsNoAgentRun: the engine can see
// whether the root blocks anything, so an empty forest dispatches no
// agent at all — the re-firing exemption ends where the forest does, and
// a label standing on a root that never had a forest costs nothing
// (§forgejo/decisions/forest-teardown,
// §forgejo/reconciliation/watermark-exemptions).
func TestHandler_TeardownEmptyForestCostsNoAgentRun(t *testing.T) {
	eng, api, q, store := newOutcomesEngine(t)
	api.issues[7] = teardownRootIssue("o/out", 7)
	api.blocks[7] = nil

	for i := 0; i < 2; i++ {
		eng.HandleEvent(Event{Type: EventReconcile, Repo: "o/out", Kind: KindIssue, Number: 7, Sender: "maitred"})
	}
	if tasks := q.all(); len(tasks) != 0 {
		t.Fatalf("got %d tasks, want none: an empty forest is not work: %v", len(tasks), tasks)
	}
	w, err := store.Load(Key{Repo: "o/out", Kind: KindIssue, Number: 7})
	if err != nil {
		t.Fatalf("load watermark: %v", err)
	}
	if w != nil {
		t.Errorf("watermark = %+v, want none: a hold-off marks nothing done", w)
	}
	if n := api.callCount("IssueBlocks:out/7"); n != 2 {
		t.Errorf("IssueBlocks on the root called %d time(s), want one hop per sweep", n)
	}
}

// TestHandler_TeardownSparedTrackerCostsNoAgentRun: a teardown that
// deleted every member leaves the tracker standing by the order's own
// rule, and that spared member must not re-fire the order — the count
// the engine reads excludes trackers, so a forest reduced to its tracker
// costs no further agent runs on the sweep or the event path
// (§forgejo/decisions/forest-teardown, s-git/maitred#173).
func TestHandler_TeardownSparedTrackerCostsNoAgentRun(t *testing.T) {
	eng, api, q, store := newOutcomesEngine(t)
	api.issues[7] = teardownRootIssue("o/out", 7)
	api.blocks[7] = []Issue{forestTracker("o/out", 30)}

	for i := 0; i < 2; i++ {
		eng.HandleEvent(Event{Type: EventReconcile, Repo: "o/out", Kind: KindIssue, Number: 7, Sender: "maitred"})
	}
	if tasks := q.all(); len(tasks) != 0 {
		t.Fatalf("got %d tasks, want none: a spared tracker is not a forest to delete: %v", len(tasks), tasks)
	}
	w, err := store.Load(Key{Repo: "o/out", Kind: KindIssue, Number: 7})
	if err != nil {
		t.Fatalf("load watermark: %v", err)
	}
	if w != nil {
		t.Errorf("watermark = %+v, want none: a hold-off marks nothing done", w)
	}

	// The event path decides identically: an edit to a root whose only
	// remaining member is the spared tracker dispatches nothing.
	eng.HandleEvent(Event{Type: EventIssueEdited, Repo: "o/out", Kind: KindIssue, Number: 7, Sender: "alice"})
	if tasks := q.all(); len(tasks) != 0 {
		t.Fatalf("got %d tasks after an edit, want none: %v", len(tasks), tasks)
	}
}

// TestHandler_TeardownStillDispatchesBesideTheTracker: the exclusion is
// an exclusion, not a veto — a member the order would delete beside the
// spared tracker is still a standing forest, and the sweep dispatches
// (§forgejo/decisions/forest-teardown, s-git/maitred#173).
func TestHandler_TeardownStillDispatchesBesideTheTracker(t *testing.T) {
	eng, api, q, _ := newOutcomesEngine(t)
	api.issues[7] = teardownRootIssue("o/out", 7)
	api.blocks[7] = []Issue{forestTracker("o/out", 30), forestMember("o/out", 8)}

	eng.HandleEvent(Event{Type: EventReconcile, Repo: "o/out", Kind: KindIssue, Number: 7, Sender: "maitred"})

	tasks := q.all()
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks, want 1: a deletable member beside the tracker is a standing forest: %v", len(tasks), tasks)
	}
	if !strings.Contains(tasks[0].Prompt, "action="+string(ActionTeardownForest)) {
		t.Errorf("prompt = %q, want the teardown-forest action", tasks[0].Prompt)
	}
}

// TestHandler_RootPerformsNoForestHop pins the cost boundary: the forest
// hop belongs to teardown alone. A root without the label decides from
// its own state and blockers as before, so the emptiness test adds no
// fetch to the curation path (§forgejo/webhook/re-fetch).
func TestHandler_RootPerformsNoForestHop(t *testing.T) {
	eng, api, q, _ := newOutcomesEngine(t)
	api.issues[7] = &Issue{
		Number: 7, State: "open", UpdatedAt: testUpdatedAt,
		Repository: "o/out", Labels: []string{LabelIdeation},
		HTMLURL: "https://forge.example.com/o/out/issues/7",
	}

	eng.HandleEvent(Event{Type: EventIssueOpened, Repo: "o/out", Kind: KindIssue, Number: 7, Sender: "alice"})

	if tasks := q.all(); len(tasks) != 1 || !strings.Contains(tasks[0].Prompt, "action="+string(ActionDecompose)) {
		t.Fatalf("want the one decompose dispatch, got %+v", tasks)
	}
	if n := api.callCount("IssueBlocks:out/7"); n != 0 {
		t.Errorf("IssueBlocks on the root called %d time(s), want none for a root under no teardown order", n)
	}
}

// TestHandler_ReviewFanOut_EndToEnd: a PR opened dispatches one task
// per configured lens — distinct dedup keys, the lens persona and
// timeout — and records one subject slot per lens while leaving the
// main slot alone; the following fix-feedback dispatch writes the
// main slot and keeps the slots (§forgejo/decisions/review-lenses,
// §forgejo/state/watermark, §forgejo/webhook/the-pipeline).
func TestHandler_ReviewFanOut_EndToEnd(t *testing.T) {
	eng, api, q, store := newTestEngine(t)
	eng.cfg.Reviews = []ReviewLens{
		{Identity: "lens-a", Prompt: "/pr-review-a {{.PRURL}}", Persona: "persona-a", Timeout: 30 * time.Minute},
		{Identity: "lens-b", Prompt: "/pr-review-b {{.PRURL}}", Persona: "persona-b", Timeout: 30 * time.Minute},
	}
	h := NewHandler(eng, "s3cret", nil)
	api.pulls[9] = testPR(9, "open", true)

	serve(t, h, signedRequest(t, "s3cret", "pull_request", "opened", prPayload("o/r", 9, "opened")))

	tasks := q.all()
	if len(tasks) != 2 {
		t.Fatalf("got %d tasks, want one per lens: %+v", len(tasks), tasks)
	}
	for i, lens := range eng.cfg.Reviews {
		task := tasks[i]
		if task.Persona != "persona-"+string(lens.Identity[len(lens.Identity)-1:]) {
			t.Errorf("task %d persona = %q, want the lens persona", i, task.Persona)
		}
		if task.Timeout != 1800 {
			t.Errorf("task %d timeout = %d, want the lens timeout in seconds", i, task.Timeout)
		}
		if !strings.Contains(task.DedupKey, "review-"+lens.Identity+"-") {
			t.Errorf("task %d dedup key = %q, want the lens identity carried", i, task.DedupKey)
		}
	}
	w, err := store.Load(Key{Repo: "o/r", Kind: KindPR, Number: 9})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, lens := range eng.cfg.Reviews {
		if w.Subjects[lens.Identity] != (Slot{Action: string(ActionReview), Revision: "sha-9"}) {
			t.Errorf("subject slot %q = %+v, want review at the head", lens.Identity, w.Subjects[lens.Identity])
		}
	}
	if w.Action != "" {
		t.Errorf("main slot = %+v, want it untouched by a fan-out", *w)
	}

	// The chain closes with a rejection: fix-feedback lands in the
	// main slot, and the lens slots survive it.
	api.reviews[9] = []Review{
		{Author: "lens-a", Event: ReviewChangesRequested, CommitID: "sha-9"},
		{Author: "lens-b", Event: ReviewChangesRequested, CommitID: "sha-9"},
	}
	serve(t, h, signedRequest(t, "s3cret", "pull_request_review_rejected", "rejected", prPayload("o/r", 9, "rejected")))
	w, err = store.Load(Key{Repo: "o/r", Kind: KindPR, Number: 9})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if w.Action != string(ActionFixFeedback) || w.Revision != "sha-9" {
		t.Errorf("main slot = %+v, want fix-feedback at the head", *w)
	}
	for _, lens := range eng.cfg.Reviews {
		if _, ok := w.Subjects[lens.Identity]; !ok {
			t.Errorf("subject slot %q lost when fix-feedback was written", lens.Identity)
		}
	}
}
