package main

import (
	"go/ast"
	"go/token"
	"reflect"
)

// This file implements the any rewrite of -strict: an empty interface type,
// interface{}, is written any, the predeclared alias of it. A file that declares its
// own any at package level keeps interface{}, since that name would refer to its own
// declaration. The alias needs Go 1.18 or later.

// anyTypes rewrites the empty interface types of file to any.
func anyTypes(file *ast.File) {
	if declaresAny(file) {
		return
	}

	repl := map[*ast.InterfaceType]ast.Expr{}

	ast.Inspect(file, func(n ast.Node) bool {
		if it, ok := n.(*ast.InterfaceType); ok && len(it.Methods.List) == 0 {
			repl[it] = &ast.Ident{NamePos: it.Pos(), Name: "any"}
		}

		return true
	})

	if len(repl) == 0 {
		return
	}

	ifacePtrType := reflect.TypeFor[*ast.InterfaceType]()

	var rewriteVal func(val reflect.Value) reflect.Value

	rewriteVal = func(val reflect.Value) reflect.Value {
		if val.IsValid() && val.Type() == ifacePtrType {
			if r, ok := repl[val.Interface().(*ast.InterfaceType)]; ok {
				return reflect.ValueOf(r)
			}
		}

		return apply(rewriteVal, val)
	}

	rewriteVal(reflect.ValueOf(file))
}

// declaresAny reports whether file declares the name any at package level.
func declaresAny(file *ast.File) bool {
	for _, d := range file.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			if d.Recv == nil && d.Name.Name == "any" {
				return true
			}
		case *ast.GenDecl:
			if d.Tok == token.IMPORT {
				continue
			}

			for _, sp := range d.Specs {
				switch sp := sp.(type) {
				case *ast.TypeSpec:
					if sp.Name.Name == "any" {
						return true
					}
				case *ast.ValueSpec:
					for _, id := range sp.Names {
						if id.Name == "any" {
							return true
						}
					}
				}
			}
		}
	}

	return false
}
