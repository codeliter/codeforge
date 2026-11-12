package src

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/codeliter/codeforge/assets"
	"github.com/codeliter/codeforge/src/review"
	"github.com/codeliter/codeforge/src/text"
)

// How much history a persona is learned from, and how.
const (
	maxCorpusBytes = 300_000
	maxStderrBytes = 300
	learnCommits   = 400
	learnDiffs     = 40
	learnOwnPRs    = 50
	// learnReviewedPRs is as many as one GraphQL page holds: every review of them comes back in the same call.
	learnReviewedPRs = 100
	// learnParallel is how many activities are learned at once.
	learnParallel = 3
	noEvidence    = "No evidence yet."
	rulesSection  = "Do and don't"
)

// evidenceKind is one kind of history: something the user did that shows how they work.
type evidenceKind int

const (
	corrections evidenceKind = iota
	commitMessages
	diffs
	pullRequests
	reviewsGiven
	repliesToReviewers
)

// evidence is one kind of history from one repository.
type evidence struct {
	kind  evidenceKind
	title string
	body  string
}

// repoHistory is everything read from one repository.
type repoHistory struct {
	name  string
	parts []evidence
}

// facet is one activity the persona describes. It is learned on its own, from only the kinds of history that show
// that activity, each weighted by how much it tells: how someone reviews is in the reviews they gave, not in their
// diffs. A correction the user accepted can be about anything, so every facet sees those.
type facet struct {
	title   string
	brief   string
	weights map[evidenceKind]int
}

var facets = []facet{
	{"Voice", "Their tone in everything they write to people: sentence length, tense, words they use and avoid, how they explain a problem. Describe the tone faithfully, but write no rule that asks for padding, hedging or vague wording: every text stays clear, concise and unambiguous, and the persona only shapes how it sounds.",
		map[evidenceKind]int{corrections: 1, commitMessages: 2, pullRequests: 3, reviewsGiven: 3, repliesToReviewers: 2}}, //nolint:mnd // weights
	{"Commit messages", "Format rules and examples: subject style and length, when they write a body and what goes in it.",
		map[evidenceKind]int{corrections: 1, commitMessages: 6}}, //nolint:mnd // weights
	{"Pull requests", "Title format, body template (verbatim if they use one), how long, what they always include (evidence, numbers, merge order).",
		map[evidenceKind]int{corrections: 1, pullRequests: 6}}, //nolint:mnd // weights
	{"Review comments", "How they review other people's work: how they phrase a finding and an ask, how long a comment is, what they focus on and what they let pass.",
		map[evidenceKind]int{corrections: 1, reviewsGiven: 6}}, //nolint:mnd // weights
	{"Replies to reviewers", "How they answer review feedback on their own work: when they agree, push back or ask a question, and how long and in what tone.",
		map[evidenceKind]int{corrections: 1, repliesToReviewers: 6}}, //nolint:mnd // weights
	{"Code style", "How they write code, per language they use: naming, error handling, function size, comments, tests, patterns they repeat. What they repeatedly ask others to change in review is what they want in code: turn those asks into rules.",
		map[evidenceKind]int{corrections: 1, diffs: 6, reviewsGiven: 2}}, //nolint:mnd // weights
}

// personaSections are the sections every learned persona has, in order: one per facet, then the rules drawn from them.
var personaSections = func() []string {
	titles := make([]string, 0, len(facets)+1)
	for _, f := range facets {
		titles = append(titles, f.title)
	}
	return append(titles, rulesSection)
}()

// generatedExcludes keeps lock files, vendored code and generated output out of the diffs: nobody wrote them.
// They are anchored at the top of the repository, like the diffs themselves, so that learning from a subdirectory
// still reads the whole repository.
var generatedExcludes = []string{
	":(top,exclude,glob)**/*.lock", ":(top,exclude,glob)**/go.sum", ":(top,exclude,glob)**/package-lock.json",
	":(top,exclude,glob)**/*.min.*", ":(top,exclude,glob)**/*.snap", ":(top,exclude,glob)**/*.pb.go",
	":(top,exclude,glob)**/vendor/**", ":(top,exclude,glob)**/node_modules/**",
}

// prReviewsQuery reads what one person wrote in review on the pull requests a search finds: each review's summary and
// its line comments, the latest of each, in one call however many pull requests there are.
const prReviewsQuery = `query($q:String!,$me:String!,$n:Int!){search(query:$q,type:ISSUE,first:$n){nodes{... on PullRequest{number reviews(author:$me,last:30){nodes{body submittedAt comments(last:50){nodes{path body createdAt}}}}}}}}`

