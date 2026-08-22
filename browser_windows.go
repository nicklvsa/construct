//go:build windows

package main

import "os/exec"

func uiOpenBrowser(url string) {
	c := exec.Command("cmd", "/c", "start", "", url)
	go func() { _ = c.Run() }()
}
