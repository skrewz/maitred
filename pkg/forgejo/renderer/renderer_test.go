package renderer

import (
	"flag"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Goldens are the drawing each rule set produces. Regenerating one is a
// deliberate act (§forgejo/renderer/the-contract): a golden that moves without
// a specification change is a regression, not a fact about the graph.
var update = flag.Bool("update", false, "rewrite the golden files from the rendered source")

func aedt() *time.Location { return time.FixedZone("AEDT", 11*3600) }

func strokes() Strokes {
	return Strokes{
		Root:    "#5319e7",
		Tracker: "#1d76db",
		Member:  "#6b7a8c",
		Closed:  "#3fb950",
	}
}

func ref(repo string, number int) Ref { return Ref{Repository: repo, Number: number} }

func url(repo string, number int) string {
	return "https://example.com/" + repo + "/issues/" + strconv.Itoa(number)
}

// simpleForest is the forest a tracker's current-state block draws: a root
// whose membership edges reach a tracker and three members, a work order that
// chains the members, and the members' blocking edges into the tracker — the
// fan the source states rather than draws (§forgejo/renderer/note-lines).
// The issues are fictional: a golden demonstrates rules, never live tracker
// state a renaming could invite anyone to "refresh".
func simpleForest() Graph {
	root := Node{Ref: ref("example/outcomes", 63), Summary: "ideation", HTMLURL: url("outcomes", 63)}
	tracker := Node{Ref: ref("example/outcomes", 66), Role: RoleTracker, Summary: "tracker", HTMLURL: url("outcomes", 66)}
	render := Node{Ref: ref("example/tasks", 182), Summary: "deterministic forest renderer", HTMLURL: url("tasks", 182)}
	write := Node{Ref: ref("example/tasks", 183), Summary: "engine writes tracker block", HTMLURL: url("tasks", 183)}
	retire := Node{Ref: ref("example/tasks", 184), Role: RoleHumanTask, Summary: "retire prompt chart authoring", HTMLURL: url("tasks", 184)}
	return Graph{
		Root:    root,
		Members: []Node{tracker, retire, render, write},
		Edges: []Edge{
			{From: root.Ref, To: tracker.Ref},
			{From: root.Ref, To: render.Ref},
			{From: root.Ref, To: write.Ref},
			{From: root.Ref, To: retire.Ref},
			{From: render.Ref, To: write.Ref},
			{From: write.Ref, To: retire.Ref},
			{From: render.Ref, To: tracker.Ref},
			{From: write.Ref, To: tracker.Ref},
			{From: retire.Ref, To: tracker.Ref},
		},
		Strokes: strokes(),
	}
}

func wideCeiling() Config { return Config{MaxSourceCharacters: 100000, Location: aedt()} }

func TestRenderGoldens(t *testing.T) {
	t.Parallel()

	closed := simpleForest()
	for i := range closed.Members {
		if closed.Members[i].Number == 183 {
			closed.Members[i].Closed = true
			closed.Members[i].ClosedAt = time.Date(2026, 11, 14, 12, 11, 0, 0, time.UTC)
		}
	}

	syncSet := Graph{
		Root: Node{Ref: ref("example/outcomes", 1), Summary: "ideation", HTMLURL: url("outcomes", 1)},
		Members: []Node{
			{Ref: ref("example/tasks", 200), Role: RoleSync, Summary: "land together", HTMLURL: url("tasks", 200)},
			{Ref: ref("example/tasks", 201), Summary: "first of the set", HTMLURL: url("tasks", 201)},
			{Ref: ref("example/tasks", 202), Summary: "second of the set", HTMLURL: url("tasks", 202)},
		},
		Edges: []Edge{
			{From: ref("example/outcomes", 1), To: ref("example/tasks", 200)},
			{From: ref("example/outcomes", 1), To: ref("example/tasks", 201)},
			{From: ref("example/outcomes", 1), To: ref("example/tasks", 202)},
			{From: ref("example/tasks", 201), To: ref("example/tasks", 200)},
			{From: ref("example/tasks", 202), To: ref("example/tasks", 200)},
		},
		Strokes: strokes(),
	}

	collapsed := Graph{
		Root: Node{Ref: ref("example/forest", 1), Summary: "ideation", HTMLURL: "https://example.com/forest/issues/1"},
		Members: []Node{
			{Ref: ref("example/outcomes", 21), Summary: "later outcomes work", HTMLURL: "https://example.com/outcomes/issues/21"},
			{Ref: ref("example/tasks", 12), Summary: "third tasks work", HTMLURL: "https://example.com/tasks/issues/12"},
			{Ref: ref("example/outcomes", 20), Summary: "first outcomes work", HTMLURL: "https://example.com/outcomes/issues/20"},
			{Ref: ref("example/tasks", 10), Summary: "first tasks work", HTMLURL: "https://example.com/tasks/issues/10"},
			{Ref: ref("example/tasks", 11), Summary: "second tasks work", HTMLURL: "https://example.com/tasks/issues/11"},
		},
		Edges: []Edge{
			{From: ref("example/forest", 1), To: ref("example/tasks", 10)},
			{From: ref("example/forest", 1), To: ref("example/tasks", 11)},
			{From: ref("example/forest", 1), To: ref("example/tasks", 12)},
			{From: ref("example/forest", 1), To: ref("example/outcomes", 20)},
			{From: ref("example/forest", 1), To: ref("example/outcomes", 21)},
			{From: ref("example/tasks", 12), To: ref("example/outcomes", 20)},
		},
		Strokes: strokes(),
	}

	fanOut := simpleForest()
	fanOut.Members = append(fanOut.Members, Node{
		Ref:     ref("example/outcomes", 90),
		Role:    RoleTracker,
		Summary: "second tracker",
		HTMLURL: url("outcomes", 90),
	})
	fanOut.Edges = append(fanOut.Edges,
		Edge{From: ref("example/outcomes", 63), To: ref("example/outcomes", 90)},
		Edge{From: ref("example/tasks", 182), To: ref("example/outcomes", 90)},
	)

	for _, tt := range []struct {
		name  string
		graph Graph
		cfg   Config
	}{
		{"simple", simpleForest(), wideCeiling()},
		{"closed-member", closed, wideCeiling()},
		{"sync-set", syncSet, wideCeiling()},
		{"collapsed", collapsed, Config{MaxSourceCharacters: 600, Location: aedt()}},
		{"tracker-fan", fanOut, wideCeiling()},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := Render(tt.graph, tt.cfg)
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			path := filepath.Join("testdata", tt.name+".golden")
			if *update {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatalf("creating testdata: %v", err)
				}
				if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
					t.Fatalf("writing golden: %v", err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading golden: %v", err)
			}
			if got != string(want) {
				t.Errorf("rendered source does not match %s:\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
			}
		})
	}
}

