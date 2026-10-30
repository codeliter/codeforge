package src

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/charmbracelet/huh"
)

const empryoInstaller = "https://empryo.com/install.sh"

var (
	errSetupIncomplete = errors.New("setup is not complete; fix the items marked ✗ and run `codeforge init` again")
	errNoConsent       = errors.New("installing Empryo needs your go-ahead; run `codeforge init --yes` or answer yes")
	providerLine       = regexp.MustCompile(`^(\S+)\s+Ready\b`)
	gitVersion         = regexp.MustCompile(`git version (\d+)\.(\d+)`)
)

// Subscriptions Empryo can sign in to with a browser; API-key providers use `empryo --set-key`.
var loginProviders = []string{"copilot", "claude", "codex", "grok", "google-gemini-cli", "kimi-code", "cursor", "openrouter"}

type check struct {
	name string
	ok   bool
	note string
}

type setupOptions struct {
	yes bool
	out io.Writer
}

// runSetup gets a new machine ready: Empryo installed and signed in, git, gh, the sandbox, models, and
// finally the persona. Each step says what it found; nothing is installed without consent.
func runSetup(ctx context.Context, home string, args []string, out io.Writer) error {
	opts := setupOptions{yes: slices.Contains(args, "--yes") || slices.Contains(args, "-y"), out: out}
	empryoDir := filepath.Join(home, ".empryo")

	bin, err := findEmpryo(empryoDir)
	if err != nil {
		if bin, err = installEmpryo(ctx, empryoDir, opts); err != nil {
			return err
		}
	}

	if err := ensureSignedIn(ctx, bin, opts); err != nil {
		return err
	}

	checks := []check{
		{name: "Empryo", ok: true, note: empryoVersion(ctx, bin)},
		checkGit(ctx),
		checkGH(ctx, empryoDir),
		checkSandbox(),
	}
	printChecks(out, checks)
	if slices.ContainsFunc(checks, func(c check) bool { return !c.ok }) {
		return errSetupIncomplete
	}

	// Only roles the user has not set are filled, so re-running never overwrites a choice.
	if cfg, err := readConfigFile(home); err == nil && slices.ContainsFunc(roles, func(r role) bool { return len(current(cfg, r.key)) == 0 }) {
		fmt.Fprintln(out, "\nChoosing models for roles you have not set:")
		// A model choice that cannot be filled must not stop init; the user can set it with `codeforge models set`.
		if err := runModels(ctx, home, bin, []string{"fill"}); err != nil {
			fmt.Fprintf(out, "codeforge: could not fill every model role (%v); set them with `codeforge models set`\n", err)
		}
	}
	return nil
}

func installEmpryo(ctx context.Context, empryoDir string, opts setupOptions) (string, error) {
	if runtime.GOOS == "windows" {
		return "", fmt.Errorf("install Empryo from https://empryo.com/docs/installation, then run `codeforge init` again: %w", errSetupIncomplete)
	}

	fmt.Fprintf(opts.out, "Empryo is not installed. The official installer (%s) downloads it and checks its SHA-256.\n", empryoInstaller)
	if !opts.yes && !confirm("Install Empryo now?") {
		return "", errNoConsent
	}

	cmd := exec.CommandContext(ctx, "bash", "-c", "set -o pipefail; curl -fsSL "+empryoInstaller+" | bash")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, opts.out, os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("installing Empryo: %w", err)
	}

	bin, err := findEmpryo(empryoDir)
	if err != nil {
		return "", fmt.Errorf("empryo installed but not found; open a new terminal and run `codeforge init` again: %w", err)
	}
	return bin, nil
}

// ensureSignedIn makes sure at least one model provider is ready, offering a subscription sign-in.
func ensureSignedIn(ctx context.Context, bin string, opts setupOptions) error {
	if ready := readyProviders(ctx, bin); len(ready) > 0 {
		fmt.Fprintf(opts.out, "Model providers ready: %s\n", strings.Join(ready, ", "))
		return nil
	}

	if !interactive() {
		return fmt.Errorf("no model provider is signed in; run `empryo --login copilot` (or claude, codex, …) or `empryo --set-key <provider> <key>`: %w", errSetupIncomplete)
	}

	provider := loginProviders[0]
	form := huh.NewSelect[string]().
		Title("No model provider is signed in. Which subscription do you use?").
		Options(huh.NewOptions(loginProviders...)...).
		Value(&provider)
	if err := form.Run(); err != nil {
		return fmt.Errorf("provider picker: %w", err)
	}

	cmd := exec.CommandContext(ctx, bin, "--login", provider)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, opts.out, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("signing in to %s: %w", provider, err)
	}

	if len(readyProviders(ctx, bin)) == 0 {
		return fmt.Errorf("sign-in to %s did not finish: %w", provider, errSetupIncomplete)
	}
	return nil
}

func readyProviders(ctx context.Context, bin string) []string {
	out, err := exec.CommandContext(ctx, bin, "--list-providers").Output()
	if err != nil {
		return nil
	}
	return parseReady(string(out))
}

func parseReady(list string) []string {
	var ready []string
	for line := range strings.SplitSeq(list, "\n") {
		if m := providerLine.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			ready = append(ready, m[1])
		}
	}
	return ready
}

func empryoVersion(ctx context.Context, bin string) string {
	out, err := exec.CommandContext(ctx, bin, "--version").Output()
	if err != nil {
		return bin
	}
	return strings.TrimSpace(string(out))
}

// checkGit needs a git that supports hook config, which the pre-push enforcement uses.
func checkGit(ctx context.Context) check {
	out, err := exec.CommandContext(ctx, "git", "version").Output()
	if err != nil {
		return check{name: "git", note: "not installed"}
	}

	m := gitVersion.FindStringSubmatch(string(out))
	if m == nil {
		return check{name: "git", note: "unknown version"}
	}
	major, errMajor := strconv.Atoi(m[1])
	minor, errMinor := strconv.Atoi(m[2])
	const needMajor, needMinor = 2, 54
	if errMajor != nil || errMinor != nil || major < needMajor || major == needMajor && minor < needMinor {
		return check{name: "git", note: fmt.Sprintf("%s.%s found; 2.54 or newer is needed for hook config", m[1], m[2])}
	}
	return check{name: "git", ok: true, note: m[1] + "." + m[2]}
}

func checkGH(ctx context.Context, empryoDir string) check {
	gh, err := realGH(empryoDir)
	if err != nil {
		return check{name: "gh", note: "not installed; get it from https://cli.github.com"}
	}
	if err := exec.CommandContext(ctx, gh, "auth", "status").Run(); err != nil {
		return check{name: "gh", note: "not signed in; run `gh auth login`"}
	}
	return check{name: "gh", ok: true, note: "signed in"}
}

func checkSandbox() check {
	if runtime.GOOS != "darwin" {
		return check{name: "sandbox", ok: true, note: "not available on " + runtime.GOOS + "; review mode will not start"}
	}
	if _, err := os.Stat(sandboxExec); err != nil {
		return check{name: "sandbox", note: sandboxExec + " missing"}
	}
	return check{name: "sandbox", ok: true, note: "macOS sandbox-exec"}
}

func printChecks(w io.Writer, checks []check) {
	fmt.Fprintln(w)
	for _, c := range checks {
		mark := "✓"
		if !c.ok {
			mark = "✗"
		}
		fmt.Fprintf(w, "  %s %-8s %s\n", mark, c.name, c.note)
	}
}

func confirm(question string) bool {
	if !interactive() {
		return false
	}

	ok := false
	if err := huh.NewConfirm().Title(question).Value(&ok).Run(); err != nil {
		return false
	}
	return ok
}
