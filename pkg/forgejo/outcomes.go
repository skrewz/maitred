package forgejo

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// The outcomes view: the forests of the organisation — the current ones
// and the past ones — enumerated from the outcomes repositories for the
// dashboard (§forgejo/observability/the-outcomes-view).

// forestIsCurrent states the view's split: a forest is current when its
// ideation root or its outcome tracker exists and is open; a forest whose
// tickets are both closed is past — a forest that finished while nobody
// was watching is history on the page, not a disappearance
// (§forgejo/observability/the-outcomes-view). The reading is a pure
// function of the rows the walk already holds, which is what lets one
// recorded set serve both lists without a read of its own.
func forestIsCurrent(f Forest) bool {
	if f.Root.State == "open" {
		return true
	}
	return f.Tracker != nil && f.Tracker.State == "open"
}

// ForestIssue is one issue of a forest, slimmed to the fields the view
// shows: number, title, state, the repository the issue lives in, and
// HTML URL. The repository is stated per row because a tracker may
// live outside the repository the forest grew in — cross-repository
// blockers are a standing case of the blocks read
// (§forgejo/client/operations) — so a cross-repo gate says where it
// lives instead of inheriting the root's address
// (§forgejo/observability/the-outcomes-view).
type ForestIssue struct {
	Number     int    `json:"number"`
	Repository string `json:"repository"`
	Title      string `json:"title"`
	State      string `json:"state"`
	HTMLURL    string `json:"html_url"`
}

// Forest is one forest of the view: the outcomes repository it grew in,
// the ideation root it grew from, and the outcome tracker the root blocks
// — or the honest absence of one, spelled as a nil tracker so the field
// is always present and a forest without its gate stays visible rather
// than being dropped (§forgejo/observability/the-outcomes-view). Whether
// the forest is current or past is the rows' own state saying, not a
// field of its own (forestIsCurrent).
type Forest struct {
	// Repository is the full name of the outcomes repository the root
	// was found in — the root's own row resolved once (forestIssue),
	// so the two can never disagree. The root's row keeps its own
	// repository because ForestIssue is the tracker's shape too, where
	// the repository genuinely varies
	// (§forgejo/observability/the-outcomes-view).
	Repository string `json:"repository"`
	// Root is the ideation issue the forest grew from, open or closed —
	// the state is the row's own saying, which is what separates a
	// current forest from a past one.
	Root ForestIssue `json:"root"`
	// Tracker is the outcome issue the root blocks, or nil when the
	// root blocks no outcome issue — the absence stated, not papered
	// over (§forgejo/observability/the-outcomes-view).
	Tracker *ForestIssue `json:"tracker"`
	// Chart is the Mermaid source the engine drew from the forest's graph
	// when the last sweep tick recorded this set — the same drawing that
	// splices into the tracker's current-state block, carried verbatim
	// and never composed, repaired or guessed here
	// (§forgejo/reconciliation/the-tracker-block,
	// §forgejo/observability/the-outcomes-view). It is empty wherever the
	// tick drew nothing: no tracker, a graph the renderer refused, a
	// repository no role admits — and never a remembered picture dressed
	// in a newer stamp. While a tick's walk is still running, the pair a
	// forest carries is the previous tick's, stamp and all — and never a
	// pair from a repository the remit has since withdrawn
	// (carryDrawnPictures).
	Chart string `json:"chart"`
	// ChartRevisited is the moment that drawing was made, spelled as the
	// current-state block's stamp spells it — the same instant, in the
	// configured zone — so a reader states the picture's recency without
	// parsing prose. It is the drawing's stamp rather than anything a
	// description claims, and it travels with Chart: where Chart is empty
	// the stamp is empty too
	// (§forgejo/config/tracker-chart,
	// §forgejo/observability/the-outcomes-view).
	ChartRevisited string `json:"chart_revisited"`
	// Warnings are the forest's ⚠️ marks: the reasons the forest is
	// waiting on a person, each stated as a reference
	// (§forgejo/observability/the-outcomes-view). A forest nothing is
	// waiting on carries an empty list rather than a null, the same
	// honest empty the forest list itself is — and WarningsWalked
	// states whether this list is a walked empty at all.
	Warnings []ForestWarning `json:"warnings"`
	// WarningsWalked is that statement: the warning walk runs for the
	// current forests alone, so a past row's empty list says "not
	// walked", never "nobody waits" — the honest empty belongs to a
	// walk that found nothing, and a finished forest's empty was never
	// read (§forgejo/observability/the-warnings-a-forest-carries).
	WarningsWalked bool `json:"warnings_walked"`
	// heldBack is this forest's waits in the shape the held-back view
	// serves them — the warning fields alongside the walked row's
	// labels and description (§forgejo/observability/the-held-back-view).
	// It is unexported because it is not part of the outcomes payload:
	// the two views are two shapes over the one walk's record, and the
	// second shape rides to the outcomes endpoint's readers for no
	// reason the brief pays for.
	heldBack []HeldBackItem
}

