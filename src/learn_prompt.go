package src

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/codeliter/codeforge/src/store"
)

// Reasons observePrompt reports, written to the shape log (never the prompt itself).
const (
	reasonPromptNotCorrection = "skipped:not-a-correction"
	reasonNotHuman            = "skipped:not-a-launch-from-a-terminal"
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

// logPromptShape logs the payload's event and the reason, never any text.
func logPromptShape(home string, in observePayload, result string) {
	logShape(home, observePayload{Event: in.Event}, result)
}
