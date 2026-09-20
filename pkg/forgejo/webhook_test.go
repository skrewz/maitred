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
	"log"
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
	cfg := &Config{Org: "o", ReconcileInterval: 15 * time.Minute}
	cfg.Actions = make(map[string]PromptConfig, len(AllActions))
	for _, a := range AllActions {
		cfg.Actions[string(a)] = PromptConfig{
			Prompt:  "action={{.Action}} repo={{.Repo}} kind={{.Kind}} number={{.Number}} issue={{.IssueURL}} pr={{.PRURL}} sender={{.Sender}}",
			Persona: "persona-" + string(a),
			Timeout: 2 * time.Hour,
		}
	}
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
		Labels:     []string{"agentic-auto-merge"},
		UpdatedAt:  testUpdatedAt,
		HTMLURL:    "https://forge.example.com/o/r/issues/" + strconv.Itoa(number),
		Repository: "o/r",
	}
}

// testPR returns a fixture pull request in repo o/r.
func testPR(number int, state string, mergeable bool) *PullRequest {
	return &PullRequest{
		Number:    number,
		Title:     "pr " + strconv.Itoa(number),
		State:     state,
		HeadSHA:   "sha-" + strconv.Itoa(number),
		Mergeable: mergeable,
		UpdatedAt: testUpdatedAt,
		HTMLURL:   "https://forge.example.com/o/r/pulls/" + strconv.Itoa(number),
	}
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

	// GetIssue blocking hooks for the concurrency tests: GetIssue
	// closes getIssueEntered (once) on entry, then waits on
	// blockGetIssue when it is set.
	blockGetIssue   chan struct{}
	getIssueEntered chan struct{}
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
	if f.err != nil {
		return nil, f.err
	}
	issue, ok := f.issues[number]
	if !ok {
		return nil, &APIError{StatusCode: http.StatusNotFound, Message: "issue not found"}
	}
	return issue, nil
}

func (f *fakeAPI) GetPullRequest(owner, repo string, number int) (*PullRequest, error) {
	f.record("GetPullRequest:" + repo + "/" + strconv.Itoa(number))
	if f.err != nil {
		return nil, f.err
	}
	pr, ok := f.pulls[number]
	if !ok {
		return nil, &APIError{StatusCode: http.StatusNotFound, Message: "pull request not found"}
	}
	return pr, nil
}

func (f *fakeAPI) ListOrgRepositories(org string) ([]Repository, error) {
	f.record("ListOrgRepositories:" + org)
	if f.err != nil {
		return nil, f.err
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
	return f.openIssues[repo], nil
}

func (f *fakeAPI) ListOpenPullRequests(owner, repo string) ([]PullRequest, error) {
	f.record("ListOpenPullRequests:" + repo)
	if f.err != nil {
		return nil, f.err
	}
	if err, ok := f.listPullsErr[repo]; ok {
		return nil, err
	}
	return f.openPulls[repo], nil
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
	if f.err != nil {
		return nil, f.err
	}
	return f.blocks[number], nil
}

func (f *fakeAPI) IssueDependencies(owner, repo string, number int) ([]Issue, error) {
	f.record("IssueDependencies:" + repo + "/" + strconv.Itoa(number))
	if f.err != nil {
		return nil, f.err
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
	eng := NewEngine(api, store, testConfig(), q, log.New(io.Discard, "", 0))
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
		{"pull_request_comment", "created"},
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
			setup: func(api *fakeAPI) {
				api.pulls[9] = testPR(9, "open", true)
				api.reviews[9] = []Review{{Author: "bob", Event: ReviewApproved}}
			},
			want: ActionReReview,
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
				api.reviews[9] = []Review{{Author: "bob", Event: ReviewApproved}}
			},
			want: "",
		},
		{
			name:      "PR reviewed, approved",
			eventType: "pull_request_review_approved", action: "approved",
			payload: prPayload("o/r", 9, "approved"),
			setup: func(api *fakeAPI) {
				api.pulls[9] = testPR(9, "open", true)
				api.reviews[9] = []Review{{Author: "bob", Event: ReviewApproved}}
			},
			want: ActionMergeOrWait,
		},
		{
			name:      "PR reviewed, rejected",
			eventType: "pull_request_review_rejected", action: "rejected",
			payload: prPayload("o/r", 9, "rejected"),
			setup: func(api *fakeAPI) {
				api.pulls[9] = testPR(9, "open", true)
				api.reviews[9] = []Review{{Author: "bob", Event: ReviewChangesRequested}}
			},
			want: ActionFixFeedback,
		},
		{
			name:      "PR reviewed, comment, hold off",
			eventType: "pull_request_review_comment", action: "created",
			payload: prPayload("o/r", 9, "created"),
			setup: func(api *fakeAPI) {
				api.pulls[9] = testPR(9, "open", true)
				api.reviews[9] = []Review{{Author: "bob", Event: "COMMENT"}}
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
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng, api, q, _ := newTestEngine(t)
			h := NewHandler(eng, "s3cret", nil)
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
	api.err = errors.New("boom")

	rec := serve(t, h, signedRequest(t, "s3cret", "issues", "opened", issuePayload("o/r", 7, "opened")))
	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rec.Code)
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
	// Issue 7 in o/r (closed) blocks issue 10 in o/other (open).
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
