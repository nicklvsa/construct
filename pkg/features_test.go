package pkg

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"time"

	"github.com/spf13/pflag"
)

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
	if _, err := p.parseCommand(0, "build {", false); err != nil {
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

// TestMatrixExecution verifies the cross product iterates all combinations
// with both variables in scope, the last variable varying fastest.
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
	if got := resolveEnvRefs(`echo "\&foo \@BAR"`); got != `echo "\&foo @BAR"` {
		t.Errorf("resolveEnvRefs escape = %q", got)
	}

	// Variable values: \&, \@, \$ are literal.
	p := &Parser{Data: &ParsedData{}}
	p.Data.Variables = append(p.Data.Variables, &Variable{Name: "foo", Value: "F", Scope: "global"})
	name, scope := "x", "global"
	if got := p.tryEvalExpression(`a \&foo b \@HOME c \$5`, &name, &scope); got != `a &foo b @HOME c $5` {
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
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
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
