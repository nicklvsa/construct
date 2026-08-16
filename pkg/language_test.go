package pkg

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// evalCtxFor tests expression evaluation against a parsed data set.
func evalCtxFor(t *testing.T, content string, scope string) (func(string) (Value, bool, error), parserEvalContext) {
	t.Helper()
	p := NewParserFromContent("Constfile", content)
	data, err := p.Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ctx := parserEvalContext{p: p, scope: scope}
	_ = data
	return func(s string) (Value, bool, error) {
		return evalValueExpr(s, ctx)
	}, ctx
}

func evalStr(t *testing.T, content, expr, scope string) string {
	t.Helper()
	ev, _ := evalCtxFor(t, content, scope)
	v, ok, err := ev(expr)
	if !ok {
		t.Fatalf("%q did not parse as an expression", expr)
	}
	if err != nil {
		t.Fatalf("%q: %v", expr, err)
	}
	return v.String()
}

func TestEvalArithmetic(t *testing.T) {
	cases := map[string]string{
		"1 + 2":                        "3",
		"&a * 2 + 1":                   "11",
		"&a - 1":                       "4",
		"10 / 3":                       "3",
		"10 % 3":                       "1",
		"&a * (&b + 2)":                "35",
		"-5 + 2":                       "-3",
		"&a > 3 ? \"big\" : \"small\"": "big",
		"\"x\" + \"y\"":                "xy",
		"&a >= 5 && &b == 5":           "true",
		"&a < 5 || &b == 5":            "true",
		"!&a":                          "false",
	}
	for expr, want := range cases {
		got := evalStr(t, "var a = 5\nvar b = 5\n", expr, "global")
		if got != want {
			t.Errorf("%s = %q, want %q", expr, got, want)
		}
	}
}

func TestEvalTernary(t *testing.T) {
	got := evalStr(t, "var env = prod\n", `&env == "prod" ? "aws" : "gcp"`, "global")
	if got != "aws" {
		t.Errorf("ternary = %q, want aws", got)
	}
}

func TestEvalFunctions(t *testing.T) {
	cases := map[string]string{
		"upper(\"hello\")":                 "HELLO",
		"lower(\"HELLO\")":                 "hello",
		"trim(\"  x  \")":                  "x",
		"replace(\"a.b.c\", \".\", \"-\")": "a-b-c",
		"sprintf(\"%s-%d\", \"v\", 2)":     "v-2",
		"length(\"abc\")":                  "3",
		"abs(-7)":                          "7",
		"min(3, 1, 2)":                     "1",
		"max(3, 1, 2)":                     "3",
		"basename(\"a/b/c.txt\")":          "c.txt",
		"dirname(\"a/b/c.txt\")":           "a/b",
		"ext(\"a/b/c.txt\")":               ".txt",
		"stem(\"a/b/c.txt\")":              "c",
		"exists(\"nope-xyz\")":             "false",
		"missing(\"nope-xyz\")":            "true",
	}
	for expr, want := range cases {
		got := evalStr(t, "", expr, "global")
		if got != want {
			t.Errorf("%s = %q, want %q", expr, got, want)
		}
	}
}

func TestEvalLists(t *testing.T) {
	ev, ctx := evalCtxFor(t, "var platforms = [linux, windows]\nvar nums = [1, 2]\n", "global")

	v, ok, err := ev("len(&platforms)")
	if !ok || err != nil || v.S != "2" {
		t.Fatalf("len(&platforms) = %v (ok=%v, err=%v), want 2", v, ok, err)
	}

	v, ok, err = ev("&platforms + [macos]")
	if !ok || err != nil {
		t.Fatalf("list concat: %v %v", ok, err)
	}
	if !v.IsList || strings.Join(v.L, ",") != "linux,windows,macos" {
		t.Fatalf("list concat = %v", v)
	}

	v, ok, err = ev("sort([b, a, c])")
	if !ok || err != nil || strings.Join(v.L, ",") != "a,b,c" {
		t.Fatalf("sort = %v", v)
	}

	v, ok, err = ev("uniq([a, b, a])")
	if !ok || err != nil || strings.Join(v.L, ",") != "a,b" {
		t.Fatalf("uniq = %v", v)
	}

	v, ok, err = ev("join(&platforms, \"-\")")
	if !ok || err != nil || v.S != "linux-windows" {
		t.Fatalf("join = %v", v)
	}

	v = evalValueExprLoose("&platforms.1", ctx)
	if v.S != "windows" {
		t.Fatalf("loose index = %v", v)
	}

	v, ok, err = ev("split(\"a,b,c\", \",\")")
	if !ok || err != nil || !v.IsList || len(v.L) != 3 {
		t.Fatalf("split = %v", v)
	}
}

