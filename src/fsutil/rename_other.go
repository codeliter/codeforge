//go:build !darwin && !linux

package fsutil

import "syscall"

func renameExclusive(_, _ string) error {
	return syscall.ENOTSUP
}
