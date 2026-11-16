package main

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/modernize"
)

// fixIn returns src with the fixes of suite applied, as a file of a module whose go.mod names goVersion, or as a file outside any module when goVersion is "".
func fixIn(t *testing.T, suite []*analysis.Analyzer, goVersion, src string) string {
	t.Helper()

	dir := t.TempDir()
	if goVersion != "" {
		gomod := "module example.com/m\n\ngo " + goVersion + "\n"
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(gomod), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	return string(modernise(filepath.Join(dir, "x.go"), []byte(src), suite))
}

func TestModernise(t *testing.T) {
	tests := []struct {
		name, goVersion, src, want string
	}{
		{
			name: "interface{} becomes any",
			goVersion: "1.27",
			src: "package p\n\nvar x interface{}\n",
			want: "package p\n\nvar x any\n",
		},
		{
			name: "counting loop becomes range over int",
			goVersion: "1.27",
			src: "package p\n\nfunc f(n int) {\n\tfor i := 0; i < n; i++ {\n\t\t_ = i\n\t}\n}\n",
			want: "package p\n\nfunc f(n int) {\n\tfor i := range n {\n\t\t_ = i\n\t}\n}\n",
		},
		{
			name: "search loop becomes slices.Contains with its import",
			goVersion: "1.27",
			src: "package p\n\nfunc f(xs []string, s string) bool {\n\tfor _, x := range xs {\n\t\tif x == s {\n\t\t\treturn true\n\t\t}\n\t}\n\treturn false\n}\n",
			want: "package p\n\nimport \"slices\"\n\nfunc f(xs []string, s string) bool {\n\treturn slices.Contains(xs, s)\n}\n",
		},
		{
			name: "fixes wait for the module's Go version",
			goVersion: "1.17",
			src: "package p\n\nvar x interface{}\n",
			want: "package p\n\nvar x interface{}\n",
		},
		{
			name: "a file outside any module is left alone",
			src: "package p\n\nvar x interface{}\n",
			want: "package p\n\nvar x interface{}\n",
		},
		{
			name: "modern code is unchanged",
			goVersion: "1.27",
			src: "package p\n\nvar x any\n",
			want: "package p\n\nvar x any\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fixIn(t, modernize.Suite, tt.goVersion, tt.src); got != tt.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}

func TestGoDirective(t *testing.T) {
	tests := []struct {
		name, gomod, want string
	}{
		{"patch version", "module m\n\ngo 1.27.2\n", "go1.27"},
		{"minor version", "module m\n\ngo 1.21\n", "go1.21"},
		{"no go directive", "module m\n", ""},
		{"invalid version", "module m\n\ngo 1\n", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := goDirective([]byte(tt.gomod)); got != tt.want {
				t.Errorf("goDirective = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestApplyFixEdits(t *testing.T) {
	tests := []struct {
		name  string
		edits []textEdit
		want  string
	}{
		{
			name: "insertions at one place keep their order",
			edits: []textEdit{{start: 2, end: 2, text: []byte("X")}, {start: 2, end: 2, text: []byte("Y")}},
			want: "abXYcdef",
		},
		{
			name: "a deletion and an insertion at its start",
			edits: []textEdit{{start: 2, end: 4}, {start: 2, end: 2, text: []byte("X")}},
			want: "abXef",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(applyFixEdits([]byte("abcdef"), tt.edits)); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestVetFix(t *testing.T) {
	const selfAssign = "package p\n\nfunc f(x int) int {\n\tx = x\n\treturn x\n}\n"

	tests := []struct {
		name, goVersion, src, want string
	}{
		{
			name: "self-assignment is removed",
			goVersion: "1.27",
			src: selfAssign,
			want: "package p\n\nfunc f(x int) int {\n\treturn x\n}\n",
		},
		{
			name: "time layout is corrected",
			goVersion: "1.27",
			src: "package p\n\nimport \"time\"\n\nfunc f() string {\n\treturn time.Now().Format(\"2006-02-01\")\n}\n",
			want: "package p\n\nimport \"time\"\n\nfunc f() string {\n\treturn time.Now().Format(\"2006-01-02\")\n}\n",
		},
		{
			name: "host and port are joined by net.JoinHostPort",
			goVersion: "1.27",
			src: "package p\n\nimport (\n\t\"fmt\"\n\t\"net\"\n)\n\nfunc f(host string, port int) (net.Conn, error) {\n\treturn net.Dial(\"tcp\", fmt.Sprintf(\"%s:%d\", host, port))\n}\n",
			want: "package p\n\nimport (\n\t\"fmt\"\n\t\"net\"\n)\n\nfunc f(host string, port int) (net.Conn, error) {\n\treturn net.Dial(\"tcp\", net.JoinHostPort(host, fmt.Sprintf(\"%d\", port)))\n}\n",
		},
		{
			name: "code after a return is removed",
			goVersion: "1.27",
			src: "package p\n\nfunc g() int {\n\treturn 1\n\tprintln(\"dead\")\n}\n",
			want: "package p\n\nfunc g() int {\n\treturn 1\n}\n",
		},
		{
			name: "a file outside any module is left alone",
			src: selfAssign,
			want: selfAssign,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fixIn(t, vetFixSuite, tt.goVersion, tt.src); got != tt.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}