// TestRenderIsDeterministic renders one graph twice, and again with its rows
// and edges gathered in another order: the source is the graph's, never the
// caller's (§forgejo/renderer/determinism).
func TestRenderIsDeterministic(t *testing.T) {
	t.Parallel()
	cfg := wideCeiling()

	first, err := Render(simpleForest(), cfg)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	second, err := Render(simpleForest(), cfg)
	if err != nil {
		t.Fatalf("Render again: %v", err)
	}
	if first != second {
		t.Errorf("two renderings of one graph differ:\n%s\n---\n%s", first, second)
	}

	gathered := simpleForest()
	gathered.Members = append([]Node{gathered.Members[3]}, gathered.Members[:3]...)
	for i, j := 0, len(gathered.Edges)-1; i < j; i, j = i+1, j-1 {
		gathered.Edges[i], gathered.Edges[j] = gathered.Edges[j], gathered.Edges[i]
	}
	shuffled, err := Render(gathered, cfg)
	if err != nil {
		t.Fatalf("Render shuffled: %v", err)
	}
	if shuffled != first {
		t.Errorf("the order the rows were gathered in moved the source:\n%s\n---\n%s", first, shuffled)
	}
}

// TestNodeID folds the repository's non-alphanumerics to underscores and keeps
// the number, never the title (§forgejo/renderer/identifiers-and-shapes).
func TestNodeID(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		repo string
		want string
	}{
		{"s-git/s-outcomes", "i_s_outcomes_182"},
		{"s-git/maitred", "i_maitred_182"},
		{"S-Git/Mai.Tred", "i_mai_tred_182"},
		{"maitred", "i_maitred_182"},
	} {
		graph := Graph{
			Root:    Node{Ref: ref(tt.repo, 182), HTMLURL: "https://example.com/x/issues/182"},
			Strokes: strokes(),
		}
		got, err := Render(graph, wideCeiling())
		if err != nil {
			t.Fatalf("Render(%q): %v", tt.repo, err)
		}
		if !strings.Contains(got, tt.want+`{{"`) {
			t.Errorf("repository %q: want node id %q, got:\n%s", tt.repo, tt.want, got)
		}
	}
}