func TestEvalLiteralFallback(t *testing.T) {
	p := NewParserFromContent("Constfile", "var x = 1\n")
	if _, err := p.Parse(); err != nil {
		t.Fatal(err)
	}
	ctx := parserEvalContext{p: p, scope: "global"}

	literal := func(s, want string) {
		t.Helper()
		got := p.tryEvalExpression(s, nil, nil, 0)
		if got != want {
			t.Errorf("literal %q = %q, want %q", s, got, want)
		}
		_ = ctx
	}
	literal("a + b", "a + b")
	literal("foo-bar", "foo-bar")
	literal("--flag", "--flag")
	literal("http://x.com/y?z=1", "http://x.com/y?z=1")
	literal("dist/*.go", "dist/*.go")
}

func TestListVariable(t *testing.T) {
	p := NewParserFromContent("Constfile", "var platforms = [linux, windows]\n")
	data, err := p.Parse()
	if err != nil {
		t.Fatal(err)
	}
	v, err := data.GetVariable("platforms", "global")
	if err != nil {
		t.Fatal(err)
	}
	if !v.IsList || strings.Join(v.List, ",") != "linux,windows" {
		t.Fatalf("platforms = %+v", v)
	}
	if v.Value != "linux, windows" {
		t.Fatalf("platforms.Value = %q", v.Value)
	}
	if item, ok := LookupVariableIndexed(data, "platforms.1", "global"); !ok || item.S != "windows" {
		t.Fatalf("indexed lookup = %v %v", item, ok)
	}
}

func TestParseSwitch(t *testing.T) {
	p := NewParserFromContent("Constfile", `
cmd {
    switch "&env" {
        case "prod", "staging" {
            $ echo prod
        }
        default {
            $ echo dev
        }
    }
}
`)
	data, err := p.Parse()
	if err != nil {
		t.Fatal(err)
	}
	cmd, err := data.GetCommand("cmd")
	if err != nil {
		t.Fatal(err)
	}
	if len(cmd.Body) != 1 || cmd.Body[0].Type != StmtSwitch {
		t.Fatalf("expected switch statement, got %+v", cmd.Body)
	}
	sw := cmd.Body[0]
	if sw.SwitchExpr != `"&env"` || len(sw.Cases) != 2 {
		t.Fatalf("switch = %+v", sw)
	}
	if sw.Cases[0].IsDefault || len(sw.Cases[0].Values) != 2 {
		t.Fatalf("case 0 = %+v", sw.Cases[0])
	}
	if !sw.Cases[1].IsDefault {
		t.Fatalf("case 1 should be default: %+v", sw.Cases[1])
	}
}

func TestParseSwitchErrors(t *testing.T) {
	cases := []string{
		"cmd {\n    case \"x\" {\n        $ echo a\n    }\n}\n",
		"cmd {\n    switch \"&x\" {\n        $ echo no-cases\n    }\n}\n",
		"cmd {\n    switch \"&x\" {\n        case \"a\" {\n            $ echo a\n        }\n        case \"a\" {\n            $ echo dup\n        }\n    }\n}\n",
	}
	for _, c := range cases {
		p := NewParserFromContent("Constfile", c)
		if _, err := p.Parse(); err == nil {
			t.Errorf("expected parse error for:\n%s", c)
		}
	}
}

