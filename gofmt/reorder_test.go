package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDotImportTypes(t *testing.T) {
	dir := t.TempDir()
	write := func(name, src string) {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("go.mod", "module tmpmod\n\ngo 1.22\n")
	write("vm/vm.go", "package vm\n\ntype Coroutine struct{}\n\ntype val any\n\nconst Limit = 3\n")
	write("main.go", "package main\n\nimport . \"tmpmod/vm\"\n\ntype Args struct{ Co *Coroutine }\n")

	src, err := os.ReadFile(filepath.Join(dir, "main.go"))
	if err != nil {
		t.Fatal(err)
	}

	file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, "main.go"), src, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}

	got := dotImportTypes(filepath.Join(dir, "main.go"), file)
	if !got["Coroutine"] {
		t.Errorf("exported type of a dot-imported package not found: %v", got)
	}

	for _, name := range []string{"val", "Limit"} {
		if got[name] {
			t.Errorf("%s wrongly counted as a type", name)
		}
	}
}

func TestSiblingTypes(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"a.go": "package p\n\ntype Coroutine struct{}\n\nconst limit = 3\n",
		"b.go": "package p\n\ntype Val any\n",
		"c.go": "package q\n\ntype Other int\n",
		"d.go": "package p\n\nfunc helper() {}\n",
		"e.txt": "not go",
		"x.go": "package p\n\ntype Self int\n",
	}

	for name, src := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got := siblingTypes(filepath.Join(dir, "x.go"), "p")
	if !got["Coroutine"] || !got["Val"] {
		t.Errorf("types of sibling files not found: %v", got)
	}

	for _, name := range []string{"limit", "helper", "Other", "Self"} {
		if got[name] {
			t.Errorf("%s wrongly counted as a sibling type", name)
		}
	}
}

// declOrder returns the names of the top-level functions and methods of src, in order.
func declOrder(t *testing.T, src string) (names []string) {
	t.Helper()

	file, err := parser.ParseFile(token.NewFileSet(), "x.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	for _, d := range file.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			names = append(names, d.Name.Name)
		case *ast.GenDecl:
			if d.Tok == token.CONST || d.Tok == token.VAR || d.Tok == token.TYPE {
				var group []string

				for _, sp := range d.Specs {
					switch sp := sp.(type) {
					case *ast.TypeSpec:
						group = append(group, sp.Name.Name)
					case *ast.ValueSpec:
						for _, id := range sp.Names {
							group = append(group, id.Name)
						}
					}
				}

				names = append(names, strings.Join(group, ","))
			}
		}
	}

	return
}

// reorderText returns src with its function runs reordered.
func reorderText(t *testing.T, src string) string {
	t.Helper()

	out, _ := reorderSource(token.NewFileSet(), "x.go", []byte(src))

	return string(out)
}

// TestHoistOrder checks that the top of a file reads types, then constants, then vars, then functions, unless a dependency keeps a declaration elsewhere.
func TestHoistOrder(t *testing.T) {
	tests := []struct {
		name, src string
		want []string
	}{
		{
			name: "types, consts, vars, then functions",
			src: "package p\n\nfunc f() int { return a }\n\nvar a = b\n\nconst b = 2\n\ntype T int\n\nfunc g() {}\n",
			want: []string{"T", "b", "a", "f", "g"},
		},
		{
			name: "vars with effects move above functions, and so do the pure vars after them",
			src: "package p\n\nfunc h() int { return 1 }\n\nvar a = h()\n\nconst b = 2\n\nvar c = 3\n",
			want: []string{"b", "a", "c", "h"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := declOrder(t, reorderText(t, tt.src)); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("declaration order %v, want %v:\n%s", got, tt.want, reorderText(t, tt.src))
			}
		})
	}
}

func TestHoistTypesAfterImports(t *testing.T) {
	const src string = "package p\n\nimport \"fmt\"\n\nfunc f() { fmt.Println() }\n\ntype T struct{ s fmt.Stringer }\n"

	got := reorderText(t, src)

	if i, j := strings.Index(got, "import"), strings.Index(got, "type T"); i < 0 || j <= i {
		t.Errorf("type T not after the import:\n%s", got)
	}

	if i, j := strings.Index(got, "type T"), strings.Index(got, "func f"); i < 0 || i >= j {
		t.Errorf("type T not above func f:\n%s", got)
	}
}

