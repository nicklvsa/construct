package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nicklvsa/construct/pkg"
)

const actionsWorkflowTemplate = `name: construct
on:
  workflow_dispatch:
    inputs:
      targets:
        description: "Space-separated construct targets"
        required: false
        default: "_"
      args:
        description: "Extra construct CLI arguments (flags, -e overrides)"
        required: false
        default: ""

jobs:
  construct:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Restore run state
        uses: actions/download-artifact@v4
        continue-on-error: true
        with:
          name: construct-cache
          path: .construct-cache

      - name: Install construct
        run: go install github.com/nicklvsa/construct@latest

      - name: Run construct
        run: construct --github-actions ${{ inputs.args }} ${{ inputs.targets }}

      - name: Upload run state
        if: always()
        uses: actions/upload-artifact@v4
        with:
          name: construct-cache
          path: .construct-cache
          if-no-files-found: ignore
`

func cloudUsage() {
	fmt.Fprintln(os.Stderr, "usage: construct cloud <list|pull|push|submit|status|logs|cancel|init-actions>")
	fmt.Fprintln(os.Stderr, "  list                   list definitions in the cloud file")
	fmt.Fprintln(os.Stderr, "  pull [names...]        write cloud definitions into construct-cloud.json")
	fmt.Fprintln(os.Stderr, "  push [names...]        upload local command bodies into the cloud file")
	fmt.Fprintln(os.Stderr, "  submit [targets...]    dispatch a GitHub Actions run (--wait to follow it)")
	fmt.Fprintln(os.Stderr, "  status <run-id>        show a run's status")
	fmt.Fprintln(os.Stderr, "  logs <run-id>          print a run's job logs")
	fmt.Fprintln(os.Stderr, "  cancel <run-id>        cancel a run")
	fmt.Fprintln(os.Stderr, "  init-actions           create .github/workflows/construct.yml")
	os.Exit(2)
}

func gitRemoteRepo() string {
	repo, err := pkg.RepoFromGitRemote()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		fmt.Fprintln(os.Stderr, "pass --repo owner/repo to override")
		os.Exit(1)
	}
	return repo
}

func currentGitBranch() string {
	if out, err := exec.Command("git", "branch", "--show-current").Output(); err == nil {
		if b := strings.TrimSpace(string(out)); b != "" {
			return b
		}
	}
	return "main"
}

func actionsClient(o *options) (*pkg.GHClient, string) {
	token := pkg.GHToken()
	if token == "" {
		fmt.Fprintln(os.Stderr, "Error: no GitHub token found; set GITHUB_TOKEN (or CONSTRUCT_GITHUB_TOKEN) or install the gh CLI")
		os.Exit(1)
	}
	repo := o.repo
	if repo == "" {
		repo = gitRemoteRepo()
	}
	return pkg.NewGHClient(repo, token, os.Getenv("CONSTRUCT_GITHUB_API")), repo
}

func actionsWorkflowPath(workflow string) string {
	return filepath.Join(".github", "workflows", workflow)
}

