// Package forgejo provides the client for the Forgejo REST API v1.
//
// The client exposes exactly the operations the Forgejo engine's decision
// function and event/reconciliation paths need (§forgejo/client/operations).
// It carries one mutation — the issue-body write that lands a tracker's
// current-state block (§forgejo/client/the-one-mutation, §forgejo/reconciliation/the-tracker-block);
// every other side effect the engine has is a dispatched task
// (§overview/non-goals).
package forgejo

import (
	"bytes"
	"context"
	"crypto/tls"
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
	// Topics are the repository's topic tags; the engine uses them to
	// derive the repository's roles (§forgejo/webhook/repository-roles).
	Topics []string
	// HasPullRequests reports whether the repository has the pull
	// requests unit on (the listing's has_pull_requests field). A
	// repository with the unit off refuses the pull request endpoints
	// outright, so the sweep skips their listing rather than reading
	// the refusal as a failed read (§forgejo/reconciliation/the-sweep).
	HasPullRequests bool
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
	// LabelColours maps each label name to the colour the forge reports
	// for it, normalised to a lowercase `#rrggbb`: the chart's strokes
	// read a role label's colour from here, which is what makes a label
	// recoloured in the forge show in the next chart
	// (§forgejo/renderer/classes-and-strokes, §forgejo/client/operations).
	// A label the response reports no usable colour for is absent rather
	// than blank, so a caller cannot read a blank as a colour.
	LabelColours map[string]string
	// ClosedAt is the moment the issue closed, as the forge reports it,
	// and is zero while the issue is open. A rendered chart stamps a
	// closed member with it (§forgejo/renderer/closed-members):
	// UpdatedAt never stands in, because the last edit to a closed
	// issue is not the moment it closed (§forgejo/client/operations).
	ClosedAt time.Time
	// Author is the login of the user who opened the issue. It is
	// carried on the model and read by nothing in the engine:
	// authorship gates nothing — the forest-seeding label gates a
	// forest member
	// (§forgejo/decisions/transition-table, s-git/maitred#135).
	Author string
	// Body is the issue's text. Two seats read it: the landing set's
	// revision digests the grouping issue's body, because that is the
	// field naming the order the set must land in, and a restatement of
	// it is the human's lever on a landing run that reported a conflict
	// (§forgejo/decisions/sync-set); and the tracker-block write reads the
	// description it splices into
	// (§forgejo/reconciliation/the-tracker-block). The outcomes view draws
	// its picture from the graph and reads no body
	// (§forgejo/observability/the-outcomes-view). It is whatever
	// the endpoint reported, and every endpoint the engine reads an issue
	// from — the single-issue read and the dependency and blocks listings
	// alike — answers with this same model, body filled the same way
	// (§forgejo/client/operations).
	Body string
	// UpdatedAt is the issue's last update time; it serves as the issue's
	// activity revision for the watermark (§forgejo/client/operations).
	UpdatedAt time.Time
	HTMLURL   string
	// Repository is the "owner/repo" the issue belongs to. It is populated
	// by every operation so that cross-repo blockers can be followed by
	// the unblock cascade.
	Repository string
	// IsPull reports whether the issue is a pull request. Forgejo
	// represents pull requests as issues and marks them in the API
	// response with a non-null "pull_request" object (there is no
	// is_pull field in the issue API; that field exists only in the
	// webhook payloads); the engine uses this to tell connected PRs
	// apart from connected issues among blockers and blocks
	// (§forgejo/client/operations).
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
	// Author is the login of the user who opened the pull request. It
	// is read by the fix-feedback ownership gate: fix-feedback is
	// dispatched only when the PR's author is the configured
	// implementer identity (§forgejo/config/implementer-identity,
	// §forgejo/decisions/loop-prevention, s-git/maitred#143).
	Author string
	// Body is the pull request's text. Two seats read it: the landing
	// set's predicate resolves a member's delivering PR through the
	// auto-close reference the PR body carries — the same pull-request-
	// side relation the forge honours when a merged PR closes the member
	// (§forgejo/client/operations, §forgejo/decisions/sync-set,
	// s-git/maitred#171) — and the merge opt-in's issue seat is the
	// issue that same closing grammar names in the body, so the body
	// also gates every path that can dispatch merge-or-wait
	// (§forgejo/decisions/merge-gate, s-git/maitred#196).
	Body string
	// Labels are the PR's label names.
	Labels    []string
	UpdatedAt time.Time
	HTMLURL   string
}

