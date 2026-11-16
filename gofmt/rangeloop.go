package main

import (
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
)

// rangeOver22Dirs remembers rangeOver22, by directory.
var rangeOver22Dirs memo[string, bool]

// integerType reports whether t is an integer type, or an untyped integer.
func integerType(t types.Type) bool {
	b, ok := t.Underlying().(*types.Basic)

	return ok && b.Info()&types.IsInteger != 0
}

// isIdent reports whether e is the identifier name.
func isIdent(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)

	return ok && id.Name == name
}

// isBuiltinCall reports whether call calls the builtin name.
func isBuiltinCall(call *ast.CallExpr, name string) bool {
	return isIdent(call.Fun, name)
}

// isZeroLit reports whether e is the integer literal 0.
func isZeroLit(e ast.Expr) bool {
	lit, ok := e.(*ast.BasicLit)

	return ok && lit.Kind == token.INT && lit.Value == "0"
}

// loopNames returns the identifiers an expression uses.
func loopNames(e ast.Expr) map[string]bool {
	out := map[string]bool{}

	ast.Inspect(e, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok {
			out[id.Name] = true
		}

		return true
	})
	return out
}

// loopPureBound reports whether a bound is an expression whose evaluation has no
// side effects: identifiers, literals, selectors, dereferences, arithmetic, and the
// calls len, cap, Len and NumField.
func loopPureBound(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.BasicLit, *ast.Ident:
		return true
	case *ast.BinaryExpr:
		return loopPureBound(x.X) && loopPureBound(x.Y)
	case *ast.CallExpr:
		if len(x.Args) > 1 {
			return false
		}

		if id, ok := x.Fun.(*ast.Ident); ok && (id.Name == "len" || id.Name == "cap") {
			return len(x.Args) == 1 && loopPureBound(x.Args[0])
		}

		if sel, ok := x.Fun.(*ast.SelectorExpr); ok && (sel.Sel.Name == "Len" || sel.Sel.Name == "NumField") && len(x.Args) == 0 {
			return loopPureBound(sel.X)
		}
	case *ast.ParenExpr:
		return loopPureBound(x.X)
	case *ast.SelectorExpr:
		return loopPureBound(x.X)
	case *ast.StarExpr:
		return loopPureBound(x.X)
	}

	return false
}

// loopRoot returns the identifier at the root of an assignable expression, such as
// x for x, x.f and x[i], or "" if there is none.
func loopRoot(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.IndexExpr:
		return loopRoot(x.X)
	case *ast.ParenExpr:
		return loopRoot(x.X)
	case *ast.SelectorExpr:
		return loopRoot(x.X)
	case *ast.StarExpr:
		return loopRoot(x.X)
	}

	return ""
}

// loopAssigns reports whether the body assigns to, increments, or takes the
// address of any of the identifiers in set.
func loopAssigns(body ast.Node, set map[string]bool) (found bool) {
	ast.Inspect(body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.AssignStmt:
			for _, l := range x.Lhs {
				found = found || set[loopRoot(l)]
			}
		case *ast.IncDecStmt:
			found = found || set[loopRoot(x.X)]
		case *ast.RangeStmt:
			for _, l := range []ast.Expr{x.Key, x.Value} {
				found = found || l != nil && set[loopRoot(l)]
			}
		case *ast.UnaryExpr:
			found = found || x.Op == token.AND && set[loopRoot(x.X)]
		}

		return !found
	})
	return
}

// loopSimpleRef reports whether e is an identifier or a selector chain of them.
func loopSimpleRef(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.Ident:
		return true
	case *ast.SelectorExpr:
		return loopSimpleRef(x.X)
	}

	return false
}

// loopUses reports whether the body refers to the variable obj, the index of the loop, by the object the type checker resolved its identifiers to. A name alone would not do: the field f.i and a key i: of a struct literal spell the name without referring to the variable.
func loopUses(body ast.Node, info *types.Info, obj types.Object) (found bool) {
	ast.Inspect(body, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && info.Uses[id] == obj {
			found = true
		}

		return !found
	})
	return
}

// moduleRangeOver22 is rangeOver22 for the directory dir, which is absolute.
func moduleRangeOver22(dir string) bool {
	for {
		if data, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil {
			for line := range strings.SplitSeq(string(data), "\n") {
				if v, ok := strings.CutPrefix(strings.TrimSpace(line), "go "); ok {
					major, minor, ok := strings.Cut(strings.TrimSpace(v), ".")
					if !ok {
						return true
					}

					ma, err1 := strconv.Atoi(major)
					mi, err2 := strconv.Atoi(strings.TrimRight(minor, "abcdefghijklmnopqrstuvwxyz"))
					if err1 != nil || err2 != nil {
						return true
					}

					return ma > 1 || ma == 1 && mi >= 22
				}
			}

			return true
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return true
		}

		dir = parent
	}
}

