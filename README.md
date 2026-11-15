# gofmt, out of the Go tree

A working copy of Go's formatter, `cmd/gofmt`, taken from the **go1.27.4** release tag (`7b28810`, `[release-branch.go1.27] go1.27.4`) and made to build as a standalone module so it can be modified, instrumented and stepped through.

Source: <https://pkg.go.dev/cmd/gofmt> documents the command; the code itself lives at <https://github.com/golang/go/tree/go1.27.4/src/cmd/gofmt>.

## Layout

```
gofmt/                 the command, verbatim from src/cmd/gofmt
  gofmt.go             flags, walking files, concurrent processing, exit codes
  internal.go          parse() and format() – the actual format pipeline
  rewrite.go           -r rewrite rules (reflection-based pattern matching)
  simplify.go          -s simplifications (a[b:len(a)] -> a[b:], range, composite literals, empty decls)
  doc.go               package docs / usage text
  testdata/            .input/.golden pairs used by the tests
go/printer/            src/go/printer – the layout engine, and gofmt builds against it
  printer.go           Config/Mode, output buffering, whitespace, comments glue
  nodes.go             per-node layout: decls, stmts, exprs, struct tags, number normalisation
  comment.go           comment placement and reflow
  gobuild.go           //go:build line formatting
  testdata/            printer golden tests (go test ./go/printer)
internal/diff/         std src/internal/diff, needed by -d
internal/{cfg,goarch,platform,testenv}/   std internals, needed by long_test.go
internal/telemetry/counter/               no-op stand-in for cmd/internal/telemetry/counter
```

`gofmt` imports `forkmat/go/printer`, so edits to `go/printer` take effect on the next build. Everything else is still the standard library (`go/ast`, `go/token`, `go/parser`, `go/scanner`, `text/tabwriter`), so the vendored copy sits on top of the same AST you already have. To go back to the standard printer, change the three `forkmat/go/printer` imports in `gofmt/` back to `go/printer`.

## Build, run, test

```sh
go build -o gofmt-bin ./gofmt
./gofmt-bin -l -w .        # list then rewrite
./gofmt-bin -d file.go     # unified diff, like diff -u
./gofmt-bin -s -w .        # with simplifications
./gofmt-bin -r 'a[b:len(a)] -> a[b:]' file.go   # experimental rewrite
go test ./gofmt            # upstream tests + golden files
```

Checked against the installed `gofmt` (go1.27.2): identical output on the same input. `cmd/gofmt` is byte-identical between go1.27.2 and go1.27.4.

`gofmt -all` enables `-strict`, `-unconvert`, `-flatten`, `-mergecases` and `-omitcases` together. Each of them is described below.

## Local changes

### gofmt's column alignment is removed

Standard gofmt pads structurally similar consecutive lines into columns using `text/tabwriter` (via the `vtab` separators the printer emits, and the tabs `writeCommentPrefix` writes before a trailing comment). This tree removes that padding. What is single-spaced now:

- struct fields, their tags, and their trailing comments
- the specs of a `const`/`var` group: names, type, `=`, values, trailing comments
- trailing comments after any line of code: import specs, statements, call arguments, interface methods, composite literal elements
- keys in multi-line composite literals (`Foo: "foo"`, `"key": 1`)
- `=` and the type column in grouped type declarations (`type ( A = int; LongName = string )`)
- the `{` of consecutive empty one-line functions

Before, with standard gofmt:

```go
type Server struct {
	Name        string
	Addr        string   `json:"addr"`
	IdleTimeout Duration // zero means no timeout
}

var (
	x              = 1 // one
	longerName int = 2
)
```

After, with this tree:

```go
type Server struct {
	Name string
	Addr string `json:"addr"`
	IdleTimeout Duration // zero means no timeout
}

var (
	x = 1 // one
	longerName int = 2
)
```

The point is not only tidiness: column padding only lines up in a monospaced font. In a variable-width font the extra blanks push text around without aligning anything, so single spaces keep the source looking the same everywhere.

Files touched:

