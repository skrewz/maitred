package forgejo

// Two guards keep the declaration table (§forgejo/decisions/actions) the
// engine's rule rather than a description of it (§forgejo/decisions/inputs,
// s-git/maitred#263).
//
//  1. Every action the `Action` enum names carries a declaration, and an
//     action with none cannot dispatch: `dispatch` asks the declaration, so
//     the refusal names the action it has no rule for. The enum is read from
//     the source that declares it rather than from a list beside it, which is
//     what makes this guard meet a silent config regression the way the
//     retired-prompt-name refusals meet a retired name.
//  2. Every input an action's predicate reads is a fact that action's
//     declaration names. The guard walks the state and event reads each
//     predicate writes and matches them against the declared set through the
//     vocabulary of §forgejo/decisions/inputs; a read that set does not name
//     is reported with its file, its field and its action.
//
// Two rules of the walk decide what the guard does not say:
//
//   - A read answers to the predicate that writes it. The digest derivation a
//     declaration binds is that declaration's own implementation, so the
//     fold's reads are never a predicate's reads and no call site needs
//     excusing anywhere below. A row of the decision function reads for more
//     than one action, so its reads answer to no single declaration; a helper
//     shared by several predicates is the same case in miniature, and the
//     state field handed to it — `openBlockers(s.Blockers)` — is still
//     recorded as the predicate's own read.
//   - A predicate never reads an activity timestamp. The fold states the
//     timestamp so the digest a row dispatches on moves; a predicate that
//     turned on the cursor itself would decide on the clock rather than on the
//     state (§forgejo/reconciliation/the-state-is-the-watermark). The rule is
//     read off the field's type, so it catches the next timestamp the model
//     gains as surely as it catches `issue.UpdatedAt`.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// inputPaths states the vocabulary of §forgejo/decisions/inputs as the walks
// it is read through: each key is a hop path out of an input carrier — the
// `State` and `Event` fields that section enumerates — and each value the
// names reaching that far states a fact for. A read no walk reaches reads
// something outside the vocabulary, which is the hole the guard exists to
// close.
var inputPaths = map[string][]string{
	"State.Issue":    {"issue.state", "issue.labels", "issue.sync label", "issue.body", "issue.updated_at"},
	"State.PR":       {"PR.state", "PR.mergeable", "PR.head sha", "PR.labels", "PR.author"},
	"State.Blockers": {"blockers.states"},
	// A connected pull request is one of the issue's blockers, so it stands
	// where the issue does (§forgejo/webhook/re-fetch).
	"State.ConnectedPRs": {"connected PRs"},
	"State.Reviews":      {"reviews"},
	// The lenses and the two identities are configuration the engine passes
	// verbatim, not fetched state (§forgejo/decisions/inputs).
	"State.ReviewLenses":                            {"config.review lenses"},
	"State.ImplementerIdentity":                     {"config.implementer identity"},
	"State.ForestGate":                              {"forest digest gate"},
	"State.Blocked":                                 {"blocked downstream edges"},
	"State.ConnectedIssues":                         {"connected issues"},
	"State.ConnectedIssues.ConnectedIssue.Blockers": {"connected issues' blockers"},
	"State.ConnectedIssues.ConnectedIssue.SyncSets": {"connected issues' sync sets"},
	"State.SyncSets":                                {"sync sets"},
	// A landing set's member is the row the set waits on, its delivery and
	// that delivery's reviews (§forgejo/decisions/sync-set).
	"State.SyncMembers":                    {"sync members"},
	"State.SyncMembers.SyncMember.Issue":   {"sync members"},
	"State.SyncMembers.SyncMember.PR":      {"members' PRs"},
	"State.SyncMembers.SyncMember.Reviews": {"members' PR reviews"},
	"Event.Type":                           {"event.type"},
	"Event.Kind":                           {"event.kind"},
	"Event.Repo":                           {"event.repo"},
	"Event.Number":                         {"event.number"},
	"Event.Sender":                         {"event.sender"},
}

