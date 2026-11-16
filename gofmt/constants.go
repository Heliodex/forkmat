package main

import (
	"bytes"
	"forkmat/go/printer"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"slices"
)

type (
	// constKind says how a candidate is declared with const.
	constKind int

	// constCandidate is a variable that -constants can declare with const. It is identified by the offset of its name, which is the same in every parse of the same source.
	constCandidate struct {
		offset int
		kind constKind
		list *[]ast.Stmt
		index int
		stmt ast.Stmt
		name *ast.Ident
		typ string
		unwrap bool // the value is a conversion of an untyped constant to typ, which the declaration makes redundant
	}
)

// This file implements -constants: a local variable that is never assigned, and that holds a constant of a type const can declare, is declared with const instead. Its initialiser moves out of an if, for or switch statement, into the block that holds the statement, when no other identifier of the name would conflict there. Floats are left alone, since constant arithmetic on them is exact where the variable's is rounded.
//
// The check is the package itself: the rewritten file is type-checked with its siblings, and a rewrite that adds a type error is not made. That catches what the rules here cannot see, such as a constant overflow or a division by zero that the variable hid at run time. The rewrite is repeated while it declares more constants, since a constant can make another one constant.

// maxConstantRounds bounds how many times -constants is applied to one file, since each round can make more variables constant.
const maxConstantRounds = 10

const (
	// constInPlace is a var declaration, which keeps its place.
	constInPlace constKind = iota

	// constDefine is a := statement, which keeps its place.
	constDefine

	// constHoist is the := initialiser of an if, for or switch statement, which moves out in front of it.
	constHoist
)

// constBasic reports whether a constant of the basic type b is declared exactly as the variable is: a boolean, a string, or an integer, not a float or a complex number.
func constBasic(b *types.Basic) bool {
	return b.Info()&types.IsUntyped == 0 && b.Info()&(types.IsBoolean|types.IsInteger|types.IsString) != 0
}

// constType returns the name of the type of obj for a const declared without a type, and whether it can be declared at all. The type must be a basic type that is not a float, and that is not named, since the declaration would need the name written where it is not in scope otherwise.
func constType(obj *types.Var) (string, bool) {
	basic, ok := obj.Type().(*types.Basic)
	if !ok || !constBasic(basic) {
		return "", false
	}

	return basic.Name(), true
}

// initOf returns the := statement that initialises the if, for or switch statement st.
func initOf(st ast.Stmt) *ast.AssignStmt {
	switch s := st.(type) {
	case *ast.ForStmt:
		return s.Init.(*ast.AssignStmt)
	case *ast.IfStmt:
		return s.Init.(*ast.AssignStmt)
	}

	return st.(*ast.SwitchStmt).Init.(*ast.AssignStmt)
}

// introducedErrors reports whether after has a type error that before does not.
func introducedErrors(before, after map[string]int) bool {
	for msg, n := range after {
		if n > before[msg] {
			return true
		}
	}

	return false
}

// objOf returns the object that id denotes or declares.
func objOf(info *types.Info, id *ast.Ident) types.Object {
	if obj := info.Defs[id]; obj != nil {
		return obj
	}

	return info.Uses[id]
}

// conflictsIn reports whether an identifier of name other than name itself, and not obj, appears in scope: such a name would clash with a constant declared in scope.
func conflictsIn(info *types.Info, scope ast.Node, name *ast.Ident, obj types.Object) bool {
	var found bool

	ast.Inspect(scope, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id != name && id.Name == name.Name && objOf(info, id) != obj {
			found = true
		}

		return !found
	})
	return found
}

// packageErrors returns the messages of the type errors of the package of filename, with src as the file, counted by message. It reports false when the package cannot be taken as a whole.
func packageErrors(filename string, src []byte) (map[string]int, bool) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, src, parser.ParseComments)
	if err != nil {
		return nil, false
	}

	files, ok := packageFiles(fset, filename, file)
	if !ok {
		return nil, false
	}

	counts := map[string]int{}
	conf := types.Config{
		Importer: sharedImporter,
		GoVersion: moduleGoVersion(filename),
		Error: func(err error) {
			if te, ok := err.(types.Error); ok {
				counts[te.Msg]++
			}
		},
	}

	conf.Check("gofmt.invalid/"+files[0].Name.Name, fset, files, nil)
	return counts, true
}

