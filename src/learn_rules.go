package src

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/codeliter/codeforge/src/store"
)

const (
	minRuleCorrection = 8
	maxRuleText       = 300
	minProposalSize   = 2
	minProposalSess   = 2
	maxRules          = 10
	correctionCueMax  = 300
)

var (
	errNoDir      = errors.New("this directory cannot be placed inside the repository, so a rule cannot be made for it")
	errNoVectors  = errors.New("vector search is not available")
	errNoProposal = errors.New("no such proposal; run codeforge learn proposals for the current list")
	errNoRuleID   = errors.New("rule IDs look like R1; see codeforge learn rules")
	errRuleText   = errors.New("rule text must be one line of 1 to 300 printable characters")
)

// correctionCues are the starts of a prompt that read as a correction. The list is deliberately short: a missed
// correction costs nothing, a mistaken one is only a proposal the user can dismiss.
var correctionCues = []string{
	"no,", "no ", "don't ", "dont ", "do not ", "stop ", "instead", "actually,", "wrong", "never ", "always ",
	"please use ", "please don't ", "prefer ", "you should ", "you shouldn't ", "you must ",
}

// correctionText returns the prompt as a correction candidate, or false. It must be one printable line of 8 to 300
// characters that starts with a cue and holds nothing that looks like a credential.
func correctionText(prompt string) (string, bool) {
	text := strings.TrimSpace(prompt)
	if n := utf8.RuneCountInString(text); n < minRuleCorrection || n > correctionCueMax || !utf8.ValidString(text) {
		return "", false
	}
	if strings.IndexFunc(text, unicode.IsControl) >= 0 || credentialShape.MatchString(text) {
		return "", false
	}
	lower := strings.ToLower(text)
	for _, cue := range correctionCues {
		if strings.HasPrefix(lower, cue) {
			return text, true
		}
	}
	return "", false
}

// ruleProposal is a group of corrections that look like one rule.
type ruleProposal struct {
	// AutoText is a sentence the user typed in at least two different recorded sessions, word for word. Only such a
	// proposal may be adopted without asking: fuzzy grouping cannot prove two corrections mean the same thing.
	AutoText string
	ID       string
	Text     string
	Key      string
	IDs      []int64
	Texts    []string
	Sessions int
	Latest   time.Time
}

// groupCorrections groups corrections whose embeddings are close. With neighbours from the vector table it links
// pairs at ruleSimilarity or better; without them (the extension is missing) it links pairs by word overlap.
func groupCorrections(obs []store.Observation, near func(store.Observation) []store.Neighbour) [][]int {
	// Opposite corrections ("use X" and "do not use X") never link, whatever their similarity.
	linkable := func(a, b store.Observation) bool { return compatible(a.Text, b.Text) }
	parent := make([]int, len(obs))
	for i := range parent {
		parent[i] = i
	}
	find := func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	members := make([][]int, len(obs))
	for i := range members {
		members[i] = []int{i}
	}
	// union joins two corrections' groups only if every member of one is compatible with every member of the other:
	// a chain of pairwise-compatible links can otherwise join opposite endpoints into one rule.
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra == rb {
			return
		}
		for _, x := range members[ra] {
			for _, y := range members[rb] {
				if !linkable(obs[x], obs[y]) {
					return
				}
			}
		}
		parent[ra] = rb
		members[rb] = append(members[rb], members[ra]...)
		members[ra] = nil
	}
	index := map[int64]int{}
	for i, o := range obs {
		index[o.ID] = i
	}
	for i, o := range obs {
		if near == nil {
			for j := i + 1; j < len(obs); j++ {
				if linkable(o, obs[j]) && jaccard(o.Text, obs[j].Text) >= jaccardFallback {
					union(i, j)
				}
			}
			continue
		}
		for _, n := range near(o) {
			if j, ok := index[n.ID]; ok && j != i && linkable(o, obs[j]) && similarFromDistance(n.Distance) >= ruleSimilarity {
				union(i, j)
			}
		}
	}
	groups := map[int][]int{}
	for i := range obs {
		groups[find(i)] = append(groups[find(i)], i)
	}
	out := make([][]int, 0, len(groups))
	for _, g := range groups {
		slices.Sort(g)
		out = append(out, g)
	}
	slices.SortFunc(out, func(a, b []int) int { return cmp.Compare(a[0], b[0]) })
	return out
}

