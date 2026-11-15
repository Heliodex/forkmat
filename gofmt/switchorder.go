package main

import (
	"bytes"
	"forkmat/go/printer"
	"go/ast"
	"go/build"
	"go/constant"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
)

type (
	// placeholderImporter gives an empty package for each import. A constant that depends on an import then has no value, so the switch keeps its order (see constOf); loading the imports in full would cost far more than the constants are worth to the check.
	placeholderImporter struct{}

	// switchEnv gives the constants that the cases of a switch may name: those of the package of the file, and those of the packages it imports. The constants are read from the package's declarations, which are checked once per directory (see packageOf).
	switchEnv struct {
		filename string
		file *ast.File
		pkg *types.Package // the package of the file, once checked (see pkgOf)
		checked bool
		imports map[string]*types.Package // the packages the file imports, by the name they are referred to with
		scopes map[ast.Node]map[string]bool // the names each scope declares (see declaredNames)
		indexes map[string]map[string]int // by import path; "" for the package of the file
	}
)

var (

	// checkedPackages remembers the packages that packageOf has checked, by directory and package name.
	checkedPackages = struct {
		sync.Mutex
		pkgs map[string]*types.Package
	}{
		pkgs: map[string]*types.Package{},
	}

	// importedIndexes remembers importedConstIndex, by the directory of the package.
	importedIndexes = struct {
		sync.Mutex
		m map[string]map[string]int
	}{
		m: map[string]map[string]int{},
	}

	// packageFileNames remembers packageNames, by directory and package name.
	packageFileNames = struct {
		sync.Mutex
		names map[string][]string
	}{
		names: map[string][]string{},
	}

	// placeholderPackages remembers placeholderPackage, by directory.
	placeholderPackages = struct {
		sync.Mutex
		pkgs map[string]*types.Package
	}{
		pkgs: map[string]*types.Package{},
	}

	// orderedFiles remembers orderedSibling, by path.
	orderedFiles = struct {
		sync.Mutex
		files map[string]*ast.File
	}{
		files: map[string]*ast.File{},
	}
)

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

// declaredNames returns the names that a scope declares. For a function, these are its parameters, results and every name declared in its body. Any other declaration is a scope only for the function literals it holds, since its own names are package-level.
func declaredNames(scope ast.Node) map[string]bool {
	names := map[string]bool{}
	add := func(n ast.Node) {
		ast.Inspect(n, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.AssignStmt:
				if x.Tok == token.DEFINE {
					for _, e := range x.Lhs {
						if id, ok := e.(*ast.Ident); ok {
							names[id.Name] = true
						}
					}
				}
			case *ast.Field:
				for _, id := range x.Names {
					names[id.Name] = true
				}
			case *ast.RangeStmt:
				if x.Tok == token.DEFINE {
					for _, e := range []ast.Expr{x.Key, x.Value} {
						if id, ok := e.(*ast.Ident); ok {
							names[id.Name] = true
						}
					}
				}
			case *ast.TypeSpec:
				names[x.Name.Name] = true
			case *ast.ValueSpec:
				for _, id := range x.Names {
					names[id.Name] = true
				}
			}

			return true
		})
	}

	if _, ok := scope.(*ast.FuncDecl); ok {
		add(scope)
	} else {
		ast.Inspect(scope, func(n ast.Node) bool {
			if fl, ok := n.(*ast.FuncLit); ok {
				add(fl)
			}

			return true
		})
	}

	return names
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

// likelyName reports whether a package imported as path is probably referred to as name, when the import has no name of its own: its last element, skipping a major version such as v2.
func likelyName(path, name string) bool {
	elems := strings.Split(path, "/")
	last := elems[len(elems)-1]
	if len(elems) > 1 && len(last) > 1 && last[0] == 'v' && strings.Trim(last[1:], "0123456789") == "" {
		last = elems[len(elems)-2]
	}

	return last == name
}

// onDisk reports whether filename names a file that exists. A file read from standard input has a placeholder name, and its directory is not its package.
func onDisk(filename string) bool {
	_, err := os.Stat(filename)

	return err == nil
}

// orderedSibling returns the file at path as -strict leaves its declarations: with the reorder passes applied to it (see reorderSource). The constants of a package are numbered from these files, since the passes change their order, for instance by sorting a run of constants. Numbering them from the source on disk would number a sibling differently before and after it is formatted, and the switches that use its constants would change on every run. The result is nil if the file cannot be read or parsed.
func orderedSibling(path string) *ast.File {
	orderedFiles.Lock()

	defer orderedFiles.Unlock()

	if f, ok := orderedFiles.files[path]; ok {
		return f
	}

	var f *ast.File

	if src, err := os.ReadFile(path); err == nil {
		fset := token.NewFileSet()

		if out, changed := reorderSource(fset, path, src); changed != nil {
			f = changed
		} else if parsed, err := parser.ParseFile(fset, path, out, parser.ParseComments); err == nil {
			f = parsed
		}
	}

	orderedFiles.files[path] = f

	return f
}

