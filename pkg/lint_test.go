package pkg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func lintText(t *testing.T, text string) []LintIssue {
	t.Helper()
	data, err := NewParserFromContent("Constfile", text).Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return Lint(strings.Split(text, "\n"), data, ".")
}

func TestLintUnknownNamedOutput(t *testing.T) {
	issues := lintText(t, "gen {\n  $ echo hi\n}\nuse {\n  $ deploy &gen.nope\n}\n")
	found := false
	for _, is := range issues {
		if is.Severity == LintError && strings.Contains(is.Message, "unknown named output") {
			found = true
			if is.Line != 4 {
				t.Errorf("issue line = %d, want 4 (the &gen.nope line)", is.Line)
			}
		}
	}
	if !found {
		t.Errorf("no unknown-named-output error in %v", issues)
	}
}

func TestLintIndexOutOfBounds(t *testing.T) {
	issues := lintText(t, "gen {\n  $ echo one\n}\nuse {\n  $ x &gen.5\n}\n")
	found := false
	for _, is := range issues {
		if is.Severity == LintError && strings.Contains(is.Message, "out of bounds") {
			found = true
		}
	}
	if !found {
		t.Errorf("no out-of-bounds error in %v", issues)
	}
}

func TestLintDuplicatePrereq(t *testing.T) {
	issues := lintText(t, "gen {\n  echo hi\n}\nmain < gen, gen {\n  echo done\n}\n")
	found := false
	for _, is := range issues {
		if is.Severity == LintWarning && strings.Contains(is.Message, "duplicate prerequisite") {
			found = true
			if is.Line != 3 {
				t.Errorf("duplicate should be on header line 3, got %d", is.Line)
			}
		}
	}
	if !found {
		t.Errorf("no duplicate warning in %v", issues)
	}
}

func TestLintMissingFileDep(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "Constfile"), []byte("build < nope.txt {\n  $ echo hi\n}\n"), 0644)
	p, err := NewParser(filepath.Join(dir, "Constfile"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := p.Parse()
	if err != nil {
		t.Fatal(err)
	}
	issues := Lint(strings.Split("build < nope.txt {\n  $ echo hi\n}\n", "\n"), data, dir)
	found := false
	for _, is := range issues {
		if is.Severity == LintWarning && strings.Contains(is.Message, "does not exist") {
			found = true
		}
	}
	if !found {
		t.Errorf("no missing-dep warning in %v", issues)
	}
}

func TestLintUnusedGlobalAndUnreferencedCommand(t *testing.T) {
	issues := lintText(t, "var unused = 1\norphan {\n  $ echo hi\n}\nused {\n  $ echo ok\n}\n_ < used {\n  $ echo default\n}\n")
	var sawUnused, sawOrphan bool
	for _, is := range issues {
		if is.Severity == LintInfo && strings.Contains(is.Message, `global variable "unused"`) {
			sawUnused = true
		}
		if is.Severity == LintInfo && strings.Contains(is.Message, `"orphan" is never referenced`) {
			sawOrphan = true
		}
	}
	if !sawUnused || !sawOrphan {
		t.Errorf("unused=%v orphan=%v in %v", sawUnused, sawOrphan, issues)
	}
}

func TestLintCleanFile(t *testing.T) {
	issues := lintText(t, "var used = 1\ngen {\n  $ echo &used as out\n}\nmain < gen {\n  $ echo &gen.out\n}\n_ < main {\n  $ echo default\n}\n")
	for _, is := range issues {
		t.Errorf("unexpected issue: %s", FormatLintIssue("Constfile", is))
	}
}

func TestLintNamedOutputInsideSwitch(t *testing.T) {
	text := "src {\n  switch \"1\" {\n    case \"1\" { $ echo x as out }\n  }\n}\nuse {\n  $ v &src.out\n}\n"
	for _, is := range lintText(t, text) {
		if is.Severity == LintError {
			t.Errorf("false positive: %s", is.Message)
		}
	}
}
