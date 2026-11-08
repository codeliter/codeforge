package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

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
const CommentNote = "This review will be posted on the PR from my GitHub account as a GitHub review, by codeforge once I choose to; you post nothing. " +
	"Each finding whose path:line is on the diff becomes an inline comment on that line and your final comment becomes the review's body. " +
	"Your closing Decision line is only a suggestion: I pick Approve, Comment or Request changes myself, or post it as plain PR comments instead, one per file (up to ten) and then the final comment. " +
	"So that every finding reads right either way, never refer to other comments or to where a comment sits. " +
	"Write it as I would, addressed to the author, in my voice as codeforge-persona describes it (codeforge-voice where it is silent), in the order the codeforge-review skill gives: " +
	"the findings, then the final comment, then the Decision line. " +
	"No headings or sections, no preamble, no mention of these instructions, no text about being an AI."

// maxCommentBytes keeps the comment under GitHub's limit of 65,536 characters with room for the footer.
const maxCommentBytes = 60_000

const commentFooter = "\n\n---\n<sub>Review assisted by Codeforge</sub>"

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

// cutAtLine cuts text to at most limit bytes at a line break (or, with none, at a character boundary). A code block the
// cut ends inside is closed, so the note after it does not show as code.
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
	head = strings.TrimSpace(head)
	if fence := openFence(strings.Split(head, "\n")); fence != "" {
		head += "\n" + fence
	}
	return head + cutNote
}

// cutNote ends a comment cut to fit; maxCommentBytes leaves room for it, and for a closing fence, under GitHub's limit.
const cutNote = "\n\n_The rest was cut: GitHub limits a comment's length._"

// openFence returns the fence of a code block lines leave open at their end, or "" when every block is closed.
func openFence(lines []string) string {
	for i := 0; i < len(lines); i++ {
		if fenceOpen(lines[i]) == "" {
			continue
		}
		end, closed := fenceBlock(lines, i)
		if !closed {
			return fenceOpen(lines[i])
		}
		i = end - 1
	}
	return ""
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
	// Inline counts the findings posted on their lines; for plain comments (DecisionPlain), the file comments posted.
	Inline int
	// Decision is the type the review went up as.
	Decision Decision
	// Downgraded is why GitHub refused the review's decision, when it went up as a Comment instead: GitHub does not
	// let you approve or request changes on your own pull request.
	Downgraded error
	// Fallback is why GitHub refused the review, when it went up as one plain comment instead.
	Fallback error
	// Final reports, for plain comments, whether the final comment went up as a comment of its own.
	Final bool
}

// markdownHeading matches a heading line. A review a person leaves has none, so none is posted, whatever the model wrote.
var markdownHeading = regexp.MustCompile(`^\s{0,3}#{1,6}\s`)

// withoutHeadings drops every heading, `### Final comment` and file headings included: posted, the final comment is
// simply the review's body.
func withoutHeadings(review string) string {
	lines := strings.Split(review, "\n")
	out := lines[:0]
	for i := 0; i < len(lines); i++ {
		if fenceOpen(lines[i]) != "" {
			// A `# comment` in a code block is code, not a heading.
			end := fenceEnd(lines, i)
			out = append(out, lines[i:end]...)
			i = end - 1
			continue
		}
		if !markdownHeading.MatchString(lines[i]) {
			out = append(out, lines[i])
		}
	}
	return strings.TrimSpace(collapseBlankLines(strings.Join(out, "\n")))
}

// collapseBlankLines keeps at most one blank line in a row.
func collapseBlankLines(s string) string {
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return s
}

// Post puts the review on the pull request as one GitHub review of the user's decision (Approve, Comment or Request
// changes; "" takes the review's own suggestion): each finding on a line of the diff is an inline comment there, and
// the final comment, any finding off the diff and the leads are its body. A decision GitHub refuses (your own PR) goes
// up as a Comment; a review GitHub refuses altogether goes up as one plain comment, so nothing written is lost.
// DecisionPlain posts no review: ordinary PR comments, one per file and then the final comment (see postPlain).
func (c *PostRequest) Post(ctx context.Context, r Runner, review, leads string, decision Decision) (Posted, error) {
	c.mu.Lock()
	repo, number, diff := c.repo, c.number, c.diff
	c.mu.Unlock()
	if number == "" {
		return Posted{}, ErrNoPR
	}
	suggested, review := ParseDecision(StripVerdict(review))
	if decision == "" {
		decision = suggested
	}
	if decision == DecisionPlain {
		return postPlain(ctx, r, repo, number, review, leads)
	}
	// The fallback comment carries every finding in its body, each named by its place and without its label.
	all, _ := SplitFindings(review, "")
	whole, err := CommentBody(withoutHeadings(all), leads)
	if err != nil {
		return Posted{}, err
	}
	summary, inline := SplitFindings(review, diff)
	body, err := CommentBody(withoutHeadings(summary), leads)
	if err != nil {
		return Posted{}, err
	}
	url, rerr := postReview(ctx, r, repo, number, decision, body, inline)
	if rerr == nil {
		return Posted{URL: url, Inline: len(inline), Decision: decision}, nil
	}
	if decision != DecisionComment {
		if curl, err := postReview(ctx, r, repo, number, DecisionComment, body, inline); err == nil {
			return Posted{URL: curl, Inline: len(inline), Decision: DecisionComment, Downgraded: rerr}, nil
		}
	}
	url, err = postComment(ctx, r, repo, number, whole)
	return Posted{URL: url, Fallback: rerr}, err
}

