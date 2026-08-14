package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func hoverAt(t *testing.T, text string, line, char int) (string, bool) {
	t.Helper()
	uri := "file:///test.constfile"
	s := newServer()
	s.updateDoc(uri, text, 1)
	params, _ := json.Marshal(map[string]interface{}{
		"textDocument": map[string]string{"uri": uri},
		"position":     map[string]int{"line": line, "character": char},
	})
	res, err := s.handleHover(params)
	if err != nil {
		t.Fatalf("hover: %v", err)
	}
	hr, ok := res.(hoverResult)
	if !ok {
		return "", false
	}
	return hr.Contents.Value, true
}

func completionAt(t *testing.T, text string, line, char int) []completionItem {
	t.Helper()
	uri := "file:///test.constfile"
	s := newServer()
	s.updateDoc(uri, text, 1)
	params, _ := json.Marshal(map[string]interface{}{
		"textDocument": map[string]string{"uri": uri},
		"position":     map[string]int{"line": line, "character": char},
	})
	res, err := s.handleCompletion(params)
	if err != nil {
		t.Fatalf("completion: %v", err)
	}
	cl, ok := res.(completionList)
	if !ok {
		return nil
	}
	return cl.Items
}

func TestLSPKeywordHover(t *testing.T) {
	text := `cmd {
    switch "&x" {
        case "a" {
            $ echo a
        }
    }
    lock "l" {
        $ echo l
    }
    cp a b
    $ echo &last.exit
}
`
	cases := []struct {
		line, char int
		want       string
	}{
		{1, 7, "switch <expr>"},
		{2, 10, "case v1, v2"},
		{6, 6, "exclusive lock"},
		{9, 5, "cp <src> <dst>"},
		{10, 12, "most recently executed"},
	}
	for _, c := range cases {
		got, ok := hoverAt(t, text, c.line, c.char)
		if !ok {
			t.Errorf("line %d char %d: no hover", c.line, c.char)
			continue
		}
		if !strings.Contains(got, c.want) {
			t.Errorf("line %d char %d: hover = %q, want contains %q", c.line, c.char, got, c.want)
		}
	}
}

func TestLSPFunctionHover(t *testing.T) {
	text := `var x = upper("a")
var y = len(&x)
`
	for _, c := range []struct {
		line, char int
		want       string
	}{
		{0, 10, "uppercases"},
		{1, 10, "number of list items"},
	} {
		got, ok := hoverAt(t, text, c.line, c.char)
		if !ok {
			t.Errorf("line %d: no hover", c.line)
			continue
		}
		if !strings.Contains(got, c.want) {
			t.Errorf("line %d: hover = %q, want contains %q", c.line, got, c.want)
		}
	}
}

func TestLSPStateRefs(t *testing.T) {
	text := `state last = "0.0.0"
cmd {
    $ echo @state("last")
}
`
	// Hover on the state("last") reference shows the persisted-value blurb.
	got, ok := hoverAt(t, text, 2, 15)
	if !ok || !strings.Contains(got, "state") {
		t.Errorf("state hover = %q (ok=%v)", got, ok)
	}

	// Go-to-definition jumps to the state declaration.
	uri := "file:///test.constfile"
	s := newServer()
	s.updateDoc(uri, text, 1)
	params, _ := json.Marshal(map[string]interface{}{
		"textDocument": map[string]string{"uri": uri},
		"position":     map[string]int{"line": 2, "character": 15},
	})
	res, err := s.handleDefinition(params)
	if err != nil {
		t.Fatalf("definition: %v", err)
	}
	loc, ok := res.(location)
	if !ok {
		t.Fatalf("expected location, got %T", res)
	}
	if loc.Range.Start.Line != 0 {
		t.Errorf("state decl line = %d, want 0", loc.Range.Start.Line)
	}
}

