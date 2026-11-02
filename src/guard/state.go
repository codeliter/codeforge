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
)

// State lives under ~/.codeforge, outside any repository, so the agent cannot write it through its own edits.
// It records the last voluntary gate pass per repository; nothing reads it to allow or block an action.
type State struct {
	Gates map[string]Pass `json:"gates"`
}

// Pass records the exact tree and, on a clean tree, the commit the review gate approved.
type Pass struct {
	Tree    string    `json:"tree"`
	Commit  string    `json:"commit,omitempty"`
	Summary string    `json:"summary"`
	At      time.Time `json:"at"`
}

func Load(dir string) (State, error) {
	st := State{Gates: map[string]Pass{}}

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

func (st State) PassGate(repo, tree, commit, summary string, now time.Time) {
	st.Gates[repo] = Pass{Tree: tree, Commit: commit, Summary: summary, At: now}
}