// inputFields states which vocabulary name each fact of a state-model row is
// read for. The same issue read as the row under decision, as a blocker and as
// a landing-set member is named by each of those, and a read is declared only
// when the walk and the fact agree on one name. A fact that stands with no
// names is one no declaration may name — authorship gates nothing
// (§forgejo/decisions/loop-prevention) — and a field absent here is a field
// the vocabulary says nothing about, which is a refusal.
var inputFields = map[string]map[string][]string{
	"Issue": {
		"State":  {"issue.state", "blockers.states", "connected issues", "connected issues' blockers", "connected issues' sync sets", "sync sets", "sync members", "blocked downstream edges"},
		"Labels": {"issue.labels", "issue.sync label"},
		"Body":   {"issue.body"},
		// The fold states the activity timestamp so a declared digest moves;
		// it is named here and still refused in a predicate by the timestamp
		// rule below.
		"UpdatedAt":    {"issue.updated_at"},
		"ClosedAt":     nil,
		"Author":       nil,
		"Title":        nil,
		"LabelColours": nil,
	},
	"PullRequest": {
		"State":     {"PR.state", "connected PRs", "members' PRs"},
		"Merged":    {"PR.state"},
		"Mergeable": {"PR.mergeable", "members' PRs"},
		"HeadSHA":   {"PR.head sha", "members' PRs"},
		"Labels":    {"PR.labels"},
		"Author":    {"PR.author"},
		"Body":      nil,
		"Title":     nil,
		"UpdatedAt": nil,
	},
	"Review": {
		"Event":       {"reviews"},
		"Author":      {"reviews"},
		"CommitID":    {"reviews"},
		"Dismissed":   {"reviews"},
		"SubmittedAt": nil,
	},
}

// rowIdentity names each row's identity: which row a reason line names, and
// what kind of row it is. An identity states no fact a row decides on, so the
// vocabulary names none and the guard passes these reads by.
var rowIdentity = map[string][]string{
	"Issue":       {"Number", "Repository", "HTMLURL", "IsPull"},
	"PullRequest": {"Number", "HTMLURL"},
}

// sourceTree is the package the two guards read: its files, the fields each of
// its structs carries, and the functions it declares.
type sourceTree struct {
	fset    *token.FileSet
	files   []*ast.File
	structs map[string]map[string]string
	funcs   map[string]*ast.FuncDecl
}

// loadSourceTree parses the package in dir. A directory holding no Go source,
// or holding source that will not parse, is an error and never a skip: a guard
// that passes because it read nothing is the guard this issue exists to
// prevent.
func loadSourceTree(dir string) (*sourceTree, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return nil, fmt.Errorf("declaration check: listing the source of %s: %w", dir, err)
	}
	src := &sourceTree{fset: token.NewFileSet(), structs: map[string]map[string]string{}, funcs: map[string]*ast.FuncDecl{}}
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(src.fset, p, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, fmt.Errorf("declaration check: reading %s: %w", p, err)
		}
		src.files = append(src.files, file)
	}
	if len(src.files) == 0 {
		return nil, fmt.Errorf("declaration check: %s holds no Go source to check", dir)
	}
	for _, file := range src.files {
		for _, d := range file.Decls {
			switch decl := d.(type) {
			case *ast.GenDecl:
				for _, spec := range decl.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok {
						continue
					}
					st, ok := ts.Type.(*ast.StructType)
					if !ok {
						continue
					}
					fields := src.fieldsOf(st)
					if known, ok := src.structs[ts.Name.Name]; ok {
						for name, typ := range known {
							fields[name] = typ
						}
					}
					src.structs[ts.Name.Name] = fields
				}
			case *ast.FuncDecl:
				src.funcs[decl.Name.Name] = decl
			}
		}
	}
	return src, nil
}

// fieldsOf is one struct's fields as the source writes their types.
func (s *sourceTree) fieldsOf(st *ast.StructType) map[string]string {
	fields := map[string]string{}
	for _, f := range st.Fields.List {
		t := s.text(f.Type)
		for _, n := range f.Names {
			fields[n.Name] = t
		}
	}
	return fields
}

// text renders an expression as it is written, which is how a field's type is
// read here: as the source states it.
func (s *sourceTree) text(e ast.Expr) string {
	var b strings.Builder
	if err := printer.Fprint(&b, s.fset, e); err != nil {
		return ""
	}
	return b.String()
}

