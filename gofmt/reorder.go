package main

// The reordering of -strict. It runs before printing, on the source text, in passes that each reorder one kind of thing:
//
//   - types move up, after the imports, when the names they use are types, imports, or type parameters (see hoistTypes);
//   - consts move up after them (see hoistConsts), and vars after those, as far as they do not move above a var that stays (see hoistVars);
//   - the const, var and type declarations of each run of consecutive ones are split into single declarations and sorted (see reorderSpecRuns);
//   - each run of consecutive plain functions, and each run of methods, is sorted by name, methods by receiver type and then name (see reorderDecls);
//   - each run of consecutive := statements with pure values is sorted by the names it declares (see reorderAssignRuns).
//
// In every run, a declaration comes after the declarations it refers to, and otherwise in alphabetical order. A cycle keeps its source order. Declarations and statements move with the comments that lead into them. A var whose value calls a function is not moved above another var, only above functions and the declarations that do not take part in initialisation (see pureExpr).
import (
	"bytes"
	"crypto/sha256"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type (
	// reorderKey identifies a reordered source: the file it is of, and a hash of the source that was reordered. The content is part of the key, since one file can be asked for in more than one source: a sibling asks for the file as it is on disk, and the file itself asks for the source it is formatting, which may differ.
	reorderKey struct {
		filename string
		sum [sha256.Size]byte
	}

	// edit replaces src[start:end] with text.
	edit struct {
		start, end int
		text []byte
	}

	// layout gives positions of a file as offsets into its source.
	layout struct {
		tf *token.File
		src []byte
		comments []*ast.CommentGroup
	}

	// unit is a declaration, or a spec of a type group, that is moved as a whole.
	unit struct {
		kind token.Token // token.FUNC for a function, otherwise the token of the declaration
		names []string // the names it declares; for a method, "Type.Name"
		body ast.Node // the part whose references count as dependencies
		objs []any // the declarations that resolved references may point to
		start, // where it starts, including its doc comment
		end token.Pos // where it ends

		method bool // a method: a function with a receiver
		methodName string // the name of a method
		sels []string // the names selected in its body, which may name other methods
	}
)

// maxStrictRounds bounds how often a file is formatted under -strict: the layout that the printer gives a file can change what the reorder passes would do, so a file whose output the passes still change is formatted again, and a file that does not settle after this many rounds keeps its last output.
const maxStrictRounds = 3

var (
	// moduleDepDirs remembers, for each module root, the directories of the packages its build depends on, by import path (see moduleDeps).
	moduleDepDirs memo[string, map[string]string]

	// modules remembers the result of enclosingModule for each directory it has been asked about.
	modules memo[string, [2]string]

	// packageDirs remembers the directory of each package found by packageDir, by its import path and the module it was imported from. The lookups are the slow part of the -strict passes: go/build runs go list for a package outside the standard library, and every file of a package imports the same packages again.
	packageDirs memo[[2]string, string]

	// predeclaredPure are the predeclared types, whose conversions have no side effects.
	predeclaredPure = map[string]bool{
		"bool": true,
		"byte": true,
		"complex128": true,
		"complex64": true,
		"float32": true,
		"float64": true,
		"int": true,
		"int16": true,
		"int32": true,
		"int64": true,
		"int8": true,
		"rune": true,
		"string": true,
		"uint": true,
		"uint16": true,
		"uint32": true,
		"uint64": true,
		"uint8": true,
		"uintptr": true,
	}

	// universeTypes are the predeclared type names.
	universeTypes = map[string]bool{
		"any": true,
		"bool": true,
		"byte": true,
		"comparable": true,
		"complex128": true,
		"complex64": true,
		"error": true,
		"float32": true,
		"float64": true,
		"int": true,
		"int16": true,
		"int32": true,
		"int64": true,
		"int8": true,
		"rune": true,
		"string": true,
		"uint": true,
		"uint16": true,
		"uint32": true,
		"uint64": true,
		"uint8": true,
		"uintptr": true,
	}

	// reordered remembers the reordered form of each source, by its file name and content (see reorderedSource).
	reordered memo[reorderKey, []byte]
)

// alphabeticalOrder orders the components of a run of types. A component is ready when every component it depends on has been placed; among the ready ones, the one whose first name (in alphabetical order) is smallest goes next. So types are sorted wherever the dependencies allow it, and a type always comes after the types it refers to.
func alphabeticalOrder(run []unit, deps, comps [][]int) (order []int) {
	type edge struct {
		from, to int
	}

	compOf := make([]int, len(run))

	for c, members := range comps {
		for _, t := range members {
			compOf[t] = c
		}
	}

	need := make([]int, len(comps)) // dependencies not yet placed
	dependents := make([][]int, len(comps))
	seen := map[edge]bool{}

	for t := range run {
		for _, u := range deps[t] {
			if a, b := compOf[t], compOf[u]; a != b && !seen[edge{a, b}] {
				seen[edge{a, b}] = true
				need[a]++
				dependents[b] = append(dependents[b], a)
			}
		}
	}

	key := make([]string, len(comps))

	for c, members := range comps {
		for _, t := range members {
			for _, n := range run[t].names {
				if key[c] == "" || n < key[c] {
					key[c] = n
				}
			}
		}
	}

	placed := make([]bool, len(comps))

	for range comps {
		best := -1

		for c := range comps {
			if !placed[c] && need[c] == 0 && (best < 0 || key[c] < key[best]) {
				best = c
			}
		}

		placed[best] = true
		order = append(order, comps[best]...)

		for _, a := range dependents[best] {
			need[a]--
		}
	}

	return
}

// applyEdits returns src with the edits applied. They must be in source order and must not overlap.
func applyEdits(src []byte, edits []edit) []byte {
	if len(edits) == 0 {
		return src
	}

	var (
		out []byte
		prev int
	)

	for _, e := range edits {
		out = append(out, src[prev:e.start]...)
		out = append(out, e.text...)
		prev = e.end
	}

	return append(out, src[prev:]...)
}

// assignDeps returns the dependencies of a run of := statements, by name. A statement j depends on an earlier statement i when one of them declares a name that the other uses or declares. Names are compared as spelled, since a declaration can shadow a name that an earlier statement reads from outside the run.
func assignDeps(run []unit) [][]int {
	mentions := make([]map[string]bool, len(run))
	declares := make([]map[string]bool, len(run))

	for t, u := range run {
		mentions[t] = map[string]bool{}

		ast.Inspect(u.body, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok {
				mentions[t][id.Name] = true
			}

			return true
		})

		declares[t] = map[string]bool{}

		for _, n := range u.names {
			declares[t][n] = true
		}
	}

	overlap := func(i, j int) bool {
		for n := range declares[i] {
			if mentions[j][n] || declares[j][n] {
				return true
			}
		}

		for n := range declares[j] {
			if mentions[i][n] {
				return true
			}
		}

		return false
	}
	deps := make([][]int, len(run))

	for j := range run {
		for i := range j {
			if overlap(i, j) {
				deps[j] = append(deps[j], i)
			}
		}
	}

	return deps
}

