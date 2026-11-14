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

// A file read from standard input has no file name on disk, but its own constants
// still count, and no other file of its directory does.
func TestSwitchCaseOrderStdin(t *testing.T) {
	src := "package p\n\nconst (\n\tia = iota\n\tib\n)\n\nfunc f(k int) int {\n\tswitch k {\n\tcase ib:\n\t\treturn 2\n\tcase ia:\n\t\treturn 1\n\t}\n\treturn 0\n}\n"
	file, err := parser.ParseFile(token.NewFileSet(), "<standard input>", src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	switchCaseOrder("<standard input>", file)
	var got []string
	ast.Inspect(file, func(n ast.Node) bool {
		if s, ok := n.(*ast.SwitchStmt); ok {
			for _, cl := range s.Body.List {
				got = append(got, cl.(*ast.CaseClause).List[0].(*ast.Ident).Name)
			}
		}
		return true
	})
	if want := []string{"ia", "ib"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v", got, want)
	}
}

// typeCases returns, for each type switch of src after switchCaseOrder, its
// clauses' types in order ("default" for the default clause).
func typeCases(t *testing.T, src string) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "x.go", src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	switchCaseOrder("x.go", file)
	var out []string
	ast.Inspect(file, func(n ast.Node) bool {
		s, ok := n.(*ast.TypeSwitchStmt)
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
				parts = append(parts, exprText(e))
			}
			names = append(names, strings.Join(parts, ","))
		}
		out = append(out, strings.Join(names, " "))
		return true
	})
	return out
}

func TestTypeSwitchCaseOrder(t *testing.T) {
	tests := []struct{ name, src, want string }{
		{
			name: "concrete types are sorted, the default keeps its slot",
			src:  "package p\n\nfunc f(v any) {\n\tswitch v.(type) {\n\tcase string:\n\t\treturn\n\tcase int:\n\t\treturn\n\tdefault:\n\t\treturn\n\tcase bool:\n\t\treturn\n\t}\n}\n",
			want: "bool int default string",
		},
		{
			name: "an interface type keeps the switch",
			src:  "package p\n\ntype I interface{ M() }\n\nfunc f(v any) {\n\tswitch v.(type) {\n\tcase string:\n\t\treturn\n\tcase I:\n\t\treturn\n\t}\n}\n",
			want: "string I",
		},
		{
			name: "a clause that does not return keeps the switch",
			src:  "package p\n\nfunc f(v any) {\n\tswitch v.(type) {\n\tcase string:\n\t\tprintln()\n\tcase int:\n\t\treturn\n\t}\n}\n",
			want: "string int",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := strings.Join(typeCases(t, tt.src), ";"); got != tt.want {
				t.Errorf("types = %q, want %q", got, tt.want)
			}
		})
	}
}
