// Package renderer turns a forest — an ideation root, the rows it blocks and
// the blocking edges between them — into the Mermaid `flowchart TD` source
// that draws it, so the drawing follows rules rather than an agent's judgement
// (§forgejo/renderer/the-contract).
//
// It is a function from a graph to a string: it reads no forge, caches nothing
// and consults no clock, so the same graph renders byte-identical source and a
// diff in the output means the graph moved (§forgejo/renderer/determinism).
// Gathering the graph — and reading the stroke hexes fresh from the role
// labels' colours, which the renderer never invents
// (§forgejo/renderer/classes-and-strokes) — is the caller's work.
package renderer

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// labelWrapBudget is the "about 30 characters" a label's text runs to before a
// <br/> breaks it (§forgejo/renderer/labels).
const labelWrapBudget = 30

// summaryWordCap is the "five words or fewer" a label's summary is cut to, so
// a caller that hands over a whole title still gets a bounded label
// (§forgejo/renderer/labels).
const summaryWordCap = 5

// closedTimeLayout renders the date and time half of a close stamp; the zone
// abbreviation follows from the configured location
// (§forgejo/renderer/closed-members).
const closedTimeLayout = "2006-01-02 15:04 MST"

// separator is what a label puts between an issue's reference and its summary
// (§forgejo/renderer/labels).
const separator = " · "

// classOrder is the order the classes are declared in and the order a node
// takes one of them: root, tracker, member, closed
// (§forgejo/renderer/classes-and-strokes).
var classOrder = []string{"root", "tracker", "member", "closed"}

// Role is what a forest row is (§forgejo/renderer/identifiers-and-shapes). The
// root's role is never carried here: the root is the graph's Root field, and
// its shape follows from that whatever its Role field holds.
type Role uint8

const (
	// RoleWork is a work item — a rectangle — and the common case, so it is the
	// value a caller leaves a node at.
	RoleWork Role = iota
	// RoleTracker is an outcome tracker: a stadium, and the one row whose
	// blocking edges are stated rather than drawn
	// (§forgejo/renderer/edge-classes).
	RoleTracker
	// RoleSync is a grouping issue: a rectangle whose label carries the numbers
	// of the members that block it (§forgejo/renderer/labels).
	RoleSync
	// RoleHumanTask is a task for a fleshier agent: a rectangle prefixed ⚑,
	// which is what carries its role — four classes are declared, and a human
	// task is drawn in member (§forgejo/renderer/classes-and-strokes).
	RoleHumanTask
)

// String names the role for an error message.
func (r Role) String() string {
	switch r {
	case RoleWork:
		return "work item"
	case RoleTracker:
		return "tracker"
	case RoleSync:
		return "sync issue"
	case RoleHumanTask:
		return "human task"
	default:
		return fmt.Sprintf("role %d", uint8(r))
	}
}

// Ref names an issue by its repository — "owner/repo" as the forge spells it, or
// a bare repository name — and its number. The chart names an issue by the
// repository's bare name (§forgejo/renderer/labels) and folds that name into the
// identifier (§forgejo/renderer/identifiers-and-shapes), so an owner never
// reaches the drawing.
type Ref struct {
	Repository string
	Number     int
}

// String is the issue's reference as the chart draws it: `<repo>#<number>`.
func (r Ref) String() string {
	return fmt.Sprintf("%s#%d", bareName(r.Repository), r.Number)
}

// key is the fold's identity: two spellings of one issue share it, which is what
// makes a row named twice an error rather than two nodes
// (§forgejo/renderer/identifiers-and-shapes).
func (r Ref) key() string {
	return fmt.Sprintf("%s#%d", strings.ToLower(bareName(r.Repository)), r.Number)
}

// Node is one issue in the forest. Summary is the label's free text — the only
// text in the source an agent's hand touches, so it is cut to five words and
// escaped (§forgejo/renderer/labels). ClosedAt is read only where Closed stands,
// and rendered in the configured zone (§forgejo/renderer/closed-members).
type Node struct {
	Ref
	Role     Role
	Summary  string
	HTMLURL  string
	Closed   bool
	ClosedAt time.Time
}

