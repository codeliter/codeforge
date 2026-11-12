package src

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/codeliter/codeforge/src/forms"
	"github.com/codeliter/codeforge/src/review"
)

// checkLeads has the reviewer, in its own session, check the unconfirmed blocking leads another reviewer found against
// the code, and rewrite the ones that stand as the user would leave them: short, plain and in the persona's voice, one
// line each, every lead kept at its place. A juror's word alone is not a verdict, so a lead the reviewer rules out from
// the code, with its reason, is left off and said on the terminal. Every lead is kept as it was when the turn fails or
// its answer does not account for every lead, so a blocker is never lost to a rewrite.
func checkLeads(ctx context.Context, leads string, turn func(context.Context, string) (string, error)) string {
	header, body, found := strings.Cut(leads, review.LeadsHeader)
	if !found || strings.TrimSpace(header) != "" {
		return leads
	}
	want := leadPlaces(body)
	if len(want) == 0 || len(want) != nonEmptyLines(body) {
		return leads // a lead in a shape that cannot be checked after a rewrite is posted as it was written
	}
	// Whatever the turn gives, no lead is posted longer than its plain form: what breaks and the fix, without the
	// walkthrough.
	var compact []string
	for _, l := range reviewLines(body) {
		if l = strings.TrimSpace(l); l != "" {
			compact = append(compact, compactLead(l))
		}
	}
	fallback := review.LeadsHeader + strings.Join(compact, "\n") + "\n"
	if len(slices.Compact(slices.Sorted(slices.Values(want)))) != len(want) {
		// Two leads at one place cannot be told apart in the answer, so ruling out one could drop the other unchecked.
		return fallback
	}
	reply, err := turn(ctx, leadsPrompt(body))
	if err != nil {
		fmt.Fprintln(os.Stderr, "codeforge: the leads could not be checked ("+err.Error()+"); posting them shortened.")
		return fallback
	}
	kept, ruledOut, ok := parseCheckedLeads(reply, want)
	if !ok {
		return fallback
	}
	var lines []string
	for i, place := range want {
		if why, out := ruledOut[place]; out {
			fmt.Fprintf(os.Stderr, "codeforge: left out the lead at %s, which the code rules out: %s\n", place, why)
			continue
		}
		// A rewrite that says next to nothing has lost the lead, and one no shorter than the plain form gains nothing:
		// the plain form stands for it.
		l := kept[place]
		if n := len(strings.Fields(l)) - leadPrefixWords; n < minLeadWords || len(strings.Fields(l)) >= len(strings.Fields(compact[i])) && n > maxLeadWords {
			l = compact[i]
		}
		lines = append(lines, l)
	}
	if len(lines) == 0 {
		return ""
	}
	return review.LeadsHeader + strings.Join(lines, "\n") + "\n"
}

// ruledOutLine is a lead the reviewer ruled out: its place, then why the code shows it does not break.
var ruledOutLine = regexp.MustCompile("^- RULED OUT `([^`]+:\\d+)` (\\S.*)$")

// parseCheckedLeads reads the reviewer's answer on the leads: each place in want either rewritten or ruled out with a
// reason, exactly once, the rewritten ones in their order. Anything else around those lines is ignored. It reports false
// when the answer does not account for every lead, or names a place that is not one.
func parseCheckedLeads(reply string, want []string) (kept, ruledOut map[string]string, ok bool) {
	kept, ruledOut = map[string]string{}, map[string]string{}
	var order []string
	for _, l := range reviewLines(reply) {
		l = strings.TrimSpace(l)
		if m := ruledOutLine.FindStringSubmatch(l); m != nil {
			if _, dup := ruledOut[m[1]]; dup || !slices.Contains(want, m[1]) {
				return nil, nil, false
			}
			ruledOut[m[1]] = m[2]
			continue
		}
		if m := leadLine.FindStringSubmatch(l); m != nil {
			if _, dup := kept[m[1]]; dup || !slices.Contains(want, m[1]) {
				return nil, nil, false
			}
			kept[m[1]] = l
			order = append(order, m[1])
		}
	}
	if len(kept)+len(ruledOut) != len(want) {
		return nil, nil, false
	}
	var standing []string
	for _, place := range want {
		_, k := kept[place]
		_, r := ruledOut[place]
		if k == r {
			return nil, nil, false
		}
		if k {
			standing = append(standing, place)
		}
	}
	return kept, ruledOut, slices.Equal(order, standing)
}

