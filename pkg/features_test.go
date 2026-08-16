package pkg

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"time"

	"github.com/spf13/pflag"
)

// chdir changes the process working directory for the rest of the test and
// restores it afterwards: Windows refuses to delete a directory that is any
// process's cwd, so t.TempDir cleanup fails without the restore.
func chdir(t *testing.T, dir string) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(orig); err != nil {
			t.Errorf("restore cwd: %v", err)
		}
	})
}

func TestElseIfParsing(t *testing.T) {
	in := `build {
    if "1" == "1" {
        $ echo one
    } else if "2" == "2" {
        $ echo two
    } else {
        $ echo three
    }
}`
	lines := strings.Split(in, "\n")
	p := &Parser{Data: &ParsedData{}, Lines: lines}
	if _, err := p.parseCommand(0, "build {", false, false, 1, ""); err != nil {
		t.Fatalf("parse: %v", err)
	}
	cmd := p.Data.Commands[0]
	if len(cmd.Body) != 1 || cmd.Body[0].Type != "if" {
		t.Fatalf("expected single if, got %#v", cmd.Body)
	}
	outer := cmd.Body[0]
	if len(outer.ElseBody) != 1 || outer.ElseBody[0].Type != "if" {
		t.Fatalf("expected nested else-if, got %#v", outer.ElseBody)
	}
	elseIf := outer.ElseBody[0]
	if elseIf.Cond != `"2" == "2"` {
		t.Errorf("else-if cond = %q, want \"2\" == \"2\"", elseIf.Cond)
	}
	if len(elseIf.ElseBody) != 1 || elseIf.ElseBody[0].Type != "shell" || elseIf.ElseBody[0].Shell != "$ echo three" {
		t.Errorf("else-if else body = %#v", elseIf.ElseBody)
	}
}

func TestElseIfExecution(t *testing.T) {
	data := &ParsedData{
		Commands: []*Command{{
			Name: "testif",
			Body: []BodyStatement{
				{
					Type: "if",
					Cond: `"1" == "2"`,
					ThenBody: []BodyStatement{
						{Type: "shell", Shell: exitNonZero()},
					},
					ElseBody: []BodyStatement{
						{
							Type: "if",
							Cond: `"2" == "2"`,
							ThenBody: []BodyStatement{
								{Type: "shell", Shell: "echo else-if-ran"},
							},
							ElseBody: []BodyStatement{
								{Type: "shell", Shell: exitNonZero()},
							},
						},
					},
				},
			},
		}},
	}
	data.buildIndexMaps()
	executor := NewExecutor(data, false, false)
	if err := executor.Execute([]string{"testif"}); err != nil {
		t.Fatalf("else-if branch should run without error, got: %v", err)
	}
}

// --- logical operators ---

func TestEvaluateConditionLogical(t *testing.T) {
	tests := []struct {
		cond string
		want bool
	}{
		{`"1" == "1" && "2" == "2"`, true},
		{`"1" == "1" && "2" == "3"`, false},
		{`"1" == "2" || "2" == "2"`, true},
		{`"1" == "2" || "2" == "3"`, false},
		{`!"1" == "1"`, false},
		{`!"1" == "2"`, true},
		{`("1" == "1" || "1" == "2") && "2" == "2"`, true},
		{`("1" == "2" || "1" == "2") && "2" == "2"`, false},
		{`"windows/amd64" contains "windows" && "2" >= "2"`, true},
		{`"a||b" == "a||b"`, true}, // operator inside quotes is not split
		{`"a&&b" == "a&&b"`, true},
		{`"3" > "2" && "3" < "4"`, true},
		{`"1 > 0" == "1 > 0"`, true}, // comparison ops inside quotes are not split
		{`"x > y" > "a"`, true},
		{`"a < b" < "c"`, true},
		{`"2 <= 3" contains "2"`, true},
		{`"a contains b" == "a contains b"`, true},
	}
	for _, tt := range tests {
		if got := evaluateCondition(tt.cond); got != tt.want {
			t.Errorf("evaluateCondition(%q) = %v, want %v", tt.cond, got, tt.want)
		}
	}
}

func TestErrorToleranceMarker(t *testing.T) {
	data := &ParsedData{
		Commands: []*Command{
			{Name: "tolerant", Body: shellBody("! "+exitNonZero(), "echo after")},
		},
	}
	data.buildIndexMaps()
	executor := NewExecutor(data, false, false)
	if err := executor.Execute([]string{"tolerant"}); err != nil {
		t.Fatalf("error-tolerant statement should not abort, got: %v", err)
	}
}

func TestErrorToleranceStillFailsWithoutMarker(t *testing.T) {
	data := &ParsedData{
		Commands: []*Command{
			{Name: "strict", Body: shellBody(exitNonZero())},
		},
	}
	data.buildIndexMaps()
	executor := NewExecutor(data, false, false)
	if err := executor.Execute([]string{"strict"}); err == nil {
		t.Fatal("expected failure without marker")
	}
}

func TestDotNamedCommandNotFileDep(t *testing.T) {
	in := `build.lsp {
    echo hi
}
main < build.lsp {
    echo done
}`
	p := NewParserFromContent("test.constfile", in)
	data, err := p.Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	main, _ := data.GetCommand("main")
	if len(main.Prereqs) != 1 || main.Prereqs[0] != "build.lsp" {
		t.Errorf("Prereqs = %v, want [build.lsp]", main.Prereqs)
	}
	if len(main.FileDeps) != 0 {
		t.Errorf("FileDeps = %v, want []", main.FileDeps)
	}
}

func TestImportMerge(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "lib.constfile"), []byte(`var shared = from-lib
libcmd {
    echo lib-output
}
`), 0644)
	os.WriteFile(filepath.Join(dir, "main.constfile"), []byte(`import "lib.constfile"
usecmd < libcmd {
    $ echo &shared and &libcmd.0
}
`), 0644)

	p, err := NewParser(filepath.Join(dir, "main.constfile"))
	if err != nil {
		t.Fatalf("NewParser: %v", err)
	}
	data, err := p.Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(data.Commands) != 2 {
		t.Fatalf("expected 2 commands (merged), got %d", len(data.Commands))
	}
	libcmd, err := data.GetCommand("libcmd")
	if err != nil {
		t.Fatalf("imported command missing: %v", err)
	}
	_ = libcmd
	usecmd, err := data.GetCommand("usecmd")
	if err != nil {
		t.Fatalf("usecmd missing: %v", err)
	}
	if len(usecmd.Prereqs) != 1 || usecmd.Prereqs[0] != "libcmd" {
		t.Errorf("cross-file prereq not classified: %v", usecmd.Prereqs)
	}
	usecmd.PrereqCmds = []*Command{libcmd}
	libcmd.PrereqOutput = []string{"lib-output"}
	if err := executorEval(data, usecmd); err != nil {
		t.Fatalf("exec failed: %v", err)
	}
}

func executorEval(data *ParsedData, cmd *Command) error {
	executor := NewExecutor(data, false, false)
	cmd.Prereqs = []string{}
	return executor.EvaluateCommand(cmd)
}

