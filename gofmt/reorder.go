package main

import (
	"go/ast"
	"go/token"
	"sort"
)

// This file implements the function reordering of -strict: within each run of
// consecutive plain functions, a function is moved below the functions it
// depends on, so that dependencies come first. Methods, init functions and
// functions without a body are never moved, and they end a run. Other
// declarations end a run too.
//
// The reordering works on the source text. Each function, with the comments
// that come before it on its own lines, is cut out as a segment; the segments
// are put back in the new order. Comments therefore travel with the function
// they lead into, and the rest of the file is left as it was. The caller
// parses the result again and prints it as usual.

// reorderFuncRuns returns src with each run of plain functions reordered. file
// is src parsed with object resolution, so that a local name that shadows a
// function is told apart from a reference to it; fset is the fileset of that
// parse.
func reorderFuncRuns(fset *token.FileSet, file *ast.File, src []byte) []byte {
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
		fd, ok := plainFunc(decls[i])
		if !ok {
			i++
			continue
		}
		run := []*ast.FuncDecl{fd}
		j := i + 1
		for j < len(decls) {
			next, ok := plainFunc(decls[j])
			// a run is laid out line by line, so two declarations on one line end it
			if !ok || line(decls[j-1].End()) == line(declStart(decls[j])) {
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

// reorderRun computes the edit that reorders run, the functions decls[i:j], or
// reports false if the run is already in order or cannot be reordered safely.
func reorderRun(run []*ast.FuncDecl, i, j int, file *ast.File, decls []ast.Decl, src []byte,
	off func(token.Pos) int, line func(token.Pos) int, lineEnd func(int) int) (edit, bool) {
	// the run's extent in the source: from the line after the declaration before
	// it (or the package clause) to the start of the declaration after it
	var lo int
	if i == 0 {
		if line(file.Name.End()) == line(declStart(run[0])) {
			return edit{}, false
		}
		lo = lineEnd(off(file.Name.End()))
	} else {
		prevEnd := decls[i-1].End()
		if line(prevEnd) == line(declStart(run[0])) {
			return edit{}, false
		}
		lo = lineEnd(off(prevEnd))
	}
	hi := len(src)
	if j < len(decls) {
		if line(run[len(run)-1].End()) == line(declStart(decls[j])) {
			return edit{}, false
		}
		hi = off(declStart(decls[j]))
	}

	// segment t starts on the line after function t-1 ends, so the comments
	// before function t travel with it
	k := len(run)
	starts := make([]int, k+1)
	starts[0] = lo
	for t := 1; t < k; t++ {
		starts[t] = lineEnd(off(run[t-1].End()))
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

// dependencyOrder returns the indices of run in an order where each function
// comes after the functions it refers to. The functions are grouped into
// strongly connected components, that is, sets of functions that refer to each
// other, directly or through others. Tarjan's algorithm emits each component
// after the components it depends on, and visits functions in source order, so
// a function moves only when it has to. The functions of one component keep
// their source order.
func dependencyOrder(run []*ast.FuncDecl) []int {
	index := map[string]int{}
	for t, fd := range run {
		index[fd.Name.Name] = t
	}
	deps := make([][]int, len(run))
	for t, fd := range run {
		seen := map[int]bool{}
		for _, u := range references(fd.Body, index) {
			if u != t && !seen[u] {
				seen[u] = true
				deps[t] = append(deps[t], u)
			}
		}
		sort.Ints(deps[t])
	}

	num := make([]int, len(run)) // discovery number, or -1 before discovery
	low := make([]int, len(run)) // lowest number reachable from the function
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

// references returns the indices, in index, of the functions that n refers to.
// A name counts as a reference when it resolves to the package-level function
// of that name, or is not resolved at all. A local name that shadows the
// function does not. The selector in x.f, the key of a struct literal field and
// labels are not references.
func references(n ast.Node, index map[string]int) []int {
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
			if t, ok := index[x.Name]; ok && (x.Obj == nil || isFuncObj(x.Obj)) {
				out = append(out, t)
			}
		}
		return true
	}
	ast.Inspect(n, visit)
	return out
}

// isFuncObj reports whether obj is a package-level function declaration.
func isFuncObj(obj *ast.Object) bool {
	if obj.Kind != ast.Fun {
		return false
	}
	_, ok := obj.Decl.(*ast.FuncDecl)
	return ok
}

// plainFunc returns d as a function that may be reordered: a function that is
// not a method, is not init, and has a body.
func plainFunc(d ast.Decl) (*ast.FuncDecl, bool) {
	fd, ok := d.(*ast.FuncDecl)
	if !ok || fd.Recv != nil || fd.Name.Name == "init" || fd.Body == nil {
		return nil, false
	}
	return fd, true
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
