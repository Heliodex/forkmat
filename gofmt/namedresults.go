package main

import (
	"go/ast"
	"go/token"
	"go/types"
	"slices"
)

// zeroVar is a var declaration at the top of a function body, with a type and no value.
type zeroVar struct {
	index int // the index of its statement in the body
	spec *ast.ValueSpec
}

// commentOnLines reports whether a comment starts on a line from the line of start to the line of end, inclusive.
func commentOnLines(fset *token.FileSet, comments []*ast.CommentGroup, start, end token.Pos) bool {
	from, to := fset.Position(start).Line, fset.Position(end).Line

	for _, cg := range comments {
		for _, c := range cg.List {
			if line := fset.Position(c.Pos()).Line; line >= from && line <= to {
				return true
			}
		}
	}

	return false
}

// hasDefer reports whether body holds a defer statement, at any depth.
func hasDefer(body *ast.BlockStmt) (found bool) {
	ast.Inspect(body, func(n ast.Node) bool {
		if _, ok := n.(*ast.DeferStmt); ok {
			found = true
		}

		return !found
	})
	return
}

// lastReturn returns the return statement that ends body, if it is the body's only return statement. A return in a function literal in body is not counted, since it returns from that literal.
func lastReturn(body *ast.BlockStmt) *ast.ReturnStmt {
	if len(body.List) == 0 {
		return nil
	}

	var returns int

	ast.Inspect(body, func(n ast.Node) bool {
		switch n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.ReturnStmt:
			returns++
		}

		return true
	})

	ret, ok := body.List[len(body.List)-1].(*ast.ReturnStmt)
	if !ok || returns != 1 {
		return nil
	}

	return ret
}

// redeclares reports whether body declares name anywhere other than in the specs listed in own: as a var, const or type, a parameter or field, or with := or a range clause.
func redeclares(body *ast.BlockStmt, name string, own map[*ast.ValueSpec]bool) (found bool) {
	isName := func(e ast.Expr) bool {
		id, ok := e.(*ast.Ident)

		return ok && id.Name == name
	}

	ast.Inspect(body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.AssignStmt:
			if x.Tok == token.DEFINE && slices.ContainsFunc(x.Lhs, isName) {
				found = true
			}
		case *ast.Field:
			found = found || slices.ContainsFunc(x.Names, func(id *ast.Ident) bool {
				return id.Name == name
			})
		case *ast.RangeStmt:
			if x.Tok == token.DEFINE && (x.Key != nil && isName(x.Key) || x.Value != nil && isName(x.Value)) {
				found = true
			}
		case *ast.TypeSpec:
			found = found || x.Name.Name == name
		case *ast.ValueSpec:
			if !own[x] && slices.ContainsFunc(x.Names, func(id *ast.Ident) bool {
				return id.Name == name
			}) {
				found = true
			}
		}

		return !found
	})
	return
}

// This file implements -namedresults. When a function has unnamed results and its only return statement, which is its last statement, returns local variables that the body declares at its top level with var and no value, those variables become the named results and the return statement is written bare:

//	func f() map[string]any {          func f() (doc map[string]any) {
//		var doc map[string]any     =>
//		json.Unmarshal(b, &doc)            json.Unmarshal(b, &doc)
//		return doc                         return
//	}                                  }

// The change is only made when it keeps the meaning of the code: each returned variable must have the type of the result at its position and no other declaration of its name in the function, and no name may be used before the variable's declaration. A function with a defer statement is left alone, since a recovered panic returns the named results, not zero values. A declaration with a comment is left alone too, so that no comment is lost.

// unnamedResults returns the types of the results of ft, one for each result, or nil if it has none or if any of them is named.
func unnamedResults(ft *ast.FuncType) []ast.Expr {
	if ft.Results == nil {
		return nil
	}

	var out []ast.Expr

	for _, f := range ft.Results.List {
		if len(f.Names) > 0 {
			return nil
		}

		out = append(out, f.Type)
	}

	return out
}

