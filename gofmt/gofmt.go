package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"forkmat/go/printer"
	"forkmat/internal/diff"
	"forkmat/internal/telemetry/counter"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"go/types"
	"io"
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"

	"golang.org/x/sync/semaphore"
)

type (
	// reporterState carries the state of a reporter instance.
	//
	// Only one reporter at a time may have access to a reporterState.
	reporterState struct {
		out, err io.Writer
		exitCode int
	}

	// A reporter reports output, warnings, and errors.
	reporter struct {
		prev <-chan *reporterState
		state *reporterState
	}

	// A sequencer performs concurrent tasks that may write output, but emits that output in a deterministic order.
	sequencer struct {
		sem *semaphore.Weighted // weighted by input bytes (an approximate proxy for memory overhead)
		prev <-chan *reporterState // 1-buffered
		maxWeight int64
	}
)

const (
	// Keep these in sync with go/format/format.go.
	tabWidth = 8

	// printerNormalizeNumbers means to canonicalize number literal prefixes and exponents while printing. See https://golang.org/doc/go1.13#gofmt.
	//
	// This value is defined in go/printer specifically for go/format and cmd/gofmt.
	printerNormalizeNumbers = 1 << 30

	// exclusive is a weight that can be passed to a sequencer to cause a task to be executed without any other concurrent tasks.
	exclusive = -1
)

var (
	// main operation modes
	list = flag.Bool("l", false, "list files whose formatting differs from gofmt's")
	write = flag.Bool("w", false, "write result to (source) file instead of stdout")
	rewriteRule = flag.String("r", "", "rewrite rule (e.g., 'a[b:len(a)] -> a[b:]')")
	simplifyAST = flag.Bool("s", false, "simplify code")
	doDiff = flag.Bool("d", false, "display diffs instead of rewriting files")
	allErrors = flag.Bool("e", false, "report all errors (not just the first 10 on different lines)")
	omitCasesFlag = flag.Bool("omitcases", false, "remove a case that returns the same code as follows its switch (see omitcases.go)")
	mergeCasesFlag = flag.Bool("mergecases", false, "merge adjacent switch cases with the same body into one case (see mergecases.go)")
	flattenFlag = flag.Bool("flatten", false, "move the final else or default branch of a statement after it, when every other branch returns (see flatten.go)")
	unconvertFlag = flag.Bool("unconvert", false, "remove conversions T(x) where x already has type T (see unconvert.go)")
	moderniseFlag = flag.Bool("modernise", false, "apply the fixes of the analyzers in golang.org/x/tools/go/analysis/passes/modernize (see modernise.go)")
	unusedFlag = flag.Bool("unused", false, "remove local variables that are declared and never referenced (see unused.go)")
	vetFixFlag = flag.Bool("vetfix", false, "apply the fixes of the vet analyzers assign, hostport, timeformat and unreachable (see modernise.go)")
	constantsFlag = flag.Bool("constants", false, "declare local variables that are never assigned and hold a constant with const, moving those out of if, for and switch initialisers where nothing conflicts (see constants.go)")
	importsFlag = flag.Bool("imports", true, "remove imports that the file does not use; -imports=false keeps them (see unusedimports.go)")
	inlineFlag = flag.Bool("inline", false, "apply the fixes of the inline analyzer, which inlines calls to functions marked //go:fix inline (see modernise.go)")
	compositeFlag = flag.Bool("composite", false, "apply the fixes of the composite analyzer, which keys unkeyed composite literals of imported struct types (see modernise.go)")
	allFlag = flag.Bool("all", false, "enable -strict, -modernise, -vetfix, -inline, -composite, -constants, -unused, -unconvert, -flatten, -mergecases and -omitcases")
	strict = flag.Bool("strict", false, "lay out code from the AST alone, ignoring the source's line breaks (see go/printer/strict.go)")

	// debugging
	cpuprofile = flag.String("cpuprofile", "", "write cpu profile to this file")

	// errors
	errFormattingDiffers = fmt.Errorf("formatting differs from gofmt's")

	// printerMode may gain printer.StrictLayout from the -strict flag.
	printerMode = printer.UseSpaces | printer.TabIndent | printerNormalizeNumbers

	// fdSem guards the number of concurrently-open file descriptors.
	//
	// For now, this is arbitrarily set to 200, based on the observation that many platforms default to a kernel limit of 256. Ideally, perhaps we should derive it from rlimit on platforms that support that system call.
	//
	// File descriptors opened from outside of this package are not tracked, so this limit may be approximate.
	fdSem = make(chan bool, 200)
	parserMode parser.Mode
	rewrite func(*token.FileSet, *ast.File) *ast.File
)