// sentenceKey is a correction's own sentence with case, spacing and trailing sentence punctuation ignored and
// nothing else dropped: "enable TLS verification" and "disable TLS verification" differ, "retries < 3" and
// "retries > 3" differ, and so do "run lint after tests" and the reverse. Every decision that adopts, covers or
// declines a rule compares sentences this way; fuzzy similarity only groups proposals for the user to read.
func sentenceKey(text string) string {
	return strings.TrimRight(strings.Join(strings.Fields(strings.ToLower(text)), " "), ".!?;:,")
}

// proposalsOf builds the proposals for a repository: groups of at least minProposalSize corrections from at least
// minProposalSess sessions, none reverted, not dismissed and not already covered by an accepted rule.
func proposalsOf(obs []store.Observation, rules []store.Rule, dismissed map[string]bool, near func(store.Observation) []store.Neighbour, dir string) []ruleProposal {
	var corrections []store.Observation
	for _, o := range obs {
		// A correction counts where it was made, like a habit: root corrections do not create rules for frontend/.
		// One a rule already is (same sentence, or a source of the rule) is set aside before grouping, so the evidence
		// behind a rule can never swallow a later, opposite wording that happens to group with it.
		if o.Kind == store.KindCorrection && o.RevertedAt == nil && cmp.Or(o.Dir, ".") == dir && utf8.RuneCountInString(o.Text) <= maxRuleText && !ruleCovers(rules, o, dir) {
			corrections = append(corrections, o)
		}
	}
	var out []ruleProposal
	for _, g := range groupCorrections(corrections, near) {
		sessions := map[string]bool{}
		repeats := map[string]map[string]bool{}
		p := ruleProposal{}
		for _, i := range g {
			o := corrections[i]
			if o.SessionID != "" {
				k := sentenceKey(o.Text)
				if repeats[k] == nil {
					repeats[k] = map[string]bool{}
				}
				repeats[k][o.SessionID] = true
				if len(repeats[k]) >= minProposalSess && (p.AutoText == "" || !o.CreatedAt.Before(p.Latest)) {
					p.AutoText = o.Text
				}
			}
			p.IDs = append(p.IDs, o.ID)
			p.Texts = append(p.Texts, o.Text)
			sessions[cmp.Or(o.SessionID, "cli-"+strconv.FormatInt(o.ID, 10))] = true
			if !o.CreatedAt.Before(p.Latest) {
				p.Latest, p.Text = o.CreatedAt, o.Text
			}
		}
		p.Sessions = len(sessions)
		p.Key = dismissKey(p.Text)
		p.ID = "P" + strconv.FormatInt(slices.Min(p.IDs), 10)
		if len(g) < minProposalSize || p.Sessions < minProposalSess || anyDismissed(dismissed, p.Texts) {
			continue
		}
		out = append(out, p)
	}
	slices.SortStableFunc(out, func(a, b ruleProposal) int {
		return cmp.Or(cmp.Compare(len(b.IDs), len(a.IDs)), b.Latest.Compare(a.Latest))
	})
	return out
}

// anyDismissed reports whether the user declined any wording in the group.
func anyDismissed(dismissed map[string]bool, texts []string) bool {
	for _, t := range texts {
		if dismissed[dismissKey(t)] {
			return true
		}
	}
	return false
}

// ruleCovers reports whether a rule in this directory already is the correction: the same sentence (case, spacing and
// trailing punctuation aside), or one of the corrections the rule was learned from. It never uses fuzzy similarity,
// so a rule cannot hide its opposite ("enable X" against "disable X"). Forgotten rules still cover, because
// forgetting is permanent even if their evidence is later reverted.
func ruleCovers(rules []store.Rule, o store.Observation, dir string) bool {
	key := sentenceKey(o.Text)
	for _, r := range rules {
		if cmp.Or(r.Dir, ".") != dir {
			continue
		}
		if sentenceKey(r.Text) == key || slices.Contains(sourceIDs(r.SourceIDs), o.ID) {
			return true
		}
	}
	return false
}

// loadLearning reads what proposals need and, when vectors are available, finds neighbours for every correction.
func loadLearning(ctx context.Context, home, repo string) (obs []store.Observation, rules []store.Rule, dismissed map[string]bool, near func(store.Observation) []store.Neighbour, err error) {
	err = withLearning(ctx, home, func(s *store.Store) error {
		var e error
		if obs, e = s.Observations(ctx, repo, false); e != nil {
			return e
		}
		if rules, e = s.Rules(ctx, repo, true); e != nil {
			return e
		}
		if dismissed, e = s.Dismissed(ctx, repo); e != nil {
			return e
		}
		// A vector problem never costs the user their proposals: without neighbours, grouping uses word overlap.
		// With fewer than two corrections there is nothing to group, so no vectors are computed.
		if countCorrections(obs) >= minProposalSize {
			if n, e := vectorNeighbours(ctx, s, obs); e == nil {
				near = n
			}
		}
		return nil
	})
	return obs, rules, dismissed, near, err
}

