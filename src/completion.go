package src

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

var errCompletionShell = errors.New("usage: codeforge completion bash|zsh|fish")

// word is one completion candidate and what it does, shown by zsh and fish.
type word struct{ name, about string }

// completionTree is what codeforge completes: the commands with what they do, then for each command the words that
// may follow it, and the values a flag takes. It is the one source the three shell scripts are generated from.
var completionTree = []struct {
	cmd   word
	words []word
}{
	{word{"work", "Build and test a change, without ticket or PR steps"}, sessionFlags},
	{word{"chat", "Questions, explanations and small edits"}, sessionFlags},
	{word{"ticket", "Take a ticket to a verified change and a draft PR"}, append([]word{{"--ticket", "Ticket ID"}}, sessionFlags...)},
	{word{"review", "Read-only review of a PR, or of your local changes"}, append([]word{
		{"--comment", "Post the review on the PR after you pick how"},
		{"--comment=yes", "Post the review without asking"},
		{"--follow-up", "Follow up on your earlier review: replies and changes since"},
		{"--fresh", "Review the whole PR again, even after an earlier review"},
		{"--repo", "The PR's repository, owner/name"},
		{"accept", "Accept a finding of your posted review as it is"}, {"unaccept", "Make an accepted finding count again"},
		{"accepted", "List the findings you accepted on a PR"},
	}, sessionFlags...)},
	{word{"respond", "Answer the open review comments on your PR"}, []word{
		{"--watch", "Wait for new review comments, then start"}, {"--print", "Print the brief instead of starting"},
		{"--model", "Model for this run"}, {"--effort", "Reasoning effort"},
	}},
	{word{"gate", "Review your branch and run the changed packages' tests"}, []word{
		{"accept", "Accept a blocking finding you will not fix"}, {"unaccept", "Make an accepted finding block again"},
		{"accepted", "List the accepted findings"}, {"reset", "Allow more gate runs on this branch"},
	}},
	{word{"job", "Run a command once its preconditions hold"}, []word{
		{"add", "Schedule a command"}, {"list", "List the jobs"}, {"show", "Show a job"}, {"wait", "Show the countdown, then the result"},
		{"log", "Show a job's log"}, {"cancel", "Cancel a job"}, {"--at", "Earliest start time"}, {"--until", "A check that must pass first"},
		{"--while", "A check that must keep passing"}, {"--hold", "How long the checks must pass"}, {"--every", "How often to check"},
		{"--expire", "When to give up waiting"}, {"--name", "A name for the job"},
	}},
	{word{"init", "Set up Empryo, sign-in and checks; learn a persona"}, []word{
		{"--yes", "Accept the defaults"}, {"--repo", "Learn this repository's persona"},
		{"--persona", "Start from a described persona"}, {"--global", "Set the persona for every project"},
	}},
	{word{"doctor", "Check setup, sign-in, models, persona and trust"}, nil},
	{word{"persona", "Show your persona and where it came from"}, []word{{"describe", "Show your persona and where it came from"}}},
	{word{"sessions", "Pick a session of this directory and open it"}, []word{{"--all", "Every project's sessions"}, {"--list", "Print the sessions instead"}}},
	{word{"models", "Choose, check and apply models"}, []word{
		{"configure", "Choose models interactively"}, {"plan", "Show what will be used"},
		{"apply", "Write the models to Empryo, after you confirm"}, {"recommend", "Show recommendations"},
		{"set", "Set a role's model"}, {"--project", "This project"}, {"--global", "All projects"},
		{"--recommended", "Apply the recommended models"},
	}},
	{word{"provider", "Provider order and backups"}, []word{
		{"show", "Show the order and connected providers"}, {"configure", "Pick the order from a menu"},
		{"set", "Save a new order"}, {"clear", "Remove the order"}, {"--for", "A separate order for some models"},
		{"--no-rules", "Remove the per-model orders"}, {"--project", "This project"}, {"--global", "All projects"},
	}},
	{word{"trust", "Approve this repository's persona"}, []word{{"--models", "Approve its model configuration"}}},
	{word{"learn", "What codeforge learned from your sessions"}, []word{
		{"habits", "Checks that keep passing here"}, {"rules", "Accepted rules"}, {"proposals", "Corrections you repeat"},
		{"accept", "Accept a proposal"}, {"dismiss", "Dismiss a proposal"}, {"forget", "Stop using a rule"},
		{"list", "This repository's observations"}, {"propose", "Corrections recorded twice or more"},
		{"revert", "Hide observations"}, {"correct", "Record a correction"}, {"outcome", "Run and record a check"},
	}},
	{word{"repair", "Rebuild managed hooks, profiles and skills"}, []word{{"--legacy-hook", "Replace an old hook"}}},
	{word{"completion", "Print the shell completion script"}, []word{{"zsh", "zsh"}, {"bash", "bash"}, {"fish", "fish"}}},
	{word{"version", "Show the codeforge version"}, nil},
	{word{"help", "Show the commands"}, []word{{"--all", "Every command"}}},
}