// actionEnum is the set the `Action` type names, read from the source that
// declares it (§forgejo/decisions/actions). An enum that names nothing is an
// error: the guard would otherwise pass on the strength of having read nothing.
func (s *sourceTree) actionEnum() ([]Action, error) {
	found := map[string]bool{}
	var actions []Action
	for _, file := range s.files {
		for _, d := range file.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok || vs.Type == nil || s.text(vs.Type) != "Action" {
					continue
				}
				for _, v := range vs.Values {
					lit, ok := v.(*ast.BasicLit)
					if !ok {
						return nil, fmt.Errorf("declaration check: an Action constant is not written as a literal, so the enum cannot be read")
					}
					name := strings.Trim(lit.Value, "`\"'")
					if found[name] {
						continue
					}
					found[name] = true
					actions = append(actions, Action(name))
				}
			}
		}
	}
	if len(actions) == 0 {
		return nil, fmt.Errorf("declaration check: the Action enum names no action, so the guard read nothing")
	}
	sort.Slice(actions, func(i, j int) bool { return actions[i] < actions[j] })
	return actions, nil
}

// binding is what one name in a predicate holds: its type, and the walk out of
// an input carrier it was read from. A value no walk reaches — one a call
// returned — is untracked: the read it stands for is the call's arguments,
// which the walk records as it passes them.
type binding struct {
	typ     string
	hops    []string
	tracked bool
}

// read is one field read, in the order its hops are written.
type read struct {
	file  string
	line  int
	hops  []string
	owner string
	field string
	ftype string
}

func (r read) String() string { return r.owner + "." + r.field }

// readsOf is every read of an input carrier written in one function, in source
// order. Names bound to a walk carry that walk with them, which is what tells
// the row under decision apart from the same issue read as a blocker.
func (s *sourceTree) readsOf(fn *ast.FuncDecl) []read {
	env := map[string]binding{}
	if fn.Type.Params != nil {
		for _, f := range fn.Type.Params.List {
			for _, n := range f.Names {
				if n.Name != "_" {
					env[n.Name] = binding{typ: s.text(f.Type), tracked: true}
				}
			}
		}
	}
	var out []read
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch at := n.(type) {
		case *ast.AssignStmt:
			if at.Tok == token.DEFINE || at.Tok == token.ASSIGN {
				for i, lhs := range at.Lhs {
					id, ok := lhs.(*ast.Ident)
					if !ok || id.Name == "_" || i >= len(at.Rhs) {
						continue
					}
					env[id.Name] = s.resolve(at.Rhs[i], env)
				}
			}
		case *ast.RangeStmt:
			elem := s.resolve(at.X, env)
			bind := func(e ast.Expr) {
				if id, ok := e.(*ast.Ident); ok && id.Name != "_" {
					env[id.Name] = binding{typ: elem.typ, hops: elem.hops, tracked: elem.tracked}
				}
			}
			if at.Value != nil {
				bind(at.Value)
			} else {
				bind(at.Key)
			}
		case *ast.SelectorExpr:
			if r, ok := s.readOf(at, env); ok {
				out = append(out, r)
				return false
			}
			// A selector that is not itself an input read — a method called
			// on a field of the state — still carries the read its base
			// writes, so the walk continues into it.
		}
		return true
	})
	return out
}

// resolve is an expression's type and the walk it was read out of.
func (s *sourceTree) resolve(e ast.Expr, env map[string]binding) binding {
	switch x := e.(type) {
	case *ast.Ident:
		b, ok := env[x.Name]
		if !ok {
			return binding{}
		}
		return b
	case *ast.ParenExpr:
		return s.resolve(x.X, env)
	case *ast.UnaryExpr:
		return s.resolve(x.X, env)
	case *ast.StarExpr:
		b := s.resolve(x.X, env)
		return binding{typ: strings.TrimPrefix(b.typ, "*"), hops: b.hops, tracked: b.tracked}
	case *ast.IndexExpr:
		b := s.resolve(x.X, env)
		return binding{typ: elemType(b.typ), hops: b.hops, tracked: b.tracked}
	case *ast.SelectorExpr:
		b := s.resolve(x.X, env)
		if !b.tracked || b.typ == "" {
			return binding{}
		}
		owner := strings.TrimPrefix(b.typ, "*")
		field, ok := s.structs[owner][x.Sel.Name]
		if !ok {
			return binding{}
		}
		return binding{typ: field, hops: append(append([]string(nil), b.hops...), owner+"."+x.Sel.Name), tracked: true}
	}
	return binding{}
}

