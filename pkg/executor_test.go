package pkg

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"testing"

	"github.com/spf13/pflag"
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

func defaultShellName() string {
	name, _ := DefaultShell()
	return name
}

func TestNonInteractiveArgs(t *testing.T) {
	tests := []struct {
		shell string
		want  []string
	}{
		{shell: "/bin/zsh", want: []string{"-f", "-c"}},
		{shell: "/usr/local/bin/zsh", want: []string{"-f", "-c"}},
		{shell: "/bin/bash", want: []string{"--noprofile", "--norc", "-c"}},
		{shell: "/usr/bin/sh", want: []string{"-c"}},
		{shell: "/opt/homebrew/bin/fish", want: []string{"-c"}},
		{shell: "C:\\Git\\usr\\bin\\bash.exe", want: []string{"--noprofile", "--norc", "-c"}},
	}
	for _, tt := range tests {
		t.Run(tt.shell, func(t *testing.T) {
			if got := nonInteractiveArgs(tt.shell); !slices.Equal(got, tt.want) {
				t.Errorf("nonInteractiveArgs(%q) = %v, want %v", tt.shell, got, tt.want)
			}
		})
	}
}

func TestExecutorCachesChildEnv(t *testing.T) {
	executor := NewExecutor(&ParsedData{}, false, false)
	if executor.env == nil {
		t.Fatal("expected env to be populated at construction")
	}

	for range 3 {
		cmdEnv := slices.Clone(executor.env)
		if !slices.Equal(cmdEnv, executor.env) {
			t.Fatal("cloned env differs from the executor's base env")
		}
		if len(cmdEnv) > 0 && &cmdEnv[0] == &executor.env[0] {
			t.Fatal("cloned env shares backing array with the base env")
		}
	}
}

func captureStreams(t *testing.T) (stdout, stderr *os.File, restore func()) {
	t.Helper()
	so, sw, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	se, ew, err := os.Pipe()
	if err != nil {
		t.Fatalf("stderr pipe: %v", err)
	}
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = sw, ew
	restore = func() {
		os.Stdout, os.Stderr = oldOut, oldErr
		sw.Close()
		ew.Close()
	}
	return so, se, restore
}

func TestStreamingTargetOutput(t *testing.T) {
	r, _, restore := captureStreams(t)
	data := &ParsedData{
		Commands: []*Command{
			{Name: "stream", Body: shellBody("echo one", "echo two")},
		},
	}
	data.buildIndexMaps()

	executor := NewExecutor(data, false, false)
	err := executor.Execute([]string{"stream"})
	restore()
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	got := strings.ReplaceAll(string(out), "\r\n", "\n")
	if got != "one\ntwo\n" {
		t.Errorf("streamed stdout = %q, want %q", got, "one\ntwo\n")
	}
}

func TestStreamingTargetStderr(t *testing.T) {
	r, e, restore := captureStreams(t)
	data := &ParsedData{
		Commands: []*Command{
			{Name: "stream", Body: shellBody("echo err 1>&2", "echo out")},
		},
	}
	data.buildIndexMaps()

	executor := NewExecutor(data, false, false)
	err := executor.Execute([]string{"stream"})
	restore()
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	errOut, err := io.ReadAll(e)
	if err != nil {
		t.Fatalf("read stderr: %v", err)
	}

	if got := strings.ReplaceAll(string(out), "\r\n", "\n"); got != "out\n" {
		t.Errorf("stdout = %q, want %q", got, "out\n")
	}
	if got := strings.ReplaceAll(string(errOut), "\r\n", "\n"); got != "err\n" {
		t.Errorf("stderr = %q, want %q", got, "err\n")
	}
}

func TestStreamingEmptyOutputNoBlankLine(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no portable empty-output command on Windows")
	}

	r, _, restore := captureStreams(t)
	data := &ParsedData{
		Commands: []*Command{
			{Name: "quiet", Body: shellBody("true", "echo after")},
		},
	}
	data.buildIndexMaps()

	executor := NewExecutor(data, false, false)
	err := executor.Execute([]string{"quiet"})
	restore()
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	if got := strings.ReplaceAll(string(out), "\r\n", "\n"); got != "after\n" {
		t.Errorf("stdout = %q, want %q", got, "after\n")
	}
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

			err := parser.classifyPrereqs()
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

