package main

import (
	"go/ast"
	"go/token"
	"slices"
)

// This file implements the case order of -strict: the clauses of a switch whose
// cases are constants declared in the file are put in the order the constants
// are declared in, typically the order of an iota block. A clause moves only
// among the slots of the other clauses of its kind; the default clause keeps its
// slot. The switch is changed only if its behaviour cannot change: every clause
// that is not the default returns, and none of them can fall through or leave the
// switch with a break. Each case is a constant named in the file, so no case
// expression runs code, and a value matches at most one case.

// switchCaseOrder reorders the clauses of the switch statements of file.
func switchCaseOrder(file *ast.File) {
	// the position of each constant in the declarations of the file
	index := map[string]int{}
	for _, d := range file.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok || g.Tok != token.CONST {
			continue
		}

		for _, sp := range g.Specs {
			for _, id := range sp.(*ast.ValueSpec).Names {
				if _, dup := index[id.Name]; !dup {
					index[id.Name] = len(index)
				}
			}
		}
	}

	if len(index) == 0 {
		return
	}

	ast.Inspect(file, func(n ast.Node) bool {
		if s, ok := n.(*ast.SwitchStmt); ok {
			reorderSwitchCases(s.Body, index, file.Comments)
		}

		return true
	})
}

// reorderSwitchCases orders the non-default clauses of body by the declaration
// index of their constants, if the switch qualifies (see switchCaseOrder).
func reorderSwitchCases(body *ast.BlockStmt, index map[string]int, comments []*ast.CommentGroup) {
	// the key of a clause: the smallest index among its cases
	keys := map[*ast.CaseClause]int{}
	var slots []int // the positions of the non-default clauses in body.List
	for i, cl := range body.List {
		c := cl.(*ast.CaseClause)
		if c.List == nil {
			continue // the default clause keeps its slot
		}

		if !endsInReturnList(c.Body) || breaksOutOf(c.Body) {
			return
		}

		key := -1
		for _, e := range c.List {
			id, ok := e.(*ast.Ident)
			if !ok {
				return
			}

			k, ok := index[id.Name]
			if !ok {
				return
			}

			if key < 0 || k < key {
				key = k
			}
		}

		keys[c] = key
		slots = append(slots, i)
	}

	if len(slots) < 2 {
		return
	}

	for _, cg := range comments {
		if cg.Pos() >= body.Lbrace && cg.Pos() < body.Rbrace {
			return
		}
	}

	clauses := make([]*ast.CaseClause, len(slots))
	for k, i := range slots {
		clauses[k] = body.List[i].(*ast.CaseClause)
	}

	slices.SortStableFunc(clauses, func(a, b *ast.CaseClause) int {
		return keys[a] - keys[b]
	})

	list := slices.Clone(body.List)
	for k, i := range slots {
		list[i] = clauses[k]
	}

	body.List = list
}