func TestLSPDocumentSymbolsIncludeState(t *testing.T) {
	text := `state last = "0.0.0"
build {
    $ echo hi
}
`
	uri := "file:///test.constfile"
	s := newServer()
	s.updateDoc(uri, text, 1)
	params, _ := json.Marshal(map[string]interface{}{
		"textDocument": map[string]string{"uri": uri},
	})
	res, err := s.handleDocumentSymbol(params)
	if err != nil {
		t.Fatalf("documentSymbol: %v", err)
	}
	found := false
	for _, sym := range res.([]documentSymbol) {
		if sym.Name == "last" && sym.Detail == "persisted state" {
			found = true
		}
	}
	if !found {
		t.Errorf("state symbol missing from %v", res)
	}
}

func TestLSPKeywordCompletion(t *testing.T) {
	text := `cmd {
    sw
}
`
	items := completionAt(t, text, 1, 4)
	labels := map[string]bool{}
	for _, it := range items {
		labels[it.Label] = true
	}
	for _, want := range []string{"switch", "lock", "state", "cp", "timeout"} {
		if !labels[want] {
			t.Errorf("completion missing %q: %v", want, items)
		}
	}
}

func TestLSPFunctionCompletionInVarValue(t *testing.T) {
	text := `var x = up
`
	items := completionAt(t, text, 0, 10)
	found := false
	for _, it := range items {
		if it.Label == "upper()" {
			found = true
		}
	}
	if !found {
		t.Errorf("function completion missing upper(): %v", items)
	}
}

func TestLSPCommandNameWithTimeoutModifier(t *testing.T) {
	for _, c := range []struct {
		line string
		want string
	}{
		{"build timeout 30s {", "build"},
		{"build (env) timeout 30s < setup {", "build"},
		{"build", "build"},
	} {
		if got, ok := commandNameAtLine(c.line); !ok || got != c.want {
			t.Errorf("commandNameAtLine(%q) = %q (ok=%v), want %q", c.line, got, ok, c.want)
		}
	}
}

func TestLSPFullFeatureFileParsesCleanly(t *testing.T) {
	text := `var platforms = [linux, windows]
var total = 2 * 3 + 1
state last = "0.0.0"

build timeout 30s {
    switch "&total" {
        case "7" {
            $ echo seven
        }
        default {
            $ echo other
        }
    }
    in dist {
        mkdir out
        cp Constfile out/copy.txt
        touch out/t
        ! $ exit 3
        $ echo "exit=&last.exit"
    }
    lock "build" {
        $ echo locked
    }
    state last = "1.0.0"
    for l in lines("names.txt") {
        $ echo "n=&l"
    }
    confirm "continue?"
    prompt "press enter"
    input name "name?"
}
`
	uri := "file:///test.constfile"
	s := newServer()
	s.updateDoc(uri, text, 1)
	if _, ok := s.docs[uri]; !ok {
		t.Fatal("doc not registered")
	}
	// The parser succeeds: the doc carries parsed data.
	s.mu.Lock()
	doc := s.docs[uri]
	s.mu.Unlock()
	if doc.data == nil {
		t.Fatal("expected parsed data (new syntax must parse)")
	}
	if _, err := doc.data.GetCommand("build"); err != nil {
		t.Fatalf("build command missing: %v", err)
	}
}

func TestLSPCaseOutsideSwitchDiagnostic(t *testing.T) {
	text := `cmd {
    case "x" {
        $ echo a
    }
}
`
	uri := "file:///test.constfile"
	s := newServer()
	s.updateDoc(uri, text, 1)
	s.mu.Lock()
	doc := s.docs[uri]
	s.mu.Unlock()
	if doc == nil || doc.data != nil {
		t.Fatalf("expected parse failure, got data %v", doc)
	}
}

func TestLSPLastResultCompletion(t *testing.T) {
	text := `cmd {
    ! $ exit 3
    $ echo "&last."
}
`
	items := completionAt(t, text, 2, 18)
	found := map[string]bool{}
	for _, it := range items {
		found[it.Label] = true
	}
	if !found["last.exit"] || !found["last.output"] {
		t.Errorf("last.* completion missing: %v", items)
	}
}
