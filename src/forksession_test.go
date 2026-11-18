package src

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// nameForks gives the forks of a test one known name.
func nameForks(t *testing.T, id string) {
	t.Helper()
	old := newForkID
	t.Cleanup(func() { newForkID = old })
	newForkID = func() string { return id }
}

func TestThrowawayFork_IsNamedRemovedAndThoseLeftBehindAreSwept(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	first, second := newForkID(), newForkID()
	require.True(t, strings.HasPrefix(first, forkPrefix))
	require.NotEqual(t, first, second, "two forks at once never share a session")
	nameForks(t, forkPrefix+"new")
	sessions := filepath.Join(dir, ".empryo", "sessions")
	session := func(id, lock string, marked bool, age time.Duration) string {
		t.Helper()
		path := filepath.Join(sessions, id)
		require.NoError(t, os.MkdirAll(path, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(path, "session.jsonl"), []byte("{}\n"), 0o600))
		if lock != "" {
			require.NoError(t, os.WriteFile(filepath.Join(path, "writer.lock"), []byte(lock), 0o600))
		}
		if marked {
			mark, when := filepath.Join(forkMarks(dir), id), time.Now().Add(-age)
			require.NoError(t, os.MkdirAll(forkMarks(dir), 0o700))
			require.NoError(t, os.WriteFile(mark, nil, 0o600))
			require.NoError(t, os.Chtimes(mark, when, when))
		}
		return path
	}
	alive := `{"pid":` + strconv.Itoa(os.Getpid()) + `}`
	killed := session(forkPrefix+"killed", `{"pid":0}`, true, time.Hour)
	running := session(forkPrefix+"running", alive, true, time.Hour)
	starting := session(forkPrefix+"starting", "", true, time.Second)
	named := session(forkPrefix+"work", "", false, 0)
	users := session("work-1", "", false, 0)

	argv, remove := throwawayFork([]string{"sandbox", "empryo", "--fork", "work-1", "--headless", "explain it"})
	require.Equal(t, []string{"sandbox", "empryo", "--fork", "work-1", "--session-id", forkPrefix + "new", "--headless", "explain it"}, argv)
	require.NoDirExists(t, killed, "a fork a killed run left is removed by the next")
	require.False(t, markedFork(dir, forkPrefix+"killed"), "and its mark with it")
	require.DirExists(t, running, "one whose engine still runs is left alone")
	require.DirExists(t, starting, "and so is one made a moment ago, which may have no lock yet")
	require.DirExists(t, named, "a session of the user's named like a fork is theirs: only the mark makes a fork")
	require.False(t, markedFork(dir, forkPrefix+"work"))
	require.DirExists(t, users, "the user's sessions are never touched")
	require.True(t, markedFork(dir, forkPrefix+"new"), "the fork is marked before the engine makes it")

	made := session(forkPrefix+"new", "", false, 0)
	remove()
	require.NoDirExists(t, made, "the fork is gone when its turn is over")
	require.False(t, markedFork(dir, forkPrefix+"new"))
	require.DirExists(t, users)

	nameForks(t, forkPrefix+"work")
	taken := []string{"empryo", "--fork", "work-1", "x"}
	got, remove := throwawayFork(taken)
	require.Equal(t, taken, got, "a name a session already has is not given to a fork")
	remove()
	require.DirExists(t, named)
	nameForks(t, forkPrefix+"new")

	for _, argv := range [][]string{
		{"empryo", "--headless", "plan it"}, {"empryo", "--fork", "work-1", "--session-id", "mine", "x"}, {"empryo", "--fork"},
		{"empryo", "--fork", "elsewhere", "x"},
	} {
		got, remove := throwawayFork(argv)
		require.Equal(t, argv, got, "only a fork that has no name is given one")
		remove()
	}

	for _, cwd := range [][]string{{"--cwd", t.TempDir()}, {"--cwd=" + t.TempDir()}} {
		require.NoError(t, os.MkdirAll(filepath.Join(flagValue(cwd, "--cwd"), ".empryo", "sessions", "a"), 0o700))
		argv, _ = throwawayFork(slices.Concat([]string{"empryo"}, cwd, []string{"--fork", "a", "x"}))
		require.Equal(t, slices.Concat([]string{"empryo"}, cwd, []string{"--fork", "a", "--session-id", forkPrefix + "new", "x"}), argv)
		require.True(t, markedFork(flagValue(cwd, "--cwd"), forkPrefix+"new"), "the fork is marked where the engine runs")
	}
}
