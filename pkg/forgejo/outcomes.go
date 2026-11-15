package forgejo

import (
	"errors"
	"fmt"
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
	// was found in, spelled as the forge's listing spells it.
	Repository string `json:"repository"`
	// Root is the open ideation issue the forest grew from.
	Root ForestIssue `json:"root"`
	// Tracker is the outcome issue the root blocks, or nil when the
	// root blocks no outcome issue — the absence stated, not papered
	// over (§forgejo/observability/the-outcomes-view).
	Tracker *ForestIssue `json:"tracker"`
}

// forestIssue slims an issue to its row in the view. The row names the
// repository the issue's own read carries — the forge's listing
// spelling — falling back to the repository the walk entered, whose
// scope-cache key is lowercased; the fallback keeps rows honest when a
// listing omits the field (§forgejo/client/operations).
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
// A root whose tracker is missing is listed with the absence stated; an
// outcome issue no open root blocks is not a forest and is not listed.
// Pull requests carry no forest. The list is empty — never nil — when
// no repository holds the role. A non-nil error means a read failed —
// the organisation listing, an issue listing, or a blocks read — each
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
			full := issue.Repository
			if full == "" {
				full = entered
			}
			forest := Forest{Repository: full, Root: forestIssue(*issue, full)}
			blocked, err := e.api.IssueBlocks(owner, name, issue.Number)
			if err != nil {
				return nil, fmt.Errorf("forgejo: outcomes: issues %s#%d blocks: %w", full, issue.Number, err)
			}
			for k := range blocked {
				b := &blocked[k]
				if b.IsPull || !isTracker(b) {
					continue
				}
				tracker := forestIssue(*b, full)
				forest.Tracker = &tracker
				break
			}
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

// Forests returns the outcomes view as the last sweep tick recorded it:
// the dashboard's picture of the current forests, served from memory
// the way Tracked serves the decisions the engine holds
// (§forgejo/observability/the-outcomes-view, the-dashboard-view) — an
// API request costs no forge read. The returned slice is the snapshot
// itself: recordForests replaces it whole and never mutates a published
// snapshot in place, so it stays safe to read after the lock is
// released, and the caller must not modify it. A non-nil error means
// the view cannot be vouched for — the most recent sweep's enumeration
// failed (that error, so the caller can log which read broke), or no
// enumeration has landed yet (errForestsUnavailable). Either way the
// result is nil: like the scope cache, the view never acts on a set it
// can no longer stand behind
// (§forgejo/webhook/the-scope-cache).
func (e *Engine) Forests() ([]Forest, error) {
	e.forestsMu.Lock()
	defer e.forestsMu.Unlock()
	if e.forestsErr != nil {
		return nil, e.forestsErr
	}
	if e.forestsSnap == nil {
		return nil, errForestsUnavailable
	}
	return e.forestsSnap, nil
}

// recordForests enumerates the forests and records the set for the
// outcomes view; Reconcile calls it on every tick and manual start, so
// the view's cost is per interval rather than per request
// (§forgejo/observability/the-outcomes-view). A successful enumeration
// replaces the snapshot whole and clears any earlier failure; a failed
// one keeps the last set but records the error, so the view answers
// that error — not a stale set served as if fresh — until a later sweep
// enumerates again. The scope cache is refreshed by the sweep itself
// just before, so the walk rides the listing the sweep already made
// fresh and costs no organisation listing of its own
// (§forgejo/webhook/the-scope-cache).
func (e *Engine) recordForests() {
	forests, err := e.enumerateForests()
	if err != nil {
		e.log.Error("reconciliation: forest enumeration failed; the outcomes view will say so until a sweep succeeds",
			"error", err)
	}
	e.forestsMu.Lock()
	if err != nil {
		e.forestsErr = err
	} else {
		e.forestsSnap, e.forestsErr = forests, nil
	}
	e.forestsMu.Unlock()
}
