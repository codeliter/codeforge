package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/codeliter/codeforge/src/effort"
	"github.com/codeliter/codeforge/src/text"
)

// PostRequest is a request to post the finished review on the pull request it was about. It is the user's explicit
// choice (codeforge review N --comment), made on the command line and carried here to where the review ends: the
// model that wrote the review stays read-only and never posts anything itself, so text in the PR cannot make it post.
type PostRequest struct {
	mu           sync.Mutex
	repo, number string
	enabled      bool
	preapproved  bool
}

type commentKey struct{}

// WithPost attaches c to ctx.
func WithPost(ctx context.Context, c *PostRequest) context.Context {
	return context.WithValue(ctx, commentKey{}, c)
}

// PostOf returns the review's comment request, or nil outside a review.
func PostOf(ctx context.Context) *PostRequest {
	if c, ok := ctx.Value(commentKey{}).(*PostRequest); ok {
		return c
	}
	return nil
}

// Target turns posting on for pull request number in repo; an empty repo means the repository of the working directory.
func (c *PostRequest) Target(repo, number string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.repo, c.number, c.enabled = repo, number, true
}

// Preapprove records that the user asked to post without being asked again (--comment=yes).
func (c *PostRequest) Preapprove() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.preapproved = true
}

// Preapproved reports whether posting needs no further confirmation.
func (c *PostRequest) Preapproved() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.preapproved
}

// Enabled reports whether the review is to be posted.
func (c *PostRequest) Enabled() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.enabled
}

// CommentNote tells the reviewer how its answer will be used, so it writes the comment itself in the runner's voice.
// The persona is a style, never a claim to be someone: the footer says the comment came from codeforge.
const CommentNote = "This review will be posted as one comment on the PR from my GitHub account, by codeforge after you finish; you post nothing. " +
	"Write your whole answer as that comment, addressed to the author, in my voice as codeforge-persona describes it (codeforge-voice where it is silent). " +
	"Verdict first, then each finding as a Conventional Comment with path:line. No preamble, no mention of these instructions, no text about being an AI."

// maxCommentBytes keeps the comment under GitHub's limit of 65,536 characters with room for the footer.
const maxCommentBytes = 60_000

const commentFooter = "\n\n---\n<sub>Reviewed with codeforge.</sub>"

var (
	// ErrNoReviewText is returned when the review produced nothing to post.
	ErrNoReviewText = errors.New("review: the run produced no text to post")
	errEngineOutput = errors.New("review: reading the run's output")
)

// EngineOutput reads the review text from Empryo's --json result.
func EngineOutput(raw []byte) (string, error) {
	var res effort.EmpryoResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", fmt.Errorf("%w: %w", errEngineOutput, err)
	}
	if res.Error != "" {
		return "", fmt.Errorf("%w: %s", errEngineOutput, res.Error)
	}
	if strings.TrimSpace(res.Output) == "" {
		return "", ErrNoReviewText
	}
	return res.Output, nil
}

// How a comment is divided. The review keeps at least minReviewShare of it; the second opinion takes the rest, and a
// juror's summary is shortened before any blocker is. A blocker is never shortened.
const (
	minReviewShare = 20_000
	maxSummaryLine = 300
	secondHeader   = "\n\n---\n**Second opinion, unverified:**\n"
)

// ErrNoRoom is returned when the jurors' blocking findings alone do not fit in one comment. Cutting them would publish a
// review that says Approve without the blockers a juror found, so nothing is posted.
var ErrNoRoom = errors.New("review: the second opinion's blocking findings do not fit in one comment")

// fitSecondOpinion shortens the second opinion to limit bytes without losing or cutting a blocker: should-fix lines go
// first, then the jurors' summaries are shortened, then the jurors who found nothing blocking are dropped. It reports
// false when the blockers alone still do not fit.
func fitSecondOpinion(opinion string, limit int) (string, bool) {
	if len(opinion) <= limit {
		return opinion, true
	}
	var kept []string
	for _, l := range strings.Split(strings.TrimRight(opinion, "\n"), "\n") {
		if !strings.HasPrefix(l, "  should") {
			kept = append(kept, l)
		}
	}
	fits := func() bool { return len(strings.Join(kept, "\n")) <= limit }
	for _, step := range []func(string) string{
		func(l string) string { return l },
		func(l string) string {
			if strings.HasPrefix(l, "  blocking") {
				return l
			}
			return text.Shorten(l, maxSummaryLine)
		},
	} {
		for i, l := range kept {
			kept[i] = step(l)
		}
		if fits() {
			return strings.Join(kept, "\n"), true
		}
	}
	// Last, the jurors who found nothing blocking: keep the opening lines (which say the change is not approved while a
	// blocker stands), the blocking lines and the juror line above each. A blocker is kept whole or the post is refused.
	var only []string
	for i, l := range kept {
		opening := !strings.HasPrefix(l, "- ") && !strings.HasPrefix(l, "  ")
		if opening || strings.HasPrefix(l, "  blocking") || (i+1 < len(kept) && strings.HasPrefix(kept[i+1], "  blocking")) {
			only = append(only, l)
		}
	}
	if joined := strings.Join(only, "\n"); len(joined) <= limit {
		return joined, true
	}
	return "", false
}

// cutAtLine cuts text to at most limit bytes at a line break (or, with none, at a character boundary).
func cutAtLine(body string, limit int) string {
	if len(body) <= limit {
		return body
	}
	head := body[:limit]
	if cut := strings.LastIndex(head, "\n"); cut > 0 {
		head = body[:cut]
	} else {
		head = text.Cut(body, limit) // never end the cut inside a character
	}
	return strings.TrimSpace(head) + "\n\n_The rest was cut: GitHub limits a comment's length._"
}

// CommentBody is the text to post: the review, then the second opinion if there is one, and a footer that says where it
// came from. When the whole is too long for GitHub the review is cut, never a blocker the jurors found; if the blockers
// alone cannot fit it returns ErrNoRoom.
func CommentBody(review, secondOpinion string) (string, error) {
	body := strings.TrimSpace(review)
	second := strings.TrimSpace(secondOpinion)
	if second != "" {
		limit := maxCommentBytes - min(len(body), minReviewShare) - len(secondHeader)
		fitted, ok := fitSecondOpinion(second, limit)
		if !ok {
			return "", ErrNoRoom
		}
		second = secondHeader + fitted
	}
	return cutAtLine(body, maxCommentBytes-len(second)) + second + commentFooter, nil
}

// Post adds the review, and the second opinion if any, to the pull request as a comment and returns what gh printed, the
// comment's address.
func (c *PostRequest) Post(ctx context.Context, r Runner, review, secondOpinion string) (string, error) {
	c.mu.Lock()
	repo, number := c.repo, c.number
	c.mu.Unlock()
	if number == "" {
		return "", ErrNoPR
	}
	args := []string{"pr", "comment", number}
	if repo != "" {
		args = append(args, "--repo", repo)
	}
	body, err := CommentBody(review, secondOpinion)
	if err != nil {
		return "", err
	}
	// --body=... keeps a review that begins with a dash from being read as a flag.
	args = append(args, "--body="+body)
	out, err := r.Run(ctx, "gh", args...)
	if err != nil {
		return "", fmt.Errorf("posting the review on PR %s: %w", number, err)
	}
	return strings.TrimSpace(string(out)), nil
}