func TestHoistVars(t *testing.T) {
	tests := []struct {
		name, src string
		want []string // the declarations, in order (see declOrder)
	}{
		{
			name: "pure vars move above functions and join",
			src: "package thing\n\nvar va = 1\n\nfunc c() int {\n\treturn 0\n}\n\nvar (\n\tvb = 2\n\tvc = 3\n)\n",
			want: []string{"va", "vb", "vc", "c"},
		},
		{
			name: "a pure var moves above a var it refers to, which is then first",
			src: "package p\n\nfunc f() {}\n\nvar y = x + 1\n\nvar x = 1\n",
			want: []string{"x", "y", "f"},
		},
		{
			name: "vars with effects move above functions, and join the vars there",
			src: "package p\n\nfunc f() int { return 0 }\n\nvar a = f()\n\nvar b = 2\n",
			want: []string{"a", "b", "f"},
		},
		{
			name: "a pure var before a var with effects moves above it",
			src: "package p\n\nfunc g() {}\n\nvar p = 1\n\nvar q = f()\n\nvar r = 2\n\nfunc f() int { return 0 }\n",
			want: []string{"p", "q", "r", "f", "g"},
		},
		{
			name: "a var with effects moves above a method, and the pure var after it follows",
			src: "package p\n\nvar (\n\tf int\n)\n\nfunc (m *M) ApplyZ(z int) {\n\tm.Z += z\n}\n\nvar (\n\tTexts = [3]*T{{V(1)}, {V(2)}, {V(3)}}\n\tterrain = &Terrain{}\n)\n",
			want: []string{"f", "Texts,terrain", "ApplyZ"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := declOrder(t, reorderText(t, tt.src)); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("declaration order %v, want %v:\n%s", got, tt.want, reorderText(t, tt.src))
			}
		})
	}
}

