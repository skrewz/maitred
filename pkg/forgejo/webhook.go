package forgejo

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"maitred/pkg/queue"
)

// WebhookPath is the route the engine's handler serves
// (§forgejo/webhook/the-route).
const WebhookPath = "/v1/forgejo/all_events"

// The Forgejo webhook headers (§forgejo/webhook/the-pipeline).
const (
	// HeaderSignature carries the hex-encoded HMAC-SHA256 of the raw
	// body, computed with the shared secret.
	HeaderSignature = "X-Forgejo-Signature"
	// HeaderEventType carries the fine-grained event type (e.g.
	// "issues", "pull_request_sync").
	HeaderEventType = "X-Forgejo-Event-Type"
)

// maxWebhookBodySize caps the webhook body (mirrors the trigger
// handler's limit).
const maxWebhookBodySize = 10 << 20

// Engine is the Forgejo engine's event path: it re-fetches the
// authoritative state of an event's object, decides, dispatches canned
// tasks, and records watermarks (§forgejo/webhook/the-pipeline). The
// reconciliation sweep feeds the same path with synthetic events.
type Engine struct {
	api   API
	store *Store
	cfg   *Config
	queue queue.TaskQueueProvider
	log   *log.Logger
}

// NewEngine creates the event path over the given Forgejo API, watermark
// store, config, and queue provider.
func NewEngine(api API, store *Store, cfg *Config, q queue.TaskQueueProvider, logger *log.Logger) *Engine {
	if logger == nil {
		logger = log.Default()
	}
	return &Engine{api: api, store: store, cfg: cfg, queue: q, log: logger}
}

// HandleEvent processes one event end to end — re-fetch, decide,
// dispatch, watermark — under the key's lock, so concurrent events for
// the same key cannot race the watermark
// (§forgejo/webhook/serialisation). A re-fetch failure holds off: the
// reconciliation sweep re-derives from the source of truth
// (§forgejo/webhook/the-pipeline).
func (e *Engine) HandleEvent(ev Event) {
	key := ev.KeyOf()
	e.store.WithKeyLock(key, func() {
		ev, st, err := e.refetch(ev)
		if err != nil {
			e.log.Printf("forgejo: %s: re-fetch failed, holding off: %v", key, err)
			return
		}
		w, err := e.store.loadLocked(key)
		if err != nil {
			e.log.Printf("forgejo: %s: load watermark: %v (continuing without)", key, err)
			w = nil
		}
		d := Decide(ev, st, w)
		if d.Dispatched {
			e.log.Printf("forgejo: %s: %s: %s", key, d.Action, d.Reason)
			if err := e.dispatch(ev, st, d.Action); err != nil {
				// No watermark: the reconciliation sweep retries.
				e.log.Printf("forgejo: %s: dispatch %s failed: %v", key, d.Action, err)
				return
			}
			if err := e.store.saveLocked(key, Watermark{Action: string(d.Action), Revision: revisionFor(ev, st)}); err != nil {
				e.log.Printf("forgejo: %s: save watermark: %v", key, err)
			}
		} else {
			e.log.Printf("forgejo: %s: hold off: %s", key, d.Reason)
		}
		for _, ck := range d.Cascade {
			if err := e.dispatchCascade(ck, ev.Sender); err != nil {
				e.log.Printf("forgejo: %s: cascade dispatch for %s failed: %v", key, ck, err)
			}
		}
	})
}

// refetch re-fetches the authoritative state for ev from Forgejo
// (§forgejo/webhook/re-fetch). A pull_request event with action closed
// is refined by the re-fetch: a merged PR is a PR-merged event, an
// unmerged one stays a PR-closed event.
func (e *Engine) refetch(ev Event) (Event, State, error) {
	owner, name := splitRepo(ev.Repo)
	switch ev.Kind {
	case KindIssue:
		issue, err := e.api.GetIssue(owner, name, ev.Number)
		if err != nil {
			return ev, State{}, err
		}
		st := State{Issue: issue}
		switch ev.Type {
		case EventIssueOpened, EventReconcile:
			blockers, err := e.api.IssueDependencies(owner, name, ev.Number)
			if err != nil {
				return ev, State{}, err
			}
			st.Blockers = blockers
			for i := range blockers {
				if blockers[i].IsPull && blockers[i].State == "open" {
					st.ConnectedPRs = append(st.ConnectedPRs, pullRequestFromIssue(blockers[i]))
				}
			}
		case EventIssueClosed:
			root, err := e.graphNode(issue)
			if err != nil {
				return ev, State{}, err
			}
			st.CascadeRoot = root
		}
		return ev, st, nil
	case KindPR:
		pr, err := e.api.GetPullRequest(owner, name, ev.Number)
		if err != nil {
			return ev, State{}, err
		}
		st := State{PR: pr}
		switch ev.Type {
		case EventPROpened, EventPRSynced, EventPRReviewed, EventReconcile:
			reviews, err := e.api.ListPullRequestReviews(owner, name, ev.Number)
			if err != nil {
				return ev, State{}, err
			}
			st.Reviews = reviews
		case EventPRClosed:
			if pr.Merged {
				ev.Type = EventPRMerged
				blocks, err := e.api.IssueBlocks(owner, name, ev.Number)
				if err != nil {
					return ev, State{}, err
				}
				for i := range blocks {
					if !blocks[i].IsPull {
						root, err := e.graphNode(&blocks[i])
						if err != nil {
							return ev, State{}, err
						}
						st.CascadeRoot = root
						break
					}
				}
			}
		}
		return ev, st, nil
	}
	return ev, State{}, fmt.Errorf("unknown kind %q", ev.Kind)
}