// This file implements the loop rewrite of -strict: a loop for i := 0; i < n; i++
// becomes for i := range n, when the body does not change i or the bound. A bound
// len(x), where x is a slice or an array, becomes range x. A loop that does not use
// i becomes for range n. The types come from checkTypes, so a bound whose type
// cannot be found keeps its loop.

// rangeOver22 reports whether a range over an integer is allowed in the file named filename: the go version of its module, from its go.mod, is 1.22 or later. A file outside any module has no version to hold to, and is taken to allow it.
func rangeOver22(filename string) bool {
	dir := filepath.Dir(filename)

	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}

	return rangeOver22Dirs.get(dir, func() bool {
		return moduleRangeOver22(dir)
	})
}

// sliceOrArray reports whether t is a slice, an array, or a pointer to an array.
func sliceOrArray(t types.Type) bool {
	switch u := t.Underlying().(type) {
	case *types.Array, *types.Slice:
		return true
	case *types.Pointer:
		_, ok := u.Elem().Underlying().(*types.Array)

		return ok
	}

	return false
}

// rangeOf returns the range statement that replaces the index loop fs, if fs
// qualifies (see rangeLoops).
func rangeOf(fs *ast.ForStmt, typesOf func() *types.Info, comments []*ast.CommentGroup) (*ast.RangeStmt, bool) {
	init, ok := fs.Init.(*ast.AssignStmt)
	if !ok || init.Tok != token.DEFINE || len(init.Lhs) != 1 || len(init.Rhs) != 1 || !isZeroLit(init.Rhs[0]) {
		return nil, false
	}

	idx, ok := init.Lhs[0].(*ast.Ident)
	if !ok {
		return nil, false
	}

	cond, ok := fs.Cond.(*ast.BinaryExpr)
	if !ok || cond.Op != token.LSS || !isIdent(cond.X, idx.Name) {
		return nil, false
	}

	post, ok := fs.Post.(*ast.IncDecStmt)
	if !ok || post.Tok != token.INC || !isIdent(post.X, idx.Name) {
		return nil, false
	}

	bound := cond.Y
	if !loopPureBound(bound) || loopAssigns(fs.Body, loopNames(bound)) || loopAssigns(fs.Body, map[string]bool{idx.Name: true}) {
		return nil, false
	}

	// comments in the header would be lost with it
	if commentIn(comments, fs.For, fs.Body.Lbrace) {
		return nil, false
	}

	// the types are only needed for a loop that has passed the checks above
	info := typesOf()

	// the index must be resolved, or it cannot be told apart from the other names in the body
	index := info.Defs[idx]
	if index == nil {
		return nil, false
	}

	if tv, ok := info.Types[bound]; !ok || !integerType(tv.Type) {
		return nil, false
	}

	// the range expression: len(x) of a slice or an array is x itself
	rng := bound

	if call, ok := bound.(*ast.CallExpr); ok && isBuiltinCall(call, "len") && len(call.Args) == 1 {
		arg := call.Args[0]
		if at, ok := info.Types[arg]; ok && loopSimpleRef(arg) && sliceOrArray(at.Type) {
			rng = arg
		}
	}

	rs := &ast.RangeStmt{
		For: fs.For,
		TokPos: init.TokPos,
		Tok: token.DEFINE,
		Range: init.TokPos,
		X: rng,
		Body: fs.Body,
	}

	if loopUses(fs.Body, info, index) {
		rs.Key = idx
	}

	return rs, true
}

// rangeLoops rewrites the index loops of file. comments are the file's comments. typesOf gives the types of the file, which are only asked for once a loop has passed the syntactic checks, so a file without such a loop is not type-checked.
func rangeLoops(filename string, file *ast.File, comments []*ast.CommentGroup, typesOf func() *types.Info) {
	repl := map[*ast.ForStmt]ast.Stmt{}

	// a range over an integer needs go 1.22, so a module older than that keeps its loops
	allowed := rangeOver22(filename)

	ast.Inspect(file, func(n ast.Node) bool {
		if fs, ok := n.(*ast.ForStmt); ok && allowed {
			if rs, ok := rangeOf(fs, typesOf, comments); ok {
				repl[fs] = rs
			}
		}

		return true
	})
	if len(repl) == 0 {
		return
	}

	forPtrType := reflect.TypeFor[*ast.ForStmt]()

	var rewriteVal func(val reflect.Value) reflect.Value

	rewriteVal = func(val reflect.Value) reflect.Value {
		if val.IsValid() && val.Type() == forPtrType {
			if r, ok := repl[val.Interface().(*ast.ForStmt)]; ok {
				return rewriteVal(reflect.ValueOf(r))
			}
		}

		return apply(rewriteVal, val)
	}

	rewriteVal(reflect.ValueOf(file))
}
