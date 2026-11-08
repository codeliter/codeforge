package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
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
	diff         string
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

// SetDiff records the pull request's diff, so findings on its lines can be posted on those lines.
func (c *PostRequest) SetDiff(diff string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.diff = diff
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
const CommentNote = "This review will be posted on the PR from my GitHub account as a GitHub review, by codeforge after you finish; you post nothing. " +
	"Each finding whose path:line is on the diff becomes an inline comment on that line, and the rest of your answer becomes the review's summary. " +
	"Write your whole answer addressed to the author, in my voice as codeforge-persona describes it (codeforge-voice where it is silent). " +
	"No verdict line. Open with one or two sentences on the change, then one `### path` heading per file with its findings under it, " +
	"each a Conventional Comment that starts `label: path:line — ` on its own line, then the points that belong to no single line. " +
	"No preamble, no mention of these instructions, no text about being an AI."

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

// minReviewShare is how much of a comment the review keeps when the leads also need room.
const minReviewShare = 20_000

// ErrNoRoom is returned when the unconfirmed blocking leads alone do not fit in one comment. Cutting them would publish
// a review without the blockers a second model found, so nothing is posted.
var ErrNoRoom = errors.New("review: the unconfirmed blocking leads do not fit in one comment")

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

// leadsRule separates the review from the unconfirmed leads under it.
const leadsRule = "\n\n---\n"

// CommentBody is the text to post: the review, then the unconfirmed blocking leads if there are any, and a footer that
// says where it came from. When the whole is too long for GitHub the review is cut, never a lead; if the leads alone
// cannot fit it returns ErrNoRoom.
func CommentBody(review, leads string) (string, error) {
	body := strings.TrimSpace(review)
	extra := strings.TrimSpace(leads)
	if extra != "" {
		if len(extra)+len(leadsRule) > maxCommentBytes-min(len(body), minReviewShare) {
			return "", ErrNoRoom
		}
		if body == "" {
			return extra + commentFooter, nil
		}
		extra = leadsRule + extra
	}
	return cutAtLine(body, maxCommentBytes-len(extra)) + extra + commentFooter, nil
}

// Posted is what a posted review left on the pull request.
type Posted struct {
	URL string
	// Inline counts the findings posted on their lines.
	Inline int
	// Fallback is why GitHub refused the inline comments, when the review went up as one comment instead.
	Fallback error
}

// Post puts the review on the pull request. Findings on lines of the diff become the inline comments of one GitHub
// review (event COMMENT, never an approval), with the rest of the review and the leads as its summary. With no such
// finding, or when GitHub refuses the inline comments, the whole review is posted as one comment.
func (c *PostRequest) Post(ctx context.Context, r Runner, review, leads string) (Posted, error) {
	c.mu.Lock()
	repo, number, diff := c.repo, c.number, c.diff
	c.mu.Unlock()
	if number == "" {
		return Posted{}, ErrNoPR
	}
	review = StripVerdict(review)
	whole, err := CommentBody(review, leads)
	if err != nil {
		return Posted{}, err
	}
	summary, inline := SplitFindings(review, diff)
	if len(inline) == 0 {
		url, err := postComment(ctx, r, repo, number, whole)
		return Posted{URL: url}, err
	}
	body, err := CommentBody(summary, leads)
	if err != nil {
		return Posted{}, err
	}
	url, rerr := postReview(ctx, r, repo, number, body, inline)
	if rerr == nil {
		return Posted{URL: url, Inline: len(inline)}, nil
	}
	url, err = postComment(ctx, r, repo, number, whole)
	return Posted{URL: url, Fallback: rerr}, err
}

func postComment(ctx context.Context, r Runner, repo, number, body string) (string, error) {
	args := []string{"pr", "comment", number}
	if repo != "" {
		args = append(args, "--repo", repo)
	}
	// --body=... keeps a review that begins with a dash from being read as a flag.
	args = append(args, "--body="+body)
	out, err := r.Run(ctx, "gh", args...)
	if err != nil {
		return "", fmt.Errorf("posting the review on PR %s: %w", number, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// reviewComment is one inline comment of a GitHub review, as the API takes it.
type reviewComment struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Side string `json:"side"`
	Body string `json:"body"`
}

// postReview creates one GitHub review with an inline comment per finding. The request body is written as json to a
// private temporary file and sent with --input: gh's own field flags cannot build an array of objects that mixes
// strings and numbers (it groups --raw-field and --field values separately, so lines landed on the wrong comments and
// GitHub answered 422), and a file keeps the review's text out of argument parsing altogether.
func postReview(ctx context.Context, r Runner, repo, number, body string, inline []InlineComment) (string, error) {
	if repo == "" {
		repo = "{owner}/{repo}" // gh fills these in from the checkout
	}
	req := struct {
		Event    string          `json:"event"`
		Body     string          `json:"body"`
		Comments []reviewComment `json:"comments"`
	}{Event: "COMMENT", Body: body}
	for _, ic := range inline {
		req.Comments = append(req.Comments, reviewComment{Path: ic.Path, Line: ic.Line, Side: "RIGHT", Body: ic.Body})
	}
	data, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("encoding the review: %w", err)
	}
	f, err := os.CreateTemp("", "codeforge-review-*.json")
	if err != nil {
		return "", fmt.Errorf("writing the review: %w", err)
	}
	defer os.Remove(f.Name()) //nolint:errcheck // best effort; the file is private and in the temp directory
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return "", fmt.Errorf("writing the review: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("writing the review: %w", err)
	}
	out, err := r.Run(ctx, "gh", "api", "--method", "POST", "repos/"+repo+"/pulls/"+number+"/reviews", "--input", f.Name())
	if err != nil {
		return "", fmt.Errorf("posting the inline review on PR %s: %w", number, err)
	}
	var res struct {
		HTMLURL string `json:"html_url"`
	}
	if json.Unmarshal(out, &res) == nil && res.HTMLURL != "" {
		return res.HTMLURL, nil
	}
	return strings.TrimSpace(string(out)), nil
}
