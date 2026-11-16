package main

import (
	"go/ast"
	"go/token"
	"go/types"
	"reflect"
)

// This file implements the new rewrite of -strict: new(T), for a struct type T, is
// written &T{}, the composite literal of T. A literal reads as the value it builds,
// and it can set fields by name, which new cannot. new stays for the other types,
// such as new(int), which have no literal form. A call in the header of a control
// clause also stays, since a composite literal of a bare type name does not parse
// there.

// headerCalls returns the calls that lie in the header of a control clause of file.
func headerCalls(file *ast.File) map[*ast.CallExpr]bool {
	calls := map[*ast.CallExpr]bool{}
	mark := func(n ast.Node) {
		if n == nil {
			return
		}

		ast.Inspect(n, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok {
				calls[c] = true
			}

			return true
		})
	}

	ast.Inspect(file, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.ForStmt:
			mark(x.Init)
			mark(x.Cond)
			mark(x.Post)
		case *ast.IfStmt:
			mark(x.Init)
			mark(x.Cond)
		case *ast.RangeStmt:
			mark(x.X)
		case *ast.SwitchStmt:
			mark(x.Init)
			mark(x.Tag)
		case *ast.TypeSwitchStmt:
			mark(x.Init)
			mark(x.Assign)
		}

		return true
	})

	return calls
}

// structNew returns the type argument T of the call new(T), if new is the builtin
// and T is a struct type. info is the type information of the file, so a new that
// is shadowed by a declaration of the file is not the builtin.
func structNew(c *ast.CallExpr, info *types.Info) (ast.Expr, bool) {
	if len(c.Args) != 1 || c.Ellipsis.IsValid() {
		return nil, false
	}

	id, ok := c.Fun.(*ast.Ident)
	if !ok {
		return nil, false
	}

	if b, ok := info.Uses[id].(*types.Builtin); !ok || b.Name() != "new" {
		return nil, false
	}

	arg := c.Args[0]
	tv, ok := info.Types[arg]

	if !ok || !tv.IsType() {
		return nil, false
	}

	if _, ok := tv.Type.Underlying().(*types.Struct); !ok {
		return nil, false
	}

	return arg, true
}

// newLiterals rewrites the calls new(T) of file, for a struct type T, as &T{}. typesOf
// gives the types of the file, and is only asked once a call has passed the syntactic
// checks, so a file without such a call is not type-checked.
func newLiterals(file *ast.File, typesOf func() *types.Info) {
	skip := headerCalls(file)

	var cands []*ast.CallExpr

	ast.Inspect(file, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok && !skip[c] && isIdent(c.Fun, "new") {
			cands = append(cands, c)
		}

		return true
	})

	if len(cands) == 0 {
		return
	}

	info := typesOf()
	repl := map[*ast.CallExpr]ast.Expr{}

	for _, c := range cands {
		arg, ok := structNew(c, info)
		if !ok {
			continue
		}

		repl[c] = &ast.UnaryExpr{
			OpPos: c.Pos(),
			Op: token.AND,
			X: &ast.CompositeLit{
				Type: arg,
				Lbrace: c.Rparen,
				Rbrace: c.Rparen,
			},
		}
	}

	if len(repl) == 0 {
		return
	}

	callPtrType := reflect.TypeFor[*ast.CallExpr]()

	var rewriteVal func(val reflect.Value) reflect.Value

	rewriteVal = func(val reflect.Value) reflect.Value {
		if val.IsValid() && val.Type() == callPtrType {
			if r, ok := repl[val.Interface().(*ast.CallExpr)]; ok {
				return reflect.ValueOf(r)
			}
		}

		return apply(rewriteVal, val)
	}

	rewriteVal(reflect.ValueOf(file))
}
