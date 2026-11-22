package renderer

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// draw says what prepare decided, whole or collapsed
// (§forgejo/renderer/the-source). The two views differ only in what one node
// stands for; the shape of the source is the same.
func draw(f *forest, strokes Strokes, cfg Config, collapsed bool) string {
	var b strings.Builder
	b.WriteString("flowchart TD\n")
	b.WriteString("%% forest " + escapeText(f.root().ref.String()) + " rendered by maitred\n")
	for _, n := range f.nodes {
		if n.role == RoleTracker && n.fan > 0 {
			noun := "issues"
			if n.fan == 1 {
				noun = "issue"
			}
			b.WriteString("%% tracker " + escapeText(n.ref.String()) + " fans to " + strconv.Itoa(n.fan) +
				" " + noun + " (fan stated, not drawn)\n")
		}
	}
	if collapsed {
		b.WriteString("%% collapsed to one node per repository: the source exceeds " +
			strconv.Itoa(cfg.MaxSourceCharacters) + " characters\n")
	}

	classes := map[string][]string{}
	if collapsed {
		drawCollapsed(&b, f, classes)
	} else {
		drawNodes(&b, f, cfg, classes)
		drawEdges(&b, f)
	}
	drawClasses(&b, strokes, classes)
	return b.String()
}

// drawNodes declares one line per issue, in node order, and files each under
// the class its role and state put it in (§forgejo/renderer/identifiers-and-shapes,
// §forgejo/renderer/classes-and-strokes).
func drawNodes(b *strings.Builder, f *forest, cfg Config, classes map[string][]string) {
	for _, n := range f.nodes {
		opening, closing := shapeOf(n.kind)
		fmt.Fprintf(b, "  %s%s\"%s\"%s\n", n.id, opening, n.label(cfg), closing)
		class := classOf(n.kind, n.closed)
		classes[class] = append(classes[class], n.id)
	}
}

// drawnRepo is one repository's node in the collapsed view
// (§forgejo/renderer/collapsing).
type drawnRepo struct {
	name        string
	id          string
	count       int
	hasRoot     bool
	hasTracker  bool
	allClosed   bool
	declaration int
}

// drawCollapsed declares one node per repository, the root's first and the rest
// by name, and aggregates the work-order edges between repositories
// (§forgejo/renderer/collapsing).
func drawCollapsed(b *strings.Builder, f *forest, classes map[string][]string) {
	repos := map[string]*drawnRepo{}
	var order []string
	for _, n := range f.nodes {
		key := strings.ToLower(n.repo())
		repo, ok := repos[key]
		if !ok {
			repo = &drawnRepo{name: n.repo(), id: repoID(n.repo()), allClosed: true}
			repos[key] = repo
			order = append(order, key)
		}
		repo.count++
		repo.hasRoot = repo.hasRoot || n.kind == 0
		repo.hasTracker = repo.hasTracker || n.kind == 1
		repo.allClosed = repo.allClosed && n.closed
	}
	rootKey := strings.ToLower(f.root().repo())
	sort.SliceStable(order, func(i, j int) bool {
		if (order[i] == rootKey) != (order[j] == rootKey) {
			return order[i] == rootKey
		}
		return order[i] < order[j]
	})
	for i, key := range order {
		repo := repos[key]
		repo.declaration = i
		// A collapsed node's shape and class follow the roles it holds, so
		// they ride the one shape table and the one class ladder
		// (§forgejo/renderer/collapsing).
		kind := 2
		switch {
		case repo.hasRoot:
			kind = 0
		case repo.hasTracker:
			kind = 1
		}
		opening, closing := shapeOf(kind)
		class := classOf(kind, repo.allClosed)
		prefix := ""
		if repo.allClosed {
			prefix = "✅ "
		}
		fmt.Fprintf(b, "  %s%s\"%s%s (%d)\"%s\n", repo.id, opening, prefix, escapeText(repo.name), repo.count, closing)
		classes[class] = append(classes[class], repo.id)
	}

	work := map[string]repoPair{}
	for _, e := range f.work {
		from, to := repos[strings.ToLower(e.from.repo())], repos[strings.ToLower(e.to.repo())]
		if from.declaration == to.declaration {
			continue // an edge inside one repository draws nothing once collapsed
		}
		work[fmt.Sprintf("%04d\x00%04d", from.declaration, to.declaration)] = repoPair{from, to}
	}
	for _, line := range collapsedLines(work) {
		b.WriteString("  " + line + "\n")
	}
}

