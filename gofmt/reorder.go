package main

// The reordering of -strict. It runs before printing, on the source text, in
// passes that each reorder one kind of thing:
//
//   - types move up, after the imports, when the names they use are types,
//     imports, or type parameters (see hoistTypes);
//   - the const, var and type declarations of each run of consecutive ones are
//     split into single declarations and sorted (see reorderSpecRuns);
//   - each run of consecutive plain functions, and each run of methods, is sorted
//     by name, methods by receiver type and then name (see reorderDecls);
//   - each run of consecutive := statements with pure values is sorted by the
//     names it declares (see reorderAssignRuns).
//
// In every run, a declaration comes after the declarations it refers to, and
// otherwise in alphabetical order. A cycle keeps its source order. Declarations
// and statements move with the comments that lead into them. A var whose value
// calls a function is never moved (see pureExpr).
import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type (
	// edit replaces src[start:end] with text.
	edit struct {
		start, end int
		text []byte
	}

	// layout gives positions of a file as offsets into its source.
	layout struct {
		tf *token.File
		src []byte
		comments []*ast.CommentGroup
	}

	// unit is a declaration, or a spec of a type group, that is moved as a whole.
	unit struct {
		kind token.Token // token.FUNC for a function, otherwise the token of the declaration
		names []string // the names it declares; for a method, "Type.Name"
		body ast.Node // the part whose references count as dependencies
		objs []any // the declarations that resolved references may point to
		start token.Pos // where it starts, including its doc comment
		end token.Pos // where it ends

		method bool // a method: a function with a receiver
		methodName string // the name of a method
		sels []string // the names selected in its body, which may name other methods
	}
)

// pureExpr reports whether the node n is free of calls (other than conversions to
// predeclared types, make and new) and of receives. A function literal is pure
// only if funcLitOK: its body may hold blocks, which a move could not cut out.
func pureExpr(n ast.Node, funcLitOK bool) bool {
	pure := true

	ast.Inspect(n, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CallExpr:
			if id, ok := x.Fun.(*ast.Ident); !ok || !(predeclaredPure[id.Name] || id.Name == "make" || id.Name == "new") {
				pure = false
			}
		case *ast.UnaryExpr:
			if x.Op == token.ARROW {
				pure = false
			}
		case *ast.FuncLit:
			if !funcLitOK {
				pure = false
			}
		}

		return pure
	})
	return pure
}

// pureVars reports whether the values of a var declaration can be evaluated
// without side effects, so that moving it does not change the order of any
// effect. Go initialises package variables in declaration order, except where a
// dependency forces another order, so a move is visible only through a side
// effect. A call can have one, and a receive can block; a conversion to a
// predeclared type, make and new cannot. Calls are checked syntactically, so a
// call to a function named like a predeclared type is taken to be impure.
func pureVars(d *ast.GenDecl) bool {
	return pureExpr(d, true)
}

// specRunToken returns the token of the declarations that reorderSpecRuns may
// split into specs and sort, or ILLEGAL if d is not one of them. Types always
// are. A var declaration is one if its values are pure (see pureVars). A const
// declaration is one unless it is a group whose specs take their values from
// their position: a spec without values repeats the one before it, and iota
// counts the specs, so such a group keeps its order. Single const declarations
// are always splittable: each is a group of its own.
func specRunToken(d ast.Decl) token.Token {
	g, ok := d.(*ast.GenDecl)
	if !ok {
		return token.ILLEGAL
	}

	switch g.Tok {
	case token.TYPE:
		return token.TYPE
	case token.VAR:
		if pureVars(g) {
			return token.VAR
		}
	case token.CONST:
		if !g.Lparen.IsValid() {
			return token.CONST
		}

		for _, sp := range g.Specs {
			if vs := sp.(*ast.ValueSpec); len(vs.Values) == 0 {
				return token.ILLEGAL
			}
		}

		if !usesIota(g) {
			return token.CONST
		}
	}

	return token.ILLEGAL
}

// specUnit returns spec sp of a declaration of token tok as a unit.
func specUnit(tok token.Token, sp ast.Spec) unit {
	var names []string

	start, end := sp.Pos(), sp.End()

	switch sp := sp.(type) {
	case *ast.ValueSpec:
		for _, id := range sp.Names {
			names = append(names, id.Name)
		}

		if sp.Doc != nil {
			start = sp.Doc.Pos()
		}
	case *ast.TypeSpec:
		names = []string{sp.Name.Name}

		if sp.Doc != nil {
			start = sp.Doc.Pos()
		}
	}

	return unit{
		kind: tok,
		names: names,
		body: sp,
		objs: []any{sp},
		start: start,
		end: end,
	}
}

