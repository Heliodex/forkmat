package printer

import (
	"bytes"
	"go/ast"
	"go/constant"
	"go/token"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// stmtKind classifies a statement for the blank lines of StrictLayout. A run
// of statements of one kind is printed without blank lines; two adjacent
// statements of different kinds are set off by one.
type stmtKind int

// Rules for StrictLayout. The source's line breaks are never consulted, except
// for comments:
//
//   - Expression lists (call arguments, right-hand sides, parameters, results)
//     are one line. A composite literal is one line if it fits in
//     strictMaxSize characters and has no comments; otherwise each element is
//     on its own line.
//   - Binary expressions and selector chains are never broken.
//   - Struct and interface bodies are one line only when empty; otherwise each
//     member is on its own line.
//   - Statements are one per line. A blank line goes between two statements
//     when one of them is a block statement (if, for, range, switch, select,
//     or a bare block), a return, or a grouped var declaration, unless the
//     other is a call statement. Calls, assignments, const declarations and
//     var declarations form runs: a blank line goes between two adjacent
//     statements of different kinds, and none within a run.
//   - Top-level declarations are separated by a blank line when the token
//     changes, when the next declaration has a doc comment, or when either
//     declaration spans several lines (a grouped declaration, or a function
//     with statements in its body).
//   - A function body with statements is always multi-line.

// strictMaxSize is the width, in characters, of a composite literal that may
// stay on one line.
const strictMaxSize = 100

// elemSpan returns the source range of e as it prints under StrictLayout.
func (p *printer) elemSpan(e ast.Expr) (token.Pos, token.Pos) {
	return leftmostPos(e, token.LowestPrec), e.End()
}

// listHasComment reports whether a comment sits between the tokens of a list:
// after the opening token prev0 (if known), between two elements, or before
// the closing token next0 (if known). Comments nested inside an element do not
// count, since they are that element's business.
func (p *printer) listHasComment(list []ast.Expr, prev0, next0 token.Pos) bool {
	// Each element is measured as it prints: a parenthesized element whose
	// parentheses are dropped starts at its content.
	first, _ := p.elemSpan(list[0])
	from := first

	if prev0.IsValid() {
		from = prev0
	}

	if p.commentIn(from, first) {
		return true
	}

	for i := 1; i < len(list); i++ {
		_, end := p.elemSpan(list[i-1])
		if start, _ := p.elemSpan(list[i]); p.commentIn(end, start) {
			return true
		}
	}

	_, last := p.elemSpan(list[len(list)-1])
	to := last

	if next0.IsValid() {
		to = next0
	}

	return p.commentIn(last, to)
}

// strict reports whether the printer lays out code with StrictLayout.
func (p *printer) strict() bool {
	return p.Mode&StrictLayout != 0
}

// layoutLine returns the line argument for linebreak. Without StrictLayout it
// is the source line of pos. With StrictLayout it is gap lines below the
// current position, so that linebreak prints exactly gap newlines (gap is at
// most maxNewlines, which is the same cap the source would get).
func (p *printer) layoutLine(pos token.Pos, gap int) int {
	if p.strict() {
		return p.pos.Line + gap
	}

	return p.lineFor(pos)
}

// measured returns what to print to measure n. Under StrictLayout that is n
// with the comments inside it: a comment can force line breaks, so a node
// without its comments would be measured too small. Without StrictLayout it
// is n itself, so the default layout is unchanged.
func (p *printer) measured(n ast.Node) any {
	if !p.strict() {
		return n
	}

	cs := p.allComments
	i := sort.Search(len(cs), func(i int) bool {
		return cs[i].Pos() >= n.Pos()
	})
	j := i

	for j < len(cs) && cs[j].Pos() < n.End() {
		j++
	}

	if i == j {
		return n
	}

	return &CommentedNode{
		Node: n,
		Comments: cs[i:j],
	}
}

// sizeConfig is the configuration nodes are printed with to measure them.
// Sizes are cached per node (p.nodeSizes), so every measurement must use the
// same configuration. Under StrictLayout the measured text is that of the
// output, so number literals are spelled as they will be printed.
func (p *printer) sizeConfig() Config {
	if p.strict() {
		return Config{
			Mode: RawFormat | StrictLayout | p.Mode&normalizeNumbers,
		}
	}

	return Config{
		Mode: RawFormat,
	}
}

// strictExprList prints an expression list under StrictLayout. Without the
// strictBreak mode the list is printed on one line. With it, each element is
// printed on its own line, and a trailing comma is added if commaTerm is set.
// A list with a comment in it is always broken, since a line comment cannot
// share a line with the code after it.
func (p *printer) strictExprList(list []ast.Expr, depth int, mode exprListMode, prev0, next0 token.Pos) {
	if p.listHasComment(list, prev0, next0) {
		mode |= strictBreak
	}

	if mode&strictBreak == 0 {
		for i, x := range list {
			if i > 0 {
				p.print(token.COMMA, blank)
			}
			p.expr0(x, depth)
		}

		return
	}

	ws := indent

	if mode&noIndent != 0 {
		ws = ignore
	}

	for i, x := range list {
		if i > 0 {
			p.print(token.COMMA)
		}
		// Without an opening token there is no line break before the first
		// element: after "return" a newline would end the statement.
		if i > 0 || prev0.IsValid() {
			p.linebreak(p.pos.Line+1, 1, ws, i == 0)

			ws = ignore
		}
		p.expr0(x, depth)
	}

	if len(list) > 0 && mode&commaTerm != 0 {
		p.print(token.COMMA)
	}

	if ws == ignore && mode&noIndent == 0 {
		p.print(unindent)
	}

	if mode&commaTerm != 0 {
		// the closing token starts a new line
		p.print(formfeed)
	}
}

// leftmostPos returns the position of the first token of e when it prints
// under StrictLayout. That can differ from e.Pos(): if the parentheses around
// the leftmost operand are dropped, the operand starts the expression.
func leftmostPos(e ast.Expr, prec int) token.Pos {
	e = strictOperand(e, prec)

	switch x := e.(type) {
	case *ast.BinaryExpr:
		return leftmostPos(x.X, x.Op.Precedence())
	case *ast.SelectorExpr:
		return leftmostPos(x.X, token.HighestPrec)
	case *ast.IndexExpr:
		return leftmostPos(x.X, token.HighestPrec)
	case *ast.SliceExpr:
		return leftmostPos(x.X, token.HighestPrec)
	case *ast.TypeAssertExpr:
		return leftmostPos(x.X, token.HighestPrec)
	case *ast.CallExpr:
		return leftmostPos(x.Fun, token.HighestPrec)
	}

	return e.Pos()
}

// commentIn reports whether a comment starts in the source range [from, to).
func (p *printer) commentIn(from, to token.Pos) bool {
	if !from.IsValid() || !to.IsValid() || to <= from {
		return false
	}

	cs := p.allComments
	i := sort.Search(len(cs), func(i int) bool {
		return cs[i].Pos() >= from
	})

	return i < len(cs) && cs[i].Pos() < to
}

// strictFitsOneLine reports whether a composite literal is printed on one
// line under StrictLayout: it has no comments, and its type and elements each
// fit, with the whole literal within strictMaxSize. A literal that initialises
// fields by name (T{X: 1}) is never on one line, unless its type is a map type,
// whose keys are not fields.
func (p *printer) strictFitsOneLine(x *ast.CompositeLit) bool {
	if x.Incomplete || p.commentBefore(p.posFor(x.Rbrace)) {
		return false
	}

	if _, isMap := x.Type.(*ast.MapType); !isMap && hasKeyedElt(x.Elts) {
		return false
	}

	size := 2 // the braces
	if x.Type != nil {
		n := p.nodeSize(x.Type, strictMaxSize)
		if n > strictMaxSize {
			return false
		}

		size += n
	}

	for i, e := range x.Elts {
		n := p.nodeSize(e, strictMaxSize)
		if n > strictMaxSize {
			return false
		}

		size += n

		if i > 0 {
			size += len(", ")
		}
	}

	return size <= strictMaxSize
}

// hasKeyedElt reports whether any element of a composite literal is a key: value
// pair.
func hasKeyedElt(elts []ast.Expr) bool {
	for _, e := range elts {
		if _, ok := e.(*ast.KeyValueExpr); ok {
			return true
		}
	}

	return false
}

// isCallStmt reports whether s is an expression statement holding a call.
func isCallStmt(s ast.Stmt) bool {
	return kindOf(s) == kindCall
}

const (
	kindNone stmtKind = iota // any other statement, which never sets off a neighbour
	kindCall // an expression statement holding a call
	kindAssign // an assignment, := or op=, or an increment or decrement
	kindConst // a const declaration
	kindVar // a var declaration
)

// kindOf returns the kind of s.
func kindOf(s ast.Stmt) stmtKind {
	switch s := s.(type) {
	case *ast.ExprStmt:
		if _, ok := s.X.(*ast.CallExpr); ok {
			return kindCall
		}
	case *ast.AssignStmt, *ast.IncDecStmt:
		return kindAssign
	case *ast.DeclStmt:
		if d, ok := s.Decl.(*ast.GenDecl); ok {
			switch d.Tok {
			case token.CONST:
				return kindConst
			case token.VAR:
				return kindVar
			}
		}
	}

	return kindNone
}

// blankStmt reports whether s gets a blank line on either side under
// StrictLayout (unless its neighbour is a call statement).
func (p *printer) blankStmt(s ast.Stmt) bool {
	switch s := s.(type) {
	case *ast.BlockStmt, *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt, *ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.SelectStmt, *ast.ReturnStmt:
		return true
	case *ast.DeclStmt:
		d, ok := s.Decl.(*ast.GenDecl)

		return ok && d.Tok == token.VAR && p.printsLines(d)
	}

	return false
}

// blankLineBetween reports whether StrictLayout puts a blank line between two
// adjacent statements.
func (p *printer) blankLineBetween(prev, next ast.Stmt) bool {
	// A run of go and defer statements is set off from the statements around
	// it, but its members are not separated from each other.
	if isGoOrDefer(prev) || isGoOrDefer(next) {
		return !(isGoOrDefer(prev) && isGoOrDefer(next))
	}
	// An if statement whose condition uses a name that the statement before it
	// declares stays with that declaration.
	if declaresUsedBy(prev, next) {
		return false
	}

	if p.blankStmt(prev) || p.blankStmt(next) {
		return !isCallStmt(prev) && !isCallStmt(next)
	}
	// Calls, assignments, and const and var declarations are each set off from
	// the others. A statement of kindNone is never set off by this rule.
	kp, kn := kindOf(prev), kindOf(next)

	return kp != kindNone && kn != kindNone && kp != kn
}

// declaresUsedBy reports whether next is an if statement whose condition uses a
// name that prev declares, with := or a var declaration. Names are compared by
// spelling, so a shadowing name counts as a use: the blank line is only a
// layout choice.
func declaresUsedBy(prev, next ast.Stmt) bool {
	is, ok := next.(*ast.IfStmt)
	if !ok {
		return false
	}

	names := map[string]bool{}

	switch s := prev.(type) {
	case *ast.AssignStmt:
		if s.Tok == token.DEFINE {
			for _, l := range s.Lhs {
				if id, ok := l.(*ast.Ident); ok && id.Name != "_" {
					names[id.Name] = true
				}
			}
		}
	case *ast.DeclStmt:
		if d, ok := s.Decl.(*ast.GenDecl); ok && d.Tok == token.VAR {
			for _, sp := range d.Specs {
				for _, id := range sp.(*ast.ValueSpec).Names {
					if id.Name != "_" {
						names[id.Name] = true
					}
				}
			}
		}
	}

	if len(names) == 0 {
		return false
	}

	var used bool

	ast.Inspect(is.Cond, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && names[id.Name] {
			used = true
		}

		return !used
	})
	return used
}

