package src

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/codeliter/codeforge/src/review"
)

// Ticket mode shows its plan before it writes any code: how the agent reads the ticket, the files it expects to
// change, and what it would ask. The user goes ahead, corrects it, or stops. A misread ticket is caught here, before
// the work is paid for, and the user starts out knowing what will be done.

// flagNoPlan starts ticket work without showing the plan first.
const flagNoPlan = "--no-plan"

const planLimit = 5 * time.Minute

// maxPlanAnswer bounds the correction typed at the plan, which goes into the task.
const maxPlanAnswer = 2000

var (
	errPlanStopped = errors.New("stopped at the plan; nothing was changed")
	errNoPlan      = errors.New("the agent sent none")
)

// ticketPlanPrompt asks for the plan of task and nothing else.
func ticketPlanPrompt(task string) string {
	return "Plan this task and stop: change nothing, and write no code. Read what you need to plan it well. Reply with " +
		"exactly these parts, each under its heading, in plain words. `Understanding:` what is asked, in one or two " +
		"sentences. `Files:` the files you expect to change, one a line as `- path — why`. `Approach:` at most five " +
		"steps. `Risks:` what could go wrong, or none. `Questions:` what you would ask me, only where two readings of " +
		"the task lead to different code, or none.\n\nThe task:\n" + task
}

// planAnswer reads the line the user answers the plan with, a byte at a time so nothing typed after it is taken from
// the terminal. ok is false when the input ends before the line does: that is no answer.
func planAnswer(in io.Reader) (answer string, ok bool) {
	var line []byte
	one := make([]byte, 1)
	for {
		n, err := in.Read(one)
		if n == 1 {
			if one[0] == '\n' {
				return strings.TrimSpace(string(line)), true
			}
			if len(line) < maxPlanAnswer {
				line = append(line, one[0])
			}
		}
		if err != nil {
			return "", false
		}
	}
}

// planRunner runs the planning turn; tests replace it.
var planRunner = runFork

// ticketPlan has the agent plan the task in args, read-only, shows the plan on w and reads the user's answer from in:
// Enter goes ahead, any other text goes ahead with that as a correction, q stops. It returns args with the agreed plan,
// and the correction, added to the task. A plan that cannot be had is said and never holds the work up.
func ticketPlan(ctx context.Context, bin string, args []string, in io.Reader, w io.Writer) ([]string, error) {
	task := promptOf(args)
	if task == "" {
		return args, nil
	}
	fmt.Fprintf(w, "codeforge: planning before any code is written (%s skips this)...\n", flagNoPlan)
	pctx, cancel := context.WithTimeout(ctx, planLimit)
	defer cancel()
	argv := []string{bin, "--profile", profileFor(modeTicket), "--mode", "plan", "--headless", "--json"}
	for _, f := range []string{"--model", "--effort"} {
		if v := flagValue(args, f); v != "" {
			argv = append(argv, f, v)
		}
	}
	plan := ""
	var out []byte
	// Planning only reads: the guard holds it to that, and so does the operating system.
	argv, err := sandboxRepo(pctx, append(argv, ticketPlanPrompt(task)))
	if err == nil {
		out, err = planRunner(pctx, argv, readOnlyEnv())
	}
	if err == nil {
		plan, err = review.EngineOutput(out)
	}
	if plan = strings.TrimSpace(plan); err != nil || plan == "" {
		if err == nil {
			err = errNoPlan
		}
		fmt.Fprintf(w, "codeforge: no plan (%v); going ahead without one.\n", err)
		return args, nil
	}
	fmt.Fprintf(w, "\n%s\n\nEnter to go ahead, a correction and Enter to go ahead with it, or q to stop: ", plan)
	answer, ok := planAnswer(in)
	if !ok || strings.EqualFold(answer, "q") {
		// Input that ends without an answer (ctrl+d, a closed terminal) agrees to nothing.
		return nil, errPlanStopped
	}
	task += "\n\nThe plan agreed with the user before work began. Follow it:\n" + plan
	if answer != "" {
		task += "\n\nThe user's correction to that plan, which wins wherever the two differ:\n" + answer
	}
	return append(withoutPrompt(args), task), nil
}