| file | change |
| --- | --- |
| `go/printer/printer.go` | `writeCommentPrefix` always writes a blank, never a tab; the `noCommentAlign` state that used to gate this is gone |
| `go/printer/nodes.go` | `fieldList` separates struct fields by a blank; `valueSpec` does the same for `const`/`var` specs, and `keepTypeColumn` (64 lines) plus `keepType`/`extraTabs` are deleted; `exprList` prints `key: value` with a blank; `spec` drops the TypeSpec column; `funcDecl` passes a blank before the body |
| `gofmt/*.go`, `go/printer/*.go` | mechanically reformatted by the new binary, so the tree stays self-consistent |
| `gofmt/testdata/*.golden`, `go/printer/testdata/*.golden`, `*.x`, `*.raw`, `*.norm` | regenerated (`go test ./go/printer -run TestFiles -update`, same for `gofmt`); three hardcoded `\t` expectations in `printer_test.go` updated |

Nothing is padded into a column now. `text/tabwriter` still runs – it is what implements `TabIndent` indentation and the `UseSpaces`/`RawFormat` modes – but the printer emits no `vtab` separators and no bare tabs between tokens any more. The one way to still get a column is a literal tab inside a comment: the printer preserves comment text verbatim, so the tabwriter sees it as a cell. One-line structs such as `struct{ A int }` are unchanged, since they were never column-aligned.

### Octal literals print as `0o…`

The old leading-zero spelling is rewritten to the `0o` form while numbers are normalised, so `0777` prints as `0o777`, `0_123` as `0o_123` and `00` as `0o0`. Decimal, hexadecimal, binary and floating-point literals are untouched, as is a lone `0`.

Imaginary literals are deliberately left alone: the grammar is `imaginary_lit = (decimal_digits | int_lit | float_lit) "i"`, so `0777i` is the *decimal* 777i, not octal 511i – rewriting it to `0o777i` would change its value. The existing normalisation still trims its leading zeros (`0777i` → `777i`). `0o777i`, being explicit, is valid and preserved.

This lives in `normalizedNumber` (`go/printer/nodes.go`), i.e. it applies when the `normalizeNumbers` mode bit is set – always true for gofmt and `go/format`.

Revert with `git diff`-level surgery on those hunks, or by pointing `gofmt/` back at the standard `go/printer` (see above) – the goldens then need regenerating as well.

### Function bodies are never one line

Any function body holding a statement – declaration or literal, one statement or many – is printed over multiple lines. Only empty bodies stay on the header line:

Before, with standard gofmt:

```go
func f() { println(1) }

var g = func() int { return 1 }
```

After, with this tree:

```go
func f() {
	println(1)
}

var g = func() int {
	return 1
}
```

That covers callbacks too: `sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })` and `defer func() { <-fdSem }()` are spread out as well.

`func f() {}` and `func f() { /* comment only */ }` keep their one-line form, since they hold no statements (a body whose only content is a line comment has to break anyway). The check is in `funcBody` (`go/printer/nodes.go`): `oneLine` requires `len(b.List) == 0`.

### `-strict`: lay code out from the AST

`gofmt -strict` ignores the source's line breaks wherever the AST alone can decide the layout. It is off by default; the default output is unchanged. The rules are documented in `go/printer/strict.go`, and the mode is `printer.StrictLayout`.

`-strict` also reorders declarations, in `gofmt/reorder.go`, so that each one comes after the declarations it refers to, and otherwise in alphabetical order. In `a` calling `b`, `b` is printed above `a`. Each of these kinds of run is sorted:

- consecutive plain functions, and consecutive methods, which are sorted by receiver type and then by name. A method that calls another method by name comes after it;
- consecutive `const`, `var` and `type` declarations. Each spec is rewritten as a single declaration, and the printer joins them into blocks. A `var` whose value calls a function or receives from a channel is never moved, because Go runs such initialisers in declaration order and a move would change the order of their effects. Conversions to predeclared types, `make` and `new` are not calls for this purpose. A `const` group whose specs take values from their position (a spec without a value, or `iota`) keeps its order;
- consecutive `:=` statements in a block, with values free of calls and receives. Statements are sorted by the names they declare, and one stays after an earlier one it uses or redeclares, compared by name.

Within a run, declarations that do not depend on each other are sorted by name, and a declaration always comes after the ones it refers to. A cycle keeps its source order. Declarations move with the comments that lead into them.

Types also move: a type declaration moves up to just after the imports when every name it uses is a predeclared type, an imported package, its own type parameter, a type declared in another file of the same package, a type that a dot-imported package exports, or another type that moves too. The other files and the dot-imported packages are read only to find which names are types. A package is found in the module of the file, or with `go/build`. A type that uses a constant, variable or function, or a name not declared in this file, stays where it is, and so does any type that depends on one. Declaration order at package level does not change what a type means, so the move is safe; the restriction keeps a type with a constant length below its constant.