// zeroVars returns the top-level var declarations of body that have a type and no value and comments, by the name they declare. A declaration of several specs is left out, since it cannot be moved whole.
func zeroVars(fset *token.FileSet, comments []*ast.CommentGroup, body *ast.BlockStmt) map[string]zeroVar {
	vars := map[string]zeroVar{}

	for i, s := range body.List {
		ds, ok := s.(*ast.DeclStmt)
		if !ok {
			continue
		}

		gd, ok := ds.Decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR || len(gd.Specs) != 1 {
			continue
		}

		vs := gd.Specs[0].(*ast.ValueSpec)
		if vs.Values != nil || vs.Type == nil || vs.Doc != nil || vs.Comment != nil || commentOnLines(fset, comments, ds.Pos(), ds.End()) {
			continue
		}

		for _, id := range vs.Names {
			vars[id.Name] = zeroVar{
				index: i,
				spec: vs,
			}
		}
	}

	return vars
}

// nameResults makes the variables that ft's body returns its named results, if it can. comments are the comments of the file.
func nameResults(fset *token.FileSet, comments []*ast.CommentGroup, ft *ast.FuncType, body *ast.BlockStmt) {
	results := unnamedResults(ft)
	if results == nil || hasDefer(body) {
		return
	}

	ret := lastReturn(body)
	if ret == nil || len(ret.Results) != len(results) {
		return
	}

	// each result must be returned as a plain name, and no name may be returned twice
	var names []string

	returned := map[string]bool{}

	for _, e := range ret.Results {
		id, ok := e.(*ast.Ident)
		if !ok || returned[id.Name] {
			return
		}

		names = append(names, id.Name)
		returned[id.Name] = true
	}

	vars := zeroVars(fset, comments, body)
	drop := map[int]bool{}
	own := map[*ast.ValueSpec]bool{}

	for i, name := range names {
		zv, ok := vars[name]
		if !ok || types.ExprString(zv.spec.Type) != types.ExprString(results[i]) {
			return
		}

		own[zv.spec] = true
		drop[zv.index] = true
	}

	// every name a dropped declaration holds must be returned, since the declaration moves whole
	for _, zv := range vars {
		if !drop[zv.index] {
			continue
		}

		for _, id := range zv.spec.Names {
			if !returned[id.Name] {
				return
			}
		}
	}

	for _, name := range names {
		one := map[string]bool{name: true}

		// a name in the signature, or used before its declaration, would refer to something else once it is a result
		if mentions(ft, one) || mentionsList(body.List[:vars[name].index], one) || redeclares(body, name, own) {
			return
		}
	}

	// the statements of the declarations are dropped, and the results take their names and types
	var list []ast.Stmt

	for i, s := range body.List {
		if !drop[i] {
			list = append(list, s)
		}
	}

	fields := make([]*ast.Field, len(names))

	for i, name := range names {
		// the name takes the position of its type, so that the result list is laid out on the line the type sits on
		id := &ast.Ident{
			NamePos: results[i].Pos(),
			Name: name,
		}
		fields[i] = &ast.Field{
			Names: []*ast.Ident{id},
			Type: results[i],
		}
	}

	body.List = list
	ft.Results.List = fields
	ret.Results = nil
}

// namedResults applies -namedresults to every function of file (see the comment at the top of this file). fset and comments are the file's.
func namedResults(fset *token.FileSet, file *ast.File) {
	var (
		funcs []*ast.FuncType
		bodies []*ast.BlockStmt
	)
	ast.Inspect(file, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncDecl:
			if x.Body != nil {
				funcs = append(funcs, x.Type)
				bodies = append(bodies, x.Body)
			}
		case *ast.FuncLit:
			funcs = append(funcs, x.Type)
			bodies = append(bodies, x.Body)
		}

		return true
	})
	for i := range funcs {
		nameResults(fset, file.Comments, funcs[i], bodies[i])
	}
}