func ensureActionsWorkflow(workflow string, force bool) {
	path := actionsWorkflowPath(workflow)
	if _, err := os.Stat(path); err == nil {
		return
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(path, []byte(actionsWorkflowTemplate), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if !force {
		fmt.Printf("created %s\n", path)
		fmt.Println("commit and push it, then re-run `construct cloud submit` to dispatch the first job")
		os.Exit(1)
	}
	fmt.Printf("created %s (dispatching anyway)\n", path)
}

func runCloudSubmit(args []string, o *options) {
	repo, ref, workflow := o.repo, o.ref, o.workflow
	wait, jsonOut, noInit, force := o.wait, o.json, o.noInit, o.force
	var targets, extra []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		next := func() (string, bool) {
			if i+1 < len(args) {
				i++
				return args[i], true
			}
			return "", false
		}
		switch {
		case strings.HasPrefix(a, "--repo="):
			repo = strings.TrimPrefix(a, "--repo=")
		case a == "--repo":
			if v, ok := next(); ok {
				repo = v
			}
		case strings.HasPrefix(a, "--ref="):
			ref = strings.TrimPrefix(a, "--ref=")
		case a == "--ref":
			if v, ok := next(); ok {
				ref = v
			}
		case strings.HasPrefix(a, "--workflow="):
			workflow = strings.TrimPrefix(a, "--workflow=")
		case a == "--workflow":
			if v, ok := next(); ok {
				workflow = v
			}
		case a == "--wait" || a == "-w":
			wait = true
		case a == "--json":
			jsonOut = true
		case a == "--no-init":
			noInit = true
		case a == "--force":
			force = true
		case strings.HasPrefix(a, "-") || strings.Contains(a, "="):
			extra = append(extra, a)
		default:
			targets = append(targets, a)
		}
	}
	if len(targets) == 0 {
		targets = []string{"_"}
	}
	if workflow == "" {
		workflow = "construct.yml"
	}
	if repo == "" {
		repo = gitRemoteRepo()
	}
	if ref == "" {
		ref = currentGitBranch()
	}

	// Env overrides passed via -e land in o.overrides; route them into the
	// workflow inputs and warn about secret-looking keys.
	var redact []string
	envArgs := make([]string, 0, len(o.overrides))
	for _, ov := range o.overrides {
		envArgs = append(envArgs, "-e "+ov)
		if key, val, ok := strings.Cut(ov, "="); ok && pkg.IsSecretName(key) {
			redact = append(redact, val)
			fmt.Printf("(!) %s looks like a secret; workflow inputs are visible to repo collaborators — prefer a GitHub repo secret\n", key)
		}
	}
	argsLine := strings.Join(append(extra, envArgs...), " ")

	if _, err := os.Stat(actionsWorkflowPath(workflow)); err != nil {
		if noInit {
			fmt.Fprintf(os.Stderr, "Error: %s not found (create it with `construct cloud init-actions`)\n", actionsWorkflowPath(workflow))
			os.Exit(1)
		}
		ensureActionsWorkflow(workflow, force)
	}

	client := pkg.NewGHClient(repo, pkg.GHToken(), os.Getenv("CONSTRUCT_GITHUB_API"))
	inputs := map[string]string{
		"targets": strings.Join(targets, " "),
		"args":    argsLine,
	}
	t0 := time.Now()
	if err := client.Dispatch(workflow, ref, inputs); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if jsonOut {
		fmt.Println(`{"submitted": true, "repo": "` + repo + `", "workflow": "` + workflow + `", "ref": "` + ref + `"}`)
	} else {
		fmt.Printf("dispatched %s on %s (%s @ %s)\n", workflow, repo, ref, strings.Join(targets, " "))
	}
	if !wait {
		return
	}

	var runID int64
	for attempt := 0; attempt < 12; attempt++ {
		r, err := client.LatestDispatchRun(workflow, t0)
		if err == nil {
			runID = r.ID
			break
		}
		time.Sleep(5 * time.Second)
	}
	if runID == 0 {
		fmt.Fprintln(os.Stderr, "Error: the run did not appear within 60s; check Actions at https://github.com/"+repo+"/actions")
		os.Exit(1)
	}
	fmt.Printf("run #%d: https://github.com/%s/actions/runs/%d\n", runID, repo, runID)
	os.Exit(waitActionsRun(client, runID, redact, jsonOut))
}

// waitActionsRun polls the run, streaming new job logs, until it completes.
func waitActionsRun(client *pkg.GHClient, runID int64, redact []string, jsonOut bool) int {
	seen := make(map[int64]int64)
	for {
		run, err := client.Run(runID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return 1
		}
		jobs, err := client.Jobs(runID)
		if err == nil {
			for _, j := range jobs {
				if j.Status == "queued" || j.Status == "in_progress" || (j.Conclusion == "" && j.Status == "completed") {
					continue
				}
				logs, lerr := client.JobLogs(j.ID)
				if lerr != nil {
					continue
				}
				text := string(logs)
				if len(redact) > 0 {
					text = pkg.RedactValues(text, redact)
				}
				if int64(len(text)) > seen[j.ID] {
					fmt.Print(text[seen[j.ID]:])
					seen[j.ID] = int64(len(text))
				}
			}
		}
		if run.Status == "completed" {
			if jsonOut {
				fmt.Printf(`{"run_id": %d, "conclusion": %q}`+"\n", runID, run.Conclusion)
			}
			return pkg.RunConclusionToExit(run.Conclusion)
		}
		time.Sleep(10 * time.Second)
	}
}

func runCloudStatus(args []string, o *options) {
	if len(args) < 1 {
		cloudUsage()
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid run id %q\n", args[0])
		os.Exit(2)
	}
	client, repo := actionsClient(o)
	run, err := client.Run(id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if o.json {
		b, _ := json.MarshalIndent(run, "", "  ")
		fmt.Println(string(b))
		return
	}
	fmt.Printf("run #%d: %s", id, run.Status)
	if run.Conclusion != "" {
		fmt.Printf(" (%s)", run.Conclusion)
	}
	fmt.Printf(" — %s\n", run.HTMLURL)
	_ = repo
}

func runCloudLogs(args []string, o *options) {
	if len(args) < 1 {
		cloudUsage()
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid run id %q\n", args[0])
		os.Exit(2)
	}
	client, _ := actionsClient(o)
	jobs, err := client.Jobs(id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	seen := 0
	for _, j := range jobs {
		logs, err := client.JobLogs(j.ID)
		if err != nil {
			continue
		}
		seen++
		fmt.Printf("## %s\n%s", j.Name, logs)
	}
	if seen == 0 {
		fmt.Println("no job logs available yet")
	}
}

func runCloudCancel(args []string, o *options) {
	if len(args) < 1 {
		cloudUsage()
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid run id %q\n", args[0])
		os.Exit(2)
	}
	client, _ := actionsClient(o)
	if err := client.Cancel(id); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("cancelled run #%d\n", id)
}

func runCloudInitActions(args []string) {
	workflow := "construct.yml"
	for _, a := range args {
		switch {
		case strings.HasPrefix(a, "--workflow="):
			workflow = strings.TrimPrefix(a, "--workflow=")
		default:
			fmt.Fprintf(os.Stderr, "unknown init-actions option %q\n", a)
			os.Exit(2)
		}
	}
	ensureActionsWorkflow(workflow, true)
	fmt.Printf("%s is ready — commit and push it\n", actionsWorkflowPath(workflow))
}