func fileWeight(path string, info fs.FileInfo) int64 {
	if info == nil {
		return exclusive
	}

	if info.Mode().Type() == fs.ModeSymlink {
		var err error

		info, err = os.Stat(path)

		if err != nil {
			return exclusive
		}
	}

	if !info.Mode().IsRegular() {
		// For non-regular files, FileInfo.Size is system-dependent and thus not a reliable indicator of weight.
		return exclusive
	}

	return info.Size()
}

func initParserMode() {
	parserMode = parser.ParseComments

	if *allErrors {
		parserMode |= parser.AllErrors
	}
	// -r and -unused make use of go/ast's object resolution, so avoid the unnecessary work if neither flag is used.
	if *rewriteRule == "" && !*unusedFlag {
		parserMode |= parser.SkipObjectResolution
	}
}

func isGoFilename(name string) bool {
	return !strings.HasPrefix(name, ".") && strings.HasSuffix(name, ".go")
}

// newSequencer returns a sequencer that allows concurrent tasks up to maxWeight and writes tasks' output to out and err.
func newSequencer(maxWeight int64, out, err io.Writer) *sequencer {
	sem := semaphore.NewWeighted(maxWeight)
	prev := make(chan *reporterState, 1)
	prev <- &reporterState{
		out: out,
		err: err,
	}

	return &sequencer{
		sem: sem,
		prev: prev,
		maxWeight: maxWeight,
	}
}

// readFile reads the contents of filename, described by info. If in is non-nil, readFile reads directly from it. Otherwise, readFile opens and reads the file itself, with the number of concurrently-open files limited by fdSem.
func readFile(filename string, info fs.FileInfo, in io.Reader) ([]byte, error) {
	if in == nil {
		fdSem <- true
		var err error

		f, err := os.Open(filename)
		if err != nil {
			return nil, err
		}

		in = f

		defer func() {
			f.Close()
			<-fdSem
		}()
	}

	// Compute the file's size and read its contents with minimal allocations.
	//
	// If we have the FileInfo from filepath.WalkDir, use it to make a buffer of the right size and avoid ReadAll's reallocations.
	//
	// If the size is unknown (or bogus, or overflows an int), fall back to a size-independent ReadAll.
	size := -1

	if info != nil && info.Mode().IsRegular() && int64(int(info.Size())) == info.Size() {
		size = int(info.Size())
	}

	if size+1 <= 0 {
		// The file is not known to be regular, so we don't have a reliable size for it.
		var err error

		src, err := io.ReadAll(in)
		if err != nil {
			return nil, err
		}

		return src, nil
	}

	// We try to read size+1 bytes so that we can detect modifications: if we read more than size bytes, then the file was modified concurrently. (If that happens, we could, say, append to src to finish the read, or proceed with a truncated buffer – but the fact that it changed at all indicates a possible race with someone editing the file, so we prefer to stop to avoid corrupting it.)
	src := make([]byte, size+1)
	n, err := io.ReadFull(in, src)

	switch err {
	case nil, io.EOF, io.ErrUnexpectedEOF:
		// io.ReadFull returns io.EOF (for an empty file) or io.ErrUnexpectedEOF (for a non-empty file) if the file was changed unexpectedly. Continue with comparing file sizes in those cases.
	default:
		return nil, err
	}

	if n < size {
		return nil, fmt.Errorf("error: size of %s changed during reading (from %d to %d bytes)", filename, size, n)
	} else if n > size {
		return nil, fmt.Errorf("error: size of %s changed during reading (from %d to >=%d bytes)", filename, size, len(src))
	}

	return src[:n], nil
}

