package pkg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const uiSample = `var greeting = hello

# says hi
hello {
    $ echo hi
}

# builds the app
manual build produces dist/app < src/*.go, hello {
    $ go build
}

|deploy| (env, opt region, opt retries=3) timeout<120s> container "golang:1.26" onchange **.go < build {
    $ echo deploy &env &region
}

_ {
    $ echo default
}
`

func mustDoc(t *testing.T, text string) *UIDoc {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "Constfile")
	if err := os.WriteFile(path, []byte(text), 0644); err != nil {
		t.Fatal(err)
	}
	d, err := NewUIDoc(path)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func docCommand(t *testing.T, d *UIDoc, file, name string) *Command {
	t.Helper()
	data, err := d.parseFile(file)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	cmd, err := data.GetCommand(name)
	if err != nil {
		t.Fatal(err)
	}
	return cmd
}

func TestUIBlockSpans(t *testing.T) {
	d := mustDoc(t, uiSample)
	_, _, blocks, err := d.blocksFor(d.Main)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		name          string
		docStart, end int
		body          string
	}{
		{"hello", 3, 6, "    $ echo hi"},
		{"build", 8, 11, "    $ go build"},
		{"deploy", 13, 15, "    $ echo deploy &env &region"},
		{"_", 17, 19, "    $ echo default"},
	}
	if len(blocks) != len(want) {
		t.Fatalf("got %d blocks, want %d", len(blocks), len(want))
	}
	for i, w := range want {
		b := blocks[i]
		if b.name != w.name || b.docStart != w.docStart || b.end != w.end {
			t.Errorf("block %d = %q doc %d end %d, want %q doc %d end %d", i, b.name, b.docStart, b.end, w.name, w.docStart, w.end)
		}
		lines := strings.Split(d.Files[d.Main].Text, "\n")
		if got := uiBlockBody(lines, b); got != w.body {
			t.Errorf("body of %q = %q, want %q", b.name, got, w.body)
		}
	}
}

func TestUIBlockBoundsTrickyBodies(t *testing.T) {
	cases := []struct {
		text string
		end  int
	}{
		{"cmd { }", 1},
		{"cmd { $ echo one }", 1},
		{"cmd {\n    $ awk '{print $1}'\n}", 3},
		{"cmd {\n    if \"x\" == \"y\" {\n        $ a\n    } else {\n        $ b\n    }\n}", 7},
		{"cmd {\n    for f in a, b {\n        $ echo &f\n    }\n    $ echo ${BRACE}\n}", 6},
		{"cmd {\n    in sub {\n        $ x\n    }\n}", 5},
	}
	for _, c := range cases {
		lines := strings.Split(c.text, "\n")
		end, ok := uiBlockBounds(lines, 1)
		if !ok || end != c.end {
			t.Errorf("uiBlockBounds(%q) = %d ok=%v, want %d", c.text, end, ok, c.end)
		}
	}
}

func TestUIDocNoOpKeepsText(t *testing.T) {
	d := mustDoc(t, uiSample)
	if err := d.Apply(nil); err != nil {
		t.Fatal(err)
	}
	if d.Files[d.Main].Text != uiSample {
		t.Errorf("no-op Apply changed text:\n got: %q\nwant: %q", d.Files[d.Main].Text, uiSample)
	}
}

func TestUIDocMoveCommandCarriesDocComment(t *testing.T) {
	d := mustDoc(t, uiSample)
	before := d.Files[d.Main].Text
	err := d.Apply([]UIEditOp{{File: d.Main, Kind: "moveCommand", Name: "build", Before: strPtr("hello")}})
	if err != nil {
		t.Fatal(err)
	}
	after := d.Files[d.Main].Text
	if after == before {
		t.Fatal("text unchanged after move")
	}
	if !strings.HasPrefix(after, "var greeting = hello\n\n") {
		t.Errorf("glue lines moved:\n%q", after)
	}
	helloIdx := strings.Index(after, "hello {")
	buildIdx := strings.Index(after, "manual build")
	deployIdx := strings.Index(after, "|deploy| (")
	if !(buildIdx < helloIdx && helloIdx < deployIdx) {
		t.Errorf("order wrong after move:\n%s", after)
	}
	commentIdx := strings.LastIndex(after[:buildIdx], "# builds the app")
	if commentIdx < 0 || commentIdx > buildIdx {
		t.Errorf("doc comment did not travel with build:\n%s", after)
	}
}

