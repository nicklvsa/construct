package pkg

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInteractiveShellEnvAndExit(t *testing.T) {
	if os.Getenv("CONSTRUCT_SHELL_TESTED") != "" {
		return
	}
	dir := t.TempDir()
	data := &ParsedData{
		Variables: []*Variable{{Name: "region", Value: "us-east-1", Scope: "global"}},
		Commands: []*Command{{
			Name: "dev",
			Body: []BodyStatement{
				{Type: StmtEnv, Env: []string{"API_ENV=development", "REGION=&region"}},
			},
		}},
	}
	data.buildIndexMaps()
	executor := NewExecutor(data, false, false)
	executor.SetBaseDir(dir)

	inR, inW, _ := os.Pipe()
	outR, outW, _ := os.Pipe()
	oldIn, oldOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = inR, outW
	io.WriteString(inW, "echo \"$API_ENV $REGION\"\nexit 7\n")
	inW.Close()

	code, err := executor.InteractiveShell("dev", "")
	os.Stdin, os.Stdout = oldIn, oldOut
	outW.Close()
	out, _ := io.ReadAll(outR)

	if err != nil {
		t.Fatalf("InteractiveShell: %v", err)
	}
	if code != 7 {
		t.Errorf("exit code = %d, want 7", code)
	}
	if !strings.Contains(string(out), "development us-east-1") {
		t.Errorf("env not applied, output: %q", string(out))
	}
}

func TestInteractiveShellUnknownCommand(t *testing.T) {
	data := &ParsedData{}
	data.buildIndexMaps()
	executor := NewExecutor(data, false, false)

	if _, err := executor.InteractiveShell("nope", ""); err == nil {
		t.Error("expected error for unknown command")
	}
}

func TestInteractiveShellWorkDir(t *testing.T) {
	dir := t.TempDir()
	data := &ParsedData{
		Commands: []*Command{{Name: "dev", WorkDir: "sub"}},
	}
	data.buildIndexMaps()
	executor := NewExecutor(data, false, false)
	executor.SetBaseDir(dir)

	inR, inW, _ := os.Pipe()
	outR, outW, _ := os.Pipe()
	oldIn, oldOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = inR, outW
	io.WriteString(inW, "pwd\n")
	inW.Close()

	code, err := executor.InteractiveShell("dev", "")
	os.Stdin, os.Stdout = oldIn, oldOut
	outW.Close()
	out, _ := io.ReadAll(outR)

	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}

	pwd := strings.ReplaceAll(strings.TrimSpace(string(out)), "\\", "/")
	if !strings.HasSuffix(pwd, "/sub") {
		t.Errorf("workdir not applied, pwd output: %q (want under %s)", pwd, dir)
	}

	if _, err := os.Stat(filepath.Join(dir, "sub")); err != nil {
		t.Errorf("workdir not created: %v", err)
	}
}
