# Docs

# Code style guidelines

- Replace em-dashes (—) with en-dashes (–) in text or comments
	- Use em-dashes only as an explicit empty placeholder value
- Replace en-dashes (–) with hyphens (-) in numerical or temporal ranges

## Go

- Use range-over-number `for i := range 5` instead of classic for loops `for i := 0; i < 5; i++` when the index is not mutated inside the loops
	- Same for range-over-slice `for i := range slice` instead of `for i := 0; i < len(slice); i++`
- Use guard clauses where possible to reduce nesting and improve readability
- Put function declarations above their usage in the same file, never below
- If multiple function parameters have the same type, group them like `func foo(a, b int)` instead of `func foo(a int, b int)`
- If multiple struct fields have the same type, group them like `type Foo struct { a, b int }` instead of `type Foo struct { a int; b int }`
- Declare zero-value variables with `var` over direct assignement, so use `var x int` instead of `x := 0`
- Prefer declaring nil-value variables with `var` over direct assignment where the difference between the nil and empty value is not significant, so prefer `var x []string` over `x := []string{}`
- Keep functions and files short, split them along clear lines if they get too large

## Markdown

- Single * for italics, double ** for bold
- Use `#` for headings, not `=` or `-` underlines
- Use fenced code blocks with language identifiers instead of indented code blocks
- Do not hard-wrap. Never split a sentence or a paragraph over multiple lines

## Comments

- Do not hard-wrap. Never split a sentence over multiple lines
	- If the new line is after a semicolon or is a new sentence, OK