// Edge is a blocking edge: From blocks To (§forgejo/renderer/edge-classes). The
// renderer never invents one.
type Edge struct {
	From, To Ref
}

// Strokes carries the classes' hexes as the graph's own data. Each is read fresh
// at each render from the colour of the label that names the role on a forest
// issue; the renderer invents none and caches none
// (§forgejo/renderer/classes-and-strokes).
type Strokes struct {
	Root       string
	Tracker    string
	Member     string
	Closed     string
	ClosedFill string
}

// Graph is the forest: the ideation root, the rows the root reaches — trackers
// included — every blocking edge the caller found, and the strokes
// (§forgejo/renderer/the-contract). The order rows and edges arrive in changes
// nothing (§forgejo/renderer/determinism).
type Graph struct {
	Root    Node
	Members []Node
	Edges   []Edge
	Strokes Strokes
}

// Config is the renderer's configuration: the ceiling past which the source
// collapses to one node per repository (§forgejo/renderer/collapsing), and the
// zone a close stamp is rendered in. A nil location is UTC.
type Config struct {
	MaxSourceCharacters int
	Location            *time.Location
}

// location is the zone a close stamp is rendered in, UTC when unset.
func (c Config) location() *time.Location {
	if c.Location == nil {
		return time.UTC
	}
	return c.Location
}

// node is a graph node folded into the drawing: its identifier, shape and class,
// the label's parts, and the declaration order.
type node struct {
	id          string
	ref         Ref
	role        Role
	kind        int
	isRoot      bool
	closed      bool
	closedAt    time.Time
	url         string
	summary     string
	syncNumbers []int
	fan         int
}

// edge is one drawn edge between two nodes.
type edge struct {
	from, to *node
}

// forest is the prepared graph: the nodes in declaration order, the edges in the
// classes the source declares them in, and each tracker's fan
// (§forgejo/renderer/edge-classes).
type forest struct {
	nodes []*node
	gate  []edge
	work  []edge
}

// Render returns the `flowchart TD` source that draws the forest, ending in a
// single newline so a caller may splice it into a fence pair verbatim
// (§forgejo/renderer/the-source). No partial source is returned with an error.
func Render(g Graph, cfg Config) (string, error) {
	if cfg.MaxSourceCharacters <= 0 {
		return "", fmt.Errorf("renderer: a ceiling of %d characters fits no graph", cfg.MaxSourceCharacters)
	}
	if err := checkStrokes(g.Strokes); err != nil {
		return "", err
	}
	f, err := prepare(g)
	if err != nil {
		return "", err
	}

	full := draw(f, g.Strokes, cfg, false)
	if len([]rune(full)) <= cfg.MaxSourceCharacters {
		return full, nil
	}
	return draw(f, g.Strokes, cfg, true), nil
}