// prRepliesQuery reads the latest review threads of the pull requests a search finds, whoever wrote in them.
const prRepliesQuery = `query($q:String!,$n:Int!){search(query:$q,type:ISSUE,first:$n){nodes{... on PullRequest{number reviewThreads(last:50){nodes{comments(last:50){totalCount nodes{author{login} createdAt body}}}}}}}}`

// loginName is a GitHub login: nothing else is put into a query.
var loginName = regexp.MustCompile(`^[A-Za-z0-9-]+$`)

// written is one thing the user wrote on a pull request, and when.
type written struct {
	at   time.Time
	line string
}

// newestFirst lays what was written out one item a line, the latest first whichever pull request it is on: the
// prompts read the history as newest first, and a cut for room must take the oldest.
func newestFirst(items []written) string {
	slices.SortStableFunc(items, func(a, b written) int { return b.at.Compare(a.at) })
	lines := make([]string, 0, len(items))
	for _, item := range items {
		lines = append(lines, item.line)
	}
	return strings.Join(lines, "\n")
}

// reviewsWritten reads the answer to prReviewsQuery: every review summary and line comment, newest first.
func reviewsWritten(answer string) string {
	var res struct {
		Data struct {
			Search struct {
				Nodes []struct {
					Number  int `json:"number"`
					Reviews struct {
						Nodes []struct {
							Body        string    `json:"body"`
							SubmittedAt time.Time `json:"submittedAt"` //nolint:tagliatelle // GitHub's field.
							Comments    struct {
								Nodes []struct {
									Path      string    `json:"path"`
									Body      string    `json:"body"`
									CreatedAt time.Time `json:"createdAt"` //nolint:tagliatelle // GitHub's field.
								} `json:"nodes"`
							} `json:"comments"`
						} `json:"nodes"`
					} `json:"reviews"`
				} `json:"nodes"`
			} `json:"search"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(answer), &res) != nil {
		return ""
	}
	var items []written
	for _, pr := range res.Data.Search.Nodes {
		for _, review := range pr.Reviews.Nodes {
			if strings.TrimSpace(review.Body) != "" {
				items = append(items, written{review.SubmittedAt, fmt.Sprintf("- (PR #%d, summary) %s", pr.Number, review.Body)})
			}
			for _, c := range review.Comments.Nodes {
				items = append(items, written{c.CreatedAt, fmt.Sprintf("- (PR #%d, %s) %s", pr.Number, c.Path, c.Body)})
			}
		}
	}
	return newestFirst(items)
}

// repliesWritten reads the answer to prRepliesQuery: what login wrote in answer to someone, newest first. That is
// every comment of theirs but the one that opens a thread, in a thread somebody else also wrote in.
func repliesWritten(answer, login string) string {
	var res struct {
		Data struct {
			Search struct {
				Nodes []struct {
					Number  int `json:"number"`
					Threads struct {
						Nodes []struct {
							Comments struct {
								Total int `json:"totalCount"` //nolint:tagliatelle // GitHub's field.
								Nodes []struct {
									Author struct {
										Login string `json:"login"`
									} `json:"author"`
									Body      string    `json:"body"`
									CreatedAt time.Time `json:"createdAt"` //nolint:tagliatelle // GitHub's field.
								} `json:"nodes"`
							} `json:"comments"`
						} `json:"nodes"`
					} `json:"reviewThreads"` //nolint:tagliatelle // GitHub's field.
				} `json:"nodes"`
			} `json:"search"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(answer), &res) != nil {
		return ""
	}
	var items []written
	for _, pr := range res.Data.Search.Nodes {
		for _, thread := range pr.Threads.Nodes {
			comments := thread.Comments.Nodes
			others := false
			for _, c := range comments {
				others = others || c.Author.Login != login
			}
			if !others {
				continue
			}
			// Only the latest comments of a long thread are read, so then none of them is the one that opened it.
			opener := 0
			if thread.Comments.Total > len(comments) {
				opener = -1
			}
			for i, c := range comments {
				if i != opener && c.Author.Login == login {
					items = append(items, written{c.CreatedAt, fmt.Sprintf("- (PR #%d) %s", pr.Number, c.Body)})
				}
			}
		}
	}
	return newestFirst(items)
}

var blankLines = regexp.MustCompile(`\n\s*\n`)

// learnPersona reads the user's history in each repository and learns the persona one activity at a time: each facet
// is drafted from the history that shows it, and the draft is then checked against that same history, so a rule the
// history does not support is taken out before it is saved. The rules and the opening are drawn from the result.
// With only named, just those sections are learned and returned, for a persona that already has the rest.
func learnPersona(ctx context.Context, home, bin, model string, repos []string, only ...string) ([]byte, error) {
	learn := facets
	if len(only) > 0 {
		learn = slices.DeleteFunc(slices.Clone(facets), func(f facet) bool { return !slices.Contains(only, f.title) })
	}
	var histories []repoHistory
	var accepted []string
	for _, repo := range repos {
		fmt.Fprintf(os.Stderr, "codeforge: reading your history in %s\n", repo)
		rules := acceptedRules(ctx, home, repo)
		h, err := collect(ctx, review.ExecRunner{}, repo, rules)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", repo, err)
		}
		histories = append(histories, h)
		accepted = append(accepted, rules...)
	}
	if !slices.ContainsFunc(histories, func(h repoHistory) bool { return len(h.parts) > 0 }) {
		return nil, errNoHistory
	}

	fmt.Fprintf(os.Stderr, "codeforge: learning how you work, one activity at a time (%d of them); this takes a few minutes\n", len(learn))
	bodies := make([]string, len(learn))
	errs := make([]error, len(learn))
	limit := make(chan struct{}, learnParallel)
	var wg sync.WaitGroup
	for i, f := range learn {
		wg.Go(func() {
			limit <- struct{}{}
			defer func() { <-limit }()
			bodies[i], errs[i] = learnFacet(ctx, bin, model, f, histories)
			if errs[i] == nil {
				fmt.Fprintf(os.Stderr, "codeforge: learned %s\n", strings.ToLower(f.title))
			}
		})
	}
	wg.Wait()
	// One activity that could not be learned does not waste the others: it reads as no evidence, which never
	// replaces what a saved persona has for it. Only when none could be learned is there nothing to show.
	failed := 0
	for i, err := range errs {
		if err != nil {
			failed++
			bodies[i] = noEvidence
			fmt.Fprintf(os.Stderr, "codeforge: %v; that section is left out\n", err)
		}
	}
	if ctx.Err() != nil {
		return nil, fmt.Errorf("learning the persona: %w", errors.Join(append(errs, ctx.Err())...))
	}
	if failed == len(learn) {
		return nil, errors.Join(errs...)
	}

	var doc strings.Builder
	for i, f := range learn {
		// Relearning a section where the history shows nothing must not wipe what the persona has for it.
		if len(only) > 0 && bodies[i] == noEvidence {
			fmt.Fprintf(os.Stderr, "codeforge: no history shows %s here; that section is left as it is\n", strings.ToLower(f.title))
			continue
		}
		doc.WriteString("## " + f.title + "\n" + bodies[i] + "\n\n")
	}
	if len(only) > 0 {
		return []byte(doc.String()), nil
	}
	opening, rules := summarise(ctx, bin, model, accepted, doc.String())
	if ctx.Err() != nil {
		return nil, fmt.Errorf("learning the persona: %w", ctx.Err())
	}
	head := "# Persona\n\n"
	if opening != "" {
		head += opening + "\n\n"
	}
	return []byte(head + doc.String() + "## " + rulesSection + "\n" + rules + "\n"), nil
}

