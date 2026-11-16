package main

import (
	"forkmat/go/printer"
	"go/ast"
	"slices"
	"strings"
)

// sortTypeList returns the types of a type switch clause in alphabetical order.
func sortTypeList(list []ast.Expr) []ast.Expr {
	texts := make(map[ast.Expr]string, len(list))

	for _, e := range list {
		texts[e] = exprText(e)
	}

	out := slices.Clone(list)

	slices.SortStableFunc(out, func(a, b ast.Expr) int {
		return strings.Compare(texts[a], texts[b])
	})

	return out
}

// This file implements the order within a case clause of -strict: the expressions of a clause such as case c, a, b: are sorted. The clauses of a type switch are sorted by their types, alphabetically, as reorderTypeSwitchCases does. The cases of another switch are sorted as the keys of a map literal are: nil, booleans, numbers, strings, and then names, each by value (see printer.SortCaseList). A clause with an expression that is not a literal or a name keeps its order, since the expressions of a clause are evaluated in order. A clause with a comment inside its list is left alone. The types of a type switch clause are sorted whether or not they are concrete: any one of them matches, so their order cannot change which clause runs.

// caseListOrder sorts the expressions of every case clause of the switch statements of file. comments are the comments of file.
func caseListOrder(file *ast.File) {
	sorted := func(c *ast.CaseClause, typed bool) {
		if len(c.List) < 2 {
			return
		}

		for _, cg := range file.Comments {
			if cg.Pos() >= c.Case && cg.Pos() < c.Colon {
				return
			}
		}

		if typed {
			c.List = sortTypeList(c.List)
		} else {
			c.List = printer.SortCaseList(c.List)
		}
	}

	ast.Inspect(file, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.SwitchStmt:
			for _, cl := range s.Body.List {
				sorted(mustBe[*ast.CaseClause](cl), false)
			}
		case *ast.TypeSwitchStmt:
			for _, cl := range s.Body.List {
				sorted(mustBe[*ast.CaseClause](cl), true)
			}
		}

		return true
	})
}