// prepare folds the graph into ordered nodes and drawn edges. It is where the
// rules that decide *what* the source says live; draw then says it, whole or
// collapsed.
func prepare(g Graph) (*forest, error) {
	nodes, byKey, err := declare(g)
	if err != nil {
		return nil, err
	}
	root := nodes[0]

	gate := map[string]edge{}
	work := map[string]edge{}
	fanPeers := map[string]map[string]bool{}
	for _, row := range g.Edges {
		from, err := lookup(byKey, row.From)
		if err != nil {
			return nil, err
		}
		to, err := lookup(byKey, row.To)
		if err != nil {
			return nil, err
		}
		if from.id == to.id {
			// An issue blocking itself states nothing
			// (§forgejo/renderer/edge-classes).
			continue
		}
		key := from.id + "\x00" + to.id
		if from.id == root.id {
			// The root's membership edges are what the forest hangs from,
			// including the one into a tracker (§forgejo/renderer/edge-classes).
			gate[key] = edge{from, to}
			continue
		}
		if from.role == RoleTracker || to.role == RoleTracker {
			// A tracker's fan is stated, never drawn: the root's membership
			// edge into it is drawn, so it is not a peer
			// (§forgejo/renderer/note-lines).
			for _, pair := range [][2]*node{{from, to}, {to, from}} {
				tracker, peer := pair[0], pair[1]
				if tracker.role != RoleTracker || peer.id == tracker.id {
					continue
				}
				if fanPeers[tracker.id] == nil {
					fanPeers[tracker.id] = map[string]bool{}
				}
				fanPeers[tracker.id][peer.id] = true
			}
			continue
		}
		work[key] = edge{from, to}
	}

	reduced, err := reduceWork(work)
	if err != nil {
		return nil, err
	}
	position := positions(nodes)
	for id, peers := range fanPeers {
		if n := findNode(nodes, id); n != nil {
			n.fan = len(peers)
		}
	}

	// A sync issue carries the numbers of the members that block it, which is
	// a fact of the graph's work order before any drawing economy is applied:
	// a blocker that also reaches the sync by a longer path still blocks it,
	// so the label reads the unreduced edges and only the drawing reduces
	// (§forgejo/renderer/labels, §forgejo/renderer/edge-classes).
	for _, n := range nodes {
		if n.role != RoleSync {
			continue
		}
		for _, e := range work {
			if e.to.id == n.id {
				n.syncNumbers = append(n.syncNumbers, e.from.ref.Number)
			}
		}
		sort.Ints(n.syncNumbers)
	}

	return &forest{
		nodes: nodes,
		gate:  orderByTarget(values(gate), position),
		work:  orderBySourceTarget(reduced, position),
	}, nil
}

// declare validates the graph and returns its nodes in declaration order: the
// root, then the trackers, then the members — each group by repository name,
// then by number (§forgejo/renderer/the-source).
func declare(g Graph) ([]*node, map[string]*node, error) {
	if strings.TrimSpace(g.Root.Repository) == "" && g.Root.Number == 0 {
		return nil, nil, errors.New("renderer: the graph has no root")
	}
	rows := append([]Node{g.Root}, g.Members...)
	byKey := make(map[string]*node, len(rows))
	nodes := make([]*node, 0, len(rows))
	for i, row := range rows {
		if err := checkNode(row); err != nil {
			return nil, nil, err
		}
		id := nodeID(row.Ref)
		if _, seen := byKey[row.Ref.key()]; seen {
			return nil, nil, fmt.Errorf("renderer: the graph names issue %s twice", row.Ref)
		}
		for _, other := range nodes {
			if other.id == id {
				return nil, nil, fmt.Errorf("renderer: issues %s and %s fold to one identifier %s",
					other.ref, row.Ref, id)
			}
		}
		kind := 2
		if i == 0 {
			kind = 0
		} else if row.Role == RoleTracker {
			kind = 1
		}
		nodes = append(nodes, &node{
			id:       id,
			ref:      row.Ref,
			role:     row.Role,
			closed:   row.Closed,
			closedAt: row.ClosedAt,
			url:      row.HTMLURL,
			summary:  row.Summary,
			kind:     kind,
		})
		byKey[row.Ref.key()] = nodes[len(nodes)-1]
	}
	sort.SliceStable(nodes, func(i, j int) bool {
		if nodes[i].kind != nodes[j].kind {
			return nodes[i].kind < nodes[j].kind
		}
		return lessRef(nodes[i].ref, nodes[j].ref)
	})
	return nodes, byKey, nil
}

// checkNode rejects the rows no drawing can be honest about: a node that names
// no issue or cannot be linked, and one that says "closed" without a time
// (§forgejo/renderer/labels, §forgejo/renderer/closed-members).
func checkNode(row Node) error {
	if strings.TrimSpace(row.Repository) == "" || row.Number <= 0 {
		return fmt.Errorf("renderer: a node names no issue (%s)", row.Ref)
	}
	if !quotable(row.HTMLURL) {
		return fmt.Errorf("renderer: issue %s has a URL that cannot be quoted: %q", row.Ref, row.HTMLURL)
	}
	if row.Closed && row.ClosedAt.IsZero() {
		return fmt.Errorf("renderer: issue %s is closed but carries no close time", row.Ref)
	}
	return nil
}