// components returns the strongly connected components of the graph deps, in the order Tarjan's algorithm emits them: each after the components it depends on. The units of a component are in source order.
func components(deps [][]int) [][]int {
	num := make([]int, len(deps)) // discovery number, or -1 before discovery
	low := make([]int, len(deps)) // lowest number reachable from the unit
	onStack := make([]bool, len(deps))

	for t := range num {
		num[t] = -1
	}

	var (
		stack []int
		counter int
		comps [][]int
		connect func(t int)
	)

	connect = func(t int) {
		num[t], low[t] = counter, counter
		counter++

		stack = append(stack, t)
		onStack[t] = true

		for _, u := range deps[t] {
			if num[u] < 0 {
				connect(u)

				low[t] = min(low[t], low[u])
			} else if onStack[u] {
				low[t] = min(low[t], num[u])
			}
		}

		if low[t] == num[t] {
			var comp []int

			for {
				w := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				onStack[w] = false
				comp = append(comp, w)

				if w == t {
					break
				}
			}

			sort.Ints(comp)

			comps = append(comps, comp)
		}
	}

	for t := range deps {
		if num[t] < 0 {
			connect(t)
		}
	}

	return comps
}

// declStart returns where d starts, including its doc comment.
func declStart(d ast.Decl) token.Pos {
	var doc *ast.CommentGroup

	switch d := d.(type) {
	case *ast.FuncDecl:
		doc = d.Doc
	case *ast.GenDecl:
		doc = d.Doc
	}

	if doc != nil {
		return doc.Pos()
	}

	return d.Pos()
}

// findEnclosingModule is enclosingModule without the memory.
func findEnclosingModule(dir string) (string, string) {
	for {
		if data, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil {
			for line := range strings.SplitSeq(string(data), "\n") {
				if mod, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
					return dir, strings.Trim(strings.TrimSpace(mod), "\"")
				}
			}

			return "", ""
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", ""
		}

		dir = parent
	}
}

// enclosingModule returns the root directory and the module path of the go.mod file that governs dir, or empty strings if there is none. The answer is remembered for each directory.
func enclosingModule(dir string) (string, string) {
	// the same directory must have one key, however it is written
	if !filepath.IsAbs(dir) {
		if abs, err := filepath.Abs(dir); err == nil {
			dir = abs
		}
	}

	m := modules.get(dir, func() [2]string {
		root, mod := findEnclosingModule(dir)

		return [2]string{root, mod}
	})

	return m[0], m[1]
}

func (l layout) line(p token.Pos) int {
	return l.tf.Line(p)
}

// lineEnd returns the offset just past the newline that ends the line containing offset o, or len(src) if there is none.
func (l layout) lineEnd(o int) int {
	for o < len(l.src) && l.src[o] != '\n' {
		o++
	}

	if o < len(l.src) {
		return o + 1
	}

	return len(l.src)
}

// lineStart returns the offset of the start of the line containing offset o.
func (l layout) lineStart(o int) int {
	for o > 0 && l.src[o-1] != '\n' {
		o--
	}

	return o
}

func (l layout) off(p token.Pos) int {
	return l.tf.Offset(p)
}