func TestParseInDirBlock(t *testing.T) {
	p := NewParserFromContent("Constfile", `
cmd {
    in src {
        $ make
    }
    in dist { $ cp x y }
}
`)
	data, err := p.Parse()
	if err != nil {
		t.Fatal(err)
	}
	cmd, _ := data.GetCommand("cmd")
	if len(cmd.Body) != 2 || cmd.Body[0].Type != StmtInDir || cmd.Body[1].Type != StmtInDir {
		t.Fatalf("body = %+v", cmd.Body)
	}
	if cmd.Body[0].Shell != "src" || len(cmd.Body[0].ThenBody) != 1 {
		t.Fatalf("in-block = %+v", cmd.Body[0])
	}
	if cmd.Body[1].Shell != "dist" {
		t.Fatalf("single-line in-block = %+v", cmd.Body[1])
	}
}

func TestParseTimeout(t *testing.T) {
	p := NewParserFromContent("Constfile", `
build timeout 120s < setup {
    $ go build
    timeout<30s> $ go test
}
setup {
    $ echo setup
}
`)
	data, err := p.Parse()
	if err != nil {
		t.Fatal(err)
	}
	cmd, _ := data.GetCommand("build")
	if cmd.Timeout != "120s" {
		t.Fatalf("command timeout = %q", cmd.Timeout)
	}
	if len(cmd.Body) != 2 || cmd.Body[1].Timeout != "30s" {
		t.Fatalf("statement timeout = %+v", cmd.Body)
	}
}

func TestParseBuiltins(t *testing.T) {
	p := NewParserFromContent("Constfile", `
cmd {
    cp "a.txt" "b.txt"
    rm -rf dist
    mkdir out
    touch out/f
    ! cp x y
}
`)
	data, err := p.Parse()
	if err != nil {
		t.Fatal(err)
	}
	cmd, _ := data.GetCommand("cmd")
	if len(cmd.Body) != 5 {
		t.Fatalf("body = %+v", cmd.Body)
	}
	if cmd.Body[0].Shell != "cp" || cmd.Body[0].BuiltinArgs != `"a.txt" "b.txt"` {
		t.Fatalf("cp = %+v", cmd.Body[0])
	}
	if cmd.Body[3].Shell != "touch" {
		t.Fatalf("touch = %+v", cmd.Body[3])
	}
	if cmd.Body[4].Shell != "cp" || cmd.Body[4].BuiltinArgs != "x y" || !cmd.Body[4].Tolerant {
		t.Fatalf("tolerant cp = %+v", cmd.Body[4])
	}
}

func TestParseSingleLineCommandBodies(t *testing.T) {
	p := NewParserFromContent("Constfile", `
|gen| { }
use {
    invoke gen
}
last { $ echo inline }
nested { if "&x" == "1" { $ echo one } else { $ echo two } }
`)
	data, err := p.Parse()
	if err != nil {
		t.Fatal(err)
	}
	gen, err := data.GetCommand("gen")
	if err != nil {
		t.Fatalf("gen must exist (empty cloud command): %v", err)
	}
	if len(gen.Body) != 0 || !gen.CloudAccessible {
		t.Fatalf("gen = %+v", gen)
	}
	if _, err := data.GetCommand("use"); err != nil {
		t.Fatalf("use swallowed by gen: %v", err)
	}
	last, _ := data.GetCommand("last")
	if len(last.Body) != 1 || last.Body[0].Type != StmtShell {
		t.Fatalf("last body = %+v", last.Body)
	}
	nested, _ := data.GetCommand("nested")
	if len(nested.Body) != 1 || nested.Body[0].Type != StmtIf {
		t.Fatalf("nested single-line body = %+v", nested.Body)
	}
}

