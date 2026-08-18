package main

import (
	"fmt"
	"os"
	"runtime"
	"sort"

	flag "github.com/spf13/pflag"
)

type options struct {
	showHelp          bool
	showVersion       bool
	debug             bool
	concurrent        bool
	dryRun            bool
	showList          bool
	watch             bool
	choose            bool
	timing            bool
	keepGoing         bool
	noCache           bool
	quiet             bool
	explain           bool
	json              bool
	resume            bool
	repeat            int
	flame             bool
	ghActions         bool
	yes               bool
	doctor            bool
	force             bool
	template          string
	fileName          string
	output            string
	wait              bool
	repo              string
	ref               string
	workflow          string
	noInit            bool
	notify            bool
	strict            bool
	cleanCache        bool
	dotGraph          bool
	checkFormat       bool
	jobsStr           string
	jobs              int
	envFile           string
	shell             string
	overrides         []string
	containerOverride string
	tui               bool
	dash              *dashboard
	uiPort            int
	uiNoOpen          bool
	since             string
	hooks             []string
	uninstall         bool
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `construct - A Make-like build tool

Usage:
  construct [options] [Constfile] [commands...]
  construct <init|import|shell|doctor|stats|cloud|clean|lint|graph|fmt|completion|ui|runs|mcp|learn|install> [args...]

Commands:
  list              List all available commands
  init [template]   Scaffold a Constfile (minimal, go, python, node, rust, monorepo)
  import [FILE] [OUT]  Convert a Makefile to a Constfile (best-effort, --force)
  shell [FILE] [cmd]  Start a shell with a command's env, workdir, or container
  doctor            Diagnose the environment, Constfile, tools, and cloud file
  stats             Show per-command timing history
  clean [targets]   Remove files declared in produces (--cache drops .construct-cache)
  lint [file]       Static checks shared with the editor (--strict, --json)
  graph [targets]   Print the dependency tree (--dot, --json)
  fmt [files]       Canonicalize Constfile indentation (--check for CI)
  completion SHELL  Emit bash/zsh/fish completions
  ui [Constfile]    Edit the Constfile in the browser (drag and drop; --port, --no-open)
  runs [FILE]       Show run history: list, show <cmd> [n], diff <cmd> [a b]
  mcp [FILE]        Serve build tools to MCP clients over stdio (for AI agents)
  learn [FILE] [targets]  Discover file deps: trace reads (strace) or unwatched files
  install           Install shell completions (--hook NAME for git hooks, --uninstall)
  cloud             Manage cloud commands and GitHub Actions jobs (see below)

Options:
  -h, --help        Show this help message
  -v, --version     Show version information
  --debug           Enable debug mode for verbose output
  --concurrent      Execute commands and their prerequisites concurrently
  --jobs N          Max parallel commands (0 = unlimited, auto = CPU count)
  -k, --keep-going  Continue other targets when one fails
  --no-cache        Ignore the file-dep cache and run everything
  --quiet, -q       Suppress command output, keep errors
  --explain         Print why commands run or are skipped
  --json            Machine-readable output (with --list, --status)
  --shell PATH      Shell to run statements with
  --watch           Rerun when the Constfile or dependencies change
  --choose          Interactively select targets to run
  --timing          Print per-command elapsed time
  --dry-run         Show commands without executing them
  --list            List all available commands
  -e, --env k=v     Override a variable (repeatable)
  --env-file PATH   Load environment variables from a dotenv-style file
  --resume          Rerun commands that failed in the last run (alias: --only-failed)
  --repeat N        Run the whole build N times (flaky detection)
  --flame           Print a per-statement flame graph after the run
  --github-actions  GitHub Actions native output (auto-enabled in CI)
  --yes             Auto-approve confirm statements
  --tui             Live dashboard for the run (q detaches, Ctrl-C cancels)
  --container IMG   shell: run in this container image instead of the command's
  --force, -f       Overwrite existing files (init, import)
  --doctor          Diagnose the environment, Constfile, tools, and cloud file
  --template NAME   init: template to scaffold (minimal, go, python, node, rust, monorepo)
  --file PATH       Target file (init, cloud push)
  --output PATH     Output file (cloud pull)
  --repo OWNER/REPO GitHub repository for cloud jobs (default: git remote)
  --ref BRANCH      Git ref to dispatch cloud jobs on (default: current branch)
  --workflow NAME   Workflow file name (cloud submit, default: construct.yml)
  --no-init         cloud submit: don't create the workflow file
  --wait            Follow a cloud job and stream its logs
  --notify          Desktop notification when the run finishes
  --since REF       Only run targets affected by changes since a git ref
  --port N          ui: serve on this port (default: random free port)
  --no-open         ui: print the URL without opening a browser

Cloud subcommands:
  cloud list|pull|push                    cloud command definitions
  cloud submit [targets...]               dispatch a GitHub Actions run
  cloud status|logs|cancel <run-id>       inspect a dispatched run
  cloud init-actions                      create .github/workflows/construct.yml

Examples:
  construct                  Run default command from Constfile
  construct build test       Run 'build' and 'test' commands
  construct MyFile build     Run 'build' from MyFile
  construct --list           List available commands
  construct --flame build    Run 'build' and show a timing flame graph
  construct cloud submit --wait test     Run 'test' on GitHub Actions
  construct import Makefile  Convert a Makefile to ./Constfile
  construct shell dev        Drop into the 'dev' command's environment
  construct --since origin/main build  Run 'build' only if affected since origin/main
  construct install          Install shell completions
  construct install --hook pre-push -- build test  Install a git hook
`)
}