The top of a file reads types, then constants, then vars, then functions, unless a dependency keeps a declaration elsewhere. Constants move up the same way as types, to just after the imports and the types (`hoistConsts`); a group with `iota` moves as a whole. Vars move up to just after the constants at the top: a `var` whose values are pure (no calls other than conversions to predeclared types, `make` and `new`, and no receives) is moved up, and the printer joins the moved vars into one `var (...)` group. A `var` whose values have effects keeps its place, and so does every `var` after it, since initialisation runs those effects in declaration order and a pure value may read what they set. A pure var may still move above a var it refers to, because initialisation follows references, and the vars of a run are sorted by dependency as above. Before, with `-strict`:

```go
var va = 1

func c() int { return 0 }

var (
	vb = 2
	vc = 3
)
```

After, with `-strict`:

```go
var (
	va = 1
	vb = 2
	vc = 3
)

func c() int {
	return 0
}
```

Methods, `init` functions and body-less functions are sorted only with other functions of the same kind, and a run of any one kind ends at a declaration of another kind. A reference is a use of the package-level name, including a function passed as a value, or a constant used in another constant. Local names that shadow a package-level one, selectors (`x.f`), struct-literal keys and labels are not references, except that a method call by name counts as a dependency. Without type information, a name shared by two statements is taken as a dependency, which keeps them in order rather than risking a change in meaning. This runs on the source text before printing, so it is applied by `gofmt`, not by `go/format`.

Before, with standard gofmt:

```go
func foo() {
    foo :=
        "bar"
    fmt.Println(
        "a",
        "b",
    )
}
```

After, with `gofmt -strict`:

```go
func foo() {
	foo := "bar"
	fmt.Println("a", "b")
}
```

The rules:

- Lists (call arguments, right-hand sides, parameters, results) are one line. A list is broken only if it holds a comment, with one element per line.
- Composite literals are one line if they fit in 100 characters and hold no comments; otherwise each element is on its own line with a trailing comma. A literal that sets fields by name (`T{X: 1}`) is always split over several lines, so that it reads as field initialisation. Map literals (`map[K]V{...}` written with an explicit map type) are exempt.
- Binary expressions and selector chains are never broken.
- Consecutive named struct fields and parameters of the same type share one type: `x int, y int` prints as `x, y int`, and `struct{ x int; y int }` as a struct over three lines holding `x, y int`. A field's type is omitted when the field after it has the same type, and a trailing comment on the first field stays on its line between the names: `start, // comment` followed by `end token.Pos`, and a trailing comment on the second field stays on its line. Fields with a tag, or a doc comment on the second field, are not merged. Interface methods are never merged. The same applies to `var` specs in a group.
- A map literal whose keys are all string literals is sorted alphabetically by key, and one whose keys are all number literals numerically (a minus sign is allowed). A map with an `any` key, whose keys are literals of several basic types (`nil`, `true`, `false`, numbers, strings), is grouped by kind in that order, and sorted within each kind the same way. Identifier keys, such as constants, come after the literal keys and are sorted by name, in a literal that has at least one literal key. A literal with only identifier keys may be a struct, so it is left to the rule below.
- The keys of a struct literal are put in the order of the struct's declaration (`gofmt/structorder.go`). The type is found from the literal itself (an anonymous struct), from the file, from the other files of its package, or from an imported package, by its qualifier or through a dot import. A literal whose type cannot be found, is generic, or is not a struct keeps its order. A literal is reordered only if each key is a field, no key repeats, it has no comment inside, and no value calls a function or receives from a channel.
- A `type (...)`, `const (...)` or `var (...)` group that holds one declaration is printed as that declaration, without parentheses, unless a comment lies inside them.
- An index loop `for i := 0; i < n; i++` is written `for i := range n` when the
  body changes neither `i` nor `n`. A bound `len(x)` of a slice or an array is
  written `range x`; of a string, `range len(x)`. An index the body does not use
  gives `for range n`. The types come from type-checking the file; a bound whose
  type cannot be found keeps its loop.
- `x := []T{}` is written `var x []T` when the rest of the block uses `x` only
  where nil and empty behave the same: an append assigned back to `x`, a range,
  `len` or `cap`, or an index. Any other use (a comparison with nil, a return, a
  call argument, a slice expression, an address, or another declaration of the
  name) keeps the `:=`.
- `interface{}` is written `any`, unless the file declares its own `any` at
  package level.
