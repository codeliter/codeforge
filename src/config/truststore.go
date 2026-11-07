package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/codeliter/codeforge/src/fsutil"
)

const trustFile = "trusted-personas.json"

// Store maps approved repository persona/model-config paths to content hashes.
// The legacy filename is retained for compatibility.
type Store map[string]string

func Digest(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func LoadTrust(home string) (Store, error) {
	ts := Store{}

	data, err := os.ReadFile(filepath.Join(Dir(home), trustFile))
	if errors.Is(err, fs.ErrNotExist) {
		return ts, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading trusted personas: %w", err)
	}

	if err := json.Unmarshal(data, &ts); err != nil {
		return nil, fmt.Errorf("decoding trusted personas: %w", err)
	}
	return ts, nil
}

func saveTrust(home string, ts Store) error {
	data, err := json.MarshalIndent(ts, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding trusted personas: %w", err)
	}

	if err := os.MkdirAll(Dir(home), fsutil.PrivateDir); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}
	if err := fsutil.AtomicWriteFile(filepath.Join(Dir(home), trustFile), append(data, '\n'), fsutil.PrivateFile); err != nil {
		return fmt.Errorf("writing trusted personas: %w", err)
	}
	return nil
}

func Trust(home, path, content string) error {
	ts, err := LoadTrust(home)
	if err != nil {
		return err
	}

	ts[path] = Digest(content)
	return saveTrust(home, ts)
}
