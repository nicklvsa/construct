package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var constructBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "construct-e2e-*")
	if err != nil {
		panic(err)
	}
	constructBin = filepath.Join(dir, "construct")
	out, err := exec.Command("go", "build", "-o", constructBin, ".").CombinedOutput()
	if err != nil {
		panic("build construct: " + err.Error() + "\n" + string(out))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func e2eRun(t *testing.T, dir string, env []string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(constructBin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			t.Fatalf("run construct %v: %v", args, err)
		}
	}
	return string(out), code
}

func e2eConstfile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Constfile"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func e2eWrite(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

const e2eAllFeatures = `var platforms = [linux, windows]
var count = 5
var total = &count * 2 + 1
var tag = &count >= 5 ? "big" : "small"
var ordered = sort([&platforms.1, &platforms.0])
var joined = join(&platforms, "+")
var num = len(&platforms)

state last_release = "0.0.0"

# arithmetic, ternary, lists, functions
features {
    $ echo "t=&total g=&tag o=&ordered j=&joined n=&num"
}

# switch + in + builtins + last.* + lock + state
deploy {
    switch "&tag" {
        case "big" {
            $ echo big-deploy
        }
        default {
            $ echo small-deploy
        }
    }
    in sub {
        $ echo in-sub
    }
    mkdir out
    cp Constfile out/copy.txt
    touch out/touched
    ! $ exit 7
    $ echo "exit=&last.exit"
    lock "deploy-lock" {
        $ echo locked
    }
    state last_release = "9.9.9"
}

slow timeout 1s {
    $ sleep 5
}

iterate {
    for l in lines("names.txt") {
        $ echo "name=&l"
    }
}

cond {
    if missing("nope.txt") {
        $ echo missing-ok
    }
    if require("sh") {
        $ echo shell-ok
    }
}

ok {
    $ echo ok-run
}

bad {
    $ echo bad-run
    $ exit 3
}
`

func TestE2ELanguageFeatures(t *testing.T) {
	dir := e2eConstfile(t, e2eAllFeatures)
	out, code := e2eRun(t, dir, nil, "--no-cache", "features")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	for _, want := range []string{"t=11", "g=big", "o=linux windows", "j=linux+windows", "n=2"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %q", want, out)
		}
	}
}

func TestE2EDeployFeatures(t *testing.T) {
	dir := e2eConstfile(t, e2eAllFeatures)
	out, code := e2eRun(t, dir, nil, "--no-cache", "deploy")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	for _, want := range []string{"big-deploy", "in-sub", "exit=7", "locked"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %q", want, out)
		}
	}
	for _, f := range []string{"out/copy.txt", "out/touched"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("builtin artifact %s missing: %v", f, err)
		}
	}
	state, err := os.ReadFile(filepath.Join(dir, ".construct-cache", "state.json"))
	if err != nil {
		t.Fatalf("state file: %v", err)
	}
	if !strings.Contains(string(state), `"last_release": "9.9.9"`) {
		t.Errorf("state file = %s", state)
	}
}

func TestE2ETimeout(t *testing.T) {
	dir := e2eConstfile(t, e2eAllFeatures)
	out, code := e2eRun(t, dir, nil, "slow")
	if code == 0 {
		t.Fatalf("expected failure, got %s", out)
	}
	if !strings.Contains(out, "timed out after 1s") {
		t.Errorf("missing timeout message in %q", out)
	}
}

func TestE2ELoopAndConditions(t *testing.T) {
	dir := e2eConstfile(t, e2eAllFeatures)
	e2eWrite(t, dir, "names.txt", "one\ntwo\nthree\n")
	out, code := e2eRun(t, dir, nil, "--no-cache", "iterate")
	if code != 0 {
		t.Fatalf("iterate exit %d: %s", code, out)
	}
	for _, want := range []string{"name=one", "name=two", "name=three"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %q", want, out)
		}
	}
	out, code = e2eRun(t, dir, nil, "--no-cache", "cond")
	if code != 0 {
		t.Fatalf("cond exit %d: %s", code, out)
	}
	if !strings.Contains(out, "missing-ok") || !strings.Contains(out, "shell-ok") {
		t.Errorf("cond output = %q", out)
	}
}

func TestE2EResume(t *testing.T) {
	dir := e2eConstfile(t, e2eAllFeatures)
	out, code := e2eRun(t, dir, nil, "ok", "bad")
	if code == 0 {
		t.Fatalf("expected failure, got %s", out)
	}
	if !strings.Contains(out, "bad-run") {
		t.Fatalf("bad did not run: %s", out)
	}
	out, code = e2eRun(t, dir, nil, "--resume")
	if code == 0 {
		t.Fatalf("resume expected failure, got %s", out)
	}
	if !strings.Contains(out, "bad-run") {
		t.Errorf("resume did not rerun bad: %s", out)
	}
	if strings.Contains(out, "ok-run") {
		t.Errorf("resume reran the successful command: %s", out)
	}
	if !strings.Contains(out, "--resume: rerunning 1 failed command(s)") {
		t.Errorf("resume header missing: %s", out)
	}
}

