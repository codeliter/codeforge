package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// AnswersNote asks the first pass of a respond session to end with its verdicts and draft replies in a form codeforge
// can read, so it can show each thread with its code and post the replies when the user presses p.
const AnswersNote = "When your first pass is done (verdicts made, valid fixes committed, checks run), post nothing and push nothing: " +
	"codeforge shows the user every thread with its code, your verdict and your draft reply, and pushes and posts only when they say so. " +
	"Write a short summary of what you changed, then end your answer with exactly one fenced json block and nothing after it:\n" +
	"```json\n{\"threads\":[{\"thread\":\"<thread id from the feedback>\",\"verdict\":\"valid|partly valid|not valid|question|out of scope|outdated\"," +
	"\"evidence\":\"<one line: the caller, test or spec that decides it>\",\"commit\":\"<short sha of the fix, or empty>\",\"reply\":\"<the reply to post, in the user's voice>\"}]}\n```\n" +
	"Give one entry per thread in the feedback. Put code in backticks exactly as written. Review summaries need no entry.\n"

// Answer is the first pass's verdict and draft reply for one review thread.
type Answer struct {
	Thread   string `json:"thread"`
	Verdict  string `json:"verdict"`
	Evidence string `json:"evidence"`
	Commit   string `json:"commit"`
	Reply    string `json:"reply"`
}

var (
	answersBlock = regexp.MustCompile("(?s)```json\\s*\\n(\\{.*?\\})\\s*```\\s*$")
	shortSHA     = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
	// ErrNoAnswers is returned when the first pass did not end with its answers block.
	ErrNoAnswers = errors.New("respond: the answer has no verdicts block")
)

// ParseAnswers splits a first pass's output into its prose and its answers. Answers for threads that are not in the
// feedback are dropped, so a reply can only ever go to a thread the reviewers opened; a commit that is not a short
// hex sha is dropped too.
func ParseAnswers(output string, fb Feedback) (string, []Answer, error) {
	out := strings.TrimSpace(output)
	m := answersBlock.FindStringSubmatchIndex(out)
	if m == nil {
		return out, nil, ErrNoAnswers
	}
	var doc struct {
		Threads []Answer `json:"threads"`
	}
	if err := json.Unmarshal([]byte(out[m[2]:m[3]]), &doc); err != nil {
		return out, nil, fmt.Errorf("%w: %w", ErrNoAnswers, err)
	}
	known := map[string]bool{}
	for _, t := range fb.Threads {
		known[t.ID] = true
	}
	var answers []Answer
	seen := map[string]bool{}
	for _, a := range doc.Threads {
		if !known[a.Thread] || seen[a.Thread] {
			continue
		}
		seen[a.Thread] = true
		a.Verdict = strings.ToLower(strings.TrimSpace(a.Verdict))
		a.Reply = strings.TrimSpace(a.Reply)
		if a.Commit = strings.TrimSpace(a.Commit); !shortSHA.MatchString(a.Commit) {
			a.Commit = ""
		}
		answers = append(answers, a)
	}
	return strings.TrimSpace(out[:m[0]]), answers, nil
}

// Reply is the outcome of posting one answer.
type Reply struct {
	Thread string
	URL    string
	Err    error
}

// PostReplies posts each non-empty reply in its thread, addressed to the thread's first comment as GitHub requires,
// and reports each outcome. Replies go out in parallel; one failing does not stop the others.
func PostReplies(ctx context.Context, r Runner, fb Feedback, answers []Answer) []Reply {
	roots := map[string]int{}
	for _, t := range fb.Threads {
		roots[t.ID] = t.RootID
	}
	out := make([]Reply, 0, len(answers))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, a := range answers {
		root := roots[a.Thread]
		if a.Reply == "" || root == 0 {
			continue
		}
		wg.Go(func() {
			raw, err := r.Run(ctx, "gh", "api", "--method", "POST",
				"repos/{owner}/{repo}/pulls/"+strconv.Itoa(fb.Number)+"/comments/"+strconv.Itoa(root)+"/replies",
				"--raw-field=body="+a.Reply)
			rep := Reply{Thread: a.Thread, Err: err}
			if err == nil {
				var res struct {
					HTMLURL string `json:"html_url"`
				}
				if json.Unmarshal(raw, &res) == nil {
					rep.URL = res.HTMLURL
				}
			}
			mu.Lock()
			out = append(out, rep)
			mu.Unlock()
		})
	}
	wg.Wait()
	return out
}

// ThreadByID returns the feedback's thread with id.
func (fb Feedback) ThreadByID(id string) (Thread, bool) {
	for _, t := range fb.Threads {
		if t.ID == id {
			return t, true
		}
	}
	return Thread{}, false
}

type respondKey struct{}

// WithRespond attaches the feedback a respond session answers to ctx.
func WithRespond(ctx context.Context, fb *Feedback) context.Context {
	return context.WithValue(ctx, respondKey{}, fb)
}

// RespondOf returns the feedback a respond session answers, or nil outside one.
func RespondOf(ctx context.Context) *Feedback {
	if fb, ok := ctx.Value(respondKey{}).(*Feedback); ok {
		return fb
	}
	return nil
}