func TestImportCircular(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.constfile"), []byte(`import "b.constfile"
`), 0644)
	os.WriteFile(filepath.Join(dir, "b.constfile"), []byte(`import "a.constfile"
`), 0644)

	p, err := NewParser(filepath.Join(dir, "a.constfile"))
	if err != nil {
		t.Fatalf("NewParser: %v", err)
	}
	if _, err := p.Parse(); err == nil || !strings.Contains(err.Error(), "circular import") {
		t.Fatalf("expected circular import error, got %v", err)
	}
}

func TestImportDuplicateCommand(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "lib.constfile"), []byte(`dup {
    echo one
}
`), 0644)
	os.WriteFile(filepath.Join(dir, "main.constfile"), []byte(`import "lib.constfile"
dup {
    echo two
}
`), 0644)

	p, err := NewParser(filepath.Join(dir, "main.constfile"))
	if err != nil {
		t.Fatalf("NewParser: %v", err)
	}
	if _, err := p.Parse(); err == nil || !strings.Contains(err.Error(), "duplicate command") {
		t.Fatalf("expected duplicate command error, got %v", err)
	}
}

func TestImportDiamond(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "d.constfile"), []byte(`libcmd {
    echo lib
}
`), 0644)
	os.WriteFile(filepath.Join(dir, "b.constfile"), []byte(`import "d.constfile"
`), 0644)
	os.WriteFile(filepath.Join(dir, "c.constfile"), []byte(`import "d.constfile"
`), 0644)
	os.WriteFile(filepath.Join(dir, "main.constfile"), []byte(`import "b.constfile"
import "c.constfile"
usecmd < libcmd {
    $ echo got:&libcmd.0
}
`), 0644)

	p, err := NewParser(filepath.Join(dir, "main.constfile"))
	if err != nil {
		t.Fatalf("NewParser: %v", err)
	}
	data, err := p.Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	count := 0
	for _, cmd := range data.Commands {
		if cmd.Name == "libcmd" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("libcmd merged %d times, want 1", count)
	}
	if _, err := data.GetCommand("usecmd"); err != nil {
		t.Fatalf("usecmd missing: %v", err)
	}
}

func TestImportNamespace(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "lib.constfile"), []byte(`var version = 2
libcmd {
    $ echo lib &version
}
use < libcmd {
    for l in &libcmd.* {
        $ echo got:&l
    }
}
shadow {
    var version = 3
    $ echo local &version
}
`), 0644)
	os.WriteFile(filepath.Join(dir, "main.constfile"), []byte(`import "lib.constfile" as lib
usecmd < lib.use {
    $ echo &lib.version / &lib.use.0
}
`), 0644)

	p, err := NewParser(filepath.Join(dir, "main.constfile"))
	if err != nil {
		t.Fatalf("NewParser: %v", err)
	}
	data, err := p.Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	libcmd, err := data.GetCommand("lib.libcmd")
	if err != nil {
		t.Fatalf("namespaced command missing: %v", err)
	}
	if len(libcmd.Prereqs) != 0 {
		t.Errorf("libcmd prereqs = %v", libcmd.Prereqs)
	}
	libuse, err := data.GetCommand("lib.use")
	if err != nil {
		t.Fatalf("namespaced use missing: %v", err)
	}
	if len(libuse.Prereqs) != 1 || libuse.Prereqs[0] != "lib.libcmd" {
		t.Errorf("namespaced prereq not rewritten: %v", libuse.Prereqs)
	}
	if !strings.Contains(libuse.Body[0].LoopItems, "&lib.libcmd.*") {
		t.Errorf("loop items not rewritten: %q", libuse.Body[0].LoopItems)
	}
	if !strings.Contains(libuse.Body[0].LoopBody[0].Shell, "&l") {
		t.Errorf("loop body broken: %q", libuse.Body[0].LoopBody[0].Shell)
	}
	shadowCmd, err := data.GetCommand("lib.shadow")
	if err != nil {
		t.Fatalf("shadow command missing: %v", err)
	}
	if !strings.Contains(shadowCmd.Body[0].Shell, "&version") || strings.Contains(shadowCmd.Body[0].Shell, "&lib.version") {
		t.Errorf("shadowed local ref rewritten: %q", shadowCmd.Body[0].Shell)
	}
	// Global variable renamed.
	if v, ok := data.LookupVariable("lib.version", "global"); !ok || v != "2" {
		t.Errorf("namespaced variable = %q, %v; want \"2\", true", v, ok)
	}
	// Command-scoped variable scope follows the renamed command.
	if v, ok := data.LookupVariable("version", "lib.shadow"); !ok || v != "3" {
		t.Errorf("scoped variable = %q, %v; want \"3\", true", v, ok)
	}
	// Watch files include the import.
	if !slices.Contains(data.SourceFiles, filepath.Join(dir, "lib.constfile")) {
		t.Errorf("SourceFiles missing import: %v", data.SourceFiles)
	}

	usecmd, err := data.GetCommand("usecmd")
	if err != nil {
		t.Fatalf("usecmd missing: %v", err)
	}
	// Execute: prereq output refs across the namespace must resolve.
	usecmd.PrereqCmds = []*Command{libuse}
	libuse.PrereqOutput = []string{"got:lib 2"}
	if err := executorEval(data, usecmd); err != nil {
		t.Fatalf("exec failed: %v", err)
	}
}

func TestImportNamespaceSameFileTwice(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "d.constfile"), []byte(`libcmd {
    echo lib
}
`), 0644)
	os.WriteFile(filepath.Join(dir, "main.constfile"), []byte(`import "d.constfile" as a
import "d.constfile" as b
a1 < a.libcmd {
    $ echo a
}
b1 < b.libcmd {
    $ echo b
}
`), 0644)

	p, err := NewParser(filepath.Join(dir, "main.constfile"))
	if err != nil {
		t.Fatalf("NewParser: %v", err)
	}
	data, err := p.Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, err := data.GetCommand("a.libcmd"); err != nil {
		t.Errorf("a.libcmd missing: %v", err)
	}
	if _, err := data.GetCommand("b.libcmd"); err != nil {
		t.Errorf("b.libcmd missing: %v", err)
	}
}

func TestImportNamespaceInvalid(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "lib.constfile"), []byte(`libcmd {
    echo lib
}
`), 0644)
	os.WriteFile(filepath.Join(dir, "main.constfile"), []byte(`import "lib.constfile" as bad-name!
`), 0644)

	p, err := NewParser(filepath.Join(dir, "main.constfile"))
	if err != nil {
		t.Fatalf("NewParser: %v", err)
	}
	if _, err := p.Parse(); err == nil || !strings.Contains(err.Error(), "namespace") {
		t.Fatalf("expected namespace error, got %v", err)
	}
}

func TestInvokeStatement(t *testing.T) {
	in := `gen {
    $ echo hello
}
use {
    invoke gen
    invoke gen as captured
    $ echo "final: &captured"
}
selfref {
    invoke selfref
}
`
	p := NewParserFromContent("t.constfile", in)
	data, err := p.Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	use, err := data.GetCommand("use")
	if err != nil {
		t.Fatalf("use missing: %v", err)
	}
	if use.Body[0].Type != "invoke" || use.Body[0].Shell != "gen" {
		t.Errorf("invoke stmt not parsed: %+v", use.Body[0])
	}
	if use.Body[1].Type != "invoke" || use.Body[1].Shell != "gen" || use.Body[1].OutputName != "captured" {
		t.Errorf("invoke as stmt not parsed: %+v", use.Body[1])
	}
	if err := executorEval(data, use); err != nil {
		t.Fatalf("exec: %v", err)
	}
	if v, ok := data.LookupVariable("captured", "use"); !ok || v != "hello" {
		t.Errorf("captured invoke output = %q, %v; want \"hello\", true", v, ok)
	}

	selfref, err := data.GetCommand("selfref")
	if err != nil {
		t.Fatalf("selfref missing: %v", err)
	}
	if err := executorEval(data, selfref); err == nil || !strings.Contains(err.Error(), "circular invoke") {
		t.Errorf("expected circular invoke error, got %v", err)
	}
}

func TestInvokeCaptureVisibleAfter(t *testing.T) {
	in := `gen {
    $ echo hello
}
use {
    invoke gen as captured
    $ echo "final: &captured"
}
`
	p := NewParserFromContent("t.constfile", in)
	data, err := p.Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	use, err := data.GetCommand("use")
	if err != nil {
		t.Fatalf("use missing: %v", err)
	}
	r, _, restore := captureStreams(t)
	err = executorEval(data, use)
	restore()
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	out, _ := io.ReadAll(r)
	if got := strings.TrimSpace(string(out)); got != "final: hello" {
		t.Errorf("stdout = %q, want \"final: hello\"", got)
	}
}

func TestEnvBlock(t *testing.T) {
	in := `build {
    env { FOO=bar, BAZ=qux }
    $ echo "env: $FOO $BAZ"
}
`
	p := NewParserFromContent("t.constfile", in)
	data, err := p.Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	cmd, err := data.GetCommand("build")
	if err != nil {
		t.Fatalf("build missing: %v", err)
	}

	if len(cmd.Body) != 2 || cmd.Body[0].Type != "env" {
		t.Fatalf("env stmt not parsed: %+v", cmd.Body)
	}

	if len(cmd.Body[0].Env) != 2 || cmd.Body[0].Env[0] != "FOO=bar" {
		t.Errorf("env pairs = %v", cmd.Body[0].Env)
	}

	if err := executorEval(data, cmd); err != nil {
		t.Fatalf("exec: %v", err)
	}

	if v, ok := data.LookupVariable("FOO", "build"); !ok || v != "bar" {
		t.Errorf("env var as &ref = %q, %v; want \"bar\", true", v, ok)
	}
}

func TestEnvBlockAmpRef(t *testing.T) {
	in := `build {
    env { MODE=fast, N=2 }
    $ echo "mode=&MODE"
    for i in 1..&N {
        $ echo "iter &i @MODE"
    }
}
`
	p := NewParserFromContent("t.constfile", in)
	data, err := p.Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cmd, err := data.GetCommand("build")
	if err != nil {
		t.Fatalf("build missing: %v", err)
	}
	r, _, restore := captureStreams(t)
	err = executorEval(data, cmd)
	restore()
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	out, _ := io.ReadAll(r)
	got := strings.ReplaceAll(string(out), "\r\n", "\n")
	if got != "mode=fast\niter 1 fast\niter 2 fast\n" {
		t.Errorf("stdout = %q, want &refs and @refs resolved", got)
	}
}

func TestEnvBlockNested(t *testing.T) {
	in := `build {
    env { MODE=base }
    if "@MODE" == "base" {
        env { MODE=nested, EXTRA=1 }
    }
    $ echo "mode=@MODE extra=@EXTRA"
    for i in 1, 2 {
        env { LOOP=on }
        $ echo "loop=@LOOP i=&i"
    }
    $ echo "after=@LOOP"
}
`
	p := NewParserFromContent("t.constfile", in)
	data, err := p.Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cmd, err := data.GetCommand("build")
	if err != nil {
		t.Fatalf("build missing: %v", err)
	}
	// The shell line inside the if branch must see MODE=nested, not base.
	if len(cmd.Body) != 5 || cmd.Body[1].Type != "if" {
		t.Fatalf("unexpected body: %+v", cmd.Body)
	}
	if err := executorEval(data, cmd); err != nil {
		t.Fatalf("exec: %v", err)
	}
	if v, ok := data.LookupVariable("i", "build"); !ok || v != "2" {
		t.Errorf("loop var not set: %q, %v", v, ok)
	}
}

func TestBodyComments(t *testing.T) {
	in := `build {
    // setup phase
    $ echo https://example.com
    $ echo "# not a construct comment" 
    # hash comment
    echo done
}`
	p := NewParserFromContent("t.constfile", in)
	data, err := p.Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cmd, _ := data.GetCommand("build")
	var shells []string
	for _, b := range cmd.Body {
		if b.Type == "shell" {
			shells = append(shells, b.Shell)
		}
	}
	want := []string{"$ echo https://example.com", "$ echo \"# not a construct comment\"", "echo done"}
	if len(shells) != len(want) {
		t.Fatalf("body statements = %v, want %v", shells, want)
	}
	for i := range want {
		if shells[i] != want[i] {
			t.Errorf("body[%d] = %q, want %q", i, shells[i], want[i])
		}
	}
}

func TestMatrixParsing(t *testing.T) {
	in := `build {
    matrix os in windows, linux; arch in amd64, arm64 {
        $ echo &os/&arch
    }
}`
	p := NewParserFromContent("t.constfile", in)
	data, err := p.Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cmd, _ := data.GetCommand("build")
	if len(cmd.Body) != 1 || cmd.Body[0].Type != "for" {
		t.Fatalf("expected outer for, got %#v", cmd.Body)
	}
	outer := cmd.Body[0]
	if outer.LoopVar != "os" || outer.LoopItems != "windows, linux" {
		t.Errorf("outer loop = %s in %s", outer.LoopVar, outer.LoopItems)
	}
	if len(outer.LoopBody) != 1 || outer.LoopBody[0].Type != "for" {
		t.Fatalf("expected inner for, got %#v", outer.LoopBody)
	}
	inner := outer.LoopBody[0]
	if inner.LoopVar != "arch" || inner.LoopItems != "amd64, arm64" {
		t.Errorf("inner loop = %s in %s", inner.LoopVar, inner.LoopItems)
	}
	if len(inner.LoopBody) != 1 || inner.LoopBody[0].Type != "shell" {
		t.Errorf("inner body = %#v", inner.LoopBody)
	}
}

func TestMatrixExecution(t *testing.T) {
	data := &ParsedData{
		Commands: []*Command{{
			Name: "build",
			Body: []BodyStatement{
				{
					Type:      "for",
					LoopVar:   "os",
					LoopItems: "windows, linux",
					LoopBody: []BodyStatement{
						{
							Type:      "for",
							LoopVar:   "arch",
							LoopItems: "amd64, arm64",
							LoopBody: []BodyStatement{
								{Type: "shell", Shell: "echo &os/&arch"},
							},
						},
					},
				},
			},
		}},
	}
	data.buildIndexMaps()
	executor := NewExecutor(data, false, false)

	so, sw, _ := os.Pipe()
	oldOut := os.Stdout
	os.Stdout = sw
	err := executor.Execute([]string{"build"})
	os.Stdout = oldOut
	sw.Close()
	out, _ := io.ReadAll(so)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}

	var saw []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		saw = append(saw, line)
	}
	want := []string{"windows/amd64", "windows/arm64", "linux/amd64", "linux/arm64"}
	if len(saw) != len(want) {
		t.Fatalf("combinations = %v, want %v", saw, want)
	}
	for i := range want {
		if saw[i] != want[i] {
			t.Errorf("combination[%d] = %q, want %q", i, saw[i], want[i])
		}
	}
}

func TestMatrixMalformed(t *testing.T) {
	for _, in := range []string{
		"build {\n    matrix os windows, linux {\n        echo hi\n    }\n}",
		"build {\n    matrix os in {\n        echo hi\n    }\n}",
	} {
		p := NewParserFromContent("t.constfile", in)
		if _, err := p.Parse(); err == nil {
			t.Errorf("expected parse error for %q", in)
		}
	}
}

func TestMatrixInsideIf(t *testing.T) {
	in := `build {
    if 1 == 1 {
        matrix os in linux, windows {
            echo &os
        }
    }
    echo done
}`
	p := NewParserFromContent("t.constfile", in)
	data, err := p.Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	build, err := data.GetCommand("build")
	if err != nil {
		t.Fatalf("build missing: %v", err)
	}
	if len(build.Body) != 2 {
		t.Fatalf("build body = %d statements, want 2 (if + echo)", len(build.Body))
	}
	if build.Body[0].Type != "if" || build.Body[1].Type != "shell" {
		t.Errorf("unexpected body layout: %+v", build.Body)
	}
	if len(build.Body[0].ThenBody) != 1 || build.Body[0].ThenBody[0].Type != "for" {
		t.Errorf("matrix should parse as a for block inside the if, got %+v", build.Body[0].ThenBody)
	}
}

func TestLoopContinueBreak(t *testing.T) {
	data := &ParsedData{
		Commands: []*Command{{
			Name: "loop",
			Body: []BodyStatement{
				{
					Type:      "for",
					LoopVar:   "n",
					LoopItems: "1, 2, 3, 4, 5",
					LoopBody: []BodyStatement{
						{
							Type: "if",
							Cond: `"&n" == "2"`,
							ThenBody: []BodyStatement{
								{Type: "continue"},
							},
						},
						{
							Type: "if",
							Cond: `"&n" == "4"`,
							ThenBody: []BodyStatement{
								{Type: "break"},
							},
						},
						{Type: "shell", Shell: "echo n=&n"},
					},
				},
			},
		}},
	}
	data.buildIndexMaps()

	out := captureStdoutFor(t, func() error {
		return NewExecutor(data, false, false).Execute([]string{"loop"})
	})
	want := "n=1\nn=3\n"
	if out != want {
		t.Errorf("output = %q, want %q", out, want)
	}
}

func TestContinueIfSugar(t *testing.T) {
	in := `build {
    continue if "&os" == "windows"
    break if "&n" == "5"
}`
	p := NewParserFromContent("t.constfile", in)
	data, err := p.Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cmd, _ := data.GetCommand("build")
	if len(cmd.Body) != 2 {
		t.Fatalf("expected 2 statements, got %#v", cmd.Body)
	}
	if cmd.Body[0].Type != "if" || cmd.Body[0].Cond != `"&os" == "windows"` ||
		len(cmd.Body[0].ThenBody) != 1 || cmd.Body[0].ThenBody[0].Type != "continue" {
		t.Errorf("continue-if = %#v", cmd.Body[0])
	}
	if cmd.Body[1].Type != "if" || cmd.Body[1].Cond != `"&n" == "5"` ||
		len(cmd.Body[1].ThenBody) != 1 || cmd.Body[1].ThenBody[0].Type != "break" {
		t.Errorf("break-if = %#v", cmd.Body[1])
	}
}

func TestContinueOutsideLoop(t *testing.T) {
	for _, body := range []string{"continue", "break"} {
		data := &ParsedData{
			Commands: []*Command{
				{Name: "cmd", Body: []BodyStatement{{Type: body}}},
			},
		}
		data.buildIndexMaps()
		executor := NewExecutor(data, false, false)
		err := executor.Execute([]string{"cmd"})
		if err == nil {
			t.Errorf("expected error for top-level %q", body)
		}
	}
}

func TestMatrixContinueIf(t *testing.T) {
	data := &ParsedData{
		Commands: []*Command{{
			Name: "build",
			Body: []BodyStatement{
				{
					Type:      "for",
					LoopVar:   "os",
					LoopItems: "windows, linux",
					LoopBody: []BodyStatement{
						{
							Type:      "for",
							LoopVar:   "arch",
							LoopItems: "amd64, arm64",
							LoopBody: []BodyStatement{
								{
									Type:     "if",
									Cond:     `"&os" == "windows" && "&arch" == "arm64"`,
									ThenBody: []BodyStatement{{Type: "continue"}},
								},
								{Type: "shell", Shell: "echo &os/&arch"},
							},
						},
					},
				},
			},
		}},
	}
	data.buildIndexMaps()

	out := captureStdoutFor(t, func() error {
		return NewExecutor(data, false, false).Execute([]string{"build"})
	})
	want := "windows/amd64\nlinux/amd64\nlinux/arm64\n"
	if out != want {
		t.Errorf("output = %q, want %q", out, want)
	}
}

func TestShellContinueEscapes(t *testing.T) {
	in := `build {
    $ continue
}`
	p := NewParserFromContent("t.constfile", in)
	data, err := p.Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cmd, _ := data.GetCommand("build")
	if cmd.Body[0].Type != "shell" || cmd.Body[0].Shell != "$ continue" {
		t.Errorf("expected shell statement '$ continue', got %#v", cmd.Body[0])
	}
}

func TestSingleLineBlocks(t *testing.T) {
	in := `build {
    if "&n" == "1" { continue }
    if "&x" == "1" { echo then } else { echo else }
    if "&x" == "1" { echo a } else if "&x" == "2" { echo b }
    for f in a, b { echo &f }
    matrix os in windows, linux; arch in amd64 { echo &os/&arch }
    if "&y" == "1" { echo one; echo two }
}`
	p := NewParserFromContent("t.constfile", in)
	data, err := p.Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cmd, _ := data.GetCommand("build")
	if len(cmd.Body) != 6 {
		t.Fatalf("expected 6 statements, got %d: %#v", len(cmd.Body), cmd.Body)
	}

	if s := cmd.Body[0]; s.Type != "if" || len(s.ThenBody) != 1 || s.ThenBody[0].Type != "continue" {
		t.Errorf("single-line continue-if = %#v", s)
	}
	if s := cmd.Body[1]; s.Type != "if" || len(s.ThenBody) != 1 || len(s.ElseBody) != 1 {
		t.Errorf("single-line if-else = %#v", s)
	}
	if s := cmd.Body[2]; s.Type != "if" || len(s.ElseBody) != 1 || s.ElseBody[0].Type != "if" {
		t.Errorf("single-line else-if = %#v", s)
	}
	if s := cmd.Body[3]; s.Type != "for" || s.LoopVar != "f" || len(s.LoopBody) != 1 {
		t.Errorf("single-line for = %#v", s)
	}
	if s := cmd.Body[4]; s.Type != "for" || s.LoopVar != "os" {
		t.Errorf("single-line matrix = %#v", s)
	}
	if s := cmd.Body[5]; s.Type != "if" || len(s.ThenBody) != 2 {
		t.Errorf("single-line semicolon body = %#v", s)
	}
}

func TestSingleLineBlocksExecution(t *testing.T) {
	data := &ParsedData{
		Commands: []*Command{{
			Name: "build",
			Body: []BodyStatement{
				{
					Type:      "for",
					LoopVar:   "n",
					LoopItems: "1, 2, 3",
					LoopBody: []BodyStatement{
						{Type: "if", Cond: `"&n" == "2"`, ThenBody: []BodyStatement{{Type: "continue"}}},
						{Type: "shell", Shell: "echo n=&n"},
					},
				},
			},
		}},
	}
	data.buildIndexMaps()
	out := captureStdoutFor(t, func() error {
		return NewExecutor(data, false, false).Execute([]string{"build"})
	})
	if out != "n=1\nn=3\n" {
		t.Errorf("output = %q, want n=1/n=3", out)
	}
}

func TestElseWithoutIf(t *testing.T) {
	in := `build {
    else { echo nope }
}`
	p := NewParserFromContent("t.constfile", in)
	if _, err := p.Parse(); err == nil || !strings.Contains(err.Error(), "else") {
		t.Fatalf("expected else error, got %v", err)
	}
}

func TestArgumentDefaults(t *testing.T) {
	in := `deploy (opt env=prod) {
    $ echo "env is &env"
}`
	p := NewParserFromContent("t.constfile", in)
	data, err := p.Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cmd, _ := data.GetCommand("deploy")
	if len(cmd.Arguments) != 1 || cmd.Arguments[0].Name != "env" || cmd.Arguments[0].Default != "prod" || !cmd.Arguments[0].IsOptional {
		t.Fatalf("args = %#v", cmd.Arguments)
	}

	flagSet := pflag.NewFlagSet("t", pflag.ContinueOnError)
	executor := NewExecutor(data, false, false)
	executor.RegisterArgumentFlags(flagSet)

	// Unset: default applies.
	if out := captureStdoutFor(t, func() error {
		return executor.Execute([]string{"deploy"})
	}); out != "env is prod\n" {
		t.Errorf("default output = %q", out)
	}

	// Explicit flag overrides.
	if err := flagSet.Parse([]string{"--deploy:env=staging"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if out := captureStdoutFor(t, func() error {
		return executor.Execute([]string{"deploy"})
	}); out != "env is staging\n" {
		t.Errorf("override output = %q", out)
	}
}

func TestEscapeHatch(t *testing.T) {
	// Shell lines: \& and \@ pass through literally.
	if got := resolveVarRefs(`echo "\&foo \@BAR"`, func(string) (string, bool) { return "X", true }); got != `echo "&foo \@BAR"` {
		t.Errorf("resolveVarRefs escape = %q", got)
	}
	if got := ResolveEnvRefs(`echo "\&foo \@BAR"`); got != `echo "\&foo @BAR"` {
		t.Errorf("resolveEnvRefs escape = %q", got)
	}

	// Variable values: \&, \@, \$ are literal.
	p := &Parser{Data: &ParsedData{}}
	p.Data.Variables = append(p.Data.Variables, &Variable{Name: "foo", Value: "F", Scope: "global"})
	name, scope := "x", "global"
	if got := p.tryEvalExpression(`a \&foo b \@HOME c \$5`, &name, &scope, 1); got != `a &foo b @HOME c $5` {
		t.Errorf("tryEvalExpression escape = %q", got)
	}
}

func TestNumericRanges(t *testing.T) {
	rng, ok := expandRange("1..5")
	if !ok || strings.Join(rng, ",") != "1,2,3,4,5" {
		t.Errorf("1..5 = %v ok=%v", rng, ok)
	}
	rng, ok = expandRange("5..3")
	if !ok || strings.Join(rng, ",") != "5,4,3" {
		t.Errorf("5..3 = %v ok=%v", rng, ok)
	}
	if _, ok := expandRange("a..b"); ok {
		t.Error("a..b should not expand")
	}
	if _, ok := expandRange("1.."); ok {
		t.Error("1.. should not expand")
	}

	data := &ParsedData{
		Commands: []*Command{{
			Name: "count",
			Body: []BodyStatement{
				{
					Type:      "for",
					LoopVar:   "i",
					LoopItems: "1..3",
					LoopBody:  []BodyStatement{{Type: "shell", Shell: "echo i=&i"}},
				},
			},
		}},
	}
	data.buildIndexMaps()
	out := captureStdoutFor(t, func() error {
		return NewExecutor(data, false, false).Execute([]string{"count"})
	})
	if out != "i=1\ni=2\ni=3\n" {
		t.Errorf("range output = %q", out)
	}
}

func TestDuplicateValidation(t *testing.T) {
	in := `var x = 1
var x = 2
build {
    echo hi
}`
	p := NewParserFromContent("t.constfile", in)
	if _, err := p.Parse(); err == nil || !strings.Contains(err.Error(), "duplicate variable") {
		t.Fatalf("expected duplicate variable error, got %v", err)
	}

	in2 := `build (a, a) {
    echo hi
}`
	p2 := NewParserFromContent("t2.constfile", in2)
	if _, err := p2.Parse(); err == nil || !strings.Contains(err.Error(), "duplicate argument") {
		t.Fatalf("expected duplicate argument error, got %v", err)
	}
}

// TestEnvRefsInShellLines verifies @ENV refs resolve inside shell lines.
func TestEnvRefsInShellLines(t *testing.T) {
	t.Setenv("CONSTRUCT_TEST_SHELL_ENV", "shell-env-value")
	data := &ParsedData{
		Commands: []*Command{
			{Name: "envtest", Body: shellBody(`$ echo "@CONSTRUCT_TEST_SHELL_ENV"`)},
		},
	}
	data.buildIndexMaps()
	out := captureStdoutFor(t, func() error {
		return NewExecutor(data, false, false).Execute([]string{"envtest"})
	})
	if out != "shell-env-value\n" {
		t.Errorf("output = %q, want env value", out)
	}
}

func TestEnvRefsInShellLinesUnsetLiteral(t *testing.T) {
	if got := resolveEnvRefsKeepUnset(`npx @vscode/vsce@latest`); got != `npx @vscode/vsce@latest` {
		t.Errorf("npm scoped package mangled: %q", got)
	}
	if got := resolveEnvRefsKeepUnset(`echo @CONSTRUCT_DEFINITELY_UNSET`); got != `echo @CONSTRUCT_DEFINITELY_UNSET` {
		t.Errorf("unset ref should stay literal, got %q", got)
	}
	t.Setenv("CONSTRUCT_TEST_SHELL_ENV2", "v")
	if got := resolveEnvRefsKeepUnset(`echo @CONSTRUCT_TEST_SHELL_ENV2 and @NOPE`); got != `echo v and @NOPE` {
		t.Errorf("set/unset mix = %q", got)
	}
	if got := resolveEnvRefsKeepUnset(`echo \@CONSTRUCT_TEST_SHELL_ENV2`); got != `echo @CONSTRUCT_TEST_SHELL_ENV2` {
		t.Errorf("escaped ref should stay literal, got %q", got)
	}
}

// TestBuiltinConditionFunctions verifies exists/missing/glob in conditions.
func TestBuiltinConditionFunctions(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "present.txt"), []byte("x"), 0644)
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("x"), 0644)
	os.WriteFile(filepath.Join(dir, "b.go"), []byte("x"), 0644)

	existsPath := filepath.Join(dir, "present.txt")
	missingPath := filepath.Join(dir, "nope.txt")
	globPat := filepath.Join(dir, "*.go")

	tests := []struct {
		cond string
		want bool
	}{
		{`exists("` + existsPath + `")`, true},
		{`exists("` + missingPath + `")`, false},
		{`missing("` + missingPath + `")`, true},
		{`missing("` + existsPath + `")`, false},
		{`glob("` + globPat + `")`, true},
		{`glob("` + filepath.Join(dir, "*.rs") + `")`, false},
		{`exists("` + existsPath + `") && "1" == "1"`, true},
		{`exists("` + missingPath + `") || "1" == "1"`, true},
		{`!exists("` + missingPath + `")`, true},
		{`missing("` + missingPath + `") && exists("` + existsPath + `")`, true},
	}
	for _, tt := range tests {
		if got := evaluateCondition(tt.cond); got != tt.want {
			t.Errorf("evaluateCondition(%q) = %v, want %v", tt.cond, got, tt.want)
		}
	}
}

func TestBuiltinConditionFunctionsNotShell(t *testing.T) {
	in := `build {
    if exists("nonexistent-file-xyz") {
        $ echo "exists"
    } else {
        $ echo "missing"
    }
}`
	p := NewParserFromContent("t.constfile", in)
	data, err := p.Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cmd, _ := data.GetCommand("build")
	if cmd.Body[0].Type != "if" {
		t.Fatalf("expected if statement, got %#v", cmd.Body[0])
	}
	if cmd.Body[0].Cond != `exists("nonexistent-file-xyz")` {
		t.Errorf("cond = %q", cmd.Body[0].Cond)
	}
}

// TestLoopIndex verifies "for i, f in ..." binds a 0-based index variable.
func TestLoopIndex(t *testing.T) {
	in := `build {
    for i, f in a, b, c {
        $ echo "&i:&f"
    }
}`
	p := NewParserFromContent("t.constfile", in)
	data, err := p.Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cmd, _ := data.GetCommand("build")
	if cmd.Body[0].LoopVar != "f" || cmd.Body[0].LoopIndex != "i" {
		t.Fatalf("loop = %#v", cmd.Body[0])
	}

	out := captureStdoutFor(t, func() error {
		return NewExecutor(data, false, false).Execute([]string{"build"})
	})
	if out != "0:a\n1:b\n2:c\n" {
		t.Errorf("output = %q, want 0:a/1:b/2:c", out)
	}
}

func TestWorkDirBaseDir(t *testing.T) {
	base := t.TempDir()
	sub := filepath.Join(base, "src")
	os.Mkdir(sub, 0755)
	os.WriteFile(filepath.Join(sub, "data.txt"), []byte("from-base"), 0644)

	// Run from a DIFFERENT directory to prove the base dir is used.
	other := t.TempDir()
	chdir(t, other)

	data := &ParsedData{
		Commands: []*Command{
			{Name: "gen", WorkDir: "src", Body: []BodyStatement{{Type: "shell", Shell: "cat data.txt"}}},
			{Name: "main", Prereqs: []string{"gen"}, Body: shellBody("echo done")},
		},
	}
	data.buildIndexMaps()

	executor := NewExecutor(data, false, false)
	executor.SetBaseDir(base)
	if err := executor.Execute([]string{"main"}); err != nil {
		t.Fatalf("exec: %v", err)
	}
	gen, _ := data.GetCommand("gen")
	if len(gen.PrereqOutput) != 1 || gen.PrereqOutput[0] != "from-base" {
		t.Errorf("prereq output = %#v, want [from-base] (workdir must anchor to base dir)", gen.PrereqOutput)
	}
}

func TestDefaultWorkDirAnchorsToBase(t *testing.T) {
	base := t.TempDir()
	os.WriteFile(filepath.Join(base, "data.txt"), []byte("base-data"), 0644)

	// Run from a different directory to prove the base dir is used.
	other := t.TempDir()
	chdir(t, other)

	data := &ParsedData{
		Commands: []*Command{
			{Name: "show", Body: []BodyStatement{{Type: "shell", Shell: "cat data.txt"}}},
		},
	}
	data.buildIndexMaps()
	executor := NewExecutor(data, false, false)
	executor.SetBaseDir(base)

	so, sw, _ := os.Pipe()
	oldOut := os.Stdout
	os.Stdout = sw
	err := executor.Execute([]string{"show"})
	os.Stdout = oldOut
	sw.Close()
	out, _ := io.ReadAll(so)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if strings.TrimSpace(string(out)) != "base-data" {
		t.Errorf("output = %q, want %q (no workdir must anchor to base dir)", strings.TrimSpace(string(out)), "base-data")
	}
}

func TestBuiltinConditionFunctionsBase(t *testing.T) {
	base := t.TempDir()
	os.Mkdir(filepath.Join(base, "src"), 0755)
	os.WriteFile(filepath.Join(base, "src", "artifact.bin"), []byte("x"), 0644)

	other := t.TempDir()
	chdir(t, other)

	data := &ParsedData{
		Commands: []*Command{
			{Name: "check", WorkDir: "src", Body: []BodyStatement{
				{Type: "if", Cond: `exists("artifact.bin")`,
					ThenBody: []BodyStatement{{Type: "shell", Shell: "echo present"}},
					ElseBody: []BodyStatement{{Type: "shell", Shell: "echo absent"}}},
			}},
		},
	}
	data.buildIndexMaps()

	executor := NewExecutor(data, false, false)
	executor.SetBaseDir(base)
	out := captureStdoutFor(t, func() error {
		return executor.Execute([]string{"check"})
	})
	if out != "present\n" {
		t.Errorf("output = %q, want present (exists must anchor to workdir/base)", out)
	}
}

// TestConditionInOperator verifies list-membership conditions.
func TestConditionInOperator(t *testing.T) {
	tests := []struct {
		cond string
		want bool
	}{
		{`"windows" in "windows, linux"`, true},
		{`"linux" in "windows, linux"`, true},
		{`"darwin" in "windows, linux"`, false},
		{`"windows" in "windows"`, true},
		{`"x" in ""`, false},
		{`" a " in "a, b"`, false},          // exact match, no trimming of the operand
		{`"a" in "a , b"`, true},            // list items are trimmed
		{`windows in windows, linux`, true}, // unquoted operands
		{`"a in b" == "c"`, false},          // " in " inside quotes is not the operator
		{`"windows" in "windows, linux" && "1" == "1"`, true},
		{`"windows" in "linux" || "1" == "1"`, true},
		{`!"windows" in "linux"`, true},
	}
	for _, tt := range tests {
		if got := evaluateCondition(tt.cond); got != tt.want {
			t.Errorf("evaluateCondition(%q) = %v, want %v", tt.cond, got, tt.want)
		}
	}
}

// TestOutputIteration verifies &prereq.* iterates captured outputs in order.
func TestOutputIteration(t *testing.T) {
	data := &ParsedData{
		Commands: []*Command{
			{Name: "gen", Body: shellBody("echo one", "echo two", "echo three")},
			{
				Name:       "use",
				Prereqs:    []string{"gen"},
				PrereqCmds: []*Command{{Name: "gen", PrereqOutput: []string{"one", "two", "three"}}},
				Body: []BodyStatement{
					{
						Type:      "for",
						LoopVar:   "line",
						LoopItems: "&gen.*",
						LoopBody:  []BodyStatement{{Type: "shell", Shell: "echo got:&line"}},
					},
				},
			},
		},
	}
	data.buildIndexMaps()

	for i, out := range []string{"one", "two", "three"} {
		data.SetVariable(fmt.Sprintf("gen.%d", i), "use", out)
	}

	out := captureStdoutFor(t, func() error {
		return NewExecutor(data, false, false).Execute([]string{"use"})
	})
	want := "got:one\ngot:two\ngot:three\n"
	if out != want {
		t.Errorf("output = %q, want %q", out, want)
	}
}

func TestOutputIterationEmpty(t *testing.T) {
	data := &ParsedData{
		Commands: []*Command{
			{Name: "quiet", Body: shellBody("true")},
			{
				Name:       "use",
				Prereqs:    []string{"quiet"},
				PrereqCmds: []*Command{{Name: "quiet", PrereqOutput: nil}},
				Body: []BodyStatement{
					{
						Type:      "for",
						LoopVar:   "line",
						LoopItems: "&quiet.*",
						LoopBody:  []BodyStatement{{Type: "shell", Shell: "echo ran:&line"}},
					},
					{Type: "shell", Shell: "echo done"},
				},
			},
		},
	}
	data.buildIndexMaps()

	out := captureStdoutFor(t, func() error {
		return NewExecutor(data, false, false).Execute([]string{"use"})
	})
	if out != "done\n" {
		t.Errorf("output = %q, want just done (zero iterations)", out)
	}
}

func TestProducesUpToDate(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.go")
	art := filepath.Join(dir, "out.bin")
	os.WriteFile(src, []byte("v1"), 0644)
	os.WriteFile(art, []byte("built"), 0644)
	now := time.Now()
	os.Chtimes(src, now, now)
	os.Chtimes(art, now, now.Add(time.Hour))

	data := &ParsedData{
		Commands: []*Command{{
			Name: "build", Produces: []string{"out.bin"}, FileDeps: []string{"src.go"},
			Body: shellBody("echo BUILDING"),
		}},
	}
	data.buildIndexMaps()

	executor := NewExecutor(data, false, false)
	executor.SetBaseDir(dir)

	out := captureStdoutFor(t, func() error {
		return executor.Execute([]string{"build"})
	})
	if out != "(build up to date)\n" {
		t.Errorf("expected up-to-date skip, got %q", out)
	}

	// A newer dependency forces a rebuild.
	os.Chtimes(src, now.Add(2*time.Hour), now.Add(2*time.Hour))
	out = captureStdoutFor(t, func() error {
		return executor.Execute([]string{"build"})
	})
	if out != "BUILDING\n" {
		t.Errorf("expected rebuild, got %q", out)
	}
}

// TestProducesParsing verifies the produces clause parses from the header.
func TestProducesParsing(t *testing.T) {
	in := `build produces dist/app, dist/lib in sub < src/*.go { 
    echo hi
}`
	p := NewParserFromContent("t.constfile", in)
	data, err := p.Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cmd, _ := data.GetCommand("build")
	if len(cmd.Produces) != 2 || cmd.Produces[0] != "dist/app" || cmd.Produces[1] != "dist/lib" {
		t.Errorf("Produces = %v", cmd.Produces)
	}
	if cmd.WorkDir != "sub" || len(cmd.FileDeps) != 1 || cmd.FileDeps[0] != "src/*.go" {
		t.Errorf("WorkDir = %q, FileDeps = %v", cmd.WorkDir, cmd.FileDeps)
	}
}

func TestCacheKeyIncludesArgs(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)
	os.WriteFile("dep.txt", []byte("x"), 0644)

	data := &ParsedData{
		Commands: []*Command{{
			Name: "build", Arguments: []*Argument{{Name: "cfg"}}, FileDeps: []string{"dep.txt"},
			Body: shellBody("echo BUILD"),
		}},
	}
	data.buildIndexMaps()

	run := func(cfg string) {
		fs := pflag.NewFlagSet("t", pflag.ContinueOnError)
		executor := NewExecutor(data, false, false)
		executor.SetBaseDir(dir)
		executor.RegisterArgumentFlags(fs)
		if err := fs.Parse([]string{"--build:cfg=" + cfg}); err != nil {
			t.Fatalf("parse: %v", err)
		}
		if err := executor.Execute([]string{"build"}); err != nil {
			t.Fatalf("exec: %v", err)
		}
	}
	run("debug")
	run("release")

	fc := loadFileCache(filepath.Join(dir, cacheDir))
	if len(fc) != 2 {
		t.Errorf("manifest has %d entries, want 2 (one per config)", len(fc))
	}
}

func TestLoadEnvFile(t *testing.T) {
	t.Setenv("PRECEDENCE_TEST", "existing")
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	os.WriteFile(path, []byte("# comment\nFOO=bar\nQUOTED=\"hi there\"\nPRECEDENCE_TEST=ignored\nUNQUOTED_SPACES=a b c\n"), 0644)

	for _, key := range []string{"FOO", "QUOTED", "UNQUOTED_SPACES"} {
		if prev, ok := os.LookupEnv(key); ok {
			t.Cleanup(func() { os.Setenv(key, prev) })
		} else {
			t.Cleanup(func() { os.Unsetenv(key) })
		}
	}

	if err := LoadEnvFile(path); err != nil {
		t.Fatalf("LoadEnvFile: %v", err)
	}
	if os.Getenv("FOO") != "bar" {
		t.Errorf("FOO = %q, want bar", os.Getenv("FOO"))
	}
	if os.Getenv("QUOTED") != "hi there" {
		t.Errorf("QUOTED = %q, want 'hi there'", os.Getenv("QUOTED"))
	}
	if os.Getenv("PRECEDENCE_TEST") != "existing" {
		t.Errorf("existing env var was overridden: %q", os.Getenv("PRECEDENCE_TEST"))
	}
	if os.Getenv("UNQUOTED_SPACES") != "a b c" {
		t.Errorf("UNQUOTED_SPACES = %q", os.Getenv("UNQUOTED_SPACES"))
	}
}

func TestRequireBuiltin(t *testing.T) {
	if got := evaluateCondition(`require("sh")`); !got {
		t.Error("require(sh) should be true on POSIX shells")
	}
	if got := evaluateCondition(`require("definitely-not-a-real-tool-xyz")`); got {
		t.Error("require of a nonexistent tool should be false")
	}
	if got := evaluateCondition(`require("sh") && "1" == "1"`); !got {
		t.Error("require composes with &&")
	}
}

func TestJobsLimit(t *testing.T) {
	data := &ParsedData{
		Commands: []*Command{
			{Name: "a", Body: shellBody("echo a")},
			{Name: "b", Body: shellBody("echo b")},
			{Name: "all", Prereqs: []string{"a", "b"}, Body: shellBody("echo all")},
		},
	}
	data.buildIndexMaps()

	executor := NewExecutor(data, true, false)
	executor.SetJobs(1)
	if err := executor.Execute([]string{"all"}); err != nil {
		t.Fatalf("exec with jobs=1 failed: %v", err)
	}
}

func captureStdoutFor(t *testing.T, run func() error) string {
	t.Helper()
	so, sw, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	oldOut := os.Stdout
	os.Stdout = sw
	runErr := run()
	os.Stdout = oldOut
	sw.Close()
	out, _ := io.ReadAll(so)
	if runErr != nil {
		t.Fatalf("exec: %v", runErr)
	}
	return strings.ReplaceAll(string(out), "\r\n", "\n")
}

func TestImportDefaultCommandConflict(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.constfile"), []byte(`_ {
    echo default-a
}
`), 0644)
	os.WriteFile(filepath.Join(dir, "b.constfile"), []byte(`_ {
    echo default-b
}
`), 0644)
	os.WriteFile(filepath.Join(dir, "main.constfile"), []byte(`import "a.constfile"
import "b.constfile"
`), 0644)

	p, err := NewParser(filepath.Join(dir, "main.constfile"))
	if err != nil {
		t.Fatalf("NewParser: %v", err)
	}
	if _, err := p.Parse(); err == nil || !strings.Contains(err.Error(), `duplicate command "_"`) {
		t.Fatalf("expected duplicate command error for two defaults, got %v", err)
	}
}

func TestPerPrereqWorkDir(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "data.txt"), []byte("in-dir"), 0644)
	sub := filepath.Join(dir, "src")
	os.Mkdir(sub, 0755)
	os.WriteFile(filepath.Join(sub, "data.txt"), []byte("in-src"), 0644)

	data := &ParsedData{
		Commands: []*Command{
			{Name: "gen", Body: []BodyStatement{{Type: "shell", Shell: "cat data.txt"}}},
			{Name: "main", Prereqs: []string{"gen"}, PrereqDirs: map[string]string{"gen": "src"},
				Body: shellBody("echo done")},
		},
	}
	data.buildIndexMaps()
	executor := NewExecutor(data, false, false)
	chdir(t, dir)
	if err := executor.Execute([]string{"main"}); err != nil {
		t.Fatalf("exec failed: %v", err)
	}
	gen, _ := data.GetCommand("gen")
	if len(gen.PrereqOutput) != 1 || gen.PrereqOutput[0] != "in-src" {
		t.Errorf("prereq ran in wrong dir: output %#v, want [in-src]", gen.PrereqOutput)
	}
}

func TestDagParallelPrereqs(t *testing.T) {
	data := &ParsedData{
		Commands: []*Command{
			{Name: "slow", Body: shellBody("sleep 0.4")},
			{Name: "fast", Body: shellBody("echo fast-done")},
			{Name: "main", Prereqs: []string{"slow", "fast"}, Body: shellBody("echo main-done")},
		},
	}
	data.buildIndexMaps()

	start := time.Now()
	executor := NewExecutor(data, true, false)
	if err := executor.Execute([]string{"main"}); err != nil {
		t.Fatalf("exec failed: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed > 700*time.Millisecond {
		t.Errorf("prereqs did not run in parallel: took %v", elapsed)
	}
}

func TestDagSharedPrereqOnce(t *testing.T) {
	data := &ParsedData{
		Commands: []*Command{
			{Name: "gen", Body: shellBody("echo shared")},
			{Name: "a", Prereqs: []string{"gen"}, Body: shellBody("$ echo a:&gen.0")},
			{Name: "b", Prereqs: []string{"gen"}, Body: shellBody("$ echo b:&gen.0")},
		},
	}
	data.buildIndexMaps()
	executor := NewExecutor(data, true, false)
	if err := executor.Execute([]string{"a", "b"}); err != nil {
		t.Fatalf("exec failed: %v", err)
	}
	gen, _ := data.GetCommand("gen")
	if len(gen.PrereqOutput) != 1 {
		t.Errorf("shared prereq ran %d times, want 1", len(gen.PrereqOutput))
	}
}

func TestShellBatchingOrder(t *testing.T) {
	data := &ParsedData{
		Commands: []*Command{
			{Name: "multi", Body: shellBody("echo one", "echo two", "echo three")},
		},
	}
	data.buildIndexMaps()
	out := runAndCapture(t, data, "multi")
	if out != "one\ntwo\nthree" {
		t.Errorf("batched output = %q, want %q", out, "one\ntwo\nthree")
	}
}

func TestShellBatchingIsolation(t *testing.T) {
	data := &ParsedData{
		Commands: []*Command{
			{Name: "iso", Body: shellBody(
				"cd /tmp && pwd",
				"pwd",
				"export CONSTRUCT_TEST_LEAK=bar",
				"echo LEAK=$CONSTRUCT_TEST_LEAK",
			)},
		},
	}
	data.buildIndexMaps()
	out := runAndCapture(t, data, "iso")
	lines := strings.Split(out, "\n")
	if len(lines) != 3 {
		t.Fatalf("output = %q", out)
	}
	if lines[0] != "/tmp" {
		t.Errorf("first statement should run in /tmp, got %q", lines[0])
	}
	if lines[1] == "/tmp" {
		t.Errorf("cd leaked to the next statement: pwd = %q", lines[1])
	}
	if lines[2] != "LEAK=" {
		t.Errorf("export leaked across statements: %q", lines[2])
	}
}

func TestShellBatchingTolerance(t *testing.T) {
	data := &ParsedData{
		Commands: []*Command{
			{Name: "tolerant", Body: shellBody("!"+exitNonZero(), "echo after")},
			{Name: "strict", Body: shellBody(exitNonZero(), "echo never")},
		},
	}
	data.buildIndexMaps()
	if out := runAndCapture(t, data, "tolerant"); out != "after" {
		t.Errorf("tolerant batch = %q, want %q", out, "after")
	}
	if err := runErr(data, "strict"); err == nil {
		t.Error("strict batch should fail when a statement fails")
	}
}

func TestShellBatchingMixedTolerance(t *testing.T) {
	data := &ParsedData{
		Commands: []*Command{
			{Name: "mix", Body: shellBody("echo first", "!"+exitNonZero(), "echo last")},
		},
	}
	data.buildIndexMaps()
	if out := runAndCapture(t, data, "mix"); out != "first\nlast" {
		t.Errorf("mixed batch = %q, want %q", out, "first\nlast")
	}
}

func TestShellBatchingInLoop(t *testing.T) {
	data := &ParsedData{
		Commands: []*Command{
			{Name: "loopy", Body: []BodyStatement{
				{Type: "for", LoopVar: "i", LoopItems: "1, 2", LoopBody: shellBody("echo a&i", "echo b&i")},
			}},
		},
	}
	data.buildIndexMaps()
	if out := runAndCapture(t, data, "loopy"); out != "a1\nb1\na2\nb2" {
		t.Errorf("loop batch = %q", out)
	}
}

func TestDollarPrefixedTolerantLine(t *testing.T) {
	data := &ParsedData{
		Commands: []*Command{
			{Name: "tol", Body: shellBody("echo first", "$ ! "+exitNonZero(), "echo last")},
		},
	}
	data.buildIndexMaps()
	if out := runAndCapture(t, data, "tol"); out != "first\nlast" {
		t.Errorf("output = %q, want %q", out, "first\nlast")
	}
}

func TestPrereqBodyBatchedWhenOutputsUnreferenced(t *testing.T) {
	data := &ParsedData{
		Commands: []*Command{
			{Name: "quiet", Body: shellBody("echo one", "echo two")},
			{Name: "top", Prereqs: []string{"quiet"}, Body: shellBody("echo done")},
		},
	}
	data.buildIndexMaps()
	if out := runAndCapture(t, data, "top"); out != "done" {
		t.Errorf("output = %q, want %q (prereq stdout stays captured)", out, "done")
	}
}

func TestNeedsShellIsolation(t *testing.T) {
	mutating := []string{
		"cd /tmp && pwd",
		"export FOO=bar",
		"set -x",
		"VAR=value cmd",
		"unset VAR",
		"trap 'echo bye' EXIT",
		"eval 'cd /tmp'",
		"((counter++))",
		"printf 'a'; cd /tmp",
		"source env.sh",
	}
	for _, line := range mutating {
		if !needsShellIsolation(line) {
			t.Errorf("needsShellIsolation(%q) = false, want true", line)
		}
	}
	plain := []string{
		"echo hello",
		"make all",
		"go build ./...",
		"cat file.txt | grep x",
		"printf '%s' hi",
		"test -f x && echo yes",
		"asset-settle-words", // substring must not trip the keyword match
		"echo a=b",
	}
	for _, line := range plain {
		if needsShellIsolation(line) {
			t.Errorf("needsShellIsolation(%q) = true, want false", line)
		}
	}
}

func runAndCapture(t *testing.T, data *ParsedData, target string) string {
	t.Helper()
	so, sw, _ := os.Pipe()
	oldOut := os.Stdout
	os.Stdout = sw
	err := NewExecutor(data, false, false).Execute([]string{target})
	os.Stdout = oldOut
	sw.Close()
	out, _ := io.ReadAll(so)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func runErr(data *ParsedData, target string) error {
	oldOut := os.Stdout
	devNull, _ := os.Open(os.DevNull)
	os.Stdout = devNull
	err := NewExecutor(data, false, false).Execute([]string{target})
	os.Stdout = oldOut
	devNull.Close()
	return err
}

func runAndCaptureErr(t *testing.T, data *ParsedData, target string) (string, error) {
	t.Helper()
	so, sw, _ := os.Pipe()
	oldOut := os.Stdout
	os.Stdout = sw
	err := NewExecutor(data, false, false).Execute([]string{target})
	os.Stdout = oldOut
	sw.Close()
	out, _ := io.ReadAll(so)
	return strings.TrimSpace(string(out)), err
}

func TestFailStatementParsingAndError(t *testing.T) {
	in := `build {
    fail "deploy requires --env"
}`
	p := NewParserFromContent("t.constfile", in)
	data, err := p.Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	build, err := data.GetCommand("build")
	if err != nil {
		t.Fatalf("build missing: %v", err)
	}
	if len(build.Body) != 1 || build.Body[0].Type != StmtFail || build.Body[0].Message != "deploy requires --env" {
		t.Fatalf("body = %+v", build.Body)
	}
	if err := executorEval(data, build); err == nil || !strings.Contains(err.Error(), "deploy requires --env") {
		t.Errorf("expected fail error, got %v", err)
	}
}

func TestFailIsNotPrefix(t *testing.T) {
	in := `build {
    failure-check-command
}`
	p := NewParserFromContent("t.constfile", in)
	data, err := p.Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	build, _ := data.GetCommand("build")
	if build.Body[0].Type != StmtShell {
		t.Errorf("'failure-...' shell line misparsed as fail: %+v", build.Body[0])
	}
}

func TestOnFailRunsOnFailure(t *testing.T) {
	data := &ParsedData{
		Commands: []*Command{{Name: "build", Body: []BodyStatement{
			{Type: StmtOnFail, OnFailBody: shellBody("echo cleaning-up"), SourceLine: 1},
			{Type: StmtShell, Shell: exitNonZero(), SourceLine: 2},
		}}},
	}
	data.buildIndexMaps()
	out, err := runAndCaptureErr(t, data, "build")
	if err == nil {
		t.Fatal("expected failure")
	}
	if !strings.Contains(out, "cleaning-up") {
		t.Errorf("onfail did not run: %q", out)
	}
}

func TestOnFailNotOnSuccess(t *testing.T) {
	data := &ParsedData{
		Commands: []*Command{{Name: "build", Body: []BodyStatement{
			{Type: StmtOnFail, OnFailBody: shellBody("echo cleaning-up"), SourceLine: 1},
			{Type: StmtShell, Shell: "echo done", SourceLine: 2},
		}}},
	}
	data.buildIndexMaps()
	out, err := runAndCaptureErr(t, data, "build")
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if strings.Contains(out, "cleaning-up") {
		t.Errorf("onfail ran on success: %q", out)
	}
}

func TestOnFailFailuresDoNotMaskOriginal(t *testing.T) {
	data := &ParsedData{
		Commands: []*Command{{Name: "build", Body: []BodyStatement{
			{Type: StmtOnFail, OnFailBody: shellBody(exitNonZero()), SourceLine: 1},
			{Type: StmtShell, Shell: exitNonZero(), SourceLine: 2},
		}}},
	}
	data.buildIndexMaps()

	so, sw, _ := os.Pipe()
	oldOut := os.Stdout
	os.Stdout = sw
	devNull, _ := os.Open(os.DevNull)
	oldErr := os.Stderr
	os.Stderr = devNull
	err := NewExecutor(data, false, false).Execute([]string{"build"})
	os.Stdout = oldOut
	os.Stderr = oldErr
	devNull.Close()
	sw.Close()
	io.ReadAll(so)
	var cmdErr *CommandError
	if !errors.As(err, &cmdErr) {
		t.Errorf("expected the original CommandError, got %v", err)
	}
}

func TestInvokeWithArguments(t *testing.T) {
	in := `log (thing_to_log) {
    $ echo "Thing to log: &thing_to_log"
}
use {
    invoke log thing_to_log="hello world"
}
`
	p := NewParserFromContent("t.constfile", in)
	data, err := p.Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	use, _ := data.GetCommand("use")
	if use.Body[0].Type != StmtInvoke || len(use.Body[0].InvokeArgs) != 1 || use.Body[0].InvokeArgs[0] != `thing_to_log="hello world"` {
		t.Fatalf("invoke stmt = %+v", use.Body[0])
	}
	if out, err := runAndCaptureErr(t, data, "use"); err != nil {
		t.Fatalf("exec: %v", err)
	} else if !strings.Contains(out, "Thing to log: hello world") {
		t.Errorf("invoke arg not applied: %q", out)
	}
}

func TestInvokeUsesDeclaredDefaults(t *testing.T) {
	in := `log (thing_to_log="fallback") {
    $ echo "Thing to log: &thing_to_log"
}
use {
    invoke log
}
`
	p := NewParserFromContent("t.constfile", in)
	data, err := p.Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if out, err := runAndCaptureErr(t, data, "use"); err != nil {
		t.Fatalf("exec: %v", err)
	} else if !strings.Contains(out, "Thing to log: fallback") {
		t.Errorf("declared default not applied: %q", out)
	}
}

func TestEnvRefDefault(t *testing.T) {
	const name = "CONSTRUCT_TEST_DEF_X"
	os.Unsetenv(name)
	defer os.Unsetenv(name)

	if got := ResolveEnvRefs("echo @CONSTRUCT_TEST_DEF_X:-hello"); got != "echo hello" {
		t.Errorf("unset with default = %q", got)
	}
	if got := resolveEnvRefsKeepUnset("echo @CONSTRUCT_TEST_DEF_X:-hello"); got != "echo hello" {
		t.Errorf("keep-unset with default = %q", got)
	}
	os.Setenv(name, "real")
	defer os.Unsetenv(name)
	if got := ResolveEnvRefs("echo @CONSTRUCT_TEST_DEF_X:-hello"); got != "echo real" {
		t.Errorf("set env should win: %q", got)
	}
	// No default: unset stays literal for keep-unset, empty otherwise.
	os.Unsetenv(name)
	if got := resolveEnvRefsKeepUnset("echo @CONSTRUCT_TEST_DEF_X"); got != "echo @CONSTRUCT_TEST_DEF_X" {
		t.Errorf("unset without default should stay literal: %q", got)
	}
}

func TestEnvRefDefaultInVarAndShell(t *testing.T) {
	const name = "CONSTRUCT_TEST_DEF_Y"
	os.Unsetenv(name)
	defer os.Unsetenv(name)

	in := `var v = @CONSTRUCT_TEST_DEF_Y:-parse-time
build {
    $ echo @CONSTRUCT_TEST_DEF_Y:-shell-time and &v
}`
	p := NewParserFromContent("t.constfile", in)
	data, err := p.Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if v, ok := data.LookupVariable("v", "global"); !ok || v != "parse-time" {
		t.Errorf("var default = %q, %v", v, ok)
	}
	if out, err := runAndCaptureErr(t, data, "build"); err != nil {
		t.Fatalf("exec: %v", err)
	} else if out != "shell-time and parse-time" {
		t.Errorf("output = %q", out)
	}
}

func TestKeepGoingRunsOtherTargets(t *testing.T) {
	data := &ParsedData{
		Commands: []*Command{
			{Name: "bad", Body: shellBody(exitNonZero())},
			{Name: "good", Body: shellBody("echo good-ran")},
		},
	}
	data.buildIndexMaps()

	executor := NewExecutor(data, false, false)
	executor.SetKeepGoing(true)
	so, sw, _ := os.Pipe()
	oldOut := os.Stdout
	os.Stdout = sw
	err := executor.Execute([]string{"bad", "good"})
	os.Stdout = oldOut
	sw.Close()
	out, _ := io.ReadAll(so)
	if err == nil {
		t.Error("keep-going should still report the failure")
	}
	if !strings.Contains(string(out), "good-ran") {
		t.Errorf("good target should have run with keep-going: %q", out)
	}
}

func TestNoCacheReruns(t *testing.T) {
	dir := t.TempDir()
	dep := filepath.Join(dir, "data.txt")
	os.WriteFile(dep, []byte("x"), 0644)

	// Unique name so the shared cache dir (keyed by command name) can't collide.
	data := &ParsedData{
		Commands: []*Command{
			{Name: "gen-nocache", WorkDir: dir, FileDeps: []string{"data.txt"}, Body: shellBody("echo gen-ran")},
		},
	}
	data.buildIndexMaps()

	run := func() (string, error) {
		so, sw, _ := os.Pipe()
		oldOut := os.Stdout
		os.Stdout = sw
		exec := NewExecutor(data, false, false)
		exec.SetBaseDir(dir) // cache lives in the test's temp dir
		err := exec.Execute([]string{"gen-nocache"})
		os.Stdout = oldOut
		sw.Close()
		out, _ := io.ReadAll(so)
		return string(out), err
	}
	if out, err := run(); err != nil || !strings.Contains(out, "gen-ran") {
		t.Fatalf("first run: %q, %v", out, err)
	}
	if out, err := run(); err != nil || !strings.Contains(out, "cached") {
		t.Fatalf("second run should be cached: %q, %v", out, err)
	}
	executor := NewExecutor(data, false, false)
	executor.SetNoCache(true)
	executor.SetBaseDir(dir)
	so, sw, _ := os.Pipe()
	oldOut := os.Stdout
	os.Stdout = sw
	err := executor.Execute([]string{"gen-nocache"})
	os.Stdout = oldOut
	sw.Close()
	out, _ := io.ReadAll(so)
	if err != nil || !strings.Contains(string(out), "gen-ran") {
		t.Fatalf("no-cache should rerun: %q, %v", out, err)
	}
	if strings.Contains(string(out), "cached") {
		t.Errorf("no-cache still used cache: %q", out)
	}
}

func TestSetShell(t *testing.T) {
	e := NewExecutor(&ParsedData{}, false, false)
	e.SetShell("/bin/sh")
	if e.shellName != "/bin/sh" {
		t.Errorf("shellName = %q", e.shellName)
	}
	e.SetShell("")
	if e.shellName != "/bin/sh" {
		t.Errorf("empty SetShell should be a no-op: %q", e.shellName)
	}
}

func TestOnFailSeesFailureContext(t *testing.T) {
	data := &ParsedData{
		Commands: []*Command{{Name: "build", Body: []BodyStatement{
			{Type: StmtOnFail, OnFailBody: shellBody(
				`echo "failed: &fail.message"`,
				`echo "line: &fail.line"`,
				`echo "exit: &fail.exit"`,
			), SourceLine: 1},
			{Type: StmtShell, Shell: "echo boom && " + exitNonZero(), SourceLine: 4},
		}}},
	}
	data.buildIndexMaps()
	out, err := runAndCaptureErr(t, data, "build")
	if err == nil {
		t.Fatal("expected failure")
	}
	if !strings.Contains(out, `failed: command`) || !strings.Contains(out, `boom`) {
		t.Errorf("fail.message missing: %q", out)
	}
	if !strings.Contains(out, "line: 4") {
		t.Errorf("fail.line missing: %q", out)
	}
	if !strings.Contains(out, "exit: 3") {
		t.Errorf("fail.exit missing: %q", out)
	}
}

func TestOnFailSeesFailMessage(t *testing.T) {
	data := &ParsedData{
		Commands: []*Command{{Name: "build", Body: []BodyStatement{
			{Type: StmtOnFail, OnFailBody: shellBody(`echo "message: &fail.message"`), SourceLine: 1},
			{Type: StmtFail, Message: "deploy refused", SourceLine: 3},
		}}},
	}
	data.buildIndexMaps()
	out, err := runAndCaptureErr(t, data, "build")
	if err == nil {
		t.Fatal("expected failure")
	}
	if !strings.Contains(out, "message: fail: deploy refused") {
		t.Errorf("fail.message = %q", out)
	}
}

// TestOnFailRunsOnCancel verifies canceling the run context (SIGINT) kills
// the command and still runs onfail cleanup on a fresh context.
func TestOnFailRunsOnCancel(t *testing.T) {
	in := `build {
    onfail {
        $ echo CLEANUP-RAN
    }
    $ sleep 30
}`
	p := NewParserFromContent("t.constfile", in)
	data, err := p.Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())

	so, sw, _ := os.Pipe()
	oldOut := os.Stdout
	os.Stdout = sw
	e := NewExecutor(data, false, false)
	e.SetRunContext(ctx)
	done := make(chan error, 1)
	go func() { done <- e.Execute([]string{"build"}) }()
	time.Sleep(400 * time.Millisecond)
	cancel()
	err = <-done
	os.Stdout = oldOut
	sw.Close()
	out, _ := io.ReadAll(so)
	if err == nil {
		t.Fatal("expected failure after cancel")
	}
	if !strings.Contains(string(out), "CLEANUP-RAN") {
		t.Errorf("onfail did not run after cancel: %q", out)
	}
}

func TestOnChangeHeaderGrammar(t *testing.T) {
	cases := []struct {
		header  string
		workDir string
	}{
		{"gen in src produces out.txt onchange src/*.c, src/*.h < dep {", "src"},
		{"gen < dep onchange src/*.c {", ""},
		{"gen in src onchange src/*.c {", "src"},
	}
	for _, tc := range cases {
		p := &Parser{Data: &ParsedData{}, Lines: []string{tc.header, "    $ echo x", "}"}}
		if _, err := p.parseCommand(0, tc.header, false, false, 1, ""); err != nil {
			t.Fatalf("%q: %v", tc.header, err)
		}
		cmd := p.Data.Commands[0]
		if cmd.Name != "gen" {
			t.Errorf("%q: name = %q", tc.header, cmd.Name)
		}
		if len(cmd.OnChange) == 0 {
			t.Errorf("%q: onchange not parsed", tc.header)
		}
		if cmd.WorkDir != tc.workDir {
			t.Errorf("%q: workdir = %q, want %q", tc.header, cmd.WorkDir, tc.workDir)
		}
	}
}
