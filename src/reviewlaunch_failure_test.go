package src

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReviewLaunch_DoesNotOpenTUIAfterFailure(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("review requires macOS sandbox-exec")
	}
	for _, code := range []int{7, 130, 0} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			t.Chdir(t.TempDir())
			home := t.TempDir()
			capture := filepath.Join(home, "launches")
			t.Setenv("CODEFORGE_REVIEW_CAPTURE", capture)
			engine := filepath.Join(home, "engine")
			script := fmt.Sprintf("#!/bin/sh\nprintf 'BEGIN\\n' >> \"$CODEFORGE_REVIEW_CAPTURE\"\nprintf '%%s\\n' \"$@\" >> \"$CODEFORGE_REVIEW_CAPTURE\"\nexit %d\n", code)
			require.NoError(t, os.WriteFile(engine, []byte(script), execPerm))
			err := launch(t.Context(), home, engine, modeReview, []string{"Review PR #42"})
			if code == 0 {
				require.ErrorIs(t, err, errSessionMissing, "do not reopen an empty session if the engine did not save the review")
			} else {
				var exit *EngineExitError
				require.True(t, errors.As(err, &exit))
				require.Equal(t, code, exit.Code)
			}
			data, readErr := os.ReadFile(capture)
			require.NoError(t, readErr)
			require.Equal(t, 1, strings.Count(string(data), "BEGIN\n"))
			require.Contains(t, string(data), "--headless\n")
		})
	}
}

func TestReviewLaunch_ResumeSkipsSubmission(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("review requires macOS sandbox-exec")
	}
	wd, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	t.Chdir(wd)
	historicalSession(t, wd, "saved-review")
	home := t.TempDir()
	capture := filepath.Join(home, "launches")
	t.Setenv("CODEFORGE_REVIEW_CAPTURE", capture)
	engine := filepath.Join(home, "engine")
	require.NoError(t, os.WriteFile(engine, []byte("#!/bin/sh\nprintf 'BEGIN\\n' >> \"$CODEFORGE_REVIEW_CAPTURE\"\nprintf '%s\\n' \"$@\" >> \"$CODEFORGE_REVIEW_CAPTURE\"\n"), execPerm))
	require.NoError(t, launch(t.Context(), home, engine, modeReview, []string{"--session", "saved-review"}))
	data, err := os.ReadFile(capture)
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(string(data), "BEGIN\n"))
	require.Contains(t, string(data), "--session\nsaved-review\n")
	require.NotContains(t, string(data), "--headless")
}