// Review is a pull request review, slimmed to the fields the engine needs
// (§forgejo/client/operations).
type Review struct {
	// Event is the review's state, e.g. "APPROVED", "REQUEST_CHANGES",
	// or "COMMENT".
	Event string
	// Author is the login of the reviewing user.
	Author string
	// CommitID is the commit the review was submitted against.
	CommitID string
	// Dismissed reports whether the review has been dismissed — by
	// stale-approval dismissal on a push or by a maintainer by hand.
	// The review lenses' gates count **undismissed** approvals, so the
	// flag is the read that separates a standing approval at head from
	// one the forge has already voided (§forgejo/client/operations,
	// §forgejo/decisions/review-lenses).
	Dismissed bool
	// SubmittedAt is when the review was submitted.
	SubmittedAt time.Time
}

// Comment is a comment on an issue or pull request, slimmed to the
// fields the engine needs: the forest's actionable-state digest counts a comment as its
// author and its body alone, so nothing else about a comment — its
// timestamps, reactions, or the object it hangs off — is mapped
// (§forgejo/decisions/actionable-state-digest).
type Comment struct {
	// Author is the login of the user who wrote the comment; a
	// response without a user object yields an empty author.
	Author string
	// Body is the comment's text.
	Body string
}

// API is the Forgejo surface the engine depends on
// (§forgejo/client/operations). It is an interface so the decision
// function, webhook handler, and reconciliation sweep can be tested
// against a fake.
type API interface {
	// ListOrgRepositories lists every repository of the given org, with
	// topics (to derive the repository roles, §forgejo/webhook/repository-roles).
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
	// given one (cross-repo blockers). A listing that succeeded is
	// never nil — an empty one is an empty slice — so a nil result
	// means a read that failed (§forgejo/client/operations).
	IssueBlocks(owner, repo string, number int) ([]Issue, error)
	// IssueDependencies lists the issues that block the given issue. The
	// returned issues carry their own Repository, which may differ from
	// the given one (cross-repo blockers). As with IssueBlocks, a
	// listing that succeeded is never nil: empty means nothing blocks,
	// nil means the read failed (§forgejo/client/operations).
	IssueDependencies(owner, repo string, number int) ([]Issue, error)
	// ListPullRequestReviews lists a pull request's reviews (event,
	// author, commit).
	ListPullRequestReviews(owner, repo string, number int) ([]Review, error)
	// ListIssueComments lists an issue's comments (author, body). The
	// forest's actionable-state digest uses it to count comments by
	// anyone other than the engine's own account or the configured
	// decomposer identity as triggers
	// (§forgejo/decisions/actionable-state-digest).
	ListIssueComments(owner, repo string, number int) ([]Comment, error)
	// UpdateIssueBody replaces an issue's description. It is the one
	// mutation the engine makes, and it is narrow by construction: the
	// payload carries the body and nothing else, so no title, state,
	// label, milestone or assignment travels with it, and the write
	// cannot reopen or close what it rewrites. The sweep addresses only
	// a forest's tracker with it
	// (§forgejo/client/the-one-mutation, §forgejo/reconciliation/the-tracker-block).
	UpdateIssueBody(owner, repo string, number int, body string) error
}

// Client is the client for the Forgejo REST API v1.
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

// ClientCertEnvVar is the environment variable holding the path to the mTLS
// client certificate the Forgejo client presents
// (§forgejo/client/configuration).
const ClientCertEnvVar = "MAITRED_FORGEJO_CLIENT_CERT"

// ClientKeyEnvVar is the environment variable holding the path to the mTLS
// client private key (§forgejo/client/configuration).
const ClientKeyEnvVar = "MAITRED_FORGEJO_CLIENT_KEY"

