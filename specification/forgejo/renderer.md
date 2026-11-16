# Forest renderer

A forest — an ideation root, the rows it blocks, and the blocking edges
between them — has one drawing. The renderer turns that graph into the
`flowchart TD` source that draws it, so the drawing follows rules rather than
an agent's judgement (§forgejo/renderer/the-contract). The renderer reads no
forge and caches nothing: it is a function from a graph to a string, and the
caller that gathers the graph is where the freshness rules below are honoured.

The rendered source is the payload of a tracker's current-state block;
splicing it into the fence pair, writing the block, and gathering the graph's
facts from the forge belong to the caller, not here.

## The contract

- **Determinism.** The same graph renders to a byte-identical source, so a
  diff in the output means the graph moved. Nothing in the renderer depends on
  the wall clock, on map iteration order, or on the order the caller happened
  to gather rows in (§forgejo/renderer/determinism).
- **A drawing, not a judgement.** Every shape, prefix, colour and edge is
  decided by the rules below. The renderer never invents an identifier, a
  colour or an edge, and never draws what the graph does not state.
- **The vocabulary is the graph's.** An identifier is derived from a repository
  and a number, never from a title (§forgejo/renderer/identifiers-and-shapes);
  a colour is read from a label (§forgejo/renderer/classes-and-strokes); an
  edge exists because a blocking edge exists (§forgejo/renderer/edge-classes).

## The source

The source is, in this order:

1. `flowchart TD`,
2. the note lines (§forgejo/renderer/note-lines),
3. one declaration per node, in **node order**: the root, then the trackers,
   then the members — each group by repository name, then by number,
4. the gate edges, in the same node order as their targets,
5. the work-order edges, sorted by source, then target,
6. the `classDef` declarations, in the order `root`, `tracker`, `member`,
   `closed`,
7. the `class` statements, in that same order, each naming its members in node
   order.

Every line but a note line is indented two spaces, and the source ends with a
single newline so a caller may splice it into a fence pair verbatim. No
`subgraph`, no `click` and no `/* */` comment appears anywhere, and a label
carries no HTML beyond `<br/>` and the anchor below.

## Identifiers and shapes

A node is an issue in the forest. Its identifier is `i_<name>_<number>`, where
`<name>` is the repository's name — the last `/`-separated element of its
`owner/repo`, lowercased — with every non-alphanumeric character folded to `_`,
and `<number>` is the issue's number. An identifier is never derived from a
title and never invented: an issue's two spellings share one identifier, and a
graph that names an issue twice — or names two issues whose identifiers fold
together — is an error rather than a silent overwrite.

The shape carries the role:

| Role | Shape |
|---|---|
| the ideation root | hexagon `{{"…"}}` |
| an outcome tracker | stadium `(["…"])` |
| a work item | rectangle `["…"]` |
| a sync issue | rectangle `["…"]`, its label carrying its members' numbers |
| a human task | rectangle `["…"]` prefixed `⚑` |

The role prefix sits outside the anchor, before the label text; where the node
is also closed the `✅` mark follows the role prefix
(§forgejo/renderer/closed-members).

## Labels

A label is the issue's reference and a summary of it:

    <repo>#<number> · <summary>

`<repo>` is the repository's name — the bare last element of its `owner/repo`, not
`owner/repo` — and `<summary>` is the summary the graph carries for the node,
cut to its first five words and collapsed to single spaces, so a caller that
passes a whole title still gets a bounded label. A sync issue carries the
numbers of the members that block it after the summary, in ascending order, as
` (#<m1> #<m2> …)`.

The summary and the repository half of the reference are the graph's text,
so they are escaped: `&`, `<`, `>` and `"` become their HTML entity
references. A label carries no other markup. A repository name carrying
whitespace is an error: no source line — label or note — could carry it.
Where the graph carries no summary for a node, its label is its reference
alone: the separator never stands without a summary to separate.

The summary text is wrapped in an anchor carrying `href` set to the issue's web
URL, `target='_blank'` and `style='color:inherit'`, in that order, quoted with
single quotes inside the double-quoted label. The URL is the graph's, never
composed from a host the renderer knows, and its scheme must be `http` or
`https` — the navigation the anchor exists for; a `javascript:` or `data:` URL
quotes cleanly yet turns the label into a clickable script. A URL that cannot
sit inside a single-quoted attribute, or whose scheme is neither, is an error.

The label is wrapped past about 30 characters: when the wrapped text would
exceed 30 characters the break falls after the `·` separator, and the remaining
text then breaks at spaces so no line runs past 30 characters. The `<br/>` this
produces sits inside the anchor.

## Edge classes

The graph states blocking edges: `from` blocks `to`. Each edge's source role
picks its class, and one edge is one class:

| Source role | Drawn as |
|---|---|
| the root | a **gate** edge `-.->`, declared before every work-order edge |
| a member | a **work-order** edge `==>`, declared after the gate edges |

