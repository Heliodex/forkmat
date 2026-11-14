package main

import (
	"bytes"
	"forkmat/go/printer"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// mergeText returns src with its adjacent cases merged, printed with white space
// collapsed.
func mergeText(t *testing.T, src string) string {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "x.go", src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	mergeCases(fset, file)

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

func TestMergeCases(t *testing.T) {
	tests := []struct {
		name, src, want string
	}{
		{
			name: "adjacent cases with the same body merge",
			src: "package p\n\nfunc f(x int) {\n\tswitch x {\n\tcase 1:\n\t\tg()\n\tcase 2:\n\t\tg()\n\tcase 3:\n\t\th()\n\t}\n}\n",
			want: "package p func f(x int) { switch x { case 1, 2: g() case 3: h() } }",
		},
		{
			name: "cases that are not adjacent are not merged",
			src: "package p\n\nfunc f(x int) {\n\tswitch x {\n\tcase 1:\n\t\tg()\n\tcase 2:\n\t\th()\n\tcase 3:\n\t\tg()\n\t}\n}\n",
			want: "package p func f(x int) { switch x { case 1: g() case 2: h() case 3: g() } }",
		},
		{
			name: "a default clause is not merged",
			src: "package p\n\nfunc f(x int) {\n\tswitch x {\n\tcase 1:\n\t\tg()\n\tdefault:\n\t\tg()\n\t}\n}\n",
			want: "package p func f(x int) { switch x { case 1: g() default: g() } }",
		},
		{
			name: "a fallthrough body is kept apart",
			src: "package p\n\nfunc f(x int) {\n\tswitch x {\n\tcase 1:\n\t\tg()\n\t\tfallthrough\n\tcase 2:\n\t\tg()\n\t\tfallthrough\n\tcase 3:\n\t}\n}\n",
			want: "package p func f(x int) { switch x { case 1: g() fallthrough case 2: g() fallthrough case 3: } }",
		},
		{
			name: "type switch clauses merge when the body does not use the variable",
			src: "package p\n\nfunc f(x any) {\n\tswitch v := x.(type) {\n\tcase int:\n\t\tg()\n\tcase string:\n\t\tg()\n\t}\n\t_ = x\n}\n",
			want: "package p func f(x any) { switch v := x.(type) { case int, string: g() } _ = x }",
		},
		{
			name: "type switch clauses are kept apart when the body uses the variable",
			src: "package p\n\nfunc f(x any) {\n\tswitch v := x.(type) {\n\tcase int:\n\t\tg(v)\n\tcase string:\n\t\tg(v)\n\t}\n}\n",
			want: "package p func f(x any) { switch v := x.(type) { case int: g(v) case string: g(v) } }",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mergeText(t, tt.src); got != tt.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}
