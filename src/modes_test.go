//nolint:err113 // dynamic error messages are okay in tests
package src

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidMode(t *testing.T) {
	for _, m := range []string{"", modeTicket, modeChat, modeReview} {
		require.NoError(t, validMode(m), m)
	}
	require.ErrorIs(t, validMode("pair"), errUnknownMode)
}

func TestPickMode(t *testing.T) {
	got, err := pickMode(Config{DefaultMode: modeReview}, []string{"anything"})
	require.NoError(t, err)
	require.Equal(t, modeReview, got)

	got, err = pickMode(Config{}, []string{"why", "is", "this", "slow"})
	require.NoError(t, err)
	require.Equal(t, modeChat, got, "a question on the command line goes to chat")

	got, err = pickMode(Config{}, nil)
	require.NoError(t, err)
	require.Equal(t, modeChat, got, "no terminal and no default falls back to chat")
}

func TestLoadConfig_DefaultMode(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, `{"default_mode":"ticket"}`)
	cfg, err := loadConfig(home)
	require.NoError(t, err)
	require.Equal(t, modeTicket, cfg.DefaultMode)

	t.Setenv("CODEFORGE_MODE", "nope")
	_, err = loadConfig(home)
	require.ErrorIs(t, err, errUnknownMode)
}

func TestModeArgs_Chat(t *testing.T) {
	cfg := Config{Model: "a/coder"}

	mode, args, err := modeArgs(t.Context(), "/nonexistent", cfg, modeChat, nil)
	require.NoError(t, err)
	require.Equal(t, modeChat, mode)
	require.Equal(t, []string{"--model", "a/coder", "--effort", "low"}, args)
}

func TestModeArgs_Resume(t *testing.T) {
	for _, mode := range modes {
		for _, want := range [][]string{
			{"--resume", "session-123"},
			{"--resume=session-123"},
			{"--session", "session-123"},
			{"-s", "session-123", "continue"},
		} {
			t.Run(mode+"/"+want[0], func(t *testing.T) {
				gotMode, args, err := modeArgs(t.Context(), "/nonexistent", Config{Model: "a/coder"}, mode, want)
				require.NoError(t, err)
				require.Equal(t, mode, gotMode)
				require.Equal(t, want, args)
			})
		}
	}
	_, _, err := modeArgs(t.Context(), "/nonexistent", Config{}, modeChat, []string{"--resume", "session-123", "--effort", "max"})
	require.ErrorIs(t, err, errEffortAboveCeiling)
}

func TestModeArgs_TicketNeedsSummaryWithoutTerminal(t *testing.T) {
	_, _, err := modeArgs(t.Context(), "/nonexistent", Config{}, modeTicket, []string{"--ticket", "ABC-1"})
	require.ErrorIs(t, err, errStartUsage)
}

func TestSyncAssets_FillsModels(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{Model: "a/coder", Review: ReviewConfig{Model: "b/reviewer"}}
	require.NoError(t, syncAssets(dir, cfg))

	for mode, want := range map[string]string{modeTicket: "a/coder", modeChat: "a/coder", modeReview: "b/reviewer"} {
		data, err := os.ReadFile(filepath.Join(dir, "agents", profileFor(mode)+".md"))
		require.NoError(t, err, mode)
		require.Contains(t, string(data), "\nmodel: "+want+"\n", mode)
		require.NotContains(t, string(data), "{{", mode)
	}

	review, err := os.ReadFile(filepath.Join(dir, "agents", "codeforge-review.md"))
	require.NoError(t, err)
	require.True(t, strings.Contains(string(review), "disallowedTools:"), "review mode must not edit")
}
