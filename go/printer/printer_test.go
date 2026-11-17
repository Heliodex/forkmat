package printer

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"forkmat/internal/diff"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type (
	checkMode uint

	entry struct {
		source, golden string
		mode checkMode
	}

	limitWriter struct {
		remaining, errCount int
	}

	visitor chan *ast.Ident
)

const (
	dataDir = "testdata"
	tabwidth = 8
)

const (
	export checkMode = 1 << iota
	rawFormat
	normNumber
	idempotent
	allowTypeParams
	strictLayout
)

var (
	update = flag.Bool("update", false, "update golden files")
	fset = token.NewFileSet()

	// Use go test -update to create/update the respective golden files.
	data = []entry{
		{"empty.input", "empty.golden", idempotent},
		{"comments.input", "comments.golden", 0},
		{"comments.input", "comments.x", export},
		{"comments2.input", "comments2.golden", idempotent},
		{"alignment.input", "alignment.golden", idempotent},
		{"linebreaks.input", "linebreaks.golden", idempotent},
		{"expressions.input", "expressions.golden", idempotent},
		{"expressions.input", "expressions.raw", rawFormat | idempotent},
		{"declarations.input", "declarations.golden", 0},
		{"statements.input", "statements.golden", 0},
		{"strict.input", "strict.golden", strictLayout | normNumber | idempotent},
		{"slow.input", "slow.golden", idempotent},
		{"complit.input", "complit.x", export},
		{"go2numbers.input", "go2numbers.golden", idempotent},
		{"go2numbers.input", "go2numbers.norm", normNumber | idempotent},
		{"generics.input", "generics.golden", idempotent | allowTypeParams},
		{"gobuild1.input", "gobuild1.golden", idempotent},
		{"gobuild2.input", "gobuild2.golden", idempotent},
		{"gobuild3.input", "gobuild3.golden", idempotent},
		{"gobuild4.input", "gobuild4.golden", idempotent},
		{"gobuild5.input", "gobuild5.golden", idempotent},
		{"gobuild6.input", "gobuild6.golden", idempotent},
		{"gobuild7.input", "gobuild7.golden", idempotent},
	}
	decls = []string{
		`import "fmt"`,
		"const pi = 3.1415\nconst e = 2.71828\n\nvar x = pi",
		"func sum(x, y int) int {\n\treturn x + y\n}",
	}
	stmts = []string{"i := 0", "select {}\nvar a, b = 1, 2\nreturn a + b", "go f()\ndefer func() {}()"}
)

// Verify that the printer doesn't crash if the AST contains BadXXX nodes.
func TestBadNodes(t *testing.T) {
	const (
		src = "package p\n("
		res = "package p\nBadDecl\n"
	)

	f, err := parser.ParseFile(fset, "", src, parser.ParseComments|parser.SkipObjectResolution)
	if err == nil {
		t.Error("expected illegal program") // error in test
	}

	var buf bytes.Buffer

	if err := Fprint(&buf, fset, f); err != nil {
		t.Fatal(err)
	}

	if buf.String() != res {
		t.Errorf("got %q, expected %q", buf.String(), res)
	}
}

func TestBaseIndent(t *testing.T) {
	// The testfile must not contain multi-line raw strings since those
	// are not indented (because their values must not change) and make
	// this test fail.
	const filename = "printer.go"

	t.Parallel()

	src, err := os.ReadFile(filename)
	if err != nil {
		panic(err) // error in test
	}

	file, err := parser.ParseFile(fset, filename, src, parser.SkipObjectResolution)
	if err != nil {
		panic(err) // error in test
	}

	for indent := range 4 {
		t.Run(strconv.Itoa(indent), func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer

			(&Config{
				Tabwidth: tabwidth,
				Indent: indent,
			}).Fprint(&buf, fset, file)

			// all code must be indented by at least 'indent' tabs
			lines := bytes.Split(buf.Bytes(), []byte{'\n'})

			for i, line := range lines {
				if len(line) == 0 {
					continue // empty lines don't have indentation
				}

				var n int

				for j, b := range line {
					if b != '\t' {
						// end of indentation
						n = j
						break
					}
				}

				if n < indent {
					t.Errorf("line %d: got only %d tabs; want at least %d: %q", i, n, indent, line)
				}
			}
		})
	}
}

