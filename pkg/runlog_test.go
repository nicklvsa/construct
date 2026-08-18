package pkg

import (
	"strings"
	"testing"
)

func TestRunLogBufferCap(t *testing.T) {
	b := &runLogBuffer{}
	chunk := strings.Repeat("a", runLogCap)
	b.Write([]byte(chunk))
	b.Write([]byte("tail-end"))

	out := b.String()
	if len(out) > runLogCap+64 {
		t.Fatalf("buffer exceeded cap: %d", len(out))
	}
	if !strings.HasPrefix(out, "... [earlier output truncated]") {
		t.Error("missing truncation marker")
	}
	if !strings.HasSuffix(out, "tail-end") {
		t.Error("tail not preserved")
	}
}

func TestRunLogRecorded(t *testing.T) {
	data := &ParsedData{
		Commands: []*Command{
			{Name: "hello", Body: shellBody("echo hello-log-test")},
		},
	}
	data.buildIndexMaps()

	e := NewExecutor(data, false, false)
	e.SetRecordRuns(true)
	cmd, _ := data.GetCommand("hello")
	if err := e.EvaluateCommand(cmd); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	log := e.RunRecords()["hello"].Log
	if !strings.Contains(log, "$ echo hello-log-test") {
		t.Errorf("statement header missing from log: %q", log)
	}
	if !strings.Contains(log, "hello-log-test\n") {
		t.Errorf("output missing from log: %q", log)
	}
}

func TestRunLogDisabledWithoutRecordRuns(t *testing.T) {
	data := &ParsedData{
		Commands: []*Command{{Name: "hello", Body: shellBody("echo x")}},
	}
	data.buildIndexMaps()

	e := NewExecutor(data, false, false)
	cmd, _ := data.GetCommand("hello")
	if err := e.EvaluateCommand(cmd); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if log := e.RunRecords()["hello"]; log.Log != "" {
		t.Errorf("log recorded without SetRecordRuns: %q", log.Log)
	}
}

func TestRunLogRecordsFailure(t *testing.T) {
	data := &ParsedData{
		Commands: []*Command{{Name: "boom", Body: shellBody("echo pre; echo bad >&2; exit 7")}},
	}
	data.buildIndexMaps()

	e := NewExecutor(data, false, false)
	e.SetRecordRuns(true)
	cmd, _ := data.GetCommand("boom")
	if err := e.EvaluateCommand(cmd); err == nil {
		t.Fatal("expected failure")
	}
	rec := e.RunRecords()["boom"]
	if !strings.Contains(rec.Log, "bad") {
		t.Errorf("stderr missing from failure log: %q", rec.Log)
	}
	if !strings.Contains(rec.Log, "(exit 7)") {
		t.Errorf("exit marker missing from log: %q", rec.Log)
	}
}
