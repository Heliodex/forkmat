package main

import (
	"go/ast"
	"go/token"
)

// This file implements the increment rewrite of -strict: an assignment x = x + 1 or x = x - 1 becomes x++ or x--. Only an identifier on the left and the integer literal 1 on the right qualify, so the statement means the same thing. The rewrite applies to statements of blocks and clauses, and to the post statement of a for loop.

// incDecStmts rewrites the increment assignments of file.
func incDecStmts(file *ast.File) {
	ast.Inspect(file, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.BlockStmt:
			x.List = incDecList(x.List)
		case *ast.CaseClause:
			x.Body = incDecList(x.Body)
		case *ast.CommClause:
			x.Body = incDecList(x.Body)
		case *ast.ForStmt:
			if x.Post != nil {
				x.Post = incDecStmt(x.Post)
			}
		}

		return true
	})
}

// incDecList rewrites the increment assignments of a statement list.
func incDecList(list []ast.Stmt) []ast.Stmt {
	for i, s := range list {
		list[i] = incDecStmt(s)
	}

	return list
}

// incDecStmt returns s as x++ or x-- if s is x = x + 1 or x = x - 1, and s otherwise.
func incDecStmt(s ast.Stmt) ast.Stmt {
	as, ok := s.(*ast.AssignStmt)
	if !ok || as.Tok != token.ASSIGN || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
		return s
	}

	lhs, ok := as.Lhs[0].(*ast.Ident)
	if !ok {
		return s
	}

	bin, ok := as.Rhs[0].(*ast.BinaryExpr)
	if !ok || (bin.Op != token.ADD && bin.Op != token.SUB) {
		return s
	}

	x, ok := bin.X.(*ast.Ident)
	if !ok || x.Name != lhs.Name {
		return s
	}

	one, ok := bin.Y.(*ast.BasicLit)
	if !ok || one.Kind != token.INT || one.Value != "1" {
		return s
	}

	tok := token.INC
	if bin.Op == token.SUB {
		tok = token.DEC
	}

	return &ast.IncDecStmt{
		X:      lhs,
		TokPos: as.End(),
		Tok:    tok,
	}
}