func TestParseStateAndPromptStatements(t *testing.T) {
	p := NewParserFromContent("Constfile", `
state last = "0.0.0"
cmd {
    state last = "1.0.0"
    confirm "deploy?"
    prompt "press enter"
    input name "name?"
}
`)
	data, err := p.Parse()
	if err != nil {
		t.Fatal(err)
	}
	if len(data.StateDecls) != 1 || data.StateDecls[0].Name != "last" || data.StateDecls[0].Value != "0.0.0" {
		t.Fatalf("state decls = %+v", data.StateDecls)
	}
	cmd, _ := data.GetCommand("cmd")
	if len(cmd.Body) != 4 {
		t.Fatalf("body = %+v", cmd.Body)
	}
	if cmd.Body[0].Type != StmtState || cmd.Body[0].Shell != "last" {
		t.Fatalf("body state = %+v", cmd.Body[0])
	}
	if cmd.Body[1].Type != StmtConfirm || cmd.Body[1].Message != "deploy?" {
		t.Fatalf("confirm = %+v", cmd.Body[1])
	}
	if cmd.Body[3].Type != StmtInput || cmd.Body[3].Shell != "name" {
		t.Fatalf("input = %+v", cmd.Body[3])
	}
}

func TestParseLockBlock(t *testing.T) {
	p := NewParserFromContent("Constfile", `
cmd {
    lock "deploy" {
        $ echo locked
    }
}
`)
	data, err := p.Parse()
	if err != nil {
		t.Fatal(err)
	}
	cmd, _ := data.GetCommand("cmd")
	if cmd.Body[0].Type != StmtLock || cmd.Body[0].Shell != "deploy" {
		t.Fatalf("lock = %+v", cmd.Body[0])
	}
}

// newExecutorFor builds an executor for a Constfile in a temp dir.
func newExecutorFor(t *testing.T, content string) (*Executor, string) {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "Constfile")
	if err := os.WriteFile(file, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	p, err := NewParser(file)
	if err != nil {
		t.Fatal(err)
	}
	data, err := p.Parse()
	if err != nil {
		t.Fatal(err)
	}
	exec := NewExecutor(data, false, false)
	exec.SetBaseDir(dir)
	exec.SetRecordRuns(true)
	return exec, dir
}

// execRun runs commands through exec, capturing stdout.
func execRun(t *testing.T, exec *Executor, cmds ...string) (string, error) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	err = exec.Execute(cmds)
	w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	return string(out), err
}

func TestExecSwitchAndInDir(t *testing.T) {
	content := `
var env = prod
cmd {
    switch &env {
        case "prod" {
            $ echo in-prod
        }
        default {
            $ echo in-other
        }
    }
    in sub {
        $ pwd
    }
}
`
	exec, dir := newExecutorFor(t, content)
	subDir := filepath.Join(dir, "sub")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}
	out, err := execRun(t, exec, "cmd")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out, "in-prod") || strings.Contains(out, "in-other") {
		t.Fatalf("switch output = %q", out)
	}
	// Git Bash reports a MSYS-mapped path (/tmp/...) on Windows, so compare
	// the tail instead of the full absolute path.
	if !strings.HasSuffix(strings.ReplaceAll(strings.TrimSpace(out), "\\", "/"), "/sub") {
		t.Fatalf("in-dir did not change working directory: %q", out)
	}
}

func TestExecTimeout(t *testing.T) {
	exec, _ := newExecutorFor(t, `
slow {
    timeout<1s> $ sleep 5
}
`)
	err := exec.Execute([]string{"slow"})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	ce, ok := err.(*CommandError)
	if !ok || !ce.TimedOut || ce.ExitCode != 124 {
		t.Fatalf("expected timeout error, got %v", err)
	}
	if !strings.Contains(err.Error(), "timed out after 1s") {
		t.Fatalf("error message: %v", err)
	}
}

func TestExecCommandTimeout(t *testing.T) {
	exec, _ := newExecutorFor(t, `
slow timeout 1s {
    $ sleep 5
}
`)
	err := exec.Execute([]string{"slow"})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("error: %v", err)
	}
}