// TestChanType tests that the tree for <-(<-chan int), without
// ParenExpr, is correctly formatted with parens.
// Test case for issue #63362.
func TestChanType(t *testing.T) {
	expr := &ast.UnaryExpr{
		Op: token.ARROW,
		X: &ast.CallExpr{
			Fun: &ast.ChanType{
				Dir: ast.RECV,
				Value: &ast.Ident{
					Name: "int",
				},
			},
			Args: []ast.Expr{
				&ast.Ident{
					Name: "nil",
				},
			},
		},
	}

	var buf bytes.Buffer

	if err := Fprint(&buf, fset, expr); err != nil {
		t.Fatal(err)
	}

	if got, want := buf.String(), `<-(<-chan int)(nil)`; got != want {
		t.Fatalf("got:\n%s\nwant:\n%s\n", got, want)
	}
}

func TestCommentedNode(t *testing.T) {
	const (
		input = `package main

func foo() {
	// comment inside func
}

// leading comment
type bar int // comment2

`
		foo = `func foo() {
	// comment inside func
}`
		bar = `// leading comment
type bar int // comment2
`
	)

	lfset := token.NewFileSet()

	f, err := parser.ParseFile(lfset, "input.go", input, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer

	err = Fprint(&buf, lfset, &CommentedNode{
		Node: f.Decls[0],
		Comments: f.Comments,
	})
	if err != nil {
		t.Fatal(err)
	}

	if buf.String() != foo {
		t.Errorf("got %q, want %q", buf.String(), foo)
	}

	buf.Reset()

	err = Fprint(&buf, lfset, &CommentedNode{
		Node: f.Decls[1],
		Comments: f.Comments,
	})
	if err != nil {
		t.Fatal(err)
	}

	if buf.String() != bar {
		t.Errorf("got %q, want %q", buf.String(), bar)
	}
}

func TestDeclLists(t *testing.T) {
	for _, src := range decls {
		file, err := parser.ParseFile(fset, "", "package p;"+src, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			panic(err) // error in test
		}

		var buf bytes.Buffer

		err = Fprint(&buf, fset, file.Decls) // only print declarations
		if err != nil {
			panic(err) // error in test
		}

		if out := buf.String(); out != src {
			t.Errorf("\ngot : %q\nwant: %q\n", out, src)
		}
	}
}

// TestEmptyDecl tests that empty decls for const, var, import are printed with
// valid syntax e.g "var ()" instead of just "var", which is invalid and cannot
// be parsed.
func TestEmptyDecl(t *testing.T) { // issue 63566
	for _, tok := range []token.Token{token.IMPORT, token.CONST, token.TYPE, token.VAR} {
		var buf bytes.Buffer

		if err := Fprint(&buf, token.NewFileSet(), &ast.GenDecl{
			Tok: tok,
		}); err != nil {
			t.Fatal(err)
		}

		got := buf.String()
		if want := tok.String() + " ()"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

// Verify that the printer can be invoked during initialization.
func init() {
	const name = "foobar"

	var buf bytes.Buffer

	if err := Fprint(&buf, fset, &ast.Ident{
		Name: name,
	}); err != nil {
		panic(err) // error in test
	}

	// in debug mode, the result contains additional information;
	// ignore it
	if s := buf.String(); !debug && s != name {
		panic("got " + s + ", want " + name)
	}
}

// TestFuncType tests that an ast.FuncType with a nil Params field
// can be printed (per go/ast specification). Test case for issue 3870.
func TestFuncType(t *testing.T) {
	const want = `package p

func f()
`

	src := &ast.File{
		Name: &ast.Ident{
			Name: "p",
		},
		Decls: []ast.Decl{
			&ast.FuncDecl{
				Name: &ast.Ident{
					Name: "f",
				},
				Type: &ast.FuncType{},
			},
		},
	}

	var buf bytes.Buffer

	if err := Fprint(&buf, fset, src); err != nil {
		t.Fatal(err)
	}

	if got := buf.String(); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s\n", got, want)
	}
}

func TestIssue11151(t *testing.T) {
	const (
		src = "package p\t/*\r/1\r*\r/2*\r\r\r\r/3*\r\r+\r\r/4*/\n"
		want = "package p /*/1*\r/2*\r/3*+/4*/\n" // \r following opening /* should be stripped
	)

	lfset := token.NewFileSet()

	f, err := parser.ParseFile(lfset, "", src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer

	if err := Fprint(&buf, lfset, f); err != nil {
		t.Fatal(err)
	}

	got := buf.String()
	if got != want {
		t.Errorf("\ngot : %q\nwant: %q", got, want)
	}

	// the resulting program must be valid
	_, err = parser.ParseFile(lfset, "", got, parser.SkipObjectResolution)
	if err != nil {
		t.Errorf("%v\norig: %q\ngot : %q", err, src, got)
	}
}

// Verify that we don't print a newline between "return" and its results, as
// that would incorrectly cause a naked return.
func TestIssue32854(t *testing.T) {
	const (
		src string = `package foo

func f() {
        return Composite{
                call(),
        }
}`
		want string = "return call()"
	)

	lfset := token.NewFileSet()

	file, err := parser.ParseFile(lfset, "", src, parser.SkipObjectResolution)
	if err != nil {
		panic(err)
	}

	// Replace the result with call(), which is on the next line.
	fd := file.Decls[0].(*ast.FuncDecl)
	ret := fd.Body.List[0].(*ast.ReturnStmt)
	ret.Results[0] = ret.Results[0].(*ast.CompositeLit).Elts[0]

	var buf bytes.Buffer

	if err := Fprint(&buf, lfset, ret); err != nil {
		t.Fatal(err)
	}

	if got := buf.String(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestIssue52605(t *testing.T) {
	const (
		orig = `
package p

// Doc
//
type T struct {
// This is not
//	a doc comment.
X int
}
`
		want = `package p

// Doc
type T struct {
	// This is not
	//	a doc comment.
	X int
}
`
	)

	f, err := parser.ParseFile(fset, "src.go", orig, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer

	err = Fprint(&buf, fset, f)
	if err != nil {
		t.Fatal(err)
	}

	if got := buf.String(); got != want {
		t.Errorf("got:\n%s\nwant:\n%s\n", got, want)
	}
}

// Verify that the SourcePos mode doesn't emit unnecessary //line directives
// before empty lines.
func TestIssue5945(t *testing.T) {
	const (
		orig = `
package p   // line 2
func f() {} // line 3

var x, y, z int


func g() { // line 8
}
`
		want = `//line src.go:2
package p

//line src.go:3
func f() {}

var x, y, z int

//line src.go:8
func g() {
}
`
	)

	// parse original
	f1, err := parser.ParseFile(fset, "src.go", orig, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}

	// pretty-print original
	var buf bytes.Buffer

	err = (&Config{
		Mode: UseSpaces | SourcePos,
		Tabwidth: 8,
	}).Fprint(&buf, fset, f1)
	if err != nil {
		t.Fatal(err)
	}

	got := buf.String()

	// compare original with desired output
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s\n", got, want)
	}
}

// TestLineComments, using a simple test case, checks that consecutive line
// comments are properly terminated with a newline even if the AST position
// information is incorrect.
func TestLineComments(t *testing.T) {
	const (
		src = `// comment 1
	// comment 2
	// comment 3
	package main
	`
		expected = 3
	)

	lfset := token.NewFileSet()

	f, err := parser.ParseFile(lfset, "", src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		panic(err) // error in test
	}

	var buf bytes.Buffer

	lfset = token.NewFileSet() // use the wrong file set

	if err := Fprint(&buf, lfset, f); err != nil {
		t.Fatal(err)
	}

	var nlines int

	for _, ch := range buf.Bytes() {
		if ch == '\n' {
			nlines++
		}
	}

	if nlines < expected {
		t.Errorf("got %d, expected %d\n", nlines, expected)
		t.Errorf("result:\n%s", buf.Bytes())
	}
}

// If a declaration has multiple specifications, a parenthesized
// declaration must be printed even if Lparen is token.NoPos.
func TestParenthesizedDecl(t *testing.T) {
	// a package with multiple specs in a single declaration
	const src = "package p; var ( a float64; b int )"

	lfset := token.NewFileSet()

	f, err := parser.ParseFile(lfset, "", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}

	// print the original package
	var buf bytes.Buffer

	err = Fprint(&buf, lfset, f)
	if err != nil {
		t.Fatal(err)
	}

	original := buf.String()

	// now remove parentheses from the declaration
	for i := 0; i != len(f.Decls); i++ {
		f.Decls[i].(*ast.GenDecl).Lparen = token.NoPos
	}

	buf.Reset()

	err = Fprint(&buf, lfset, f)
	if err != nil {
		t.Fatal(err)
	}

	if noparen := buf.String(); noparen != original {
		t.Errorf("got %q, want %q", noparen, original)
	}
}

func TestSourcePosNewline(t *testing.T) {
	const (
		// We don't provide a syntax for escaping or unescaping characters in line
		// directives (see https://go.dev/issue/24183#issuecomment-372449628).
		// As a result, we cannot write a line directive with the correct path for a
		// filename containing newlines. We should return an error rather than
		// silently dropping or mangling it.

		fname string = "foo\nbar/bar.go"
		src string = `package bar`
	)

	lfset := token.NewFileSet()

	f, err := parser.ParseFile(lfset, fname, src, parser.ParseComments|parser.AllErrors|parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}

	cfg := &Config{
		Mode: SourcePos, // emit line comments
		Tabwidth: 8,
	}

	var buf bytes.Buffer

	if err := cfg.Fprint(&buf, lfset, f); err == nil {
		t.Errorf("Fprint did not error for source file path containing newline")
	}

	if buf.Len() != 0 {
		t.Errorf("unexpected Fprint output:\n%s", buf.Bytes())
	}
}

func TestStmtLists(t *testing.T) {
	for _, src := range stmts {
		file, err := parser.ParseFile(fset, "", "package p; func _() {"+src+"}", parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			panic(err) // error in test
		}

		var buf bytes.Buffer

		err = Fprint(&buf, fset, mustBe[*ast.FuncDecl](file.Decls[0]).Body.List) // only print statements
		if err != nil {
			panic(err) // error in test
		}

		if out := buf.String(); out != src {
			t.Errorf("\ngot : %q\nwant: %q\n", out, src)
		}
	}
}

// Test whether the printer stops writing after the first error
func TestWriteErrors(t *testing.T) {
	const filename = "printer.go"

	t.Parallel()

	src, err := os.ReadFile(filename)
	if err != nil {
		panic(err) // error in test
	}

	file, err := parser.ParseFile(fset, filename, src, parser.SkipObjectResolution)
	if err != nil {
		panic(err) // error in test
	}

	for i := range 20 {
		lw := &limitWriter{
			remaining: i,
		}
		err := (&Config{
			Mode: RawFormat,
		}).Fprint(lw, fset, file)

		if lw.errCount > 1 {
			t.Fatal("Writes continued after first error returned")
		}

		// We expect errCount be 1 iff err is set
		if lw.errCount != 0 != (err != nil) {
			t.Fatal("Expected err when errCount != 0")
		}
	}
}

// checkEqual compares a and b.
func checkEqual(aname, bname string, a, b []byte) error {
	if bytes.Equal(a, b) {
		return nil
	}

	return errors.New(string(diff.Diff(aname, a, bname, b)))
}

// format parses src, prints the corresponding AST, verifies the resulting
// src is syntactically correct, and returns the resulting src or an error
// if any.
func format(src []byte, mode checkMode) ([]byte, error) {
	// parse src
	f, err := parser.ParseFile(fset, "", src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parse: %w\n%s", err, src)
	}

	// filter exports if necessary
	if mode&export != 0 {
		ast.FileExports(f) // ignore result

		f.Comments = nil // don't print comments that are not in AST
	}

	// determine printer configuration
	cfg := Config{
		Tabwidth: tabwidth,
	}

	if mode&rawFormat != 0 {
		cfg.Mode |= RawFormat
	}

	if mode&normNumber != 0 {
		cfg.Mode |= normalizeNumbers
	}

	if mode&strictLayout != 0 {
		cfg.Mode |= StrictLayout
	}

	// print AST
	var buf bytes.Buffer

	if err := cfg.Fprint(&buf, fset, f); err != nil {
		return nil, fmt.Errorf("print: %w", err)
	}

	// make sure formatted output is syntactically correct
	res := buf.Bytes()

	if _, err := parser.ParseFile(fset, "", res, parser.ParseComments|parser.SkipObjectResolution); err != nil {
		return nil, fmt.Errorf("re-parse: %w\n%s", err, buf.Bytes())
	}

	return res, nil
}

// TestIssue7195 checks that go/printer does not add an extra level of indentation
// when printing a return statement with multiple multi-line composite literals.
func TestIssue7195(t *testing.T) {
	const (
		src = `package p
type T struct{ x int }
func _() (T, *T) {
	return T{
			x: 1,
		}, &T{
			x: 2,
		}
}
`
		want = `package p

type T struct{ x int }

func _() (T, *T) {
	return T{
		x: 1,
	}, &T{
		x: 2,
	}
}
`
	)

	got, err := format([]byte(src), 0)
	if err != nil {
		t.Fatal(err)
	}

	if got := string(got); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s\n", got, want)
	}
}

// TestX is a skeleton test that can be filled in for debugging one-off cases.
// Do not remove.
func TestX(t *testing.T) {
	const src = `
package p
func _() {}
`

	if _, err := format([]byte(src), 0); err != nil {
		t.Error(err)
	}
}

// idents is an iterator that returns all idents in f via the result channel.
func idents(f *ast.File) <-chan *ast.Ident {
	v := make(visitor)

	go func() {
		ast.Walk(v, f)
		close(v)
	}()

	return v
}

// identCount returns the number of identifiers found in f.
func identCount(f *ast.File) (n int) {
	for range idents(f) {
		n++
	}

	return
}

// Verify that the SourcePos mode emits correct //line directives
// by testing that position information for matching identifiers
// is maintained.
func TestSourcePos(t *testing.T) {
	const src = `
package p
import ( "go/printer"; "math" )
const pi = 3.14; var x = 0
type t struct{ x, y, z int; u, v, w float32 }
func (t *t) foo(a, b, c int) int {
	return a*t.x + b*t.y +
		// two extra lines here
		// ...
		c*t.z
}
`

	// parse original
	f1, err := parser.ParseFile(fset, "src", src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}

	// pretty-print original
	var buf bytes.Buffer

	err = (&Config{
		Mode: UseSpaces | SourcePos,
		Tabwidth: 8,
	}).Fprint(&buf, fset, f1)
	if err != nil {
		t.Fatal(err)
	}

	// parse pretty printed original
	// (//line directives must be interpreted even w/o parser.ParseComments set)
	f2, err := parser.ParseFile(fset, "", buf.Bytes(), parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("%s\n%s", err, buf.Bytes())
	}

	// At this point the position information of identifiers in f2 should
	// match the position information of corresponding identifiers in f1.

	// number of identifiers must be > 0 (test should run) and must match
	n1 := identCount(f1)
	n2 := identCount(f2)

	if n1 == 0 {
		t.Fatal("got no idents")
	}

	if n2 != n1 {
		t.Errorf("got %d idents; want %d", n2, n1)
	}

	// verify that all identifiers have correct line information
	i2range := idents(f2)

	for i1 := range idents(f1) {
		i2 := <-i2range
		if i2.Name != i1.Name {
			t.Errorf("got ident %s; want %s", i2.Name, i1.Name)
		}

		// here we care about the relative (line-directive adjusted) positions
		l1 := fset.Position(i1.Pos()).Line
		if l2 := fset.Position(i2.Pos()).Line; l2 != l1 {
			t.Errorf("got line %d; want %d for %s", l2, l1, i1.Name)
		}
	}

	if t.Failed() {
		t.Logf("\n%s", buf.Bytes())
	}
}

func (l *limitWriter) Write(buf []byte) (n int, err error) {
	n = len(buf)

	if n >= l.remaining {
		n = l.remaining
		err = io.EOF
		l.errCount++
	}

	l.remaining -= n

	return n, err
}

func runcheck(t *testing.T, source, golden string, mode checkMode) {
	t.Helper()

	src, err := os.ReadFile(source)
	if err != nil {
		t.Error(err)
		return
	}

	res, err := format(src, mode)
	if err != nil {
		t.Error(err)
		return
	}

	// update golden files if necessary
	if *update {
		if werr := os.WriteFile(golden, res, 0o644); werr != nil {
			t.Error(werr)
		}

		return
	}

	// get golden
	gld, err := os.ReadFile(golden)
	if err != nil {
		t.Error(err)
		return
	}

	// formatted source and golden must be the same
	if cerr := checkEqual(fmt.Sprintf("format(%v)", source), golden, res, gld); cerr != nil {
		t.Error(cerr)
		return
	}

	if mode&idempotent != 0 {
		// formatting golden must be idempotent
		// (This is very difficult to achieve in general and for now
		// it is only checked for files explicitly marked as such.)
		res, err = format(gld, mode)
		if err != nil {
			t.Error(err)
			return
		}

		if err := checkEqual(golden, fmt.Sprintf("format(%s)", golden), gld, res); err != nil {
			t.Errorf("golden is not idempotent: %s", err)
		}
	}
}

func check(t *testing.T, source, golden string, mode checkMode) {
	t.Helper()

	// run the test
	cc := make(chan int, 1)

	go func() {
		runcheck(t, source, golden, mode)

		cc <- 0
	}()

	// wait with timeout
	select {
	case <-time.After(10 * time.Second): // plenty of a safety margin, even for very slow machines
		// test running past time out
		t.Errorf("%s: running too slowly", source)
	case <-cc:
		// test finished within allotted time margin
	}
}

func TestFiles(t *testing.T) {
	t.Parallel()

	for _, e := range data {
		source := filepath.Join(dataDir, e.source)
		golden := filepath.Join(dataDir, e.golden)
		mode := e.mode

		t.Run(e.source, func(t *testing.T) {
			t.Parallel()
			check(t, source, golden, mode)
			// TODO(gri) check that golden is idempotent
			// check(t, golden, golden, e.mode)
		})
	}
}

// strictText formats src with StrictLayout, as gofmt -strict does.
func strictText(t *testing.T, src string) string {
	t.Helper()

	fset := token.NewFileSet()

	file, err := parser.ParseFile(fset, "x.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	var buf bytes.Buffer

	cfg := Config{
		Mode: UseSpaces | TabIndent | StrictLayout,
		Tabwidth: 8,
	}

	if err := cfg.Fprint(&buf, fset, file); err != nil {
		t.Fatalf("print: %v", err)
	}

	return buf.String()
}

// TestStrictBinaryExprIndent checks that a condition broken by a comment indents its right operand, as it does without StrictLayout.
func TestStrictBinaryExprIndent(t *testing.T) {
	tests := []struct {
		name, src string
		// want is text the output must contain
		want string
	}{
		{
			name: "a comment ends the line after the operator",
			src: "package p\n\nfunc f(a, b bool) {\n\tif a && // first\n\tb {\n\t\tg()\n\t}\n}\n",
			want: "if a && // first\n\t\tb {",
		},
		{
			name: "the operand after a broken operator stays indented",
			src: "package p\n\nfunc f(a, b, c bool) {\n\tif a && // first\n\tb &&\n\tc {\n\t\tg()\n\t}\n}\n",
			want: "if a && // first\n\t\tb && c {",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := strictText(t, tt.src); !strings.Contains(got, tt.want) {
				t.Errorf("output lacks %q:\n%s", tt.want, got)
			}
		})
	}
}

// TestStrictCallClosingToken checks that a call broken by a comment ends with its closing parenthesis on a line of its own, after a trailing comma, with no blank line before it.
func TestStrictCallClosingToken(t *testing.T) {
	tests := []struct {
		name, src string
		// want is text the output must contain, and unwant text it must not
		want, unwant string
	}{
		{
			name: "a block comment before the closing parenthesis",
			src: "package p\n\nfunc f() {\n\tg(a, b /* c */)\n}\n",
			want: "\t\tb, /* c */\n\t)",
			unwant: "/* c */)",
		},
		{
			name: "a line comment before the closing parenthesis",
			src: "package p\n\nfunc f() {\n\tg(a, b, // c\n\t)\n}\n",
			want: "\t\tb, // c\n\t)",
			unwant: "// c\n\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := strictText(t, tt.src)
			if !strings.Contains(got, tt.want) {
				t.Errorf("output lacks %q:\n%s", tt.want, got)
			}

			if strings.Contains(got, tt.unwant) {
				t.Errorf("output holds %q:\n%s", tt.unwant, got)
			}
		})
	}
}

// TestStrictDeclGroupLeadingBlank checks that a blank line after the parenthesis of a declaration group is removed when a comment comes first in the group, as it is after a brace.
func TestStrictDeclGroupLeadingBlank(t *testing.T) {
	tests := []struct {
		name, src string
		// want is text the output must contain, and unwant text it must not
		want, unwant string
	}{
		{
			name: "a doc comment first in a package-level var group",
			src: "package p\n\nvar (\n\n\t// doc\n\ta = 1\n\tb = 2\n)\n",
			want: "var (\n\t// doc\n\ta = 1",
			unwant: "var (\n\n",
		},
		{
			name: "a comment first in a var group in a function",
			src: "package p\n\nfunc f() {\n\tvar (\n\n\t\t// w\n\t\tw = 1 // trailing\n\t)\n\t_ = w\n}\n",
			want: "var (\n\t\t// w\n\t\tw = 1 // trailing",
			unwant: "var (\n\n",
		},
		{
			name: "a comment first in a const group",
			src: "package p\n\nfunc f() {\n\tconst (\n\n\t\t// c\n\t\tk = 1\n\t)\n\t_ = k\n}\n",
			want: "const (\n\t\t// c\n\t\tk = 1",
			unwant: "const (\n\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := strictText(t, tt.src)
			if !strings.Contains(got, tt.want) {
				t.Errorf("output lacks %q:\n%s", tt.want, got)
			}

			if strings.Contains(got, tt.unwant) {
				t.Errorf("output holds %q:\n%s", tt.unwant, got)
			}
		})
	}
}

// TestStrictIfInitKeepsHeaderValid checks that an := moved into an if header still parses there, and does not shadow a named result.
func TestStrictIfInitKeepsHeaderValid(t *testing.T) {
	tests := []struct {
		name, src string
		// want is text the output must contain
		want string
	}{
		{
			name: "a composite literal of a named type stays out of the header",
			src: "package p\n\ntype T struct{ A int }\n\nfunc f() int {\n\tx := T{A: 1}\n\tif x.A > 0 {\n\t\treturn 1\n\t}\n\treturn 0\n}\n",
			want: "x := T{\n\t\tA: 1,\n\t}\n\tif x.A > 0 {",
		},
		{
			name: "a named result that a bare return gives stays unshadowed",
			src: "package p\n\nfunc read(p string) ([]byte, error) { return nil, nil }\n\nfunc g(p string) (b []byte) {\n\tb, err := read(p)\n\tif err != nil {\n\t\tpanic(err)\n\t}\n\treturn\n}\n",
			want: "b, err := read(p)\n\tif err != nil {",
		},
		{
			name: "a value without those problems still moves into the header",
			src: "package p\n\nfunc f() int { return 1 }\n\nfunc g() int {\n\tx := f()\n\tif x > 0 {\n\t\treturn x\n\t}\n\treturn 0\n}\n",
			want: "if x := f(); x > 0 {",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := strictText(t, tt.src); !strings.Contains(got, tt.want) {
				t.Errorf("output lacks %q:\n%s", tt.want, got)
			}
		})
	}
}

// TestStrictVarHeaders checks that a var declaration without a value moves into the header of the for or if statement after it when that statement is the only place its name is used, and stays where it is otherwise.
func TestStrictVarHeaders(t *testing.T) {
	tests := []struct {
		name, src string
		// want is text the output must contain, and unwant text it must not
		want, unwant string
	}{
		{
			name: "a var before a for loop with no header",
			src: "package p\n\nfunc f() {\n\tvar total int64\n\n\tfor {\n\t\ttotal++\n\t\tif total > 3 {\n\t\t\tbreak\n\t\t}\n\t}\n}\n",
			want: "for total := int64(0); ; {",
			unwant: "var total int64",
		},
		{
			name: "a var before a for loop with a condition",
			src: "package p\n\nfunc f() {\n\tvar n int\n\tfor n < 4 {\n\t\tn++\n\t}\n}\n",
			want: "for n := 0; n < 4; {",
		},
		{
			name: "a var before a switch",
			src: "package p\n\nfunc f() {\n\tvar n int64\n\tswitch n {\n\tcase 1:\n\t}\n}\n",
			want: "switch n := int64(0); n {",
		},
		{
			name: "a := with a value before a for loop",
			src: "package p\n\nfunc f() int {\n\tx := g()\n\tfor x < 4 {\n\t\tx++\n\t}\n\treturn 0\n}\n\nfunc g() int { return 1 }\n",
			want: "for x := g(); x < 4; {",
		},
		{
			name: "a := with a value before a switch",
			src: "package p\n\nfunc g() int { return 1 }\n\nfunc f() {\n\tx := g()\n\tswitch x {\n\tcase 1:\n\t}\n}\n",
			want: "switch x := g(); x {",
		},
		{
			name: "a := used after the loop keeps its declaration",
			src: "package p\n\nfunc g() int { return 1 }\n\nfunc f() int {\n\tx := g()\n\tfor x < 4 {\n\t\tx++\n\t}\n\treturn x\n}\n",
			want: "x := g()\n\n\tfor x < 4 {",
		},
		{
			name: "a := captured by a closure in a for loop keeps its declaration",
			src: "package p\n\nfunc g() int { return 1 }\n\nfunc f() {\n\tx := g()\n\tfor {\n\t\tgo func() { println(x) }()\n\t\tbreak\n\t}\n}\n",
			want: "x := g()\n\n\tfor {",
		},
		{
			name: "a := that redeclares a parameter keeps its place",
			src: "package p\n\nfunc g() (int, error) { return 1, nil }\n\nfunc f(err error) error {\n\tn, err := g()\n\tfor n < 3 {\n\t\tn++\n\t}\n\treturn err\n}\n",
			want: "n, err := g()\n\n\tfor n < 3 {",
		},
		{
			name: "a := that redeclares a named result keeps its place",
			src: "package p\n\nfunc g() (int, error) { return 1, nil }\n\nfunc f() (err error) {\n\tn, err := g()\n\tif n > 0 {\n\t\tprintln(n)\n\t}\n\treturn\n}\n",
			want: "n, err := g()\n\tif n > 0 {",
		},
		{
			name: "a := in a nested block that shadows a parameter still moves",
			src: "package p\n\nfunc g() (int, error) { return 1, nil }\n\nfunc f(err error) {\n\tif err != nil {\n\t\tn, err := g()\n\t\tif n > 0 {\n\t\t\tprintln(n)\n\t\t}\n\t}\n}\n",
			want: "if n, err := g(); n > 0 {",
		},
		{
			name: "a var before an if statement",
			src: "package p\n\nfunc f() {\n\tvar s string\n\tif s == \"\" {\n\t\tprintln(\"empty\")\n\t}\n}\n",
			want: "if s := \"\"; s == \"\" {",
		},
		{
			name: "a var used after the loop keeps its declaration",
			src: "package p\n\nfunc f() {\n\tvar n int\n\tfor n < 4 {\n\t\tn++\n\t}\n\tprintln(n)\n}\n",
			want: "var n int",
		},
		{
			name: "a var captured by a closure in a for loop keeps its declaration",
			src: "package p\n\nfunc f() {\n\tvar n int\n\tfor {\n\t\tgo func() { println(n) }()\n\t\tbreak\n\t}\n}\n",
			want: "var n int",
		},
		{
			name: "a var whose address is taken in a for loop keeps its declaration",
			src: "package p\n\nfunc f() {\n\tvar n int\n\tfor {\n\t\tp := &n\n\t\t*p++\n\t\tbreak\n\t}\n}\n",
			want: "var n int",
		},
		{
			name: "a var of a type that is not predeclared keeps its declaration",
			src: "package p\n\ntype S struct{}\n\nfunc f() {\n\tvar s S\n\tfor {\n\t\t_ = s\n\t\tbreak\n\t}\n}\n",
			want: "var s S",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := strictText(t, tt.src)
			if !strings.Contains(got, tt.want) {
				t.Errorf("output lacks %q:\n%s", tt.want, got)
			}

			if tt.unwant != "" && strings.Contains(got, tt.unwant) {
				t.Errorf("output holds %q:\n%s", tt.unwant, got)
			}

			// the header form is what the formatter writes, so formatting it again changes nothing
			if again := strictText(t, got); again != got {
				t.Errorf("second pass differs:\n%s", again)
			}
		})
	}
}

