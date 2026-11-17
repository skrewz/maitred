package forgejo

import (
	"fmt"
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
	// which the merge gate would fire at: an approval at head from
	// every lens, a mergeable head, no open work order ahead of it and
	// no landing set of its own still waiting, and which has stood that
	// way unmerged past the configured threshold
	// (§forgejo/observability/the-outcomes-view).
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
// state deserves, in the order the walk found them: the rows come from
// one blocks read, listed in the forge's own order, so one state
// enumerated twice answers alike without the walk imposing an order of
// its own. rootRepo is where the root lives: the enumeration resolves it
// once, because a blocks row — the root's own row included — may name no
// repository at all (§forgejo/client/operations).
//
// The walk is the digest's walk and asks the digest's questions of the
// same rows: a tracker row is the forest's gate and warns nothing of its
// own, a pull-request row is a delivery rather than a member
// (§forgejo/decisions/sync-set), and a closed member is done rather than
// waiting. A member's repository is admitted the way every other
// cross-repository read admits one — a blocking-edge row names whichever
// repository its author had in mind, so the walk asks the scope cache
// before it reads there, and reads nothing outside the engine's remit
// (§forgejo/webhook/repository-roles, §forgejo/webhook/re-fetch).
// Everything else the walk reads is read at the moment of the walk: the
// stale-merge bound is a comparison against the clock, never against a
// watermark, because the question is how long a human has been
// unnecessary rather than whether the engine has acted
// (§forgejo/observability/the-outcomes-view).
//
// A warning is stated once per reference, since one pull request may
// deliver two members and two ⚠️ for one wait would read as two waits.
//
// A failed read fails the walk. A warning that cannot be established is
// not a warning absent: the forest would present as healthy what the
// engine has not checked, the mistake the outcomes view refuses on every
// other path (§forgejo/observability/the-outcomes-view,
// §forgejo/decisions/actionable-state-digest). The states that are an
// ordinary answer rather than a failure — a repository outside the
// remit, one whose pull requests unit is off — are settled before the
// read, so what is left to fail is a read the engine should have been
// able to pay and could not.
func (e *Engine) forestWarnings(root *Issue, rootRepo string, blocked []Issue) ([]ForestWarning, error) {
	warnings := []ForestWarning{}
	seen := map[string]bool{}
	// add states one warning, once.
	add := func(w *ForestWarning) {
		if w == nil {
			return
		}
		key := w.Kind + " " + warningKey(w.Repository, w.Number)
		if seen[key] {
			return // one pull request delivering two members is one wait
		}
		seen[key] = true
		warnings = append(warnings, *w)
	}
	// The forest's own rows are not work orders: the root's
	// membership edge stands while the forest lives, so reading it
	// as a wait would keep every human task from ever warning, and
	// the tracker is a gate for the same reason. The stale walk
	// takes the same carve-out where the merge gate itself counts
	// the edge — the spec says why (§forgejo/decisions/commissioning,
	// §forgejo/observability/the-warnings-a-forest-carries).
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
		landing := hasLabel(member, LabelAgenticAutoMerge) || hasLabel(member, LabelAgenticSyncMerge)
		claimed := hasLabel(member, LabelHumanTask)
		if !landing && !claimed {
			continue // nothing here can wait on a person
		}
		repo := rowRepository(member, rootRepo)
		scope, err := e.repoRoles(repo)
		if err != nil {
			return nil, fmt.Errorf("scope of %s: %w", repo, err)
		}
		if !scope.any() {
			// Outside the engine's remit: neither read nor warned
			// on, as the walk's note above states once
			// (§forgejo/webhook/repository-roles).
			e.log.Debug("forest warnings: skipping a member outside the engine's remit",
				"forest", rootRepo+"#"+strconv.Itoa(root.Number),
				"member", repo+"#"+strconv.Itoa(member.Number))
			continue
		}
		owner, name := splitRepo(repo)
		// One blockers read answers both predicates: the human task
		// asks whether any work order still stands, the stale merge
		// asks the same question as the merge gate does
		// (§forgejo/decisions/merge-gate).
		rows, err := e.api.IssueDependencies(owner, name, member.Number)
		if err != nil {
			return nil, fmt.Errorf("blockers of %s#%d: %w", repo, member.Number, err)
		}
		work := workOrders(rows, repo, gates)
		if claimed {
			add(humanTaskWarning(member, repo, work, now))
		}
		if landing {
			warn, err := e.staleMergeWarning(member, repo, work, listings, now)
			if err != nil {
				return nil, err
			}
			add(warn)
		}
	}
	return warnings, nil
}

