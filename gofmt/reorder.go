package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strconv"
	"strings"
)

// This file implements the reordering of -strict. A declaration is moved below
// the declarations it depends on, so that dependencies come first. Two kinds of
// move are made, each as a pass over the source:
//
//   - the specs of a type group are reordered among themselves;
//   - in each run of consecutive declarations of one kind, a declaration is
//     moved below the ones it refers to. Runs are of plain functions, of const
//     declarations, of var declarations whose values are free of side effects,
//     and of type declarations. A group of const or var declarations is one
//     unit, since its specs depend on each other through iota or on order of
//     evaluation.
//
// Methods, init functions and body-less functions are never moved and end a
// run, as does any other kind of declaration. A var with a side-effecting
// initialiser is never moved either (see pureVars).
//
// A pass works on the source text. Each unit, with the comments that come before
// it on its own lines, is cut out as a segment; the segments are put back in the
// new order. Comments therefore travel with the declaration they lead into, and
// the rest of the file is left as it was. reorderSource runs the passes in turn,
// parsing the result again after each.

// unit is a declaration, or a spec of a type group, that is moved as a whole.
type unit struct {
	kind  token.Token // token.FUNC for a function, otherwise the token of the declaration
	names []string    // the names it declares
	body  ast.Node    // the part whose references count as dependencies
	objs  []any       // the declarations that resolved references may point to
	start token.Pos   // where it starts, including its doc comment
	end   token.Pos   // where it ends
}

// unitOf returns d as a unit, or false if the reordering does not move d.
func unitOf(d ast.Decl) (unit, bool) {
	switch d := d.(type) {
	case *ast.FuncDecl:
		if d.Recv != nil || d.Name.Name == "init" || d.Body == nil {
			return unit{}, false
		}
		return unit{kind: token.FUNC, names: []string{d.Name.Name}, body: d.Body,
			objs: []any{d}, start: declStart(d), end: d.End()}, true
	case *ast.GenDecl:
		switch d.Tok {
		case token.CONST:
		case token.VAR:
			if !pureVars(d) {
				return unit{}, false
			}
		default:
			// types are reordered by reorderTypeRuns
			return unit{}, false
		}
		var names []string
		var objs []any
		for _, sp := range d.Specs {
			objs = append(objs, sp)
			switch sp := sp.(type) {
			case *ast.ValueSpec:
				for _, id := range sp.Names {
					names = append(names, id.Name)
				}
			case *ast.TypeSpec:
				names = append(names, sp.Name.Name)
			}
		}
		return unit{kind: d.Tok, names: names, body: d, objs: objs, start: declStart(d), end: d.End()}, true
	}
	return unit{}, false
}

// specUnit returns spec s of a type group as a unit.
func specUnit(s *ast.TypeSpec) unit {
	start := s.Pos()
	if s.Doc != nil {
		start = s.Doc.Pos()
	}
	return unit{kind: token.TYPE, names: []string{s.Name.Name}, body: s, objs: []any{s},
		start: start, end: s.End()}
}

// pureVars reports whether the values of a var declaration can be evaluated
// without side effects, so that moving it does not change the order of any
// effect. Go initialises package variables in declaration order, except where a
// dependency forces another order, so a move is visible only through a side
// effect. A call can have one, and a receive can block; a conversion to a
// predeclared type, make and new cannot. Calls are checked syntactically, so a
// call to a function named like a predeclared type is taken to be impure.
func pureVars(d *ast.GenDecl) bool {
	pure := true
	ast.Inspect(d, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CallExpr:
			id, ok := x.Fun.(*ast.Ident)
			if !ok || !(predeclaredPure[id.Name] || id.Name == "make" || id.Name == "new") {
				pure = false
			}
		case *ast.UnaryExpr:
			if x.Op == token.ARROW {
				pure = false
			}
		}
		return pure
	})
	return pure
}

// predeclaredPure are the predeclared types, whose conversions have no side
// effects.
var predeclaredPure = map[string]bool{
	"bool": true, "string": true, "byte": true, "rune": true, "uintptr": true,
	"int": true, "int8": true, "int16": true, "int32": true, "int64": true,
	"uint": true, "uint8": true, "uint16": true, "uint32": true, "uint64": true,
	"float32": true, "float64": true, "complex64": true, "complex128": true,
}