// assignRun returns the edit that reorders the statements list[i:j], or reports false if the run is already in order or has no room to move.
func (l layout) assignRun(list []ast.Stmt, i, j int, head token.Pos) (edit, bool) {
	var lo int

	if i == 0 {
		if l.line(head) == l.line(list[0].Pos()) {
			return edit{}, false
		}

		lo = l.lineEnd(l.off(head))
	} else {
		if l.line(list[i-1].End()) == l.line(list[i].Pos()) {
			return edit{}, false
		}

		lo = l.lineEnd(l.off(list[i-1].End()))
	}

	if j < len(list) && l.line(list[j-1].End()) == l.line(list[j].Pos()) {
		return edit{}, false
	}

	hi := l.lineEnd(l.off(list[j-1].End()))
	cut := lo
	run := make([]unit, 0, j-i)
	texts := make([][]byte, 0, j-i)

	for _, s := range list[i:j] {
		as := s.(*ast.AssignStmt)

		var names []string

		for _, lhs := range as.Lhs {
			if id, ok := lhs.(*ast.Ident); ok && id.Name != "_" {
				names = append(names, id.Name)
			}
		}

		run = append(run, unit{
			kind: token.DEFINE,
			names: names,
			body: as,
			objs: []any{as},
			start: as.Pos(),
			end: as.End(),
		})
		// the lines before the statement lead into it; then the statement itself, through the end of its line
		texts = append(texts, append(append([]byte{}, l.src[cut:l.off(as.Pos())]...), l.src[l.off(as.Pos()):l.lineEnd(l.off(as.End()))]...))
		cut = l.lineEnd(l.off(as.End()))
	}

	deps := assignDeps(run)
	order := alphabeticalOrder(run, deps, components(deps))
	identity := true

	for t, o := range order {
		if t != o {
			identity = false
			break
		}
	}

	if identity {
		return edit{}, false
	}

	var text []byte

	for _, t := range order {
		text = append(text, texts[t]...)
	}

	return edit{
		start: lo,
		end: hi,
		text: text,
	}, true
}

// commentIn reports whether a comment of the file starts in the offsets [from, to).
func (l layout) commentIn(from, to int) bool {
	for _, cg := range l.comments {
		for _, c := range cg.List {
			if o := l.off(c.Pos()); o >= from && o < to {
				return true
			}
		}
	}

	return false
}

// moduleDeps returns the directory of each package that the packages of the module at root depend on, by import path. go/build finds such a package by running go list in the working directory of the process, so a lookup from elsewhere would find the wrong module, and each lookup costs a process. One go list in the module root lists them all. A package the list misses is left to go/build.
func moduleDeps(root string) map[string]string {
	return moduleDepDirs.get(root, func() map[string]string {
		dirs := map[string]string{}
		cmd := exec.Command("go", "list", "-e", "-deps", "-f", "{{.ImportPath}}\t{{.Dir}}", "./...")
		cmd.Dir = root

		// a module that cannot be listed has no dependencies found this way; go/build still works
		if out, err := cmd.Output(); err == nil || len(out) > 0 {
			for line := range strings.SplitSeq(string(out), "\n") {
				if path, dir, ok := strings.Cut(line, "\t"); ok && dir != "" {
					dirs[path] = dir
				}
			}
		}

		return dirs
	})
}

// findPackageDir is packageDir without the memory.
func findPackageDir(path, srcDir string) string {
	if root, mod := enclosingModule(srcDir); mod != "" {
		if path == mod {
			return root
		}

		if rest, ok := strings.CutPrefix(path, mod+"/"); ok {
			return filepath.Join(root, filepath.FromSlash(rest))
		}

		// the other packages of the module's build are listed once, in a single go list
		if dir, ok := moduleDeps(root)[path]; ok {
			return dir
		}
	}

	pkg, err := build.Default.Import(path, srcDir, build.FindOnly)
	if err != nil {
		return ""
	}

	return pkg.Dir
}

// movable reports whether decls[i] can be cut out of the source alone: a declaration moves with its own lines, so nothing may share them.
func movable(l layout, decls []ast.Decl, i int) bool {
	if i > 0 && l.line(decls[i-1].End()) == l.line(declStart(decls[i])) {
		return false
	}

	return i+1 >= len(decls) || l.line(decls[i].End()) != l.line(declStart(decls[i+1]))
}

// moveDecls returns src with the declarations decls[i], for each i in moved, cut out of their places and inserted at offset at, in source order. Each moves with the comments that lead into it.
func moveDecls(l layout, src []byte, decls []ast.Decl, at int, moved []int) []byte {
	if len(moved) == 0 {
		return src
	}

	// the moved declarations go in at at; each is cut out of its place with the comments that lead into it
	var text []byte

	edits := []edit{
		{
			start: at,
			end: at,
		},
	}

	for _, i := range moved {
		start := at

		if i > 0 {
			start = l.lineEnd(l.off(decls[i-1].End()))
		}

		end := l.lineEnd(l.off(decls[i].End()))
		text = append(text, src[start:end]...)
		edits = append(edits, edit{
			start: start,
			end: end,
		})
	}

	edits[0].text = text

	return applyEdits(src, edits)
}