// readOf is one selector's read, when the selector reaches an input carrier.
func (s *sourceTree) readOf(sel *ast.SelectorExpr, env map[string]binding) (read, bool) {
	b := s.resolve(sel.X, env)
	if !b.tracked {
		return read{}, false
	}
	owner := strings.TrimPrefix(b.typ, "*")
	ftype, ok := s.structs[owner][sel.Sel.Name]
	if !ok {
		return read{}, false
	}
	pos := s.fset.Position(sel.Pos())
	return read{
		file:  filepath.Base(pos.Filename),
		line:  pos.Line,
		hops:  append(append([]string(nil), b.hops...), owner+"."+sel.Sel.Name),
		owner: owner,
		field: sel.Sel.Name,
		ftype: ftype,
	}, true
}

// elemType is the element type of a slice or map as the source states it.
func elemType(t string) string {
	switch {
	case strings.HasPrefix(t, "[]"):
		return t[2:]
	case strings.HasPrefix(t, "map["):
		if i := strings.Index(t, "]"); i >= 0 {
			return t[i+1:]
		}
	}
	return t
}

// declaredInput is one action's declaration as the guards read it.
type declaredInput struct {
	action    Action
	predicate string
	inputs    []string
}

// declarationsUnderTest is the declaration table in the form the guards need:
// each action, the predicate it binds and the inputs it names.
func declarationsUnderTest() []declaredInput {
	out := make([]declaredInput, 0, len(actionDecls))
	for a, decl := range actionDecls {
		out = append(out, declaredInput{action: a, predicate: funcName(decl.ready), inputs: decl.inputs})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].action < out[j].action })
	return out
}

// funcName is the name of a function the table binds, as the source spells it.
func funcName(fn any) string {
	name := runtime.FuncForPC(reflect.ValueOf(fn).Pointer()).Name()
	name = strings.TrimSuffix(name, "-fm")
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	return name
}

// checkActionEnum refuses an action the enum names that carries no
// declaration, naming the action (§forgejo/decisions/actions,
// s-git/maitred#263).
func checkActionEnum(src *sourceTree, decls []declaredInput) ([]string, error) {
	actions, err := src.actionEnum()
	if err != nil {
		return nil, err
	}
	declared := map[Action]bool{}
	for _, d := range decls {
		declared[d.action] = true
	}
	var problems []string
	for _, a := range actions {
		if !declared[a] {
			problems = append(problems, fmt.Sprintf("action %q is named by the enum and carries no declaration, so nothing states the inputs it would dispatch on", a))
		}
	}
	return problems, nil
}

// checkDeclaredReads refuses a read an action's declaration does not name, and
// any activity timestamp read by a predicate at all; each finding names its
// file, its field and its action (§forgejo/decisions/inputs,
// s-git/maitred#263).
func checkDeclaredReads(src *sourceTree, decls []declaredInput) ([]string, error) {
	var problems []string
	for _, d := range decls {
		fn, ok := src.funcs[d.predicate]
		if !ok {
			return nil, fmt.Errorf("declaration check: the %s declaration binds predicate %s, which the source does not declare", d.action, d.predicate)
		}
		declared := map[string]bool{}
		for _, in := range d.inputs {
			declared[in] = true
		}
		for _, r := range src.readsOf(fn) {
			if problem := r.refusal(d.action, declared); problem != "" {
				problems = append(problems, problem)
			}
		}
	}
	sort.Strings(problems)
	return problems, nil
}

// refusal is one read's answer against one declaration: nothing when the
// declaration names it, a finding when it does not.
func (r read) refusal(a Action, declared map[string]bool) string {
	unnamed := func() string {
		return fmt.Sprintf("%s:%d: the %s declaration's predicate reads %s, which nothing in the vocabulary of §forgejo/decisions/inputs states",
			r.file, r.line, a, r)
	}
	for _, name := range rowIdentity[r.owner] {
		if name == r.field {
			return ""
		}
	}
	if r.ftype == "time.Time" {
		return fmt.Sprintf("%s:%d: the %s declaration's predicate reads the timestamp %s: the fold states it so the digest moves, and a row that turned on it would decide on the clock",
			r.file, r.line, a, r)
	}
	reached := r.reachedNames()
	if facts, ok := inputFields[r.owner]; ok {
		// The read lands on a row of the state model, so the fact it reads
		// must be named: a field the vocabulary says nothing about decides
		// nothing the declaration can name.
		names := facts[r.field]
		if len(names) == 0 {
			return unnamed()
		}
		reached = intersect(reached, names)
	}
	if len(reached) == 0 {
		return unnamed()
	}
	for _, name := range reached {
		if declared[name] {
			return ""
		}
	}
	return fmt.Sprintf("%s:%d: the %s declaration's predicate reads %s, a fact of %s; the %s declaration names %s",
		r.file, r.line, a, r, strings.Join(reached, ", "), a, strings.Join(sortedKeys(declared), ", "))
}

