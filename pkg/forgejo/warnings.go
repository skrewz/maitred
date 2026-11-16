package forgejo

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The forest warnings: the ⚠️ marks the outcomes view publishes per
// forest, saying which forests are waiting on a person
// (§forgejo/observability/the-outcomes-view). A warning is a reference
// plus a kind plus an age, so the dashboard links to the object holding
// the forest up rather than constructing the judgement from its own
// reads. Nothing here dispatches, holds off, or gates: the warning
// reports what the decision paths already know, and no decision reads it
// back (§forgejo/decisions/actions).

// The warning kinds the outcomes view publishes
// (§forgejo/observability/the-outcomes-view). They are the payload's
// vocabulary, not a label: the object a warning names carries no marker
// of its own saying it is stale.
const (
	// WarningStaleAgenticMerge names an open pull request delivering an
	// open forest member that opted its landing in —
	// LabelAgenticAutoMerge or LabelAgenticSyncMerge
	// (§forgejo/decisions/merge-gate, §forgejo/decisions/sync-set) —
	// whose review chain is an approval at head, whose head is
	// mergeable, and which has stood that way unmerged past the
	// configured threshold (§forgejo/observability/the-outcomes-view).
	WarningStaleAgenticMerge = "stale_agentic_merge"
	// WarningHumanTaskBlocking names an open forest member carrying
	// LabelHumanTask whose work-order blockers are all closed: the
	// forest's remaining wait is the person the label names
	// (§forgejo/decisions/transition-table,
	// §forgejo/observability/the-outcomes-view).
	WarningHumanTaskBlocking = "human_task_blocking"
)

// ForestWarning is one ⚠️ of a forest: the reference to the object
// holding the forest up — repository, number, title and HTML URL, the
// same four fields a forest row carries and for the same reason, since
// a member may live across the organisation
// (§forgejo/observability/the-outcomes-view) — the kind that says why it
// warns, and the age of the condition.
//
// The age is seconds rather than a Go duration because the payload is
// read by a browser: a time.Duration encodes as a bare integer of
// nanoseconds, and a field that does not state its unit is a unit
// accident waiting to happen.
type ForestWarning struct {
	Kind       string  `json:"kind"`
	Repository string  `json:"repository"`
	Number     int     `json:"number"`
	Title      string  `json:"title"`
	HTMLURL    string  `json:"html_url"`
	AgeSeconds float64 `json:"age_seconds"`
}

// forestWarnings walks a forest's members along the root's membership
// edges — the issues the root blocks, the one hop the forest is
// (§forgejo/decisions/commissioning) — and returns the warnings the
// state deserves, ordered by reference rather than in the order the forge
// listed the rows, so two enumerations of one state answer alike
// (§forgejo/observability/the-outcomes-view). rootRepo is where the root
// lives: the enumeration resolves it once, because a blocks row — the
// root's own row included — may name no repository at all
// (§forgejo/client/operations).
//
// The walk is the digest's walk and asks the digest's questions of the
// same rows: a tracker row is the forest's gate and warns nothing of its
// own, a pull-request row is a delivery rather than a member
// (§forgejo/decisions/sync-set), and a closed member is done rather than
// waiting. Everything the walk reads is read at the moment of the walk:
// the stale-merge bound is a comparison against the clock, never against
// a watermark, because the question is how long a human has been
// unnecessary rather than whether the engine has acted
// (§forgejo/observability/the-outcomes-view).
//
// A failed read fails the walk. A warning that cannot be established is
// not a warning absent: the forest would present as healthy what the
// engine has not checked, the mistake the outcomes view refuses on every
// other path (§forgejo/observability/the-outcomes-view,
// §forgejo/decisions/actionable-state-digest).
func (e *Engine) forestWarnings(root *Issue, rootRepo string, blocked []Issue) ([]ForestWarning, error) {
	warnings := []ForestWarning{}
	// The forest's own rows are not blockers of its members: the root's
	// membership edge stands while the forest lives, so reading it as a
	// work order would keep every human task from ever warning
	// (§forgejo/decisions/commissioning). The tracker is a gate for the
	// same reason.
	gates := map[string]bool{warningKey(rowRepository(root, rootRepo), root.Number): true}
	for i := range blocked {
		if !blocked[i].IsPull && isTracker(&blocked[i]) {
			gates[warningKey(rowRepository(&blocked[i], rootRepo), blocked[i].Number)] = true
		}
	}
	// One open-pull-request listing per repository the members live in,
	// the same sharing the landing-set predicate applies
	// (§forgejo/decisions/sync-set).
	listings := map[string][]PullRequest{}
	now := time.Now()
	for i := range blocked {
		member := &blocked[i]
		if member.IsPull || member.State != "open" || isTracker(member) {
			continue
		}
		repo := rowRepository(member, rootRepo)
		owner, name := splitRepo(repo)
		if hasLabel(member, LabelAgenticAutoMerge) || hasLabel(member, LabelAgenticSyncMerge) {
			warn, err := e.staleMergeWarning(member, repo, owner, name, listings, now)
			if err != nil {
				return nil, err
			}
			if warn != nil {
				warnings = append(warnings, *warn)
			}
		}
		if hasLabel(member, LabelHumanTask) {
			warn, err := e.humanTaskWarning(member, repo, owner, name, gates, now)
			if err != nil {
				return nil, err
			}
			if warn != nil {
				warnings = append(warnings, *warn)
			}
		}
	}
	sort.SliceStable(warnings, func(i, j int) bool {
		if warnings[i].Repository != warnings[j].Repository {
			return strings.ToLower(warnings[i].Repository) < strings.ToLower(warnings[j].Repository)
		}
		if warnings[i].Number != warnings[j].Number {
			return warnings[i].Number < warnings[j].Number
		}
		return warnings[i].Kind < warnings[j].Kind
	})
	return warnings, nil
}

