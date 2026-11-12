//nolint:err113 // dynamic error messages are okay in tests
package src

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/codeliter/codeforge/src/config"
)

// historyFake answers git and gh by the first argument that names what is asked for.
type historyFake struct {
	calls [][]string
	gh    bool
}

func (f *historyFake) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	line := strings.Join(args, " ")
	switch {
	case name == "git" && strings.Contains(line, "config user.email"):
		return []byte("me@example.test\n"), nil
	case name == "git" && strings.Contains(line, "rev-parse"):
		return []byte("/work/api\n"), nil
	case name == "git" && strings.Contains(line, "remote get-url"):
		if !f.gh {
			return []byte("https://gitlab.com/acme/api.git\n"), nil
		}
		return []byte("git@github.com:acme/api.git\n"), nil
	case name == "git" && strings.Contains(line, " -p "):
		return []byte("### Fix it\n+code"), nil
	case name == "git" && strings.Contains(line, "log"):
		return []byte("- Fix it\n\n\n- Add that\n  Because of this."), nil
	case name == "gh" && strings.Contains(line, "pr list"):
		return []byte(`[{"title":"Fix it","body":"Why."}]`), nil
	case name == "gh" && strings.Contains(line, "api user"):
		return []byte("octo\n"), nil
	case name == "gh" && strings.Contains(line, "reviewed-by:octo"):
		return []byte(`{"data":{"search":{"nodes":[{"number":7,"reviews":{"nodes":[{"body":"","submittedAt":"2026-01-02T00:00:00Z","comments":{"nodes":[{"path":"a.go","body":"Wrap this error.","createdAt":"2026-01-02T00:00:00Z"}]}}]}}]}}}`), nil
	case name == "gh" && strings.Contains(line, "author:octo"):
		return []byte(`{"data":{"search":{"nodes":[{"number":9,"reviewThreads":{"nodes":[{"comments":{"totalCount":2,"nodes":[{"author":{"login":"rev"},"body":"Why?","createdAt":"2026-01-01T00:00:00Z"},{"author":{"login":"octo"},"body":"Done.","createdAt":"2026-01-02T00:00:00Z"}]}}]}}]}}}`), nil
	}
	return nil, errors.New("unexpected call: " + name + " " + line)
}

func kinds(h repoHistory) []evidenceKind {
	var out []evidenceKind
	for _, p := range h.parts {
		out = append(out, p.kind)
	}
	return out
}

func TestCollect_ReadsEveryKindOfHistoryInOneCallEach(t *testing.T) {
	f := &historyFake{gh: true}
	h, err := collect(t.Context(), f, "repo", []string{"Never force push."})
	require.NoError(t, err)
	require.Equal(t, "api", h.name)
	require.Equal(t, []evidenceKind{corrections, commitMessages, diffs, pullRequests, reviewsGiven, repliesToReviewers}, kinds(h))
	require.Equal(t, "- Never force push.", h.parts[0].body)
	require.Equal(t, "- Fix it\n- Add that\n  Because of this.", h.parts[1].body, "bodies are kept, blank lines are not")
	require.Equal(t, "- (PR #7, a.go) Wrap this error.", h.parts[4].body)
	require.Equal(t, "- (PR #9) Done.", h.parts[5].body)

	graphql := 0
	for _, call := range f.calls {
		line := strings.Join(call, " ")
		if strings.Contains(line, "api graphql") {
			graphql++
			require.Contains(t, line, "repo:acme/api sort:updated-desc")
		}
		if strings.Contains(line, " -p ") {
			require.Contains(t, line, ":(top,exclude,glob)**/go.sum", "generated files are kept out of the diffs")
			require.Contains(t, line, "-- :/ ", "the whole repository is read, wherever the command was started")
		}
	}
	last := strings.Join(f.calls[len(f.calls)-1], " ")
	require.Contains(t, last, "reviewThreads", "replies are read from the threads, not from the reviews the user submitted")
	require.Equal(t, 2, graphql, "one call for the reviews given, one for the replies, however many pull requests")
}

func TestCollect_OffGitHubThereIsStillTheLocalHistory(t *testing.T) {
	f := &historyFake{}
	h, err := collect(t.Context(), f, "repo", nil)
	require.NoError(t, err)
	require.Equal(t, []evidenceKind{commitMessages, diffs}, kinds(h))
	for _, call := range f.calls {
		require.NotEqual(t, "gh", call[0])
	}
}