// compactLead is a lead line cut to what a reader needs: its place, the sentence that says what breaks, and the
// sentence that says the fix. Examples and walkthroughs between them go; a lead with nothing to cut stays as written.
func compactLead(line string) string {
	m := leadLine.FindStringSubmatch(line)
	if m == nil || len(strings.Fields(line)) <= maxLeadWords {
		return line
	}
	prefix := "- `" + m[1] + "` "
	sentences := splitSentences(strings.TrimPrefix(line, prefix))
	if len(sentences) == 1 {
		return line // one sentence: nothing between what breaks and the fix to cut
	}
	// The fix is the sentence that says so, or else the last one: a reviewer ends a lead with what to do.
	fix := sentences[len(sentences)-1]
	for _, s := range sentences[1:] {
		if fixLead.MatchString(s) {
			fix = s
			break
		}
	}
	kept := []string{sentences[0], fixLead.ReplaceAllString(fix, "")}
	for i, s := range kept {
		s = strings.TrimSpace(s)
		if !strings.HasSuffix(s, ".") {
			s += "."
		}
		r, w := utf8.DecodeRuneInString(s)
		kept[i] = string(unicode.ToUpper(r)) + s[w:]
	}
	return prefix + strings.Join(kept, " ")
}

// splitSentences splits prose into its sentences at each sentenceBreak, keeping every character but the space between.
// A dot after an abbreviation or a single letter ("e.g.", "vs.", "A.") ends no sentence.
func splitSentences(text string) []string {
	var out []string
	start := 0
	for _, m := range sentenceBreak.FindAllStringIndex(text, -1) {
		words := strings.Fields(text[start : m[0]+1])
		if len(words) == 0 {
			continue
		}
		last := strings.ToLower(strings.Trim(words[len(words)-1], "(`"))
		if abbreviations[last] || len([]rune(strings.TrimSuffix(last, "."))) <= 1 {
			continue
		}
		out = append(out, text[start:m[0]+1])
		start = m[1] - 1 // the letter or backtick that opens the next sentence
	}
	return append(out, text[start:])
}

// abbreviations end with a dot that ends no sentence.
var abbreviations = map[string]bool{"e.g.": true, "i.e.": true, "vs.": true, "etc.": true, "fig.": true, "cf.": true, "approx.": true, "no.": true}

var (
	// sentenceBreak splits prose at a full stop that ends a sentence: one followed by a space and a capital or a
	// backtick, so a dot inside code or a number stays.
	sentenceBreak = regexp.MustCompile(`\.\s+(?:[A-Za-z\x60])`)
	// fixLead opens the sentence that says the fix.
	fixLead = regexp.MustCompile(`(?i)^\s*(fix|to fix|the fix)\s*(is)?\s*:\s*`)
)

// nonEmptyLines counts the lines of text that hold anything.
func nonEmptyLines(text string) int {
	n := 0
	for _, l := range reviewLines(text) {
		if strings.TrimSpace(l) != "" {
			n++
		}
	}
	return n
}

// leadLine is one lead as listed: a list item that opens with its place in backticks.
var leadLine = regexp.MustCompile("^- `([^`]+:\\d+)` \\S")

// leadPlaces lists the places of the leads in text, in order.
func leadPlaces(text string) []string {
	var places []string
	for _, l := range reviewLines(text) {
		if m := leadLine.FindStringSubmatch(strings.TrimSpace(l)); m != nil {
			places = append(places, m[1])
		}
	}
	return places
}

// maxLeadWords bounds one rewritten lead: a reader takes it in at a glance. minLeadWords is the fewest that can still
// say what breaks and what to do.
const (
	maxLeadWords = 45
	minLeadWords = 5
	// leadPrefixWords are the list marker and the place a lead's line opens with.
	leadPrefixWords = 2
)

