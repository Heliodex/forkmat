package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"maps"
	"slices"
	"strings"

	"github.com/bombsimon/wsl/v5"
	"golang.org/x/tools/go/analysis"
)

type (
	// listedStmt is a statement of a statement list, with the list that holds it and its index in that list.
	listedStmt struct {
		stmt ast.Stmt
		list []ast.Stmt
		index int
	}

	// wslDiag is a diagnostic of wsl: its position, and its message.
	wslDiag struct {
		pos token.Pos
		message string
	}
)

const (
	// maxWSLRounds bounds how many times -wsl prints a file, since a blank line can change what wsl reports about the statements around it.
	maxWSLRounds = 8

	// This file implements -wsl. Under -strict the blank lines between statements come from the layout's own rules, which do not follow the wsl linter (github.com/bombsimon/wsl/v5, with its default checks): wsl asks for a blank line where a statement is cuddled with one it may not follow, and for none where a statement is separated from one it must follow, such as an if err != nil check after the assignment of err. -wsl prints the file, runs wsl on the output, and sets the blank line of each statement that wsl reports, then prints again, until wsl reports nothing or the layout stops changing.

	// The messages of wsl that -wsl acts on. Each begins its message; the rest of a message names the check.
	messageMissingAbove = "missing whitespace above this line"
	messageMissingBelow = "missing whitespace below this line"
	messageUnnecessary = "unnecessary whitespace"
)

// listedStmts returns every statement of a statement list of file (a block, or the body of a case or communication clause), in source order.
func listedStmts(file *ast.File) (out []listedStmt) {
	add := func(list []ast.Stmt) {
		for i, s := range list {
			out = append(out, listedStmt{
				stmt: s,
				list: list,
				index: i,
			})
		}
	}

	ast.Inspect(file, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.BlockStmt:
			add(x.List)
		case *ast.CaseClause:
			add(x.Body)
		case *ast.CommClause:
			add(x.Body)
		}

		return true
	})
	slices.SortStableFunc(out, func(a, b listedStmt) int {
		return int(a.stmt.Pos()) - int(b.stmt.Pos())
	})

	return
}

// wslBlanks sets the blank lines that wsl asks for, from its diagnostics of the output out, which was parsed into file, and whose statements are in fset. The statements are numbered as StmtBlank numbers them, in source order. It returns the blank lines to print with next, and whether they changed.
func wslBlanks(out []byte, fset *token.FileSet, diags []wslDiag, file *ast.File, blank map[int]bool) (map[int]bool, bool) {
	listed := listedStmts(file)
	next := maps.Clone(blank)
	lines := strings.Split(string(out), "\n")

	// number maps each statement to its number
	number := make(map[ast.Stmt]int, len(listed))

	for i, s := range listed {
		number[s.stmt] = i
	}

	for _, d := range diags {
		switch {
		case strings.HasPrefix(d.message, messageMissingAbove):
			// the statement that starts at the reported position
			for i, s := range listed {
				if s.stmt.Pos() == d.pos {
					next[i] = true
				}
			}
		case strings.HasPrefix(d.message, messageMissingBelow):
			// the statement after the innermost one that holds the reported position
			inner := -1

			for i, s := range listed {
				if s.stmt.Pos() <= d.pos && d.pos <= s.stmt.End() {
					inner = i
				}
			}

			if inner < 0 || listed[inner].index+1 >= len(listed[inner].list) {
				continue
			}

			if n, ok := number[listed[inner].list[listed[inner].index+1]]; ok {
				next[n] = true
			}
		case strings.HasPrefix(d.message, messageUnnecessary):
			// the statement that starts on the first line after the reported blank line that is not blank
			line := fset.Position(d.pos).Line

			for n := line + 1; n <= len(lines); n++ {
				if strings.TrimSpace(lines[n-1]) == "" {
					continue
				}

				for i, s := range listed {
					if fset.Position(s.stmt.Pos()).Line == n {
						next[i] = false
					}
				}

				break
			}
		}
	}

	return next, !maps.Equal(next, blank)
}

// wslTypes returns the types of the package of file, with file as one of its files, for wsl to read.
func wslTypes(fset *token.FileSet, filename string, file *ast.File) *types.Info {
	files := []*ast.File{file}

	if pkgFiles, ok := packageFiles(fset, filename, file); ok {
		files = pkgFiles
	}

	info := &types.Info{
		Types: map[ast.Expr]types.TypeAndValue{},
		Defs: map[*ast.Ident]types.Object{},
		Uses: map[*ast.Ident]types.Object{},
	}
	conf := types.Config{
		Importer: sharedImporter,
		Error: func(error) {},
	}

	conf.Check(file.Name.Name, fset, files, info)

	return info
}

// wslDiagnostics returns what wsl reports about file, which is in fset and was parsed from filename.
func wslDiagnostics(fset *token.FileSet, filename string, file *ast.File) []wslDiag {
	var diags []wslDiag

	pass := &analysis.Pass{
		Analyzer: wsl.NewAnalyzer(nil),
		Fset: fset,
		Files: []*ast.File{file},
		TypesInfo: wslTypes(fset, filename, file),
		Report: func(d analysis.Diagnostic) {
			diags = append(diags, wslDiag{
				pos: d.Pos,
				message: d.Message,
			})
		},
	}

	if _, err := pass.Analyzer.Run(pass); err != nil {
		return nil
	}

	return diags
}

// wslLayout prints the file with printFile, which takes the blank lines to give each statement, numbered as StmtBlank numbers them. It runs wsl on the result, and sets the blank lines that wsl asks for, until wsl reports nothing. The result is the output of the last print. filename is the file's name.
func wslLayout(filename string, printFile func(map[int]bool) ([]byte, error)) ([]byte, error) {
	// wsl reads a file by its name, so a file that has none on disk is checked under a name that ends in .go
	name := filename
	if !strings.HasSuffix(name, ".go") {
		name = "x.go"
	}

	blank := map[int]bool{}

	var out []byte

	for range maxWSLRounds {
		var err error
		if out, err = printFile(blank); err != nil {
			return nil, err
		}

		fset := token.NewFileSet()

		parsed, err := parser.ParseFile(fset, name, out, parser.ParseComments)
		if err != nil {
			return out, nil
		}

		diags := wslDiagnostics(fset, filename, parsed)
		if len(diags) == 0 {
			return out, nil
		}

		next, changed := wslBlanks(out, fset, diags, parsed, blank)
		if !changed {
			return out, nil
		}

		blank = next
	}

	return out, nil
}