// loadClientCertificate loads the mTLS pair the environment names, or
// (nil, nil) when the environment names neither and the client stays
// plain (§forgejo/client/configuration). Setting exactly one of the two
// is an environment the engine cannot serve, so it wraps
// ErrEnvironmentIncomplete and classifies as *credentials missing*; a
// pair that is named but will not load is a fault the named causes do
// not describe (§forgejo/observability/why-the-engine-is-off). Startup
// and the offline classifier share this one function, so the two can
// never disagree about the pair.
func loadClientCertificate(certPath, keyPath string) (*tls.Certificate, error) {
	if certPath == "" && keyPath == "" {
		return nil, nil
	}
	if certPath == "" || keyPath == "" {
		return nil, fmt.Errorf("%w: %s and %s must be set together",
			ErrEnvironmentIncomplete, ClientCertEnvVar, ClientKeyEnvVar)
	}
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("forgejo: load mTLS client certificate/key: %w", err)
	}
	return &cert, nil
}

// CheckClientCertificate answers whether the mTLS pair the environment
// names can be loaded, without building anything: it is the offline half
// of NewClient, so a health probe can ask the same question startup
// answers (§forgejo/client/configuration,
// §forgejo/observability/why-the-engine-is-off).
func CheckClientCertificate(certPath, keyPath string) error {
	_, err := loadClientCertificate(certPath, keyPath)
	return err
}

// NewClient builds the *http.Client the Forgejo client uses. When both
// certPath and keyPath are set, the client presents that mTLS client
// certificate on every request (a Forgejo instance's ingress may require a
// client certificate); when neither is set it is a plain client so non-mTLS
// Forgejo instances keep working. Setting exactly one of the two is a
// configuration error (§forgejo/client/configuration).
func NewClient(certPath, keyPath string) (*http.Client, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	cert, err := loadClientCertificate(certPath, keyPath)
	if err != nil {
		return nil, err
	}
	if cert == nil {
		return client, nil
	}
	client.Transport = &http.Transport{
		TLSClientConfig: &tls.Config{
			Certificates: []tls.Certificate{*cert},
			RootCAs:      nil, // trust system CAs
		},
	}
	return client, nil
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
			Dismissed:   w.Dismissed,
			SubmittedAt: w.SubmittedAt,
		}
	}
	return reviews, nil
}

// ListIssueComments implements API.
func (c *Client) ListIssueComments(owner, repo string, number int) ([]Comment, error) {
	batch, err := listAll[wireComment](c, repoPath(owner, repo)+"/issues/"+strconv.Itoa(number)+"/comments")
	if err != nil {
		return nil, err
	}
	comments := make([]Comment, len(batch))
	for i, w := range batch {
		comments[i] = Comment{Author: w.User.Login, Body: w.Body}
	}
	return comments, nil
}

