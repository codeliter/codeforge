package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Runner runs a command and returns stdout; it exists so tests never call gh or git.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

//nolint:tagliatelle // field names are fixed by the gh CLI JSON output
type PR struct {
	Number      int    `json:"number"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	URL         string `json:"url"`
	BaseRefName string `json:"baseRefName"`
	HeadRefName string `json:"headRefName"`
	Author      struct {
		Login string `json:"login"`
	} `json:"author"`
	Additions int `json:"additions"`
	Deletions int `json:"deletions"`
	Files     []struct {
		Path      string `json:"path"`
		Additions int    `json:"additions"`
		Deletions int    `json:"deletions"`
	} `json:"files"`
	Commits []struct {
		MessageHeadline string `json:"messageHeadline"`
	} `json:"commits"`
	Reviews []struct {
		Author struct {
			Login string `json:"login"`
		} `json:"author"`
		State string `json:"state"`
		Body  string `json:"body"`
	} `json:"reviews"`
	StatusCheckRollup []struct {
		Name       string `json:"name"`
		Conclusion string `json:"conclusion"`
	} `json:"statusCheckRollup"`
}

const prFields = "number,title,body,url,baseRefName,headRefName,author,additions,deletions,files,commits,reviews,statusCheckRollup"

var (
	ErrNoPR       = errors.New("review: PR number or URL is required")
	prRefPattern  = regexp.MustCompile(`^(\d+)$|^https://github\.com/[\w.-]+/[\w.-]+/pull/(\d+)`)
	ticketPattern = regexp.MustCompile(`\b[A-Z][A-Z0-9]+-\d+\b`)
)

// ParseRef accepts "123" or a github.com pull URL and returns the number as a string.
func ParseRef(ref string) (string, error) {
	m := prRefPattern.FindStringSubmatch(strings.TrimSpace(ref))
	if m == nil {
		return "", fmt.Errorf("%w: got %q", ErrNoPR, ref)
	}
	if m[1] != "" {
		return m[1], nil
	}
	return m[2], nil
}

type Brief struct {
	PR      PR
	Diff    string
	Tickets []string
	HeadRef string
}

const maxDiffBytes = 400_000

// Load collects everything a reviewer reads before the diff: intent, history, CI and prior reviews.
func Load(ctx context.Context, r Runner, repo, number string) (Brief, error) {
	repoArgs := []string{}
	if repo != "" {
		repoArgs = []string{"--repo", repo}
	}

	out, err := r.Run(ctx, "gh", append([]string{"pr", "view", number, "--json", prFields}, repoArgs...)...)
	if err != nil {
		return Brief{}, fmt.Errorf("reading PR %s: %w", number, err)
	}

	var b Brief
	if err := json.Unmarshal(out, &b.PR); err != nil {
		return Brief{}, fmt.Errorf("decoding PR %s: %w", number, err)
	}

	diff, err := r.Run(ctx, "gh", append([]string{"pr", "diff", number}, repoArgs...)...)
	if err != nil {
		return Brief{}, fmt.Errorf("reading diff of PR %s: %w", number, err)
	}
	b.Diff = string(diff)
	if len(b.Diff) > maxDiffBytes {
		b.Diff = b.Diff[:maxDiffBytes] + "\n[diff truncated; read the remaining files from the PR head ref]\n"
	}

	b.Tickets = uniqueTickets(b.PR.Title + "\n" + b.PR.Body + "\n" + b.PR.HeadRefName)

	if repo == "" {
		ref := "refs/codeforge/pr-" + number
		if _, err := r.Run(ctx, "git", "fetch", "--quiet", "origin", "pull/"+number+"/head:"+ref); err == nil {
			b.HeadRef = ref
		}
	}
	return b, nil
}

func uniqueTickets(s string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range ticketPattern.FindAllString(s, -1) {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}
