package renderer

import (
	"fmt"
	"strings"
)

// bareName is the repository's name: the last /-separated element of the
// forge's "owner/repo", so an owner never reaches the drawing
// (§forgejo/renderer/labels).
func bareName(repository string) string {
	trimmed := strings.TrimRight(repository, "/")
	if i := strings.LastIndex(trimmed, "/"); i >= 0 {
		return trimmed[i+1:]
	}
	return trimmed
}

// foldName is a repository name as an identifier carries it: lowercased, with
// every non-alphanumeric character folded to an underscore
// (§forgejo/renderer/identifiers-and-shapes).
func foldName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('_')
	}
	return b.String()
}

// nodeID is a node's identifier: i_<repo>_<number>, never derived from a title
// (§forgejo/renderer/identifiers-and-shapes).
func nodeID(ref Ref) string {
	return "i_" + foldName(bareName(ref.Repository)) + "_" + fmt.Sprint(ref.Number)
}

// repoID is a collapsed node's identifier: one per repository
// (§forgejo/renderer/collapsing).
func repoID(repository string) string {
	return "i_" + foldName(bareName(repository))
}

// lessRef orders rows by repository name, then number — the name
// case-insensitively, the way the forge addresses a repository
// (§forgejo/renderer/the-source).
func lessRef(a, b Ref) bool {
	x, y := strings.ToLower(bareName(a.Repository)), strings.ToLower(bareName(b.Repository))
	if x != y {
		return x < y
	}
	return a.Number < b.Number
}

// quotable reports whether a URL can sit inside a single-quoted attribute:
// non-empty, with nothing that would close the attribute or the label
// (§forgejo/renderer/labels).
func quotable(url string) bool {
	if url == "" {
		return false
	}
	return !strings.ContainsAny(url, "'\"<> \t\n\r\v\f")
}

// checkStrokes refuses a class the renderer coloured for itself: every hex
// travels in with the graph, read from a label's colour, and an absent or
// malformed one means the caller never read it
// (§forgejo/renderer/classes-and-strokes).
func checkStrokes(s Strokes) error {
	for _, stroke := range []struct {
		class string
		hex   string
	}{
		{"root", s.Root},
		{"tracker", s.Tracker},
		{"member", s.Member},
		{"closed", s.Closed},
	} {
		if !isHex(stroke.hex) {
			return fmt.Errorf("renderer: the %s stroke %q is not a #rrggbb read from a label", stroke.class, stroke.hex)
		}
	}
	return nil
}

// isHex reports whether a stroke is a six-digit colour the forge could have
// reported.
func isHex(s string) bool {
	if len(s) != 7 || s[0] != '#' {
		return false
	}
	for _, r := range s[1:] {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

// escapeText keeps a label's HTML to <br/> and the anchor: the summary is the
// source's only free text, so its markup becomes entity references
// (§forgejo/renderer/labels).
func escapeText(text string) string {
	text = strings.ReplaceAll(text, "&", "&amp;")
	text = strings.ReplaceAll(text, "<", "&lt;")
	text = strings.ReplaceAll(text, ">", "&gt;")
	return strings.ReplaceAll(text, `"`, "&quot;")
}

// sanitizeSummary cuts the label's free text to five words of single-spaced,
// escaped prose (§forgejo/renderer/labels).
func sanitizeSummary(summary string) string {
	words := strings.Fields(summary)
	if len(words) > summaryWordCap {
		words = words[:summaryWordCap]
	}
	return escapeText(strings.Join(words, " "))
}

// wrapLabel breaks a label's text past about 30 characters: the first break
// falls after the separator, the rest fall at spaces so no line runs past the
// budget (§forgejo/renderer/labels).
func wrapLabel(text string) string {
	if len([]rune(text)) <= labelWrapBudget {
		return text
	}
	head, rest, found := strings.Cut(text, separator)
	if !found {
		return strings.Join(greedyWrap(strings.Fields(text)), "<br/>")
	}
	lines := append([]string{head + strings.TrimSuffix(separator, " ")}, greedyWrap(strings.Fields(rest))...)
	return strings.Join(lines, "<br/>")
}

// greedyWrap fills lines to the budget, breaking at spaces
// (§forgejo/renderer/labels).
func greedyWrap(words []string) []string {
	var lines []string
	line := ""
	for _, word := range words {
		switch {
		case line == "":
			line = word
		case len([]rune(line))+1+len([]rune(word)) <= labelWrapBudget:
			line += " " + word
		default:
			lines = append(lines, line)
			line = word
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}
