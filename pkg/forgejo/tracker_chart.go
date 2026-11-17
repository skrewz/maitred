package forgejo

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"maitred/pkg/forgejo/renderer"
)

// The tracker's current-state block: the region of an outcome tracker's own
// description the engine writes on every sweep tick
// (§forgejo/reconciliation/the-tracker-block). An agent authored this region
// before the renderer landed; the renderer (#182) makes the drawing
// deterministic, and this file makes the write narrow enough that the drawing
// is the only thing that changes.
//
// The block is delimited by a pair of HTML comments so it can sit inside prose
// a human owns. Everything outside the pair — the outcome prose, a member's
// body, a grouping issue's body — is out of reach by construction: this file
// addresses no issue but a forest's tracker, and it sends no field but the body
// (§forgejo/client/the-one-mutation).
const (
	// fenceBegin opens the region the engine owns.
	fenceBegin = "<!-- maitred:forest:begin -->"
	// fenceEnd closes it.
	fenceEnd = "<!-- maitred:forest:end -->"
	// stateHeading is the heading the block stands under when a tracker's
	// description carries no fence pair yet and the block is inserted as
	// the body's first heading.
	stateHeading = "## Current state"
	// stampLayout renders the stamp's date and time half; the label is the
	// shared stampLabel the outcomes view scrapes, and the zone abbreviation
	// follows from the configured location
	// (§forgejo/config/tracker-chart).
	stampLayout = "2006-01-02 15:04 MST"
	// strokeMemberHex and strokeClosedHex colour the two classes no role
	// label names: a work item carries no kind label whose colour could be
	// read, and neither does "closed". They are the only colours the engine
	// owns, and they are the hexes the drawing was converted from — the
	// same pair the renderer's golden files pin
	// (§forgejo/reconciliation/the-tracker-block,
	// §forgejo/renderer/classes-and-strokes).
	strokeMemberHex = "#6b7a8c"
	strokeClosedHex = "#3fb950"
)

// errFenceAmbiguous reports a description whose region belongs to the engine is
// not guessable: more than one pair, a pair with no closer, or a closer with no
// opener. Which region is whose is a question no rule answers, so nothing is
// written to that tracker and the sweep says so — a chart landed in the wrong
// region is worse than a stale one (§forgejo/reconciliation/the-tracker-block).
var errFenceAmbiguous = errors.New("the tracker's fence pair is ambiguous")

// writeTrackerBlocks lands the current-state block on the tracker of every
// forest the tick enumerated (§forgejo/reconciliation/the-sweep,
// §forgejo/reconciliation/the-tracker-block). Each forest stands or falls on its
// own: a read that fails, a graph the renderer refuses and a write the forge
// refuses are logged naming that forest and skipped, so one ragged forest costs
// the others nothing and the tick carries on to the walk. at is the moment the
// stamp carries; it is a parameter rather than a clock read here so a test can
// watch the stamp move while the graph stands still.
func (e *Engine) writeTrackerBlocks(forests []Forest, at time.Time) {
	for _, forest := range forests {
		// A forest whose tracker is missing states its absence through the
		// outcomes view; there is nothing here to write it to and nothing
		// worth a log line (§forgejo/observability/the-outcomes-view).
		if forest.Tracker == nil {
			continue
		}
		if err := e.writeTrackerBlock(forest, at); err != nil {
			e.log.Error("reconciliation: tracker current-state block not written",
				"tracker", trackerRef(forest), "error", err)
		}
	}
}

// trackerRef names the tracker a forest's block is written to — the address a
// diagnosis starts from (§forgejo/observability/the-outcomes-view).
func trackerRef(forest Forest) string {
	return forestRef(forest.Tracker.Repository, forest.Tracker.Number)
}

// writeTrackerBlock renders one forest and lands its block on that forest's
// tracker, which the caller has established the forest has. Where the body the
// composition produced is the body already there — two ticks inside one minute
// over an unchanged graph — nothing is sent, so a tick never bumps a tracker for
// nothing (§forgejo/reconciliation/the-tracker-block).
func (e *Engine) writeTrackerBlock(forest Forest, at time.Time) error {
	owner, name := splitRepo(forest.Tracker.Repository)
	ref := trackerRef(forest)
	tracker, err := e.api.GetIssue(owner, name, forest.Tracker.Number)
	if err != nil {
		return fmt.Errorf("tracker read %s: %w", ref, err)
	}
	if tracker == nil {
		return fmt.Errorf("tracker read %s: no issue returned", ref)
	}
	source, err := e.renderForestChart(forest)
	if err != nil {
		return fmt.Errorf("rendering %s: %w", ref, err)
	}
	body, err := spliceStateBlock(tracker.Body, stateBlock(source, at, e.cfg.ChartLocation()))
	if err != nil {
		return fmt.Errorf("%s: %w", ref, err)
	}
	if body == tracker.Body {
		return nil
	}
	if err := e.api.UpdateIssueBody(owner, name, tracker.Number, body); err != nil {
		return fmt.Errorf("writing %s: %w", ref, err)
	}
	return nil
}

