package main

import (
	"go/parser"
	"go/token"
	"testing"
)

// blockOrderOf returns src with the block pass applied, as reorderSource gives it.
func blockOrderOf(t *testing.T, src string) string {
	t.Helper()

	fset := token.NewFileSet()

	file, err := parser.ParseFile(fset, "x.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	return string(reorderBlockDecls(fset, file, []byte(src)))
}

func TestBlockOrder(t *testing.T) {
	tests := []struct {
		name, src, want string
	}{
		{
			name: "types and constants move above a variable",
			src: "package p\n\nfunc f() int {\n\tx := 1\n\ttype T int\n\tconst C = 2\n\treturn x + C + int(T(1))\n}\n",
			want: "package p\n\nfunc f() int {\n\ttype T int\n\tconst C = 2\n\tx := 1\n\treturn x + C + int(T(1))\n}\n",
		},
		{
			name: "a type moves above a call, but a variable with a call stays",
			src: "package p\n\nfunc g() {\n\tfmt.Println()\n\tx := h()\n\ttype U int\n\t_ = x\n\t_ = U(0)\n}\n",
			want: "package p\n\nfunc g() {\n\ttype U int\n\tfmt.Println()\n\tx := h()\n\t_ = x\n\t_ = U(0)\n}\n",
		},
		{
			name: "a variable does not pass an if statement",
			src: "package p\n\nfunc k(c bool) int {\n\tif c {\n\t\treturn 0\n\t}\n\ty := 3\n\ttype V int\n\treturn y + int(V(1))\n}\n",
			want: "package p\n\nfunc k(c bool) int {\n\ttype V int\n\tif c {\n\t\treturn 0\n\t}\n\ty := 3\n\treturn y + int(V(1))\n}\n",
		},
		{
			name: "a type stays below a statement that uses its name",
			src: "package p\n\nfunc s() {\n\t_ = T(0)\n\ttype T int\n\t_ = T(1)\n}\n",
			want: "package p\n\nfunc s() {\n\t_ = T(0)\n\ttype T int\n\t_ = T(1)\n}\n",
		},
		{
			name: "a variable stays below the variable it uses",
			src: "package p\n\nfunc m() {\n\ta := 1\n\tb := T(a)\n\ttype T int\n\t_ = b\n}\n",
			want: "package p\n\nfunc m() {\n\ta := 1\n\tb := T(a)\n\ttype T int\n\t_ = b\n}\n",
		},
		{
			name: "a constant moves above a variable it does not use",
			src: "package p\n\nfunc n() {\n\tx := 1\n\ty := x + 1\n\tconst x2 = 3\n\t_ = y\n\t_ = x2\n}\n",
			want: "package p\n\nfunc n() {\n\tconst x2 = 3\n\tx := 1\n\ty := x + 1\n\t_ = y\n\t_ = x2\n}\n",
		},
		{
			name: "a block with a label is left alone",
			src: "package p\n\nfunc l() {\n\tx := 1\n\ttype T int\n\t_ = x\n\t_ = T(0)\nL:\n\tgoto L\n}\n",
			want: "package p\n\nfunc l() {\n\tx := 1\n\ttype T int\n\t_ = x\n\t_ = T(0)\nL:\n\tgoto L\n}\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := blockOrderOf(t, tt.src); got != tt.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}
