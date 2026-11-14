package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// incDecOps returns the statements of the body of f in src after incDecStmts, each
// written as its operator: "++", "--", or "=" for any other assignment.
func incDecOps(t *testing.T, src string) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "x.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	incDecStmts(file)
	var out []string
	ast.Inspect(file, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.IncDecStmt:
			out = append(out, s.Tok.String())
		case *ast.AssignStmt:
			out = append(out, "=")
		}
		return true
	})
	return out
}

func TestIncDecStmts(t *testing.T) {
	tests := []struct{ name, body, want string }{
		{"plus one", "x = x + 1", "++"},
		{"minus one", "x = x - 1", "--"},
		{"one on the left", "x = 1 + x", "="},
		{"another constant", "x = x + 2", "="},
		{"another name", "x = y + 1", "="},
		{"a string", "s = s + \"a\"", "="},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := "package p\n\nfunc f(x, y int, s string) {\n\t" + tt.body + "\n}\n"
			if got := strings.Join(incDecOps(t, src), ","); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
