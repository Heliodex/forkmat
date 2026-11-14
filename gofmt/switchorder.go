package main

import (
	"bytes"
	"forkmat/go/printer"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// switchEnv gives the constants that the cases of the switch statements of a file may name: those of its package, and those of the packages it imports. The package is type-checked with its sibling files on first use, so that the value of each constant is known.
type switchEnv struct {
	fset *token.FileSet // the file set of file, which the type check needs
	filename string
	file *ast.File
	pkg *types.Package
	info *types.Info
	indexes map[string]map[string]int // by import path; "" for the package of the file
}

// commentsKeepOrder reports whether the comments inside the clauses of list stay in their clauses once the clauses are in that order. The printer writes a comment before the first token that follows it in the source, so a comment is misplaced when a clause printed before its clause comes later in the source.
func commentsKeepOrder(list []ast.Stmt, comments []*ast.CommentGroup) bool {
	for p, s := range list {
		cl := s.(*ast.CaseClause)
		if !commentIn(comments, cl.Pos(), cl.End()) {
			continue
		}

		for _, before := range list[:p] {
			if before.Pos() > cl.Pos() {
				return false
			}
		}
	}

	return true
}

// concreteType reports whether the type expression e names a type that is not an interface, so that no two of the types of a type switch can both match a value. A named type is looked up in types; a type that cannot be found is not concrete.
func concreteType(e ast.Expr, types map[string]ast.Expr) bool {
	for range 8 {
		switch x := e.(type) {
		case *ast.ArrayType, *ast.ChanType, *ast.FuncType, *ast.MapType, *ast.StarExpr, *ast.StructType:
			return true
		case *ast.Ident:
			if predeclaredPure[x.Name] {
				return true
			}

			next, ok := types[x.Name]
			if !ok {
				return false
			}

			e = next
		case *ast.ParenExpr:
			e = x.X
		default:
			return false
		}
	}

	return false
}

// defaultLast moves the default clause of body to the end of the switch. The default runs only when no case matches, wherever it is written, so the move changes nothing. It is left in place when a fallthrough would change: when the default ends in one, or when the clause before it does, since that clause would then run a different clause.
func defaultLast(body *ast.BlockStmt, comments []*ast.CommentGroup) {
	at := -1

	for i, cl := range body.List {
		if cl.(*ast.CaseClause).List == nil {
			at = i
		}
	}

	if at < 0 || at == len(body.List)-1 || !clausesHoldComments(body, comments) {
		return
	}

	if endsInFallthrough(body.List[at].(*ast.CaseClause).Body) || at > 0 && endsInFallthrough(body.List[at-1].(*ast.CaseClause).Body) {
		return
	}

	list := slices.Clone(body.List)
	list = append(append(list[:at], list[at+1:]...), body.List[at])

	if !commentsKeepOrder(list, comments) {
		return
	}

	body.List = list
}

// exprText returns the source text of an expression, as the printer writes it.
func exprText(e ast.Expr) string {
	var buf bytes.Buffer

	cfg := printer.Config{
		Mode: printer.RawFormat,
	}

	if err := cfg.Fprint(&buf, token.NewFileSet(), e); err != nil {
		return ""
	}

	return buf.String()
}

// indexConsts returns the position of each constant of files, by name: the constants are numbered in the order of the files, and of the declarations within each file. A name declared twice keeps its first position.
func indexConsts(files []*ast.File) map[string]int {
	index := map[string]int{}

	for _, f := range files {
		for _, d := range f.Decls {
			g, ok := d.(*ast.GenDecl)
			if !ok || g.Tok != token.CONST {
				continue
			}

			for _, sp := range g.Specs {
				for _, id := range sp.(*ast.ValueSpec).Names {
					if _, dup := index[id.Name]; !dup {
						index[id.Name] = len(index)
					}
				}
			}
		}
	}

	return index
}

// importedConstIndex returns indexConsts of the package with import path path, as seen from the file named filename: the files that build here. A package that cannot be found has no constants.
func importedConstIndex(path, filename string) map[string]int {
	dir := packageDir(path, filepath.Dir(filename))
	if dir == "" {
		return nil
	}

	pkg, err := build.Default.ImportDir(dir, 0)
	if err != nil {
		return nil
	}

	var files []*ast.File

	for _, name := range pkg.GoFiles {
		if f := parsedSibling(filepath.Join(dir, name)); f != nil {
			files = append(files, f)
		}
	}

	return indexConsts(files)
}

// onDisk reports whether filename names a file that exists. A file read from standard input has a placeholder name, and its directory is not its package.
func onDisk(filename string) bool {
	_, err := os.Stat(filename)

	return err == nil
}

// packageFiles returns the files of the package of the file named filename, in the order of the directory, by name: file itself, and the other files of its directory with the same package name, which parse reads. A file read from standard input has no siblings, so only file is returned.
func packageFiles(filename string, file *ast.File, parse func(path string) *ast.File) []*ast.File {
	if !onDisk(filename) {
		return []*ast.File{file}
	}

	dir := filepath.Dir(filename)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []*ast.File{file}
	}

	self := filepath.Base(filename)

	var files []*ast.File

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			continue
		}

		if name == self {
			files = append(files, file)
			continue
		}

		f := parse(filepath.Join(dir, name))
		if f == nil || f.Name.Name != file.Name.Name {
			continue
		}

		files = append(files, f)
	}

	return files
}