// hoistTypes is the pass that moves type declarations to the top of the file, after the imports. Only a type whose names are all types (of this file, or predeclared), imported packages, or its own type parameters moves: a name that is a constant, variable or function, or that cannot be resolved in this file, keeps the type where it is, and so does every type that depends on such a type. Declaration order at package level does not change the meaning of a type, so a move is safe; the restriction keeps a type with a constant length below its constant.
func hoistTypes(fset *token.FileSet, file *ast.File, src []byte, external map[string]bool) []byte {
	tf := fset.File(file.Pos())
	if tf == nil {
		return src
	}

	decls := file.Decls

	// imports, by the name each package is referred to with
	imports := map[string]bool{}
	l := layout{
		tf: tf,
		src: src,
		comments: file.Comments,
	}

	var m int // index of the first declaration after the imports

	for ; m < len(decls); m++ {
		g, ok := decls[m].(*ast.GenDecl)
		if !ok || g.Tok != token.IMPORT {
			break
		}

		for _, sp := range g.Specs {
			is := sp.(*ast.ImportSpec)

			var name string

			if is.Name != nil {
				name = is.Name.Name
			} else if path, err := strconv.Unquote(is.Path.Value); err == nil {
				name = path[strings.LastIndex(path, "/")+1:]
			}

			if name != "" && name != "_" && name != "." {
				imports[name] = true
			}
		}
	}

	// P is where moved types go: the line after the imports, or after the package clause if there are none
	P := l.lineEnd(l.off(file.Name.End()))

	if m > 0 {
		P = l.lineEnd(l.off(decls[m-1].End()))
	}

	// the candidates are the type declarations; specIdx finds a declaration by one of its specs
	cand := map[int]bool{}
	specIdx := map[any]int{}

	for i, d := range decls {
		if g, ok := d.(*ast.GenDecl); ok && g.Tok == token.TYPE {
			cand[i] = true

			for _, sp := range g.Specs {
				specIdx[sp] = i
			}
		}
	}

	// bad[i] is set for a declaration that uses a name which is neither a type nor an import; deps[i] lists the candidate declarations it uses
	bad := map[int]bool{}
	deps := map[int][]int{}

	for i := range cand {
		seen := map[int]bool{}
		visit := func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok {
				return true
			}

			if id.Obj == nil {
				if !universeTypes[id.Name] && !imports[id.Name] && !external[id.Name] {
					bad[i] = true
				}

				return true
			}

			switch d := id.Obj.Decl.(type) {
			case *ast.TypeSpec:
				if j, ok := specIdx[d]; ok && !seen[j] {
					seen[j] = true
					deps[i] = append(deps[i], j)
				}
			case *ast.Field:
				// a type parameter
			default:
				bad[i] = true
			}

			return true
		}

		var walk func(ast.Node) bool

		walk = func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.SelectorExpr:
				// only the package name of pkg.T is a use; T is not
				ast.Inspect(x.X, walk)
				return false
			case *ast.Field:
				// a field name is not a use; its type is
				if x.Type != nil {
					ast.Inspect(x.Type, walk)
				}

				return false
			}

			return visit(n)
		}

		ast.Inspect(decls[i], walk)
	}

	// a declaration with a bad dependency is bad too, and so is one that depends on a bad declaration
	for changed := true; changed; {
		changed = false

		for i := range cand {
			if bad[i] {
				continue
			}

			for _, j := range deps[i] {
				if bad[j] {
					bad[i] = true
					changed = true

					break
				}
			}
		}
	}

	// a run of types already at the top stays where it is
	top := m

	for top < len(decls) && cand[top] {
		top++
	}

	var moved []int

	for i := top; i < len(decls); i++ {
		if cand[i] && !bad[i] && movable(l, decls, i) {
			moved = append(moved, i)
		}
	}

	return moveDecls(l, src, decls, P, moved)
}

// packageDir returns the directory of the package with import path path, as seen from srcDir: a package of the module that srcDir is in is found from the module's root; any other package is found with go/build. It returns "" if the package cannot be found.
func packageDir(path, srcDir string) string {
	// a directory outside any module is searched the same way from any directory, so it shares one memory
	root, _ := enclosingModule(srcDir)

	return packageDirs.get([2]string{path, root}, func() string {
		return findPackageDir(path, srcDir)
	})
}

// dotImportTypes returns the names of the exported types of the packages that file imports with a dot, which bring those names in without a qualifier. The packages are located with go/build; a package that cannot be found adds no names.
func dotImportTypes(filename string, file *ast.File) map[string]bool {
	types := map[string]bool{}
	dir := filepath.Dir(filename)

	for _, imp := range file.Imports {
		if imp.Name == nil || imp.Name.Name != "." {
			continue
		}

		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}

		pkgDir := packageDir(path, dir)
		if pkgDir == "" {
			continue
		}

		entries, err := os.ReadDir(pkgDir)
		if err != nil {
			continue
		}

		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
				continue
			}

			f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(pkgDir, name), nil, parser.SkipObjectResolution)
			if err != nil {
				continue
			}

			for _, d := range f.Decls {
				if g, ok := d.(*ast.GenDecl); ok && g.Tok == token.TYPE {
					for _, sp := range g.Specs {
						if n := sp.(*ast.TypeSpec).Name; n.IsExported() {
							types[n.Name] = true
						}
					}
				}
			}
		}
	}

	return types
}

// pureExpr reports whether the node n is free of calls (other than conversions to predeclared types, make and new) and of receives. A function literal is pure only if funcLitOK: its body may hold blocks, which a move could not cut out.
func pureExpr(n ast.Node, funcLitOK bool) bool {
	pure := true

	ast.Inspect(n, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CallExpr:
			if id, ok := x.Fun.(*ast.Ident); !ok || !(predeclaredPure[id.Name] || id.Name == "make" || id.Name == "new") {
				pure = false
			}
		case *ast.FuncLit:
			if !funcLitOK {
				pure = false
			}
		case *ast.UnaryExpr:
			if x.Op == token.ARROW {
				pure = false
			}
		}

		return pure
	})

	return pure
}

// assignable reports whether s can be part of a run of := statements.
func assignable(s ast.Stmt) bool {
	as, ok := s.(*ast.AssignStmt)

	return ok && as.Tok == token.DEFINE && pureExpr(as, false)
}

// assignRunEdits returns the edits that reorder each run of := statements in list. head is the position of the token that opens the list, a brace or a colon.
func (l layout) assignRunEdits(list []ast.Stmt, head token.Pos) (edits []edit) {
	for i := 0; i < len(list); {
		if !assignable(list[i]) {
			i++
			continue
		}

		j := i + 1

		for j < len(list) && assignable(list[j]) && l.line(list[j-1].End()) != l.line(list[j].Pos()) {
			j++
		}

		if e, ok := l.assignRun(list, i, j, head); ok {
			edits = append(edits, e)
		}

		i = j
	}

	return
}

