package src

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReviewLaunch_SubmitsBeforeOpeningTUI(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("review requires macOS sandbox-exec")
	}
	wd, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	t.Chdir(wd)
	home := t.TempDir()
	capture := filepath.Join(home, "launches")
	t.Setenv("CODEFORGE_REVIEW_CAPTURE", capture)
	engine := filepath.Join(home, "engine")
	script := `#!/bin/sh
printf 'BEGIN\n' >> "$CODEFORGE_REVIEW_CAPTURE"
if [ "$CODEFORGE_GUARD" != review ]; then exit 90; fi
if touch review-write-probe 2>/dev/null; then exit 91; fi
printf '%s\n' "$@" >> "$CODEFORGE_REVIEW_CAPTURE"
id=
headless=0
while [ "$#" -gt 0 ]; do
  case "$1" in
    --headless) headless=1 ;;
    --session-id) shift; id="$1" ;;
  esac
  shift
done
if [ "$headless" = 1 ]; then
  mkdir -p ".empryo/sessions/$id"
  printf '{"id":"%s","cwd":"%s"}\n' "$id" "$PWD" > ".empryo/sessions/$id/meta.json"
  : > ".empryo/sessions/$id/session.jsonl"
fi
`
	require.NoError(t, os.WriteFile(engine, []byte(script), execPerm))
	brief := "Review PR #42 with the codeforge-review skill. Draft only; post nothing."
	require.NoError(t, launch(t.Context(), home, engine, modeReview, []string{"--session-id", "review-start", "--model", "a/reviewer", "--effort", "high", brief}))
	data, err := os.ReadFile(capture)
	require.NoError(t, err)
	calls := strings.Split(strings.TrimSpace(string(data)), "BEGIN\n")
	require.Len(t, calls, 3, "submit the review first, then open its saved session")
	require.Contains(t, calls[1], "--headless\n")
	require.Contains(t, calls[1], "--save-session\n")
	require.Contains(t, calls[1], "--session-id\nreview-start\n")
	require.Contains(t, calls[1], "--model\na/reviewer\n")
	require.Contains(t, calls[1], "--effort\nhigh\n")
	require.Contains(t, calls[1], brief)
	require.Contains(t, calls[2], "--session\nreview-start")
	require.NotContains(t, calls[2], "--headless")
	require.NotContains(t, calls[2], brief)
	for _, call := range calls[1:] {
		require.Contains(t, call, "--profile\ncodeforge-review\n")
		require.Contains(t, call, "--mode\nplan\n")
	}
}
