package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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
        env:
          # Inputs never interpolate into the shell script directly (injection);
          # unquoted expansion below only word-splits, it does not re-parse.
          CONSTRUCT_ARGS: ${{ inputs.args }}
          CONSTRUCT_TARGETS: ${{ inputs.targets }}
        run: construct --github-actions $CONSTRUCT_ARGS $CONSTRUCT_TARGETS

      - name: Upload run state
        if: always()
        uses: actions/upload-artifact@v4
        with:
          name: construct-cache
          path: .construct-cache
          if-no-files-found: ignore
`

func cloudUsage() error {
	fmt.Fprintln(os.Stderr, "usage: construct cloud <list|pull|push|submit|status|logs|cancel|init-actions>")
	fmt.Fprintln(os.Stderr, "  list                   list definitions in the cloud file")
	fmt.Fprintln(os.Stderr, "  pull [names...]        write cloud definitions into construct-cloud.json")
	fmt.Fprintln(os.Stderr, "  push [names...]        upload local command bodies into the cloud file")
	fmt.Fprintln(os.Stderr, "  submit [targets...]    dispatch a GitHub Actions run (--wait to follow it)")
	fmt.Fprintln(os.Stderr, "  status <run-id>        show a run's status")
	fmt.Fprintln(os.Stderr, "  logs <run-id>          print a run's job logs")
	fmt.Fprintln(os.Stderr, "  cancel <run-id>        cancel a run")
	fmt.Fprintln(os.Stderr, "  init-actions           create .github/workflows/construct.yml")
	return exitAt(2, "")
}

func runCloud(args []string, o *options, inputs *ConstructInput) error {
	if len(args) == 0 {
		return cloudUsage()
	}
	sub := args[0]
	rest := args[1:]

	baseDir := filepath.Dir(inputs.FileName)
	switch sub {
	case "list":
		executor := pkg.NewExecutor(&pkg.ParsedData{}, o.debug, false)
		executor.SetBaseDir(baseDir)
		if data, err := parseConstfileOptional(inputs.FileName); err != nil {
			return err
		} else if data != nil {
			executor.SetParsedData(data)
		}
		entries, err := executor.CloudList()
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			fmt.Println("no cloud definitions")
			return nil
		}
		fmt.Printf("%-20s %s\n", "name", "statements")
		for _, en := range entries {
			fmt.Printf("%-20s %d\n", en.Name, en.BodyStmts)
		}
	case "pull":
		executor := pkg.NewExecutor(&pkg.ParsedData{}, o.debug, false)
		executor.SetBaseDir(baseDir)
		if data, err := parseConstfileOptional(inputs.FileName); err != nil {
			return err
		} else if data != nil {
			executor.SetParsedData(data)
		}
		n, err := executor.CloudPull(rest, o.output)
		if err != nil {
			return err
		}
		target := o.output
		if target == "" {
			target = filepath.Join(baseDir, "construct-cloud.json")
		}
		fmt.Printf("pulled %d cloud command(s) into %s\n", n, target)
	case "push":
		executor := pkg.NewExecutor(&pkg.ParsedData{}, o.debug, false)
		executor.SetBaseDir(baseDir)
		if data, err := parseConstfileOptional(inputs.FileName); err != nil {
			return err
		} else if data != nil {
			executor.SetParsedData(data)
		}
		n, err := executor.CloudPush(rest, o.fileName)
		if err != nil {
			return err
		}
		fmt.Printf("pushed %d command(s) into the cloud file\n", n)
	case "submit":
		return runCloudSubmit(rest, o)
	case "status":
		return runCloudStatus(rest, o)
	case "logs":
		return runCloudLogs(rest, o)
	case "cancel":
		return runCloudCancel(rest, o)
	case "init-actions":
		return runCloudInitActions(rest)
	default:
		return cloudUsage()
	}
	return nil
}

func gitRemoteRepo() (string, error) {
	repo, err := pkg.RepoFromGitRemote()
	if err != nil {
		return "", fmt.Errorf("%w; pass --repo owner/repo to override", err)
	}
	return repo, nil
}

func currentGitBranch() (string, error) {
	out, err := exec.Command("git", "branch", "--show-current").Output()
	if err != nil {
		return "", fmt.Errorf("could not detect the current git branch (git branch --show-current): %w; pass --ref to override", err)
	}
	if b := strings.TrimSpace(string(out)); b != "" {
		return b, nil
	}
	return "", errors.New("no current git branch (detached HEAD?); pass --ref to override")
}

func actionsClient(o *options) (*pkg.GHClient, error) {
	token := pkg.GHToken()
	if token == "" {
		return nil, errors.New("no GitHub token found; set GITHUB_TOKEN (or CONSTRUCT_GITHUB_TOKEN) or install the gh CLI")
	}
	repo := o.repo
	if repo == "" {
		var err error
		repo, err = gitRemoteRepo()
		if err != nil {
			return nil, err
		}
	}
	return pkg.NewGHClient(repo, token, os.Getenv("CONSTRUCT_GITHUB_API")), nil
}

func actionsWorkflowPath(workflow string) (string, error) {
	if workflow == "" || workflow == "." || workflow == ".." ||
		strings.ContainsAny(workflow, `/\`) {
		return "", fmt.Errorf("invalid workflow name %q (must be a plain filename inside .github/workflows)", workflow)
	}
	return filepath.Join(".github", "workflows", filepath.Base(filepath.FromSlash(workflow))), nil
}

func ensureActionsWorkflow(workflow string) (bool, error) {
	path, err := actionsWorkflowPath(workflow)
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(path); err == nil {
		return false, nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return false, err
	}
	if err := os.WriteFile(path, []byte(actionsWorkflowTemplate), 0644); err != nil {
		return false, err
	}
	return true, nil
}

func runCloudSubmit(args []string, o *options) error {
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
		var err error
		repo, err = gitRemoteRepo()
		if err != nil {
			return err
		}
	}
	if ref == "" {
		var err error
		ref, err = currentGitBranch()
		if err != nil {
			return err
		}
	}

	// Route -e overrides into workflow inputs, warning on secret-looking keys.
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

	wfPath, err := actionsWorkflowPath(workflow)
	if err != nil {
		return err
	}
	if _, err := os.Stat(wfPath); err != nil {
		if noInit {
			return fmt.Errorf("%s not found (create it with `construct cloud init-actions`)", wfPath)
		}
		created, err := ensureActionsWorkflow(workflow)
		if err != nil {
			return err
		}
		if created && !force {
			fmt.Printf("created %s\n", wfPath)
			return exitAt(1, "commit and push the workflow, then re-run `construct cloud submit` (or pass --force to dispatch now)")
		}
	}

	o.repo = repo
	client, err := actionsClient(o)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	inputs := map[string]string{
		"targets": strings.Join(targets, " "),
		"args":    argsLine,
	}

	t0 := time.Now().Add(-2 * time.Minute)
	if err := client.Dispatch(ctx, workflow, ref, inputs); err != nil {
		return err
	}

	if jsonOut {
		out, err := json.Marshal(map[string]any{
			"submitted": true,
			"repo":      repo,
			"workflow":  workflow,
			"ref":       ref,
		})
		if err != nil {
			return err
		}
		fmt.Println(string(out))
	} else {
		fmt.Printf("dispatched %s on %s (%s @ %s)\n", workflow, repo, ref, strings.Join(targets, " "))
	}

	if !wait {
		return nil
	}

	var runID int64
	for range 12 {
		r, err := client.LatestDispatchRun(ctx, workflow, t0)
		if err == nil {
			runID = r.ID
			break
		}
		if ctx.Err() != nil {
			return fmt.Errorf("interrupted while waiting for the run to appear")
		}
		time.Sleep(5 * time.Second)
	}

	if runID == 0 {
		return fmt.Errorf("the run did not appear within 60s; check Actions at https://github.com/%s/actions", repo)
	}

	fmt.Printf("run #%d: https://github.com/%s/actions/runs/%d\n", runID, repo, runID)
	return waitActionsRun(ctx, client, runID, redact, jsonOut)
}

func waitActionsRun(ctx context.Context, client *pkg.GHClient, runID int64, redact []string, jsonOut bool) error {
	seen := make(map[int64]int64)
	errStreak := 0
	for {
		run, err := client.Run(ctx, runID)
		if err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("interrupted")
			}

			errStreak++
			if errStreak >= 3 {
				return err
			}
		} else {
			errStreak = 0
		}

		jobs, err := client.Jobs(ctx, runID)
		if err == nil {
			for _, j := range jobs {
				if j.Status == "queued" || j.Status == "in_progress" || (j.Conclusion == "" && j.Status == "completed") {
					continue
				}
				logs, lerr := client.JobLogs(ctx, j.ID)
				if lerr != nil {
					continue
				}

				text := string(logs)
				if len(redact) > 0 {
					text = pkg.RedactValues(text, redact)
				}

				if int64(len(text)) < seen[j.ID] {
					seen[j.ID] = 0
				}

				if int64(len(text)) > seen[j.ID] {
					fmt.Print(text[seen[j.ID]:])
					seen[j.ID] = int64(len(text))
				}
			}
		}

		if run.Status == "completed" {
			if jsonOut {
				out, err := json.Marshal(map[string]any{
					"run_id":     runID,
					"conclusion": run.Conclusion,
				})

				if err != nil {
					return err
				}

				fmt.Println(string(out))
			}

			if code := pkg.RunConclusionToExit(run.Conclusion); code != 0 {
				return exitAt(code, "run #%d concluded: %s", runID, run.Conclusion)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("interrupted")
		case <-time.After(10 * time.Second):
		}
	}
}

func runCloudStatus(args []string, o *options) error {
	if len(args) < 1 {
		return cloudUsage()
	}

	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		return exitAt(2, "invalid run id %q", args[0])
	}

	client, err := actionsClient(o)
	if err != nil {
		return err
	}

	run, err := client.Run(context.Background(), id)
	if err != nil {
		return err
	}

	if o.json {
		b, _ := json.MarshalIndent(run, "", "  ")
		fmt.Println(string(b))
		return nil
	}

	fmt.Printf("run #%d: %s", id, run.Status)
	if run.Conclusion != "" {
		fmt.Printf(" (%s)", run.Conclusion)
	}

	fmt.Printf(" — %s\n", run.HTMLURL)
	return nil
}

func runCloudLogs(args []string, o *options) error {
	if len(args) < 1 {
		return cloudUsage()
	}

	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		return exitAt(2, "invalid run id %q", args[0])
	}
	client, err := actionsClient(o)
	if err != nil {
		return err
	}
	jobs, err := client.Jobs(context.Background(), id)
	if err != nil {
		return err
	}
	// Redact values passed via -e overrides; CI logs may echo them.
	var secrets []string
	for _, ov := range o.overrides {
		if _, val, ok := strings.Cut(ov, "="); ok && val != "" {
			secrets = append(secrets, val)
		}
	}
	printed := 0
	for _, j := range jobs {
		logs, err := client.JobLogs(context.Background(), j.ID)
		if err != nil {
			continue
		}
		text := string(logs)
		if len(secrets) > 0 {
			text = pkg.RedactValues(text, secrets)
		}
		printed++
		fmt.Printf("## %s\n%s", j.Name, text)
	}
	if printed == 0 {
		fmt.Println("no job logs available yet")
	}
	return nil
}

func runCloudCancel(args []string, o *options) error {
	if len(args) < 1 {
		return cloudUsage()
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		return exitAt(2, "invalid run id %q", args[0])
	}
	client, err := actionsClient(o)
	if err != nil {
		return err
	}
	if err := client.Cancel(context.Background(), id); err != nil {
		return err
	}
	fmt.Printf("cancelled run #%d\n", id)
	return nil
}

func runCloudInitActions(args []string) error {
	workflow := "construct.yml"
	for _, a := range args {
		switch {
		case strings.HasPrefix(a, "--workflow="):
			workflow = strings.TrimPrefix(a, "--workflow=")
		default:
			return exitAt(2, "unknown init-actions option %q", a)
		}
	}
	path, err := actionsWorkflowPath(workflow)
	if err != nil {
		return err
	}
	if _, err := ensureActionsWorkflow(workflow); err != nil {
		return err
	}
	fmt.Printf("%s is ready — commit and push it\n", path)
	return nil
}
