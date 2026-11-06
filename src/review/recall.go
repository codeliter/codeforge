package review

import (
	"path/filepath"
	"strings"
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
// lines or the finding's own words naming it.
func (d Defect) Matches(f Finding) bool {
	if f.File == "" || filepath.ToSlash(filepath.Clean(f.File)) != d.File {
		return false
	}
	if f.Line >= d.Lo-lineSlack && f.Line <= d.Hi+lineSlack {
		return true
	}
	what := strings.ToLower(f.What)
	for _, k := range d.Keywords {
		if strings.Contains(what, strings.ToLower(k)) {
			return true
		}
	}
	return false
}

// Score is the outcome of one review against one case's seeded defects.
type Score struct {
	Found    []string
	Missed   []string
	Spurious int
}

// ScoreBlocking compares a verdict's blocking findings with the seeded defects. A finding that matches no defect is
// spurious; on a clean case every blocking finding is.
func ScoreBlocking(defects []Defect, blocking []Finding) Score {
	var s Score
	matched := make([]bool, len(blocking))
	for _, d := range defects {
		hit := false
		for i, f := range blocking {
			if d.Matches(f) {
				hit, matched[i] = true, true
			}
		}
		if hit {
			s.Found = append(s.Found, d.Name)
		} else {
			s.Missed = append(s.Missed, d.Name)
		}
	}
	for _, m := range matched {
		if !m {
			s.Spurious++
		}
	}
	return s
}
