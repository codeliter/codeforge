//nolint:err113 // dynamic error messages are okay in tests
package src

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/codeliter/codeforge/src/store"
)

func TestCorrectionCue(t *testing.T) {
	for prompt, want := range map[string]bool{
		"always use table-driven tests":          true,
		"use table-driven tests":                 false,
		"No, use table tests instead":            true,
		"  Don't mock the database  ":            true,
		"never commit generated files":           true,
		"always run gofmt before finishing":      true,
		"prefer small functions":                 true,
		"please use errors.Is here":              true,
		"stop adding comments":                   true,
		"add a test for the parser":              false,
		"what does this function do?":            false,
		"fix the bug":                            false,
		"use x":                                  false,
		"use table tests\nand also refactor":     false,
		"use token ghp_abcdefghijklmnopqrstuvwx": false,
		"use " + strings.Repeat("a", 400):        false,
		"":                                       false,
	} {
		_, ok := correctionText(prompt)
		require.Equal(t, want, ok, prompt)
	}
	text, _ := correctionText("  Prefer table tests  ")
	require.Equal(t, "Prefer table tests", text, "the stored text is trimmed")
}

func TestEmbed(t *testing.T) {
	a, b, c := embed("use table-driven tests"), embed("prefer table tests"), embed("never mock the database")
	require.Len(t, a, store.VectorDims)
	require.InDelta(t, 1, cosine(a, a), 1e-5, "vectors have unit length")
	require.Equal(t, a, embed("use table-driven tests"), "embedding is deterministic")
	require.Greater(t, cosine(a, b), cosine(a, c), "shared wording is closer than unrelated wording")
	require.GreaterOrEqual(t, cosine(a, b), ruleSimilarity, "the example corrections link at the grouping threshold")
	require.Less(t, cosine(a, c), ruleSimilarity, "unrelated corrections do not")
	require.Equal(t, make([]float32, store.VectorDims), embed("please"), "text of only stop words is the zero vector")
}

func TestJaccardFallback(t *testing.T) {
	require.GreaterOrEqual(t, jaccard("use table-driven tests", "prefer table tests"), 0.3)
	require.Less(t, jaccard("use table-driven tests", "never mock the database"), jaccardFallback)
	require.Zero(t, jaccard("", "x"))
}

func obsAt(id int64, text, session string, at time.Time) store.Observation {
	return store.Observation{ID: id, Kind: store.KindCorrection, Text: text, SessionID: session, Origin: store.OriginHook, CreatedAt: at}
}

func TestProposals_NeedTwoSessions(t *testing.T) {
	now := time.Now()
	same := []store.Observation{obsAt(1, "use table tests", "s1", now), obsAt(2, "use table tests", "s1", now.Add(time.Minute))}
	require.Empty(t, proposalsOf(same, nil, nil, nil, "."), "two corrections in one session are one session")
	two := []store.Observation{obsAt(1, "use table tests", "s1", now), obsAt(2, "prefer table tests", "s2", now.Add(time.Minute))}
	got := proposalsOf(two, nil, nil, nil, ".")
	require.Len(t, got, 1)
	require.Equal(t, "prefer table tests", got[0].Text, "the most recent wording leads")
	require.Equal(t, []int64{1, 2}, got[0].IDs)
	require.Equal(t, 2, got[0].Sessions)
}

func TestProposals_ExcludeRevertedDismissedAndRuled(t *testing.T) {
	now := time.Now()
	obs := []store.Observation{obsAt(1, "use table tests", "s1", now), obsAt(2, "use table tests", "s2", now), obsAt(3, "never mock the database", "s1", now), obsAt(4, "never mock the database", "s2", now)}
	require.Len(t, proposalsOf(obs, nil, nil, nil, "."), 2)

	reverted := now
	obs[1].RevertedAt = &reverted
	require.Len(t, proposalsOf(obs, nil, nil, nil, "."), 1, "a reverted correction no longer counts")
	obs[1].RevertedAt = nil

	dismissed := map[string]bool{dismissKey("never mock the database"): true}
	got := proposalsOf(obs, nil, dismissed, nil, ".")
	require.Len(t, got, 1)
	require.Equal(t, "use table tests", got[0].Text)

	rules := []store.Rule{{Text: "use table tests", Dir: "."}}
	require.Len(t, proposalsOf(obs, rules, nil, nil, "."), 1, "an accepted rule covers its group")
	forgotten := now
	require.Len(t, proposalsOf(obs, []store.Rule{{Text: "use table tests", Dir: ".", ForgottenAt: &forgotten}}, nil, nil, "."), 1, "a forgotten rule keeps covering its group, so forgetting is permanent")
}

func TestProposals_CoverageIsScopedToDirectory(t *testing.T) {
	now := time.Now()
	at := func(dir string, o store.Observation) store.Observation { o.Dir = dir; return o }
	root := []store.Rule{{Text: "prefer table tests", Dir: "."}}
	rootObs := []store.Observation{obsAt(1, "use table tests", "s1", now), obsAt(2, "prefer table tests", "s2", now)}
	require.Empty(t, proposalsOf(rootObs, root, nil, nil, "."), "a root rule covers the root")
	frontObs := []store.Observation{at("frontend", obsAt(3, "use table tests", "s3", now)), at("frontend", obsAt(4, "prefer table tests", "s4", now))}
	require.Len(t, proposalsOf(frontObs, root, nil, nil, "frontend"), 1, "but not the same convention in frontend, where it never reaches the prompt")
	require.Empty(t, proposalsOf(frontObs, nil, nil, nil, "."), "corrections made in frontend never create a root rule")
	require.Empty(t, proposalsOf(rootObs, nil, nil, nil, "frontend"), "nor do root corrections create a frontend rule")
}

func TestProposals_OppositeCorrectionsStayApart(t *testing.T) {
	now := time.Now()
	rule := []store.Rule{{Text: "use table tests", Dir: "."}}
	against := []store.Observation{obsAt(1, "do not use table tests", "s1", now), obsAt(2, "never use table tests", "s2", now)}
	require.Len(t, proposalsOf(against, rule, nil, nil, "."), 1, "an accepted rule does not suppress its opposite")

	mixed := []store.Observation{obsAt(1, "use table tests", "s1", now), obsAt(2, "do not use table tests", "s2", now)}
	require.Empty(t, proposalsOf(mixed, nil, nil, nil, "."), "opposites never group into one proposal")
	require.False(t, negative("No, use table tests instead"), "a leading no only introduces the correction")
	require.True(t, negative("Don't use table tests"))
	require.True(t, negative("avoid mocks"))
}

