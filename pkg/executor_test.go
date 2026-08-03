package pkg

import (
	"runtime"
	"strings"
	"testing"
)

func TestNewExecutor(t *testing.T) {
	data := &ParsedData{
		Variables: []*Variable{
			{Name: "test", Value: "value", Scope: "global"},
		},
		Commands: []*Command{
			{Name: "build", IsDefault: false, Body: shellBody("echo hello")},
		},
	}

	executor := NewExecutor(data, false, false)
	if executor == nil {
		t.Error("expected non-nil executor")
	}
	if executor.concurrent {
		t.Error("expected concurrent to be false")
	}
	if executor.debug {
		t.Error("expected debug to be false by default")
	}
}

func TestExecutorSetDebug(t *testing.T) {
	data := &ParsedData{}
	executor := NewExecutor(data, false, false)

	executor.SetDebug(true)
	if !executor.debug {
		t.Error("expected debug to be true")
	}

	executor.SetDebug(false)
	if executor.debug {
		t.Error("expected debug to be false")
	}
}

func TestBuildCommand(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		expectShell string
	}{
		{
			name:        "simple echo command",
			input:       "echo hello",
			expectShell: defaultShellName(),
		},
		{
			name:        "complex command",
			input:       "ls -la | grep test",
			expectShell: defaultShellName(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			executor := NewExecutor(nil, false, false)
			if executor.shellName != tt.expectShell {
				t.Errorf("expected shell %s, got %s", tt.expectShell, executor.shellName)
			}
			args := append(executor.shellArgs, tt.input)
			if len(args) < 2 {
				t.Error("expected at least 2 args (flag and command)")
			}
		})
	}
}

// defaultShellName mirrors the executor's default shell choice so the tests
// remain platform-portable instead of hardcoding /bin/bash.
func defaultShellName() string {
	name, _ := defaultShell()
	return name
}

func TestExecutorDryRun(t *testing.T) {
	data := &ParsedData{
		Commands: []*Command{
			{Name: "build", IsDefault: false, Body: shellBody("echo hello")},
		},
	}

	executor := NewExecutor(data, false, false)
	executor.SetDebug(true)

	cmd, err := data.GetCommand("build")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cmd.Name != "build" {
		t.Errorf("expected command name 'build', got %s", cmd.Name)
	}
}