// packageTypeExprs returns the named types of the file and of the other files of its package, by name, as their type expressions.
func packageTypeExprs(filename string, file *ast.File) map[string]ast.Expr {
	dir := filepath.Dir(filename)

	if !onDisk(filename) {
		dir = ""
	}

	types := packageTypes(dir, filepath.Base(filename), file.Name.Name)

	for _, d := range file.Decls {
		if g, ok := d.(*ast.GenDecl); ok && g.Tok == token.TYPE {
			for _, sp := range g.Specs {
				ts := sp.(*ast.TypeSpec)
				types[ts.Name.Name] = ts.Type
			}
		}
	}

	return types
}

// reorderTypeSwitchCases orders the non-default clauses of a type switch by their types, alphabetically, if every type is concrete (see concreteType). The clause bodies do not change. A type switch cannot fall through, and a break leaves the switch whichever clause it is in, so the bodies need not return. A clause with several types is ordered by its first type in alphabetical order. The default clause is last (see defaultLast).
func reorderTypeSwitchCases(body *ast.BlockStmt, types map[string]ast.Expr, comments []*ast.CommentGroup) {
	keys := map[*ast.CaseClause]string{}

	var slots []int

	for i, cl := range body.List {
		c := cl.(*ast.CaseClause)
		if c.List == nil {
			continue
		}

		var key string

		for _, e := range c.List {
			if !concreteType(e, types) {
				return
			}

			if text := exprText(e); key == "" || text < key {
				key = text
			}
		}

		keys[c] = key
		slots = append(slots, i)
	}

	if len(slots) < 2 {
		return
	}

	for _, cg := range comments {
		if cg.Pos() >= body.Lbrace && cg.Pos() < body.Rbrace {
			return
		}
	}

	clauses := make([]*ast.CaseClause, len(slots))

	for k, i := range slots {
		clauses[k] = body.List[i].(*ast.CaseClause)
	}
	slices.SortStableFunc(clauses, func(a, b *ast.CaseClause) int {
		return strings.Compare(keys[a], keys[b])
	})

	list := slices.Clone(body.List)

	for k, i := range slots {
		list[i] = clauses[k]
	}

	body.List = list
}

// check type-checks the package of the file, once. Only the uses of names are recorded, since that is all the case order needs.
func (env *switchEnv) check() {
	if env.info != nil {
		return
	}

	env.info = &types.Info{
		Uses: map[*ast.Ident]types.Object{},
	}
	conf := types.Config{
		Importer: sharedImporter,
		Error: func(error) {},
	}

	// the siblings are parsed into the file set of the file: the checker finds positions through it
	parse := func(path string) *ast.File {
		f, err := parser.ParseFile(env.fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil
		}

		return f
	}
	env.pkg, _ = conf.Check(env.file.Name.Name, env.fset, packageFiles(env.filename, env.file, parse), env.info)
}