// isGoOrDefer reports whether s is a go or defer statement.
func isGoOrDefer(s ast.Stmt) bool {
	switch s.(type) {
	case *ast.GoStmt, *ast.DeferStmt:
		return true
	}

	return false
}

// printsLines reports whether n is printed over several lines under
// StrictLayout. Unlike nodeSize it is not limited to a width.
func (p *printer) printsLines(n ast.Node) bool {
	cfg := p.sizeConfig()

	var counter sizeCounter

	if err := cfg.fprint(&counter, p.fset, p.measured(n), p.nodeSizes); err != nil {
		return false
	}

	return counter.hasNewline
}

// multiLineDecl reports whether a declaration is printed over several lines
// under StrictLayout (a grouped declaration, a function with statements, a
// multi-line type or call, and so on). It is decided by printing the node, so
// that it follows the same rules as the output.
func (p *printer) multiLineDecl(d ast.Decl) bool {
	if d == nil {
		return false // no previous declaration
	}

	return p.printsLines(d)
}

// strictParenExpr prints a parenthesized expression under StrictLayout. The
// parentheses are dropped when the precedence of the context already groups
// the expression (see needsParens), and any chain of parentheses collapses to
// a single pair.
func (p *printer) strictParenExpr(x *ast.ParenExpr, prec1, depth int) {
	inner := x.X

	for {
		px, ok := inner.(*ast.ParenExpr)
		if !ok {
			break
		}

		inner = px.X
	}

	if !needsParens(inner, prec1) {
		p.expr1(inner, prec1, depth)
		return
	}
	p.print(token.LPAREN)
	p.expr0(inner, reduceDepth(depth))
	p.setPos(x.Rparen)
	p.print(token.RPAREN)
}