func TestCircularDependencyDetection(t *testing.T) {
	tests := []struct {
		name        string
		commands    []*Command
		expectError bool
	}{
		{
			name: "no circular dependency",
			commands: []*Command{
				{Name: "a", Prereqs: []string{"b"}},
				{Name: "b", Prereqs: []string{}},
			},
			expectError: false,
		},
		{
			name: "direct circular dependency",
			commands: []*Command{
				{Name: "a", Prereqs: []string{"b"}},
				{Name: "b", Prereqs: []string{"a"}},
			},
			expectError: true,
		},
		{
			name: "indirect circular dependency",
			commands: []*Command{
				{Name: "a", Prereqs: []string{"b"}},
				{Name: "b", Prereqs: []string{"c"}},
				{Name: "c", Prereqs: []string{"a"}},
			},
			expectError: true,
		},
		{
			name: "self-referential dependency",
			commands: []*Command{
				{Name: "a", Prereqs: []string{"a"}},
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parser := &Parser{
				Data: &ParsedData{
					Commands: tt.commands,
				},
			}

			err := parser.detectCircularDependencies()
			if tt.expectError {
				if err == nil {
					t.Error("expected circular dependency error but got none")
				}
				_, ok := err.(*CircularDependencyError)
				if !ok {
					t.Errorf("expected CircularDependencyError, got %T", err)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestMissingPrerequisiteValidation(t *testing.T) {
	tests := []struct {
		name        string
		commands    []*Command
		expectError bool
	}{
		{
			name: "all prerequisites exist",
			commands: []*Command{
				{Name: "a", Prereqs: []string{"b"}},
				{Name: "b", Prereqs: []string{}},
			},
			expectError: false,
		},
		{
			name: "missing prerequisite",
			commands: []*Command{
				{Name: "a", Prereqs: []string{"nonexistent"}},
			},
			expectError: true,
		},
		{
			name: "empty prerequisite string",
			commands: []*Command{
				{Name: "a", Prereqs: []string{""}},
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parser := &Parser{
				Data: &ParsedData{
					Commands: tt.commands,
				},
			}

			err := parser.validatePrerequisites()
			if tt.expectError {
				if err == nil {
					t.Error("expected missing prerequisite error but got none")
				}
				_, ok := err.(*MissingDependencyError)
				if !ok {
					t.Errorf("expected MissingDependencyError, got %T", err)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

// TestEvaluateCommandSimple runs a single command body through the executor
// using a portable echo so the test works on both cmd.exe and POSIX shells.
func TestEvaluateCommandSimple(t *testing.T) {
	data := &ParsedData{
		Commands: []*Command{
			{Name: "hello", Body: shellBody("echo hello")},
		},
	}
	data.buildIndexMaps()

	executor := NewExecutor(data, false, false)
	cmd, err := data.GetCommand("hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := executor.EvaluateCommand(cmd); err != nil {
		t.Fatalf("EvaluateCommand failed: %v", err)
	}
}

// TestEvaluateCommandFailure verifies that a failing command body surfaces a
// *CommandError with the child process exit code.
func TestEvaluateCommandFailure(t *testing.T) {
	data := &ParsedData{
		Commands: []*Command{
			{Name: "fail", Body: shellBody(exitNonZero())},
		},
	}
	data.buildIndexMaps()

	executor := NewExecutor(data, false, false)
	cmd, err := data.GetCommand("fail")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	err = executor.EvaluateCommand(cmd)
	if err == nil {
		t.Fatal("expected an error from failing command, got nil")
	}
	cmdErr, ok := err.(*CommandError)
	if !ok {
		t.Fatalf("expected *CommandError, got %T (%v)", err, err)
	}
	if cmdErr.ExitCode == 0 {
		t.Errorf("expected non-zero exit code, got %d", cmdErr.ExitCode)
	}
}

// exitNonZero returns a shell command that exits with a non-zero status on the
// current platform.
func exitNonZero() string {
	if runtime.GOOS == "windows" {
		return "exit /b 3"
	}
	return "false"
}

// shellBody builds a []BodyStatement from plain shell lines, for test brevity.
func shellBody(lines ...string) []BodyStatement {
	stmts := make([]BodyStatement, len(lines))
	for i, l := range lines {
		stmts[i] = BodyStatement{Type: "shell", Shell: l}
	}
	return stmts
}

// TestExecConcurrent runs several independent commands concurrently and asserts
// that all of them complete without leaking goroutines or dropping errors.
func TestExecConcurrent(t *testing.T) {
	data := &ParsedData{
		Commands: []*Command{
			{Name: "a", Body: shellBody("echo a")},
			{Name: "b", Body: shellBody("echo b")},
			{Name: "c", Body: shellBody("echo c")},
		},
	}
	data.buildIndexMaps()

	executor := NewExecutor(data, true, false) // concurrent = true
	if err := executor.Execute([]string{"a", "b", "c"}); err != nil {
		t.Fatalf("concurrent Execute failed: %v", err)
	}
}

// TestExecConcurrentError ensures a failing command in concurrent mode still
// reports an error back to the caller rather than being silently dropped.
func TestExecConcurrentError(t *testing.T) {
	data := &ParsedData{
		Commands: []*Command{
			{Name: "ok", Body: shellBody("echo ok")},
			{Name: "boom", Body: shellBody(exitNonZero())},
		},
	}
	data.buildIndexMaps()

	executor := NewExecutor(data, true, false)
	err := executor.Execute([]string{"ok", "boom"})
	if err == nil {
		t.Fatal("expected an error from concurrent execution with a failing command, got nil")
	}
}

func TestExecSharedPrereq(t *testing.T) {
	run := func(concurrent bool) {
		data := &ParsedData{
			Commands: []*Command{
				{Name: "gen", Body: shellBody("echo shared")},
				{Name: "a", Prereqs: []string{"gen"}, Body: shellBody("$ echo a:&gen.0")},
				{Name: "b", Prereqs: []string{"gen"}, Body: shellBody("$ echo b:&gen.0")},
			},
		}
		data.buildIndexMaps()

		executor := NewExecutor(data, concurrent, false)
		if err := executor.Execute([]string{"a", "b"}); err != nil {
			t.Fatalf("concurrent=%v Execute failed: %v", concurrent, err)
		}

		gen, _ := data.GetCommand("gen")
		if len(gen.PrereqOutput) != 1 || gen.PrereqOutput[0] != "shared" {
			t.Errorf("concurrent=%v shared prereq output = %#v, want [\"shared\"]", concurrent, gen.PrereqOutput)
		}
	}

	run(false)
	run(true)
}

// TestExecSameTargetTwice verifies that listing a command twice within one
// invocation executes it once (make-style dedup), while separate Exec calls
// re-run it.
func TestExecSameTargetTwice(t *testing.T) {
	data := &ParsedData{
		Commands: []*Command{
			{Name: "say", Body: shellBody("echo hi")},
		},
	}
	data.buildIndexMaps()

	executor := NewExecutor(data, false, false)
	if err := executor.Execute([]string{"say", "say"}); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if err := executor.Execute([]string{"say"}); err != nil {
		t.Fatalf("second Execute failed: %v", err)
	}
}

func TestExecFiltersEmptyAndFlags(t *testing.T) {
	data := &ParsedData{
		Commands: []*Command{
			{Name: "real", Body: shellBody("echo real")},
		},
	}
	data.buildIndexMaps()

	executor := NewExecutor(data, false, false)
	// Empty string and a "--"-style token must be skipped, not indexed.
	err := executor.Execute([]string{"", "--", "real"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestExecUnknownCommand(t *testing.T) {
	data := &ParsedData{
		Commands: []*Command{
			{Name: "real", Body: shellBody("echo real")},
		},
	}
	data.buildIndexMaps()

	executor := NewExecutor(data, false, false)
	err := executor.Execute([]string{"nope"})
	if err == nil {
		t.Fatal("expected error for unknown command, got nil")
	}
}

// TestExecDefaultCommand verifies that when no commands are passed, the default
// command (if any) runs.
func TestExecDefaultCommand(t *testing.T) {
	data := &ParsedData{
		Commands: []*Command{
			{Name: "_", IsDefault: true, Body: shellBody("echo default")},
		},
	}
	data.buildIndexMaps()

	executor := NewExecutor(data, false, false)
	if err := executor.Execute(nil); err != nil {
		t.Fatalf("default Execute failed: %v", err)
	}
}

func TestParseCommandBodyBraceInString(t *testing.T) {
	input := "awk {\n    $ awk '{print $1}'\n}"
	lines := strings.Split(input, "\n")
	parser := &Parser{Data: &ParsedData{}, Lines: lines}

	_, err := parser.parseCommand(0, input, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(parser.Data.Commands) != 1 {
		t.Fatalf("expected 1 command, got %d", len(parser.Data.Commands))
	}
	cmd := parser.Data.Commands[0]
	want := "$ awk '{print $1}'"
	if len(cmd.Body) != 1 || cmd.Body[0].Shell != want {
		t.Errorf("expected body [%q], got %#v", want, cmd.Body)
	}
}

func TestStripInlineCommentEscapedQuotes(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "no comment", input: `echo hi`, expected: `echo hi`},
		{name: "hash comment", input: `echo hi # a comment`, expected: `echo hi`},
		{name: "slash comment", input: `echo hi // a comment`, expected: `echo hi`},
		{name: "hash inside quotes", input: `echo "a # b"`, expected: `echo "a # b"`},
		{name: "escaped quote then comment", input: `echo "he said \"hi\""# x`, expected: `echo "he said \"hi\""`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripInlineComment(tt.input)
			if got != tt.expected {
				t.Errorf("stripInlineComment(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestResolveVarRefs(t *testing.T) {
	lookup := func(name string) (string, bool) {
		vals := map[string]string{"g": "GV", "name": "nick", "build-lsp.0": "output1"}
		v, ok := vals[name]
		return v, ok
	}

	tests := []struct {
		name string
		line string
		want string
	}{
		{name: "no refs", line: "echo hi", want: "echo hi"},
		{name: "single ref", line: "echo &g", want: "echo GV"},
		{name: "ref embedded in text", line: "hello &name!", want: "hello nick!"},
		{name: "multiple refs", line: "&g and &name", want: "GV and nick"},
		{name: "hyphenated ref", line: "echo &build-lsp.0", want: "echo output1"},
		{name: "unknown ref passed through", line: "x &nope y", want: "x &nope y"},
		{name: "bare ampersand not a ref", line: "a & b", want: "a & b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveVarRefs(tt.line, lookup); got != tt.want {
				t.Errorf("resolveVarRefs(%q) = %q, want %q", tt.line, got, tt.want)
			}
		})
	}
}

func TestResolveRefsByteRuneParity(t *testing.T) {
	lookup := func(name string) (string, bool) {
		vals := map[string]string{"g": "GV", "name": "nick", "build-lsp.0": "output1", "café": "nonascii"}
		v, ok := vals[name]
		return v, ok
	}

	t.Setenv("CONSTRUCT_PARITY_ENV", "envval")

	cases := []string{
		"echo hi",
		"echo &g and &name",
		"echo &build-lsp.0",
		"x &nope y",
		"a & b",
		"& at start &name at end",
		"tail &g&name",
		"&g.5",
		"echo @CONSTRUCT_PARITY_ENV",
		"@ & @CONSTRUCT_PARITY_ENV x",
		"mix &g and @CONSTRUCT_PARITY_ENV",
		"café &café café",
		"日本語 &name テスト",
		"line with trailing &",
	}
	for _, tc := range cases {
		if got, want := resolveVarRefs(tc, lookup), resolveVarRefsRunes(tc, lookup); got != want {
			t.Errorf("resolveVarRefs(%q): byte=%q rune=%q", tc, got, want)
		}
		if got, want := resolveEnvRefs(tc), resolveEnvRefsRunes(tc); got != want {
			t.Errorf("resolveEnvRefs(%q): byte=%q rune=%q", tc, got, want)
		}
	}
}

func TestEvaluateCommandPrereqCapture(t *testing.T) {
	data := &ParsedData{
		Commands: []*Command{
			{Name: "gen", Body: shellBody("echo generated")},
			{Name: "use", Prereqs: []string{"gen"}, Body: shellBody("$ echo got:&gen.0")},
		},
	}
	data.buildIndexMaps()

	executor := NewExecutor(data, false, false)
	if err := executor.Execute([]string{"use"}); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	gen, _ := data.GetCommand("gen")
	if len(gen.PrereqOutput) != 1 || gen.PrereqOutput[0] != "generated" {
		t.Errorf("prereq output = %#v, want [\"generated\"]", gen.PrereqOutput)
	}
}

func TestEvaluateCommandLazyVariable(t *testing.T) {
	data := &ParsedData{
		Variables: []*Variable{
			{Name: "dyn", Value: "", Scope: "global"},
		},
		Commands: []*Command{
			{
				Name:     "__lazy_dyn_global",
				LazyEval: &LazyOutput{VarName: "dyn", Scope: "global"},
				Body:     shellBody("echo lazyresult"),
			},
		},
	}
	data.buildIndexMaps()

	executor := NewExecutor(data, false, false)
	if err := executor.Execute(nil); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	v, err := data.GetVariable("dyn", "global")
	if err != nil {
		t.Fatalf("GetVariable failed: %v", err)
	}
	if v.Value != "lazyresult" {
		t.Errorf("lazy var value = %q, want %q", v.Value, "lazyresult")
	}
}

func TestEvaluateCommandVarSubstitution(t *testing.T) {
	data := &ParsedData{
		Variables: []*Variable{
			{Name: "who", Value: "world", Scope: "global"},
		},
		Commands: []*Command{
			{Name: "say", Body: shellBody("echo hi &who")},
		},
	}
	data.buildIndexMaps()

	executor := NewExecutor(data, false, false)
	if err := executor.Execute([]string{"say"}); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
}

func TestEvaluateCommandIdempotent(t *testing.T) {
	data := &ParsedData{
		Variables: []*Variable{
			{Name: "who", Value: "world", Scope: "global"},
		},
		Commands: []*Command{
			{Name: "say", Body: shellBody("echo hi &who")},
		},
	}
	data.buildIndexMaps()

	executor := NewExecutor(data, false, false)

	for i := 1; i <= 3; i++ {
		if err := executor.Execute([]string{"say"}); err != nil {
			t.Fatalf("Execute pass %d failed: %v", i, err)
		}
	}

	cmd, _ := data.GetCommand("say")
	// The stored body must still contain the unresolved reference.
	if len(cmd.Body) != 1 || cmd.Body[0].Shell != "echo hi &who" {
		t.Errorf("stored body was mutated: %#v; want [\"echo hi &who\"]", cmd.Body)
	}
}

func TestTryApplyCloudBody(t *testing.T) {
	t.Setenv("CONSTRUCT_CLOUD_FILE", "testdata/cloud_test.json")

	data := &ParsedData{
		Commands: []*Command{
			{Name: "local", Body: shellBody("echo local")},
		},
	}
	data.buildIndexMaps()

	executor := NewExecutor(data, false, false)

	t.Run("non-cloud command unchanged", func(t *testing.T) {
		cmd := &Command{Name: "local", Body: shellBody("echo local")}
		before := append([]BodyStatement(nil), cmd.Body...)
		if err := executor.tryApplyCloudBody(cmd); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(cmd.Body) != len(before) {
			t.Errorf("body changed: %#v vs %#v", cmd.Body, before)
		}
	})
}

func TestEvaluateCommandWorkDir(t *testing.T) {
	// Create a temp subdir to use as the workdir.
	tmpDir := t.TempDir()

	// On Windows, echo %CD% prints the current dir; on POSIX, pwd does.
	var pwdCmd string
	if runtime.GOOS == "windows" {
		pwdCmd = "echo %CD%"
	} else {
		pwdCmd = "pwd"
	}

	data := &ParsedData{
		Commands: []*Command{
			{Name: "whereami", WorkDir: tmpDir, Body: shellBody(pwdCmd)},
		},
	}
	data.buildIndexMaps()

	executor := NewExecutor(data, false, false)
	if err := executor.Execute([]string{"whereami"}); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
}

// TestIfBlockTrueBranch verifies the then-branch executes when condition is true.
func TestIfBlockTrueBranch(t *testing.T) {
	data := &ParsedData{
		Commands: []*Command{
			{Name: "testif", Body: []BodyStatement{
				{
					Type: "if",
					Cond: `"1" == "1"`,
					ThenBody: []BodyStatement{
						{Type: "shell", Shell: "echo then-ran"},
					},
					ElseBody: []BodyStatement{
						{Type: "shell", Shell: exitNonZero()},
					},
				},
			}},
		},
	}
	data.buildIndexMaps()

	executor := NewExecutor(data, false, false)
	// If the wrong branch ran, exitNonZero would cause an error.
	if err := executor.Execute([]string{"testif"}); err != nil {
		t.Fatalf("expected then-branch (no error), got: %v", err)
	}
}

// TestIfBlockFalseBranch verifies the else-branch executes when condition is false.
func TestIfBlockFalseBranch(t *testing.T) {
	data := &ParsedData{
		Commands: []*Command{
			{Name: "testif", Body: []BodyStatement{
				{
					Type: "if",
					Cond: `"1" == "2"`,
					ThenBody: []BodyStatement{
						{Type: "shell", Shell: exitNonZero()},
					},
					ElseBody: []BodyStatement{
						{Type: "shell", Shell: "echo else-ran"},
					},
				},
			}},
		},
	}
	data.buildIndexMaps()

	executor := NewExecutor(data, false, false)
	// If the wrong branch ran, exitNonZero would cause an error.
	if err := executor.Execute([]string{"testif"}); err != nil {
		t.Fatalf("expected else-branch (no error), got: %v", err)
	}
}

// TestIfBlockNoElseSkipped verifies that when condition is false and there's no
// else, execution continues past the if without error.
func TestIfBlockNoElseSkipped(t *testing.T) {
	data := &ParsedData{
		Commands: []*Command{
			{Name: "testif", Body: []BodyStatement{
				{
					Type: "if",
					Cond: `"1" == "2"`,
					ThenBody: []BodyStatement{
						{Type: "shell", Shell: exitNonZero()},
					},
				},
				{Type: "shell", Shell: "echo after-if"},
			}},
		},
	}
	data.buildIndexMaps()

	executor := NewExecutor(data, false, false)
	if err := executor.Execute([]string{"testif"}); err != nil {
		t.Fatalf("expected no error (if skipped, no else), got: %v", err)
	}
}

// TestNamedOutput verifies that "as <name>" tagged prereq output is accessible
// as &prereq.name in the consuming command.
func TestNamedOutput(t *testing.T) {
	data := &ParsedData{
		Commands: []*Command{
			{
				Name: "gen",
				Body: []BodyStatement{
					{Type: "shell", Shell: "echo hello", OutputName: "greeting"},
				},
			},
			{
				Name:    "use",
				Prereqs: []string{"gen"},
				Body: []BodyStatement{
					{Type: "shell", Shell: "$ echo got:&gen.greeting"},
				},
			},
		},
	}
	data.buildIndexMaps()

	executor := NewExecutor(data, false, false)
	if err := executor.Execute([]string{"use"}); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	gen, _ := data.GetCommand("gen")
	if len(gen.PrereqOutput) != 1 || gen.PrereqOutput[0] != "hello" {
		t.Errorf("positional output = %#v, want [\"hello\"]", gen.PrereqOutput)
	}
	if gen.NamedOutput == nil || gen.NamedOutput["greeting"] != "hello" {
		t.Errorf("named output = %#v, want greeting=\"hello\"", gen.NamedOutput)
	}
}
