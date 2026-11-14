package main

import (
	"bytes"
	"forkmat/go/printer"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// unconvertText returns src with its redundant conversions removed, printed without comments changing layout.
func unconvertText(t *testing.T, src string) string {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "x.go", src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	var buf bytes.Buffer

	cfg := printer.Config{
		Mode: printer.UseSpaces | printer.TabIndent,
		Tabwidth: 8,
	}

	if err := cfg.Fprint(&buf, fset, unconvert(fset, file, checkTypes(fset, file))); err != nil {
		t.Fatalf("print: %v", err)
	}

	return buf.String()
}

func TestUnconvert(t *testing.T) {
	tests := []struct {
		name, src, want string
	}{
		{
			name: "string of string",
			src: "package p\n\nfunc f(s string) string { return string(string(s)) }\n",
			want: "package p\n\nfunc f(s string) string { return s }\n",
		},
		{
			name: "named type to itself",
			src: "package p\n\ntype M int\n\nfunc f(m M) M { return M(m) }\n",
			want: "package p\n\ntype M int\n\nfunc f(m M) M { return m }\n",
		},
		{
			name: "conversion to a different type is kept",
			src: "package p\n\nfunc f(n int) int64 { return int64(n) }\n",
			want: "package p\n\nfunc f(n int) int64 { return int64(n) }\n",
		},
		{
			name: "constant is kept",
			src: "package p\n\nvar x = int64(5)\n",
			want: "package p\n\nvar x = int64(5)\n",
		},
		{
			name: "conversion between slice types is kept",
			src: "package p\n\nfunc f(b []byte) string { return string(b) }\n",
			want: "package p\n\nfunc f(b []byte) string { return string(b) }\n",
		},
		{
			name: "conversion naming a package is kept",
			src: "package p\n\nimport \"time\"\n\nfunc f(d time.Duration) time.Duration { return time.Duration(d) }\n",
			want: "package p\n\nimport \"time\"\n\nfunc f(d time.Duration) time.Duration { return time.Duration(d) }\n",
		},
		{
			name: "nested redundant conversions are removed",
			src: "package p\n\nfunc f(b []byte) []byte { return []byte(([]byte)(b)) }\n",
			want: "package p\n\nfunc f(b []byte) []byte { return b }\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// the printer lays out function bodies over several lines, so the comparison is of the tokens, with white space collapsed
			got := strings.Join(strings.Fields(unconvertText(t, tt.src)), " ")
			if want := strings.Join(strings.Fields(tt.want), " "); got != want {
				t.Errorf("got:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}
