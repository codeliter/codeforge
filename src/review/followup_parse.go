package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// ThreadAnswer is a follow-up round's verdict on one of the user's threads.
type ThreadAnswer struct {
	Thread  string `json:"thread"`
	Verdict string `json:"verdict"`
	Reply   string `json:"reply"`
	Resolve bool   `json:"resolve"`
}

// resolvable verdicts close a thread: the point is settled.
var resolvable = map[string]bool{"fixed": true, "agreed": true, "accepted": true}

// followUpVerdicts are the verdicts a follow-up round may give a thread.
var followUpVerdicts = map[string]bool{
	"fixed": true, "not fixed": true, "agreed": true, "held": true, "answered": true, "accepted": true, "waiting": true,
}

// ErrUnanswered reports a round that left some of the user's threads without a verdict: those wait, and the round does
// not count as having judged them.
var ErrUnanswered = errors.New("some of your earlier threads got no verdict")

// trailingJSON finds a fenced ```json block that ends the output, its fences on lines of their own, and returns the
// text before it and the block's contents.
func trailingJSON(out string) (before, block string, ok bool) {
	lines := strings.Split(out, "\n")
	last := len(lines) - 1
	if last < 1 || strings.TrimSpace(lines[last]) != "```" {
		return "", "", false
	}
	for i := last - 1; i >= 0; i-- {
		switch {
		case isJSONFence(lines[i]):
			return strings.TrimSpace(strings.Join(lines[:i], "\n")), strings.Join(lines[i+1:last], "\n"), true
		case strings.TrimSpace(lines[i]) == "```":
			return "", "", false
		}
	}
	return "", "", false
}

// HeadMoved reports whether the pull request has a new head since this round read it: then its verdicts were reached
// on code that is no longer the PR's, and no thread may be resolved on them. A head that cannot be read counts as moved.
func (fu *FollowUp) HeadMoved(ctx context.Context, r Runner) bool {
	fu.mu.Lock()
	repo, number, head := fu.data.Repo, fu.data.Number, fu.data.Head
	fu.mu.Unlock()
	now, err := prHead(ctx, r, repo, number)
	return err != nil || now != head
}

// Me is the GitHub account the round was read as.
func (fu *FollowUp) Me() string {
	if fu == nil {
		return ""
	}
	fu.mu.Lock()
	defer fu.mu.Unlock()
	return fu.data.Me
}

// Since is the commit the user last reviewed, "" when it is not known.
func (fu *FollowUp) Since() string {
	if fu == nil {
		return ""
	}
	fu.mu.Lock()
	defer fu.mu.Unlock()
	return fu.data.Since
}

// afterDecision splits out at its first Decision line outside code: the review up to and including it, and what
// follows, which in a follow-up round is only ever the verdicts. found is false when there is no Decision line.
func afterDecision(out string) (review, tail string, found bool) {
	lines := strings.Split(out, "\n")
	code := inCode(lines)
	for i, l := range lines {
		if !code[i] && decisionLine.MatchString(l) {
			return strings.TrimSpace(strings.Join(lines[:i+1], "\n")), strings.TrimSpace(strings.Join(lines[i+1:], "\n")), true
		}
	}
	return out, "", false
}

// verdictsFence finds the first fenced block in out that holds verdicts, in the wrong place or cut off, and returns the
// text before it. Text after a block is never read as part of it. A block in the verdicts' own fence always counts; a
// closed ```json or bare block counts only when it holds verdicts on one of ids, the round's threads, since anything
// else in it, whatever its keys, is a finding's example; a block that never closes counts when it looks like verdicts
// at all (any key that reads threads), since nothing an answer quotes is cut off there.
func verdictsFence(out string, ids map[string]bool) (string, bool) {
	lines := strings.Split(out, "\n")
	for i := 0; i < len(lines); i++ {
		if fenceOpen(lines[i]) == "" {
			continue
		}
		end, closed := fenceBlock(lines, i)
		body := lines[i+1 : end]
		if closed {
			body = lines[i+1 : end-1] // without the closing fence, so a closed block can decode
		}
		tag := fenceTag(lines[i])
		text := strings.Join(body, "\n")
		var judged bool
		switch {
		case tag != "json" && tag != "":
		case closed:
			judged = namesThreads(text, ids)
		default:
			// A bare block only when its keys read threads, so quoted code or prose that merely mentions them is kept.
			judged = looksLikeVerdicts(text, tag == "json")
		}
		if tag == verdictsTag || judged {
			return strings.TrimSpace(strings.Join(lines[:i], "\n")), true
		}
		i = end - 1
	}
	return "", false
}