// The view draws rather than scrapes: the picture fields are filled by the
// sweep tick's one drawing, recorded onto the set this file's reader serves
// (recordForestCharts, §forgejo/reconciliation/the-tracker-block,
// §forgejo/observability/the-outcomes-view). The tracker's description is
// out of the picture's chain of command — the fence pair the write half
// splices into is declared once, in tracker_chart.go.

// picture is one forest's picture pair: the drawing the engine made and the
// moment it made it. The two travel together or not at all, which is what
// keeps a remembered chart from ever arriving under a newer stamp.
type picture struct {
	chart string
	stamp string
}

// carried is a pair as the carry reads it: the picture and the repository that
// drawing came from, read off the previous set's tracker row — safe because the
// set the carry reads is the one step 5 of the same sweep folded onto, so a
// non-empty pair beside that row was drawn against it moments earlier. The
// repository travels with it because the pair was drawn from the tracker the
// previous tick paired the root with, which a re-pairing can leave behind — and
// a picture the view may publish is evidence about the repository it was drawn
// from, not only the one it now travels to
// (§forgejo/webhook/repository-roles, §forgejo/reconciliation/the-sweep).
type carried struct {
	picture
	from string
}

// keyOf names the forest a picture pair belongs to across two recorded sets.
// It is the store's own forest key (ForestKey, §forgejo/state/forest-digest):
// a forest is the ideation root it grew from, so a tracker replaced by another
// outcome issue between ticks leaves the forest — and the pair it carries —
// the same. Reusing the store's spelling is what keeps "which forest is the
// same forest" written down once.
func keyOf(forest Forest) Key {
	return ForestKey(forest.Repository, forest.Root.Number)
}