// testComment verifies that f can be parsed again after printing it
// with its first comment set to comment at any possible source offset.
func testComment(t *testing.T, f *ast.File, srclen int, comment *ast.Comment) {
	t.Helper()

	f.Comments[0].List[0] = comment

	var buf bytes.Buffer

	for offs := 0; offs <= srclen; offs++ {
		buf.Reset()
		// Printing f should result in a correct program no
		// matter what the (incorrect) comment position is.
		if err := Fprint(&buf, fset, f); err != nil {
			t.Error(err)
		}

		if _, err := parser.ParseFile(fset, "", buf.Bytes(), parser.SkipObjectResolution); err != nil {
			t.Fatalf("incorrect program for pos = %d:\n%s", comment.Slash, buf.String())
		}

		// Position information is just an offset.
		// Move comment one byte down in the source.
		comment.Slash++
	}
}

// Verify that the printer produces a correct program
// even if the position information of comments introducing newlines
// is incorrect.
func TestBadComments(t *testing.T) {
	const src = `
// first comment - text and position changed by test
package p
import "fmt"
const pi = 3.14 // rough circle
var (
	x, y, z int = 1, 2, 3
	u, v float64
)
func fibo(n int) {
	if n < 2 {
		return n /* seed values */
	}
	return fibo(n-1) + fibo(n-2)
}
`

	t.Parallel()

	f, err := parser.ParseFile(fset, "", src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		t.Error(err) // error in test
	}

	comment := f.Comments[0].List[0]
	pos := comment.Pos()

	if fset.PositionFor(
		pos,
		false, /* absolute position */
	).Offset != 1 {
		t.Error("expected offset 1") // error in test
	}

	testComment(t, f, len(src), &ast.Comment{
		Slash: pos,
		Text: "//-style comment",
	})
	testComment(t, f, len(src), &ast.Comment{
		Slash: pos,
		Text: "/*-style comment */",
	})
	testComment(t, f, len(src), &ast.Comment{
		Slash: pos,
		Text: "/*-style \n comment */",
	})
	testComment(t, f, len(src), &ast.Comment{
		Slash: pos,
		Text: "/*-style comment \n\n\n */",
	})
}

func (v visitor) Visit(n ast.Node) (w ast.Visitor) {
	if ident, ok := n.(*ast.Ident); ok {
		v <- ident
	}

	return v
}