// unitOf returns d as a unit, or false if the reordering does not move d.
func unitOf(d ast.Decl) (unit, bool) {
	switch d := d.(type) {
	case *ast.FuncDecl:
		if d.Body == nil || d.Recv == nil && d.Name.Name == "init" {
			return unit{}, false
		}

		if d.Recv != nil {
			recv := receiverType(d.Recv)
			if recv == "" {
				return unit{}, false
			}

			return unit{
				kind: token.FUNC,
				method: true,
				names: []string{recv + "." + d.Name.Name},
				body: d.Body,
				objs: []any{d},
				start: declStart(d),
				end: d.End(),
				methodName: d.Name.Name,
				sels: selectorNames(d.Body),
			}, true
		}

		return unit{
			kind: token.FUNC,
			names: []string{d.Name.Name},
			body: d.Body,
			objs: []any{d},
			start: declStart(d),
			end: d.End(),
		}, true
	}
	// const, var and type declarations are reordered by reorderSpecRuns
	return unit{}, false
}

// predeclaredPure are the predeclared types, whose conversions have no side
// effects.
var predeclaredPure = map[string]bool{
	"bool": true,
	"string": true,
	"byte": true,
	"rune": true,
	"uintptr": true,
	"int": true,
	"int8": true,
	"int16": true,
	"int32": true,
	"int64": true,
	"uint": true,
	"uint8": true,
	"uint16": true,
	"uint32": true,
	"uint64": true,
	"float32": true,
	"float64": true,
	"complex64": true,
	"complex128": true,
}

// reorderSource returns src with the passes of reordering applied: first the
// specs of type groups, then the runs of declarations. src is returned as it is
// if nothing moves.
func reorderSource(filename string, src []byte) []byte {
	passes := []func(*token.FileSet, *ast.File, []byte) []byte{
		func(fset *token.FileSet, file *ast.File, src []byte) []byte {
			return hoistTypes(fset, file, src, siblingTypes(filename, file.Name.Name))
		},
		reorderSpecRuns,
		reorderDecls,
		reorderAssignRuns,
	}

	for _, pass := range passes {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, filename, src, parser.ParseComments)
		if err != nil {
			return src
		}

		src = pass(fset, file, src)
	}

	return src
}

func (l layout) line(p token.Pos) int {
	return l.tf.Line(p)
}

// lineEnd returns the offset just past the newline that ends the line containing
// offset o, or len(src) if there is none.
func (l layout) lineEnd(o int) int {
	for o < len(l.src) && l.src[o] != '\n' {
		o++
	}

	if o < len(l.src) {
		return o + 1
	}

	return len(l.src)
}

// lineStart returns the offset of the start of the line containing offset o.
func (l layout) lineStart(o int) int {
	for o > 0 && l.src[o-1] != '\n' {
		o--
	}

	return o
}

func (l layout) off(p token.Pos) int {
	return l.tf.Offset(p)
}

// commentIn reports whether a comment of the file starts in the offsets [from, to).
func (l layout) commentIn(from, to int) bool {
	for _, cg := range l.comments {
		for _, c := range cg.List {
			if o := l.off(c.Pos()); o >= from && o < to {
				return true
			}
		}
	}

	return false
}

// applyEdits returns src with the edits applied. They must be in source order
// and must not overlap.
func applyEdits(src []byte, edits []edit) []byte {
	if len(edits) == 0 {
		return src
	}

	var (
		out []byte
		prev int
	)

	for _, e := range edits {
		out = append(out, src[prev:e.start]...)
		out = append(out, e.text...)
		prev = e.end
	}

	return append(out, src[prev:]...)
}

