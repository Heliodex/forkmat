package main

import (
	"go/ast"
	"go/token"
	"slices"
	"sort"
)

// breaksOut reports whether the statement s holds an unlabeled break that is not inside a loop or switch within s; nested reports whether s itself is one.
func breaksOut(s ast.Stmt, nested bool) bool {
	switch s := s.(type) {
	case *ast.BlockStmt:
		for _, x := range s.List {
			if breaksOut(x, nested) {
				return true
			}
		}
	case *ast.BranchStmt:
		return s.Tok == token.BREAK && s.Label == nil && !nested
	case *ast.ForStmt:
		return breaksOut(s.Body, true)
	case *ast.IfStmt:
		if breaksOut(s.Body, nested) {
			return true
		}

		if s.Else != nil {
			return breaksOut(s.Else, nested)
		}
	case *ast.LabeledStmt:
		return breaksOut(s.Stmt, nested)
	case *ast.RangeStmt:
		return breaksOut(s.Body, true)
	case *ast.SelectStmt:
		return clausesBreakOut(s.Body)
	case *ast.SwitchStmt:
		return clausesBreakOut(s.Body)
	case *ast.TypeSwitchStmt:
		return clausesBreakOut(s.Body)
	}

	return false
}

// clausesBreakOut reports whether a switch or select body holds an unlabeled break that leaves the statement it belongs to.
func clausesBreakOut(body *ast.BlockStmt) bool {
	for _, cl := range body.List {
		var list []ast.Stmt

		switch c := cl.(type) {
		case *ast.CaseClause:
			list = c.Body
		case *ast.CommClause:
			list = c.Body
		}

		for _, x := range list {
			if breaksOut(x, true) {
				return true
			}
		}
	}

	return false
}

// breaksOutOf reports whether the statements of a switch clause hold an unlabeled break that would leave the switch. Such a break refers to the switch, so moving the clause out would change its meaning.
func breaksOutOf(list []ast.Stmt) bool {
	for _, s := range list {
		if breaksOut(s, false) {
			return true
		}
	}

	return false
}

// clausesHoldComments reports whether every comment in the body of a switch lies inside one of its clauses, so that moving a clause out leaves no comment between clauses behind.
func clausesHoldComments(body *ast.BlockStmt, comments []*ast.CommentGroup) bool {
	for _, cg := range comments {
		for _, c := range cg.List {
			if c.Pos() <= body.Lbrace || c.Pos() >= body.Rbrace {
				continue
			}

			var inside bool

			for _, cl := range body.List {
				if cl.Pos() <= c.Pos() && c.Pos() < cl.End() {
					inside = true
					break
				}
			}

			if !inside {
				return false
			}
		}
	}

	return true
}

// commentIn reports whether a comment starts in the source range [from, to).
func commentIn(comments []*ast.CommentGroup, from, to token.Pos) bool {
	for i := sort.Search(len(comments), func(i int) bool {
		return comments[i].End() > from
	}); i < len(comments) && comments[i].Pos() < to; i++ {
		if comments[i].Pos() >= from {
			return true
		}
	}

	return false
}

func endsInReturnList(list []ast.Stmt) bool {
	if len(list) == 0 {
		return false
	}

	_, ok := list[len(list)-1].(*ast.ReturnStmt)

	return ok
}

// endsInReturn reports whether a block's last statement is a return.
func endsInReturn(b *ast.BlockStmt) bool {
	if b == nil {
		return false
	}

	return endsInReturnList(b.List)
}

// initNames returns the names that an init statement declares.
func initNames(s ast.Stmt) map[string]bool {
	names := map[string]bool{}

	switch s := s.(type) {
	case *ast.AssignStmt:
		if s.Tok == token.DEFINE {
			for _, lhs := range s.Lhs {
				if id, ok := lhs.(*ast.Ident); ok {
					names[id.Name] = true
				}
			}
		}
	}

	return names
}

