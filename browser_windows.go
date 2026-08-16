//go:build windows

package main

import "os/exec"

func uiOpenBrowser(url string) {
	// start treats the first quoted argument as a window title.
	c := exec.Command("cmd", "/c", "start", "", url)
	_ = c.Start()
}
