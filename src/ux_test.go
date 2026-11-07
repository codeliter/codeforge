//nolint:err113 // dynamic error messages are okay in tests
package src

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/codeliter/codeforge/src/config"

	"github.com/stretchr/testify/require"

	"github.com/codeliter/codeforge/src/review"
)

func TestSuggestMode(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		want string
	}{
		{"pr link", []string{"https://github.com/o/r/pull/12"}, modeReview},
		{"pr link with path", []string{"https://github.com/o/r/pull/12/files"}, modeReview},
		{"pr number", []string{"#42"}, modeReview},
		{"ticket flag", []string{"--ticket", "ABC-1"}, modeTicket},
		{"ticket flag with text", []string{"--ticket=ABC-1", "fix it"}, modeTicket},
		{"question", []string{"why", "is", "this", "slow"}, ""},
		{"link inside a sentence", []string{"look", "at", "https://github.com/o/r/pull/12"}, ""},
		{"issue link", []string{"https://github.com/o/r/issues/12"}, ""},
		{"non-github pull url", []string{"https://gitlab.com/o/r/pull/12"}, ""},
		{"bare number", []string{"42"}, ""},
		{"none", nil, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, reason := suggestMode(tt.args)
			require.Equal(t, tt.want, got)
			require.Equal(t, tt.want != "", reason != "")
		})
	}
}

func TestPickMode_SuggestionBeatsDefault(t *testing.T) {
	got, err := pickMode(config.Config{DefaultMode: modeWork}, []string{"#7"})
	require.NoError(t, err)
	require.Equal(t, modeReview, got)

	_, err = pickMode(config.Config{}, []string{"--resume", "abc12345", "#7"})
	require.ErrorIs(t, err, errSessionMode, "a resume never picks a mode")
}

func TestHelp_EverydayAndAll(t *testing.T) {
	var short, all bytes.Buffer
	printHelp(&short, false)
	printHelp(&all, true)
	require.Equal(t, helpText, all.String())
	for _, want := range []string{"doctor", "help --all", "ticket [--ticket ID]", "gate"} {
		require.Contains(t, short.String(), want)
	}
	for _, hidden := range []string{"models configure", "learn propose", "provider set"} {
		require.NotContains(t, short.String(), hidden)
		require.Contains(t, all.String(), hidden)
	}
	require.Less(t, len(short.String()), len(all.String()))
	require.True(t, wantsAllHelp([]string{"--all"}))
	require.False(t, wantsAllHelp(nil))
}

func TestRenderDoctor(t *testing.T) {
	var out bytes.Buffer
	failed := renderDoctor(&out, []doctorItem{
		{check: check{name: "Empryo", ok: true, note: "1.0"}},
		{check: check{name: "persona", note: "none"}, warn: true, fix: "codeforge init"},
		{check: check{name: "gh", note: "not signed in"}, fix: "gh auth login"},
	})
	require.True(t, failed)
	require.Contains(t, out.String(), "✗ gh")
	require.Contains(t, out.String(), "! persona")
	require.Contains(t, out.String(), "Next: gh auth login", "a blocking fix comes before a warning")

	out.Reset()
	require.False(t, renderDoctor(&out, []doctorItem{{check: check{name: "persona", note: "none"}, warn: true, fix: "codeforge init"}}))
	require.Contains(t, out.String(), "Next: codeforge init")

	out.Reset()
	require.False(t, renderDoctor(&out, []doctorItem{{check: check{name: "git", ok: true, note: "2.54"}}}))
	require.Contains(t, out.String(), "Everything is ready")
}