func TestReviewsWritten_NewestFirstAcrossPullRequests(t *testing.T) {
	answer := `{"data":{"search":{"nodes":[
{"number":1,"reviews":{"nodes":[{"body":"Old summary.","submittedAt":"2026-01-01T00:00:00Z","comments":{"nodes":[{"path":"a.go","body":"Old comment.","createdAt":"2026-01-01T00:00:01Z"}]}}]}},
{"number":2,"reviews":{"nodes":[{"body":"","submittedAt":"2026-03-01T00:00:00Z","comments":{"nodes":[{"path":"b.go","body":"Newest.","createdAt":"2026-03-01T00:00:00Z"}]}},
 {"body":"Middle summary.","submittedAt":"2026-02-01T00:00:00Z","comments":{"nodes":[]}}]}},
{}]}}}`
	require.Equal(t, "- (PR #2, b.go) Newest.\n- (PR #2, summary) Middle summary.\n- (PR #1, a.go) Old comment.\n- (PR #1, summary) Old summary.", reviewsWritten(answer),
		"an old review on a pull request updated lately does not come before a newer one elsewhere, and an empty summary is left out")
	require.Empty(t, reviewsWritten("not json"))
	require.Empty(t, reviewsWritten(`{"data":{"search":{"nodes":[]}}}`))
}

func TestRepliesWritten_OnlyAnswersToSomeoneElseNewestFirst(t *testing.T) {
	c := func(login, body, at string) string {
		return `{"author":{"login":"` + login + `"},"body":"` + body + `","createdAt":"` + at + `"}`
	}
	thread := func(total string, comments ...string) string {
		return `{"comments":{"totalCount":` + total + `,"nodes":[` + strings.Join(comments, ",") + `]}}`
	}
	answer := `{"data":{"search":{"nodes":[
{"number":1,"reviewThreads":{"nodes":[` + strings.Join([]string{
		thread("2", c("rev", "Why?", "2026-01-01T00:00:00Z"), c("octo", "Old reply.", "2026-01-02T00:00:00Z")),
		thread("2", c("octo", "My own note.", "2026-01-03T00:00:00Z"), c("octo", "More of it.", "2026-01-04T00:00:00Z")),
		thread("2", c("octo", "I opened this.", "2026-01-05T00:00:00Z"), c("rev", "Agreed.", "2026-01-06T00:00:00Z")),
	}, ",") + `]}},
{"number":2,"reviewThreads":{"nodes":[` + thread("60", c("octo", "Cut off, so a reply.", "2026-02-01T00:00:00Z"), c("rev", "Still?", "2026-02-02T00:00:00Z"), c("octo", "Newest reply.", "2026-02-03T00:00:00Z")) + `]}},
{"number":3,"reviewThreads":{"nodes":[` + thread("1", `{"author":null,"body":"A deleted account.","createdAt":"2026-02-04T00:00:00Z"}`) + `]}}]}}}`
	require.Equal(t, "- (PR #2) Newest reply.\n- (PR #2) Cut off, so a reply.\n- (PR #1) Old reply.", repliesWritten(answer, "octo"),
		"a thread nobody else wrote in holds no reply, the comment that opens a thread is not one, and in a thread too long to read whole none is the opener")
	require.Empty(t, repliesWritten("not json", "octo"))
}

func TestPersonaChanges_AnOpeningThatCouldNotBeWrittenIsNotAChange(t *testing.T) {
	old := "# Persona\n\nCareful.\n\n## Voice\nPlain.\n"
	require.Empty(t, personaChanges(old, "# Persona\n\n## Voice\nPlain.\n"))
}

func TestFit_AShortKindLeavesItsRoomToTheOthers(t *testing.T) {
	weights := map[evidenceKind]int{commitMessages: 1, diffs: 1}
	parts := []evidence{{kind: commitMessages, body: strings.Repeat("a", 10)}, {kind: diffs, body: strings.Repeat("b", 500)}}
	fit(parts, weights, 100)
	require.Len(t, parts[0].body, 10, "what fits is kept whole")
	require.Len(t, parts[1].body, 90, "and the rest of the room goes to what does not")

	parts = []evidence{{kind: commitMessages, body: strings.Repeat("a", 500)}, {kind: diffs, body: strings.Repeat("b", 500)}}
	fit(parts, map[evidenceKind]int{commitMessages: 1, diffs: 3}, 100)
	require.Len(t, parts[0].body, 25)
	require.Len(t, parts[1].body, 75, "with too much of everything the weights decide")
}

