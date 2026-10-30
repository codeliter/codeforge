package guard

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

const (
	stateFile   = "state.json"
	privateDir  = 0o700
	privateFile = 0o600

	// ApprovalTTL bounds a one-off approval so a forgotten `allow` cannot authorise a later session.
	ApprovalTTL = 10 * time.Minute
)

// State lives under ~/.codeforge, outside any repository, so the agent cannot approve itself through its own edits.
type State struct {
	Approvals map[string]Approval `json:"approvals"`
	Gates     map[string]Pass     `json:"gates"`
}

type Approval struct {
	Rule    string    `json:"rule"`
	Repo    string    `json:"repo"`
	Expires time.Time `json:"expires"`
}

// Pass records the exact tree the review gate approved; any later change invalidates it.
// Commit is set when the reviewed tree was clean, so git's pre-push hook can check the exact commit being pushed.
type Pass struct {
	Tree    string    `json:"tree"`
	Commit  string    `json:"commit,omitempty"`
	Summary string    `json:"summary"`
	At      time.Time `json:"at"`
}

func Load(dir string) (State, error) {
	st := State{Approvals: map[string]Approval{}, Gates: map[string]Pass{}}

	data, err := os.ReadFile(filepath.Join(dir, stateFile))
	if errors.Is(err, fs.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, fmt.Errorf("reading guard state: %w", err)
	}

	if err := json.Unmarshal(data, &st); err != nil {
		return st, fmt.Errorf("decoding guard state: %w", err)
	}
	if st.Approvals == nil {
		st.Approvals = map[string]Approval{}
	}
	if st.Gates == nil {
		st.Gates = map[string]Pass{}
	}
	return st, nil
}

func (st State) Save(dir string) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding guard state: %w", err)
	}

	if err := os.MkdirAll(dir, privateDir); err != nil {
		return fmt.Errorf("creating state dir: %w", err)
	}

	// A unique temp file per writer, because Empryo runs parallel tool calls and each one runs the hook.
	tmp, err := os.CreateTemp(dir, stateFile+".*")
	if err != nil {
		return fmt.Errorf("creating guard state: %w", err)
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("writing guard state: %w", err)
	}
	if err := tmp.Chmod(privateFile); err != nil {
		tmp.Close()
		return fmt.Errorf("securing guard state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing guard state: %w", err)
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, stateFile)); err != nil {
		return fmt.Errorf("saving guard state: %w", err)
	}
	return nil
}

func approvalKey(repo, rule string) string {
	return repo + "\x00" + rule
}

func (st State) Allow(repo, rule string, now time.Time) {
	st.Approvals[approvalKey(repo, rule)] = Approval{Rule: rule, Repo: repo, Expires: now.Add(ApprovalTTL)}
}

func (st State) Approved(repo, rule string, now time.Time) bool {
	a, ok := st.Approvals[approvalKey(repo, rule)]
	return ok && now.Before(a.Expires)
}

// Consume spends a one-off approval, so each allow covers exactly one action.
func (st State) Consume(repo, rule string, now time.Time) bool {
	ok := st.Approved(repo, rule, now)
	delete(st.Approvals, approvalKey(repo, rule))
	return ok
}

// PruneExpired reports whether it removed anything, so callers only write state when it changed.
func (st State) PruneExpired(now time.Time) bool {
	pruned := false
	for k, a := range st.Approvals {
		if !now.Before(a.Expires) {
			delete(st.Approvals, k)
			pruned = true
		}
	}
	return pruned
}

func (st State) PassGate(repo, tree, commit, summary string, now time.Time) {
	st.Gates[repo] = Pass{Tree: tree, Commit: commit, Summary: summary, At: now}
}

// CommitReviewed reports whether the gate passed for exactly this commit on a clean tree.
func (st State) CommitReviewed(repo, commit string) bool {
	p, ok := st.Gates[repo]
	return ok && p.Commit != "" && p.Commit == commit
}

func (st State) GatePassed(repo, tree string) bool {
	p, ok := st.Gates[repo]
	return ok && p.Tree == tree
}
