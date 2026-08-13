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

// TestDefinitionNamespacedPrereq verifies go-to-definition on a namespaced
// prereq ("lib.build") jumps into the imported file's original header.
func TestDefinitionNamespacedPrereq(t *testing.T) {
	dir := t.TempDir()
	libPath := filepath.Join(dir, "lib.constfile")
	os.WriteFile(libPath, []byte("var version = 2\nbuild {\n    echo hi\n}\n"), 0644)
	mainText := "import \"lib.constfile\" as lib\nrelease < lib.build {\n    echo done\n}\n"
	mainPath := filepath.Join(dir, "main.constfile")
	os.WriteFile(mainPath, []byte(mainText), 0644)

	uri := pathToURI(mainPath)
	s := newServer()
	s.updateDoc(uri, mainText, 1)

	lines := strings.Split(mainText, "\n")
	params, _ := json.Marshal(map[string]interface{}{
		"textDocument": map[string]string{"uri": uri},
		"position":     map[string]int{"line": 1, "character": strings.Index(lines[1], "lib.build") + 3},
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
	// The header lives on line 2 of lib.constfile (0-indexed).
	if loc.Range.Start.Line != 1 {
		t.Errorf("definition line = %d, want 1", loc.Range.Start.Line)
	}
}

// TestDefinitionFileDepStillOpens verifies real file deps still open the file
// even after the command-first ordering change.
func TestDefinitionFileDepStillOpens(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "main.go")
	os.WriteFile(srcPath, []byte("package main\n"), 0644)
	mainText := "build < main.go {\n    echo done\n}\n"
	mainPath := filepath.Join(dir, "main.constfile")
	os.WriteFile(mainPath, []byte(mainText), 0644)

	uri := pathToURI(mainPath)
	s := newServer()
	s.updateDoc(uri, mainText, 1)

	lines := strings.Split(mainText, "\n")
	params, _ := json.Marshal(map[string]interface{}{
		"textDocument": map[string]string{"uri": uri},
		"position":     map[string]int{"line": 0, "character": strings.Index(lines[0], "main.go") + 2},
	})
	res, err := s.handleDefinition(params)
	if err != nil {
		t.Fatalf("definition: %v", err)
	}
	loc, ok := res.(location)
	if !ok {
		t.Fatalf("expected location, got %T", res)
	}
	if want := pathToURI(srcPath); loc.URI != want {
		t.Errorf("definition URI = %q, want %q", loc.URI, want)
	}
}

// TestHoverPlainNamedOutput verifies hover on a plain "&cmd.output" ref
// (non-namespaced command) resolves to the referenced command's output.
func TestHoverPlainNamedOutput(t *testing.T) {
	text := `log {
    $ echo hi as out
}
ls in examples {
    $ ls -l as out
}
_ < log, ls {
    echo "&log.out"
    echo "&ls.out"
}
`
	uri := "file:///x/main.constfile"
	s := newServer()
	s.updateDoc(uri, text, 1)

	lines := strings.Split(text, "\n")
	for i := 7; i <= 8; i++ {
		amp := strings.Index(lines[i], "&")
		end := amp + strings.Index(lines[i][amp:], `"`)
		sub := lines[i][amp+1 : end]
		params, _ := json.Marshal(map[string]interface{}{
			"textDocument": map[string]string{"uri": uri},
			"position":     map[string]int{"line": i, "character": amp + 2},
		})
		res, err := s.handleHover(params)
		if err != nil {
			t.Fatalf("hover %s: %v", sub, err)
		}
		hr, ok := res.(hoverResult)
		if !ok {
			t.Fatalf("hover %s: expected hoverResult, got %T", sub, res)
		}
		if !strings.Contains(hr.Contents.Value, "output of") {
			t.Errorf("hover %s should mention the output, got %q", sub, hr.Contents.Value)
		}
	}
}

// TestHoverNamespacedOutput verifies hover on "&lib.gen.0" resolves against
// the namespaced command.
func TestHoverNamespacedOutput(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "lib.constfile"), []byte("gen {\n    echo hi\n}\n"), 0644)
	text := `import "lib.constfile" as lib
use < lib.gen {
    $ echo &lib.gen.0
}
`
	mainPath := filepath.Join(dir, "main.constfile")
	uri := pathToURI(mainPath)
	s := newServer()
	s.updateDoc(uri, text, 1)

	lines := strings.Split(text, "\n")
	params, _ := json.Marshal(map[string]interface{}{
		"textDocument": map[string]string{"uri": uri},
		"position":     map[string]int{"line": 2, "character": strings.Index(lines[2], "&lib.gen.0") + 5},
	})
	res, err := s.handleHover(params)
	if err != nil {
		t.Fatalf("hover: %v", err)
	}
	hr, ok := res.(hoverResult)
	if !ok {
		t.Fatalf("expected hoverResult, got %T", res)
	}
	if !strings.Contains(hr.Contents.Value, "lib.gen") {
		t.Errorf("hover should mention lib.gen, got %q", hr.Contents.Value)
	}
}

