package review

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var ErrNoVerdict = errors.New("review: reviewer returned no verdict")

const (
	VerdictPass = "pass"
	VerdictFail = "fail"
)

type Finding struct {
	File string `json:"file"`
	Line int    `json:"line"`
	What string `json:"what"`
	// Invariant names the contract or documented behaviour the finding breaks. Optional: a concrete input with a wrong
	// result is Blocking whether or not any invariant names it.
	Invariant string `json:"invariant,omitempty"`
}

type Verdict struct {
	Verdict  string    `json:"verdict"`
	Summary  string    `json:"summary"`
	Blocking []Finding `json:"blocking"`
	Should   []Finding `json:"should"`
}

// VerdictInstructions asks a headless reviewer for a machine-readable verdict after its prose review.
const VerdictInstructions = `

You are a gate reviewer. Do not dispatch other agents; the gate already runs each reviewer independently.
Keep the prose short, but list EVERY blocking issue you find: never stop at the first, never cap the count, and report each distinct input or spelling that triggers a bug once it is known. Later rounds only confirm fixes, so what you leave out now is found one round late. End your answer with exactly one fenced json block and nothing after it:
` + "```json" + `
{"verdict":"pass|fail","summary":"<one sentence>","blocking":[{"file":"path","line":1,"what":"what breaks and the fix","invariant":"the documented behaviour or contract it breaks"}],"should":[{"file":"path","line":1,"what":"..."}]}
` + "```" + `
verdict is "fail" when and only when blocking is non-empty. Blocking means wrong behaviour, data loss, a security problem or an unsafe rollout. Style is never blocking.
A finding is Blocking, not a should, when the code produces a wrong result or crashes for some input, drops or reorders data, breaks a documented contract, or fails a test that ships with the change. "The tests would fail" or "the last line is lost" is Blocking even when no existing test covers that case. If you can state a concrete input that gets the wrong output, it is Blocking, and its what names that input.`

// stepBudget tells a reviewer how many tool steps it has. A run that reaches its limit without a verdict is a wasted
// review, so it must answer before then.
// answerSteps are kept back from the tool budget for the answer itself.
const answerSteps = 3

func stepBudget(steps int) string {
	calls := max(steps-answerSteps, 1)
	unit := "tool calls"
	if calls == 1 {
		unit = "tool call"
	}
	return fmt.Sprintf("\n\nYou have at most %d tool steps, and the answer itself is one of them: make at most %d %s, then write the review. "+
		"A review that runs out of steps returns nothing, whatever you found. The brief already holds the diff and the changed files in full, so read more only when a finding depends on it.", steps, calls, unit)
}

// ParseVerdict reads only the final json object of the answer. Quoted diff content earlier in the answer
// cannot spoof it, and a malformed final object is an error rather than a fall back to an older one.
// One known wrapper is accepted: some models return the whole answer as a json string, so the verdict arrives
// as an escaped fenced block at the very end of the answer.
func ParseVerdict(out string) (Verdict, error) {
	v, err := parseFinalObject(out)
	if err == nil {
		return v, nil
	}
	if inner, ok := escapedFencedBlock(out); ok {
		if v2, err2 := parseFinalObject(inner); err2 == nil {
			return v2, nil
		}
	}
	return Verdict{}, err
}

// escapedFencedBlock extracts the last ` + "```json" + ` block when the answer is itself a json string, and only when
// nothing but the wrapper's closing characters follows the block.
func escapedFencedBlock(out string) (string, bool) {
	const open, closing, maxTrailer = "```json", "```", 8
	start := strings.LastIndex(out, open)
	if start < 0 {
		return "", false
	}
	rest := out[start+len(open):]
	end := strings.LastIndex(rest, closing)
	if end < 0 || len(strings.TrimSpace(rest[end+len(closing):])) > maxTrailer {
		return "", false
	}
	block := strings.NewReplacer(`\"`, `"`, `\n`, "\n", `\t`, "\t", `\\`, `\`).Replace(rest[:end])
	return strings.TrimSpace(block), true
}

func parseFinalObject(out string) (Verdict, error) {
	body := strings.TrimSpace(out)
	body = strings.TrimSpace(strings.TrimSuffix(body, "```"))

	end := strings.LastIndex(body, "}")
	if end < 0 || end != len(body)-1 {
		return Verdict{}, ErrNoVerdict
	}

	start := matchingBrace(body, end)
	if start < 0 {
		return Verdict{}, ErrNoVerdict
	}

	var v Verdict
	if err := json.Unmarshal([]byte(body[start:end+1]), &v); err != nil {
		return Verdict{}, fmt.Errorf("%w: %w", ErrNoVerdict, err)
	}
	if v.Verdict != VerdictPass && v.Verdict != VerdictFail {
		return Verdict{}, fmt.Errorf("%w: unknown verdict %q", ErrNoVerdict, v.Verdict)
	}
	if len(v.Blocking) > 0 {
		v.Verdict = VerdictFail
	}
	return v, nil
}

// matchingBrace walks back from a closing brace to its opening one, skipping braces inside JSON strings.
func matchingBrace(s string, end int) int {
	depth := 0
	inString := false
	for i := end; i >= 0; i-- {
		c := s[i]
		if inString {
			if c == '"' && !escaped(s, i) {
				inString = false
			}
			continue
		}

		switch c {
		case '"':
			inString = true
		case '}':
			depth++
		case '{':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func escaped(s string, i int) bool {
	n := 0
	for j := i - 1; j >= 0 && s[j] == '\\'; j-- {
		n++
	}
	return n%2 == 1
}