// printFile returns file printed, as the other passes print it.
func printFile(fset *token.FileSet, file *ast.File) ([]byte, error) {
	var buf bytes.Buffer

	cfg := printer.Config{
		Mode: printerMode,
		Tabwidth: tabWidth,
	}

	if err := cfg.Fprint(&buf, fset, file); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// setInit sets the initialiser of the if, for or switch statement st to init.
func setInit(st, init ast.Stmt) {
	switch s := st.(type) {
	case *ast.ForStmt:
		s.Init = init
	case *ast.IfStmt:
		s.Init = init
	case *ast.SwitchStmt:
		s.Init = init
	}
}

// unparen returns e without its parentheses.
func unparen(e ast.Expr) ast.Expr {
	for {
		p, ok := e.(*ast.ParenExpr)
		if !ok {
			return e
		}

		e = p.X
	}
}

// identOf returns the identifier that e is, if it is one.
func identOf(e ast.Expr) *ast.Ident {
	id, _ := unparen(e).(*ast.Ident)

	return id
}

// constDecl returns the const declaration of the := statement a, with the type typ when it is inferred.
func constDecl(a *ast.AssignStmt, typ string, unwrap bool) ast.Stmt {
	spec := &ast.ValueSpec{
		Names: []*ast.Ident{identOf(a.Lhs[0])},
		Values: a.Rhs,
	}

	if typ != "" {
		spec.Type = &ast.Ident{
			NamePos: a.Pos(),
			Name: typ,
		}
	}

	if unwrap {
		unwrapConversion(spec)
	}

	return &ast.DeclStmt{
		Decl: &ast.GenDecl{
			TokPos: a.Pos(),
			Tok: token.CONST,
			Specs: []ast.Spec{spec},
		},
	}
}

// redundantConversion reports whether value is a conversion to the type named typ, of an operand that already has the type t: the declaration of a constant of type t makes the conversion redundant, as in const r uint32 = uint32(0), which means the same as const r uint32 = 0. A conversion that changes the type is not redundant.
func redundantConversion(value ast.Expr, typ string, t types.Type, info *types.Info) bool {
	call, ok := value.(*ast.CallExpr)
	if !ok || typ == "" || len(call.Args) != 1 || call.Ellipsis.IsValid() {
		return false
	}

	if fun, ok := call.Fun.(*ast.Ident); !ok || fun.Name != typ {
		return false
	}

	tv, ok := info.Types[call.Args[0]]

	return ok && tv.Type != nil && types.Identical(tv.Type, t)
}

// defType returns the type of the variable that name declares.
func defType(info *types.Info, name *ast.Ident) types.Type {
	return info.Defs[name].Type()
}

// unwrapConversion replaces the value of vs, a conversion of an untyped constant (see untypedConversionTo), by the constant.
func unwrapConversion(vs *ast.ValueSpec) {
	vs.Values[0] = vs.Values[0].(*ast.CallExpr).Args[0]
}

// declareConstants rewrites the candidates of file, which they were found in. The candidates are done from the last in the file, so that the index of each one in its list is still right when it is reached.
func declareConstants(file *ast.File, cands []constCandidate) {
	slices.SortFunc(cands, func(a, b constCandidate) int {
		return b.offset - a.offset
	})
	for _, c := range cands {
		switch c.kind {
		case constInPlace:
			gd := c.stmt.(*ast.DeclStmt).Decl.(*ast.GenDecl)
			gd.Tok = token.CONST

			vs := gd.Specs[0].(*ast.ValueSpec)

			if c.typ != "" {
				vs.Type = &ast.Ident{
					NamePos: vs.Pos(),
					Name: c.typ,
				}
			}

			if c.unwrap {
				unwrapConversion(vs)
			}
		case constDefine:
			(*c.list)[c.index] = constDecl(c.stmt.(*ast.AssignStmt), c.typ, c.unwrap)
		case constHoist:
			*c.list = slices.Insert(*c.list, c.index, constDecl(initOf(c.stmt), c.typ, c.unwrap))

			setInit(c.stmt, nil)
		}
	}
}

// defineOf returns the := statement that init is, when it declares one name.
func defineOf(init ast.Stmt) (*ast.AssignStmt, bool) {
	a, ok := init.(*ast.AssignStmt)
	if !ok || a.Tok != token.DEFINE || len(a.Lhs) != 1 || len(a.Rhs) != 1 || identOf(a.Lhs[0]) == nil {
		return nil, false
	}

	return a, true
}

// modifiedIn reports whether obj is assigned, incremented, has its address taken, or is a range target with =, anywhere in scope.
func modifiedIn(info *types.Info, scope ast.Node, obj types.Object) bool {
	isObj := func(e ast.Expr) bool {
		id := identOf(e)

		return id != nil && objOf(info, id) == obj
	}

	var found bool

	ast.Inspect(scope, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.AssignStmt:
			for _, lhs := range s.Lhs {
				// the identifier a := declares is the variable's own declaration, not a change of it
				if id := identOf(lhs); id != nil && info.Defs[id] != nil {
					continue
				}

				found = found || isObj(lhs)
			}
		case *ast.IncDecStmt:
			found = found || isObj(s.X)
		case *ast.UnaryExpr:
			found = found || s.Op == token.AND && isObj(s.X)
		case *ast.RangeStmt:
			found = found || s.Tok == token.ASSIGN && (s.Key != nil && isObj(s.Key) || s.Value != nil && isObj(s.Value))
		}

		return !found
	})
	return found
}

