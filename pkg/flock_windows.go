//go:build windows

package pkg

import (
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32          = syscall.NewLazyDLL("kernel32.dll")
	lockFileEx        = kernel32.NewProc("LockFileEx")
	unlockFileEx      = kernel32.NewProc("UnlockFileEx")
	lockfileExclusive = uint32(0x00000002)
	lockfileFailImmed = uint32(0x00000001)
)

// tryFlock attempts a non-blocking exclusive lock on the open file.
func tryFlock(f *os.File) bool {
	var overlapped [20]byte
	r1, _, _ := lockFileEx.Call(
		f.Fd(),
		uintptr(lockfileExclusive|lockfileFailImmed),
		0,
		1,
		1,
		uintptr(unsafe.Pointer(&overlapped[0])),
	)
	return r1 != 0
}

// unlockFlock releases the exclusive lock.
func unlockFlock(f *os.File) {
	var overlapped [20]byte
	_, _, _ = unlockFileEx.Call(
		f.Fd(),
		0,
		1,
		1,
		uintptr(unsafe.Pointer(&overlapped[0])),
	)
}
