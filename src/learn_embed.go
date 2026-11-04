package src

import (
	"hash/fnv"
	"math"
	"slices"
	"strings"
	"unicode"

	"github.com/codeliter/codeforge/src/store"
)

const (
	trigramSize     = 3
	trigramWeight   = 0.35 // whole words matter more than their letter pieces
	minWordStemLen  = 4
	embedSignBit    = 1 << 63
	ruleSimilarity  = 0.75
	ruleNeighbours  = 8
	jaccardFallback = 0.5
)

var stopWords = map[string]bool{
	"a": true, "an": true, "the": true, "and": true, "or": true, "to": true, "of": true, "in": true, "on": true, "for": true,
	"is": true, "it": true, "this": true, "that": true, "please": true, "you": true, "your": true, "my": true, "me": true,
	"we": true, "be": true, "as": true, "at": true, "do": true, "does": true, "did": true, "with": true, "so": true, "make": true,
	// Words that open a correction say that it is one, not what it says.
	"use": true, "prefer": true, "always": true, "should": true, "must": true, "instead": true, "actually": true, "no": true, "stop": true,
}

// words lowercases text, drops punctuation and stop words, and stems each word. Equal ideas written in different
// forms ("tests", "testing", "tested") reach the same stem.
func words(text string) []string {
	fields := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if !stopWords[f] {
			out = append(out, stem(f))
		}
	}
	return out
}

func stem(w string) string {
	for _, suffix := range []string{"ing", "ed", "es", "s"} {
		if len(w) >= len(suffix)+minWordStemLen-1 && strings.HasSuffix(w, suffix) {
			return strings.TrimSuffix(w, suffix)
		}
	}
	return w
}

// embed turns text into a unit vector of store.VectorDims numbers by feature hashing: each word stem and each
// character trigram of it adds to a bucket chosen by its hash. It is local, deterministic and free, and it groups
// by shared wording, not by meaning. A provider embedding model could replace it behind the same signature.
func embed(text string) []float32 {
	v := make([]float32, store.VectorDims)
	add := func(feature string, weight float32) {
		h := fnv.New64a()
		_, _ = h.Write([]byte(feature)) //nolint:errcheck // hash writes never fail
		sum := h.Sum64()
		sign := weight
		if sum&embedSignBit != 0 {
			sign = -weight
		}
		v[sum%store.VectorDims] += sign
	}
	for _, w := range words(text) {
		add("w:"+w, 1)
		padded := "^" + w + "$"
		for i := 0; i+trigramSize <= len(padded); i++ {
			add("t:"+padded[i:i+trigramSize], trigramWeight)
		}
	}
	var norm float64
	for _, x := range v {
		norm += float64(x) * float64(x)
	}
	if norm == 0 {
		return v
	}
	scale := float32(1 / math.Sqrt(norm))
	for i := range v {
		v[i] *= scale
	}
	return v
}

// cosine is the cosine similarity of two unit vectors.
func cosine(a, b []float32) float64 {
	var dot float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
	}
	return dot
}

// similarFromDistance converts the Euclidean distance between unit vectors to cosine similarity.
func similarFromDistance(d float64) float64 { return 1 - d*d/2 } //nolint:mnd // identity for unit vectors

// "stop", "avoid" and the like are negations too; a pair like "stop running X" and "do not stop running X" counts them.
var negations = map[string]bool{
	"not": true, "never": true, "dont": true, "don": true, "stop": true, "avoid": true, "no": true, "without": true,
	"shouldn": true, "mustn": true, "cannot": true, "cant": true, "wont": true, "won": true, "nor": true,
}

// stance is what a correction says about polarity: whether it forbids something, what it affirms before the
// negation and what it forbids after it. "prefer unit tests, not integration tests" and "prefer integration tests,
// not unit tests" share every word but swap the two, so they must never be treated as one rule.
type stance struct {
	negative  bool
	negations int // "stop X" and "don't stop X" both contain a negation; a second one flips or muddies the meaning
	before    []string
	after     []string
}

