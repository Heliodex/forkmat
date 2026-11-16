package main

import (
	"bytes"
	"forkmat/go/printer"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// testNamesText returns src with its test parameters renamed, printed with white space collapsed, so that layout does not matter.
func testNamesText(t *testing.T, src string) string {
	t.Helper()

	fset := token.NewFileSet()

	file, err := parser.ParseFile(fset, "x.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	testNames(fset, file)

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

func TestTestNames(t *testing.T) {
	tests := []struct {
		name, src, want string
	}{
		{
			name: "a testing.TB parameter becomes tb",
			src: "package p\n\nimport \"testing\"\n\nfunc h(x testing.TB) {\n\tx.Helper()\n}\n",
			want: "package p import \"testing\" func h(tb testing.TB) { tb.Helper() }",
		},
		{
			name: "a *testing.T parameter becomes t",
			src: "package p\n\nimport \"testing\"\n\nfunc h(tt *testing.T) {\n\ttt.Helper()\n}\n",
			want: "package p import \"testing\" func h(t *testing.T) { t.Helper() }",
		},
		{
			name: "a *testing.B parameter becomes b",
			src: "package p\n\nimport \"testing\"\n\nfunc h(bench *testing.B) {\n\tbench.ResetTimer()\n}\n",
			want: "package p import \"testing\" func h(b *testing.B) { b.ResetTimer() }",
		},
		{
			name: "a parameter already named right is kept",
			src: "package p\n\nimport \"testing\"\n\nfunc h(t *testing.T, tb testing.TB) {\n\tt.Log(tb)\n}\n",
			want: "package p import \"testing\" func h(t *testing.T, tb testing.TB) { t.Log(tb) }",
		},
		{
			name: "a name already used in the function is a conflict",
			src: "package p\n\nimport \"testing\"\n\nfunc h(tt *testing.T) {\n\tt := 1\n\t_ = t\n\ttt.Log()\n}\n",
			want: "package p import \"testing\" func h(tt *testing.T) { t := 1 _ = t tt.Log() }",
		},
		{
			name: "a nested closure that uses the new name is a conflict",
			src: "package p\n\nimport \"testing\"\n\nfunc h(tt *testing.T) {\n\tf := func(t int) int { return t }\n\ttt.Log(f(1))\n}\n",
			want: "package p import \"testing\" func h(tt *testing.T) { f := func(t int) int { return t } tt.Log(f(1)) }",
		},
		{
			name: "a field with the old name is not renamed",
			src: "package p\n\nimport \"testing\"\n\ntype s struct{ tt int }\n\nfunc h(tt *testing.T, v s) {\n\tt := v.tt\n\t_ = t\n\ttt.Log()\n}\n",
			want: "package p import \"testing\" type s struct{ tt int } func h(tt *testing.T, v s) { t := v.tt _ = t tt.Log() }",
		},
		{
			name: "a function literal's parameter is renamed",
			src: "package p\n\nimport \"testing\"\n\nvar g = func(tt *testing.T) {\n\ttt.Helper()\n}\n",
			want: "package p import \"testing\" var g = func(t *testing.T) { t.Helper() }",
		},
		{
			name: "a parameter of another type is kept",
			src: "package p\n\nfunc h(x int) int {\n\treturn x\n}\n",
			want: "package p func h(x int) int { return x }",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := testNamesText(t, tt.src); got != tt.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}