// TestDefinitionInvokeTarget verifies command-click on an `invoke <cmd>`
// target jumps to the command's definition, including namespaced targets.
func TestDefinitionInvokeTarget(t *testing.T) {
	dir := t.TempDir()
	libPath := filepath.Join(dir, "lib.constfile")
	os.WriteFile(libPath, []byte("gen {\n    echo hi\n}\n"), 0644)
	mainText := "import \"lib.constfile\" as lib\nuse {\n    invoke lib.gen as lines\n}\n"
	mainPath := filepath.Join(dir, "main.constfile")
	os.WriteFile(mainPath, []byte(mainText), 0644)

	uri := pathToURI(mainPath)
	s := newServer()
	s.updateDoc(uri, mainText, 1)

	lines := strings.Split(mainText, "\n")
	params, _ := json.Marshal(map[string]interface{}{
		"textDocument": map[string]string{"uri": uri},
		"position":     map[string]int{"line": 2, "character": strings.Index(lines[2], "lib.gen") + 3},
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

// TestDefinitionInvokeLocal verifies local invoke targets stay in the doc.
func TestDefinitionInvokeLocal(t *testing.T) {
	text := "gen {\n    echo hi\n}\nuse {\n    invoke gen\n}\n"
	uri := "file:///x/main.constfile"
	s := newServer()
	s.updateDoc(uri, text, 1)

	lines := strings.Split(text, "\n")
	params, _ := json.Marshal(map[string]interface{}{
		"textDocument": map[string]string{"uri": uri},
		"position":     map[string]int{"line": 4, "character": strings.Index(lines[4], "gen") + 2},
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

// TestHoverInvokeTarget verifies hovering an invoke target shows command info.
func TestHoverInvokeTarget(t *testing.T) {
	text := "gen {\n    echo hi\n}\nuse {\n    invoke gen\n}\n"
	uri := "file:///x/main.constfile"
	s := newServer()
	s.updateDoc(uri, text, 1)

	lines := strings.Split(text, "\n")
	params, _ := json.Marshal(map[string]interface{}{
		"textDocument": map[string]string{"uri": uri},
		"position":     map[string]int{"line": 4, "character": strings.Index(lines[4], "gen") + 1},
	})
	res, err := s.handleHover(params)
	if err != nil {
		t.Fatalf("hover: %v", err)
	}
	hr, ok := res.(hoverResult)
	if !ok {
		t.Fatalf("expected hoverResult, got %T", res)
	}
	if !strings.Contains(hr.Contents.Value, "gen") {
		t.Errorf("hover should mention gen, got %q", hr.Contents.Value)
	}
}

// TestCompletionInvokeTarget verifies command-name completion after `invoke`.
func TestCompletionInvokeTarget(t *testing.T) {
	text := "gen {\n    echo hi\n}\nuse {\n    invoke \n}\n"
	uri := "file:///x/main.constfile"
	s := newServer()
	s.updateDoc(uri, text, 1)

	lines := strings.Split(text, "\n")
	params, _ := json.Marshal(map[string]interface{}{
		"textDocument": map[string]string{"uri": uri},
		"position":     map[string]int{"line": 4, "character": len(lines[4])},
	})
	res, err := s.handleCompletion(params)
	if err != nil {
		t.Fatalf("completion: %v", err)
	}
	cl, ok := res.(completionList)
	if !ok {
		t.Fatalf("expected completionList, got %T", res)
	}
	found := false
	for _, item := range cl.Items {
		if item.Label == "gen" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("completion should include `gen`, got %v", cl.Items)
	}
}

// TestHoverLoopVarInvokeCapture verifies &lines in a for-loop clause hovers
// as an invoke capture, not as a working directory.
func TestHoverLoopVarInvokeCapture(t *testing.T) {
	text := `gen {
    echo hi
}
use {
    invoke gen as lines
    for l in &lines {
        $ echo "&l"
    }
}
`
	uri := "file:///x/main.constfile"
	s := newServer()
	s.updateDoc(uri, text, 1)

	lines := strings.Split(text, "\n")
	params, _ := json.Marshal(map[string]interface{}{
		"textDocument": map[string]string{"uri": uri},
		"position":     map[string]int{"line": 5, "character": strings.Index(lines[5], "&lines") + 3},
	})
	res, err := s.handleHover(params)
	if err != nil {
		t.Fatalf("hover: %v", err)
	}
	hr, ok := res.(hoverResult)
	if !ok {
		t.Fatalf("expected hoverResult, got %T", res)
	}
	if strings.Contains(hr.Contents.Value, "working directory") {
		t.Errorf("hover misread &lines as a directory: %q", hr.Contents.Value)
	}
	if !strings.Contains(hr.Contents.Value, "invoke gen as lines") {
		t.Errorf("hover should mention the invoke capture: %q", hr.Contents.Value)
	}
}

// TestHoverLoopVarUnknown no-op: an unknown &ref inside a for loop must not
// produce a file/directory tooltip.
func TestHoverLoopVarUnknown(t *testing.T) {
	text := `use {
    for l in &nope {
        $ echo "&l"
    }
}
`
	uri := "file:///x/main.constfile"
	s := newServer()
	s.updateDoc(uri, text, 1)

	lines := strings.Split(text, "\n")
	params, _ := json.Marshal(map[string]interface{}{
		"textDocument": map[string]string{"uri": uri},
		"position":     map[string]int{"line": 1, "character": strings.Index(lines[1], "&nope") + 3},
	})
	res, err := s.handleHover(params)
	if err != nil {
		t.Fatalf("hover: %v", err)
	}
	if res != nil {
		t.Errorf("expected nil hover for unknown loop var, got %#v", res)
	}
}

// TestDefinitionLoopVarInvokeCapture verifies command-click on an invoke
// capture jumps to the `invoke ... as name` statement.
func TestDefinitionLoopVarInvokeCapture(t *testing.T) {
	text := `gen {
    echo hi
}
use {
    invoke gen as lines
    for l in &lines {
        $ echo "&l"
    }
}
`
	uri := "file:///x/main.constfile"
	s := newServer()
	s.updateDoc(uri, text, 1)

	lines := strings.Split(text, "\n")
	params, _ := json.Marshal(map[string]interface{}{
		"textDocument": map[string]string{"uri": uri},
		"position":     map[string]int{"line": 5, "character": strings.Index(lines[5], "&lines") + 3},
	})
	res, err := s.handleDefinition(params)
	if err != nil {
		t.Fatalf("definition: %v", err)
	}
	loc, ok := res.(location)
	if !ok {
		t.Fatalf("expected location, got %T", res)
	}
	// The invoke statement is line 4 (0-indexed).
	if loc.Range.Start.Line != 4 {
		t.Errorf("definition line = %d, want 4", loc.Range.Start.Line)
	}
	if !strings.Contains(lines[loc.Range.Start.Line], "as lines") {
		t.Errorf("definition should point at the invoke statement, line = %q", lines[loc.Range.Start.Line])
	}
}

// TestCompletionVarWhileTyping verifies completion fires while typing &lo
// (cursor after the letters, not right after the &).
func TestCompletionVarWhileTyping(t *testing.T) {
	text := `var version = "1.0"
gen {
    $ echo hi as out
}
use {
    echo "&ver"
}
`
	uri := "file:///x/main.constfile"
	s := newServer()
	s.updateDoc(uri, text, 1)

	lines := strings.Split(text, "\n")
	params, _ := json.Marshal(map[string]interface{}{
		"textDocument": map[string]string{"uri": uri},
		"position":     map[string]int{"line": 5, "character": len(lines[5]) - 1},
	})
	res, err := s.handleCompletion(params)
	if err != nil {
		t.Fatalf("completion: %v", err)
	}
	cl, ok := res.(completionList)
	if !ok {
		t.Fatalf("expected completionList, got %T", res)
	}
	found := false
	for _, item := range cl.Items {
		if item.Label == "version" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("completion should include `version`, got %v", cl.Items)
	}
}

// TestCompletionOutputAfterDot verifies &gen. completes the command's outputs.
func TestCompletionOutputAfterDot(t *testing.T) {
	text := `gen {
    $ echo hi as out
}
use {
    echo "&gen."
}
`
	uri := "file:///x/main.constfile"
	s := newServer()
	s.updateDoc(uri, text, 1)

	lines := strings.Split(text, "\n")
	params, _ := json.Marshal(map[string]interface{}{
		"textDocument": map[string]string{"uri": uri},
		"position":     map[string]int{"line": 4, "character": len(lines[4]) - 1},
	})
	res, err := s.handleCompletion(params)
	if err != nil {
		t.Fatalf("completion: %v", err)
	}
	cl, ok := res.(completionList)
	if !ok {
		t.Fatalf("expected completionList, got %T", res)
	}
	found := false
	for _, item := range cl.Items {
		if item.Label == "gen.out" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("completion should include `gen.out`, got %v", cl.Items)
	}
}

// TestCompletionNoPrereqOnShellLine verifies shell lines containing '<'
// (e.g. cat << EOF) don't trigger command-name completion.
func TestCompletionNoPrereqOnShellLine(t *testing.T) {
	text := `gen {
    $ echo hi
}
use {
    $ cat << EOF
}
`
	uri := "file:///x/main.constfile"
	s := newServer()
	s.updateDoc(uri, text, 1)

	lines := strings.Split(text, "\n")
	params, _ := json.Marshal(map[string]interface{}{
		"textDocument": map[string]string{"uri": uri},
		"position":     map[string]int{"line": 4, "character": len(lines[4]) - 1},
	})
	res, err := s.handleCompletion(params)
	if err != nil {
		t.Fatalf("completion: %v", err)
	}
	cl, ok := res.(completionList)
	if !ok {
		t.Fatalf("expected completionList, got %T", res)
	}
	for _, item := range cl.Items {
		if item.Label == "gen" {
			t.Errorf("shell line should not complete command names, got %v", cl.Items)
		}
	}
}

// TestHoverPrereqInHeader verifies hovering a prereq name in `_ < log, ls`
// shows that prereq's info, not the header command's.
func TestHoverPrereqInHeader(t *testing.T) {
	text := `log (thing_to_log) {
    $ echo "&thing_to_log"
}
ls in examples {
    $ ls -l
}
_ < log, ls {
    echo done
}
`
	uri := "file:///x/main.constfile"
	s := newServer()
	s.updateDoc(uri, text, 1)

	lines := strings.Split(text, "\n")
	params, _ := json.Marshal(map[string]interface{}{
		"textDocument": map[string]string{"uri": uri},
		"position":     map[string]int{"line": 6, "character": strings.Index(lines[6], "ls") + 1},
	})
	res, err := s.handleHover(params)
	if err != nil {
		t.Fatalf("hover: %v", err)
	}
	hr, ok := res.(hoverResult)
	if !ok {
		t.Fatalf("expected hoverResult, got %T", res)
	}
	if !strings.Contains(hr.Contents.Value, "prerequisite") || !strings.Contains(hr.Contents.Value, "ls") {
		t.Errorf("hover should identify the ls prereq, got %q", hr.Contents.Value)
	}
}

// TestHoverUnknownOutputRef verifies an unmatched &cmd.suffix (e.g. a
// non-existent output) gets no misleading tooltip.
func TestHoverUnknownOutputRef(t *testing.T) {
	text := `log {
    $ echo hi as out
}
_ < log {
    echo "&log.nope"
}
`
	uri := "file:///x/main.constfile"
	s := newServer()
	s.updateDoc(uri, text, 1)

	lines := strings.Split(text, "\n")
	params, _ := json.Marshal(map[string]interface{}{
		"textDocument": map[string]string{"uri": uri},
		"position":     map[string]int{"line": 4, "character": strings.Index(lines[4], "&log.nope") + 5},
	})
	res, err := s.handleHover(params)
	if err != nil {
		t.Fatalf("hover: %v", err)
	}
	if res != nil {
		t.Errorf("expected nil hover for unknown output, got %#v", res)
	}
}

func TestNamedOutputHintsForLoop(t *testing.T) {
	text := `gen {
    $ echo one
    for i in 1, 2 {
        $ echo hi as out
    }
}
_ < gen {
    echo "&gen.out"
}
`
	data := parseForTest(t, text)
	gen, err := data.GetCommand("gen")
	if err != nil {
		t.Fatalf("gen missing: %v", err)
	}
	if countShellLines(gen.Body) != 2 {
		t.Errorf("countShellLines = %d, want 2", countShellLines(gen.Body))
	}
	if !hasNamedOutput(gen.Body, "out") {
		t.Error("hasNamedOutput should find `out` inside the for loop")
	}
	if hint := namedOutputAt(data, "gen", 1); hint != "gen.out" {
		t.Errorf("namedOutputAt(gen, 1) = %q, want gen.out", hint)
	}

	diags := namedOutputHints(text, data)
	for _, d := range diags {
		if d.Severity == sevError {
			t.Errorf("unexpected error diagnostic: %q", d.Message)
		}
	}
}

func TestNamedOutputHintsInvokeCapture(t *testing.T) {
	text := `log {
    $ echo hi
}
ls {
    $ ls -l as out
    invoke log as result
}
_ < log, ls {
    echo "&ls.result"
}
`
	data := parseForTest(t, text)
	diags := namedOutputHints(text, data)
	found := false
	for _, d := range diags {
		if strings.Contains(d.Message, "result") {
			found = true
			if d.Severity != sevWarning {
				t.Errorf("invoke capture should warn (local), got severity %d: %q", d.Severity, d.Message)
			}
			if !strings.Contains(d.Message, "only inside") {
				t.Errorf("warning should mention locality: %q", d.Message)
			}
		}
	}
	if !found {
		t.Errorf("expected a warning for &ls.result, got %v", diags)
	}
}

// TestDefinitionNamedOutput verifies go-to-definition on &cmd.out jumps to// the producing shell statement.
func TestDefinitionNamedOutput(t *testing.T) {
	text := `log {
    $ echo hi as out
    $ echo bye
}
_ < log {
    echo "&log.out"
}
`
	uri := "file:///x/main.constfile"
	s := newServer()
	s.updateDoc(uri, text, 1)

	lines := strings.Split(text, "\n")
	params, _ := json.Marshal(map[string]interface{}{
		"textDocument": map[string]string{"uri": uri},
		"position":     map[string]int{"line": 5, "character": strings.Index(lines[5], "log.out") + 2},
	})
	res, err := s.handleDefinition(params)
	if err != nil {
		t.Fatalf("definition: %v", err)
	}
	loc, ok := res.(location)
	if !ok {
		t.Fatalf("expected location, got %T", res)
	}
	if loc.Range.Start.Line != 1 {
		t.Errorf("definition line = %d, want 1 (the `echo hi as out` line)", loc.Range.Start.Line)
	}
	if !strings.Contains(lines[loc.Range.Start.Line], "echo hi as out") {
		t.Errorf("definition should point at the producing statement, got %q", lines[loc.Range.Start.Line])
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

func TestDocumentSymbol(t *testing.T) {
	text := `build {
    $ echo hi
}
_ < build {
    echo done
}
`
	uri := "file:///x/main.constfile"
	s := newServer()
	s.updateDoc(uri, text, 1)

	params, _ := json.Marshal(map[string]interface{}{
		"textDocument": map[string]string{"uri": uri},
	})
	res, err := s.handleDocumentSymbol(params)
	if err != nil {
		t.Fatalf("documentSymbol: %v", err)
	}
	syms, ok := res.([]documentSymbol)
	if !ok {
		t.Fatalf("expected []documentSymbol, got %T", res)
	}
	if len(syms) != 2 {
		t.Fatalf("symbols = %+v, want 2", syms)
	}
	names := map[string]documentSymbol{}
	for _, sym := range syms {
		names[sym.Name] = sym
	}
	build, ok := names["build"]
	if !ok {
		t.Fatalf("missing build symbol: %v", names)
	}
	if build.SelectionRange.Start.Line != 0 {
		t.Errorf("build selection line = %d", build.SelectionRange.Start.Line)
	}
	if build.Range.End.Line != 2 {
		t.Errorf("build range end = %d, want 2 (closing brace)", build.Range.End.Line)
	}
	if _, ok := names["_"]; !ok {
		t.Errorf("default command symbol missing: %v", names)
	}
}
func TestEnvRefDefaultInWorkdir(t *testing.T) {
	os.Unsetenv("CONSTRUCT_LSP_DEF_DIR")
	defer os.Unsetenv("CONSTRUCT_LSP_DEF_DIR")
	if got := resolveEnvRefsInString("sub/@CONSTRUCT_LSP_DEF_DIR:-src"); got != "sub/src" {
		t.Errorf("resolveEnvRefsInString default = %q", got)
	}
	os.Setenv("CONSTRUCT_LSP_DEF_DIR", "real")
	if got := resolveEnvRefsInString("sub/@CONSTRUCT_LSP_DEF_DIR:-src"); got != "sub/real" {
		t.Errorf("resolveEnvRefsInString set = %q", got)
	}
}

// TestDocumentSymbolImportedCommands verifies imported commands don't emit
// symbols: their SourceLine belongs to the imported file, and a bogus
// selection range would violate VS Code's containment validation.
func TestDocumentSymbolImportedCommands(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "lib.constfile"), []byte("gen {\n    echo hi\n}\n"), 0644)
	text := `import "lib.constfile" as lib
main < lib.gen {
    echo done
}
`
	mainPath := filepath.Join(dir, "main.constfile")
	uri := pathToURI(mainPath)
	s := newServer()
	s.updateDoc(uri, text, 1)

	params, _ := json.Marshal(map[string]interface{}{
		"textDocument": map[string]string{"uri": uri},
	})
	res, err := s.handleDocumentSymbol(params)
	if err != nil {
		t.Fatalf("documentSymbol: %v", err)
	}
	syms, ok := res.([]documentSymbol)
	if !ok {
		t.Fatalf("expected []documentSymbol, got %T", res)
	}
	for _, sym := range syms {
		if strings.Contains(sym.Name, "gen") && !strings.Contains(sym.Name, "main") {
			t.Errorf("imported command leaked into symbols: %+v", sym)
		}
		for _, r := range []range_{sym.Range, sym.SelectionRange} {
			if r.Start.Line > r.End.Line ||
				(r.Start.Line == r.End.Line && r.Start.Character > r.End.Character) {
				t.Errorf("inverted range for %s: %+v", sym.Name, r)
			}
		}
	}
}

// TestHoverFailContext verifies &fail.* refs inside an onfail block get a
// context hint, and stay quiet outside one.
func TestHoverFailContext(t *testing.T) {
	text := `deploy {
    onfail {
        $ echo "&fail.message at &fail.line"
    }
    $ echo "&fail.message"
}
`
	uri := "file:///x/main.constfile"
	s := newServer()
	s.updateDoc(uri, text, 1)
	lines := strings.Split(text, "\n")

	hover := func(line int, sub string) string {
		params, _ := json.Marshal(map[string]interface{}{
			"textDocument": map[string]string{"uri": uri},
			"position":     map[string]int{"line": line, "character": strings.Index(lines[line], sub) + 2},
		})
		res, err := s.handleHover(params)
		if err != nil {
			t.Fatalf("hover %s: %v", sub, err)
		}
		if res == nil {
			return ""
		}
		return res.(hoverResult).Contents.Value
	}

	if msg := hover(2, "fail.message"); !strings.Contains(msg, "triggered this") {
		t.Errorf("inside onfail hover = %q", msg)
	}
	if msg := hover(2, "fail.line"); !strings.Contains(msg, "source line") {
		t.Errorf("fail.line hover = %q", msg)
	}
	if msg := hover(4, "fail.message"); msg != "" {
		t.Errorf("outside onfail should hover nothing, got %q", msg)
	}
}

// TestHoverOnFailKeyword verifies the onfail header shows the available
// failure context.
func TestHoverOnFailKeyword(t *testing.T) {
	text := "deploy {\n    onfail {\n        $ echo x\n    }\n}\n"
	uri := "file:///x/main.constfile"
	s := newServer()
	s.updateDoc(uri, text, 1)

	params, _ := json.Marshal(map[string]interface{}{
		"textDocument": map[string]string{"uri": uri},
		"position":     map[string]int{"line": 1, "character": 3},
	})
	res, err := s.handleHover(params)
	if err != nil {
		t.Fatalf("hover: %v", err)
	}
	hr, ok := res.(hoverResult)
	if !ok {
		t.Fatalf("expected hoverResult, got %T", res)
	}
	if !strings.Contains(hr.Contents.Value, "&fail.message") {
		t.Errorf("onfail hover should list context vars: %q", hr.Contents.Value)
	}
}

// TestCompletionFailContext verifies &fail. completes inside onfail blocks
// only.
func TestCompletionFailContext(t *testing.T) {
	text := `deploy {
    onfail {
        $ echo "&fail."
    }
    $ echo "&fail."
}
`
	uri := "file:///x/main.constfile"
	s := newServer()
	s.updateDoc(uri, text, 1)
	lines := strings.Split(text, "\n")

	complete := func(line int) []string {
		params, _ := json.Marshal(map[string]interface{}{
			"textDocument": map[string]string{"uri": uri},
			"position":     map[string]int{"line": line, "character": len(lines[line]) - 1},
		})
		res, err := s.handleCompletion(params)
		if err != nil {
			t.Fatalf("completion: %v", err)
		}
		cl := res.(completionList)
		var labels []string
		for _, it := range cl.Items {
			labels = append(labels, it.Label)
		}
		return labels
	}

	inside := complete(2)
	found := false
	for _, l := range inside {
		if l == "fail.message" {
			found = true
		}
	}
	if !found {
		t.Errorf("inside onfail should suggest fail.message: %v", inside)
	}
	for _, l := range complete(4) {
		if l == "fail.message" {
			t.Errorf("outside onfail should not suggest fail.*: %v", l)
		}
	}
}
