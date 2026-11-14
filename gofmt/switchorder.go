package main

import (
	"bytes"
	"forkmat/go/printer"
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// concreteType reports whether the type expression e names a type that is not an interface, so that no two of the types of a type switch can both match a value. A named type is looked up in types; a type that cannot be found is not concrete.
func concreteType(e ast.Expr, types map[string]ast.Expr) bool {
	for range 8 {
		switch x := e.(type) {
		case *ast.ArrayType, *ast.ChanType, *ast.FuncType, *ast.MapType, *ast.StarExpr, *ast.StructType:
			return true
		case *ast.Ident:
			if predeclaredPure[x.Name] {
				return true
			}

			next, ok := types[x.Name]
			if !ok {
				return false
			}

			e = next
		case *ast.ParenExpr:
			e = x.X
		default:
			return false
		}
	}

	return false
}

// exprText returns the source text of an expression, as the printer writes it.
func exprText(e ast.Expr) string {
	var buf bytes.Buffer

	cfg := printer.Config{
		Mode: printer.RawFormat,
	}

	if err := cfg.Fprint(&buf, token.NewFileSet(), e); err != nil {
		return ""
	}

	return buf.String()
}

// onDisk reports whether filename names a file that exists. A file read from standard input has a placeholder name, and its directory is not its package.
func onDisk(filename string) bool {
	_, err := os.Stat(filename)

	return err == nil
}

// packageConstIndex returns the position of each constant of the package of the file named filename, in the order of the files of the package, by name, and then of the declarations within each file. file is that file, parsed. A file that cannot be read adds no constants.
func packageConstIndex(filename string, file *ast.File) map[string]int {
	index := map[string]int{}
	add := func(f *ast.File) {
		for _, d := range f.Decls {
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
	}
	dir := filepath.Dir(filename)
	self := filepath.Base(filename)
	entries, err := os.ReadDir(dir)
	if err != nil || !onDisk(filename) {
		// read from standard input there is no package to scan, only the file
		add(file)
		return index
	}

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			continue
		}

		if name == self {
			add(file)
			continue
		}

		f := parsedSibling(filepath.Join(dir, name))
		if f == nil || f.Name.Name != file.Name.Name {
			continue
		}
		add(f)
	}

	return index
}

// packageTypeExprs returns the named types of the file and of the other files of its package, by name, as their type expressions.
func packageTypeExprs(filename string, file *ast.File) map[string]ast.Expr {
	dir := filepath.Dir(filename)

	if !onDisk(filename) {
		dir = ""
	}

	types := packageTypes(dir, filepath.Base(filename), file.Name.Name)

	for _, d := range file.Decls {
		if g, ok := d.(*ast.GenDecl); ok && g.Tok == token.TYPE {
			for _, sp := range g.Specs {
				ts := sp.(*ast.TypeSpec)
				types[ts.Name.Name] = ts.Type
			}
		}
	}

	return types
}

// reorderSwitchCases orders the non-default clauses of body by the declaration index of their constants, if the switch qualifies (see switchCaseOrder).
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

// reorderTypeSwitchCases orders the non-default clauses of a type switch by their types, alphabetically, if every type is concrete (see concreteType). The clause bodies do not change. A type switch cannot fall through, and a break leaves the switch whichever clause it is in, so the bodies need not return. A clause with several types is ordered by its first type in alphabetical order. The default clause keeps its slot.
func reorderTypeSwitchCases(body *ast.BlockStmt, types map[string]ast.Expr, comments []*ast.CommentGroup) {
	keys := map[*ast.CaseClause]string{}

	var slots []int

	for i, cl := range body.List {
		c := cl.(*ast.CaseClause)
		if c.List == nil {
			continue
		}

		var key string

		for _, e := range c.List {
			if !concreteType(e, types) {
				return
			}

			if text := exprText(e); key == "" || text < key {
				key = text
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
		return strings.Compare(keys[a], keys[b])
	})

	list := slices.Clone(body.List)

	for k, i := range slots {
		list[i] = clauses[k]
	}

	body.List = list
}

// This file implements the case order of -strict: the clauses of a switch whose cases are constants declared in the file are put in the order the constants are declared in, typically the order of an iota block. A clause moves only among the slots of the other clauses of its kind; the default clause keeps its slot. The switch is changed only if its behaviour cannot change: every clause that is not the default returns, and none of them can fall through or leave the switch with a break. Each case is a constant named in the file, so no case expression runs code, and a value matches at most one case.

// switchCaseOrder reorders the clauses of the switch statements of file, whose name is filename. The constants of the whole package count: those of the file and of the other files of its directory with the same package name.
func switchCaseOrder(filename string, file *ast.File) {
	index := packageConstIndex(filename, file)
	types := packageTypeExprs(filename, file)

	ast.Inspect(file, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.SwitchStmt:
			if len(index) > 0 {
				reorderSwitchCases(s.Body, index, file.Comments)
			}
		case *ast.TypeSwitchStmt:
			reorderTypeSwitchCases(s.Body, types, file.Comments)
		}

		return true
	})
}
