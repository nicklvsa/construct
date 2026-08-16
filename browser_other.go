//go:build !windows && !darwin

package main

import "os/exec"

func uiOpenBrowser(url string) {
	_ = exec.Command("xdg-open", url).Start()
}
