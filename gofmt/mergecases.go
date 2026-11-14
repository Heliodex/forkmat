package main

import (
	"bytes"
	"forkmat/go/printer"
	"go/ast"
	"go/token"
)

// bindName returns the name a type switch declares with v := x.(type), or nil.
func bindName(s *ast.TypeSwitchStmt) map[string]bool {
	as, ok := s.Assign.(*ast.AssignStmt)
	if !ok {
		return nil
	}

	names := map[string]bool{}

	for _, lhs := range as.Lhs {
		if id, ok := lhs.(*ast.Ident); ok {
			names[id.Name] = true
		}
	}

	return names
}

// commentWithin reports whether a comment lies in the source range [from, to).
func commentWithin(comments []*ast.CommentGroup, from, to token.Pos) bool {
	for _, cg := range comments {
		if cg.Pos() >= from && cg.End() <= to {
			return true
		}

		if cg.Pos() < to && cg.End() > from {
			return true
		}
	}

	return false
}

// endsInFallthrough reports whether a clause body's last statement is a
// fallthrough.
func endsInFallthrough(list []ast.Stmt) bool {
	if len(list) == 0 {
		return false
	}

	br, ok := list[len(list)-1].(*ast.BranchStmt)

	return ok && br.Tok == token.FALLTHROUGH
}

// printList returns the text of a statement list as the printer writes it.
func printList(fset *token.FileSet, list []ast.Stmt) string {
	var buf bytes.Buffer

	cfg := printer.Config{
		Mode: printer.UseSpaces | printer.TabIndent,
		Tabwidth: 8,
	}

	for _, s := range list {
		if err := cfg.Fprint(&buf, fset, s); err != nil {
			return "\x00" // never equal to another body
		}
		buf.WriteByte('\n')
	}

	return buf.String()
}

// sameBody reports whether two statement lists print the same.
func sameBody(fset *token.FileSet, a, b []ast.Stmt) bool {
	return printList(fset, a) == printList(fset, b)
}

// mergeable reports whether the adjacent clauses a and b can be one clause.
func mergeable(fset *token.FileSet, a, b *ast.CaseClause, bound map[string]bool, file *ast.File) bool {
	if endsInFallthrough(a.Body) || endsInFallthrough(b.Body) {
		return false
	}

	if bound != nil && (mentionsList(a.Body, bound) || mentionsList(b.Body, bound)) {
		return false
	}

	if commentWithin(file.Comments, a.Pos(), b.End()) {
		return false
	}

	return sameBody(fset, a.Body, b.Body)
}

// mergeClauses merges the adjacent clauses of body that can be merged. bound is
// the variable a type switch binds, or nil.
func mergeClauses(fset *token.FileSet, body *ast.BlockStmt, bound map[string]bool, file *ast.File) {
	clauses := body.List

	for i := 0; i+1 < len(clauses); {
		a, b := clauses[i].(*ast.CaseClause), clauses[i+1].(*ast.CaseClause)
		if a.List == nil || b.List == nil || !mergeable(fset, a, b, bound, file) {
			i++
			continue
		}

		a.List = append(append([]ast.Expr{}, a.List...), b.List...)
		clauses = append(clauses[:i+1:i+1], clauses[i+2:]...)
	}

	body.List = clauses
}

// This file implements -mergecases: two adjacent case clauses of a switch with
// the same body become one clause with both conditions:
//
//	case 1:              case 1, 2:
//		f()          =>      f()
//	case 2:
//		f()
//
// Only adjacent clauses merge, because the conditions of a switch are evaluated
// in order and a clause in between could change which expressions run. A body
// that ends in fallthrough is kept apart, since its target would change. In a
// type switch, the clauses merge only if the body does not use the variable the
// switch binds: in a clause with several types the variable has the type of the
// switch's operand, not one of the case types. A default clause never merges.
// A comment in either clause keeps them apart, so that no comment is lost.

// mergeCases merges the adjacent case clauses of every switch statement in file.
// fset is the fileset of file.
func mergeCases(fset *token.FileSet, file *ast.File) {
	ast.Inspect(file, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.SwitchStmt:
			mergeClauses(fset, s.Body, nil, file)
		case *ast.TypeSwitchStmt:
			mergeClauses(fset, s.Body, bindName(s), file)
		}

		return true
	})
}