// reachedNames is the walk's answer: the names the longest walk to this read
// states a fact for.
func (r read) reachedNames() []string {
	for i := len(r.hops); i > 0; i-- {
		if names, ok := inputPaths[strings.Join(r.hops[:i], ".")]; ok {
			return names
		}
	}
	return nil
}

// hopsOf splits a walk as inputPaths states it — "State.PR",
// "PullRequest.HeadSHA" — into its hops.
func hopsOf(path string) []string {
	parts := strings.Split(path, ".")
	var hops []string
	for i := 0; i+1 < len(parts); i += 2 {
		hops = append(hops, parts[i]+"."+parts[i+1])
	}
	return hops
}

func intersect(a, b []string) []string {
	var out []string
	for _, x := range a {
		for _, y := range b {
			if x == y {
				out = append(out, x)
				break
			}
		}
	}
	return out
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// TestDeclarationCheck_EveryActionIsDeclared refuses an action the enum names
// with no declaration beside it (§forgejo/decisions/actions,
// s-git/maitred#263).
func TestDeclarationCheck_EveryActionIsDeclared(t *testing.T) {
	src := sourceUnderTest(t, ".")
	problems, err := checkActionEnum(src, declarationsUnderTest())
	if err != nil {
		t.Fatalf("the action-enum guard could not read the source: %v", err)
	}
	for _, p := range problems {
		t.Error(p)
	}
}

// TestDeclarationCheck_RefusesAnActionTheEnumNamesWithoutADeclaration proves
// the enum guard is a guard: an action the enum names and the table does not
// declare is refused by name.
func TestDeclarationCheck_RefusesAnActionTheEnumNamesWithoutADeclaration(t *testing.T) {
	src := sourceUnderTest(t, writeSource(t, map[string]string{
		"enum.go": `package probe

type Action string

const (
	ActionKnown  Action = "known"
	ActionStray  Action = "stray"
)
`,
	}))
	problems, err := checkActionEnum(src, []declaredInput{{action: "known", predicate: "knownReady"}})
	if err != nil {
		t.Fatalf("the action-enum guard: %v", err)
	}
	if len(problems) != 1 || !strings.Contains(problems[0], `"stray"`) {
		t.Fatalf("problems = %v, want one refusal naming the undeclared action", problems)
	}
}

// TestDispatch_RefusesAnUndeclaredAction pins the other half of the first
// guard: an action with no declaration cannot dispatch, and the refusal names
// it.
func TestDispatch_RefusesAnUndeclaredAction(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("dispatching an undeclared action returned a decision; want a refusal")
		}
		if !strings.Contains(fmt.Sprint(r), "undeclared action") || !strings.Contains(fmt.Sprint(r), "unwritten") {
			t.Fatalf("refusal = %v, want it to name the action it has no declaration for", r)
		}
	}()
	dispatch(Action("unwritten"), "no declaration states this action's inputs")
	t.Fatal("unreachable: dispatch must refuse before returning")
}

// TestDeclarationCheck_DecisionCodeReadsOnlyDeclaredInputs is the guard over
// the code as it stands: every read each predicate writes is a fact its own
// declaration names (§forgejo/decisions/inputs, s-git/maitred#263).
func TestDeclarationCheck_DecisionCodeReadsOnlyDeclaredInputs(t *testing.T) {
	src := sourceUnderTest(t, ".")
	problems, err := checkDeclaredReads(src, declarationsUnderTest())
	if err != nil {
		t.Fatalf("the declared-inputs guard could not read the source: %v", err)
	}
	for _, p := range problems {
		t.Error(p)
	}
}