The one exception is the tracker. The root's membership edge into a tracker
is drawn — as a gate edge, like every other edge from the root, because the
forest hangs from its root — and every other blocking edge with a tracker at
either end is **never drawn**, the fan it would have drawn being stated in a
note line instead (§forgejo/renderer/note-lines). So the forest hangs visibly
from its root, its work order reads as a chain, and a tracker — which every
member waits on and which waits on nothing it must order — stays a single
node rather than a hub spraying lines across the chart.

Work-order edges carry no transitive edge: where a path of at least two
work-order edges already reaches the target, the direct edge is redundant and
is dropped. A work-order cycle has no reduction and is an error, as is an edge
naming an issue the graph holds no node for. An edge from an issue to itself
states nothing and is drawn nowhere. Edges that fold to the same pair are drawn
once.

## Classes and strokes

Four classes are declared, in the order `root`, `tracker`, `member`, `closed`:

```
classDef root stroke:#<hex>,stroke-width:2px
classDef tracker stroke:#<hex>,stroke-width:2px
classDef member stroke:#<hex>
classDef closed stroke:#<hex>,stroke-dasharray:4 3,fill:#<hex>,fill-opacity:0.25
```

Four classes, four hexes: the `closed` dim's `fill` carries the same hex as
its own `stroke`, so no fifth hex — which no role label could have supplied
— travels in with the graph.

A node carries exactly one class: `root` for the root, `tracker` for a
tracker, `member` for every other member — a human task included, whose role
the `⚑` prefix carries — and `closed` for a closed member.

No `stroke` hex is invented by the renderer: each is read **fresh at each
render** from the colour of the label that names the role on a forest issue,
and travels into the graph as the graph's own data. The renderer reads no forge
and caches no hex, which is what makes a label recoloured in the forge show in
the next chart. Beyond the `closed` dim no `fill` is set, no `color` is set and
no `stroke-width` above `2px` is set.

## Closed members

A node that is closed carries:

- the `✅` mark before its label text,
- `text-decoration:line-through` on the anchor's inline style, after
  `color:inherit`, and never on the node, so the strike crosses the words and
  not the box, and
- a `<br/>closed <YYYY-MM-DD HH:MM> <TZ>` line after the anchor, rendered in
  the configured timezone from the issue's close time.

The `closed` class itself goes to a closed **member**: the root and a tracker
keep the classes that name their roles, since a node carries one class. A
closed node whose close time the graph does not carry is an error: a chart that
says "closed" without a time is a chart that guessed.

## Note lines

Every note is a `%%` line at column zero, so it states rather than draws:

1. always, first: `%% forest <repo>#<number> rendered by maitred`, naming the
   root,
2. one per tracker sharing a blocking edge with at least one row other than the
   root, in tracker order: `%% tracker <repo>#<number> fans to <K> issues (fan
   stated, not drawn)`, where `<K>` counts the distinct rows on those undrawn
   edges, whatever side of the edge each of them is on,
3. one when the source was collapsed (§forgejo/renderer/collapsing).

## Collapsing

The renderer renders the whole forest and, only when that source would exceed
the configured ceiling of characters, renders the forest again collapsed to one
node per repository. A collapsed node's identifier is `i_<name>`, its label is
`<repo> (N)` — the bare name and the count of forest issues in that repository
— and it carries no anchor, because no single issue owns it. The root's
repository is declared first and the rest follow by name. Its shape and
class follow the roles it holds: the root's shape wins over a tracker's, a
tracker's over a member's. An edge between two issues in one repository is not
drawn, edges between two repositories are drawn once apiece in their own
classes, and the collapse is named in a note line. A collapsed node carries the
`✅` mark and the `closed` class when every issue it stands for is closed, and
nothing of closedness otherwise: which of the issues it stands for is closed is
the graph's, not the drawing's.

Raising the ceiling is a config decision; inventing a second, shorter drawing
for large forests is not — one graph has one drawing at a given ceiling.

Collapsing is the remedy, not a second and unbounded drawing: where even one
node per repository exceeds the ceiling, that is an error, so a caller that
reads the ceiling as the limit on what it may write is never handed source past
it.

## Determinism

The renderer's output depends on the graph, the ceiling and the timezone, and
on nothing else: rendering the same graph twice yields byte-identical source,
and a diff between two renderings means the graph, the ceiling or a close time
moved. A caller deciding whether a chart needs rewriting therefore compares
rendered sources, not graph hashes — the digest of
§forgejo/decisions/actionable-state-digest answers the separate question of
whether the graph moved at all.

Each of these is an error, named, and none of them yields partial source: a
graph with no root, a node that names no issue, a repository name carrying
whitespace, two issues folding to one identifier, an edge naming an issue the
graph holds no node for, a work-order cycle, a closed node with no close time,
a URL that cannot be quoted or whose scheme is neither http nor https, a
stroke hex that is not a six-digit `#rrggbb`, a ceiling of zero or less which
no graph can fit, or a graph whose collapsed form still exceeds that ceiling.