// sessionFlags are the flags every mode that starts a session takes.
var sessionFlags = []word{
	{"--model", "Model for this run"}, {"--effort", "Reasoning effort"},
	{"--headless", "Run without the TUI"}, {"--resume", "Resume a session"},
}

// topFlags may come instead of a command.
var topFlags = []word{{"--resume", "Resume a session by ID"}, {"--cwd", "Run in another directory"}, {"--help", "Show the commands"}}

// flagValues are the values a flag takes, completed after it; a flag with nil takes a value that cannot be listed.
var flagValues = map[string][]string{
	"--persona": {"oss", "pragmatic", "regulated", "startup"},
	"--effort":  {"low", "medium", "high"},
	"--ticket":  nil, "--repo": nil, "--model": nil, "--resume": nil, "--session": nil, "-s": nil, "--for": nil,
}

// runCompletion prints the completion script for shell.
func runCompletion(w io.Writer, args []string) error {
	if len(args) != 1 {
		return errCompletionShell
	}
	scripts := map[string]func() string{"bash": bashCompletion, "zsh": zshCompletion, "fish": fishCompletion}
	script, ok := scripts[args[0]]
	if !ok {
		return fmt.Errorf("%w (got %q)", errCompletionShell, args[0])
	}
	if _, err := io.WriteString(w, script()); err != nil {
		return fmt.Errorf("writing the %s completion: %w", args[0], err)
	}
	return nil
}

func names(ws []word) string {
	out := make([]string, len(ws))
	for i, w := range ws {
		out[i] = w.name
	}
	return strings.Join(out, " ")
}

// valueFlags lists the flags that take a value, sorted, so the scripts come out the same every time.
func valueFlags() []string {
	out := make([]string, 0, len(flagValues))
	for f := range flagValues {
		out = append(out, f)
	}
	slices.Sort(out)
	return out
}

func bashCompletion() string {
	var b strings.Builder
	b.WriteString("# bash completion for codeforge. Load it with: source <(codeforge completion bash)\n")
	b.WriteString("_codeforge() {\n\tlocal cur prev words=\"\"\n\tcur=\"${COMP_WORDS[COMP_CWORD]}\"\n\tprev=\"${COMP_WORDS[COMP_CWORD-1]}\"\n")
	b.WriteString("\tcase \"$prev\" in\n")
	for _, f := range valueFlags() {
		if vals := flagValues[f]; vals != nil {
			fmt.Fprintf(&b, "\t%s) COMPREPLY=($(compgen -W %q -- \"$cur\")); return ;;\n", f, strings.Join(vals, " "))
		} else {
			fmt.Fprintf(&b, "\t%s) COMPREPLY=(); return ;;\n", f)
		}
	}
	b.WriteString("\t--cwd) COMPREPLY=($(compgen -d -- \"$cur\")); return ;;\n\tesac\n")
	var top []word
	for _, c := range completionTree {
		top = append(top, c.cmd)
	}
	fmt.Fprintf(&b, "\tif [[ $COMP_CWORD -eq 1 ]]; then\n\t\tCOMPREPLY=($(compgen -W %q -- \"$cur\"))\n\t\treturn\n\tfi\n", names(top)+" "+names(topFlags))
	b.WriteString("\tcase \"${COMP_WORDS[1]}\" in\n")
	for _, c := range completionTree {
		if len(c.words) > 0 {
			fmt.Fprintf(&b, "\t%s) words=%q ;;\n", c.cmd.name, names(c.words))
		}
	}
	b.WriteString("\tesac\n\tCOMPREPLY=($(compgen -W \"$words\" -- \"$cur\"))\n}\ncomplete -F _codeforge codeforge\n")
	return b.String()
}

