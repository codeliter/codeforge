package src

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/codeliter/codeforge/src/config"
	"github.com/codeliter/codeforge/src/store"
)

func TestSessionsByMode_EachModeApartNewestFirst(t *testing.T) {
	now := time.Now()
	at := func(id, mode string, ago time.Duration) sessionInfo {
		return sessionInfo{ID: id, Mode: mode, Updated: now.Add(-ago)}
	}
	groups := sessionsByMode([]sessionInfo{
		at("w1", modeWork, time.Minute), at("r1", modeReview, time.Hour), at("w2", modeWork, 2*time.Hour),
		at("c1", modeChat, 3*time.Hour), at("r2", modeReview, 4*time.Hour),
	})
	var got []string
	for _, g := range groups {
		ids := []string{modeHeading(g.Mode) + ":"}
		for _, s := range g.Sessions {
			ids = append(ids, s.ID)
		}
		got = append(got, strings.Join(ids, " "))
	}
	require.Equal(t, []string{"Work: w1 w2", "Review: r1 r2", "Chat: c1"}, got, "the mode used last comes first, and each keeps its order")
	require.Empty(t, sessionsByMode(nil))
	require.Empty(t, modeHeading(""))
}

func TestSessions_ThePickerAndTheListSeparateInterleavedModes(t *testing.T) {
	type session struct {
		id, mode string
		ago      time.Duration
	}
	tests := []struct {
		name     string
		sessions []session // in any order: the list sorts them newest first
		marked   []string  // forks codeforge made to throw away
		list     []string  // what --list prints: each heading, then the sessions under it
		picker   []string  // what the picker offers, in order: heading/session
	}{
		{
			name: "interleaved modes",
			sessions: []session{
				{"c1", modeChat, 3 * time.Hour}, {"w1", modeWork, time.Minute}, {"r2", modeReview, 4 * time.Hour},
				{"w2", modeWork, 2 * time.Hour}, {"r1", modeReview, time.Hour},
			},
			list:   []string{"Work (2)", "w1", "w2", "Review (2)", "r1", "r2", "Chat (1)", "c1"},
			picker: []string{"Work/w1", "Review/r1", "Work/w2", "Chat/c1", "Review/r2"},
		},
		{
			name:     "the mode used last is first, whatever its name",
			sessions: []session{{"w1", modeWork, 2 * time.Hour}, {"t1", modeTicket, time.Hour}, {"t2", modeTicket, 3 * time.Hour}},
			list:     []string{"Ticket (2)", "t1", "t2", "Work (1)", "w1"},
			picker:   []string{"Ticket/t1", "Work/w1", "Ticket/t2"},
		},
		{
			name:     "one mode has one heading",
			sessions: []session{{"c2", modeChat, 2 * time.Hour}, {"c1", modeChat, time.Hour}},
			list:     []string{"Chat (2)", "c1", "c2"},
			picker:   []string{"Chat/c1", "Chat/c2"},
		},
		{
			name: "a fork being thrown away is no session, one only named like it is",
			sessions: []session{
				{"w1", modeWork, 2 * time.Hour}, {forkPrefix + "live", modeWork, time.Minute}, {forkPrefix + "mine", modeWork, time.Hour},
			},
			marked: []string{forkPrefix + "live"},
			list:   []string{"Work (2)", forkPrefix + "mine", "w1"},
			picker: []string{"Work/" + forkPrefix + "mine", "Work/w1"},
		},
	}
	heading := regexp.MustCompile(`^[A-Z][a-z]+ \(\d+\)$`)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			wd, err := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, err)
			t.Chdir(wd)
			now := time.Now()
			st, err := store.Open(t.Context(), filepath.Join(config.Dir(home), "sessions"))
			require.NoError(t, err)
			for _, s := range tt.sessions {
				writeSession(t, wd, s.id, wd, "Title of "+s.id, now.Add(-s.ago), 1, false)
				require.NoError(t, st.Put(t.Context(), store.Session{ID: s.id, Mode: s.mode, Profile: profileFor(s.mode), Repository: wd}))
			}
			require.NoError(t, st.Close())
			for _, id := range tt.marked {
				require.NoError(t, os.MkdirAll(forkMarks(wd), 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(forkMarks(wd), id), nil, 0o600))
			}

			var out bytes.Buffer
			dir, id, err := runSessions(t.Context(), home, []string{"--list"}, &out)
			require.NoError(t, err)
			require.Empty(t, dir+id, "--list opens nothing")
			var list []string
			for _, line := range strings.Split(out.String(), "\n") {
				fields := strings.Fields(line)
				switch {
				case heading.MatchString(line):
					list = append(list, line)
				case len(fields) > 0 && strings.Contains(line, " turn"):
					list = append(list, strings.TrimSuffix(fields[len(fields)-1], "…"))
				}
			}
			want := make([]string, len(tt.list))
			for i, l := range tt.list {
				want[i] = l
				if !heading.MatchString(l) {
					want[i] = l[:min(len("12345678"), len(l))] // the list shows the start of an ID
				}
			}
			require.Equal(t, want, list)

			sessions, _, err := listSessions(t.Context(), home, wd)
			require.NoError(t, err)
			var picker []string
			for _, o := range sessionOptions(sessions, now, false) {
				i, err := strconv.Atoi(o.Value)
				require.NoError(t, err)
				picker = append(picker, o.Group+"/"+sessions[i].ID)
				require.Contains(t, o.Label, "Title of "+sessions[i].ID, "an option answers with the session its line shows")
			}
			require.Equal(t, tt.picker, picker)
		})
	}
}

func TestSplitDetails_ASessionLineSplitsWhereItsDetailsStart(t *testing.T) {
	now := time.Now()
	s := sessionInfo{ID: "0123456789abcdef", Mode: "chat", Title: "A title", Turns: 3, Updated: now, Started: now, Model: "copilot/gpt-6.1-sol"}
	columns, details := splitDetails(sessionLabel(s, now, false), false)
	require.Equal(t, "3 turns · gpt-6.1-sol · 01234567", details)
	require.Contains(t, columns, "A title")
	require.NotContains(t, columns, "turns")

	require.Equal(t, []string{"WHEN             MODE    TITLE", "TURNS · TIME · COST · MODEL · ID"}, headerParts(false))
	columns, details = splitDetails("short", false)
	require.Equal(t, "short", columns)
	require.Empty(t, details)

	s.Title = "Fix  the  thing"
	columns, details = splitDetails(sessionLabel(s, now, true), true)
	require.Contains(t, columns, "Fix  the  thing", "a title with wide gaps stays whole")
	require.Equal(t, "3 turns · gpt-6.1-sol · 01234567", details)
}
