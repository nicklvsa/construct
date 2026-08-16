//go:build windows

package main

import (
	"os"
	"sync"

	"golang.org/x/sys/windows"
)

var (
	ansiOnce sync.Once
	ansiOK   bool
)

func enableANSI(f *os.File) bool {
	ansiOnce.Do(func() {
		handle := windows.Handle(f.Fd())
		var mode uint32
		if err := windows.GetConsoleMode(handle, &mode); err != nil {
			return // not a console (redirected or a mintty pipe)
		}
		if mode&windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING != 0 {
			ansiOK = true
			return
		}
		ansiOK = windows.SetConsoleMode(handle, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING) == nil
	})
	return ansiOK
}
