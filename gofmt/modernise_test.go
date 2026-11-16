package main

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/composite"
	"golang.org/x/tools/go/analysis/passes/inline"
	"golang.org/x/tools/go/analysis/passes/modernize"
)

func TestApplyFixEdits(t *testing.T) {
	tests := []struct {
		name string
		edits []textEdit
		want string
	}{
		{
			name: "insertions at one place keep their order",
			edits: []textEdit{
				{
					start: 2,
					end: 2,
					text: []byte("X"),
				},
				{
					start: 2,
					end: 2,
					text: []byte("Y"),
				},
			},
			want: "abXYcdef",
		},
		{
			name: "a deletion and an insertion at its start",
			edits: []textEdit{
				{
					start: 2,
					end: 4,
				},
				{
					start: 2,
					end: 2,
					text: []byte("X"),
				},
			},
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

// fixInPackage writes the files of one package, under the module of goVersion when it is not empty, and returns x.go with the fixes of suite applied. The fixes read x.go from its text here, and the other files from disk.
func fixInPackage(t *testing.T, suite []*analysis.Analyzer, goVersion string, files map[string]string) string {
	t.Helper()

	dir := t.TempDir()

	if goVersion != "" {
		gomod := "module example.com/m\n\ngo " + goVersion + "\n"

		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(gomod), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	for name, src := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	return string(modernise(filepath.Join(dir, "x.go"), []byte(files["x.go"]), suite))
}

func TestCrossFileTypes(t *testing.T) {
	const (
		// Component is an alias for Owner, and Source is a struct, both declared in b.go. Their types are different, so the comparison must stay a loop: slices.Contains over []Component with a Source does not compile.
		a = "package p\n\ntype Component = Owner\n\nfunc Add(comps []Component, src Source) bool {\n\tfor _, c := range comps {\n\t\tif c == src {\n\t\t\treturn true\n\t\t}\n\t}\n\n\treturn false\n}\n"
		b = "package p\n\ntype Owner interface {\n\tOwns() bool\n}\n\ntype Source struct {\n\tId uint32\n}\n\nfunc (Source) Owns() bool { return true }\n"

		// Here both types are declared in b.go, so the same types are compared, and the loop becomes slices.Contains.
		same = "package p\n\nfunc Has(xs []Source, s Source) bool {\n\tfor _, x := range xs {\n\t\tif x == s {\n\t\t\treturn true\n\t\t}\n\t}\n\n\treturn false\n}\n"
		sameWant = "package p\n\nimport \"slices\"\n\nfunc Has(xs []Source, s Source) bool {\n\treturn slices.Contains(xs, s)\n}\n"
	)

	t.Run("types from another file keep the loop", func(t *testing.T) {
		if got := fixInPackage(t, modernize.Suite, "1.27", map[string]string{"b.go": b, "x.go": a}); got != a {
			t.Errorf("got:\n%s\nwant:\n%s", got, a)
		}
	})
	t.Run("types from another file still allow the rewrite", func(t *testing.T) {
		if got := fixInPackage(t, modernize.Suite, "1.27", map[string]string{"b.go": b, "x.go": same}); got != sameWant {
			t.Errorf("got:\n%s\nwant:\n%s", got, sameWant)
		}
	})
	t.Run("a sibling that does not parse disables the fixes", func(t *testing.T) {
		if got := fixInPackage(t, modernize.Suite, "1.27", map[string]string{"b.go": "package p\n\nfunc {", "x.go": same}); got != same {
			t.Errorf("got:\n%s\nwant:\n%s", got, same)
		}
	})
}

// fixIn returns src with the fixes of suite applied, as a file of a module whose go.mod names goVersion, or as a file outside any module when goVersion is "".
func fixIn(t *testing.T, suite []*analysis.Analyzer, goVersion, src string) string {
	t.Helper()
	return fixInPackage(t, suite, goVersion, map[string]string{"x.go": src})
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

func TestOptInFixes(t *testing.T) {
	tests := []struct {
		name string
		suite []*analysis.Analyzer
		src, want string
	}{
		{
			name: "inline replaces a call to a function marked //go:fix inline",
			suite: []*analysis.Analyzer{inline.Analyzer},
			src: "package p\n\n//go:fix inline\nfunc double(x int) int { return x + x }\n\nfunc f() int {\n\treturn double(3)\n}\n",
			want: "package p\n\n//go:fix inline\nfunc double(x int) int { return x + x }\n\nfunc f() int {\n\treturn 3 + 3\n}\n",
		},
		{
			name: "composite keys an unkeyed literal of an imported struct",
			suite: []*analysis.Analyzer{composite.Analyzer},
			src: "package p\n\nimport \"net/url\"\n\nfunc f() *url.URL {\n\treturn &url.URL{\"https\", \"\", nil, \"example.com\", \"\", \"\", \"\", \"\", \"\", false, false}\n}\n",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fixIn(t, tt.suite, "1.27", tt.src)
			if tt.want != "" && got != tt.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, tt.want)
			}

			if tt.want == "" && got == tt.src {
				t.Errorf("composite made no fix")
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
