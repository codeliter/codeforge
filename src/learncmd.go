package src

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/codeliter/codeforge/src/store"
)

var (
	errLearnUsage   = errors.New("usage: codeforge learn correct [--session ID] TEXT | outcome [--session ID] -- COMMAND [ARGS] | list [--all] | propose | revert ID")
	errLearnText    = errors.New("text must be one line of 1 to 2000 printable characters")
	errLearnSecret  = errors.New("text looks like it contains a credential; nothing was saved")
	errLearnSession = errors.New("unknown codeforge session for this repository")
	errCheckFailed  = errors.New("check did not pass")
	errLearnCwd     = errors.New("learn does not read --cwd; run it from the repository")
)

const (
	maxLearnText    = 2000
	minCorrections  = 2
	outcomeTimeout  = 10 * time.Minute
	outcomeKillWait = 5 * time.Second
)

// credentialShape is a hygiene check so obvious secrets are not written to disk. It is not a policy boundary.
var credentialShape = regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----|\b(gh[pousr]|github_pat)_[A-Za-z0-9_]{20,}|\bsk-[A-Za-z0-9_-]{20,}|\bAKIA[0-9A-Z]{16}\b|\bxox[abprs]-[A-Za-z0-9-]{10,}|(?i)\b(password|passwd|secret|token|api[_-]?key)\s*[=:]\s*\S`)

// runLearn records and reads inert learning evidence. Nothing here changes prompts, personas or guard policy.
func runLearn(ctx context.Context, home string, args []string, w io.Writer) error {
	if len(args) == 0 {
		return errLearnUsage
	}
	repo, err := learnRepository(ctx)
	if err != nil {
		return err
	}
	switch args[0] {
	case "correct":
		return learnCorrect(ctx, home, repo, args[1:], w)
	case "outcome":
		return learnOutcome(ctx, home, repo, args[1:], w, outcomeTimeout)
	case "list":
		all := slices.Equal(args[1:], []string{"--all"})
		if len(args) > 1 && !all {
			return errLearnUsage
		}
		return learnList(ctx, home, repo, all, w)
	case "propose":
		if len(args) != 1 {
			return errLearnUsage
		}
		return learnPropose(ctx, home, repo, w)
	case "revert":
		if len(args[1:]) != 1 {
			return errLearnUsage
		}
		return learnRevert(ctx, home, repo, args[1], w)
	}
	return errLearnUsage
}

func learnRepository(ctx context.Context) (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("finding working directory: %w", err)
	}
	wd, err = filepath.EvalSymlinks(wd)
	if err != nil {
		return "", fmt.Errorf("resolving working directory: %w", err)
	}
	return sessionRepository(ctx, wd)
}

func openLearning(ctx context.Context, home string) (*store.Store, error) {
	return store.Open(ctx, filepath.Join(configDir(home), "sessions"))
}

// learnSession consumes a leading --session ID or --session=ID.
func learnSession(args []string) (string, []string, error) {
	if len(args) == 0 {
		return "", args, nil
	}
	id, ok := strings.CutPrefix(args[0], "--session=")
	switch {
	case ok:
		args = args[1:]
	case args[0] != "--session":
		return "", args, nil
	case len(args) == 1:
		return "", nil, errLearnUsage
	default:
		id, args = args[1], args[2:]
	}
	if !safeSessionID(id) {
		return "", nil, errLearnUsage
	}
	return id, args, nil
}

func checkLearnText(text string) error {
	if text == "" || utf8.RuneCountInString(text) > maxLearnText || !utf8.ValidString(text) || strings.IndexFunc(text, unicode.IsControl) >= 0 {
		return errLearnText
	}
	if credentialShape.MatchString(text) {
		return errLearnSecret
	}
	return nil
}

// checkSession accepts only sessions codeforge launched in this repository, so evidence names a real session.
func checkSession(ctx context.Context, s *store.Store, repo, id string) error {
	if id == "" {
		return nil
	}
	saved, err := s.Get(ctx, id)
	if errors.Is(err, store.ErrNotFound) || err == nil && saved.Repository != repo {
		return fmt.Errorf("%w: %s", errLearnSession, id)
	}
	return err
}

func learnCorrect(ctx context.Context, home, repo string, args []string, w io.Writer) error {
	session, rest, err := learnSession(args)
	if err != nil {
		return err
	}
	// --session is only read before the text; anywhere else it would be saved as part of the correction.
	if slices.ContainsFunc(rest, func(a string) bool { return strings.HasPrefix(a, "--session") }) {
		return errLearnUsage
	}
	text := strings.TrimSpace(strings.Join(rest, " "))
	if err := checkLearnText(text); err != nil {
		return err
	}
	var id int64
	if err := withLearning(ctx, home, func(s *store.Store) error {
		if err := checkSession(ctx, s, repo, session); err != nil {
			return err
		}
		id, err = s.Record(ctx, store.Observation{Kind: store.KindCorrection, Text: text, Repository: repo, SessionID: session, Origin: store.OriginCLI, CreatedAt: time.Now()})
		return err
	}); err != nil {
		return err
	}
	fmt.Fprintf(w, "codeforge: recorded correction #%d (evidence only; nothing is applied)\n", id)
	return nil
}

func learnOutcome(ctx context.Context, home, repo string, args []string, w io.Writer, timeout time.Duration) error {
	session, rest, err := learnSession(args)
	if err != nil {
		return err
	}
	if len(rest) < 2 || rest[0] != "--" {
		return errLearnUsage
	}
	argv := rest[1:]
	text := strings.Join(argv, " ")
	if err := checkLearnText(text); err != nil {
		return err
	}
	// The session is checked before the run, and the store is closed while the check runs.
	if session != "" {
		if err := withLearning(ctx, home, func(s *store.Store) error { return checkSession(ctx, s, repo, session) }); err != nil {
			return err
		}
	}

	res, err := runCheck(ctx, argv, timeout, os.Stdout, os.Stderr)
	if err != nil {
		return err
	}
	o := store.Observation{Kind: store.KindOutcome, Text: text, Repository: repo, SessionID: session, Origin: store.OriginRun, Status: res.status, ExitCode: res.exit, Duration: res.duration, CreatedAt: time.Now()}
	var id int64
	// An interrupted check is still recorded as incomplete, so the write must outlive the cancelled context.
	record := context.WithoutCancel(ctx)
	if err := withLearning(record, home, func(s *store.Store) error {
		id, err = s.Record(record, o)
		return err
	}); err != nil {
		return err
	}
	fmt.Fprintf(w, "codeforge: recorded outcome #%d: %s in %s (evidence only; nothing is applied)\n", id, describeStatus(o), res.duration.Round(time.Millisecond))
	if res.status != store.StatusPassed {
		return fmt.Errorf("%w: %s", errCheckFailed, describeStatus(o))
	}
	return nil
}

func withLearning(ctx context.Context, home string, f func(*store.Store) error) error {
	s, err := openLearning(ctx, home)
	if err != nil {
		return err
	}
	return errors.Join(f(s), s.Close())
}

type checkResult struct {
	status   string
	exit     *int
	duration time.Duration
}

// runCheck runs argv without a shell in its own process group. A timeout or interrupt kills the whole group
// while the leader still runs, so the pgid cannot have been recycled; that run counts as incomplete. Output
// goes to the terminal only and is never stored.
func runCheck(ctx context.Context, argv []string, timeout time.Duration, stdout, stderr io.Writer) (checkResult, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killGroup(cmd.Process.Pid) }
	cmd.WaitDelay = outcomeKillWait
	start := time.Now()
	if err := cmd.Start(); err != nil {
		return checkResult{}, fmt.Errorf("starting check: %w", err)
	}
	err := cmd.Wait()
	res := checkResult{status: store.StatusIncomplete, duration: time.Since(start)}

	var exit *exec.ExitError
	switch {
	case ctx.Err() != nil:
	case err == nil:
		code := 0
		res.status, res.exit = store.StatusPassed, &code
	case errors.As(err, &exit) && exit.ExitCode() > 0:
		code := exit.ExitCode()
		res.status, res.exit = store.StatusFailed, &code
	}
	return res, nil
}

func killGroup(pid int) error {
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("stopping check: %w", err)
	}
	return nil
}

func describeStatus(o store.Observation) string {
	if o.ExitCode == nil {
		return o.Status
	}
	return fmt.Sprintf("%s (exit %d)", o.Status, *o.ExitCode)
}

func learnList(ctx context.Context, home, repo string, all bool, w io.Writer) error {
	var obs []store.Observation
	if err := withLearning(ctx, home, func(s *store.Store) (err error) {
		obs, err = s.Observations(ctx, repo, all)
		return err
	}); err != nil {
		return err
	}
	if len(obs) == 0 {
		fmt.Fprintln(w, "No observations for", repo)
		return nil
	}
	for _, o := range obs {
		line := fmt.Sprintf("#%d %s %s origin=%s", o.ID, o.CreatedAt.UTC().Format(time.RFC3339), o.Kind, o.Origin)
		if o.SessionID != "" {
			line += " session=" + o.SessionID
		}
		if o.Kind == store.KindOutcome {
			line += " " + strings.ReplaceAll(describeStatus(o), " ", "") + " " + o.Duration.Round(time.Millisecond).String()
		}
		if o.RevertedAt != nil {
			line += " reverted"
		}
		// Quoted so stored text can never write terminal control sequences.
		fmt.Fprintf(w, "%s %s\n", line, strconv.Quote(o.Text))
	}
	return nil
}

type proposal struct {
	Text        string
	Corrections []int64
	Sessions    []string
	Passed      int
	Failed      int
	Incomplete  int
}

// propose groups identical corrections, ignoring case and spacing. Outcomes from the same sessions are
// reported as evidence only; a passing check does not show a preference is right.
func propose(obs []store.Observation) []proposal {
	groups := map[string]*proposal{}
	var order []string
	for _, o := range obs {
		if o.Kind != store.KindCorrection || o.RevertedAt != nil {
			continue
		}
		key := strings.Join(strings.Fields(strings.ToLower(o.Text)), " ")
		p, ok := groups[key]
		if !ok {
			p = &proposal{Text: o.Text}
			groups[key] = p
			order = append(order, key)
		}
		p.Corrections = append(p.Corrections, o.ID)
		if o.SessionID != "" && !slices.Contains(p.Sessions, o.SessionID) {
			p.Sessions = append(p.Sessions, o.SessionID)
		}
	}

	var out []proposal
	for _, key := range order {
		p := groups[key]
		if len(p.Corrections) < minCorrections {
			continue
		}
		for _, o := range obs {
			if o.Kind != store.KindOutcome || o.RevertedAt != nil || !slices.Contains(p.Sessions, o.SessionID) {
				continue
			}
			switch o.Status {
			case store.StatusPassed:
				p.Passed++
			case store.StatusFailed:
				p.Failed++
			default:
				p.Incomplete++
			}
		}
		slices.Sort(p.Sessions)
		out = append(out, *p)
	}
	slices.SortStableFunc(out, func(a, b proposal) int {
		return cmp.Or(cmp.Compare(len(b.Corrections), len(a.Corrections)), cmp.Compare(a.Corrections[0], b.Corrections[0]))
	})
	return out
}

func learnPropose(ctx context.Context, home, repo string, w io.Writer) error {
	var obs []store.Observation
	if err := withLearning(ctx, home, func(s *store.Store) (err error) {
		obs, err = s.Observations(ctx, repo, false)
		return err
	}); err != nil {
		return err
	}
	proposals := propose(obs)
	fmt.Fprintln(w, "Candidates only. Nothing is applied: promotion is not available yet.")
	if len(proposals) == 0 {
		fmt.Fprintf(w, "No correction has been recorded %d or more times in %s.\n", minCorrections, repo)
		return nil
	}
	for i, p := range proposals {
		ids := make([]string, len(p.Corrections))
		for j, id := range p.Corrections {
			ids[j] = "#" + strconv.FormatInt(id, 10)
		}
		fmt.Fprintf(w, "%d. %s\n   corrections: %s\n", i+1, strconv.Quote(p.Text), strings.Join(ids, " "))
		if len(p.Sessions) > 0 {
			fmt.Fprintf(w, "   sessions: %s\n   checks in those sessions: %d passed, %d failed, %d incomplete\n", strings.Join(p.Sessions, " "), p.Passed, p.Failed, p.Incomplete)
		}
	}
	return nil
}

func learnRevert(ctx context.Context, home, repo, raw string, w io.Writer) error {
	id, err := strconv.ParseInt(strings.TrimPrefix(raw, "#"), 10, 64)
	if err != nil || id <= 0 {
		return errLearnUsage
	}
	if err := withLearning(ctx, home, func(s *store.Store) error { return s.Revert(ctx, repo, id, time.Now()) }); err != nil {
		return err
	}
	fmt.Fprintf(w, "codeforge: reverted #%d; it stays in `codeforge learn list --all`\n", id)
	return nil
}