// pureVars reports whether the values of a var declaration can be evaluated without side effects, so that moving it does not change the order of any effect. Go initialises package variables in declaration order, except where a dependency forces another order, so a move is visible only through a side effect. A call can have one, and a receive can block; a conversion to a predeclared type, make and new cannot. Calls are checked syntactically, so a call to a function named like a predeclared type is taken to be impure.
func pureVars(d *ast.GenDecl) bool {
	return pureExpr(d, true)
}

// receiverType returns the name of the type a method is declared on: the receiver's type without its pointer, parentheses and type arguments.
func receiverType(recv *ast.FieldList) string {
	if recv == nil || len(recv.List) == 0 {
		return ""
	}

	for t := recv.List[0].Type; ; {
		switch x := t.(type) {
		case *ast.Ident:
			return x.Name
		case *ast.IndexExpr:
			t = x.X
		case *ast.IndexListExpr:
			t = x.X
		case *ast.ParenExpr:
			t = x.X
		case *ast.StarExpr:
			t = x.X
		default:
			return ""
		}
	}
}

// references returns the indices of the units that n refers to. An identifier refers to a unit if it resolves to one of the unit's declarations, or, when it is not resolved at all, if it has the name of one of the units. The selector in x.f, the name of a field, the key of a struct literal field and labels are not references.
func references(n ast.Node, byName map[string]int, byObj map[any]int) []int {
	var (
		out []int
		visit func(ast.Node) bool
	)

	visit = func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.BranchStmt:
			return false
		case *ast.Field:
			if x.Type != nil {
				ast.Inspect(x.Type, visit)
			}

			return false
		case *ast.Ident:
			var (
				t int
				ok bool
			)

			if x.Obj == nil {
				t, ok = byName[x.Name]
			} else {
				t, ok = byObj[x.Obj.Decl]
			}

			if ok {
				out = append(out, t)
			}
		case *ast.KeyValueExpr:
			if _, isName := x.Key.(*ast.Ident); !isName {
				ast.Inspect(x.Key, visit)
			}

			ast.Inspect(x.Value, visit)

			return false
		case *ast.LabeledStmt:
			ast.Inspect(x.Stmt, visit)
			return false
		case *ast.SelectorExpr:
			ast.Inspect(x.X, visit)
			return false
		}

		return true
	}

	ast.Inspect(n, visit)

	return out
}

// dependencyOrder returns the indices of run in an order where each unit comes after the units it refers to, and otherwise in alphabetical order by name (see alphabeticalOrder). The units are grouped into strongly connected components, that is, sets of units that refer to each other, directly or through others; the units of one component keep their source order.
func dependencyOrder(run []unit) []int {
	byName := map[string]int{} // for names that are not resolved
	byObj := map[any]int{} // for names resolved to a unit's declaration

	for t, u := range run {
		for _, n := range u.names {
			byName[n] = t
		}

		for _, o := range u.objs {
			byObj[o] = t
		}
	}

	// a method may call another method by name; that is a dependency too
	byMethod := map[string][]int{}

	for t, u := range run {
		if u.method {
			byMethod[u.methodName] = append(byMethod[u.methodName], t)
		}
	}

	deps := make([][]int, len(run))

	for t, u := range run {
		seen := map[int]bool{}
		refs := references(u.body, byName, byObj)

		for _, name := range u.sels {
			refs = append(refs, byMethod[name]...)
		}

		for _, v := range refs {
			if v != t && !seen[v] {
				seen[v] = true
				deps[t] = append(deps[t], v)
			}
		}

		sort.Ints(deps[t])
	}

	return alphabeticalOrder(run, deps, components(deps))
}

// reorderItems returns the edit that reorders run, the units between lo and hi in the source, or reports false if the run is already in order. Unit t occupies the lines from the line after unit t-1 ends, so the comments before it travel with it.
func (l layout) reorderItems(run []unit, lo, hi int) (edit, bool) {
	k := len(run)
	starts := make([]int, k+1)
	starts[0] = lo

	for t := 1; t < k; t++ {
		starts[t] = l.lineEnd(l.off(run[t-1].end))
	}

	starts[k] = hi
	order := dependencyOrder(run)
	identity := true

	for t, o := range order {
		if t != o {
			identity = false
			break
		}
	}

	if identity {
		return edit{}, false
	}

	var text []byte

	for _, t := range order {
		text = append(text, l.src[starts[t]:starts[t+1]]...)
	}

	return edit{
		start: lo,
		end: hi,
		text: text,
	}, true
}

// reorderAssignRuns is the pass over the runs of := statements. A run is a sequence of consecutive := statements with pure values (see pureExpr), each on a line of its own. Within a run, statements that do not depend on each other are sorted by the names they declare, and a statement stays after an earlier one it could interact with (see assignDeps). It runs over every block of the file, and over the clauses of switch and select statements.
func reorderAssignRuns(fset *token.FileSet, file *ast.File, src []byte) []byte {
	tf := fset.File(file.Pos())
	if tf == nil {
		return src
	}

	l := layout{
		tf: tf,
		src: src,
		comments: file.Comments,
	}

	var edits []edit

	ast.Inspect(file, func(n ast.Node) bool {
		switch b := n.(type) {
		case *ast.BlockStmt:
			edits = append(edits, l.assignRunEdits(b.List, b.Lbrace)...)
		case *ast.CaseClause:
			edits = append(edits, l.assignRunEdits(b.Body, b.Colon)...)
		case *ast.CommClause:
			edits = append(edits, l.assignRunEdits(b.Body, b.Colon)...)
		}

		return true
	})
	sort.Slice(edits, func(i, j int) bool {
		return edits[i].start < edits[j].start
	})

	return applyEdits(src, edits)
}

