//nolint:err113 // dynamic error messages are okay in tests
package src

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTaskArgs_ExplicitEffort(t *testing.T) {
	cfg := Config{Model: "a/coder"}

	tests := []struct {
		name    string
		args    []string
		want    []string
		wantErr bool
	}{
		{"high allowed", []string{"--effort", "high", "migrate the db"}, []string{"--model", "a/coder", "--effort", "high", "migrate the db"}, false},
		{"low with equals", []string{"--effort=low"}, []string{"--model", "a/coder", "--effort=low"}, false},
		{"explicit model wins", []string{"--model", "b/x", "--effort", "low"}, []string{"--model", "b/x", "--effort", "low"}, false},
		{"xhigh rejected", []string{"--effort=xhigh"}, nil, true},
		{"max rejected", []string{"--effort", "max"}, nil, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := taskArgs(t.Context(), "/nonexistent", cfg, tt.args)
			if tt.wantErr {
				require.ErrorIs(t, err, errEffortAboveCeiling)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestTaskArgs_NoPromptStartsAtMedium(t *testing.T) {
	got, err := taskArgs(t.Context(), "/nonexistent", Config{}, []string{"--cwd", "/tmp/migrate"})
	require.NoError(t, err)
	require.Equal(t, []string{"--effort", "medium", "--cwd", "/tmp/migrate"}, got)
}

func TestTaskArgs_JudgeFailureFallsBackToMedium(t *testing.T) {
	got, err := taskArgs(t.Context(), "/nonexistent", Config{Judges: []string{"x/y"}}, []string{"--headless", "explain", "this"})
	require.NoError(t, err)
	require.Equal(t, []string{"--effort", "medium", "--headless", "explain", "this"}, got)
}

func TestPromptOf(t *testing.T) {
	require.Equal(t, "fix the bug", promptOf([]string{"--cwd", "/x", "fix", "--headless", "the", "bug"}))
	require.Empty(t, promptOf([]string{"--model", "a/b"}))
}

func TestHasFlag(t *testing.T) {
	require.True(t, hasFlag([]string{"--model", "x"}, "--model"))
	require.True(t, hasFlag([]string{"--model=x"}, "--model"))
	require.False(t, hasFlag([]string{"--models"}, "--model"))
}

func TestFlagValue(t *testing.T) {
	require.Equal(t, "/b", flagValue([]string{"chat", "--cwd", "/b"}, "--cwd"))
	require.Equal(t, "/b", flagValue([]string{"--cwd=/b"}, "--cwd"))
	require.Empty(t, flagValue([]string{"--cwd"}, "--cwd"))
}

func TestDropFlag(t *testing.T) {
	require.Equal(t, []string{"chat", "--effort", "low"}, dropFlag([]string{"chat", "--cwd", "sub", "--effort", "low"}, "--cwd"))
	require.Equal(t, []string{"chat"}, dropFlag([]string{"chat", "--cwd=sub"}, "--cwd"))
}
