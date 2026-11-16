package main

import (
	"bytes"
	"forkmat/go/printer"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSwitchCaseOrderComments checks that a comment inside a clause still sits in that clause in the printed output. A comment is left alone when moving the clauses would put it in another clause.
func TestSwitchCaseOrderComments(t *testing.T) {
	tests := []struct {
		name, body string
		// the names that must come in this order in the printed output
		order []string
	}{
		{
			name: "a comment in a clause that moves earlier stays in it",
			body: "switch k {\ncase KindB:\nreturn 2\ncase KindA:\n// about a\nreturn 1\n}",
			order: []string{"case KindA:", "// about a", "case KindB:"},
		},
		{
			name: "a comment in a clause that a clause before it would pass keeps the switch",
			body: "switch k {\ncase KindB:\n// about b\nreturn 2\ncase KindA:\nreturn 1\n}",
			order: []string{"case KindB:", "// about b", "case KindA:"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := "package p\n\nconst (\n\tKindA = iota\n\tKindB\n)\n\nfunc f(k int) int {\n" + tt.body + "\n\treturn 0\n}\n"
			fset := token.NewFileSet()

			file, err := parser.ParseFile(fset, "x.go", src, parser.ParseComments|parser.SkipObjectResolution)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}

			switchCaseOrder("x.go", file)

			var buf bytes.Buffer

			cfg := printer.Config{
				Mode: printer.UseSpaces | printer.TabIndent,
				Tabwidth: 8,
			}

			if err := cfg.Fprint(&buf, fset, file); err != nil {
				t.Fatal(err)
			}

			out := buf.String()
			last := -1

			for _, want := range tt.order {
				i := strings.Index(out, want)
				if i <= last {
					t.Errorf("%q not after the text before it:\n%s", want, out)
					return
				}

				last = i
			}
		})
	}
}

// TestSwitchCaseOrderStableAcrossSiblings checks that a switch on constants of a sibling file is ordered as the sibling will be once formatted: the sibling's reorder passes sort its constants, and the order must not depend on whether it has been formatted yet.
func TestSwitchCaseOrderStableAcrossSiblings(t *testing.T) {
	const (
		consts string = "package p\n\nconst (\n\tstatusPass = \"pass\"\n\tstatusFail = \"fail\"\n\tstatusError = \"error\"\n)\n"
		sw string = "package p\n\nfunc f(s string) int {\n\tswitch s {\n\tcase statusFail:\n\t\treturn 1\n\tcase statusPass:\n\t\treturn 2\n\tcase statusError:\n\t\treturn 3\n\t}\n\treturn 0\n}\n"
		want string = "statusError statusFail statusPass"
	)

	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte(consts), 0o644); err != nil {
		t.Fatal(err)
	}

	name := filepath.Join(dir, "a.go")

	if err := os.WriteFile(name, []byte(sw), 0o644); err != nil {
		t.Fatal(err)
	}

	// the file is processed as gofmt does it: its reorder passes first, then the switch order
	order := func() string {
		fset := token.NewFileSet()

		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}

		file, err := parser.ParseFile(fset, name, src, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}

		if _, reordered := reorderSource(fset, name, src); reordered != nil {
			file = reordered
		}

		switchCaseOrder(name, file)

		var names []string

		ast.Inspect(file, func(n ast.Node) bool {
			if s, ok := n.(*ast.SwitchStmt); ok {
				for _, cl := range s.Body.List {
					for _, e := range cl.(*ast.CaseClause).List {
						names = append(names, exprText(e))
					}
				}
			}

			return true
		})

		return strings.Join(names, " ")
	}

	first := order()
	if first != want {
		t.Errorf("order = %q, want %q", first, want)
	}

	// the sibling is now as it would be formatted, and the next run must agree
	if second := order(); second != first {
		t.Errorf("second run = %q, first = %q", second, first)
	}
}

