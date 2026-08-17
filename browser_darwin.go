//go:build darwin

package main

import "os/exec"

func uiOpenBrowser(url string) {
	// Run (not Start) in a goroutine so the helper is reaped instead of
	// lingering as a zombie for the rest of the process lifetime.
	go func() { _ = exec.Command("open", url).Run() }()
}