// stateBlock is the bytes between one tracker's fence pair: the stamp and the
// rendered source in its Mermaid fence. The renderer's source already ends in
// one newline, so the closing fence follows it directly. The stamp carries
// whether or not the graph moved, which is what makes a stale chart
// distinguishable from an untouched forest.
func stateBlock(source string, at time.Time, loc *time.Location) string {
	return stampLabel + " " + at.In(loc).Format(stampLayout) + "\n\n" +
		"```mermaid\n" + source + "```\n"
}

// spliceStateBlock lands block between the fence pair of body and returns the
// body that results. One pair replaces the bytes between it; no pair inserts
// the block as the body's first heading, leaving the prose found in the order
// it was in; anything else is errFenceAmbiguous and the body is returned
// unchanged (§forgejo/reconciliation/the-tracker-block).
func spliceStateBlock(body, block string) (string, error) {
	openers := strings.Count(body, fenceBegin)
	closers := strings.Count(body, fenceEnd)
	switch {
	case openers == 0 && closers == 0:
		head := stateHeading + "\n\n" + fenceBegin + "\n\n" + block + "\n" + fenceEnd + "\n"
		if strings.TrimSpace(body) == "" {
			return head, nil
		}
		return head + "\n" + body, nil
	case openers != 1 || closers != 1:
		return body, fmt.Errorf("%w: %d openers and %d closers", errFenceAmbiguous, openers, closers)
	}
	start := strings.Index(body, fenceBegin) + len(fenceBegin)
	end := strings.Index(body, fenceEnd)
	if end < start {
		return body, fmt.Errorf("%w: the closer stands before the opener", errFenceAmbiguous)
	}
	return body[:start] + "\n\n" + block + "\n" + body[end:], nil
}

// renderForestChart gathers one forest's graph and renders it
// (§forgejo/renderer/the-contract). The reads are the ones the forest's own
// relations answer — the root's row, the rows the root blocks, and each row's
// blocks for the edges between them — so the drawing follows the graph rather
// than anything remembered between ticks. The strokes come from the labels
// themselves: the root's `ideation` colour and the tracker's `outcome` colour,
// read fresh from the rows these reads already carry
// (§forgejo/renderer/classes-and-strokes,
// §forgejo/reconciliation/the-tracker-block).
func (e *Engine) renderForestChart(forest Forest) (string, error) {
	rootOwner, rootName := splitRepo(forest.Root.Repository)
	root, err := e.api.GetIssue(rootOwner, rootName, forest.Root.Number)
	if err != nil {
		return "", fmt.Errorf("root read %s: %w", forestRef(forest.Root.Repository, forest.Root.Number), err)
	}
	if root == nil {
		return "", fmt.Errorf("root read %s: no issue returned", forestRef(forest.Root.Repository, forest.Root.Number))
	}
	rootIssue := *root
	// A single-object read may report no repository of its own; the row
	// then lives where the enumeration found it
	// (§forgejo/client/operations).
	if rootIssue.Repository == "" {
		rootIssue.Repository = forest.Root.Repository
	}

	rows, edges, err := e.forestGraph(&rootIssue)
	if err != nil {
		return "", err
	}
	tracker := trackerRow(rows)
	if tracker == nil {
		return "", fmt.Errorf("the root %s reaches no tracker row", forestRef(rootIssue.Repository, rootIssue.Number))
	}

	rootHex, ok := labelColour(&rootIssue, LabelIdeation)
	if !ok {
		return "", fmt.Errorf("the root %s carries no readable colour for the %q label",
			forestRef(rootIssue.Repository, rootIssue.Number), LabelIdeation)
	}
	trackerHex, ok := labelColour(tracker, LabelOutcome)
	if !ok {
		return "", fmt.Errorf("the tracker %s carries no readable colour for the %q label",
			forestRef(tracker.Repository, tracker.Number), LabelOutcome)
	}

	graph := renderer.Graph{
		Root:    chartNode(&rootIssue),
		Members: chartNodes(rows),
		Edges:   edges,
		Strokes: renderer.Strokes{
			Root:    rootHex,
			Tracker: trackerHex,
			Member:  strokeMemberHex,
			Closed:  strokeClosedHex,
		},
	}
	return renderer.Render(graph, renderer.Config{
		MaxSourceCharacters: e.cfg.ChartCeiling(),
		Location:            e.cfg.ChartLocation(),
	})
}

