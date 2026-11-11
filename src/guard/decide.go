package guard

import (
	"slices"
	"strings"
)

type Decision struct {
	Allow bool
	// Ask hands the call to the user, who sees it and approves or refuses it in their own session. Only a PR comment
	// with its text on the command line is offered this way; every other write in review mode stays refused.
	Ask    bool
	Reason string
}

// onlyComments reports whether every finding is a PR comment, so anything else in the same command (a second command,
// a file write) still refuses the whole call.
func onlyComments(found []Finding) bool {
	return len(found) > 0 && !slices.ContainsFunc(found, func(f Finding) bool { return f.Rule != RuleComment })
}

// DecideOutsideReview is the only check outside review mode: git, gh, scripts and settings are not gated, but the
// agent may not write learning evidence (codeforge observe, codeforge learn), because that would record its own
// words as the user's corrections. Commands the guard cannot read (process substitution, git
// aliases and hooks that run programs, code_script it cannot parse) are refused too, since they could run those
// commands unseen. Script files that cannot be read are not.
func DecideOutsideReview(in Input) Decision {
	// Protected-path hits return early in CheckTool, so classify the command on its own as well; otherwise a
	// protected tool_paths entry would hide a codeforge observe call from the learning check.
	bare := in
	bare.Paths = nil
	var kept []string
	for _, f := range CheckTool(bare, false).Blocked {
		if f.Rule == RuleLearning || f.Rule == RuleOpaque || f.Rule == RuleUserOnly {
			kept = append(kept, f.Reason+". Only the user can do this, outside the agent.")
		}
	}
	if len(kept) == 0 {
		return Decision{Allow: true}
	}
	return Decision{Reason: "codeforge blocked this: " + strings.Join(kept, " ")}
}

// Decide applies the rules to one tool call. Review mode stays read-only and codeforge's own settings stay with the
// user; the one thing offered for approval is a PR comment whose text is on the command line, so the user sees exactly
// what would be posted. Opening a PR and merging are refused in review mode.
func Decide(in Input, readOnly bool) Decision {
	res := CheckTool(in, readOnly)
	if len(res.Blocked) == 0 {
		return Decision{Allow: true}
	}
	// Only a shell call that is nothing but the comment: a second command in the same line, even a harmless-looking one,
	// would go up for approval together with it, and a script or wrapper hides what runs.
	if slices.Contains(shellTools, in.Tool) && res.commands == 1 && onlyComments(res.Blocked) {
		return Decision{Ask: true, Reason: "codeforge asks you first: review mode is read-only, and this " + res.Blocked[0].Reason + ". Approve it only if you want exactly this comment posted."}
	}

	reasons := make([]string, 0, len(res.Blocked))
	for _, f := range res.Blocked {
		switch f.Rule {
		case RuleReadOnly:
			reasons = append(reasons, f.Reason+": review mode is read-only. Switch to ticket or chat mode to change files.")
		case RuleSelf, RuleLearning, RuleUserOnly:
			reasons = append(reasons, f.Reason+". Only the user can do this, outside the agent.")
		default:
			reasons = append(reasons, f.Reason+" ("+f.Rule+").")
		}
	}
	return Decision{Reason: "codeforge blocked this: " + strings.Join(reasons, " ")}
}