// isVerdicts reports whether block decodes to verdicts on ids, the round's threads, and nothing else: a threads list
// whose entries hold only the fields of a ThreadAnswer, each naming one of ids, with no key given twice. Anything else
// (another key at either level, a repeated key whose first value the decoder would drop, an entry for a thread that is
// not the round's) may carry review text that must not vanish with the block.
func isVerdicts(block string, ids map[string]bool) bool {
	_, ok := verdictsOf(block, ids)
	return ok
}

// verdictsOf decodes block once, as isVerdicts checks it, and returns its verdicts (never nil when ok).
func verdictsOf(block string, ids map[string]bool) ([]ThreadAnswer, bool) {
	// repeatsKey is the real guard: encoding/json matches field names in any case, and even look-alike letters, and
	// keeps the last of two that match, so DisallowUnknownFields alone lets a second copy of a key hide the first.
	if repeatsKey(block) {
		return nil, false
	}
	var doc struct {
		Threads *[]ThreadAnswer `json:"threads"`
	}
	dec := json.NewDecoder(strings.NewReader(block))
	dec.DisallowUnknownFields() // checked on every nested struct too
	if dec.Decode(&doc) != nil || doc.Threads == nil {
		return nil, false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) { // nothing after the object
		return nil, false
	}
	if slices.ContainsFunc(*doc.Threads, func(a ThreadAnswer) bool { return !ids[a.Thread] }) {
		return nil, false
	}
	return *doc.Threads, true
}

// verdictKeys are the only keys a verdicts block may use, spelled exactly so: encoding/json would match any case of
// them, and even look-alike letters, and keep only the last of two that match.
var verdictKeys = map[string]bool{"threads": true, "thread": true, "verdict": true, "reply": true, "resolve": true}

// repeatsKey reports whether any object in the json of block gives a key twice, or a key that is not one of
// verdictKeys as written, or whether block does not read as json at all.
func repeatsKey(block string) bool {
	dec := json.NewDecoder(strings.NewReader(block))
	var repeats func() bool
	repeats = func() bool {
		tok, err := dec.Token()
		if err != nil {
			return true
		}
		switch tok {
		case json.Delim('{'):
			seen := map[string]bool{}
			for dec.More() {
				k, err := dec.Token()
				key, ok := k.(string)
				if err != nil || !ok || !verdictKeys[key] || seen[key] {
					return true
				}
				seen[key] = true
				if repeats() {
					return true
				}
			}
		case json.Delim('['):
			for dec.More() {
				if repeats() {
					return true
				}
			}
		default:
			return false // a scalar
		}
		_, err = dec.Token() // the closing delimiter
		return err != nil
	}
	return repeats()
}

// fenceTag is the info string of a fence-opening line, in lower case: "json", "codeforge-verdicts", "" for a bare one.
func fenceTag(line string) string {
	return strings.ToLower(strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "`~")))
}

// taggedFence finds the first block in the verdicts' own fence and returns the text before it and after it, and
// whether it closes; every other block, closed or cut off, is passed over. A block that never closes has no text after
// it: then after is what follows its opening line, which may be review text the open fence swallowed.
func taggedFence(out string) (before, after string, closed, ok bool) {
	lines := strings.Split(out, "\n")
	for i := 0; i < len(lines); i++ {
		if fenceOpen(lines[i]) == "" {
			continue
		}
		end, closed := fenceBlock(lines, i)
		if fenceTag(lines[i]) == verdictsTag {
			rest := lines[end:]
			if !closed {
				rest = lines[i+1:]
			}
			return strings.TrimSpace(strings.Join(lines[:i], "\n")), strings.TrimSpace(strings.Join(rest, "\n")), closed, true
		}
		i = end - 1
	}
	return "", "", false, false
}

// threadIDs is the set of the round's thread ids, never nil.
func (fu *FollowUp) threadIDs() map[string]bool {
	ids := map[string]bool{}
	for _, t := range fu.Threads() {
		ids[t.ID] = true
	}
	return ids
}

// namesThreads reports whether block decodes to verdicts with a thread one of ids names: real verdicts, not an example
// that only shares their shape.
func namesThreads(block string, ids map[string]bool) bool {
	var doc struct {
		Threads []struct {
			Thread string `json:"thread"`
		} `json:"threads"`
	}
	if json.Unmarshal([]byte(block), &doc) != nil {
		return false
	}
	return slices.ContainsFunc(doc.Threads, func(t struct {
		Thread string `json:"thread"`
	}) bool {
		return ids[t.Thread]
	})
}