// hasCompositeLit reports whether x holds a composite literal that is not
// enclosed in parentheses of its own.
func hasCompositeLit(x ast.Expr) bool {
	var found bool

	ast.Inspect(x, func(n ast.Node) bool {
		switch n.(type) {
		case *ast.ParenExpr:
			return false
		case *ast.CompositeLit:
			found = true
		}

		return !found
	})
	return found
}

// needsParens reports whether x, printed where an operator of precedence prec1
// binds, must keep parentheses. Primary expressions never need them. Types,
// composite and function literals, and unary <- and ~ always keep theirs: for
// those, dropping the parentheses could change how the code parses. A
// parenthesized composite literal also keeps its parentheses wherever one
// appears inside, so that a control clause still parses.
func needsParens(x ast.Expr, prec1 int) bool {
	if hasCompositeLit(x) {
		return true
	}

	switch x := x.(type) {
	case *ast.Ident, *ast.BasicLit, *ast.CallExpr, *ast.SelectorExpr, *ast.IndexExpr, *ast.IndexListExpr, *ast.SliceExpr, *ast.TypeAssertExpr:
		return false
	case *ast.BinaryExpr:
		return x.Op.Precedence() < prec1
	case *ast.StarExpr:
		return token.UnaryPrec < prec1
	case *ast.UnaryExpr:
		return x.Op == token.ARROW || x.Op == token.TILDE || token.UnaryPrec < prec1
	}

	return true
}