func TestE2ERepeat(t *testing.T) {
	dir := e2eConstfile(t, e2eAllFeatures)
	out, code := e2eRun(t, dir, nil, "--repeat", "2", "--no-cache", "ok", "bad")
	if code == 0 {
		t.Fatalf("expected failure, got %s", out)
	}
	for _, want := range []string{"(run 1/2)", "(run 2/2)", "2 of 2 run(s) failed"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %q", want, out)
		}
	}
}

func TestE2EFlame(t *testing.T) {
	dir := e2eConstfile(t, e2eAllFeatures)
	out, code := e2eRun(t, dir, nil, "--flame", "--no-cache", "features")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	if !strings.Contains(out, "flame:") || !strings.Contains(out, "features:") {
		t.Errorf("flame output = %q", out)
	}
}

func TestE2EGithubActions(t *testing.T) {
	dir := e2eConstfile(t, e2eAllFeatures)
	env := []string{"GITHUB_ACTIONS=true"}
	out, code := e2eRun(t, dir, env, "bad")
	if code == 0 {
		t.Fatalf("expected failure, got %s", out)
	}
	if !strings.Contains(out, "::group::bad") || !strings.Contains(out, "::endgroup::") {
		t.Errorf("missing group markers in %q", out)
	}
	if !strings.Contains(out, "::error file=Constfile") {
		t.Errorf("missing ::error annotation in %q", out)
	}
}

func TestE2EInitDoctorStats(t *testing.T) {
	dir := t.TempDir()
	out, code := e2eRun(t, dir, nil, "init", "minimal")
	if code != 0 {
		t.Fatalf("init exit %d: %s", code, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "Constfile")); err != nil {
		t.Fatalf("init did not create Constfile: %v", err)
	}
	if !strings.Contains(out, "created Constfile") {
		t.Errorf("init output = %q", out)
	}

	out, code = e2eRun(t, dir, nil, "doctor")
	if code != 0 {
		t.Fatalf("doctor exit %d: %s", code, out)
	}
	if !strings.Contains(out, "no problems found") {
		t.Errorf("doctor output = %q", out)
	}

	out, code = e2eRun(t, dir, nil, "--no-cache", "test")
	if code != 0 {
		t.Fatalf("test exit %d: %s", code, out)
	}
	out, code = e2eRun(t, dir, nil, "stats")
	if code != 0 {
		t.Fatalf("stats exit %d: %s", code, out)
	}
	if !strings.Contains(out, "test") || !strings.Contains(out, "last status") {
		t.Errorf("stats output = %q", out)
	}
}

func TestE2EDryRunAndList(t *testing.T) {
	dir := e2eConstfile(t, e2eAllFeatures)
	out, code := e2eRun(t, dir, nil, "--dry-run", "deploy")
	if code != 0 {
		t.Fatalf("dry-run exit %d: %s", code, out)
	}
	for _, want := range []string{"switch", "case", "default", "in sub", "lock", "state last_release"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run missing %q in %q", want, out)
		}
	}
	out, code = e2eRun(t, dir, nil, "--list")
	if code != 0 {
		t.Fatalf("list exit %d: %s", code, out)
	}
	if !strings.Contains(out, "slow") || !strings.Contains(out, "Timeout: 1s") {
		t.Errorf("list output = %q", out)
	}
}

func TestE2ECloud(t *testing.T) {
	dir := e2eConstfile(t, `|remote| {
    $ echo local-marker
}

use {
    invoke remote
}
`)
	cloudFile := filepath.Join(dir, "cloud.json")
	e2eWrite(t, dir, "cloud.json", `{
  "remote": {
    "name": "remote",
    "body": [{"type": "shell", "shell": "echo from-cloud"}]
  }
}`)
	env := []string{"CONSTRUCT_CLOUD_FILE=" + cloudFile}

	out, code := e2eRun(t, dir, env, "cloud", "list")
	if code != 0 {
		t.Fatalf("cloud list exit %d: %s", code, out)
	}
	if !strings.Contains(out, "remote") {
		t.Errorf("cloud list output = %q", out)
	}

	out, code = e2eRun(t, dir, env, "--no-cache", "use")
	if code != 0 {
		t.Fatalf("invoke exit %d: %s", code, out)
	}
	if !strings.Contains(out, "from-cloud") {
		t.Errorf("invoke cloud output = %q", out)
	}

	out, code = e2eRun(t, dir, env, "cloud", "pull", "remote")
	if code != 0 {
		t.Fatalf("cloud pull exit %d: %s", code, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "construct-cloud.json")); err != nil {
		t.Fatalf("pull did not write construct-cloud.json: %v", err)
	}
	if !strings.Contains(out, "pulled 1 cloud command(s)") {
		t.Errorf("pull output = %q", out)
	}

	pushed := filepath.Join(dir, "pushed.json")
	out, code = e2eRun(t, dir, nil, "cloud", "push", "--file", pushed)
	if code != 0 {
		t.Fatalf("cloud push exit %d: %s", code, out)
	}
	data, err := os.ReadFile(pushed)
	if err != nil {
		t.Fatalf("push output file: %v", err)
	}
	if !strings.Contains(string(data), "local-marker") {
		t.Errorf("push did not include body: %s", data)
	}
}