func TestDoctorFile_Trust(t *testing.T) {
	path := filepath.Join(t.TempDir(), "persona.md")
	require.NoError(t, os.WriteFile(path, []byte("\nrules\n"), 0o600))

	item := doctorFile("persona", path, config.Store{}, "codeforge init", "codeforge trust")
	require.False(t, item.ok)
	require.True(t, item.warn)
	require.Equal(t, "codeforge trust", item.fix)

	item = doctorFile("persona", path, config.Store{path: config.Digest("rules")}, "codeforge init", "codeforge trust")
	require.True(t, item.ok, "the ledger holds the trimmed persona")

	missing := filepath.Join(t.TempDir(), "none.md")
	require.Equal(t, "codeforge init", doctorFile("persona", missing, config.Store{}, "codeforge init", "").fix)
	require.Empty(t, doctorFile("config", missing, config.Store{}, "", "x").name, "a missing project config is normal")
}

func TestDoctor_NoEmpryo(t *testing.T) {
	// Never call Run here: past the early commands it installs hooks for the running executable into HOME.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir())
	items := doctorItems(t.Context(), home)
	require.Equal(t, "Empryo", items[0].name)
	require.True(t, items[0].failed())
	require.Equal(t, "codeforge init", items[0].fix)
	require.ErrorIs(t, runDoctor(t.Context(), home, &bytes.Buffer{}), errDoctorFailed)
}

func TestDoctor_RejectsArguments(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.ErrorIs(t, Run(t.Context(), []string{"doctor", "extra"}), errDoctorUsage, "returns before any install step")
}

func TestFormatSummary(t *testing.T) {
	require.Equal(t, "\n  Where things stand\n    main: no changes\n", formatSummary("## main...origin/main\n", false))

	got := formatSummary("## feat/x...origin/feat/x [ahead 2]\n M a.go\nA  b.go\n?? c.go\n", false)
	require.Contains(t, got, "feat/x: 2 changed, 1 new, 2 unpushed")
	require.Contains(t, got, "codeforge gate")

	require.Contains(t, formatSummary("## main\n", true), "\x1b[2mWhere things stand")
	require.Empty(t, formatSummary("", false))
	require.Empty(t, formatSummary("garbage\n", false))
	require.Empty(t, formatSummary("## bad\x1b[31mname\n", false), "control characters never reach the terminal")
	require.False(t, strings.Contains(formatSummary("## main\n", false), "gate"), "a clean tree needs no review hint")
}

func TestSessionSummary_NotARepo(t *testing.T) {
	require.Empty(t, sessionSummary(t.Context(), t.TempDir(), false))
}

func TestFormatSummary_UnbornBranch(t *testing.T) {
	require.Contains(t, formatSummary("## No commits yet on main\n?? a.go\n", false), "main: 1 new")
	require.Contains(t, formatSummary("## Initial commit on trunk\n", false), "trunk: no changes")
}

func TestStartArgs_TicketEqualsForm(t *testing.T) {
	for _, args := range [][]string{{"--ticket=ABC-1", "fix it"}, {"--ticket", "ABC-1", "fix it"}} {
		w, rest, err := workFromArgs(args)
		require.NoError(t, err)
		require.Equal(t, "ABC-1", w.ticket)
		require.Equal(t, "fix it", w.description)
		require.Empty(t, rest, "the ticket flag is consumed, not forwarded to Empryo")
		require.Contains(t, w.prompt(), "Ticket: ABC-1")
	}
}

func TestDoctorModels_InvalidConfigIsBlocking(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeConfig(t, home, `{"model":"a/coder","review":{"model":"a/coder"}}`)
	item := doctorModels(home, "")
	require.True(t, item.failed(), item.note)
	require.Contains(t, item.note, "invalid global configuration")
}

func TestDoctorModels_ApprovedProjectConfigIsValidated(t *testing.T) {
	home, project := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	writeConfig(t, home, `{"review":{"model":"a/coder"}}`)
	path := config.ProjectPath(project)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	body := "model: a/coder\n"
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))

	item := doctorModels(home, project)
	require.NotContains(t, item.note, "invalid", "an unapproved project config is ignored, as a launch ignores it")

	require.NoError(t, config.Trust(home, path, body))
	item = doctorModels(home, project)
	require.True(t, item.failed(), item.note)
	require.Contains(t, item.note, "invalid project configuration")
}