// reorderItems returns the edit that reorders run, the units between lo and hi
// in the source, or reports false if the run is already in order. Unit t
// occupies the lines from the line after unit t-1 ends, so the comments before
// it travel with it.
func (l layout) reorderItems(run []unit, lo, hi int) (edit, bool) {
	k := len(run)
	starts := make([]int, k+1)
	starts[0] = lo

	for t := 1; t < k; t++ {
		starts[t] = l.lineEnd(l.off(run[t-1].end))
	}

	starts[k] = hi
	order := dependencyOrder(run)
	identity := true

	for t, o := range order {
		if t != o {
			identity = false
			break
		}
	}

	if identity {
		return edit{}, false
	}

	var text []byte

	for _, t := range order {
		text = append(text, l.src[starts[t]:starts[t+1]]...)
	}

	return edit{
		start: lo,
		end: hi,
		text: text,
	}, true
}

// receiverType returns the name of the type a method is declared on: the
// receiver's type without its pointer, parentheses and type arguments.
func receiverType(recv *ast.FieldList) string {
	if recv == nil || len(recv.List) == 0 {
		return ""
	}

	t := recv.List[0].Type

	for {
		switch x := t.(type) {
		case *ast.StarExpr:
			t = x.X
		case *ast.ParenExpr:
			t = x.X
		case *ast.IndexExpr:
			t = x.X
		case *ast.IndexListExpr:
			t = x.X
		case *ast.Ident:
			return x.Name
		default:
			return ""
		}
	}
}

// runBounds returns the edit for the run decls[i:j], whose units are run, or
// reports false if the run has no room to move: a declaration before or after
// it shares a line with it.
func runBounds(l layout, file *ast.File, decls []ast.Decl, i, j int, run []unit) (edit, bool) {
	var lo int

	if i == 0 {
		if l.line(file.Name.End()) == l.line(run[0].start) {
			return edit{}, false
		}

		lo = l.lineEnd(l.off(file.Name.End()))
	} else {
		prevEnd := decls[i-1].End()
		if l.line(prevEnd) == l.line(run[0].start) {
			return edit{}, false
		}

		lo = l.lineEnd(l.off(prevEnd))
	}

	hi := len(l.src)

	if j < len(decls) {
		next := declStart(decls[j])
		if l.line(run[len(run)-1].end) == l.line(next) {
			return edit{}, false
		}

		if _, ok := unitOf(decls[j]); ok {
			// the next declaration starts a run of its own, which takes the
			// lines between: this run ends with its own last line
			hi = l.lineEnd(l.off(run[len(run)-1].end))
		} else {
			hi = l.off(next)
		}
	}

	return l.reorderItems(run, lo, hi)
}

// sameRun reports whether two units can be in one run: both are functions, or
// both are methods, or both are of the same declaration token.
func sameRun(a, b unit) bool {
	return a.kind == b.kind && a.method == b.method
}

// reorderDecls is the pass over the runs of declarations.
func reorderDecls(fset *token.FileSet, file *ast.File, src []byte) []byte {
	tf := fset.File(file.Pos())
	if tf == nil {
		return src
	}

	decls := file.Decls
	l := layout{
		tf: tf,
		src: src,
		comments: file.Comments,
	}

	var edits []edit

	for i := 0; i < len(decls); {
		u, ok := unitOf(decls[i])
		if !ok {
			i++
			continue
		}

		j := i + 1
		run := []unit{u}

		for j < len(decls) {
			next, ok := unitOf(decls[j])
			// a run is laid out line by line, so two declarations on one line end it
			if !ok || !sameRun(next, run[0]) || l.line(run[len(run)-1].end) == l.line(next.start) {
				break
			}

			run = append(run, next)
			j++
		}

		if e, ok := runBounds(l, file, decls, i, j, run); ok {
			edits = append(edits, e)
		}

		i = j
	}

	return applyEdits(src, edits)
}

// selectorNames returns the names that n selects, as in x.name.
func selectorNames(n ast.Node) []string {
	var out []string

	ast.Inspect(n, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			out = append(out, sel.Sel.Name)
		}

		return true
	})
	return out
}

// universeTypes are the predeclared type names.
var universeTypes = map[string]bool{
	"any": true,
	"bool": true,
	"byte": true,
	"comparable": true,
	"complex64": true,
	"complex128": true,
	"error": true,
	"float32": true,
	"float64": true,
	"int": true,
	"int8": true,
	"int16": true,
	"int32": true,
	"int64": true,
	"rune": true,
	"string": true,
	"uint": true,
	"uint8": true,
	"uint16": true,
	"uint32": true,
	"uint64": true,
	"uintptr": true,
}