// selectorNames returns the names that n selects, as in x.name.
func selectorNames(n ast.Node) (out []string) {
	ast.Inspect(n, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			out = append(out, sel.Sel.Name)
		}

		return true
	})

	return
}

// siblingTypes returns the names of the types that other files of the package of filename declare. A file's package is the files of its directory with the same package name. A name that a sibling declares as a type is a type, so a type may use it and still move; a constant, variable or function of a sibling is not.
func siblingTypes(filename, pkg string) map[string]bool {
	types := map[string]bool{}

	if !onDisk(filename) {
		return types // read from standard input, there are no sibling files
	}

	self := filepath.Base(filename)

	for _, t := range dirTypes(filepath.Dir(filename), pkg, true) {
		if t.file != self {
			types[t.name] = true
		}
	}

	return types
}

// specTextFrom returns lead followed by a single declaration of sp: the keyword kw, then the spec's own text, through the end of its line.
func specTextFrom(src, lead []byte, sp ast.Spec, kw string, l layout) []byte {
	out := append([]byte{}, lead...)
	out = append(out, kw...)

	return append(out, src[l.off(sp.Pos()):l.lineEnd(l.off(sp.End()))]...)
}

// specText returns the text of a single declaration of spec sp, which starts with the lines src[from:keyword] and whose keyword is at keyword.
func specText(src []byte, from int, keyword token.Pos, sp ast.Spec, kw string, l layout) []byte {
	return specTextFrom(src, src[from:l.off(keyword)], sp, kw, l)
}

// specUnit returns spec sp of a declaration of token tok as a unit.
func specUnit(tok token.Token, sp ast.Spec) unit {
	var names []string

	start, end := sp.Pos(), sp.End()

	switch sp := sp.(type) {
	case *ast.TypeSpec:
		names = []string{sp.Name.Name}

		if sp.Doc != nil {
			start = sp.Doc.Pos()
		}
	case *ast.ValueSpec:
		for _, id := range sp.Names {
			names = append(names, id.Name)
		}

		if sp.Doc != nil {
			start = sp.Doc.Pos()
		}
	}

	return unit{
		kind: tok,
		names: names,
		body: sp,
		objs: []any{sp},
		start: start,
		end: end,
	}
}

// specRunEdit returns the edit that rewrites the declarations decls[i:j] as single declarations in their new order, or reports false if the run is left alone.
func specRunEdit(l layout, file *ast.File, decls []ast.Decl, i, j int) (edit, bool) {
	src := l.src

	// the run's extent: the lines after the declaration before it (or the package clause) to the end of its last declaration
	var lo int

	if i == 0 {
		if l.line(file.Name.End()) == l.line(declStart(decls[0])) {
			return edit{}, false
		}

		lo = l.lineEnd(l.off(file.Name.End()))
	} else {
		if l.line(decls[i-1].End()) == l.line(declStart(decls[i])) {
			return edit{}, false
		}

		lo = l.lineEnd(l.off(decls[i-1].End()))
	}

	last := decls[j-1].End()
	hi := l.lineEnd(l.off(last))

	if j < len(decls) && l.line(last) == l.line(declStart(decls[j])) {
		return edit{}, false
	}

	var (
		// one unit per spec. text is what the unit prints as: the lines that lead into it, then a single declaration of the spec.
		run []unit
		texts [][]byte
	)

	cut := lo

	for _, d := range decls[i:j] {
		g := d.(*ast.GenDecl)
		keyword := g.Tok.String() + " "

		if !g.Lparen.IsValid() {
			sp := g.Specs[0]
			run = append(run, specUnit(g.Tok, sp))
			texts = append(texts, specText(src, cut, g.TokPos, sp, keyword, l))
			cut = l.lineEnd(l.off(g.End()))

			continue
		}

		// a group with a comment anywhere in it is left alone: its comments would not follow their specs once they are sorted, and the next pass would sort them differently
		if l.commentIn(l.off(g.TokPos), l.off(g.Rparen)+1) {
			return edit{}, false
		}

		groupCut := l.lineEnd(l.off(g.Lparen))

		for k, sp := range g.Specs {
			run = append(run, specUnit(g.Tok, sp))

			if k == 0 {
				// the lines before the keyword lead into the first spec too
				lead := append(append([]byte{}, src[cut:l.off(g.TokPos)]...), src[groupCut:l.off(sp.Pos())]...)
				texts = append(texts, specTextFrom(src, lead, sp, keyword, l))
			} else {
				texts = append(texts, specTextFrom(src, src[groupCut:l.off(sp.Pos())], sp, keyword, l))
			}

			groupCut = l.lineEnd(l.off(sp.End()))
		}

		cut = l.lineEnd(l.off(g.End()))
	}

	order := dependencyOrder(run)
	identity := true

	for t, o := range order {
		if t != o {
			identity = false
			break
		}
	}

	if identity && j-i == 1 {
		return edit{}, false // a single declaration, or a group, already in order
	}

	var text []byte

	for _, t := range order {
		text = append(text, texts[t]...)
	}

	return edit{
		start: lo,
		end: hi,
		text: text,
	}, true
}

// topRunDecl reports whether d may stay in the run at the top of the file that hoistDecls moves kind into: an import, a type or a const, and for vars, a var.
func topRunDecl(d ast.Decl, kind token.Token) bool {
	g, ok := d.(*ast.GenDecl)
	if !ok {
		return false
	}

	switch g.Tok {
	case token.IMPORT, token.TYPE, token.CONST:
		return true
	case token.VAR:
		return kind == token.VAR
	}

	return false
}