// graphNode builds the blocker-graph node for issue: its blocks, each
// open block with its blockers, each closed block followed recursively —
// the shape the unblock cascade walks. A visited set terminates cycles
// (§forgejo/webhook/re-fetch).
func (e *Engine) graphNode(issue *Issue) (*IssueNode, error) {
	visited := map[Key]bool{issueKey(issue): true}
	return e.fillNode(issue, visited)
}

func (e *Engine) fillNode(issue *Issue, visited map[Key]bool) (*IssueNode, error) {
	node := &IssueNode{Issue: *issue}
	owner, name := splitRepo(issue.Repository)
	blocks, err := e.api.IssueBlocks(owner, name, issue.Number)
	if err != nil {
		return nil, err
	}
	node.Blocks = make([]IssueNode, len(blocks))
	for i := range blocks {
		b := blocks[i]
		node.Blocks[i].Issue = b
		bo, bn := splitRepo(b.Repository)
		switch b.State {
		case "open":
			blockers, err := e.api.IssueDependencies(bo, bn, b.Number)
			if err != nil {
				return nil, err
			}
			node.Blocks[i].Blockers = issueNodes(blockers)
		case "closed":
			k := issueKey(&b)
			if visited[k] {
				continue // cycle: leave the node as a leaf
			}
			visited[k] = true
			child, err := e.fillNode(&b, visited)
			if err != nil {
				return nil, err
			}
			node.Blocks[i] = *child
		}
	}
	return node, nil
}

// dispatch fills the action's canned prompt and enqueues the task
// (§forgejo/webhook/the-pipeline).
func (e *Engine) dispatch(ev Event, st State, action Action) error {
	pc, err := e.cfg.PromptFor(action)
	if err != nil {
		return err
	}
	p := Placeholders{
		Repo:   ev.Repo,
		Kind:   string(ev.Kind),
		Number: ev.Number,
		Sender: ev.Sender,
		Action: string(action),
	}
	if st.Issue != nil {
		p.IssueURL = st.Issue.HTMLURL
	}
	if st.PR != nil {
		p.PRURL = st.PR.HTMLURL
	}
	prompt, err := pc.Render(p)
	if err != nil {
		return err
	}
	task := &queue.Task{
		ID:      fmt.Sprintf("forgejoeng-%s-%s-%d-%s-%d", strings.ReplaceAll(ev.Repo, "/", "-"), ev.Kind, ev.Number, action, time.Now().UnixNano()),
		Prompt:  prompt,
		Persona: pc.Persona,
		Timeout: int(pc.Timeout.Seconds()),
	}
	if err := e.queue.AddTask(task); err != nil {
		return fmt.Errorf("enqueue %s for %s: %w", action, ev.KeyOf(), err)
	}
	e.log.Printf("forgejo: %s: dispatched %s", ev.KeyOf(), task.ID)
	return nil
}

// dispatchCascade dispatches implement for one key of the unblock
// cascade, applying the key's own watermark first, so an
// already-dispatched (action, revision) is not re-dispatched
// (§forgejo/decisions/unblock-cascade).
func (e *Engine) dispatchCascade(k Key, sender string) error {
	owner, name := splitRepo(k.Repo)
	issue, err := e.api.GetIssue(owner, name, k.Number)
	if err != nil {
		return err
	}
	if issue.State != "open" {
		return nil // closed in the meantime: nothing to dispatch
	}
	w, err := e.store.Load(k)
	if err != nil {
		e.log.Printf("forgejo: %s: load watermark: %v (continuing without)", k, err)
		w = nil
	}
	rev := issueRevision(issue)
	if alreadyDispatched(w, ActionImplement, rev) {
		return nil
	}
	ev := Event{Type: EventIssueOpened, Repo: k.Repo, Kind: KindIssue, Number: k.Number, Sender: sender}
	if err := e.dispatch(ev, State{Issue: issue}, ActionImplement); err != nil {
		return err
	}
	return e.store.Save(k, Watermark{Action: string(ActionImplement), Revision: rev})
}

// revisionFor is the revision a dispatch's watermark records: the PR
// head sha for pull requests, the issue's updated_at for issues
// (§forgejo/webhook/the-pipeline).
func revisionFor(ev Event, st State) string {
	if ev.Kind == KindPR && st.PR != nil {
		return st.PR.HeadSHA
	}
	if st.Issue != nil {
		return issueRevision(st.Issue)
	}
	return ""
}