// staleMergeWarning asks whether the member's delivering pull request is
// the forest's stalled wait: an open pull request whose body closes the
// member — the delivery relation the landing-set predicate and the merge
// opt-in read (§forgejo/decisions/sync-set,
// §forgejo/decisions/merge-gate) — that is still open, still mergeable,
// and approved at head by every lens for longer than the configured
// threshold (§forgejo/observability/the-outcomes-view). The listing is
// the lead and the single read the authority, as everywhere else
// (§forgejo/client/operations). The answer is nil, not an error, when
// the member delivers nothing, delivers something a human still owes a
// verdict on, or has not waited long enough.
func (e *Engine) staleMergeWarning(member *Issue, repo, owner, name string, listings map[string][]PullRequest, now time.Time) (*ForestWarning, error) {
	pulls, ok := listings[repo]
	if !ok {
		var err error
		pulls, err = e.api.ListOpenPullRequests(owner, name)
		if err != nil {
			return nil, fmt.Errorf("open pull requests of %s: %w", repo, err)
		}
		listings[repo] = pulls
	}
	for j := range pulls {
		if !prAutoClosesIssue(pulls[j].Body, repo, repo, member.Number) {
			continue
		}
		pr, err := e.api.GetPullRequest(owner, name, pulls[j].Number)
		if err != nil {
			return nil, fmt.Errorf("pull request %s#%d: %w", repo, pulls[j].Number, err)
		}
		if !prOpen(pr) || !pr.Mergeable {
			continue // merged, closed, or a head the rebase owns rather than a human
		}
		reviews, err := e.api.ListPullRequestReviews(owner, name, pulls[j].Number)
		if err != nil {
			return nil, fmt.Errorf("reviews of %s#%d: %w", repo, pulls[j].Number, err)
		}
		if !prReviewGate(reviews, e.cfg.Reviews, pr.HeadSHA).mergeFires() {
			continue // the chain owes a verdict, so the wait is the reviewers'
		}
		at := standingApprovalAt(reviews, e.cfg.Reviews, pr.HeadSHA)
		if at.IsZero() {
			// The listing carried no timestamp for the verdict that
			// satisfied the gate; the pull request's own last activity
			// is the newest moment known, so the age is measured from
			// it rather than from the epoch.
			at = pr.UpdatedAt
		}
		age := now.Sub(at)
		if age <= e.cfg.staleMergeThreshold() {
			continue
		}
		return &ForestWarning{
			Kind:       WarningStaleAgenticMerge,
			Repository: repo,
			Number:     pr.Number,
			Title:      pr.Title,
			HTMLURL:    pr.HTMLURL,
			AgeSeconds: ageSeconds(age),
		}, nil
	}
	return nil, nil
}

// humanTaskWarning asks whether an open member claimed by a human is
// what the forest now waits on: every blocker that is a work order — the
// forest's own rows aside, which are membership
// (§forgejo/decisions/commissioning) — is closed, so nothing but the
// person stands in the way (§forgejo/observability/the-outcomes-view).
// The wait is dated from the last blocker to close, since that is the
// moment the ticket became the person's; a member with no work-order
// blocker at all is dated from its own last activity, the newest moment
// the read carries.
func (e *Engine) humanTaskWarning(member *Issue, repo, owner, name string, gates map[string]bool, now time.Time) (*ForestWarning, error) {
	blockers, err := e.api.IssueDependencies(owner, name, member.Number)
	if err != nil {
		return nil, fmt.Errorf("blockers of %s#%d: %w", repo, member.Number, err)
	}
	var waited time.Time
	for _, b := range blockers {
		if gates[warningKey(rowRepository(&b, repo), b.Number)] {
			continue // the forest's own gate, not a work order
		}
		if b.State == "open" {
			return nil, nil // the human cannot start yet: the wait is the blocker's
		}
		if b.UpdatedAt.After(waited) {
			waited = b.UpdatedAt
		}
	}
	if waited.IsZero() {
		waited = member.UpdatedAt
	}
	age := now.Sub(waited)
	return &ForestWarning{
		Kind:       WarningHumanTaskBlocking,
		Repository: repo,
		Number:     member.Number,
		Title:      member.Title,
		HTMLURL:    member.HTMLURL,
		AgeSeconds: ageSeconds(age),
	}, nil
}

// rowRepository is the repository a blocks or dependencies row lives in,
// falling back to the repository the walk entered for a row that names
// none — the rule the forest rows and the digest both apply
// (§forgejo/client/operations, §forgejo/decisions/actionable-state-digest).
func rowRepository(row *Issue, entered string) string {
	if row.Repository != "" {
		return row.Repository
	}
	return entered
}

// warningKey identifies an issue in the walk's set of the forest's own
// rows. The forge addresses a repository case-insensitively, so the key
// is lowercased as the scope cache keys one
// (§forgejo/webhook/the-scope-cache).
func warningKey(repo string, number int) string {
	return strings.ToLower(repo) + "#" + strconv.Itoa(number)
}

// ageSeconds is a warning's age as the payload states it: never
// negative, because a timestamp the forge holds ahead of this engine's
// clock would otherwise state a wait that has not happened.
func ageSeconds(age time.Duration) float64 {
	if age < 0 {
		return 0
	}
	return age.Seconds()
}