// TestShapeCarriesRole checks each role's shape, and the ⚑ prefix that carries
// a human task's role where the class list leaves it out.
func TestShapeCarriesRole(t *testing.T) {
	t.Parallel()
	graph := Graph{
		Root: Node{Ref: ref("example/forest", 1), HTMLURL: "https://example.com/forest/issues/1"},
		Members: []Node{
			{Ref: ref("example/forest", 2), Role: RoleTracker, HTMLURL: "https://example.com/forest/issues/2"},
			{Ref: ref("example/forest", 3), Role: RoleSync, HTMLURL: "https://example.com/forest/issues/3"},
			{Ref: ref("example/forest", 4), Role: RoleHumanTask, HTMLURL: "https://example.com/forest/issues/4"},
			{Ref: ref("example/forest", 5), HTMLURL: "https://example.com/forest/issues/5"},
		},
		Strokes: strokes(),
	}
	got, err := Render(graph, wideCeiling())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, want := range []string{
		`i_forest_1{{"`,
		`i_forest_2(["`,
		`i_forest_3["`,
		`i_forest_4["⚑ <a `,
		`i_forest_5["<a `,
		"  class i_forest_3,i_forest_4,i_forest_5 member\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("want %q in the source, got:\n%s", want, got)
		}
	}
}

// TestSummaryIsCutToFiveWords keeps a caller that hands over a whole title
// inside the label the contract states (§forgejo/renderer/labels).
func TestSummaryIsCutToFiveWords(t *testing.T) {
	t.Parallel()
	graph := Graph{
		Root: Node{
			Ref:     ref("example/forest", 1),
			Summary: "  one   two three four five   six   seven ",
			HTMLURL: "https://example.com/forest/issues/1",
		},
		Strokes: strokes(),
	}
	got, err := Render(graph, wideCeiling())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	want := ">forest#1 ·<br/>one two three four five</a>"
	if !strings.Contains(got, want) {
		t.Errorf("want %q, got:\n%s", want, got)
	}
}

// TestLabelWrapsPastThirtyCharacters: the break falls after the middle dot
// first, and the summary then wraps at spaces so no line runs past 30.
func TestLabelWrapsPastThirtyCharacters(t *testing.T) {
	t.Parallel()
	graph := Graph{
		Root: Node{
			Ref:     ref("example/forest", 1),
			Summary: "alpha beta gamma delta epsilon-omega",
			HTMLURL: "https://example.com/forest/issues/1",
		},
		Members: []Node{
			{Ref: ref("example/forest", 2), Summary: "short one", HTMLURL: "https://example.com/forest/issues/2"},
		},
		Strokes: strokes(),
	}
	got, err := Render(graph, wideCeiling())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, want := range []string{
		`forest#1 ·<br/>alpha beta gamma delta<br/>epsilon-omega`,
		`forest#2 · short one`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("want %q in the source, got:\n%s", want, got)
		}
	}
}