func strPtr(s string) *string { return &s }

const uiStmtSample = `build {
    $ echo start

    if "&x" == "1" {
        $ echo one
        for f in a, b {
            $ echo &f
        }
    } else {
        $ echo other
    }

    env { CI=true }
    $ echo end
}
`

func TestUIStmtTree(t *testing.T) {
	d := mustDoc(t, uiStmtSample)
	_, lines, blocks, err := d.blocksFor(d.Main)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := uiFindBlock(blocks, "build")
	stmts := uiStmtTree(b.cmd.Body, b.end-1, lines)
	if len(stmts) != 4 {
		t.Fatalf("stmts = %d, want 4", len(stmts))
	}
	want := []struct {
		typ       string
		line, end int
		close     int
	}{
		{"shell", 2, 3, 0},
		{"if", 4, 12, 11},
		{"env", 13, 13, 0},
		{"shell", 14, 14, 0},
	}
	for i, w := range want {
		s := stmts[i]
		if s.Type != w.typ || s.Line != w.line || s.End != w.end || s.Close != w.close {
			t.Errorf("stmt %d = %s %d-%d close %d, want %s %d-%d close %d", i, s.Type, s.Line, s.End, s.Close, w.typ, w.line, w.end, w.close)
		}
	}
	ifKids := stmts[1].Children
	if len(ifKids) != 3 {
		t.Fatalf("if children = %d (%+v), want 3", len(ifKids), ifKids)
	}
	if ifKids[1].Type != "for" || ifKids[1].Line != 6 || ifKids[1].Close != 8 {
		t.Errorf("for stmt = %+v", ifKids[1])
	}
}