func TestGroupCorrections_VectorNeighbours(t *testing.T) {
	obs := []store.Observation{{ID: 1, Text: "a"}, {ID: 2, Text: "b"}, {ID: 3, Text: "c"}}
	near := func(o store.Observation) []store.Neighbour {
		if o.ID == 1 {
			return []store.Neighbour{{ID: 1, Distance: 0}, {ID: 2, Distance: 0.5}, {ID: 3, Distance: 1.4}}
		}
		return nil
	}
	groups := groupCorrections(obs, near)
	require.Equal(t, [][]int{{0, 1}, {2}}, groups, "distance 0.5 is cosine 0.875 and links; 1.4 is cosine 0.02 and does not")
}

func TestProposals_AcceptedRuleInOneDirectoryDoesNotBlockAnother(t *testing.T) {
	home, repo := learnRepo(t)
	require.NoError(t, os.Mkdir(filepath.Join(repo, "frontend"), 0o755))
	teachCorrections(t, home, repo, "always use table-driven tests", "always use table-driven tests")
	adoptRules(t.Context(), home, repo, ".", io.Discard)
	front := filepath.Join(repo, "frontend")
	t.Chdir(front)
	for i, text := range []string{"always use table-driven tests", "always use table-driven tests"} {
		require.NoError(t, recordCorrection(t.Context(), home, store.Observation{Kind: store.KindCorrection, Text: text, Repository: repo, SessionID: "f" + string(rune('a'+i)), Dir: "frontend", Origin: store.OriginHook, CreatedAt: time.Now()}))
	}
	out, err := learn(t, home, "proposals")
	require.NoError(t, err)
	require.Contains(t, out, "always use table-driven tests", "the same convention can be learned for frontend too")
	adoptRules(t.Context(), home, repo, "frontend", io.Discard)
	require.Len(t, learnedRules(t.Context(), home, repo, "frontend"), 1)
}

func TestHumanLaunch(t *testing.T) {
	require.True(t, humanLaunch(true, false), "a terminal and no codeforge around it is a person")
	require.False(t, humanLaunch(false, false), "no terminal: a script or the agent's shell")
	require.False(t, humanLaunch(true, true), "started from inside a codeforge session: the agent, whatever it claims")
	require.False(t, humanLaunch(false, true))
}

func TestObservePrompt_AgentStartedSessionsTeachNothing(t *testing.T) {
	home, repo := t.TempDir(), gitRepo(t)
	knownSession(t, home, repo)
	t.Setenv(launchMarker, "1")
	t.Setenv("CODEFORGE_GUARD", "")
	t.Setenv(humanLaunchMarker, "")
	data, err := json.Marshal(map[string]any{"session_id": "sess-1", "cwd": repo, "hook_event_name": promptEvent, "prompt": "always use table-driven tests"})
	require.NoError(t, err)
	for range 3 {
		runObserve(t.Context(), home, strings.NewReader(string(data)))
	}
	require.Empty(t, loadObservations(t, home, repo), "a prompt typed by the agent into a session it launched is not the user's word")
	shape, err := os.ReadFile(filepath.Join(configDir(home), shapeLogFile))
	require.NoError(t, err)
	require.Contains(t, string(shape), "result=skipped:not-a-launch-from-a-terminal")
}

func TestLaunchCommand_MarksOnlyHumanLaunches(t *testing.T) {
	t.Setenv(launchMarker, "1")
	_, env, err := launchCommand(t.Context(), t.TempDir(), "/not-an-executable", modeChat, []string{"--session-id", "abcdef12"})
	require.NoError(t, err)
	require.NotContains(t, env, humanLaunchMarker+"=1", "a launch nested in a codeforge session is never human")
	t.Setenv(humanLaunchMarker, "1")
	_, env, err = launchCommand(t.Context(), t.TempDir(), "/not-an-executable", modeChat, []string{"--session-id", "abcdef13"})
	require.NoError(t, err)
	require.NotContains(t, env, humanLaunchMarker+"=1", "an inherited marker is cleared, not trusted")
}

func TestProposals_SwappedTargetsWithEqualOverlapNeverMerge(t *testing.T) {
	now := time.Now()
	a := "prefer unit tests for database access, not integration tests for database access"
	b := "prefer integration tests for database access, not unit tests for database access"
	require.False(t, compatible(a, b))
	require.True(t, compatible(a, "prefer unit tests for database access, not integration tests for database access please"))
	obs := []store.Observation{obsAt(1, a, "s1", now), obsAt(2, b, "s2", now)}
	require.Empty(t, proposalsOf(obs, nil, nil, nil, "."))
	require.Len(t, proposalsOf([]store.Observation{obsAt(3, b, "s1", now), obsAt(4, b, "s2", now)}, []store.Rule{{Text: a, Dir: "."}}, nil, nil, "."), 1, "an accepted rule never suppresses its reverse")
}

func TestForget_StaysForgottenWhenEveryEvidenceIsReverted(t *testing.T) {
	home, repo := learnRepo(t)
	teachCorrections(t, home, repo, "always use table-driven tests", "always use table-driven tests")
	adoptRules(t.Context(), home, repo, ".", io.Discard)
	_, err := learn(t, home, "forget", "R1")
	require.NoError(t, err)
	_, err = learn(t, home, "revert", "1", "2")
	require.NoError(t, err)
	teachCorrections(t, home, repo, "prefer table tests", "always use table-driven tests")
	var out bytes.Buffer
	adoptRules(t.Context(), home, repo, ".", &out)
	require.Empty(t, out.String(), "the forgotten rule still covers its convention, whatever the new wording")
	require.Empty(t, learnedRules(t.Context(), home, repo, "."))
}

func TestVecGrouper_SameGroupsAsWordOverlap(t *testing.T) {
	home, repo := t.TempDir(), gitRepo(t)
	t.Chdir(repo)
	texts := []string{"use table-driven tests", "prefer table tests", "never mock the database", "use table tests please"}
	now := time.Now()
	for i, text := range texts {
		require.NoError(t, recordCorrection(t.Context(), home, store.Observation{Kind: store.KindCorrection, Text: text, Repository: repo, SessionID: "s" + string(rune('a'+i)), Origin: store.OriginHook, CreatedAt: now.Add(time.Duration(i) * time.Minute)}))
	}
	obs, rules, dismissed, near, err := loadLearning(t.Context(), home, repo)
	require.NoError(t, err)
	require.NotNil(t, near, "vector search is used when the extension loads")
	got := proposalsOf(obs, rules, dismissed, near, ".")
	require.Len(t, got, 1)
	require.Len(t, got[0].IDs, 3, "the three table-test corrections group; the mocking one stays apart")

	// The same input through the word-overlap fallback finds the same group.
	fallback := proposalsOf(obs, rules, dismissed, nil, ".")
	require.Len(t, fallback, 1)
	require.Equal(t, got[0].IDs, fallback[0].IDs)
}

