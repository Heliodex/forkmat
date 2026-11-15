package main

import (
	"go/ast"
	"go/build"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
)

type (
	// lockedImporter serializes the calls of an importer, and remembers the packages it
	// has imported: a source importer otherwise looks every import up again, which
	// means reading the files of the package each time.
	lockedImporter struct {
		mu sync.Mutex
		imp types.ImporterFrom
		done map[string]*types.Package
		failed map[string]error
	}

	// reentrantImporter imports the packages that a package loaded by checkPackageDir imports, for code in ctx. The lock of lockedImporter is already held there, so this does not take it.
	reentrantImporter struct {
		l *lockedImporter
		ctx string
	}
)

// The source importer runs the cgo tool for a package with cgo files, such as net, which costs about a second for each. The pure Go files of those packages declare the same API, so the checks read those instead. Set before the importer reads build.Default.
func init() {
	build.Default.CgoEnabled = false
}

// checkTypes type-checks file on its own and returns the types of its expressions.
// Errors are expected for names declared elsewhere, and are ignored: an expression
// whose type cannot be found has no entry.
func checkTypes(fset *token.FileSet, file *ast.File) *types.Info {
	info := &types.Info{
		Types: map[ast.Expr]types.TypeAndValue{},
	}
	conf := types.Config{
		Importer: sharedImporter,
		Error: func(error) {},
	}

	conf.Check(file.Name.Name, fset, []*ast.File{file}, info)
	return info
}

// sharedImporter imports packages from source, once for the whole run: a source
// importer keeps the packages it has read, so one instance serves every file. It is
// not safe for concurrent use, so each import holds the lock.
var sharedImporter types.ImporterFrom = &lockedImporter{
	imp: importer.ForCompiler(token.NewFileSet(), "source", nil).(types.ImporterFrom),
}

// hasSelector reports whether an expression holds a selector, such as pkg.T.
func hasSelector(e ast.Expr) bool {
	var found bool

	ast.Inspect(e, func(n ast.Node) bool {
		if _, ok := n.(*ast.SelectorExpr); ok {
			found = true
		}

		return !found
	})
	return found
}

// isUntyped reports whether t is an untyped basic type, such as an untyped boolean from a comparison.
func isUntyped(t types.Type) bool {
	b, ok := t.(*types.Basic)

	return ok && b.Info()&types.IsUntyped != 0
}

// callExprPtrType is the type of a *ast.CallExpr.
var callExprPtrType = reflect.TypeFor[*ast.CallExpr]()

