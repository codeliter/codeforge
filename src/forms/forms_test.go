package forms

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeHelper stands in for codeforge-forms: it reads the form on descriptor 3 and writes a canned answer on 4.
func fakeHelper(t *testing.T, body string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), HelperName)
	require.NoError(t, os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o700))
	t.Setenv(HelperEnv, p)
}

func TestRun_SendsTheFormAndReadsTheAnswers(t *testing.T) {
	spec := filepath.Join(t.TempDir(), "spec.json")
	fakeHelper(t, "cat <&3 > "+spec+"\nprintf '%s' '{\"answer\":{\"value\":\"chat\",\"yes\":true}}' >&4\n")
	a, err := Ask("mode picker", Field{Kind: Select, Title: "What are we doing?", Options: []Option{{Label: "Chat", Value: "chat"}}, Value: "ticket"})
	require.NoError(t, err)
	require.Equal(t, "chat", a.Value)
	require.True(t, a.Yes)
	sent, err := os.ReadFile(spec)
	require.NoError(t, err)
	require.Contains(t, string(sent), `"name":"mode picker"`)
	require.Contains(t, string(sent), `"value":"ticket"`)
}

func TestRun_ALeftFormIsAborted(t *testing.T) {
	fakeHelper(t, "cat <&3 >/dev/null\nexit 2\n")
	_, err := YesNo("Save?", "")
	require.ErrorIs(t, err, ErrAborted)
}

func TestRun_HelperFailureIsAnError(t *testing.T) {
	fakeHelper(t, "cat <&3 >/dev/null\necho boom >&2\nexit 1\n")
	_, err := Choose("Pick", "", []Option{{Label: "a", Value: "a"}}, "a")
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrAborted)
}

func TestHelper_MissingIsReported(t *testing.T) {
	t.Setenv(HelperEnv, "")
	t.Setenv("PATH", t.TempDir())
	_, err := Run(Page("x", Field{Kind: Confirm, Key: "ok"}))
	require.ErrorIs(t, err, ErrNoHelper)
}

func TestWhen_Shown(t *testing.T) {
	values := map[string]string{"scope": "global", "source": "learn"}
	current := func(k string) string { return values[k] }
	require.True(t, (*When)(nil).Shown(current))
	require.True(t, (&When{Key: "scope", Is: []string{"global"}}).Shown(current))
	require.False(t, (&When{Key: "scope", IsNot: []string{"global"}}).Shown(current))
	require.True(t, (&When{Key: "scope", Is: []string{"global"}, And: &When{Key: "source", Is: []string{"learn"}}}).Shown(current))
	require.False(t, (&When{Key: "scope", Is: []string{"global"}, And: &When{Key: "source", Is: []string{"starter"}}}).Shown(current))
}

func TestValidate_RequiredAndChecks(t *testing.T) {
	require.EqualError(t, Validate(Field{Kind: Text, Required: "enter a description"}, "  ", nil), "enter a description")
	require.NoError(t, Validate(Field{Kind: Text, Required: "enter a description"}, "fix it", nil))
	require.EqualError(t, Validate(Field{Kind: MultiSelect, Required: "no models given"}, "", nil), "no models given")
	require.NoError(t, Validate(Field{Kind: Input, Check: CheckPaths}, t.TempDir()+", "+t.TempDir(), nil))
	require.Error(t, Validate(Field{Kind: Input, Check: CheckPaths}, filepath.Join(t.TempDir(), "missing"), nil))
	require.ErrorIs(t, Validate(Field{Kind: Input, Check: "nope"}, "x", nil), ErrUnknownCheck)
}

func TestShow_AnOlderHelperIsRefusedForEdits(t *testing.T) {
	tests := []struct {
		name    string
		helper  string
		wantErr bool
	}{
		{name: "a helper that cannot say its protocol", helper: "exit 1\n", wantErr: true},
		{name: "a helper on the old protocol", helper: "[ \"$1\" = --protocol ] && { echo 1; exit 0; }\nexit 1\n", wantErr: true},
		{name: "a helper on this protocol", helper: "[ \"$1\" = --protocol ] && { echo 2; exit 0; }\nprintf '%s' '{\"key\":{\"value\":\"s\"},\"changes\":{\"value\":\"[]\"}}' >&4\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeHelper(t, tt.helper)
			key, _, err := Show("review", Accordion{Edits: true, Items: []Section{{Title: "a.go"}}})
			if tt.wantErr {
				require.ErrorIs(t, err, ErrOldHelper)
				return
			}
			require.NoError(t, err)
			require.Equal(t, KeySend, key)
		})
	}
}

func TestHelperProtocol_EachBuildIsAskedOnce(t *testing.T) {
	dir := t.TempDir()
	bin, count := filepath.Join(dir, HelperName), filepath.Join(dir, "asked")
	write := func(p int, at time.Time) {
		body := fmt.Sprintf("#!/bin/sh\necho x >> %s\necho banner\necho %d\n", count, p)
		require.NoError(t, os.WriteFile(bin, []byte(body), 0o700))
		require.NoError(t, os.Chtimes(bin, at, at))
	}
	asked := func() int {
		b, err := os.ReadFile(count)
		require.NoError(t, err)
		return strings.Count(string(b), "x")
	}
	write(1, time.Unix(1000, 0))
	require.Equal(t, 1, helperProtocol(bin), "the number is read from the last line")
	require.Equal(t, 1, helperProtocol(bin))
	require.Equal(t, 1, asked(), "one build is asked once")
	write(2, time.Unix(2000, 0))
	require.Equal(t, 2, helperProtocol(bin), "a helper replaced at the same path is asked again")
	require.Equal(t, 2, asked())
}

func TestShow_NotesNeedTheNewProtocolToo(t *testing.T) {
	fakeHelper(t, "exit 1\n")
	_, _, err := Show("review", Accordion{Items: []Section{{Title: "a.go", Notes: []Note{{ID: "f0", Body: "x"}}}}})
	require.ErrorIs(t, err, ErrOldHelper)
}