func stanceOf(text string) stance {
	lower := strings.ToLower(strings.TrimSpace(text))
	for _, lead := range []string{"no,", "actually,"} {
		lower = strings.TrimSpace(strings.TrimPrefix(lower, lead))
	}
	// "X instead of Y" and "X rather than Y" forbid Y just as "X, not Y" does.
	for _, replacement := range []string{" instead of ", " rather than ", " over ", " versus ", " vs ", " vs. ", " in preference to ", " before "} {
		lower = strings.ReplaceAll(lower, replacement, " not ")
	}
	fields := strings.FieldsFunc(lower, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	first := -1
	count := 0
	for i, f := range fields {
		if negations[f] {
			if first < 0 {
				first = i
			}
			count++
		}
	}
	if first < 0 {
		return stance{}
	}
	return stance{negative: true, negations: count, before: words(strings.Join(fields[:first], " ")), after: words(strings.Join(fields[first+1:], " "))}
}

const targetOverlap = 0.6

// sameOrder reports whether the words two lists share occur in the same order in both, by first occurrence.
func sameOrder(a, b []string) bool {
	inB := map[string]bool{}
	for _, w := range b {
		inB[w] = true
	}
	inA := map[string]bool{}
	for _, w := range a {
		inA[w] = true
	}
	order := func(list []string, other map[string]bool) []string {
		seen := map[string]bool{}
		var out []string
		for _, w := range list {
			if other[w] && !seen[w] {
				seen[w] = true
				out = append(out, w)
			}
		}
		return out
	}
	return slices.Equal(order(a, inB), order(b, inA))
}

// overlap is the word overlap of two targets; two empty targets are identical.
func overlap(a, b []string) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 1
	}
	return jaccardWords(a, b)
}

// compatible reports whether two corrections can be the same rule: both allow, or both forbid the same thing
// while affirming the same thing. A pair whose targets are swapped ("unit tests, not integration tests" against
// "integration tests, not unit tests") is rejected before any fuzzy comparison, because such a pair matches closely
// on both sides. When in doubt it says no: two rules cost less than one wrong merge.
func compatible(a, b string) bool {
	// Same words in a different order can say the opposite ("run lint after tests" against "run tests after lint"),
	// and a bag of words cannot tell them apart, so the words two corrections share must come in the same order.
	if !sameOrder(words(a), words(b)) {
		return false
	}
	x, y := stanceOf(a), stanceOf(b)
	if x.negative != y.negative {
		return false
	}
	if !x.negative {
		return true
	}
	// Different numbers of negations ("stop X" against "please don't stop X") can mean opposite things, and several
	// negations in one sentence are too ambiguous to compare: refuse both.
	if x.negations != y.negations || x.negations > 1 {
		return false
	}
	direct1, direct2 := overlap(x.before, y.before), overlap(x.after, y.after)
	cross1, cross2 := overlap(x.before, y.after), overlap(x.after, y.before)
	if direct1 < targetOverlap || direct2 < targetOverlap {
		return false
	}
	return cross1 < direct1 && cross2 < direct2 || len(x.before) == 0 && len(y.before) == 0 && cross2 < direct2
}

// dismissKey names one sentence the user declined. It is the sentence itself, not a stemmed bag of words, so
// declining "use X" never hides "do not use X" or "retries > 3" when "retries < 3" was declined.
func dismissKey(text string) string { return "s:" + sentenceKey(text) }

// negative reports whether a correction forbids something. "no," or "actually," at the start only introduces a
// correction ("No, use table tests"), so it is not counted. Opposite corrections must never be grouped or treated
// as covering each other, even though they share most words.
func negative(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	for _, lead := range []string{"no,", "actually,"} {
		lower = strings.TrimSpace(strings.TrimPrefix(lower, lead))
	}
	for _, f := range strings.FieldsFunc(lower, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if negations[f] {
			return true
		}
	}
	return false
}

// jaccard is the overlap of two texts' stemmed word sets.
func jaccard(a, b string) float64 { return jaccardWords(words(a), words(b)) }

// jaccardWords is the overlap of two lists of word stems.
func jaccardWords(a, b []string) float64 {
	set := func(list []string) map[string]bool {
		m := map[string]bool{}
		for _, w := range list {
			m[w] = true
		}
		return m
	}
	x, y := set(a), set(b)
	if len(x) == 0 || len(y) == 0 {
		return 0
	}
	shared := 0
	for w := range x {
		if y[w] {
			shared++
		}
	}
	return float64(shared) / float64(len(x)+len(y)-shared)
}