// staleMergeWarning asks whether the member's delivering pull request is
// the forest's stalled wait: an open pull request delivering the member
// — the delivery relation the landing-set predicate and the merge opt-in
// read, taken through the one helper that walks it
// (§forgejo/decisions/sync-set, §forgejo/decisions/merge-gate) — that is
// still open, still mergeable, and approved at head by every lens for
// longer than the configured threshold
// (§forgejo/observability/the-outcomes-view). The answer is nil, not an
// error, wherever a hold other than a person stands on the head — the
// holds the next paragraph names — except the forest's own membership
// edge, which the walk sets aside and the spec accounts for.
//
// work is the member's work-order blockers, forest rows and deliveries
// aside. An open one holds the warning back because the merge gate holds
// the merge off on it: the forest waits on that work, not on a reviewer
// or an operator (§forgejo/decisions/merge-gate). The forest's own rows
// are the one hold the walk sets aside even where the gate counts it,
// and why the ⚠️ may fire while the gate waits on the membership edge is
// the spec's (§forgejo/observability/the-warnings-a-forest-carries). A
// member of an open landing set holds the warning back for the gate's
// own reason, stated the other way: landing a set is the set's decision
// and never the individual pull request's, so an approved member whose
// set has not landed waits on the set's other members
// (§forgejo/decisions/sync-set).
func (e *Engine) staleMergeWarning(member *Issue, repo string, work []Issue, listings map[string][]PullRequest, now time.Time) (*ForestWarning, error) {
	for _, b := range work {
		if b.State == "open" {
			return nil, nil // the wait is the open work order, not a person
		}
	}
	pr, reviews, err := e.deliveryPR(member, repo, listings)
	if err != nil {
		return nil, err
	}
	if pr == nil {
		return nil, nil // the member delivers nothing
	}
	if !pr.Mergeable {
		return nil, nil // a head the rebase owns rather than a human
	}
	if !prReviewGate(reviews, e.cfg.Reviews, pr.HeadSHA).mergeFires() {
		return nil, nil // the chain owes a verdict, so the wait is the reviewers'
	}
	at := standingApprovalAt(reviews, e.cfg.Reviews, pr.HeadSHA)
	if at.IsZero() {
		// Unreachable against a real forge, which stamps every review:
		// an approval the chain stands on that carries no moment says
		// the wait cannot be dated, and saying nothing is honest where
		// borrowing another object's timestamp would be a guess.
		return nil, nil
	}
	age := now.Sub(at)
	if age <= e.cfg.staleMergeThreshold() {
		return nil, nil
	}
	// The membership edge is the one read this predicate pays for
	// itself, so it is the last thing asked: only a wait that would
	// already warn can be a set's landing rather than a person's.
	if hasLabel(member, LabelAgenticSyncMerge) {
		inSet, err := e.inOpenSyncSet(member, repo)
		if err != nil {
			return nil, err
		}
		if inSet {
			return nil, nil // the wait is the set's landing, not a person
		}
	}
	return &ForestWarning{
		Kind:       WarningStaleAgenticMerge,
		Repository: repo,
		Number:     pr.Number,
		Title:      pr.Title,
		HTMLURL:    pr.HTMLURL,
		AgeSeconds: age.Seconds(),
	}, nil
}

// humanTaskWarning states that an open member claimed by a human is what
// the forest now waits on: every work-order blocker is closed, so
// nothing but the person stands in the way
// (§forgejo/observability/the-outcomes-view). The wait is dated from the
// last blocker to close, since that is the moment the ticket became the
// person's; a member with no work-order blocker at all is dated from its
// own last activity, the newest moment the read carries.
func humanTaskWarning(member *Issue, repo string, work []Issue, now time.Time) *ForestWarning {
	var waited time.Time
	for _, b := range work {
		if b.State == "open" {
			return nil // the human cannot start yet: the wait is the blocker's
		}
		if b.UpdatedAt.After(waited) {
			waited = b.UpdatedAt
		}
	}
	if waited.IsZero() {
		waited = member.UpdatedAt
	}
	return &ForestWarning{
		Kind:       WarningHumanTaskBlocking,
		Repository: repo,
		Number:     member.Number,
		Title:      member.Title,
		HTMLURL:    member.HTMLURL,
		AgeSeconds: now.Sub(waited).Seconds(),
	}
}

// workOrders is a member's blockers as the warnings read them. A
// pull-request row is a delivery and never a work order, the way every
// other blocker read in the package treats it
// (§forgejo/decisions/merge-gate, §forgejo/webhook/re-fetch): counting
// it would silence the human-task warning for the one ticket whose
// delivery is up for review — the exact state the ⚠️ exists to publish.
// The forest's own rows are membership rather than a wait
// (§forgejo/decisions/commissioning).
func workOrders(rows []Issue, entered string, gates map[string]bool) []Issue {
	work := []Issue{}
	for i := range rows {
		b := &rows[i]
		if b.IsPull || gates[warningKey(rowRepository(b, entered), b.Number)] {
			continue
		}
		work = append(work, *b)
	}
	return work
}

// inOpenSyncSet reports whether a member stands in an open landing set —
// the membership edge the approval path's hold-off reads, taken the same
// way it is read on the event path: an open grouping issue the member
// blocks (§forgejo/decisions/sync-set, §forgejo/webhook/re-fetch). A
// member of no open set is a stale label the decomposer left behind, not
// a set to wait on, and so answers false here as it does there.
func (e *Engine) inOpenSyncSet(member *Issue, repo string) (bool, error) {
	owner, name := splitRepo(repo)
	rows, err := e.api.IssueBlocks(owner, name, member.Number)
	if err != nil {
		return false, fmt.Errorf("sync sets of %s#%d: %w", repo, member.Number, err)
	}
	for i := range rows {
		b := &rows[i]
		if !b.IsPull && b.State == "open" && hasLabel(b, LabelSync) {
			return true, nil
		}
	}
	return false, nil
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

// warningKey identifies an issue or pull request in the walk's sets: the
// set of the forest's own rows, and the set of references already
// warned on. The forge addresses a repository case-insensitively, so the
// key is lowercased as the scope cache keys one
// (§forgejo/webhook/the-scope-cache).
func warningKey(repo string, number int) string {
	return strings.ToLower(repo) + "#" + strconv.Itoa(number)
}