// getState blocks until any prior reporters are finished with the reporter state, then returns the state for manipulation.
func (r *reporter) getState() *reporterState {
	if r.state == nil {
		r.state = <-r.prev
	}

	return r.state
}

func (r *reporter) ExitCode() int {
	return r.getState().exitCode
}

// Report emits a non-nil error to the reporter's error stream, changing its exit code to a nonzero value.
func (r *reporter) Report(err error) {
	if err == nil {
		panic("Report with nil error")
	}

	if st := r.getState(); err == errFormattingDiffers {
		st.exitCode = 1
	} else {
		scanner.PrintError(st.err, err)

		st.exitCode = 2
	}
}

// Warnf emits a warning message to the reporter's error stream, without changing its exit code.
func (r *reporter) Warnf(format string, args ...any) {
	fmt.Fprintf(r.getState().err, format, args...)
}

// formatSource returns src, the source of filename, formatted as the options ask. fromDisk says that src is the file as it is on disk, whose reordered form may be shared (see reorderedSource). fragmentOk accepts a program fragment, and r reports warnings.
func formatSource(filename string, src []byte, fragmentOk, fromDisk bool, r *reporter) ([]byte, error) {
	fileSet := token.NewFileSet()
	file, sourceAdj, indentAdj, err := parse(fileSet, filename, src, fragmentOk)
	if err != nil {
		return nil, err
	}

	// a generated file keeps the layout its generator wrote, so the fork's passes and the strict layout leave it alone (see generated.go)
	generated := sourceAdj == nil && isGenerated(file)
	rewritable := sourceAdj == nil && !generated
	if suite := enabledSuite(); len(suite) > 0 && rewritable {
		// -modernise and -vetfix apply the fixes of their analyzers to the source, and the file is parsed again from the result (see modernise.go)
		if out := modernise(filename, src, suite); !bytes.Equal(out, src) {
			src = out
			fileSet = token.NewFileSet()

			if file, err = parser.ParseFile(fileSet, filename, src, parserMode); err != nil {
				return nil, err
			}
		}
	}

	if *importsFlag && rewritable {
		// -imports removes the imports that the file does not use, after the fixes, which can leave some unused (see unusedimports.go)
		if out := removeUnusedImports(filename, src); !bytes.Equal(out, src) {
			src = out
			fileSet = token.NewFileSet()

			if file, err = parser.ParseFile(fileSet, filename, src, parserMode); err != nil {
				return nil, err
			}
		}
	}

	if *constantsFlag && rewritable {
		// -constants declares the variables that are never assigned and hold a constant with const, and the file is parsed again from the result (see constants.go)
		if out := constantsSource(filename, src); !bytes.Equal(out, src) {
			src = out
			fileSet = token.NewFileSet()

			if file, err = parser.ParseFile(fileSet, filename, src, parserMode); err != nil {
				return nil, err
			}
		}
	}

	if *strict && rewritable {
		// -strict also reorders declarations below the ones they depend on (see reorder.go). The reordered source is parsed again, for printing.
		if out := reorderSourceOf(fileSet, filename, src, fromDisk); !bytes.Equal(out, src) {
			if reordered, err := parser.ParseFile(fileSet, filename, out, parser.ParseComments); err == nil {
				file = reordered
			}
		}
	}

	// -unused runs before the passes that rebuild nodes, such as the reflection rewrite of rangeLoops, since it needs the object links the parser wrote (see unused.go)
	if *unusedFlag && rewritable {
		unusedVars(fileSet, file, file.Comments)
	}

	// the types of the file, checked once and shared by the passes that need them;
	// a pass that rebuilds the file from its source checks it again
	var typeInfo *types.Info

	typesOf := func() *types.Info {
		if typeInfo == nil {
			typeInfo = checkTypes(fileSet, file)
		}

		return typeInfo
	}

	if *unconvertFlag && rewritable && mayConvert(filename, file) {
		file = unconvert(fileSet, file, typesOf())
	}

	if *omitCasesFlag && rewritable {
		omitCases(fileSet, file, file.Comments)
	}

	if *strict && rewritable {
		// -strict puts the keys of struct literals in declaration order (see structorder.go)
		structLiteralOrder(filename, file)
	}

	if *strict && rewritable {
		// -strict orders the cases of a switch over iota constants by declaration (see switchorder.go)
		switchCaseOrder(filename, file)
	}

	if *strict && rewritable {
		// -strict writes x = x + 1 and x = x - 1 as x++ and x-- (see incdec.go)
		incDecStmts(file)
	}

	if *strict && rewritable {
		// -strict writes index loops as range loops (see rangeloop.go)
		rangeLoops(filename, file, file.Comments, typesOf)
	}

	if *strict && rewritable {
		// -strict declares empty slices that nil can stand in for with var (see
		// emptyslice.go)
		emptySlices(file)
	}

	if *strict && rewritable {
		// -strict writes interface{} as any (see anytype.go)
		anyTypes(file)
	}

	if *strict && rewritable {
		// -strict writes new(T) of a struct type as &T{} (see newlit.go)
		newLiterals(file, typesOf)
	}

	if *mergeCasesFlag && rewritable {
		mergeCases(fileSet, file)
	}

	if *strict && rewritable {
		// -strict sorts the expressions of each case clause, after clauses have merged (see caseorder.go)
		caseListOrder(file)
	}

	if *flattenFlag && rewritable {
		flatten(file, file.Comments)
	}

	if rewrite != nil {
		if sourceAdj == nil {
			file = rewrite(fileSet, file)
		} else {
			r.Warnf("warning: rewrite ignored for incomplete programs\n")
		}
	}
	ast.SortImports(fileSet, file)
	if *simplifyAST {
		simplify(file)
	}

	mode := printerMode

	if generated {
		mode &^= printer.StrictLayout
	}

	res, err := format(fileSet, file, sourceAdj, indentAdj, src, printer.Config{
		Mode: mode,
		Tabwidth: tabWidth,
	})
	if err != nil {
		return nil, err
	}

	return res, nil
}