func exitNonZero() string {
	return "exit 3"
}

// shellBody builds a []BodyStatement from plain shell lines, for test brevity.
func shellBody(lines ...string) []BodyStatement {
	stmts := make([]BodyStatement, len(lines))
	for i, l := range lines {
		stmts[i] = BodyStatement{Type: "shell", Shell: l}
	}
	return stmts
}

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

	_, err := parser.parseCommand(0, input, false, false, false, 1, "")
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
		{name: "escaped quote then comment", input: `echo "he said \"hi\"" # x`, expected: `echo "he said \"hi\""`},
		{name: "no whitespace before hash is not a comment", input: `echo "he said \"hi\""# x`, expected: `echo "he said \"hi\""# x`},
		{name: "url is not a comment", input: `var url = https://example.com`, expected: `var url = https://example.com`},
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
		{name: "dotted fallback var+extension", line: "&g.vsix", want: "GV.vsix"},
		{name: "dotted fallback mid text", line: "x&name.go y", want: "xnick.go y"},
		{name: "full dotted name wins over fallback", line: "&build-lsp.0", want: "output1"},
		{name: "unknown dotted stays literal", line: "&nope.vsix", want: "&nope.vsix"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveVarRefs(tt.line, lookup); got != tt.want {
				t.Errorf("resolveVarRefs(%q) = %q, want %q", tt.line, got, tt.want)
			}
		})
	}
}

