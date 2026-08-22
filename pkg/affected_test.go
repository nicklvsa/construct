package pkg

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func affectedData() *ParsedData {
	data := &ParsedData{
		Commands: []*Command{
			{Name: "gen", FileDeps: []string{"src/*.go"}},
			{Name: "build", Prereqs: []string{"gen"}, FileDeps: []string{"main.go"}},
			{Name: "deploy", Prereqs: []string{"build"}},
			{Name: "docs", FileDeps: []string{"docs/*.md"}},
		},
	}
	data.buildIndexMaps()
	return data
}

func TestAffectedCommands(t *testing.T) {
	base := t.TempDir()
	data := affectedData()

	changed := map[string]bool{filepath.Join(base, "src", "main.go"): true}
	affected := AffectedCommands(data, changed, base)
	for _, name := range []string{"gen", "build", "deploy"} {
		if !affected[name] {
			t.Errorf("%s should be affected by src/main.go", name)
		}
	}
	if affected["docs"] {
		t.Error("docs should not be affected")
	}

	changed = map[string]bool{filepath.Join(base, "docs", "intro.md"): true}
	affected = AffectedCommands(data, changed, base)
	if !affected["docs"] {
		t.Error("docs should be affected by docs/intro.md")
	}
	if affected["build"] {
		t.Error("build should not be affected by docs/intro.md")
	}
}

func TestAffectedCommandsSourceFile(t *testing.T) {
	base := t.TempDir()
	data := affectedData()
	data.Commands[0].SourceFile = "Constfile"

	changed := map[string]bool{filepath.Join(base, "Constfile"): true}
	affected := AffectedCommands(data, changed, base)
	if !affected["gen"] || !affected["build"] {
		t.Error("commands declared in a changed Constfile should be affected")
	}
}

func TestAffectedCommandsDeletedFile(t *testing.T) {
	base := t.TempDir()
	data := affectedData()

	// A deleted dep never expands from disk; globs must match the changed path.
	if err := os.WriteFile(filepath.Join(base, "main.go"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	changed := map[string]bool{filepath.Join(base, "main.go"): true}
	if affected := AffectedCommands(data, changed, base); !affected["build"] {
		t.Error("deleted main.go should affect build via glob matching")
	}
}

func TestAffectedCommandsNewUntrackedDirectory(t *testing.T) {
	base := t.TempDir()
	data := &ParsedData{
		Commands: []*Command{
			{Name: "read", FileDeps: []string{"docs/new/page.md"}},
		},
	}
	data.buildIndexMaps()

	// git ls-files collapses fully-untracked directories to "docs/new/".
	changed := map[string]bool{filepath.Join(base, "docs", "new"): true}
	if affected := AffectedCommands(data, changed, base); !affected["read"] {
		t.Error("a dep inside a newly-added directory should match via the parent walk")
	}
}

func TestGitChangedFiles(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")

	os.WriteFile(filepath.Join(dir, "base.txt"), []byte("v1"), 0644)
	os.Mkdir(filepath.Join(dir, "src"), 0755)
	os.WriteFile(filepath.Join(dir, "src", "a.go"), []byte("a"), 0644)
	run("add", "-A")
	run("commit", "-qm", "base")

	os.WriteFile(filepath.Join(dir, "base.txt"), []byte("v2"), 0644)
	os.WriteFile(filepath.Join(dir, "src", "b.go"), []byte("b"), 0644) // untracked

	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := GitChangedFiles(dir, "HEAD")
	if err != nil {
		t.Fatalf("GitChangedFiles: %v", err)
	}
	if !changed[filepath.Join(root, "base.txt")] {
		t.Error("modified base.txt missing from changed set")
	}
	if !changed[filepath.Join(root, "src", "b.go")] {
		t.Error("untracked src/b.go missing from changed set")
	}

	if _, err := GitChangedFiles(dir, "no-such-ref"); err == nil {
		t.Error("expected an error for an unknown ref")
	}
}

func TestFilesNotWatched(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "watched.txt"), nil, 0644)
	os.WriteFile(filepath.Join(dir, "orphan.md"), nil, 0644)
	os.MkdirAll(filepath.Join(dir, ".git"), 0755)
	os.WriteFile(filepath.Join(dir, ".git", "HEAD"), nil, 0644)

	data := &ParsedData{
		Commands: []*Command{
			{Name: "build", FileDeps: []string{"watched.txt"}, Produces: []string{"made.txt"}},
		},
	}
	data.buildIndexMaps()

	unwatched, err := FilesNotWatched(data, dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range unwatched {
		if filepath.Base(f) != "orphan.md" {
			t.Errorf("unexpected unwatched file %s", f)
		}
	}
	if len(unwatched) != 1 {
		t.Errorf("expected only orphan.md, got %d file(s)", len(unwatched))
	}
}
