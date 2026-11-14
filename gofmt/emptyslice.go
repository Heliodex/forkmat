package main

import (
	"go/ast"
	"go/token"
)

// This file implements the empty slice rewrite of -strict: x := []T{} becomes
// var x []T, when nil and empty are not told apart by the rest of the block. A
// slice that is only appended to, ranged over, measured with len or cap, or indexed
// behaves the same from nil as from empty. Any other use keeps the declaration: a
// comparison with nil, a return, a call argument, a slice expression, an address,
// or a further declaration of the name.

// emptySlices rewrites the empty slice declarations of the statement lists of file.
func emptySlices(file *ast.File) {
	ast.Inspect(file, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.BlockStmt:
			x.List = emptySliceList(x.List)
		case *ast.CaseClause:
			x.Body = emptySliceList(x.Body)
		case *ast.CommClause:
			x.Body = emptySliceList(x.Body)
		}

		return true
	})
}

// emptySliceList rewrites the declarations of list that qualify.
func emptySliceList(list []ast.Stmt) []ast.Stmt {
	for i, s := range list {
		name, typ, ok := emptySliceDecl(s)
		if !ok || !onlyNilSafeUses(list[i+1:], name.Name) {
			continue
		}

		list[i] = &ast.DeclStmt{Decl: &ast.GenDecl{
			TokPos: s.Pos(),
			Tok:    token.VAR,
			Specs: []ast.Spec{&ast.ValueSpec{
				Names: []*ast.Ident{name},
				Type:  typ,
			}},
		}}
	}

	return list
}

// emptySliceDecl returns the name and type of s if s is x := []T{}, for a slice
// type written in full with no elements.
func emptySliceDecl(s ast.Stmt) (*ast.Ident, *ast.ArrayType, bool) {
	as, ok := s.(*ast.AssignStmt)
	if !ok || as.Tok != token.DEFINE || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
		return nil, nil, false
	}

	name, ok := as.Lhs[0].(*ast.Ident)
	if !ok || name.Name == "_" {
		return nil, nil, false
	}

	lit, ok := as.Rhs[0].(*ast.CompositeLit)
	if !ok || len(lit.Elts) != 0 {
		return nil, nil, false
	}

	typ, ok := lit.Type.(*ast.ArrayType)
	if !ok || typ.Len != nil {
		return nil, nil, false
	}

	return name, typ, true
}

// onlyNilSafeUses reports whether every use of the identifier name in list is one
// that behaves the same for a nil slice and an empty one: an append assigned back to
// the name, a range, a len or cap, or an index. Every occurrence must be such a use.
func onlyNilSafeUses(list []ast.Stmt, name string) bool {
	total := 0
	safe := 0

	for _, s := range list {
		ast.Inspect(s, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && id.Name == name {
				total++
			}

			switch x := n.(type) {
			case *ast.AssignStmt:
				// x = append(x, ...)
				if x.Tok == token.ASSIGN && len(x.Lhs) == 1 && len(x.Rhs) == 1 && isIdent(x.Lhs[0], name) {
					if call, ok := x.Rhs[0].(*ast.CallExpr); ok && isBuiltinCall(call, "append") && len(call.Args) > 0 && isIdent(call.Args[0], name) {
						safe += 2
					}
				}
			case *ast.RangeStmt:
				if isIdent(x.X, name) {
					safe++
				}
			case *ast.CallExpr:
				if (isBuiltinCall(x, "len") || isBuiltinCall(x, "cap")) && len(x.Args) == 1 && isIdent(x.Args[0], name) {
					safe++
				}
			case *ast.IndexExpr:
				if isIdent(x.X, name) {
					safe++
				}
			}

			return true
		})
	}

	return total == safe
}