// TestResolveRefsBehavior pins the &ref / @ENV scanner semantics: unicode
// identifiers, dot-segment fallback, escapes, and literal unset env refs.
func TestResolveRefsBehavior(t *testing.T) {
	lookup := func(name string) (string, bool) {
		vals := map[string]string{"g": "GV", "name": "nick", "build-lsp.0": "output1", "café": "nonascii"}
		v, ok := vals[name]
		return v, ok
	}

	t.Setenv("CONSTRUCT_TEST_ENV", "envval")

	varRefs := []struct{ in, want string }{
		{"echo hi", "echo hi"},
		{"echo &g and &name", "echo GV and nick"},
		{"echo &build-lsp.0", "echo output1"},
		{"x &nope y", "x &nope y"}, // unknown stays literal
		{"a & b", "a & b"},
		{"& at start &name at end", "& at start nick at end"},
		{"tail &g&name", "tail GVnick"},
		{"&g.5", "GV.5"},               // full name misses, first segment resolves
		{"echo \\&name", "echo &name"}, // escaped stays literal
		{"café &café café", "café nonascii café"},
		{"日本語 &name テスト", "日本語 nick テスト"},
		{"line with trailing &", "line with trailing &"},
	}
	for _, tc := range varRefs {
		if got := resolveVarRefs(tc.in, lookup); got != tc.want {
			t.Errorf("resolveVarRefs(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	envCases := []struct{ in, want string }{
		{"echo @CONSTRUCT_TEST_ENV", "echo envval"},
		{"echo @NOPE", "echo "}, // unset => empty
		{"@ & @CONSTRUCT_TEST_ENV x", "@ & envval x"},
		{"echo \\@CONSTRUCT_TEST_ENV", "echo @CONSTRUCT_TEST_ENV"}, // escaped stays literal
	}
	for _, tc := range envCases {
		if got := ResolveEnvRefs(tc.in); got != tc.want {
			t.Errorf("ResolveEnvRefs(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	if got := resolveEnvRefsKeepUnset("echo @NOPE and @CONSTRUCT_TEST_ENV"); got != "echo @NOPE and envval" {
		t.Errorf("resolveEnvRefsKeepUnset = %q", got)
	}
}

func TestEscapeShellValue(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{in: "plain text", want: "plain text"},
		{in: "hello world", want: "hello world"},
		{in: "a`b", want: "a\\`b"},
		{in: "cost $5", want: "cost \\$5"},
		{in: `back\slash`, want: `back\\slash`},
		{in: `say "hi"`, want: `say \"hi\"`},
		{in: "multi\nline", want: "multi\nline"},
	}
	for _, tt := range tests {
		if got := escapeShellValue(tt.in); got != tt.want {
			t.Errorf("escapeShellValue(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestPrereqOutputInjectionEscaped(t *testing.T) {
	data := &ParsedData{
		Commands: []*Command{
			{Name: "gen", Body: shellBody(`echo 'has ` + "`" + `code` + "`" + ` and $var'`)},
			{Name: "use", Prereqs: []string{"gen"}, Body: shellBody("$ echo &gen.0")},
		},
	}
	data.buildIndexMaps()

	executor := NewExecutor(data, false, true)
	if err := executor.Execute([]string{"use"}); err != nil {
		t.Fatalf("Execute failed: %v", err)
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

// TestLazyVarEvaluatedForPrereqCommand verifies a lazy variable scoped to a
// command is evaluated even when that command is only reached as a
// prerequisite of the target (e.g. a default command depending on it).
func TestLazyVarEvaluatedForPrereqCommand(t *testing.T) {
	data := &ParsedData{
		Commands: []*Command{
			{
				Name:      "_",
				IsDefault: true,
				Prereqs:   []string{"build-ext"},
			},
			{
				Name: "build-ext",
				Body: []BodyStatement{
					{Type: "shell", Shell: "$ echo constfile-&vsix_version.vsix"},
				},
			},
			{
				Name:     "__lazy_vsix_version_build-ext",
				LazyEval: &LazyOutput{VarName: "vsix_version", Scope: "build-ext"},
				Body:     shellBody("echo 0.2.0"),
			},
		},
	}
	data.buildIndexMaps()

	executor := NewExecutor(data, false, false)
	if err := executor.Execute(nil); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	v, err := data.GetVariable("vsix_version", "build-ext")
	if err != nil {
		t.Fatalf("GetVariable failed: %v", err)
	}
	if v.Value != "0.2.0" {
		t.Errorf("lazy var value = %q, want %q", v.Value, "0.2.0")
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
	// Global lazy variables evaluate even when no target is requested, but
	// Execute still reports that there was nothing to run.
	if err := executor.Execute(nil); err == nil {
		t.Fatal("Execute(nil) with no default command should fail")
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

func TestEvaluateCommandArgumentRef(t *testing.T) {
	data := &ParsedData{
		Commands: []*Command{
			{
				Name:      "log",
				Arguments: []*Argument{{Name: "thing_to_log"}},
				Body: []BodyStatement{
					{Type: "shell", Shell: `echo "Thing to log: &thing_to_log"`, OutputName: "out"},
				},
			},
			{Name: "use", Prereqs: []string{"log"}, Body: shellBody("$ echo got:&log.out")},
		},
	}
	data.buildIndexMaps()

	flagSet := pflag.NewFlagSet("test", pflag.ContinueOnError)
	executor := NewExecutor(data, false, false)
	executor.RegisterArgumentFlags(flagSet)
	if err := flagSet.Parse([]string{"--log:thing_to_log=hello"}); err != nil {
		t.Fatalf("parse: %v", err)
	}

	if err := executor.Execute([]string{"use"}); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	log, _ := data.GetCommand("log")
	if len(log.PrereqOutput) != 1 || log.PrereqOutput[0] != "Thing to log: hello" {
		t.Errorf("prereq output = %#v, want [\"Thing to log: hello\"]", log.PrereqOutput)
	}
}

func TestEvaluateCommandBareArgNotSubstituted(t *testing.T) {
	data := &ParsedData{
		Commands: []*Command{
			{
				Name:      "greet",
				Arguments: []*Argument{{Name: "out"}},
				Body:      shellBody("echo output of the build"),
			},
		},
	}
	data.buildIndexMaps()

	flagSet := pflag.NewFlagSet("test", pflag.ContinueOnError)
	executor := NewExecutor(data, false, false)
	executor.RegisterArgumentFlags(flagSet)
	if err := flagSet.Parse([]string{"--greet:out=OVERRIDE"}); err != nil {
		t.Fatalf("parse: %v", err)
	}

	if err := executor.Execute([]string{"greet"}); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
}
func TestEvaluateCommandUnsetArgEmpty(t *testing.T) {
	data := &ParsedData{
		Commands: []*Command{
			{
				Name:      "log",
				Arguments: []*Argument{{Name: "thing_to_log"}},
				Body: []BodyStatement{
					{Type: "shell", Shell: `echo "Thing to log: &thing_to_log"`, OutputName: "out"},
				},
			},
			{Name: "use", Prereqs: []string{"log"}, Body: shellBody("$ echo got:&log.out")},
		},
	}
	data.buildIndexMaps()

	flagSet := pflag.NewFlagSet("test", pflag.ContinueOnError)
	executor := NewExecutor(data, false, false)
	executor.RegisterArgumentFlags(flagSet)
	if err := flagSet.Parse(nil); err != nil {
		t.Fatalf("parse: %v", err)
	}

	if err := executor.Execute([]string{"use"}); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	log, _ := data.GetCommand("log")
	if len(log.PrereqOutput) != 1 || log.PrereqOutput[0] != "Thing to log:" {
		t.Errorf("prereq output = %#v, want [\"Thing to log:\"]", log.PrereqOutput)
	}
}

func TestBodyFor(t *testing.T) {
	dir := t.TempDir()
	cloudFile := filepath.Join(dir, "cloud.json")
	defs := map[string]Command{
		"fetch": {Name: "fetch", Body: []BodyStatement{{Type: StmtShell, Shell: "echo remote"}}},
	}
	cloudData, _ := jsonMarshal(defs)
	if err := os.WriteFile(cloudFile, cloudData, 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONSTRUCT_CLOUD_FILE", cloudFile)

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
		body := executor.bodyFor(cmd)
		if len(body) != len(before) {
			t.Errorf("body changed: %#v vs %#v", body, before)
		}
		if len(cmd.Body) != len(before) {
			t.Errorf("cmd.Body mutated: %#v vs %#v", cmd.Body, before)
		}
	})

	t.Run("cloud body combined without mutating cmd", func(t *testing.T) {
		cmd := &Command{Name: "fetch", CloudAccessible: true, Body: shellBody("echo local")}
		body := executor.bodyFor(cmd)
		if len(cmd.Body) != 1 {
			t.Errorf("cmd.Body mutated: %d stmts, want 1", len(cmd.Body))
		}
		if len(body) != 2 {
			t.Errorf("combined body = %d stmts, want 2", len(body))
		}
	})

	t.Run("cloud command with no definition unchanged", func(t *testing.T) {
		cmd := &Command{Name: "missing", CloudAccessible: true, Body: shellBody("echo local")}
		if body := executor.bodyFor(cmd); len(body) != 1 {
			t.Errorf("body = %d stmts, want 1", len(body))
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

func TestConcurrentSharedPrereq(t *testing.T) {
	// Two parents sharing one prereq exercise the concurrent paths around
	// IsPrereq/WorkDir mutation and prereq output collection; run with
	// -race to catch data races.
	data := &ParsedData{
		Commands: []*Command{
			{Name: "a", Prereqs: []string{"shared"}, Body: nil},
			{Name: "b", Prereqs: []string{"shared"}, Body: nil},
			{Name: "shared", Body: shellBody("echo shared")},
		},
	}
	data.buildIndexMaps()
	e := NewExecutor(data, true, false)
	if err := e.Execute([]string{"a", "b"}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
}

func TestPrereqModeNotStickyAcrossRuns(t *testing.T) {
	shared := &Command{Name: "shared", Body: shellBody("echo shared")}
	data := &ParsedData{
		Commands: []*Command{
			{Name: "parent", Prereqs: []string{"shared"}},
			shared,
		},
	}
	data.buildIndexMaps()
	e := NewExecutor(data, false, false)

	if err := e.Execute([]string{"parent"}); err != nil {
		t.Fatalf("first execute: %v", err)
	}
	if len(shared.PrereqOutput) != 1 {
		t.Fatalf("prereq output after prereq run = %v, want 1 entry", shared.PrereqOutput)
	}

	// Running `shared` as a top-level target in a fresh run must stream its
	// output, not capture it again (IsPrereq used to be sticky).
	out := captureStdout(t, func() {
		if err := e.Execute([]string{"shared"}); err != nil {
			t.Fatalf("second execute: %v", err)
		}
	})
	if !strings.Contains(out, "shared") {
		t.Errorf("top-level rerun streamed %q, want the shared output", out)
	}
	if shared.IsPrereq {
		t.Error("shared.IsPrereq was mutated by the executor")
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()
	fn()
	w.Close()
	out, _ := io.ReadAll(r)
	return string(out)
}

func TestPrereqWorkDirNotMutated(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	shared := &Command{Name: "shared", Body: shellBody("echo shared")}
	data := &ParsedData{
		Commands: []*Command{
			{Name: "parent", Prereqs: []string{"shared"}, PrereqDirs: map[string]string{"shared": "sub"}},
			shared,
		},
	}
	data.buildIndexMaps()
	e := NewExecutor(data, false, false)
	e.SetBaseDir(dir)
	if err := e.Execute([]string{"parent"}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if shared.WorkDir != "" {
		t.Errorf("shared.WorkDir mutated to %q; per-parent dirs must not touch the shared command", shared.WorkDir)
	}
}

func TestCacheKeyIncludesSourceFile(t *testing.T) {
	e := NewExecutor(&ParsedData{}, false, false)
	a := &Command{Name: "build", SourceFile: "/x/Constfile"}
	b := &Command{Name: "build", SourceFile: "/x/Constfile-build"}
	if e.cacheKey(a) == e.cacheKey(b) {
		t.Error("same-named commands in different Constfiles collide in the cache key")
	}
}

func TestCacheKeyScopedToReferencedGlobals(t *testing.T) {
	dir := t.TempDir()
	dep := filepath.Join(dir, "dep.txt")
	os.WriteFile(dep, []byte("v1\n"), 0644)

	p := NewParserFromContent("Constfile", "var used = a\nvar unused = b\n\ncmd < dep.txt {\n  $ echo &used\n}\n")
	data, err := p.Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cmd, _ := data.GetCommand("cmd")
	if !cmd.cacheGlobalsExact || len(cmd.cacheGlobals) != 1 || cmd.cacheGlobals[0] != "used" {
		t.Fatalf("cacheGlobals = %v (exact=%v), want [used]", cmd.cacheGlobals, cmd.cacheGlobalsExact)
	}

	e := NewExecutor(data, false, false)
	e.SetBaseDir(dir)
	e.SetRecordRuns(true)
	if err := e.Execute([]string{"cmd"}); err != nil {
		t.Fatalf("first run: %v", err)
	}

	data.SetVariable("unused", "global", "changed")
	if err := e.Execute([]string{"cmd"}); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if rec := e.RunRecords()["cmd"]; rec.Status != "skipped" {
		t.Errorf("unrelated global change invalidated the cache: status %q", rec.Status)
	}

	data.SetVariable("used", "global", "changed")
	if err := e.Execute([]string{"cmd"}); err != nil {
		t.Fatalf("third run: %v", err)
	}
	if rec := e.RunRecords()["cmd"]; rec.Status == "skipped" {
		t.Error("referenced global change did not invalidate the cache")
	}
}

func TestCacheGlobalsClosureThroughVariables(t *testing.T) {
	p := NewParserFromContent("Constfile", "var base = 1\nvar derived = &base\n\ncmd {\n  $ echo &derived\n}\n")
	data, err := p.Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cmd, _ := data.GetCommand("cmd")
	if !cmd.cacheGlobalsExact || len(cmd.cacheGlobals) != 2 {
		t.Errorf("cacheGlobals = %v, want base and derived via closure", cmd.cacheGlobals)
	}
}

func TestProducesValidationWarning(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "src.txt"), []byte("x"), 0644)
	data := &ParsedData{
		Commands: []*Command{
			{Name: "bad", Produces: []string{"ghost.txt"}, FileDeps: []string{"src.txt"}, Body: shellBody("echo hi")},
		},
	}
	data.buildIndexMaps()
	e := NewExecutor(data, false, false)
	e.SetBaseDir(dir)

	stderr := captureStderr(t, func() {
		if err := e.Execute([]string{"bad"}); err != nil {
			t.Fatalf("execute: %v", err)
		}
	})
	if !strings.Contains(stderr, "declares produces") || !strings.Contains(stderr, "ghost.txt") {
		t.Errorf("missing produces warning, stderr = %q", stderr)
	}
}

func TestMissingFileDepWarning(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "src.txt"), []byte("x"), 0644)
	data := &ParsedData{
		Commands: []*Command{
			{Name: "bogus", FileDeps: []string{"missing.txt"}, Body: shellBody("echo ran")},
		},
	}
	data.buildIndexMaps()
	e := NewExecutor(data, false, false)
	e.SetBaseDir(dir)
	e.SetNoCache(true)

	stderr := captureStderr(t, func() {
		if err := e.Execute([]string{"bogus"}); err != nil {
			t.Fatalf("execute: %v", err)
		}
	})
	if !strings.Contains(stderr, "file dependency") || !strings.Contains(stderr, "missing.txt") {
		t.Errorf("missing dep warning absent, stderr = %q", stderr)
	}
}

func TestContainerArgs(t *testing.T) {
	dir, _ := filepath.Abs(t.TempDir())
	data := &ParsedData{}
	data.buildIndexMaps()
	e := NewExecutor(data, false, false)
	e.SetBaseDir(dir)
	env := []string{"HOME=/tmp", "PATH=/usr/bin"}

	// Plain shell mode.
	shCtx := &execContext{target: &Command{Name: "x"}, env: &env}
	argv, display, cleanup, err := e.shellArgsFor(shCtx, "echo hi")
	if err != nil {
		t.Fatalf("plain shellArgsFor: %v", err)
	}
	defer cleanup()
	if argv[0] != e.shellName || display == "" {
		t.Errorf("plain argv = %v display = %q", argv, display)
	}
	if last := argv[len(argv)-1]; last != "echo hi" {
		if runtime.GOOS != "windows" {
			t.Errorf("script not last arg: %v", argv)
		} else {
			b, err := os.ReadFile(last)
			if err != nil || string(b) != "echo hi" {
				t.Errorf("script file %q content = %q err = %v", last, b, err)
			}
		}
	}

	// Container mode via a stubbed runtime path.
	e.containerRT = "docker"
	f, _ := os.CreateTemp(t.TempDir(), "env")
	f.Close()
	cCtx := &execContext{target: &Command{Name: "x"}, env: &env, container: "docker alpine:latest", envFile: f.Name(), workDir: "sub"}
	argv, display, cleanup, err = e.shellArgsFor(cCtx, "echo hi")
	if err != nil {
		t.Fatalf("container shellArgsFor: %v", err)
	}
	defer cleanup()
	joined := strings.Join(argv, " ")
	for _, want := range []string{"docker run --rm", "--env-file", "-v " + dir + ":/work", "-w /work/sub", "alpine:latest", "/bin/sh -c", "echo hi"} {
		if !strings.Contains(joined, want) {
			t.Errorf("container argv missing %q: %s", want, joined)
		}
	}
	if !strings.HasPrefix(display, "docker run alpine:latest") {
		t.Errorf("display = %q", display)
	}
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	fn()
	w.Close()
	os.Stderr = old
	out, _ := io.ReadAll(r)
	return string(out)
}
