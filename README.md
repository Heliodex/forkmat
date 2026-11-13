# gofmt, out of the Go tree

A working copy of Go's formatter, `cmd/gofmt`, taken from the **go1.27.4**
release tag (`7b28810`, `[release-branch.go1.27] go1.27.4`) and made to build as
a standalone module so it can be modified, instrumented and stepped through.

Source: <https://pkg.go.dev/cmd/gofmt> documents the command; the code itself
lives at <https://github.com/golang/go/tree/go1.27.4/src/cmd/gofmt>.

## Layout

```
gofmt/                 the command, verbatim from src/cmd/gofmt
  gofmt.go             flags, walking files, concurrent processing, exit codes
  internal.go          parse() and format() — the actual format pipeline
  rewrite.go           -r rewrite rules (reflection-based pattern matching)
  simplify.go          -s simplifications (a[b:len(a)] -> a[b:], range, composite literals, empty decls)
  doc.go               package docs / usage text
  testdata/            .input/.golden pairs used by the tests
go/printer/            src/go/printer — the layout engine, and gofmt builds against it
  printer.go           Config/Mode, output buffering, whitespace, comments glue
  nodes.go             per-node layout: decls, stmts, exprs, struct tags, number normalisation
  comment.go           comment placement and reflow
  gobuild.go           //go:build line formatting
  testdata/            printer golden tests (go test ./go/printer)
internal/diff/         std src/internal/diff, needed by -d
internal/{cfg,goarch,platform,testenv}/   std internals, needed by long_test.go
internal/telemetry/counter/               no-op stand-in for cmd/internal/telemetry/counter
```

`gofmt` imports `forkmat/go/printer`, so edits to `go/printer` take effect on the
next build. Everything else is still the standard library (`go/ast`, `go/token`,
`go/parser`, `go/scanner`, `text/tabwriter`), so the vendored copy sits on top of
the same AST you already have. To go back to the standard printer, change the
three `forkmat/go/printer` imports in `gofmt/` back to `go/printer`.

## Build, run, test

```sh
go build -o gofmt-bin ./gofmt
./gofmt-bin -l -w .        # list then rewrite
./gofmt-bin -d file.go     # unified diff, like diff -u
./gofmt-bin -s -w .        # with simplifications
./gofmt-bin -r 'a[b:len(a)] -> a[b:]' file.go   # experimental rewrite
go test ./gofmt            # upstream tests + golden files
```

Checked against the installed `gofmt` (go1.27.2): identical output on the same
input. `cmd/gofmt` is byte-identical between go1.27.2 and go1.27.4.

## Local changes

### gofmt's column alignment is removed

Standard gofmt pads structurally similar consecutive lines into columns using
`text/tabwriter` (via the `vtab` separators the printer emits, and the tabs
`writeCommentPrefix` writes before a trailing comment). This tree removes that
padding. What is single-spaced now:

- struct fields, their tags, and their trailing comments
- the specs of a `const`/`var` group: names, type, `=`, values, trailing comments
- trailing comments after any line of code: import specs, statements, call
  arguments, interface methods, composite literal elements
- keys in multi-line composite literals (`Foo: "foo"`, `"key": 1`)
- `=` and the type column in grouped type declarations
  (`type ( A = int; LongName = string )`)
- the `{` of consecutive one-line functions
  (`func a()    { println(1) }`)

```go
// before (standard gofmt)
type Server struct {                 var (
	Name        string                   x                 = 1 // one
	Addr        string   `json:"addr"`   longerName int    = 2
	IdleTimeout Duration // zero         w          string
}                                    )

// after (this tree)
type Server struct {                 var (
	Name string                          x = 1 // one
	Addr string `json:"addr"`            longerName int = 2
	IdleTimeout Duration // zero         w string
}                                    )
```

Files touched:

| file | change |
| --- | --- |
| `go/printer/printer.go` | `writeCommentPrefix` always writes a blank, never a tab; the `noCommentAlign` state that used to gate this is gone |
| `go/printer/nodes.go` | `fieldList` separates struct fields by a blank; `valueSpec` does the same for `const`/`var` specs, and `keepTypeColumn` (64 lines) plus `keepType`/`extraTabs` are deleted; `exprList` prints `key: value` with a blank; `spec` drops the TypeSpec column; `funcDecl` passes a blank before the body |
| `gofmt/*.go`, `go/printer/*.go` | mechanically reformatted by the new binary, so the tree stays self-consistent |
| `gofmt/testdata/*.golden`, `go/printer/testdata/*.golden`, `*.x`, `*.raw`, `*.norm` | regenerated (`go test ./go/printer -run TestFiles -update`, same for `gofmt`); three hardcoded `\t` expectations in `printer_test.go` updated |

Nothing is padded into a column now. `text/tabwriter` still runs — it is what
implements `TabIndent` indentation and the `UseSpaces`/`RawFormat` modes — but
the printer emits no `vtab` separators and no bare tabs between tokens any
more. The one way to still get a column is a literal tab inside a comment: the
printer preserves comment text verbatim, so the tabwriter sees it as a cell.
One-line structs such as `struct{ A int }` are unchanged, since they were never
column-aligned.