// neighbourCap bounds how far a search widens; sqlite-vec accepts k up to 4096. It is a variable so a test can lower it.
var neighbourCap = 4096

func countCorrections(obs []store.Observation) int {
	n := 0
	for _, o := range obs {
		if o.Kind == store.KindCorrection {
			n++
		}
	}
	return n
}

// vectorNeighbours finds, for every live correction in obs, its nearest live corrections of the same repository.
// The vector table holds every repository's corrections, so each search widens until it has enough eligible
// neighbours or runs out of vectors: other repositories and reverted rows can never crowd a real match out.
func vectorNeighbours(ctx context.Context, s *store.Store, obs []store.Observation) (func(store.Observation) []store.Neighbour, error) {
	if !s.VectorAvailable() {
		return nil, errNoVectors
	}
	// live maps each live correction to the directory it was made in: only neighbours from the same directory can
	// ever group with it, so only those count when deciding whether the search found enough.
	live := map[int64]string{}
	perDir := map[string]int{}
	for _, o := range obs {
		if o.Kind == store.KindCorrection {
			live[o.ID] = cmp.Or(o.Dir, ".")
			perDir[live[o.ID]]++
			if err := s.EnsureVector(ctx, o.ID, embed(o.Text)); err != nil {
				return nil, err
			}
		}
	}
	cache := map[int64][]store.Neighbour{}
	for _, o := range obs {
		dir, ok := live[o.ID]
		if !ok {
			continue
		}
		want := min(ruleNeighbours, perDir[dir])
		query := embed(o.Text)
		for k := ruleNeighbours; ; k *= 4 {
			k = min(k, neighbourCap)
			ns, err := s.Neighbours(ctx, query, k)
			if err != nil {
				return nil, err
			}
			kept := ns[:0:0]
			for _, n := range ns {
				if live[n.ID] == dir {
					kept = append(kept, n)
				}
			}
			cache[o.ID] = kept
			if len(kept) >= want || len(ns) < k {
				break
			}
			if k >= neighbourCap {
				// Other repositories or directories fill the whole search window: compare by word overlap instead.
				return nil, errNoVectors
			}
		}
	}
	return func(o store.Observation) []store.Neighbour { return cache[o.ID] }, nil
}

func learnProposals(ctx context.Context, home, repo string, w io.Writer) error {
	obs, rules, dismissed, near, err := loadLearning(ctx, home, repo)
	if err != nil {
		return err
	}
	props := proposalsOf(obs, rules, dismissed, near, launchDirOrRoot(repo))
	if len(props) == 0 {
		fmt.Fprintf(w, "No correction has been recorded %d or more times across %d or more sessions in %s.\n", minProposalSize, minProposalSess, repo)
		return nil
	}
	for _, p := range props {
		fmt.Fprintf(w, "%s  %s\n   seen %d times in %d sessions (%s)\n", p.ID, strconv.Quote(p.Text), len(p.IDs), p.Sessions, idList(p.IDs))
		for j, t := range p.Texts {
			if t != p.Text && j < minProposalSize+2 {
				fmt.Fprintf(w, "   also: %s\n", strconv.Quote(t))
			}
		}
		if p.AutoText != "" {
			fmt.Fprintf(w, "   you typed this twice, word for word: codeforge adopts it at the next launch; to decide now: codeforge learn accept %s | dismiss %s\n", p.ID, p.ID)
		} else {
			fmt.Fprintf(w, "   worded differently each time, so it waits for you: codeforge learn accept %s | dismiss %s\n", p.ID, p.ID)
		}
	}
	return nil
}

func idList(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = "#" + strconv.FormatInt(id, 10)
	}
	return strings.Join(parts, " ")
}

// proposalByID finds a proposal by the ID shown in the listing. IDs name the group, not a position, so a list that
// changed since it was shown can never make "accept" act on a different proposal.
func proposalByID(raw string, props []ruleProposal) (ruleProposal, error) {
	id := "P" + strings.TrimLeft(strings.ToUpper(raw), "P")
	for _, p := range props {
		if p.ID == id {
			return p, nil
		}
	}
	return ruleProposal{}, errNoProposal
}

