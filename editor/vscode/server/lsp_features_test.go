package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pkg "github.com/nicklvsa/construct/pkg"
)

func hoverAt(t *testing.T, text string, line, char int) (string, bool) {
	t.Helper()
	uri := "file:///test.constfile"
	s := newServer()
	s.updateDoc(uri, text)
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
	s.updateDoc(uri, text)
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
	s.updateDoc(uri, text)
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
	s.updateDoc(uri, text)
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
	s.updateDoc(uri, text)
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
	s.updateDoc(uri, text)
	s.mu.Lock()
	doc := s.docs[uri]
	s.mu.Unlock()
	if doc == nil || doc.data != nil {
		t.Fatalf("expected parse failure, got data %v", doc)
	}
}

func TestLSPBuiltinShellPrecedenceHover(t *testing.T) {
	text := `cmd {
    cp a b
    $ cp a b
    rm x
    $ rm x
    ! $ false
}
`
	// Hover on the bare builtin name shows the builtin hover.
	got, ok := hoverAt(t, text, 1, 5)
	if !ok || !strings.Contains(got, "Bare `cp` runs the builtin; `$ cp` runs the shell's cp") {
		t.Errorf("bare cp hover = %q (ok=%v)", got, ok)
	}
	got, ok = hoverAt(t, text, 3, 5)
	if !ok || !strings.Contains(got, "Bare `rm` runs the builtin; `$ rm` runs the shell's rm") {
		t.Errorf("bare rm hover = %q (ok=%v)", got, ok)
	}
	// Hovering the same word inside a `$` shell line shows NO construct hover.
	if got, ok := hoverAt(t, text, 2, 8); ok || strings.Contains(got, "builtin") {
		t.Errorf("$ cp should not show a builtin hover, got %q (ok=%v)", got, ok)
	}
	if got, ok := hoverAt(t, text, 4, 8); ok || strings.Contains(got, "builtin") {
		t.Errorf("$ rm should not show a builtin hover, got %q (ok=%v)", got, ok)
	}
	// Hover on the $ prefix explains the precedence rule.
	got, ok = hoverAt(t, text, 2, 4)
	if !ok || !strings.Contains(got, "bare") || !strings.Contains(got, "builtin") {
		t.Errorf("$ prefix hover = %q (ok=%v)", got, ok)
	}
	// Hover on the ! tolerance marker.
	got, ok = hoverAt(t, text, 5, 4)
	if !ok || !strings.Contains(got, "error-tolerant") || !strings.Contains(got, "last.exit") {
		t.Errorf("! prefix hover = %q (ok=%v)", got, ok)
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

func TestLSPCommandNameWithProducesOnChange(t *testing.T) {
	for _, c := range []struct {
		line string
		want string
	}{
		{"build produces dist/app < src {", "build"},
		{"build onchange src/** {", "build"},
		{"build produces a, b onchange src < dep.txt timeout 5s in dir {", "build"},
		{"|deploy| produces dist {", "deploy"},
	} {
		if got, ok := commandNameAtLine(c.line); !ok || got != c.want {
			t.Errorf("commandNameAtLine(%q) = %q (ok=%v), want %q", c.line, got, ok, c.want)
		}
	}
}

func TestLSPHoverOnProducesHeader(t *testing.T) {
	text := "build produces dist/app < src/main.go {\n    $ echo built\n}\n"
	msg, ok := hoverAt(t, text, 0, 2)
	if !ok {
		t.Fatal("no hover on a produces header")
	}
	if !strings.Contains(msg, "**build**") {
		t.Errorf("hover = %q, want the build command summary", msg)
	}
}

// Named outputs and shell-line indexes must see into switch/onfail/in/lock
// bodies, matching what the executor actually captures.
func TestLSPNamedOutputInSwitchAndOnFail(t *testing.T) {
	text := `src {
    switch "&os" {
        case "linux" {
            $ echo lnx as out
        }
    }
    onfail {
        $ echo cleanup as err
    }
    in sub {
        $ echo nested as deep
    }
}
use {
    $ deploy &src.out &src.err &src.deep &src.1
}
`
	parser := pkg.NewParserFromContent("file:///test.constfile", text)
	data, err := parser.Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := namedOutputHints(strings.Split(text, "\n"), data)
	for _, d := range diags {
		if strings.Contains(d.Message, "unknown named output") || strings.Contains(d.Message, "out of bounds") {
			t.Errorf("false diagnostic: %s", d.Message)
		}
	}
	if n := countShellLines(mustCommand(t, data, "src").Body); n != 3 {
		t.Errorf("countShellLines = %d, want 3 (switch case, onfail, and in-sub statements)", n)
	}
}

func mustCommand(t *testing.T, data *pkg.ParsedData, name string) *pkg.Command {
	t.Helper()
	cmd, err := data.GetCommand(name)
	if err != nil {
		t.Fatalf("get command %s: %v", name, err)
	}
	return cmd
}

func TestLSPDidCloseForgetsDocument(t *testing.T) {
	uri := "file:///test.constfile"
	s := newServer()
	s.updateDoc(uri, "build {\n    $ echo hi\n}\n")
	params, _ := json.Marshal(map[string]any{
		"textDocument": map[string]string{"uri": uri},
	})
	if _, err := s.handleDidClose(params); err != nil {
		t.Fatalf("didClose: %v", err)
	}
	s.mu.Lock()
	_, open := s.docs[uri]
	s.mu.Unlock()
	if open {
		t.Error("document still tracked after didClose")
	}
}

// Every keyword we complete must have hover documentation, and vice versa —
// this keeps hover coverage from silently rotting as the language grows.
func TestLSPKeywordHoverCoverage(t *testing.T) {
	for _, kw := range statementKeywords {
		if _, ok := keywordHover(kw); !ok {
			t.Errorf("keywordHover(%q) has no documentation", kw)
		}
	}
	for _, extra := range []string{"produces", "onchange"} {
		if _, ok := keywordHover(extra); !ok {
			t.Errorf("keywordHover(%q) has no documentation", extra)
		}
	}
}

func TestLSPFunctionHoverCoverage(t *testing.T) {
	for _, fn := range builtinFunctions {
		if _, ok := functionHover(fn); !ok {
			t.Errorf("functionHover(%q) has no documentation", fn)
		}
	}
}

func TestLSPStateRefHover(t *testing.T) {
	text := "state version = 3\nshow {\n    $ echo state(\"version\") @state(\"version\")\n}\n"
	for _, char := range []int{17, 18, 36, 37} {
		msg, ok := hoverAt(t, text, 2, char)
		if !ok {
			t.Fatalf("no hover at char %d", char)
		}
		if !strings.Contains(msg, "persisted state") {
			t.Errorf("hover at char %d = %q, want persisted-state info", char, msg)
		}
		if strings.Contains(msg, "environment variable") {
			t.Errorf("hover at char %d = %q: @state misread as an env var", char, msg)
		}
		if !strings.Contains(msg, "declared value: `3`") {
			t.Errorf("hover at char %d = %q, want the declared value", char, msg)
		}
	}
}

func TestLSPWorkdirHover(t *testing.T) {
	text := "build in sub {\n    $ pwd\n}\n"
	// "sub" starts at column 9.
	msg, ok := hoverAt(t, text, 0, 10)
	if !ok {
		t.Fatal("no hover over the workdir")
	}
	if !strings.Contains(msg, "working directory") || !strings.Contains(msg, "`sub`") {
		t.Errorf("hover = %q, want working-directory info", msg)
	}
	// The command name itself still gets the command hover.
	msg, ok = hoverAt(t, text, 0, 2)
	if !ok || !strings.Contains(msg, "**build**") {
		t.Errorf("hover over header name = %q (ok=%v), want the command summary", msg, ok)
	}
}

func TestLSPFileDepHover(t *testing.T) {
	dir := t.TempDir()
	srcDir := filepath.Join(dir, "src")
	os.MkdirAll(srcDir, 0755)
	os.WriteFile(filepath.Join(srcDir, "main.go"), []byte("package main\n"), 0644)
	uri := pathToURI(filepath.Join(dir, "Constfile"))
	text := "build < src/main.go {\n    $ go build ./...\n}\n"
	s := newServer()
	s.updateDoc(uri, text)
	params, _ := json.Marshal(map[string]any{
		"textDocument": map[string]string{"uri": uri},
		"position":     map[string]int{"line": 0, "character": 11},
	})
	res, err := s.handleHover(params)
	if err != nil {
		t.Fatalf("hover: %v", err)
	}
	msg := res.(hoverResult).Contents.Value
	if !strings.Contains(msg, "file dependency") || !strings.Contains(msg, "src/main.go") {
		t.Errorf("hover = %q, want file-dependency info", msg)
	}
	if !strings.Contains(msg, "modified") {
		t.Errorf("hover = %q, want the file's mtime", msg)
	}
}

func TestLSPHeaderArgHover(t *testing.T) {
	text := "deploy (env, opt region) < build {\n    $ echo &env\n}\nbuild {\n    $ echo ok\n}\n"
	// "region" is at column 17.
	msg, ok := hoverAt(t, text, 0, 18)
	if !ok {
		t.Fatal("no hover over an argument declaration")
	}
	if !strings.Contains(msg, "argument of command `deploy`") || !strings.Contains(msg, "--deploy:region") {
		t.Errorf("hover = %q, want argument info with the flag spelling", msg)
	}
	if !strings.Contains(msg, "optional") {
		t.Errorf("hover = %q, want the optional marker", msg)
	}
	// "opt" itself explains the marker.
	msg, ok = hoverAt(t, text, 0, 13)
	if !ok || !strings.Contains(msg, "optional") {
		t.Errorf("hover over opt = %q (ok=%v), want the opt explanation", msg, ok)
	}
}

func TestLSPFunctionCallBeatsKeyword(t *testing.T) {
	// `env` is both a block keyword and a builtin function; in a call
	// position the function hover must win.
	text := "var home = env(\"HOME\")\ncmd {\n    $ echo &home\n}\n"
	at := strings.Index(text[:strings.Index(text, "\n")], "env") + 1
	msg, ok := hoverAt(t, text, 0, at)
	if !ok {
		t.Fatal("no hover over env()")
	}
	if !strings.Contains(msg, "environment variable's value") {
		t.Errorf("hover = %q, want the builtin-function hover", msg)
	}
}

func TestLSPEnvDefaultHover(t *testing.T) {
	os.Unsetenv("CONSTRUCT_LSP_HOVER_MISSING")
	text := "cmd {\n    $ echo @CONSTRUCT_LSP_HOVER_MISSING:-fallback\n}\n"
	at := strings.Index(text, "@CONSTRUCT") + 3
	msg, ok := hoverAt(t, text, 1, at)
	if !ok {
		t.Fatal("no hover over the env ref")
	}
	if !strings.Contains(msg, "default: `fallback`") {
		t.Errorf("hover = %q, want the default value shown", msg)
	}
}
