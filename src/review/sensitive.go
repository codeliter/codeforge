package review

import (
	"fmt"
	"path"
	"strings"
)

// SensitiveMatch returns the files that match any of the sensitive path patterns the repository's owners set, the way
// Anthropic's code review lets owners mark paths that always need human review (citation [14] in TECHNICAL.md). A
// pattern is one of:
//
//   - "dir/" (or "dir/**"): every file under a directory with that name, at any depth ("auth/" matches internal/auth/x.go);
//   - a pattern with a slash: matched against the whole path with path.Match ("src/guard/*.go");
//   - a pattern without one: matched against the file's base name ("*.sql", "Dockerfile").
func SensitiveMatch(files, patterns []string) []string {
	var out []string
	for _, f := range files {
		f = path.Clean(strings.ReplaceAll(f, "\\", "/"))
		for _, p := range patterns {
			if sensitiveFile(f, p) {
				out = append(out, f)
				break
			}
		}
	}
	return out
}

// normalizePattern trims a pattern and drops a leading "./", which names the repository root like no prefix at all.
func normalizePattern(pattern string) string {
	pattern = strings.TrimSpace(pattern)
	for strings.HasPrefix(pattern, "./") {
		pattern = strings.TrimPrefix(pattern, "./")
	}
	return pattern
}

// dirPattern returns the directory part of a "dir/" or "dir/**" pattern, and whether it is one.
func dirPattern(pattern string) (string, bool) {
	if dir, ok := strings.CutSuffix(pattern, "/**"); ok {
		return dir, true
	}
	if dir, ok := strings.CutSuffix(pattern, "/"); ok {
		return dir, true
	}
	return "", false
}

func sensitiveFile(file, pattern string) bool {
	pattern = normalizePattern(pattern)
	if ValidSensitivePattern(pattern) != "" {
		return false
	}
	if dir, ok := dirPattern(pattern); ok {
		return dirMatches(path.Dir(file), strings.Split(dir, "/"))
	}
	var target string
	if strings.Contains(pattern, "/") {
		target = file
	} else {
		target = path.Base(file)
	}
	ok, err := path.Match(pattern, target)
	return err == nil && ok
}

// dirMatches reports whether the directory names in dir contain, in a row, directories matching each element of want.
func dirMatches(dir string, want []string) bool {
	if dir == "." {
		return false
	}
	have := strings.Split(dir, "/")
	for start := 0; start+len(want) <= len(have); start++ {
		all := true
		for i, w := range want {
			if ok, err := path.Match(w, have[start+i]); err != nil || !ok {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

// ValidSensitivePattern reports why a pattern cannot be used, or "" when it can. Patterns are relative to the
// repository root, hold no ".." segment, and are valid for path.Match; "**" is only allowed as the last element, as in
// "dir/**".
func ValidSensitivePattern(pattern string) string {
	pattern = normalizePattern(pattern)
	if pattern == "" {
		return "is empty"
	}
	if strings.HasPrefix(pattern, "/") || strings.HasPrefix(pattern, "~") {
		return "must be relative to the repository root"
	}
	segments := strings.Split(strings.TrimSuffix(pattern, "/"), "/")
	for i, seg := range segments {
		switch {
		case seg == "":
			return "has an empty path element"
		case seg == "..":
			return "must not contain .."
		case strings.Contains(seg, "**") && (seg != "**" || i != len(segments)-1 || i == 0):
			return "may use ** only as the last element, as in dir/**"
		}
		if seg == "**" {
			continue
		}
		if _, err := path.Match(seg, "x"); err != nil {
			return fmt.Sprintf("is not a valid pattern: %v", err)
		}
	}
	return ""
}

// RaiseForPaths lifts the tier to risky when the change touches a path the repository's owners marked sensitive. It can
// only add scrutiny: patterns never lower a tier. why is extended with the files that caused it.
func RaiseForPaths(tier Tier, why string, files, patterns []string) (Tier, string) {
	hit := SensitiveMatch(files, patterns)
	if len(hit) == 0 {
		return tier, why
	}
	const shown = 3
	names := strings.Join(hit[:min(len(hit), shown)], ", ")
	if len(hit) > shown {
		names += fmt.Sprintf(" and %d more", len(hit)-shown)
	}
	note := "sensitive path: " + names
	if why != "" {
		note = why + "; " + note
	}
	return higher(tier, TierRisky), note
}

// largeDiffBytes is the size from which the user is told to consider splitting the change.
const (
	largeDiffBytes = 100_000
	bytesPerKB     = 1000
)

// SizeNote is a one-line advice for a change too large to review comfortably, or "" for one that is not. It never
// blocks anything: a review of a large change still runs.
func SizeNote(diffBytes int) string {
	if diffBytes < largeDiffBytes {
		return ""
	}
	return fmt.Sprintf("codeforge: this change is %d KB of diff. Smaller changes are easier to review, debug and revert; consider splitting it.", diffBytes/bytesPerKB)
}