func learnRepo(t *testing.T) (home, repo string) {
	t.Helper()
	home, repo = t.TempDir(), gitRepo(t)
	t.Chdir(repo)
	return home, repo
}

func teachCorrections(t *testing.T, home, repo string, texts ...string) {
	t.Helper()
	now := time.Now()
	for i, text := range texts {
		require.NoError(t, recordCorrection(t.Context(), home, store.Observation{Kind: store.KindCorrection, Text: text, Repository: repo, SessionID: "s" + string(rune('a'+i)), Origin: store.OriginHook, CreatedAt: now.Add(time.Duration(i) * time.Minute)}))
	}
}

func learn(t *testing.T, home string, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := runLearn(t.Context(), home, args, &out)
	return out.String(), err
}

func TestLearn_ProposalsAcceptRulesForget(t *testing.T) {
	home, repo := learnRepo(t)
	teachCorrections(t, home, repo, "always use table-driven tests", "prefer table tests")

	out, err := learn(t, home, "proposals")
	require.NoError(t, err)
	require.Contains(t, out, "P1  \"prefer table tests\"")
	require.Contains(t, out, "seen 2 times in 2 sessions")
	require.Contains(t, out, "learn accept P1")

	_, err = learn(t, home, "accept", "P99")
	require.ErrorIs(t, err, errNoProposal, "a number that is not in the current list fails")
	_, err = learn(t, home, "accept", "x")
	require.ErrorIs(t, err, errNoProposal)

	out, err = learn(t, home, "accept", "P1")
	require.NoError(t, err)
	require.Contains(t, out, "accepted rule R1: prefer table tests")
	require.Contains(t, out, "codeforge learn forget R1")

	out, err = learn(t, home, "rules")
	require.NoError(t, err)
	require.Contains(t, out, "R1  \"prefer table tests\"")
	out, err = learn(t, home, "proposals")
	require.NoError(t, err)
	require.NotContains(t, out, "prefer table tests", "an accepted group is no longer proposed")

	out, err = learn(t, home, "forget", "R1")
	require.NoError(t, err)
	require.Contains(t, out, "forgot rule R1")
	_, err = learn(t, home, "forget", "R1")
	require.ErrorIs(t, err, store.ErrNoRule)
	_, err = learn(t, home, "forget", "nope")
	require.ErrorIs(t, err, errNoRuleID)
	out, err = learn(t, home, "rules")
	require.NoError(t, err)
	require.Contains(t, out, "No rules accepted yet")
}

func TestLearn_DismissHidesAProposal(t *testing.T) {
	home, repo := learnRepo(t)
	teachCorrections(t, home, repo, "never mock the database", "never mock the database")
	out, err := learn(t, home, "proposals")
	require.NoError(t, err)
	require.Contains(t, out, "never mock the database")

	out, err = learn(t, home, "dismiss", "P1")
	require.NoError(t, err)
	require.Contains(t, out, "dismissed:")
	out, err = learn(t, home, "proposals")
	require.NoError(t, err)
	require.Contains(t, out, "No correction has been recorded")
	_, err = learn(t, home, "accept", "P1")
	require.ErrorIs(t, err, errNoProposal)
}

func TestLearn_RuleCommandsRejectBadUsage(t *testing.T) {
	home, _ := learnRepo(t)
	for _, args := range [][]string{{"accept"}, {"accept", "1", "2"}, {"dismiss"}, {"forget"}, {"proposals", "x"}, {"rules", "x"}} {
		_, err := learn(t, home, args...)
		require.ErrorIs(t, err, errLearnUsage, strings.Join(args, " "))
	}
}

func TestObservePrompt_RecordsCorrectionsOnly(t *testing.T) {
	t.Setenv(humanLaunchMarker, "1")
	home, repo := t.TempDir(), gitRepo(t)
	knownSession(t, home, repo)
	t.Setenv(launchMarker, "1")
	t.Setenv("CODEFORGE_GUARD", "")
	prompt := func(text string) *strings.Reader {
		data, err := json.Marshal(map[string]any{"session_id": "sess-1", "cwd": repo, "hook_event_name": promptEvent, "prompt": text})
		require.NoError(t, err)
		return strings.NewReader(string(data))
	}
	runObserve(t.Context(), home, prompt("always use table-driven tests"))
	runObserve(t.Context(), home, prompt("add a test for the parser"))
	runObserve(t.Context(), home, prompt("use token ghp_abcdefghijklmnopqrstuvwx"))

	obs := loadObservations(t, home, repo)
	require.Len(t, obs, 1)
	require.Equal(t, store.KindCorrection, obs[0].Kind)
	require.Equal(t, "always use table-driven tests", obs[0].Text)
	require.Equal(t, store.OriginHook, obs[0].Origin)
	require.Equal(t, "sess-1", obs[0].SessionID)

	data, err := os.ReadFile(filepath.Join(configDir(home), shapeLogFile))
	require.NoError(t, err)
	require.Contains(t, string(data), "event=UserPromptSubmit")
	require.Contains(t, string(data), "result=recorded:correction")
	require.Contains(t, string(data), "result=skipped:not-a-correction")
	require.NotContains(t, string(data), "table-driven", "the shape log never holds prompt text")
}

func TestObservePrompt_TeachesNothingFromTheWrongSession(t *testing.T) {
	t.Setenv(humanLaunchMarker, "1")
	home, repo := t.TempDir(), gitRepo(t)
	knownSession(t, home, repo)
	payload := func() *strings.Reader {
		data, err := json.Marshal(map[string]any{"session_id": "sess-1", "cwd": repo, "hook_event_name": promptEvent, "prompt": "always use table-driven tests"})
		require.NoError(t, err)
		return strings.NewReader(string(data))
	}
	t.Setenv(launchMarker, "")
	runObserve(t.Context(), home, payload())
	t.Setenv(launchMarker, "0")
	runObserve(t.Context(), home, payload())
	t.Setenv(launchMarker, "1")
	t.Setenv("CODEFORGE_GUARD", "review")
	runObserve(t.Context(), home, payload())
	require.Empty(t, loadObservations(t, home, repo), "plain Empryo, jurors and review sessions teach nothing")

	t.Setenv("CODEFORGE_GUARD", "")
	other := gitRepo(t)
	data, err := json.Marshal(map[string]any{"session_id": "sess-1", "cwd": other, "hook_event_name": promptEvent, "prompt": "always use table-driven tests"})
	require.NoError(t, err)
	runObserve(t.Context(), home, strings.NewReader(string(data)))
	require.Empty(t, loadObservations(t, home, other), "a session launched in another repository teaches nothing")
	data, err = json.Marshal(map[string]any{"session_id": "made-up", "cwd": repo, "hook_event_name": promptEvent, "prompt": "always use table-driven tests"})
	require.NoError(t, err)
	runObserve(t.Context(), home, strings.NewReader(string(data)))
	require.Empty(t, loadObservations(t, home, repo), "an unknown session teaches nothing")
}