// packageNames returns the names of the .go files of the package pkg in dir, by name, as a build of the directory would take them: a file that its build constraints exclude is left out. A directory whose files do not build as one package lists every file of pkg instead.
func packageNames(dir, pkg string) []string {
	key := dir + "\x00" + pkg

	packageFileNames.Lock()

	defer packageFileNames.Unlock()

	if names, ok := packageFileNames.names[key]; ok {
		return names
	}

	var names []string

	if bp, err := build.Default.ImportDir(dir, 0); err == nil && bp.Name == pkg {
		names = bp.GoFiles
	} else if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
				continue
			}

			if f := parsedSibling(filepath.Join(dir, name)); f != nil && f.Name.Name == pkg {
				names = append(names, name)
			}
		}
	}

	packageFileNames.names[key] = names

	return names
}

// packagePaths returns the .go files of the package of the file named filename, in the order of the directory, by name: the file itself, if it takes part in the build, and the other files of its package (see packageNames). A file read from standard input has no paths.
func packagePaths(filename, pkg string) []string {
	if !onDisk(filename) {
		return nil
	}

	dir := filepath.Dir(filename)

	var paths []string

	for _, name := range packageNames(dir, pkg) {
		if name == filepath.Base(filename) {
			paths = append(paths, filename)
		} else {
			paths = append(paths, filepath.Join(dir, name))
		}
	}

	return paths
}

// packageConstFiles returns the files of the package of file, for the constant positions (see indexConsts): the file as it is now, and the siblings as they are on disk.
func packageConstFiles(filename string, file *ast.File) []*ast.File {
	if !onDisk(filename) {
		return []*ast.File{file}
	}

	var files []*ast.File

	for _, path := range packagePaths(filename, file.Name.Name) {
		if path == filename {
			files = append(files, file)
		} else if f := orderedSibling(path); f != nil {
			files = append(files, f)
		}
	}

	return files
}

// packageOf returns the package of file, checked from its source with the function bodies skipped: the constants and the types are all a switch needs. Each directory is checked once, however many of its files have switches. A file read from standard input is checked alone.
func packageOf(filename string, file *ast.File) *types.Package {
	var key string

	if onDisk(filename) {
		key = filepath.Dir(filename) + "\x00" + file.Name.Name
	}
	checkedPackages.Lock()

	defer checkedPackages.Unlock()

	if pkg, ok := checkedPackages.pkgs[key]; ok && key != "" {
		return pkg
	}

	// the files are parsed into one file set, which the checker needs
	fset := token.NewFileSet()

	var files []*ast.File

	if key == "" {
		// the file keeps its positions: it is registered with the base it was parsed with
		fset.AddFile(filename, int(file.FileStart), int(file.FileEnd-file.FileStart))

		files = []*ast.File{file}
	} else {
		for _, path := range packagePaths(filename, file.Name.Name) {
			if f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution); err == nil {
				files = append(files, f)
			}
		}
	}

	conf := types.Config{
		Importer: placeholderImporter{},
		IgnoreFuncBodies: true,
		Error: func(error) {},
	}
	pkg, _ := conf.Check(file.Name.Name, fset, files, nil)

	if key != "" {
		checkedPackages.pkgs[key] = pkg
	}

	return pkg
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

// Import implements types.Importer.
func (placeholderImporter) Import(path string) (*types.Package, error) {
	pkg := types.NewPackage(path, path[strings.LastIndex(path, "/")+1:])

	pkg.MarkComplete()
	return pkg, nil
}

// placeholderPackage checks the package with import path path, in dir, with placeholderImporter: its constants have the values they have in its own files, and the ones that depend on another package have none. It is remembered for the directory, and is nil for a package that cannot be read.
func placeholderPackage(path, dir string) *types.Package {
	if dir == "" {
		return nil
	}
	placeholderPackages.Lock()

	defer placeholderPackages.Unlock()

	if pkg, ok := placeholderPackages.pkgs[dir]; ok {
		return pkg
	}

	var pkg *types.Package

	if bp, err := build.Default.ImportDir(dir, 0); err == nil {
		fset := token.NewFileSet()

		var files []*ast.File

		for _, name := range bp.GoFiles {
			if f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution); err == nil {
				files = append(files, f)
			}
		}

		conf := types.Config{
			Importer: placeholderImporter{},
			IgnoreFuncBodies: true,
			Error: func(error) {},
		}
		pkg, _ = conf.Check(path, fset, files, nil)
	}

	placeholderPackages.pkgs[dir] = pkg

	return pkg
}

// readImportedConstIndex is importedConstIndex for the package in dir.
func readImportedConstIndex(dir string) map[string]int {
	pkg, err := build.Default.ImportDir(dir, 0)
	if err != nil {
		return nil
	}

	var files []*ast.File

	for _, name := range pkg.GoFiles {
		if f := orderedSibling(filepath.Join(dir, name)); f != nil {
			files = append(files, f)
		}
	}

	return indexConsts(files)
}

