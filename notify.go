package main

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// notifyResult fires a best-effort desktop notification.
func notifyResult(success bool, summary string) {
	title := "construct: build passed"
	if !success {
		title = "construct: build FAILED"
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		script := fmt.Sprintf(`display notification %s with title %s`, appleScriptString(summary), appleScriptString(title))
		cmd = exec.Command("osascript", "-e", script)
	case "windows":
		script := fmt.Sprintf(`[void][System.Reflection.Assembly]::LoadWithPartialName('System.Windows.Forms');`+
			`[void][System.Reflection.Assembly]::LoadWithPartialName('System.Drawing');`+
			`$n=New-Object System.Windows.Forms.NotifyIcon;`+
			`$n.Icon=[System.Drawing.SystemIcons]::Information;`+
			`$n.Visible=$true;`+
			`$n.ShowBalloonTip(5000,'%s','%s',[System.Windows.Forms.ToolTipIcon]::None);`+
			`Start-Sleep -Seconds 1`, title, escapePowerShell(summary))
		cmd = exec.Command("powershell", "-NoProfile", "-Command", script)
	default:
		cmd = exec.Command("notify-send", title, summary)
	}
	// Notifications must never break or delay the build outcome.
	_ = cmd.Run()
}

func escapePowerShell(s string) string {
	s = strings.ReplaceAll(s, "'", "''")
	return strings.ReplaceAll(s, "\n", " ")
}

// appleScriptString quotes s for an AppleScript string literal. AppleScript
// has no escape sequences like \n (Go's %q would print them literally), so
// newlines are collapsed to spaces.
func appleScriptString(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	s = strings.ReplaceAll(s, "\n", " ")
	return `"` + s + `"`
}
