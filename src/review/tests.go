package review

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	verifyTimeout  = 10 * time.Minute
	maxVerifyShown = 400
)

// TestPackages lists the distinct package directories of the changed Go files. Anything else, docs included,
// returns nothing and costs no test run.
func TestPackages(files []string) []string {
	var dirs []string
	for _, f := range files {
		if !strings.HasSuffix(f, ".go") || secretName.MatchString(f) {
			continue
		}
		d := filepath.ToSlash(filepath.Dir(f))
		if d == "" || d == "." {
			d = "."
		}
		if !slices.Contains(dirs, d) {
			dirs = append(dirs, d)
		}
	}
	return dirs
}

// Verify runs the changed packages' tests in the repository and returns one Blocking finding per failing package.
// A reviewer that reports pass while the code's own tests fail has missed a defect the repository already proves, so
// the gate blocks on that proof instead of trusting the verdict. It is deterministic: no model is involved.
func Verify(ctx context.Context, r DirRunner, repo string, files []string) []Finding {
	dirs := TestPackages(files)
	if len(dirs) == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, verifyTimeout)
	defer cancel()

	args := append([]string{"test", "-count=1"}, prefixed(dirs)...)
	out, err := r.RunDir(ctx, repo, "go", args...)
	if err == nil {
		return nil
	}

	text := string(out)
	var findings []Finding
	for pkg := range strings.SplitSeq(text, "\n") {
		rest, ok := strings.CutPrefix(pkg, "FAIL\t")
		if !ok {
			continue
		}
		name, _, _ := strings.Cut(rest, "\t")
		findings = append(findings, Finding{
			File: fileIn(dirs, files, name),
			What: "the repository's own tests fail for " + strings.TrimSpace(name) + reason(text),
		})
	}
	if len(findings) == 0 {
		// A build failure or a missing toolchain prints no FAIL line; it still means the change is unverified.
		findings = append(findings, Finding{File: fileIn(dirs, files, ""), What: "the repository's tests could not be run: " + tail(text)})
	}
	return findings
}

func prefixed(dirs []string) []string {
	out := make([]string, len(dirs))
	for i, d := range dirs {
		if d == "." {
			out[i] = "./..."
			continue
		}
		out[i] = "./" + d
	}
	return out
}

// fileIn names a changed file of the failing package, so the finding points at the change rather than the package.
func fileIn(dirs, files []string, pkg string) string {
	for _, d := range dirs {
		if pkg != "" && !strings.HasSuffix(pkg, "/"+d) && !strings.HasSuffix(pkg, d) {
			continue
		}
		for _, f := range files {
			if !strings.HasSuffix(f, ".go") {
				continue
			}
			if filepath.ToSlash(filepath.Dir(f)) == d {
				return f
			}
		}
	}
	return ""
}

// reason names the first failing test or build error, so the finding says what broke rather than only which package.
func reason(out string) string {
	if len(out) > maxVerifyShown {
		out = out[:maxVerifyShown]
	}
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "--- FAIL") || strings.Contains(line, "build failed") || strings.Contains(line, "cannot find") {
			return ": " + line
		}
	}
	return ""
}
