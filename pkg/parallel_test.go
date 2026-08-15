package pkg

import (
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/spf13/pflag"
)

func parseBuild(t *testing.T, in string) (*ParsedData, *Command) {
	t.Helper()
	p := NewParserFromContent("t.constfile", in)
	data, err := p.Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cmd, err := data.GetCommand("build")
	if err != nil {
		t.Fatalf("get command: %v", err)
	}
	return data, cmd
}

func runParsed(t *testing.T, in string) (string, error) {
	t.Helper()
	data, _ := parseBuild(t, in)
	return runCaptured(t, data, []string{"build"})
}

func runCaptured(t *testing.T, data *ParsedData, targets []string) (string, error) {
	t.Helper()
	data.buildIndexMaps()
	executor := NewExecutor(data, false, false)

	so, sw, _ := os.Pipe()
	oldOut := os.Stdout
	os.Stdout = sw
	err := executor.Execute(targets)
	os.Stdout = oldOut
	sw.Close()
	out, _ := io.ReadAll(so)
	return string(out), err
}

func TestParallelForParsing(t *testing.T) {
	_, cmd := parseBuild(t, "build {\n    parallel for x in a, b, c {\n        $ echo &x\n    }\n}")
	if len(cmd.Body) != 1 || cmd.Body[0].Type != "for" {
		t.Fatalf("expected for, got %#v", cmd.Body)
	}
	if !cmd.Body[0].Parallel || cmd.Body[0].ParallelJobs != 0 {
		t.Errorf("parallel = %v jobs = %d", cmd.Body[0].Parallel, cmd.Body[0].ParallelJobs)
	}
	if cmd.Body[0].LoopVar != "x" || cmd.Body[0].LoopItems != "a, b, c" {
		t.Errorf("loop = %s in %s", cmd.Body[0].LoopVar, cmd.Body[0].LoopItems)
	}
}

func TestParallelForModifierParsing(t *testing.T) {
	for _, line := range []string{
		"parallel<4> for x in a, b {",
		"parallel<4>for x in a, b {",
		"parallel <4> for x in a, b {",
	} {
		_, cmd := parseBuild(t, "build {\n    "+line+"\n        $ echo &x\n    }\n}")
		if len(cmd.Body) != 1 || !cmd.Body[0].Parallel {
			t.Fatalf("%q: expected parallel for, got %#v", line, cmd.Body)
		}
		if cmd.Body[0].ParallelJobs != 4 {
			t.Errorf("%q: jobs = %d, want 4", line, cmd.Body[0].ParallelJobs)
		}
	}
}

func TestParallelForMalformedModifier(t *testing.T) {
	for _, in := range []string{
		"build {\n    parallel<0> for x in a {\n        echo hi\n    }\n}",
		"build {\n    parallel<x> for x in a {\n        echo hi\n    }\n}",
		"build {\n    parallel<4 for x in a {\n        echo hi\n    }\n}",
	} {
		p := NewParserFromContent("t.constfile", in)
		if _, err := p.Parse(); err == nil {
			t.Errorf("expected error for %q", in)
		}
	}
}

func TestParallelNonLoopStillShell(t *testing.T) {
	_, cmd := parseBuild(t, "build {\n    parallel echo hi\n}")
	if len(cmd.Body) != 1 || cmd.Body[0].Type != "shell" || cmd.Body[0].Parallel {
		t.Fatalf("expected plain shell, got %#v", cmd.Body)
	}
}

func TestParallelMatrixParsing(t *testing.T) {
	_, cmd := parseBuild(t, "build {\n    parallel<3> matrix os in a, b; arch in x, y {\n        $ echo &os/&arch\n    }\n}")
	if len(cmd.Body) != 1 || cmd.Body[0].Type != "for" {
		t.Fatalf("expected outer for, got %#v", cmd.Body)
	}
	outer := cmd.Body[0]
	if !outer.Parallel || outer.ParallelJobs != 3 {
		t.Errorf("outer parallel = %v jobs = %d", outer.Parallel, outer.ParallelJobs)
	}
	if len(outer.LoopBody) != 1 || outer.LoopBody[0].Parallel {
		t.Errorf("inner loop should not be parallel: %#v", outer.LoopBody)
	}
}

func TestParallelForNestedInIf(t *testing.T) {
	_, cmd := parseBuild(t, "build {\n    if \"&env\" == \"prod\" {\n        parallel for x in a, b {\n            $ echo &x\n        }\n    }\n}")
	if len(cmd.Body) != 1 || cmd.Body[0].Type != "if" {
		t.Fatalf("expected if, got %#v", cmd.Body)
	}
	then := cmd.Body[0].ThenBody
	if len(then) != 1 || then[0].Type != "for" || !then[0].Parallel {
		t.Fatalf("expected parallel for in then-body, got %#v", then)
	}
}