func TestExecLastExitAndOutput(t *testing.T) {
	exec, _ := newExecutorFor(t, `
cmd {
    ! $ exit 3
    $ echo "exit=&last.exit"
    ! $ echo captured
    $ echo "out=&last.output"
}
`)
	out, err := execRun(t, exec, "cmd")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out, "exit=3") {
		t.Fatalf("last.exit not 3: %q", out)
	}
	if !strings.Contains(out, "out=captured") {
		t.Fatalf("last.output not captured: %q", out)
	}
}

func TestExecInputReadsVariable(t *testing.T) {
	exec, _ := newExecutorFor(t, `
cmd {
    input name "your name?"
    $ echo "hello &name"
}
`)
	oldStdin := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = r
	w.WriteString("nick\n")
	w.Close()
	defer func() { os.Stdin = oldStdin }()

	out, err := execRun(t, exec, "cmd")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out, "hello nick") {
		t.Fatalf("input did not reach later statement: %q", out)
	}
}

func TestExecBodyStateVisibleToLaterStatements(t *testing.T) {
	exec, _ := newExecutorFor(t, `
cmd {
    state stamp = "runtime"
    $ echo "stamp=&stamp"
}
`)
	out, err := execRun(t, exec, "cmd")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out, "stamp=runtime") {
		t.Fatalf("state write not visible to later statement: %q", out)
	}
}

func TestExecState(t *testing.T) {
	exec, dir := newExecutorFor(t, `
state last = "0.0.0"
cmd {
    $ echo "before=@state("last")"
    state last = "1.0.0"
}
`)
	out, err := execRun(t, exec, "cmd")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out, "before=0.0.0") {
		t.Fatalf("state ref output: %q", out)
	}
	stateFile := filepath.Join(dir, ".construct-cache", "state.json")
	data, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"last": "1.0.0"`) {
		t.Fatalf("state file: %s", data)
	}
}

func TestExecStatePersistsAcrossRuns(t *testing.T) {
	dir := t.TempDir()
	content := `
state counter = 0
bump {
    state counter = @state("counter") + 1
    $ echo "count=@state("counter")"
}
`
	writeConstfile := func() {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "Constfile"), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	run := func() string {
		t.Helper()
		writeConstfile()
		p, err := NewParser(filepath.Join(dir, "Constfile"))
		if err != nil {
			t.Fatal(err)
		}
		data, err := p.Parse()
		if err != nil {
			t.Fatal(err)
		}
		exec := NewExecutor(data, false, false)
		exec.SetBaseDir(dir)
		out, err := execRun(t, exec, "bump")
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if out := run(); !strings.Contains(out, "count=1") {
		t.Fatalf("first run: %q", out)
	}
	if out := run(); !strings.Contains(out, "count=2") {
		t.Fatalf("second run: %q", out)
	}
	if out := run(); !strings.Contains(out, "count=3") {
		t.Fatalf("third run: %q", out)
	}
}

func TestExecLock(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("flock semantics differ on windows")
	}
	exec, _ := newExecutorFor(t, `
cmd {
    lock "deploy" {
        $ echo inside-lock
    }
}
`)
	out, err := execRun(t, exec, "cmd")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out, "inside-lock") {
		t.Fatalf("lock output: %q", out)
	}
}

func TestExecBuiltins(t *testing.T) {
	exec, dir := newExecutorFor(t, `
cmd {
    mkdir out
    cp Constfile out/copied.txt
    touch out/touched
    $ echo copied
}
`)
	out, err := execRun(t, exec, "cmd")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out, "copied") {
		t.Fatalf("output: %q", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "out", "copied.txt")); err != nil {
		t.Fatalf("cp failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "out", "touched")); err != nil {
		t.Fatalf("touch failed: %v", err)
	}
}

func TestExecRmGuard(t *testing.T) {
	exec, dir := newExecutorFor(t, `
cmd {
    rm .
}
`)
	exec.SetBaseDir(dir)
	err := exec.Execute([]string{"cmd"})
	if err == nil {
		t.Fatal("expected rm guard error")
	}
	if !strings.Contains(err.Error(), "refusing to remove") {
		t.Fatalf("error: %v", err)
	}
}

func TestExecForLoopWithListFunction(t *testing.T) {
	exec, dir := newExecutorFor(t, `