// alphabeticalOrder orders the components of a run of types. A component is
// ready when every component it depends on has been placed; among the ready
// ones, the one whose first name (in alphabetical order) is smallest goes next.
// So types are sorted wherever the dependencies allow it, and a type always
// comes after the types it refers to.
func alphabeticalOrder(run []unit, deps, comps [][]int) []int {
	compOf := make([]int, len(run))

	for c, members := range comps {
		for _, t := range members {
			compOf[t] = c
		}
	}

	need := make([]int, len(comps)) // dependencies not yet placed
	dependents := make([][]int, len(comps))
	type edge struct {
		from, to int
	}
	seen := map[edge]bool{}

	for t := range run {
		for _, u := range deps[t] {
			if a, b := compOf[t], compOf[u]; a != b && !seen[edge{a, b}] {
				seen[edge{a, b}] = true
				need[a]++
				dependents[b] = append(dependents[b], a)
			}
		}
	}

	key := make([]string, len(comps))

	for c, members := range comps {
		for _, t := range members {
			for _, n := range run[t].names {
				if key[c] == "" || n < key[c] {
					key[c] = n
				}
			}
		}
	}

	placed := make([]bool, len(comps))

	var order []int

	for range comps {
		best := -1

		for c := range comps {
			if !placed[c] && need[c] == 0 && (best < 0 || key[c] < key[best]) {
				best = c
			}
		}

		placed[best] = true
		order = append(order, comps[best]...)

		for _, a := range dependents[best] {
			need[a]--
		}
	}

	return order
}

// assignable reports whether s can be part of a run of := statements.
func assignable(s ast.Stmt) bool {
	as, ok := s.(*ast.AssignStmt)

	return ok && as.Tok == token.DEFINE && pureExpr(as, false)
}

// components returns the strongly connected components of the graph deps, in the
// order Tarjan's algorithm emits them: each after the components it depends on.
// The units of a component are in source order.
func components(deps [][]int) [][]int {
	num := make([]int, len(deps)) // discovery number, or -1 before discovery
	low := make([]int, len(deps)) // lowest number reachable from the unit
	onStack := make([]bool, len(deps))

	for t := range num {
		num[t] = -1
	}

	var (
		stack []int
		counter int
		comps [][]int
		connect func(t int)
	)

	connect = func(t int) {
		num[t], low[t] = counter, counter
		counter++
		stack = append(stack, t)
		onStack[t] = true

		for _, u := range deps[t] {
			if num[u] < 0 {
				connect(u)

				low[t] = min(low[t], low[u])
			} else if onStack[u] {
				low[t] = min(low[t], num[u])
			}
		}

		if low[t] == num[t] {
			var comp []int

			for {
				w := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				onStack[w] = false
				comp = append(comp, w)

				if w == t {
					break
				}
			}
			sort.Ints(comp)

			comps = append(comps, comp)
		}
	}

	for t := range deps {
		if num[t] < 0 {
			connect(t)
		}
	}

	return comps
}

// declStart returns where d starts, including its doc comment.
func declStart(d ast.Decl) token.Pos {
	var doc *ast.CommentGroup

	switch d := d.(type) {
	case *ast.FuncDecl:
		doc = d.Doc
	case *ast.GenDecl:
		doc = d.Doc
	}

	if doc != nil {
		return doc.Pos()
	}

	return d.Pos()
}

// siblingTypes returns the names of the types that other files of the package of
// filename declare. A file's package is the files of its directory with the same
// package name. A name that a sibling declares as a type is a type, so a type may
// use it and still move; a constant, variable or function of a sibling is not.
func siblingTypes(filename, pkg string) map[string]bool {
	types := map[string]bool{}
	dir := filepath.Dir(filename)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return types
	}
	self, _ := filepath.Abs(filename)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".go" {
			continue
		}
		path := filepath.Join(dir, name)
		if abs, _ := filepath.Abs(path); abs == self {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if err != nil || f.Name.Name != pkg {
			continue
		}
		for _, d := range f.Decls {
			if g, ok := d.(*ast.GenDecl); ok && g.Tok == token.TYPE {
				for _, sp := range g.Specs {
					types[sp.(*ast.TypeSpec).Name.Name] = true
				}
			}
		}
	}
	return types
}

