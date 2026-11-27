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
// reads. The same walk records the held-back view's rows — the same
// waits with the waited-on object's labels alongside
// (§forgejo/observability/the-held-back-view). Nothing here dispatches,
// holds off, or gates: the warning reports what the decision paths
// already know, and no decision reads it back (§forgejo/decisions/actions).

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

// HeldBackItem is one row of the held-back view: the reference, kind
// and age of a forest warning — the same wait stated in the same words
// (§forgejo/observability/the-warnings-a-forest-carries) — alongside
// the labels the waited-on object carries, the field that turns a ⚠️
// into the overview a human can act on without opening the object
// first. A row carries no object body, and its labels are the object's
// whole set: the spec states that line as one disclosure decision
// (§forgejo/observability/the-held-back-view).
type HeldBackItem struct {
	ForestWarning
	Labels []string `json:"labels"`
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
// The same record feeds the held-back view: each stated wait also
// becomes a held-back row carrying the walked row's labels alongside
// (§forgejo/observability/the-held-back-view).
//
// A failed read fails the walk. A warning that cannot be established is
// not a warning absent: the forest would present as healthy what the
// engine has not checked, the mistake the outcomes view refuses on every
// other path (§forgejo/observability/the-outcomes-view,
// §forgejo/decisions/actionable-state-digest). The forge's definitive 404
// excepts itself only at the read that first asks whether an object is
// there; each such read names its own site, and the rule's fail-closed
// half — the `404` further down the fan-out, or at a later read of an
// object this walk established — is the spec's
// (§forgejo/observability/the-warnings-a-forest-carries). The states that
// are an ordinary answer rather than a failure — a repository outside the
// remit, one whose pull requests unit is off, a delivery whose graph reaches
// past the read budget — are settled before the read or hold their warning
// back, so what is left to fail is a read the engine should have been able
// to pay and could not.
func (e *Engine) forestWarnings(root *Issue, rootRepo string, blocked []Issue) ([]ForestWarning, []HeldBackItem, error) {
	warnings := []ForestWarning{}
	held := []HeldBackItem{}
	seen := map[string]bool{}
	// add states one warning, once, and records the same wait as a
	// held-back row: warning and row are one statement in two shapes,
	// so neither can exist without the other
	// (§forgejo/observability/the-held-back-view).
	add := func(item *HeldBackItem) {
		if item == nil {
			return
		}
		key := waitKey(item.Kind, item.Repository, item.Number)
		if seen[key] {
			return // one pull request delivering two members is one wait
		}
		seen[key] = true
		warnings = append(warnings, item.ForestWarning)
		held = append(held, *item)
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
		inRemit, err := e.remitAdmits(repo, rootRepo)
		if err != nil {
			return nil, nil, fmt.Errorf("scope of %s: %w", repo, err)
		}
		if !inRemit {
			// Outside the engine's remit: neither read nor warned
			// on, as the walk's note above states once
			// (§forgejo/webhook/repository-roles).
			e.log.Debug("forest warnings: skipping a member outside the engine's remit",
				"forest", rootRepo+"#"+strconv.Itoa(root.Number),
				"member", repo+"#"+strconv.Itoa(member.Number))
			continue
		}
		if claimed {
			// A human task waits on the member's own work orders —
			// the question the delivery's wait does not ask, since a
			// delivery whose connected issues hold open work is the
			// stale predicate's to hold back on
			// (§forgejo/observability/the-warnings-a-forest-carries).
			owner, name := splitRepo(repo)
			rows, err := e.api.IssueDependencies(owner, name, member.Number)
			if isNotFound(err) {
				// The member's own read, so this is its row that dangled
				// since the listing (§forgejo/webhook/re-fetch,
				// s-git/maitred#268). Its delivery is another object, and
				// is still read below.
				e.log.Info("forest warnings: member not found; nothing waits on it",
					"member", repo+"#"+strconv.Itoa(member.Number),
					"error", err)
			} else if err != nil {
				return nil, nil, fmt.Errorf("blockers of %s#%d: %w", repo, member.Number, err)
			} else {
				add(humanTaskWarning(member, repo, workOrders(rows, repo, gates), now))
			}
		}
		if landing {
			item, err := e.staleMergeWarning(member, repo, gates, listings, now)
			if err != nil {
				return nil, nil, err
			}
			add(item)
		}
	}
	return warnings, held, nil
}

// repoInRemit reports whether a repository is one the engine may read at
// all — the admission test every cross-repository read pays, asked of
// the scope cache so it costs no forge read (§forgejo/webhook/repository-roles,
// §forgejo/webhook/re-fetch). A non-nil error means the scope is unknown
// and the caller must hold off naming the failure; a false answer means a
// repository no maitred role names.
func (e *Engine) repoInRemit(repo string) (bool, error) {
	roles, err := e.repoRoles(repo)
	if err != nil {
		return false, err
	}
	return roles.any(), nil
}

// remitAdmits is the remit test every cross-repository read pays, stated
// once so the rule cannot update one walk's loop and miss another: the
// repository the walk entered is admitted by the walk itself — the
// enumeration read the forest there — and every other repository must be
// one a maitred role names. Its readers are the graph's four
// (§forgejo/reconciliation/the-tracker-block), alongside the warning walk
// and the merge reads (§forgejo/webhook/repository-roles,
// §forgejo/webhook/re-fetch). The premise of the first clause is that the
// caller established the entry: a pass that admitted no repository — the
// inert close, which runs because the repository it arrived in holds no
// role — passes the empty string, which admits nothing, so every
// repository is named by a role in its own right.
// A non-nil error means the scope is unknown, which is the caller's
// failure to name, not a hold-off; a false answer is the caller's
// ordinary silence to log in its own words.
func (e *Engine) remitAdmits(repo, entered string) (bool, error) {
	if entered != "" && strings.EqualFold(repo, entered) {
		return true, nil // the walk's own repository is admitted already
	}
	return e.repoInRemit(repo)
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
// Whether anything but a person stands on the head is the merge gate's
// question, so it is asked the gate's way: through the connected-issues
// read the gate reads (§forgejo/webhook/re-fetch), whose open blockers
// the gate's own mergeGateOpenBlockers names and whose landing-set
// membership its syncMemberSetHoldOff names. The ⚠️ therefore does not
// keep a second copy of "stale" that a change to the gate could silently
// strand — it asks the gate whether the gate would fire, and says nothing
// while the gate waits on work (§forgejo/decisions/merge-gate,
// §forgejo/decisions/sync-set). Two answers the gate path forces on a
// merge are turned the other way here, because the view has no merge to
// protect and an unchecked wait is not asserted: a graph that reaches past
// the read budget, and a reference or blocking-edge row naming a repository
// outside the remit, hold the warning back rather than failing the whole
// view (§forgejo/webhook/re-fetch,
// §forgejo/webhook/repository-roles). The forest's own rows are the one
// hold the walk sets aside even where the gate counts it, and why the ⚠️
// may fire while the gate waits on the membership edge is the spec's
// (§forgejo/observability/the-warnings-a-forest-carries).
func (e *Engine) staleMergeWarning(member *Issue, repo string, gates map[string]bool, listings map[string][]PullRequest, now time.Time) (*HeldBackItem, error) {
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
	// Everything beyond the head — the blockers of every issue the
	// delivery connects, the landing set whose landing it waits on — is
	// the last thing asked, paid for only by a wait that would already
	// warn.
	holds, err := e.gateHoldsDeliveryOff(pr, repo, gates)
	if err != nil || holds {
		return nil, err
	}
	return &HeldBackItem{
		ForestWarning: ForestWarning{
			Kind:       WarningStaleAgenticMerge,
			Repository: repo,
			Number:     pr.Number,
			Title:      pr.Title,
			HTMLURL:    pr.HTMLURL,
			AgeSeconds: age.Seconds(),
		},
		Labels: labelsOrEmpty(pr.Labels),
	}, nil
}

// gateHoldsDeliveryOff asks a delivery the two questions that hold the
// merge gate off — the open blockers of its connected issues
// (§forgejo/decisions/merge-gate) and membership of an open landing set
// (§forgejo/decisions/sync-set) — through the gate's own readings, so
// "the gate would fire at this head" is one implementation's answer and
// not two that must be kept alike.
//
// The delivery's body chooses which issues the ask reaches, so it is
// screened against the two bounds the gate's resolving loop carries —
// the read budget and the remit — before anything is read, and a graph
// that cannot be read completely holds the warning back rather than
// buying reads or failing the view (§forgejo/webhook/re-fetch,
// §forgejo/webhook/repository-roles, §forgejo/observability/the-warnings-a-forest-carries).
// What the screen admits is then assembled and resolved the way the gate
// does, so the states the forge itself ignores — a dangling reference, a
// reference naming a pull request — arrive as the same ordinary answers
// here as they do there, and the two predicates are never handed two
// different states (§forgejo/webhook/re-fetch). A true answer says the
// wait is that work or that set, not a person — or that the wait cannot
// be established, which warns nothing for the same reason.
func (e *Engine) gateHoldsDeliveryOff(pr *PullRequest, repo string, gates map[string]bool) (bool, error) {
	rows, askable, err := e.admittedGraph(pr, repo)
	if err != nil {
		return false, err
	}
	if !askable {
		return true, nil
	}
	owner, name := splitRepo(repo)
	// The state the two predicates read is the gate's own assembly: the
	// connected issues of the body and the blocking edges, then the
	// landing-set membership those rows imply — the call the approval path
	// makes, so a field the assembly grows cannot be present for a merge
	// and zero for the walk (§forgejo/webhook/re-fetch,
	// §forgejo/decisions/sync-set). The blocking-edge listing is handed in
	// from the screen above (§forgejo/observability/the-warnings-a-forest-carries).
	st := State{PR: pr}
	if err := e.fetchPRSetState(owner, name, pr.Number, &st, rows); err != nil {
		return false, fmt.Errorf("delivery %s#%d: %w", repo, pr.Number, err)
	}
	if _, held := syncMemberSetHoldOff(pr, repo, st); held {
		return true, nil // the wait is the set's landing, not a person
	}
	// The event names the delivery alone — the two fields the gate's blocker
	// test consults, to exclude the delivery from its own blockers.
	open := mergeGateOpenBlockers(Event{Kind: KindPR, Repo: repo, Number: pr.Number}, st)
	for i := range open {
		if gates[warningKey(rowRepository(&open[i], repo), open[i].Number)] {
			continue // the forest's own rows are membership, not a wait
		}
		return true, nil // the wait is that open work, not a person
	}
	return false, nil
}

// admittedGraph reports whether the graph the stale question reaches can be
// read completely within the bounds the resolving loop carries — the read
// budget and the remit — before any of it is read. Two shapes choose what the
// ask reaches, and neither is the walk's author: the references the delivery's
// body names, which the grammar lists for free, and the delivery's blocking
// edges, which the walk lists before it reads a reference and whose rows name
// whichever repository their author had in mind. Both pay the remit test every
// other cross-repository read pays, and both are counted against the one budget
// the resolving loop charges, distinct objects the way that loop deduplicates
// them — so the reads the ask pays are bounded by a graph the delivery states,
// never by one a stranger draws (§forgejo/webhook/re-fetch,
// §forgejo/webhook/repository-roles,
// §forgejo/observability/the-warnings-a-forest-carries).
//
// A false answer does not say the delivery is healthy — it says the wait cannot
// be established, which is the one thing a warning may not paper over, and it
// leaves the line every other dropped relation leaves. One false is stronger
// than the other: the delivery's own blocking-edge listing answering the
// forge's 404 says the delivery is gone and the wait its edges stood on is
// gone with it. Both hold the warning back, and the walk publishes neither
// way. Unknown scope is the one answer that is not a hold-off but an error —
// scope the engine cannot
// establish fails the walk rather than reading on, here as everywhere else
// (§forgejo/webhook/the-scope-cache).
//
// A true answer carries the blocking-edge listing it screened, which the
// ask pays once and hands to the gate's own assembly — the listing paid
// for is the listing admitted
// (§forgejo/observability/the-warnings-a-forest-carries).
func (e *Engine) admittedGraph(pr *PullRequest, prRepo string) (rows []Issue, askable bool, err error) {
	delivery := prRepo + "#" + strconv.Itoa(pr.Number)
	refs := autoCloseRefs(pr.Body, prRepo)
	if len(refs) > maxAutoCloseRefs {
		e.log.Debug("forest warnings: holding a stale warning back: the body names more references than the read budget pays",
			"delivery", delivery, "references", len(refs))
		return nil, false, nil
	}
	seen := make(map[Key]bool, len(refs))
	for _, ref := range refs {
		if !validRepoRef(ref.repo) {
			e.log.Debug("forest warnings: holding a stale warning back: a reference names a path segment",
				"delivery", delivery)
			return nil, false, nil
		}
		seen[ref.key()] = true
		admits, err := e.remitAdmits(ref.repo, prRepo)
		if err != nil {
			return nil, false, fmt.Errorf("scope of %s: %w", ref.repo, err)
		}
		if !admits {
			e.log.Debug("forest warnings: holding a stale warning back: a reference names a repository outside the engine's remit",
				"delivery", delivery, "reference", ref.repo+"#"+strconv.Itoa(ref.number))
			return nil, false, nil
		}
	}
	owner, name := splitRepo(prRepo)
	rows, err = e.api.IssueBlocks(owner, name, pr.Number)
	if isNotFound(err) {
		// The delivery's own listing, so this 404 can name no other object:
		// the delivery was deleted since the read that established it, and
		// the wait its edges stood on went with it — held back, not answered
		// (§forgejo/webhook/re-fetch, s-git/maitred#268).
		e.log.Info("forest warnings: holding a stale warning back: the delivery's own listing answers not found",
			"delivery", delivery, "error", err)
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("blocking edges of %s#%d: %w", prRepo, pr.Number, err)
	}
	if rows == nil {
		// An empty listing is a fact the screen established, not an
		// absence: handing nil would tell the assembly no listing had been
		// read and send it to read one
		// (§forgejo/observability/the-warnings-a-forest-carries).
		rows = []Issue{}
	}
	for i := range rows {
		if rows[i].IsPull {
			continue // a pull request is a delivery; the walk reads it nowhere
		}
		repo := rowRepository(&rows[i], prRepo)
		key := connectedKey(repo, rows[i].Number)
		if seen[key] {
			continue // an issue reachable both ways is one object, one read
		}
		seen[key] = true
		if len(seen) > maxAutoCloseRefs {
			e.log.Debug("forest warnings: holding a stale warning back: the graph reaches more objects than the read budget pays",
				"delivery", delivery, "objects", len(seen))
			return nil, false, nil
		}
		admits, err := e.remitAdmits(repo, prRepo)
		if err != nil {
			return nil, false, fmt.Errorf("scope of %s: %w", repo, err)
		}
		if !admits {
			e.log.Debug("forest warnings: holding a stale warning back: a blocking edge names a repository outside the engine's remit",
				"delivery", delivery, "reference", repo+"#"+strconv.Itoa(rows[i].Number))
			return nil, false, nil
		}
	}
	return rows, true, nil
}

// humanTaskWarning states that an open member claimed by a human is what
// the forest now waits on: every work-order blocker is closed, so
// nothing but the person stands in the way
// (§forgejo/observability/the-outcomes-view). The wait is dated from the
// last blocker to close, since that is the moment the ticket became the
// person's; a member with no work-order blocker at all is dated from its
// own last activity, the newest moment the read carries.
func humanTaskWarning(member *Issue, repo string, work []Issue, now time.Time) *HeldBackItem {
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
	return &HeldBackItem{
		ForestWarning: ForestWarning{
			Kind:       WarningHumanTaskBlocking,
			Repository: repo,
			Number:     member.Number,
			Title:      member.Title,
			HTMLURL:    member.HTMLURL,
			AgeSeconds: now.Sub(waited).Seconds(),
		},
		Labels: labelsOrEmpty(member.Labels),
	}
}

// labelsOrEmpty spells a label list as the payload discipline demands:
// an empty list rather than null (§forgejo/observability/the-held-back-view).
func labelsOrEmpty(labels []string) []string {
	if labels == nil {
		return []string{}
	}
	return labels
}

// workOrders is a member's blockers as the warnings read them. A
// pull-request row is a delivery and never a work order, the way the merge
// gate excludes the delivering pull request itself from the blockers it
// counts (§forgejo/decisions/merge-gate, §forgejo/webhook/re-fetch):
// counting it would silence the human-task warning for the one ticket whose
// delivery is up for review — the exact state the ⚠️ exists to publish.
// Where the gate counts *another* pull request as an open blocker of a
// connected issue, this read names no work order at all: the question here
// is whether a person can start, and a delivery on the table does not
// answer it.
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

// waitKey names one wait in the sets the warning walk and the held-back
// view dedup through: a kind beside a reference, so one pull request
// delivering two members — or two forests naming one object — is stated
// once (§forgejo/observability/the-warnings-a-forest-carries,
// §forgejo/observability/the-held-back-view). Both sites call this, so
// the rule that a wait is stated once is written down once.
func waitKey(kind, repo string, number int) string {
	return kind + " " + warningKey(repo, number)
}