// verdictsInShape reports whether block is verdicts and nothing else, whatever threads they name (isVerdicts on the
// threads the block names itself), with no finding in a reply.
func verdictsInShape(block string) bool {
	var doc struct {
		Threads []struct {
			Thread string `json:"thread"`
			Reply  string `json:"reply"`
		} `json:"threads"`
	}
	if json.Unmarshal([]byte(block), &doc) != nil {
		return false
	}
	ids := map[string]bool{}
	for _, t := range doc.Threads {
		ids[t.Thread] = true
		// A reply that holds a finding is review text: cut with the block, it would vanish.
		for _, l := range strings.Split(t.Reply, "\n") {
			if _, ok := ParseLabeled(l); ok {
				return false
			}
		}
	}
	return isVerdicts(block, ids)
}

// looksLikeVerdicts reports whether a block's contents carry a threads key, in any case or escape the json decoder
// would accept. With loose, contents that do not decode (a block cut off) count when they say "threads" at all.
func looksLikeVerdicts(block string, loose bool) bool {
	var doc map[string]json.RawMessage
	if json.Unmarshal([]byte(block), &doc) == nil {
		for k := range doc {
			if strings.EqualFold(k, "threads") {
				return true
			}
		}
		return false
	}
	// Broken json is still verdicts when any key in it reads threads once unescaped; a value or a Go tag that merely
	// names threads is not. A block cut off mid-key is caught by its loose read.
	for _, m := range jsonKey.FindAllStringSubmatch(block, -1) {
		var key string
		if json.Unmarshal([]byte(`"`+m[1]+`"`), &key) == nil && strings.EqualFold(key, "threads") {
			return true
		}
	}
	if loose {
		unescaped := uEscape.ReplaceAllStringFunc(block, func(e string) string {
			n, err := strconv.ParseUint(e[2:], 16, 32)
			if err != nil {
				return e
			}
			return string(rune(n))
		})
		return strings.Contains(strings.ToLower(unescaped), "threads")
	}
	return false
}

// uEscape matches a json \uXXXX escape.
var uEscape = regexp.MustCompile(`\\u[0-9a-fA-F]{4}`)

// jsonKey matches a quoted json key: a string followed by a colon, never a string value.
var jsonKey = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"\s*:`)

// verdictsTag names the fence the verdicts go in: a tag no example in a review uses, so the block is told apart from
// any ```json a finding quotes. A ```json block in the verdicts' place is read too.
const verdictsTag = "codeforge-verdicts"

// isJSONFence reports whether a line opens the verdicts block: ```codeforge-verdicts, or ```json, in any case.
func isJSONFence(l string) bool {
	t := strings.TrimSpace(l)
	return strings.EqualFold(t, "```"+verdictsTag) || strings.EqualFold(t, "```json")
}

// ParseFollowUp splits a follow-up round's output into the review and its thread verdicts, one for each of the user's
// threads in order. Everything after the Decision line is the verdicts' and is cut from the review, whether or not it
// reads, so none of it is ever posted; with no Decision line, a ```json block that ends the output is. Answers for
// threads that are not the user's are dropped. A thread with no answer, or with a verdict that is not one of the
// round's, is "waiting" (no reply, left open) and ErrUnanswered says so. A thread is resolved only when its verdict
// settles it, and "accepted" resolves only a thread known not to block: a deferred blocker stays open.
func (fu *FollowUp) ParseFollowUp(output string) (string, []ThreadAnswer, error) {
	review, answers, err := fu.splitFollowUp(output)
	if len(fu.Threads()) == 0 {
		// No thread to judge: splitFollowUp has already settled what is the round's and what was shown.
		return review, answers, err
	}
	// Whatever path the parse took, verdicts left in the review mean the answer broke its shape: cut them there so they
	// are never shown or posted, and read no verdicts from a round that did. What counts as verdicts is verdictsFence's
	// to say: a ```json example a finding quotes stays, however it reads, unless it holds verdicts on these threads.
	if cut, ok := verdictsFence(review, fu.threadIDs()); ok {
		return cut, nil, fmt.Errorf("%w: a verdicts block is out of place", ErrNoAnswers)
	}
	return review, answers, err
}

// fenceLines counts the lines of s that open a ```json block.
func fenceLines(s string) int {
	n := 0
	for _, l := range strings.Split(s, "\n") {
		if isJSONFence(l) {
			n++
		}
	}
	return n
}