// hoistTypes is the pass that moves type declarations to the top of the file,
// after the imports. Only a type whose names are all types (of this file, or
// predeclared), imported packages, or its own type parameters moves: a name
// that is a constant, variable or function, or that cannot be resolved in this
// file, keeps the type where it is, and so does every type that depends on such
// a type. Declaration order at package level does not change the meaning of a
// type, so a move is safe; the restriction keeps a type with a constant length
// below its constant.
func hoistTypes(fset *token.FileSet, file *ast.File, src []byte, external map[string]bool) []byte {
	tf := fset.File(file.Pos())
	if tf == nil {
		return src
	}

	decls := file.Decls

	// imports, by the name each package is referred to with
	imports := map[string]bool{}
	l := layout{
		tf: tf,
		src: src,
		comments: file.Comments,
	}

	var m int // index of the first declaration after the imports
	for ; m < len(decls); m++ {
		g, ok := decls[m].(*ast.GenDecl)
		if !ok || g.Tok != token.IMPORT {
			break
		}

		for _, sp := range g.Specs {
			is := sp.(*ast.ImportSpec)

			var name string

			if is.Name != nil {
				name = is.Name.Name
			} else if path, err := strconv.Unquote(is.Path.Value); err == nil {
				name = path[strings.LastIndex(path, "/")+1:]
			}

			if name != "" && name != "_" && name != "." {
				imports[name] = true
			}
		}
	}
	// P is where moved types go: the line after the imports, or after the
	// package clause if there are none
	P := l.lineEnd(l.off(file.Name.End()))

	if m > 0 {
		P = l.lineEnd(l.off(decls[m-1].End()))
	}

	// the candidates are the type declarations; specIdx finds a declaration by
	// one of its specs
	cand := map[int]bool{}
	specIdx := map[any]int{}

	for i, d := range decls {
		if g, ok := d.(*ast.GenDecl); ok && g.Tok == token.TYPE {
			cand[i] = true

			for _, sp := range g.Specs {
				specIdx[sp] = i
			}
		}
	}

	// bad[i] is set for a declaration that uses a name which is neither a type
	// nor an import; deps[i] lists the candidate declarations it uses
	bad := map[int]bool{}
	deps := map[int][]int{}

	for i := range cand {
		seen := map[int]bool{}
		visit := func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok {
				return true
			}

			if id.Obj == nil {
				if !universeTypes[id.Name] && !imports[id.Name] && !external[id.Name] {
					bad[i] = true
				}

				return true
			}

			switch d := id.Obj.Decl.(type) {
			case *ast.TypeSpec:
				if j, ok := specIdx[d]; ok && !seen[j] {
					seen[j] = true
					deps[i] = append(deps[i], j)
				}
			case *ast.Field:
				// a type parameter
			default:
				bad[i] = true
			}

			return true
		}

		var walk func(ast.Node) bool

		walk = func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.SelectorExpr:
				// only the package name of pkg.T is a use; T is not
				ast.Inspect(x.X, walk)
				return false
			case *ast.Field:
				// a field name is not a use; its type is
				if x.Type != nil {
					ast.Inspect(x.Type, walk)
				}

				return false
			}

			return visit(n)
		}

		ast.Inspect(decls[i], walk)
	}
	// a declaration with a bad dependency is bad too, and so is one that depends
	// on a bad declaration
	for changed := true; changed; {
		changed = false

		for i := range cand {
			if bad[i] {
				continue
			}

			for _, j := range deps[i] {
				if bad[j] {
					bad[i] = true
					changed = true
					break
				}
			}
		}
	}

	// a run of types already at the top stays where it is
	top := m

	for top < len(decls) && cand[top] {
		top++
	}

	var moved []int

	for i := top; i < len(decls); i++ {
		if !cand[i] || bad[i] {
			continue
		}
		// a declaration moves with its own lines: nothing may share them
		if i > 0 && l.line(decls[i-1].End()) == l.line(declStart(decls[i])) {
			continue
		}

		if i+1 < len(decls) && l.line(decls[i].End()) == l.line(declStart(decls[i+1])) {
			continue
		}

		moved = append(moved, i)
	}

	if len(moved) == 0 {
		return src
	}

	// the moved declarations go in at P; each is cut out of its place with the
	// comments that lead into it
	var text []byte

	edits := []edit{
		{
			start: P,
			end: P,
		},
	}

	for _, i := range moved {
		start := P

		if i > 0 {
			start = l.lineEnd(l.off(decls[i-1].End()))
		}

		end := l.lineEnd(l.off(decls[i].End()))
		text = append(text, src[start:end]...)
		edits = append(edits, edit{
			start: start,
			end: end,
		})
	}

	edits[0].text = text

	return applyEdits(src, edits)
}

