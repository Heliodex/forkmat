package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// embeddedName returns the name an embedded field of type t has, or "".
func embeddedName(t ast.Expr) string {
	switch x := t.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.IndexExpr:
		return embeddedName(x.X)
	case *ast.IndexListExpr:
		return embeddedName(x.X)
	case *ast.SelectorExpr:
		return x.Sel.Name
	case *ast.StarExpr:
		return embeddedName(x.X)
	}

	return ""
}

// fieldNames returns the names of the fields of a struct type, in declaration order. A field without a name is embedded, and is named by its type. It reports false if a field has no name that can be found.
func fieldNames(st *ast.StructType) ([]string, bool) {
	var names []string

	for _, f := range st.Fields.List {
		if len(f.Names) > 0 {
			for _, id := range f.Names {
				names = append(names, id.Name)
			}

			continue
		}

		name := embeddedName(f.Type)
		if name == "" {
			return nil, false
		}

		names = append(names, name)
	}

	return names, true
}

// packageTypes returns the named types declared in the .go files of dir, by name. The file named skip is left out, and so is any file whose package is not pkg, unless pkg is "". A file that cannot be parsed contributes nothing.
func packageTypes(dir, skip, pkg string) map[string]ast.Expr {
	types := map[string]ast.Expr{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return types
	}

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".go" || name == skip || pkg == "" && strings.HasSuffix(name, "_test.go") {
			continue
		}

		f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if err != nil || pkg != "" && f.Name.Name != pkg {
			continue
		}

		for _, d := range f.Decls {
			if g, ok := d.(*ast.GenDecl); ok && g.Tok == token.TYPE {
				for _, sp := range g.Specs {
					ts := sp.(*ast.TypeSpec)
					types[ts.Name.Name] = ts.Type
				}
			}
		}
	}

	return types
}

// sortStructLiteral puts the keys of the literal x in the order of names, if the literal can be reordered (see the comment on structLiteralOrder).
func sortStructLiteral(x *ast.CompositeLit, names []string, comments []*ast.CommentGroup) {
	if len(x.Elts) < 2 {
		return
	}

	index := map[string]int{}

	for i, n := range names {
		if _, dup := index[n]; !dup {
			index[n] = i
		}
	}

	seen := map[string]bool{}

	for _, e := range x.Elts {
		kv, ok := e.(*ast.KeyValueExpr)
		if !ok {
			return
		}

		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			return
		}

		if _, ok := index[key.Name]; !ok || seen[key.Name] || !pureExpr(kv.Value, false) {
			return
		}

		seen[key.Name] = true
	}

	for _, cg := range comments {
		if cg.Pos() >= x.Lbrace && cg.Pos() < x.Rbrace {
			return
		}
	}

	elts := slices.Clone(x.Elts)

	slices.SortStableFunc(elts, func(a, b ast.Expr) int {
		return index[a.(*ast.KeyValueExpr).Key.(*ast.Ident).Name] - index[b.(*ast.KeyValueExpr).Key.(*ast.Ident).Name]
	})

	x.Elts = elts
}

// This file implements the field order of -strict: the keys of a struct literal are put in the order the fields are declared in. The printer cannot do this, since it has no declarations, so it runs here, on the parsed file.
//
// The type of a literal is found from its declaration when it is a struct type written in the literal, or a named type declared in the file, in a sibling file of the same package, or in a package the file imports (by qualifier, or with a dot). Other literals keep their order: an elided type, a generic type, a type that is not a struct, or a type that cannot be found. A literal is reordered only if every key is a field of the struct, and no key repeats, and it has no comment inside and no value that calls a function or receives from a channel.

// structLiteralOrder reorders the keys of the struct literals of file, in the declaration order of their types. filename is the file's name, which locates the other files of its package.
func structLiteralOrder(filename string, file *ast.File) {
	// named types by name, in this file and the package's other files
	dir := filepath.Dir(filename)

	if !onDisk(filename) {
		dir = "" // read from standard input, there are no sibling files
	}

	types := packageTypes(dir, filepath.Base(filename), file.Name.Name)

	for _, d := range file.Decls {
		if g, ok := d.(*ast.GenDecl); ok && g.Tok == token.TYPE {
			for _, sp := range g.Specs {
				ts := sp.(*ast.TypeSpec)
				types[ts.Name.Name] = ts.Type
			}
		}
	}

	// the packages the file imports: a qualifier maps to its import path, and the exported types of dot-imported packages are added to types. A qualified package is only read when a literal's type refers to it.
	paths := map[string]string{}

	for _, imp := range file.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}

		if imp.Name != nil && imp.Name.Name == "." {
			for name, t := range packageTypes(packageDir(path, filepath.Dir(filename)), "", "") {
				if ast.IsExported(name) {
					types[name] = t
				}
			}

			continue
		}

		name := path[strings.LastIndex(path, "/")+1:]

		if imp.Name != nil {
			name = imp.Name.Name
		}

		paths[name] = path
	}

	loaded := map[string]map[string]ast.Expr{}
	qualifiedType := func(qualifier, name string) ast.Expr {
		pkgTypes, ok := loaded[qualifier]
		if !ok {
			if path, known := paths[qualifier]; known {
				pkgTypes = packageTypes(packageDir(path, filepath.Dir(filename)), "", "")
			}

			loaded[qualifier] = pkgTypes
		}

		return pkgTypes[name]
	}
	resolve := func(t ast.Expr) *ast.StructType {
		// an alias or a named type of a named type is followed, to a fixed depth
		for range 8 {
			switch x := t.(type) {
			case *ast.Ident:
				next, ok := types[x.Name]
				if !ok {
					return nil
				}

				t = next
			case *ast.SelectorExpr:
				X, ok := x.X.(*ast.Ident)
				if !ok {
					return nil
				}

				next := qualifiedType(X.Name, x.Sel.Name)
				if next == nil {
					return nil
				}

				t = next
			case *ast.StructType:
				return x
			default:
				return nil
			}
		}

		return nil
	}

	ast.Inspect(file, func(n ast.Node) bool {
		x, ok := n.(*ast.CompositeLit)
		if !ok || x.Type == nil {
			return true
		}

		st := resolve(x.Type)
		if st == nil {
			return true
		}

		if order, ok := fieldNames(st); ok {
			sortStructLiteral(x, order, file.Comments)
		}

		return true
	})
}
