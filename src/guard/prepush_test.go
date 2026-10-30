package guard

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func gitOut(t *testing.T, repo string, args ...string) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "git", append([]string{"-C", repo,
		"-c", "user.email=t@t", "-c", "user.name=t", "-c", "commit.gpgsign=false"}, args...)...).Output()
	require.NoError(t, err)
	return strings.TrimSpace(string(out))
}

func TestParsePushUpdates(t *testing.T) {
	in := "refs/heads/main abc refs/heads/main def\nbad line\n"
	require.Equal(t, []PushUpdate{{"refs/heads/main", "abc", "refs/heads/main", "def"}}, ParsePushUpdates(strings.NewReader(in)))
}

func TestCheckPush(t *testing.T) {
	repo := gitRepo(t)
	base := gitOut(t, repo, "rev-parse", "HEAD")
	gitOut(t, repo, "commit", "-q", "--allow-empty", "-m", "two")
	head := gitOut(t, repo, "rev-parse", "HEAD")
	now := time.Now()

	fresh := func() State {
		st, err := Load(t.TempDir())
		require.NoError(t, err)
		return st
	}
	push := []PushUpdate{{"refs/heads/main", head, "refs/heads/main", base}}

	st := fresh()
	require.ErrorIs(t, CheckPush(t.Context(), st, repo, push, now), ErrPushRefused, "not reviewed")

	st.PassGate(repo, "tree", head, "ok", now)
	require.NoError(t, CheckPush(t.Context(), st, repo, push, now), "reviewed commit")

	other := []PushUpdate{{"refs/heads/feat", base, "refs/heads/feat", zeroSHA}}
	require.ErrorIs(t, CheckPush(t.Context(), st, repo, other, now), ErrPushRefused, "a different commit is not covered")

	rewrite := []PushUpdate{{"refs/heads/main", head, "refs/heads/main", gitOut(t, repo, "commit-tree", "-m", "x", head+"^{tree}")}}
	require.ErrorIs(t, CheckPush(t.Context(), st, repo, rewrite, now), ErrPushRefused, "force push needs approval")
	st.Allow(repo, RuleForcePush, now)
	require.NoError(t, CheckPush(t.Context(), st, repo, rewrite, now))
	require.False(t, st.Approved(repo, RuleForcePush, now), "approval spent")

	del := []PushUpdate{{"(delete)", zeroSHA, "refs/heads/old", base}}
	require.ErrorIs(t, CheckPush(t.Context(), st, repo, del, now), ErrPushRefused)

	st = fresh()
	st.Allow(repo, GatePush, now)
	require.NoError(t, CheckPush(t.Context(), st, repo, push, now), "one-off push approval")
	require.ErrorIs(t, CheckPush(t.Context(), st, repo, push, now), ErrPushRefused, "spent")
}

func TestCheckPush_AnnotatedTagOfReviewedCommit(t *testing.T) {
	repo := gitRepo(t)
	head := gitOut(t, repo, "rev-parse", "HEAD")
	gitOut(t, repo, "tag", "-a", "v1", "-m", "v1")
	tag := gitOut(t, repo, "rev-parse", "v1")
	require.NotEqual(t, head, tag)

	st, err := Load(t.TempDir())
	require.NoError(t, err)
	now := time.Now()
	st.PassGate(repo, "tree", head, "ok", now)

	push := []PushUpdate{{"refs/tags/v1", tag, "refs/tags/v1", zeroSHA}}
	require.NoError(t, CheckPush(t.Context(), st, repo, push, now))
}
