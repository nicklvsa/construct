package pkg

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseImportSpec(t *testing.T) {
	cases := []struct {
		line           string
		path, ns, cond string
		isGit          bool
		wantErr        bool
	}{
		{line: `import "lib.constfile"`, path: "lib.constfile"},
		{line: `import "lib.constfile" as lib`, path: "lib.constfile", ns: "lib"},
		{line: `import "lib.constfile" if os("darwin")`, path: "lib.constfile", cond: `os("darwin")`},
		{line: `import "lib.constfile" as lib if exists("lib.constfile")`, path: "lib.constfile", ns: "lib", cond: `exists("lib.constfile")`},
		{line: `import "mac.constfile" on darwin`, path: "mac.constfile", cond: `os("darwin")`},
		{line: `import "mac.constfile" on macos`, path: "mac.constfile", cond: `os("darwin")`},
		{line: `import "nix.constfile" on darwin, linux`, path: "nix.constfile", cond: `os("darwin") || os("linux")`},
		{line: `import git "github.com/acme/recipes"`, path: "github.com/acme/recipes", isGit: true},
		{line: `import git "github.com/acme/recipes@v1.2.0" as r if require("git")`, path: "github.com/acme/recipes@v1.2.0", ns: "r", cond: `require("git")`, isGit: true},
		{line: `import "strange if path.constfile"`, path: "strange if path.constfile"},
		{line: `import "x.constfile" on`, wantErr: true},
		{line: `import "x.constfile" if`, wantErr: true},
	}
	for _, tc := range cases {
		spec, err := parseImportSpec(tc.line)
		if tc.wantErr {
			if err == nil {
				t.Errorf("%s: expected an error", tc.line)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", tc.line, err)
			continue
		}
		if spec.path != tc.path || spec.ns != tc.ns || spec.cond != tc.cond || spec.isGit != tc.isGit {
			t.Errorf("%s: got {path:%q ns:%q cond:%q git:%v}", tc.line, spec.path, spec.ns, spec.cond, spec.isGit)
		}
	}
}

func TestConditionalImportSkipped(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "skipped.constfile"), []byte("never {\n  $ true\n}\n"), 0644)
	os.WriteFile(filepath.Join(dir, "Constfile"), []byte("import \"skipped.constfile\" on plan9\n"), 0644)

	p, err := NewParser(filepath.Join(dir, "Constfile"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := p.Parse()
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Commands) != 0 {
		t.Errorf("plan9-only import merged on %s", runtime.GOOS)
	}
}

func TestConditionalImportTaken(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "local.constfile"), []byte("here {\n  $ true\n}\n"), 0644)
	os.WriteFile(filepath.Join(dir, "Constfile"), []byte("import \"local.constfile\" if exists(\"local.constfile\")\n"), 0644)

	p, err := NewParser(filepath.Join(dir, "Constfile"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := p.Parse()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := data.GetCommand("here"); err != nil {
		t.Error("condition-true import was skipped")
	}
}

func TestParseGitSpec(t *testing.T) {
	cases := []struct {
		spec          string
		url, sub, ref string
		wantErr       bool
	}{
		{spec: "acme/recipes", url: "https://github.com/acme/recipes"},
		{spec: "github.com/acme/recipes", url: "https://github.com/acme/recipes"},
		{spec: "github.com/acme/recipes/tools", url: "https://github.com/acme/recipes", sub: "tools"},
		{spec: "gitlab.com/acme/recipes", url: "https://gitlab.com/acme/recipes"},
		{spec: "https://example.com/acme/recipes.git", url: "https://example.com/acme/recipes"},
		{spec: "acme/recipes@v2.1.0", url: "https://github.com/acme/recipes", ref: "v2.1.0"},
		{spec: "github.com/acme/recipes/sub@main", url: "https://github.com/acme/recipes", sub: "sub", ref: "main"},
		{spec: "acme", wantErr: true},
		{spec: "acme/recipes@", wantErr: true},
	}
	for _, tc := range cases {
		src, err := parseGitSpec(tc.spec)
		if tc.wantErr {
			if err == nil {
				t.Errorf("%s: expected an error", tc.spec)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", tc.spec, err)
			continue
		}
		if src.url != tc.url || src.subPath != tc.sub || src.ref != tc.ref {
			t.Errorf("%s: got url=%q sub=%q ref=%q", tc.spec, src.url, src.subPath, src.ref)
		}
	}
}

func TestParseGitSpecSSH(t *testing.T) {
	src, err := parseGitSpec("git@github.com:acme/recipes.git")
	if err != nil {
		t.Fatal(err)
	}
	if src.url != "git@github.com:acme/recipes.git" || src.repo != "recipes" {
		t.Errorf("ssh spec parsed wrong: %+v", src)
	}
}

func TestImportRootDir(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, CacheDirName(), "imports", "abc123", "sub")
	if got := importRootDir(nested); got != root {
		t.Errorf("nested cache dir: got %q want %q", got, root)
	}
	if got := importRootDir(root); got != root {
		t.Errorf("root dir: got %q", got)
	}
}

func TestGitImportEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	remote := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = remote
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(remote, "Constfile"), []byte("remote-cmd {\n  $ echo hi\n}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	run("commit", "-qm", "v1")

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Constfile"), []byte("import git \"file://"+filepath.ToSlash(remote)+"\" as r\n"), 0644); err != nil {
		t.Fatal(err)
	}
	p, err := NewParser(filepath.Join(root, "Constfile"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := p.Parse()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := data.GetCommand("r.remote-cmd"); err != nil {
		t.Fatalf("remote command missing: %v", err)
	}
	lock := loadImportLock(importLockPath(root))
	entry, ok := lock.Imports["file://"+filepath.ToSlash(remote)]
	if !ok || entry.Rev == "" {
		t.Fatalf("lock entry missing or has no rev: %+v", lock.Imports)
	}
	if !strings.Contains(filepath.ToSlash(entry.Dir), ".construct-cache") {
		t.Errorf("entry dir not in the cache: %q", entry.Dir)
	}
}

func TestServiceParsing(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "Constfile")
	os.WriteFile(file, []byte("service api {\n  port 8137\n  $ serve\n}\n\nplain {\n  port 9999\n  $ echo ok\n}\n"), 0644)
	p, err := NewParser(file)
	if err != nil {
		t.Fatal(err)
	}
	data, err := p.Parse()
	if err != nil {
		t.Fatal(err)
	}
	svc, err := data.GetCommand("api")
	if err != nil {
		t.Fatal(err)
	}
	if !svc.IsService || svc.Port != "8137" {
		t.Errorf("api: IsService=%v Port=%q", svc.IsService, svc.Port)
	}
	plain, _ := data.GetCommand("plain")
	if plain.IsService || plain.Port != "9999" {
		t.Errorf("plain command port should lift without service: %+v", plain)
	}

	// a `port <non-numeric>` shell line stays a shell statement
	os.WriteFile(file, []byte("cmd {\n  $ port list all\n}\n"), 0644)
	data, err = func() (*ParsedData, error) {
		p2, err := NewParser(file)
		if err != nil {
			return nil, err
		}
		return p2.Parse()
	}()
	if err != nil {
		t.Fatal(err)
	}
	cmd, _ := data.GetCommand("cmd")
	if len(cmd.Body) != 1 || cmd.Body[0].Type != StmtShell {
		t.Errorf("port list should stay shell, got %+v", cmd.Body)
	}
}
