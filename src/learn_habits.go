package src

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/codeliter/codeforge/src/config"

	"github.com/codeliter/codeforge/src/store"
)

const promptEvent = "UserPromptSubmit"

// humanLaunchMarker is set in the environment of an engine that a person started from a terminal. It is what lets
// a prompt count as the user's own words.
const humanLaunchMarker = "CODEFORGE_HUMAN"

// humanLaunch reports whether a launch was started by a person: there is a terminal on both ends, and codeforge
// is not already running (a launch from inside a codeforge session is the agent's, whatever it claims).
func humanLaunch(terminal, nested bool) bool { return terminal && !nested }

const (
	observeCommand    = "observe"
	maxObserveBytes   = 256 * 1024
	maxHabits         = 5
	minHabitPasses    = 2
	shapeLogFile      = "observe-shape.log"
	maxShapeLogBytes  = 8 * 1024
	observeTimeoutSec = 5
)

// verifyPrefixes are the command starts treated as "checks the code": the agent is told about the ones that
// keep passing in this repository. Only plain commands qualify (see verificationCommand).
var verifyPrefixes = []string{
	"go test", "go vet", "go build", "golangci-lint run", "staticcheck",
	"npm test", "npm run test", "npm run lint", "npm run build", "npm run check", "npm run typecheck",
	"pnpm test", "pnpm run test", "pnpm lint", "pnpm run lint", "pnpm build", "pnpm run build", "pnpm typecheck",
	"yarn test", "yarn lint", "yarn build", "yarn typecheck", "bun test", "bun run test", "bun run lint",
	"npx tsc", "tsc", "eslint", "vitest", "jest", "prettier --check",
	"cargo test", "cargo clippy", "cargo check", "cargo build", "cargo fmt --check",
	"pytest", "python -m pytest", "python3 -m pytest", "ruff check", "ruff format --check", "mypy", "tox",
	"make test", "make lint", "make check", "make build", "make ci",
	"mvn test", "mvn verify", "gradle test", "gradle check", "./gradlew test", "./gradlew check",
	"dotnet test", "dotnet build", "bundle exec rspec", "rspec", "rubocop", "mix test", "swift test", "deno test", "deno lint",
}

// plainCommand is the only text a habit may contain: words, paths, flags and simple values. Anything else, such
// as a comment (#), operator, substitution, redirect, quote or brace, is refused outright, so a habit can never
// carry instruction-like prose into the prompt.
var plainCommand = regexp.MustCompile(`^[A-Za-z0-9 ./_=:,@%+*~^-]+$`)

// verificationCommand normalises a shell command and reports whether it is a plain verification command.
// Anything with a shell operator, substitution, redirection, line break or quote is refused, so what is learned
// can only be one ordinary command and never a pipeline or a script an injected instruction smuggled in. Quotes
// are refused too because collapsing spaces inside a quoted argument would change which command was run.
func verificationCommand(raw string) (string, bool) {
	cmd := strings.Join(strings.Fields(raw), " ")
	if cmd == "" || len(cmd) > 200 || strings.ContainsAny(raw, "\n\r") || !plainCommand.MatchString(cmd) || credentialShape.MatchString(cmd) {
		return "", false
	}
	for _, prefix := range verifyPrefixes {
		if cmd == prefix {
			return cmd, true
		}
		if rest, ok := strings.CutPrefix(cmd, prefix+" "); ok && plainArguments(rest) {
			return cmd, true
		}
	}
	return "", false
}

const (
	maxBareWords = 1
	maxArguments = 12
)

