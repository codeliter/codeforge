package review

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Logic bugs hide in the small tokens that read as plausible: an inverted comparison, a boundary off by one, && for
// ||, arguments swapped in a comparator. A reviewer that found one bug in a function tends to move on, and one that
// reads 70 KB of diff skims. The checklist names every added code line that holds such a token, so each one is looked
// at whatever the reviewer's attention did.

var (
	// checkedToken matches comparisons, boolean operators, bounds and one-off arithmetic.
	checkedToken = regexp.MustCompile(`<=|>=|==|!=|&&|\|\||[^<>=!:]<[^<=-]|[^-=<>]>[^>=]|\bmin\(|\bmax\(|[+-]\s*1\b|\blen\(|\bcompare|\bCompare|\bsort|\bSort|\bsubtle|\[[^\]]*:[^\]]*\]`)
	// noise lines say nothing about logic: comments, imports, struct tags, log and format strings only, and Go's
	// error check idiom, which is never the subtle one.
	noiseLine = regexp.MustCompile(`^\s*(//|#|\*|/\*|import\b|"[^"]*"\s*,?\s*$)|^\}?\s*(else\s+)?if\s+(\w+(,\s*\w+)*\s*:?=\s*[^;]+;\s*)?err\s*!=\s*nil\s*\{$`)
	// sharpToken marks the lines most likely to hide a plausible-looking slip: ordering and boolean combination,
	// bounds and comparators. They go first, so a long checklist cut at its cap keeps them.
	sharpToken = regexp.MustCompile(`<=|>=|&&|\|\||[^<>=!:]<[^<=-]|[^-=<>]>[^>=]|\bmin\(|\bmax\(|[+-]\s*1\b|\bcompare|\bCompare`)
)

// maxAuditLines keeps the checklist to what a reviewer can go through within its steps: ordering, boolean and bound
// lines first, then equality checks, each in diff order.
const maxAuditLines = 80

// AuditLines is the checklist of added code lines holding a comparison, boolean operator or bound, each as
// "path:line: code", for the reviewer to account for one by one. Tests and docs are left out: a wrong comparison there
// fails loudly on its own.
func AuditLines(diff string) []string {
	var sharp, plain []string
	collect := func(entry, code string) {
		if sharpToken.MatchString(" " + code + " ") {
			sharp = append(sharp, entry)
		} else {
			plain = append(plain, entry)
		}
	}
	file, line, inHunk := "", 0, false
	for _, l := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(l, "diff --git "):
			file, inHunk = "", false
			continue
		case !inHunk && strings.HasPrefix(l, "+++ "):
			file = strings.TrimPrefix(strings.TrimPrefix(l, "+++ "), "b/")
			if file == "/dev/null" || isTestFile(file) || docExts[strings.ToLower(pathExt(file))] || strings.HasPrefix(file, "docs/") {
				file = ""
			}
			continue
		case strings.HasPrefix(l, "@@ "):
			m := hunkStart.FindStringSubmatch(l)
			inHunk = m != nil
			if inHunk {
				line, _ = strconv.Atoi(m[1]) //nolint:errcheck // the pattern only matches digits
			}
			continue
		}
		if !inHunk || file == "" {
			continue
		}
		switch {
		case strings.HasPrefix(l, "+"):
			code := strings.TrimSpace(l[1:])
			if code != "" && !noiseLine.MatchString(code) && checkedToken.MatchString(" "+code+" ") {
				collect(fmt.Sprintf("%s:%d: %s", file, line, code), code)
			}
			line++
		case strings.HasPrefix(l, " ") || l == "":
			line++
		case strings.HasPrefix(l, "-"), strings.HasPrefix(l, `\`):
		default:
			inHunk = false
		}
	}
	out := slices.Concat(sharp, plain)
	return out[:min(len(out), maxAuditLines)]
}

func pathExt(f string) string {
	if i := strings.LastIndex(f, "."); i > strings.LastIndex(f, "/") {
		return f[i:]
	}
	return ""
}

// auditSection renders the checklist for the brief, or "" when the diff has no such lines.
func auditSection(diff string) string {
	lines := AuditLines(diff)
	if len(lines) == 0 {
		return ""
	}
	var s strings.Builder
	s.WriteString("\n<check_each_line>\n")
	s.WriteString("These added lines hold a comparison, boolean operator or bound. Check every one against what the code around it intends: " +
		"is it < or <=, && or ||, == or !=, are the arguments in the right order, is the bound off by one? " +
		"A line that is wrong is a finding even when you already found another bug in the same function; bugs cluster. " +
		"Do not list the lines that are right.\n")
	for _, l := range lines {
		s.WriteString(l + "\n")
	}
	s.WriteString("</check_each_line>\n")
	return s.String()
}

// withAudit can be switched off by the evals, to measure what the checklist adds.
var withAudit = true
