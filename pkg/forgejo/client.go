// Package forgejo provides a read-only client for the Forgejo REST API v1.
//
// The client exposes exactly the operations the Forgejo engine's decision
// function and event/reconciliation paths need (§forgejo/client/operations).
// It has no mutation operations: the engine's side effects are dispatched
// tasks, never direct writes to Forgejo (§overview/non-goals).
package forgejo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// pageLimit is the page size used for paginated list endpoints. Forgejo
// caps the limit query parameter at 50.
const pageLimit = 50

// Repository is a Forgejo repository, slimmed to the fields the engine
// needs (§forgejo/client/operations).
type Repository struct {
	ID       int64
	Name     string
	FullName string
	// Owner is the login of the repository's owner.
	Owner   string
	HTMLURL string
	// Topics are the repository's topic tags; the engine uses them to find
	// the maitred-enabled repositories.
	Topics []string
}

// Issue is a Forgejo issue, slimmed to the fields the engine needs
// (§forgejo/client/operations).
type Issue struct {
	Number int
	Title  string
	// State is "open" or "closed".
	State string
	// Labels are the issue's label names.
	Labels []string
	// UpdatedAt is the issue's last update time; it serves as the issue's
	// activity revision for the watermark (§forgejo/client/operations).
	UpdatedAt time.Time
	HTMLURL   string
	// Repository is the "owner/repo" the issue belongs to. It is populated
	// by every operation so that cross-repo blockers can be followed by
	// the unblock cascade.
	Repository string
	// IsPull reports whether the issue is a pull request. Forgejo
	// represents pull requests as issues; the engine uses this to tell
	// connected PRs apart from connected issues among blockers and
	// blocks.
	IsPull bool
}

// PullRequest is a Forgejo pull request, slimmed to the fields the engine
// needs (§forgejo/client/operations).
type PullRequest struct {
	Number int
	Title  string
	// State is "open" or "closed".
	State string
	// Merged reports whether the PR has been merged.
	Merged bool
	// Mergeable reports whether the PR can be merged without conflicts.
	Mergeable bool
	// HeadSHA is the SHA of the PR's head commit; it serves as the PR's
	// activity revision for the watermark.
	HeadSHA string
	// Labels are the PR's label names.
	Labels    []string
	UpdatedAt time.Time
	HTMLURL   string
}

// Review is a pull request review, slimmed to the fields the engine needs
// (§forgejo/client/operations).
type Review struct {
	// Event is the review's state, e.g. "APPROVED", "CHANGES_REQUESTED",
	// or "COMMENT".
	Event string
	// Author is the login of the reviewing user.
	Author string
	// CommitID is the commit the review was submitted against.
	CommitID string
	// SubmittedAt is when the review was submitted.
	SubmittedAt time.Time
}

// API is the read-only Forgejo surface the engine depends on
// (§forgejo/client/operations). It is an interface so the decision
// function, webhook handler, and reconciliation sweep can be tested
// against a fake.
type API interface {
	// ListOrgRepositories lists every repository of the given org, with
	// topics (to find the maitred-enabled repos).
	ListOrgRepositories(org string) ([]Repository, error)
	// ListOpenIssues lists the open issues of a repository.
	ListOpenIssues(owner, repo string) ([]Issue, error)
	// ListOpenPullRequests lists the open pull requests of a repository.
	ListOpenPullRequests(owner, repo string) ([]PullRequest, error)
	// GetIssue fetches a single issue.
	GetIssue(owner, repo string, number int) (*Issue, error)
	// GetPullRequest fetches a single pull request.
	GetPullRequest(owner, repo string, number int) (*PullRequest, error)
	// IssueBlocks lists the issues the given issue blocks. The returned
	// issues carry their own Repository, which may differ from the
	// given one (cross-repo blockers).
	IssueBlocks(owner, repo string, number int) ([]Issue, error)
	// IssueDependencies lists the issues that block the given issue. The
	// returned issues carry their own Repository, which may differ from
	// the given one (cross-repo blockers).
	IssueDependencies(owner, repo string, number int) ([]Issue, error)
	// ListPullRequestReviews lists a pull request's reviews (event,
	// author, commit).
	ListPullRequestReviews(owner, repo string, number int) ([]Review, error)
}

// Client is a read-only client for the Forgejo REST API v1.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// New creates a Client for the given Forgejo base URL (e.g.
// "https://forge.example.com") and bearer token. If httpClient is nil, a
// default client with a 30-second timeout is used.
func New(baseURL, token string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    httpClient,
	}
}