// importedConstIndex returns indexConsts of the package with import path path, as seen from the file named filename: the files that build here. A package that cannot be found has no constants. The index is remembered for each directory.
func importedConstIndex(path, filename string) map[string]int {
	dir := packageDir(path, filepath.Dir(filename))
	if dir == "" {
		return nil
	}
	importedIndexes.Lock()

	defer importedIndexes.Unlock()

	if idx, ok := importedIndexes.m[dir]; ok {
		return idx
	}

	idx := readImportedConstIndex(dir)
	importedIndexes.m[dir] = idx

	return idx
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

// declared reports whether scope declares name, so that a use of name in it does not refer to the package's name.
func (env *switchEnv) declared(scope ast.Node, name string) bool {
	names, ok := env.scopes[scope]
	if !ok {
		names = declaredNames(scope)
		env.scopes[scope] = names
	}

	return names[name]
}

// importNamed returns the imported package that the file refers to as name, or nil. A dot import is not found this way, so a constant that it brings in is not a case the switch can order.
func (env *switchEnv) importNamed(name string) *types.Package {
	if pkg, ok := env.imports[name]; ok {
		return pkg
	}

	var found *types.Package

	for _, imp := range env.file.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || imp.Name != nil && (imp.Name.Name == "." || imp.Name.Name == "_") {
			continue
		}

		// an explicit name is the one the file refers to the package with; otherwise the name is the last element of the path, which is the package's name unless that is a major version
		if imp.Name != nil && imp.Name.Name != name || imp.Name == nil && !likelyName(path, name) {
			continue
		}

		// only the packages that could be named are checked, and only for their constants (see placeholderPackage)
		pkg := placeholderPackage(path, packageDir(path, filepath.Dir(env.filename)))
		if pkg == nil {
			continue
		}

		if imp.Name != nil || pkg.Name() == name {
			found = pkg
			break
		}
	}

	env.imports[name] = found

	return found
}

// index returns the constant positions of the package with import path path (see constOf).
func (env *switchEnv) index(path string) map[string]int {
	if idx, ok := env.indexes[path]; ok {
		return idx
	}

	var idx map[string]int

	if path == "" {
		idx = indexConsts(packageConstFiles(env.filename, env.file))
	} else {
		idx = importedConstIndex(path, env.filename)
	}

	env.indexes[path] = idx

	return idx
}

// pkgOf returns the package of the file (see packageOf), which is checked on first use.
func (env *switchEnv) pkgOf() *types.Package {
	if !env.checked {
		env.pkg = packageOf(env.filename, env.file)
		env.checked = true
	}

	return env.pkg
}

// constOf returns the constant that the case expression e names, within scope: an identifier of the package of the file, or a selector pkg.Name of an imported package. path is "" for a constant of the package of the file, and the import path of the other package otherwise. ok is false when e names anything else, a name the scope declares, or a constant that is not declared at package level.
func (env *switchEnv) constOf(e ast.Expr, scope ast.Node) (path string, c *types.Const, ok bool) {
	var (
		pkg *types.Package
		name string
	)

	switch x := e.(type) {
	case *ast.Ident:
		if env.declared(scope, x.Name) {
			return "", nil, false
		}

		pkg, name = env.pkgOf(), x.Name
	case *ast.SelectorExpr:
		id, isIdent := x.X.(*ast.Ident)
		if !isIdent || !ast.IsExported(x.Sel.Name) || env.declared(scope, id.Name) {
			return "", nil, false
		}

		pkg, name = env.importNamed(id.Name), x.Sel.Name
	default:
		return "", nil, false
	}

	if pkg == nil {
		return "", nil, false
	}

	c, ok = pkg.Scope().Lookup(name).(*types.Const)

	if !ok || c.Val().Kind() == constant.Unknown {
		return "", nil, false
	}

	if pkg == env.pkg {
		return "", c, true
	}

	return pkg.Path(), c, true
}

// reorderSwitchCases orders the non-default clauses of body by the declaration index of their constants, if the switch qualifies (see switchCaseOrder).
func reorderSwitchCases(body *ast.BlockStmt, env *switchEnv, scope ast.Node, comments []*ast.CommentGroup) {
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
			p, obj, ok := env.constOf(e, scope)
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

// switchCaseOrder reorders the clauses of the switch statements of file, whose name is filename. The constants of a package count: those of the file and of the other files of its directory with the same package name, and those of the packages the file imports.
func switchCaseOrder(filename string, file *ast.File) {
	env := &switchEnv{
		filename: filename,
		file: file,
		imports: map[string]*types.Package{},
		scopes: map[ast.Node]map[string]bool{},
		indexes: map[string]map[string]int{},
	}
	typeExprs := packageTypeExprs(filename, file)

	// each declaration is the scope of the switches in it, since a case name is looked up in the function that holds the switch
	for _, d := range file.Decls {
		ast.Inspect(d, func(n ast.Node) bool {
			switch s := n.(type) {
			case *ast.SwitchStmt:
				defaultLast(s.Body, file.Comments)
				reorderSwitchCases(s.Body, env, d, file.Comments)
			case *ast.TypeSwitchStmt:
				defaultLast(s.Body, file.Comments)
				reorderTypeSwitchCases(s.Body, typeExprs, file.Comments)
			}

			return true
		})
	}
}