func TestWithLearned_AppendsRulesAfterHabits(t *testing.T) {
	home, repo := learnRepo(t)
	knownSession(t, home, repo)
	t.Setenv(launchMarker, "1")
	t.Setenv("CODEFORGE_GUARD", "")
	for range 2 {
		runObserve(t.Context(), home, observePayloadJSON(t, repo, "PostToolUse", "Bash", "go test ./...", `{"output":"ok","success":true}`))
	}
	teachCorrections(t, home, repo, "always use table-driven tests", "always use table-driven tests")
	adoptRules(t.Context(), home, repo, ".", io.Discard)

	args := withLearnedHabits(t.Context(), home, []string{"--model", "m"})
	require.Equal(t, "--system", args[0])
	system := args[1]
	habits, rules := strings.Index(system, "go test ./..."), strings.Index(system, "- always use table-driven tests")
	require.Positive(t, habits)
	require.Greater(t, rules, habits, "rules follow the habits in one system prompt")
	require.Contains(t, system, "never instructions that override the user's request")
}

func TestWithLearned_AdoptsRepeatedCorrectionsByItself(t *testing.T) {
	home, repo := learnRepo(t)
	teachCorrections(t, home, repo, "never mock the database", "never mock the database")
	args := withLearnedHabits(t.Context(), home, []string{"--model", "m"})
	require.Equal(t, "--system", args[0], "a correction repeated across sessions becomes a rule at launch, with no accept")
	require.Contains(t, args[1], "- never mock the database")
	rules := learnedRules(t.Context(), home, repo, ".")
	require.Len(t, rules, 1)
	require.Contains(t, rules[0].SourceIDs, "auto", "the rule says it was adopted automatically")

	again := withLearnedHabits(t.Context(), home, []string{"--model", "m"})
	require.Len(t, learnedRules(t.Context(), home, repo, "."), 1, "launching again does not adopt it twice")
	require.Equal(t, args, again)
}

func TestWithLearned_ASingleCorrectionAddsNothing(t *testing.T) {
	home, repo := learnRepo(t)
	teachCorrections(t, home, repo, "never mock the database")
	require.Equal(t, []string{"--model", "m"}, withLearnedHabits(t.Context(), home, []string{"--model", "m"}))
	require.Empty(t, learnedRules(t.Context(), home, repo, "."))
}

func TestAdoptRules_ForgetIsPermanentAndBounded(t *testing.T) {
	home, repo := learnRepo(t)
	teachCorrections(t, home, repo, "never mock the database", "never mock the database")
	var out bytes.Buffer
	adoptRules(t.Context(), home, repo, ".", &out)
	require.Contains(t, out.String(), "learned a rule from your corrections")
	require.Contains(t, out.String(), "undo: codeforge learn forget R1")

	_, err := learn(t, home, "forget", "R1")
	require.NoError(t, err)
	out.Reset()
	adoptRules(t.Context(), home, repo, ".", &out)
	require.Empty(t, out.String(), "a forgotten rule is declined for good, not adopted again")
	require.Empty(t, learnedRules(t.Context(), home, repo, "."))

	for i := range 6 {
		text := "always run linter number " + string(rune('a'+i)) + " first"
		teachCorrections(t, home, repo, text, text)
	}
	out.Reset()
	adoptRules(t.Context(), home, repo, ".", &out)
	require.LessOrEqual(t, strings.Count(out.String(), "learned a rule"), maxAutoRules, "one launch adopts at most a few")
}

func TestProposals_NegationScope(t *testing.T) {
	now := time.Now()
	a := []store.Observation{obsAt(1, "prefer unit tests, not integration tests", "s1", now), obsAt(2, "prefer integration tests, not unit tests", "s2", now)}
	require.Empty(t, proposalsOf(a, nil, nil, nil, "."), "forbidding different things is not one rule")
	rule := []store.Rule{{Text: "prefer unit tests, not integration tests", Dir: "."}}
	opposite := []store.Observation{obsAt(1, "prefer integration tests, not unit tests", "s1", now), obsAt(2, "prefer integration tests, not unit tests please", "s2", now)}
	require.Len(t, proposalsOf(opposite, rule, nil, nil, "."), 1, "an accepted rule does not suppress the opposite")
	same := []store.Observation{obsAt(1, "prefer unit tests, not integration tests", "s1", now), obsAt(2, "prefer unit tests and not integration tests", "s2", now)}
	require.Len(t, proposalsOf(same, nil, nil, nil, "."), 1, "the same stance still groups")
}

func TestProposals_DismissalSurvivesANewWording(t *testing.T) {
	now := time.Now()
	obs := []store.Observation{obsAt(1, "never mock the database", "s1", now), obsAt(2, "never mock the database", "s2", now.Add(time.Minute))}
	dismissed := map[string]bool{dismissKey("never mock the database"): true}
	require.Empty(t, proposalsOf(obs, nil, dismissed, nil, "."))
	obs = append(obs, obsAt(3, "never mock database calls", "s3", now.Add(time.Hour)))
	require.Empty(t, proposalsOf(obs, nil, dismissed, nil, "."), "a new wording as the group's latest does not bring it back")
	require.NotEqual(t, dismissKey("use table tests"), dismissKey("do not use table tests"), "polarity is part of the key")
}

func TestProposals_IDsAreStableAcrossListChanges(t *testing.T) {
	now := time.Now()
	obs := []store.Observation{
		obsAt(1, "never mock the database", "s1", now), obsAt(2, "never mock the database", "s2", now),
		obsAt(3, "always run gofmt first", "s1", now), obsAt(4, "always run gofmt first", "s2", now),
		obsAt(5, "prefer table tests", "s1", now), obsAt(6, "prefer table tests", "s2", now),
	}
	first := proposalsOf(obs, nil, nil, nil, ".")
	require.Len(t, first, 3)
	ids := map[string]string{}
	for _, p := range first {
		ids[p.ID] = p.Text
	}
	remaining := proposalsOf(obs, []store.Rule{{Text: first[0].Text, Dir: "."}}, nil, nil, ".")
	require.Len(t, remaining, 2)
	for _, p := range remaining {
		require.Equal(t, ids[p.ID], p.Text, "an ID keeps naming the same proposal")
	}
	got, err := proposalByID("p"+strings.TrimPrefix(first[1].ID, "P"), remaining)
	require.NoError(t, err)
	require.Equal(t, first[1].Text, got.Text)
}