// Write emits a slice to the reporter's output stream.
//
// Any error is returned to the caller, and does not otherwise affect the reporter's exit code.
func (r *reporter) Write(p []byte) (int, error) {
	return r.getState().out.Write(p)
}

// backupFile writes data to a new file named filename<number> with permissions perm, with <number> randomly chosen such that the file name is unique. backupFile returns the chosen file name.
func backupFile(filename string, data []byte, perm fs.FileMode) (string, error) {
	fdSem <- true

	defer func() {
		<-fdSem
	}()

	nextRandom := func() string {
		return strconv.Itoa(rand.Int())
	}
	dir, base := filepath.Split(filename)

	var (
		bakname string
		f *os.File
	)

	for {
		bakname = filepath.Join(dir, base+"."+nextRandom())

		var err error

		f, err = os.OpenFile(bakname, os.O_RDWR|os.O_CREATE|os.O_EXCL, perm)

		if err == nil {
			break
		}

		if !os.IsExist(err) {
			return "", err
		}
	}

	// write data to backup file
	_, err := f.Write(data)
	if err1 := f.Close(); err == nil {
		err = err1
	}

	return bakname, err
}

// Add blocks until the sequencer has enough weight to spare, then adds f as a task to be executed concurrently.
//
// If the weight is either negative or larger than the sequencer's maximum weight, Add blocks until all other tasks have completed, then the task executes exclusively (blocking all other calls to Add until it completes).
//
// f may run concurrently in a goroutine, but its output to the passed-in reporter will be sequential relative to the other tasks in the sequencer.
//
// If f invokes a method on the reporter, execution of that method may block until the previous task has finished. (To maximize concurrency, f should avoid invoking the reporter until it has finished any parallelizable work.)
//
// If f returns a non-nil error, that error will be reported after f's output (if any) and will cause a nonzero final exit code.
func (s *sequencer) Add(weight int64, f func(*reporter) error) {
	if weight < 0 || weight > s.maxWeight {
		weight = s.maxWeight
	}

	if err := s.sem.Acquire(context.TODO(), weight); err != nil {
		// Change the task from "execute f" to "report err".
		weight = 0
		f = func(*reporter) error {
			return err
		}
	}

	next := make(chan *reporterState, 1)
	r := &reporter{
		prev: s.prev,
	}
	s.prev = next

	// Start f in parallel: it can run until it invokes a method on r, at which point it will block until the previous task releases the output state.
	go func() {
		if err := f(r); err != nil {
			r.Report(err)
		}

		next <- r.getState() // Release the next task.
		s.sem.Release(weight)
	}()
}

