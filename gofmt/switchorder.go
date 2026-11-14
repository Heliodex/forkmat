package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// packageConstIndex returns the position of each constant of the package of the
// file named filename, in the order of the files of the package, by name, and
// then of the declarations within each file. file is that file, parsed. A file
// that cannot be read adds no constants.
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

		f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if err != nil || f.Name.Name != file.Name.Name {
			continue
		}
		add(f)
	}

	return index
}

// reorderSwitchCases orders the non-default clauses of body by the declaration
// index of their constants, if the switch qualifies (see switchCaseOrder).
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

// This file implements the case order of -strict: the clauses of a switch whose
// cases are constants declared in the file are put in the order the constants
// are declared in, typically the order of an iota block. A clause moves only
// among the slots of the other clauses of its kind; the default clause keeps its
// slot. The switch is changed only if its behaviour cannot change: every clause
// that is not the default returns, and none of them can fall through or leave the
// switch with a break. Each case is a constant named in the file, so no case
// expression runs code, and a value matches at most one case.

// switchCaseOrder reorders the clauses of the switch statements of file, whose
// name is filename. The constants of the whole package count: those of the file
// and of the other files of its directory with the same package name.
func switchCaseOrder(filename string, file *ast.File) {
	index := packageConstIndex(filename, file)
	if len(index) == 0 {
		return
	}
	ast.Inspect(file, func(n ast.Node) bool {
		if s, ok := n.(*ast.SwitchStmt); ok {
			reorderSwitchCases(s.Body, index, file.Comments)
		}

		return true
	})
}

// onDisk reports whether filename names a file that exists. A file read from
// standard input has a placeholder name, and its directory is not its package.
func onDisk(filename string) bool {
	_, err := os.Stat(filename)
	return err == nil
}