func TestLearnedRules_ScopeLimitAndForget(t *testing.T) {
	home, repo := learnRepo(t)
	front := filepath.Join(repo, "frontend")
	require.NoError(t, os.Mkdir(front, 0o755))
	add := func(dir, text string) int64 {
		var id int64
		require.NoError(t, withLearning(t.Context(), home, func(s *store.Store) (err error) {
			id, err = s.AddRule(t.Context(), store.Rule{Repository: repo, Dir: dir, Text: text, AcceptedAt: time.Now()})
			return err
		}))
		return id
	}
	add(".", "root rule")
	add("frontend", "frontend rule")
	require.Len(t, learnedRules(t.Context(), home, repo, "."), 1)
	require.Equal(t, "frontend rule", learnedRules(t.Context(), home, repo, "frontend")[0].Text)
	require.Empty(t, learnedRules(t.Context(), home, repo, "backend"))

	for i := range 12 {
		add(".", "rule number "+string(rune('a'+i)))
	}
	got := learnedRules(t.Context(), home, repo, ".")
	require.Len(t, got, maxRules)
	require.Equal(t, "rule number l", got[0].Text, "newest first")

	add(".", "bad\x1b[31mrule")
	add(".", "token ghp_abcdefghijklmnopqrstuvwx")
	for _, r := range learnedRules(t.Context(), home, repo, ".") {
		require.NotContains(t, r.Text, "\x1b")
		require.NotContains(t, r.Text, "ghp_")
	}
}

func TestInstallHooks_RegistersPromptObserver(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, installHooks(dir, "/usr/local/bin/codeforge"))
	data, err := os.ReadFile(filepath.Join(dir, "hooks.json"))
	require.NoError(t, err)
	var doc struct {
		Hooks map[string][]struct {
			Matcher string           `json:"matcher"`
			Hooks   []map[string]any `json:"hooks"`
		} `json:"hooks"`
	}
	require.NoError(t, json.Unmarshal(data, &doc))
	rules := doc.Hooks[promptEvent]
	require.Len(t, rules, 1)
	require.Empty(t, rules[0].Matcher, "prompts have no tool to match")
	require.Equal(t, true, rules[0].Hooks[0]["async"])
	require.Equal(t, "'/usr/local/bin/codeforge' observe", rules[0].Hooks[0]["command"])
	require.NotEqual(t, true, rules[0].Hooks[0]["failClosed"])
}

func TestHelp_ListsRuleCommands(t *testing.T) {
	for _, want := range []string{"learn proposals", "learn accept PN", "learn rules", "learn forget"} {
		require.Contains(t, helpText, want)
	}
}

func TestVectorNeighbours_OtherRepositoriesCannotCrowdOutAMatch(t *testing.T) {
	home := t.TempDir()
	s, err := store.Open(t.Context(), filepath.Join(configDir(home), "sessions"))
	require.NoError(t, err)
	defer func() { require.NoError(t, s.Close()) }()
	now := time.Now()
	add := func(repo, text, session string) store.Observation {
		o := store.Observation{Kind: store.KindCorrection, Text: text, Repository: repo, SessionID: session, Origin: store.OriginHook, CreatedAt: now}
		id, err := s.RecordWithVector(t.Context(), o, embed(text))
		require.NoError(t, err)
		o.ID = id
		return o
	}
	// Twenty exact copies elsewhere sit closer to each query than the real partner does.
	for i := range 20 {
		add("/other", "use table-driven tests", "x"+string(rune('a'+i)))
		add("/other", "prefer table tests", "y"+string(rune('a'+i)))
	}
	mine := []store.Observation{add("/repo", "use table-driven tests", "s1"), add("/repo", "prefer table tests", "s2")}
	near, err := vectorNeighbours(t.Context(), s, mine)
	require.NoError(t, err)
	got := proposalsOf(mine, nil, nil, near, ".")
	require.Len(t, got, 1, "the search widens past other repositories' corrections")
	require.Len(t, got[0].IDs, 2)
}

func TestVectorNeighbours_UnavailableFallsBackToWordOverlap(t *testing.T) {
	home := t.TempDir()
	s, err := store.OpenWith(t.Context(), filepath.Join(configDir(home), "sessions"), store.Options{DisableVector: true})
	require.NoError(t, err)
	defer func() { require.NoError(t, s.Close()) }()
	_, err = vectorNeighbours(t.Context(), s, nil)
	require.ErrorIs(t, err, errNoVectors)
}

func TestLoadLearning_BrokenVectorTableStillProposes(t *testing.T) {
	home, repo := learnRepo(t)
	teachCorrections(t, home, repo, "always use table-driven tests", "prefer table tests")
	db, err := sql.Open("sqlite", "file:"+filepath.Join(configDir(home), "sessions", "sessions.db"))
	require.NoError(t, err)
	for _, stmt := range []string{"DROP TABLE IF EXISTS correction_vec", "CREATE TABLE correction_vec (note TEXT)"} {
		_, err := db.ExecContext(t.Context(), stmt)
		require.NoError(t, err, stmt)
	}
	require.NoError(t, db.Close())
	obs, rules, dismissed, near, err := loadLearning(t.Context(), home, repo)
	require.NoError(t, err, "a vector failure never costs the user their proposals")
	require.Nil(t, near)
	require.Len(t, proposalsOf(obs, rules, dismissed, near, "."), 1)
}

func TestForget_RevertingEvidenceCannotResurrectARule(t *testing.T) {
	home, repo := learnRepo(t)
	teachCorrections(t, home, repo, "always use table-driven tests", "always use table-driven tests")
	var out bytes.Buffer
	adoptRules(t.Context(), home, repo, ".", &out)
	require.Contains(t, out.String(), "learned a rule")
	_, err := learn(t, home, "forget", "R1")
	require.NoError(t, err)

	// Reverting one source and adding a later wording must not bring the rule back.
	_, err = learn(t, home, "revert", "1")
	require.NoError(t, err)
	teachCorrections(t, home, repo, "prefer table-driven tests", "always use table-driven tests")
	out.Reset()
	adoptRules(t.Context(), home, repo, ".", &out)
	require.Empty(t, out.String(), "every wording the rule came from stays declined")
	require.Empty(t, learnedRules(t.Context(), home, repo, "."))
}

func TestLearnCorrect_RecordsTheDirectoryItWasTypedIn(t *testing.T) {
	home, repo := learnRepo(t)
	front := filepath.Join(repo, "frontend")
	require.NoError(t, os.Mkdir(front, 0o755))
	t.Chdir(front)
	_, err := learn(t, home, "correct", "always use table-driven tests")
	require.NoError(t, err)
	_, err = learn(t, home, "correct", "prefer table tests")
	require.NoError(t, err)
	obs := loadObservations(t, home, repo)
	require.Len(t, obs, 2)
	require.Equal(t, "frontend", obs[0].Dir)
	require.Empty(t, proposalsOf(obs, nil, nil, nil, "."), "corrections typed in frontend never create a root rule")
	require.Len(t, proposalsOf(obs, nil, nil, nil, "frontend"), 1, "they do create one for frontend, where they were typed")
}

