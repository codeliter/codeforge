package fsutil

import "fmt"

// RenameNoReplace moves a directory or file to a name nothing has yet, in one step: it fails, with fs.ErrExist, if
// anything is there, even an empty directory. Where the system has no such step it fails rather than fall back to
// one that could replace what another process put there.
func RenameNoReplace(from, to string) error {
	if err := renameExclusive(from, to); err != nil {
		return fmt.Errorf("moving %s into place: %w", to, err)
	}
	return nil
}