// hoistDecls moves the declarations of kind (token.CONST or token.VAR) up to the run at the top of the file: after the imports, the types, the consts, and (for vars) the vars already there. Moved declarations keep their order. A var may only move when it does not pass a var that stays, since initialisation runs in declaration order; a pure var may also not pass a var with effects (see hoistVars).
func hoistDecls(fset *token.FileSet, file *ast.File, src []byte, kind token.Token) []byte {
	tf := fset.File(file.Pos())
	if tf == nil {
		return src
	}

	decls := file.Decls
	l := layout{
		tf: tf,
		src: src,
		comments: file.Comments,
	}

	// top is the end of the run at the top of the file that the moved declarations join
	var top int

	for top < len(decls) && topRunDecl(decls[top], kind) {
		top++
	}

	// P is where the moved declarations go: the line after the run, or after the package clause if there is no run
	P := l.lineEnd(l.off(file.Name.End()))

	if top > 0 {
		P = l.lineEnd(l.off(decls[top-1].End()))
	}

	var (
		moved []int
		stayed bool // an earlier var stays, so a var with effects may not move above it
		stayedImpure bool // an earlier var with effects stays, so a pure var may not move above it
	)

	for i := top; i < len(decls); i++ {
		g, ok := decls[i].(*ast.GenDecl)
		if !ok {
			continue
		}

		var impure, move bool

		switch {
		case g.Tok == token.VAR && kind == token.VAR:
			impure = !pureVars(g)

			// a var may not move above a var that stays: initialisation runs in declaration order, so a pure var could read state that the effects of one it passes change, and effects themselves must keep their order. A pure var may pass a pure one, since neither has effects and references still decide their order.
			if impure {
				move = movable(l, decls, i) && !stayed
			} else {
				move = movable(l, decls, i) && !stayedImpure
			}
		case g.Tok == kind:
			move = movable(l, decls, i)
		default:
			continue
		}

		if move {
			moved = append(moved, i)
			continue
		}

		if kind == token.VAR {
			stayed = true
			stayedImpure = stayedImpure || impure
		}
	}

	return moveDecls(l, src, decls, P, moved)
}

// hoistConsts is the pass that moves const declarations up to just after the imports and the types at the top of the file. A constant has no effect, so its place does not change what it means, and a group with iota moves as a whole.
func hoistConsts(fset *token.FileSet, file *ast.File, src []byte) []byte {
	return hoistDecls(fset, file, src, token.CONST)
}

// hoistVars is the pass that moves var declarations up to just after the imports, the types and the consts at the top of the file, so that the vars follow the consts. A var whose values have effects may move above functions and any other declaration that takes no part in initialisation, but not above a var that stays: initialisation runs those effects in declaration order, and a pure value may read a variable that such an initialiser changes. A pure var may not move above a var with effects that stays, whose effects it could read. The moved vars are then joined into a block by the printer, and reorderSpecRuns sorts them by dependency.
func hoistVars(fset *token.FileSet, file *ast.File, src []byte) []byte {
	return hoistDecls(fset, file, src, token.VAR)
}

// unitOf returns d as a unit, or false if the reordering does not move d.
func unitOf(d ast.Decl) (unit, bool) {
	switch d := d.(type) {
	case *ast.FuncDecl:
		if d.Body == nil || d.Recv == nil && d.Name.Name == "init" {
			return unit{}, false
		}

		if d.Recv != nil {
			recv := receiverType(d.Recv)
			if recv == "" {
				return unit{}, false
			}

			return unit{
				kind: token.FUNC,
				method: true,
				names: []string{recv + "." + d.Name.Name},
				body: d.Body,
				objs: []any{d},
				start: declStart(d),
				end: d.End(),
				methodName: d.Name.Name,
				sels: selectorNames(d.Body),
			}, true
		}

		return unit{
			kind: token.FUNC,
			names: []string{d.Name.Name},
			body: d.Body,
			objs: []any{d},
			start: declStart(d),
			end: d.End(),
			sels: selectorNames(d.Body),
		}, true
	}

	// const, var and type declarations are reordered by reorderSpecRuns
	return unit{}, false
}

// reorderFuncSlots is the pass over the functions and methods of the file. Each
// function and method, except init, is moved so that the functions it refers to
// come before it, and otherwise in alphabetical order (see dependencyOrder). The
// functions keep the slots they hold among the declarations, so the other
// declarations stay where they are. A function may move past a type, variable or
// constant: declaration order at package level does not change what they mean.
func reorderFuncSlots(fset *token.FileSet, file *ast.File, src []byte) []byte {
	tf := fset.File(file.Pos())
	if tf == nil {
		return src
	}

	decls := file.Decls
	l := layout{
		tf: tf,
		src: src,
		comments: file.Comments,
	}

	// the units, and the slots they hold
	var (
		run []unit
		slots []int
	)

	for i, d := range decls {
		if u, ok := unitOf(d); ok {
			run = append(run, u)
			slots = append(slots, i)
		}
	}

	if len(run) < 2 {
		return src
	}

	// a declaration that shares a line with another cannot be cut out alone
	for i := 1; i < len(decls); i++ {
		if l.line(decls[i-1].End()) == l.line(declStart(decls[i])) {
			return src
		}
	}

	// the text of each declaration with the lines that lead into it: the
	// declaration i occupies cut[i] to cut[i+1]
	cut := make([]int, len(decls)+1)
	cut[0] = l.lineEnd(l.off(file.Name.End()))

	for i, d := range decls {
		cut[i+1] = l.lineEnd(l.off(d.End()))
	}

	cut[len(decls)] = len(src)
	order := dependencyOrder(run)

	var edits []edit

	for k, slot := range slots {
		from := order[k]
		if from == k {
			continue
		}

		// the unit that moves into the slot keeps its own lines
		unitDecl := slots[from]
		edits = append(edits, edit{
			start: cut[slot],
			end: cut[slot+1],
			text: append([]byte{}, src[cut[unitDecl]:cut[unitDecl+1]]...),
		})
	}

	return applyEdits(src, edits)
}