// reorderSource returns src with the passes of reordering applied: first the
// specs of type groups, then the runs of declarations. src is returned as it is
// if nothing moves.
func reorderSource(filename string, src []byte) []byte {
	passes := []func(*token.FileSet, *ast.File, []byte) []byte{hoistTypes, reorderTypeRuns, reorderDecls}
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

// layout gives positions of a file as offsets into its source.
type layout struct {
	tf       *token.File
	src      []byte
	comments []*ast.CommentGroup
}

func (l layout) off(p token.Pos) int { return l.tf.Offset(p) }

func (l layout) line(p token.Pos) int { return l.tf.Line(p) }

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

// lineStart returns the offset of the start of the line containing offset o.
func (l layout) lineStart(o int) int {
	for o > 0 && l.src[o-1] != '\n' {
		o--
	}
	return o
}

// edit replaces src[start:end] with text.
type edit struct {
	start, end int
	text       []byte
}

// applyEdits returns src with the edits applied. They must be in source order
// and must not overlap.
func applyEdits(src []byte, edits []edit) []byte {
	if len(edits) == 0 {
		return src
	}
	var out []byte
	prev := 0
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
	return edit{start: lo, end: hi, text: text}, true
}

// reorderDecls is the pass over the runs of declarations.
func reorderDecls(fset *token.FileSet, file *ast.File, src []byte) []byte {
	tf := fset.File(file.Pos())
	if tf == nil {
		return src
	}
	l := layout{tf: tf, src: src, comments: file.Comments}
	decls := file.Decls
	var edits []edit
	for i := 0; i < len(decls); {
		u, ok := unitOf(decls[i])
		if !ok {
			i++
			continue
		}
		run := []unit{u}
		j := i + 1
		for j < len(decls) {
			next, ok := unitOf(decls[j])
			// a run is laid out line by line, so two declarations on one line end it
			if !ok || next.kind != run[0].kind || l.line(run[len(run)-1].end) == l.line(next.start) {
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
		hi = l.off(next)
	}
	return l.reorderItems(run, lo, hi)
}

// universeTypes are the predeclared type names.
var universeTypes = map[string]bool{
	"any": true, "bool": true, "byte": true, "comparable": true, "complex64": true,
	"complex128": true, "error": true, "float32": true, "float64": true, "int": true,
	"int8": true, "int16": true, "int32": true, "int64": true, "rune": true,
	"string": true, "uint": true, "uint8": true, "uint16": true, "uint32": true,
	"uint64": true, "uintptr": true,
}

// hoistTypes is the pass that moves type declarations to the top of the file,
// after the imports. Only a type whose names are all types (of this file, or
// predeclared), imported packages, or its own type parameters moves: a name
// that is a constant, variable or function, or that cannot be resolved in this
// file, keeps the type where it is, and so does every type that depends on such
// a type. Declaration order at package level does not change the meaning of a
// type, so a move is safe; the restriction keeps a type with a constant length
// below its constant.
func hoistTypes(fset *token.FileSet, file *ast.File, src []byte) []byte {
	tf := fset.File(file.Pos())
	if tf == nil {
		return src
	}
	l := layout{tf: tf, src: src, comments: file.Comments}
	decls := file.Decls

	// imports, by the name each package is referred to with
	imports := map[string]bool{}
	m := 0 // index of the first declaration after the imports
	for ; m < len(decls); m++ {
		g, ok := decls[m].(*ast.GenDecl)
		if !ok || g.Tok != token.IMPORT {
			break
		}
		for _, sp := range g.Specs {
			is := sp.(*ast.ImportSpec)
			name := ""
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
				if !universeTypes[id.Name] && !imports[id.Name] {
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
	edits := []edit{{start: P, end: P}}
	for _, i := range moved {
		start := P
		if i > 0 {
			start = l.lineEnd(l.off(decls[i-1].End()))
		}
		end := l.lineEnd(l.off(decls[i].End()))
		text = append(text, src[start:end]...)
		edits = append(edits, edit{start: start, end: end})
	}
	edits[0].text = text
	return applyEdits(src, edits)
}

// reorderTypeRuns is the pass over the runs of type declarations. A run is
// rewritten as single type declarations, one per spec, in dependency order, and
// the printer then joins them into one type block. Within a run, types that do
// not depend on each other are sorted alphabetically (see alphabeticalOrder).
// A group is opened and closed by its own lines, and a comment on those lines
// is not carried over, so a run with such a comment is left alone.
func reorderTypeRuns(fset *token.FileSet, file *ast.File, src []byte) []byte {
	tf := fset.File(file.Pos())
	if tf == nil {
		return src
	}
	l := layout{tf: tf, src: src, comments: file.Comments}
	decls := file.Decls
	var edits []edit
	for i := 0; i < len(decls); {
		if !isTypeDecl(decls[i]) {
			i++
			continue
		}
		j := i
		for j < len(decls) && isTypeDecl(decls[j]) {
			j++
		}
		if e, ok := typeRunEdit(l, file, decls, i, j); ok {
			edits = append(edits, e)
		}
		i = j
	}
	return applyEdits(src, edits)
}

// isTypeDecl reports whether d is a type declaration, single or grouped.
func isTypeDecl(d ast.Decl) bool {
	g, ok := d.(*ast.GenDecl)
	return ok && g.Tok == token.TYPE
}

// typeRunEdit returns the edit that rewrites the type declarations decls[i:j]
// as single declarations in their new order, or reports false if the run is
// left alone.
func typeRunEdit(l layout, file *ast.File, decls []ast.Decl, i, j int) (edit, bool) {
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
	if j < len(decls) {
		if l.line(last) == l.line(declStart(decls[j])) {
			return edit{}, false
		}
	}

	// one unit per spec. text is what the unit prints as: the lines that lead
	// into it, then a single declaration of the spec.
	var run []unit
	var texts [][]byte
	cut := lo
	for _, d := range decls[i:j] {
		g := d.(*ast.GenDecl)
		if !g.Lparen.IsValid() {
			sp := g.Specs[0].(*ast.TypeSpec)
			run = append(run, specUnit(sp))
			texts = append(texts, typeText(src, cut, g.TokPos, sp, l))
			cut = l.lineEnd(l.off(g.End()))
			continue
		}
		// a group: its own lines, and any comment on them, are not carried over
		lastEnd := l.off(g.Specs[len(g.Specs)-1].End())
		if l.commentIn(l.off(g.TokPos), l.lineEnd(l.off(g.Lparen))) || l.commentIn(lastEnd, l.off(g.Rparen)+1) {
			return edit{}, false
		}
		groupCut := l.lineEnd(l.off(g.Lparen))
		for k, s := range g.Specs {
			sp := s.(*ast.TypeSpec)
			run = append(run, specUnit(sp))
			if k == 0 {
				// the lines before the keyword lead into the first spec too
				lead := append(append([]byte{}, src[cut:l.off(g.TokPos)]...), src[groupCut:l.off(sp.Pos())]...)
				texts = append(texts, typeTextFrom(src, lead, sp, l))
			} else {
				texts = append(texts, typeTextFrom(src, src[groupCut:l.off(sp.Pos())], sp, l))
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
	return edit{start: lo, end: hi, text: text}, true
}

// typeText returns the text of a single type declaration that starts with the
// lines src[from:keyword] and whose keyword is at keyword.
func typeText(src []byte, from int, keyword token.Pos, sp *ast.TypeSpec, l layout) []byte {
	return typeTextFrom(src, src[from:l.off(keyword)], sp, l)
}

// typeTextFrom returns lead followed by a single type declaration of sp: the
// keyword, then the spec's own text, through the end of its line.
func typeTextFrom(src []byte, lead []byte, sp *ast.TypeSpec, l layout) []byte {
	out := append([]byte{}, lead...)
	out = append(out, "type "...)
	return append(out, src[l.off(sp.Pos()):l.lineEnd(l.off(sp.End()))]...)
}

// dependencyOrder returns the indices of run in an order where each unit comes
// after the units it refers to. The units are grouped into strongly connected
// components, that is, sets of units that refer to each other, directly or
// through others; the units of one component keep their source order. A run of
// functions, constants or variables takes the components in the order Tarjan's
// algorithm emits them, which visits units in source order, so a unit moves only
// when it has to. A run of types takes the alphabetically first name among the
// components that are ready (see alphabeticalOrder), so that types which do not
// depend on each other are sorted.
func dependencyOrder(run []unit) []int {
	byName := map[string]int{} // for names that are not resolved
	byObj := map[any]int{}     // for names resolved to a unit's declaration
	for t, u := range run {
		for _, n := range u.names {
			byName[n] = t
		}
		for _, o := range u.objs {
			byObj[o] = t
		}
	}
	deps := make([][]int, len(run))
	for t, u := range run {
		seen := map[int]bool{}
		for _, v := range references(u.body, byName, byObj) {
			if v != t && !seen[v] {
				seen[v] = true
				deps[t] = append(deps[t], v)
			}
		}
		sort.Ints(deps[t])
	}

	comps := components(deps)
	if run[0].kind == token.TYPE {
		return alphabeticalOrder(run, deps, comps)
	}
	var order []int
	for _, c := range comps {
		order = append(order, c...)
	}
	return order
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
	var stack []int
	counter := 0
	var comps [][]int
	var connect func(t int)
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

// alphabeticalOrder orders the components of a run of types. A component is
// ready when every component it depends on has been placed; among the ready
// ones, the one whose first name (in alphabetical order) is smallest goes next.
// So types are sorted wherever the dependencies allow it, and a type always
// comes after the types it refers to.
func alphabeticalOrder(run []unit, deps [][]int, comps [][]int) []int {
	compOf := make([]int, len(run))
	for c, members := range comps {
		for _, t := range members {
			compOf[t] = c
		}
	}
	need := make([]int, len(comps)) // dependencies not yet placed
	dependents := make([][]int, len(comps))
	type edge struct{ from, to int }
	seen := map[edge]bool{}
	for t := range run {
		for _, u := range deps[t] {
			a, b := compOf[t], compOf[u]
			if a != b && !seen[edge{a, b}] {
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

// references returns the indices of the units that n refers to. An identifier
// refers to a unit if it resolves to one of the unit's declarations, or, when it
// is not resolved at all, if it has the name of one of the units. The selector
// in x.f, the name of a field, the key of a struct literal field and labels are
// not references.
func references(n ast.Node, byName map[string]int, byObj map[any]int) []int {
	var out []int
	var visit func(ast.Node) bool
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
			var t int
			var ok bool
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