// strictOperand returns the operand e as it prints under StrictLayout when it
// is an operand of an operator of precedence prec1: if its parentheses are
// dropped, the parenthesized expression itself, otherwise e.
func strictOperand(e ast.Expr, prec1 int) ast.Expr {
	px, ok := e.(*ast.ParenExpr)
	if !ok {
		return e
	}

	inner := px.X

	for {
		ipx, ok := inner.(*ast.ParenExpr)
		if !ok {
			break
		}

		inner = ipx.X
	}

	if needsParens(inner, prec1) {
		return e
	}

	return inner
}

// afterOpenBrace reports whether the last token written, ignoring white space,
// is an opening brace. The printer's lastTok does not serve here: white space
// clears it.
func (p *printer) afterOpenBrace() bool {
	for _, c := range slices.Backward(p.output) {
		switch c {
		case ' ', '\t', '\n', '\f', '\v', '\r':
			continue
		}

		return c == '{'
	}

	return false
}

// sameType reports whether a and b print the same type, with no comment in
// either.
func (p *printer) sameType(a, b ast.Expr) bool {
	if p.commentIn(a.Pos(), a.End()) || p.commentIn(b.Pos(), b.End()) {
		return false
	}

	cfg := p.sizeConfig()

	var sa, sb bytes.Buffer
	if cfg.fprint(&sa, p.fset, a, p.nodeSizes) != nil || cfg.fprint(&sb, p.fset, b, p.nodeSizes) != nil {
		return false
	}

	return sa.String() == sb.String()
}

// mergeable reports whether field cur can join the names of prev.
func (p *printer) mergeable(prev, cur *ast.Field) bool {
	if len(prev.Names) == 0 || len(cur.Names) == 0 || prev.Tag != nil || cur.Tag != nil || prev.Comment != nil || cur.Comment != nil || cur.Doc != nil {
		return false
	}

	return !p.commentIn(prev.End(), cur.Pos()) && p.sameType(prev.Type, cur.Type)
}

// strictFields returns the fields of a parameter or struct list as they print
// under StrictLayout. Consecutive named fields of the same type are merged into
// one field with several names: x int, y int becomes x, y int. Fields with a
// tag or a comment, or with a comment between them, are not merged.
func (p *printer) strictFields(list []*ast.Field) []*ast.Field {
	if !p.strict() || len(list) < 2 {
		return list
	}

	out := make([]*ast.Field, 0, len(list))

	for _, f := range list {
		if n := len(out); n > 0 && p.mergeable(out[n-1], f) {
			prev := out[n-1]
			out[n-1] = &ast.Field{
				Doc: prev.Doc,
				Names: append(append([]*ast.Ident{}, prev.Names...), f.Names...),
				Type: prev.Type,
				Comment: f.Comment,
			}
			continue
		}

		out = append(out, f)
	}

	return out
}

// groupDecls joins a run of single var declarations into one parenthesized
// declaration.
func groupDecls(run []*ast.GenDecl) *ast.GenDecl {
	var specs []ast.Spec

	for _, d := range run {
		specs = append(specs, d.Specs...)
	}

	first, last := run[0], run[len(run)-1]

	return &ast.GenDecl{
		TokPos: first.TokPos,
		Tok: first.Tok,
		Lparen: first.TokPos,
		Specs: specs,
		Rparen: last.End(),
	}
}

// mergeDoc returns the comment group of a followed by that of b.
func mergeDoc(a, b *ast.CommentGroup) *ast.CommentGroup {
	if b == nil {
		return a
	}

	return &ast.CommentGroup{
		List: append(append([]*ast.Comment{}, a.List...), b.List...),
	}
}

// constJoinable reports whether a const declaration can join a group. Its specs
// must all have values, since a spec without one repeats the value before it,
// and no value may use iota, which counts the specs of a group.
func constJoinable(g *ast.GenDecl) bool {
	for _, sp := range g.Specs {
		if len(sp.(*ast.ValueSpec).Values) == 0 {
			return false
		}
	}
	return !usesIota(g)
}

// usesIota reports whether a declaration mentions iota.
func usesIota(d *ast.GenDecl) bool {
	var found bool

	ast.Inspect(d, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == "iota" {
			found = true
		}

		return !found
	})
	return found
}