// constOf returns the constant that the case expression e names: an identifier, or a selector pkg.Name of an imported package. path is "" for a constant of the package of the file, and the import path of the other package otherwise. ok is false when e names anything else, or a constant that is not declared at package level, such as one in a function.
func (env *switchEnv) constOf(e ast.Expr) (path string, c *types.Const, ok bool) {
	env.check()

	var obj types.Object

	switch x := e.(type) {
	case *ast.Ident:
		obj = env.info.Uses[x]
	case *ast.SelectorExpr:
		id, isIdent := x.X.(*ast.Ident)
		if !isIdent {
			return "", nil, false
		}

		if _, isPkg := env.info.Uses[id].(*types.PkgName); !isPkg {
			return "", nil, false
		}

		obj = env.info.Uses[x.Sel]
	}

	c, ok = obj.(*types.Const)

	if !ok || c.Pkg() == nil || c.Parent() != c.Pkg().Scope() {
		return "", nil, false
	}

	if c.Pkg() == env.pkg {
		return "", c, true
	}

	return c.Pkg().Path(), c, true
}

// index returns the constant positions of the package with import path path (see constOf).
func (env *switchEnv) index(path string) map[string]int {
	if idx, ok := env.indexes[path]; ok {
		return idx
	}

	var idx map[string]int

	if path == "" {
		idx = indexConsts(packageFiles(env.filename, env.file, parsedSibling))
	} else {
		idx = importedConstIndex(path, env.filename)
	}

	env.indexes[path] = idx

	return idx
}

// reorderSwitchCases orders the non-default clauses of body by the declaration index of their constants, if the switch qualifies (see switchCaseOrder).
func reorderSwitchCases(body *ast.BlockStmt, env *switchEnv, comments []*ast.CommentGroup) {
	// the key of a clause: the smallest index among its cases
	keys := map[*ast.CaseClause]int{}

	var (
		slots []int // the positions of the non-default clauses in body.List
		path string // the package that every case names a constant of
		index map[string]int
		named bool
		values = map[string]bool{} // the values of the cases, which must be distinct
	)

	for i, cl := range body.List {
		c := cl.(*ast.CaseClause)
		// a clause that falls through would run the next clause in the source, which moves
		if endsInFallthrough(c.Body) {
			return
		}

		if c.List == nil {
			continue // the default clause is last (see defaultLast)
		}

		key := -1

		for _, e := range c.List {
			p, obj, ok := env.constOf(e)
			if !ok {
				return
			}

			if !named {
				path, index, named = p, env.index(p), true
			} else if p != path {
				return
			}

			k, ok := index[obj.Name()]
			if !ok {
				return
			}

			// two cases with one value would match in the order they are written, so they keep it
			val := obj.Val().ExactString()
			if values[val] {
				return
			}

			values[val] = true

			if key < 0 || k < key {
				key = k
			}
		}

		keys[c] = key
		slots = append(slots, i)
	}

	// a comment between clauses, or after a clause's last statement, would stay behind; one inside a clause is checked against the new order
	if len(slots) < 2 || !clausesHoldComments(body, comments) {
		return
	}

	clauses := make([]*ast.CaseClause, len(slots))

	for k, i := range slots {
		clauses[k] = body.List[i].(*ast.CaseClause)
	}
	slices.SortStableFunc(clauses, func(a, b *ast.CaseClause) int {
		return keys[a] - keys[b]
	})

	list := slices.Clone(body.List)

	for k, i := range slots {
		list[i] = clauses[k]
	}

	if !commentsKeepOrder(list, comments) {
		return
	}

	body.List = list
}

// This file implements the case order of -strict: the clauses of a switch whose cases are constants are put in the order the constants are declared in, typically the order of an iota block. The constants may be those of the package of the file, or of another package, such as token.CONST from go/token. A clause moves only among the slots of the other clauses of its kind; the default clause goes last (see defaultLast). The switch is changed only if its behaviour cannot change: no clause falls through, every case is a package-level constant with a value of its own, and all cases name constants of one package. Constants are not evaluated, so moving clauses changes nothing else.

// switchCaseOrder reorders the clauses of the switch statements of file, whose name is filename and whose positions are in fset. The constants of a package count: those of the file and of the other files of its directory with the same package name, and those of the packages the file imports.
func switchCaseOrder(fset *token.FileSet, filename string, file *ast.File) {
	env := &switchEnv{
		fset: fset,
		filename: filename,
		file: file,
		indexes: map[string]map[string]int{},
	}
	types := packageTypeExprs(filename, file)

	ast.Inspect(file, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.SwitchStmt:
			defaultLast(s.Body, file.Comments)
			reorderSwitchCases(s.Body, env, file.Comments)
		case *ast.TypeSwitchStmt:
			defaultLast(s.Body, file.Comments)
			reorderTypeSwitchCases(s.Body, types, file.Comments)
		}

		return true
	})
}