// TestSyncCarriesItsMembers lists the numbers of the issues that block the
// grouping issue, ascending, whatever order the graph named them in — and
// including a blocker the transitive reduction drops from the drawing: the
// label states the graph's blocker list, the reduction is an edge economy
// (§forgejo/renderer/labels, §forgejo/renderer/edge-classes).
func TestSyncCarriesItsMembers(t *testing.T) {
	t.Parallel()
	graph := Graph{
		Root: Node{Ref: ref("example/forest", 1), Summary: "ideation", HTMLURL: "https://example.com/forest/issues/1"},
		Members: []Node{
			{Ref: ref("example/forest", 9), Role: RoleSync, Summary: "land together", HTMLURL: "https://example.com/forest/issues/9"},
			{Ref: ref("example/forest", 3), Summary: "second", HTMLURL: "https://example.com/forest/issues/3"},
			{Ref: ref("example/forest", 7), Summary: "third", HTMLURL: "https://example.com/forest/issues/7"},
		},
		Edges: []Edge{
			{From: ref("example/forest", 7), To: ref("example/forest", 9)},
			{From: ref("example/forest", 3), To: ref("example/forest", 9)},
		},
		Strokes: strokes(),
	}
	got, err := Render(graph, wideCeiling())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	want := "forest#9 ·<br/>land together (#3 #7)"
	if !strings.Contains(got, want) {
		t.Errorf("want %q in the source, got:\n%s", want, got)
	}

	// #3 blocks #9 directly and also reaches it through #7, so the drawing
	// drops the direct edge — but #9's label must still carry #3.
	transitive := Graph{
		Root: Node{Ref: ref("example/forest", 1), Summary: "ideation", HTMLURL: "https://example.com/forest/issues/1"},
		Members: []Node{
			{Ref: ref("example/forest", 9), Role: RoleSync, Summary: "land together", HTMLURL: "https://example.com/forest/issues/9"},
			{Ref: ref("example/forest", 3), Summary: "second", HTMLURL: "https://example.com/forest/issues/3"},
			{Ref: ref("example/forest", 7), Summary: "third", HTMLURL: "https://example.com/forest/issues/7"},
		},
		Edges: []Edge{
			{From: ref("example/forest", 7), To: ref("example/forest", 9)},
			{From: ref("example/forest", 3), To: ref("example/forest", 9)},
			{From: ref("example/forest", 3), To: ref("example/forest", 7)},
		},
		Strokes: strokes(),
	}
	got, err = Render(transitive, wideCeiling())
	if err != nil {
		t.Fatalf("Render transitive: %v", err)
	}
	if !strings.Contains(got, "forest#9 ·<br/>land together (#3 #7)") {
		t.Errorf("a blocker the reduction dropped vanished from the sync label, want it in:\n%s", got)
	}
	if strings.Contains(got, "i_forest_3 ==> i_forest_9") {
		t.Errorf("the transitive edge into the sync issue was drawn:\n%s", got)
	}
}