- `x = x + 1` and `x = x - 1` are written `x++` and `x--`, when `x` is an identifier and the other operand is the integer literal `1`. This includes the post statement of a `for` loop.
- The `default` clause of every switch and type switch goes last, since it runs only when no other clause matches, wherever it is written. It stays where it is when it ends in `fallthrough`, or when the clause before it does, since that clause would then fall into a different clause (`defaultLast` in `gofmt/switchorder.go`). A comment between clauses also keeps it in place.
- The clauses of a type switch are sorted alphabetically by their types, when every type is concrete: a pointer, slice, map, array, channel, function or struct type, a predeclared type other than an interface, or a named type whose declaration is not an interface. Interfaces may overlap, so a type switch with one is left alone. The clause bodies need not return: a type switch cannot fall through, and a `break` leaves the switch whichever clause it is in.
- The clauses of a switch whose cases are constants are put in the order those constants are declared in, typically an `iota` block (`gofmt/switchorder.go`). The constants may be declared in the package of the file, in its other files, or in an imported package: a switch over `token.Token` is ordered by the declarations in `go/token`. The switch is left alone unless:
	- every case is a package-level constant, with a value that differs from the value of every other case, since the clauses of a switch match in the order they are written, so two cases with one value would change which one runs;
	- every case names a constant of the same package, so that all the cases can be ordered by one declaration order;
	- no clause ends in `fallthrough`, since that would run the next clause in the source; the bodies need not return;
	- no comment lies between clauses, or after a clause's last statement, and no comment inside a clause would be printed in another clause once the clauses are reordered.

  Constants of the other files of the package are ordered after the file's own by file name, then by declaration, since declaration order across files is not defined. The constants of other files and packages are numbered as the `-strict` passes leave them, so the switch order does not change between runs. A constant's value comes from its package's own declarations: one that depends on another package has no value, and keeps the switch. An imported package is found by its name, which is its explicit name, or else the last element of its path (skipping a major version such as `v2`); a package named differently is not found this way, and keeps the switch.
- The expressions of a `case a, b, c:` clause are sorted (`gofmt/caseorder.go`). In a type switch the types are sorted alphabetically, as the clauses of a type switch are, and need not be concrete: any one of them matches, so their order cannot change which clause runs. In another switch the expressions are sorted as the keys of a map literal are: `nil`, booleans, numbers (a character literal counts as its code point), strings, and then names, each kind by value. A clause with an expression that is not a literal of a basic type or a name, such as a call, keeps its order, since the expressions of a clause are evaluated in order, and so does a clause with a comment inside its list. This runs after `-mergecases`, so merged lists are sorted too.
- Struct and interface bodies with members are always multi-line, even with a single simple field or method. An empty body stays `struct{}`.
- Statements are one per line. A blank line goes before and after a block statement (`if`, `for`, `range`, `switch`, `select`, a bare block), a `return`, and a grouped `var` declaration, but not next to a call statement. The exception is an `if` whose condition uses a name that the statement before it declares with `:=` or `var`: the two stay together, as in `err := f()` followed by `if err != nil {`.
- An `:=` declaration directly before an `if` statement moves into the `if` header when every name it declares is used only inside that `if`: `m := a * 2` followed by `if m > 3 {` becomes `if m := a * 2; m > 3 {`. The `if` must have no init statement of its own, and no comment may lie between the two. A name that is used anywhere else in the block keeps the declaration where it is. A value holding a composite literal of a named type outside brackets keeps it too, since the header would not parse, and so does a declaration followed by a bare `return`, which would otherwise return a shadowed named result. Calls, assignments (`=`, `:=`, `op=`, `++`, `--`), `const` declarations and `var` declarations form runs: a blank line goes between two adjacent statements of different kinds, and none within a run. A run of `go` or `defer` statements is set off by blank lines from the statements around it, but its members are not separated from each other.
- Parentheses are dropped where operator precedence already groups the expression (`(a * b) + c` prints as `a*b + c`). Types, composite and function literals, and unary `<-` and `~` keep theirs.
- Imports are sorted by path within two groups: standard library packages first, then the rest, with one blank line between the groups and none inside them. A package is in the standard library when the first element of its path has no dot, so a module path such as `forkmat/...` counts as standard library. A block with a comment inside keeps its order.
- Top-level declarations get a blank line when the token changes, when the next one has a doc comment, or when either spans several lines.
- A function body holding a statement is always multi-line.
- Adjacent `type` declarations, at package level or in a block, are joined into one `type (...)` group, including declarations that are already in a group. Every doc comment moves onto its spec inside the group, so none is left above the `type (` line. A comment between two of them stays where it is, inside the group, so section comments do not split a run. Another statement between two of them keeps them apart. A group holds one keyword only, and a spec that spans several lines is set off from its neighbours by a blank line.
- A `const`, `var` or `type` group with a comment anywhere inside it is left alone by the sorting, since its comments would not follow their specs once sorted, and a second pass would sort them differently.
Adjacent `var` declarations, at package level or in a block, are joined into one `var (...)` group, with their doc comments and trailing comments kept on their specs inside it. Existing `var (...)` groups join the run too, with their specs kept in order, so two groups side by side become one. Another statement between two of them keeps them apart. A group holds one keyword only.
- Adjacent `const` declarations, singles and groups alike, are joined into one `const (...)` block, at package level or in a block, with their doc comments kept on their specs inside it. A declaration whose values use `iota`, or a group with a spec that takes its value from the one before it, stays apart: `iota` and the implicit repetition both depend on position in the group.
- Inside a function, a `var` with values and no type becomes `:=`: `var x = y` is `x := y`, and `var a, b = 1, 2` is `a, b := 1, 2`. A `var` with a type stays a declaration, and so does one whose names are all `_`, since `:=` needs a new name. A group with a comment inside is left alone.
- Consecutive `var` specs of one type with no values share one line: `var pc int32` then `var top int32` (or the same two in a group) is `var pc, top int32`. Specs with a value, or with a comment, stay apart. When one spec is left, the parentheses of its group are dropped.
- A single `:=` whose value is a zero value becomes a `var` declaration with its type written out: `x := 0` is `var x int`, `x := 0.` is `var x float64`, `x := int32(0)` is `var x int32`, and `""`, `false` and `string("")`, `bool(false)` likewise give `string` and `bool`. Only predeclared type names are used, and only a zero literal (or a conversion of one) counts. Adjacent results join a `var (...)` group, as above.