// references returns the indices of the units that n refers to. An identifier
// refers to a unit if it resolves to one of the unit's declarations, or, when it
// is not resolved at all, if it has the name of one of the units. The selector
// in x.f, the name of a field, the key of a struct literal field and labels are
// not references.
func references(n ast.Node, byName map[string]int, byObj map[any]int) []int {
	var (
		out []int
		visit func(ast.Node) bool
	)

	visit = func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectorExpr:
			ast.Inspect(x.X, visit)
			return false
		case *ast.Field:
			if x.Type != nil {
				ast.Inspect(x.Type, visit)
			}

			return false
		case *ast.KeyValueExpr:
			if _, isName := x.Key.(*ast.Ident); !isName {
				ast.Inspect(x.Key, visit)
			}
			ast.Inspect(x.Value, visit)
			return false
		case *ast.LabeledStmt:
			ast.Inspect(x.Stmt, visit)
			return false
		case *ast.BranchStmt:
			return false
		case *ast.Ident:
			var (
				t int
				ok bool
			)

			if x.Obj == nil {
				t, ok = byName[x.Name]
			} else {
				t, ok = byObj[x.Obj.Decl]
			}

			if ok {
				out = append(out, t)
			}
		}

		return true
	}

	ast.Inspect(n, visit)
	return out
}

// dependencyOrder returns the indices of run in an order where each unit comes
// after the units it refers to, and otherwise in alphabetical order by name (see
// alphabeticalOrder). The units are grouped into strongly connected components,
// that is, sets of units that refer to each other, directly or through others;
// the units of one component keep their source order.
func dependencyOrder(run []unit) []int {
	byName := map[string]int{} // for names that are not resolved
	byObj := map[any]int{} // for names resolved to a unit's declaration
	for t, u := range run {
		for _, n := range u.names {
			byName[n] = t
		}

		for _, o := range u.objs {
			byObj[o] = t
		}
	}
	// a method may call another method by name; that is a dependency too
	byMethod := map[string][]int{}

	for t, u := range run {
		if u.method {
			byMethod[u.methodName] = append(byMethod[u.methodName], t)
		}
	}

	deps := make([][]int, len(run))

	for t, u := range run {
		seen := map[int]bool{}
		refs := references(u.body, byName, byObj)

		for _, name := range u.sels {
			refs = append(refs, byMethod[name]...)
		}

		for _, v := range refs {
			if v != t && !seen[v] {
				seen[v] = true
				deps[t] = append(deps[t], v)
			}
		}
		sort.Ints(deps[t])
	}

	return alphabeticalOrder(run, deps, components(deps))
}

// reorderAssignRuns is the pass over the runs of := statements. A run is a
// sequence of consecutive := statements with pure values (see pureExpr), each on
// a line of its own. Within a run, statements that do not depend on each other
// are sorted by the names they declare, and a statement stays after an earlier
// one it could interact with (see assignDeps). It runs over every block of the
// file, and over the clauses of switch and select statements.
func reorderAssignRuns(fset *token.FileSet, file *ast.File, src []byte) []byte {
	tf := fset.File(file.Pos())
	if tf == nil {
		return src
	}

	l := layout{
		tf: tf,
		src: src,
		comments: file.Comments,
	}

	var edits []edit

	ast.Inspect(file, func(n ast.Node) bool {
		switch b := n.(type) {
		case *ast.BlockStmt:
			edits = append(edits, l.assignRunEdits(b.List, b.Lbrace)...)
		case *ast.CaseClause:
			edits = append(edits, l.assignRunEdits(b.Body, b.Colon)...)
		case *ast.CommClause:
			edits = append(edits, l.assignRunEdits(b.Body, b.Colon)...)
		}

		return true
	})
	sort.Slice(edits, func(i, j int) bool {
		return edits[i].start < edits[j].start
	})
	return applyEdits(src, edits)
}

// specTextFrom returns lead followed by a single declaration of sp: the keyword
// kw, then the spec's own text, through the end of its line.
func specTextFrom(src, lead []byte, sp ast.Spec, kw string, l layout) []byte {
	out := append([]byte{}, lead...)
	out = append(out, kw...)

	return append(out, src[l.off(sp.Pos()):l.lineEnd(l.off(sp.End()))]...)
}