func TestCorpusFor_AFacetSeesOnlyTheHistoryThatShowsItAndEachRepositoryGetsTheSameRoom(t *testing.T) {
	big := repoHistory{name: "big", parts: []evidence{
		{kind: corrections, title: "corrections", body: "- Never force push."},
		{kind: reviewsGiven, title: "reviews", body: strings.Repeat("r", 5000)},
		{kind: diffs, title: "diffs", body: "+code"},
	}}
	small := repoHistory{name: "small", parts: []evidence{{kind: reviewsGiven, title: "reviews", body: "- Wrap this error."}}}
	local := repoHistory{name: "local", parts: []evidence{{kind: corrections, title: "corrections", body: "- A rule."}, {kind: diffs, title: "diffs", body: "+x"}}}

	var reviewing facet
	for _, f := range facets {
		if f.title == "Review comments" {
			reviewing = f
		}
	}
	got := corpusFor(reviewing, []repoHistory{big, small, local}, 1000)
	require.Contains(t, got, "## big: reviews")
	require.Contains(t, got, "- Never force push.", "a correction can bear on any activity")
	require.NotContains(t, got, "+code", "diffs do not show how someone reviews")
	require.Contains(t, got, "- Wrap this error.", "the small repository is not crowded out")
	require.Contains(t, got, "- A rule.", "a correction is evidence on its own")
	require.NotContains(t, got, "+x")
	require.Less(t, strings.Count(got, "r"), 600)

	onlyDiffs := repoHistory{name: "d", parts: []evidence{{kind: diffs, title: "diffs", body: "+x"}}}
	require.Empty(t, corpusFor(reviewing, []repoHistory{onlyDiffs}, 1000), "no evidence, so nothing to ask a model about")
}

func TestSectionBody_DropsTheFenceAndARepeatedHeading(t *testing.T) {
	require.Equal(t, "Short sentences.", sectionBody("```markdown\n## Voice\n\nShort sentences.\n```\n"))
	require.Equal(t, "Short sentences.", sectionBody("# Persona\nShort sentences.\n"))
	require.Empty(t, sectionBody("## Voice\n"))
	require.Equal(t, "### Go\nEarly returns.", sectionBody("## Code style\n### Go\nEarly returns.\n"), "a subsection is the section's own")
}

func TestPersonaSectionsFollowTheFacets(t *testing.T) {
	require.Equal(t, []string{"Voice", "Commit messages", "Pull requests", "Review comments", "Replies to reviewers", "Code style", "Do and don't"}, personaSections)
}

func TestSplitAndMergePersona(t *testing.T) {
	old := "# Persona\n\nCareful.\n\n## Voice\nPlain.\n\n## Code style\nEarly returns.\n\n## Personal notes\nTickets look like ABC-1.\n"
	next := "# Persona\n\nFast.\n\n## Voice\nPlain.\n\n## Code style\nEarly returns.\nSmall functions.\n\n## Review comments\nOne finding each.\n"

	sections := splitPersona(old)
	require.Len(t, sections, 4)
	require.Empty(t, sections[0].title)
	require.Equal(t, "# Persona\n\nCareful.", sections[0].body)
	require.Equal(t, old, joinPersona(sections), "splitting and joining changes nothing")

	changes := personaChanges(old, next)
	require.Len(t, changes, 3, "Voice is the same, so it is not a change")
	require.Empty(t, personaChanges(old, "## Code style\n"+noEvidence+"\n"), "no evidence never replaces what the persona says")
	require.Equal(t, "Opening", changes[0].name())
	require.Equal(t, sectionChange{title: "Code style", added: 1}, changes[1])
	require.Equal(t, "+1 −0 lines", changes[1].summary())
	require.Equal(t, "new", changes[2].summary())

	merged := mergePersona(old, next, func(title string) bool { return title == "Code style" || title == "Review comments" })
	require.Contains(t, merged, "Careful.", "an opening that was not taken stays")
	require.NotContains(t, merged, "Fast.")
	require.Contains(t, merged, "Small functions.")
	require.Contains(t, merged, "One finding each.")
	require.Contains(t, merged, "## Personal notes\nTickets look like ABC-1.", "a section the new persona lacks is never deleted")
	require.True(t, strings.HasPrefix(merged, "# Persona"), "the opening stays first")

	all := mergePersona(old, next, func(string) bool { return true })
	require.Contains(t, all, "Fast.")
	require.Contains(t, all, "## Personal notes")
}