// learnFacet drafts one section from the history that shows it, then has the draft checked against that history. A
// facet the history says nothing about costs no model call.
func learnFacet(ctx context.Context, bin, model string, f facet, histories []repoHistory) (string, error) {
	history := corpusFor(f, histories, maxCorpusBytes)
	if history == "" {
		return noEvidence, nil
	}
	fill := strings.NewReplacer("{{section}}", f.title, "{{brief}}", f.brief)
	input := "<history>\n" + history + "\n</history>"
	draft, err := distil(ctx, bin, model, fill.Replace(assets.InitPrompt), input)
	if err != nil {
		return "", fmt.Errorf("learning %s: %w", strings.ToLower(f.title), err)
	}
	checked, err := distil(ctx, bin, model, fill.Replace(assets.PersonaCheckPrompt), input+"\n<draft>\n"+string(draft)+"</draft>")
	if ctx.Err() != nil {
		return "", fmt.Errorf("checking %s: %w", strings.ToLower(f.title), ctx.Err())
	}
	if err != nil || sectionBody(string(checked)) == "" {
		// The check adds rigour, not content: without it the draft still stands.
		checked = draft
	}
	if body := sectionBody(string(checked)); body != "" {
		return body, nil
	}
	return noEvidence, nil
}

// summarise draws the persona's opening paragraph and its short rules from the learned sections and the corrections
// the user accepted. When that fails there is no opening and the rules read as no evidence, neither of which ever
// replaces what a saved persona has.
func summarise(ctx context.Context, bin, model string, accepted []string, sections string) (opening, rules string) {
	var kept strings.Builder
	for _, rule := range accepted {
		kept.WriteString("- " + rule + "\n")
	}
	const fallback = noEvidence
	out, err := distil(ctx, bin, model, assets.PersonaSummaryPrompt, "<corrections>\n"+kept.String()+"</corrections>\n<persona>\n"+sections+"</persona>")
	if err != nil {
		return "", fallback
	}
	before, after, found := strings.Cut(stripFence(string(out)), "## "+rulesSection)
	if !found || strings.TrimSpace(after) == "" {
		return sectionBody(before), fallback
	}
	return sectionBody(before), strings.TrimSpace(after)
}