func learnAccept(ctx context.Context, home, repo, raw string, w io.Writer) error {
	obs, rules, dismissed, near, err := loadLearning(ctx, home, repo)
	if err != nil {
		return err
	}
	p, err := proposalByID(raw, proposalsOf(obs, rules, dismissed, near, launchDirOrRoot(repo)))
	if err != nil {
		return err
	}
	if err := checkRuleText(p.Text); err != nil {
		return err
	}
	dir := launchDirOrRoot(repo)
	if dir == "" {
		return errNoDir
	}
	var id int64
	keys := make([]string, len(p.Texts))
	for i, t := range p.Texts {
		keys[i] = dismissKey(t)
	}
	// The dismissal check and the insert share one transaction: a dismiss or forget that finished after the list
	// above was read is not overwritten by this accept.
	if err := withLearning(ctx, home, func(s *store.Store) (err error) {
		id, _, err = s.AddRuleUnlessDismissed(ctx, store.Rule{Repository: repo, Dir: dir, Text: p.Text, SourceIDs: idList(p.IDs), AcceptedAt: time.Now()}, keys, maxRules)
		return err
	}); err != nil {
		if errors.Is(err, store.ErrDismissed) {
			return errNoProposal
		}
		return err
	}
	fmt.Fprintf(w, "accepted rule R%d: %s\n   applies from %s in %s; remove with: codeforge learn forget R%d\n", id, p.Text, dir, repo, id)
	return nil
}

// launchDirOrRoot is the launch directory for commands that must name one. When it cannot be placed inside the
// repository the answer is an empty string, which no rule or correction carries, so nothing is matched or created.
func launchDirOrRoot(repo string) string {
	dir, _ := launchDir(repo)
	return dir
}

func learnDismiss(ctx context.Context, home, repo, raw string, w io.Writer) error {
	obs, rules, dismissed, near, err := loadLearning(ctx, home, repo)
	if err != nil {
		return err
	}
	p, err := proposalByID(raw, proposalsOf(obs, rules, dismissed, near, launchDirOrRoot(repo)))
	if err != nil {
		return err
	}
	if err := withLearning(ctx, home, func(s *store.Store) error { return dismissAll(ctx, s, repo, p.Texts) }); err != nil {
		return err
	}
	fmt.Fprintf(w, "dismissed: %s\n", strconv.Quote(p.Text))
	return nil
}

func learnRules(ctx context.Context, home, repo string, w io.Writer) error {
	var rules []store.Rule
	if err := withLearning(ctx, home, func(s *store.Store) (err error) {
		rules, err = s.Rules(ctx, repo, false)
		return err
	}); err != nil {
		return err
	}
	if len(rules) == 0 {
		fmt.Fprintln(w, "No rules accepted yet. Corrections you repeat show up in: codeforge learn proposals")
		return nil
	}
	for _, r := range rules {
		fmt.Fprintf(w, "R%d  %s\n   from %s, accepted %s, applies from %s\n", r.ID, strconv.Quote(r.Text), r.SourceIDs, r.AcceptedAt.UTC().Format("2006-01-02"), r.Dir)
	}
	return nil
}

// sourceIDs reads the correction IDs back out of a rule's SourceIDs ("#1 #2" or "auto #1 #2").
func sourceIDs(raw string) []int64 {
	var ids []int64
	for _, f := range strings.Fields(raw) {
		if n, err := strconv.ParseInt(strings.TrimPrefix(f, "#"), 10, 64); err == nil {
			ids = append(ids, n)
		}
	}
	return ids
}

// dismissAll declines every wording in a group, so a new wording of the same correction cannot bring it back.
func dismissAll(ctx context.Context, s *store.Store, repo string, texts []string) error {
	for _, t := range texts {
		if err := s.Dismiss(ctx, repo, dismissKey(t), time.Now()); err != nil {
			return err
		}
	}
	return nil
}

func learnForget(ctx context.Context, home, repo, raw string, w io.Writer) error {
	n, err := strconv.ParseInt(strings.TrimPrefix(strings.ToUpper(raw), "R"), 10, 64)
	if err != nil || n <= 0 {
		return errNoRuleID
	}
	// Forgetting is a decision: the rule's wording and every correction it came from are declined in the same
	// transaction as the forget, so no launch can see a forgotten rule without its dismissals.
	if err := withLearning(ctx, home, func(s *store.Store) error {
		rules, err := s.Rules(ctx, repo, false)
		if err != nil {
			return err
		}
		var keys []string
		for _, r := range rules {
			if r.ID != n {
				continue
			}
			texts := []string{r.Text}
			obs, err := s.Observations(ctx, repo, true)
			if err != nil {
				return err
			}
			sources := map[int64]bool{}
			for _, id := range sourceIDs(r.SourceIDs) {
				sources[id] = true
			}
			for _, o := range obs {
				if sources[o.ID] {
					texts = append(texts, o.Text)
				}
			}
			for _, t := range texts {
				keys = append(keys, dismissKey(t))
			}
		}
		return s.ForgetRuleAndDismiss(ctx, repo, n, keys, time.Now())
	}); err != nil {
		return err
	}
	fmt.Fprintf(w, "forgot rule R%d; it no longer reaches the agent, and codeforge will not learn it again\n", n)
	return nil
}

