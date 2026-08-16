//go:build darwin

package main

import "os/exec"

func uiOpenBrowser(url string) {
	_ = exec.Command("open", url).Start()
}
