package effort

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/codeliter/codeforge/assets"
)

type Level int

const (
	Low Level = iota
	Medium
	High
)

const (
	maxScore       = 2
	redFlagTotal   = 5
	highTotal      = 7
	lowTotal       = 2
	defaultTimeout = 30 * time.Second
	maxRawShown    = 120
)

func (l Level) String() string {
	return [...]string{"low", "medium", "high"}[l]
}

type Scores struct {
	Ambiguity int    `json:"ambiguity"`
	Blast     int    `json:"blast"`
	Risk      int    `json:"risk"`
	Novelty   int    `json:"novelty"`
	Verify    int    `json:"verify"`
	Reason    string `json:"reason"`
}

func (s Scores) total() int {
	return s.Ambiguity + s.Blast + s.Risk + s.Novelty + s.Verify
}

func (s Scores) valid() bool {
	for _, v := range []int{s.Ambiguity, s.Blast, s.Risk, s.Novelty, s.Verify} {
		if v < 0 || v > maxScore {
			return false
		}
	}
	return true
}

// Decide maps rubric scores to the cheapest sufficient level; any red flag goes straight to High.
func Decide(s Scores) Level {
	switch {
	case s.Risk == maxScore, s.Ambiguity == maxScore, s.Verify == maxScore && s.total() >= redFlagTotal, s.total() >= highTotal:
		return High
	case s.total() <= lowTotal:
		return Low
	default:
		return Medium
	}
}

type Signals struct {
	ChangedFiles int
	DiffLines    int
	Branch       string
}

// Judge sends the system prompt and the user message to a cheap model and returns its raw answer.
type Judge interface {
	Ask(ctx context.Context, system, user string) (string, error)
}

type Options struct {
	Judges  []Judge
	Timeout time.Duration
}

type Classifier struct {
	judges  []Judge
	timeout time.Duration
}

var (
	errNoJudge      = errors.New("effort: at least one judge is required")
	errNoJSON       = errors.New("judge returned no JSON")
	errScoreOutside = errors.New("judge score out of range")
)

func New(opts Options) (*Classifier, error) {
	if len(opts.Judges) == 0 {
		return nil, errNoJudge
	}
	if opts.Timeout == 0 {
		opts.Timeout = defaultTimeout
	}
	return &Classifier{judges: opts.Judges, timeout: opts.Timeout}, nil
}

type Result struct {
	Level  Level
	Scores Scores
	Votes  []Scores
}

// Classify asks every judge in parallel and merges the votes; with no usable vote it fails safe to Medium.
func (c *Classifier) Classify(ctx context.Context, task string, sig Signals) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	msg := userMessage(task, sig)
	votes := make([]Scores, len(c.judges))
	errs := make([]error, len(c.judges))

	var wg sync.WaitGroup
	for i, j := range c.judges {
		wg.Go(func() {
			raw, err := j.Ask(ctx, assets.Rubric, msg)
			if err != nil {
				errs[i] = fmt.Errorf("asking judge %d: %w", i, err)
				return
			}
			votes[i], errs[i] = parseScores(raw)
		})
	}
	wg.Wait()

	var ok []Scores
	for i := range votes {
		if errs[i] == nil {
			ok = append(ok, votes[i])
		}
	}
	if len(ok) == 0 {
		return Result{Level: Medium}, errors.Join(errs...)
	}

	merged := Merge(ok)
	return Result{Level: Decide(merged), Scores: merged, Votes: ok}, nil
}

// Merge takes the highest risk any judge saw and the rounded mean of everything else.
func Merge(votes []Scores) Scores {
	var m Scores
	var reasons []string
	for _, v := range votes {
		m.Ambiguity += v.Ambiguity
		m.Blast += v.Blast
		m.Novelty += v.Novelty
		m.Verify += v.Verify
		m.Risk = max(m.Risk, v.Risk)
		if v.Reason != "" {
			reasons = append(reasons, v.Reason)
		}
	}

	n := len(votes)
	m.Ambiguity = roundDiv(m.Ambiguity, n)
	m.Blast = roundDiv(m.Blast, n)
	m.Novelty = roundDiv(m.Novelty, n)
	m.Verify = roundDiv(m.Verify, n)
	m.Reason = strings.Join(reasons, " | ")
	return m
}

func roundDiv(sum, n int) int {
	return (sum + n/2) / n
}

func userMessage(task string, sig Signals) string {
	return fmt.Sprintf("<task>\n%s\n</task>\n<signals changed_files=%d diff_lines=%d branch=%q/>",
		task, sig.ChangedFiles, sig.DiffLines, sig.Branch)
}

func parseScores(raw string) (Scores, error) {
	start, end := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return Scores{}, fmt.Errorf("%w: %q", errNoJSON, truncate(raw, maxRawShown))
	}

	var s Scores
	if err := json.Unmarshal([]byte(raw[start:end+1]), &s); err != nil {
		return Scores{}, fmt.Errorf("decoding judge scores: %w", err)
	}
	if !s.valid() {
		return Scores{}, fmt.Errorf("%w: %+v", errScoreOutside, s)
	}
	return s, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