// TestNoTransitiveEdge drops the direct edge a path already states
// (§forgejo/renderer/edge-classes).
func TestNoTransitiveEdge(t *testing.T) {
	t.Parallel()
	a, b, c := ref("example/forest", 2), ref("example/forest", 3), ref("example/forest", 4)
	graph := Graph{
		Root: Node{Ref: ref("example/forest", 1), HTMLURL: "https://example.com/forest/issues/1"},
		Members: []Node{
			{Ref: a, HTMLURL: "https://example.com/forest/issues/2"},
			{Ref: b, HTMLURL: "https://example.com/forest/issues/3"},
			{Ref: c, HTMLURL: "https://example.com/forest/issues/4"},
		},
		Edges: []Edge{
			{From: ref("example/forest", 1), To: a},
			{From: ref("example/forest", 1), To: b},
			{From: ref("example/forest", 1), To: c},
			{From: a, To: b},
			{From: b, To: c},
			{From: a, To: c},
		},
		Strokes: strokes(),
	}
	got, err := Render(graph, wideCeiling())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(got, "i_forest_2 ==> i_forest_4") {
		t.Errorf("the transitive edge was drawn:\n%s", got)
	}
	for _, want := range []string{"i_forest_2 ==> i_forest_3", "i_forest_3 ==> i_forest_4"} {
		if !strings.Contains(got, want) {
			t.Errorf("want %q in the source, got:\n%s", want, got)
		}
	}
}

// TestTrackerEdgesAreStatedNotDrawn: no edge at either end of a tracker
// reaches the source, and the fan they would have drawn is a note line
// (§forgejo/renderer/note-lines).
func TestTrackerEdgesAreStatedNotDrawn(t *testing.T) {
	t.Parallel()
	graph := Graph{
		Root: Node{Ref: ref("example/forest", 1), HTMLURL: "https://example.com/forest/issues/1"},
		Members: []Node{
			{Ref: ref("example/forest", 2), Role: RoleTracker, HTMLURL: "https://example.com/forest/issues/2"},
			{Ref: ref("example/forest", 3), HTMLURL: "https://example.com/forest/issues/3"},
		},
		Edges: []Edge{
			{From: ref("example/forest", 1), To: ref("example/forest", 2)},
			{From: ref("example/forest", 1), To: ref("example/forest", 3)},
			{From: ref("example/forest", 3), To: ref("example/forest", 2)},
			{From: ref("example/forest", 2), To: ref("example/forest", 3)},
		},
		Strokes: strokes(),
	}
	got, err := Render(graph, wideCeiling())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, drawn := range []string{"i_forest_2 -.-> ", "i_forest_2 ==> ", " ==> i_forest_2"} {
		if strings.Contains(got, drawn) {
			t.Errorf("an edge at the tracker (%q) was drawn:\n%s", drawn, got)
		}
	}
	for _, want := range []string{
		"%% forest forest#1 rendered by maitred",
		"%% tracker forest#2 fans to 1 issue (fan stated, not drawn)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("want %q in the source, got:\n%s", want, got)
		}
	}
}

// TestClosedMember: the mark, the strike on the anchor alone, the class swap,
// and the timestamp in the configured timezone
// (§forgejo/renderer/closed-members).
func TestClosedMember(t *testing.T) {
	t.Parallel()
	graph := Graph{
		Root: Node{Ref: ref("example/forest", 1), Summary: "ideation", HTMLURL: "https://example.com/forest/issues/1"},
		Members: []Node{
			{
				Ref:      ref("example/forest", 2),
				Summary:  "wrapped up early",
				HTMLURL:  "https://example.com/forest/issues/2",
				Closed:   true,
				ClosedAt: time.Date(2026, 11, 14, 1, 11, 0, 0, time.UTC),
			},
			{
				Ref:     ref("example/forest", 3),
				Role:    RoleHumanTask,
				Summary: "awaiting a human",
				HTMLURL: "https://example.com/forest/issues/3",
				Closed:  true,
				// The zone is the config's: 2:45 UTC is 13:45 AEDT.
				ClosedAt: time.Date(2026, 11, 14, 2, 45, 0, 0, time.UTC),
			},
		},
		Strokes: strokes(),
	}
	got, err := Render(graph, wideCeiling())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, want := range []string{
		`i_forest_2["✅ <a href='https://example.com/forest/issues/2' target='_blank' style='color:inherit;text-decoration:line-through'>forest#2 · wrapped up early</a><br/>closed 2026-11-14 12:11 AEDT"]`,
		`i_forest_3["⚑ ✅ <a href='https://example.com/forest/issues/3' target='_blank' style='color:inherit;text-decoration:line-through'>forest#3 · awaiting a human</a><br/>closed 2026-11-14 13:45 AEDT"]`,
		"  class i_forest_2,i_forest_3 closed\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("want %q in the source, got:\n%s", want, got)
		}
	}
	if strings.Count(got, "line-through") != 2 {
		t.Errorf("the strike reached beyond the two anchors:\n%s", got)
	}
}

