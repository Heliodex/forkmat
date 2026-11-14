package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"testing"
)

// reorderText returns src with its function runs reordered.
func reorderText(t *testing.T, src string) string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "x.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return string(reorderFuncRuns(fset, file, []byte(src)))
}

// declOrder returns the names of the top-level functions and methods of src, in
// order.
func declOrder(t *testing.T, src string) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "x.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var names []string
	for _, d := range file.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok {
			names = append(names, fd.Name.Name)
		}
	}
	return names
}

func TestReorderFuncs(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []string
	}{
		{
			name: "caller moves below callee",
			src:  "package p\n\nfunc a() {\n\tb()\n}\n\nfunc b() {}\n",
			want: []string{"b", "a"},
		},
		{
			name: "chain",
			src:  "package p\n\nfunc a() {\n\tb()\n}\n\nfunc b() {\n\tc()\n}\n\nfunc c() {}\n",
			want: []string{"c", "b", "a"},
		},
		{
			name: "independent functions keep their order",
			src:  "package p\n\nfunc a() {}\n\nfunc b() {}\n",
			want: []string{"a", "b"},
		},
		{
			name: "cycle keeps source order",
			src:  "package p\n\nfunc cyc1() {\n\tcyc2()\n}\n\nfunc cyc2() {\n\tcyc1()\n}\n",
			want: []string{"cyc1", "cyc2"},
		},
		{
			name: "function value counts as a reference",
			src:  "package p\n\nfunc a() {\n\tf := b\n\tf()\n}\n\nfunc b() {}\n",
			want: []string{"b", "a"},
		},
		{
			name: "local shadowing a function is not a reference",
			src:  "package p\n\nfunc p1() {\n\tleaf := 1\n\t_ = leaf\n}\n\nfunc leaf() {}\n",
			want: []string{"p1", "leaf"},
		},
		{
			name: "selector is not a reference",
			src:  "package p\n\nfunc p1(x T) {\n\tx.leaf()\n}\n\nfunc leaf() {}\n",
			want: []string{"p1", "leaf"},
		},
		{
			name: "method is a barrier",
			src:  "package p\n\nfunc a() {\n\tb()\n}\n\nfunc (T) M() {}\n\nfunc b() {}\n",
			want: []string{"a", "M", "b"},
		},
		{
			name: "init is a barrier",
			src:  "package p\n\nfunc a() {\n\tb()\n}\n\nfunc init() {}\n\nfunc b() {}\n",
			want: []string{"a", "init", "b"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := declOrder(t, reorderText(t, tt.src))
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("order = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestReorderComments(t *testing.T) {
	src := "package p\n\n// a is documented.\nfunc a() {\n\tb()\n}\n\n// b is documented.\nfunc b() {}\n"
	want := "package p\n\n// b is documented.\nfunc b() {}\n\n// a is documented.\nfunc a() {\n\tb()\n}\n"
	if got := reorderText(t, src); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestReorderIdempotent(t *testing.T) {
	src := "package p\n\nfunc a() {\n\tb()\n}\n\nfunc b() {\n\tc()\n}\n\nfunc c() {}\n"
	once := reorderText(t, src)
	if twice := reorderText(t, once); once != twice {
		t.Errorf("second pass changed the output:\n%s", twice)
	}
}
