package pkg

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseRepoFromRemote(t *testing.T) {
	cases := map[string]string{
		"https://github.com/owner/repo.git":   "owner/repo",
		"https://github.com/owner/repo":       "owner/repo",
		"git@github.com:owner/repo.git":       "owner/repo",
		"git@github.com:owner/repo":           "owner/repo",
		"ssh://git@github.com/owner/repo.git": "owner/repo",
		"http://github.com/a/b/c/repo.git":    "c/repo",
	}
	for in, want := range cases {
		got, err := ParseRepoFromRemote(in)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("%q = %q, want %q", in, got, want)
		}
	}
	for _, bad := range []string{"", "not-a-remote", "https://gitlab.com/o/r"} {
		if _, err := ParseRepoFromRemote(bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
}

func TestIsSecretName(t *testing.T) {
	for _, k := range []string{"TOKEN", "GITHUB_TOKEN", "AWS_SECRET_ACCESS_KEY", "API_KEY", "password", "CLIENT_SECRET"} {
		if !IsSecretName(k) {
			t.Errorf("%q should be secret", k)
		}
	}
	for _, k := range []string{"REGION", "CI", "BUILD_NUM"} {
		if IsSecretName(k) {
			t.Errorf("%q should not be secret", k)
		}
	}
}

func TestRedactValues(t *testing.T) {
	got := RedactValues("password=hunter2 hunter2 again", []string{"hunter2"})
	if strings.Contains(got, "hunter2") {
		t.Errorf("redaction failed: %q", got)
	}
	if !strings.Contains(got, "*****") {
		t.Errorf("no mask: %q", got)
	}
}

// mockGithub spins up a fake Actions API server for o/r.
func mockGithub(t *testing.T) (*GHClient, *httptest.Server) {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)

	var dispatched struct {
		Ref    string
		Inputs map[string]string
	}
	completedRun := func() GHRun {
		return GHRun{ID: 42, Status: "completed", Conclusion: "success",
			HTMLURL: "https://github.com/o/r/actions/runs/42", CreatedAt: time.Now()}
	}

	mux.HandleFunc("POST /repos/o/r/actions/workflows/construct.yml/dispatches", func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&dispatched)
		w.WriteHeader(204)
	})
	mux.HandleFunc("GET /repos/o/r/actions/runs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"workflow_runs": []GHRun{completedRun()}})
	})
	mux.HandleFunc("GET /repos/o/r/actions/runs/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(completedRun())
	})
	mux.HandleFunc("GET /repos/o/r/actions/runs/{id}/jobs", func(w http.ResponseWriter, r *http.Request) {
		jobs := map[string]any{"jobs": []GHJob{{ID: 7, Name: "construct", Status: "completed", Conclusion: "success", HTMLURL: "https://github.com/o/r/actions/runs/42/job/7"}}}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(jobs)
	})
	mux.HandleFunc("POST /repos/o/r/actions/runs/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(202)
	})
	mux.HandleFunc("GET /repos/o/r/actions/jobs/{id}/logs", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "group: building\nsecret value hunter2\nline two\n")
	})

	client := NewGHClient("o/r", "test-token", srv.URL)
	return client, srv
}

func TestGHClientDispatch(t *testing.T) {
	client, srv := mockGithub(t)
	defer srv.Close()
	if err := client.Dispatch("construct.yml", "main", map[string]string{"targets": "build"}); err != nil {
		t.Fatal(err)
	}
}

func TestGHClientRunAndJobs(t *testing.T) {
	client, srv := mockGithub(t)
	defer srv.Close()
	run, err := client.Run(42)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "completed" || run.Conclusion != "success" {
		t.Fatalf("run = %+v", run)
	}
	jobs, err := client.Jobs(42)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].Name != "construct" {
		t.Fatalf("jobs = %+v", jobs)
	}
}

func TestGHClientLatestDispatchRun(t *testing.T) {
	client, srv := mockGithub(t)
	defer srv.Close()
	run, err := client.LatestDispatchRun("construct.yml", time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if run.ID != 42 {
		t.Fatalf("run id = %d", run.ID)
	}
	if _, err := client.LatestDispatchRun("construct.yml", time.Now().Add(time.Minute)); err == nil {
		t.Fatal("expected no-run error for a future since")
	}
}

func TestGHClientJobLogsAndCancel(t *testing.T) {
	client, srv := mockGithub(t)
	defer srv.Close()
	logs, err := client.JobLogs(7)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logs), "line two") {
		t.Fatalf("logs = %q", logs)
	}
	if err := client.Cancel(42); err != nil {
		t.Fatal(err)
	}
}

func TestRunConclusionToExit(t *testing.T) {
	for c, want := range map[string]int{"success": 0, "skipped": 0, "neutral": 0, "failure": 1, "cancelled": 1, "timed_out": 1, "": 1} {
		if got := RunConclusionToExit(c); got != want {
			t.Errorf("%q = %d, want %d", c, got, want)
		}
	}
}