// carryDrawnPictures moves each forest's picture pair from the set the tick is
// replacing onto its successor, so the view keeps a picture between the moment
// the tick records the set at its head and the moment its step-5 drawing
// replaces every pair in it (recordForestCharts,
// §forgejo/reconciliation/the-tracker-block). Without it the set the tick
// publishes would answer every forest's picture empty until step 5 ran, and
// the page would lose its charts for as long as the tick's walk takes — one
// re-fetch per open row in every repository holding a role
// (§forgejo/reconciliation/the-sweep).
//
// The pair only travels as one, so a remembered chart never meets a newer
// stamp; a forest the new set holds and the old one did not — one this tick
// found for the first time — carries both fields empty, the honest empty
// §forgejo/observability/the-outcomes-view states. And a carried pair is never
// the view's last word: the step-5 fold overwrites every pair in the set it
// lands on, so a forest this tick drew nothing for still ends the tick with
// both fields empty.
//
// A remembered picture travels only while both the repository it was drawn
// from and the one its tracker now stands in are still admitted. The pair is
// the last drawing the engine made, so a role withdrawn since then must not
// keep feeding the unauthenticated view for the length of the walk: the carry
// pays the same admission test the drawing pays (writeTrackerBlock,
// §forgejo/webhook/repository-roles), and pays it of the drawing's own
// repository as well — the forest's tracker is forge data, so a re-pairing can
// move a picture to a repository whose remit says nothing about the bytes it
// carries. A scope the carry cannot read empties
// the pair too — a remembered drawing whose remit is unknown is no evidence
// the view may publish — and a forest this tick names no tracker for carries
// nothing at all, the absence the view states
// (§forgejo/observability/the-outcomes-view, §forgejo/webhook/the-scope-cache).
func (e *Engine) carryDrawnPictures(next []Forest) {
	e.forestsMu.Lock()
	previous := e.forestsSnap
	e.forestsMu.Unlock()
	// The remit is asked outside forestsMu, because a scope the cache cannot
	// answer is refreshed there and the request path must never wait on a forge
	// read (Forests, §forgejo/observability/the-outcomes-view). Reading the set
	// the previous tick published is safe for as long as a recorded set is
	// replaced whole rather than mutated in place.
	carry := make(map[Key]carried, len(previous))
	for _, forest := range previous {
		if forest.Chart == "" && forest.ChartRevisited == "" {
			continue // a forest holding no pair has nothing to carry
		}
		from := ""
		if forest.Tracker != nil {
			from = forest.Tracker.Repository
		}
		carry[keyOf(forest)] = carried{
			picture: picture{forest.Chart, forest.ChartRevisited},
			from:    from,
		}
	}
	for i, forest := range next {
		pair, ok := carry[keyOf(forest)]
		if !ok || forest.Tracker == nil {
			continue
		}
		// Both ends of the pair pay the test and both answers count: the
		// repository the tracker stands in now, and the one the drawing came
		// from, because a re-pairing can move a picture between repositories
		// and either end can have withdrawn since the drawing
		// (§forgejo/webhook/repository-roles). The second question is asked
		// only while the first still admits, and is skipped when the two
		// repositories are one because the line above then already asked the
		// identical question — remitAdmits' trivial admission for the walk's
		// own repository is not what makes the equal case safe.
		admits, err := e.remitAdmits(forest.Tracker.Repository, forest.Root.Repository)
		if err == nil && admits && !strings.EqualFold(pair.from, forest.Tracker.Repository) {
			admits, err = e.remitAdmits(pair.from, forest.Root.Repository)
		}
		if err != nil {
			// An unknown scope is a read that broke, not the remit's
			// ordinary silence, so it is named as one — the level the scope
			// test itself reports a broken listing at — rather than being
			// recorded as a refusal the remit never made
			// (§forgejo/webhook/the-scope-cache, §forgejo/webhook/repository-roles).
			e.log.Warn("outcomes: cannot tell whether the picture's repository is admitted; nothing carried",
				"forest", forestRef(forest.Repository, forest.Root.Number),
				"tracker", forestRef(forest.Tracker.Repository, forest.Tracker.Number),
				"drawn", pair.from,
				"error", err)
			continue
		}
		if !admits {
			e.log.Debug("outcomes: nothing carried; the picture's repository is not admitted",
				"forest", forestRef(forest.Repository, forest.Root.Number),
				"tracker", forestRef(forest.Tracker.Repository, forest.Tracker.Number),
				"drawn", pair.from)
			continue
		}
		next[i].Chart, next[i].ChartRevisited = pair.chart, pair.stamp
	}
}

// recordForestCharts folds the tick's drawings onto the recorded forest set
// so the outcomes payload carries the picture the same tick drew for the
// trackers' blocks — one drawing, two sinks
// (§forgejo/reconciliation/the-tracker-block,
// §forgejo/observability/the-outcomes-view). The sources are the drawings
// writeTrackerBlocks made walking the set in order; an empty source is
// a forest the tick drew nothing for, and both of its picture fields go
// empty rather than keeping the pair the set arrived with under this tick's
// stamp. The set is replaced whole, never mutated in place — the discipline
// Forests' snapshot contract rests on. sources is indexed by position: the
// one caller, writeTrackerBlocks, builds one source per forest of the set it
// hands over, so the two slices are equal-length by construction.
//
// The set folded onto is the one step 2 of this same tick published: Reconcile
// admits one sweep at a time, so no newer enumeration exists for this replace
// to roll back (§forgejo/reconciliation/the-sweep).
func (e *Engine) recordForestCharts(forests []Forest, sources []string, at time.Time) {
	stamp := stampAt(at, e.cfg.ChartLocation())
	e.forestsMu.Lock()
	defer e.forestsMu.Unlock()
	updated := make([]Forest, len(forests))
	for i, forest := range forests {
		// A past forest's pair is frozen: step 5 draws the current
		// forests alone, so the fold leaves the carried pair — the last
		// drawing made while the forest lived, stamp and all — standing
		// as the row's picture (§forgejo/observability/the-outcomes-view).
		if !forestIsCurrent(forest) {
			updated[i] = forest
			continue
		}
		// The pair is written together or erased together: this is where a
		// pair carried onto the set (carryDrawnPictures) stops being the
		// view's answer, whether or not this tick had a drawing for it.
		if sources[i] == "" {
			forest.Chart, forest.ChartRevisited = "", ""
		} else {
			forest.Chart, forest.ChartRevisited = sources[i], stamp
		}
		updated[i] = forest
	}
	e.forestsSnap = updated
}

