package pkg

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func runRm(t *testing.T, args, modifier string) error {
	t.Helper()
	data := &ParsedData{
		Commands: []*Command{{
			Name: "rmcmd",
			Body: []BodyStatement{{
				Type:        StmtBuiltin,
				Shell:       "rm",
				BuiltinArgs: args,
				Modifier:    modifier,
			}},
		}},
	}
	data.buildIndexMaps()
	return NewExecutor(data, false, false).Execute([]string{"rmcmd"})
}

func TestBuiltinRmKillModifierRemovesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(path, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := runRm(t, `"`+filepath.ToSlash(path)+`"`, "kill"); err != nil {
		t.Fatalf("rm<kill>: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("file should be gone, stat err = %v", err)
	}
}

func TestBuiltinRmKillModifierMissingPath(t *testing.T) {
	if err := runRm(t, `"`+filepath.Join(t.TempDir(), "nope.txt")+`"`, "kill"); err != nil {
		t.Fatalf("rm<kill> on missing path should succeed, got: %v", err)
	}
}

func TestBuiltinRmKillModifierRequiresPath(t *testing.T) {
	if err := runRm(t, "", "kill"); err == nil {
		t.Fatal("rm<kill> without a path should fail")
	}
}

func TestBuiltinRmKillModifierRunningExe(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-only: other platforms delete running executables directly")
	}
	src, err := exec.LookPath("ping.exe")
	if err != nil {
		t.Skip("ping.exe not found")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "sleeper.exe")
	in, err := os.ReadFile(src)
	if err != nil {
		t.Skipf("cannot read ping.exe: %v", err)
	}
	if err := os.WriteFile(path, in, 0755); err != nil {
		t.Fatal(err)
	}

	proc := exec.Command(path, "-n", "30", "127.0.0.1")
	if err := proc.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- proc.Wait() }()
	time.Sleep(500 * time.Millisecond)

	if err := os.Remove(path); err == nil {
		t.Skip("platform did not lock the running exe; nothing to test")
	}
	if err := runRm(t, `"`+filepath.ToSlash(path)+`"`, "kill"); err != nil {
		t.Fatalf("rm<kill> should terminate the holder and delete: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("exe should be gone, stat err = %v", err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("holder process did not exit")
	}
}
