package review

import (
	"path/filepath"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Defect is one seeded bug a reviewer is expected to report: where it is, and words a finding about it would use.
type Defect struct {
	Name     string
	File     string
	Lo, Hi   int
	Keywords []string
}

// lineSlack is how far from the seeded lines a finding may point and still count as the same defect.
const lineSlack = 3

// Matches reports whether a blocking finding is about this defect: the same file, and either a line near the seeded
// lines or the defect's own words naming it within keywordSpan lines.
func (d Defect) Matches(f Finding) bool {
	_, ok := d.fit(f)
	return ok
}

// keywordSpan is how far from the seeded lines a finding may point and still count through its words alone: a finding
// that names a defect says roughly where it is, and a word far away in the same file is not evidence.
const keywordSpan = 15

// minKeywordLetters is how many letters a keyword needs to count as evidence: operators and numbers such as && or -1
// turn up in unrelated findings about the same file.
const minKeywordLetters = 3

func meaningful(keyword string) bool {
	letters := 0
	for _, r := range keyword {
		if unicode.IsLetter(r) {
			letters++
		}
	}
	return letters >= minKeywordLetters
}

// exact ranks a finding that points at the seeded lines themselves above every other kind of fit: whatever words a
// neighbouring defect shares with it, the finding is about the code it points at.
func exact(distance int) int {
	if distance == 0 {
		return 1
	}
	return 0
}

// containsWord reports whether keyword appears in text as a whole word: a keyword that begins or ends with a letter or
// digit must not sit inside a longer word, so "cap" is not found in "escapes" nor "order" in "border".
func containsWord(text, keyword string) bool {
	isWord := func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' }
	first, _ := utf8.DecodeRuneInString(keyword)
	last, _ := utf8.DecodeLastRuneInString(keyword)
	for from := 0; ; {
		i := strings.Index(text[from:], keyword)
		if i < 0 {
			return false
		}
		start, end := from+i, from+i+len(keyword)
		before, _ := utf8.DecodeLastRuneInString(text[:start])
		after, _ := utf8.DecodeRuneInString(text[end:])
		glued := (isWord(first) && start > 0 && isWord(before)) || (isWord(last) && end < len(text) && isWord(after))
		if !glued {
			return true
		}
		from = start + 1
	}
}

// fit rates how well a finding names this defect, or reports that it does not. It must be in the same file, and either
// point near the seeded lines or use the defect's own words within keywordSpan lines of them. The rank orders fits:
// pointing at the seeded lines exactly first, then more of the defect's own words (so a finding that describes one
// defect is not taken for a neighbour it merely points next to), then a line fit before a words-only fit, then nearer
// before farther. Words are a heuristic: they can still tie a finding to a sibling defect that shares a generic one.
func (d Defect) fit(f Finding) (rank [4]int, ok bool) {
	if f.File == "" || filepath.ToSlash(filepath.Clean(f.File)) != d.File {
		return rank, false
	}
	distance := max(d.Lo-f.Line, f.Line-d.Hi, 0)
	what := strings.ToLower(f.What)
	hits := 0
	for _, k := range d.Keywords {
		if meaningful(k) && containsWord(what, strings.ToLower(k)) {
			hits++
		}
	}
	switch {
	case distance <= lineSlack:
		return [4]int{exact(distance), hits, 2, -distance}, true
	case hits > 0 && distance <= keywordSpan:
		return [4]int{0, hits, 1, -distance}, true
	}
	return rank, false
}

// Score is the outcome of one review against one case's seeded defects.
type Score struct {
	Found    []string
	Missed   []string
	Spurious int
}

// ScoreBlocking compares a verdict's blocking findings with the seeded defects. Each finding is credited to the one
// defect it fits best, so a single finding can never count as finding several defects that share a file, a line window
// or a generic word. A finding that fits no defect, or fits two equally well, is spurious: crediting the first of two
// equal fits would be a guess; on a clean case every blocking finding is spurious.
func ScoreBlocking(defects []Defect, blocking []Finding) Score {
	var s Score
	hit := make([]bool, len(defects))
	for _, f := range blocking {
		best, bestRank, tied := -1, [4]int{}, false
		for i, d := range defects {
			rank, ok := d.fit(f)
			switch {
			case !ok:
			case best < 0 || slices.Compare(rank[:], bestRank[:]) > 0:
				best, bestRank, tied = i, rank, false
			case slices.Compare(rank[:], bestRank[:]) == 0:
				tied = true
			}
		}
		if best < 0 || tied {
			s.Spurious++
			continue
		}
		hit[best] = true
	}
	for i, d := range defects {
		if hit[i] {
			s.Found = append(s.Found, d.Name)
		} else {
			s.Missed = append(s.Missed, d.Name)
		}
	}
	return s
}
