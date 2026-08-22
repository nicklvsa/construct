//go:build darwin

package main

import "os/exec"

func uiOpenBrowser(url string) {
	go func() { _ = exec.Command("open", url).Run() }()
}
