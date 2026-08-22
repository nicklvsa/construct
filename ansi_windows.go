//go:build windows

package main

import (
	"os"
	"sync"

	"golang.org/x/sys/windows"
)

var (
	ansiMu   sync.Mutex
	ansiSeen = map[uintptr]bool{} // per handle: stdout and stderr differ
)

func enableANSI(f *os.File) bool {
	ansiMu.Lock()
	defer ansiMu.Unlock()
	if ok, seen := ansiSeen[f.Fd()]; seen {
		return ok
	}
	handle := windows.Handle(f.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(handle, &mode); err != nil {
		ansiSeen[f.Fd()] = false // not a console (redirected or a mintty pipe)
		return false
	}
	if mode&windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING != 0 {
		ansiSeen[f.Fd()] = true
		return true
	}
	ok := windows.SetConsoleMode(handle, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING) == nil
	ansiSeen[f.Fd()] = ok
	return ok
}
