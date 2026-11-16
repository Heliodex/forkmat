package main

import "testing"

func TestRemoveUnusedImports(t *testing.T) {
	tests := []struct {
		name, src, want string
	}{
		{
			name: "single unused import is deleted",
			src: "package main\n\nimport \"fmt\"\n\nfunc f() {}\n",
			want: "package main\n\n\nfunc f() {}\n",
		},
		{
			name: "unused import in a group is deleted, the used one kept",
			src: "package main\n\nimport (\n\t\"fmt\"\n\t\"os\" // c\n\t\"strings\"\n)\n\nfunc f() { fmt.Print(strings.ToUpper(\"\")) }\n",
			want: "package main\n\nimport (\n\t\"fmt\"\n\t\"strings\"\n)\n\nfunc f() { fmt.Print(strings.ToUpper(\"\")) }\n",
		},
		{
			name: "a group with every import unused goes with its declaration",
			src: "package main\n\nimport (\n\t\"fmt\"\n\t\"os\"\n)\n\nfunc f() {}\n",
			want: "package main\n\n\nfunc f() {}\n",
		},
		{
			name: "an unused explicit name is deleted",
			src: "package main\n\nimport str \"bytes\"\n\nfunc f() {}\n",
			want: "package main\n\n\nfunc f() {}\n",
		},
		{
			name: "a blank and a dot import are kept",
			src: "package main\n\nimport (\n\t_ \"embed\"\n\t. \"math\"\n)\n\nfunc f() {}\n",
			want: "package main\n\nimport (\n\t_ \"embed\"\n\t. \"math\"\n)\n\nfunc f() {}\n",
		},
		{
			name: "an import that cannot be resolved is kept",
			src: "package main\n\nimport \"example.com/nope\"\n\nfunc f() {}\n",
			want: "package main\n\nimport \"example.com/nope\"\n\nfunc f() {}\n",
		},
		{
			name: "an import sharing its line with other code is kept",
			src: "package main\n\nimport \"fmt\"; import \"os\"\n\nfunc f() { fmt.Print() }\n",
			want: "package main\n\nimport \"fmt\"; import \"os\"\n\nfunc f() { fmt.Print() }\n",
		},
		{
			name: "a used import is kept",
			src: "package main\n\nimport \"fmt\"\n\nfunc f() { fmt.Print() }\n",
			want: "package main\n\nimport \"fmt\"\n\nfunc f() { fmt.Print() }\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(removeUnusedImports("x.go", []byte(tt.src))); got != tt.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}