// ifFinal returns the owner of the final else block of the if statement s, the block itself, and the names the if statements of the chain declare in their headers. It reports false if s has no final else or if an if statement of the chain does not end in a return.
func ifFinal(s *ast.IfStmt) (*ast.IfStmt, *ast.BlockStmt, map[string]bool, bool) {
	cur := s

	for names := map[string]bool{}; ; {
		for name := range initNames(cur.Init) {
			names[name] = true
		}

		if !endsInReturn(cur.Body) {
			return nil, nil, nil, false
		}

		switch e := cur.Else.(type) {
		case *ast.BlockStmt:
			return cur, e, names, true
		case *ast.IfStmt:
			cur = e
		default:
			return nil, nil, nil, false
		}
	}
}

// mentions reports whether n uses one of the names.
func mentions(n ast.Node, names map[string]bool) bool {
	if len(names) == 0 {
		return false
	}

	var found bool

	ast.Inspect(n, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && names[id.Name] {
			found = true
		}

		return !found
	})

	return found
}

// mentionsList reports whether any statement of list uses one of the names.
func mentionsList(list []ast.Stmt, names map[string]bool) bool {
	for _, s := range list {
		if mentions(s, names) {
			return true
		}
	}

	return false
}

// flattenSwitch moves the default clause of the switch statement s, the last statement of list, out after it. body is s's body. names are the names the switch declares.
func flattenSwitch(list *[]ast.Stmt, s ast.Stmt, body *ast.BlockStmt, names map[string]bool, comments []*ast.CommentGroup) bool {
	def := -1

	for i, cl := range body.List {
		if cl.(*ast.CaseClause).List == nil {
			def = i
		}
	}

	if def < 0 {
		return false
	}

	// the other clauses must each end in a return: that makes the default clause the only one that reaches the statement after the switch
	for i, cl := range body.List {
		if i != def && !endsInReturnList(cl.(*ast.CaseClause).Body) {
			return false
		}
	}

	dc := body.List[def].(*ast.CaseClause)
	if breaksOutOf(dc.Body) || mentionsList(dc.Body, names) || !clausesHoldComments(body, comments) {
		return false
	}

	l := *list
	n := len(l)

	body.List = append(append([]ast.Stmt{}, body.List[:def]...), body.List[def+1:]...)
	*list = append(append([]ast.Stmt{}, l[:n-1]...), s)
	*list = append(*list, dc.Body...)

	return true
}

// flattenList moves the final branch out of the last statement of list, if it can. It reports whether it did, so that the statement now moved out may be flattened in turn.
func flattenList(list *[]ast.Stmt, comments []*ast.CommentGroup) bool {
	l := *list

	n := len(l)
	if n == 0 {
		return false
	}

	switch s := l[n-1].(type) {
	case *ast.IfStmt:
		owner, final, names, ok := ifFinal(s)
		if !ok || mentions(final, names) || commentIn(comments, owner.Body.Rbrace, final.Lbrace) {
			return false
		}

		owner.Else = nil

		*list = append(append([]ast.Stmt{}, l[:n-1]...), s)
		*list = append(*list, final.List...)

		return true
	case *ast.SwitchStmt:
		names := initNames(s.Init)

		return flattenSwitch(list, s, s.Body, names, comments)
	case *ast.TypeSwitchStmt:
		names := initNames(s.Init)

		if as, ok := s.Assign.(*ast.AssignStmt); ok {
			for _, lhs := range as.Lhs {
				if id, ok := lhs.(*ast.Ident); ok {
					names[id.Name] = true
				}
			}
		}

		return flattenSwitch(list, s, s.Body, names, comments)
	}

	return false
}

// This file implements -flatten. When the last statement of a block is an if statement with a final else, or a switch with a default clause, and every other branch of it ends in a return, the final branch is moved out after the statement:
//
//	if x {                 if x {
//		y()                	y()
//		return 1               return 1
//	} else {         =>    }
//		return 2           return 2
//	}
//
// The statement is only changed when moving the branch keeps its meaning: the names declared in the statement's header must not be used in the branch, and no unlabeled break may leave a switch through it. Comments in the parts that move are kept where they are, and a statement with a comment between its branches is left alone.

// flatten returns file with the final branches of its statements moved out. comments are the comments of the file.
func flatten(file *ast.File, comments []*ast.CommentGroup) {
	// the statement lists of the file, in source order: an inner list comes after the outer list that holds it, so the lists are handled last to first
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

	for _, list := range slices.Backward(lists) {
		for flattenList(list, comments) {
		}
	}
}