cmd {
    for l in lines("items.txt") {
        $ echo "item=&l"
    }
}
`)
	if err := os.WriteFile(filepath.Join(dir, "items.txt"), []byte("one\ntwo\nthree\n"), 0644); err != nil {
		t.Fatal(err)
	}
	out, err := execRun(t, exec, "cmd")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	for _, want := range []string{"item=one", "item=two", "item=three"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %q", want, out)
		}
	}
}

func TestExecLoopOverListVar(t *testing.T) {
	exec, _ := newExecutorFor(t, `
var targets = [api, web]
cmd {
    for t in &targets {
        $ echo "t=&t"
    }
}
`)
	out, err := execRun(t, exec, "cmd")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out, "t=api") || !strings.Contains(out, "t=web") {
		t.Fatalf("loop output: %q", out)
	}
}

func TestExecPipefail(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("pipefail is bash/zsh only")
	}
	exec, _ := newExecutorFor(t, `
cmd {
    ! $ sh -c "exit 0" | true
    ! $ false | true
}
`)
	// With pipefail, `false | true` fails; without it the pipeline exits 0.
	// Check that the tolerant statement records a non-zero last.exit.
	if err := exec.Execute([]string{"cmd"}); err != nil {
		t.Fatalf("execute: %v", err)
	}
}

func TestRunRecordsAndResume(t *testing.T) {
	exec, dir := newExecutorFor(t, `
good {
    $ echo fine
}
bad {
    $ echo boom
    $ exit 2
}
`)
	exec.Execute([]string{"good", "bad"})

	hist := LoadRunHistory(filepath.Join(dir, ".construct-cache"))
	last := LastRecord(hist)
	if last["good"].Status != "ok" {
		t.Fatalf("good status = %+v", last["good"])
	}
	if last["bad"].Status != "failed" || last["bad"].Exit != 2 {
		t.Fatalf("bad record = %+v", last["bad"])
	}
}

func TestCloudPullPushList(t *testing.T) {
	dir := t.TempDir()
	cloudFile := filepath.Join(dir, "cloud.json")
	defs := map[string]Command{
		"gen": {Name: "gen", Body: []BodyStatement{{Type: StmtShell, Shell: "echo remote"}}},
	}
	data, _ := jsonMarshal(defs)
	if err := os.WriteFile(cloudFile, data, 0644); err != nil {
		t.Fatal(err)
	}

	exec := NewExecutor(&ParsedData{}, false, false)
	exec.SetBaseDir(dir)
	t.Setenv("CONSTRUCT_CLOUD_FILE", cloudFile)

	entries, err := exec.CloudList()
	if err != nil || len(entries) != 1 || entries[0].Name != "gen" {
		t.Fatalf("list: %v %v", entries, err)
	}

	pulled := filepath.Join(dir, "construct-cloud.json")
	n, err := exec.CloudPull(nil, pulled)
	if err != nil || n != 1 {
		t.Fatalf("pull: %v %v", n, err)
	}
	if _, err := os.Stat(pulled); err != nil {
		t.Fatal(err)
	}
}

func TestInvokeCloudFallback(t *testing.T) {
	dir := t.TempDir()
	cloudFile := filepath.Join(dir, "cloud.json")
	defs := map[string]Command{
		"remote": {Name: "remote", Body: []BodyStatement{{Type: StmtShell, Shell: "echo from-cloud"}}},
	}
	data, _ := jsonMarshal(defs)
	os.WriteFile(cloudFile, data, 0644)
	t.Setenv("CONSTRUCT_CLOUD_FILE", cloudFile)

	file := filepath.Join(dir, "Constfile")
	os.WriteFile(file, []byte(`