// lookup finds the node an edge names, failing when the graph holds no node for
// it (§forgejo/renderer/edge-classes).
func lookup(byKey map[string]*node, ref Ref) (*node, error) {
	if n, ok := byKey[ref.key()]; ok {
		return n, nil
	}
	return nil, fmt.Errorf("renderer: an edge names %s, which the graph holds no node for", ref)
}

// reduceWork drops the work-order edges a path already states, and refuses a
// cycle, which has no reduction (§forgejo/renderer/edge-classes).
func reduceWork(work map[string]edge) ([]edge, error) {
	next := map[string][]string{}
	for _, e := range work {
		next[e.from.id] = append(next[e.from.id], e.to.id)
	}
	starts := make([]string, 0, len(next))
	for id := range next {
		sort.Strings(next[id])
		starts = append(starts, id)
	}
	sort.Strings(starts)
	if at := findCycle(next, starts); at != "" {
		return nil, fmt.Errorf("renderer: the work order contains a cycle at %s", at)
	}

	reduced := make([]edge, 0, len(work))
	for _, e := range work {
		if reachable(next, e.from.id, e.to.id) {
			continue // a path of at least two edges already states this one
		}
		reduced = append(reduced, e)
	}
	return reduced, nil
}

// reachable reports whether to stands past a path of at least two edges from
// from: the long way round, never the edge under test.
func reachable(next map[string][]string, from, to string) bool {
	seen := map[string]bool{from: true}
	var frontier []string
	for _, id := range next[from] {
		if id != to {
			frontier = append(frontier, id)
		}
	}
	for len(frontier) > 0 {
		id := frontier[0]
		frontier = frontier[1:]
		if id == from || seen[id] {
			continue
		}
		seen[id] = true
		if id == to {
			return true
		}
		frontier = append(frontier, next[id]...)
	}
	return false
}

// findCycle returns an identifier a work-order cycle passes through, or the
// empty string when the order is acyclic.
func findCycle(next map[string][]string, starts []string) string {
	const (
		unread = iota
		open
		closed
	)
	state := make(map[string]int, len(next))
	var walk func(string) string
	walk = func(id string) string {
		state[id] = open
		for _, to := range next[id] {
			switch state[to] {
			case open:
				return to
			case unread:
				if found := walk(to); found != "" {
					return found
				}
			}
		}
		state[id] = closed
		return ""
	}
	for _, id := range starts {
		if state[id] == unread {
			if found := walk(id); found != "" {
				return found
			}
		}
	}
	return ""
}

// orderByTarget sorts the gate edges in the node order of their targets
// (§forgejo/renderer/the-source).
func orderByTarget(edges []edge, position map[string]int) []edge {
	sort.SliceStable(edges, func(i, j int) bool {
		return position[edges[i].to.id] < position[edges[j].to.id]
	})
	return edges
}

// orderBySourceTarget sorts the work-order edges by source, then target
// (§forgejo/renderer/the-source).
func orderBySourceTarget(edges []edge, position map[string]int) []edge {
	sort.SliceStable(edges, func(i, j int) bool {
		a, b := edges[i], edges[j]
		if position[a.from.id] != position[b.from.id] {
			return position[a.from.id] < position[b.from.id]
		}
		return position[a.to.id] < position[b.to.id]
	})
	return edges
}

// positions maps each node's identifier to its place in node order.
func positions(nodes []*node) map[string]int {
	position := make(map[string]int, len(nodes))
	for i, n := range nodes {
		position[n.id] = i
	}
	return position
}

// values flattens an edge set keyed for de-duplication.
func values(set map[string]edge) []edge {
	out := make([]edge, 0, len(set))
	for _, e := range set {
		out = append(out, e)
	}
	return out
}

// findNode returns the node an identifier names.
func findNode(nodes []*node, id string) *node {
	for _, n := range nodes {
		if n.id == id {
			return n
		}
	}
	return nil
}