// singleDecl returns d if it can join a run of declarations of its token: an
// ungrouped var or const declaration of one spec with no comment, or a var
// group, whose specs can be joined with others without changing their meaning.
// A const declaration is joined only if its values do not use iota: iota
// counts the specs of a group, so grouping such a const would change its value.
// A const group is never joined. A doc comment on the declaration keeps it apart.
func singleDecl(d *ast.GenDecl) *ast.GenDecl {
	if d == nil || d.Doc != nil {
		return nil
	}

	switch d.Tok {
	case token.VAR:
		if d.Lparen.IsValid() {
			// a var group: its specs keep their own comments inside the group
			return d
		}
	case token.CONST:
		if d.Lparen.IsValid() || len(d.Specs) != 1 || usesIota(d) {
			return nil
		}
	default:
		return nil
	}

	if len(d.Specs) != 1 {
		return nil
	}

	if vs := d.Specs[0].(*ast.ValueSpec); vs.Doc != nil || vs.Comment != nil {
		return nil
	}

	return d
}

// withDoc returns spec s with the comment group doc placed before it, merged
// with any doc comment s already has.
func withDoc(s ast.Spec, doc *ast.CommentGroup) ast.Spec {
	if doc == nil {
		return s
	}

	switch s := s.(type) {
	case *ast.TypeSpec:
		c := *s
		c.Doc = mergeDoc(doc, c.Doc)

		return &c
	case *ast.ValueSpec:
		c := *s
		c.Doc = mergeDoc(doc, c.Doc)

		return &c
	}

	return s
}

// joinTypeDecls joins a run of type declarations into one type (...)
// declaration. Each declaration's doc comment moves onto its first spec, inside
// the group, so that no doc comment is left outside the block. pos is the
// position of the keyword and the parenthesis (see typeBlockPos).
func joinTypeDecls(run []*ast.GenDecl, pos token.Pos) *ast.GenDecl {
	var specs []ast.Spec

	for _, d := range run {
		for j, sp := range d.Specs {
			if j == 0 {
				sp = withDoc(sp, d.Doc)
			}

			specs = append(specs, sp)
		}
	}

	last := run[len(run)-1]

	return &ast.GenDecl{
		TokPos: pos,
		Tok: run[0].Tok,
		Lparen: pos,
		Specs: specs,
		Rparen: last.End(),
	}
}

// typeBlockPos returns the position of the keyword and the parenthesis of a
// type block that starts with the declaration first, after the position before.
// The printer writes a comment before the first token that comes after it in
// the source, and writes a comment on the line of the previous token as a
// trailing one. So the position is on the line just above the first comment
// that leads into first, or above first itself: the block then opens on the
// line before, and the comments come after it, on lines of their own.
func (p *printer) typeBlockPos(before token.Pos, first *ast.GenDecl) token.Pos {
	anchor := first.TokPos

	if first.Doc != nil {
		anchor = first.Doc.Pos()
	}

	cs := p.allComments
	if i := sort.Search(len(cs), func(i int) bool {
		return cs[i].Pos() > before
	}); i < len(cs) && cs[i].Pos() < anchor {
		anchor = cs[i].Pos()
	}

	col := p.fset.Position(anchor).Column

	return token.Pos(int(anchor) - col) // the newline that ends the line above
}

// groupTypes joins each run of adjacent type declarations into one type (...)
// declaration. A declaration may be a single one or a group already, and may
// carry doc comments. Comments between two declarations stay where they are,
// inside the block.
func groupTypes[T ast.Node](p *printer, list []T, head token.Pos, tok token.Token, joinable func(*ast.GenDecl) bool, decl func(T) *ast.GenDecl, wrap func(*ast.GenDecl) T) []T {
	var out []T

	for i := 0; i < len(list); {
		j := i + 1

		var run []*ast.GenDecl

		if d := decl(list[i]); d != nil && d.Tok == tok && joinable(d) {
			run = append(run, d)

			for j < len(list) {
				d := decl(list[j])
				if d == nil || d.Tok != tok || !joinable(d) {
					break
				}

				run = append(run, d)
				j++
			}
		}
		// the block needs a position before the run (see joinTypeDecls); where
		// there is none, a run that starts with a doc comment is left alone
		var before token.Pos

		if i > 0 {
			before = list[i-1].End()
		} else {
			before = head
		}

		if len(run) > 1 && (before.IsValid() || run[0].Doc == nil) {
			pos := run[0].TokPos

			if before.IsValid() {
				pos = p.typeBlockPos(before, run[0])
			}

			out = append(out, wrap(joinTypeDecls(run, pos)))
		} else {
			out = append(out, list[i])
		}

		i = j
	}

	return out
}

// mergeableVars reports whether var spec cur can join the names of prev: both
// have the same type written out, and neither has values or comments.
func (p *printer) mergeableVars(prev, cur *ast.ValueSpec) bool {
	return prev.Type != nil && cur.Type != nil && len(prev.Values) == 0 && len(cur.Values) == 0 && prev.Doc == nil && cur.Doc == nil && prev.Comment == nil && cur.Comment == nil && p.sameType(prev.Type, cur.Type)
}

