package src

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLaunchModesLifecycle(t *testing.T) {
	for _, mode := range []string{modeChat, modeWork, modeTicket} {
		for _, variant := range []string{"empty", "resume", "missing session", "interrupted"} {
			t.Run(mode+"/"+variant, func(t *testing.T) {
				wd, err := filepath.EvalSymlinks(t.TempDir())
				require.NoError(t, err)
				t.Chdir(wd)
				home := t.TempDir()
				capture := filepath.Join(home, "launches")
				t.Setenv("CODEFORGE_CLI_CAPTURE", capture)
				engine := filepath.Join(home, "engine")
				code := 0
				if variant == "interrupted" {
					code = 130
				}
				script := "#!/bin/sh\nprintf 'BEGIN\\n' >> \"$CODEFORGE_CLI_CAPTURE\"\nprintf '%s\\n' \"$@\" >> \"$CODEFORGE_CLI_CAPTURE\"\nexit " + strconv.Itoa(code) + "\n"
				require.NoError(t, os.WriteFile(engine, []byte(script), execPerm))
				var args []string
				switch variant {
				case "resume":
					historicalSession(t, wd, "saved-session")
					args = []string{"--session", "saved-session"}
				case "missing session", "interrupted":
					args = []string{"do this task"}
				}
				err = launch(t.Context(), home, engine, mode, args)
				switch variant {
				case "missing session":
					require.ErrorIs(t, err, errSessionMissing)
				case "interrupted":
					var exit *EngineExitError
					require.True(t, errors.As(err, &exit))
					require.Equal(t, 130, exit.Code)
				default:
					require.NoError(t, err)
				}
				data, err := os.ReadFile(capture)
				require.NoError(t, err)
				require.Equal(t, 1, strings.Count(string(data), "BEGIN\n"))
				if variant == "missing session" || variant == "interrupted" {
					require.Contains(t, string(data), "--headless\n")
				} else {
					require.NotContains(t, string(data), "--headless")
				}
			})
		}
	}
}
