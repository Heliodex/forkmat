package main

import (
	"go/ast"
	"go/token"
	"sort"
)

type (
	// blockItem is a statement of a block, with what the pass needs to know about it.
	blockItem struct {
		stage int
		movable, // a declaration that may move: a type, a constant, or a variable with a safe value
		inert bool // a statement that a variable may pass: no control flow, calls or panics, and no assignment to anything but a name
		decls, // the names it declares or assigns
		refs map[string]bool // the names it mentions
	}
)

// This file implements the block pass of -strict: in a block (a function body, a nested block, or the clause of a switch or select), type declarations move to the top, then constant declarations, then variables, and each moves as far up as it can. A type or a constant has no effect, so it only has to keep clear of the names it shares with the statements it passes: it does not move past a statement that declares a name it uses, or that uses a name it declares. A variable also has to keep the order of effects: it moves only when its value cannot panic or have an effect, and it passes only statements that are themselves free of control flow, calls and panics, and whose assignments do not touch a name it uses.
//
// A block that holds a label or a goto is left alone, since moving statements could change where a jump lands.

const (
	stageType = iota
	stageConst
	stageVar
	stageOther
)

// allIdents reports whether every expression of list is a plain name.
func allIdents(list []ast.Expr) bool {
	for _, e := range list {
		if _, ok := e.(*ast.Ident); !ok {
			return false
		}
	}

	return true
}

// conflicts reports whether x and y share a name that one of them declares: the one that moves past the other could then see a different variable or type.
func conflicts(x, y blockItem) bool {
	for n := range x.decls {
		if y.refs[n] {
			return true
		}
	}

	for n := range y.decls {
		if x.refs[n] {
			return true
		}
	}

	return false
}

// crosses reports whether item x may move up past the item y, which is just above it.
func crosses(x, y blockItem) bool {
	// a declaration that is not moved yet keeps the kinds in order: nothing passes it that belongs to a later kind
	if y.stage < x.stage {
		return false
	}

	if conflicts(x, y) {
		return false
	}

	return x.stage != stageVar || y.inert
}

// declaredIn returns the names that n declares or assigns, at any depth: the names of declarations, the left-hand sides of assignments, the operands of ++ and --, the variables of a range, and the names of parameters and fields. It is conservative: a name that is only shadowed inside n counts too.
func declaredIn(n ast.Node) map[string]bool {
	names := map[string]bool{}
	add := func(id *ast.Ident) {
		if id != nil && id.Name != "_" {
			names[id.Name] = true
		}
	}

	ast.Inspect(n, func(m ast.Node) bool {
		switch x := m.(type) {
		case *ast.AssignStmt:
			for _, e := range x.Lhs {
				if id, ok := e.(*ast.Ident); ok {
					add(id)
				}
			}
		case *ast.Field:
			for _, id := range x.Names {
				add(id)
			}
		case *ast.IncDecStmt:
			if id, ok := x.X.(*ast.Ident); ok {
				add(id)
			}
		case *ast.RangeStmt:
			if id, ok := x.Key.(*ast.Ident); ok {
				add(id)
			}

			if id, ok := x.Value.(*ast.Ident); ok {
				add(id)
			}
		case *ast.TypeSpec:
			add(x.Name)
		case *ast.ValueSpec:
			for _, id := range x.Names {
				add(id)
			}
		}

		return true
	})
	return names
}

// hasLabelOrGoto reports whether list holds a labeled statement or a goto, anywhere in its statements.
func hasLabelOrGoto(list []ast.Stmt) (found bool) {
	for _, s := range list {
		ast.Inspect(s, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.BranchStmt:
				if x.Tok == token.GOTO {
					found = true
				}
			case *ast.LabeledStmt:
				found = true
			}

			return !found
		})
	}

	return
}

// mentioned returns the names of every identifier in n.
func mentioned(n ast.Node) map[string]bool {
	names := map[string]bool{}

	ast.Inspect(n, func(m ast.Node) bool {
		if id, ok := m.(*ast.Ident); ok {
			names[id.Name] = true
		}

		return true
	})
	return names
}

// safeExpr reports whether evaluating e cannot have an effect or panic: it has no call except a conversion to a predeclared type, len or cap, and no division, shift, index, selector, receive or function literal.
func safeExpr(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.BasicLit, *ast.Ident:
		return true
	case *ast.BinaryExpr:
		switch x.Op {
		case token.QUO, token.REM, token.SHL, token.SHR:
			return false
		}

		return safeExpr(x.X) && safeExpr(x.Y)
	case *ast.CallExpr:
		if id, ok := x.Fun.(*ast.Ident); !ok || x.Ellipsis.IsValid() || len(x.Args) != 1 || !(predeclaredPure[id.Name] || id.Name == "len" || id.Name == "cap") {
			return false
		}

		return safeExpr(x.Args[0])
	case *ast.ParenExpr:
		return safeExpr(x.X)
	case *ast.UnaryExpr:
		return x.Op != token.ARROW && safeExpr(x.X)
	}

	return false
}

// safeExprs reports whether every expression of list is safe (see safeExpr).
func safeExprs(list []ast.Expr) bool {
	for _, e := range list {
		if !safeExpr(e) {
			return false
		}
	}

	return true
}

