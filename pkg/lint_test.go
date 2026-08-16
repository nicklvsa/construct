package pkg

import (
	"errors"
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

func TestLintManualNotFlagged(t *testing.T) {
	issues := lintText(t, "manual build-construct {\n  $ echo hi\n}\n")
	for _, is := range issues {
		if strings.Contains(is.Message, "never referenced") {
			t.Errorf("manual command flagged: %s", is.Message)
		}
	}
}

func TestLintHeaderKeywordAfterPrereqs(t *testing.T) {
	issues := lintText(t, "build {\n    $ echo step\n}\npackage < build produces dist/app {\n    $ echo hi\n}\n")
	found := false
	for _, is := range issues {
		if is.Severity == LintError && strings.Contains(is.Message, "`produces` after the prerequisite list") {
			found = true
			if is.Line != 3 {
				t.Errorf("issue line = %d, want 3", is.Line)
			}
		}
	}
	if !found {
		t.Errorf("no misplaced-produces error in %v", issues)
	}

	issues = lintText(t, "build {\n    $ echo step\n}\npackage produces dist/app < build {\n    $ echo hi\n}\n")
	for _, is := range issues {
		if strings.Contains(is.Message, "after the prerequisite list") {
			t.Errorf("false positive on valid header: %v", is)
		}
	}
}

func TestLintHeaderInvalidTimeout(t *testing.T) {
	issues := lintText(t, "build timeout 30x {\n    $ echo hi\n}\n")
	found := false
	for _, is := range issues {
		if is.Severity == LintError && strings.Contains(is.Message, "invalid timeout duration") {
			found = true
		}
	}
	if !found {
		t.Errorf("no invalid-timeout error in %v", issues)
	}

	for _, is := range lintText(t, "build timeout 30s {\n    $ echo hi\n}\n") {
		if strings.Contains(is.Message, "invalid timeout duration") {
			t.Errorf("false positive on valid timeout: %v", is)
		}
	}
}

func TestLintStatementPrefixMisuse(t *testing.T) {
	issues := lintText(t, "build {\n    timeout 30x $ go test\n}\n")
	timeouts := 0
	for _, is := range issues {
		if is.Severity == LintError && strings.Contains(is.Message, "statement timeout is written with a modifier now") {
			timeouts++
		}
	}
	if timeouts != 1 {
		t.Errorf("timeouts=%d, want 1: %v", timeouts, issues)
	}

	for _, is := range lintText(t, "build {\n    timeout 5 ./server\n    retry<3> $ go test\n}\n") {
		if strings.Contains(is.Message, "statement timeout is written with a modifier now") {
			t.Errorf("false positive on plain shell line: %v", is)
		}
	}
}

func TestLintLoopControlOutsideLoop(t *testing.T) {
	issues := lintText(t, "build {\n    continue\n}\naft {\n    break\n}\n")
	found := 0
	for _, is := range issues {
		if is.Severity == LintError && strings.Contains(is.Message, "outside a loop") {
			found++
		}
	}
	if found != 2 {
		t.Errorf("outside-loop errors = %d, want 2: %v", found, issues)
	}

	for _, is := range lintText(t, "build {\n    for x in a, b {\n        continue\n        break\n    }\n}\n") {
		if strings.Contains(is.Message, "outside a loop") {
			t.Errorf("false positive inside loop: %v", is)
		}
	}
}

func TestLintBreakInParallelLoop(t *testing.T) {
	issues := lintText(t, "build {\n    parallel for x in a, b {\n        break\n    }\n}\n")
	found := false
	for _, is := range issues {
		if is.Severity == LintError && strings.Contains(is.Message, "concurrent iterations") {
			found = true
		}
	}
	if !found {
		t.Errorf("no parallel-break error in %v", issues)
	}

	// break bound to a nested serial loop inside a parallel one is fine
	for _, is := range lintText(t, "build {\n    parallel for x in a, b {\n        for y in 1, 2 {\n            break\n        }\n    }\n}\n") {
		if strings.Contains(is.Message, "concurrent iterations") {
			t.Errorf("false positive on nested serial break: %v", is)
		}
	}
}

func TestLintRefTrailingHyphen(t *testing.T) {
	issues := lintText(t, "var svc = api\nbuild {\n    $ echo &svc-&n\n}\n")
	found := false
	for _, is := range issues {
		if is.Severity == LintWarning && strings.Contains(is.Message, "trailing `-`") {
			found = true
			if is.Line != 2 {
				t.Errorf("issue line = %d, want 2", is.Line)
			}
		}
	}
	if !found {
		t.Errorf("no trailing-hyphen warning in %v", issues)
	}

	// a variable really named with a hyphen is fine
	for _, is := range lintText(t, "var my-svc = api\nbuild {\n    $ echo &my-svc\n}\n") {
		if strings.Contains(is.Message, "trailing `-`") {
			t.Errorf("false positive on hyphenated name: %v", is)
		}
	}
}

func TestParseParallelModifierMisuse(t *testing.T) {
	in := "build {\n    parallel<4> echo hi\n}\n"
	if _, err := NewParserFromContent("t.constfile", in).Parse(); err == nil {
		t.Error("expected parse error for parallel<4> on a non-loop statement")
	}
}

func TestParseHeaderKeywordInBody(t *testing.T) {
	for _, kw := range []string{"manual", "produces", "container", "onchange", "import"} {
		in := "build {\n    $ echo ok\n    " + kw + " thing\n}\n"
		_, err := NewParserFromContent("t.constfile", in).Parse()
		if err == nil {
			t.Errorf("expected error for `%s` in body", kw)
			continue
		}
		if !strings.Contains(err.Error(), "`"+kw+"` belongs in the command header") {
			t.Errorf("wrong message for %s: %v", kw, err)
		}
		var pe *ParseError
		if !errors.As(err, &pe) || pe.Line != 3 {
			t.Errorf("error should point at line 3, got %+v", pe)
		}
	}

	// $ escape keeps shell access to such names
	if _, err := NewParserFromContent("t.constfile", "build {\n    $ manual --version\n}\n").Parse(); err != nil {
		t.Errorf("shell escape should parse: %v", err)
	}
}

func TestParseUnknownTopLevelStatement(t *testing.T) {
	for _, in := range []string{
		"garbage line here\nbuild {\n    $ echo hi\n}\n",
		"invoke deploy\nbuild {\n    $ echo hi\n}\n",
		"continue\nbuild {\n    $ echo hi\n}\n",
	} {
		if _, err := NewParserFromContent("t.constfile", in).Parse(); err == nil {
			t.Errorf("expected top-level error for %q", in)
		} else if !strings.Contains(err.Error(), "unrecognized top-level statement") {
			t.Errorf("wrong message: %v", err)
		}
	}
}

func TestLintStatementKeywordCommands(t *testing.T) {
	issues := lintText(t, "env { CI=true }\nbuild {\n    $ echo hi\n}\n")
	found := false
	for _, is := range issues {
		if is.Severity == LintError && strings.Contains(is.Message, "command literally named `env`") {
			found = true
		}
	}
	if !found {
		t.Errorf("no phantom-env error in %v", issues)
	}

	for _, is := range lintText(t, "build {\n    $ echo hi\n}\n") {
		if strings.Contains(is.Message, "statement keyword") {
			t.Errorf("false positive on plain shell line: %v", is)
		}
	}
}

func TestLintUnknownVarRefs(t *testing.T) {
	issues := lintText(t, "var version = 1\nbuild {\n    $ echo &vresion\n}\n")
	found := false
	for _, is := range issues {
		if is.Severity == LintWarning && strings.Contains(is.Message, "unknown reference `&vresion`") {
			found = true
			if is.Line != 2 {
				t.Errorf("issue line = %d, want 2", is.Line)
			}
		}
	}
	if !found {
		t.Errorf("no unknown-ref warning in %v", issues)
	}
}

func TestLintUnknownVarRefsKnownShapes(t *testing.T) {
	in := `var list = [a, b]
var flag = on
gen {
    $ echo one as out
}
use (env) < gen {
    for x in &list {
        if "&x" == "a" {
            $ echo &env &x &gen.out &gen.0 &flag &last.exit
        }
    }
}
`
	for _, is := range lintText(t, in) {
		if strings.Contains(is.Message, "unknown reference") {
			t.Errorf("false positive on plain shell line: %v", is)
		}
	}
}

func TestLintCaseWithoutValues(t *testing.T) {
	issues := lintText(t, `build {
    switch "&x" {
        case {
            $ echo hi
        }
        default {
            $ echo bye
        }
    }
}`)
	found := false
	for _, is := range issues {
		if is.Severity == LintWarning && strings.Contains(is.Message, "case without values never matches") {
			found = true
		}
	}
	if !found {
		t.Errorf("no empty-case warning in %v", issues)
	}
}

func TestLintDuplicateNamedOutputs(t *testing.T) {
	issues := lintText(t, "gen {\n    $ echo a as out\n    $ echo b as out\n}\n")
	found := false
	for _, is := range issues {
		if is.Severity == LintError && strings.Contains(is.Message, `duplicate named output "out"`) {
			found = true
		}
	}
	if !found {
		t.Errorf("no duplicate-output error in %v", issues)
	}
}

func TestLintInvokeCountsAsReference(t *testing.T) {
	text := "gen {\n  $ echo hi\n}\nrun {\n  if \"1\" == \"1\" {\n    invoke gen\n  }\n}\n_ < run {\n  $ echo ok\n}\n"
	for _, is := range lintText(t, text) {
		if strings.Contains(is.Message, "never referenced") {
			t.Errorf("invoked command flagged: %s", is.Message)
		}
	}
}

func TestLintImportedCommandAttribution(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Constfile-lib"), []byte("check {\n  $ echo ok\n}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	main := "import \"Constfile-lib\" as lib\nrun {\n  $ echo hi\n}\n_ < run {\n  $ echo ok\n}\n"
	data, err := NewParserFromContent(filepath.Join(dir, "Constfile"), main).Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	issues := Lint(strings.Split(main, "\n"), data, dir)
	found := false
	for _, is := range issues {
		if !strings.Contains(is.Message, `"lib.check" is never referenced`) {
			continue
		}
		found = true
		if is.File != filepath.Join(dir, "Constfile-lib") {
			t.Errorf("issue attributed to %q, want the imported file", is.File)
		}
		if is.Line != 0 {
			t.Errorf("issue line = %d, want 0 (line 1 of the import)", is.Line)
		}
	}
	if !found {
		t.Errorf("unreferenced imported command not flagged: %v", issues)
	}
}