func printVersion() {
	fmt.Printf("construct version %s %s/%s\n", version, runtime.GOOS, runtime.GOARCH)
}

func defineFlags(fs *flag.FlagSet, o *options) {
	fs.BoolVarP(&o.showHelp, "help", "h", false, "Show help message")
	fs.BoolVarP(&o.showVersion, "version", "v", false, "Show version")
	fs.BoolVar(&o.showList, "list", false, "List commands")
	fs.BoolVar(&o.debug, "debug", false, "Debug mode")
	fs.BoolVar(&o.concurrent, "concurrent", false, "Run concurrently")
	fs.BoolVar(&o.dryRun, "dry-run", false, "Dry run")
	fs.BoolVar(&o.watch, "watch", false, "Rerun when files change")
	fs.BoolVar(&o.choose, "choose", false, "Interactively select targets")
	fs.BoolVar(&o.timing, "timing", false, "Print per-command elapsed time")
	fs.BoolVarP(&o.keepGoing, "keep-going", "k", false, "Continue other targets when one fails")
	fs.BoolVar(&o.noCache, "no-cache", false, "Ignore the file-dep cache and run everything")
	fs.BoolVarP(&o.quiet, "quiet", "q", false, "Suppress command output, keep errors")
	fs.BoolVar(&o.explain, "explain", false, "Print why commands run or are skipped")
	fs.BoolVar(&o.json, "json", false, "Machine-readable output (with --list)")
	fs.BoolVar(&o.resume, "resume", false, "Rerun commands that failed in the last run")
	fs.BoolVar(&o.resume, "only-failed", false, "Alias for --resume")
	fs.IntVar(&o.repeat, "repeat", 0, "Run the build N times (flaky detection)")
	fs.BoolVar(&o.flame, "flame", false, "Print a per-statement flame graph after the run")
	fs.BoolVar(&o.ghActions, "github-actions", os.Getenv("GITHUB_ACTIONS") == "true", "GitHub Actions native output (groups, ::error::)")
	fs.BoolVar(&o.yes, "yes", false, "Auto-approve confirmations")
	fs.BoolVar(&o.doctor, "doctor", false, "Diagnose the environment, Constfile, tools, and cloud file")
	fs.BoolVarP(&o.force, "force", "f", false, "Overwrite existing files (init)")
	fs.StringVar(&o.template, "template", "", "Init template (minimal, go, python, node, rust, monorepo)")
	fs.StringVar(&o.fileName, "file", "", "Target file (init, cloud push)")
	fs.StringVar(&o.output, "output", "", "Output file (cloud pull)")
	fs.BoolVar(&o.wait, "wait", false, "Wait for a cloud job and stream its logs")
	fs.StringVar(&o.repo, "repo", "", "GitHub repository owner/repo (cloud submit)")
	fs.StringVar(&o.ref, "ref", "", "Git ref to dispatch on (cloud submit)")
	fs.StringVar(&o.workflow, "workflow", "", "Workflow file name (cloud submit)")
	fs.BoolVar(&o.noInit, "no-init", false, "Do not create the workflow file (cloud submit)")
	fs.BoolVar(&o.notify, "notify", false, "Send a desktop notification when the run finishes")
	fs.BoolVar(&o.strict, "strict", false, "lint: fail on warnings too")
	fs.BoolVar(&o.cleanCache, "cache", false, "clean: also remove the .construct-cache directory")
	fs.BoolVar(&o.dotGraph, "dot", false, "graph: emit Graphviz DOT instead of a tree")
	fs.BoolVar(&o.checkFormat, "check", false, "fmt: exit 1 when files are not formatted")
	fs.StringVar(&o.jobsStr, "jobs", "", "Max parallel commands (0 = unlimited, auto = CPU count)")
	fs.StringVar(&o.envFile, "env-file", "", "Load environment from file")
	fs.StringVar(&o.containerOverride, "container", "", "`shell`: run in this container image instead of the command's")
	fs.BoolVar(&o.tui, "tui", false, "Live dashboard for the run (requires a terminal)")
	fs.IntVar(&o.uiPort, "port", 0, "`ui`: port to serve on (default: random)")
	fs.BoolVar(&o.uiNoOpen, "no-open", false, "`ui`: print the URL instead of opening a browser")
	fs.StringVar(&o.shell, "shell", "", "Shell to run statements with (default: $SHELL; `install`: shell to install completions for)")
	fs.StringArrayVarP(&o.overrides, "env", "e", []string{}, "Override variable (key=value)")
	fs.StringVar(&o.since, "since", "", "Only run targets affected by changes since a git ref (e.g. origin/main)")
	fs.StringArrayVar(&o.hooks, "hook", []string{}, "install: git hook(s) to install (pre-commit, pre-push, ...); targets follow `--`")
	fs.BoolVar(&o.uninstall, "uninstall", false, "install: remove installed completions or hooks")
}

func flagList() [][2]string {
	fs := flag.NewFlagSet("construct", flag.ContinueOnError)
	defineFlags(fs, &options{})
	var out [][2]string
	fs.VisitAll(func(f *flag.Flag) {
		name := "--" + f.Name
		if f.Shorthand != "" {
			name += "/-" + f.Shorthand
		}
		out = append(out, [2]string{name, f.Usage})
	})
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out
}