// corpusFor is the history a facet is learned from, at most budget bytes of it. Each repository gets the same room, so
// the first one named cannot crowd out the rest; inside a repository each kind takes its weight's share.
func corpusFor(f facet, histories []repoHistory, budget int) string {
	var with []repoHistory
	for _, h := range histories {
		var parts []evidence
		for _, p := range h.parts {
			if f.weights[p.kind] > 0 && p.body != "" {
				parts = append(parts, p)
			}
		}
		// Corrections alone are evidence enough: they stand as rules, and the prompt keeps those that bear on the facet.
		if len(parts) > 0 {
			with = append(with, repoHistory{name: h.name, parts: parts})
		}
	}
	var b strings.Builder
	for _, h := range with {
		fit(h.parts, f.weights, budget/len(with))
		for _, p := range h.parts {
			fmt.Fprintf(&b, "\n## %s: %s\n%s\n", h.name, p.title, p.body)
		}
	}
	return strings.TrimSpace(b.String())
}

// fit cuts the evidence to budget bytes in all. Each kind may take its weight's share; what a short one leaves is
// shared out among the rest, so room is only ever lost to history that is not there.
func fit(parts []evidence, weights map[evidenceKind]int, budget int) {
	open := make([]int, 0, len(parts))
	for i := range parts {
		open = append(open, i)
	}
	for len(open) > 0 {
		total := 0
		for _, i := range open {
			total += weights[parts[i].kind]
		}
		var long []int
		used := 0
		for _, i := range open {
			if len(parts[i].body) <= budget*weights[parts[i].kind]/total {
				used += len(parts[i].body)
				continue
			}
			long = append(long, i)
		}
		if len(long) == len(open) {
			for _, i := range long {
				parts[i].body = text.Cut(parts[i].body, budget*weights[parts[i].kind]/total)
			}
			return
		}
		budget -= used
		open = long
	}
}

// distil runs one headless turn with system as its instructions and input as the data, and returns what it wrote.
func distil(ctx context.Context, bin, model, system, input string) ([]byte, error) {
	args := []string{"--headless", "--json", "--no-profile", "--no-genome", "--effort", "high", "--max-steps", "2"}
	if model != "" {
		args = append(args, "--model", model)
	}
	// The history goes in a file: as an argument it can pass what one argument may hold (128 KB on Linux).
	file, err := os.CreateTemp("", "codeforge-history-*.md")
	if err != nil {
		return nil, fmt.Errorf("keeping the history for empryo: %w", err)
	}
	defer os.Remove(file.Name()) //nolint:errcheck // a leftover temporary file is harmless
	_, err = file.WriteString(input)
	if err := errors.Join(err, file.Close()); err != nil {
		return nil, fmt.Errorf("keeping the history for empryo: %w", err)
	}
	args = append(args, "--system", system, "--include", file.Name(), "The data your instructions speak of is in the included file "+filepath.Base(file.Name())+". Follow your instructions on it.")

	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("running empryo: %w: %s", err, text.Cut(stderr.String(), maxStderrBytes))
	}

	var res struct {
		Output string `json:"output"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return nil, fmt.Errorf("decoding empryo result: %w", err)
	}
	if res.Error != "" || strings.TrimSpace(res.Output) == "" {
		return nil, fmt.Errorf("%w: %s", errEmptyPersona, res.Error)
	}
	return []byte(strings.TrimSpace(res.Output) + "\n"), nil
}

// stripFence removes a code fence a model wrapped the whole document in.
func stripFence(doc string) string {
	doc = strings.TrimSpace(doc)
	rest, ok := strings.CutPrefix(doc, "```")
	if !ok {
		return doc
	}
	_, body, found := strings.Cut(rest, "\n")
	if !found {
		return doc
	}
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(body), "```"))
}