// splitRepo splits an "owner/repo" full name.
func splitRepo(full string) (owner, name string) {
	i := strings.IndexByte(full, '/')
	if i < 0 {
		return full, ""
	}
	return full[:i], full[i+1:]
}

// issueKey is the watermark key of an issue.
func issueKey(issue *Issue) Key {
	return Key{Repo: issue.Repository, Kind: KindIssue, Number: issue.Number}
}

// pullRequestFromIssue projects a blocker that is a pull request onto
// the slim PR type (the fields the decision function needs).
func pullRequestFromIssue(issue Issue) PullRequest {
	return PullRequest{
		Number:  issue.Number,
		Title:   issue.Title,
		State:   issue.State,
		HTMLURL: issue.HTMLURL,
	}
}

// issueNodes projects issues onto blocker-graph nodes.
func issueNodes(issues []Issue) []IssueNode {
	nodes := make([]IssueNode, len(issues))
	for i := range issues {
		nodes[i].Issue = issues[i]
	}
	return nodes
}

// Handler serves the organisation's all-events webhook at WebhookPath
// (§forgejo/webhook/the-route).
type Handler struct {
	engine *Engine
	secret []byte
	log    *log.Logger
}

// NewHandler creates the webhook handler for the engine, verifying
// deliveries against the shared secret.
func NewHandler(engine *Engine, secret string, logger *log.Logger) *Handler {
	if logger == nil {
		logger = log.Default()
	}
	return &Handler{engine: engine, secret: []byte(secret), log: logger}
}

// ServeHTTP implements the pipeline (§forgejo/webhook/the-pipeline):
// verify, filter, re-fetch-then-decide (in the engine), acknowledge.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxWebhookBodySize)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	if !verifySignature(h.secret, body, r.Header.Get(HeaderSignature)) {
		h.log.Printf("forgejo: rejected delivery with invalid signature")
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	var p wireEvent
	if err := json.Unmarshal(body, &p); err != nil {
		// A signed but unparseable body is not an event we can act on;
		// acknowledge and move on.
		h.log.Printf("forgejo: unparseable payload: %v", err)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	ev, ok := mapEvent(r.Header.Get(HeaderEventType), p)
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	h.engine.HandleEvent(ev)
	w.WriteHeader(http.StatusNoContent)
}

// verifySignature reports whether sig is the hex-encoded HMAC-SHA256 of
// body under secret — the format Forgejo sends in X-Forgejo-Signature
// (§forgejo/webhook/the-pipeline).
func verifySignature(secret, body []byte, sig string) bool {
	if len(secret) == 0 || sig == "" {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))
	return subtle.ConstantTimeCompare([]byte(want), []byte(sig)) == 1
}

// wireEvent is the slim payload the engine reads from a Forgejo
// all-events delivery (§forgejo/webhook/event-mapping).
type wireEvent struct {
	Action     string `json:"action"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	Issue struct {
		Number int `json:"number"`
	} `json:"issue"`
	PullRequest struct {
		Number int `json:"number"`
	} `json:"pull_request"`
	Sender struct {
		Login string `json:"login"`
	} `json:"sender"`
}

// mapEvent maps a delivery (the X-Forgejo-Event-Type header and the
// payload) to a decision event, or (Event{}, false) when the delivery is
// cheaply ignored (§forgejo/webhook/event-mapping). A pull_request
// delivery with action closed is mapped to EventPRClosed; the re-fetch
// refines it to EventPRMerged when the PR is merged
// (§forgejo/webhook/re-fetch).
func mapEvent(eventType string, p wireEvent) (Event, bool) {
	repo := p.Repository.FullName
	sender := p.Sender.Login
	issue := func(typ EventType) (Event, bool) {
		if repo == "" || p.Issue.Number == 0 {
			return Event{}, false
		}
		return Event{Type: typ, Repo: repo, Kind: KindIssue, Number: p.Issue.Number, Sender: sender}, true
	}
	pr := func(typ EventType) (Event, bool) {
		if repo == "" || p.PullRequest.Number == 0 {
			return Event{}, false
		}
		return Event{Type: typ, Repo: repo, Kind: KindPR, Number: p.PullRequest.Number, Sender: sender}, true
	}

	switch eventType {
	case "issues":
		switch p.Action {
		case "opened":
			return issue(EventIssueOpened)
		case "edited":
			return issue(EventIssueEdited)
		case "closed":
			return issue(EventIssueClosed)
		}
	case "issue_label":
		if p.Action == "label_updated" || p.Action == "label_cleared" {
			return issue(EventIssueLabelsChanged)
		}
	case "issue_comment":
		if p.Action == "created" {
			return issue(EventIssueCommented)
		}
	case "pull_request":
		switch p.Action {
		case "opened":
			return pr(EventPROpened)
		case "closed":
			return pr(EventPRClosed)
		}
	case "pull_request_sync":
		if p.Action == "synchronized" {
			return pr(EventPRSynced)
		}
	case "pull_request_review_approved", "pull_request_review_rejected", "pull_request_review_comment":
		return pr(EventPRReviewed)
	}
	return Event{}, false
}
