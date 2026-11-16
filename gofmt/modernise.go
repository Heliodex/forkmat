package main

import (
	"cmp"
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"go/types"
	"go/version"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/assign"
	"golang.org/x/tools/go/analysis/passes/composite"
	"golang.org/x/tools/go/analysis/passes/hostport"
	"golang.org/x/tools/go/analysis/passes/inline"
	"golang.org/x/tools/go/analysis/passes/modernize"
	"golang.org/x/tools/go/analysis/passes/timeformat"
	"golang.org/x/tools/go/analysis/passes/unreachable"
)

type (
	// analysisResult is the result of an analyzer, or the error that stopped it.
	analysisResult struct {
		value any
		err error
	}

	// factKey identifies an object fact: the analyzer that exports it, the object, and the type of the fact.
	factKey struct {
		analyzer *analysis.Analyzer
		obj types.Object
		typ reflect.Type
	}

	// moderniseRun holds the state shared by the analyzers over one file: the file and its types, the results and facts that the analyzers share, and the diagnostics they report.
	moderniseRun struct {
		fset *token.FileSet
		filename string
		src []byte
		files []*ast.File
		pkg *types.Package
		info *types.Info
		suite []*analysis.Analyzer
		results map[*analysis.Analyzer]analysisResult
		facts map[factKey]analysis.Fact
		diags []analysis.Diagnostic
	}

	// textEdit replaces the bytes from start to end of the source with text. An insertion has start equal to end.
	textEdit struct {
		start, end int
		text []byte
	}
)

// maxModeniseRounds bounds how many rounds of fixes are applied to one file. Fixes that overlap are left for the next round, so a file with many fixes can need several rounds.
const maxModeniseRounds = 50

// This file implements -modernise: the fixes that the analyzers of golang.org/x/tools/go/analysis/passes/modernize suggest are applied to the source text, the way go fix applies them. The analyzers run over the package of the file, with its types from checkModerniseFiles, and only the fixes that edit the file are applied. The fixes of a round are applied together. The file is then parsed again, and the analyzers run again, until no fix applies, since a fix can expose another one.

// vetFixSuite lists the vet analyzers whose fixes -vetfix applies: assign removes self-assignments, hostport uses net.JoinHostPort, timeformat corrects time layouts, and unreachable removes code that cannot run.
var vetFixSuite = []*analysis.Analyzer{assign.Analyzer, hostport.Analyzer, timeformat.Analyzer, unreachable.Analyzer}

// applyFixEdits returns src with the edits made. The edits are sorted by place, then applied from the last, so that the offsets of the edits before each one stay valid. Edits at the same place keep their order: an edit that ends later goes first, and of two insertions at one place the first listed ends up first.
func applyFixEdits(src []byte, edits []textEdit) []byte {
	slices.SortStableFunc(edits, func(a, b textEdit) int {
		return cmp.Or(cmp.Compare(a.start, b.start), cmp.Compare(a.end, b.end))
	})

	out := slices.Clone(src)

	for _, e := range slices.Backward(edits) {
		out = slices.Concat(out[:e.start], e.text, out[e.end:])
	}

	return out
}

// bytesOverlap reports whether two edits change some of the same bytes. An insertion changes no bytes, so it overlaps only an edit that spans its place.
func bytesOverlap(a, b textEdit) bool {
	return a.start < b.end && b.start < a.end
}