### Octal literals print as `0o…`

The old leading-zero spelling is rewritten to the `0o` form while numbers are
normalised, so `0777` prints as `0o777`, `0_123` as `0o_123` and `00` as `0o0`.
Decimal, hexadecimal, binary and floating-point literals are untouched, as is a
lone `0`.

Imaginary literals are deliberately left alone: the grammar is
`imaginary_lit = (decimal_digits | int_lit | float_lit) "i"`, so `0777i` is the
*decimal* 777i, not octal 511i — rewriting it to `0o777i` would change its
value. The existing normalisation still trims its leading zeros (`0777i` →
`777i`). `0o777i`, being explicit, is valid and preserved.

This lives in `normalizedNumber` (`go/printer/nodes.go`), i.e. it applies when
the `normalizeNumbers` mode bit is set — always true for gofmt and `go/format`.

Revert with `git diff`-level surgery on those hunks, or by pointing `gofmt/`
back at the standard `go/printer` (see above) — the goldens then need
regenerating as well.

### A function that only returns is never one line

A function declaration whose body is a single `return` statement is always
spread over multiple lines, so the returned expressions never get crammed into
the declaration line:

```go
// before (standard gofmt)
func f() int { return 1 }

// after (this tree)
func f() int {
	return 1
}
```

Function *literals* still collapse when short — `var f = func() int { return 1 }`
and inline callbacks such as `sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })`
keep their one-line form — as do declarations whose body is some other single
statement (`func f() { println(1) }`). The check is in `funcBody`
(`go/printer/nodes.go`), which takes an `isDecl` flag from its two callers.

## What differs from upstream

Only import paths, so that code outside the Go tree can reach packages that
used to be resolved as `std`/`cmd` internals:

| upstream import | here |
| --- | --- |
| `internal/diff` | `forkmat/internal/diff` |
| `internal/testenv` | `forkmat/internal/testenv` |
| `internal/cfg`, `internal/goarch`, `internal/platform` | `forkmat/internal/...` |
| `cmd/internal/telemetry/counter` | `forkmat/internal/telemetry/counter` (no-op shim) |
| `go/printer` (in `gofmt/`) | `forkmat/go/printer` (the vendored copy) |
| `internal/diff` (in `go/printer/printer_test.go`) | `forkmat/internal/diff` |

Nothing else is edited; the files were then run through the built gofmt itself,
which only re-sorted those import blocks. `internal/diff/diff_test.go` was
dropped (it needs std `internal/txtar` and does not cover gofmt).

## Where the formatting actually happens

The rules you are probably after are now mostly in this tree:

- `go/printer` — layout: tabs vs spaces (`printer.Config`), line breaks,
  alignment via `text/tabwriter`, comment placement, number literal
  normalisation. `printerNormalizeNumbers = 1 << 30` in `gofmt.go` is a mode
  bit defined in `go/printer` for `go/format` and gofmt only.

Still in your GOROOT (`go env GOROOT`):

- `src/go/ast` — `SortImports`, used to keep import blocks grouped and sorted.
- `src/go/parser` — parser modes; `initParserMode()` turns on comments and
  skips object resolution.
- `src/go/format` — the library version of the same pipeline.

The pipeline in `gofmt/internal.go`:

1. `parse` — `parser.ParseFile`; fragments (`-` stdin partial code) are tried
   as declarations/statements/expressions in turn.
2. `ast.SortImports` on the parsed file.
3. optional `simplify` (`-s`) and `rewriteFile` (`-r`).
4. `printer.Config.Fprint` with `tabWidth = 8`,
   `printerMode = UseSpaces | TabIndent | printerNormalizeNumbers`.
5. `processFile` compares output to input for `-l`, or emits a diff for `-d`.

## Notes for experimenting

- `sequencer` in `gofmt.go` bounds concurrency and orders output by input size;
  handy to know before adding prints.
- `reporter` collects the first 10 distinct error lines unless `-e` is given;
  it also decides the exit code (2 for usage, 1 for failed files).
- `writeFile` writes via a temp file and backs up the original; `-w` on a
  symlink or read-only tree can surprise you, so point it at scratch copies.
- Golden-file tests: `gofmt/testdata/*.input` with matching `*.golden`. To probe
  a printing rule, add a case there and run `go test ./gofmt -run TestRewrite`
  or the full package.
- Same idea in `go/printer/testdata/`, exercised by `go test ./go/printer`;
  `go test ./go/printer -run TestFiles -update` rewrites the `.golden` files
  from current behaviour, which is the quickest way to see what a printer tweak
  changed.
- The printer's knobs are `Config.Mode`: `RawFormat`, `TabIndent`, `UseSpaces`,
  `SourcePos`, plus the unexported `normalizeNumbers` (1 << 30) that gofmt and
  `go/format` pass numerically. `Config.Indent` and `Tabwidth` control the rest;
  gofmt uses `Tabwidth: 8`, `Indent: 0`.

## Licence

Go source, BSD 3-clause; see `LICENSE`.
