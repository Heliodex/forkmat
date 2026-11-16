package main

import (
	"errors"
	"go/ast"
	"go/build"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type (
	// depImporter imports packages from source, once per run and module. Each import is a chain of work, started by a call from outside (checkTypes, or a switch check): the packages it loads are part of the chain, and a chain waits for a package that another chain is loading. The waits are followed to find a cycle, which is reported instead of waited for.
	depImporter struct {
		mu sync.Mutex
		entries map[string]*importEntry // by module root and import path
		waits map[int]*importEntry // the entry that each chain is waiting for
		chains int // the chains started so far

		// the source importer, which is not safe for concurrent use
		srcMu sync.Mutex
		src types.ImporterFrom
	}

	// importEntry is the result for one package. It is ready once done is closed; owner is the chain that loads it.
	importEntry struct {
		done chan struct{}
		owner int
		pkg *types.Package
		err error
	}

	// reentrantImporter imports the packages that a package loaded by checkPackageDir imports, for code in ctx and in chain.
	reentrantImporter struct {
		l *depImporter
		ctx string
		chain int
	}
)

var (
	// errImportCycle is what an import reports when it would wait for a package that is waiting for it.
	errImportCycle = errors.New("import cycle")

	// sharedImporter imports the packages of every check of the run (see depImporter).
	sharedImporter types.ImporterFrom = &depImporter{
		entries: map[string]*importEntry{},
		waits: map[int]*importEntry{},
		src: importer.ForCompiler(token.NewFileSet(), "source", nil).(types.ImporterFrom),
	}
)

// This file is the importer of the type checks: -unconvert checks each file against the types of the packages it imports, and -strict checks the package of a switch. A package outside the standard library is read from its source, with the function bodies skipped, which is much faster than a full check, and every package is loaded once per run. Packages load in parallel: a file waits only for the packages it imports, and not for every other file's loads. A package that imports itself, through a cycle that is invalid Go, is reported as a cycle rather than waited for. The standard library, and a package with cgo files, go to the source importer, which is not safe for concurrent use, so it runs one import at a time.

// The source importer runs the cgo tool for a package with cgo files, such as net, which costs about a second for each. The pure Go files of those packages declare the same API, so the checks read those instead. Set before the importer reads build.Default.
func init() {
	build.Default.CgoEnabled = false

	// The standard library is read from the source of the toolchain that runs gofmt, and the go command that lists its packages must not switch to the toolchain that a module names, since the types of one do not check against the other. A GOTOOLCHAIN that the user set is kept.
	if os.Getenv("GOTOOLCHAIN") == "" {
		_ = os.Setenv("GOTOOLCHAIN", "local")
	}
}

// checkPackageDir type-checks the package with import path path, in pdir, from its source. Only its declarations are needed to import it, so the function bodies are skipped, which is much faster. Its own imports are loaded the same way, for code in ctx: the directory of the code that first imported it, since the packages of that module are the ones listed. The result is false, and nothing is checked, for a package with cgo files: those need the cgo tool, which the source importer runs.
func (l *depImporter) checkPackageDir(path, pdir, ctx string, chain int) (*types.Package, bool, error) {
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
			chain: chain,
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

// newChain returns the number of a new chain of work.
func (l *depImporter) newChain() int {
	l.mu.Lock()

	defer l.mu.Unlock()

	l.chains++

	return l.chains
}

// waitsForItself reports whether chain, waiting for e, would wait for itself: e's chain is waiting, directly or through other chains, for a package that chain loads. The caller holds l.mu.
func (l *depImporter) waitsForItself(chain int, e *importEntry) bool {
	for range len(l.waits) + 1 {
		// a finished package is not waited for, even by a chain that has not yet taken its wait down
		select {
		case <-e.done:
			return false
		default:
		}

		if e.owner == chain {
			return true
		}

		next, ok := l.waits[e.owner]
		if !ok {
			return false
		}

		e = next
	}

	return false
}

// ImportFrom implements types.ImporterFrom. It starts a chain of its own.
func (l *depImporter) ImportFrom(path, dir string, _ types.ImportMode) (*types.Package, error) {
	return l.importPkg(path, dir, l.newChain())
}

// importPkg returns the package with import path path, for code in dir, loaded as part of chain. The package is loaded by this chain if no other has started it, and otherwise waited for, unless that would deadlock.
func (l *depImporter) importPkg(path, dir string, chain int) (*types.Package, error) {
	root, _ := enclosingModule(dir)
	key := root + "\x00" + path

	l.mu.Lock()

	e, ok := l.entries[key]
	if !ok {
		e = &importEntry{
			done: make(chan struct{}),
			owner: chain,
		}
		l.entries[key] = e

		l.mu.Unlock()

		e.pkg, e.err = l.load(path, dir, chain)

		close(e.done)

		return e.pkg, e.err
	}

	select {
	case <-e.done:
		l.mu.Unlock()
		return e.pkg, e.err
	default:
	}

	if l.waitsForItself(chain, e) {
		l.mu.Unlock()
		return nil, errImportCycle
	}

	l.waits[chain] = e

	l.mu.Unlock()
	<-e.done
	l.mu.Lock()
	delete(l.waits, chain)
	l.mu.Unlock()

	return e.pkg, e.err
}

// load imports the package with import path path, for code in dir, as part of chain: from its source if it is outside the standard library (see checkPackageDir), and otherwise with the source importer.
func (l *depImporter) load(path, dir string, chain int) (*types.Package, error) {
	if pdir := packageDir(path, dir); pdir != "" && !strings.HasPrefix(pdir, filepath.Join(build.Default.GOROOT, "src")+string(filepath.Separator)) {
		if pkg, handled, err := l.checkPackageDir(path, pdir, dir, chain); handled {
			return pkg, err
		}
	}

	l.srcMu.Lock()

	defer l.srcMu.Unlock()

	return l.src.ImportFrom(path, dir, 0)
}

// ImportFrom implements types.ImporterFrom. The directory types passes is that of the package being checked, which is not where its module's packages are listed, so ctx is used instead.
func (r reentrantImporter) ImportFrom(path, _ string, _ types.ImportMode) (*types.Package, error) {
	return r.l.importPkg(path, r.ctx, r.chain)
}

// Import implements types.Importer.
func (l *depImporter) Import(path string) (*types.Package, error) {
	return l.ImportFrom(path, "", 0)
}

// Import implements types.Importer.
func (r reentrantImporter) Import(path string) (*types.Package, error) {
	return r.l.importPkg(path, r.ctx, r.chain)
}