// UpdateIssueBody implements API. It is the one mutation the client carries:
// the PATCH carries `body` and nothing else, so the fields the engine has no
// business changing are never sent, and a body is the only thing the forge may
// change (§forgejo/client/the-one-mutation). It travels the same transport
// every read travels — the auth header and the non-2xx rule live in
// doJSON alone, so a second operation cannot drift away from them
// (§forgejo/client/errors).
func (c *Client) UpdateIssueBody(owner, repo string, number int, body string) error {
	_, err := c.doJSON(http.MethodPatch, repoPath(owner, repo)+"/issues/"+strconv.Itoa(number), map[string]any{"body": body})
	return err
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

// doJSON is this file's one transport: an authenticated request with method
// on path (relative to the "/api/v1" prefix of the base URL), whose non-nil
// payload is sent as a JSON body, whose response body it returns, and whose
// non-2xx answer is an *APIError. Every operation here — each read and the
// engine's one mutation — goes through it, so the auth header, the response
// ceiling and the non-2xx rule are stated once and a new operation cannot
// drift away from them (§forgejo/client/operations, §forgejo/client/errors).
func (c *Client) doJSON(method, path string, payload any) ([]byte, error) {
	var reqBody io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("forgejo: encode request for %s: %w", path, err)
		}
		reqBody = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, c.baseURL+"/api/v1"+path, reqBody)
	if err != nil {
		return nil, fmt.Errorf("forgejo: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("forgejo: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, fmt.Errorf("forgejo: read response from %s: %w", path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &APIError{StatusCode: resp.StatusCode, Message: apiErrorMessage(body)}
	}
	return body, nil
}

// getJSON performs an authenticated GET on path and decodes the JSON
// response into out (§forgejo/client/operations).
func (c *Client) getJSON(out any, path string) error {
	body, err := c.doJSON(http.MethodGet, path, nil)
	if err != nil {
		return err
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
	Name  string `json:"name"`
	Color string `json:"color"` // the hex the forge draws the label with, sans '#'
}

type wireRepository struct {
	ID       int64    `json:"id"`
	Name     string   `json:"name"`
	FullName string   `json:"full_name"`
	Owner    wireUser `json:"owner"`
	HTMLURL  string   `json:"html_url"`
	Topics   []string `json:"topics"`
	// HasPullRequests is the repository's pull requests unit
	// (has_pull_requests); see Repository.HasPullRequests.
	HasPullRequests bool `json:"has_pull_requests"`
}

type wireIssue struct {
	Number    int         `json:"number"`
	Title     string      `json:"title"`
	Body      string      `json:"body"`
	State     string      `json:"state"`
	Labels    []wireLabel `json:"labels"`
	User      wireUser    `json:"user"` // the issue's author (§forgejo/decisions/loop-prevention)
	UpdatedAt time.Time   `json:"updated_at"`
	ClosedAt  time.Time   `json:"closed_at"`
	HTMLURL   string      `json:"html_url"`
	// PullRequest is non-nil only when the issue is a pull request —
	// the issue API's indicator (the API's PullRequestMeta object,
	// slimmed to its presence); the issue API has no is_pull field.
	PullRequest *struct{} `json:"pull_request"`
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
	User      wireUser    `json:"user"`
	Labels    []wireLabel `json:"labels"`
	Body      string      `json:"body"`
	UpdatedAt time.Time   `json:"updated_at"`
	HTMLURL   string      `json:"html_url"`
}

type wireReview struct {
	State       string    `json:"state"`
	User        wireUser  `json:"user"`
	CommitID    string    `json:"commit_id"`
	Dismissed   bool      `json:"dismissed"`
	SubmittedAt time.Time `json:"submitted_at"`
}

type wireComment struct {
	Body string   `json:"body"`
	User wireUser `json:"user"`
}

func toRepository(w wireRepository) Repository {
	return Repository{
		ID:       w.ID,
		Name:     w.Name,
		FullName: w.FullName,
		Owner:    w.Owner.Login,
		HTMLURL:  w.HTMLURL,
		Topics:   w.Topics,
		// The repository units the sweep consults (§forgejo/reconciliation/the-sweep).
		HasPullRequests: w.HasPullRequests,
	}
}

func toIssue(w wireIssue) Issue {
	issue := Issue{
		Number:       w.Number,
		Title:        w.Title,
		Body:         w.Body,
		State:        w.State,
		Labels:       labelNames(w.Labels),
		LabelColours: labelColours(w.Labels),
		Author:       w.User.Login,
		UpdatedAt:    w.UpdatedAt,
		ClosedAt:     w.ClosedAt,
		HTMLURL:      w.HTMLURL,
		IsPull:       w.PullRequest != nil,
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
		Author:    w.User.Login,
		Labels:    labelNames(w.Labels),
		Body:      w.Body,
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

// labelColours maps each label name to the colour the forge reports for it,
// normalised to the `#rrggbb` a chart's stroke must be
// (§forgejo/renderer/classes-and-strokes). The forge reports the hex without
// its '#'; a label whose colour is absent or is no six-digit hex is left out
// rather than mapped to something a chart could draw with, because a missing
// colour reads as a missing stroke and stops the write
// (§forgejo/reconciliation/the-tracker-block).
func labelColours(labels []wireLabel) map[string]string {
	var colours map[string]string
	for _, l := range labels {
		hex, ok := strokeHex(l.Color)
		if !ok {
			continue
		}
		if colours == nil {
			colours = make(map[string]string, len(labels))
		}
		colours[l.Name] = hex
	}
	return colours
}

// strokeHex normalises a hex the forge reports for a label to `#rrggbb`, and
// reports whether anything usable was there: upper- and lower-case spellings
// become one, a leading '#' is accepted and always emitted.
func strokeHex(colour string) (string, bool) {
	hex := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(colour), "#"))
	if len(hex) != 6 {
		return "", false
	}
	for _, r := range hex {
		if r >= '0' && r <= '9' || r >= 'a' && r <= 'f' {
			continue
		}
		return "", false
	}
	return "#" + hex, true
}
