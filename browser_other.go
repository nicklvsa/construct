//go:build !windows && !darwin

package main

import "os/exec"

func uiOpenBrowser(url string) {
	go func() { _ = exec.Command("xdg-open", url).Run() }()
}