func TestProposals_LegacyCLICorrectionsHaveNoScope(t *testing.T) {
	now := time.Now()
	legacy := func(id int64, text, session string) store.Observation {
		o := obsAt(id, text, session, now)
		o.Origin, o.Dir = store.OriginCLI, "?"
		return o
	}
	obs := []store.Observation{legacy(1, "always use table tests", "s1"), legacy(2, "prefer table tests", "s2")}
	require.Empty(t, proposalsOf(obs, nil, nil, nil, "."), "a correction of unknown scope never becomes a root rule")
	require.Empty(t, proposalsOf(obs, nil, nil, nil, "frontend"))
}

func TestProposals_SwappedNegationTargetsNeverMerge(t *testing.T) {
	now := time.Now()
	obs := []store.Observation{
		obsAt(1, "prefer unit tests, not integration tests in this package", "s1", now),
		obsAt(2, "prefer integration tests, not unit tests in this package", "s2", now),
	}
	require.Empty(t, proposalsOf(obs, nil, nil, nil, "."), "the reverse of a correction is not the same rule, by word overlap")
	vec := func(_ store.Observation) []store.Neighbour {
		return []store.Neighbour{{ID: 1, Distance: 0}, {ID: 2, Distance: 0}}
	}
	require.Empty(t, proposalsOf(obs, nil, nil, vec, "."), "nor by identical embeddings")
	rule := []store.Rule{{Text: "prefer unit tests, not integration tests in this package", Dir: "."}}
	require.Len(t, proposalsOf([]store.Observation{obsAt(3, "prefer integration tests, not unit tests in this package", "s1", now), obsAt(4, "prefer integration tests, not unit tests in this package", "s2", now)}, rule, nil, nil, "."), 1)
}

func TestProposals_ReplacementDirectionNeverMerges(t *testing.T) {
	now := time.Now()
	obs := []store.Observation{obsAt(1, "always use sqlite instead of postgres", "s1", now), obsAt(2, "always use postgres instead of sqlite", "s2", now)}
	require.Empty(t, proposalsOf(obs, nil, nil, nil, "."), "swapped replacement targets are opposite rules")
	vec := func(_ store.Observation) []store.Neighbour {
		return []store.Neighbour{{ID: 1, Distance: 0}, {ID: 2, Distance: 0}}
	}
	require.Empty(t, proposalsOf(obs, nil, nil, vec, "."), "even when their embeddings are identical")
	rule := []store.Rule{{Text: "always use sqlite instead of postgres", Dir: "."}}
	same := []store.Observation{obsAt(3, "always use postgres instead of sqlite", "s1", now), obsAt(4, "always use postgres rather than sqlite", "s2", now)}
	require.Len(t, proposalsOf(same, rule, nil, nil, "."), 1, "an accepted rule does not suppress its reverse")
}

func TestVectorNeighbours_OtherDirectoriesCannotCrowdOutAMatch(t *testing.T) {
	home := t.TempDir()
	s, err := store.Open(t.Context(), filepath.Join(configDir(home), "sessions"))
	require.NoError(t, err)
	defer func() { require.NoError(t, s.Close()) }()
	now := time.Now()
	add := func(dir, text, session string) store.Observation {
		o := store.Observation{Kind: store.KindCorrection, Text: text, Repository: "/repo", SessionID: session, Dir: dir, Origin: store.OriginHook, CreatedAt: now}
		id, err := s.RecordWithVector(t.Context(), o, embed(text))
		require.NoError(t, err)
		o.ID = id
		return o
	}
	var all []store.Observation
	for i := range 20 {
		all = append(all, add("frontend", "always use table-driven tests", "x"+string(rune('a'+i))), add("frontend", "prefer table tests", "y"+string(rune('a'+i))))
	}
	all = append(all, add(".", "always use table-driven tests", "s1"), add(".", "prefer table tests", "s2"))
	near, err := vectorNeighbours(t.Context(), s, all)
	require.NoError(t, err)
	require.Len(t, proposalsOf(all, nil, nil, near, "."), 1, "frontend copies never hide the root's own match")
}

func TestVectorNeighbours_WindowExhaustedFallsBack(t *testing.T) {
	home := t.TempDir()
	s, err := store.Open(t.Context(), filepath.Join(configDir(home), "sessions"))
	require.NoError(t, err)
	defer func() { require.NoError(t, s.Close()) }()
	saved := neighbourCap
	neighbourCap = ruleNeighbours
	t.Cleanup(func() { neighbourCap = saved })
	add := func(dir, text, session string) store.Observation {
		o := store.Observation{Kind: store.KindCorrection, Text: text, Repository: "/repo", SessionID: session, Dir: dir, Origin: store.OriginHook, CreatedAt: time.Now()}
		id, err := s.RecordWithVector(t.Context(), o, embed(text))
		require.NoError(t, err)
		o.ID = id
		return o
	}
	var all []store.Observation
	// Ten frontend copies sit exactly where the root's query lands, filling the whole search window.
	for i := range ruleNeighbours + 2 {
		all = append(all, add("frontend", "prefer table tests", "x"+string(rune('a'+i))))
	}
	all = append(all, add(".", "always use table-driven tests", "r1"), add(".", "prefer table tests", "r2"))
	_, err = vectorNeighbours(t.Context(), s, all)
	require.ErrorIs(t, err, errNoVectors, "a full window of other-directory vectors means word overlap decides, never a missed match")
	require.Len(t, proposalsOf(all, nil, nil, nil, "."), 1, "and word overlap still finds the root's pair")
}

func TestForget_IsAtomicWithItsDismissals(t *testing.T) {
	home, repo := learnRepo(t)
	teachCorrections(t, home, repo, "always use table-driven tests", "always use table-driven tests")
	adoptRules(t.Context(), home, repo, ".", io.Discard)
	require.NoError(t, withLearning(t.Context(), home, func(s *store.Store) error {
		return s.ForgetRuleAndDismiss(t.Context(), repo, 1, []string{dismissKey("always use table-driven tests")}, time.Now())
	}))
	require.NoError(t, withLearning(t.Context(), home, func(s *store.Store) error {
		_, _, err := s.AddRuleUnlessDismissed(t.Context(), store.Rule{Repository: repo, Dir: ".", Text: "always use table-driven tests", AcceptedAt: time.Now()}, []string{dismissKey("always use table-driven tests")}, maxRules)
		require.ErrorIs(t, err, store.ErrDismissed, "a launch that decided from older data cannot bring a forgotten rule back")
		return nil
	}))
	require.Empty(t, learnedRules(t.Context(), home, repo, "."))
	require.NoError(t, withLearning(t.Context(), home, func(s *store.Store) error {
		require.ErrorIs(t, s.ForgetRuleAndDismiss(t.Context(), repo, 99, []string{"-x"}, time.Now()), store.ErrNoRule)
		got, err := s.Dismissed(t.Context(), repo)
		require.NoError(t, err)
		require.NotContains(t, got, "-x", "a failed forget leaves no dismissal behind")
		return nil
	}))
}

