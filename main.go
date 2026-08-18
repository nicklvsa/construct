package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/nicklvsa/construct/pkg"
	flag "github.com/spf13/pflag"
)

const version = "0.2.0"

type ConstructInput struct {
	FileName string
	Commands []string
}

func getPlatformConstfile() string {
	return fmt.Sprintf("Constfile-%s", runtime.GOOS)
}

// defaultConstfileName prefers Constfile, else Constfile-$GOOS when only that exists.
func defaultConstfileName() string {
	if fileExists(getPlatformConstfile()) && !fileExists("Constfile") {
		return getPlatformConstfile()
	}
	return "Constfile"
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func determineInputs(remaining []string) *ConstructInput {
	defaultFileName := defaultConstfileName()

	if len(remaining) > 0 {
		info, err := os.Stat(remaining[0])
		if err == nil && !info.IsDir() {
			return &ConstructInput{
				FileName: remaining[0],
				Commands: remaining[1:],
			}
		}
		return &ConstructInput{
			FileName: defaultFileName,
			Commands: remaining,
		}
	}
	return &ConstructInput{
		FileName: defaultFileName,
	}
}

type exitCodeError struct {
	err  error
	code int
}

func (e *exitCodeError) Error() string { return e.err.Error() }
func (e *exitCodeError) Unwrap() error { return e.err }
func (e *exitCodeError) ExitCode() int { return e.code }

func exitAt(code int, format string, args ...any) error {
	return &exitCodeError{err: fmt.Errorf(format, args...), code: code}
}

func exitError(err error) {
	if msg := err.Error(); msg != "" {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
	}
	if ee, ok := err.(interface{ ExitCode() int }); ok {
		os.Exit(ee.ExitCode())
	}
	os.Exit(1)
}

var subcommandNames = []string{"init", "import", "shell", "doctor", "stats", "cloud", "clean", "lint", "graph", "completion", "fmt", "ui", "runs", "mcp", "learn", "install"}

func isSubcommandName(s string) bool {
	return slices.Contains(subcommandNames, s)
}

func commandExistsInConstfile(name string) bool {
	fileName := defaultConstfileName()
	if !fileExists(fileName) {
		return false
	}
	p, err := pkg.NewParser(fileName)
	if err != nil {
		return false
	}
	data, err := p.Parse()
	if err != nil {
		return false
	}
	_, err = data.GetCommand(name)
	return err == nil
}

func parseConstfileOptional(fileName string) (*pkg.ParsedData, error) {
	if !fileExists(fileName) {
		return nil, nil
	}
	p, err := pkg.NewParser(fileName)
	if err != nil {
		return nil, err
	}
	return p.Parse()
}

func main() {
	var o options
	flagSet := flag.NewFlagSet("construct", flag.ExitOnError)
	defineFlags(flagSet, &o)

	flagSet.ParseErrorsWhitelist.UnknownFlags = true
	if err := flagSet.Parse(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing flags: %v\n", err)
		os.Exit(1)
	}

	if o.showHelp {
		printUsage()
		os.Exit(0)
	}

	if o.showVersion {
		printVersion()
		os.Exit(0)
	}

	var positionals []string
	for _, a := range flagSet.Args() {
		if strings.Contains(a, "=") && !fileExists(a) {
			o.overrides = append(o.overrides, a)
		} else {
			positionals = append(positionals, a)
		}
	}

	if len(positionals) > 0 && positionals[0] == "__targets" {
		if err := runTargets(); err != nil {
			exitError(err)
		}
		return
	}

	// `construct dev` always means service supervision, even when a command
	// named dev exists (run that one via `construct Constfile dev`).
	if len(positionals) > 0 && positionals[0] == "dev" {
		if err := runDev(positionals[1:], &o); err != nil {
			exitError(err)
		}
		return
	}

	// --doctor is the doctor subcommand with an optional Constfile path.
	if o.doctor {
		if err := runDoctor(&o, determineInputs(positionals)); err != nil {
			exitError(err)
		}
		return
	}

	if len(positionals) > 0 && isSubcommandName(positionals[0]) && !commandExistsInConstfile(positionals[0]) {
		inputs := &ConstructInput{FileName: defaultConstfileName()}
		if len(positionals) > 1 && fileExists(positionals[1]) {
			inputs.FileName = positionals[1]
		}
		var err error
		switch positionals[0] {
		case "init":
			err = runInit(positionals[1:], &o)
		case "import":
			err = runImport(positionals[1:], &o)
		case "shell":
			err = runShellCmd(positionals[1:], &o)
		case "doctor":
			err = runDoctor(&o, inputs)
		case "stats":
			err = runStats(inputs)
		case "cloud":
			err = runCloud(positionals[1:], &o, inputs)
		case "clean":
			err = runClean(positionals[1:], &o)
		case "lint":
			err = runLint(positionals[1:], &o)
		case "graph":
			err = runGraph(positionals[1:], &o)
		case "completion":
			err = runCompletion(positionals[1:])
		case "fmt":
			err = runFmt(positionals[1:], &o)
		case "ui":
			err = runUI(positionals[1:], &o)
		case "runs":
			err = runRuns(positionals[1:], &o)
		case "mcp":
			err = runMCP(positionals[1:])
		case "learn":
			err = runLearn(positionals[1:], &o)
		case "install":
			err = runInstall(positionals[1:], &o)
		}
		if err != nil {
			exitError(err)
		}
		return
	}

	runBuildMain(&o, determineInputs(positionals))
}

func runBuildMain(o *options, inputs *ConstructInput) {
	if o.jobsStr == "auto" {
		o.jobs = runtime.NumCPU()
	} else if o.jobsStr != "" {
		n, err := strconv.Atoi(o.jobsStr)
		if err != nil || n < 0 {
			fmt.Fprintf(os.Stderr, "invalid --jobs %q (expected a number, 0, or auto)\n", o.jobsStr)
			os.Exit(1)
		}
		o.jobs = n
	}

	if o.watch && o.repeat > 0 {
		fmt.Fprintln(os.Stderr, "--repeat cannot be combined with --watch")
		os.Exit(2)
	}

	if o.since != "" && o.showList {
		fmt.Fprintln(os.Stderr, "--since cannot be combined with --list")
		os.Exit(2)
	}

	// Load environment variables: --env-file, or .env next to the Constfile.
	envPath := o.envFile
	if envPath == "" {
		candidate := filepath.Join(filepath.Dir(inputs.FileName), ".env")
		if fileExists(candidate) {
			envPath = candidate
		}
	}
	if envPath != "" {
		if err := pkg.LoadEnvFile(envPath); err != nil {
			fmt.Fprintf(os.Stderr, "Error loading %s: %v\n", envPath, err)
			os.Exit(1)
		}
	}

	o.concurrent = o.concurrent || o.jobs > 0

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var interrupted atomic.Bool
	go func() {
		for range sigCh {
			interrupted.Store(true)
			cancel()
		}
	}()

	if o.resume && !o.watch && !o.dryRun && !o.showList {
		hist := pkg.LoadRunHistory(filepath.Join(filepath.Dir(inputs.FileName), pkg.CacheDirName()))
		last := pkg.LastRecord(hist)
		var failed []string
		for name, r := range last {
			if r.Status == "failed" {
				failed = append(failed, name)
			}
		}
		sort.Strings(failed)
		if len(failed) == 0 {
			fmt.Println("nothing to resume (no failed commands in the last run)")
			return
		}
		for _, f := range failed {
			if !slices.Contains(inputs.Commands, f) {
				inputs.Commands = append(inputs.Commands, f)
			}
		}
		fmt.Printf("(--resume: rerunning %d failed command(s): %s)\n", len(failed), strings.Join(failed, ", "))
	}

	if o.watch && !o.showList && !o.dryRun {
		for {
			runStart := time.Now()
			files, err := executeBuild(inputs, o, runCtx)
			if files != nil && o.notify {
				notifySummary(inputs, err, time.Since(runStart))
			}
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				files = []string{inputs.FileName}
			}
			if !waitForChange(files, &interrupted) {
				return
			}
		}
	}

	if o.repeat > 0 {
		runStart := time.Now()
		failures := 0
		for i := 1; i <= o.repeat; i++ {
			fmt.Printf("(run %d/%d)\n", i, o.repeat)
			_, err := executeBuild(inputs, o, runCtx)
			if err != nil {
				failures++
				fmt.Fprintf(os.Stderr, "run %d/%d failed: %v\n", i, o.repeat, err)
			}
		}
		if o.notify {
			notifySummary(inputs, fmt.Errorf("%d of %d run(s) failed", failures, o.repeat), time.Since(runStart))
		}
		if failures > 0 {
			exitError(fmt.Errorf("%d of %d run(s) failed", failures, o.repeat))
		}
		if interrupted.Load() {
			fmt.Fprintln(os.Stderr, "interrupted")
			os.Exit(130)
		}
		return
	}

	if o.tui && !tuiEligible(inputs, o) {
		o.tui = false
	}

	runStart := time.Now()
	files, err := executeBuild(inputs, o, runCtx)
	if o.dash != nil {
		o.dash.stop()
	}
	if files != nil && o.notify {
		notifySummary(inputs, err, time.Since(runStart))
	}
	if err != nil {
		if interrupted.Load() {
			fmt.Fprintln(os.Stderr, "interrupted")
			os.Exit(130)
		}
		exitError(err)
	}
	if interrupted.Load() {
		fmt.Fprintln(os.Stderr, "interrupted")
		os.Exit(130)
	}
}
