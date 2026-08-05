package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pkg "github.com/nicklvsa/construct/pkg"
)

func TestDuplicatePrereqWarningsWithInDir(t *testing.T) {
	text := `gen {
    echo hi
}
main < gen in subdir, gen in other {
    echo done
}`
	data := parseForTest(t, text)
	diags := duplicatePrereqWarnings(text, data)
	if len(diags) != 1 {
		t.Fatalf("expected 1 duplicate warning, got %d: %v", len(diags), diags)
	}
	if !strings.Contains(diags[0].Message, "gen") {
		t.Errorf("warning should mention `gen`, got %q", diags[0].Message)
	}
	if diags[0].Range.Start.Line != 3 {
		t.Errorf("duplicate should be flagged on the header line (0-indexed 3), got line %d", diags[0].Range.Start.Line)
	}
}

func TestDuplicatePrereqWarningsNoFalsePositive(t *testing.T) {
	text := `gen {
    echo hi
}
main < gen in subdir {
    echo done
}`
	data := parseForTest(t, text)
	diags := duplicatePrereqWarnings(text, data)
	if len(diags) != 0 {
		t.Fatalf("expected no duplicate warnings, got %v", diags)
	}
}

func TestWorkDirAtPositionLastIn(t *testing.T) {
	line := "main in root < gen in a, test in b {"
	dir, start, end, ok := workDirAtPosition(line, strings.Index(line, "root"))
	if !ok || dir != "root" {
		t.Fatalf("workDirAtPosition on %q = %q (ok=%v), want %q", line, dir, ok, "root")
	}
	if line[start:end] != "root" {
		t.Errorf("column span = %q, want %q", line[start:end], "root")
	}
	// Prereq dirs after "<" are not the command's workdir.
	if _, _, _, ok := workDirAtPosition(line, strings.Index(line, "b")); ok {
		t.Error("expected no workdir hit for a prereq 'in' occurrence")
	}
	// No workdir declared at all.
	if _, _, _, ok := workDirAtPosition("main < gen {", 3); ok {
		t.Error("expected no workdir hit without an 'in' modifier")
	}
}

func TestEnclosingCommand(t *testing.T) {
	text := `log (thing_to_log) {
    $ echo "&thing_to_log"
}
_ {
    echo done
}`
	data := parseForTest(t, text)
	lines := strings.Split(text, "\n")

	cmd := enclosingCommand(lines, 1, data)
	if cmd == nil || cmd.Name != "log" {
		t.Fatalf("enclosingCommand(line 2) = %v, want log", cmd)
	}
	cmd = enclosingCommand(lines, 3, data)
	if cmd == nil || cmd.Name != "_" {
		t.Fatalf("enclosingCommand(line 4) = %v, want _", cmd)
	}
	cmd = enclosingCommand(lines, 0, data)
	if cmd == nil || cmd.Name != "log" {
		t.Fatalf("enclosingCommand(line 1) = %v, want log", cmd)
	}
}

// TestDefinitionAcrossImport verifies that go-to-definition on a prerequisite
// defined in an imported file jumps to that file.
func TestDefinitionAcrossImport(t *testing.T) {
	dir := t.TempDir()
	libPath := filepath.Join(dir, "lib.constfile")
	os.WriteFile(libPath, []byte("build {\n    echo hi\n}\n"), 0644)
	mainText := "import \"lib.constfile\"\nrelease < build {\n    echo done\n}\n"
	mainPath := filepath.Join(dir, "main.constfile")
	os.WriteFile(mainPath, []byte(mainText), 0644)

	uri := pathToURI(mainPath)
	s := newServer()
	s.updateDoc(uri, mainText, 1)

	lines := strings.Split(mainText, "\n")
	params, _ := json.Marshal(map[string]interface{}{
		"textDocument": map[string]string{"uri": uri},
		"position":     map[string]int{"line": 1, "character": strings.Index(lines[1], "build") + 2},
	})
	res, err := s.handleDefinition(params)
	if err != nil {
		t.Fatalf("definition: %v", err)
	}
	loc, ok := res.(location)
	if !ok {
		t.Fatalf("expected location, got %T", res)
	}
	if want := pathToURI(libPath); loc.URI != want {
		t.Errorf("definition URI = %q, want %q", loc.URI, want)
	}
	if loc.Range.Start.Line != 0 {
		t.Errorf("definition line = %d, want 0", loc.Range.Start.Line)
	}
}

// TestDefinitionLocalCommandStillResolves verifies local prereqs still resolve
// within the current document (no spurious cross-file jump).
func TestDefinitionLocalCommandStillResolves(t *testing.T) {
	text := "gen {\n    echo hi\n}\nmain < gen {\n    echo done\n}\n"
	uri := "file:///x/main.constfile"
	s := newServer()
	s.updateDoc(uri, text, 1)

	lines := strings.Split(text, "\n")
	params, _ := json.Marshal(map[string]interface{}{
		"textDocument": map[string]string{"uri": uri},
		"position":     map[string]int{"line": 3, "character": strings.Index(lines[3], "gen") + 2},
	})
	res, err := s.handleDefinition(params)
	if err != nil {
		t.Fatalf("definition: %v", err)
	}
	loc, ok := res.(location)
	if !ok {
		t.Fatalf("expected location, got %T", res)
	}
	if loc.URI != uri {
		t.Errorf("definition URI = %q, want current doc %q", loc.URI, uri)
	}
	if loc.Range.Start.Line != 0 {
		t.Errorf("definition line = %d, want 0", loc.Range.Start.Line)
	}
}

func parseForTest(t *testing.T, text string) *pkg.ParsedData {
	t.Helper()
	parser := pkg.NewParserFromContent("test.constfile", text)
	data, err := parser.Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return data
}