// usesIota reports whether a declaration mentions iota.
func usesIota(d ast.Node) (found bool) {
	ast.Inspect(d, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == "iota" {
			found = true
		}

		return !found
	})

	return
}

// specRunToken returns the token of the declarations that reorderSpecRuns may split into specs and sort, or ILLEGAL if d is not one of them. Types always are. A var declaration is one if its values are pure (see pureVars). A const declaration is one unless it is a group whose specs take their values from their position: a spec without values repeats the one before it, and iota counts the specs, so such a group keeps its order. Single const declarations are always splittable: each is a group of its own.
func specRunToken(d ast.Decl) token.Token {
	g, ok := d.(*ast.GenDecl)
	if !ok {
		return token.ILLEGAL
	}

	switch g.Tok {
	case token.CONST:
		if !g.Lparen.IsValid() {
			return token.CONST
		}

		for _, sp := range g.Specs {
			if vs := sp.(*ast.ValueSpec); len(vs.Values) == 0 {
				return token.ILLEGAL
			}
		}

		if !usesIota(g) {
			return token.CONST
		}
	case token.TYPE:
		return token.TYPE
	case token.VAR:
		if pureVars(g) {
			return token.VAR
		}
	}

	return token.ILLEGAL
}

// reorderSpecRuns is the pass over the runs of const, var and type declarations (see specRunToken). A run is rewritten as single declarations, one per spec, in dependency order, and the printer then joins them into blocks. Within a run, specs that do not depend on each other are sorted alphabetically (see alphabeticalOrder). A group with a comment anywhere in it, and so a run that holds one, is left alone, since its comments would not follow their specs.
func reorderSpecRuns(fset *token.FileSet, file *ast.File, src []byte) []byte {
	tf := fset.File(file.Pos())
	if tf == nil {
		return src
	}

	decls := file.Decls
	l := layout{
		tf: tf,
		src: src,
		comments: file.Comments,
	}

	var edits []edit

	for i := 0; i < len(decls); {
		tok := specRunToken(decls[i])
		if tok == token.ILLEGAL {
			i++
			continue
		}

		j := i + 1

		for j < len(decls) && specRunToken(decls[j]) == tok {
			j++
		}

		if e, ok := specRunEdit(l, file, decls, i, j); ok {
			edits = append(edits, e)
		}

		i = j
	}

	return applyEdits(src, edits)
}

// reorderSource returns src with the passes of reordering applied: first the specs of type groups, then the runs of declarations. src is returned as it is if nothing moves.
// reorderSource applies the passes of reordering to src, which is the source of the
// file named filename. The source is parsed once, and parsed again only after a pass
// changes it. It returns the reordered source, and the file parsed from it, or nil if
// nothing changed.
func reorderSource(fset *token.FileSet, filename string, src []byte) ([]byte, *ast.File) {
	passes := []func(*token.FileSet, *ast.File, []byte) []byte{
		func(fset *token.FileSet, file *ast.File, src []byte) []byte {
			external := siblingTypes(filename, file.Name.Name)

			for name := range dotImportTypes(filename, file) {
				external[name] = true
			}

			return hoistTypes(fset, file, src, external)
		},
		hoistConsts,
		hoistVars,
		reorderSpecRuns,
		reorderFuncSlots,
		reorderAssignRuns,
		reorderBlockDecls,
	}

	file, err := parser.ParseFile(fset, filename, src, parser.ParseComments)
	if err != nil {
		return src, nil
	}

	var changed *ast.File

	for _, pass := range passes {
		out := pass(fset, file, src)
		if bytes.Equal(out, src) {
			continue
		}

		f, err := parser.ParseFile(fset, filename, out, parser.ParseComments)
		if err != nil {
			return src, nil
		}

		src, file, changed = out, f, f
	}

	return src, changed
}

// reorderStable reports whether the reorder passes leave src as it is. A file that cannot be parsed, or that is generated, is taken to be stable: the passes do not touch it.
func reorderStable(filename string, src []byte) bool {
	if header, err := parser.ParseFile(token.NewFileSet(), filename, src, parser.ParseComments|parser.PackageClauseOnly); err != nil || isGenerated(header) {
		return true
	}

	out, _ := reorderSource(token.NewFileSet(), filename, src)

	return bytes.Equal(out, src)
}

// reorderedSource returns the source of the file named filename, whose source is src, with the reorder passes applied. A file on disk is reordered once per source and run: the file itself, and the siblings that the switch pass reads, share the result of the same source.
func reorderedSource(fset *token.FileSet, filename string, src []byte) []byte {
	if !onDisk(filename) {
		out, _ := reorderSource(fset, filename, src)

		return out
	}

	return reordered.get(reorderKey{filename, sha256.Sum256(src)}, func() []byte {
		out, _ := reorderSource(token.NewFileSet(), filename, src)

		return out
	})
}

// reorderSourceOf returns src with the reorder passes applied. src is the file on disk when fromDisk is set, and its reordered form is then shared (see reorderedSource).
func reorderSourceOf(fset *token.FileSet, filename string, src []byte, fromDisk bool) []byte {
	if fromDisk {
		return reorderedSource(fset, filename, src)
	}

	out, _ := reorderSource(fset, filename, src)

	return out
}
