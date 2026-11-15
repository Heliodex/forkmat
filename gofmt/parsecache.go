package main

import (
	"go/ast"
	"go/parser"
	"go/token"
)

// This file caches the sibling files of a package that the -strict passes read.
// Every file of a directory reads the same siblings, so each is parsed once per run.
// The trees are only read, never changed, so the workers share them.

var siblingCache memo[string, *ast.File]

// parsedSibling returns the parsed file at path, with object resolution skipped, or
// nil if the file cannot be parsed. The result is kept for the rest of the run.
func parsedSibling(path string) *ast.File {
	return siblingCache.get(path, func() *ast.File {
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil
		}

		return f
	})
}
