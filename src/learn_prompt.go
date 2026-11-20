package src

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/codeliter/codeforge/src/store"
)

// Reasons observePrompt reports, written to the shape log (never the prompt itself).
const (
	reasonPromptNotCorrection = "skipped:not-a-correction"
	reasonNotHuman            = "skipped:not-a-launch-from-a-terminal"
	reasonNoCorrection        = "skipped:no-matching-correction"
)

// observePrompt records a submitted prompt that reads as a correction. It applies the same checks as observeOne: a
// session codeforge launched in this repository, a directory inside it, and never a review session or juror.
func observePrompt(ctx context.Context, home string, in observePayload) string {
	text, ok := correctionText(in.Prompt)
	if !ok {
		return reasonPromptNotCorrection
	}
	if !safeSessionID(in.SessionID) || !printablePath(in.Cwd) || !filepath.IsAbs(in.Cwd) {
		return reasonBadPayload
	}
	cwd, err := filepath.EvalSymlinks(in.Cwd)
	if err != nil {
		return reasonBadPayload
	}
	repo, err := sessionRepository(ctx, cwd)
	if err != nil {
		return reasonNoRepo
	}
	dir, ok := relativeDir(repo, cwd)
	if !ok {
		return reasonNoRepo
	}
	if err := withLearning(ctx, home, func(s *store.Store) error { return checkSession(ctx, s, repo, in.SessionID) }); err != nil {
		return reasonUnknownSession
	}
	// Only a session a person started from a terminal can teach rules. A session the agent started itself (for
	// example "codeforge chat --headless ...") has no terminal and is nested in another launch, so its prompts are
	// the agent's own words, not the user's.
	if os.Getenv(humanLaunchMarker) != "1" {
		return reasonNotHuman
	}
	o := store.Observation{Kind: store.KindCorrection, Text: text, Repository: repo, SessionID: in.SessionID, Dir: dir, Origin: store.OriginHook, CreatedAt: time.Now()}
	if err := recordCorrection(ctx, home, o); err != nil {
		return reasonStoreFailed
	}
	return reasonRecorded + ":correction"
}

// recordedLine is what learnCorrect prints for the observation it saved.
var recordedLine = regexp.MustCompile(`codeforge: recorded correction #(\d+) `)

// recordedCorrection is the observation a `codeforge learn correct` tool call says it recorded. Only a call Empryo
// reports as passed counts, and one that names no observation or more than one names none: the stamp goes to the
// correction this call saved, never to an older one with the same words. learnCorrect prints that line and never the
// correction's own words, so the text cannot spell an ID.
func recordedCorrection(in observePayload) (int64, bool) {
	if status, _ := observedStatus(in.Event, in.ToolResponse); status != store.StatusPassed {
		return 0, false
	}
	found := recordedLine.FindAllSubmatch(in.ToolResponse, -1)
	if len(found) != 1 {
		return 0, false
	}
	id, err := strconv.ParseInt(string(found[0][1]), 10, 64)
	return id, err == nil
}

// stampWindow is how long after `codeforge learn correct` ran its tool result may still name the session.
const stampWindow = 10 * time.Minute

// stampCorrection names the session of a correction the agent just recorded with `codeforge learn correct`. The
// session comes from Empryo's tool result, never from the command, so the agent cannot claim a session it is not in;
// the checks are observePrompt's. A stamped correction counts towards a rule like one the user typed as a prompt:
// the same sentence in two sessions is adopted at the next launch.
func stampCorrection(ctx context.Context, home string, in observePayload, text string) string {
	if !safeSessionID(in.SessionID) || !printablePath(in.Cwd) || !filepath.IsAbs(in.Cwd) {
		return reasonBadPayload
	}
	cwd, err := filepath.EvalSymlinks(in.Cwd)
	if err != nil {
		return reasonBadPayload
	}
	repo, err := sessionRepository(ctx, cwd)
	if err != nil {
		return reasonNoRepo
	}
	dir, ok := relativeDir(repo, cwd)
	if !ok {
		return reasonNoRepo
	}
	if os.Getenv(humanLaunchMarker) != "1" {
		return reasonNotHuman
	}
	id, ok := recordedCorrection(in)
	if !ok {
		return reasonNoCorrection
	}
	stamped := false
	if err := withLearning(ctx, home, func(s *store.Store) error {
		if err := checkSession(ctx, s, repo, in.SessionID); err != nil {
			return err
		}
		stamped, err = s.StampCorrection(ctx, id, repo, dir, text, in.SessionID, time.Now().Add(-stampWindow))
		return err
	}); errors.Is(err, errLearnSession) {
		return reasonUnknownSession
	} else if err != nil {
		return reasonStoreFailed
	}
	if !stamped {
		return reasonNoCorrection
	}
	return reasonRecorded + ":correction-session"
}

// logPromptShape logs the payload's event and the reason, never any text.
func logPromptShape(home string, in observePayload, result string) {
	logShape(home, observePayload{Event: in.Event}, result)
}