// mergeVarSpecs joins consecutive specs of a var declaration that name the same
// type and have no values, so that var pc int32; var top int32 is written as
// var pc, top int32. A declaration with a comment inside is left alone. When one
// spec is left, the parentheses of the group are dropped.
func (p *printer) mergeVarSpecs(d *ast.GenDecl) *ast.GenDecl {
	if d.Tok != token.VAR || p.commentIn(d.Pos(), d.End()) {
		return d
	}

	var (
		specs []ast.Spec
		merged bool
	)

	for _, sp := range d.Specs {
		vs := sp.(*ast.ValueSpec)

		if n := len(specs); n > 0 {
			if prev := specs[n-1].(*ast.ValueSpec); p.mergeableVars(prev, vs) {
				specs[n-1] = &ast.ValueSpec{
					Names: append(append([]*ast.Ident{}, prev.Names...), vs.Names...),
					Type: prev.Type,
				}
				merged = true
				continue
			}
		}

		specs = append(specs, sp)
	}

	if !merged {
		return d
	}

	out := &ast.GenDecl{
		TokPos: d.TokPos,
		Tok: token.VAR,
		Doc: d.Doc,
		Specs: specs,
	}

	if len(specs) > 1 {
		out.Lparen, out.Rparen = d.Lparen, d.Rparen
	}

	return out
}

// strictGroupDecls groups each run of adjacent single var declarations at
// package level into one var (...) declaration, and each run of type
// declarations into one type (...) declaration (see groupTypes). A comment
// between two var declarations keeps them apart.
func (p *printer) strictGroupDecls(list []ast.Decl) []ast.Decl {
	list = groupTypes(p, list, p.head, token.TYPE, func(*ast.GenDecl) bool { return true }, func(d ast.Decl) *ast.GenDecl {
		g, _ := d.(*ast.GenDecl)

		return g
	}, func(g *ast.GenDecl) ast.Decl {
		return g
	})

	// constants join as types do, with their doc comments, when no value takes
	// its meaning from its place in the group (see constJoinable)
	list = groupTypes(p, list, p.head, token.CONST, constJoinable, func(d ast.Decl) *ast.GenDecl {
		g, _ := d.(*ast.GenDecl)
		return g
	}, func(g *ast.GenDecl) ast.Decl {
		return g
	})

	var out []ast.Decl

	for i := 0; i < len(list); {
		j := i + 1

		var run []*ast.GenDecl

		if d, ok := list[i].(*ast.GenDecl); ok && singleDecl(d) != nil {
			run = append(run, d)

			for j < len(list) {
				d, ok := list[j].(*ast.GenDecl)
				if !ok || singleDecl(d) == nil || d.Tok != run[0].Tok || p.commentIn(list[j-1].End(), list[j].Pos()) {
					break
				}

				run = append(run, d)
				j++
			}
		}

		if len(run) > 1 {
			out = append(out, groupDecls(run))
		} else {
			out = append(out, list[i])
		}

		i = j
	}

	for i, d := range out {
		if g, ok := d.(*ast.GenDecl); ok {
			out[i] = p.mergeVarSpecs(g)
		}
	}

	return out
}

// strictGroupStmts is strictGroupDecls for the statements of a block.
func (p *printer) strictGroupStmts(list []ast.Stmt) []ast.Stmt {
	list = groupTypes(p, list, token.NoPos, token.TYPE, func(*ast.GenDecl) bool { return true }, func(s ast.Stmt) *ast.GenDecl {
		if ds, ok := s.(*ast.DeclStmt); ok {
			g, _ := ds.Decl.(*ast.GenDecl)

			return g
		}

		return nil
	}, func(g *ast.GenDecl) ast.Stmt {
		return &ast.DeclStmt{
			Decl: g,
		}
	})
	declOf := func(s ast.Stmt) *ast.GenDecl {
		if ds, ok := s.(*ast.DeclStmt); ok {
			if d, ok := ds.Decl.(*ast.GenDecl); ok {
				return singleDecl(d)
			}
		}

		return nil
	}

	var out []ast.Stmt

	for i := 0; i < len(list); {
		j := i + 1

		var run []*ast.GenDecl

		if d := declOf(list[i]); d != nil {
			run = append(run, d)

			for j < len(list) {
				d := declOf(list[j])
				if d == nil || d.Tok != run[0].Tok || p.commentIn(list[j-1].End(), list[j].Pos()) {
					break
				}

				run = append(run, d)
				j++
			}
		}

		if len(run) > 1 {
			out = append(out, &ast.DeclStmt{
				Decl: groupDecls(run),
			})
		} else {
			out = append(out, list[i])
		}

		i = j
	}

	for i, s := range out {
		if ds, ok := s.(*ast.DeclStmt); ok {
			if g, ok := ds.Decl.(*ast.GenDecl); ok {
				out[i] = &ast.DeclStmt{
					Decl: p.mergeVarSpecs(g),
				}
			}
		}
	}

	return out
}