func TestProposals_OverlongCLICorrectionsAreSkipped(t *testing.T) {
	now := time.Now()
	long := "always " + strings.Repeat("use table tests ", 30)
	obs := []store.Observation{obsAt(1, long, "s1", now), obsAt(2, long, "s2", now)}
	require.Empty(t, proposalsOf(obs, nil, nil, nil, "."), "a correction longer than a rule can be is never proposed")
}

func TestProposals_NestedNegationNeverMerges(t *testing.T) {
	now := time.Now()
	a := "stop running the database integration tests before each release"
	b := "please don't stop running the database integration tests before each release"
	require.False(t, compatible(a, b), "one negation against two can mean the opposite")
	obs := []store.Observation{obsAt(1, a, "s1", now), obsAt(2, b, "s2", now)}
	require.Empty(t, proposalsOf(obs, nil, nil, nil, "."), "by word overlap")
	vec := func(_ store.Observation) []store.Neighbour {
		return []store.Neighbour{{ID: 1, Distance: 0.3}, {ID: 2, Distance: 0.3}}
	}
	require.Empty(t, proposalsOf(obs, nil, nil, vec, "."), "and by embeddings")
	require.False(t, compatible("don't never mock the database", "don't never mock the database"), "several negations are too ambiguous to compare at all")
	require.True(t, compatible("never mock the database", "never mock the database please"))
}

func TestAddRuleUnlessDismissed_EnforcesTheLimitInsideTheTransaction(t *testing.T) {
	home, repo := learnRepo(t)
	require.NoError(t, withLearning(t.Context(), home, func(s *store.Store) error {
		for i := range 3 {
			_, created, err := s.AddRuleUnlessDismissed(t.Context(), store.Rule{Repository: repo, Dir: ".", Text: "rule " + string(rune('a'+i)), AcceptedAt: time.Now()}, nil, 3)
			require.NoError(t, err)
			require.True(t, created)
		}
		_, _, err := s.AddRuleUnlessDismissed(t.Context(), store.Rule{Repository: repo, Dir: ".", Text: "rule d", AcceptedAt: time.Now()}, nil, 3)
		require.ErrorIs(t, err, store.ErrRuleLimit)
		id, created, err := s.AddRuleUnlessDismissed(t.Context(), store.Rule{Repository: repo, Dir: ".", Text: "rule a", AcceptedAt: time.Now()}, nil, 3)
		require.NoError(t, err)
		require.False(t, created, "an identical live rule is reported as already there, not created again")
		require.NotZero(t, id)
		return nil
	}))
}

func TestProposals_ReversedPreferencesNeverMerge(t *testing.T) {
	now := time.Now()
	obs := []store.Observation{obsAt(1, "prefer sqlite over postgres", "s1", now), obsAt(2, "prefer postgres over sqlite", "s2", now)}
	require.False(t, compatible(obs[0].Text, obs[1].Text))
	require.Empty(t, proposalsOf(obs, nil, nil, nil, "."), "by word overlap")
	vec := func(_ store.Observation) []store.Neighbour {
		return []store.Neighbour{{ID: 1, Distance: 0}, {ID: 2, Distance: 0}}
	}
	require.Empty(t, proposalsOf(obs, nil, nil, vec, "."), "and by identical embeddings")
	rule := []store.Rule{{Text: "prefer sqlite over postgres", Dir: "."}}
	require.Len(t, proposalsOf([]store.Observation{obsAt(3, "prefer postgres over sqlite", "s1", now), obsAt(4, "prefer postgres over sqlite", "s2", now)}, rule, nil, nil, "."), 1, "an existing rule never suppresses its reverse")
	for _, pair := range [][2]string{{"use vitest versus jest", "use jest vs vitest"}, {"run lint before tests", "run tests before lint"}} {
		require.False(t, compatible(pair[0], pair[1]), pair[0])
	}
	require.True(t, compatible("prefer sqlite over postgres", "please prefer sqlite over postgres"))
}

func TestGroupCorrections_ChainsCannotJoinOppositeEndpoints(t *testing.T) {
	now := time.Now()
	texts := []string{
		"prefer sqlite caches, not postgres caches",
		"prefer sqlite redis caches, not postgres caches",
		"prefer redis caches, not sqlite postgres caches",
		"prefer postgres redis caches, not sqlite caches",
		"prefer postgres caches, not sqlite caches",
	}
	var obs []store.Observation
	for i, text := range texts {
		obs = append(obs, obsAt(int64(i+1), text, "s"+string(rune('a'+i)), now))
	}
	require.False(t, compatible(texts[0], texts[4]), "the endpoints are opposites")
	for _, near := range []func(store.Observation) []store.Neighbour{nil, func(_ store.Observation) []store.Neighbour {
		var out []store.Neighbour
		for _, other := range obs {
			out = append(out, store.Neighbour{ID: other.ID, Distance: 0.1})
		}
		return out
	}} {
		for _, group := range groupCorrections(obs, near) {
			require.False(t, slices.Contains(group, 0) && slices.Contains(group, 4), "opposite endpoints are never in one group: %v", group)
		}
	}
}

func TestProposals_OrderSensitiveCorrectionsNeverMerge(t *testing.T) {
	now := time.Now()
	for _, pair := range [][2]string{
		{"always run tests after lint", "always run lint after tests"},
		{"run lint before tests", "run tests before lint"},
		{"prefer sqlite over postgres", "prefer postgres over sqlite"},
	} {
		require.False(t, compatible(pair[0], pair[1]), pair[0])
		obs := []store.Observation{obsAt(1, pair[0], "s1", now), obsAt(2, pair[1], "s2", now)}
		require.Empty(t, proposalsOf(obs, nil, nil, nil, "."), "by word overlap: "+pair[0])
		vec := func(_ store.Observation) []store.Neighbour {
			return []store.Neighbour{{ID: 1, Distance: 0}, {ID: 2, Distance: 0}}
		}
		require.Empty(t, proposalsOf(obs, nil, nil, vec, "."), "by identical embeddings: "+pair[0])
		rule := []store.Rule{{Text: pair[0], Dir: "."}}
		require.Len(t, proposalsOf([]store.Observation{obsAt(3, pair[1], "s1", now), obsAt(4, pair[1], "s2", now)}, rule, nil, nil, "."), 1, "a rule never suppresses its reverse: "+pair[0])
	}
	for _, pair := range [][2]string{
		{"always use table-driven tests", "prefer table tests"},
		{"never mock the database", "never mock database calls"},
		{"always run tests after lint", "please always run tests after lint"},
	} {
		require.True(t, compatible(pair[0], pair[1]), "the same order still groups: "+pair[0])
	}
}