// TestLabelEscapesFreeText keeps a label's HTML to <br/> and the anchor, the
// repository name included — graph text is escaped wherever it is drawn
// (§forgejo/renderer/labels).
func TestLabelEscapesFreeText(t *testing.T) {
	t.Parallel()
	graph := Graph{
		Root: Node{
			Ref:     ref("example/forest", 1),
			Summary: "fix & run <tests>",
			HTMLURL: "https://example.com/forest/issues/1",
		},
		Members: []Node{
			{Ref: ref("example/fo<re\"st", 2), Summary: "here", HTMLURL: "https://example.com/forest/issues/2"},
		},
		Strokes: strokes(),
	}
	got, err := Render(graph, wideCeiling())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, want := range []string{
		"forest#1 ·<br/>fix &amp; run &lt;tests&gt;",
		`>fo&lt;re&quot;st#2 · here</a>`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("want %q, got:\n%s", want, got)
		}
	}
	if strings.Contains(got, "<tests>") || strings.Contains(got, `fo<re"st`) {
		t.Errorf("graph text reached the source as markup:\n%s", got)
	}
}

// TestNoSummaryNamesNoSeparator: an issue with no summary is its reference
// alone, never a dangling middle dot.
func TestNoSummaryNamesNoSeparator(t *testing.T) {
	t.Parallel()
	graph := Graph{
		Root:    Node{Ref: ref("example/forest", 1), HTMLURL: "https://example.com/forest/issues/1"},
		Strokes: strokes(),
	}
	got, err := Render(graph, wideCeiling())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(got, `>forest#1</a>`) {
		t.Errorf("want a bare reference as the label, got:\n%s", got)
	}
}

// TestCollapseNamesItself: under the ceiling nothing is collapsed, over it one
// node per repository stands, and the note says which
// (§forgejo/renderer/collapsing).
func TestCollapseNamesItself(t *testing.T) {
	t.Parallel()
	graph := Graph{
		Root: Node{Ref: ref("example/forest", 1), Summary: "ideation", HTMLURL: "https://example.com/forest/issues/1"},
		Members: []Node{
			{Ref: ref("example/forest", 2), Summary: "here", HTMLURL: "https://example.com/forest/issues/2"},
			{Ref: ref("example/tasks", 3), Summary: "there", HTMLURL: "https://example.com/tasks/issues/3", Closed: true, ClosedAt: time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC)},
			{Ref: ref("example/tasks", 4), Summary: "also there", HTMLURL: "https://example.com/tasks/issues/4", Closed: true, ClosedAt: time.Date(2026, 1, 2, 3, 5, 0, 0, time.UTC)},
		},
		Edges: []Edge{
			{From: ref("example/forest", 1), To: ref("example/forest", 2)},
			{From: ref("example/forest", 1), To: ref("example/tasks", 3)},
			{From: ref("example/forest", 1), To: ref("example/tasks", 4)},
			{From: ref("example/tasks", 3), To: ref("example/forest", 2)},
			{From: ref("example/tasks", 4), To: ref("example/forest", 2)},
		},
		Strokes: strokes(),
	}

	full, err := Render(graph, wideCeiling())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(full, "collapsed") || strings.Contains(full, "tasks (2)") {
		t.Errorf("a source under the ceiling was collapsed:\n%s", full)
	}

	small, err := Render(graph, Config{MaxSourceCharacters: 500, Location: aedt()})
	if err != nil {
		t.Fatalf("Render under the ceiling: %v", err)
	}
	for _, want := range []string{
		`i_forest{{"forest (2)"}}`,
		`i_tasks["✅ tasks (2)"]`,
		"%% collapsed to one node per repository: the source exceeds 500 characters",
		"  i_forest -.-> i_tasks\n",
		"  class i_tasks closed\n",
	} {
		if !strings.Contains(small, want) {
			t.Errorf("want %q in the collapsed source, got:\n%s", want, small)
		}
	}
	if strings.Contains(small, "tasks (2)</a>") || strings.Contains(small, "forest (2)</a>") {
		t.Errorf("a collapsed node carried an anchor:\n%s", small)
	}
}

