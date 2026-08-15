package pkg

import (
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/term"
)

func DefaultShell() (string, []string) {
	if runtime.GOOS == "windows" {
		var gitBashPaths []string
		if dir := os.Getenv("ProgramFiles"); dir != "" {
			gitBashPaths = append(gitBashPaths, filepath.Join(dir, `Git\usr\bin\bash.exe`))
		}
		if dir := os.Getenv("ProgramFiles(x86)"); dir != "" {
			gitBashPaths = append(gitBashPaths, filepath.Join(dir, `Git\usr\bin\bash.exe`))
		}
		if dir := os.Getenv("LOCALAPPDATA"); dir != "" {
			gitBashPaths = append(gitBashPaths, filepath.Join(dir, `Programs\Git\usr\bin\bash.exe`))
		}
		for _, p := range gitBashPaths {
			if _, err := os.Stat(p); err == nil {
				return p, []string{"-c"}
			}
		}
		// Fall back to cmd.exe if Git Bash isn't installed.
		return "cmd", []string{"/c"}
	}
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh, nonInteractiveArgs(sh)
	}
	return "/bin/sh", []string{"-c"}
}

func nonInteractiveArgs(shell string) []string {
	switch base := path.Base(strings.ReplaceAll(shell, "\\", "/")); base {
	case "zsh":
		return []string{"-f", "-c"}
	case "bash", "bash.exe":
		return []string{"--noprofile", "--norc", "-c"}
	}
	return []string{"-c"}
}

func supportsPipefail(shell string) bool {
	switch path.Base(strings.ReplaceAll(shell, "\\", "/")) {
	case "bash", "zsh", "bash.exe":
		return true
	}
	return false
}

func termIsTTY(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}

func (e *Executor) computeChildEnv() []string {
	env := os.Environ()
	if runtime.GOOS != "windows" {
		return env
	}
	// Only adjust if we're using Git Bash.
	bashDir := filepath.Dir(e.shellName)           // ...\Git\usr\bin
	gitRoot := filepath.Dir(filepath.Dir(bashDir)) // ...\Git
	usrBin := filepath.Join(gitRoot, "usr", "bin")
	if _, err := os.Stat(usrBin); err != nil {
		return env
	}

	for i, kv := range env {
		if strings.HasPrefix(strings.ToUpper(kv), "PATH=") {
			env[i] = "PATH=" + usrBin + string(os.PathListSeparator) + kv[5:]
			break
		}
	}
	return env
}

var containerEnvBlocked = map[string]bool{
	"TMPDIR": true, "TMP": true, "TEMP": true,
	"HOME": true, "PWD": true, "OLDPWD": true, "SHELL": true,
	"USER": true, "LOGNAME": true, "SHLVL": true,
	"PATH": true, "SSH_AUTH_SOCK": true, "COMMAND_MODE": true,
	"NVM_DIR": true, "NVM_CD_FLAGS": true,
	"XPC_FLAGS": true, "XPC_SERVICE_NAME": true,
	"__CFBundleIdentifier": true, "__CF_USER_TEXT_ENCODING": true,
	"MallocNanoZone": true, "OSLogRateLimit": true,
	"MACH_PORT_RENDEZVOUS_PEER_VALDATION": true,
}

func containerForwardedEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if containerEnvBlocked[name] {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func (e *Executor) containerRuntime() string {
	if e.containerRT == "" {
		e.containerRT = "docker"
		if _, err := exec.LookPath("docker"); err != nil {
			if _, err := exec.LookPath("podman"); err == nil {
				e.containerRT = "podman"
			} else {
				e.containerRT = "none"
			}
		}
	}
	return e.containerRT
}

func (e *Executor) resolveContainer(image string) string {
	if image == "" {
		return ""
	}
	switch e.containerRuntime() {
	case "none":
		return image // reported as an error at run time
	default:
		return e.containerRT + " " + image
	}
}