// forestGraph gathers a forest's rows and the blocking edges between them in
// the reads the forest's own relations answer: the rows the root blocks —
// trackers included, the reading the digest and the cascade apply
// (§forgejo/decisions/actionable-state-digest) — then each row's blocks for the
// edges out of it. A row that is a pull request is a delivery, not a member: it
// draws no node and no edge (§forgejo/renderer/edge-classes).
//
// The root's own listing is read once and serves both jobs, so a forest of N
// rows costs the tick N+1 blocks reads and no more: the drawing is the sweep's
// slowest new step, which is why it belongs to the tick and not to a request
// (§forgejo/reconciliation/the-sweep).
//
// An edge reaching an issue the forest holds no row for is not part of this
// drawing — a member blocking a ticket no forest names is that ticket's
// business, not the chart's — and the renderer refuses an edge naming no node on
// either side, so filtering here is what keeps one wide blocking edge from
// failing the whole forest.
func (e *Engine) forestGraph(root *Issue) ([]Issue, []renderer.Edge, error) {
	blocked, err := e.rowsBlocked(root)
	if err != nil {
		return nil, nil, err
	}
	rows := make([]Issue, 0, len(blocked))
	for i := range blocked {
		row := &blocked[i]
		if row.IsPull {
			continue
		}
		if row.Repository == "" {
			row.Repository = root.Repository
		}
		rows = append(rows, *row)
	}

	peers := make([]Issue, 0, len(rows)+1)
	peers = append(peers, *root)
	peers = append(peers, rows...)

	edges := drawnEdges(peers, *root, blocked)
	for _, row := range rows {
		blocked, err := e.rowsBlocked(&row)
		if err != nil {
			return nil, nil, err
		}
		edges = append(edges, drawnEdges(peers, row, blocked)...)
	}
	return rows, edges, nil
}

// rowsBlocked is the one read forestGraph makes per row, wrapped with the row it
// concerned (§forgejo/client/operations).
func (e *Engine) rowsBlocked(row *Issue) ([]Issue, error) {
	owner, name := splitRepo(row.Repository)
	blocked, err := e.api.IssueBlocks(owner, name, row.Number)
	if err != nil {
		return nil, fmt.Errorf("issues %s blocks: %w", forestRef(row.Repository, row.Number), err)
	}
	return blocked, nil
}

// drawnEdges keeps, from one row's blocks listing, the edges whose far end is a
// row this forest holds — a listing reports no repository of its own for a row
// that lives where the read was made (§forgejo/client/operations).
func drawnEdges(peers []Issue, from Issue, blocked []Issue) []renderer.Edge {
	edges := []renderer.Edge{}
	for i := range blocked {
		to := &blocked[i]
		if to.IsPull {
			continue
		}
		home := to.Repository
		if home == "" {
			home = from.Repository
		}
		for _, row := range peers {
			if row.Number == to.Number && sameRepo(row.Repository, home) {
				edges = append(edges, renderer.Edge{
					From: renderer.Ref{Repository: from.Repository, Number: from.Number},
					To:   renderer.Ref{Repository: home, Number: to.Number},
				})
				break
			}
		}
	}
	return edges
}

// chartNode is an issue as the renderer draws it. The whole title travels as the
// summary on purpose: the renderer cuts it to the five words a label carries
// (§forgejo/renderer/labels). A closed row carries the forge's own close time —
// never its `updated_at`, whose last edit is not the moment of the close
// (§forgejo/renderer/closed-members).
func chartNode(issue *Issue) renderer.Node {
	return renderer.Node{
		Ref:      renderer.Ref{Repository: issue.Repository, Number: issue.Number},
		Role:     chartRole(issue),
		Summary:  issue.Title,
		HTMLURL:  issue.HTMLURL,
		Closed:   issue.State == "closed",
		ClosedAt: issue.ClosedAt,
	}
}

// chartNodes is the forest's rows as nodes.
func chartNodes(rows []Issue) []renderer.Node {
	nodes := make([]renderer.Node, len(rows))
	for i := range rows {
		nodes[i] = chartNode(&rows[i])
	}
	return nodes
}

// chartRole is a row's role in the drawing, named the same way the decision
// code names what a row is: the outcome label without ideation is a tracker,
// the sync label a grouping, the human-task label a person's claim, and
// anything else a work item (§forgejo/renderer/identifiers-and-shapes,
// §forgejo/decisions/transition-table).
func chartRole(issue *Issue) renderer.Role {
	switch {
	case isTracker(issue):
		return renderer.RoleTracker
	case hasLabel(issue, LabelSync):
		return renderer.RoleSync
	case hasLabel(issue, LabelHumanTask):
		return renderer.RoleHumanTask
	default:
		return renderer.RoleWork
	}
}

// trackerRow is the forest's tracker among its rows, or nil when it holds none.
func trackerRow(rows []Issue) *Issue {
	for i := range rows {
		if isTracker(&rows[i]) {
			return &rows[i]
		}
	}
	return nil
}

// labelColour is the stroke hex a role label gives a node: the colour the forge
// reports for the label, matched the way every reserved name is matched —
// case-insensitively (§forgejo/decisions/transition-table). A label the issue
// carries with no readable colour is as good as absent
// (§forgejo/client/operations).
func labelColour(issue *Issue, label string) (string, bool) {
	for name, hex := range issue.LabelColours {
		if strings.EqualFold(name, label) {
			return hex, true
		}
	}
	return "", false
}

// sameRepo reports whether two spellings name one repository, the way the
// renderer's own collapsed view does: case-insensitively on the full
// "owner/repo" (§forgejo/renderer/identifiers-and-shapes).
func sameRepo(a, b string) bool {
	return strings.EqualFold(strings.TrimRight(a, "/"), strings.TrimRight(b, "/"))
}