// leadsPrompt asks the reviewer, in its own session, to check the leads against the code and put the ones that stand in
// the user's words.
func leadsPrompt(leads string) string {
	return "Another check of this change found the possible blockers below, which your review does not make. " +
		"Before anything goes on the PR, open the file at each place and read the code around it. " +
		"Only when the code you read shows it does not break, answer with a line that starts with - RULED OUT and the " +
		"place exactly as given, then why, citing that code in one sentence, for example: - RULED OUT `a.go:3` the body is " +
		"buffered in send() before the retry. When you cannot tell, keep the lead. " +
		"Otherwise it goes on the PR under your review, so rewrite it as I would leave it, in my voice as codeforge-persona " +
		"describes it (codeforge-voice where it is silent): what breaks, then what to do, in plain words, " +
		fmt.Sprintf("at most two sentences and %d words, with no hedging, no walkthrough and no mention of models, tools or ", maxLeadWords) +
		"a second check. Answer for every lead, in the same order, each on one line that starts with its place exactly as " +
		"given (`- `path:line` text`). Reply with those lines and nothing else.\n\n" + leads
}

// isBlocking reports whether a finding's label blocks the merge, as ParseDecision reads it.
func isBlocking(label string) bool {
	l := strings.ToLower(label)
	return strings.Contains(l, "blocking") && !strings.Contains(l, "non-blocking")
}

// leadNearby is how many lines from a finding a lead may sit and still be the same point.
const leadNearby = 5

// dropCoveredLeads leaves out the leads the review already makes as a finding of its own: one in the same file within
// leadNearby lines. Posted twice, the same blocker reads as two. The rest keep their order; with none left, no leads.
func dropCoveredLeads(leads, text string) string {
	header, body, found := strings.Cut(leads, review.LeadsHeader)
	if !found || strings.TrimSpace(header) != "" {
		return leads
	}
	spans := findingSpans(reviewLines(text))
	var kept []string
	for _, l := range reviewLines(body) {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		m := leadLine.FindStringSubmatch(l)
		if m == nil {
			kept = append(kept, l) // a lead it cannot place is never dropped
			continue
		}
		path, line, _ := strings.Cut(m[1], ":")
		n, err := strconv.Atoi(line)
		// Only a blocking finding covers a lead: a nit nearby says nothing about the blocker.
		covered := err == nil && slices.ContainsFunc(spans, func(s findingSpan) bool {
			return isBlocking(s.F.Label) && s.F.Path == path && s.F.Line >= n-leadNearby && s.F.Line <= n+leadNearby
		})
		if !covered {
			kept = append(kept, l)
		}
	}
	if len(kept) == 0 {
		return ""
	}
	return review.LeadsHeader + strings.Join(kept, "\n") + "\n"
}

// The most words a posted comment may have before the reviewer is asked to cut it: the instructions ask for about 40
// per finding and 80 for the final comment, and these leave room for a needed example.
const (
	maxFindingWords = 60
	maxFinalWords   = 120
	// What a cut asks for: the instructions' own targets.
	targetFindingWords = 40
	targetFinalWords   = 80
)

// leadID names a lead by its text, so the same lead keeps its id however the list around it changes.
func leadID(line string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(line)))
	return leadIDPrefix + hex.EncodeToString(sum[:6])
}

// leadLabel names a lead by its place, for the view's key help and a question about it.
func leadLabel(line string) string {
	if m := leadLine.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
		return m[1]
	}
	return "this lead"
}

// splitLeads splits the leads into the words that open them and one line per lead.
func splitLeads(leads string) (header string, items []string) {
	var head []string
	for _, l := range reviewLines(leads) {
		switch t := strings.TrimSpace(l); {
		case strings.HasPrefix(t, "- "):
			items = append(items, t)
		case len(items) == 0:
			head = append(head, l)
		case t != "":
			items[len(items)-1] += " " + t // a lead that runs onto a second line
		}
	}
	return strings.TrimSpace(strings.Join(head, "\n")), items
}

// withoutDeletedLeads drops the leads the user deleted in the view; with none left, there are no leads.
func withoutDeletedLeads(leads string, changes []forms.Change) string {
	gone := map[string]bool{}
	for _, c := range changes {
		if c.Kind == forms.ChangeDelete && strings.HasPrefix(c.Note, leadIDPrefix) {
			gone[c.Note] = true
		}
	}
	if len(gone) == 0 || strings.TrimSpace(leads) == "" {
		return leads
	}
	header, items := splitLeads(leads)
	kept := slices.DeleteFunc(items, func(l string) bool { return gone[leadID(l)] })
	if len(kept) == 0 {
		return ""
	}
	return header + "\n\n" + strings.Join(kept, "\n") + "\n"
}
