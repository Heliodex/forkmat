package main

import (
	"go/ast"
	"go/token"
)

// hasDeclarations reports whether a statement list declares a name at its top level, with := or a declaration.
func hasDeclarations(list []ast.Stmt) bool {
	for _, s := range list {
		switch s := s.(type) {
		case *ast.AssignStmt:
			if s.Tok == token.DEFINE {
				return true
			}
		case *ast.DeclStmt:
			return true
		}
	}

	return false
}

// omittable reports whether clause i of body, which is c, can be removed.
func omittable(fset *token.FileSet, body *ast.BlockStmt, i int, c *ast.CaseClause, names map[string]bool, rest []ast.Stmt, want string, comments []*ast.CommentGroup) bool {
	if !endsInReturnList(c.Body) || printList(fset, c.Body) != want {
		return false
	}

	if breaksOutOf(c.Body) || hasDeclarations(c.Body) || mentionsList(c.Body, names) {
		return false
	}

	if commentWithin(comments, c.Pos(), c.End()) {
		return false
	}

	for _, e := range c.List {
		if !pureExpr(e, false) {
			return false
		}
	}

	// the other clauses must reach the same code: each one either has the same body, or this clause is the last and the switch has no default clause
	last := i == len(body.List)-1

	var hasDefault bool

	for _, cl := range body.List {
		if cl.(*ast.CaseClause).List == nil {
			hasDefault = true
		}
	}

	for j, cl := range body.List {
		if j == i {
			continue
		}

		if other := cl.(*ast.CaseClause); printList(fset, other.Body) != want || !endsInReturnList(other.Body) {
			if !last || hasDefault {
				return false
			}
		}
	}

	return true
}

// omitClauses removes the clauses of body that match the code rest, one at a time, until none does. names are the names the switch declares.
func omitClauses(fset *token.FileSet, body *ast.BlockStmt, names map[string]bool, rest []ast.Stmt, comments []*ast.CommentGroup) {
	want := printList(fset, rest)

	for changed := true; changed; {
		changed = false

		for i, cl := range body.List {
			if c := cl.(*ast.CaseClause); !omittable(fset, body, i, c, names, rest, want, comments) {
				continue
			}

			body.List = append(append([]ast.Stmt{}, body.List[:i]...), body.List[i+1:]...)
			changed = true

			break
		}
	}
}

// omitInList removes the redundant clauses of the switch statements in list, which are followed by the code they are compared with.
func omitInList(fset *token.FileSet, list []ast.Stmt, comments []*ast.CommentGroup) {
	for i := 0; i+1 < len(list); i++ {
		rest := list[i+1:]

		switch s := list[i].(type) {
		case *ast.SwitchStmt:
			names := initNames(s.Init)

			omitClauses(fset, s.Body, names, rest, comments)
		case *ast.TypeSwitchStmt:
			names := initNames(s.Init)

			for name := range bindName(s) {
				names[name] = true
			}

			omitClauses(fset, s.Body, names, rest, comments)
		}
	}
}

// This file implements -omitcases: a case clause of a switch is removed when its body is exactly the code that follows the switch in the same block, and that body ends in a return. A value that matched the clause now falls through to the same code, so the result is the same:
//
//	switch x {                switch x {
//	case 1:               =>  }
//	    return 5              return 5
//	}
//	return 5
//
// Other clauses must not change the outcome. A value that the removed clause matched used to stop there; now it may reach a later clause or the default clause instead. So the removal is made only when every other clause has the same body, or when the clause is the last one and the switch has no default clause. The clause's case expressions must have no side effects, since they are no longer evaluated. The body must not declare names (which would clash with the same declarations after the switch), must not use names that the switch's header declares, and must not contain a break that leaves the switch.

// omitCases removes the redundant clauses of the switches of file. fset is the fileset of file, and comments are its comments.
func omitCases(fset *token.FileSet, file *ast.File, comments []*ast.CommentGroup) {
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
		omitInList(fset, *list, comments)
	}
}
