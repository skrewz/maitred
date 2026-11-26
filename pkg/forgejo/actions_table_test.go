package forgejo

// The actions table's Inputs column is the declaration table rendered into
// the specification: each row states, as code spans, exactly the input names
// that action's declaration names (§forgejo/decisions/actions). This guard
// reads the column from `specification/forgejo/decisions.md` and compares it
// with the declarations the engine dispatches through, so the paradigm the
// section states — an action is a predicate over a declared input set — is
// true of the table a reader arrives at first. It is the third of the three
// checks §forgejo/decisions/actions names; the other two, over the enum and
// over a predicate's reads, live in declaration_check_test.go
// (s-git/maitred#263, s-git/maitred#264).
//
// The guard reads one section and nothing else: a table cell holding a name
// the declaration does not name, a name the table omits, and an action the
// declarations carry with no row at all are each a finding. A file the guard
// cannot read, a section it cannot find and a table it reads no row from are
// errors rather than passes — a guard that succeeds by having read nothing is
// no guard.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// actionsTablePath is the specification file holding the actions table.
var actionsTablePath = filepath.Join("..", "..", "specification", "forgejo", "decisions.md")

// tableRow is a markdown table row's cells, trimmed of their padding.
func tableRow(line string) []string {
	cells := strings.Split(strings.TrimSpace(line), "|")
	if len(cells) >= 2 {
		cells = cells[1 : len(cells)-1]
	}
	for i, cell := range cells {
		cells[i] = strings.TrimSpace(cell)
	}
	return cells
}

// codeSpans is a table cell's code spans, in the order they are written.
var codeSpans = regexp.MustCompile("`([^`]+)`")

// specActionInputs is the actions table as the specification writes it: each
// action to the input names its row states. Reading starts at the
// `## Actions` heading and ends with the table beneath it, so prose elsewhere
// in the file states no row here.
func specActionInputs(markdown string) (map[Action][]string, error) {
	rows := map[Action][]string{}
	inSection := false
	inTable := false
	for _, line := range strings.Split(markdown, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "## Actions" {
			inSection = true
			continue
		}
		if inSection && strings.HasPrefix(trimmed, "## ") {
			inTable = false
			inSection = false
			break
		}
		if !inSection {
			continue
		}
		if !strings.HasPrefix(trimmed, "|") {
			if inTable {
				break
			}
			continue
		}
		cells := tableRow(trimmed)
		if len(cells) < 3 || !strings.HasPrefix(cells[0], "`") {
			continue // the header and its separator row name no action
		}
		name := strings.Trim(cells[0], "`")
		spanned := codeSpans.FindAllStringSubmatch(cells[len(cells)-1], -1)
		inputs := make([]string, 0, len(spanned))
		for _, span := range spanned {
			inputs = append(inputs, span[1])
		}
		rows[Action(name)] = inputs
		inTable = true
	}
	if !inSection {
		return nil, fmt.Errorf("the actions table was not found: no `## Actions` heading in %s", actionsTablePath)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("the actions table in %s holds no action row", actionsTablePath)
	}
	return rows, nil
}

// checkActionsTable compares the specification's rows with the declarations
// (§forgejo/decisions/actions): a row must name each input its declaration
// names, and each action must be named by exactly one row.
func checkActionsTable(rows map[Action][]string, decls []declaredInput) []string {
	declared := map[Action][]string{}
	for _, d := range decls {
		named := append([]string(nil), d.inputs...)
		sort.Strings(named)
		declared[d.action] = named
	}
	var problems []string
	for _, a := range sortedActions(rows) {
		want, ok := declared[a]
		if !ok {
			problems = append(problems, fmt.Sprintf("the actions table has a row for %q, which carries no declaration", a))
			continue
		}
		got := append([]string(nil), rows[a]...)
		sort.Strings(got)
		for _, name := range want {
			if !contains(got, name) {
				problems = append(problems, fmt.Sprintf("the %q row of the actions table omits %s, which its declaration names", a, name))
			}
		}
		for _, name := range got {
			if !contains(want, name) {
				problems = append(problems, fmt.Sprintf("the %q row of the actions table names %s, which its declaration does not", a, name))
			}
		}
	}
	for _, d := range decls {
		if _, ok := rows[d.action]; !ok {
			problems = append(problems, fmt.Sprintf("the %s declaration names its inputs, and the actions table has no row for them", d.action))
		}
	}
	sort.Strings(problems)
	return problems
}

func sortedActions(rows map[Action][]string) []Action {
	out := make([]Action, 0, len(rows))
	for a := range rows {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func contains(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

// TestActionsTable_InputsColumnMatchesTheDeclarations keeps the specification's
// Inputs column in lockstep with the declarations the decision path consults
// (§forgejo/decisions/actions, s-git/maitred#264).
func TestActionsTable_InputsColumnMatchesTheDeclarations(t *testing.T) {
	markdown, err := os.ReadFile(actionsTablePath)
	if err != nil {
		t.Fatalf("reading the actions table: %v", err)
	}
	rows, err := specActionInputs(string(markdown))
	if err != nil {
		t.Fatalf("reading the actions table: %v", err)
	}
	for _, problem := range checkActionsTable(rows, declarationsUnderTest()) {
		t.Error(problem)
	}
}

// TestActionsTable_FlagsAColumnTheDeclarationsContradict proves the lockstep
// guard bites: a row missing one declared input, one holding an input no
// declaration names, an action the table rows without a declaration, and a
// declaration with no row are each refused.
func TestActionsTable_FlagsAColumnTheDeclarationsContradict(t *testing.T) {
	rows := map[Action][]string{
		"present": {"issue.state"},
		"extra":   {"issue.state", "issue.body"},
		"stray":   {"issue.state"},
	}
	problems := checkActionsTable(rows, []declaredInput{
		{action: "present", inputs: []string{"issue.state", "issue.body"}},
		{action: "extra", inputs: []string{"issue.state"}},
		{action: "absent", inputs: []string{"issue.state"}},
	})
	if len(problems) != 4 {
		t.Fatalf("problems = %v, want four findings", problems)
	}
	for _, want := range []string{
		`omits issue.body`,
		`names issue.body, which its declaration does not`,
		`has a row for "stray", which carries no declaration`,
		`no row for them`,
	} {
		found := false
		for _, p := range problems {
			if strings.Contains(p, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no finding contains %q; problems = %v", want, problems)
		}
	}
}

// TestActionsTable_RefusesATableItCannotRead keeps the guard honest about its
// reading: a section with no rows is an error, never a pass.
func TestActionsTable_RefusesATableItCannotRead(t *testing.T) {
	if _, err := specActionInputs("# Forgejo decisions\n\n## Something else\n\nNo table here.\n"); err == nil {
		t.Error("a specification with no actions section read as an empty table")
	}
	if _, err := specActionInputs("# Forgejo decisions\n\n## Actions\n\nProse, and no row.\n\n## Next\n"); err == nil {
		t.Error("an actions section with no table read as an empty table")
	}
}
