package main

import (
	"bytes"
	"forkmat/go/printer"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// wslText returns src laid out under -strict and -wsl, and fails the test if the result is not parsable.
func wslText(t *testing.T, src string) string {
	t.Helper()

	return wslTextFile(t, "x.go", src)
}

// wslTextFile returns src, the file filename, laid out under -strict and -wsl, and fails the test if the result is not parsable.
func wslTextFile(t *testing.T, filename, src string) string {
	t.Helper()

	out, err := wslLayout(filename, func(blank map[int]bool) ([]byte, error) {
		fset := token.NewFileSet()

		file, err := parser.ParseFile(fset, filename, src, parser.ParseComments)
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

// TestWSLBlankBeforeIf checks that a blank line sets off an if statement that cuddles two assignments, rather than splitting the assignments, when the second one is used again after the if.
func TestWSLBlankBeforeIf(t *testing.T) {
	const (
		src string = "package p\n\nfunc f(p1, p2 int, dx float32) int {\n\ttotal := 0\n\tfor y := range 3 {\n\t\tex := int(float32(p1) + float32(y)*dx)\n\t\tsx := int(float32(p2) + float32(y)*dx)\n\t\tif sx > ex {\n\t\t\tsx, ex = ex, sx\n\t\t}\n\n\t\ttotal += sx\n\t}\n\treturn total\n}\n"
		want string = "\t\tex := int(float32(p1) + float32(y)*dx)\n\t\tsx := int(float32(p2) + float32(y)*dx)\n\n\t\tif sx > ex {"
	)

	if out := wslText(t, src); !strings.Contains(out, want) {
		t.Errorf("want the blank line above the if:\n%s\ngot:\n%s", want, out)
	}
}

// TestWSLTestFileHelpers checks that wsl reads the types of a test file with the other test files of its package, so an err check after a call to a helper that a sibling test file declares keeps its assignment.
func TestWSLTestFileHelpers(t *testing.T) {
	const (
		helpers string = "package p\n\ntype srv struct{}\n\nfunc (srv) call() ([]int, error) {\n\treturn nil, nil\n}\n"
		src string = "package p\n\nimport \"testing\"\n\nfunc TestCall(t *testing.T) {\n\ts := srv{}\n\tvals, err := s.call()\n\tif err != nil {\n\t\tt.Fatal(err)\n\t}\n\n\t_ = vals\n}\n"
		want string = "\tvals, err := s.call()\n\tif err != nil {"
	)

	dir := t.TempDir()
	name := filepath.Join(dir, "call_test.go")

	if err := os.WriteFile(filepath.Join(dir, "helpers_test.go"), []byte(helpers), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(name, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}

	out := wslTextFile(t, name, src)
	if !strings.Contains(out, want) {
		t.Errorf("want no blank line between the assignment of err and its check:\n%s\ngot:\n%s", want, out)
	}

	fset := token.NewFileSet()

	file, err := parser.ParseFile(fset, name, out, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if diags := wslDiagnostics(fset, name, file); len(diags) > 0 {
		t.Errorf("wsl reports %d issues in:\n%s", len(diags), out)
	}
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
			name: "an err check after two assignments, the last of which assigns err",
			src: "package p\n\ntype app struct {\n\tname string\n}\n\nfunc g(a *app) ([]int, []int, error) {\n\treturn nil, nil, nil\n}\n\nfunc use(a, b []int) error {\n\treturn nil\n}\n\nfunc f() error {\n\ta := &app{\n\t\tname: \"frontend\",\n\t}\n\targs, env, err := g(a)\n\tif err != nil {\n\t\treturn err\n\t}\n\n\treturn use(args, env)\n}\n",
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
