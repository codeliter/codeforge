package src

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReviewAcknowledgesBeforeFetchingPR(t *testing.T) {
	home, wd, bins := t.TempDir(), t.TempDir(), t.TempDir()
	capture := filepath.Join(home, "acknowledgement")
	launches := filepath.Join(home, "launches")
	gh := `#!/bin/sh
if [ ! -s "$CODEFORGE_ACK_CAPTURE" ]; then
  printf 'no acknowledgement before fetching the PR\n' >&2
  exit 94
fi
` + strings.TrimPrefix(modeCLIGH, "#!/bin/sh\n")
	require.NoError(t, os.WriteFile(filepath.Join(bins, "gh"), []byte(gh), execPerm))
	require.NoError(t, os.WriteFile(filepath.Join(bins, "empryo"), []byte(modeCLIEngine), execPerm))
	ack, err := os.Create(capture)
	require.NoError(t, err)
	defer func() { require.NoError(t, ack.Close()) }()
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestModesCLI$")
	cmd.Dir = wd
	cmd.Env = append(os.Environ(), "HOME="+home, "PATH="+bins+string(os.PathListSeparator)+os.Getenv("PATH"), "CODEFORGE_ACK_CAPTURE="+capture, "CODEFORGE_CLI_CAPTURE="+launches, "CODEFORGE_CLI_FAIL=7", `CODEFORGE_CLI_ARGS=["review","42","--repo","o/r","--effort","low","--model","fixture/model"]`)
	var stdout bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, ack
	err = cmd.Run()
	data, readErr := os.ReadFile(capture)
	require.NoError(t, readErr)
	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit, string(data))
	if runtime.GOOS == "darwin" {
		require.Equal(t, 7, exit.ExitCode(), string(data))
	} else {
		require.Contains(t, string(data), errNoSandbox.Error())
	}
	require.Contains(t, string(data), "Review requested. Collecting context; draft only, nothing will be posted.")
	require.NotContains(t, string(data), "no acknowledgement before fetching")
}

func TestAcknowledgeTask(t *testing.T) {
	for _, tc := range []struct {
		name, mode, want string
		args             []string
	}{
		{"PR review", modeReview, "Review requested.", []string{"42"}},
		{"local review", modeReview, "Review requested.", nil},
		{"work", modeWork, "Task received.", []string{"private task details"}},
		{"chat", modeChat, "Task received.", []string{"private question"}},
		{"ticket", modeTicket, "Task received.", []string{"fix this bug"}},
		{"respond", "respond", "Review responses requested.", []string{"42"}},
		{"idle chat", modeChat, "", nil},
		{"idle work", modeWork, "", nil},
		{"headless JSON", modeReview, "", []string{"42", "--headless", "--json"}},
		{"headless events", modeWork, "", []string{"task", "--headless", "--events"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			acknowledgeTask(&output, tc.mode, tc.args)
			if tc.want == "" {
				require.Empty(t, output.String())
			} else {
				require.Contains(t, output.String(), tc.want)
				require.Equal(t, 1, strings.Count(output.String(), "\n"))
				require.NotContains(t, output.String(), "private")
			}
		})
	}
}