// TestNoZoneIsUTC: with no location configured a close stamp is rendered in UTC
// rather than the machine's zone (§forgejo/renderer/closed-members).
func TestNoZoneIsUTC(t *testing.T) {
	t.Parallel()
	graph := Graph{
		Root: Node{Ref: ref("example/forest", 1), HTMLURL: "https://example.com/forest/issues/1"},
		Members: []Node{{
			Ref:      ref("example/forest", 2),
			HTMLURL:  "https://example.com/forest/issues/2",
			Closed:   true,
			ClosedAt: time.Date(2026, 11, 14, 12, 11, 0, 0, time.UTC),
		}},
		Strokes: strokes(),
	}
	got, err := Render(graph, Config{MaxSourceCharacters: 100000})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(got, "closed 2026-11-14 12:11 UTC") {
		t.Errorf("want the stamp in UTC, got:\n%s", got)
	}
}

// manyRepos is a forest of rows in that many repositories, the shortest drawing
// of them one node per repository: a graph whose collapsed form still runs past
// a small ceiling (§forgejo/renderer/collapsing).
func manyRepos(count int) Graph {
	graph := Graph{
		Root:    Node{Ref: ref("example/forest", 1), Summary: "ideation", HTMLURL: "https://example.com/forest/issues/1"},
		Strokes: strokes(),
	}
	for i := range count {
		repo := "example/r" + strconv.Itoa(i+2)
		row := Node{Ref: ref(repo, i+2), Summary: "work", HTMLURL: "https://example.com/" + repo + "/issues/" + strconv.Itoa(i+2)}
		graph.Members = append(graph.Members, row)
		graph.Edges = append(graph.Edges, Edge{From: graph.Root.Ref, To: row.Ref})
	}
	return graph
}

