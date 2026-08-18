package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const installSourceLine = "# added by construct install\nsource \"$HOME/.construct/completions/construct.%s\"\n"

var installableHooks = map[string]bool{
	"pre-commit":         true,
	"pre-merge-commit":   true,
	"prepare-commit-msg": true,
	"commit-msg":         true,
	"pre-rebase":         true,
	"pre-push":           true,
	"post-merge":         true,
	"post-checkout":      true,
}

func runInstall(args []string, o *options) error {
	if err := rejectSubcommandFlags(args, "install"); err != nil {
		return err
	}
	if o.uninstall {
		return installUninstall(o, args)
	}
	var errs []string
	if len(o.hooks) > 0 {
		if err := installHooks(o.hooks, args, false); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if err := installCompletions(o); err != nil {
		errs = append(errs, err.Error())
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

func installCompletions(o *options) error {
	shell := o.shell
	if shell == "" {
		shell = detectShell()
	}
	switch shell {
	case "zsh", "bash":
		return installShellCompletion(shell)
	case "fish":
		return installFishCompletion()
	case "":
		return fmt.Errorf("could not detect your shell (set $SHELL or pass --shell bash|zsh|fish)")
	default:
		return fmt.Errorf("unsupported shell %q (bash, zsh, fish)", shell)
	}
}

func detectShell() string {
	sh := os.Getenv("SHELL")
	if sh == "" {
		return ""
	}
	return strings.TrimSuffix(filepath.Base(sh), ".exe")
}

func installShellCompletion(shell string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(home, ".construct", "completions")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	var script string
	if shell == "zsh" {
		script = completionZsh()
	} else {
		script = completionBash()
	}
	target := filepath.Join(dir, "construct."+shell)
	if err := os.WriteFile(target, []byte(script), 0644); err != nil {
		return err
	}

	rc := filepath.Join(home, "."+shell+"rc")
	line := fmt.Sprintf(installSourceLine, shell)
	if err := appendOnce(rc, line); err != nil {
		return err
	}
	fmt.Printf("installed %s completions: %s (sourced from %s)\n", shell, target, rc)
	return nil
}

func installFishCompletion() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(home, ".config", "fish", "completions")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	target := filepath.Join(dir, "construct.fish")
	if err := os.WriteFile(target, []byte(completionFish()), 0644); err != nil {
		return err
	}
	fmt.Printf("installed fish completions: %s\n", target)
	return nil
}

func appendOnce(path, block string) error {
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if strings.Contains(string(data), block) {
		return nil
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
		if _, err := f.WriteString("\n"); err != nil {
			return err
		}
	}
	_, err = f.WriteString(block)
	return err
}

func installUninstall(o *options, args []string) error {
	if len(o.hooks) > 0 {
		if err := installHooks(o.hooks, args, true); err != nil {
			return err
		}
		return nil
	}
	return uninstallCompletions(o)
}

func uninstallCompletions(o *options) error {
	shell := o.shell
	if shell == "" {
		shell = detectShell()
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	switch shell {
	case "zsh", "bash":
		target := filepath.Join(home, ".construct", "completions", "construct."+shell)
		if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
			return err
		}
		rc := filepath.Join(home, "."+shell+"rc")
		if err := removeBlock(rc, fmt.Sprintf(installSourceLine, shell)); err != nil {
			return err
		}
		fmt.Printf("removed %s completions (%s)\n", shell, target)
		return nil
	case "fish":
		target := filepath.Join(home, ".config", "fish", "completions", "construct.fish")
		if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
			return err
		}
		fmt.Printf("removed fish completions (%s)\n", target)
		return nil
	case "":
		return fmt.Errorf("could not detect your shell (set $SHELL or pass --shell bash|zsh|fish)")
	default:
		return fmt.Errorf("unsupported shell %q (bash, zsh, fish)", shell)
	}
}

func removeBlock(path, block string) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	out := strings.ReplaceAll(string(data), block, "")
	if out == string(data) {
		return nil
	}
	return os.WriteFile(path, []byte(out), 0644)
}

func installHooks(names []string, targets []string, uninstall bool) error {
	root, err := gitRepoRoot()
	if err != nil {
		return err
	}
	hooksDir := filepath.Join(root, ".construct-hooks")
	for _, name := range names {
		if !installableHooks[name] {
			return fmt.Errorf("unknown git hook %q", name)
		}
		hookPath := filepath.Join(hooksDir, name)
		if uninstall {
			if err := os.Remove(hookPath); err != nil && !os.IsNotExist(err) {
				return err
			}
			fmt.Printf("removed git hook %s\n", hookPath)
			continue
		}
		if err := os.MkdirAll(hooksDir, 0755); err != nil {
			return err
		}
		cmdLine := "construct"
		if len(targets) > 0 {
			cmdLine += " " + strings.Join(targets, " ")
		}
		script := fmt.Sprintf("#!/bin/sh\n# installed by `construct install --hook %s` — edit freely; rerun install to regenerate\nexec %s\n", name, cmdLine)
		if err := os.WriteFile(hookPath, []byte(script), 0755); err != nil {
			return err
		}
		fmt.Printf("installed git hook %s -> %s\n", hookPath, cmdLine)
	}

	if uninstall {
		if empty, _ := isEmptyDir(hooksDir); empty {
			os.Remove(hooksDir)
		}
		if cur, err := gitConfigGet(root, "core.hooksPath"); err == nil && cur == ".construct-hooks" {
			gitConfigUnset(root, "core.hooksPath")
		}
		return nil
	}
	if err := gitConfigSet(root, "core.hooksPath", ".construct-hooks"); err != nil {
		return fmt.Errorf("could not set core.hooksPath: %w", err)
	}
	fmt.Printf("git hooks path: .construct-hooks (commit the directory to share hooks with your team)\n")
	return nil
}

func gitRepoRoot() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("not inside a git repository")
	}
	return strings.TrimSpace(string(out)), nil
}

func gitConfigGet(root, key string) (string, error) {
	out, err := exec.Command("git", "-C", root, "config", "--get", key).Output()
	return strings.TrimSpace(string(out)), err
}

func gitConfigSet(root, key, value string) error {
	return exec.Command("git", "-C", root, "config", key, value).Run()
}

func gitConfigUnset(root, key string) error {
	exec.Command("git", "-C", root, "config", "--unset", key).Run()
	return nil
}

func isEmptyDir(path string) (bool, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false, err
	}
	return len(entries) == 0, nil
}
