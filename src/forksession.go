package src

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// A fork codeforge makes for one turn (an explanation, a walkthrough) is thrown away: Empryo keeps every fork as a
// session of its own, a whole copy of the one it came from, so codeforge names each fork and removes it when the turn
// is over. One left by a run that was killed before it could is removed by the next. What makes a session a fork to
// throw away is the mark codeforge leaves beside the sessions when it makes one, never its name: a session of the
// user's that happens to be named like one is theirs.

const (
	// forkPrefix starts the session ID of every fork codeforge throws away, so one is told at a glance.
	forkPrefix = "codeforge-fork-"
	// staleFork is how long after it was made a fork no engine is writing is taken for one a killed run left: longer
	// than any forked turn is given.
	staleFork = 15 * time.Minute
)

// newForkID names a fork; tests replace it.
var newForkID = func() string {
	const idBytes = 6
	b := make([]byte, idBytes)
	_, _ = rand.Read(b) // never fails
	return forkPrefix + hex.EncodeToString(b)
}

// forkMarks is where the forks made in wd are marked, one empty file a fork, named by its session.
func forkMarks(wd string) string { return filepath.Join(wd, ".empryo", "codeforge-forks") }

// markedFork reports whether the session id in wd is a fork codeforge made to throw away.
func markedFork(wd, id string) bool {
	_, err := os.Stat(filepath.Join(forkMarks(wd), id))
	return err == nil
}

// throwawayFork names the session of the fork argv makes and marks it as one to throw away, and returns the command
// line with that name and what removes the session once the turn is over. Forks left by runs that were killed are
// removed first. A command line that makes no fork, or names its session itself, is returned as it is; so is one
// whose fork cannot be marked, or that forks a session not kept in its directory: that fork is kept as any session is.
func throwawayFork(argv []string) (named []string, remove func()) {
	forked := slices.Index(argv, "--fork") + 1 // the session it forks follows the flag
	if forked == 0 || forked >= len(argv) || slices.Contains(argv, "--session-id") {
		return argv, func() {}
	}
	wd, err := os.Getwd()
	if dir := flagValue(argv, "--cwd"); dir != "" {
		wd, err = dir, nil
	}
	if err != nil {
		return argv, func() {}
	}
	sessions := filepath.Join(wd, ".empryo", "sessions")
	if _, err := os.Stat(filepath.Join(sessions, argv[forked])); err != nil {
		return argv, func() {} // the session it forks is not kept here, so neither is its fork
	}
	sweepForks(wd, time.Now())
	id := newForkID()
	session, mark := filepath.Join(sessions, id), filepath.Join(forkMarks(wd), id)
	if _, err := os.Lstat(session); err == nil {
		return argv, func() {} // a session of that name is there already: it is not this fork
	}
	const dirMode, markMode = 0o700, 0o600
	if os.MkdirAll(forkMarks(wd), dirMode) != nil || os.WriteFile(mark, nil, markMode) != nil {
		return argv, func() {}
	}
	named = slices.Insert(slices.Clone(argv), forked+1, "--session-id", id)
	return named, func() { dropFork(session, mark) }
}

// dropFork removes a fork's session, and its mark once the session is gone: one that could not be removed stays
// marked, for the next fork to try again.
func dropFork(session, mark string) {
	if os.RemoveAll(session) == nil {
		_ = os.Remove(mark) // a mark left over names no session, and is removed with the next sweep
	}
}

// sweepForks removes the marked forks of wd that a killed run left behind: those made long enough ago that no turn
// is still writing them, and whose engine is gone.
func sweepForks(wd string, now time.Time) {
	entries, err := os.ReadDir(forkMarks(wd))
	if err != nil {
		return
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() || !safeSessionID(e.Name()) || now.Sub(info.ModTime()) < staleFork {
			continue
		}
		session := filepath.Join(wd, ".empryo", "sessions", e.Name())
		if writerAlive(filepath.Join(session, "writer.lock")) {
			continue
		}
		dropFork(session, filepath.Join(forkMarks(wd), e.Name()))
	}
}
