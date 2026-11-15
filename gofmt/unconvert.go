package main

import (
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"reflect"
)

// callExprPtrType is the type of a *ast.CallExpr.
var callExprPtrType = reflect.TypeFor[*ast.CallExpr]()

// checkTypes type-checks file on its own and returns the types of its expressions.
// Errors are expected for names declared elsewhere, and are ignored: an expression
// whose type cannot be found has no entry.
func checkTypes(fset *token.FileSet, file *ast.File) *types.Info {
	info := &types.Info{
		Types: map[ast.Expr]types.TypeAndValue{},
		Defs: map[*ast.Ident]types.Object{},
		Uses: map[*ast.Ident]types.Object{},
	}
	conf := types.Config{
		Importer: sharedImporter,
		Error: func(error) {},
	}

	conf.Check(file.Name.Name, fset, []*ast.File{file}, info)
	return info
}

// hasSelector reports whether an expression holds a selector, such as pkg.T.
func hasSelector(e ast.Expr) bool {
	var found bool

	ast.Inspect(e, func(n ast.Node) bool {
		if _, ok := n.(*ast.SelectorExpr); ok {
			found = true
		}

		return !found
	})
	return found
}

// isUntyped reports whether t is an untyped basic type, such as an untyped boolean from a comparison.
func isUntyped(t types.Type) bool {
	b, ok := t.(*types.Basic)

	return ok && b.Info()&types.IsUntyped != 0
}

// operand returns the expression that stands in for a conversion T(x) once it is removed: x itself, in parentheses when its operators would bind differently in the place of the conversion. The parentheses are dropped again by the printer under -strict when they are not needed.
func operand(x ast.Expr) ast.Expr {
	switch x.(type) {
	case *ast.BasicLit, *ast.CallExpr, *ast.CompositeLit, *ast.Ident, *ast.IndexExpr, *ast.IndexListExpr, *ast.ParenExpr, *ast.SelectorExpr, *ast.SliceExpr, *ast.TypeAssertExpr:
		return x
	}

	// the parentheses take the operand's position: without one, the printer takes the call's arguments to be on another line, and lays them out so
	return &ast.ParenExpr{
		Lparen: x.Pos(),
		X: x,
		Rparen: x.End(),
	}
}

// packageTypeNames returns the names of the types declared in the package of the file, in the other files as well as in it, and the types declared inside functions of the file.
func packageTypeNames(filename string, file *ast.File) map[string]bool {
	dir := filepath.Dir(filename)

	if !onDisk(filename) {
		dir = ""
	}

	names := map[string]bool{}

	for n := range packageTypes(dir, "", file.Name.Name) {
		names[n] = true
	}

	// a type declared inside a function is a name too
	ast.Inspect(file, func(n ast.Node) bool {
		if ts, ok := n.(*ast.TypeSpec); ok {
			names[ts.Name.Name] = true
		}

		return true
	})
	return names
}

// typeLikeFun reports whether the function of a call may be a type: a type literal, or a name in names or a predeclared type, under any parentheses, pointer, or type arguments.
func typeLikeFun(e ast.Expr, names map[string]bool) bool {
	switch x := e.(type) {
	case *ast.ArrayType, *ast.ChanType, *ast.FuncType, *ast.InterfaceType, *ast.MapType, *ast.StructType:
		return true
	case *ast.Ident:
		return universeTypes[x.Name] || names[x.Name]
	case *ast.IndexExpr:
		return typeLikeFun(x.X, names)
	case *ast.IndexListExpr:
		return typeLikeFun(x.X, names)
	case *ast.ParenExpr:
		return typeLikeFun(x.X, names)
	case *ast.StarExpr:
		return typeLikeFun(x.X, names)
	}

	return false
}

// mayConvert reports whether file holds a call that unconvert could remove: one with a single argument, whose function is a type literal, or a name of a type in the package, the file, or a dot import, possibly generic or parenthesized. Only such a file is type-checked, since the check is the slow part of -unconvert and most files have no conversion.
func mayConvert(filename string, file *ast.File) bool {
	names := packageTypeNames(filename, file)

	for n := range dotImportTypes(filename, file) {
		names[n] = true
	}

	var found bool

	ast.Inspect(file, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok && len(call.Args) == 1 && !call.Ellipsis.IsValid() && !hasSelector(call.Fun) && typeLikeFun(call.Fun, names) {
			found = true
		}

		return !found
	})
	return found
}

// This file implements -unconvert: a conversion T(x) is removed when x already has the type T. The types come from type-checking the file on its own. Names that cannot be resolved from this file (declared in another file, or in a package that cannot be imported) have no type, so a conversion of them is kept: the check only removes what it can prove redundant.
//
// A conversion of a constant is kept. Removing int64(5) would change the type of the constant from int64 to its default. A conversion whose type names a member of an imported package is kept too, since removing it could leave the import unused.

// unconvert returns file with its redundant conversions removed. file must be parsed with comments; it is type-checked as a package of its own.
func unconvert(fset *token.FileSet, file *ast.File, info *types.Info) *ast.File {
	repl := map[*ast.CallExpr]ast.Expr{}

	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || call.Ellipsis.IsValid() || len(call.Args) != 1 || hasSelector(call.Fun) {
			return true
		}

		if tv, ok := info.Types[call.Fun]; !ok || !tv.IsType() {
			return true
		}

		arg := call.Args[0]
		av := info.Types[arg]
		if target := info.Types[call.Fun].Type; av.Type == nil || av.Value != nil || isUntyped(av.Type) || !types.Identical(target, av.Type) {
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