// constVar reports whether the variable declared by name, with value, can be declared with const within scope, and the name of its type when it is inferred. explicit says whether the declaration writes its type.
func constVar(fset *token.FileSet, info *types.Info, scope ast.Node, name *ast.Ident, value ast.Expr, explicit, hoist bool) (string, bool) {
	obj, ok := info.Defs[name].(*types.Var)
	if !ok || obj.Parent() == nil || obj.Parent() == obj.Pkg().Scope() {
		return "", false
	}

	if tv, ok := info.Types[value]; !ok || tv.Value == nil {
		return "", false
	}

	var typ string

	if explicit {
		if basic, ok := obj.Type().Underlying().(*types.Basic); !ok || !constBasic(basic) {
			return "", false
		}
	} else {
		var ok bool
		if typ, ok = constType(obj); !ok {
			return "", false
		}
	}

	if modifiedIn(info, scope, obj) {
		return "", false
	}

	if hoist && conflictsIn(info, scope, name, obj) {
		return "", false
	}

	return typ, true
}

// constHoistCandidate returns the candidate of an if, for or switch statement whose := initialiser can move out in front of it.
func constHoistCandidate(fset *token.FileSet, info *types.Info, scope ast.Node, list *[]ast.Stmt, i int, st ast.Stmt, mk func(constKind, ast.Stmt, *ast.Ident, string) constCandidate) (constCandidate, bool) {
	var init ast.Stmt

	switch s := st.(type) {
	case *ast.ForStmt:
		init = s.Init
	case *ast.IfStmt:
		init = s.Init
	case *ast.SwitchStmt:
		init = s.Init
	default:
		return constCandidate{}, false
	}

	a, ok := defineOf(init)
	if !ok {
		return constCandidate{}, false
	}

	name := identOf(a.Lhs[0])
	typ, ok := constVar(fset, info, scope, name, a.Rhs[0], false, true)
	if !ok {
		return constCandidate{}, false
	}

	c := mk(constHoist, st, name, typ)
	c.unwrap = redundantConversion(a.Rhs[0], typ, defType(info, name), info)

	return c, true
}

// constCandidateOf returns the candidate that the statement st, at index i of list, makes, if it makes one. scope is the node that holds list.
func constCandidateOf(fset *token.FileSet, info *types.Info, scope ast.Node, list *[]ast.Stmt, i int, st ast.Stmt) (constCandidate, bool) {
	mk := func(kind constKind, stmt ast.Stmt, name *ast.Ident, typ string) constCandidate {
		return constCandidate{
			offset: fset.Position(name.Pos()).Offset,
			kind: kind,
			list: list,
			index: i,
			stmt: stmt,
			name: name,
			typ: typ,
		}
	}

	switch s := st.(type) {
	case *ast.AssignStmt:
		a, ok := defineOf(s)
		if !ok {
			return constCandidate{}, false
		}

		name := identOf(a.Lhs[0])
		typ, ok := constVar(fset, info, scope, name, a.Rhs[0], false, false)
		if !ok {
			return constCandidate{}, false
		}

		c := mk(constDefine, s, name, typ)
		c.unwrap = redundantConversion(a.Rhs[0], typ, defType(info, name), info)

		return c, true
	case *ast.DeclStmt:
		gd, ok := s.Decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR || len(gd.Specs) != 1 {
			return constCandidate{}, false
		}

		vs := gd.Specs[0].(*ast.ValueSpec)
		if len(vs.Names) != 1 || len(vs.Values) != 1 {
			return constCandidate{}, false
		}

		typ, ok := constVar(fset, info, scope, vs.Names[0], vs.Values[0], vs.Type != nil, false)
		if !ok {
			return constCandidate{}, false
		}

		// the type is the one written, or else the one that := gives
		declared := typ
		if id, ok := vs.Type.(*ast.Ident); ok {
			declared = id.Name
		}

		c := mk(constInPlace, s, vs.Names[0], typ)
		c.unwrap = redundantConversion(vs.Values[0], declared, defType(info, vs.Names[0]), info)

		return c, true
	}

	return constHoistCandidate(fset, info, scope, list, i, st, mk)
}

