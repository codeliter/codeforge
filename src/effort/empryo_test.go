package effort

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

func fakeEmpryo(t *testing.T, stdout string, code int) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "empryo")
	body := "#!/bin/sh\ncat <<'EOF'\n" + stdout + "\nEOF\nexit " + strconv.Itoa(code) + "\n"
	require.NoError(t, os.WriteFile(bin, []byte(body), 0o700))
	return bin
}

func TestEmpryoJudgeKeepsAnswerFromMaxStepsExit(t *testing.T) {
	answer := `{"ambiguity":1,"blast":0,"risk":1,"novelty":0,"verify":1,"reason":"x"}`
	bin := fakeEmpryo(t, `{"output":"{\"ambiguity\":1,\"blast\":0,\"risk\":1,\"novelty\":0,\"verify\":1,\"reason\":\"x\"}","error":"Max steps reached (1)"}`, 1)
	got, err := EmpryoJudge{Bin: bin, Model: "a/b"}.Ask(t.Context(), "s", "u")
	require.NoError(t, err, "a complete answer counts even when Empryo exits on the step limit")
	require.Equal(t, answer, got)
}

func TestEmpryoJudgeErrors(t *testing.T) {
	bin := fakeEmpryo(t, `{"output":"","error":"model unavailable"}`, 1)
	_, err := EmpryoJudge{Bin: bin, Model: "a/b"}.Ask(t.Context(), "s", "u")
	require.ErrorIs(t, err, errEmpryo)
	bin = fakeEmpryo(t, `not json`, 2)
	_, err = EmpryoJudge{Bin: bin, Model: "a/b"}.Ask(t.Context(), "s", "u")
	require.ErrorContains(t, err, "running empryo")
	bin = fakeEmpryo(t, `{"output":""}`, 0)
	_, err = EmpryoJudge{Bin: bin, Model: "a/b"}.Ask(t.Context(), "s", "u")
	require.ErrorIs(t, err, errEmpryo)
}

func TestUserMessageOmitsEmptySignals(t *testing.T) {
	require.Equal(t, "<task>\nfix it\n</task>", userMessage("fix it", Signals{}))
	require.Contains(t, userMessage("fix it", Signals{ChangedFiles: 2}), "changed_files=2")
}
