package src

import (
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/codeliter/codeforge/src/forms"
)

const mapUpload = "package p\n\nfunc Upload(f File) error {\n\treturn retry(func() error {\n\t\treturn sendOnce(f)\n\t})\n}\n\n" +
	"func sendOnce(f File) error {\n\tif f.Empty() {\n\t\treturn nil\n\t}\n\treturn post(f)\n}\n"

const mapRetry = "package p\n\nconst tries = 3\n\nfunc retry(do func() error) (err error) {\n\tfor range tries {\n\t\tif err = do(); err == nil {\n\t\t\treturn nil\n\t\t}\n\t}\n\treturn err\n}\n"

func TestWalkMap_ShowsWhichChangedCodeUsesWhich(t *testing.T) {
	files := map[string]string{"upload.go": mapUpload, "retry.go": mapRetry, "README.md": "# Uploads\n\nUploads are retried.\n"}
	read := func(file string) (string, bool) { c, ok := files[file]; return c, ok }
	stops := []walkStop{
		{Path: "upload.go", Line: 5, Text: "the send"},   // inside Upload
		{Path: "retry.go", Line: 7, Text: "the loop"},    // inside retry
		{Path: "upload.go", Line: 9, Text: "one try"},    // sendOnce, on its header
		{Path: "README.md", Line: 3, Text: "the doc"},    // no declaration
		{Path: "gone.go", Line: 1, Text: "not readable"}, // no file
	}
	nodes := walkMap(stops, read)
	require.Equal(t, []mapNode{{Name: "Upload", Uses: []int{1, 2}}, {Name: "retry"}, {Name: "sendOnce"}, {}, {}}, nodes,
		"a stop is the declaration it sits in, and uses the stops whose names are written in it")

	tree := walkMapText(stops, nodes, codeStyle{width: 100})
	require.Equal(t, "1 Upload  upload.go:5\n├─▶ 2 retry  retry.go:7\n└─▶ 3 sendOnce  upload.go:9\n\n"+
		"On their own\n4 README.md  README.md:3\n5 gone.go  gone.go:1", tree)

	require.Empty(t, walkMapText(stops[1:], walkMap(stops[1:], read), codeStyle{}), "stops that do not use each other make no map")

	// Two stops that use each other are both drawn, and the second time one is only named.
	loop := map[string]string{"a.go": "package p\n\nfunc ping() {\n\tpong()\n}\n\nfunc pong() {\n\tping()\n}\n"}
	pair := []walkStop{{Path: "a.go", Line: 4}, {Path: "a.go", Line: 8}}
	round := walkMapText(pair, walkMap(pair, func(f string) (string, bool) { c, ok := loop[f]; return c, ok }), codeStyle{})
	require.Equal(t, "1 ping  a.go:4\n└─▶ 2 pong  a.go:8\n    └─▶ 1 ping  a.go:4  (shown above)", round)

	// A method is part of its class: the class does not use it.
	class := map[string]string{"c.py": "class Queue:\n    def push(self, x):\n        self.items.append(x)\n\ndef fill(q):\n    q.push(1)\n"}
	inner := []walkStop{{Path: "c.py", Line: 1}, {Path: "c.py", Line: 3}, {Path: "c.py", Line: 6}}
	got := walkMap(inner, func(f string) (string, bool) { c, ok := class[f]; return c, ok })
	require.Equal(t, []mapNode{{Name: "Queue"}, {Name: "push"}, {Name: "fill", Uses: []int{1}}}, got)
}

