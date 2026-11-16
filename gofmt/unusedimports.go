package main

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"path"
	"path/filepath"
	"strconv"
)

// guessName returns the name that an import of path most likely binds, which is the last element of the path. It is only a guess, used to decide whether the types are needed.
func guessName(importPath string) string {
	return path.Base(importPath)
}

// hasComment reports whether file has a comment between from and to.
func hasComment(file *ast.File, from, to token.Pos) bool {
	for _, cg := range file.Comments {
		if cg.Pos() >= from && cg.End() <= to {
			return true
		}
	}

	return false
}

// importedName returns the name that the unnamed import spec of path binds, as the type checker finds it in info. It reports false when the package cannot be imported, so its name is not known.
func importedName(spec *ast.ImportSpec, importPath, dir string, info *types.Info) (string, bool) {
	pn, ok := info.Implicits[spec].(*types.PkgName)
	if !ok {
		return "", false
	}

	if _, err := sharedImporter.ImportFrom(importPath, dir, 0); err != nil {
		return "", false
	}

	return pn.Name(), true
}

// lineEdit returns the edit that deletes the whole lines from the position from to the position to, with the newline that ends the last of them. It reports false when other code shares those lines, except a line comment after to.
func lineEdit(src []byte, fset *token.FileSet, from, to token.Pos) (textEdit, bool) {
	start := fset.Position(from).Offset
	end := fset.Position(to).Offset

	lineStart := bytes.LastIndexByte(src[:start], '\n') + 1
	if len(bytes.TrimSpace(src[lineStart:start])) != 0 {
		return textEdit{}, false
	}

	lineEnd := len(src)

	if nl := bytes.IndexByte(src[end:], '\n'); nl >= 0 {
		lineEnd = end + nl + 1
	}

	if rest := bytes.TrimSpace(src[end:lineEnd]); len(rest) != 0 && !bytes.HasPrefix(rest, []byte("//")) {
		return textEdit{}, false
	}

	return textEdit{
		start: lineStart,
		end: lineEnd,
	}, true
}

// This file implements the removal of unused imports, which -imports does on formatting, by default. A package name is used only as the base of a selector, such as fmt in fmt.Println, so an import whose name is not the base of any selector in the file is unused. An import without a name is only removed when its package can be imported, since its name comes from the package. An import that cannot be removed cleanly, because other code shares its lines, is kept.

// selectorBases returns the names that are the base of a selector in file.
func selectorBases(file *ast.File) map[string]bool {
	bases := map[string]bool{}

	ast.Inspect(file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}

		if id, ok := sel.X.(*ast.Ident); ok {
			bases[id.Name] = true
		}

		return true
	})

	return bases
}

// unusedImportSpecs returns the import specs of file that nothing in it uses. A dot import and a blank import are never unused.
func unusedImportSpecs(fset *token.FileSet, file *ast.File, dir string) []*ast.ImportSpec {
	bases := selectorBases(file)

	var (
		info *types.Info
		unused []*ast.ImportSpec
	)

	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}

		if spec.Name != nil {
			if name := spec.Name.Name; name != "_" && name != "." && !bases[name] {
				unused = append(unused, spec)
			}

			continue
		}

		if bases[guessName(importPath)] {
			continue
		}

		if info == nil {
			_, info = checkModernise(fset, file, "")
		}

		if name, ok := importedName(spec, importPath, dir, info); ok && !bases[name] {
			unused = append(unused, spec)
		}
	}

	return unused
}

// wholeDeclEdit returns the edit that deletes the import declaration gd, when every one of its specs is in gone and no comment lies in it.
func wholeDeclEdit(fset *token.FileSet, file *ast.File, src []byte, gd *ast.GenDecl, gone map[*ast.ImportSpec]bool) (textEdit, bool) {
	for _, spec := range gd.Specs {
		if is, ok := spec.(*ast.ImportSpec); !ok || !gone[is] {
			return textEdit{}, false
		}
	}

	if hasComment(file, gd.Pos(), gd.End()) {
		return textEdit{}, false
	}

	return lineEdit(src, fset, gd.Pos(), gd.End())
}

// importDeleteEdits returns the edits that delete the specs in unused from file. When every spec of an import declaration goes, and no comment lies in it, the whole declaration goes. A spec whose lines hold other code is kept.
func importDeleteEdits(fset *token.FileSet, file *ast.File, src []byte, unused []*ast.ImportSpec) (edits []textEdit) {
	gone := map[*ast.ImportSpec]bool{}

	for _, spec := range unused {
		gone[spec] = true
	}

	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.IMPORT {
			continue
		}

		if edit, ok := wholeDeclEdit(fset, file, src, gd, gone); ok {
			edits = append(edits, edit)
			continue
		}

		for _, spec := range gd.Specs {
			if is, ok := spec.(*ast.ImportSpec); ok && gone[is] {
				if edit, ok := lineEdit(src, fset, is.Pos(), is.End()); ok {
					edits = append(edits, edit)
				}
			}
		}
	}

	return
}

// removeUnusedImports returns src without the imports that nothing in it uses. It returns src unchanged when there are none, or when removing them would leave a file that does not parse.
func removeUnusedImports(filename string, src []byte) []byte {
	fset := token.NewFileSet()

	file, err := parser.ParseFile(fset, filename, src, parser.ParseComments)
	if err != nil {
		return src
	}

	unused := unusedImportSpecs(fset, file, filepath.Dir(filename))
	if len(unused) == 0 {
		return src
	}

	edits := importDeleteEdits(fset, file, src, unused)
	if len(edits) == 0 {
		return src
	}

	out := applyFixEdits(src, edits)

	if _, err := parser.ParseFile(token.NewFileSet(), filename, out, parser.ParseComments); err != nil {
		return src
	}

	return out
}