// AddReport prints an error to s after the output of any previously-added tasks, causing the final exit code to be nonzero.
func (s *sequencer) AddReport(err error) {
	s.Add(0, func(*reporter) error {
		return err
	})
}

// GetExitCode waits for all previously-added tasks to complete, then returns an exit code for the sequence suitable for passing to os.Exit.
func (s *sequencer) GetExitCode() int {
	c := make(chan int, 1)

	s.Add(0, func(r *reporter) error {
		c <- r.ExitCode()

		return nil
	})
	return <-c
}

func usage() {
	fmt.Fprintf(os.Stderr, "usage: gofmt [flags] [path ...]\n")
	flag.PrintDefaults()
}

// writeFile updates a file with the new formatted data.
func writeFile(filename string, orig, formatted []byte, perm fs.FileMode) error {
	// Make a temporary backup file before rewriting the original file.
	bakname, err := backupFile(filename, orig, perm)
	if err != nil {
		return err
	}

	fdSem <- true

	defer func() {
		<-fdSem
	}()

	fout, err := os.OpenFile(filename, os.O_WRONLY, perm)
	if err != nil {
		// We couldn't even open the file, so it should not have changed.
		os.Remove(bakname)
		return err
	}

	defer fout.Close() // for error paths

	restoreFail := func(err error) {
		fmt.Fprintf(os.Stderr, "gofmt: %s: error restoring file to original: %v; backup in %s\n", filename, err, bakname)
	}
	n, err := fout.Write(formatted)
	if err == nil {
		err = fout.Truncate(int64(n))
	}

	if err != nil {
		// Rewriting the file failed.

		if n == 0 {
			// Original file unchanged.
			os.Remove(bakname)
			return err
		}

		// Try to restore the original contents.

		no, erro := fout.WriteAt(orig, 0)
		if erro != nil {
			// That failed too.
			restoreFail(erro)
			return err
		}

		if no < n {
			// Original file is shorter. Truncate.
			if erro = fout.Truncate(int64(no)); erro != nil {
				restoreFail(erro)
				return err
			}
		}

		if erro := fout.Close(); erro != nil {
			restoreFail(erro)
			return err
		}

		// Original contents restored.
		os.Remove(bakname)
		return err
	}

	if err := fout.Close(); err != nil {
		restoreFail(err)
		return err
	}

	// File updated.
	os.Remove(bakname)
	return nil
}