// trackerBlockedBy is the forest's tracker: the first row of a root's blocks
// listing the engine reads as a tracker — the pairing the view, the cascade and
// the teardown all apply, stated once so no reader of the graph keeps a second
// copy of it (§forgejo/observability/the-outcomes-view,
// §forgejo/decisions/unblock-cascade). A blocks listing names no repository for
// a row that lives where the listing was made, so rootRepo is the fallback
// (§forgejo/client/operations).
func trackerBlockedBy(blocked []Issue, rootRepo string) *ForestIssue {
	for i := range blocked {
		row := &blocked[i]
		if row.IsPull || !isTracker(row) {
			continue // a delivery carries no forest, and a root's row is another forest's root
		}
		tracker := forestIssue(*row, rootRepo)
		return &tracker
	}
	return nil
}

// recordForestPicture lands one forest's drawing on the recorded set, so an
// event-path redraw reaches the outcomes payload as well as the tracker's block
// and the two sinks of one forest never state two drawings
// (§forgejo/reconciliation/the-tracker-block, s-git/maitred#274). It is
// recordForestCharts' single-forest twin and shares its discipline: the pair is
// written together, the set is replaced whole rather than mutated in place, and
// the forest is addressed by the store's own forest key — the root it grew from
// (keyOf, §forgejo/state/forest-digest).
//
// A forest the recorded set does not hold is left out, not invented: the set is
// what a sweep's enumeration vouches for, and a row no enumeration named is a row
// the view would publish without knowing what else stands beside it
// (§forgejo/observability/the-outcomes-view). The block such an event draws still
// lands, and the payload carries the forest from the next tick's head.
func (e *Engine) recordForestPicture(forest Forest, source string, at time.Time) {
	stamp := stampAt(at, e.cfg.ChartLocation())
	e.forestsMu.Lock()
	defer e.forestsMu.Unlock()
	for i, row := range e.forestsSnap {
		if keyOf(row) != keyOf(forest) {
			continue
		}
		updated := make([]Forest, len(e.forestsSnap))
		copy(updated, e.forestsSnap)
		updated[i].Chart, updated[i].ChartRevisited = source, stamp
		e.forestsSnap = updated
		return
	}
	e.log.Debug("outcomes: the recorded forest set holds no row to record this drawing onto; the tick's head records it",
		"forest", forestRef(forest.Repository, forest.Root.Number))
}

// forestIssue slims an issue to its row in the view. The row names the
// repository the issue's own read carries — the forge's listing
// spelling — and falls back to the address the caller supplies when a
// listing omits the field: the repository the walk entered for a root
// row, and where the root lives for a row read from the root's blocks
// — the same rule the digest applies to that row
// (§forgejo/client/operations, §forgejo/decisions/actionable-state-digest).
func forestIssue(i Issue, fallback string) ForestIssue {
	repo := i.Repository
	if repo == "" {
		repo = fallback
	}
	return ForestIssue{
		Number:     i.Number,
		Repository: repo,
		Title:      i.Title,
		State:      i.State,
		HTMLURL:    i.HTMLURL,
	}
}