// A file read from standard input has no file name on disk, but its own constants still count, and no other file of its directory does.
func TestSwitchCaseOrderStdin(t *testing.T) {
	const src string = "package p\n\nconst (\n\tia = iota\n\tib\n)\n\nfunc f(k int) int {\n\tswitch k {\n\tcase ib:\n\t\treturn 2\n\tcase ia:\n\t\treturn 1\n\t}\n\treturn 0\n}\n"

	fset := token.NewFileSet()

	file, err := parser.ParseFile(fset, "<standard input>", src, parser.ParseComments|parser.SkipObjectResolution)
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

// caseOrder returns, for each switch of the file after switchCaseOrder, its clauses' case expressions in order ("default" for the default clause).
func caseOrder(t *testing.T, filename, src string) (out []string) {
	t.Helper()

	fset := token.NewFileSet()

	file, err := parser.ParseFile(fset, filename, src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	switchCaseOrder(filename, file)
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
				parts = append(parts, exprText(e))
			}

			names = append(names, strings.Join(parts, ","))
		}

		out = append(out, strings.Join(names, " "))

		return true
	})

	return
}

// TestDefaultLast checks that the default clause goes last, unless a fallthrough would change what runs.
func TestDefaultLast(t *testing.T) {
	tests := []struct {
		name, body, want string
	}{
		{
			name: "a default in the middle goes last",
			body: "switch x {\ncase 1:\nreturn 1\ndefault:\nreturn 0\ncase 2:\nreturn 2\n}",
			want: "1 2 default",
		},
		{
			name: "a default that falls through stays",
			body: "switch x {\ncase 1:\nreturn 1\ndefault:\nprintln()\nfallthrough\ncase 2:\nreturn 2\n}",
			want: "1 default 2",
		},
		{
			name: "a clause that falls into the default keeps it in place",
			body: "switch x {\ncase 1:\nprintln()\nfallthrough\ndefault:\nreturn 0\ncase 2:\nreturn 2\n}",
			want: "1 default 2",
		},
		{
			name: "a comment between clauses keeps the default in place",
			body: "switch x {\ncase 1:\nreturn 1\n// before the default\ndefault:\nreturn 0\ncase 2:\nreturn 2\n}",
			want: "1 default 2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := "package p\n\nfunc f(x int) int {\n" + tt.body + "\nreturn 0\n}\n"

			if got := strings.Join(caseOrder(t, "x.go", src), ";"); got != tt.want {
				t.Errorf("order = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSwitchCaseOrder(t *testing.T) {
	const consts = "package p\n\nconst (\n\tKindA = iota\n\tKindC\n\tKindB\n)\n\n"

	tests := []struct {
		name, body, want string
	}{
		{
			name: "cases follow the declaration, the default goes last",
			body: "switch k {\ncase KindB:\nreturn\ncase KindA:\nreturn\ndefault:\nreturn\ncase KindC:\nreturn\n}",
			want: "KindA KindC KindB default",
		},
		{
			name: "a clause that does not return is ordered too",
			body: "switch k {\ncase KindB:\nprintln()\ncase KindA:\nreturn\n}",
			want: "KindA KindB",
		},
		{
			name: "a clause that falls through keeps the switch",
			body: "switch k {\ncase KindB:\nprintln()\nfallthrough\ncase KindA:\nreturn\n}",
			want: "KindB KindA",
		},
		{
			name: "a default that falls through keeps the switch",
			body: "switch k {\ncase KindB:\nreturn\ndefault:\nprintln()\nfallthrough\ncase KindA:\nreturn\n}",
			want: "KindB default KindA",
		},
		{
			name: "a comment after a clause's last statement keeps the switch",
			body: "switch k {\ncase KindB:\nreturn // two\ncase KindA:\nreturn\n}",
			want: "KindB KindA",
		},
		{
			name: "a comment on a line of its own before a clause keeps the switch",
			body: "switch k {\ncase KindB:\nreturn\n// about a\ncase KindA:\nreturn\n}",
			want: "KindB KindA",
		},
		{
			name: "a constant declared in a function keeps the switch",
			body: "const local = 7\nswitch k {\ncase KindB:\nreturn\ncase local:\nreturn\ncase KindA:\nreturn\n}",
			want: "KindB local KindA",
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

// TestSwitchCaseOrderPackages covers the cases that name constants of other packages, and the values that must be distinct.
func TestSwitchCaseOrderPackages(t *testing.T) {
	const (
		tokenSwitch = "package p\n\nimport \"go/token\"\n\nfunc f(t token.Token) int {\n\tswitch t {\n\tcase token.VAR:\n\t\treturn 4\n\tcase token.CONST:\n\t\treturn 1\n\tcase token.TYPE:\n\t\treturn 3\n\tcase token.FUNC:\n\t\treturn 2\n\t}\n\treturn 0\n}\n"
		aliasSwitch = "package p\n\ntype K int\n\nconst (\n\tKA K = iota\n\tKZ = KA\n\tKB\n)\n\nfunc f(k K) int {\n\tswitch k {\n\tcase KZ:\n\t\treturn 9\n\tcase KA:\n\t\treturn 1\n\tcase KB:\n\t\treturn 2\n\t}\n\treturn 0\n}\n"
		mixedSwitch = "package p\n\nimport \"go/token\"\n\nconst KA = token.ILLEGAL\n\nfunc f(t token.Token) int {\n\tswitch t {\n\tcase token.VAR:\n\t\treturn 4\n\tcase KA:\n\t\treturn 1\n\t}\n\treturn 0\n}\n"
	)

	tests := []struct {
		name, src, want string
	}{
		{
			name: "constants of an imported package are ordered by their declaration",
			src: tokenSwitch,
			want: "token.CONST token.FUNC token.TYPE token.VAR",
		},
		{
			name: "a constant with a value from an imported package keeps the switch",
			src: "package p\n\nimport \"go/token\"\n\ntype K int\n\nconst (\n\tKA K = iota\n\tKB = token.VAR\n)\n\nfunc f(k K) int {\n\tswitch k {\n\tcase KB:\n\t\treturn 9\n\tcase KA:\n\t\treturn 1\n\t}\n\treturn 0\n}\n",
			want: "KB KA",
		},
		{
			name: "an alias of another constant keeps the switch",
			src: aliasSwitch,
			want: "KZ KA KB",
		},
		{
			name: "cases from two packages keep the switch",
			src: mixedSwitch,
			want: "token.VAR KA",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name := filepath.Join(t.TempDir(), "x.go")

			if got := strings.Join(caseOrder(t, name, tt.src), ";"); got != tt.want {
				t.Errorf("order = %q, want %q", got, tt.want)
			}
		})
	}
}

// typeCases returns, for each type switch of src after switchCaseOrder, its clauses' types in order ("default" for the default clause).
func typeCases(t *testing.T, src string) (out []string) {
	t.Helper()

	fset := token.NewFileSet()

	file, err := parser.ParseFile(fset, "x.go", src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	switchCaseOrder("x.go", file)
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

	return
}

func TestTypeSwitchCaseOrder(t *testing.T) {
	tests := []struct {
		name, src, want string
	}{
		{
			name: "concrete types are sorted, the default goes last",
			src: "package p\n\nfunc f(v any) {\n\tswitch v.(type) {\n\tcase string:\n\t\treturn\n\tcase int:\n\t\treturn\n\tdefault:\n\t\treturn\n\tcase bool:\n\t\treturn\n\t}\n}\n",
			want: "bool int string default",
		},
		{
			name: "an interface type keeps the switch",
			src: "package p\n\ntype I interface{ M() }\n\nfunc f(v any) {\n\tswitch v.(type) {\n\tcase string:\n\t\treturn\n\tcase I:\n\t\treturn\n\t}\n}\n",
			want: "string I",
		},
		{
			name: "a clause that does not return is sorted too",
			src: "package p\n\nfunc f(v any) {\n\tswitch v.(type) {\n\tcase string:\n\t\tprintln()\n\tcase int:\n\t\treturn\n\t}\n}\n",
			want: "int string",
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