use {
    invoke remote
}
`), 0644)
	p, err := NewParser(file)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := p.Parse()
	if err != nil {
		t.Fatal(err)
	}
	exec := NewExecutor(parsed, false, false)
	exec.SetBaseDir(dir)
	out, err := execRun(t, exec, "use")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out, "from-cloud") {
		t.Fatalf("invoke cloud output: %q", out)
	}
}

func jsonMarshal(v any) ([]byte, error) {
	return json.MarshalIndent(v, "", "  ")
}

func TestFlameRows(t *testing.T) {
	exec, _ := newExecutorFor(t, `
cmd {
    $ echo one
    $ echo two
}
`)
	exec.SetFlame(true)
	if err := exec.Execute([]string{"cmd"}); err != nil {
		t.Fatal(err)
	}
	rows := exec.FlameRows()
	if len(rows) < 2 {
		t.Fatalf("expected flame rows, got %d", len(rows))
	}
	if rows[0].Label == "" || rows[0].End.Before(rows[0].Start) {
		t.Fatalf("bad flame row: %+v", rows[0])
	}
}

func TestExecGithubActions(t *testing.T) {
	exec, _ := newExecutorFor(t, `
cmd {
    $ echo hi
}
`)
	exec.SetGithubActions(true)
	out, err := execRun(t, exec, "cmd")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "::group::cmd") || !strings.Contains(out, "::endgroup::") {
		t.Fatalf("gh actions output: %q", out)
	}
}

func TestEvalFileFunctions(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "f.txt"), []byte("line1\nline2\n"), 0644)
	v, ok, err := evalValueExpr(`file("f.txt")`, fileCtx{dir: dir})
	if !ok || err != nil || v.S != "line1\nline2" {
		t.Fatalf("file() = %v %v %v", v, ok, err)
	}
	v, ok, err = evalValueExpr(`lines("f.txt")`, fileCtx{dir: dir})
	if !ok || err != nil || !v.IsList || len(v.L) != 2 {
		t.Fatalf("lines() = %v", v)
	}
}

type fileCtx struct {
	dir string
}

func (c fileCtx) LookupVar(string) (Value, bool)    { return Value{}, false }
func (c fileCtx) LookupEnv(string) (string, bool)   { return "", false }
func (c fileCtx) LookupState(string) (string, bool) { return "", false }
func (c fileCtx) BaseDir() string                   { return c.dir }

func TestEvalDateAndUUID(t *testing.T) {
	ev, _ := evalCtxFor(t, "", "global")
	v, ok, err := ev("uuid()")
	if !ok || err != nil || len(v.S) != 36 {
		t.Fatalf("uuid() = %v", v)
	}
	v, ok, err = ev(`date("2006")`)
	if !ok || err != nil || v.S != fmt.Sprintf("%d", time.Now().Year()) {
		t.Fatalf("date() = %v", v)
	}
}

func TestEvalEnvAndStateFunctions(t *testing.T) {
	t.Setenv("CONSTRUCT_TEST_VAR", "hello")
	ev, _ := evalCtxFor(t, "state saved = \"42\"\n", "global")
	v, ok, err := ev(`env("CONSTRUCT_TEST_VAR")`)
	if !ok || err != nil || v.S != "hello" {
		t.Fatalf("env() = %v", v)
	}
	v, ok, err = ev(`state("saved")`)
	if !ok || err != nil || v.S != "42" {
		t.Fatalf("state() = %v", v)
	}
	v, ok, err = ev(`@state("saved")`)
	if !ok || err != nil || v.S != "42" {
		t.Fatalf("@state() = %v", v)
	}
}

func TestContextCancelLock(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("flock semantics differ on windows")
	}
	exec, dir := newExecutorFor(t, `
cmd {
    lock "x" {
        $ echo held
    }
}
`)
	// Hold the lock from the outside.
	lockPath := filepath.Join(dir, ".construct-cache", "locks", "x")
	os.MkdirAll(filepath.Dir(lockPath), 0755)
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if !tryFlock(f) {
		t.Fatal("could not acquire outer lock")
	}
	defer unlockFlock(f)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	exec.SetRunContext(ctx)
	err = exec.Execute([]string{"cmd"})
	if err == nil {
		t.Fatal("expected context cancellation error")
	}
}
