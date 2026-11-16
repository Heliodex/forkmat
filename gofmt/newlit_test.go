package main

import (
	"bytes"
	"forkmat/go/printer"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"
)

// newText returns src after newLiterals, as it prints.
func newText(t *testing.T, src string) string {
	t.Helper()

	fset := token.NewFileSet()

	file, err := parser.ParseFile(fset, "x.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	newLiterals(file, func() *types.Info {
		return checkTypes(fset, file)
	})

	var buf bytes.Buffer

	if err := printer.Fprint(&buf, fset, file); err != nil {
		t.Fatalf("print: %v", err)
	}

	return buf.String()
}

func TestNewLiterals(t *testing.T) {
	tests := []struct {
		name, src string
		// want is text the output must contain, and unwant text it must not
		want, unwant string
	}{
		{
			name: "a struct type of the file",
			src: "package p\n\ntype T struct{ A int }\n\nfunc f() *T {\n\treturn new(T)\n}\n",
			want: "return &T{}",
			unwant: "new(T)",
		},
		{
			name: "a generic struct type",
			src: "package p\n\ntype Value[T any] struct{ v T }\n\nfunc f[T any]() *Value[T] {\n\tv := new(Value[T])\n\treturn v\n}\n",
			want: "v := &Value[T]{}",
			unwant: "new(Value[T])",
		},
		{
			name: "a non-struct type keeps new",
			src: "package p\n\ntype N int\n\nfunc f() (*int, *N) {\n\treturn new(int), new(N)\n}\n",
			want: "new(int), new(N)",
			unwant: "&",
		},
		{
			name: "a shadowed new keeps its call",
			src: "package p\n\ntype T struct{ A int }\n\nfunc new(t T) int { return t.A }\n\nfunc f() int {\n\treturn new(T)\n}\n",
			want: "return new(T)",
			unwant: "&T{}",
		},
		{
			name: "a call in an if header keeps new",
			src: "package p\n\ntype T struct{ A int }\n\nfunc f() bool {\n\tif p := new(T); p != nil {\n\t\treturn true\n\t}\n\treturn false\n}\n",
			want: "if p := new(T); p != nil {",
			unwant: "&T{}",
		},
		{
			name: "a call in a for header keeps new",
			src: "package p\n\ntype T struct{ A int }\n\nfunc f() {\n\tfor p := new(T); p.A < 3; {\n\t\tp.A++\n\t}\n}\n",
			want: "for p := new(T); p.A < 3; {",
			unwant: "&T{}",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := newText(t, tt.src)
			if !strings.Contains(got, tt.want) {
				t.Errorf("output lacks %q:\n%s", tt.want, got)
			}

			if tt.unwant != "" && strings.Contains(got, tt.unwant) {
				t.Errorf("output holds %q:\n%s", tt.unwant, got)
			}

			if _, err := parser.ParseFile(token.NewFileSet(), "x.go", got, 0); err != nil {
				t.Errorf("output does not parse: %v\n%s", err, got)
			}
		})
	}
}