func TestReconcilePersona_WithNobodyToAskEveryChangeIsTakenAndNotesAreKept(t *testing.T) {
	if interactive() {
		t.Skip("non-interactive behaviour")
	}
	home := t.TempDir()
	first, err := reconcilePersona(home, nil, []byte("# Persona\n\n## Voice\nPlain.\n"))
	require.NoError(t, err)
	require.False(t, first.keep)
	require.True(t, first.fresh)
	require.Equal(t, "# Persona\n\n## Voice\nPlain.\n", string(first.persona), "with nothing saved the new persona is used as it is")

	p, err := savePersona(home, []byte("# Persona\n\n## Voice\nPlain.\n\n## Code style\nEarly returns.\n\n## Personal notes\nMine.\n"))
	require.NoError(t, err)
	saved, err := personaSnapshot(home)
	require.NoError(t, err)
	_, err = reconcilePersona(home, []byte("# Something older\n"), []byte("# Persona\n"))
	require.ErrorIs(t, err, errPersonaChanged, "a persona changed since the learning began is not fitted to")
	starter, err := reconcilePersona(home, saved, []byte("# Persona\n\n## Voice\nBlunt.\n"))
	require.NoError(t, err)
	require.False(t, starter.fresh, "a learnable section the new persona lacks is kept, so not all of it is new")
	fitted, err := reconcilePersona(home, saved, []byte("# Persona\n\n## Voice\nBlunt.\n\n## Code style\n"+noEvidence+"\n"))
	require.NoError(t, err)
	require.False(t, fitted.keep)
	require.Contains(t, string(fitted.persona), "Blunt.")
	require.Contains(t, string(fitted.persona), "Early returns.", "a section the history showed nothing for is left as it is")
	require.Contains(t, string(fitted.persona), "## Personal notes\nMine.")
	require.False(t, fitted.fresh, "and so the persona is not all as new as today")

	_, err = commitPersona(t.Context(), home, fitted, func() error { return nil })
	require.NoError(t, err)
	previous, err := os.ReadFile(filepath.Join(config.Dir(home), previousPersonaFile))
	require.NoError(t, err)
	require.Contains(t, string(previous), "Plain.", "the persona a save replaces is kept beside it")
	now, err := os.ReadFile(p)
	require.NoError(t, err)
	require.Contains(t, string(now), "Blunt.")

	_, err = commitPersona(t.Context(), home, fitted, func() error { return nil })
	require.ErrorIs(t, err, errPersonaChanged, "a persona saved since this one was fitted is not overwritten")
	now, err = os.ReadFile(p)
	require.NoError(t, err)
	require.Contains(t, string(now), "Blunt.")
}

func TestLearnFrom_ARefreshWidensToTheRepositoriesAlreadyLearnedFrom(t *testing.T) {
	home, other := t.TempDir(), t.TempDir()
	require.Equal(t, []string{"."}, learnFrom(home, []string{"."}), "nothing recorded yet")

	require.NoError(t, os.MkdirAll(config.Dir(home), privateDir))
	require.NoError(t, savePersonaSource(home, personaSource{Kind: sourceKindLearned, Repos: []string{other, filepath.Join(other, "gone"), "relative"}}))
	require.Equal(t, []string{".", other}, learnFrom(home, []string{"."}), "recorded repositories that are still there are learned from again")
	require.Equal(t, []string{"../named"}, learnFrom(home, []string{"../named"}), "repositories named on the command line are used as given")

	require.NoError(t, savePersonaSource(home, personaSource{Kind: sourceKindStarter, Starter: "oss"}))
	require.Equal(t, []string{"."}, learnFrom(home, []string{"."}))
}