func TestUIDocMoveAndDeleteStmt(t *testing.T) {
	d := mustDoc(t, uiStmtSample)
	// move the env block above the if
	err := d.Apply([]UIEditOp{{File: d.Main, Kind: "moveStmt", Name: "build", Line: 13, At: 4}})
	if err != nil {
		t.Fatal(err)
	}
	_, lines, blocks, _ := d.blocksFor(d.Main)
	b, _ := uiFindBlock(blocks, "build")
	stmts := uiStmtTree(b.cmd.Body, b.end-1, lines)
	if stmts[1].Type != "env" || stmts[2].Type != "if" {
		t.Fatalf("order after move: %+v", stmts)
	}
	if !strings.HasPrefix(strings.Join(lines[3:4], "\n"), "    env {") {
		t.Errorf("env not at line 4: %q", lines[3])
	}

	// move into the if block, before the closing brace
	ifClose := stmts[1].Close
	err = d.Apply([]UIEditOp{{File: d.Main, Kind: "moveStmt", Name: "build", Line: stmts[0].Line, At: ifClose}})
	if err != nil {
		t.Fatal(err)
	}
	data, err := d.parseFile(d.Main)
	if err != nil {
		t.Fatalf("parse after nested move: %v", err)
	}
	build, _ := data.GetCommand("build")
	var types []string
	for _, s := range build.Body {
		types = append(types, s.Type)
	}
	// env now nested inside the if's then-body
	var envNested bool
	var walk func(stmts []BodyStatement)
	walk = func(stmts []BodyStatement) {
		for _, s := range stmts {
			if s.Type == StmtEnv {
				envNested = true
			}
			walk(s.ThenBody)
			walk(s.ElseBody)
			walk(s.LoopBody)
		}
	}
	walk(build.Body)
	if !envNested {
		t.Errorf("env not nested after move; top-level types = %v", types)
	}

	// delete the env statement wherever it is
	_, lines, blocks, _ = d.blocksFor(d.Main)
	b, _ = uiFindBlock(blocks, "build")
	stmts = uiStmtTree(b.cmd.Body, b.end-1, lines)
	envStmt := uiFindStmtByLine(stmts, func() int {
		var find func(ss []UIStmt) int
		find = func(ss []UIStmt) int {
			for _, s := range ss {
				if s.Type == "env" {
					return s.Line
				}
				if l := find(s.Children); l > 0 {
					return l
				}
			}
			return 0
		}
		return find(stmts)
	}())
	if envStmt == nil {
		t.Fatal("env statement not found")
	}
	err = d.Apply([]UIEditOp{{File: d.Main, Kind: "deleteStmt", Name: "build", Line: envStmt.Line}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(d.Files[d.Main].Text, "CI=true") {
		t.Error("env statement still present after delete")
	}
	if _, err := d.parseFile(d.Main); err != nil {
		t.Fatalf("parse after delete: %v", err)
	}

	// moving a statement into itself is rejected
	_, lines, blocks, _ = d.blocksFor(d.Main)
	b, _ = uiFindBlock(blocks, "build")
	stmts = uiStmtTree(b.cmd.Body, b.end-1, lines)
	for _, s := range stmts {
		if s.Type == "if" {
			before := d.Files[d.Main].Text
			err := d.Apply([]UIEditOp{{File: d.Main, Kind: "moveStmt", Name: "build", Line: s.Line, At: s.Line + 1}})
			if err == nil {
				t.Error("move into itself accepted")
			}
			if d.Files[d.Main].Text != before {
				t.Error("rejected move was not rolled back")
			}
			break
		}
	}
}

func TestUIDocSetHeaderAndBody(t *testing.T) {
	d := mustDoc(t, uiSample)
	err := d.Apply([]UIEditOp{{
		File: d.Main, Kind: "setHeader", Name: "hello",
		Header: &UIHeaderPatch{
			Prereqs:   &[]string{},
			Timeout:   strPtr("30s"),
			Arguments: []*Argument{{Name: "env"}},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	cmd := docCommand(t, d, d.Main, "hello")
	if cmd.Timeout != "30s" || len(cmd.Arguments) != 1 || cmd.Arguments[0].Name != "env" {
		t.Errorf("setHeader did not apply: %+v", cmd)
	}

	err = d.Apply([]UIEditOp{{
		File: d.Main, Kind: "setBody", Name: "hello",
		Body: strPtr("$ echo changed\n$ echo twice"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if body := uiCommandBody(t, d, "hello"); body != "$ echo changed\n$ echo twice" {
		t.Errorf("setBody = %q", body)
	}
}

func uiCommandBody(t *testing.T, d *UIDoc, name string) string {
	t.Helper()
	_, lines, blocks, err := d.blocksFor(d.Main)
	if err != nil {
		t.Fatal(err)
	}
	b, err := uiFindBlock(blocks, name)
	if err != nil {
		t.Fatal(err)
	}
	return uiBlockBody(lines, b)
}

func TestUIDocInsertDeleteDuplicate(t *testing.T) {
	d := mustDoc(t, uiSample)
	err := d.Apply([]UIEditOp{{
		File: d.Main, Kind: "insertCommand",
		Header: &UIHeaderPatch{
			Name:    strPtr("test"),
			Prereqs: &[]string{"build"},
		},
		Body: strPtr("$ go test ./..."),
	}})
	if err != nil {
		t.Fatal(err)
	}
	cmd := docCommand(t, d, d.Main, "test")
	if len(cmd.Prereqs) != 1 || cmd.Prereqs[0] != "build" {
		t.Errorf("insertCommand prereqs = %v", cmd.Prereqs)
	}
	if body := uiCommandBody(t, d, "test"); body != "$ go test ./..." {
		t.Errorf("insertCommand body = %q", body)
	}

	err = d.Apply([]UIEditOp{{File: d.Main, Kind: "duplicateCommand", Name: "test"}})
	if err != nil {
		t.Fatal(err)
	}
	docCommand(t, d, d.Main, "test-copy")

	err = d.Apply([]UIEditOp{{File: d.Main, Kind: "deleteCommand", Name: "test-copy"}})
	if err != nil {
		t.Fatal(err)
	}
	data, err := d.parseFile(d.Main)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := data.GetCommand("test-copy"); err == nil {
		t.Error("test-copy still present after delete")
	}
}

func TestUIDocGateRejectsBrokenEdit(t *testing.T) {
	d := mustDoc(t, uiSample)
	before := d.Files[d.Main].Text
	// renaming build breaks deploy's prereq reference
	err := d.Apply([]UIEditOp{{File: d.Main, Kind: "setHeader", Name: "build", Header: &UIHeaderPatch{Name: strPtr("built")}}})
	if err == nil {
		t.Fatal("rename with dangling prereq accepted")
	}
	if d.Files[d.Main].Text != before {
		t.Error("rejected op was not rolled back")
	}
	// both sides in one batch passes the gate
	err = d.Apply([]UIEditOp{
		{File: d.Main, Kind: "setHeader", Name: "deploy", Header: &UIHeaderPatch{Prereqs: &[]string{"built"}}},
		{File: d.Main, Kind: "setHeader", Name: "build", Header: &UIHeaderPatch{Name: strPtr("built")}},
	})
	if err != nil {
		t.Fatalf("coordinated rename rejected: %v", err)
	}
	docCommand(t, d, d.Main, "built")
}

func TestUIDocUndoRedo(t *testing.T) {
	d := mustDoc(t, uiSample)
	orig := d.Files[d.Main].Text
	if err := d.Apply([]UIEditOp{{File: d.Main, Kind: "deleteCommand", Name: "deploy"}}); err != nil {
		t.Fatal(err)
	}
	if d.Files[d.Main].Text == orig {
		t.Fatal("delete did nothing")
	}
	if !d.Undo() {
		t.Fatal("nothing to undo")
	}
	if d.Files[d.Main].Text != orig {
		t.Error("undo did not restore text")
	}
	if !d.Redo() {
		t.Fatal("nothing to redo")
	}
	if d.Files[d.Main].Text == orig {
		t.Error("redo did not reapply")
	}
}

func TestUIDocLineOps(t *testing.T) {
	d := mustDoc(t, uiSample)
	err := d.Apply([]UIEditOp{{File: d.Main, Kind: "insertLines", Line: 0, Text: strPtr("var added = 1")}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(d.Files[d.Main].Text, "var added = 1\nvar greeting = hello") {
		t.Errorf("insertLines = %q", d.Files[d.Main].Text[:40])
	}
	// line 1 is now var added; edit it
	err = d.Apply([]UIEditOp{{File: d.Main, Kind: "setLine", Line: 1, Text: strPtr("var added = 2")}})
	if err != nil {
		t.Fatal(err)
	}
	// inside a command: refused
	err = d.Apply([]UIEditOp{{File: d.Main, Kind: "setLine", Line: 5, Text: strPtr("nope")}})
	if err == nil {
		t.Error("setLine inside a command accepted")
	}
	err = d.Apply([]UIEditOp{{File: d.Main, Kind: "deleteLines", Line: 1, EndLine: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(d.Files[d.Main].Text, "var added") {
		t.Error("deleteLines did not remove the line")
	}
}

func TestUIDocImportsMultiFile(t *testing.T) {
	dir := t.TempDir()
	main := `import "lib.constfile"
import "other.constfile" as other

run < lib.build, other.build {
    $ echo &lib.version
}
`
	lib := `var version = 2

build {
    $ make
}
`
	other := `build {
    $ echo other
}
`
	for name, content := range map[string]string{"Constfile": main, "lib.constfile": lib, "other.constfile": other} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	d, err := NewUIDoc(filepath.Join(dir, "Constfile"))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Files) != 3 {
		t.Fatalf("closure = %d files, want 3", len(d.Files))
	}

	st := d.State()
	if len(st.Files) != 3 {
		t.Fatalf("state files = %d", len(st.Files))
	}
	var libFile, otherFile *UIFileState
	for _, f := range st.Files {
		switch f.Rel {
		case "lib.constfile":
			libFile = f
		case "other.constfile":
			otherFile = f
		}
	}
	if libFile == nil || otherFile == nil {
		t.Fatal("imported files missing from state")
	}
	if len(libFile.Commands) != 1 || libFile.Commands[0].Name != "build" {
		t.Fatalf("lib commands = %+v", libFile.Commands)
	}
	// namespaced import shows the merged display name; plain import keeps it
	if otherFile.Commands[0].Display != "other.build" {
		t.Errorf("display name = %q, want other.build", otherFile.Commands[0].Display)
	}
	if libFile.Commands[0].Display != "build" {
		t.Errorf("display name = %q, want build", libFile.Commands[0].Display)
	}
	// the main file sees both imported commands as prereq targets
	if len(st.Files[0].Visible) != 2 {
		t.Errorf("main visible targets = %+v, want 2", st.Files[0].Visible)
	}

	// renaming lib's build alone leaves main's prereq dangling; the parser
	// treats unknown dotted names as file deps, so the gate allows it and
	// lint is what surfaces the problem
	libPath := filepath.Join(dir, "lib.constfile")
	err = d.Apply([]UIEditOp{{File: libPath, Kind: "setHeader", Name: "build", Header: &UIHeaderPatch{Name: strPtr("build2")}}})
	if err != nil {
		t.Fatalf("dangling dotted prereq rejected: %v", err)
	}
	d.Undo()
	// coordinated batch across files passes
	err = d.Apply([]UIEditOp{
		{File: libPath, Kind: "setHeader", Name: "build", Header: &UIHeaderPatch{Name: strPtr("build2")}},
		{File: d.Main, Kind: "setHeader", Name: "run", Header: &UIHeaderPatch{Prereqs: &[]string{"lib.build2", "other.build"}}},
	})
	if err != nil {
		t.Fatalf("cross-file batch rejected: %v", err)
	}

	// add an import line to main; the new file registers from disk
	err = os.WriteFile(filepath.Join(dir, "extra.constfile"), []byte("extra {\n    $ echo e\n}\n"), 0644)
	if err != nil {
		t.Fatal(err)
	}
	err = d.Apply([]UIEditOp{{File: d.Main, Kind: "insertLines", Line: 0, Text: strPtr(`import "extra.constfile"`)}})
	if err != nil {
		t.Fatal(err)
	}
	st = d.State()
	found := false
	for _, f := range st.Files {
		if f.Rel == "extra.constfile" {
			found = true
		}
	}
	if !found {
		t.Error("newly imported file missing from state")
	}
}

func TestUIDocSaveAndConflict(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Constfile")
	if err := os.WriteFile(path, []byte(uiSample), 0644); err != nil {
		t.Fatal(err)
	}
	d, err := NewUIDoc(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Apply([]UIEditOp{{File: path, Kind: "setBody", Name: "hello", Body: strPtr("$ echo changed")}}); err != nil {
		t.Fatal(err)
	}
	saved, conflicts, err := d.Save()
	if err != nil || len(conflicts) != 0 || len(saved) != 1 {
		t.Fatalf("save = %v %v %v", saved, conflicts, err)
	}
	disk, _ := os.ReadFile(path)
	if !strings.Contains(string(disk), "$ echo changed") {
		t.Error("saved file missing the edit")
	}
	if d.DirtyFiles() != nil && len(d.DirtyFiles()) != 0 {
		t.Error("doc still dirty after save")
	}

	// external change after load
	if err := d.Apply([]UIEditOp{{File: path, Kind: "setBody", Name: "hello", Body: strPtr("$ echo again")}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(uiSample+"\n# touched externally\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, conflicts, err = d.Save()
	if err == nil || len(conflicts) != 1 {
		t.Fatalf("expected conflict, got err=%v conflicts=%v", err, conflicts)
	}
}

func TestEmitHeaderRoundTrip(t *testing.T) {
	p := NewParserFromContent("Constfile", uiSample)
	data, err := p.Parse()
	if err != nil {
		t.Fatal(err)
	}
	var bld strings.Builder
	byName := map[string]*Command{}
	for _, c := range data.Commands {
		if IsLazyName(c.Name) || c.SourceFile != "Constfile" {
			continue
		}
		bld.WriteString(EmitHeader(c))
		bld.WriteString("\n    $ x\n}\n\n")
		byName[patchName(c)] = c
	}
	roundData, err := NewParserFromContent("Constfile", bld.String()).Parse()
	if err != nil {
		t.Fatalf("re-parse of emitted headers: %v\n%s", err, bld.String())
	}
	for name, c := range byName {
		got, err := roundData.GetCommand(name)
		if err != nil {
			t.Errorf("command %q not found after emit: %v", name, err)
			continue
		}
		if got.Timeout != c.Timeout || got.Container != c.Container || got.WorkDir != c.WorkDir {
			t.Errorf("%q: timeout/container/workdir = %q/%q/%q, want %q/%q/%q",
				name, got.Timeout, got.Container, got.WorkDir, c.Timeout, c.Container, c.WorkDir)
		}
		if got.CloudAccessible != c.CloudAccessible || got.Manual != c.Manual || got.IsDefault != c.IsDefault {
			t.Errorf("%q: flags = %v/%v/%v, want %v/%v/%v", name,
				got.CloudAccessible, got.Manual, got.IsDefault, c.CloudAccessible, c.Manual, c.IsDefault)
		}
		if !equalStrSet(append(append([]string{}, got.Prereqs...), got.FileDeps...),
			append(append([]string{}, c.Prereqs...), c.FileDeps...)) {
			t.Errorf("%q: prereqs+deps = %v, want %v", name, append(got.Prereqs, got.FileDeps...), append(c.Prereqs, c.FileDeps...))
		}
		if !equalArgs(got.Arguments, c.Arguments) {
			t.Errorf("%q: args = %+v, want %+v", name, got.Arguments, c.Arguments)
		}
		if !equalStrSet(got.Produces, c.Produces) || !equalStrSet(got.OnChange, c.OnChange) {
			t.Errorf("%q: produces/onchange = %v/%v, want %v/%v", name, got.Produces, got.OnChange, c.Produces, c.OnChange)
		}
	}
}

func equalStrSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, s := range a {
		seen[s]++
	}
	for _, s := range b {
		seen[s]--
		if seen[s] < 0 {
			return false
		}
	}
	return true
}

func equalArgs(a, b []*Argument) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name || a[i].IsOptional != b[i].IsOptional || a[i].Default != b[i].Default {
			return false
		}
	}
	return true
}

func TestUIDocCorpusNoOp(t *testing.T) {
	files, _ := filepath.Glob("../examples/Constfile*")
	if len(files) == 0 {
		t.Skip("no corpus")
	}
	for _, f := range files {
		d, err := NewUIDoc(f)
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		if err := d.Apply(nil); err != nil {
			t.Errorf("%s: no-op apply: %v", f, err)
		}
		for p, uf := range d.Files {
			if uf.Text != uf.OrigText {
				t.Errorf("%s: file %s modified by no-op", f, p)
			}
		}
	}
}

func TestUIDocCorpusHeaderRoundTrip(t *testing.T) {
	files, _ := filepath.Glob("../examples/Constfile*")
	if len(files) == 0 {
		t.Skip("no corpus")
	}
	for _, f := range files {
		d, err := NewUIDoc(f)
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		for p := range d.Files {
			data, err := d.parseFile(p)
			if err != nil {
				continue
			}
			for _, c := range data.Commands {
				if IsLazyName(c.Name) || c.SourceFile != p {
					continue
				}
				patch := sameFieldsPatch(c)
				if err := d.Apply([]UIEditOp{{File: p, Kind: "setHeader", Name: c.Name, Header: patch}}); err != nil {
					t.Errorf("%s:%s: identity setHeader: %v", f, c.Name, err)
					d.Undo()
					continue
				}
				after := docCommand(t, d, p, patchName(c))
				if !equalStrSet(append(append([]string{}, after.Prereqs...), after.FileDeps...),
					append(append([]string{}, c.Prereqs...), c.FileDeps...)) ||
					after.Container != c.Container || after.Timeout != c.Timeout ||
					after.WorkDir != c.WorkDir || !equalArgs(after.Arguments, c.Arguments) {
					t.Errorf("%s:%s: header drifted after identity setHeader\n was: %s\n now: %s", f, c.Name, EmitHeader(c), EmitHeader(after))
				}
			}
		}
	}
}

func patchName(c *Command) string {
	if c.IsDefault {
		return "_"
	}
	return c.Name
}

func sameFieldsPatch(c *Command) *UIHeaderPatch {
	prereqs := append([]string{}, c.Prereqs...)
	deps := append([]string{}, c.FileDeps...)
	produces := append([]string{}, c.Produces...)
	onchange := append([]string{}, c.OnChange...)
	args := make([]*Argument, len(c.Arguments))
	for i, a := range c.Arguments {
		cp := *a
		args[i] = &cp
	}
	return &UIHeaderPatch{
		Name:            strPtr(patchName(c)),
		IsDefault:       &c.IsDefault,
		CloudAccessible: &c.CloudAccessible,
		Manual:          &c.Manual,
		Arguments:       args,
		Prereqs:         &prereqs,
		FileDeps:        &deps,
		PrereqDirs:      c.PrereqDirs,
		Produces:        &produces,
		OnChange:        &onchange,
		Container:       strPtr(c.Container),
		Timeout:         strPtr(c.Timeout),
		WorkDir:         strPtr(c.WorkDir),
	}
}