func filterRepo(t *testing.T, clean string) string {
	t.Helper()
	repo := t.TempDir()
	git := func(args ...string) {
		out, err := exec.CommandContext(t.Context(), "git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		require.NoError(t, err, string(out))
	}
	git("init", "-q")
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte("*.txt filter=evil\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\n"), 0o600))
	git("add", "-A")
	git("-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "x")
	git("config", "filter.evil.clean", clean)
	// A same-size rewrite with a fresh mtime forces git to compare content, which runs the clean filter.
	require.NoError(t, os.WriteFile(filepath.Join(repo, "a.txt"), []byte("two\n"), 0o600))
	return repo
}

func requireSummarySandbox(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("the summary sandbox needs macOS sandbox-exec")
	}
	probe := t.TempDir()
	out, err := exec.CommandContext(t.Context(), "git", "-C", probe, "init", "-q").CombinedOutput()
	require.NoError(t, err, string(out))
	if sessionSummary(t.Context(), probe, false) == "" {
		t.Skip("sandbox-exec cannot be applied in this environment (for example a nested sandbox)")
	}
}

func TestSessionSummary_NeverRunsRepositoryFilters(t *testing.T) {
	requireSummarySandbox(t)
	marker := filepath.Join(t.TempDir(), "ran")
	repo := filterRepo(t, "touch '"+marker+"'; cat")

	// Control: unsandboxed git does run the filter, so the assertion below can fail.
	out, err := exec.CommandContext(t.Context(), "git", "-C", repo, "status", "--porcelain").CombinedOutput()
	require.NoError(t, err, string(out))
	require.FileExists(t, marker, "control: the filter runs under plain git status")
	require.NoError(t, os.Remove(marker))

	got := sessionSummary(t.Context(), repo, false)
	require.NotContains(t, got, "unreachable")
	require.NoFileExists(t, marker, "a repository filter must not be able to write from the summary")
}

func TestSessionSummary_BoundedByFilterDescendants(t *testing.T) {
	requireSummarySandbox(t)
	repo := filterRepo(t, "sleep 30 & cat")
	start := time.Now()
	_ = sessionSummary(t.Context(), repo, false)
	require.Less(t, time.Since(start), 3*time.Second, "a descendant holding the output pipes must not hang shutdown")
}

func TestSuggestedReferenceParses(t *testing.T) {
	for _, ref := range []string{"#42", "https://github.com/o/r/pull/12", "https://github.com/o/r/pull/12/files"} {
		mode, _ := suggestMode([]string{ref})
		require.Equal(t, modeReview, mode, ref)
		_, err := review.ParseRef(ref)
		require.NoError(t, err, "a suggested reference must be one review accepts: "+ref)
	}
	for _, ref := range []string{"https://gitlab.com/o/r/pull/12", "https://example.com/o/r/pull/1"} {
		mode, _ := suggestMode([]string{ref})
		require.Empty(t, mode, "an unsupported host is never suggested: "+ref)
	}
}

func TestSessionSummary_SeesChangesOutsideSubdirectory(t *testing.T) {
	requireSummarySandbox(t)
	repo := t.TempDir()
	out, err := exec.CommandContext(t.Context(), "git", "-C", repo, "init", "-q").CombinedOutput()
	require.NoError(t, err, string(out))
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "sub"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "other"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "other", "a.go"), []byte("x"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "other", "b.go"), []byte("x"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".empryo"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".empryo", "state"), []byte("x"), 0o600))

	got := sessionSummary(t.Context(), filepath.Join(repo, "sub"), false)
	require.Contains(t, got, "2 new", "files in a sibling directory count one by one; Empryo state does not")
	require.Contains(t, got, "codeforge gate")
}

func TestDoctor_DoesNotRepair(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(launchMarker, "")
	withVersion(t, "9.9.9")
	require.ErrorIs(t, Run(t.Context(), []string{"doctor", "extra"}), errDoctorUsage)
	require.NoDirExists(t, filepath.Join(home, ".empryo"), "doctor must not run the upgrade repair")
	require.NoFileExists(t, filepath.Join(config.Dir(home), repairStampFile))
}