// APIError is returned for non-2xx responses from the Forgejo API
// (§forgejo/client/errors).
type APIError struct {
	// StatusCode is the HTTP status code of the failed response.
	StatusCode int
	// Message is the API's error message, or the raw body when the
	// response is not a JSON error object.
	Message string
}

// Error implements the error interface.
func (e *APIError) Error() string {
	return fmt.Sprintf("forgejo: HTTP %d: %s", e.StatusCode, e.Message)
}

// ListOrgRepositories implements API.
func (c *Client) ListOrgRepositories(org string) ([]Repository, error) {
	batch, err := listAll[wireRepository](c, "/orgs/"+url.PathEscape(org)+"/repos")
	if err != nil {
		return nil, err
	}
	repos := make([]Repository, len(batch))
	for i, w := range batch {
		repos[i] = toRepository(w)
	}
	return repos, nil
}

// ListOpenIssues implements API.
func (c *Client) ListOpenIssues(owner, repo string) ([]Issue, error) {
	batch, err := listAll[wireIssue](c, repoPath(owner, repo)+"/issues", "state", "open")
	if err != nil {
		return nil, err
	}
	return toIssues(batch), nil
}

// ListOpenPullRequests implements API.
func (c *Client) ListOpenPullRequests(owner, repo string) ([]PullRequest, error) {
	batch, err := listAll[wirePullRequest](c, repoPath(owner, repo)+"/pulls", "state", "open")
	if err != nil {
		return nil, err
	}
	pulls := make([]PullRequest, len(batch))
	for i, w := range batch {
		pulls[i] = toPullRequest(w)
	}
	return pulls, nil
}

// GetIssue implements API.
func (c *Client) GetIssue(owner, repo string, number int) (*Issue, error) {
	var w wireIssue
	if err := c.getJSON(&w, repoPath(owner, repo)+"/issues/"+strconv.Itoa(number)); err != nil {
		return nil, err
	}
	issue := toIssue(w)
	return &issue, nil
}

// GetPullRequest implements API.
func (c *Client) GetPullRequest(owner, repo string, number int) (*PullRequest, error) {
	var w wirePullRequest
	if err := c.getJSON(&w, repoPath(owner, repo)+"/pulls/"+strconv.Itoa(number)); err != nil {
		return nil, err
	}
	pr := toPullRequest(w)
	return &pr, nil
}

// IssueBlocks implements API.
func (c *Client) IssueBlocks(owner, repo string, number int) ([]Issue, error) {
	batch, err := listAll[wireIssue](c, repoPath(owner, repo)+"/issues/"+strconv.Itoa(number)+"/blocks")
	if err != nil {
		return nil, err
	}
	return toIssues(batch), nil
}

// IssueDependencies implements API.
func (c *Client) IssueDependencies(owner, repo string, number int) ([]Issue, error) {
	batch, err := listAll[wireIssue](c, repoPath(owner, repo)+"/issues/"+strconv.Itoa(number)+"/dependencies")
	if err != nil {
		return nil, err
	}
	return toIssues(batch), nil
}

// ListPullRequestReviews implements API.
func (c *Client) ListPullRequestReviews(owner, repo string, number int) ([]Review, error) {
	batch, err := listAll[wireReview](c, repoPath(owner, repo)+"/pulls/"+strconv.Itoa(number)+"/reviews")
	if err != nil {
		return nil, err
	}
	reviews := make([]Review, len(batch))
	for i, w := range batch {
		reviews[i] = Review{
			Event:       w.State,
			Author:      w.User.Login,
			CommitID:    w.CommitID,
			SubmittedAt: w.SubmittedAt,
		}
	}
	return reviews, nil
}

// repoPath builds the escaped "/repos/{owner}/{repo}" path prefix.
func repoPath(owner, repo string) string {
	return "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo)
}

// listAll fetches a paginated list endpoint (Forgejo "page"/"limit"
// pagination), following pages until the server returns a short page.
// The result is never nil.
func listAll[T any](c *Client, path string, params ...string) ([]T, error) {
	all := []T{}
	page := 1
	for {
		q := url.Values{}
		q.Set("page", strconv.Itoa(page))
		q.Set("limit", strconv.Itoa(pageLimit))
		for i := 0; i+1 < len(params); i += 2 {
			q.Set(params[i], params[i+1])
		}
		var batch []T
		if err := c.getJSON(&batch, path+"?"+q.Encode()); err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) < pageLimit {
			return all, nil
		}
		page++
	}
}

