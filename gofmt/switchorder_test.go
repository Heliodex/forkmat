package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// caseOrder returns, for each switch of the file after switchCaseOrder, its
// clauses' case expressions in order ("default" for the default clause).
func caseOrder(t *testing.T, filename, src string) []string {
	t.Helper()

	file, err := parser.ParseFile(token.NewFileSet(), filename, src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	switchCaseOrder(filename, file)

	var out []string

	ast.Inspect(file, func(n ast.Node) bool {
		s, ok := n.(*ast.SwitchStmt)
		if !ok {
			return true
		}

		var names []string

		for _, cl := range s.Body.List {
			c := cl.(*ast.CaseClause)
			if c.List == nil {
				names = append(names, "default")
				continue
			}

			var parts []string

			for _, e := range c.List {
				parts = append(parts, e.(*ast.Ident).Name)
			}

			names = append(names, strings.Join(parts, ","))
		}

		out = append(out, strings.Join(names, " "))

		return true
	})
	return out
}

func TestSwitchCaseOrder(t *testing.T) {
	const consts = "package p\n\nconst (\n\tKindA = iota\n\tKindC\n\tKindB\n)\n\n"

	tests := []struct {
		name, body, want string
	}{
		{
			name: "cases follow the declaration, the default keeps its slot",
			body: "switch k {\ncase KindB:\nreturn\ncase KindA:\nreturn\ndefault:\nreturn\ncase KindC:\nreturn\n}",
			want: "KindA KindC default KindB",
		},
		{
			name: "a clause that does not return keeps the switch",
			body: "switch k {\ncase KindB:\nprintln()\ncase KindA:\nreturn\n}",
			want: "KindB KindA",
		},
		{
			name: "constants of another file of the package count",
			body: "switch k {\ncase Far:\nreturn\ncase KindA:\nreturn\n}",
			want: "KindA Far",
		},
		{
			name: "a case that is not a declared constant keeps the switch",
			body: "switch k {\ncase Other:\nreturn\ncase KindA:\nreturn\n}",
			want: "Other KindA",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			name := filepath.Join(dir, "x.go")
			src := consts + "func f(k int) {\n" + tt.body + "\n}\n"

			if err := os.WriteFile(name, []byte(src), 0o644); err != nil {
				t.Fatal(err)
			}
			// a constant of another file of the package, declared after the others
			if err := os.WriteFile(filepath.Join(dir, "y.go"), []byte("package p\n\nconst Far = 99\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			if got := strings.Join(caseOrder(t, name, src), ";"); got != tt.want {
				t.Errorf("order = %q, want %q", got, tt.want)
			}
		})
	}
}
