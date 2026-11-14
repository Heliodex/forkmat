package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// caseOrder returns, for each switch of src after switchCaseOrder, its clauses'
// case expressions in order ("default" for the default clause).
func caseOrder(t *testing.T, src string) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "x.go", src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	switchCaseOrder(file)
	var out []string
	ast.Inspect(file, func(n ast.Node) bool {
		s, ok := n.(*ast.SwitchStmt)
		if !ok {
			return true
		}
		var names []string
		for _, cl := range s.Body.List {
			c := cl.(*ast.CaseClause)
			if c.List == nil {
				names = append(names, "default")
				continue
			}
			var parts []string
			for _, e := range c.List {
				parts = append(parts, e.(*ast.Ident).Name)
			}
			names = append(names, strings.Join(parts, ","))
		}
		out = append(out, strings.Join(names, " "))
		return true
	})
	return out
}

func TestSwitchCaseOrder(t *testing.T) {
	const consts = "package p\n\nconst (\n\tKindA = iota\n\tKindC\n\tKindB\n)\n\n"
	tests := []struct {
		name, body string
		want       string
	}{
		{
			name: "cases follow the declaration, the default keeps its slot",
			body: "switch k {\ncase KindB:\nreturn\ncase KindA:\nreturn\ndefault:\nreturn\ncase KindC:\nreturn\n}",
			want: "KindA KindC default KindB",
		},
		{
			name: "a clause that does not return keeps the switch",
			body: "switch k {\ncase KindB:\nprintln()\ncase KindA:\nreturn\n}",
			want: "KindB KindA",
		},
		{
			name: "a case that is not a declared constant keeps the switch",
			body: "switch k {\ncase Other:\nreturn\ncase KindA:\nreturn\n}",
			want: "Other KindA",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := consts + "func f(k int) {\n" + tt.body + "\n}\n"
			if got := strings.Join(caseOrder(t, src), ";"); got != tt.want {
				t.Errorf("order = %q, want %q", got, tt.want)
			}
		})
	}
}
