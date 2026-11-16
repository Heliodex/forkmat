package main

import (
	"os"
	"path/filepath"
	"testing"
)

// indexOf returns the byte index of sub in s, or -1.
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}

	return -1
}

// TestSwitchNumbersTheBufferConstants checks that a constant of the file being formatted is numbered from its source as given, not from its copy on disk: a constant that only the unsaved buffer declares orders the cases of a switch like the others.
func TestSwitchNumbersTheBufferConstants(t *testing.T) {
	// the buffer adds Z, which sorts after A and B, and puts its case first
	const buffer string = "package p\n\nconst A = 1\n\nconst B = 2\n\nconst Z = 9\n\nfunc f(v int) int {\n\tswitch v {\n\tcase Z:\n\t\treturn 3\n\tcase A:\n\t\treturn 1\n\t}\n\treturn 0\n}\n"

	saved := *strict

	defer func() {
		*strict = saved
	}()

	*strict = true
	dir := t.TempDir()
	must := func(err error) {
		t.Helper()

		if err != nil {
			t.Fatal(err)
		}
	}

	must(os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/m\n\ngo 1.27\n"), 0o644))
	// the file on disk declares A and B, but not Z
	must(os.WriteFile(filepath.Join(dir, "x.go"), []byte("package p\n\nconst A = 1\n\nconst B = 2\n"), 0o644))

	got := formatStdinAs(t, filepath.Join(dir, "x.go"), buffer)

	ai := indexOf(got, "case A:")
	if zi := indexOf(got, "case Z:"); ai < 0 || zi < 0 || ai > zi {
		t.Errorf("cases not ordered by the buffer's constants:\n%s", got)
	}
}