func TestRenderErrors(t *testing.T) {
	t.Parallel()
	cfg := wideCeiling()
	one := Node{Ref: ref("example/forest", 1), HTMLURL: "https://example.com/forest/issues/1"}
	two := Node{Ref: ref("example/forest", 2), HTMLURL: "https://example.com/forest/issues/2"}

	for _, tt := range []struct {
		name  string
		graph Graph
		cfg   Config
		want  string
	}{
		{
			name:  "no root",
			graph: Graph{Members: []Node{two}, Strokes: strokes()},
			cfg:   cfg,
			want:  "no root",
		},
		{
			name:  "the same issue named twice",
			graph: Graph{Root: one, Members: []Node{two, two}, Strokes: strokes()},
			cfg:   cfg,
			want:  "twice",
		},
		{
			name:  "two spellings of one issue",
			graph: Graph{Root: one, Members: []Node{two, {Ref: ref("example/Forest", 2), HTMLURL: two.HTMLURL}}, Strokes: strokes()},
			cfg:   cfg,
			want:  "twice",
		},
		{
			name:  "two repositories folding together",
			graph: Graph{Root: Node{Ref: ref("example/for-est", 1), HTMLURL: two.HTMLURL}, Members: []Node{{Ref: ref("example/for.est", 1), HTMLURL: two.HTMLURL}}, Strokes: strokes()},
			cfg:   cfg,
			want:  "identifier",
		},
		{
			name:  "edge to an issue the graph holds no node for",
			graph: Graph{Root: one, Edges: []Edge{{From: one.Ref, To: ref("example/forest", 99)}}, Strokes: strokes()},
			cfg:   cfg,
			want:  "forest#99",
		},
		{
			name: "work order in a cycle",
			graph: Graph{
				Root: one,
				Members: []Node{
					two,
					{Ref: ref("example/forest", 3), HTMLURL: "https://example.com/forest/issues/3"},
				},
				Edges: []Edge{
					{From: two.Ref, To: ref("example/forest", 3)},
					{From: ref("example/forest", 3), To: two.Ref},
				},
				Strokes: strokes(),
			},
			cfg:  cfg,
			want: "cycle",
		},
		{
			name:  "closed with no close time",
			graph: Graph{Root: one, Members: []Node{{Ref: two.Ref, HTMLURL: two.HTMLURL, Closed: true}}, Strokes: strokes()},
			cfg:   cfg,
			want:  "close time",
		},
		{
			name:  "a URL that cannot be quoted",
			graph: Graph{Root: Node{Ref: one.Ref, HTMLURL: "https://example.com/a'b"}, Strokes: strokes()},
			cfg:   cfg,
			want:  "URL",
		},
		{
			name:  "a URL of a scheme the anchor will not link",
			graph: Graph{Root: Node{Ref: one.Ref, HTMLURL: "javascript:alert(document.domain)"}, Strokes: strokes()},
			cfg:   cfg,
			want:  "URL",
		},
		{
			name:  "a data URL",
			graph: Graph{Root: Node{Ref: one.Ref, HTMLURL: "data:text/html;base64,PHNjcmlwdD4="}, Strokes: strokes()},
			cfg:   cfg,
			want:  "URL",
		},
		{
			name:  "a repository name carrying whitespace",
			graph: Graph{Root: Node{Ref: ref("example/fo\nrest", 1), HTMLURL: "https://example.com/forest/issues/1"}, Strokes: strokes()},
			cfg:   cfg,
			want:  "cannot be drawn",
		},
		{
			name:  "too long even collapsed to one node per repository",
			graph: manyRepos(60),
			cfg:   Config{MaxSourceCharacters: 400, Location: aedt()},
			want:  "even collapsed",
		},
		{
			name: "two repositories the collapsed view cannot tell apart",
			graph: Graph{
				Root: Node{Ref: ref("example/a-b", 1), Summary: "ideation", HTMLURL: "https://example.com/a-b/issues/1"},
				Members: []Node{
					{Ref: ref("other/a_b", 2), Summary: "work", HTMLURL: "https://example.com/a_b/issues/2"},
					{Ref: ref("other/zz", 3), Summary: "work", HTMLURL: "https://example.com/zz/issues/3"},
				},
				Edges: []Edge{
					{From: ref("example/a-b", 1), To: ref("other/a_b", 2)},
					{From: ref("other/a_b", 2), To: ref("other/zz", 3)},
				},
				Strokes: strokes(),
			},
			cfg:  Config{MaxSourceCharacters: 400, Location: aedt()},
			want: "fold to one identifier",
		},
		{
			name:  "an invented colour",
			graph: Graph{Root: one, Strokes: Strokes{Root: "#fff", Tracker: "#1d76db", Member: "#6b7a8c", Closed: "#3fb950"}},
			cfg:   cfg,
			want:  "stroke",
		},
		{
			name:  "no ceiling",
			graph: simpleForest(),
			cfg:   Config{MaxSourceCharacters: 0, Location: aedt()},
			want:  "ceiling",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := Render(tt.graph, tt.cfg)
			if err == nil {
				t.Fatalf("Render succeeded, want %q:\n%s", tt.want, got)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q, want it to name %q", err, tt.want)
			}
		})
	}
}