// TestDeclarationCheck_FlagsAReadTheDeclarationDoesNotName proves the guard
// bites: a predicate reading a fact its action's set omits is refused, and the
// finding names its file, its field and its action.
func TestDeclarationCheck_FlagsAReadTheDeclarationDoesNotName(t *testing.T) {
	src := sourceUnderTest(t, writeSource(t, map[string]string{"probe.go": probeSource(`	if issue := s.Issue; issue != nil && issue.Body != "" {
		return dispatch(Action("probe"), "reading a fact nobody declared")
	}`)}))
	problems, err := checkDeclaredReads(src, []declaredInput{{
		action:    "probe",
		predicate: "probeReady",
		inputs:    []string{"issue.state"},
	}})
	if err != nil {
		t.Fatalf("the declared-inputs guard: %v", err)
	}
	if len(problems) != 1 {
		t.Fatalf("problems = %v, want one refusal", problems)
	}
	for _, want := range []string{"probe.go", "Issue.Body", "issue.body", "probe"} {
		if !strings.Contains(problems[0], want) {
			t.Errorf("refusal %q does not name %q", problems[0], want)
		}
	}
}

// TestDeclarationCheck_SilentAboutAReadTheDeclarationNames is the same code
// with the fact declared: the guard says nothing about a read the declaration
// owns.
func TestDeclarationCheck_SilentAboutAReadTheDeclarationNames(t *testing.T) {
	src := sourceUnderTest(t, writeSource(t, map[string]string{"probe.go": probeSource(`	if issue := s.Issue; issue != nil && issue.Body != "" {
		return dispatch(Action("probe"), "reading a declared fact")
	}`)}))
	problems, err := checkDeclaredReads(src, []declaredInput{{
		action:    "probe",
		predicate: "probeReady",
		inputs:    []string{"issue.state", "issue.body"},
	}})
	if err != nil {
		t.Fatalf("the declared-inputs guard: %v", err)
	}
	if len(problems) != 0 {
		t.Fatalf("problems = %v, want silence about a declared read", problems)
	}
}

// TestDeclarationCheck_RefusesATimestampRead pins the flagship: an action may
// declare its issue's activity timestamp as an input the digest folds, and a
// predicate that reads it still fails, because the row would then decide on the
// cursor rather than on the state.
func TestDeclarationCheck_RefusesATimestampRead(t *testing.T) {
	src := sourceUnderTest(t, writeSource(t, map[string]string{"probe.go": probeSource(`	if issue := s.Issue; issue != nil && !issue.UpdatedAt.IsZero() {
		return dispatch(Action("probe"), "deciding on the clock")
	}`)}))
	problems, err := checkDeclaredReads(src, []declaredInput{{
		action:    "probe",
		predicate: "probeReady",
		inputs:    []string{"issue.state", "issue.updated_at"},
	}})
	if err != nil {
		t.Fatalf("the declared-inputs guard: %v", err)
	}
	if len(problems) != 1 || !strings.Contains(problems[0], "timestamp") || !strings.Contains(problems[0], "Issue.UpdatedAt") {
		t.Fatalf("problems = %v, want one refusal naming the timestamp read", problems)
	}
}

// TestDeclarationCheck_RefusesAReadBesideTheVocabulary refuses a predicate
// reading a field the vocabulary of §forgejo/decisions/inputs says nothing
// about: a fact no declaration can name is a fact no row may decide on.
func TestDeclarationCheck_RefusesAReadBesideTheVocabulary(t *testing.T) {
	src := sourceUnderTest(t, writeSource(t, map[string]string{"probe.go": probeSource(`	if issue := s.Issue; issue != nil && issue.Title != "" {
		return dispatch(Action("probe"), "reading what no declaration names")
	}`)}))
	problems, err := checkDeclaredReads(src, []declaredInput{{
		action:    "probe",
		predicate: "probeReady",
		inputs:    []string{"issue.state", "issue.labels"},
	}})
	if err != nil {
		t.Fatalf("the declared-inputs guard: %v", err)
	}
	if len(problems) != 1 || !strings.Contains(problems[0], "vocabulary") {
		t.Fatalf("problems = %v, want one refusal that the vocabulary states no such input", problems)
	}
}