// checkModerniseFiles type-checks the files of one package under the Go version of its module, the first file among them. Errors are ignored: an expression whose type cannot be found has no entry.
func checkModerniseFiles(fset *token.FileSet, files []*ast.File, goVersion string) (*types.Package, *types.Info) {
	info := &types.Info{
		Types: map[ast.Expr]types.TypeAndValue{},
		Instances: map[*ast.Ident]types.Instance{},
		Defs: map[*ast.Ident]types.Object{},
		Uses: map[*ast.Ident]types.Object{},
		Implicits: map[ast.Node]types.Object{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
		Scopes: map[ast.Node]*types.Scope{},
		FileVersions: map[*ast.File]string{},
	}
	conf := types.Config{
		Importer: sharedImporter,
		GoVersion: goVersion,
		Error: func(error) {},
	}

	// the path has a dot, so that the analyzers do not take the package for a standard one
	pkg, _ := conf.Check("gofmt.invalid/"+files[0].Name.Name, fset, files, info)

	return pkg, info
}

// checkModernise type-checks the file on its own, under the Go version of its module. Errors are expected for names declared in other files, and are ignored: an expression whose type cannot be found has no entry, and the analyzers leave it alone. The import pass uses it, which needs only the names of the imports.
func checkModernise(fset *token.FileSet, file *ast.File, goVersion string) (*types.Package, *types.Info) {
	return checkModerniseFiles(fset, []*ast.File{file}, goVersion)
}

// enabledSuite returns the analyzers whose fixes the flags enable, in the order they run.
func enabledSuite() (suite []*analysis.Analyzer) {
	if *moderniseFlag {
		suite = append(suite, modernize.Suite...)
	}

	if *vetFixFlag {
		suite = append(suite, vetFixSuite...)
	}

	if *inlineFlag {
		suite = append(suite, inline.Analyzer)
	}

	if *compositeFlag {
		suite = append(suite, composite.Analyzer)
	}

	return
}

// goDirective returns the language version in the go directive of a go.mod file, such as go1.27 for go 1.27.2, or "" if it has no valid one.
func goDirective(gomod []byte) string {
	for line := range strings.Lines(string(gomod)) {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != "go" {
			continue
		}

		major, rest, ok := strings.Cut(fields[1], ".")
		if !ok {
			return ""
		}

		minor, _, _ := strings.Cut(rest, ".")

		if v := "go" + major + "." + minor; version.IsValid(v) {
			return v
		}

		return ""
	}

	return ""
}

// importFact copies the fact that analyzer a exported for obj into fact, and reports whether there was one.
func (r *moderniseRun) importFact(a *analysis.Analyzer, obj types.Object, fact analysis.Fact) bool {
	stored, ok := r.facts[factKey{a, obj, reflect.TypeOf(fact)}]
	if !ok {
		return false
	}

	reflect.ValueOf(fact).Elem().Set(reflect.ValueOf(stored).Elem())

	return true
}

// inFile reports whether pos is in the file being fixed. The analyzers run over the whole package, so a fix may edit a sibling file; that fix is not applied.
func (r *moderniseRun) inFile(pos token.Pos) bool {
	f := r.fset.File(pos)

	return f != nil && f.Name() == r.filename
}

// editsOf returns the edits of a suggested fix, as offsets into the source.
func (r *moderniseRun) editsOf(fix analysis.SuggestedFix) ([]textEdit, bool) {
	var edits []textEdit

	for _, te := range fix.TextEdits {
		if !r.inFile(te.Pos) || te.End.IsValid() && !r.inFile(te.End) {
			return nil, false
		}

		start := r.fset.Position(te.Pos).Offset
		end := start

		if te.End.IsValid() {
			end = r.fset.Position(te.End).Offset
		}

		edits = append(edits, textEdit{
			start: start,
			end: end,
			text: te.NewText,
		})
	}

	return edits, true
}

// readFile reads a file for the analyzers. The file being fixed is read from the source in memory, which may differ from the file on disk.
func (r *moderniseRun) readFile(name string) ([]byte, error) {
	if name == r.filename {
		return r.src, nil
	}

	return os.ReadFile(name)
}

// pass returns the pass that runs analyzer a, with the results of the analyzers it requires in resultOf.
func (r *moderniseRun) pass(a *analysis.Analyzer, resultOf map[*analysis.Analyzer]any) *analysis.Pass {
	return &analysis.Pass{
		Analyzer: a,
		Fset: r.fset,
		Files: r.files,
		Pkg: r.pkg,
		TypesInfo: r.info,
		TypesSizes: types.SizesFor("gc", runtime.GOARCH),
		ResultOf: resultOf,
		ReadFile: r.readFile,
		Report: func(d analysis.Diagnostic) {
			r.diags = append(r.diags, d)
		},
		ImportObjectFact: func(obj types.Object, fact analysis.Fact) bool {
			return r.importFact(a, obj, fact)
		},
		ExportObjectFact: func(obj types.Object, fact analysis.Fact) {
			r.facts[factKey{a, obj, reflect.TypeOf(fact)}] = fact
		},
	}
}

// runAnalyzer runs analyzer a, with the results of the analyzers it requires in resultOf, and returns its result. A panic in an analyzer is an error, since the file need not type-check fully; the diagnostics of an analyzer that fails are dropped.
func (r *moderniseRun) runAnalyzer(a *analysis.Analyzer, resultOf map[*analysis.Analyzer]any) (res analysisResult) {
	mark := len(r.diags)

	defer func() {
		if p := recover(); p != nil {
			res = analysisResult{
				err: fmt.Errorf("%s: %v", a.Name, p),
			}
		}

		if res.err != nil {
			r.diags = r.diags[:mark]
		}
	}()

	value, err := a.Run(r.pass(a, resultOf))

	return analysisResult{
		value: value,
		err: err,
	}
}

// analyze returns the result of analyzer a, running it, and the analyzers it requires, the first time it is asked for. An analyzer whose requirement fails has no result either.
func (r *moderniseRun) analyze(a *analysis.Analyzer) analysisResult {
	if res, done := r.results[a]; done {
		return res
	}

	resultOf := make(map[*analysis.Analyzer]any, len(a.Requires))

	for _, req := range a.Requires {
		required := r.analyze(req)
		if required.err != nil {
			res := analysisResult{
				err: required.err,
			}
			r.results[a] = res

			return res
		}

		resultOf[req] = required.value
	}

	res := r.runAnalyzer(a, resultOf)
	r.results[a] = res

	return res
}

// fixes runs the modernize suite over the file, and returns the edits of the first suggested fix of each diagnostic, in the order the analyzers report them.
func (r *moderniseRun) fixes() (fixes [][]textEdit) {
	for _, a := range r.suite {
		r.analyze(a)
	}

	for _, d := range r.diags {
		if len(d.SuggestedFixes) == 0 {
			continue
		}

		if edits, ok := r.editsOf(d.SuggestedFixes[0]); ok {
			fixes = append(fixes, edits)
		}
	}

	return
}

// moduleGoVersion returns the language version, such as go1.27, that the go.mod governing filename names, or "" if no go.mod governs it. A file outside any module has no version to hold its fixes to.
func moduleGoVersion(filename string) string {
	dir, err := filepath.Abs(filepath.Dir(filename))
	if err != nil {
		return ""
	}

	for {
		if data, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil {
			return goDirective(data)
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}

		dir = parent
	}
}

// overlaps reports whether two edits from different fixes conflict: they change the same bytes, or insert at the same place.
func overlaps(a, b textEdit) bool {
	return a.start == b.start || bytesOverlap(a, b)
}

// fixOverlaps reports whether the edits of a fix overlap the accepted edits, or change the same bytes as each other. The edits of one fix may share a place, such as a deletion and an insertion at its start, since they are applied together.
func fixOverlaps(fix, accepted []textEdit) bool {
	for i, e := range fix {
		for _, other := range accepted {
			if overlaps(e, other) {
				return true
			}
		}

		for _, other := range fix[:i] {
			if bytesOverlap(e, other) {
				return true
			}
		}
	}

	return false
}

// packageFiles parses the package of the file being fixed into fset: the file itself, as src gives it, and its siblings as they are on disk. It reports false when the package cannot be taken as a whole, such as a file read from standard input, a directory that does not build as one package, or a package that uses cgo. Types declared in another file of a package are otherwise undefined, and the analyzers can then take two different types for the same one (see moderniseRun).
func packageFiles(fset *token.FileSet, filename string, file *ast.File) ([]*ast.File, bool) {
	if !onDisk(filename) {
		return nil, false
	}

	dir := filepath.Dir(filename)
	bp, err := build.Default.ImportDir(dir, 0)

	if err != nil || bp.Name != file.Name.Name || len(bp.CgoFiles) > 0 {
		return nil, false
	}

	files := []*ast.File{file}

	for _, name := range bp.GoFiles {
		if name == filepath.Base(filename) {
			continue
		}

		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.ParseComments)
		if err != nil {
			return nil, false
		}

		files = append(files, f)
	}

	return files, true
}

