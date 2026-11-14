package main

import (
	"bytes"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"forkmat/go/printer"
)

// flattenText returns src with its final branches moved out, printed with
// white space collapsed, so that layout does not matter.
func flattenText(t *testing.T, src string) string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "x.go", src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	flatten(file, file.Comments)
	var buf bytes.Buffer
	cfg := printer.Config{Mode: printer.UseSpaces | printer.TabIndent, Tabwidth: 8}
	if err := cfg.Fprint(&buf, fset, file); err != nil {
		t.Fatalf("print: %v", err)
	}
	return strings.Join(strings.Fields(buf.String()), " ")
}

func TestFlatten(t *testing.T) {
	tests := []struct{ name, src, want string }{
		{
			name: "if else moves its else out",
			src:  "package p\n\nfunc f(x bool) int {\n\tif x {\n\t\ty()\n\t\treturn 1\n\t} else {\n\t\treturn 2\n\t}\n}\n",
			want: "package p func f(x bool) int { if x { y() return 1 } return 2 }",
		},
		{
			name: "switch default moves out",
			src:  "package p\n\nfunc f(x int) int {\n\tswitch {\n\tcase x > 0:\n\t\ty()\n\t\treturn 1\n\tdefault:\n\t\treturn 2\n\t}\n}\n",
			want: "package p func f(x int) int { switch { case x > 0: y() return 1 } return 2 }",
		},
		{
			name: "else if chain moves its final else out",
			src:  "package p\n\nfunc f(x int) int {\n\tif x > 0 {\n\t\treturn 1\n\t} else if x < 0 {\n\t\treturn 2\n\t} else {\n\t\treturn 3\n\t}\n}\n",
			want: "package p func f(x int) int { if x > 0 { return 1 } else if x < 0 { return 2 } return 3 }",
		},
		{
			name: "a branch that does not return keeps the statement",
			src:  "package p\n\nfunc f(x bool) int {\n\tif x {\n\t\ty()\n\t} else {\n\t\treturn 2\n\t}\n\treturn 0\n}\n",
			want: "package p func f(x bool) int { if x { y() } else { return 2 } return 0 }",
		},
		{
			name: "a name declared in the header is kept in place",
			src:  "package p\n\nfunc f(x bool) int {\n\tif y := x; y {\n\t\treturn 1\n\t} else {\n\t\treturn g(y)\n\t}\n}\n",
			want: "package p func f(x bool) int { if y := x; y { return 1 } else { return g(y) } }",
		},
		{
			name: "a break in a switch default is kept in place",
			src:  "package p\n\nfunc f(x int) int {\n\tfor {\n\t\tswitch x {\n\t\tcase 1:\n\t\t\treturn 1\n\t\tdefault:\n\t\t\tbreak\n\t\t}\n\t}\n}\n",
			want: "package p func f(x int) int { for { switch x { case 1: return 1 default: break } } }",
		},
		{
			name: "a case body is flattened too",
			src:  "package p\n\nfunc f(x, y bool) int {\n\tswitch {\n\tcase x:\n\t\tif y {\n\t\t\treturn 1\n\t\t} else {\n\t\t\treturn 2\n\t\t}\n\t}\n\treturn 0\n}\n",
			want: "package p func f(x, y bool) int { switch { case x: if y { return 1 } return 2 } return 0 }",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := flattenText(t, tt.src); got != tt.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}