// splitFollowUp is ParseFollowUp before its last check.
func (fu *FollowUp) splitFollowUp(output string) (string, []ThreadAnswer, error) {
	out := strings.TrimSpace(output)
	noThreads := len(fu.Threads()) == 0
	review, tail, decided := afterDecision(out)
	block, found := "", false
	tailLines := strings.Split(tail, "\n")
	switch {
	case noThreads:
		// No thread to judge, so no verdicts are needed and none are read: before the Decision line (or in the whole
		// answer without one) only a block in the verdicts' own fence is the round's. Every other block, however it
		// reads, is a finding's example. When the answer broke its shape, the verdicts are dropped and nothing else, so
		// no finding is lost, and the round is not posted.
		if !decided {
			review = out
		}
		cut, after, closed, ok := taggedFence(review)
		switch {
		case ok && closed && after == "":
			// The verdicts' fence ends the answer: it is cut only when it holds verdicts and nothing else, so a finding
			// written inside it is shown as written, and the round not posted.
			if before, last, whole := trailingJSON(review); !whole || before != cut || !isVerdicts(last, fu.threadIDs()) {
				return review, nil, fmt.Errorf("%w: the verdicts block does not read", ErrNoAnswers)
			}
			review = cut
		case ok:
			return strings.TrimSpace(cut + "\n\n" + after), nil, fmt.Errorf("%w: a verdicts block is out of place", ErrNoAnswers)
		}
		// The Decision line closes the review: after it comes the verdicts block or nothing.
		// A round with no thread to judge needs no verdicts, but a model may still write a block of them on threads it
		// invented: one that is verdicts in shape and nothing else is cut, and the round stands.
		if before, last, whole := trailingJSON(tail); tail != "" && (!whole || before != "" || fenceLines(tail) != 1 || !verdictsInShape(last)) {
			// Shown as written, since a finding may sit inside a block there; it is never posted.
			return strings.TrimSpace(review + "\n\n" + tail), nil, fmt.Errorf("%w: the review goes on after its Decision line", ErrNoAnswers)
		}
		return review, []ThreadAnswer{}, nil
	case decided:
		_, block, found = trailingJSON(tail)
		if !found || !isJSONFence(tailLines[0]) || fenceLines(tail) != 1 {
			return review, nil, fmt.Errorf("%w: what follows the Decision line is not the verdicts block", ErrNoAnswers)
		}
	default:
		review, block, found = trailingJSON(out)
		if !found {
			if cut, ok := verdictsFence(out, fu.threadIDs()); ok {
				// The verdicts, cut off or not where they belong: cut, so they are never posted.
				return cut, nil, fmt.Errorf("%w: the verdicts block is cut off", ErrNoAnswers)
			}
			return out, nil, ErrNoAnswers
		}
		// With no Decision line to mark where the verdicts start, the block that ends the answer is theirs only in
		// their own fence, or when it holds verdicts on the round's threads: else it is a finding's example, and the
		// round, which gave no verdicts, is not posted.
		opener, _, _ := strings.Cut(strings.TrimSpace(out[len(review):]), "\n")
		opener = strings.TrimSpace(opener)
		if !strings.EqualFold(opener, "```"+verdictsTag) && !namesThreads(block, fu.threadIDs()) {
			return out, nil, fmt.Errorf("%w: the answer ends with a finding's example, not the verdicts", ErrNoAnswers)
		}
		if cut, ok := verdictsFence(review, fu.threadIDs()); ok { // an earlier ```json block is a finding's example
			return cut, nil, fmt.Errorf("%w: more than one verdicts block", ErrNoAnswers)
		}
	}
	// The block holds verdicts on the round's threads and nothing else (verdictsOf): anything more may carry review
	// text, which must not vanish with it.
	verdicts, ok := verdictsOf(block, fu.threadIDs())
	if !ok {
		if decided || looksLikeVerdicts(block, true) { // the block that ends the answer, where the verdicts go
			return review, nil, fmt.Errorf("%w: the verdicts do not read", ErrNoAnswers)
		}
		return out, nil, ErrNoAnswers
	}
	given := map[string]ThreadAnswer{}
	for _, a := range verdicts {
		if _, dup := given[a.Thread]; dup {
			// Two verdicts for one thread cannot both stand, and picking one could approve past the other.
			return review, nil, fmt.Errorf("%w: thread %s has two verdicts", ErrNoAnswers, a.Thread)
		}
		given[a.Thread] = a
	}
	threads := fu.Threads()
	answers := make([]ThreadAnswer, 0, len(threads))
	var missing error
	for _, t := range threads {
		a, ok := given[t.ID]
		a.Thread = t.ID
		a.Verdict = strings.ToLower(strings.TrimSpace(a.Verdict))
		a.Reply = strings.TrimSpace(a.Reply)
		if !ok || !followUpVerdicts[a.Verdict] {
			a, missing = ThreadAnswer{Thread: t.ID, Verdict: "waiting"}, ErrUnanswered
		}
		if a.Verdict == "waiting" {
			a.Reply = ""
		}
		// The verdict decides, not the model's flag: a settled point is resolved, and "accepted" settles only a thread
		// known not to block.
		a.Resolve = resolvable[a.Verdict] && (a.Verdict != "accepted" || (t.Known && !t.Blocking))
		answers = append(answers, a)
	}
	return review, answers, missing
}