// specText returns the text of a single declaration of spec sp, which starts with
// the lines src[from:keyword] and whose keyword is at keyword.
func specText(src []byte, from int, keyword token.Pos, sp ast.Spec, kw string, l layout) []byte {
	return specTextFrom(src, src[from:l.off(keyword)], sp, kw, l)
}

// specRunEdit returns the edit that rewrites the declarations decls[i:j] as
// single declarations in their new order, or reports false if the run is left
// alone.
func specRunEdit(l layout, file *ast.File, decls []ast.Decl, i, j int) (edit, bool) {
	src := l.src
	// the run's extent: the lines after the declaration before it (or the
	// package clause) to the end of its last declaration
	var lo int

	if i == 0 {
		if l.line(file.Name.End()) == l.line(declStart(decls[0])) {
			return edit{}, false
		}

		lo = l.lineEnd(l.off(file.Name.End()))
	} else {
		if l.line(decls[i-1].End()) == l.line(declStart(decls[i])) {
			return edit{}, false
		}

		lo = l.lineEnd(l.off(decls[i-1].End()))
	}

	last := decls[j-1].End()
	hi := l.lineEnd(l.off(last))

	if j < len(decls) && l.line(last) == l.line(declStart(decls[j])) {
		return edit{}, false
	}

	// one unit per spec. text is what the unit prints as: the lines that lead
	// into it, then a single declaration of the spec.
	var run []unit

	var texts [][]byte

	cut := lo

	for _, d := range decls[i:j] {
		g := d.(*ast.GenDecl)
		keyword := g.Tok.String() + " "

		if !g.Lparen.IsValid() {
			sp := g.Specs[0]
			run = append(run, specUnit(g.Tok, sp))
			texts = append(texts, specText(src, cut, g.TokPos, sp, keyword, l))
			cut = l.lineEnd(l.off(g.End()))
			continue
		}
		// a group: its own lines, and any comment on them, are not carried over

		if lastEnd := l.off(g.Specs[len(g.Specs)-1].End()); l.commentIn(l.off(g.TokPos), l.lineEnd(l.off(g.Lparen))) || l.commentIn(lastEnd, l.off(g.Rparen)+1) {
			return edit{}, false
		}

		groupCut := l.lineEnd(l.off(g.Lparen))

		for k, sp := range g.Specs {
			run = append(run, specUnit(g.Tok, sp))

			if k == 0 {
				// the lines before the keyword lead into the first spec too
				lead := append(append([]byte{}, src[cut:l.off(g.TokPos)]...), src[groupCut:l.off(sp.Pos())]...)
				texts = append(texts, specTextFrom(src, lead, sp, keyword, l))
			} else {
				texts = append(texts, specTextFrom(src, src[groupCut:l.off(sp.Pos())], sp, keyword, l))
			}

			groupCut = l.lineEnd(l.off(sp.End()))
		}

		cut = l.lineEnd(l.off(g.End()))
	}

	order := dependencyOrder(run)
	identity := true

	for t, o := range order {
		if t != o {
			identity = false
			break
		}
	}

	if identity && j-i == 1 {
		return edit{}, false // a single declaration, or a group, already in order
	}

	var text []byte

	for _, t := range order {
		text = append(text, texts[t]...)
	}

	return edit{
		start: lo,
		end: hi,
		text: text,
	}, true
}

// reorderSpecRuns is the pass over the runs of const, var and type declarations
// (see specRunToken). A run is rewritten as single declarations, one per spec, in
// dependency order, and the printer then joins them into blocks. Within a run,
// specs that do not depend on each other are sorted alphabetically (see
// alphabeticalOrder). A group is opened and closed by its own lines, and a
// comment on those lines is not carried over, so a run with such a comment is
// left alone.
func reorderSpecRuns(fset *token.FileSet, file *ast.File, src []byte) []byte {
	tf := fset.File(file.Pos())
	if tf == nil {
		return src
	}

	decls := file.Decls
	l := layout{
		tf: tf,
		src: src,
		comments: file.Comments,
	}

	var edits []edit

	for i := 0; i < len(decls); {
		tok := specRunToken(decls[i])
		if tok == token.ILLEGAL {
			i++
			continue
		}

		j := i + 1

		for j < len(decls) && specRunToken(decls[j]) == tok {
			j++
		}

		if e, ok := specRunEdit(l, file, decls, i, j); ok {
			edits = append(edits, e)
		}

		i = j
	}

	return applyEdits(src, edits)
}

