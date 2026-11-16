package main

import (
	"go/ast"
	"go/token"
	"slices"
)

// unusedBlank returns the blank identifier at the position of id.
func unusedBlank(id *ast.Ident) *ast.Ident {
	return &ast.Ident{
		NamePos: id.Pos(),
		Name: "_",
	}
}

// unusedIndex reports whether i is in list.
func unusedIndex(list []int, i int) bool {
	return slices.Contains(list, i)
}

// unusedName reports whether id declares a local variable that is never referenced.
func unusedName(id *ast.Ident, refs map[*ast.Object]int) bool {
	return id.Name != "_" && id.Obj != nil && refs[id.Obj] == 1
}

// unusedNearComment reports whether a comment lies within n or on the line before or after it, so that removing n would leave it behind.
func unusedNearComment(fset *token.FileSet, comments []*ast.CommentGroup, n ast.Node) bool {
	start := fset.Position(n.Pos()).Line
	end := fset.Position(n.End()).Line

	for _, cg := range comments {
		if fset.Position(cg.End()).Line >= start-1 && fset.Position(cg.Pos()).Line <= end {
			return true
		}
	}

	return false
}

// unusedPure reports whether evaluating e has no side effects and cannot panic, so that removing a declaration whose value is e cannot be observed. It is deliberately conservative: a call other than a conversion to a predeclared or local type, or new(T), a receive, a dereference, an index or a selector may have an effect or panic, and so may a division.
func unusedPure(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.BasicLit, *ast.FuncLit, *ast.Ident:
		return true
	case *ast.ParenExpr:
		return unusedPure(x.X)
	case *ast.UnaryExpr:
		if x.Op == token.ARROW || x.Op == token.MUL {
			return false
		}

		return unusedPure(x.X)
	case *ast.BinaryExpr:
		if x.Op == token.QUO || x.Op == token.REM {
			return false
		}

		return unusedPure(x.X) && unusedPure(x.Y)
	case *ast.KeyValueExpr:
		return unusedPure(x.Key) && unusedPure(x.Value)
	case *ast.CompositeLit:
		for _, el := range x.Elts {
			if !unusedPure(el) {
				return false
			}
		}

		return true
	case *ast.CallExpr:
		id, ok := x.Fun.(*ast.Ident)
		if !ok {
			return false
		}

		if id.Obj == nil && id.Name == "new" {
			return true
		}

		if id.Obj != nil && id.Obj.Kind != ast.Typ {
			// a call through a local name that is not a type
			return false
		}

		if id.Obj == nil && !predeclaredPure[id.Name] {
			return false
		}

		for _, arg := range x.Args {
			if !unusedPure(arg) {
				return false
			}
		}

		return true
	}

	return false
}

// unusedPureList reports whether none of the expressions has an effect.
func unusedPureList(list []ast.Expr) bool {
	for _, e := range list {
		if !unusedPure(e) {
			return false
		}
	}

	return true
}

// unusedQualified reports whether n holds a selector, such as pkg.T, whose package reference would be lost with n.
func unusedQualified(n ast.Node) (found bool) {
	ast.Inspect(n, func(n ast.Node) bool {
		if _, ok := n.(*ast.SelectorExpr); ok {
			found = true
		}

		return !found
	})

	return
}

// unusedDiscard returns what is left of a := statement whose names are all unused: nothing when the values have no effects and use no package, and an assignment to the blank identifiers otherwise. A call is written as an assignment too, since a conversion to an imported type cannot be told from a call without types, and a conversion is not a valid statement.
func unusedDiscard(s *ast.AssignStmt) []ast.Stmt {
	lhs := make([]ast.Expr, len(s.Lhs))

	for i, l := range s.Lhs {
		lhs[i] = unusedBlank(l.(*ast.Ident))
	}

	if unusedPureList(s.Rhs) && !unusedQualified(s) {
		return nil
	}

	return []ast.Stmt{
		&ast.AssignStmt{
			Lhs: lhs,
			Tok: token.ASSIGN,
			Rhs: s.Rhs,
		},
	}
}

// unusedAssign removes or blanks the unused names a := statement declares.
func unusedAssign(fset *token.FileSet, s *ast.AssignStmt, refs map[*ast.Object]int, comments []*ast.CommentGroup) []ast.Stmt {
	if s.Tok != token.DEFINE {
		return []ast.Stmt{s}
	}

	var (
		unused []int
		named int
	)

	for i, l := range s.Lhs {
		id, ok := l.(*ast.Ident)
		if !ok {
			return []ast.Stmt{s}
		}

		if id.Name == "_" {
			continue
		}

		named++

		if unusedName(id, refs) {
			unused = append(unused, i)
		}
	}

	if len(unused) == 0 {
		return []ast.Stmt{s}
	}

	if len(unused) == named {
		if unusedNearComment(fset, comments, s) {
			return []ast.Stmt{s}
		}

		return unusedDiscard(s)
	}

	for _, i := range unused {
		s.Lhs[i] = unusedBlank(s.Lhs[i].(*ast.Ident))
	}

	// with every new name blanked, := would declare nothing
	var declares bool

	for _, l := range s.Lhs {
		if id, ok := l.(*ast.Ident); ok && id.Obj != nil && id.Obj.Decl == s {
			declares = true
			break
		}
	}

	if !declares {
		s.Tok = token.ASSIGN
	}

	return []ast.Stmt{s}
}

