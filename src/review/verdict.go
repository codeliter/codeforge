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
Keep the prose short. End your answer with exactly one fenced json block and nothing after it:
` + "```json" + `
{"verdict":"pass|fail","summary":"<one sentence>","blocking":[{"file":"path","line":1,"what":"what breaks and the fix"}],"should":[{"file":"path","line":1,"what":"..."}]}
` + "```" + `
verdict is "fail" when and only when blocking is non-empty. Blocking means wrong behaviour, data loss, a security problem or an unsafe rollout. Style is never blocking.`

// ParseVerdict reads only the final json object of the answer. Quoted diff content earlier in the answer
// cannot spoof it, and a malformed final object is an error rather than a fall back to an older one.
func ParseVerdict(out string) (Verdict, error) {
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