// enumerateForests walks the forests — current and past
// (§forgejo/observability/the-outcomes-view): it walks the
// repositories the scope cache classifies as outcomes repositories
// (§forgejo/webhook/repository-roles), lists each one's issues once,
// open and closed alike, and pairs every ideation issue in either state
// with the first issue it blocks that
// reads as a tracker — isTracker's one reading, the one the cascade and
// the teardown apply (Client.IssueBlocks, the same read the cascade
// follows, §forgejo/client/operations, §forgejo/decisions/unblock-cascade).
// Each tracker found brings its rows; the picture the payload carries is not
// read from the tracker's description — the sweep tick draws it once from
// the graph and records it onto the set (recordForestCharts,
// §forgejo/reconciliation/the-tracker-block). A root whose tracker is
// missing is listed with the absence stated; an outcome issue no
// ideation issue blocks is not a forest and is not listed. The listing
// is stateless — one listing per repository, so a root that changes
// state mid-walk cannot be enumerated on both sides of the split — and
// the walk keeps each issue number once, so a row the forge's paging
// hands twice because the ordering shifted mid-listing enumerates one
// forest and counts once. The pairing costs no read beyond the one
// blocks read per forest row; what the widening does cost is the
// listing itself, one full paging per outcomes repository per tick which
// the repository's whole history grows
// (§forgejo/observability/the-outcomes-view, §forgejo/reconciliation/the-sweep).
// The rows' own states decide one thing in the walk: whether this row
// pays the warning walk. Which list a row lands in is the reader's
// reading, not the walk's (ForestSplit), and the set is recorded in the
// order the listings arrived.
// Pull requests carry no forest. Each current forest additionally carries its
// warnings, walked from the same rows the pairing reads: one blockers
// read per member either kind concerns, one membership read per
// landing-set member, and the pull-request listing, read and review
// reads the delivery needs — every one of them in a repository the scope
// test admitted first, so a blocking-edge row cannot send the engine's
// token where no role names it (§forgejo/webhook/repository-roles,
// §forgejo/observability/the-warnings-a-forest-carries). A failed read
// fails the enumeration — save the forge's definitive "no such issue", at
// the read that first asks whether a root, member or delivery is there,
// which says that object was deleted: the row or warning it stood on drops
// out, with a logged line naming it; the exception's limits are the spec's
// (§forgejo/observability/the-outcomes-view). The list is empty — never nil
// — when no repository holds the role. A non-nil error means a read failed —
// the organisation listing, an issue listing, a
// blocks read, a scope lookup, or one of the warning reads — each
// wrapped with the repository or issue it concerned, and the partial
// result is dropped: a failed read must not read as an empty set of
// forests, the same fail-closed discipline as the scope test
// (§forgejo/webhook/the-scope-cache). It is the sweep tick's reader,
// called by recordForests; the request path answers from the snapshot
// the last enumeration recorded (Forests), so serving the view costs
// no forge read (§forgejo/observability/the-outcomes-view).
func (e *Engine) enumerateForests() ([]Forest, error) {
	repos, err := e.outcomesRepositories()
	if err != nil {
		return nil, fmt.Errorf("forgejo: outcomes: scope of the outcomes repositories: %w", err)
	}
	forests := []Forest{}
	for _, entered := range repos {
		owner, name := splitRepo(entered)
		rows, err := e.api.ListAllIssues(owner, name)
		if err != nil {
			return nil, fmt.Errorf("forgejo: outcomes: list issues of %s: %w", entered, err)
		}
		// A repository numbers its issues once, so the number is the root's
		// identity within the listing: paging a listing whose ordering shifted
		// mid-walk can hand one row on two pages, and the second reading must
		// duplicate neither the forest nor its count
		// (§forgejo/observability/the-outcomes-view).
		seen := make(map[int]bool, len(rows))
		rootsRead, rootsGone := 0, 0
		for j := range rows {
			issue := &rows[j]
			if issue.IsPull || !hasLabel(issue, LabelIdeation) || seen[issue.Number] {
				continue
			}
			seen[issue.Number] = true
			root := forestIssue(*issue, entered)
			forest := Forest{Repository: root.Repository, Root: root}
			blocked, err := e.api.IssueBlocks(owner, name, issue.Number)
			rootsRead++
			if err != nil {
				if isNotFound(err) {
					rootsGone++
					// The root itself is gone; every other failure is a read
					// of a forest that stands (§forgejo/webhook/re-fetch,
					// s-git/maitred#268).
					e.log.Info("outcomes: forest root not found; the forest is no longer listed",
						"forest", forestRef(root.Repository, issue.Number),
						"error", err)
					continue
				}
				return nil, fmt.Errorf("forgejo: outcomes: issues %s#%d blocks: %w", root.Repository, issue.Number, err)
			}
			// The pairing is the one rule every reader of the graph applies
			// (trackerBlockedBy); it reads no picture from the row — the tick
			// draws it once, after the walk, and records it onto this set
			// (recordForestCharts, §forgejo/reconciliation/the-tracker-block,
			// §forgejo/reconciliation/the-sweep).
			forest.Tracker = trackerBlockedBy(blocked, root.Repository)
			// The warning walk is the digest's one hop out of the root
			// (§forgejo/decisions/commissioning,
			// §forgejo/observability/the-warnings-a-forest-carries). Its
			// held-back rows are the same record in the other shape
			// (§forgejo/observability/the-held-back-view).
			if forestIsCurrent(forest) {
				warnings, held, err := e.forestWarnings(issue, root.Repository, blocked)
				if err != nil {
					return nil, fmt.Errorf("forgejo: outcomes: warnings of forest %s#%d: %w", root.Repository, issue.Number, err)
				}
				forest.Warnings = warnings
				forest.heldBack = held
				forest.WarningsWalked = true
			} else {
				forest.Warnings = []ForestWarning{}
			}
			forests = append(forests, forest)
		}
		// The exception's limit: a wholly-404 repository with roots enough to
		// speak for a set (§forgejo/observability/the-outcomes-view).
		if rootsGone > 0 && rootsRead > 1 && rootsGone == rootsRead {
			return nil, fmt.Errorf("forgejo: outcomes: every forest root of %s answers not found, which no healthy read of a forest set answers", entered)
		}
	}
	return forests, nil
}

