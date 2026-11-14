package main

import (
	"bytes"
	"forkmat/go/printer"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// rangeText returns src with its index loops rewritten, printed with white space
// collapsed.
func rangeText(t *testing.T, src string) string {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "x.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rangeLoops(file, file.Comments, checkTypes(fset, file))

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

func TestRangeLoops(t *testing.T) {
	tests := []struct {
		name, loop, want string
	}{
		{
			name: "an int bound",
			loop: "for i := 0; i < n; i++ { use(i) }",
			want: "for i := range n { use(i) }",
		},
		{
			name: "len of a slice is the slice",
			loop: "for i := 0; i < len(s); i++ { use(i) }",
			want: "for i := range s { use(i) }",
		},
		{
			name: "len of a string is kept",
			loop: "for i := 0; i < len(t); i++ { use(i) }",
			want: "for i := range len(t) { use(i) }",
		},
		{
			name: "an unused index is dropped",
			loop: "for i := 0; i < n; i++ { use() }",
			want: "for range n { use() }",
		},
		{
			name: "an index changed in the body keeps the loop",
			loop: "for i := 0; i < n; i++ { i++ }",
			want: "for i := 0; i < n; i++ { i++ }",
		},
		{
			name: "a bound changed in the body keeps the loop",
			loop: "for i := 0; i < n; i++ { n-- }",
			want: "for i := 0; i < n; i++ { n-- }",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := "package p\n\nfunc f(n int, s []int, t string) {\n\t" + tt.loop + "\n}\n"

			if got := rangeText(t, src); !strings.Contains(got, strings.Join(strings.Fields(tt.want), " ")) {
				t.Errorf("got:\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}