// getJSON performs an authenticated GET on path (relative to the "/api/v1"
// prefix of the base URL) and decodes the JSON response into out.
// Non-2xx responses are returned as *APIError.
func (c *Client) getJSON(out any, path string) error {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, c.baseURL+"/api/v1"+path, nil)
	if err != nil {
		return fmt.Errorf("forgejo: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("forgejo: GET %s: %w", path, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return fmt.Errorf("forgejo: read response from %s: %w", path, err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{StatusCode: resp.StatusCode, Message: apiErrorMessage(body)}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("forgejo: decode response from %s: %w", path, err)
	}
	return nil
}

// apiErrorMessage extracts the "message" field from a Forgejo JSON error
// object, falling back to the raw (trimmed, truncated) body.
func apiErrorMessage(body []byte) string {
	var e struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &e); err == nil && e.Message != "" {
		return e.Message
	}
	s := strings.TrimSpace(string(body))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// Wire types mirror the (slimmed) JSON shapes of the Forgejo REST API v1.

type wireUser struct {
	Login string `json:"login"`
}

type wireLabel struct {
	Name string `json:"name"`
}

type wireRepository struct {
	ID       int64    `json:"id"`
	Name     string   `json:"name"`
	FullName string   `json:"full_name"`
	Owner    wireUser `json:"owner"`
	HTMLURL  string   `json:"html_url"`
	Topics   []string `json:"topics"`
}

type wireIssue struct {
	Number    int         `json:"number"`
	Title     string      `json:"title"`
	State     string      `json:"state"`
	Labels    []wireLabel `json:"labels"`
	UpdatedAt time.Time   `json:"updated_at"`
	HTMLURL   string      `json:"html_url"`
	IsPull    bool        `json:"is_pull"`
	// Repository is nil on some endpoints; the issue's home repo is then
	// known to the caller.
	Repository *struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

type wirePullRequest struct {
	Number    int    `json:"number"`
	Title     string `json:"title"`
	State     string `json:"state"`
	Merged    bool   `json:"merged"`
	Mergeable bool   `json:"mergeable"`
	Head      struct {
		SHA string `json:"sha"`
	} `json:"head"`
	Labels    []wireLabel `json:"labels"`
	UpdatedAt time.Time   `json:"updated_at"`
	HTMLURL   string      `json:"html_url"`
}

type wireReview struct {
	State       string    `json:"state"`
	User        wireUser  `json:"user"`
	CommitID    string    `json:"commit_id"`
	SubmittedAt time.Time `json:"submitted_at"`
}

func toRepository(w wireRepository) Repository {
	return Repository{
		ID:       w.ID,
		Name:     w.Name,
		FullName: w.FullName,
		Owner:    w.Owner.Login,
		HTMLURL:  w.HTMLURL,
		Topics:   w.Topics,
	}
}

func toIssue(w wireIssue) Issue {
	issue := Issue{
		Number:    w.Number,
		Title:     w.Title,
		State:     w.State,
		Labels:    labelNames(w.Labels),
		UpdatedAt: w.UpdatedAt,
		HTMLURL:   w.HTMLURL,
		IsPull:    w.IsPull,
	}
	if w.Repository != nil {
		issue.Repository = w.Repository.FullName
	}
	return issue
}

func toIssues(batch []wireIssue) []Issue {
	issues := make([]Issue, len(batch))
	for i, w := range batch {
		issues[i] = toIssue(w)
	}
	return issues
}

func toPullRequest(w wirePullRequest) PullRequest {
	return PullRequest{
		Number:    w.Number,
		Title:     w.Title,
		State:     w.State,
		Merged:    w.Merged,
		Mergeable: w.Mergeable,
		HeadSHA:   w.Head.SHA,
		Labels:    labelNames(w.Labels),
		UpdatedAt: w.UpdatedAt,
		HTMLURL:   w.HTMLURL,
	}
}

// labelNames maps wire labels to their names; a nil input yields a nil
// slice.
func labelNames(labels []wireLabel) []string {
	if labels == nil {
		return nil
	}
	names := make([]string, len(labels))
	for i, l := range labels {
		names[i] = l.Name
	}
	return names
}