func TestWalkMap_AStopDeepInALongFunctionIsStillThatFunction(t *testing.T) {
	long := "package p\n\nfunc big() {\n" + strings.Repeat("\tstep()\n", 150) + "\thelper()\n}\n\nfunc helper() {}\n"
	stops := []walkStop{{Path: "a.go", Line: 140}, {Path: "a.go", Line: 157}}
	nodes := walkMap(stops, func(string) (string, bool) { return long, true })
	require.Equal(t, []mapNode{{Name: "big", Uses: []int{1}}, {Name: "helper"}}, nodes)
	stops[0].Line = 155 // its closing brace
	require.Equal(t, "big", walkMap(stops, func(string) (string, bool) { return long, true })[0].Name)
	require.False(t, wholeWord("fetch").MatchString("fetch$data()"), "a longer name that holds it is another name")

	// Where indentation does not say what a line sits in, brackets do.
	for name, src := range map[string]string{
		"code that is not indented":          "package p\n\nfunc caller() {\nhelper()\n}\n",
		"a header that ends on its own line": "package p\n\nfunc caller(\n\ta int,\n) error {\n\thelper()\n\treturn nil\n}\n",
		"a brace on a line of its own":       "package p\n\nfunc caller()\n{\nhelper()\n}\n",
	} {
		all := snippetLines(src)
		at := slices.IndexFunc(all, func(l string) bool { return strings.TrimSpace(l) == "helper()" }) + 1
		_, got := stopDeclaration("a.go", all, at)
		require.Equal(t, "caller", got, name)
	}
	_, nested := stopDeclaration("a.js", snippetLines("function outer() {\n  function $fetch() {\n    go()\n  }\n}\n"), 2)
	require.Equal(t, "$fetch", nested, "a declaration inside another is itself")
	_, spread := stopDeclaration("a.js", snippetLines("function outer() {\n  const rows = [\n    helper(),\n  ]\n  after()\n}\n"), 3)
	require.Equal(t, "outer", spread, "a local value spread over lines is part of its function too")
	inner := "function outer() {\n  function inner() {\n    go()\n  }\n  inner()\n}\n"
	nest := []walkStop{{Path: "a.js", Line: 1}, {Path: "a.js", Line: 3}}
	require.Equal(t, []mapNode{{Name: "outer", Uses: []int{1}}, {Name: "inner"}}, walkMap(nest, func(string) (string, bool) { return inner, true }),
		"declaring an inner function is not using it, calling it is")
	for name, c := range map[string]struct{ file, src, at, want string }{
		"a Python header that ends on its own line": {"a.py", "class Queue:\n    def push(\n        self,\n    ):\n        go()\n", "go()", "push"},
		"a nested header that ends on its own line": {"a.js", "function outer() {\n  function inner(\n    a,\n  ) {\n    go()\n  }\n}\n", "go()", "inner"},
		"a const that is not indented":              {"a.js", "function outer() {\nconst rows = [\n1,\n]\n}\n", "const rows = [", "outer"},
		"a let that is not indented":                {"a.js", "function outer() {\nlet rows = [\n1,\n]\n}\n", "let rows = [", "outer"},
		"a var that is not indented":                {"a.go", "package p\n\nfunc outer() {\nvar rows = []int{\n1,\n}\n_ = rows\n}\n", "var rows = []int{", "outer"},
		"a value at the top level":                  {"a.js", "const rows = [\n  1,\n]\n", "const rows = [", "rows"},
		"a comment at the margin":                   {"a.py", "def caller():\n    one()\n# why\n    go()\n", "go()", "caller"},
		"a long string at the margin":               {"a.py", "def caller():\n    s = '''\nfunc text():\n'''\n    go()\n", "go()", "caller"},
		"a comment in a language not known":         {"a.zzz", "def caller():\n    one()\n# why\n    go()\n", "go()", "caller"},
		"a declaration written in a long string":    {"a.py", "def caller():\n    s = '''\n    def fake():\n    '''\n    go()\n", "def fake():", "caller"},
		"a declaration written in a comment":        {"a.go", "package p\n\nfunc caller() {\n\t/*\n\tfunc fake() {\n\t*/\n\tgo1()\n}\n", "func fake() {", "caller"},
		"a stop on a brace on a line of its own":    {"a.js", "function caller()\n{ // body\nhelper()\n}\nfunction helper() {}\n", "{ // body", "caller"},
		"a nested brace on a line of its own":       {"a.js", "function outer() {\n  function inner()\n  {\n    go()\n  }\n}\n", "go()", "inner"},
		"a function kept in a local":                {"a.js", "function outer() {\n  const helper = () => {\n    go()\n  }\n}\n", "go()", "outer"},
	} {
		all := snippetLines(c.src)
		at := slices.IndexFunc(all, func(l string) bool { return strings.TrimSpace(l) == c.at }) + 1
		_, got := stopDeclaration(c.file, all, at)
		require.Equal(t, c.want, got, name)
	}
	_, local := stopDeclaration("a.js", snippetLines("function outer() {\n  const rows = load()\n}\n"), 2)
	require.Equal(t, "outer", local, "a local value is part of its function")
}

func TestDeclName(t *testing.T) {
	for line, want := range map[string]string{
		"func (c *Client) Send(ctx context.Context) error {": "Send",
		"func retry(do func() error) error {":                "retry",
		"var explainSession = func(ctx context.Context) {":   "explainSession",
		"export default async function load(id) {":           "load",
		"    def push(self, x):":                             "push",
		"pub(crate) fn parse(s: &str) -> Ast {":              "parse",
		"deploy() {":                                         "deploy",
		"def café(self):":                                    "café",
		"function fetch$data(id) {":                          "fetch$data",
		"if err != nil {":                                    "",
		"func(x int) bool {":                                 "",
		"func (s *Set[T]) Add[K any](k K) {":                 "Add",
		"x := retry(do)":                                     "",
	} {
		require.Equal(t, want, declName(line), line)
	}
}

func TestWithMap_ComesAfterTheOverview(t *testing.T) {
	files := map[string]string{"upload.go": mapUpload, "retry.go": mapRetry}
	r := &codeReader{read: func(file string) (string, bool) { c, ok := files[file]; return c, ok }, path: func(f string) string { return f }}
	stops := []walkStop{{Path: "upload.go", Line: 5, Text: "a"}, {Path: "retry.go", Line: 7, Text: "b"}}
	w := walkthrough{Overview: "Retries uploads.", Stops: stops}
	st := codeStyle{width: 100}
	tabs := func(s forms.Steps) (out []string) {
		for _, it := range s.Items {
			out = append(out, it.Tab)
		}
		return out
	}
	steps := withMap(walkthroughSteps(w, st, nil, false), stops, r, st)
	require.Equal(t, []string{"Overview", "Map", "1", "2"}, tabs(steps), "the whole before its parts; a stop's number still opens it")
	require.Contains(t, steps.Items[1].Body, "└─▶ 2 retry")
	require.Equal(t, []string{"Overview", "1", "2"}, tabs(withMap(walkthroughSteps(w, st, nil, false), stops, nil, st)), "no code to read, no map")
}
