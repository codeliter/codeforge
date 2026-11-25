package src

import (
	"math"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/codeliter/codeforge/src/forms"
)

// The map of a tour shows how its stops connect: which changed code uses which. It is read from the code itself, not
// asked of the model: a stop is the declaration it sits in, and it uses another stop when that declaration's name is
// written in it. That is a reference by name, so a common name can link two stops that have nothing to do with each
// other; the map says "uses", and the reader has the code a key away.

// minMapName is the shortest declared name the map follows: a shorter one is found everywhere.
const minMapName = 3

// mapNode is a stop on the map: the name its declaration declares ("" when it sits in none) and the stops it uses.
type mapNode struct {
	Name string
	Uses []int
}

var (
	// A method has a name before its parameters: "func(x int) bool {" is a function with no name, not one called bool.
	declMethod = regexp.MustCompile(`^func\s*\([^)]*\)\s*([\p{L}_$][\p{L}\p{M}\p{N}_$]*)\s*[(\[]`)
	declNamed  = regexp.MustCompile(`^(?:(?:export|default|pub(?:\([^)]*\))?|async|static|public|private|protected|abstract|declare)\s+)*` +
		`(?:func|function\*?|def|class|fn|type|struct|enum|trait|interface|module|const|let|var)\s+([\p{L}_$][\p{L}\p{M}\p{N}_$]*)`)
	declShell = regexp.MustCompile(`^([\p{L}_$][\p{L}\p{M}\p{N}_$.-]*)\s*\(\)\s*\{`)
)

// declName is the name a line declares in common languages, "" when it declares none.
func declName(l string) string {
	t := strings.TrimSpace(l)
	for _, re := range []*regexp.Regexp{declMethod, declNamed, declShell} {
		if m := re.FindStringSubmatch(t); m != nil {
			return m[1]
		}
	}
	return ""
}

// stopDeclaration finds the declaration line sits in: its first line and the name it declares, or 0 and "".
func stopDeclaration(file string, all []string, line int) (start int, name string) {
	if line < 1 || line > len(all) {
		return 0, ""
	}
	// An opening brace on a line of its own belongs to the header above it.
	for line > 1 && (onlyBrace(all[line-1]) || (strings.TrimSpace(all[line-1]) == "" && onlyBraceBelow(all, line))) {
		line--
	}
	// The code without its comments and strings, where the language is known: a comment or a line of a long string
	// written at the margin says nothing of where the code around it sits.
	code, depth, literal, known := scanLines(file, all)
	inBrackets := func(n int) bool { return known && depth[n-1] > 0 }
	aside := func(n int) bool {
		t := strings.TrimSpace(code[n-1])
		return t == "" || literal[n-1] || (!known && (strings.HasPrefix(t, "#") || strings.HasPrefix(t, "//")))
	}
	// A value declared inside a function is a local, no declaration to name the stop by: indentation says it is
	// inside one, and brackets do where the code is not indented.
	local := func(n int) bool {
		l := all[n-1]
		return localValue.MatchString(strings.TrimSpace(l)) && (indentOf(l) > 0 || inBrackets(n))
	}
	// The line itself, when it declares.
	if !local(line) && !aside(line) {
		if name := declName(all[line-1]); name != "" {
			return line, name
		}
	}
	// Up through ever less indented lines, however long the declaration is.
	target := indentOf(all[line-1])
	switch t := strings.TrimSpace(all[line-1]); {
	case aside(line):
		target = math.MaxInt
	case closesBlock(t):
		target++ // a declaration's closing line: it starts at this indentation, further up
	}
	for n := line - 1; n >= 1 && target > 0; n-- {
		l := all[n-1]
		t := strings.TrimSpace(l)
		if aside(n) || indentOf(l) >= target {
			continue
		}
		target = indentOf(l)
		if strings.HasPrefix(t, ")") || onlyBrace(l) {
			target++ // a brace on a line of its own, or the last line of a header, "):" or ") error {": the header starts at this indentation, further up
			continue
		}
		if local(n) {
			continue // a local whose value spans lines: the stop is in the function that holds it
		}
		if name := declName(l); name != "" {
			return n, name
		}
	}
	// Indentation found none: the code is not indented, or the declaration's header ends on a line of its own
	// (") error {"). Brackets still say where the line sits: the outermost one open here was opened by the nearest
	// line above that starts outside every bracket.
	if !inBrackets(line) {
		return 0, ""
	}
	n := line
	for n > 1 && depth[n-1] > 0 {
		n--
	}
	for n > 1 && (onlyBrace(all[n-1]) || strings.TrimSpace(all[n-1]) == "") {
		n-- // a brace on a line of its own belongs to the header above it
	}
	if name := declName(all[n-1]); name != "" {
		return n, name
	}
	return 0, ""
}

// onlyBraceBelow reports whether the first line with text after line is an opening brace alone.
func onlyBraceBelow(all []string, line int) bool {
	for n := line + 1; n <= len(all); n++ {
		if strings.TrimSpace(all[n-1]) != "" {
			return onlyBrace(all[n-1])
		}
	}
	return false
}

// localValue matches the start of a line that declares a value, which inside a function is a local.
var localValue = regexp.MustCompile(`^(const|let|var)\b`)

