package forgejo

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"maitred/pkg/forgejo/renderer"
)

// The tracker block: the region of an outcome tracker's own description the
// engine writes on every sweep tick (§forgejo/reconciliation/the-tracker-block).
// The drawing comes from the renderer (§forgejo/renderer/the-contract); this
// file makes the write narrow enough that the drawing is the only thing the
// write can change.
//
// The block is delimited by a pair of HTML comments so it can sit inside prose
// a human owns. Everything outside the pair — the outcome prose, a member's
// body, a grouping issue's body — is out of reach by construction: this file
// addresses no issue but a forest's tracker, and it sends no field but the body
// (§forgejo/client/the-one-mutation).
const (
	// fenceBegin and fenceEnd open and close the region the engine owns.
	// They are the pair this file writes and the pair a revision keyed
	// outside of leaves untouched (outsideFence), stated once so the
	// region the engine draws inside can never drift from the region it
	// splices (§forgejo/reconciliation/the-tracker-block).
	fenceBegin = "<!-- maitred:forest:begin -->"
	fenceEnd   = "<!-- maitred:forest:end -->"
	// stateHeading is the heading the block stands under when a tracker's
	// description carries no fence pair yet and the block is inserted as
	// the body's first heading.
	stateHeading = "## Current state"
	// stampLabel prefixes the recency line the block carries:
	// `Last revisited: <YYYY-MM-DD HH:MM> <TZ>`. The outcomes payload
	// spells its chart_revisited field with the same layout, so the two
	// sinks of one drawing state the same moment
	// (§forgejo/observability/the-outcomes-view).
	stampLabel = "Last revisited:"
	// stampLayout renders the stamp's date and time half; the label is the
	// shared stampLabel, and the zone abbreviation follows from the
	// configured location (§forgejo/config/tracker-chart). stampAt spells an
	// instant in it.
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
// the others nothing. The drawing each forest produces — the one drawing the
// tick makes for it — also goes onto the outcomes payload, empty wherever the
// tick drew nothing (recordForestCharts,
// §forgejo/observability/the-outcomes-view). The walk has already decided and
// dispatched by the time this runs — the drawing is the tick's last step,
// never a decision's predecessor. at is the moment the stamp carries: one
// clock read for the whole pass, so every forest a tick draws states the same
// instant, in the block and in the payload alike. It is a parameter rather
// than a clock read here for that, and so a test can watch the stamp move
// while the graph stands still.
func (e *Engine) writeTrackerBlocks(forests []Forest, at time.Time) {
	sources := make([]string, len(forests))
	for i, forest := range forests {
		// A forest whose tracker is missing states its absence through the
		// outcomes view; there is nothing here to write it to and nothing
		// worth a log line (§forgejo/observability/the-outcomes-view).
		if forest.Tracker == nil {
			continue
		}
		source, err := e.writeTrackerBlock(forest, at)
		if err != nil {
			e.log.Error("reconciliation: tracker current-state block not written",
				"tracker", trackerRef(forest), "error", err)
		}
		sources[i] = source
	}
	e.recordForestCharts(forests, sources, at)
}

// trackerRef names the tracker a forest's block is written to — the address a
// diagnosis starts from (§forgejo/observability/the-outcomes-view).
func trackerRef(forest Forest) string {
	return forestRef(forest.Tracker.Repository, forest.Tracker.Number)
}

// drawTrackerBeforeWrapUp is the wrap-up path's call to the tracker block:
// the same write the tick makes, at the one moment a finished forest would
// otherwise be left holding a stale drawing (§forgejo/reconciliation/the-tracker-block,
// s-git/maitred#232). The root comes from the blocker rows the dispatch
// already holds — the gate relation is the one every other walk of the graph
// reads — and a tracker gated by no ideation issue, never commissioned, has
// no forest to draw. Which of those two absences it is matters to whoever
// reads the log: no blocker rows at all is a read that failed, not a graph
// fact, so the two say so apart. The rows the dispatch holds were read for a
// tracker — the sweep's wrap-up row and the cascade's classifier both decide
// wrap-up on the outcome label first (§forgejo/reconciliation/watermark-exemptions,
// §forgejo/decisions/unblock-cascade) — so the write addresses that issue in
// the repository the dispatch found it in, and the root — which a blocking
// edge placed in whichever repository it likes — is admitted by the shared
// write's own test of both ends of the forest, stated there once so every
// caller inherits it (§forgejo/webhook/repository-roles). A failure is logged
// naming the tracker and costs the dispatch nothing: the chart is UX, the
// wrap-up is the work. The drawing's other sink is not fed here: step 5's fold
// is a whole-set fold and this path holds one ad-hoc forest, so the source is
// discarded and the payload catches up with this drawing at the next tick's
// step 5 (§forgejo/reconciliation/the-tracker-block).
func (e *Engine) drawTrackerBeforeWrapUp(tracker *Issue, blockers []Issue, foundIn string) {
	if tracker == nil {
		return
	}
	home := tracker.Repository
	if home == "" {
		home = foundIn // a single-object read may name no repository of its own (§forgejo/client/operations)
	}
	ref := forestRef(home, tracker.Number)
	root := ideationGate(blockers)
	if root == nil {
		if blockers == nil {
			e.log.Warn("wrap-up: tracker blocker rows unread; cannot tell whether a gate draws this forest",
				"tracker", ref)
		} else {
			e.log.Debug("wrap-up: tracker is gated by no ideation issue; nothing to draw", "tracker", ref)
		}
		return
	}
	rootRow := forestIssue(*root, home)
	trackerRow := forestIssue(*tracker, home)
	forest := Forest{Repository: rootRow.Repository, Root: rootRow, Tracker: &trackerRow}
	// The source is discarded on purpose — see the note above on why the
	// wrap-up path feeds the block alone.
	if _, err := e.writeTrackerBlock(forest, time.Now()); err != nil {
		e.log.Error("wrap-up: tracker current-state block not written",
			"tracker", ref, "error", err)
	}
}

// writeTrackerBlock renders one forest and lands its block on that forest's
// tracker, which the caller has established the forest has. Both ends of the
// forest pay the repository-admission test here, before anything of either is
// read, so every call site inherits the rule that no repository a maitred
// role does not name is drawn or published. It returns the source the tick
// drew — the bytes the payload carries alongside these rows — whether or not
// the write succeeded: a drawing refused a body (a fence pair too ambiguous
// to splice into) still stands in the outcomes view
// (§forgejo/observability/the-outcomes-view). Where the body the composition
// produced is the body already there — two ticks inside one minute over an
// unchanged graph — nothing is sent, so a tick never bumps a tracker for
// nothing (§forgejo/reconciliation/the-tracker-block).
func (e *Engine) writeTrackerBlock(forest Forest, at time.Time) (string, error) {
	owner, name := splitRepo(forest.Tracker.Repository)
	ref := trackerRef(forest)
	// Which repository holds a tracker is forge data, not configuration: the
	// root's blocking edges name it, and a tracker may live elsewhere
	// (§forgejo/observability/the-outcomes-view). So the one mutation the
	// engine makes asks the admission test every other cross-repository hop
	// pays before it reads or writes that repository — the enumeration's own
	// repository admitted by the enumeration, every other one only if a
	// maitred role names it (§forgejo/webhook/repository-roles,
	// §forgejo/reconciliation/the-tracker-block).
	admits, err := e.remitAdmits(forest.Tracker.Repository, forest.Root.Repository)
	if err != nil {
		return "", fmt.Errorf("scope of %s: %w", forest.Tracker.Repository, err)
	}
	if !admits {
		// Debug, the level every other cross-repository hop uses for this
		// sentence: where a deployment intends the tracker to stay unwritten,
		// this is one line per forest per tick forever, not an event worth
		// reading (§forgejo/webhook/repository-roles,
		// §forgejo/observability/the-decision-log).
		e.log.Debug("reconciliation: tracker lives in a repository no maitred role names; nothing written",
			"tracker", ref)
		return "", nil
	}
	// A blocking edge names its far end, so the root's repository is forge
	// data too, and the same test is asked of it here rather than at
	// whichever call site thought of it: a root standing where no maitred
	// role names its repository is read nothing of and drawn nowhere, so
	// its title reaches no tracker body the outcomes view publishes
	// (§forgejo/webhook/repository-roles, §forgejo/reconciliation/the-tracker-block).
	admits, err = e.remitAdmits(forest.Root.Repository, forest.Tracker.Repository)
	if err != nil {
		return "", fmt.Errorf("scope of the root %s: %w", forestRef(forest.Root.Repository, forest.Root.Number), err)
	}
	if !admits {
		// Debug for this sentence as well — an unadmitted repository is a
		// deployment's deliberate silence, not an event worth reading
		// (§forgejo/webhook/repository-roles, §forgejo/observability/the-decision-log).
		e.log.Debug("drawing skips a root in a repository no maitred role names; nothing written",
			"tracker", ref, "root", forestRef(forest.Root.Repository, forest.Root.Number))
		return "", nil
	}
	source, err := e.renderForestChart(forest)
	if err != nil {
		return "", fmt.Errorf("rendering %s: %w", ref, err)
	}
	// The description is read here, at the one moment the splice needs it: the
	// window in which a human's edit outside the pair could be overwritten is
	// then a request rather than a forest's worth of reads. The forge offers no
	// conditional body write — this endpoint's parameters are the owner, the
	// repository, the number and the body — so the race is narrowed, not
	// closed, and inside one request the last writer still wins
	// (§forgejo/client/the-one-mutation).
	tracker, err := e.api.GetIssue(owner, name, forest.Tracker.Number)
	if err != nil {
		return source, fmt.Errorf("tracker read %s: %w", ref, err)
	}
	if tracker == nil {
		return source, fmt.Errorf("tracker read %s: no issue returned", ref)
	}
	body, err := spliceStateBlock(tracker.Body, stateBlock(source, at, e.cfg.ChartLocation()))
	if err != nil {
		return source, fmt.Errorf("%s: %w", ref, err)
	}
	if body == tracker.Body {
		// "Nothing was written" is not "nothing was done": the difference
		// between a converged tick and a forest the tick never reached is
		// what an operator reads at Debug (§forgejo/observability/the-decision-log).
		e.log.Debug("tracker current-state block unchanged; nothing written", "tracker", ref)
		return source, nil
	}
	if err := e.api.UpdateIssueBody(owner, name, tracker.Number, body); err != nil {
		return source, fmt.Errorf("writing %s: %w", ref, err)
	}
	// The engine's one mutation says so when it lands, as every other side
	// effect it has does: the question six months out is why a description
	// changed at 03:12, and the forge's revision history alone cannot answer
	// whether it was the engine or an agent
	// (§forgejo/observability/the-decision-log).
	e.log.Info("tracker current-state block written", "tracker", ref)
	return source, nil
}

// stampAt is one instant spelled the way a current-state block's stamp line
// spells it: the moment a drawing was made, in the configured zone. Both sinks
// of one drawing call it — the block composes its stamp line here and the
// outcomes payload spells chart_revisited here — so the two can never drift
// apart into two different instants for the same drawing
// (§forgejo/observability/the-outcomes-view, §forgejo/config/tracker-chart).
func stampAt(at time.Time, loc *time.Location) string {
	return at.In(loc).Format(stampLayout)
}

// stateBlock is the bytes between one tracker's fence pair: the stamp and the
// rendered source in its Mermaid fence. The renderer's source already ends in
// one newline, so the closing fence follows it directly. The stamp carries
// whether or not the graph moved, which is what makes a stale chart
// distinguishable from an untouched forest.
func stateBlock(source string, at time.Time, loc *time.Location) string {
	return stampLabel + " " + stampAt(at, loc) + "\n\n" +
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

// outsideFence is a description with the region the engine owns masked out:
// the fence pair and the bytes between it are the only bytes a current-state
// write moves, and they are the bytes a revision must not be keyed on
// (§forgejo/decisions/loop-prevention). Everything else comes back as it was
// found — including a description whose region is too ambiguous to write to,
// where the engine writes nothing at all and the body is the deployment's own.
func outsideFence(body string) string {
	if strings.Count(body, fenceBegin) != 1 || strings.Count(body, fenceEnd) != 1 {
		return body
	}
	start := strings.Index(body, fenceBegin)
	end := strings.Index(body, fenceEnd)
	if end < start {
		return body
	}
	return body[:start] + body[end+len(fenceEnd):]
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
// failing the whole forest. Which repositories the edges name is forge data, so
// the same filter drops a row no maitred role admits before it is drawn, before
// its title reaches a tracker body the outcomes view publishes, and before its
// own blocks are read (§forgejo/webhook/repository-roles).
//
// The listing is read again rather than handed down from the enumeration that
// paired the root with its tracker: the walk runs between the two, so the
// drawing depicts the graph as it stands when it is drawn rather than rows
// remembered from before the decisions the same tick made
// (§forgejo/reconciliation/the-sweep).
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
		home := row.Repository
		if home == "" {
			home = root.Repository
		}
		// A blocking edge names its far end, so which repositories this
		// gather reads is forge data too. The admission test every other
		// cross-repository read pays is paid here, before the row becomes a
		// node and before its own blocks are read: a row no maitred role
		// admits draws nothing, and its title reaches no tracker body
		// (§forgejo/webhook/repository-roles,
		// §forgejo/reconciliation/the-tracker-block).
		admits, err := e.remitAdmits(home, root.Repository)
		if err != nil {
			return nil, nil, fmt.Errorf("scope of %s: %w", home, err)
		}
		if !admits {
			e.log.Debug("reconciliation: drawing omits a row in a repository no maitred role names",
				"row", forestRef(home, row.Number))
			continue
		}
		drawn := *row
		drawn.Repository = home
		rows = append(rows, drawn)
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
// that lives where the read was made (§forgejo/client/operations). One
// repository is the renderer's own test (§forgejo/renderer/identifiers-and-shapes),
// so an edge this gathering builds can never name two nodes the drawing itself
// would fold into one.
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
			if row.Number == to.Number && renderer.SameRepository(row.Repository, home) {
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
// case-insensitively (§forgejo/decisions/transition-table). The label names are
// walked in the order the row carries them, never the colour map, so a forge
// that let one issue carry `outcome` and `Outcome` in two colours would draw
// the same stroke every tick (§forgejo/renderer/determinism). A label the issue
// carries with no readable colour is as good as absent
// (§forgejo/client/operations).
func labelColour(issue *Issue, label string) (string, bool) {
	for _, name := range issue.Labels {
		if !strings.EqualFold(name, label) {
			continue
		}
		if hex, ok := issue.LabelColours[name]; ok {
			return hex, true
		}
	}
	return "", false
}
