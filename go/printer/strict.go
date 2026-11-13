package printer

import (
	"go/ast"
	"go/token"
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
func (p *printer) strictExprList(list []ast.Expr, depth int, mode exprListMode) {
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
		p.linebreak(p.pos.Line+1, 1, ws, i == 0)
		ws = ignore
		p.expr0(x, depth)
	}
	if len(list) > 0 && mode&commaTerm != 0 {
		p.print(token.COMMA)
	}
	if ws == ignore && mode&noIndent == 0 {
		p.print(unindent)
	}
	p.print(formfeed)
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

// isGroupedDecl reports whether d is printed in its parenthesized form.
func isGroupedDecl(d *ast.GenDecl) bool {
	return d.Lparen.IsValid() || len(d.Specs) != 1
}

// blankStmt reports whether s gets a blank line on either side under
// StrictLayout (unless its neighbour is a call statement).
func blankStmt(s ast.Stmt) bool {
	switch s := s.(type) {
	case *ast.BlockStmt, *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt,
		*ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.SelectStmt, *ast.ReturnStmt:
		return true
	case *ast.DeclStmt:
		d, ok := s.Decl.(*ast.GenDecl)
		return ok && d.Tok == token.VAR && isGroupedDecl(d)
	}
	return false
}

// blankLineBetween reports whether StrictLayout puts a blank line between two
// adjacent statements.
func blankLineBetween(prev, next ast.Stmt) bool {
	if isCallStmt(prev) || isCallStmt(next) {
		return false
	}
	return blankStmt(prev) || blankStmt(next)
}

// multiLineDecl reports whether d is printed over several lines under
// StrictLayout, regardless of the source: a grouped declaration, a function
// with statements, or a type whose struct or interface body is multi-line.
func (p *printer) multiLineDecl(d ast.Decl) bool {
	switch d := d.(type) {
	case *ast.FuncDecl:
		return d.Body != nil && len(d.Body.List) > 0
	case *ast.GenDecl:
		if isGroupedDecl(d) {
			return true
		}
		for _, s := range d.Specs {
			if ts, ok := s.(*ast.TypeSpec); ok && p.multiLineType(ts.Type) {
				return true
			}
		}
	}
	return false
}

// multiLineType reports whether a struct or interface type is printed over
// several lines under StrictLayout.
func (p *printer) multiLineType(t ast.Expr) bool {
	var fields *ast.FieldList
	switch t := t.(type) {
	case *ast.StructType:
		fields = t.Fields
	case *ast.InterfaceType:
		fields = t.Methods
	default:
		return false
	}
	return len(fields.List) > 0 && !p.isOneLineFieldList(fields.List)
}
