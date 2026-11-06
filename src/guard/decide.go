package guard

import (
	"strings"
)

type Decision struct {
	Allow  bool
	Reason string
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
		if f.Rule == RuleLearning || f.Rule == RuleOpaque {
			kept = append(kept, f.Reason+". Only the user can do this, outside the agent.")
		}
	}
	if len(kept) == 0 {
		return Decision{Allow: true}
	}
	return Decision{Reason: "codeforge blocked this: " + strings.Join(kept, " ")}
}

// Decide applies the rules to one tool call. Nothing here is approvable: review mode stays read-only and
// codeforge's own settings stay with the user. Opening a PR, merging and commenting need no approval.
func Decide(in Input, readOnly bool) Decision {
	res := CheckTool(in, readOnly)
	if len(res.Blocked) == 0 {
		return Decision{Allow: true}
	}

	reasons := make([]string, 0, len(res.Blocked))
	for _, f := range res.Blocked {
		switch f.Rule {
		case RuleReadOnly:
			reasons = append(reasons, f.Reason+": review mode is read-only. Switch to ticket or chat mode to change files.")
		case RuleSelf, RuleLearning:
			reasons = append(reasons, f.Reason+". Only the user can do this, outside the agent.")
		default:
			reasons = append(reasons, f.Reason+" ("+f.Rule+").")
		}
	}
	return Decision{Reason: "codeforge blocked this: " + strings.Join(reasons, " ")}
}
