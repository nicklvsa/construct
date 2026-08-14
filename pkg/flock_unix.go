//go:build !windows

package pkg

import (
	"os"
	"syscall"
)

// tryFlock attempts a non-blocking exclusive lock on the open file.
func tryFlock(f *os.File) bool {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil
}

// unlockFlock releases the exclusive lock.
func unlockFlock(f *os.File) {
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