// importPath returns the path of an import spec, without its quotes.
func importPath(s *ast.ImportSpec) string {
	if p, err := strconv.Unquote(s.Path.Value); err == nil {
		return p
	}

	return s.Path.Value
}

// shortVarOK reports whether a var spec can be written as a := assignment: it
// has no type, has values, has no comment, and declares at least one new name
// (:= needs one, so var _ = x stays a declaration).
func shortVarOK(vs *ast.ValueSpec) bool {
	if vs.Type != nil || len(vs.Values) == 0 || vs.Doc != nil || vs.Comment != nil {
		return false
	}

	for _, n := range vs.Names {
		if n.Name != "_" {
			return true
		}
	}

	return false
}

// stdImport reports whether an import spec names a standard library package:
// the first element of its path has no dot.
func stdImport(s *ast.ImportSpec) bool {
	first, _, _ := strings.Cut(importPath(s), "/")

	return !strings.Contains(first, ".")
}

// strictImportSpecs returns the specs of an import declaration in the order
// StrictLayout prints them: standard library packages first, then the rest,
// each sorted by path and then by name. A declaration with a comment inside
// keeps its order.
func (p *printer) strictImportSpecs(d *ast.GenDecl) []ast.Spec {
	if p.commentIn(d.Pos(), d.End()) {
		return d.Specs
	}

	specs := slices.Clone(d.Specs)

	slices.SortStableFunc(specs, func(a, b ast.Spec) int {
		sa, sb := a.(*ast.ImportSpec), b.(*ast.ImportSpec)

		if std, other := stdImport(sa), stdImport(sb); std != other {
			if std {
				return -1
			}

			return 1
		}

		if c := strings.Compare(importPath(sa), importPath(sb)); c != 0 {
			return c
		}

		name := func(s *ast.ImportSpec) string {
			if s.Name == nil {
				return ""
			}

			return s.Name.Name
		}

		return strings.Compare(name(sa), name(sb))
	})
	return specs
}

// numericTypes are the predeclared numeric types, which a conversion of a zero
// value can name.
var numericTypes = map[string]bool{
	"int": true,
	"int8": true,
	"int16": true,
	"int32": true,
	"int64": true,
	"uint": true,
	"uint8": true,
	"uint16": true,
	"uint32": true,
	"uint64": true,
	"uintptr": true,
	"byte": true,
	"rune": true,
	"float32": true,
	"float64": true,
	"complex64": true,
	"complex128": true,
}

// defaultNumeric returns the type that := gives an untyped numeric literal of
// the given kind.
func defaultNumeric(kind token.Token) string {
	switch kind {
	case token.FLOAT:
		return "float64"
	case token.IMAG:
		return "complex128"
	}

	return "int"
}

// zeroNumber reports whether lit is a numeric literal with the value zero.
func zeroNumber(lit *ast.BasicLit) bool {
	switch lit.Kind {
	case token.INT, token.FLOAT, token.IMAG:
		v := constant.MakeFromLiteral(lit.Value, lit.Kind, 0)

		return v.Kind() != constant.Unknown && constant.Sign(v) == 0
	}

	return false
}

// zeroVarType returns the type that a := declaration of e takes, when e is a
// zero value that can be written as a var declaration with that type: a zero
// literal (0 is int, 0. is float64, 0i is complex128, "" is string), false, or
// a conversion of a zero value to a predeclared type (int32(0), string(""),
// bool(false)). It returns nil otherwise. The type is written as the source
// spells it, so it names the same type as := gives e.
func zeroVarType(e ast.Expr) ast.Expr {
	switch x := e.(type) {
	case *ast.BasicLit:
		if zeroNumber(x) {
			return &ast.Ident{
				NamePos: x.Pos(),
				Name: defaultNumeric(x.Kind),
			}
		}

		if x.Kind == token.STRING && constant.StringVal(constant.MakeFromLiteral(x.Value, x.Kind, 0)) == "" {
			return &ast.Ident{
				NamePos: x.Pos(),
				Name: "string",
			}
		}
	case *ast.Ident:
		if x.Name == "false" {
			return &ast.Ident{
				NamePos: x.Pos(),
				Name: "bool",
			}
		}
	case *ast.CallExpr:
		fun, ok := x.Fun.(*ast.Ident)
		if !ok || len(x.Args) != 1 || x.Ellipsis.IsValid() {
			return nil
		}

		switch arg := x.Args[0].(type) {
		case *ast.BasicLit:
			if numericTypes[fun.Name] && zeroNumber(arg) {
				return fun
			}

			if fun.Name == "string" && arg.Kind == token.STRING && constant.StringVal(constant.MakeFromLiteral(arg.Value, arg.Kind, 0)) == "" {
				return fun
			}
		case *ast.Ident:
			if fun.Name == "bool" && arg.Name == "false" {
				return fun
			}
		}
	}

	return nil
}