// sectionBody is what a model wrote for one section, without a fence round it or the heading it may have repeated.
func sectionBody(out string) string {
	lines := strings.Split(stripFence(out), "\n")
	// Only a repeated document or section heading goes; a "###" subsection is the section's own.
	for len(lines) > 0 && (strings.HasPrefix(lines[0], "# ") || strings.HasPrefix(lines[0], "## ") || strings.TrimSpace(lines[0]) == "") {
		lines = lines[1:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// acceptedRules are the corrections the user accepted in codeforge sessions in repo: what they told the agent to do
// differently, in their own words. None when the repository or the store cannot be read.
func acceptedRules(ctx context.Context, home, repo string) []string {
	out, err := review.ExecRunner{}.Run(ctx, "git", "-C", repo, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil
	}
	root := strings.TrimSpace(string(out))
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	st, err := openLearning(ctx, home)
	if err != nil {
		return nil
	}
	rules, err := st.Rules(ctx, root, false)
	if st.Close() != nil || err != nil {
		return nil
	}
	texts := make([]string, 0, len(rules))
	for _, r := range rules {
		texts = append(texts, r.Text)
	}
	return texts
}

// collect reads the user's history in repo: the corrections they accepted, their commit messages, the code they wrote
// and, for a repository on GitHub, their pull requests, the reviews they gave and their replies to reviewers. Kinds
// with nothing in them are left out.
func collect(ctx context.Context, r review.Runner, repo string, rules []string) (repoHistory, error) {
	git := func(args ...string) string {
		out, err := r.Run(ctx, "git", append([]string{"-C", repo}, args...)...)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	gh := func(args ...string) string {
		out, err := r.Run(ctx, "gh", args...)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}

	email := git("config", "user.email")
	if email == "" {
		return repoHistory{}, errNoEmail
	}
	h := repoHistory{name: filepath.Base(git("rev-parse", "--show-toplevel"))}
	add := func(kind evidenceKind, title, body string) {
		if body != "" {
			h.parts = append(h.parts, evidence{kind: kind, title: title, body: body})
		}
	}
	mine := []string{"log", "--author=" + email, "--no-merges"}

	var kept strings.Builder
	for _, rule := range rules {
		kept.WriteString("- " + rule + "\n")
	}
	add(corrections, "corrections you accepted in codeforge sessions", strings.TrimSpace(kept.String()))
	messages := git(append(mine, "-n", strconv.Itoa(learnCommits), "--pretty=format:- %s%n%w(0,2,2)%b")...)
	add(commitMessages, "commit messages, newest first", blankLines.ReplaceAllString(messages, "\n"))
	add(diffs, "code you wrote (recent diffs, newest first)",
		git(append(append(mine, "-n", strconv.Itoa(learnDiffs), "-p", "--unified=2", "--pretty=### %s", "--", ":/"), generatedExcludes...)...))

	slug := repoSlug(git("remote", "get-url", "origin"))
	if slug == "" {
		return h, nil
	}
	add(pullRequests, "your pull requests",
		gh("pr", "list", "--repo", slug, "--author", "@me", "--state", "all", "--limit", strconv.Itoa(learnOwnPRs), "--json", "title,body"))
	me := gh("api", "user", "--jq", ".login")
	if !loginName.MatchString(me) {
		return h, nil
	}
	// The pull requests most lately active; what was written on them is then put newest first.
	search := func(what string) string { return "q=" + what + " repo:" + slug + " sort:updated-desc" }
	add(reviewsGiven, "reviews you gave on other people's pull requests, newest first",
		reviewsWritten(gh("api", "graphql", "-f", "query="+prReviewsQuery, "-f", search("is:pr reviewed-by:"+me+" -author:"+me), "-f", "me="+me, "-F", "n="+strconv.Itoa(learnReviewedPRs))))
	// Replies are read from the threads themselves: a reply is a comment after a thread's first, by whoever wrote it.
	add(repliesToReviewers, "your replies to reviewers on your own pull requests, newest first",
		repliesWritten(gh("api", "graphql", "-f", "query="+prRepliesQuery, "-f", search("is:pr author:"+me), "-F", "n="+strconv.Itoa(learnOwnPRs)), me))
	return h, nil
}