// repoPair is one drawn edge between two repositories.
type repoPair struct {
	from, to *drawnRepo
}

// collapsedLines renders an aggregated work-order set, ordered by source, then
// target (§forgejo/renderer/the-source).
func collapsedLines(set map[string]repoPair) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		pair := set[key]
		out = append(out, pair.from.id+" ==> "+pair.to.id)
	}
	return out
}

// drawEdges declares the work-order edges (§forgejo/renderer/the-source). The
// root's blocking edges and a tracker's fan are stated elsewhere, never drawn
// here (§forgejo/renderer/edge-classes).
func drawEdges(b *strings.Builder, f *forest) {
	for _, e := range f.work {
		fmt.Fprintf(b, "  %s ==> %s\n", e.from.id, e.to.id)
	}
}

// drawClasses declares the four classes in order, then names each class's
// members (§forgejo/renderer/classes-and-strokes).
func drawClasses(b *strings.Builder, strokes Strokes, classes map[string][]string) {
	fmt.Fprintf(b, "  classDef root stroke:%s,stroke-width:2px\n", strokes.Root)
	fmt.Fprintf(b, "  classDef tracker stroke:%s,stroke-width:2px\n", strokes.Tracker)
	fmt.Fprintf(b, "  classDef member stroke:%s\n", strokes.Member)
	fmt.Fprintf(b, "  classDef closed stroke:%s,stroke-dasharray:4 3,fill:%s,fill-opacity:0.25\n",
		strokes.Closed, strokes.Closed)
	for _, class := range classOrder {
		if members := classes[class]; len(members) > 0 {
			fmt.Fprintf(b, "  class %s %s\n", strings.Join(members, ","), class)
		}
	}
}

// shapeOf is the shape that carries a role. Each half sits outside the quotes
// the emit formats already write, so a label is quoted once and once only
// (§forgejo/renderer/identifiers-and-shapes).
func shapeOf(kind int) (opening, closing string) {
	switch kind {
	case 0:
		return `{{`, `}}`
	case 1:
		return `([`, `])`
	default:
		return `[`, `]`
	}
}

// classOf is the ladder both views take a class from: the root's, a
// tracker's, the dim of a closed member, and member for every other role
// (§forgejo/renderer/classes-and-strokes).
func classOf(kind int, closed bool) string {
	switch {
	case kind == 0:
		return "root"
	case kind == 1:
		return "tracker"
	case closed:
		return "closed"
	default:
		return "member"
	}
}

// label composes a node's label: its reference, its summary, the anchor the
// summary is wrapped in, and — where the node is closed — the mark and the
// stamp (§forgejo/renderer/labels, §forgejo/renderer/closed-members).
func (n *node) label(cfg Config) string {
	summary := sanitizeSummary(n.summary)
	if n.role == RoleSync && len(n.syncNumbers) > 0 {
		var carry []string
		for _, number := range n.syncNumbers {
			carry = append(carry, fmt.Sprintf("#%d", number))
		}
		if summary != "" {
			summary += " "
		}
		summary += "(" + strings.Join(carry, " ") + ")"
	}
	text := escapeText(n.ref.String())
	if summary != "" {
		text += separator + summary
	}

	style := "color:inherit"
	if n.closed {
		style += ";text-decoration:line-through"
	}
	prefix := ""
	if n.role == RoleHumanTask {
		prefix += "⚑ "
	}
	if n.closed {
		prefix += "✅ "
	}
	label := fmt.Sprintf("%s<a href='%s' target='_blank' style='%s'>%s</a>", prefix, n.url, style, wrapLabel(text))
	if n.closed {
		label += "<br/>closed " + n.closedAt.In(cfg.location()).Format(closedTimeLayout)
	}
	return label
}

// repo is the node's repository, named bare as the chart names it
// (§forgejo/renderer/labels).
func (n *node) repo() string { return bareName(n.ref.Repository) }

// root is the forest's ideation root, first in node order.
func (f *forest) root() *node { return f.nodes[0] }