// TestDeclarationCheck_FailsLoudlyOnSourceItCannotRead proves neither guard
// passes on an empty reading: no source, unreadable source and a predicate the
// source does not declare are all errors.
func TestDeclarationCheck_FailsLoudlyOnSourceItCannotRead(t *testing.T) {
	empty := t.TempDir()
	if _, err := loadSourceTree(empty); err == nil {
		t.Error("a directory with no Go source read as an empty pass")
	}
	broken := writeSource(t, map[string]string{"broken.go": "package probe\nthis is not go\n"})
	if _, err := loadSourceTree(broken); err == nil {
		t.Error("source that will not parse read as an empty pass")
	}
	noEnum := sourceUnderTest(t, writeSource(t, map[string]string{"none.go": "package probe\n\nvar Action = \"not an enum\"\n"}))
	if _, err := checkActionEnum(noEnum, nil); err == nil {
		t.Error("an enum that names no action read as a pass")
	}
	src := sourceUnderTest(t, writeSource(t, map[string]string{"probe.go": probeSource("")}))
	if _, err := checkDeclaredReads(src, []declaredInput{{action: "probe", predicate: "missingReady"}}); err == nil {
		t.Error("a declaration binding a predicate the source does not declare read as a pass")
	}
}

// TestDeclarationCheck_VocabularyAndDeclarationsAgree keeps the tables above
// from rotting: every name they state is one some action declares, every name
// an action declares is one they state, and every hop they state is a field the
// source really carries (§forgejo/decisions/inputs, s-git/maitred#263).
func TestDeclarationCheck_VocabularyAndDeclarationsAgree(t *testing.T) {
	src := sourceUnderTest(t, ".")
	declared := map[string]bool{}
	for _, d := range declarationsUnderTest() {
		for _, in := range d.inputs {
			declared[in] = true
		}
	}
	stated := map[string]bool{}
	for path, names := range inputPaths {
		for _, hop := range hopsOf(path) {
			owner, field, ok := strings.Cut(hop, ".")
			if !ok {
				t.Errorf("inputPath %q states a hop that is not a field", path)
				continue
			}
			if _, ok := src.structs[owner][field]; !ok {
				t.Errorf("inputPath %q reads %s, which no struct in the source carries", path, hop)
			}
		}
		for _, name := range names {
			stated[name] = true
			if !declared[name] {
				t.Errorf("inputPath %q states input %q, which no action declares", path, name)
			}
		}
	}
	for owner, facts := range inputFields {
		for field, names := range facts {
			if _, ok := src.structs[owner][field]; !ok {
				t.Errorf("inputFields names %s.%s, which no struct in the source carries", owner, field)
			}
			for _, name := range names {
				stated[name] = true
				if !declared[name] {
					t.Errorf("inputFields names %s.%s for input %q, which no action declares", owner, field, name)
				}
			}
		}
	}
	for name := range declared {
		if !stated[name] {
			t.Errorf("an action declares input %q, which no read of the vocabulary reaches", name)
		}
	}
}

// readActionEnum is the action enum read from the source that declares it,
// for the tests that walk every action: a list beside the enum is one a new
// action can miss (§forgejo/decisions/actions, s-git/maitred#263).
func readActionEnum() []Action {
	src, err := loadSourceTree(".")
	if err != nil {
		panic(err)
	}
	actions, err := src.actionEnum()
	if err != nil {
		panic(err)
	}
	return actions
}

// sourceUnderTest loads a package, failing the test if it cannot be read.
func sourceUnderTest(t *testing.T, dir string) *sourceTree {
	t.Helper()
	src, err := loadSourceTree(dir)
	if err != nil {
		t.Fatalf("reading the source under %s: %v", dir, err)
	}
	return src
}

// writeSource lays a package out on disk for the guards to read.
func writeSource(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	return dir
}

// probeSource is a package carrying the input carriers the guards read, and a
// predicate named probeReady whose body the test supplies.
func probeSource(body string) string {
	return `package probe

import "time"

type Action string

type Issue struct {
	Number     int
	State      string
	Labels     []string
	Body       string
	Title      string
	Repository string
	UpdatedAt  time.Time
}

type Event struct {
	Type   string
	Sender string
}

type State struct {
	Issue    *Issue
	Blockers []Issue
}

func dispatch(a Action, reason string) Decision { return Decision{} }

type Decision struct{}

func probeReady(e Event, s State) Decision {
` + body + `
	return dispatch(Action("probe"), "nothing read")
}
`
}