func TestLearnAccept_DoesNotOverwriteAConcurrentDismiss(t *testing.T) {
	home, repo := learnRepo(t)
	teachCorrections(t, home, repo, "always use table-driven tests", "prefer table tests")
	obs, rules, dismissed, near, err := loadLearning(t.Context(), home, repo)
	require.NoError(t, err)
	props := proposalsOf(obs, rules, dismissed, near, ".")
	require.Len(t, props, 1)
	// Another process declines the group after this one listed it.
	_, err = learn(t, home, "dismiss", props[0].ID)
	require.NoError(t, err)
	_, err = learn(t, home, "accept", props[0].ID)
	require.ErrorIs(t, err, errNoProposal, "a stale accept finds the proposal gone")
	require.Empty(t, learnedRules(t.Context(), home, repo, "."))
}

func TestFuzzyGroupsWaitForAccept(t *testing.T) {
	home, repo := learnRepo(t)
	teachCorrections(t, home, repo, "always use table-driven tests", "prefer table tests")
	var out bytes.Buffer
	adoptRules(t.Context(), home, repo, ".", &out)
	require.Empty(t, out.String(), "differently worded corrections are not adopted without the user")
	require.Empty(t, learnedRules(t.Context(), home, repo, "."))
	listing, err := learn(t, home, "proposals")
	require.NoError(t, err)
	require.Contains(t, listing, "P1")
	accepted, err := learn(t, home, "accept", "P1")
	require.NoError(t, err)
	require.Contains(t, accepted, "accepted rule R1")
	require.Len(t, learnedRules(t.Context(), home, repo, "."), 1)
}

func TestSessionlessCorrectionsAreNeverAdoptedAutomatically(t *testing.T) {
	home, repo := learnRepo(t)
	for range 2 {
		_, err := learn(t, home, "correct", "always use table-driven tests")
		require.NoError(t, err)
	}
	var out bytes.Buffer
	adoptRules(t.Context(), home, repo, ".", &out)
	require.Empty(t, out.String(), "two corrections typed with no recorded session are not two sessions")
	require.Empty(t, learnedRules(t.Context(), home, repo, "."))
}

func TestOppositeActionsAreNeverAdoptedAutomatically(t *testing.T) {
	home, repo := learnRepo(t)
	teachCorrections(t, home, repo, "always enable TLS certificate verification for database connections", "always disable TLS certificate verification for database connections")
	var out bytes.Buffer
	adoptRules(t.Context(), home, repo, ".", &out)
	require.Empty(t, out.String(), "enable and disable are not the same sentence, however alike they look")
	require.Empty(t, learnedRules(t.Context(), home, repo, "."))
	teachCorrections(t, home, repo, "always disable the cache", "Always disable the cache!")
	adoptRules(t.Context(), home, repo, ".", &out)
	require.Contains(t, strings.ToLower(out.String()), "always disable the cache", "the same sentence, ignoring case and punctuation, is")
}

func TestAutoRepeatKeepsOperatorsAndNeedsTheSameSentence(t *testing.T) {
	require.NotEqual(t, sentenceKey("always check retries < 3"), sentenceKey("always check retries > 3"))
	require.Equal(t, sentenceKey("Always disable the cache!"), sentenceKey("always   disable the cache"))
	home, repo := learnRepo(t)
	teachCorrections(t, home, repo, "always check retries < 3", "always check retries > 3")
	var out bytes.Buffer
	adoptRules(t.Context(), home, repo, ".", &out)
	require.Empty(t, out.String(), "two different sentences are not a repeat, however alike they look")
	listing, err := learn(t, home, "proposals")
	require.NoError(t, err)
	require.Contains(t, listing, "worded differently each time", "they wait for the user")
}

func TestCoverageNeverHidesAnOpposite(t *testing.T) {
	now := time.Now()
	rule := []store.Rule{{Text: "always enable TLS certificate verification for database connections", Dir: "."}}
	opposite := []store.Observation{
		obsAt(1, "always disable TLS certificate verification for database connections", "s1", now),
		obsAt(2, "always disable TLS certificate verification for database connections", "s2", now),
	}
	require.Len(t, proposalsOf(opposite, rule, nil, nil, "."), 1, "an accepted rule does not suppress the reversal")
	forgotten := now
	rule[0].ForgottenAt = &forgotten
	require.Len(t, proposalsOf(opposite, rule, nil, nil, "."), 1, "nor does a forgotten one")
	same := []store.Observation{obsAt(3, "Always enable TLS certificate verification for database connections.", "s1", now), obsAt(4, "always enable TLS certificate verification for database connections", "s2", now)}
	require.Empty(t, proposalsOf(same, rule, nil, nil, "."), "the same sentence is covered, ignoring case and trailing punctuation")
}

func TestDeclinedSentenceDoesNotHideALookalike(t *testing.T) {
	now := time.Now()
	dismissed := map[string]bool{dismissKey("always check retries < 3"): true}
	lookalike := []store.Observation{obsAt(1, "always check retries > 3", "s1", now), obsAt(2, "always check retries > 3", "s2", now)}
	require.Len(t, proposalsOf(lookalike, nil, dismissed, nil, "."), 1)
}

func TestRuleCoversItsOwnSourceGroup(t *testing.T) {
	now := time.Now()
	obs := []store.Observation{obsAt(7, "always use table-driven tests", "s1", now), obsAt(8, "prefer table tests", "s2", now)}
	rule := []store.Rule{{Text: "something the user edited", SourceIDs: "#7 #8", Dir: "."}}
	require.Empty(t, proposalsOf(obs, rule, nil, nil, "."), "a rule covers the group it was learned from")
}

func TestOppositeWordingIsNotSwallowedByTheEvidenceOfAnExistingRule(t *testing.T) {
	now := time.Now()
	enable := "always enable TLS certificate verification for database connections"
	disable := "always disable TLS certificate verification for database connections"
	obs := []store.Observation{obsAt(1, enable, "s1", now), obsAt(2, enable, "s2", now), obsAt(3, disable, "s3", now), obsAt(4, disable, "s4", now)}
	rule := []store.Rule{{Text: enable, SourceIDs: "auto #1 #2", Dir: "."}}
	got := proposalsOf(obs, rule, nil, nil, ".")
	require.Len(t, got, 1, "the original observations stay behind the rule; the reversal is judged on its own")
	require.Equal(t, disable, got[0].AutoText)
}