// If info == nil, we are formatting stdin instead of a file. If in == nil, the source is the contents of the file with the given filename.
func processFile(filename string, info fs.FileInfo, in io.Reader, r *reporter) error {
	src, err := readFile(filename, info, in)
	if err != nil {
		return err
	}

	// If we are formatting stdin, we accept a program fragment in lieu of a complete source file.
	fragmentOk := info == nil
	res, err := formatSource(filename, src, fragmentOk, info != nil, r)
	if err != nil {
		return err
	}

	// the reorder passes must leave the output as it is: a pass that the printed layout changes would change it again on the next run, so the output is formatted again
	for range maxStrictRounds - 1 {
		if !*strict || reorderStable(filename, res) {
			break
		}

		if res, err = formatSource(filename, res, fragmentOk, false, r); err != nil {
			return err
		}
	}

	if !bytes.Equal(src, res) {
		// formatting has changed
		if *list {
			fmt.Fprintln(r, filename)
		}

		if *write {
			if info == nil {
				panic("-w should not have been allowed with stdin")
			}

			perm := info.Mode().Perm()

			if werr := writeFile(filename, src, res, perm); werr != nil {
				return werr
			}
		}

		if *doDiff {
			newName := filepath.ToSlash(filename)
			oldName := newName + ".orig"

			r.Write(diff.Diff(oldName, src, newName, res))
			return errFormattingDiffers
		}
	}

	if !*list && !*write && !*doDiff {
		_, err = r.Write(res)
	}

	return err
}

func gofmtMain(s *sequencer) {
	counter.Open()

	flag.Usage = usage

	flag.Parse()
	if *allFlag {
		*strict = true
		*moderniseFlag = true
		*vetFixFlag = true
		*inlineFlag = true
		*compositeFlag = true
		*constantsFlag = true
		*unusedFlag = true
		*unconvertFlag = true
		*flattenFlag = true
		*mergeCasesFlag = true
		*omitCasesFlag = true
	}
	counter.Inc("gofmt/invocations")
	counter.CountFlags("gofmt/flag:", *flag.CommandLine)
	if *strict {
		printerMode |= printer.StrictLayout
	}

	if *cpuprofile != "" {
		fdSem <- true
		f, err := os.Create(*cpuprofile)
		if err != nil {
			s.AddReport(fmt.Errorf("creating cpu profile: %s", err))
			return
		}

		defer func() {
			f.Close()
			<-fdSem
		}()

		pprof.StartCPUProfile(f)

		defer pprof.StopCPUProfile()
	}
	initParserMode()
	initRewrite()

	args := flag.Args()
	if len(args) == 0 {
		if *write {
			s.AddReport(fmt.Errorf("error: cannot use -w with standard input"))
			return
		}
		s.Add(0, func(r *reporter) error {
			return processFile("<standard input>", nil, os.Stdin, r)
		})
		return
	}

	for _, arg := range args {
		// Walk each given argument as a directory tree. If the argument is not a directory, it's always formatted as a Go file. If the argument is a directory, we walk it, ignoring non-Go files.
		if err := filepath.WalkDir(arg, func(path string, d fs.DirEntry, err error) error {
			switch {
			case err != nil:
				return err
			case d.IsDir():
				return nil // simply recurse into directories
			case path == arg:
				// non-directories given as explicit arguments are always formatted
			case !isGoFilename(d.Name()):
				return nil // skip walked non-Go files
			}

			info, err := d.Info()
			if err != nil {
				return err
			}
			s.Add(fileWeight(path, info), func(r *reporter) error {
				return processFile(path, info, nil, r)
			})
			return nil
		}); err != nil {
			s.AddReport(err)
		}
	}
}

func main() {
	// Arbitrarily limit in-flight work to 2MiB times the number of threads.
	//
	// The actual overhead for the parse tree and output will depend on the specifics of the file, but this at least keeps the footprint of the process roughly proportional to GOMAXPROCS.
	maxWeight := 2 << 20 * int64(runtime.GOMAXPROCS(0))
	s := newSequencer(maxWeight, os.Stdout, os.Stderr)

	// call gofmtMain in a separate function so that it can use defer and have them run before the exit.
	gofmtMain(s)
	os.Exit(s.GetExitCode())
}