// inertStmt reports whether a variable may pass s: s is a declaration of a type, a constant or a variable with safe values, or an assignment or ++ or -- of plain names with safe values. Such a statement has no effect that a variable's value could depend on, beyond the names it sets, which the name check covers.
func inertStmt(s ast.Stmt) bool {
	switch x := s.(type) {
	case *ast.AssignStmt:
		if !safeExprs(x.Rhs) {
			return false
		}

		for _, e := range x.Lhs {
			if _, ok := e.(*ast.Ident); !ok {
				return false
			}
		}

		return true
	case *ast.DeclStmt:
		d, ok := x.Decl.(*ast.GenDecl)
		if !ok {
			return false
		}

		for _, sp := range d.Specs {
			if vs, ok := sp.(*ast.ValueSpec); ok && !safeExprs(vs.Values) {
				return false
			}
		}

		return true
	case *ast.IncDecStmt:
		_, ok := x.X.(*ast.Ident)

		return ok
	}

	return false
}

// itemOf describes the statement s, which is a statement of a block.
func itemOf(s ast.Stmt) blockItem {
	it := blockItem{
		stage: stageOther,
		decls: declaredIn(s),
		refs: mentioned(s),
	}

	switch x := s.(type) {
	case *ast.AssignStmt:
		if x.Tok == token.DEFINE && allIdents(x.Lhs) && safeExprs(x.Rhs) {
			it.stage, it.movable = stageVar, true
		}
	case *ast.DeclStmt:
		d, ok := x.Decl.(*ast.GenDecl)
		if !ok {
			return it
		}

		switch d.Tok {
		case token.CONST:
			it.stage, it.movable = stageConst, true
		case token.TYPE:
			it.stage, it.movable = stageType, true
		case token.VAR:
			it.stage = stageVar
			it.movable = true

			for _, sp := range d.Specs {
				if vs, ok := sp.(*ast.ValueSpec); ok && !safeExprs(vs.Values) {
					it.movable = false
				}
			}
		}
	}

	it.inert = inertStmt(s)

	return it
}

// blockOrder returns the order in which the statements of a block are to be printed: the indices of list, in their new order. prefix counts the statements at the top that are placed; each kind of declaration is placed after the one before it.
func blockOrder(list []ast.Stmt) []int {
	items := make([]blockItem, len(list))
	order := make([]int, len(list))

	for i, s := range list {
		items[i] = itemOf(s)
		order[i] = i
	}

	var prefix int

	for stage := stageType; stage <= stageVar; stage++ {
		for i := prefix; i < len(items); i++ {
			if !items[i].movable || items[i].stage != stage {
				continue
			}

			q := i

			for q > prefix && crosses(items[i], items[q-1]) {
				q--
			}

			if q < i {
				it, ord := items[i], order[i]

				copy(items[q+1:i+1], items[q:i])
				copy(order[q+1:i+1], order[q:i])

				items[q], order[q] = it, ord
			}

			prefix = q + 1
		}
	}

	return order
}

// blockEdit returns the edit that reorders the statements of list, a block whose opening token is at head, or reports false if they are already in order or the block is left alone. Each statement moves with the lines before it, which hold its comments (see assignRun).
func (l layout) blockEdit(list []ast.Stmt, head token.Pos) (edit, bool) {
	if len(list) < 2 || hasLabelOrGoto(list) {
		return edit{}, false
	}

	var movable int

	for _, s := range list {
		if itemOf(s).movable {
			movable++
		}
	}

	if movable == 0 {
		return edit{}, false
	}

	if l.line(head) == l.line(list[0].Pos()) {
		return edit{}, false
	}

	for i := 1; i < len(list); i++ {
		if l.line(list[i-1].End()) == l.line(list[i].Pos()) {
			return edit{}, false
		}
	}

	order := blockOrder(list)
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

	lo := l.lineEnd(l.off(head))
	hi := l.lineEnd(l.off(list[len(list)-1].End()))
	texts := make([][]byte, len(list))
	cut := lo

	for k, s := range list {
		end := l.lineEnd(l.off(s.End()))
		texts[k] = append([]byte{}, l.src[cut:end]...)
		cut = end
	}

	var text []byte

	for _, k := range order {
		text = append(text, texts[k]...)
	}

	return edit{
		start: lo,
		end: hi,
		text: text,
	}, true
}

// reorderBlockDecls is the pass over the blocks of the file (see the comment at the top of this file). A block whose statements it reorders is not searched further in the same pass: the blocks inside it, whose edits would overlap, are reordered in the next round of -strict.
func reorderBlockDecls(fset *token.FileSet, file *ast.File, src []byte) []byte {
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
		var (
			list []ast.Stmt
			head token.Pos
		)

		switch b := n.(type) {
		case *ast.BlockStmt:
			list, head = b.List, b.Lbrace
		case *ast.CaseClause:
			list, head = b.Body, b.Colon
		case *ast.CommClause:
			list, head = b.Body, b.Colon
		default:
			return true
		}

		if e, ok := l.blockEdit(list, head); ok {
			edits = append(edits, e)

			return false
		}

		return true
	})
	sort.Slice(edits, func(i, j int) bool {
		return edits[i].start < edits[j].start
	})
	return applyEdits(src, edits)
}
