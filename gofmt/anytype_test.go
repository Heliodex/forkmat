package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// anyText returns the types of the parameters of f in src after anyTypes, each
// written as it prints: "interface{}" for an empty interface, or the type's name.
func anyText(t *testing.T, src string) string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "x.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	anyTypes(file)
	var out []string
	ast.Inspect(file, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.Ident:
			if x.Name == "any" {
				out = append(out, "any")
			}
		case *ast.InterfaceType:
			out = append(out, "interface{}")
		}
		return true
	})
	return strings.Join(out, ",")
}

func TestAnyTypes(t *testing.T) {
	tests := []struct{ name, src, want string }{
		{
			name: "an empty interface becomes any",
			src:  "package p\n\nvar x interface{}\n",
			want: "any",
		},
		{
			name: "nested empty interfaces become any",
			src:  "package p\n\nvar x []map[string]interface{}\n",
			want: "any",
		},
		{
			name: "an interface with methods is kept",
			src:  "package p\n\nvar x interface{ M() }\n",
			want: "interface{}",
		},
		{
			name: "a file that declares any keeps interface{}",
			// the declaration's own name, then the interface kept as it is
			src:  "package p\n\ntype any int\n\nvar x interface{}\n",
			want: "any,interface{}",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := anyText(t, tt.src); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
