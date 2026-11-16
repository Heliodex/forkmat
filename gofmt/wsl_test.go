package main

import (
	"bytes"
	"forkmat/go/printer"
	"go/parser"
	"go/token"
	"testing"
)

// wslText returns src laid out under -strict and -wsl, and fails the test if the result is not parsable.
func wslText(t *testing.T, src string) string {
	t.Helper()

	out, err := wslLayout("x.go", func(blank map[int]bool) ([]byte, error) {
		fset := token.NewFileSet()

		file, err := parser.ParseFile(fset, "x.go", src, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}

		var buf bytes.Buffer

		cfg := printer.Config{
			Mode: printerMode | printer.StrictLayout,
			Tabwidth: tabWidth,
			StmtBlank: blank,
		}

		if err := cfg.Fprint(&buf, fset, file); err != nil {
			return nil, err
		}

		return buf.Bytes(), nil
	})
	if err != nil {
		t.Fatalf("layout: %v", err)
	}

	return string(out)
}

func TestWSLLayout(t *testing.T) {
	tests := []struct {
		name, src string
	}{
		{
			name: "a block is set off from the statement before it",
			src: "package p\n\nfunc f(x int) int {\n\ty := x + 1\n\tif y > 0 {\n\t\treturn y\n\t}\n\treturn x\n}\n",
		},
		{
			name: "an err check stays with the assignment of err",
			src: "package p\n\nfunc f() error {\n\tx, err := g()\n\n\tif err != nil {\n\t\treturn err\n\t}\n\n\t_ = x\n\treturn nil\n}\n",
		},
		{
			name: "a statement after a trailing comment",
			src: "package p\n\nfunc f() {\n\tconst c = 3 // the count\n\tif c > 0 {\n\t\tg()\n\t}\n}\n",
		},
		{
			name: "a grouped var after a call",
			src: "package p\n\nfunc f(t T) {\n\tg(t)\n\tvar (\n\t\ta int\n\t\tb int // trailing\n\t)\n\th(a, b)\n}\n",
		},
		{
			name: "a comment before the first statement of a block",
			src: "package p\n\nfunc f(d int) {\n\tif d == 1 {\n\t\t// a leading comment\n\n\t\tif prev := d; prev != 0 {\n\t\t\tg()\n\t\t}\n\t}\n}\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := wslText(t, tt.src)
			fset := token.NewFileSet()

			file, err := parser.ParseFile(fset, "x.go", out, parser.ParseComments)
			if err != nil {
				t.Fatalf("parse: %v\n%s", err, out)
			}

			if diags := wslDiagnostics(fset, "x.go", file); len(diags) > 0 {
				t.Errorf("wsl reports %d issues in:\n%s", len(diags), out)
			}
		})
	}
}
