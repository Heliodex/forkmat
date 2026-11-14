package main

import (
	"go/ast"
	"go/importer"
	"go/token"
	"go/types"
	"reflect"
)

// This file implements -unconvert: a conversion T(x) is removed when x already
// has the type T. The types come from type-checking the file on its own. Names
// that cannot be resolved from this file (declared in another file, or in a
// package that cannot be imported) have no type, so a conversion of them is
// kept: the check only removes what it can prove redundant.
//
// A conversion of a constant is kept. Removing int64(5) would change the type
// of the constant from int64 to its default. A conversion whose type names a
// member of an imported package is kept too, since removing it could leave the
// import unused.

// unconvert returns file with its redundant conversions removed. file must be
// parsed with comments; it is type-checked as a package of its own.
func unconvert(fset *token.FileSet, file *ast.File) *ast.File {
	info := &types.Info{Types: map[ast.Expr]types.TypeAndValue{}}
	conf := types.Config{
		Importer: importer.ForCompiler(fset, "source", nil),
		Error:    func(error) {}, // errors are expected for names declared elsewhere
	}
	conf.Check(file.Name.Name, fset, []*ast.File{file}, info)

	repl := map[*ast.CallExpr]ast.Expr{}
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || call.Ellipsis.IsValid() || len(call.Args) != 1 || hasSelector(call.Fun) {
			return true
		}
		if tv, ok := info.Types[call.Fun]; !ok || !tv.IsType() {
			return true
		}
		target := info.Types[call.Fun].Type
		arg := call.Args[0]
		av := info.Types[arg]
		if av.Type == nil || av.Value != nil || isUntyped(av.Type) || !types.Identical(target, av.Type) {
			return true
		}
		repl[call] = operand(arg)
		return true
	})
	if len(repl) == 0 {
		return file
	}

	var rewriteVal func(val reflect.Value) reflect.Value
	rewriteVal = func(val reflect.Value) reflect.Value {
		if val.IsValid() && val.Type() == callExprPtrType {
			if r, ok := repl[val.Interface().(*ast.CallExpr)]; ok {
				// the replacement may hold further conversions to remove
				return rewriteVal(reflect.ValueOf(r))
			}
		}
		return apply(rewriteVal, val)
	}
	return rewriteVal(reflect.ValueOf(file)).Interface().(*ast.File)
}

// callExprPtrType is the type of a *ast.CallExpr.
var callExprPtrType = reflect.TypeFor[*ast.CallExpr]()

// hasSelector reports whether an expression holds a selector, such as pkg.T.
func hasSelector(e ast.Expr) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if _, ok := n.(*ast.SelectorExpr); ok {
			found = true
		}
		return !found
	})
	return found
}

// isUntyped reports whether t is an untyped basic type, such as an untyped
// boolean from a comparison.
func isUntyped(t types.Type) bool {
	b, ok := t.(*types.Basic)
	return ok && b.Info()&types.IsUntyped != 0
}

// operand returns the expression that stands in for a conversion T(x) once it
// is removed: x itself, in parentheses when its operators would bind differently
// in the place of the conversion. The parentheses are dropped again by the
// printer under -strict when they are not needed.
func operand(x ast.Expr) ast.Expr {
	switch x.(type) {
	case *ast.Ident, *ast.BasicLit, *ast.CallExpr, *ast.SelectorExpr,
		*ast.IndexExpr, *ast.IndexListExpr, *ast.SliceExpr, *ast.ParenExpr,
		*ast.CompositeLit, *ast.TypeAssertExpr:
		return x
	}
	return &ast.ParenExpr{X: x}
}