func TestE2ECloudSubmit(t *testing.T) {
	dir := e2eConstfile(t, `build {
    $ echo "local build"
}
`)
	os.MkdirAll(filepath.Join(dir, ".github", "workflows"), 0755)
	e2eWrite(t, dir, ".github/workflows/construct.yml", "name: construct\non: workflow_dispatch\n")

	mux := http.NewServeMux()
	mux.HandleFunc("POST /repos/o/r/actions/workflows/construct.yml/dispatches", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(204)
	})
	mux.HandleFunc("GET /repos/o/r/actions/runs", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"workflow_runs": []map[string]any{
			{"id": 42, "status": "completed", "conclusion": "success", "html_url": "https://x/42", "created_at": time.Now()},
		}})
	})
	mux.HandleFunc("GET /repos/o/r/actions/runs/{id}", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"id": 42, "status": "completed", "conclusion": "success", "html_url": "https://x/42"})
	})
	mux.HandleFunc("GET /repos/o/r/actions/runs/{id}/jobs", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"jobs": []map[string]any{
			{"id": 7, "name": "construct", "status": "completed", "conclusion": "success"},
		}})
	})
	mux.HandleFunc("GET /repos/o/r/actions/jobs/{id}/logs", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "remote: building...\nremote: token hunter2 leaked\n")
	})
	mux.HandleFunc("POST /repos/o/r/actions/runs/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(202)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	env := []string{
		"GITHUB_TOKEN=test-token",
		"CONSTRUCT_GITHUB_API=" + srv.URL,
	}

	// Submit with --wait and a secret-looking env: the log value is redacted.
	out, code := e2eRun(t, dir, env, "cloud", "submit", "--wait", "--repo", "o/r", "-e", "API_TOKEN=hunter2", "build")
	if code != 0 {
		t.Fatalf("submit exit %d: %s", code, out)
	}
	for _, want := range []string{"dispatched", "run #42", "remote: building..."} {
		if !strings.Contains(out, want) {
			t.Errorf("submit output missing %q: %q", want, out)
		}
	}
	if strings.Contains(out, "hunter2") {
		t.Errorf("secret leaked in output: %q", out)
	}
	if !strings.Contains(out, "*****") {
		t.Errorf("secret not redacted: %q", out)
	}

	// Status, logs, cancel.
	out, code = e2eRun(t, dir, env, "cloud", "status", "--json", "--repo", "o/r", "42")
	if code != 0 {
		t.Fatalf("status exit %d: %s", code, out)
	}
	if !strings.Contains(out, `"conclusion": "success"`) {
		t.Errorf("status output = %q", out)
	}
	out, code = e2eRun(t, dir, env, "cloud", "logs", "--repo", "o/r", "42")
	if code != 0 {
		t.Fatalf("logs exit %d: %s", code, out)
	}
	if !strings.Contains(out, "remote: building...") {
		t.Errorf("logs output = %q", out)
	}
	out, code = e2eRun(t, dir, env, "cloud", "cancel", "--repo", "o/r", "42")
	if code != 0 {
		t.Fatalf("cancel exit %d: %s", code, out)
	}
	if !strings.Contains(out, "cancelled run #42") {
		t.Errorf("cancel output = %q", out)
	}
}

func TestE2ECloudSubmitInitsWorkflow(t *testing.T) {
	dir := e2eConstfile(t, "build {\n    $ echo hi\n}\n")
	env := []string{"GITHUB_TOKEN=test-token", "CONSTRUCT_GITHUB_API=https://127.0.0.1:1"}
	// No workflow file: submit creates it and asks for a commit+push.
	out, code := e2eRun(t, dir, env, "cloud", "submit", "--repo", "o/r", "build")
	if code == 0 {
		t.Fatalf("expected exit 1 after creating the workflow, got %d: %s", code, out)
	}
	if !strings.Contains(out, "created .github/workflows/construct.yml") {
		t.Errorf("output = %q", out)
	}
	if _, err := os.Stat(filepath.Join(dir, ".github", "workflows", "construct.yml")); err != nil {
		t.Fatalf("workflow not created: %v", err)
	}
}

func TestE2EEnvOverridesAndArgs(t *testing.T) {
	dir := e2eConstfile(t, `
deploy (env, opt region) {
    $ echo "env=&env region=&region"
}
`)
	out, code := e2eRun(t, dir, nil, "--no-cache", "deploy", "--deploy:env=prod", "--deploy:region=us-east")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	if !strings.Contains(out, "env=prod region=us-east") {
		t.Errorf("args output = %q", out)
	}
	out, code = e2eRun(t, dir, nil, "--no-cache", "-e", "env=staging", "deploy")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	if !strings.Contains(out, "env=staging") {
		t.Errorf("override output = %q", out)
	}
}
