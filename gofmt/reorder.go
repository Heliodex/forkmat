package main

import (
	"go/ast"
	"go/token"
	"sort"
)

// This file implements the reordering of -strict: within each run of
// consecutive functions, or of consecutive const declarations, a declaration is
// moved below the declarations it depends on, so that dependencies come first.
// Methods, init functions and functions without a body are never moved, and
// neither is anything else; each of them ends a run. A const declaration is one
// unit with its whole group: the specs of a group depend on each other through
// iota, so a group is never split.
//
// The reordering works on the source text. Each unit, with the comments that
// come before it on its own lines, is cut out as a segment; the segments are put
// back in the new order. Comments therefore travel with the declaration they
// lead into, and the rest of the file is left as it was. The caller parses the
// result again and prints it as usual.

// unit is a declaration that is moved as a whole.
type unit struct {
	decl  ast.Decl
	names []string // the names it declares
	body  ast.Node // the part whose references count as dependencies
}

// unitOf returns d as a unit, or false if the reordering does not move d. A var
// declaration is a unit only if its values are pure (see pureVars).
func unitOf(d ast.Decl) (unit, bool) {
	switch d := d.(type) {
	case *ast.FuncDecl:
		if d.Recv != nil || d.Name.Name == "init" || d.Body == nil {
			return unit{}, false
		}
		return unit{decl: d, names: []string{d.Name.Name}, body: d.Body}, true
	case *ast.GenDecl:
		if d.Tok != token.CONST && !(d.Tok == token.VAR && pureVars(d)) {
			return unit{}, false
		}
		var names []string
		for _, sp := range d.Specs {
			for _, id := range sp.(*ast.ValueSpec).Names {
				names = append(names, id.Name)
			}
		}
		return unit{decl: d, names: names, body: d}, true
	}
	return unit{}, false
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

// unitKind returns the kind of a unit: token.FUNC for a function, otherwise the
// token of its declaration. Consecutive units of one kind form a run.
func unitKind(u unit) token.Token {
	if _, ok := u.decl.(*ast.FuncDecl); ok {
		return token.FUNC
	}
	return u.decl.(*ast.GenDecl).Tok
}

// reorderDeclRuns returns src with each run of functions, and each run of const
// declarations, reordered. file is src parsed with object resolution, so that a
// local name that shadows a package-level one is not taken for a reference to
// it; fset is the fileset of that parse.
func reorderDeclRuns(fset *token.FileSet, file *ast.File, src []byte) []byte {
	tf := fset.File(file.Pos())
	if tf == nil {
		return src
	}
	off := func(p token.Pos) int { return tf.Offset(p) }
	line := func(p token.Pos) int { return tf.Line(p) }
	// lineEnd returns the offset just past the newline that ends the line
	// containing offset o, or len(src) if there is none.
	lineEnd := func(o int) int {
		for o < len(src) && src[o] != '\n' {
			o++
		}
		if o < len(src) {
			return o + 1
		}
		return len(src)
	}

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
			if !ok || unitKind(next) != unitKind(run[0]) || line(decls[j-1].End()) == line(declStart(decls[j])) {
				break
			}
			run = append(run, next)
			j++
		}
		if e, ok := reorderRun(run, i, j, file, decls, src, off, line, lineEnd); ok {
			edits = append(edits, e)
		}
		i = j
	}

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

// edit replaces src[start:end] with text.
type edit struct {
	start, end int
	text       []byte
}

// reorderRun computes the edit that reorders run, the declarations decls[i:j],
// or reports false if the run is already in order or cannot be reordered safely.
func reorderRun(run []unit, i, j int, file *ast.File, decls []ast.Decl, src []byte,
	off func(token.Pos) int, line func(token.Pos) int, lineEnd func(int) int) (edit, bool) {
	// the run's extent in the source: from the line after the declaration before
	// it (or the package clause) to the start of the declaration after it
	var lo int
	if i == 0 {
		if line(file.Name.End()) == line(declStart(decls[0])) {
			return edit{}, false
		}
		lo = lineEnd(off(file.Name.End()))
	} else {
		prevEnd := decls[i-1].End()
		if line(prevEnd) == line(declStart(decls[i])) {
			return edit{}, false
		}
		lo = lineEnd(off(prevEnd))
	}
	hi := len(src)
	if j < len(decls) {
		if line(decls[j-1].End()) == line(declStart(decls[j])) {
			return edit{}, false
		}
		hi = off(declStart(decls[j]))
	}

	// segment t starts on the line after unit t-1 ends, so the comments before
	// unit t travel with it
	k := len(run)
	starts := make([]int, k+1)
	starts[0] = lo
	for t := 1; t < k; t++ {
		starts[t] = lineEnd(off(decls[i+t-1].End()))
	}
	starts[k] = hi
	segs := make([][]byte, k)
	for t := range k {
		segs[t] = src[starts[t]:starts[t+1]]
	}

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
		text = append(text, segs[t]...)
	}
	return edit{start: lo, end: hi, text: text}, true
}

// dependencyOrder returns the indices of run in an order where each unit comes
// after the units it refers to. The units are grouped into strongly connected
// components, that is, sets of units that refer to each other, directly or
// through others. Tarjan's algorithm emits each component after the components
// it depends on, and visits units in source order, so a unit moves only when it
// has to. The units of one component keep their source order.
func dependencyOrder(run []unit) []int {
	byName := map[string]int{}   // for names that are not resolved
	byObj := map[any]int{}       // for names resolved to a unit's declaration
	for t, u := range run {
		for _, n := range u.names {
			byName[n] = t
		}
		switch d := u.decl.(type) {
		case *ast.FuncDecl:
			byObj[d] = t
		case *ast.GenDecl:
			for _, sp := range d.Specs {
				byObj[sp] = t
			}
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
// in x.f, the key of a struct literal field and labels are not references.
func references(n ast.Node, byName map[string]int, byObj map[any]int) []int {
	var out []int
	var visit func(ast.Node) bool
	visit = func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectorExpr:
			ast.Inspect(x.X, visit)
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
