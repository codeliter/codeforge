package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
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
	diff, head   string
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

// Repo, Number and Head say where the review goes: the repository ("" for the checkout's), the pull request, and the
// commit it reviewed.
func (c *PostRequest) Repo() string   { c.mu.Lock(); defer c.mu.Unlock(); return c.repo }
func (c *PostRequest) Number() string { c.mu.Lock(); defer c.mu.Unlock(); return c.number }
func (c *PostRequest) Head() string   { c.mu.Lock(); defer c.mu.Unlock(); return c.head }

// SetHead records the commit at the head of the pull request, which comments on its lines are pinned to.
func (c *PostRequest) SetHead(sha string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.head = sha
}

// Preapprove records that the user asked to post without being asked again (--comment=yes).
func (c *PostRequest) Preapprove() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.preapproved = true
}

// Preapproved reports whether posting needs no further confirmation.
func (c *PostRequest) Preapproved() bool {
	if c == nil {
		return false
	}
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
	"Your closing Decision line is only a suggestion: I pick Approve, Comment or Request changes myself, or post the same comments without a review around them. " +
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

// closeFence closes a code block body leaves open, so text added after it is not read as code.
func closeFence(body string) string {
	if fence := openFence(strings.Split(body, "\n")); fence != "" {
		return body + "\n" + fence
	}
	return body
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

// leadsRule separates the review from the unconfirmed leads under it: a blank line, as a person would leave.
const leadsRule = "\n\n"

// CommentBody is the text to post: the review, then the unconfirmed blocking leads if there are any, and a footer that
// says where it came from. When the whole is too long for GitHub the review is cut, never a lead; if the leads alone
// cannot fit it returns ErrNoRoom.
func CommentBody(review, leads string) (string, error) {
	body := closeFence(strings.TrimSpace(review)) // the leads and the footer must never read as code
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
	// ReviewID is the GitHub id of the review that went up, 0 when none did (plain comments, or the fallback): the key a
	// later round tells codeforge's own reviews apart from the user's by.
	ReviewID int64
	// Inline counts the findings posted on their lines, in a review or, for DecisionPlain, as comments of their own.
	Inline int
	// Decision is the type the review went up as.
	Decision Decision
	// Downgraded is why GitHub refused the review's decision, when it went up as a Comment instead: GitHub does not
	// let you approve or request changes on your own pull request.
	Downgraded error
	// Fallback is why GitHub refused the review, when it went up as one plain comment instead.
	Fallback error
	// OnFile counts the findings posted on their file because GitHub would not take their line.
	OnFile int
	// Final reports, for plain comments, whether the final comment went up as a comment of its own.
	Final bool
	// Refused holds, for plain comments, why GitHub refused each finding it would not put on its line; those findings
	// are in the final comment instead.
	Refused []error
}

// markdownHeading matches a heading line. A review a person leaves has none, so none is posted, whatever the model wrote.
var markdownHeading = regexp.MustCompile(`^\s{0,3}#{1,6}\s`)

// boldHeading matches a line that is only bold words, such as "**Final comment**" or "__Summary:__". Such a line is a
// heading in all but syntax when it names a part of the review (headingWords); any other bold words are the reviewer's.
var boldHeading = regexp.MustCompile(`^\s{0,3}(\*\*|__)\s*[^*_.!?]{1,40}?\s*:?\s*(\*\*|__)\s*:?\s*$`)

// headingWords are the labels a model puts over parts of a review. A person's review has none of them; "**Do not
// merge**" is the reviewer speaking and stays.
var headingWords = map[string]bool{
	"final comment": true, "final comments": true, "summary": true, "verdict": true, "review": true, "overall": true,
	"findings": true, "comments": true, "conclusion": true, "overview": true, "general": true, "notes": true,
	"before merge": true, "foresight": true, "decision": true,
}

// unheaded is line as posted, and false when it is a heading to drop: one in markdown syntax, or a short bold label on a
// line of its own. A bold label that ends in a colon leads into what follows ("**Before merge:**"), so it stays, as
// plain words.
func unheaded(line string) (string, bool) {
	if markdownHeading.MatchString(line) {
		return "", false
	}
	if !boldHeading.MatchString(line) {
		return line, true
	}
	// The colon may sit inside the markers or after them: "**Before merge:**", "**Before merge**:".
	inner := strings.TrimSpace(line)
	colon := strings.HasSuffix(inner, ":")
	inner = strings.Trim(strings.TrimSuffix(inner, ":"), "*_ ")
	colon = colon || strings.HasSuffix(inner, ":")
	label := strings.TrimSpace(strings.TrimSuffix(inner, ":"))
	switch {
	case !headingWords[strings.ToLower(label)]:
		return line, true // the reviewer's own emphasis
	case colon:
		return label + ":", true // a label that leads into what follows: kept, as plain words
	}
	return "", false // a label over a part of the review
}

// withoutHeadings drops every heading, `### Final comment`, `**Final comment**` and file headings included: posted,
// the final comment is simply the review's body.
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
		if line, keep := unheaded(lines[i]); keep {
			out = append(out, line)
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
// DecisionPlain posts no review: a comment on each finding's line and then the final comment, each deletable on its
// own (see postPlain).
func (c *PostRequest) Post(ctx context.Context, r Runner, review, leads string, decision Decision) (Posted, error) {
	c.mu.Lock()
	repo, number, diff, head := c.repo, c.number, c.diff, c.head
	c.mu.Unlock()
	if number == "" {
		return Posted{}, ErrNoPR
	}
	suggested, review := ParseDecision(StripVerdict(review))
	if decision == "" {
		decision = suggested
	}
	if decision == DecisionPlain {
		return postPlain(ctx, r, repo, number, head, diff, review, leads)
	}
	// The fallback comment carries every finding in its body, each named by its place and without its label.
	all, _ := SplitFindings(review, "")
	whole, err := CommentBody(withoutHeadings(all), leads)
	if err != nil {
		return Posted{}, err
	}
	summary, inline, onFile := SplitFindingsByFile(review, diff)
	body, err := CommentBody(withoutHeadings(summary), leads)
	if err != nil {
		return Posted{}, err
	}
	// A finding on a line the change does not touch, in a file it does, goes on that file after the review: never into
	// the final comment.
	withFiles := func(p Posted) (Posted, error) {
		return p, sideComments(ctx, r, repo, number, head, onFile, 0, &p)
	}
	url, id, rerr := postReview(ctx, r, repo, number, head, decision, body, inline)
	if rerr == nil {
		return withFiles(Posted{URL: url, ReviewID: id, Inline: len(inline), Decision: decision})
	}
	if decision != DecisionComment {
		if curl, cid, err := postReview(ctx, r, repo, number, head, DecisionComment, body, inline); err == nil {
			return withFiles(Posted{URL: curl, ReviewID: cid, Inline: len(inline), Decision: DecisionComment, Downgraded: rerr})
		}
	}
	// GitHub refuses a whole review over one line comment it will not place (the line left the diff, the head moved).
	// Then the review goes up without them and each finding follows on its own, on its line or else on its file, so no
	// finding is ever run together with the final comment.
	if len(inline) > 0 {
		// One pass after the review: the line comments first, then those that belong on their files, so a failure
		// part way names every comment that is not on the PR.
		if posted, ok, err := postApart(ctx, r, repo, number, head, decision, body, append(slices.Clone(inline), onFile...), len(inline)); ok {
			return posted, err
		}
	}
	url, err = postComment(ctx, r, repo, number, whole)
	return Posted{URL: url, Fallback: rerr}, err
}

// postApart posts the review without its line comments, as decision or else as a Comment, then each comment on its
// own (see sideComments). ok is false when not even the review went up.
// The first lines of comments go on their lines where GitHub takes them; the rest go on their files.
func postApart(ctx context.Context, r Runner, repo, number, head string, decision Decision, body string, comments []InlineComment, lines int) (Posted, bool, error) {
	url, id, err := postReview(ctx, r, repo, number, head, decision, body, nil)
	posted := Posted{Decision: decision}
	if err != nil {
		var derr error
		if url, id, derr = postReview(ctx, r, repo, number, head, DecisionComment, body, nil); derr != nil {
			return Posted{}, false, nil //nolint:nilerr // not even a bare review went up: the caller posts one plain comment
		}
		posted.Decision, posted.Downgraded = DecisionComment, err
	}
	posted.URL, posted.ReviewID = url, id
	err = sideComments(ctx, r, repo, number, head, comments, lines, &posted)
	return posted, true, err
}

// sideComments posts each comment after the review has gone up: the first lines of them on their line, or on their file when GitHub will
// not take the line, and those it takes on neither together in one PR comment, named by their places. Only GitHub's
// refusal (a 422, which creates nothing) moves a comment on; any other failure may have posted it, so it stops there
// with an error that names what is not on the PR. posted counts what went up.
func sideComments(ctx context.Context, r Runner, repo, number, head string, comments []InlineComment, lines int, posted *Posted) error {
	if len(comments) == 0 {
		return nil
	}
	if head == "" {
		head, _ = prHead(ctx, r, repo, number) //nolint:errcheck // without it GitHub refuses each comment, and they are named below
	}
	var leftover []string
	notPosted := func(i int, err error) error {
		var places []string
		for _, ic := range comments[i:] {
			places = append(places, ic.Place())
		}
		return fmt.Errorf("the review is on the PR, but the comments on %s are not: %w", strings.Join(append(leftoverPlaces(leftover), places...), ", "), err)
	}
	for i, ic := range comments {
		if i < lines { // the first lines comments try their line first
			_, err := bounded(ctx, func(ctx context.Context) (string, error) { return postLineComment(ctx, r, repo, number, head, ic) })
			if err == nil {
				posted.Inline++
				continue
			}
			if !errors.Is(err, errRefused) {
				return notPosted(i, err)
			}
		}
		_, err := bounded(ctx, func(ctx context.Context) (string, error) { return "", postFileComment(ctx, r, repo, number, head, ic) })
		if err == nil {
			posted.OnFile++
			continue
		}
		if !errors.Is(err, errRefused) {
			return notPosted(i, err)
		}
		leftover = append(leftover, Named(ic.Place(), ic.Body))
	}
	if len(leftover) == 0 {
		return nil
	}
	if _, err := bounded(ctx, func(ctx context.Context) (string, error) {
		return postComment(ctx, r, repo, number, strings.Join(leftover, "\n\n")+commentFooter)
	}); err != nil {
		return fmt.Errorf("the review is on the PR, but the comments on %s are not: %w", strings.Join(leftoverPlaces(leftover), ", "), err)
	}
	posted.Refused = append(posted.Refused, fmt.Errorf("%w: %d comments posted together", errRefused, len(leftover)))
	return nil
}

// leftoverPlaces names the places of comments written with Named.
func leftoverPlaces(named []string) []string {
	var out []string
	for _, n := range named {
		place, _, _ := strings.Cut(n, ":`")
		if p, _, ok := strings.Cut(strings.TrimPrefix(n, "`"), "`"); ok {
			place = p
		}
		out = append(out, place)
	}
	return out
}

// postFileComment posts a comment on a file of the pull request rather than a line, naming the line it is about: the
// place a finding goes when GitHub will not take its line.
func postFileComment(ctx context.Context, r Runner, repo, number, head string, ic InlineComment) error {
	req := struct {
		Path        string `json:"path"`
		Body        string `json:"body"`
		SubjectType string `json:"subject_type"` //nolint:tagliatelle // GitHub's REST field
		CommitID    string `json:"commit_id"`    //nolint:tagliatelle // GitHub's REST field
	}{ic.Path, fmt.Sprintf("Line %d: %s", ic.Line, ic.Body), "file", head}
	if _, err := ghAPIPost(ctx, r, repoPath(repo)+"/pulls/"+number+"/comments", req); err != nil {
		if strings.Contains(err.Error(), "(HTTP 422)") {
			err = fmt.Errorf("%w: %w", errRefused, err)
		}
		return fmt.Errorf("posting the comment on %s: %w", ic.Path, err)
	}
	return nil
}

// MaxPlainLineComments caps the line comments a plain post makes: each one notifies everyone on the PR, and many in a
// row run into GitHub's limit on creating content. The findings past it go into the final comment.
const MaxPlainLineComments = 20

// postPlain posts the review as comments that are no GitHub review, so each can be edited or deleted on its own: every
// finding on a line of the diff as a comment on that line, with the code shown above it as in a review, then the final
// comment, which holds any finding off the diff and the leads, as an ordinary PR comment. Inline counts the line
// comments and Final reports whether a final comment went up. On a failure partway, Posted says what is on the PR.
func postPlain(ctx context.Context, r Runner, repo, number, head, diff, review, leads string) (Posted, error) {
	summary, inline := SplitFindings(review, diff)
	closing := withoutHeadings(summary)
	var folded []string // findings that go in the final comment after all: past the cap, or refused by GitHub
	for _, ic := range inline[min(len(inline), MaxPlainLineComments):] {
		folded = append(folded, Named(ic.Place(), ic.Body))
	}
	inline = inline[:min(len(inline), MaxPlainLineComments)]
	if len(inline) == 0 && closing == "" && len(folded) == 0 && strings.TrimSpace(leads) == "" {
		return Posted{}, ErrNoReviewText
	}
	// The leads must reach the PR. Check they fit beside the most the final comment can hold, every finding folded in
	// (past the cap, or refused on its line), before anything goes up: nothing is posted when they cannot.
	worst := slices.Clone(folded)
	for _, ic := range inline {
		worst = append(worst, Named(ic.Place(), ic.Body))
	}
	if _, err := CommentBody(foldInto(closing, worst), leads); err != nil {
		return Posted{}, err
	}
	for i := range inline {
		inline[i].Body = fitComment(inline[i].Body, maxCommentBytes)
	}
	footed := closing == "" && len(folded) == 0 && strings.TrimSpace(leads) == ""
	if footed {
		// Nothing but findings: the last line comment carries the footer. (inline is not empty: the check above.)
		last := &inline[len(inline)-1]
		last.Body = closeFence(fitComment(last.Body, maxCommentBytes-len(commentFooter)-len("\n```"))) + commentFooter
	}
	if len(inline) > 0 && head == "" {
		// A line comment is pinned to a commit; without the head from the brief, ask GitHub for it.
		hctx, cancel := context.WithTimeout(ctx, PostCallLimit) // a lookup, not a post: nothing can be up if it times out
		var err error
		head, err = prHead(hctx, r, repo, number)
		cancel()
		if err != nil {
			return Posted{}, err
		}
	}
	posted := Posted{Decision: DecisionPlain}
	footerUp := false // whether a posted line comment carries the footer
	for i, ic := range inline {
		url, err := bounded(ctx, func(ctx context.Context) (string, error) { return postLineComment(ctx, r, repo, number, head, ic) })
		if err == nil {
			posted.URL, posted.Inline = url, posted.Inline+1
			footerUp = footerUp || strings.HasSuffix(ic.Body, commentFooter)
			continue
		}
		if !errors.Is(err, errRefused) {
			// It may be up (a timeout), or GitHub is failing: posting it again could show it twice, so stop and say
			// what is not on the PR.
			var missing []string
			for _, m := range inline[i:] {
				missing = append(missing, m.Place())
			}
			left := "the comments on " + strings.Join(missing, ", ")
			if !footed || len(folded) > 0 {
				left += " and " + finalName(leads)
			}
			if n := len(posted.Refused); n > 0 {
				left += fmt.Sprintf(" (which holds the %s GitHub refused)", CountLineComments(n))
			}
			what := "are not posted"
			if errors.Is(err, ErrMayBePosted) {
				what += ", except possibly the one on " + ic.Place() + ", which may already be on the PR"
			}
			return posted, fmt.Errorf("posted %s; %s %s: %w", CountLineComments(posted.Inline), left, what, err)
		}
		// GitHub would not put it on its line (a head that moved, a file too large to diff): it joins the final
		// comment, named by its place, so it is not lost.
		folded = append(folded, Named(ic.Place(), strings.TrimSuffix(ic.Body, commentFooter)))
		posted.Refused = append(posted.Refused, err)
	}
	if footed && len(posted.Refused) == 0 {
		return posted, nil
	}
	final, err := CommentBody(foldInto(closing, folded), leads)
	if err != nil {
		return posted, err
	}
	if footerUp {
		final = strings.TrimSuffix(final, commentFooter) // said once, on the line comment
	}
	url, err := bounded(ctx, func(ctx context.Context) (string, error) { return postComment(ctx, r, repo, number, final) })
	if err != nil {
		what := "is not posted"
		if errors.Is(err, ErrMayBePosted) {
			what = "may already be on the PR"
		}
		return posted, fmt.Errorf("posted %s; %s %s: %w", CountLineComments(posted.Inline), finalName(leads), what, err)
	}
	posted.URL, posted.Final = url, true
	return posted, nil
}

// foldInto puts findings that could not sit on their lines into the final comment, after the reviewer's own words so
// a cut to fit takes from them first.
func foldInto(closing string, folded []string) string {
	if len(folded) == 0 {
		return closing
	}
	return strings.TrimSpace(closing + "\n\n---\n\n" + strings.Join(folded, "\n\n"))
}

// postLineComment posts one comment on a line of the pull request's diff, outside any review, so its author can delete
// it on its own.
func postLineComment(ctx context.Context, r Runner, repo, number, head string, ic InlineComment) (string, error) {
	req := struct {
		reviewComment
		CommitID string `json:"commit_id"`
	}{commentOn(ic), head}
	out, err := ghAPIPost(ctx, r, repoPath(repo)+"/pulls/"+number+"/comments", req)
	if err != nil {
		if strings.Contains(err.Error(), "(HTTP 422)") { // gh's own wording for the status
			err = fmt.Errorf("%w: %w", errRefused, err)
		}
		return "", fmt.Errorf("posting the comment on %s: %w", ic.Place(), err)
	}
	return htmlURL(out), nil
}

// errRefused marks a line comment GitHub answered 422 for: it will not sit on that line (the head moved, the file is
// too large to diff), and nothing was created, so it can safely go in the final comment instead.
var errRefused = errors.New("GitHub would not put it on its line")

// fitComment is cutAtLine for a comment that may hold a ```suggestion block: a cut one would apply an incomplete
// replacement with one click, so a comment that must be cut keeps its code as a plain block.
func fitComment(body string, limit int) string {
	if len(body) <= limit {
		return body
	}
	return cutAtLine(plainCode(body), limit)
}

// prHead asks GitHub for the commit at the head of the pull request.
func prHead(ctx context.Context, r Runner, repo, number string) (string, error) {
	args := []string{"pr", "view", number, "--json", "headRefOid", "--jq", ".headRefOid"}
	if repo != "" {
		args = append(args, "--repo", repo)
	}
	out, err := r.Run(ctx, "gh", args...)
	if sha := strings.TrimSpace(string(out)); err == nil && sha != "" {
		return sha, nil
	}
	return "", fmt.Errorf("finding the head commit of PR %s, which line comments are pinned to: %w", number, errors.Join(err, errNoHead))
}

var errNoHead = errors.New("no head commit")

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

// bounded runs one gh call that posts a comment within PostCallLimit.
func bounded(ctx context.Context, post func(context.Context) (string, error)) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, PostCallLimit)
	defer cancel()
	url, err := post(cctx)
	if err != nil && cctx.Err() != nil {
		return "", fmt.Errorf("%w (%w)", err, ErrMayBePosted)
	}
	return url, err
}

// CountLineComments says "1 line comment" or "n line comments".
func CountLineComments(n int) string {
	if n == 1 {
		return "1 line comment"
	}
	return fmt.Sprintf("%d line comments", n)
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

// reviewComment is one inline comment of a GitHub review, as the API takes it. A comment on a range also has its first
// line and side.
type reviewComment struct {
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Side      string `json:"side"`
	StartLine int    `json:"start_line,omitempty"`
	StartSide string `json:"start_side,omitempty"`
	Body      string `json:"body"`
}

// commentOn is the API form of ic, on the new side of the diff.
func commentOn(ic InlineComment) reviewComment {
	c := reviewComment{Path: ic.Path, Line: ic.Line, Side: "RIGHT", Body: ic.Body}
	if ic.StartLine > 0 {
		c.StartLine, c.StartSide = ic.StartLine, "RIGHT"
	}
	return c
}

// postReview creates one GitHub review with an inline comment per finding (see ghAPIPost for how it is sent).
// The review is pinned to head, the commit that was reviewed, so a push in the meantime cannot move it onto code nobody
// read; "" leaves it to GitHub's latest.
// It returns the review's URL and its GitHub id, 0 when the answer has none.
func postReview(ctx context.Context, r Runner, repo, number, head string, decision Decision, body string, inline []InlineComment) (string, int64, error) {
	req := struct {
		CommitID string          `json:"commit_id,omitempty"` //nolint:tagliatelle // GitHub's REST field
		Event    Decision        `json:"event"`
		Body     string          `json:"body"`
		Comments []reviewComment `json:"comments"`
	}{CommitID: head, Event: decision, Body: body, Comments: []reviewComment{}}
	for _, ic := range inline {
		req.Comments = append(req.Comments, commentOn(ic))
	}
	out, err := ghAPIPost(ctx, r, repoPath(repo)+"/pulls/"+number+"/reviews", req)
	if err != nil {
		return "", 0, fmt.Errorf("posting the inline review on PR %s: %w", number, err)
	}
	var res struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(out, &res) //nolint:errcheck // an answer with no id leaves 0, which records nothing
	return htmlURL(out), res.ID, nil
}

// repoPath is the API path of repo; an empty repo is the checkout's, which gh fills in.
func repoPath(repo string) string {
	if repo == "" {
		repo = "{owner}/{repo}"
	}
	return "repos/" + repo
}

// ghAPIPost sends payload as json to the GitHub API path. It goes through a private temporary file and --input: gh's
// own field flags cannot build an array of objects that mixes strings and numbers (it groups --raw-field and --field
// values separately, so lines landed on the wrong comments and GitHub answered 422), and a file keeps the review's text
// out of argument parsing altogether.
func ghAPIPost(ctx context.Context, r Runner, path string, payload any) ([]byte, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encoding the request: %w", err)
	}
	f, err := os.CreateTemp("", "codeforge-review-*.json")
	if err != nil {
		return nil, fmt.Errorf("writing the request: %w", err)
	}
	defer os.Remove(f.Name()) //nolint:errcheck // best effort; the file is private and in the temp directory
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("writing the request: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("writing the request: %w", err)
	}
	out, err := r.Run(ctx, "gh", "api", "--method", "POST", path, "--input", f.Name())
	if err != nil {
		return nil, fmt.Errorf("calling the GitHub API: %w", err)
	}
	return out, nil
}

// htmlURL is the html_url of a GitHub API answer, or the answer itself when it has none.
func htmlURL(out []byte) string {
	var res struct {
		HTMLURL string `json:"html_url"`
	}
	if json.Unmarshal(out, &res) == nil && res.HTMLURL != "" {
		return res.HTMLURL
	}
	return strings.TrimSpace(string(out))
}
