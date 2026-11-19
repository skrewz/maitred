package forgejo

import (
	"errors"
	"fmt"
	"time"
)

// The outcomes view: the current forests of the organisation, enumerated
// from the outcomes repositories for the dashboard
// (§forgejo/observability/the-outcomes-view).

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

// Forest is one current forest: the outcomes repository it grew in, the
// open ideation root, and the outcome tracker the root blocks — or the
// honest absence of one, spelled as a nil tracker so the field is always
// present and a forest without its gate stays visible rather than being
// dropped (§forgejo/observability/the-outcomes-view).
type Forest struct {
	// Repository is the full name of the outcomes repository the root
	// was found in — the root's own row resolved once (forestIssue),
	// so the two can never disagree. The root's row keeps its own
	// repository because ForestIssue is the tracker's shape too, where
	// the repository genuinely varies
	// (§forgejo/observability/the-outcomes-view).
	Repository string `json:"repository"`
	// Root is the open ideation issue the forest grew from.
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
	// honest empty the forest list itself is.
	Warnings []ForestWarning `json:"warnings"`
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
// A remembered picture travels only while its repository is still admitted.
// The pair is the last drawing the engine made, so a role withdrawn since then
// must not keep feeding the unauthenticated view for the length of the walk:
// the carry pays the same admission test the drawing pays (writeTrackerBlock,
// §forgejo/webhook/repository-roles). A scope the carry cannot read empties
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
	drawn := make(map[Key]picture, len(previous))
	for _, forest := range previous {
		if forest.Chart == "" && forest.ChartRevisited == "" {
			continue // a forest holding no pair has nothing to carry
		}
		drawn[keyOf(forest)] = picture{forest.Chart, forest.ChartRevisited}
	}
	for i, forest := range next {
		old, ok := drawn[keyOf(forest)]
		if !ok || forest.Tracker == nil {
			continue
		}
		admits, err := e.remitAdmits(forest.Tracker.Repository, forest.Root.Repository)
		if err != nil {
			// An unknown scope is a read that broke, not the remit's
			// ordinary silence, so it is named as one — the level the scope
			// test itself reports a broken listing at — rather than being
			// recorded as a refusal the remit never made
			// (§forgejo/webhook/the-scope-cache, §forgejo/webhook/repository-roles).
			e.log.Warn("outcomes: cannot tell whether the picture's repository is admitted; nothing carried",
				"forest", forestRef(forest.Repository, forest.Root.Number),
				"tracker", forestRef(forest.Tracker.Repository, forest.Tracker.Number),
				"error", err)
			continue
		}
		if !admits {
			e.log.Debug("outcomes: nothing carried; the picture's repository is not admitted",
				"forest", forestRef(forest.Repository, forest.Root.Number),
				"tracker", forestRef(forest.Tracker.Repository, forest.Tracker.Number))
			continue
		}
		next[i].Chart, next[i].ChartRevisited = old.chart, old.stamp
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
// Forests' snapshot contract rests on.
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

// enumerateForests walks the current forests
// (§forgejo/observability/the-outcomes-view): it walks the
// repositories the scope cache classifies as outcomes repositories
// (§forgejo/webhook/repository-roles), lists each one's open issues,
// and pairs every ideation issue with the first issue it blocks that
// reads as a tracker — isTracker's one reading, the one the cascade and
// the teardown apply (Client.IssueBlocks, the same read the cascade
// follows, §forgejo/client/operations, §forgejo/decisions/unblock-cascade).
// Each tracker found brings its rows; the picture the payload carries is not
// read from the tracker's description — the sweep tick draws it once from
// the graph and records it onto the set (recordForestCharts,
// §forgejo/reconciliation/the-tracker-block). A root whose tracker is
// missing is listed with the absence stated; an outcome issue no open root
// blocks is not a forest and is not listed.
// Pull requests carry no forest. Each forest additionally carries its
// warnings, walked from the same rows the pairing reads: one blockers
// read per member either kind concerns, one membership read per
// landing-set member, and the pull-request listing, read and review
// reads the delivery needs — every one of them in a repository the scope
// test admitted first, so a blocking-edge row cannot send the engine's
// token where no role names it (§forgejo/webhook/repository-roles,
// §forgejo/observability/the-warnings-a-forest-carries). A failed read
// fails the enumeration. The list is
// empty — never nil — when no repository holds the role. A non-nil error
// means a read failed — the organisation listing, an issue listing, a
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
		issues, err := e.api.ListOpenIssues(owner, name)
		if err != nil {
			return nil, fmt.Errorf("forgejo: outcomes: list open issues of %s: %w", entered, err)
		}
		for j := range issues {
			issue := &issues[j]
			if issue.IsPull || !hasLabel(issue, LabelIdeation) {
				continue
			}
			root := forestIssue(*issue, entered)
			forest := Forest{Repository: root.Repository, Root: root}
			blocked, err := e.api.IssueBlocks(owner, name, issue.Number)
			if err != nil {
				return nil, fmt.Errorf("forgejo: outcomes: issues %s#%d blocks: %w", root.Repository, issue.Number, err)
			}
			for k := range blocked {
				b := &blocked[k]
				if b.IsPull || !isTracker(b) {
					continue
				}
				tracker := forestIssue(*b, root.Repository)
				forest.Tracker = &tracker
				// The pairing reads no picture from the row: the tick draws
				// it once, after the walk, and records it onto this set
				// (recordForestCharts, §forgejo/reconciliation/the-tracker-block,
				// §forgejo/reconciliation/the-sweep).
				break
			}
			// The same rows say whether the forest is waiting on a
			// person; the walk is the digest's one hop out of the root
			// (§forgejo/decisions/commissioning,
			// §forgejo/observability/the-outcomes-view).
			warnings, err := e.forestWarnings(issue, root.Repository, blocked)
			if err != nil {
				return nil, fmt.Errorf("forgejo: outcomes: warnings of forest %s#%d: %w", root.Repository, issue.Number, err)
			}
			forest.Warnings = warnings
			forests = append(forests, forest)
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

// Forests returns the outcomes view as the last sweep tick recorded it:
// the dashboard's picture of the current forests, served from memory
// the way Tracked serves the decisions the engine holds
// (§forgejo/observability/the-outcomes-view,
// §forgejo/observability/the-dashboard-view) — an API request costs no
// forge read. The returned slice is the snapshot itself:
// recordForests replaces it whole and never mutates a published
// snapshot in place, so it stays safe to read after the lock is
// released, and the caller must not modify it. A non-nil error means
// the view cannot be vouched for — the most recent sweep's enumeration
// failed (that error, so the caller can log which read broke), no
// enumeration has landed yet (errForestsUnavailable), or the recorded
// set has aged past one reconcile interval without being refreshed
// (errForestsStale). Either way the result is nil: like the scope
// cache, the view never acts on a set it can no longer stand behind
// (§forgejo/webhook/the-scope-cache). The bound is applied as the set
// is read rather than left to every sweep path to mark it, so a sweep
// that returns before the step that records the set — a forge too
// unreachable to list the organisation — leaves the view saying it is
// stale rather than serving the pre-outage set as if fresh.
func (e *Engine) Forests() ([]Forest, error) {
	e.forestsMu.Lock()
	defer e.forestsMu.Unlock()
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
// forest it enumerated to that forest's tracker, and a forest the
// enumeration did not name is never written to
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