func TestParallelForExecution(t *testing.T) {
	out, err := runParsed(t, "build {\n    parallel for x in a, b, c {\n        $ echo item-&x\n    }\n}")
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	var got []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.HasPrefix(line, "[build/") {
			got = append(got, strings.TrimPrefix(line, "[build/"))
		}
	}
	sort.Strings(got)
	want := []string{"a] item-a", "b] item-b", "c] item-c"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("prefixed output = %v, want %v", got, want)
	}
}

func TestParallelForIsolation(t *testing.T) {
	out, err := runParsed(t, `build {
    var prefix = val-
    parallel for x in a, b, c {
        if "&x" == "b" {
            $ echo &prefix&x is b
        } else {
            $ echo &prefix&x is not b
        }
    }
}`)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	for _, want := range []string{"val-a is not b", "val-b is b", "val-c is not b"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q in:\n%s", want, out)
		}
	}
}

func TestParallelForConcurrency(t *testing.T) {
	data, _ := parseBuild(t, "build {\n    parallel for i in 1, 2, 3 {\n        $ sleep 0.3\n    }\n}")
	data.buildIndexMaps()
	executor := NewExecutor(data, false, false)
	start := time.Now()
	err := executor.Execute([]string{"build"})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if elapsed >= 600*time.Millisecond {
		t.Errorf("parallel loop took %v; iterations did not overlap", elapsed)
	}
}

func TestParallelForCapOneRunsInOrder(t *testing.T) {
	out, err := runParsed(t, "build {\n    parallel<1> for i in 1..5 {\n        $ echo n&i\n    }\n}")
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	var got []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if i := strings.Index(line, "] n"); i >= 0 {
			got = append(got, line[i+2:])
		}
	}
	want := "n1|n2|n3|n4|n5"
	if strings.Join(got, "|") != want {
		t.Errorf("ordered output = %v, want %v", got, want)
	}
}

func TestParallelForError(t *testing.T) {
	dir := t.TempDir()
	in := `build {
    parallel for x in a, b, c, d, e, f {
        $ touch ` + filepath.Join(dir, `&x.done`) + `
        if "&x" == "b" {
            fail "boom"
        }
    }
}`
	_, err := runParsed(t, in)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected boom error, got %v", err)
	}
}

func TestParallelForBreak(t *testing.T) {
	_, err := runParsed(t, "build {\n    parallel for x in a, b {\n        $ echo &x\n        break\n    }\n}")
	if err == nil || !strings.Contains(err.Error(), "break is not supported") {
		t.Fatalf("expected break error, got %v", err)
	}
}

func TestParallelForContinueIf(t *testing.T) {
	out, err := runParsed(t, "build {\n    parallel for x in a, b, c {\n        continue if \"&x\" == \"b\"\n        $ echo ran-&x\n    }\n}")
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if strings.Contains(out, "ran-b") {
		t.Errorf("continue if did not skip b:\n%s", out)
	}
	if !strings.Contains(out, "ran-a") || !strings.Contains(out, "ran-c") {
		t.Errorf("missing iterations:\n%s", out)
	}
}

func TestParallelForArguments(t *testing.T) {
	data, _ := parseBuild(t, "build (env) {\n    parallel for x in a, b {\n        $ echo &env=&x\n    }\n}")
	data.buildIndexMaps()

	flagSet := pflag.NewFlagSet("test", pflag.ContinueOnError)
	executor := NewExecutor(data, false, false)
	executor.RegisterArgumentFlags(flagSet)
	if err := flagSet.Parse([]string{"--build:env=prod"}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}

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
	s := string(out)
	if !strings.Contains(s, "prod=a") || !strings.Contains(s, "prod=b") {
		t.Errorf("argument not substituted inside parallel body:\n%s", s)
	}
}

func TestParallelNestedSerialFor(t *testing.T) {
	out, err := runParsed(t, `build {
    parallel for svc in api, web {
        for n in 1, 2 {
            $ echo &svc &n
        }
    }
}`)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	for _, want := range []string{"api 1", "api 2", "web 1", "web 2"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestParallelForDuplicateItems(t *testing.T) {
	out, err := runParsed(t, "build {\n    parallel for x in a, a {\n        $ echo saw-&x\n    }\n}")
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if got := strings.Count(out, "saw-a"); got != 2 {
		t.Errorf("saw-a count = %d, want 2:\n%s", got, out)
	}
}
