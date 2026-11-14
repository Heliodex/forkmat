package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strings"
	"testing"
)

// literalKeys returns the keys of each composite literal of src, in source order, after the struct literal order has been applied. Each literal is written as its keys joined by commas, or "-" when it has no keyed elements.
func literalKeys(t *testing.T, src string) []string {
	t.Helper()

	file, err := parser.ParseFile(token.NewFileSet(), "x.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	structLiteralOrder("x.go", file)

	var out []string

	ast.Inspect(file, func(n ast.Node) bool {
		x, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}

		var keys []string

		for _, e := range x.Elts {
			if kv, ok := e.(*ast.KeyValueExpr); ok {
				if id, ok := kv.Key.(*ast.Ident); ok {
					keys = append(keys, id.Name)
				}
			}
		}

		if len(keys) == 0 {
			keys = []string{"-"}
		}

		out = append(out, strings.Join(keys, ","))

		return true
	})
	return out
}

func TestStructLiteralOrder(t *testing.T) {
	tests := []struct {
		name, src string
		want []string // the keys of the literals, in source order
	}{
		{
			name: "keys follow the declaration",
			src: "package p\n\ntype S struct{ B, A int; C string }\n\nvar v = S{C: \"c\", A: 1, B: 2}\n",
			want: []string{"B,A,C"},
		},
		{
			name: "an anonymous struct type follows its own declaration",
			src: "package p\n\nvar v = struct{ Y, X int }{X: 1, Y: 2}\n",
			want: []string{"Y,X"},
		},
		{
			name: "an unresolved type keeps its order",
			src: "package p\n\nvar v = Unknown{B: 1, A: 2}\n",
			want: []string{"B,A"},
		},
		{
			name: "a value with a call keeps the order",
			src: "package p\n\ntype S struct{ B, A int }\n\nvar v = S{A: f(), B: 2}\n\nfunc f() int { return 1 }\n",
			want: []string{"A,B"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := literalKeys(t, tt.src); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("keys = %v, want %v", got, tt.want)
			}
		})
	}
}