// zshQuote quotes a "name:description" item for a zsh array; a colon in the name is escaped as _describe wants.
func zshQuote(w word) string {
	item := strings.ReplaceAll(w.name, ":", `\:`) + ":" + w.about
	return "'" + strings.ReplaceAll(item, "'", `'\''`) + "'"
}

func zshCompletion() string {
	var b strings.Builder
	b.WriteString("#compdef codeforge\n# zsh completion for codeforge. Load it with: source <(codeforge completion zsh)\n")
	b.WriteString("_codeforge() {\n\tlocal -a items\n")
	b.WriteString("\tcase \"${words[CURRENT-1]}\" in\n")
	for _, f := range valueFlags() {
		if vals := flagValues[f]; vals != nil {
			fmt.Fprintf(&b, "\t%s) compadd -- %s; return ;;\n", f, strings.Join(vals, " "))
		} else {
			fmt.Fprintf(&b, "\t%s) _message 'value'; return ;;\n", f)
		}
	}
	b.WriteString("\t--cwd) _files -/; return ;;\n\tesac\n")
	b.WriteString("\tif (( CURRENT == 2 )); then\n\t\titems=(")
	for _, c := range completionTree {
		b.WriteString(" " + zshQuote(c.cmd))
	}
	for _, f := range topFlags {
		b.WriteString(" " + zshQuote(f))
	}
	b.WriteString(" )\n\t\t_describe -t commands 'codeforge command' items\n\t\treturn\n\tfi\n")
	b.WriteString("\tcase \"${words[2]}\" in\n")
	for _, c := range completionTree {
		if len(c.words) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\t%s) items=(", c.cmd.name)
		for _, w := range c.words {
			b.WriteString(" " + zshQuote(w))
		}
		b.WriteString(" ) ;;\n")
	}
	b.WriteString("\t*) return ;;\n\tesac\n\t_describe -t arguments 'argument' items\n}\n")
	// Loaded from fpath, the file is the function; sourced, it registers itself.
	b.WriteString("if [[ \"${funcstack[1]}\" == \"_codeforge\" ]]; then\n\t_codeforge \"$@\"\nelse\n\tcompdef _codeforge codeforge\nfi\n")
	return b.String()
}

// fishQuote quotes s for fish.
func fishQuote(s string) string {
	return "'" + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), "'", `\'`) + "'"
}

func fishCompletion() string {
	var b strings.Builder
	b.WriteString("# fish completion for codeforge. Load it with: codeforge completion fish | source\ncomplete -c codeforge -f\n")
	for _, c := range completionTree {
		fmt.Fprintf(&b, "complete -c codeforge -n '__fish_use_subcommand' -a %s -d %s\n", fishQuote(c.cmd.name), fishQuote(c.cmd.about))
	}
	for _, f := range topFlags {
		fmt.Fprintf(&b, "complete -c codeforge -n '__fish_use_subcommand' -l %s -d %s\n", strings.TrimPrefix(f.name, "--"), fishQuote(f.about))
	}
	for _, c := range completionTree {
		for _, w := range c.words {
			cond := fishQuote("__fish_seen_subcommand_from " + c.cmd.name)
			if long, ok := strings.CutPrefix(w.name, "--"); ok && !strings.Contains(long, "=") {
				req := ""
				if _, takes := flagValues[w.name]; takes {
					req = " -r"
					if vals := flagValues[w.name]; vals != nil {
						req += " -a " + fishQuote(strings.Join(vals, " "))
					}
				}
				fmt.Fprintf(&b, "complete -c codeforge -n %s -l %s%s -d %s\n", cond, long, req, fishQuote(w.about))
				continue
			}
			fmt.Fprintf(&b, "complete -c codeforge -n %s -a %s -d %s\n", cond, fishQuote(w.name), fishQuote(w.about))
		}
	}
	return b.String()
}
