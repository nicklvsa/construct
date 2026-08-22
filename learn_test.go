package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nicklvsa/construct/pkg"
)

const straceSample = `1234  openat(AT_FDCWD, "/repo/src/main.go", O_RDONLY|O_CLOEXEC) = 4
1234  openat(AT_FDCWD, "/repo/src/extra.go", O_RDWR) = 5
1234  openat(AT_FDCWD, "/repo/dist/app", O_WRONLY|O_CREAT|O_TRUNC, 0644) = 6
1234  openat(AT_FDCWD, "/repo/missing.txt", O_RDONLY|O_CLOEXEC) = -1 ENOENT (No such file or directory)
1234  execve("/usr/local/bin/go", ["go", "build"], 0x10 /* 50 vars */) = 0
1234  stat("/repo/src", {st_mode=S_IFDIR|0755, ...}) = 0
1234  newfstatat(AT_FDCWD, "/repo/go.mod", 0x..., 0) = 0
12345 openat(AT_FDCWD, "/repo/src/nested/deep.go", O_RDONLY) = 7
`

func TestParseStraceReads(t *testing.T) {
	reads := parseStraceReads([]byte(straceSample), "/repo")
	want := map[string]bool{
		"/repo/src/main.go":        true,
		"/repo/src/extra.go":       true, // O_RDWR counts as an input
		"/repo/src/nested/deep.go": true,
		"/usr/local/bin/go":        true, // execve; filtered by repo scope later
	}
	for p := range want {
		if !reads[p] {
			t.Errorf("missing read %s", p)
		}
	}
	for p := range reads {
		if !want[p] {
			t.Errorf("unexpected read %s", p)
		}
	}
}

func TestParseStraceReadsResolvesRelativePaths(t *testing.T) {
	sample := `1234  openat(AT_FDCWD, "src/main.go", O_RDONLY|O_CLOEXEC) = 4
1234  openat(AT_FDCWD, "/abs/other.go", O_RDONLY) = 5
`
	reads := parseStraceReads([]byte(sample), "/repo")
	if !reads[filepath.Join("/repo", "src", "main.go")] {
		t.Errorf("relative read not resolved against base dir: %v", reads)
	}
	if !reads["/abs/other.go"] {
		t.Errorf("absolute read dropped: %v", reads)
	}
}

func TestFirstQuoted(t *testing.T) {
	if got := firstQuoted(`openat(AT_FDCWD, "/a/b c.txt", O_RDONLY) = 3`); got != "/a/b c.txt" {
		t.Errorf("firstQuoted = %q", got)
	}
	if got := firstQuoted(`no quotes here`); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestFilterRepoReadsExcludesProducedAndInternal(t *testing.T) {
	base := t.TempDir()
	os.WriteFile(filepath.Join(base, "src.go"), nil, 0644)
	os.WriteFile(filepath.Join(base, "Constfile"), []byte("build produces out.app < *.go {\n  $ true\n}\n"), 0644)
	p, err := pkg.NewParser(filepath.Join(base, "Constfile"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := p.Parse()
	if err != nil {
		t.Fatal(err)
	}

	reads := map[string]bool{
		filepath.Join(base, "src.go"):                         true,
		filepath.Join(base, "out.app"):                        true,
		filepath.Join(base, ".git", "x"):                      true,
		filepath.Join(base, ".construct-cache", "state.json"): true,
		filepath.Join(base, "..", "outside.txt"):              true,
	}
	got := filterRepoReads(reads, base, data)
	if len(got) != 1 || got[0] != "src.go" {
		t.Errorf("expected only src.go, got %v", got)
	}
}