// errForestsUnavailable reports that the outcomes view has nothing to
// stand on: no sweep has yet enumerated the forests, so the engine
// cannot tell forests from a read that never landed. The view answers
// as unavailable rather than with an empty list, so a deployment whose
// reads have not yet succeeded never reads as "no forests"
// (§forgejo/observability/the-outcomes-view).
var errForestsUnavailable = errors.New("forests not yet enumerated")

// errForestsStale reports that the recorded forest set has aged past
// one reconcile interval without a sweep having recorded a newer one —
// typically because the sweep returned at the organisation listing it
// starts with and never reached the step that records the set
// (§forgejo/reconciliation/the-sweep). Serving the set then would hand
// out as fresh what the engine has not re-read since the forge went
// unreachable, so the view answers stale instead: the same bound the
// scope cache puts on its own snapshot
// (§forgejo/webhook/the-scope-cache,
// §forgejo/observability/the-outcomes-view).
var errForestsStale = errors.New("forest enumeration is stale")

// Forests returns the current forests as the last sweep tick recorded
// them: the dashboard's picture of the forests in flight, served from
// memory the way Tracked serves the decisions the engine holds
// (§forgejo/observability/the-outcomes-view,
// §forgejo/observability/the-dashboard-view) — an API request costs no
// forge read. It is ForestSplit's current half, and vouches for nothing
// beyond what that read vouches for; a caller serving both lists calls
// ForestSplit.
func (e *Engine) Forests() ([]Forest, error) {
	current, _, err := e.ForestSplit()
	return current, err
}

// ForestSplit answers both halves of the view — the current forests
// and the past ones — from one acquisition of the snapshot. One lock is
// the point: a response built from two reads could straddle a sweep that
// landed between them and name a forest in both tabs or in neither,
// which is the disappearance s-git/maitred#266 was filed against,
// reimported as a race; one read promises the set the spec promises —
// the one the last tick recorded (§forgejo/observability/the-outcomes-view).
// Which rows are current is the rows' own saying (forestIsCurrent), so
// the split is a pure reading that costs nothing beyond the acquisition.
// Both halves are newly built and the caller must not modify them. A
// non-nil error means the view cannot be vouched for — the most recent
// sweep's enumeration failed (that error, so the caller can log which
// read broke), no enumeration has landed yet (errForestsUnavailable), or
// the recorded set has aged past one reconcile interval without being
// refreshed (errForestsStale). Either way both halves are nil: like the
// scope cache, the view never acts on a set it can no longer stand
// behind (§forgejo/webhook/the-scope-cache). The bound is applied as the
// set is read rather than left to every sweep path to mark it, so a
// sweep that returns before the step that records the set — a forge too
// unreachable to list the organisation — leaves the view saying it is
// stale rather than serving the pre-outage set as if fresh.
func (e *Engine) ForestSplit() (current, past []Forest, err error) {
	e.forestsMu.Lock()
	defer e.forestsMu.Unlock()
	snap, err := e.forestsSnapshot()
	if err != nil {
		return nil, nil, err
	}
	current, past = []Forest{}, []Forest{}
	for _, forest := range snap {
		if forestIsCurrent(forest) {
			current = append(current, forest)
		} else {
			past = append(past, forest)
		}
	}
	return current, past, nil
}

