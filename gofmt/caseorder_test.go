package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func TestCaseListOrder(t *testing.T) {
	tests := []struct {
		name, switchSrc, want string
	}{
		{
			name: "numbers sort by value, names after them alphabetically",
			switchSrc: "switch k {\ncase Zed, 10, Alpha, -1, 2.5:\n}",
			want: "-1,2.5,10,Alpha,Zed",
		},
		{
			name: "strings sort alphabetically, before names",
			switchSrc: "switch k {\ncase Name, \"b\", \"a\":\n}",
			want: "\"a\",\"b\",Name",
		},
		{
			name: "kinds go nil, booleans, numbers, strings",
			switchSrc: "switch k {\ncase \"x\", 2, true, nil, false, 1:\n}",
			want: "nil,false,true,1,2,\"x\"",
		},
		{
			name: "characters sort among the numbers",
			switchSrc: "switch k {\ncase 'z', 'a', 100:\n}",
			want: "'a',100,'z'",
		},
		{
			name: "a call keeps the list",
			switchSrc: "switch k {\ncase 3, g(), 1:\n}",
			want: "3,g(),1",
		},
		{
			name: "a comment keeps the list",
			switchSrc: "switch k {\ncase 2, /* c */ 1:\n}",
			want: "2,1",
		},
		{
			name: "types sort alphabetically",
			switchSrc: "switch k.(type) {\ncase string, *int, bool, nil, []byte, I:\n}",
			want: "*int,I,[]byte,bool,nil,string",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := "package p\n\nfunc f(k any) {\n" + tt.switchSrc + "\n}\n"

			file, err := parser.ParseFile(token.NewFileSet(), "x.go", src, parser.ParseComments|parser.SkipObjectResolution)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}

			caseListOrder(file)

			var got []string

			ast.Inspect(file, func(n ast.Node) bool {
				if c, ok := n.(*ast.CaseClause); ok {
					for _, e := range c.List {
						got = append(got, exprText(e))
					}
				}

				return true
			})

			if g := strings.Join(got, ","); g != tt.want {
				t.Errorf("list = %q, want %q", g, tt.want)
			}
		})
	}
}