func TestReorderComments(t *testing.T) {
	const (
		src string = "package p\n\n// a is documented.\nfunc a() {\n\tb()\n}\n\n// b is documented.\nfunc b() {}\n"
		want string = "package p\n\n// b is documented.\nfunc b() {}\n\n// a is documented.\nfunc a() {\n\tb()\n}\n"
	)

	if got := reorderText(t, src); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestReorderFuncs(t *testing.T) {
	tests := []struct {
		name, src string
		want []string
	}{
		{
			name: "caller moves below callee",
			src: "package p\n\nfunc a() {\n\tb()\n}\n\nfunc b() {}\n",
			want: []string{"b", "a"},
		},
		{
			name: "chain",
			src: "package p\n\nfunc a() {\n\tb()\n}\n\nfunc b() {\n\tc()\n}\n\nfunc c() {}\n",
			want: []string{"c", "b", "a"},
		},
		{
			name: "independent functions keep their order",
			src: "package p\n\nfunc a() {}\n\nfunc b() {}\n",
			want: []string{"a", "b"},
		},
		{
			name: "cycle keeps source order",
			src: "package p\n\nfunc cyc1() {\n\tcyc2()\n}\n\nfunc cyc2() {\n\tcyc1()\n}\n",
			want: []string{"cyc1", "cyc2"},
		},
		{
			name: "function value counts as a reference",
			src: "package p\n\nfunc a() {\n\tf := b\n\tf()\n}\n\nfunc b() {}\n",
			want: []string{"b", "a"},
		},
		{
			name: "local shadowing a function is not a reference",
			src: "package p\n\nfunc a1() {\n\tleaf := 1\n\t_ = leaf\n}\n\nfunc leaf() {}\n",
			want: []string{"a1", "leaf"},
		},
		{
			name: "selector is not a reference",
			src: "package p\n\nfunc a1(x T) {\n\tx.leaf()\n}\n\nfunc leaf() {}\n",
			want: []string{"a1", "leaf"},
		},
		{
			name: "a method is ordered like a function",
			src: "package p\n\nfunc a() {\n\tb()\n}\n\nfunc (T) M() {}\n\nfunc b() {}\n",
			want: []string{"M", "b", "a"},
		},
		{
			name: "const declared before its use moves below it",
			src: "package p\n\nconst y = x\n\nconst x = 5\n",
			want: []string{"x", "y"},
		},
		{
			name: "const group moves as one unit",
			src: "package p\n\nconst (\n\tA = iota\n\tB\n)\n\nconst c = A\n",
			want: []string{"A,B", "c"},
		},
		{
			name: "local const shadowing a package const is not a reference",
			src: "package p\n\nfunc p1() {\n\tconst x = 1\n\t_ = x\n}\n\nconst x = 5\n",
			want: []string{"x", "p1"},
		},
		{
			name: "var declared before its use moves below it",
			src: "package p\n\nvar y = x\n\nvar x = 5\n",
			want: []string{"x", "y"},
		},
		{
			name: "var with a call is a barrier",
			src: "package p\n\nvar y = x\n\nvar x = f()\n",
			want: []string{"y", "x"},
		},
		{
			name: "var with a conversion of a constant moves",
			src: "package p\n\nvar y = x\n\nvar x = int32(5)\n",
			want: []string{"x", "y"},
		},
		{
			name: "type declared before its use moves below it",
			src: "package p\n\ntype m n\n\ntype n int\n",
			want: []string{"n", "m"},
		},
		{
			name: "specs of a type group reorder among themselves",
			src: "package p\n\ntype (\n\tm n\n\tn int\n)\n",
			want: []string{"n", "m"},
		},
		{
			name: "type with only type dependencies moves to the top",
			src: "package p\n\nfunc f() {}\n\ntype T struct{ x int }\n\ntype U struct{ t T }\n",
			want: []string{"T", "U", "f"},
		},
		{
			name: "type with a constant length stays below it",
			src: "package p\n\nconst N = 3\n\ntype Buf [N]byte\n\nfunc f() {}\n\ntype U struct{ b Buf }\n",
			want: []string{"N", "Buf", "f", "U"},
		},
		{
			name: "type with an imported package moves below the imports",
			src: "package p\n\nimport \"fmt\"\n\nfunc f() { fmt.Println() }\n\ntype T struct{ s fmt.Stringer }\n",
			want: []string{"T", "f"},
		},
		{
			name: "generic type moves to the top",
			src: "package p\n\nfunc f() {}\n\ntype G[T any] struct{ x T }\n",
			want: []string{"G", "f"},
		},
		{
			name: "types are sorted alphabetically",
			src: "package p\n\ntype Z int\n\ntype A int\n\ntype M int\n",
			want: []string{"A", "M", "Z"},
		},
		{
			name: "a dependency comes before alphabetical order",
			src: "package p\n\ntype A B\n\ntype B int\n\ntype C int\n",
			want: []string{"B", "A", "C"},
		},
		{
			name: "specs of a type group are sorted alphabetically",
			src: "package p\n\ntype (\n\tZ int\n\tA int\n)\n",
			want: []string{"A", "Z"},
		},
		{
			name: "functions are sorted alphabetically",
			src: "package p\n\nfunc z() {}\n\nfunc a() {}\n",
			want: []string{"a", "z"},
		},
		{
			name: "methods are sorted by receiver type, then name",
			src: "package p\n\ntype B struct{}\n\ntype A struct{}\n\nfunc (b B) Z() {}\n\nfunc (a A) Y() {}\n\nfunc (b B) X() {}\n",
			want: []string{"A", "B", "Y", "X", "Z"},
		},
		{
			name: "a method that calls another comes after it",
			src: "package p\n\ntype B struct{}\n\nfunc (b B) A() { b.Z() }\n\nfunc (b B) Z() {}\n",
			want: []string{"B", "Z", "A"},
		},
		{
			name: "init keeps its place",
			src: "package p\n\nfunc a() {\n\tb()\n}\n\nfunc init() {}\n\nfunc b() {}\n",
			want: []string{"b", "init", "a"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := declOrder(t, reorderText(t, tt.src)); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("order = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestReorderIdempotent(t *testing.T) {
	const src string = "package p\n\nfunc a() {\n\tb()\n}\n\nfunc b() {\n\tc()\n}\n\nfunc c() {}\n"

	once := reorderText(t, src)
	if twice := reorderText(t, once); once != twice {
		t.Errorf("second pass changed the output:\n%s", twice)
	}
}

// stmtPos returns the offset of the first occurrence of text in src.
func stmtPos(t *testing.T, src, text string) int {
	t.Helper()

	i := strings.Index(src, text)
	if i < 0 {
		t.Fatalf("%q not in output:\n%s", text, src)
	}

	return i
}

func TestReorderAssignRuns(t *testing.T) {
	tests := []struct {
		name, src, first, then string // first must come before then in the output
		sorted bool // whether the statements are sorted (then comes first) or kept
	}{
		{
			name: "independent := statements are sorted by name",
			src: "package p\n\nfunc f() {\n\tz := 1\n\ta := 2\n\t_, _ = z, a\n}\n",
			first: "a := 2",
			then: "z := 1",
			sorted: true,
		},
		{
			name: "a statement stays after the one it uses",
			src: "package p\n\nfunc f() {\n\tz := 1\n\ta := z\n\t_ = a\n}\n",
			first: "z := 1",
			then: "a := z",
		},
		{
			name: "a shadowing declaration stays after the read it would change",
			src: "package p\n\nvar n = 5\n\nfunc f() {\n\tz := n\n\tn := 1\n\t_, _ = z, n\n}\n",
			first: "z := n",
			then: "n := 1",
		},
		{
			name: "a call is not moved",
			src: "package p\n\nfunc f() {\n\tz := g()\n\ta := 2\n\t_, _ = z, a\n}\n",
			first: "z := g()",
			then: "a := 2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reorderText(t, tt.src)

			i, j := stmtPos(t, got, tt.first), stmtPos(t, got, tt.then)
			if tt.sorted && i >= j {
				t.Errorf("not sorted:\n%s", got)
			}

			if !tt.sorted && i >= j {
				t.Errorf("order changed:\n%s", got)
			}
		})
	}
}
