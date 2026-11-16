package main

import (
	"os"
	"path/filepath"
	"testing"
)

// constantsIn returns src with -constants applied, as x.go of a module with the other files given.
func constantsIn(t *testing.T, src string, others map[string]string) string {
	t.Helper()
	const gomod string = "module example.com/m\n\ngo 1.27\n"

	t.Helper()

	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(gomod), 0o644); err != nil {
		t.Fatal(err)
	}

	for name, body := range others {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	return string(constantsSource(filepath.Join(dir, "x.go"), []byte(src)))
}

func TestConstants(t *testing.T) {
	tests := []struct {
		name, src, want string
	}{
		{
			name: "an unassigned := becomes a typed const",
			src: "package p\n\nfunc f() int {\n\tx := 5\n\treturn x\n}\n",
			want: "package p\n\nfunc f() int {\n\tconst x int = 5\n\treturn x\n}\n",
		},
		{
			name: "a var keeps its written type",
			src: "package p\n\nfunc f() string {\n\tvar s string = \"hi\"\n\treturn s\n}\n",
			want: "package p\n\nfunc f() string {\n\tconst s string = \"hi\"\n\treturn s\n}\n",
		},
		{
			name: "an inferred var gets the default type",
			src: "package p\n\nfunc f() int {\n\tvar s = 2\n\treturn s\n}\n",
			want: "package p\n\nfunc f() int {\n\tconst s int = 2\n\treturn s\n}\n",
		},
		{
			name: "an if initialiser moves out in front of the if",
			src: "package p\n\nimport \"fmt\"\n\nfunc f() {\n\tif n := 3; n > 2 {\n\t\tfmt.Println(n)\n\t}\n}\n",
			want: "package p\n\nimport \"fmt\"\n\nfunc f() {\n\tconst n int = 3\n\tif n > 2 {\n\t\tfmt.Println(n)\n\t}\n}\n",
		},
		{
			name: "a switch initialiser moves out in front of the switch",
			src: "package p\n\nimport \"fmt\"\n\nfunc f() {\n\tswitch k := 2; k {\n\tcase 2:\n\t\tfmt.Println(k)\n\t}\n}\n",
			want: "package p\n\nimport \"fmt\"\n\nfunc f() {\n\tconst k int = 2\n\tswitch k {\n\tcase 2:\n\t\tfmt.Println(k)\n\t}\n}\n",
		},
		{
			name: "a for initialiser that is never changed moves out",
			src: "package p\n\nimport \"fmt\"\n\nfunc f() {\n\tfor k := 10; k > 0; {\n\t\tfmt.Println(k)\n\t\tbreak\n\t}\n}\n",
			want: "package p\n\nimport \"fmt\"\n\nfunc f() {\n\tconst k int = 10\n\tfor k > 0 {\n\t\tfmt.Println(k)\n\t\tbreak\n\t}\n}\n",
		},
		{
			name: "a conversion to the declared type is dropped",
			src: "package p\n\nfunc f() uint32 {\n\tr := uint32(0)\n\treturn r\n}\n",
			want: "package p\n\nfunc f() uint32 {\n\tconst r uint32 = 0\n\treturn r\n}\n",
		},
		{
			name: "a conversion of a var to its declared type is dropped",
			src: "package p\n\nfunc g() int {\n\tvar k int = int(7)\n\treturn k\n}\n",
			want: "package p\n\nfunc g() int {\n\tconst k int = 7\n\treturn k\n}\n",
		},
		{
			name: "a conversion to another type stays",
			src: "package p\n\nfunc h() int64 {\n\tx := int64(uint8(3))\n\treturn x\n}\n",
			want: "package p\n\nfunc h() int64 {\n\tconst x int64 = int64(uint8(3))\n\treturn x\n}\n",
		},
		{
			name: "a variable that is incremented stays",
			src: "package p\n\nfunc f() int {\n\tc := 1\n\tc++\n\treturn c\n}\n",
			want: "package p\n\nfunc f() int {\n\tc := 1\n\tc++\n\treturn c\n}\n",
		},
		{
			name: "a variable changed by a closure stays",
			src: "package p\n\nfunc f() int {\n\tc := 0\n\tinc := func() { c++ }\n\tinc()\n\treturn c\n}\n",
			want: "package p\n\nfunc f() int {\n\tc := 0\n\tinc := func() { c++ }\n\tinc()\n\treturn c\n}\n",
		},
		{
			name: "a variable assigned by range stays",
			src: "package p\n\nfunc f(xs []int) int {\n\tw := 7\n\tfor _, w = range xs {\n\t}\n\treturn w\n}\n",
			want: "package p\n\nfunc f(xs []int) int {\n\tw := 7\n\tfor _, w = range xs {\n\t}\n\treturn w\n}\n",
		},
		{
			name: "a float stays a variable",
			src: "package p\n\nfunc f() float64 {\n\tx := 1.5\n\treturn x\n}\n",
			want: "package p\n\nfunc f() float64 {\n\tx := 1.5\n\treturn x\n}\n",
		},
		{
			name: "an initialiser whose name is also used outside stays",
			src: "package p\n\nimport \"fmt\"\n\nfunc f() {\n\tn := 1\n\tif n := 3; n > 2 {\n\t\tfmt.Println(n)\n\t}\n\tfmt.Println(n)\n}\n",
			want: "package p\n\nimport \"fmt\"\n\nfunc f() {\n\tconst n int = 1\n\tif n := 3; n > 2 {\n\t\tfmt.Println(n)\n\t}\n\tfmt.Println(n)\n}\n",
		},
		{
			name: "a constant whose use would overflow stays a variable",
			src: "package p\n\nfunc f() uint8 {\n\tu := uint8(255)\n\tv := u + 1\n\treturn v\n}\n",
			want: "package p\n\nfunc f() uint8 {\n\tu := uint8(255)\n\tv := u + 1\n\treturn v\n}\n",
		},
		{
			name: "a package-level variable stays",
			src: "package p\n\nvar pkgLevel = 3\n\nfunc f() int {\n\treturn pkgLevel\n}\n",
			want: "package p\n\nvar pkgLevel = 3\n\nfunc f() int {\n\treturn pkgLevel\n}\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := constantsIn(t, tt.src, nil); got != tt.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}

func TestConstantsNeedsThePackage(t *testing.T) {
	const (
		// the variable has a named type declared in a sibling file, whose name is not written here, so it stays a variable
		src = "package p\n\nfunc f() Count {\n\tn := Count(3)\n\treturn n\n}\n"

		// a file read from standard input has no package to check, so it is left alone
		plain = "package p\n\nfunc f() int {\n\tx := 5\n\treturn x\n}\n"
	)

	got := constantsIn(t, src, map[string]string{"b.go": "package p\n\ntype Count int\n"})
	if got != src {
		t.Errorf("got:\n%s\nwant:\n%s", got, src)
	}

	if got := string(constantsSource("<standard input>", []byte(plain))); got != plain {
		t.Errorf("standard input got:\n%s\nwant:\n%s", got, plain)
	}
}