What still follows the source:

- Comments, and the blank lines around them. A comment is kept on the line the source put it on. The exception is a block's first and last items: a comment right after `{` or right before `}` takes no blank line, and nor does a closing `}` after a comment.
- Blank lines between import groups. `gofmt` sorts within each group, and the groups come from those blank lines, so they are kept.
- Lines with several statements in the source (`a; b`) are split, and a trailing comment may move to a different node as a result.

Not done yet: breaking long lines to a column width, redundant parentheses, and normalising string escapes.

### `-unconvert`: remove redundant conversions

`gofmt -unconvert` removes a conversion `T(x)` when `x` already has the type `T`, so `string(string(s))` prints as `s` and `MyInt(m)` as `m` when `m` is a `MyInt`. This is the first check that uses type information: the file is type-checked on its own (`go/types`, with the source importer), and a name that cannot be resolved from the file has no type, so a conversion of it is kept.

A conversion is kept when:

- its operand is a constant, since `int64(5)` is not the same as `5` once untyped (`unconvert.go`);
- its operand's type differs, such as `string(b)` for a byte slice or `int(m)` for a `MyInt`;
- its type names a member of an imported package, such as `time.Duration(d)`, since removing it could leave the import unused.

Where the operand's operators would bind differently in the conversion's place, it is parenthesised; `-strict` drops parentheses that are not needed.

It is off by default, and runs on complete files only. Type-checking a file loads its imports from source, so it is slower than the other passes. A package with cgo files, such as `net`, is read from its pure Go files, since running the cgo tool for each one cost about a second.

### `-flatten`: move a final else or default out

`gofmt -flatten` moves the final branch of an `if`/`else` chain, or the `default` clause of a `switch` or type switch, to after the statement, when every other branch ends in a `return`:

```go
if x {                          if x {
	y()                             y()
	return 1                        return 1
} else {                  =>    }
	return 2                    return 2
}
```

The statement must be the last one in its block. The move is skipped when it would change the meaning of the code: when the branch uses a name declared in the statement's header with `:=` (such as `if y := f(); y`, or a type switch's `v := x.(type)`), since that name is not in scope after the statement; when an unlabeled `break` in a `switch` default would leave the `switch`; or when a comment lies between the parts that move. It is applied after `-strict`, and off by default.

### `-mergecases`: merge adjacent cases with the same body

