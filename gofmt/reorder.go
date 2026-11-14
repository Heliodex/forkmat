package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
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
		case token.CONST, token.TYPE:
		case token.VAR:
			if !pureVars(d) {
				return unit{}, false
			}
		default:
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
	passes := []func(*token.FileSet, *ast.File, []byte) []byte{reorderTypeSpecs, reorderDecls}
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
	tf  *token.File
	src []byte
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
	l := layout{tf: tf, src: src}
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

// reorderTypeSpecs is the pass over the specs of each type group.
func reorderTypeSpecs(fset *token.FileSet, file *ast.File, src []byte) []byte {
	tf := fset.File(file.Pos())
	if tf == nil {
		return src
	}
	l := layout{tf: tf, src: src}
	var edits []edit
	for _, d := range file.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok || g.Tok != token.TYPE || !g.Lparen.IsValid() || len(g.Specs) < 2 {
			continue
		}
		run := make([]unit, 0, len(g.Specs))
		for _, sp := range g.Specs {
			run = append(run, specUnit(sp.(*ast.TypeSpec)))
		}
		// the specs need a line each, with the parentheses on lines of their own
		ok = l.line(g.Lparen) != l.line(run[0].start) && l.line(run[len(run)-1].end) != l.line(g.Rparen)
		for t := 1; ok && t < len(run); t++ {
			ok = l.line(run[t-1].end) != l.line(run[t].start)
		}
		if !ok {
			continue
		}
		if e, ok := l.reorderItems(run, l.lineEnd(l.off(g.Lparen)), l.lineStart(l.off(g.Rparen))); ok {
			edits = append(edits, e)
		}
	}
	return applyEdits(src, edits)
}

// dependencyOrder returns the indices of run in an order where each unit comes
// after the units it refers to. The units are grouped into strongly connected
// components, that is, sets of units that refer to each other, directly or
// through others. Tarjan's algorithm emits each component after the components
// it depends on, and visits units in source order, so a unit moves only when it
// has to. The units of one component keep their source order.
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

	num := make([]int, len(run)) // discovery number, or -1 before discovery
	low := make([]int, len(run)) // lowest number reachable from the unit
	onStack := make([]bool, len(run))
	for t := range num {
		num[t] = -1
	}
	var stack []int
	counter := 0
	var order []int
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
			order = append(order, comp...)
		}
	}
	for t := range run {
		if num[t] < 0 {
			connect(t)
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
