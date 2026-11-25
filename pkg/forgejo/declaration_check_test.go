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
//   - A read answers to the declaration whose walk reaches it. The
//     digest derivation a declaration binds is that declaration's own
//     implementation, so the fold's reads are never a predicate's
//     reads, and a helper that also serves code outside the walked
//     declarations — another predicate, the fold, the rows themselves
//     — answers to no one of them alone. Neither is excused by a list
//     of call sites, nor by the shape of the call: the walk follows a
//     predicate's calls into a package-level function and into a
//     method's body alike, entering a callable only when every caller
//     of it — a method counted under the row it hangs off — already
//     answers to the same declaration. That rule is taken to its
//     greatest fixpoint, so a helper a predicate calls and which calls
//     itself, or two helpers calling one another, stays in the set the
//     predicate reaches, and no call order decides membership. What
//     joins a single-caller helper's reads to its one caller's
//     therefore leaves everything shared out, of whatever shape. Each
//     call is walked on its own path, so a helper one walk reaches
//     twice answers to each call's bindings, not the first call's
//     alone. A state field handed across a call is recorded where it is
//     written, so the reads behind the call answer to the declaration
//     that reached it. The calls the walk cannot name are those on a
//     value it never bound: a method on a value a call returned —
//     `gate.fixFires` — or a closure held in a field, whose bodies it
//     therefore never enters. Whether a call names a package function at
//     all is the caller-set rule's decision, not a limit of the walk.
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
	"unicode"
	"unicode/utf8"
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
	// The thread is read for its own sake (§forgejo/decisions/actions).
	"State.Comments": {"issue.comments"},
	// The lenses and the three identities are configuration the engine
	// passes verbatim, not fetched state (§forgejo/decisions/inputs,
	// §forgejo/config/engine-account, §forgejo/config/decomposer-identity).
	// A row reads one the same way it reads any fact — by declaring it —
	// and the fold states it nowhere, like an event fact. The vocabulary
	// names these fields of the carriers and nothing inside them, so a
	// read within a configuration carrier answers under the carrier's
	// name.
	"State.ReviewLenses":                            {"config.review lenses"},
	"State.ImplementerIdentity":                     {"config.implementer identity"},
	"State.DecomposerIdentity":                      {"config.decomposer identity"},
	"State.EngineAccount":                           {"config.engine account"},
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
// when the walk and the fact agree on one name. A field absent here is one the
// vocabulary says nothing about, which is a refusal — authorship gates nothing
// (§forgejo/decisions/loop-prevention), so no name in the vocabulary belongs
// to an author.
var inputFields = map[string]map[string][]string{
	"Issue": {
		"State":  {"issue.state", "blockers.states", "connected issues", "connected issues' blockers", "connected issues' sync sets", "sync sets", "sync members", "blocked downstream edges"},
		"Labels": {"issue.labels", "issue.sync label"},
		"Body":   {"issue.body"},
		// The fold states the activity timestamp so a declared digest moves;
		// it is named here and still refused in a predicate by the timestamp
		// rule below.
		"UpdatedAt": {"issue.updated_at"},
	},
	"PullRequest": {
		"State":     {"PR.state", "connected PRs", "members' PRs"},
		"Merged":    {"PR.state"},
		"Mergeable": {"PR.mergeable", "members' PRs"},
		"HeadSHA":   {"PR.head sha", "members' PRs"},
		"Labels":    {"PR.labels"},
		"Author":    {"PR.author"},
	},
	"Review": {
		"Event":     {"reviews"},
		"Author":    {"reviews"},
		"CommitID":  {"reviews"},
		"Dismissed": {"reviews"},
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
// its structs carries, and the callables it declares — plain functions by their
// names, methods under the row they hang off.
type sourceTree struct {
	fset    *token.FileSet
	files   []*ast.File
	structs map[string]map[string]string
	funcs   map[string]*ast.FuncDecl
	methods map[string]*ast.FuncDecl
}

// methodKey is a method as the walk names it: the row it hangs off, and its
// name. A plain function's key is its name alone, so a method can never be
// mistaken for a function sharing its name — nor take that function's place in
// the table the walk reads.
func methodKey(recv, name string) string {
	return strings.TrimPrefix(recv, "*") + "." + name
}

// callable is the body the walk enters under one key: a plain function, or the
// method of a row.
func (s *sourceTree) callable(key string) *ast.FuncDecl {
	if strings.Contains(key, ".") {
		return s.methods[key]
	}
	return s.funcs[key]
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
	src := &sourceTree{
		fset:    token.NewFileSet(),
		structs: map[string]map[string]string{},
		funcs:   map[string]*ast.FuncDecl{},
		methods: map[string]*ast.FuncDecl{},
	}
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
				// Plain functions and methods are indexed apart, so a
				// method sharing a predicate's name cannot take its place
				// in the table the walk reads — and so that a method's own
				// body is a callable the caller-set rule can enter, under
				// the row it hangs off, rather than a body no call names.
				if decl.Recv == nil {
					src.funcs[decl.Name.Name] = decl
				} else if len(decl.Recv.List) > 0 {
					src.methods[methodKey(src.text(decl.Recv.List[0].Type), decl.Name.Name)] = decl
				}
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

// namesAnAction reports whether a constant's name is the shape an action's
// takes: `Action`, and an upper-case rune after it. A name merely beginning
// with those six letters — `ActionableLabel` — states no action, and refusing
// one would send its reader hunting for a hole in the enum that is not there.
func namesAnAction(name string) bool {
	rest, ok := strings.CutPrefix(name, "Action")
	if !ok || rest == "" {
		return false
	}
	first, size := utf8.DecodeRuneInString(rest)
	return size > 0 && unicode.IsUpper(first)
}

// stringValued reports whether a constant is written as a string literal — the
// shape an Action's name takes, and so the shape a missed one would take.
func stringValued(vs *ast.ValueSpec) bool {
	for _, v := range vs.Values {
		if lit, ok := v.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			return true
		}
	}
	return false
}

// actionEnum is the set the `Action` type names, read from the source that
// declares it (§forgejo/decisions/actions). An enum that names nothing is an
// error: the guard would otherwise pass on the strength of having read nothing.
// And because the decision code can name any constant the package declares, a
// constant named like an Action the read did not name is an error by name — in
// the typed block without its own type or written beside it — so a missed
// action is never a silent hole waiting to surface as a runtime refusal.
func (s *sourceTree) actionEnum() ([]Action, error) {
	found := map[string]bool{}
	named := map[string]bool{}
	var actions []Action
	for _, file := range s.files {
		for _, d := range file.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			typed := false
			for _, spec := range gd.Specs {
				if vs, ok := spec.(*ast.ValueSpec); ok && vs.Type != nil && s.text(vs.Type) == "Action" {
					typed = true
				}
			}
			if !typed {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				if vs.Type == nil {
					names := make([]string, len(vs.Names))
					for i, n := range vs.Names {
						names[i] = n.Name
					}
					return nil, fmt.Errorf("declaration check: %s sits with the typed Action constants without declaring its own type, so the enum cannot be read in full", strings.Join(names, ", "))
				}
				if s.text(vs.Type) != "Action" {
					continue
				}
				for _, n := range vs.Names {
					named[n.Name] = true
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
	for _, file := range s.files {
		for _, d := range file.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok || !stringValued(vs) {
					continue
				}
				// A constant declaring a type of its own states what it
				// is, and is no action the read missed.
				if vs.Type != nil && s.text(vs.Type) != "Action" {
					continue
				}
				for _, n := range vs.Names {
					if namesAnAction(n.Name) && !named[n.Name] {
						return nil, fmt.Errorf("declaration check: %s is named like an Action but is not one of the typed Action constants, so the enum cannot be read in full", n.Name)
					}
				}
			}
		}
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

// callAt is one written call, named as the walk names it, with the receiver and
// each argument resolved to the binding it stands on.
type callAt struct {
	name string
	args []binding
}

// callGraph maps each callable the walk can enter to the names of the callables
// whose bodies call it. A callable no body calls by name has no entry at all,
// which is what keeps a derivation bound in the declaration table from being
// followed into.
func (s *sourceTree) callGraph() map[string]map[string]bool {
	callers := map[string]map[string]bool{}
	for _, file := range s.files {
		for _, d := range file.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			caller := fn.Name.Name
			if fn.Recv != nil && len(fn.Recv.List) > 0 {
				caller = methodKey(s.text(fn.Recv.List[0].Type), caller)
			}
			_, calls := s.walkBody(fn, nil)
			for _, call := range calls {
				if callers[call.name] == nil {
					callers[call.name] = map[string]bool{}
				}
				callers[call.name][caller] = true
			}
		}
	}
	return callers
}

// answersTo is the set of callables whose reads answer to one declaration: the
// predicate, and every callable every caller of which already answers to it.
// That is the rule that keeps the digest machinery and shared helpers out of a
// declaration's reads — they serve code the walk has not come from — taken to
// its greatest fixpoint: every callable the predicate reaches by name starts in
// the set, and one is shed only when a caller outside the set reaches it, or
// when the declarations forbid it. A cycle therefore stays in, so a helper a
// predicate calls and which calls itself — or two helpers calling one another —
// has its reads attributed rather than silently lost; and no call order decides
// membership. A callable no body calls by name, a derivation bound in the
// declaration table, is reached by nothing and so is none's to follow.
func (s *sourceTree) answersTo(predicate string, callers map[string]map[string]bool, forbidden map[string]bool) map[string]bool {
	callees := calleesOf(callers)
	mine := map[string]bool{}
	for reach := []string{predicate}; len(reach) > 0; {
		name := reach[len(reach)-1]
		reach = reach[:len(reach)-1]
		if mine[name] {
			continue
		}
		mine[name] = true
		reach = append(reach, callees[name]...)
	}
	for changed := true; changed; {
		changed = false
		for name := range mine {
			if name == predicate {
				continue
			}
			if forbidden[name] || s.callable(name) == nil || !everyCallerIn(callers[name], mine) {
				delete(mine, name)
				changed = true
			}
		}
	}
	return mine
}

// calleesOf is a call graph read the other way: each callable and the callables
// whose bodies call it, as the fixpoint's reach.
func calleesOf(callers map[string]map[string]bool) map[string][]string {
	callees := map[string][]string{}
	for callee, cs := range callers {
		for caller := range cs {
			callees[caller] = append(callees[caller], callee)
		}
	}
	return callees
}

// everyCallerIn reports whether each name calling one callable is itself in the
// set answering to the declaration being walked.
func everyCallerIn(callers, mine map[string]bool) bool {
	for caller := range callers {
		if !mine[caller] {
			return false
		}
	}
	return true
}

// extend is one call path's visited set with one more function walked on it.
// Each call is walked with the path that reached it, so a helper one walk
// reaches twice attributes its reads to each call's own bindings rather than to
// the first call's alone; a function already on the path is not walked again,
// which is what ends the walk at a cycle.
func extend(visited map[string]bool, name string) map[string]bool {
	next := make(map[string]bool, len(visited)+1)
	for fn := range visited {
		next[fn] = true
	}
	next[name] = true
	return next
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

// readsOf is every read of an input carrier a callable writes: its own, in
// source order, then the reads of the callables in `mine` — the helpers whose
// every caller answers to the declaration being walked, a method's body no
// different from a package-level helper's. Names bound to a walk carry that
// walk with them, which is what tells the row under decision apart from the
// same issue read as a blocker; a callee's parameter carries the walk its
// argument was read out of, and each call is walked on its own path, which is
// what keeps every call's reads answerable to the declaration being walked.
func (s *sourceTree) readsOf(fn *ast.FuncDecl, args []binding, mine, visited map[string]bool) []read {
	out, calls := s.walkBody(fn, args)
	for _, c := range calls {
		callee := s.callable(c.name)
		if callee == nil || !mine[c.name] || visited[c.name] {
			continue
		}
		out = append(out, s.readsOf(callee, c.args, mine, extend(visited, c.name))...)
	}
	return out
}

// walkBody is one body's reads and calls: every read of an input carrier it
// writes, in the order it writes them, and every call it makes with the
// bindings its arguments — and, for a method, its receiver — stand on. env
// begins from what the callee's own parameters and receiver hold and is
// maintained as the body's definitions and range statements bind more, which is
// why reads and calls are gathered in one walk rather than two.
func (s *sourceTree) walkBody(fn *ast.FuncDecl, args []binding) ([]read, []callAt) {
	env := s.paramsOf(fn, args)
	var out []read
	var calls []callAt
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
			container := s.resolve(at.X, env)
			key, elem := keyType(container.typ), elemType(container.typ)
			bind := func(e ast.Expr, typ string) {
				if id, ok := e.(*ast.Ident); ok && id.Name != "_" {
					env[id.Name] = binding{typ: typ, hops: container.hops, tracked: container.tracked}
				}
			}
			// Each iteration variable holds its own type, not the container's:
			// the element for the value, the index or key for the first, which
			// is all a lone variable holds. Binding either to the container's
			// own type would make a read through it invisible, a slice carrying
			// no fields of any row to match against the vocabulary.
			bind(at.Key, key)
			if at.Value != nil {
				bind(at.Value, elem)
			}
		case *ast.CallExpr:
			if call, ok := s.callTo(at, env); ok {
				calls = append(calls, call)
			}
			// A call hides nothing: its arguments are walked on, so the
			// state they read is recorded here as the caller's own.
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
	return out, calls
}

// callTo is one written call as the walk names it: a plain function by its name,
// a method by the row it hangs off with its receiver standing as the callee's
// first argument, so a body behind the call attributes its reads the same way a
// helper does. A call on a value the walk never bound — one on a value a call
// returned, a closure held in a field — answers with none, and the reads behind
// it stay unattributed; the reads its arguments write are recorded here either
// way.
func (s *sourceTree) callTo(at *ast.CallExpr, env map[string]binding) (callAt, bool) {
	call := callAt{}
	switch fun := at.Fun.(type) {
	case *ast.Ident:
		call.name = fun.Name
	case *ast.SelectorExpr:
		recv := s.resolve(fun.X, env)
		if !recv.tracked || recv.typ == "" {
			return callAt{}, false
		}
		key := methodKey(recv.typ, fun.Sel.Name)
		if s.methods[key] == nil {
			return callAt{}, false
		}
		call.name = key
		call.args = append(call.args, recv)
	default:
		return callAt{}, false
	}
	for _, a := range at.Args {
		call.args = append(call.args, s.resolve(a, env))
	}
	return call, true
}

// paramsOf is one callable's own names as the walk reads them: its receiver,
// then its parameters, each bound to the binding standing in the argument
// position it fills.
func (s *sourceTree) paramsOf(fn *ast.FuncDecl, args []binding) map[string]binding {
	env := map[string]binding{}
	next := 0
	if fn.Recv != nil {
		next = s.bindParams(fn.Recv.List, args, next, env)
	}
	if fn.Type.Params != nil {
		s.bindParams(fn.Type.Params.List, args, next, env)
	}
	return env
}

// bindParams binds one parameter list's names and answers the argument position
// the next list starts at. A list writing no name — a bare receiver, an unnamed
// parameter — binds nothing and fills one position.
func (s *sourceTree) bindParams(list []*ast.Field, args []binding, i int, env map[string]binding) int {
	for _, f := range list {
		if f.Names == nil {
			i++
			continue
		}
		for _, n := range f.Names {
			if n.Name != "_" {
				env[n.Name] = s.paramBinding(s.text(f.Type), args, i)
			}
		}
		i++
	}
	return i
}

// paramBinding is one walked function's parameter as the walk reads it.
// A predicate's own parameters start every walk, so their type is the
// carrier's name; a callee's parameter carries the walk its argument was
// read out of, and one handed a value no walk reaches — a Decision, a
// derived row — stays as untracked as that value is, unless its own type
// names the carrier outright.
func (s *sourceTree) paramBinding(typ string, args []binding, i int) binding {
	switch {
	case i >= len(args):
		return binding{typ: typ, tracked: true}
	case args[i].tracked:
		return binding{typ: typ, hops: args[i].hops, tracked: true}
	case typ == "State" || typ == "Event":
		return binding{typ: typ, tracked: true}
	}
	return binding{}
}

// resolve is an expression's type and the walk it was read out of. The forms
// it places are those that state where a value was reached from: a name, a
// parenthesised, address-of or dereferenced value, an element or a re-slice of
// one it places, and a field of one it places. Everything else is answered
// untracked by right, and TestDeclarationCheck_EveryFormInWalkedCodeIsAccountedFor
// keeps that claim checked rather than hoped: a literal, a call, a composite
// literal and a closure are values no walk reaches; an operator's result is
// computed from its operands, whose reads the walk records where they stand;
// a type states no value at all. A form that could carry a walk and is named
// by neither list is the census's to refuse, so a new hop is added to resolve
// or named in the census, and nowhere else.
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
	case *ast.SliceExpr:
		// A re-slice holds the same rows as the container cut from, so it
		// carries that container's walk: `s.Blockers[1:]` ranges over the
		// same row type the whole listing does.
		return s.resolve(x.X, env)
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

// keyType is what a range statement's first iteration variable holds: the index
// for a slice, the key for a map, and the ranged value itself for anything the
// source does not state as a collection. A lone variable holds just that; and a
// type no row is written as carries no field any read could answer to.
func keyType(t string) string {
	switch {
	case strings.HasPrefix(t, "[]"):
		return "int"
	case strings.HasPrefix(t, "map["):
		if i := strings.Index(t, "]"); i >= 0 {
			return t[4:i]
		}
	}
	return t
}

// declaredInput is one action's declaration as the guards read it.
type declaredInput struct {
	action    Action
	predicate string
	digest    any
	inputs    []string
}

// declarationsUnderTest is the declaration table in the form the guards need:
// each action, the predicate and digest it binds and the inputs it names.
func declarationsUnderTest() []declaredInput {
	out := make([]declaredInput, 0, len(actionDecls))
	for a, decl := range actionDecls {
		out = append(out, declaredInput{action: a, predicate: funcName(decl.ready), digest: decl.digest, inputs: decl.inputs})
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
// s-git/maitred#263). A predicate's walk follows its calls into the functions
// every caller of which answers to that declaration, each call with its own
// bindings; the predicates and digest derivations the table binds are each
// their declaration's own implementation, never a helper to follow into.
func checkDeclaredReads(src *sourceTree, decls []declaredInput) ([]string, error) {
	callers := src.callGraph()
	forbidden := forbiddenDerivations(decls)
	seen := map[string]bool{}
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
		mine := src.answersTo(d.predicate, callers, forbidden)
		for _, r := range src.readsOf(fn, nil, mine, map[string]bool{d.predicate: true}) {
			problem := r.refusal(d.action, declared)
			// A function one walk reaches through two calls states its reads
			// twice, once for each call's bindings; the same refusal written
			// twice is one finding, not two.
			if problem != "" && !seen[problem] {
				seen[problem] = true
				problems = append(problems, problem)
			}
		}
	}
	sort.Strings(problems)
	return problems, nil
}

// refusal is one read's answer against one declaration: nothing when the
// declaration names it, a finding when it does not. Every finding takes the
// same shape — where the read stands, what it reads, and one clause naming
// why the declaration does not own it.
func (r read) refusal(a Action, declared map[string]bool) string {
	for _, name := range rowIdentity[r.owner] {
		if name == r.field {
			return ""
		}
	}
	if t := strings.TrimPrefix(r.ftype, "*"); t == "time.Time" {
		// The rule is read off the field's type, pointer included: a
		// timestamp spelled `*time.Time` decides on the clock exactly as
		// one spelled `time.Time` does.
		return r.finding(a, ": the fold states this timestamp so the digest moves, and a row that turned on the cursor itself would decide on the clock rather than on the state")
	}
	reached := r.reachedNames()
	if facts, ok := inputFields[r.owner]; ok {
		// The read lands on a row of the state model, so the fact it reads
		// must be named: a field the vocabulary says nothing about decides
		// nothing the declaration can name.
		names := facts[r.field]
		if len(names) == 0 {
			return r.finding(a, ", which nothing in the vocabulary of §forgejo/decisions/inputs states")
		}
		reached = intersect(reached, names)
	}
	if len(reached) == 0 {
		return r.finding(a, ", which nothing in the vocabulary of §forgejo/decisions/inputs states")
	}
	for _, name := range reached {
		// A read the declaration names is refused by nothing — a
		// configuration fact included, though the fold states it
		// nowhere.
		if declared[name] {
			return ""
		}
	}
	return r.finding(a, fmt.Sprintf(", a fact of %s; the declaration names %s",
		strings.Join(reached, ", "), strings.Join(sortedKeys(declared), ", ")))
}

// finding is a refused read: where it stands, what it reads, and the reason
// it is refused.
func (r read) finding(a Action, why string) string {
	return fmt.Sprintf("%s:%d: the %s declaration's predicate reads %s%s", r.file, r.line, a, r, why)
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

// TestDeclarationCheck_RefusesAnEnumItCannotFullyRead keeps the first guard
// honest about its reading: a constant the decision code can name and the enum
// read would miss is an error, not a silent skip — whether it shares the typed
// block without a type of its own or sits beside it in a declaration of its
// own. The miss must not wait for the runtime refusal to surface.
func TestDeclarationCheck_RefusesAnEnumItCannotFullyRead(t *testing.T) {
	for seat, source := range map[string]string{
		"in the typed block": `package probe

type Action string

const (
	ActionKnown Action = "known"
	ActionStray = "stray"
)
`,
		"beside it": `package probe

type Action string

const ActionKnown Action = "known"

const ActionStray = "stray"
`,
	} {
		src := sourceUnderTest(t, writeSource(t, map[string]string{"enum.go": source}))
		if _, err := checkActionEnum(src, []declaredInput{{action: "known", predicate: "knownReady"}}); err == nil {
			t.Errorf("an untyped Action-named constant %s read as no action at all", seat)
		}
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

// TestDeclarationCheck_AttributesReadsInASingleCallerHelper walks a helper
// that serves exactly one predicate into that predicate's reads: a timestamp
// read no predicate writes directly still decides the row on the clock
// (§forgejo/decisions/inputs, s-git/maitred#263).
func TestDeclarationCheck_AttributesReadsInASingleCallerHelper(t *testing.T) {
	src := sourceUnderTest(t, writeSource(t, map[string]string{"probe.go": probeSource(`	if touchedRecently(s) {
		return dispatch(Action("probe"), "deciding through a helper")
	}`) + `
func touchedRecently(s State) bool {
	issue := s.Issue
	return issue != nil && !issue.UpdatedAt.IsZero()
}
`}))
	problems, err := checkDeclaredReads(src, []declaredInput{{
		action:    "probe",
		predicate: "probeReady",
		inputs:    []string{"issue.state"},
	}})
	if err != nil {
		t.Fatalf("the declared-inputs guard: %v", err)
	}
	if len(problems) != 1 || !strings.Contains(problems[0], "timestamp") || !strings.Contains(problems[0], "Issue.UpdatedAt") {
		t.Fatalf("problems = %v, want one refusal naming the timestamp read a single-caller helper writes", problems)
	}
}

// TestDeclarationCheck_AttributesAReadThroughARangeVariable is the same rule
// over the form every Go programmer writes: the iteration variable holds the
// element, not the slice, so a timestamp read through it decides the row on the
// clock and is refused (§forgejo/decisions/inputs, s-git/maitred#263).
func TestDeclarationCheck_AttributesAReadThroughARangeVariable(t *testing.T) {
	src := sourceUnderTest(t, writeSource(t, map[string]string{"probe.go": probeSource(`	for _, b := range s.Blockers {
		if !b.UpdatedAt.IsZero() {
			return dispatch(Action("probe"), "deciding on the clock through a range")
		}
	}`)}))
	problems, err := checkDeclaredReads(src, []declaredInput{{
		action:    "probe",
		predicate: "probeReady",
		inputs:    []string{"issue.state", "blockers.states"},
	}})
	if err != nil {
		t.Fatalf("the declared-inputs guard: %v", err)
	}
	if len(problems) != 1 || !strings.Contains(problems[0], "timestamp") || !strings.Contains(problems[0], "Issue.UpdatedAt") {
		t.Fatalf("problems = %v, want one refusal naming the timestamp read through the range variable", problems)
	}
}

// TestDeclarationCheck_AttributesAReadReachedThroughAMethod is the same rule
// over the shape a predicate takes the moment it needs a second opinion of the
// row it is already holding: a call on that row, whose body the walk enters
// under the row it hangs off, so a timestamp read behind the call answers to the
// declaration rather than to no one (§forgejo/decisions/inputs,
// s-git/maitred#263).
func TestDeclarationCheck_AttributesAReadReachedThroughAMethod(t *testing.T) {
	src := sourceUnderTest(t, writeSource(t, map[string]string{"probe.go": probeSource(`	if s.Issue.probeTouched() {
		return dispatch(Action("probe"), "deciding through a method on the row")
	}`) + `
func (i *Issue) probeTouched() bool { return !i.UpdatedAt.IsZero() }
`}))
	problems, err := checkDeclaredReads(src, []declaredInput{{
		action:    "probe",
		predicate: "probeReady",
		inputs:    []string{"issue.state"},
	}})
	if err != nil {
		t.Fatalf("the declared-inputs guard: %v", err)
	}
	if len(problems) != 1 || !strings.Contains(problems[0], "timestamp") || !strings.Contains(problems[0], "Issue.UpdatedAt") {
		t.Fatalf("problems = %v, want one refusal naming the timestamp read a method's body writes", problems)
	}
}

// TestDeclarationCheck_SilentAboutAMethodSharedWithAnotherPredicate keeps that
// entry a rule and not a licence: a method one predicate reaches and another
// calls as well serves no one of them alone, and the walk stays out of it — as
// it stays out of a helper shared the same way.
func TestDeclarationCheck_SilentAboutAMethodSharedWithAnotherPredicate(t *testing.T) {
	src := sourceUnderTest(t, writeSource(t, map[string]string{"probe.go": probeSource(`	if s.Issue.probeTouched() {
		return dispatch(Action("probe"), "a method the two predicates share")
	}`) + `
func otherReady(e Event, s State) Decision {
	if s.Issue.probeTouched() {
		return dispatch(Action("other"), "the method's other caller")
	}
	return dispatch(Action("other"), "nothing read")
}

func (i *Issue) probeTouched() bool { return !i.UpdatedAt.IsZero() }
`}))
	problems, err := checkDeclaredReads(src, []declaredInput{
		{action: "probe", predicate: "probeReady", inputs: []string{"issue.state"}},
		{action: "other", predicate: "otherReady", inputs: []string{"issue.state"}},
	})
	if err != nil {
		t.Fatalf("the declared-inputs guard: %v", err)
	}
	if len(problems) != 0 {
		t.Fatalf("problems = %v, want a method shared by two predicates left out of both declarations", problems)
	}
}

// TestDeclarationCheck_AttributesAReadThroughASliceOfTheContainer is the same
// rule over the form that skips a listing's head: a re-slice holds the same
// rows as the container cut from, so the read behind it answers to the walk
// that reached the container (§forgejo/decisions/inputs, s-git/maitred#263).
func TestDeclarationCheck_AttributesAReadThroughASliceOfTheContainer(t *testing.T) {
	src := sourceUnderTest(t, writeSource(t, map[string]string{"probe.go": probeSource(`	if anyTouched(s.Blockers[1:]) {
		return dispatch(Action("probe"), "deciding through a re-sliced listing")
	}`) + `

func anyTouched(rows []Issue) bool {
	for _, r := range rows {
		if !r.UpdatedAt.IsZero() {
			return true
		}
	}
	return false
}
`}))
	problems, err := checkDeclaredReads(src, []declaredInput{{
		action:    "probe",
		predicate: "probeReady",
		inputs:    []string{"issue.state", "blockers.states"},
	}})
	if err != nil {
		t.Fatalf("the declared-inputs guard: %v", err)
	}
	if len(problems) != 1 || !strings.Contains(problems[0], "timestamp") || !strings.Contains(problems[0], "Issue.UpdatedAt") {
		t.Fatalf("problems = %v, want one refusal naming the timestamp read behind the re-slice", problems)
	}
}

// TestDeclarationCheck_AttributesAReadInAHelperThatCallsItself is the same rule
// over the shape walking these graphs reaches for: a helper a predicate calls
// and which calls itself stays in the set that one caller reaches, so the reads
// of every iteration it walks are attributed (§forgejo/decisions/inputs,
// s-git/maitred#263).
func TestDeclarationCheck_AttributesAReadInAHelperThatCallsItself(t *testing.T) {
	src := sourceUnderTest(t, writeSource(t, map[string]string{"probe.go": probeSource(`	if anyTouched(s.Blockers) {
		return dispatch(Action("probe"), "deciding through a helper that recurses")
	}`) + `
func anyTouched(rows []Issue) bool {
	for _, r := range rows {
		if !r.UpdatedAt.IsZero() {
			return true
		}
	}
	return len(rows) > 0 && anyTouched(rows[1:])
}
`}))
	problems, err := checkDeclaredReads(src, []declaredInput{{
		action:    "probe",
		predicate: "probeReady",
		inputs:    []string{"issue.state", "blockers.states"},
	}})
	if err != nil {
		t.Fatalf("the declared-inputs guard: %v", err)
	}
	if len(problems) != 1 || !strings.Contains(problems[0], "timestamp") || !strings.Contains(problems[0], "Issue.UpdatedAt") {
		t.Fatalf("problems = %v, want one refusal naming the timestamp read a recursive helper writes", problems)
	}
}

// TestDeclarationCheck_SilentAboutAConstantNamedLikeNoAction keeps the by-name
// sweep about the enum it reads: a label whose name merely begins with those six
// letters, and a constant stating a type of its own, are no action the read
// missed, and refusing them sends the reader hunting for a hole that is not
// there.
func TestDeclarationCheck_SilentAboutAConstantNamedLikeNoAction(t *testing.T) {
	src := sourceUnderTest(t, writeSource(t, map[string]string{"enum.go": `package probe

type Action string

type Kind string

const (
	ActionKnown Action = "known"
)

const (
	ActionableLabel = "actionable"
	ActionKind      Kind   = "kind"
)
`}))
	if _, err := checkActionEnum(src, []declaredInput{{action: "known", predicate: "knownReady"}}); err != nil {
		t.Errorf("an unrelated constant read as an action the enum missed: %v", err)
	}
}

// TestDeclarationCheck_SilentAboutAHelperSharedByTwoPredicates is the other
// half of the same rule: a helper that serves more than one walked predicate
// answers to no one of them alone, and the walk stays out of it — by rule,
// not by a list of call sites.
func TestDeclarationCheck_SilentAboutAHelperSharedByTwoPredicates(t *testing.T) {
	src := sourceUnderTest(t, writeSource(t, map[string]string{"probe.go": probeSource(`	if touchedRecently(s) {
		return dispatch(Action("probe"), "a helper the two predicates share")
	}`) + `
func otherReady(e Event, s State) Decision {
	if touchedRecently(s) {
		return dispatch(Action("other"), "the helper's other caller")
	}
	return dispatch(Action("other"), "nothing read")
}

func touchedRecently(s State) bool {
	issue := s.Issue
	return issue != nil && !issue.UpdatedAt.IsZero()
}
`}))
	problems, err := checkDeclaredReads(src, []declaredInput{
		{action: "probe", predicate: "probeReady", inputs: []string{"issue.state"}},
		{action: "other", predicate: "otherReady", inputs: []string{"issue.state"}},
	})
	if err != nil {
		t.Fatalf("the declared-inputs guard: %v", err)
	}
	if len(problems) != 0 {
		t.Fatalf("problems = %v, want a shared helper left out of both declarations", problems)
	}
}

// TestDeclarationCheck_AttributesEachCallToItsOwnBindings is the shared-caller
// rule one level down: a helper one walk reaches from two calls answers to each
// call's bindings, not the first call's alone, so the read the second call
// stands for is refused rather than lost (§forgejo/decisions/inputs,
// s-git/maitred#263).
func TestDeclarationCheck_AttributesEachCallToItsOwnBindings(t *testing.T) {
	src := sourceUnderTest(t, writeSource(t, map[string]string{"probe.go": probeSource(`	for _, b := range s.Blockers {
		if touched(s.Issue) || touched(&b) {
			return dispatch(Action("probe"), "one helper, reached twice")
		}
	}`) + `
func touched(i *Issue) bool { return i != nil && i.Body != "" }
`}))
	problems, err := checkDeclaredReads(src, []declaredInput{{
		action:    "probe",
		predicate: "probeReady",
		inputs:    []string{"issue.state", "issue.body", "blockers.states"},
	}})
	if err != nil {
		t.Fatalf("the declared-inputs guard: %v", err)
	}
	if len(problems) != 1 || !strings.Contains(problems[0], "Issue.Body") {
		t.Fatalf("problems = %v, want one refusal for the read the second call stands for", problems)
	}
}

// TestDeclarationCheck_WalksTheDeclaredFunctionNotASameNamedMethod pins the
// seat the declaration table is read through: a method sharing a predicate's
// name must not take the function's place in the walked source, or the
// predicate's reads vanish with it.
func TestDeclarationCheck_WalksTheDeclaredFunctionNotASameNamedMethod(t *testing.T) {
	src := sourceUnderTest(t, writeSource(t, map[string]string{"probe.go": probeSource(`	if issue := s.Issue; issue != nil && issue.Body != "" {
		return dispatch(Action("probe"), "reading a fact nobody declared")
	}`) + `
func (s State) probeReady() Decision { return Decision{} }
`}))
	problems, err := checkDeclaredReads(src, []declaredInput{{
		action:    "probe",
		predicate: "probeReady",
		inputs:    []string{"issue.state"},
	}})
	if err != nil {
		t.Fatalf("the declared-inputs guard: %v", err)
	}
	if len(problems) != 1 || !strings.Contains(problems[0], "Issue.Body") {
		t.Fatalf("problems = %v, want the declared function walked, not the same-named method", problems)
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

// TestDeclarationCheck_RefusesATimestampReadWhateverTheSpelling keeps the
// flagship rule off its spelling: a timestamp the model gains as a pointer is
// the same decision on the clock, and the rule is read off the field's type
// with the pointer stripped (§forgejo/decisions/inputs, s-git/maitred#263).
func TestDeclarationCheck_RefusesATimestampReadWhateverTheSpelling(t *testing.T) {
	src := sourceUnderTest(t, writeSource(t, map[string]string{"probe.go": probeSource(`	if issue := s.Issue; issue != nil && issue.DeletedAt == nil {
		return dispatch(Action("probe"), "deciding on a pointer to the clock")
	}`)}))
	problems, err := checkDeclaredReads(src, []declaredInput{{
		action:    "probe",
		predicate: "probeReady",
		inputs:    []string{"issue.state"},
	}})
	if err != nil {
		t.Fatalf("the declared-inputs guard: %v", err)
	}
	if len(problems) != 1 || !strings.Contains(problems[0], "timestamp") || !strings.Contains(problems[0], "Issue.DeletedAt") {
		t.Fatalf("problems = %v, want one refusal naming the pointer-spelled timestamp read", problems)
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

// forbiddenDerivations is the set of callables no walk enters as a helper:
// the predicates and digest derivations the table binds, each of which is its
// own declaration's implementation rather than a body the caller-set rule
// weighs.
func forbiddenDerivations(decls []declaredInput) map[string]bool {
	forbidden := map[string]bool{}
	for _, d := range decls {
		forbidden[d.predicate] = true
		if d.digest != nil {
			// A derivation bound as a closure has no source name to forbid;
			// no body can call it by name anyway.
			if name := funcName(d.digest); !strings.HasPrefix(name, "func") {
				forbidden[name] = true
			}
		}
	}
	return forbidden
}

// TestDeclarationCheck_EveryFormInWalkedCodeIsAccountedFor is the census that
// keeps resolve's enumeration safe rather than lucky: every expression written
// in a walked body, at every position the walk asks one, is either a form
// resolve places or one the walk answers untracked by right — a literal, a
// call, a composite literal, a closure, an operator's result, a type. A form
// the enumeration has not been told about is an error naming it and where it
// stands, so a new spelling fails the day it is written instead of in the next
// review (s-git/maitred#263).
func TestDeclarationCheck_EveryFormInWalkedCodeIsAccountedFor(t *testing.T) {
	src := sourceUnderTest(t, ".")
	decls := declarationsUnderTest()
	callers := src.callGraph()
	forbidden := forbiddenDerivations(decls)
	asked := map[string]bool{}
	var problems []string
	for _, d := range decls {
		for name := range src.answersTo(d.predicate, callers, forbidden) {
			fn := src.callable(name)
			if fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch at := n.(type) {
				case *ast.AssignStmt:
					if at.Tok == token.DEFINE || at.Tok == token.ASSIGN {
						for _, rhs := range at.Rhs {
							src.censusExpr(rhs, asked, &problems)
						}
					}
				case *ast.RangeStmt:
					src.censusExpr(at.X, asked, &problems)
				case *ast.SelectorExpr:
					src.censusExpr(at.X, asked, &problems)
				case *ast.CallExpr:
					src.censusExpr(at.Fun, asked, &problems)
					for _, arg := range at.Args {
						src.censusExpr(arg, asked, &problems)
					}
				}
				return true
			})
		}
	}
	for _, p := range problems {
		t.Error(p)
	}
}

// censusExpr is the walk's question asked of one expression written where the
// walk asks one: is this form placed by resolve, is it something the walk
// answers untracked by right — a literal, a call, a composite literal, a
// closure, an operator's result, a type — or is it a form the enumeration has
// not been told about, which is an error naming it? A call is asked of its
// arguments either way, and each placed form of the expression it was reached
// from; the reads a named-untracked form's operands write are recorded by the
// walk's own selector case, never by this one.
func (s *sourceTree) censusExpr(e ast.Expr, asked map[string]bool, problems *[]string) {
	switch x := e.(type) {
	case *ast.Ident:
	case *ast.BasicLit:
	case *ast.CompositeLit:
	case *ast.FuncLit:
	case *ast.BinaryExpr:
		// What an operator yields is a value computed from its operands,
		// not a row reached from a carrier — and the reads the operands
		// write are the walk's selector case's, recorded wherever they
		// stand.
	case *ast.ArrayType, *ast.MapType, *ast.ChanType, *ast.FuncType, *ast.StructType, *ast.InterfaceType:
		// A type states no value any walk reaches: `make([]Issue, n)`
		// passes one where the walk asks an argument.
	case *ast.ParenExpr:
		s.censusExpr(x.X, asked, problems)
	case *ast.UnaryExpr:
		s.censusExpr(x.X, asked, problems)
	case *ast.StarExpr:
		s.censusExpr(x.X, asked, problems)
	case *ast.IndexExpr:
		s.censusExpr(x.X, asked, problems)
	case *ast.SliceExpr:
		s.censusExpr(x.X, asked, problems)
	case *ast.SelectorExpr:
		s.censusExpr(x.X, asked, problems)
	case *ast.CallExpr:
		s.censusExpr(x.Fun, asked, problems)
		for _, arg := range x.Args {
			s.censusExpr(arg, asked, problems)
		}
	default:
		pos := s.fset.Position(e.Pos())
		key := fmt.Sprintf("%s:%d %T", filepath.Base(pos.Filename), pos.Line, e)
		if !asked[key] {
			asked[key] = true
			*problems = append(*problems, fmt.Sprintf("%s: walked code writes a %T where the walk asks an expression; neither resolve places it nor does the census record it as answered untracked by right", key, e))
		}
	}
}

// TestDeclarationCheck_TheTwoEnumerationsAgree keeps those enumerations
// from drifting apart: the forms that carry a walk are listed once, as
// data, and asked of both switches — resolve must place each carrier
// form's walk, and censusExpr must ask the walk's question inside every
// form it recurses through. The one asymmetry the lists carry is
// recorded as data too: a call's result answers untracked by right, so
// resolve places no walk through a CallExpr while the census still
// asks its parts. A case added to one switch and not the other fails
// here by name (s-git/maitred#277).
func TestDeclarationCheck_TheTwoEnumerationsAgree(t *testing.T) {
	src := sourceUnderTest(t, ".")
	env := map[string]binding{"x": {typ: "Issue", hops: []string{"x"}, tracked: true}}
	forms := []struct {
		name    string
		resolve bool // whether resolve places a walk through this form
		wrap    func(ast.Expr) ast.Expr
	}{
		{"*ast.Ident", true, func(e ast.Expr) ast.Expr { return e }},
		{"*ast.ParenExpr", true, func(e ast.Expr) ast.Expr { return &ast.ParenExpr{X: e} }},
		{"*ast.UnaryExpr", true, func(e ast.Expr) ast.Expr { return &ast.UnaryExpr{Op: token.AND, X: e} }},
		{"*ast.StarExpr", true, func(e ast.Expr) ast.Expr { return &ast.StarExpr{X: e} }},
		{"*ast.IndexExpr", true, func(e ast.Expr) ast.Expr { return &ast.IndexExpr{X: e, Index: ast.NewIdent("i")} }},
		{"*ast.SliceExpr", true, func(e ast.Expr) ast.Expr { return &ast.SliceExpr{X: e, Low: ast.NewIdent("i")} }},
		{"*ast.SelectorExpr", true, func(e ast.Expr) ast.Expr { return &ast.SelectorExpr{X: e, Sel: ast.NewIdent("Number")} }},
		{"*ast.CallExpr", false, func(e ast.Expr) ast.Expr { return &ast.CallExpr{Fun: e} }},
	}
	for _, f := range forms {
		if b := src.resolve(f.wrap(ast.NewIdent("x")), env); b.tracked != f.resolve {
			if f.resolve {
				t.Errorf("resolve places no walk through %s, which the list says carries one", f.name)
			} else {
				t.Errorf("resolve places a walk through %s, which the list says answers untracked by right", f.name)
			}
		}
		uncounted := &ast.TypeAssertExpr{X: ast.NewIdent("x"), Type: &ast.Ident{Name: "int"}}
		asked := map[string]bool{}
		var problems []string
		src.censusExpr(f.wrap(uncounted), asked, &problems)
		if len(problems) == 0 || !strings.Contains(problems[0], "*ast.TypeAssertExpr") {
			t.Errorf("censusExpr did not ask the walk's question inside %s of an uncounted form; it refused: %v", f.name, problems)
		}
	}
}

// TestDeclarationCheck_SilentAboutAConfigurationRead is one rule unchanged:
// a read the declaration names is refused by nothing, a configuration fact
// included — and a read inside a configuration carrier answers under the
// carrier's name, which is where the vocabulary ends (§forgejo/decisions/inputs,
// s-git/maitred#263).
func TestDeclarationCheck_SilentAboutAConfigurationRead(t *testing.T) {
	src := sourceUnderTest(t, writeSource(t, map[string]string{"probe.go": probeSource(`	for _, lens := range s.ReviewLenses {
		if lens.Identity != "" {
			return dispatch(Action("probe"), "asking which lenses to review with")
		}
	}`)}))
	problems, err := checkDeclaredReads(src, []declaredInput{{
		action:    "probe",
		predicate: "probeReady",
		inputs:    []string{"issue.state", "config.review lenses"},
	}})
	if err != nil {
		t.Fatalf("the declared-inputs guard: %v", err)
	}
	if len(problems) != 0 {
		t.Fatalf("problems = %v, want a configuration read the declaration names left alone", problems)
	}
}

// TestDeclarationCheck_RefusesAnUndeclaredConfigurationRead is the other half:
// a configuration fact is declared like any other, and the row that reads one
// its declaration does not name is refused — the fold states it nowhere, but
// the declaration must still name it (§forgejo/decisions/inputs,
// s-git/maitred#263).
func TestDeclarationCheck_RefusesAnUndeclaredConfigurationRead(t *testing.T) {
	src := sourceUnderTest(t, writeSource(t, map[string]string{"probe.go": probeSource(`	if len(s.ReviewLenses) > 0 {
		return dispatch(Action("probe"), "asking which lenses to review with")
	}`)}))
	problems, err := checkDeclaredReads(src, []declaredInput{{
		action:    "probe",
		predicate: "probeReady",
		inputs:    []string{"issue.state"},
	}})
	if err != nil {
		t.Fatalf("the declared-inputs guard: %v", err)
	}
	if len(problems) != 1 || !strings.Contains(problems[0], "config.review lenses") {
		t.Fatalf("problems = %v, want one refusal naming the undeclared configuration read", problems)
	}
}

// TestDeclarationCheck_RefusesAStateFieldTheVocabularyDoesNotState draws the
// boundary the other way: a field of a carrier the vocabulary says nothing
// about reaches no name, and is refused whatever the row declares — naming a
// configuration carrier states nothing about fields it later gains.
func TestDeclarationCheck_RefusesAStateFieldTheVocabularyDoesNotState(t *testing.T) {
	src := sourceUnderTest(t, writeSource(t, map[string]string{"probe.go": probeSource(`	if s.Note != "" {
		return dispatch(Action("probe"), "reading a field no section states")
	}`)}))
	problems, err := checkDeclaredReads(src, []declaredInput{{
		action:    "probe",
		predicate: "probeReady",
		inputs:    []string{"issue.state", "config.review lenses"},
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
			// A name no action declares is drift: the vocabulary states
			// only facts a declaration names, configuration included.
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
	DeletedAt  *time.Time
}

type Event struct {
	Type   string
	Sender string
}

type ReviewLens struct {
	Identity string
}

type State struct {
	Issue        *Issue
	Blockers     []Issue
	ReviewLenses []ReviewLens
	Note         string
}

func dispatch(a Action, reason string) Decision { return Decision{} }

type Decision struct{}

func probeReady(e Event, s State) Decision {
` + body + `
	return dispatch(Action("probe"), "nothing read")
}
`
}
