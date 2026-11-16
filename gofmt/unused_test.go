package main

import (
	"bytes"
	"forkmat/go/printer"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// unusedText returns src with its unused local variables removed, printed with white space collapsed.
func unusedText(t *testing.T, src string) string {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "x.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	unusedVars(fset, file, file.Comments)

	var buf bytes.Buffer

	cfg := printer.Config{
		Mode: printer.UseSpaces | printer.TabIndent,
		Tabwidth: 8,
	}

	if err := cfg.Fprint(&buf, fset, file); err != nil {
		t.Fatalf("print: %v", err)
	}

	return strings.Join(strings.Fields(buf.String()), " ")
}

func TestUnused(t *testing.T) {
	tests := []struct {
		name, src, want string
	}{
		{
			name: "a pure declaration that is never referenced is removed",
			src: "package p\n\nfunc f() {\n\tx := 5\n}\n",
			want: "package p func f() { }",
		},
		{
			name: "a declaration that is referenced is kept",
			src: "package p\n\nfunc f() {\n\tx := 5\n\t_ = x\n}\n",
			want: "package p func f() { x := 5 _ = x }",
		},
		{
			name: "a call that is never referenced is kept as an assignment to the blank identifier",
			src: "package p\n\nfunc f() {\n\tx := g()\n}\n",
			want: "package p func f() { _ = g() }",
		},
		{
			name: "a receive that is never referenced is kept as an assignment",
			src: "package p\n\nfunc f(ch chan int) {\n\tx := <-ch\n}\n",
			want: "package p func f(ch chan int) { _ = <-ch }",
		},
		{
			name: "a value that may panic becomes an assignment to the blank identifier",
			src: "package p\n\nfunc f(m map[int]int) {\n\tx := m[1]\n}\n",
			want: "package p func f(m map[int]int) { _ = m[1] }",
		},
		{
			name: "a division that may panic is kept",
			src: "package p\n\nfunc f(a, b int) {\n\tx := a / b\n}\n",
			want: "package p func f(a, b int) { _ = a / b }",
		},
		{
			name: "a conversion to a predeclared type is pure",
			src: "package p\n\nfunc f() {\n\tx := int64(5)\n}\n",
			want: "package p func f() { }",
		},
		{
			name: "both names of a multi-value declaration unused are removed with a pure call",
			src: "package p\n\nfunc f() {\n\tx, y := g()\n}\n",
			want: "package p func f() { _, _ = g() }",
		},
		{
			name: "an unused name of a multi-value declaration becomes blank",
			src: "package p\n\nfunc f() {\n\tx, y := g()\n\t_ = y\n}\n",
			want: "package p func f() { _, y := g() _ = y }",
		},
		{
			name: "a := stays when another new name is used",
			src: "package p\n\nfunc f() {\n\tx, y := g()\n\t_ = y\n\t_ = x\n}\n",
			want: "package p func f() { x, y := g() _ = y _ = x }",
		},
		{
			name: "an unused name of a var spec becomes blank",
			src: "package p\n\nfunc f() {\n\tvar x, y = 1, 2\n\t_ = y\n}\n",
			want: "package p func f() { var _, y = 1, 2 _ = y }",
		},
		{
			name: "a redeclaration stays valid when the only new name is removed",
			src: "package p\n\nfunc f() {\n\tvar err error\n\tx, err := g()\n\t_ = err\n}\n",
			want: "package p func f() { var err error _, err = g() _ = err }",
		},
		{
			name: "a var declaration without a value is removed",
			src: "package p\n\nfunc f() {\n\tvar x int\n}\n",
			want: "package p func f() { }",
		},
		{
			name: "a var declaration with a pure value is removed",
			src: "package p\n\nfunc f() {\n\tvar x = 5\n}\n",
			want: "package p func f() { }",
		},
		{
			name: "a var declaration with an effectful value keeps it in place",
			src: "package p\n\nfunc f() {\n\tvar x = g()\n}\n",
			want: "package p func f() { var _ = g() }",
		},
		{
			name: "a var declaration whose type names a package keeps the package referenced",
			src: "package p\n\nimport \"time\"\n\nfunc f() {\n\tvar x time.Duration\n}\n",
			want: "package p import \"time\" func f() { var _ time.Duration }",
		},
		{
			name: "a literal of an imported type keeps the package referenced",
			src: "package p\n\nimport \"time\"\n\nfunc f() {\n\tx := time.Time{}\n}\n",
			want: "package p import \"time\" func f() { _ = time.Time{} }",
		},
		{
			name: "a comma-ok map read that may panic keeps both values",
			src: "package p\n\nfunc f(m map[int]int) {\n\tx, ok := m[1]\n}\n",
			want: "package p func f(m map[int]int) { _, _ = m[1] }",
		},
		{
			name: "a conversion to a type declared in the file is pure",
			src: "package p\n\ntype T int\n\nfunc f() {\n\tx := T(5)\n}\n",
			want: "package p type T int func f() { }",
		},
		{
			name: "the unused spec of a var group is removed",
			src: "package p\n\nfunc f() {\n\tvar (\n\t\tx = 5\n\t\ty = 6\n\t)\n\t_ = y\n}\n",
			want: "package p func f() { var ( y = 6 ) _ = y }",
		},
		{
			name: "a package-level var is kept",
			src: "package p\n\nvar x = 5\n",
			want: "package p var x = 5",
		},
		{
			name: "a parameter is kept",
			src: "package p\n\nfunc f(x int) {\n}\n",
			want: "package p func f(x int) { }",
		},
		{
			name: "an outer name used only by a shadowing declaration is removed",
			src: "package p\n\nfunc f() {\n\tx := 5\n\t{\n\t\tx := 6\n\t\t_ = x\n\t}\n}\n",
			want: "package p func f() { { x := 6 _ = x } }",
		},
		{
			name: "a name a closure refers to is kept",
			src: "package p\n\nfunc f() {\n\tx := 5\n\t_ = func() { _ = x }\n}\n",
			want: "package p func f() { x := 5 _ = func() { _ = x } }",
		},
		{
			name: "a name that is only assigned to is referenced, and kept",
			src: "package p\n\nfunc f() {\n\tx := 5\n\tx = 6\n}\n",
			want: "package p func f() { x := 5 x = 6 }",
		},
		{
			name: "a range variable is kept",
			src: "package p\n\nfunc f(s []int) {\n\tfor i := range s {\n\t}\n}\n",
			want: "package p func f(s []int) { for i := range s { } }",
		},
		{
			name: "a declaration with a comment is kept",
			src: "package p\n\nfunc f() {\n\tx := 5 // why\n}\n",
			want: "package p func f() { x := 5 // why }",
		},
		{
			name: "a blank declaration is left alone",
			src: "package p\n\nfunc f() {\n\tvar _ = 5\n}\n",
			want: "package p func f() { var _ = 5 }",
		},
		{
			name: "a var group with a doc comment is kept",
			src: "package p\n\nfunc f() {\n\t// why\n\tvar (\n\t\tx = 5\n\t)\n}\n",
			want: "package p func f() { // why var ( x = 5 ) }",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := unusedText(t, tt.src); got != tt.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}