`gofmt -mergecases` joins two adjacent `case` clauses of a switch when their bodies are the same, so `case 1: f()` and `case 2: f()` become `case 1, 2: f()`. A type switch clause only merges when its body does not use the variable the switch binds, since in a clause with several types that variable has the type of the switch's operand. Clauses that are not adjacent, a `default` clause, a body that ends in `fallthrough`, and clauses with a comment are left alone. The case expressions are evaluated in order, so merging across another clause could change which of them run.

Under `-strict` the merged list is laid out on one line. Without it, the printer keeps the source's line breaks inside a case list, so the merged list may be split over lines.

### `-omitcases`: drop a case that repeats the code after its switch

`gofmt -omitcases` removes a `case` clause whose body is exactly the code that follows the switch in the same block, when that body ends in a `return`:

```go
switch x {                    switch x {
case 1:                  =>   }
	return 5                  return 5
}
return 5
```

The removal is made only when it cannot change which code runs. A value that the clause matched would reach a later clause or the `default` clause instead, so every other clause must have the same body, or the clause must be the last one with no `default` clause. The clause's case expressions must have no side effects, since they are no longer evaluated. The body must not declare names (they would clash with the code after the switch), must not use names the switch's header declares, and must not contain a `break` that leaves the switch.

## What differs from upstream

Only import paths, so that code outside the Go tree can reach packages that used to be resolved as `std`/`cmd` internals:

| upstream import | here |
| --- | --- |
| `internal/diff` | `forkmat/internal/diff` |
| `internal/testenv` | `forkmat/internal/testenv` |
| `internal/cfg`, `internal/goarch`, `internal/platform` | `forkmat/internal/...` |
| `cmd/internal/telemetry/counter` | `forkmat/internal/telemetry/counter` (no-op shim) |
| `go/printer` (in `gofmt/`) | `forkmat/go/printer` (the vendored copy) |
| `internal/diff` (in `go/printer/printer_test.go`) | `forkmat/internal/diff` |

Nothing else is edited; the files were then run through the built gofmt itself, which only re-sorted those import blocks. `internal/diff/diff_test.go` was dropped (it needs std `internal/txtar` and does not cover gofmt).

## Where the formatting actually happens

The rules you are probably after are now mostly in this tree:

- `go/printer` – layout: tabs vs spaces (`printer.Config`), line breaks, alignment via `text/tabwriter`, comment placement, number literal normalisation. `printerNormalizeNumbers = 1 << 30` in `gofmt.go` is a mode bit defined in `go/printer` for `go/format` and gofmt only.

Still in your GOROOT (`go env GOROOT`):

- `src/go/ast` – `SortImports`, used to keep import blocks grouped and sorted.
- `src/go/parser` – parser modes; `initParserMode()` turns on comments and skips object resolution.
- `src/go/format` – the library version of the same pipeline.

The pipeline in `gofmt/internal.go`:

1. `parse` – `parser.ParseFile`; fragments (`-` stdin partial code) are tried as declarations/statements/expressions in turn.
2. `ast.SortImports` on the parsed file.
3. optional `simplify` (`-s`) and `rewriteFile` (`-r`).
4. `printer.Config.Fprint` with `tabWidth = 8`, `printerMode = UseSpaces | TabIndent | printerNormalizeNumbers`.
5. `processFile` compares output to input for `-l`, or emits a diff for `-d`.

## Notes for experimenting

- `sequencer` in `gofmt.go` bounds concurrency and orders output by input size; handy to know before adding prints.
- `reporter` collects the first 10 distinct error lines unless `-e` is given; it also decides the exit code (2 for usage, 1 for failed files).
- `writeFile` writes via a temp file and backs up the original; `-w` on a symlink or read-only tree can surprise you, so point it at scratch copies.
- Golden-file tests: `gofmt/testdata/*.input` with matching `*.golden`. To probe a printing rule, add a case there and run `go test ./gofmt -run TestRewrite` or the full package.
- Same idea in `go/printer/testdata/`, exercised by `go test ./go/printer`; `go test ./go/printer -run TestFiles -update` rewrites the `.golden` files from current behaviour, which is the quickest way to see what a printer tweak changed.
- The printer's knobs are `Config.Mode`: `RawFormat`, `TabIndent`, `UseSpaces`, `SourcePos`, plus the unexported `normalizeNumbers` (1 << 30) that gofmt and `go/format` pass numerically. `Config.Indent` and `Tabwidth` control the rest; gofmt uses `Tabwidth: 8`, `Indent: 0`.

## Licence

Go source, BSD 3-clause; see `LICENSE`.
