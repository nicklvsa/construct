package main

import (
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
	line := "main < gen in a, test in b {"
	// The last " in " binds the workdir (matches the parser).
	dir, start, end, ok := workDirAtPosition(line, strings.Index(line, "b"))
	if !ok || dir != "b" {
		t.Fatalf("workDirAtPosition on %q = %q (ok=%v), want %q", line, dir, ok, "b")
	}
	if line[start:end] != "b" {
		t.Errorf("column span = %q, want %q", line[start:end], "b")
	}
	// Hovering an earlier " in " (part of a prereq name) is not the workdir.
	if _, _, _, ok := workDirAtPosition(line, strings.Index(line, "a")); ok {
		t.Error("expected no workdir hit on the first 'in' occurrence")
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

func parseForTest(t *testing.T, text string) *pkg.ParsedData {
	t.Helper()
	parser := pkg.NewParserFromContent("test.constfile", text)
	data, err := parser.Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return data
}