// walkMap reads how the stops connect. read gives a file's text; a stop whose file cannot be read is on the map
// with no name and no links.
func walkMap(stops []walkStop, read func(file string) (string, bool)) []mapNode {
	type extent struct {
		path     string
		from, to int
		lines    []string
		text     string
	}
	nodes, extents := make([]mapNode, len(stops)), make([]extent, len(stops))
	files := map[string][]string{}
	for i, s := range stops {
		path := filepath.Clean(s.Path)
		all, ok := files[path]
		if !ok {
			if content, found := read(s.Path); found {
				all = snippetLines(content)
			}
			files[path] = all
		}
		if s.Line < 1 || s.Line > len(all) {
			continue
		}
		from, to := s.Line, min(len(all), max(s.Line, s.End))
		start, name := stopDeclaration(s.Path, all, s.Line)
		if start > 0 {
			end := indentedEnd(all, start)
			if code, _, literal, known := scanLines(s.Path, all); known {
				end, _ = declarationEnd(code, literal, start)
			}
			from, to = start, min(len(all), max(end, to))
		}
		nodes[i].Name = name
		extents[i] = extent{path: path, from: from, to: to, lines: all[from-1 : to], text: strings.Join(all[from-1:to], "\n")}
	}
	words := make([]*regexp.Regexp, len(nodes))
	for j, n := range nodes {
		if len(n.Name) >= minMapName {
			words[j] = wholeWord(n.Name)
		}
	}
	for i, a := range extents {
		for j, b := range extents {
			if i == j || a.text == "" || words[j] == nil || nodes[j].Name == nodes[i].Name {
				continue
			}
			text := a.text
			if a.path == b.path && b.from >= a.from && b.from <= a.to {
				// Declared inside it: declaring a method or an inner function is not using it, a call elsewhere in it is.
				text = strings.Join(a.lines[:b.from-a.from], "\n") + "\n" + strings.Join(a.lines[min(len(a.lines), b.to-a.from+1):], "\n")
			}
			if words[j].MatchString(text) {
				nodes[i].Uses = append(nodes[i].Uses, j)
			}
		}
	}
	return nodes
}

// wholeWord matches name where it stands as a whole word.
func wholeWord(name string) *regexp.Regexp {
	return regexp.MustCompile(`(^|[^\p{L}\p{M}\p{N}_$])` + regexp.QuoteMeta(name) + `($|[^\p{L}\p{M}\p{N}_$])`)
}

// walkMapText draws the map as a tree: each stop that nothing uses at the margin, and under each stop the stops it
// uses. A stop already drawn is named again and not opened again; stops with no link are listed last. It is "" when
// no stop uses another, since there is then nothing to draw.
func walkMapText(stops []walkStop, nodes []mapNode, st codeStyle) string {
	used := make([]bool, len(nodes))
	links := 0
	for _, n := range nodes {
		for _, j := range n.Uses {
			used[j] = true
			links++
		}
	}
	if links == 0 {
		return ""
	}
	label := func(i int) string {
		name := nodes[i].Name
		if name == "" {
			name = filepath.Base(stops[i].Path)
		}
		return st.paint(st.pal.accent, strconv.Itoa(i+1)) + " " + st.paint(st.pal.bold, name) + "  " + st.paint(st.pal.dim, walkPlace(stops[i]))
	}
	var b strings.Builder
	drawn := make([]bool, len(nodes))
	var draw func(i int, lead, under string)
	draw = func(i int, lead, under string) {
		again := drawn[i] && len(nodes[i].Uses) > 0
		b.WriteString(lead + label(i))
		if again {
			b.WriteString(st.paint(st.pal.dim, "  (shown above)"))
		}
		b.WriteString("\n")
		if drawn[i] {
			return
		}
		drawn[i] = true
		for k, j := range nodes[i].Uses {
			branch, next := "├─▶ ", "│   "
			if k == len(nodes[i].Uses)-1 {
				branch, next = "└─▶ ", "    "
			}
			draw(j, under+st.paint(st.pal.dim, branch), under+st.paint(st.pal.dim, next))
		}
	}
	// What nothing uses starts a tree; stops that only use each other have no such start, and the first of them is one.
	for i, n := range nodes {
		if !used[i] && len(n.Uses) > 0 {
			draw(i, "", "")
			b.WriteString("\n")
		}
	}
	for i, n := range nodes {
		if !drawn[i] && len(n.Uses) > 0 {
			draw(i, "", "")
			b.WriteString("\n")
		}
	}
	var alone []string
	for i := range nodes {
		if !drawn[i] {
			alone = append(alone, label(i))
		}
	}
	if len(alone) > 0 {
		b.WriteString(st.paint(st.pal.dim, "On their own") + "\n" + strings.Join(alone, "\n") + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// withMap adds the map to a tour's steps, after the overview and what to know first: where the reader sees the whole
// before its parts. A tour whose stops do not use each other gets none.
func withMap(steps forms.Steps, stops []walkStop, r *codeReader, st codeStyle) forms.Steps {
	if r == nil {
		return steps
	}
	tree := walkMapText(stops, walkMap(stops, r.read), st)
	if tree == "" {
		return steps
	}
	at := 0
	for at < len(steps.Items) && (steps.Items[at].Tab == "Overview" || steps.Items[at].Tab == "Know first") {
		at++
	}
	intro := strings.TrimRight(wrapSpans(st, max(0, st.width), "", "", span{
		text: "How the changes connect. An arrow goes from code to the changed code it uses, by name. Type a stop's number to read it.",
		code: st.pal.dim,
	}), "\n")
	step := forms.Step{Tab: "Map", Body: intro + "\n\n" + tree}
	steps.Items = append(steps.Items[:at:at], append([]forms.Step{step}, steps.Items[at:]...)...)
	return steps
}
