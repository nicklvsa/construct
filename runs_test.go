package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicklvsa/construct/pkg"
)

func seedRunHistory(t *testing.T, hist map[string][]pkg.RunRecord) string {
	t.Helper()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "Constfile"), []byte("run {\n  $ echo hi\n}\n"), 0644)
	pkg.SaveRunHistory(filepath.Join(dir, ".construct-cache"), hist)
	old, _ := os.Getwd()
	os.Chdir(dir)
	t.Cleanup(func() { os.Chdir(old) })
	return dir
}

func TestRunsListAndShow(t *testing.T) {
	now := time.Now()
	seedRunHistory(t, map[string][]pkg.RunRecord{
		"build": {
			{Status: "ok", DurationMs: 10, End: now.Add(-2 * time.Hour)},
			{Status: "failed", Exit: 2, DurationMs: 20, End: now, Error: "boom", Log: "$ make\nboom\n"},
		},
	})

	if err := runRuns(nil, &options{}); err != nil {
		t.Fatalf("runs list: %v", err)
	}
	if err := runRuns([]string{"show", "build"}, &options{}); err != nil {
		t.Fatalf("runs show: %v", err)
	}
	if err := runRuns([]string{"show", "build", "2"}, &options{}); err != nil {
		t.Fatalf("runs show 2: %v", err)
	}
}

func TestRunsShowJSON(t *testing.T) {
	now := time.Now()
	seedRunHistory(t, map[string][]pkg.RunRecord{
		"build": {{Status: "failed", Exit: 1, End: now, Log: "$ go build\nerror x\n"}},
	})

	capture, _ := os.CreateTemp("", "runs-json-*")
	old := os.Stdout
	os.Stdout = capture
	err := runRuns([]string{"show", "build", "1"}, &options{json: true})
	os.Stdout = old
	capture.Close()
	if err != nil {
		t.Fatalf("runs show --json: %v", err)
	}
	data, _ := os.ReadFile(capture.Name())
	os.Remove(capture.Name())
	if !strings.Contains(string(data), `"log": "$ go build\nerror x\n"`) {
		t.Errorf("log missing from JSON output: %s", data)
	}
}

func TestRunsShowErrors(t *testing.T) {
	seedRunHistory(t, map[string][]pkg.RunRecord{
		"build": {{Status: "ok", End: time.Now()}},
	})
	if err := runRuns([]string{"show", "missing"}, &options{}); err == nil {
		t.Error("expected error for unknown command")
	}
	if err := runRuns([]string{"show", "build", "9"}, &options{}); err == nil {
		t.Error("expected error for out-of-range index")
	}
	if err := runRuns([]string{"show", "build", "0"}, &options{}); err == nil {
		t.Error("expected error for index 0")
	}
}

func TestRunsDiff(t *testing.T) {
	seedRunHistory(t, map[string][]pkg.RunRecord{
		"test": {
			{Status: "ok", End: time.Now().Add(-time.Hour), Log: "$ gotest\nPASS\nold line\n"},
			{Status: "ok", End: time.Now(), Log: "$ gotest\nPASS\nnew line\n"},
		},
	})
	if err := runRuns([]string{"diff", "test"}, &options{}); err != nil {
		t.Fatalf("runs diff: %v", err)
	}
	if err := runRuns([]string{"diff", "test", "1", "2"}, &options{}); err != nil {
		t.Fatalf("runs diff explicit: %v", err)
	}
}

func TestDiffLines(t *testing.T) {
	out := diffLines(
		[]string{"a", "b", "c"},
		[]string{"a", "x", "c"},
	)
	joined := strings.Join(out, "\n")
	if !strings.Contains(joined, "- b") || !strings.Contains(joined, "+ x") {
		t.Errorf("diff missing changes: %s", joined)
	}
	if strings.Contains(joined, "- a") || strings.Contains(joined, "+ a") {
		t.Errorf("unchanged line marked as changed: %s", joined)
	}
}

func TestRunsJSONMarshal(t *testing.T) {
	e := runEntry{Name: "x", Index: 2, Record: pkg.RunRecord{Status: "ok"}}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"index":2`) {
		t.Errorf("bad json: %s", b)
	}
}
