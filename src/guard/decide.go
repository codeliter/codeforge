package guard

import (
	"strings"
)

type Decision struct {
	Allow  bool
	Reason string
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
		case RuleSelf:
			reasons = append(reasons, f.Reason+". Only the user can do this, outside the agent.")
		default:
			reasons = append(reasons, f.Reason+" ("+f.Rule+").")
		}
	}
	return Decision{Reason: "codeforge blocked this: " + strings.Join(reasons, " ")}
}
