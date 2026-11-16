package main

import (
	"go/ast"
	"go/token"
	"go/types"
)

// mentionsName reports whether an identifier of name appears in any of the nodes.
func mentionsName(name string, nodes ...ast.Node) (found bool) {
	for _, n := range nodes {
		ast.Inspect(n, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && id.Name == name {
				found = true
			}

			return !found
		})
	}

	return
}

// renameParam renames every identifier that refers to obj, within the nodes, to name.
func renameParam(info *types.Info, obj types.Object, name string, nodes ...ast.Node) {
	for _, n := range nodes {
		ast.Inspect(n, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && objOf(info, id) == obj {
				id.Name = name
			}

			return true
		})
	}
}

// This file implements -testnames. A parameter of type testing.TB is renamed tb, a parameter of type *testing.T is renamed t, and a parameter of type *testing.B is renamed b, so that the names of test helpers follow the convention that the thelper linter checks. A parameter is only renamed when its new name is not used anywhere in its function, including in a nested function literal, so that the rename cannot capture or hide another name. Every use of the parameter is renamed with it, found by the type checker, so a field or method that happens to have the old name is left alone.

// testParamName returns the name that a parameter of type typ should have, or "" if it has none.
func testParamName(info *types.Info, typ ast.Expr) string {
	star, isPtr := typ.(*ast.StarExpr)
	if isPtr {
		typ = star.X
	}

	sel, ok := typ.(*ast.SelectorExpr)
	if !ok {
		return ""
	}

	tn, ok := info.Uses[sel.Sel].(*types.TypeName)
	if !ok || tn.Pkg() == nil || tn.Pkg().Path() != "testing" {
		return ""
	}

	switch {
	case sel.Sel.Name == "TB" && !isPtr:
		return "tb"
	case sel.Sel.Name == "T" && isPtr:
		return "t"
	case sel.Sel.Name == "B" && isPtr:
		return "b"
	}

	return ""
}

// nameTestParams renames the parameters of a function with type ft and body, where they can be. The parameters are renamed in order, so that a parameter renamed earlier is a name the next must not take.
func nameTestParams(info *types.Info, ft *ast.FuncType, body *ast.BlockStmt) {
	if ft.Params == nil {
		return
	}

	for _, f := range ft.Params.List {
		name := testParamName(info, f.Type)
		if name == "" {
			continue
		}

		for _, id := range f.Names {
			obj := info.Defs[id]
			if id.Name == name || id.Name == "_" || obj == nil || mentionsName(name, ft, body) {
				continue
			}

			renameParam(info, obj, name, ft, body)
		}
	}
}

// testNames applies -testnames to every function of file (see the comment at the top of this file). fset is the file's file set.
func testNames(fset *token.FileSet, file *ast.File) {
	info := checkTypes(fset, file)

	ast.Inspect(file, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncDecl:
			if x.Body != nil {
				nameTestParams(info, x.Type, x.Body)
			}
		case *ast.FuncLit:
			nameTestParams(info, x.Type, x.Body)
		}

		return true
	})
}