// selectFixes returns the edits of the fixes that do not overlap an earlier fix. A fix that overlaps one is left for a later round, once the source has changed.
func selectFixes(fixes [][]textEdit) (accepted []textEdit) {
	for _, fix := range fixes {
		if !fixOverlaps(fix, accepted) {
			accepted = append(accepted, fix...)
		}
	}

	return
}

// moderniseOnce applies one round of fixes to src, and reports whether any applied. The result must still parse, or the round does not apply.
func moderniseOnce(filename string, src []byte, goVersion string, suite []*analysis.Analyzer) ([]byte, bool) {
	fset := token.NewFileSet()

	file, err := parser.ParseFile(fset, filename, src, parser.ParseComments)
	if err != nil {
		return src, false
	}

	files, ok := packageFiles(fset, filename, file)
	if !ok {
		return src, false
	}

	pkg, info := checkModerniseFiles(fset, files, goVersion)
	run := &moderniseRun{
		fset: fset,
		filename: filename,
		src: src,
		files: files,
		pkg: pkg,
		info: info,
		suite: suite,
		results: map[*analysis.Analyzer]analysisResult{},
		facts: map[factKey]analysis.Fact{},
	}
	edits := selectFixes(run.fixes())

	if len(edits) == 0 {
		return src, false
	}

	out := applyFixEdits(src, edits)

	if _, err := parser.ParseFile(token.NewFileSet(), filename, out, parser.ParseComments); err != nil {
		return src, false
	}

	return out, true
}

// modernise returns src with the fixes of the modernize analyzers applied, round after round, until none apply. The fixes are held to the Go version of the module that governs filename, so a file outside any module is left alone.
func modernise(filename string, src []byte, suite []*analysis.Analyzer) []byte {
	goVersion := moduleGoVersion(filename)
	if goVersion == "" {
		return src
	}

	for range maxModeniseRounds {
		out, applied := moderniseOnce(filename, src, goVersion, suite)
		if !applied {
			break
		}

		src = out
	}

	return src
}
