package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// formatStdinAs formats src as the file at path, read from standard input, the way -stdin-filepath does, and returns the output.
func formatStdinAs(t *testing.T, path, src string) string {
	t.Helper()

	initParserMode()
	initRewrite()

	var buf, errBuf bytes.Buffer

	s := newSequencer(2<<20, &buf, &errBuf)
	s.Add(0, func(r *reporter) error {
		return processFile(path, nil, strings.NewReader(src), r)
	})

	if code := s.GetExitCode(); code != 0 {
		t.Fatalf("exit code %d: %s", code, errBuf.String())
	}

	return buf.String()
}

func TestStdinFilepathUsesThePackageOnDisk(t *testing.T) {
	// the file on disk is an old version, and the buffer holds the loop that slices.Contains replaces. The type Source is declared in a sibling file, which only the package on disk gives the fix its types from.
	saved := *moderniseFlag
	defer func() { *moderniseFlag = saved }()
	*moderniseFlag = true

	dir := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	must(os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/m\n\ngo 1.27\n"), 0o644))
	must(os.WriteFile(filepath.Join(dir, "b.go"), []byte("package p\n\ntype Source struct {\n\tId uint32\n}\n"), 0o644))
	must(os.WriteFile(filepath.Join(dir, "a.go"), []byte("package p\n"), 0o644))

	buffer := "package p\n\nfunc Has(xs []Source, s Source) bool {\n\tfor _, x := range xs {\n\t\tif x == s {\n\t\t\treturn true\n\t\t}\n\t}\n\n\treturn false\n}\n"
	want := "package p\n\nimport \"slices\"\n\nfunc Has(xs []Source, s Source) bool {\n\treturn slices.Contains(xs, s)\n}\n"

	got := formatStdinAs(t, filepath.Join(dir, "a.go"), buffer)
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}