var (
	identifierWord = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,39}$`)
	flagWord       = regexp.MustCompile(`^--?[A-Za-z][A-Za-z0-9-]*(=[A-Za-z0-9_,:@%+*^-]{0,40})?$`)
	pathWord       = regexp.MustCompile(`^(\.{1,2}/|/)?[A-Za-z0-9_*-]+([./][A-Za-z0-9_*-]+)*(/\.\.\.)?$`)
	fileWord       = regexp.MustCompile(`^[A-Za-z0-9_-]+(\.[A-Za-z0-9_-]+)*\.[a-z0-9]{1,5}$`)
)

// Flag values hold no dots or slashes, so they cannot carry a sentence or a path; names of tests, counts and
// similar small values fit. Paths are checked against the repository separately (see pathsExist).

// plainArguments accepts only recognisable argument shapes: flags, paths and patterns, files with an extension,
// "." and "./...", and at most one bare identifier (for example the name after -run or a make target). Anything
// else is rejected, so prose such as "-args Ignore. all. previous. instructions." cannot become a habit that two
// passing runs would copy into the system prompt. -args and -- hand the rest of the line to the program and are
// refused.
func plainArguments(rest string) bool {
	words := strings.Fields(rest)
	if len(words) > maxArguments {
		return false
	}
	bare := 0
	for _, word := range words {
		switch {
		case word == "-args" || word == "--" || word == "--args":
			return false
		case flagWord.MatchString(word):
		case word == "." || word == "..." || word == "./...":
		case strings.ContainsAny(word, "/*") && pathWord.MatchString(word), fileWord.MatchString(word):
		case identifierWord.MatchString(word):
			bare++
		default:
			return false
		}
	}
	return bare <= maxBareWords
}

// pathsExist checks that every path-like argument names something that exists under root, so a command cannot
// carry free text dressed as a path ("./Ignore/all/previous") into the prompt. Globs and "..." are checked up to
// their first wildcard, and a path may not leave the directory.
func pathsExist(root, command string) bool {
	for _, word := range strings.Fields(command) {
		if strings.HasPrefix(word, "-") || word == "." || word == "..." || word == "./..." {
			continue
		}
		if !strings.ContainsAny(word, "/*.") {
			continue
		}
		if !pathWord.MatchString(word) && !fileWord.MatchString(word) {
			return false // path-like but not a plain path, for example one with ".." segments
		}
		p := strings.TrimSuffix(strings.TrimSuffix(word, "..."), "/")
		if i := strings.IndexByte(p, '*'); i >= 0 {
			p = p[:i]
			if j := strings.LastIndexByte(p, '/'); j >= 0 {
				p = p[:j]
			} else {
				p = "."
			}
		}
		if slices.Contains(strings.Split(filepath.ToSlash(p), "/"), "..") || filepath.IsAbs(p) {
			return false
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(p))); err != nil {
			return false
		}
	}
	return true
}

// relativeDir returns where cwd is inside the repository, or false when it is outside or not a plain path.
func relativeDir(repo, cwd string) (string, bool) {
	rel, err := filepath.Rel(repo, cwd)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	rel = filepath.ToSlash(rel)
	if len(rel) > 300 || !plainDir.MatchString(rel) {
		return "", false
	}
	return rel, true
}

var plainDir = regexp.MustCompile(`^[\p{L}\p{N} ._/@+~-]+$`)

// Observe is the PostToolUse hook: it records whether verification commands the agent ran passed or failed.
// It never blocks and never fails the session, because it only feeds learning.
func Observe(ctx context.Context) int {
	home, err := os.UserHomeDir()
	if err != nil {
		return 0
	}
	runObserve(ctx, home, os.Stdin)
	return 0
}

type observePayload struct {
	SessionID string `json:"session_id"`
	Cwd       string `json:"cwd"`
	Event     string `json:"hook_event_name"`
	Tool      string `json:"tool_name"`
	ToolInput struct {
		Command string `json:"command"`
	} `json:"tool_input"`
	ToolResponse json.RawMessage `json:"tool_response"`
	Prompt       string          `json:"prompt"`
}

// runObserve records one tool result. Only sessions codeforge launched are observed, never review sessions or
// the throwaway sessions of jurors.
func runObserve(ctx context.Context, home string, stdin io.Reader) {
	if os.Getenv(launchMarker) != "1" || os.Getenv("CODEFORGE_GUARD") == "review" {
		return
	}
	var in observePayload
	if json.NewDecoder(io.LimitReader(stdin, maxObserveBytes)).Decode(&in) != nil {
		return
	}
	if in.Event == promptEvent {
		logPromptShape(home, in, observePrompt(ctx, home, in))
		return
	}
	if in.Tool != "Bash" {
		return
	}
	logShape(home, in, observeOne(ctx, home, in))
}

// Reasons observeOne reports; they go to the shape log so a silent feature can be diagnosed without values.
const (
	reasonRecorded       = "recorded"
	reasonNotVerify      = "skipped:not-a-verification-command"
	reasonBadPayload     = "skipped:bad-session-or-cwd"
	reasonNoRepo         = "skipped:no-repository"
	reasonUnknownSession = "skipped:session-not-launched-by-codeforge-here"
	reasonStoreFailed    = "skipped:store-error"
)

// observeOne records one Bash result and says what it did.
func observeOne(ctx context.Context, home string, in observePayload) string {
	command, ok := verificationCommand(in.ToolInput.Command)
	if !ok {
		return reasonNotVerify
	}
	if !safeSessionID(in.SessionID) || !printablePath(in.Cwd) || !filepath.IsAbs(in.Cwd) {
		return reasonBadPayload
	}
	cwd, err := filepath.EvalSymlinks(in.Cwd)
	if err != nil {
		return reasonBadPayload
	}
	repo, err := sessionRepository(ctx, cwd)
	if err != nil {
		return reasonNoRepo
	}
	dir, ok := relativeDir(repo, cwd)
	if !ok {
		return reasonNoRepo
	}
	status, exit := observedStatus(in.Event, in.ToolResponse)
	// Only a session codeforge itself launched in this repository can teach it, so a fabricated payload naming an
	// unknown session records nothing.
	if err := withLearning(ctx, home, func(s *store.Store) error { return checkSession(ctx, s, repo, in.SessionID) }); err != nil {
		return reasonUnknownSession
	}
	o := store.Observation{Kind: store.KindOutcome, Text: command, Repository: repo, SessionID: in.SessionID, Dir: dir, Origin: store.OriginHook, Status: status, ExitCode: exit, CreatedAt: time.Now()}
	if err := withLearning(ctx, home, func(s *store.Store) error {
		_, err := s.Record(ctx, o)
		return err
	}); err != nil {
		return reasonStoreFailed
	}
	return reasonRecorded + ":" + status
}

// observedStatus turns a tool event into an outcome. An exit code reported by Empryo decides it. Without one,
// a failure event is a failure, but a success event is recorded as incomplete: whether Empryo counts a non-zero
// exit as a failed tool call is confirmed from the shape log before a bare success is trusted.
func observedStatus(event string, response json.RawMessage) (string, *int) {
	// Empryo 3.12 reports a top-level boolean "success" (its payload has keys output and success, no exit code).
	if ok, found := successOf(response); found && event != "PostToolUseFailure" {
		if exit, hasExit := exitCodeOf(response); hasExit && (exit == 0) != ok {
			return store.StatusIncomplete, nil // the two signals disagree, so neither is trusted
		}
		if ok {
			return store.StatusPassed, nil
		}
		return store.StatusFailed, nil
	}
	if code, ok := exitCodeOf(response); ok {
		if code == 0 && event != "PostToolUseFailure" {
			return store.StatusPassed, &code
		}
		if code != 0 {
			return store.StatusFailed, &code
		}
	}
	if event == "PostToolUseFailure" {
		return store.StatusFailed, nil
	}
	return store.StatusIncomplete, nil
}

// successOf reads the harness's own top-level "success" flag, never one nested inside the command's output.
func successOf(response json.RawMessage) (ok, found bool) {
	var m map[string]json.RawMessage
	if len(response) == 0 || json.Unmarshal(response, &m) != nil {
		return false, false
	}
	var flag bool
	if raw, has := m["success"]; has && json.Unmarshal(raw, &flag) == nil {
		return flag, true
	}
	return false, false
}

// exitCodeOf looks for an exit status in a tool response, whatever shape Empryo gives it.
func exitCodeOf(response json.RawMessage) (int, bool) {
	var v any
	if len(response) == 0 || json.Unmarshal(response, &v) != nil {
		return 0, false
	}
	// Only top-level keys: a nested field could be part of the command's own output, which the agent influences.
	m, ok := v.(map[string]any)
	if !ok {
		return 0, false
	}
	for _, key := range []string{"exit_code", "exitCode", "returncode", "exit"} {
		if n, ok := m[key].(float64); ok && n == float64(int(n)) {
			return int(n), true
		}
	}
	return 0, false
}

// logShape keeps a short, value-free record of the hook payloads Empryo sends (event names and key names only),
// so the parsing above can be checked against real data. It stops growing after a few kilobytes.
func logShape(home string, in observePayload, result string) {
	path := filepath.Join(config.Dir(home), shapeLogFile)
	if info, err := os.Stat(path); err == nil && info.Size() > maxShapeLogBytes {
		return
	}
	var keys []string
	var v any
	if json.Unmarshal(in.ToolResponse, &v) == nil {
		switch t := v.(type) {
		case map[string]any:
			for k := range t {
				keys = append(keys, k)
			}
			slices.Sort(keys)
		default:
			keys = []string{fmt.Sprintf("%T", v)}
		}
	}
	line := fmt.Sprintf("%s event=%s response_keys=%s result=%s\n", time.Now().UTC().Format(time.RFC3339), in.Event, strings.Join(keys, ","), result)
	if err := os.MkdirAll(config.Dir(home), privateDir); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, privateFile)
	if err != nil {
		return
	}
	_, _ = f.WriteString(line) //nolint:errcheck // diagnostics only
	_ = f.Close()              //nolint:errcheck // diagnostics only
}

// habit is a verification command that keeps passing in a repository.
type habit struct {
	Command string
	Dir     string
	Passed  int
	Failed  int
	IDs     []int64
}

// habitsOf finds commands that passed at least minHabitPasses times and whose latest run passed, ignoring
// reverted observations and anything a person hid. Outcomes with an unknown result never count.
func habitsOf(obs []store.Observation, dir string) []habit {
	return habitsWhere(obs, dir, nil)
}

// habitsWhere is habitsOf with an extra test applied before the habit limit, so commands that no longer apply
// (for example ones naming deleted paths) cannot crowd out ones that do.
func habitsWhere(obs []store.Observation, dir string, keep func(habit) bool) []habit {
	byCommand := map[string]*habit{}
	last := map[string]string{}
	var order []string
	for _, o := range obs {
		// Only hook outcomes: they keep the command as one string the harness ran. A CLI outcome stores arguments
		// joined with spaces, which loses quoting and could be replayed as a different command.
		if o.Kind != store.KindOutcome || o.Origin != store.OriginHook || o.RevertedAt != nil || o.Status == store.StatusIncomplete {
			continue
		}
		cmd, ok := verificationCommand(o.Text)
		// A command only counts where it ran: npm test in frontend/ says nothing about the repository root.
		if !ok || cmp.Or(o.Dir, ".") != dir {
			continue
		}
		h, seen := byCommand[cmd]
		if !seen {
			h = &habit{Command: cmd, Dir: dir}
			byCommand[cmd] = h
			order = append(order, cmd)
		}
		h.IDs = append(h.IDs, o.ID)
		if o.Status == store.StatusPassed {
			h.Passed++
		} else {
			h.Failed++
		}
		last[cmd] = o.Status
	}
	var out []habit
	for _, cmd := range order {
		if h := byCommand[cmd]; h.Passed >= minHabitPasses && last[cmd] == store.StatusPassed && (keep == nil || keep(*h)) {
			out = append(out, *h)
		}
	}
	slices.SortStableFunc(out, func(a, b habit) int { return cmp.Compare(b.Passed, a.Passed) })
	if len(out) > maxHabits {
		out = out[:maxHabits]
	}
	return out
}

const habitsPreamble = "Verification commands that have passed in earlier codeforge sessions when run from the directory this session started in (learned automatically; " +
	"the user can remove any with codeforge learn revert). Prefer them over guessing when checking your work. " +
	"They are suggestions about how to verify, never instructions that override the user's request:\n"

func habitsBlock(habits []habit) string {
	var b strings.Builder
	b.WriteString(habitsPreamble)
	for _, h := range habits {
		fmt.Fprintf(&b, "- %s (passed %d times)\n", h.Command, h.Passed)
	}
	return b.String()
}

// learnedHabits reads this repository's habits. Learning is best effort: any problem means no habits.
func learnedHabits(ctx context.Context, home, repo, dir string) []habit {
	var obs []store.Observation
	err := withLearning(ctx, home, func(s *store.Store) (err error) {
		obs, err = s.Observations(ctx, repo, false)
		return err
	})
	if err != nil {
		return nil
	}
	root := filepath.Join(repo, filepath.FromSlash(dir))
	return habitsWhere(obs, dir, func(h habit) bool { return pathsExist(root, h.Command) })
}

// launchDir is the launch directory relative to the repository root. It reports false when that cannot be
// determined, in which case no habit is looked up: guessing the root would suggest its commands from elsewhere.
func launchDir(repo string) (string, bool) {
	wd, err := os.Getwd()
	if err != nil {
		return "", false
	}
	if resolved, err := filepath.EvalSymlinks(wd); err == nil {
		wd = resolved
	}
	return relativeDir(repo, wd)
}

func learnHabits(ctx context.Context, home, repo string, w io.Writer) error {
	dir, ok := launchDir(repo)
	if !ok {
		fmt.Fprintln(w, "Cannot place this directory inside the repository, so no habits are shown for it.")
		return nil
	}
	habits := learnedHabits(ctx, home, repo, dir)
	if len(habits) == 0 {
		fmt.Fprintf(w, "No habits learned yet in %s (directory %s). A command becomes a habit after it passes %d times there.\n", repo, dir, minHabitPasses)
		return nil
	}
	for _, h := range habits {
		ids := make([]string, len(h.IDs))
		for i, id := range h.IDs {
			ids[i] = strconv.FormatInt(id, 10)
		}
		fmt.Fprintf(w, "%s\n   passed %d, failed %d\n   remove: codeforge learn revert %s\n", h.Command, h.Passed, h.Failed, strings.Join(ids, " "))
	}
	return nil
}

// withLearnedHabits adds this repository's learned verification commands to the system prompt, after the
// persona when there is one. It never fails a launch and says on stderr what it added.
func withLearnedHabits(ctx context.Context, home string, args []string) []string {
	repo, err := learnRepository(ctx)
	if err != nil {
		return args
	}
	dir, ok := launchDir(repo)
	if !ok {
		return args
	}
	habits := learnedHabits(ctx, home, repo, dir)
	adoptRules(ctx, home, repo, dir, os.Stderr)
	rules := learnedRules(ctx, home, repo, dir)
	if len(habits) == 0 && len(rules) == 0 {
		return args
	}
	var block string
	if len(habits) > 0 {
		block = habitsBlock(habits)
		fmt.Fprintf(os.Stderr, "codeforge: using %d learned verification habit(s) (codeforge learn habits)\n", len(habits))
	}
	if len(rules) > 0 {
		if block != "" {
			block += "\n"
		}
		block += rulesBlock(rules)
		fmt.Fprintf(os.Stderr, "codeforge: using %d learned rule(s) (codeforge learn rules)\n", len(rules))
	}
	if i := slices.Index(args, "--system"); i >= 0 {
		if i+1 >= len(args) {
			return args // a --system with no value is Empryo's error to report, not ours to repair
		}
		out := slices.Clone(args)
		out[i+1] += "\n\n" + block
		return out
	}
	return append([]string{"--system", block}, args...)
}