// MaxPlainFileComments caps the comments a plain post makes for files: each one notifies everyone on the PR, and many
// in a row run into GitHub's limit on creating content. The files past it go into the final comment.
const MaxPlainFileComments = 10

// postPlain posts the review as ordinary PR comments, no GitHub review: one per file with that file's findings, in the
// order the review gives them, then the final comment with the leads and the footer. Inline counts the file comments
// and Final reports whether a final comment went up. On a failure partway, Posted says what is already on the PR.
func postPlain(ctx context.Context, r Runner, repo, number, review, leads string) (Posted, error) {
	files, rest := FileComments(review)
	closing := withoutHeadings(rest)
	if len(files) > MaxPlainFileComments {
		// After the reviewer's own words, so a cut to fit takes from the folded findings first.
		var folded []string
		for _, f := range files[MaxPlainFileComments:] {
			folded = append(folded, f.Body)
		}
		closing = strings.TrimSpace(closing + "\n\n---\n\n" + strings.Join(folded, "\n\n"))
		files = files[:MaxPlainFileComments]
	}
	if len(files) == 0 && closing == "" && strings.TrimSpace(leads) == "" {
		return Posted{}, ErrNoReviewText
	}
	final, err := CommentBody(closing, leads)
	if err != nil {
		return Posted{}, err
	}
	// maxCommentBytes counts bytes, which are never fewer than the characters GitHub counts, so a cut in bytes keeps a
	// comment, footer included, under GitHub's limit.
	for i := range files {
		files[i].Body = cutAtLine(files[i].Body, maxCommentBytes)
	}
	if final == commentFooter {
		// Nothing but findings: the last file comment carries the footer. (files is not empty: the check above.)
		last := &files[len(files)-1]
		last.Body = cutAtLine(last.Body, maxCommentBytes-len(commentFooter)) + commentFooter
		final = ""
	}
	posted := Posted{Decision: DecisionPlain}
	for i, f := range files {
		url, err := postCommentBounded(ctx, r, repo, number, f.Body)
		if err != nil {
			var missing []string
			for _, m := range files[i:] {
				missing = append(missing, m.Path)
			}
			left := "the comments on " + strings.Join(missing, ", ")
			if final != "" {
				left += " and " + finalName(leads)
			}
			what := "not posted"
			if errors.Is(err, ErrMayBePosted) {
				what = "not posted, except possibly the one on " + f.Path + ", which may already be on the PR"
			}
			return posted, fmt.Errorf("posted %d of %d file comments; %s are %s: %w", i, len(files), left, what, err)
		}
		posted.URL, posted.Inline = url, i+1
	}
	if final != "" {
		url, err := postCommentBounded(ctx, r, repo, number, final)
		if err != nil {
			what := "is not posted"
			if errors.Is(err, ErrMayBePosted) {
				what = "may already be on the PR"
			}
			return posted, fmt.Errorf("posted %s; %s %s: %w", CountFileComments(len(files)), finalName(leads), what, err)
		}
		posted.URL, posted.Final = url, true
	}
	return posted, nil
}

// finalName names the final comment in an error, with the unconfirmed blocking leads when it carries them, since
// those must reach the PR.
func finalName(leads string) string {
	if strings.TrimSpace(leads) != "" {
		return "the final comment with the unconfirmed blocking leads"
	}
	return "the final comment"
}

// PostCallLimit bounds one gh call that posts a comment: each plain comment gets its own, so a slow one never takes the
// time of those after it.
const PostCallLimit = 20 * time.Second

// ErrMayBePosted marks a comment whose gh call ran out of time: GitHub may have created it all the same.
var ErrMayBePosted = errors.New("it timed out, so it may be on the PR already; check before posting it by hand")

// postCommentBounded is postComment within PostCallLimit.
func postCommentBounded(ctx context.Context, r Runner, repo, number, body string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, PostCallLimit)
	defer cancel()
	url, err := postComment(cctx, r, repo, number, body)
	if err != nil && cctx.Err() != nil {
		return "", fmt.Errorf("%w (%w)", err, ErrMayBePosted)
	}
	return url, err
}

// CountFileComments says "1 file comment" or "n file comments".
func CountFileComments(n int) string {
	if n == 1 {
		return "1 file comment"
	}
	return fmt.Sprintf("%d file comments", n)
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
func postReview(ctx context.Context, r Runner, repo, number string, decision Decision, body string, inline []InlineComment) (string, error) {
	if repo == "" {
		repo = "{owner}/{repo}" // gh fills these in from the checkout
	}
	req := struct {
		Event    Decision        `json:"event"`
		Body     string          `json:"body"`
		Comments []reviewComment `json:"comments"`
	}{Event: decision, Body: body, Comments: []reviewComment{}}
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