// constCandidates returns the candidates of file, which info types. The blocks are walked in the order of the file.
func constCandidates(fset *token.FileSet, file *ast.File, info *types.Info) []constCandidate {
	var cands []constCandidate

	ast.Inspect(file, func(n ast.Node) bool {
		var list *[]ast.Stmt

		switch b := n.(type) {
		case *ast.BlockStmt:
			list = &b.List
		case *ast.CaseClause:
			list = &b.Body
		case *ast.CommClause:
			list = &b.Body
		default:
			return true
		}

		for i, st := range *list {
			if c, ok := constCandidateOf(fset, info, n, list, i, st); ok {
				cands = append(cands, c)
			}
		}

		return true
	})
	return cands
}

// analyseConstants parses src as filename, and returns its file and candidates, checked with its package. It reports false when the package cannot be taken as a whole.
func analyseConstants(filename string, src []byte) (*token.FileSet, *ast.File, []constCandidate, bool) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, src, parser.ParseComments)
	if err != nil {
		return nil, nil, nil, false
	}

	files, ok := packageFiles(fset, filename, file)
	if !ok {
		return nil, nil, nil, false
	}

	_, info := checkModerniseFiles(fset, files, moduleGoVersion(filename))

	return fset, file, constCandidates(fset, file, info), true
}

// constantsWith returns src with the candidates at the offsets declared with const, or false when that is not possible, or adds a type error to the package, whose errors before are base.
func constantsWith(filename string, src []byte, offsets []int, base map[string]int) ([]byte, bool) {
	fset, file, cands, ok := analyseConstants(filename, src)
	if !ok {
		return nil, false
	}

	var chosen []constCandidate

	for _, c := range cands {
		if slices.Contains(offsets, c.offset) {
			chosen = append(chosen, c)
		}
	}

	if len(chosen) == 0 {
		return nil, false
	}
	declareConstants(file, chosen)

	out, err := printFile(fset, file)
	if err != nil {
		return nil, false
	}

	after, ok := packageErrors(filename, out)
	if !ok || introducedErrors(base, after) {
		return nil, false
	}

	return out, true
}

// constantsOnce declares as many of the candidates of src with const as can be, all at once if that is possible, and otherwise one at a time. It returns src unchanged when none can be.
func constantsOnce(filename string, src []byte) []byte {
	base, ok := packageErrors(filename, src)
	if !ok {
		return src
	}

	_, _, cands, ok := analyseConstants(filename, src)
	if !ok || len(cands) == 0 {
		return src
	}

	offsets := make([]int, 0, len(cands))

	for _, c := range cands {
		offsets = append(offsets, c.offset)
	}

	if out, ok := constantsWith(filename, src, offsets, base); ok {
		return out
	}

	var kept []int

	for _, off := range offsets {
		if _, ok := constantsWith(filename, src, []int{off}, base); ok {
			kept = append(kept, off)
		}
	}

	if len(kept) == 0 {
		return src
	}

	if out, ok := constantsWith(filename, src, kept, base); ok {
		return out
	}

	return src
}

// constantsSource returns src with the variables that can be declared with const declared so, repeating while that makes more of them constant. A file that cannot be taken as a package is returned unchanged.
func constantsSource(filename string, src []byte) []byte {
	for range maxConstantRounds {
		out := constantsOnce(filename, src)
		if bytes.Equal(out, src) {
			return src
		}

		src = out
	}

	return src
}