// unusedRefs counts the identifiers of file that are bound to each object. A local variable that appears once, at its declaration, is never referenced.
func unusedRefs(file *ast.File) map[*ast.Object]int {
	refs := map[*ast.Object]int{}

	ast.Inspect(file, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Obj != nil {
			refs[id.Obj]++
		}

		return true
	})

	return refs
}

// unusedSpecNames returns the indices of the names of a var spec that are never referenced.
func unusedSpecNames(vs *ast.ValueSpec, refs map[*ast.Object]int) (out []int) {
	for i, id := range vs.Names {
		if unusedName(id, refs) {
			out = append(out, i)
		}
	}

	return
}

// unusedDecl removes the unused names of a local var declaration, and the specs that hold nothing else. A spec whose values have effects keeps them, at its place in the declaration, with its names blanked.
func unusedDecl(fset *token.FileSet, s *ast.DeclStmt, refs map[*ast.Object]int, comments []*ast.CommentGroup) []ast.Stmt {
	g, ok := s.Decl.(*ast.GenDecl)
	if !ok || g.Tok != token.VAR {
		return []ast.Stmt{s}
	}

	var (
		specs []ast.Spec
		changed bool
	)

	for _, sp := range g.Specs {
		vs := sp.(*ast.ValueSpec)
		unused := unusedSpecNames(vs, refs)

		if len(unused) == 0 || unusedNearComment(fset, comments, vs) {
			specs = append(specs, vs)
			continue
		}

		changed = true

		if len(vs.Values) == 0 {
			if len(unused) == len(vs.Names) && unusedQualified(vs) {
				// the type names an imported package, which the file may not refer to otherwise
				for _, i := range unused {
					vs.Names[i] = unusedBlank(vs.Names[i])
				}

				specs = append(specs, vs)

				continue
			}

			var names []*ast.Ident

			for i, id := range vs.Names {
				if !unusedIndex(unused, i) {
					names = append(names, id)
				}
			}

			if len(names) > 0 {
				vs.Names = names
				specs = append(specs, vs)
			}

			continue
		}

		if len(unused) == len(vs.Names) && unusedPureList(vs.Values) && !unusedQualified(vs) {
			continue
		}

		for _, i := range unused {
			vs.Names[i] = unusedBlank(vs.Names[i])
		}

		specs = append(specs, vs)
	}

	if !changed {
		return []ast.Stmt{s}
	}

	if len(specs) == 0 {
		if unusedNearComment(fset, comments, s) {
			// the declaration's own comment would be left behind; the specs were not changed yet
			return []ast.Stmt{s}
		}

		return nil
	}

	g.Specs = specs

	return []ast.Stmt{s}
}

// unusedStmt returns what replaces s after the unused local variables it declares are removed, if any.
func unusedStmt(fset *token.FileSet, s ast.Stmt, refs map[*ast.Object]int, comments []*ast.CommentGroup) []ast.Stmt {
	switch s := s.(type) {
	case *ast.AssignStmt:
		return unusedAssign(fset, s, refs, comments)
	case *ast.DeclStmt:
		return unusedDecl(fset, s, refs, comments)
	}

	return []ast.Stmt{s}
}

// This file implements -unused: a local variable that is declared but never referenced is removed. The value is kept where evaluating it has an effect, or where it names an imported package, so x := f() becomes _ = f(), x := <-ch becomes _ = <-ch, and x := pkg.T{} becomes _ = pkg.T{}. Otherwise the statement goes: x := 5 disappears.
//
//	var (                     var (
//	    x = 5          =>         y = 6
//	    y = 6                 )
//	)
//
// A name that is written but never read, as in x := 5 followed by x = 6, is referenced, and is kept. So are package-level vars, parameters, results, struct fields, and the names a range, if, for, switch or select header declares, since removing one of those changes a signature or a header.
//
// A statement is removed only when no comment lies in it or on the line before or after it, so that no comment is left behind. A declaration group keeps its other specs, and a value with effects stays in place with its name blanked, which keeps the evaluation order.

// unusedVars removes the local variables of file that are never referenced. fset is the fileset of file, and comments are its comments. It needs the object links the parser writes, so a file it is run on is parsed without parser.SkipObjectResolution.
func unusedVars(fset *token.FileSet, file *ast.File, comments []*ast.CommentGroup) {
	refs := unusedRefs(file)

	var lists []*[]ast.Stmt

	ast.Inspect(file, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.BlockStmt:
			lists = append(lists, &x.List)
		case *ast.CaseClause:
			lists = append(lists, &x.Body)
		case *ast.CommClause:
			lists = append(lists, &x.Body)
		}

		return true
	})

	for _, list := range lists {
		out := make([]ast.Stmt, 0, len(*list))

		for _, s := range *list {
			out = append(out, unusedStmt(fset, s, refs, comments)...)
		}

		*list = out
	}
}
