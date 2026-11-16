package main

import (
	"bytes"
	"forkmat/go/printer"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// namedResultsText returns src with its returned variables made named results, printed with white space collapsed, so that layout does not matter.
func namedResultsText(t *testing.T, src string) string {
	t.Helper()

	fset := token.NewFileSet()

	file, err := parser.ParseFile(fset, "x.go", src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	namedResults(fset, file)

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

func TestNamedResults(t *testing.T) {
	tests := []struct {
		name, src, want string
	}{
		{
			name: "a zero var that is returned becomes the result",
			src: "package p\n\nfunc f(body string) map[string]any {\n\tvar doc map[string]any\n\tif err := g(body, &doc); err != nil {\n\t\tpanic(err)\n\t}\n\treturn doc\n}\n",
			want: "package p func f(body string) (doc map[string]any) { if err := g(body, &doc); err != nil { panic(err) } return }",
		},
		{
			name: "several vars become several results in return order",
			src: "package p\n\nfunc f() (int, string) {\n\tvar b string\n\tvar a int\n\ta = 1\n\tb = \"x\"\n\treturn a, b\n}\n",
			want: "package p func f() (a int, b string) { a = 1 b = \"x\" return }",
		},
		{
			name: "a var with a value is left alone",
			src: "package p\n\nfunc f() int {\n\tvar x = 1\n\treturn x\n}\n",
			want: "package p func f() int { var x = 1 return x }",
		},
		{
			name: "a result that is not returned from a variable is left alone",
			src: "package p\n\nfunc f() (int, error) {\n\tvar x int\n\treturn x, nil\n}\n",
			want: "package p func f() (int, error) { var x int return x, nil }",
		},
		{
			name: "a type that differs from the result is left alone",
			src: "package p\n\nfunc f() any {\n\tvar x int\n\treturn x\n}\n",
			want: "package p func f() any { var x int return x }",
		},
		{
			name: "a function with more than one return is left alone",
			src: "package p\n\nfunc f(b bool) int {\n\tvar x int\n\tif b {\n\t\treturn 1\n\t}\n\treturn x\n}\n",
			want: "package p func f(b bool) int { var x int if b { return 1 } return x }",
		},
		{
			name: "a defer in the function is left alone",
			src: "package p\n\nfunc f() int {\n\tvar x int\n\tdefer g()\n\treturn x\n}\n",
			want: "package p func f() int { var x int defer g() return x }",
		},
		{
			name: "a name used before its declaration is left alone",
			src: "package p\n\nfunc f() int {\n\tx++\n\tvar x int\n\treturn x\n}\n",
			want: "package p func f() int { x++ var x int return x }",
		},
		{
			name: "a name declared again with := is left alone",
			src: "package p\n\nfunc f() int {\n\tvar x int\n\tif y := 1; y > 0 {\n\t\tx, z := y, 2\n\t\t_ = z\n\t}\n\treturn x\n}\n",
			want: "package p func f() int { var x int if y := 1; y > 0 { x, z := y, 2 _ = z } return x }",
		},
		{
			name: "a declaration with a comment is left alone",
			src: "package p\n\nfunc f() int {\n\tvar x int // the count\n\treturn x\n}\n",
			want: "package p func f() int { var x int // the count return x }",
		},
		{
			name: "a function literal is named on its own",
			src: "package p\n\nvar g = func() int {\n\tvar x int\n\treturn x\n}\n",
			want: "package p var g = func() (x int) { return }",
		},
		{
			name: "named results are left alone",
			src: "package p\n\nfunc f() (x int) {\n\treturn x\n}\n",
			want: "package p func f() (x int) { return x }",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := namedResultsText(t, tt.src); got != tt.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}
