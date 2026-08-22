package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInstallHookRefusesForeignScript(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	old, _ := os.Getwd()
	os.Chdir(dir)
	t.Cleanup(func() { os.Chdir(old) })
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	hooksDir := filepath.Join(dir, ".construct-hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooksDir, "pre-commit"), []byte("#!/bin/sh\nrm -rf /\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := installHooks([]string{"pre-commit"}, nil, false); err == nil {
		t.Error("expected refusal to overwrite a foreign hook script")
	}
}

func TestInstallShellCompletionZsh(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/zsh")

	if err := installCompletions(&options{}); err != nil {
		t.Fatalf("install: %v", err)
	}
	script, err := os.ReadFile(filepath.Join(home, ".construct", "completions", "construct.zsh"))
	if err != nil {
		t.Fatalf("completion script missing: %v", err)
	}
	if !strings.Contains(string(script), "_construct") {
		t.Error("script does not look like the zsh completion")
	}
	rc, _ := os.ReadFile(filepath.Join(home, ".zshrc"))
	if !strings.Contains(string(rc), "construct.zsh") {
		t.Error("rc file missing the source line")
	}

	// Idempotent: a second install must not duplicate the rc block.
	if err := installCompletions(&options{}); err != nil {
		t.Fatalf("reinstall: %v", err)
	}
	rc, _ = os.ReadFile(filepath.Join(home, ".zshrc"))
	if got := strings.Count(string(rc), "construct.zsh"); got != 1 {
		t.Errorf("rc contains %d source lines, want 1", got)
	}

	if err := uninstallCompletions(&options{}); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".construct", "completions", "construct.zsh")); !os.IsNotExist(err) {
		t.Error("completion script still present after uninstall")
	}
	rc, _ = os.ReadFile(filepath.Join(home, ".zshrc"))
	if strings.Contains(string(rc), "construct.zsh") {
		t.Error("rc still sources completions after uninstall")
	}
}

func TestInstallFishCompletion(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := installFishCompletion(); err != nil {
		t.Fatalf("fish install: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "fish", "completions", "construct.fish")); err != nil {
		t.Fatalf("fish completion missing: %v", err)
	}
}

func TestInstallHooks(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	old, _ := os.Getwd()
	os.Chdir(dir)
	t.Cleanup(func() { os.Chdir(old) })

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")

	if err := installHooks([]string{"pre-push"}, []string{"build", "test"}, false); err != nil {
		t.Fatalf("installHooks: %v", err)
	}
	script, err := os.ReadFile(filepath.Join(dir, ".construct-hooks", "pre-push"))
	if err != nil {
		t.Fatalf("hook missing: %v", err)
	}
	if !strings.Contains(string(script), "'build' 'test'") {
		t.Errorf("hook does not run the requested targets safely: %s", script)
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(filepath.Join(dir, ".construct-hooks", "pre-push"))
		if info.Mode()&0111 == 0 {
			t.Error("hook is not executable")
		}
	}
	if out, err := exec.Command("git", "config", "--get", "core.hooksPath").Output(); err != nil || strings.TrimSpace(string(out)) != ".construct-hooks" {
		t.Errorf("core.hooksPath not set: %s %v", out, err)
	}

	if err := installHooks([]string{"pre-push"}, nil, true); err != nil {
		t.Fatalf("uninstall hook: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".construct-hooks")); !os.IsNotExist(err) {
		t.Error("hooks dir still present after uninstall")
	}
	if out, _ := exec.Command("git", "config", "--get", "core.hooksPath").Output(); strings.TrimSpace(string(out)) != "" {
		t.Error("core.hooksPath still set after uninstall")
	}
}

func TestInstallRejectsUnknownHook(t *testing.T) {
	if err := installHooks([]string{"evil-hook"}, nil, false); err == nil {
		t.Error("expected error for unknown hook name")
	}
}