// strictIfInits moves an := declaration into the header of the if statement
// that directly follows it, as the init statement, when every name the
// declaration declares is used only inside that if statement. The if must have
// no init statement of its own, and no comment may lie between the two.
func (p *printer) strictIfInits(list []ast.Stmt) []ast.Stmt {
	out := make([]ast.Stmt, 0, len(list))

	for i := 0; i < len(list); i++ {
		as, ok := list[i].(*ast.AssignStmt)
		if !ok || as.Tok != token.DEFINE || i+1 >= len(list) {
			out = append(out, list[i])
			continue
		}

		is, ok := list[i+1].(*ast.IfStmt)
		if !ok || is.Init != nil || is.Cond == nil || p.commentIn(as.Pos(), is.End()) || !onlyUsedIn(list, i, is) {
			out = append(out, list[i])
			continue
		}

		out = append(out, &ast.IfStmt{
			If: is.If,
			Init: as,
			Cond: is.Cond,
			Body: is.Body,
			Else: is.Else,
		})
		i++
	}

	return out
}

// strictZeroVars rewrites a := declaration of a block whose value is a zero
// value (see zeroVarType) as a var declaration that names the type.
func (p *printer) strictZeroVars(list []ast.Stmt) []ast.Stmt {
	out := make([]ast.Stmt, 0, len(list))

	for _, s := range list {
		as, ok := s.(*ast.AssignStmt)
		if !ok || as.Tok != token.DEFINE || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
			out = append(out, s)
			continue
		}

		name, ok := as.Lhs[0].(*ast.Ident)
		if !ok || name.Name == "_" || p.commentIn(as.Pos(), as.End()) {
			out = append(out, s)
			continue
		}

		typ := zeroVarType(as.Rhs[0])
		if typ == nil {
			out = append(out, s)
			continue
		}

		out = append(out, &ast.DeclStmt{
			Decl: &ast.GenDecl{
				TokPos: as.Pos(),
				Tok: token.VAR,
				Specs: []ast.Spec{
					&ast.ValueSpec{
						Names: []*ast.Ident{name},
						Type: typ,
					},
				},
			},
		})
	}

	return out
}

// onlyUsedIn reports whether every name that the := declaration list[i] declares
// is used only inside the if statement is, among the statements of list other
// than list[i] itself. A name declared earlier in the block is a use outside
// the if statement, so it keeps the declaration where it is.
func onlyUsedIn(list []ast.Stmt, i int, is *ast.IfStmt) bool {
	names := map[string]bool{}

	for _, l := range list[i].(*ast.AssignStmt).Lhs {
		if id, ok := l.(*ast.Ident); ok && id.Name != "_" {
			names[id.Name] = true
		}
	}

	if len(names) == 0 {
		return false
	}

	for k, s := range list {
		if k == i {
			continue
		}

		var outside bool

		ast.Inspect(s, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && names[id.Name] && !(id.Pos() >= is.Pos() && id.Pos() < is.End()) {
				outside = true
			}

			return !outside
		})
		if outside {
			return false
		}
	}

	return true
}

// strictShortVars rewrites the var declarations of a block that qualify (see
// shortVarOK) as := assignments. A group keeps its other specs as a declaration.
func (p *printer) strictShortVars(list []ast.Stmt) []ast.Stmt {
	var out []ast.Stmt

	for _, s := range list {
		ds, ok := s.(*ast.DeclStmt)
		if !ok {
			out = append(out, s)
			continue
		}

		g, ok := ds.Decl.(*ast.GenDecl)
		// a group with a comment inside is left alone: splitting it would
		// move the comments
		if !ok || g.Tok != token.VAR || g.Doc != nil || p.commentIn(g.Pos(), g.End()) {
			out = append(out, s)
			continue
		}

		var converted bool

		for _, sp := range g.Specs {
			if vs := sp.(*ast.ValueSpec); shortVarOK(vs) {
				converted = true
			}
		}

		if !converted {
			out = append(out, s)
			continue
		}

		var pending []ast.Spec

		flush := func() {
			if len(pending) > 0 {
				d := &ast.GenDecl{
					TokPos: g.TokPos,
					Tok: token.VAR,
					Specs: pending,
				}

				if len(pending) > 1 { // a lone declaration needs no parentheses
					d.Lparen, d.Rparen = g.Lparen, g.Rparen
				}

				out = append(out, &ast.DeclStmt{
					Decl: d,
				})
				pending = nil
			}
		}

		for _, sp := range g.Specs {
			vs := sp.(*ast.ValueSpec)
			if !shortVarOK(vs) {
				pending = append(pending, sp)
				continue
			}
			flush()

			lhs := make([]ast.Expr, len(vs.Names))

			for i, n := range vs.Names {
				lhs[i] = n
			}

			out = append(out, &ast.AssignStmt{
				Lhs: lhs,
				TokPos: vs.Names[len(vs.Names)-1].End(),
				Tok: token.DEFINE,
				Rhs: vs.Values,
			})
		}
		flush()
	}

	return out
}
