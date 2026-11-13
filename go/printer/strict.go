package printer

import (
	"go/ast"
	"go/token"
	"sort"
)

// Rules for StrictLayout. The source's line breaks are never consulted, except
// for comments:
//
//   - Expression lists (call arguments, right-hand sides, parameters, results)
//     are one line. A composite literal is one line if it fits in
//     strictMaxSize characters and has no comments; otherwise each element is
//     on its own line.
//   - Binary expressions and selector chains are never broken.
//   - Struct and interface bodies are one line only if they hold a single
//     simple field or method; otherwise each member is on its own line.
//   - Statements are one per line. A blank line goes between two statements
//     when one of them is a block statement (if, for, range, switch, select,
//     or a bare block), a return, or a grouped var declaration, unless the
//     other is a call statement.
//   - Top-level declarations are separated by a blank line when the token
//     changes, when the next declaration has a doc comment, or when either
//     declaration spans several lines (a grouped declaration, or a function
//     with statements in its body).
//   - A function body with statements is always multi-line.

// strictMaxSize is the width, in characters, of a composite literal that may
// stay on one line.
const strictMaxSize = 100

// strict reports whether the printer lays out code with StrictLayout.
func (p *printer) strict() bool {
	return p.Mode&StrictLayout != 0
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
	return &CommentedNode{Node: n, Comments: cs[i:j]}
}

// sizeConfig is the configuration nodes are printed with to measure them.
// Sizes are cached per node (p.nodeSizes), so every measurement must use the
// same configuration. Under StrictLayout the measured text is that of the
// output, so number literals are spelled as they will be printed.
func (p *printer) sizeConfig() Config {
	if p.strict() {
		return Config{Mode: RawFormat | StrictLayout | p.Mode&normalizeNumbers}
	}
	return Config{Mode: RawFormat}
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
		start, _ := p.elemSpan(list[i])
		if p.commentIn(end, start) {
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

// elemSpan returns the source range of e as it prints under StrictLayout.
func (p *printer) elemSpan(e ast.Expr) (token.Pos, token.Pos) {
	return leftmostPos(e, token.LowestPrec), e.End()
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
// fit, with the whole literal within strictMaxSize.
func (p *printer) strictFitsOneLine(x *ast.CompositeLit) bool {
	if x.Incomplete || p.commentBefore(p.posFor(x.Rbrace)) {
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

// isCallStmt reports whether s is an expression statement holding a call.
func isCallStmt(s ast.Stmt) bool {
	e, ok := s.(*ast.ExprStmt)
	if !ok {
		return false
	}
	_, ok = e.X.(*ast.CallExpr)
	return ok
}

// blankStmt reports whether s gets a blank line on either side under
// StrictLayout (unless its neighbour is a call statement).
func (p *printer) blankStmt(s ast.Stmt) bool {
	switch s := s.(type) {
	case *ast.BlockStmt, *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt,
		*ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.SelectStmt, *ast.ReturnStmt:
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
	if isCallStmt(prev) || isCallStmt(next) {
		return false
	}
	return p.blankStmt(prev) || p.blankStmt(next)
}

// isGoOrDefer reports whether s is a go or defer statement.
func isGoOrDefer(s ast.Stmt) bool {
	switch s.(type) {
	case *ast.GoStmt, *ast.DeferStmt:
		return true
	}
	return false
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
	case *ast.Ident, *ast.BasicLit, *ast.CallExpr, *ast.SelectorExpr,
		*ast.IndexExpr, *ast.IndexListExpr, *ast.SliceExpr, *ast.TypeAssertExpr:
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

// hasCompositeLit reports whether x holds a composite literal that is not
// enclosed in parentheses of its own.
func hasCompositeLit(x ast.Expr) bool {
	found := false
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