// HeldBackSplit answers both halves of the held-back view — the waits
// on a person and the deliveries stalled past the threshold — from one
// acquisition of the same snapshot the outcomes view serves
// (§forgejo/observability/the-held-back-view,
// §forgejo/observability/the-outcomes-view): one lock, so a response
// cannot straddle two ticks, and the snapshot's own honesty rules —
// unavailable before the first enumeration, the last failure while it
// stands, stale once aged out — decide the answer. A request to the
// view it serves costs no forge read, however often the page polls.
// Current forests' rows flatten in the walk's own order, and an object
// two forests name is stated once: one object held back twice holds
// the organisation back once. Past forests contribute nothing, as they
// contribute no walked warnings. A non-nil error means both halves are
// nil, the same rule as ForestSplit.
func (e *Engine) HeldBackSplit() (heldByHumans, stalledDeliveries []HeldBackItem, err error) {
	e.forestsMu.Lock()
	defer e.forestsMu.Unlock()
	snap, err := e.forestsSnapshot()
	if err != nil {
		return nil, nil, err
	}
	heldByHumans, stalledDeliveries = []HeldBackItem{}, []HeldBackItem{}
	seen := map[string]bool{}
	for _, forest := range snap {
		if !forestIsCurrent(forest) {
			continue
		}
		for _, item := range forest.heldBack {
			key := item.Kind + " " + warningKey(item.Repository, item.Number)
			if seen[key] {
				continue // one object held back twice is one wait
			}
			seen[key] = true
			// The two warning kinds are the waiting vocabulary in full
			// (§forgejo/observability/the-warnings-a-forest-carries), so
			// anything that is not a stalled delivery is a wait on a
			// person; there is no third wait to lose a row to.
			if item.Kind == WarningStaleAgenticMerge {
				stalledDeliveries = append(stalledDeliveries, item)
			} else {
				heldByHumans = append(heldByHumans, item)
			}
		}
	}
	return heldByHumans, stalledDeliveries, nil
}

// forestsSnapshot applies the snapshot's one discipline — failure,
// absence, age — and hands back the recorded set. The caller holds
// forestsMu.
func (e *Engine) forestsSnapshot() ([]Forest, error) {
	if e.forestsErr != nil {
		return nil, e.forestsErr
	}
	// The moment of the last recording is the sentinel, not the slice:
	// an enumeration of zero forests recorded at least something, so an
	// honest empty stays distinguishable from a view that never read.
	if e.forestsAt.IsZero() {
		return nil, errForestsUnavailable
	}
	if time.Since(e.forestsAt) > e.cfg.ReconcileInterval {
		return nil, errForestsStale
	}
	return e.forestsSnap, nil
}

// recordForests enumerates the forests and records the set for the
// outcomes view; Reconcile calls it on every tick and manual start, at
// the tick's head (§forgejo/reconciliation/the-sweep), so the view's
// cost is per interval rather than per request
// (§forgejo/observability/the-outcomes-view). A successful enumeration
// replaces the snapshot whole, stamps the moment it landed and clears
// any earlier failure; a failed one keeps the last set but
// records the error, so the view answers that error — not a stale set
// served as if fresh — until a later sweep enumerates again. How long
// a recorded set stays servable is Forests' concern, not this
// function's. The scope cache is refreshed by the sweep
// itself just before, so the walk rides the listing the sweep already
// made fresh and costs no organisation listing of its own
// (§forgejo/webhook/the-scope-cache). The set and the error are reported
// to the caller as well as recorded: the sweep's next step writes each
// current forest's drawing to that forest's tracker, and a forest the
// enumeration did not name — or named as past — is never written to
// (§forgejo/reconciliation/the-tracker-block). The picture pair each forest
// carries travels across from the set this one replaces
// (carryDrawnPictures), so the view holds a picture for as long as the tick's
// walk runs before step 5 draws the next one. The carry is the tick's own
// remit test for a remembered picture, paid here rather than at step 5
// (carryDrawnPictures, §forgejo/webhook/repository-roles).
func (e *Engine) recordForests() ([]Forest, error) {
	forests, err := e.enumerateForests()
	if err != nil {
		e.log.Error("reconciliation: forest enumeration failed; the outcomes view will say so until a sweep succeeds",
			"error", err)
	} else {
		// The carry fills the new set before it is published, so a pair only
		// ever lands on bytes no reader can see yet.
		e.carryDrawnPictures(forests)
	}
	e.forestsMu.Lock()
	if err != nil {
		e.forestsErr = err
	} else {
		e.forestsSnap, e.forestsAt, e.forestsErr = forests, time.Now(), nil
	}
	e.forestsMu.Unlock()
	return forests, err
}
