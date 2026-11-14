package main

import (
	"bytes"
	"forkmat/go/printer"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// emptyText returns src with its empty slice declarations rewritten, printed with
// white space collapsed.
func emptyText(t *testing.T, src string) string {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "x.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	emptySlices(file)

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

func TestEmptySlices(t *testing.T) {
	tests := []struct {
		name, body, want string
	}{
		{
			name: "only appended to and ranged over",
			body: "x := []int{}\n\tfor _, v := range xs {\n\t\tx = append(x, v)\n\t}\n\t_ = len(x)",
			want: "var x []int for _, v := range xs { x = append(x, v) } _ = len(x)",
		},
		{
			name: "returned keeps the declaration",
			body: "x := []int{}\n\treturn x",
			want: "x := []int{} return x",
		},
		{
			name: "a comparison with nil keeps the declaration",
			body: "x := []int{}\n\t_ = x == nil",
			want: "x := []int{} _ = x == nil",
		},
		{
			name: "a slice expression keeps the declaration",
			body: "x := []int{}\n\t_ = x[:0]",
			want: "x := []int{} _ = x[:0]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := "package p\n\nfunc f(xs []int) {\n\t" + tt.body + "\n}\n"

			if got := emptyText(t, src); !strings.Contains(got, tt.want) {
				t.Errorf("got:\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}