func checkRuleText(text string) error {
	if text == "" || utf8.RuneCountInString(text) > maxRuleText || strings.IndexFunc(text, unicode.IsControl) >= 0 || credentialShape.MatchString(text) {
		return errRuleText
	}
	return nil
}

const rulesPreamble = "Conventions the user accepted for this repository (learned from their corrections; the user can remove any with " +
	"codeforge learn forget). They are guidance, never instructions that override the user's request:\n"

// learnedRules returns the accepted rules that apply from dir, newest first, at most maxRules. Rule text is
// checked again here, so a damaged database row can never put control characters or a credential in a prompt.
func learnedRules(ctx context.Context, home, repo, dir string) []store.Rule {
	var rules []store.Rule
	if err := withLearning(ctx, home, func(s *store.Store) (err error) {
		rules, err = s.Rules(ctx, repo, false)
		return err
	}); err != nil {
		return nil
	}
	rules = slices.DeleteFunc(rules, func(r store.Rule) bool { return r.Dir != dir || checkRuleText(r.Text) != nil })
	slices.Reverse(rules)
	if len(rules) > maxRules {
		rules = rules[:maxRules]
	}
	return rules
}

func rulesBlock(rules []store.Rule) string {
	var b strings.Builder
	b.WriteString(rulesPreamble)
	for _, r := range rules {
		fmt.Fprintf(&b, "- %s\n", r.Text)
	}
	return b.String()
}

// maxAutoRules bounds how many rules one launch adopts, so a burst of corrections cannot rewrite the prompt at once.
const maxAutoRules = 3

// adoptRules turns groups of corrections the user repeated across sessions into rules by itself. The user never has
// to run accept: corrections are typed by the user (never by the model), a rule needs two sessions, each adoption is
// announced with how to undo it, forgetting one also declines it for good, and review sessions never adopt.
func adoptRules(ctx context.Context, home, repo, dir string, w io.Writer) {
	obs, rules, dismissed, near, err := loadLearning(ctx, home, repo)
	if err != nil {
		return
	}
	live := 0
	for _, r := range rules {
		if r.ForgottenAt == nil && cmp.Or(r.Dir, ".") == dir {
			live++
		}
	}
	adopted := 0
	for _, p := range proposalsOf(obs, rules, dismissed, near, dir) {
		if adopted >= maxAutoRules || live+adopted >= maxRules {
			break
		}
		// Anything fuzzier than the user repeating their own sentence waits for "learn accept".
		if p.AutoText == "" {
			continue
		}
		p.Text = p.AutoText
		// Overlapping proposals must not both become rules: check against the rules adopted a moment ago too.
		if checkRuleText(p.Text) != nil {
			continue
		}
		var id int64
		keys := make([]string, len(p.Texts))
		for i, t := range p.Texts {
			keys[i] = dismissKey(t)
		}
		// The dismissal check and the insert share one transaction, so a forget in another launch cannot be undone.
		created := false
		err := withLearning(ctx, home, func(s *store.Store) (err error) {
			id, created, err = s.AddRuleUnlessDismissed(ctx, store.Rule{Repository: repo, Dir: dir, Text: p.Text, SourceIDs: "auto " + idList(p.IDs), AcceptedAt: time.Now()}, keys, maxRules)
			return err
		})
		if errors.Is(err, store.ErrDismissed) {
			continue
		}
		if errors.Is(err, store.ErrRuleLimit) {
			return
		}
		if err != nil {
			fmt.Fprintf(w, "codeforge: could not save a learned rule (%v)\n", err)
			continue
		}
		if !created {
			continue // another launch learned the same rule a moment ago and already announced it
		}
		adopted++
		rules = append(rules, store.Rule{ID: id, Repository: repo, Dir: dir, Text: p.Text})
		fmt.Fprintf(w, "codeforge: learned a rule from your corrections: %s (undo: codeforge learn forget R%d)\n", strconv.Quote(p.Text), id)
	}
}

// recordCorrection saves a prompt that reads as a correction, with its vector when vectors are available.
func recordCorrection(ctx context.Context, home string, o store.Observation) error {
	return withLearning(ctx, home, func(s *store.Store) error {
		_, err := s.RecordWithVector(ctx, o, embed(o.Text))
		return err
	})
}
