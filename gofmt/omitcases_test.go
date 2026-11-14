package main

import (
	"bytes"
	"forkmat/go/printer"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// omitText returns src with its redundant cases removed, printed with white
// space collapsed.
func omitText(t *testing.T, src string) string {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "x.go", src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	omitCases(fset, file, file.Comments)

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

func TestOmitCases(t *testing.T) {
	tests := []struct {
		name, src, want string
	}{
		{
			name: "a last case returning the code after the switch is omitted",
			src: "package p\n\nfunc f(x int) int {\n\tswitch x {\n\tcase 1:\n\t\treturn 5\n\t}\n\treturn 5\n}\n",
			want: "package p func f(x int) int { switch x { } return 5 }",
		},
		{
			name: "a case whose body differs is kept",
			src: "package p\n\nfunc f(x int) int {\n\tswitch x {\n\tcase 1:\n\t\treturn 5\n\t}\n\treturn 6\n}\n",
			want: "package p func f(x int) int { switch x { case 1: return 5 } return 6 }",
		},
		{
			name: "a case that is not last is kept when another clause could take over",
			src: "package p\n\nfunc f(x int) int {\n\tswitch x {\n\tcase 1:\n\t\treturn 5\n\tcase 2:\n\t\treturn 6\n\t}\n\treturn 5\n}\n",
			want: "package p func f(x int) int { switch x { case 1: return 5 case 2: return 6 } return 5 }",
		},
		{
			name: "a case is omitted when every other clause reaches the same code",
			src: "package p\n\nfunc f(x int) int {\n\tswitch x {\n\tcase 1:\n\t\treturn 5\n\tcase 2:\n\t\treturn 5\n\t}\n\treturn 5\n}\n",
			want: "package p func f(x int) int { switch x { } return 5 }",
		},
		{
			name: "a case is kept when the switch has a default that differs",
			src: "package p\n\nfunc f(x int) int {\n\tswitch x {\n\tcase 1:\n\t\treturn 5\n\tdefault:\n\t\tg()\n\t}\n\treturn 5\n}\n",
			want: "package p func f(x int) int { switch x { case 1: return 5 default: g() } return 5 }",
		},
		{
			name: "a case with a call in its expression is kept",
			src: "package p\n\nfunc f(x int) int {\n\tswitch x {\n\tcase g():\n\t\treturn 5\n\t}\n\treturn 5\n}\n",
			want: "package p func f(x int) int { switch x { case g(): return 5 } return 5 }",
		},
		{
			name: "a body that declares a name is kept",
			src: "package p\n\nfunc f(x int) int {\n\tswitch x {\n\tcase 1:\n\t\ty := 5\n\t\treturn y\n\t}\n\ty := 5\n\treturn y\n}\n",
			want: "package p func f(x int) int { switch x { case 1: y := 5 return y } y := 5 return y }",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := omitText(t, tt.src); got != tt.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}
