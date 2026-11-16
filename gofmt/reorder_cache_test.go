package main

import (
	"bytes"
	"go/token"
	"os"
	"path/filepath"
	"testing"
)

// TestReorderedSourceFollowsItsSource checks that the reordered source of a file is the one of the source given, not of an earlier source of the same file: a sibling of a file on disk may ask for its reordered form before the file itself is formatted, and the file's edited source must not be replaced by the one on disk.
func TestReorderedSourceFollowsItsSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.go")
	disk := []byte("package p\n\nfunc f() {}\n\ntype T int\n")
	edited := []byte("package p\n\nfunc f() {}\n")

	if err := os.WriteFile(path, disk, 0o644); err != nil {
		t.Fatal(err)
	}

	// the sibling's request, for the file as it is on disk
	reorderedSource(token.NewFileSet(), path, disk)

	want, _ := reorderSource(token.NewFileSet(), path, edited)
	if got := reorderedSource(token.NewFileSet(), path, edited); !bytes.Equal(got, want) {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}