// checkPackageDir type-checks the package with import path path, in pdir, from its source. Only its declarations are needed to import it, so the function bodies are skipped, which is much faster. Its own imports are loaded the same way, for code in ctx: the directory of the code that first imported it, since the packages of that module are the ones listed. The result is false, and nothing is checked, for a package with cgo files: those need the cgo tool, which the source importer runs.
func (l *lockedImporter) checkPackageDir(path, pdir, ctx string) (*types.Package, bool, error) {
	bp, err := build.Default.ImportDir(pdir, 0)
	if err != nil {
		return nil, true, err
	}

	if len(bp.CgoFiles) > 0 {
		return nil, false, nil
	}

	fset := token.NewFileSet()

	var files []*ast.File

	for _, name := range bp.GoFiles {
		f, err := parser.ParseFile(fset, filepath.Join(pdir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, true, err
		}

		files = append(files, f)
	}

	// an error in the declarations means the package is not imported at all, as the source importer does
	var firstErr error

	conf := types.Config{
		Importer: reentrantImporter{
			l: l,
			ctx: ctx,
		},
		IgnoreFuncBodies: true,
		Error: func(err error) {
			if firstErr == nil {
				firstErr = err
			}
		},
	}
	pkg, _ := conf.Check(path, fset, files, nil)

	if firstErr != nil {
		return nil, true, firstErr
	}

	return pkg, true, nil
}

// ImportFrom implements types.ImporterFrom.
func (l *lockedImporter) ImportFrom(path, dir string, mode types.ImportMode) (*types.Package, error) {
	l.mu.Lock()

	defer l.mu.Unlock()

	return l.importPkg(path, dir)
}

// importPkg imports the package with import path path, for code in the directory dir. It must be called with the lock held: a package's own imports are loaded through it too. Packages are remembered, failures as well, so a package that cannot be read is not looked up again for every file that imports it. The memory is kept per module: a path can name different packages in nested modules of one tree, so the first file to import a path must not decide for the others.
func (l *lockedImporter) importPkg(path, dir string) (*types.Package, error) {
	root, _ := enclosingModule(dir)
	key := root + "\x00" + path

	if pkg, ok := l.done[key]; ok {
		return pkg, nil
	}

	if err, ok := l.failed[key]; ok {
		return nil, err
	}

	pkg, err := l.load(path, dir)
	if err != nil {
		if l.failed == nil {
			l.failed = map[string]error{}
		}

		l.failed[key] = err

		return nil, err
	}

	if l.done == nil {
		l.done = map[string]*types.Package{}
	}

	l.done[key] = pkg

	return pkg, nil
}

// load imports the package with import path path, for code in dir, from its source if it is outside the standard library (see checkPackageDir). The standard library, and any package with cgo files, go to the source importer.
func (l *lockedImporter) load(path, dir string) (*types.Package, error) {
	if pdir := packageDir(path, dir); pdir != "" && !strings.HasPrefix(pdir, filepath.Join(build.Default.GOROOT, "src")+string(filepath.Separator)) {
		if pkg, handled, err := l.checkPackageDir(path, pdir, dir); handled {
			return pkg, err
		}
	}

	return l.imp.ImportFrom(path, dir, 0)
}

// ImportFrom implements types.ImporterFrom. The directory types passes is that of the package being checked, which is not where its module's packages are listed, so ctx is used instead.
func (r reentrantImporter) ImportFrom(path, _ string, mode types.ImportMode) (*types.Package, error) {
	return r.l.importPkg(path, r.ctx)
}

// Import implements types.Importer.
func (l *lockedImporter) Import(path string) (*types.Package, error) {
	return l.ImportFrom(path, "", 0)
}

// operand returns the expression that stands in for a conversion T(x) once it is removed: x itself, in parentheses when its operators would bind differently in the place of the conversion. The parentheses are dropped again by the printer under -strict when they are not needed.
func operand(x ast.Expr) ast.Expr {
	switch x.(type) {
	case *ast.BasicLit, *ast.CallExpr, *ast.CompositeLit, *ast.Ident, *ast.IndexExpr, *ast.IndexListExpr, *ast.ParenExpr, *ast.SelectorExpr, *ast.SliceExpr, *ast.TypeAssertExpr:
		return x
	}

	// the parentheses take the operand's position: without one, the printer takes the call's arguments to be on another line, and lays them out so
	return &ast.ParenExpr{
		Lparen: x.Pos(),
		X: x,
		Rparen: x.End(),
	}
}

// Import implements types.Importer.
func (r reentrantImporter) Import(path string) (*types.Package, error) {
	return r.l.importPkg(path, r.ctx)
}

// This file implements -unconvert: a conversion T(x) is removed when x already has the type T. The types come from type-checking the file on its own. Names that cannot be resolved from this file (declared in another file, or in a package that cannot be imported) have no type, so a conversion of them is kept: the check only removes what it can prove redundant.
//
// A conversion of a constant is kept. Removing int64(5) would change the type of the constant from int64 to its default. A conversion whose type names a member of an imported package is kept too, since removing it could leave the import unused.

// unconvert returns file with its redundant conversions removed. file must be parsed with comments; it is type-checked as a package of its own.
func unconvert(fset *token.FileSet, file *ast.File, info *types.Info) *ast.File {
	repl := map[*ast.CallExpr]ast.Expr{}

	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || call.Ellipsis.IsValid() || len(call.Args) != 1 || hasSelector(call.Fun) {
			return true
		}

		if tv, ok := info.Types[call.Fun]; !ok || !tv.IsType() {
			return true
		}

		arg := call.Args[0]
		av := info.Types[arg]
		if target := info.Types[call.Fun].Type; av.Type == nil || av.Value != nil || isUntyped(av.Type) || !types.Identical(target, av.Type) {
			return true
		}

		repl[call] = operand(arg)

		return true
	})
	if len(repl) == 0 {
		return file
	}

	var rewriteVal func(val reflect.Value) reflect.Value

	rewriteVal = func(val reflect.Value) reflect.Value {
		if val.IsValid() && val.Type() == callExprPtrType {
			if r, ok := repl[val.Interface().(*ast.CallExpr)]; ok {
				// the replacement may hold further conversions to remove
				return rewriteVal(reflect.ValueOf(r))
			}
		}

		return apply(rewriteVal, val)
	}

	return rewriteVal(reflect.ValueOf(file)).Interface().(*ast.File)
}