// assignRun returns the edit that reorders the statements list[i:j], or reports
// false if the run is already in order or has no room to move.
func (l layout) assignRun(list []ast.Stmt, i, j int, head token.Pos) (edit, bool) {
	var lo int

	if i == 0 {
		if l.line(head) == l.line(list[0].Pos()) {
			return edit{}, false
		}

		lo = l.lineEnd(l.off(head))
	} else {
		if l.line(list[i-1].End()) == l.line(list[i].Pos()) {
			return edit{}, false
		}

		lo = l.lineEnd(l.off(list[i-1].End()))
	}

	if j < len(list) && l.line(list[j-1].End()) == l.line(list[j].Pos()) {
		return edit{}, false
	}

	hi := l.lineEnd(l.off(list[j-1].End()))
	cut := lo
	run := make([]unit, 0, j-i)
	texts := make([][]byte, 0, j-i)

	for _, s := range list[i:j] {
		as := s.(*ast.AssignStmt)

		var names []string

		for _, lhs := range as.Lhs {
			if id, ok := lhs.(*ast.Ident); ok && id.Name != "_" {
				names = append(names, id.Name)
			}
		}

		run = append(run, unit{
			kind: token.DEFINE,
			names: names,
			body: as,
			objs: []any{as},
			start: as.Pos(),
			end: as.End(),
		})
		// the lines before the statement lead into it; then the statement itself,
		// through the end of its line
		texts = append(texts, append(append([]byte{}, l.src[cut:l.off(as.Pos())]...), l.src[l.off(as.Pos()):l.lineEnd(l.off(as.End()))]...))
		cut = l.lineEnd(l.off(as.End()))
	}

	deps := assignDeps(run)
	order := alphabeticalOrder(run, deps, components(deps))
	identity := true

	for t, o := range order {
		if t != o {
			identity = false
			break
		}
	}

	if identity {
		return edit{}, false
	}

	var text []byte

	for _, t := range order {
		text = append(text, texts[t]...)
	}

	return edit{
		start: lo,
		end: hi,
		text: text,
	}, true
}

// assignRunEdits returns the edits that reorder each run of := statements in
// list. head is the position of the token that opens the list, a brace or a
// colon.
func (l layout) assignRunEdits(list []ast.Stmt, head token.Pos) []edit {
	var edits []edit

	for i := 0; i < len(list); {
		if !assignable(list[i]) {
			i++
			continue
		}

		j := i + 1

		for j < len(list) && assignable(list[j]) && l.line(list[j-1].End()) != l.line(list[j].Pos()) {
			j++
		}

		if e, ok := l.assignRun(list, i, j, head); ok {
			edits = append(edits, e)
		}

		i = j
	}

	return edits
}

// assignDeps returns the dependencies of a run of := statements, by name. A
// statement j depends on an earlier statement i when one of them declares a name
// that the other uses or declares. Names are compared as spelled, since a
// declaration can shadow a name that an earlier statement reads from outside the
// run.
func assignDeps(run []unit) [][]int {
	mentions := make([]map[string]bool, len(run))
	declares := make([]map[string]bool, len(run))

	for t, u := range run {
		mentions[t] = map[string]bool{}

		ast.Inspect(u.body, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok {
				mentions[t][id.Name] = true
			}

			return true
		})

		declares[t] = map[string]bool{}

		for _, n := range u.names {
			declares[t][n] = true
		}
	}

	overlap := func(i, j int) bool {
		for n := range declares[i] {
			if mentions[j][n] || declares[j][n] {
				return true
			}
		}

		for n := range declares[j] {
			if mentions[i][n] {
				return true
			}
		}

		return false
	}
	deps := make([][]int, len(run))

	for j := range run {
		for i := 0; i < j; i++ {
			if overlap(i, j) {
				deps[j] = append(deps[j], i)
			}
		}
	}

	return deps
}

// usesIota reports whether a declaration mentions iota.
func usesIota(d ast.Node) bool {
	var found bool

	ast.Inspect(d, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == "iota" {
			found = true
		}

		return !found
	})
	return found
}
