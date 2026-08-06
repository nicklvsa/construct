package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nicklvsa/construct/pkg"
	flag "github.com/spf13/pflag"
)

const version = "0.1.0"

type ConstructInput struct {
	FileName string
	Commands []string
}

type options struct {
	showHelp    bool
	showVersion bool
	debug       bool
	concurrent  bool
	dryRun      bool
	showList    bool
	watch       bool
	choose      bool
	timing      bool
	jobs        int
	envFile     string
	overrides   []string
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `construct - A Make-like build tool

Usage:
  construct [options] [Constfile] [commands...]

Commands:
  list              List all available commands

Options:
  -h, --help        Show this help message
  -v, --version     Show version information
  --debug           Enable debug mode for verbose output
  --concurrent      Execute commands and their prerequisites concurrently
  --jobs N          Max parallel commands (implies --concurrent)
  --watch           Rerun when the Constfile or dependencies change
  --choose          Interactively select targets to run
  --timing          Print per-command elapsed time
  --dry-run         Show commands without executing them
  --list            List all available commands
  -e, --env k=v     Override a variable (repeatable)
  --env-file PATH   Load environment variables from a dotenv-style file

Examples:
  construct                  Run default command from Constfile
  construct build test       Run 'build' and 'test' commands
  construct MyFile build     Run 'build' from MyFile
  construct --list           List available commands
`)
}

func printVersion() {
	fmt.Printf("construct version %s %s/%s\n", version, runtime.GOOS, runtime.GOARCH)
}

func getPlatformConstfile() string {
	return fmt.Sprintf("Constfile-%s", runtime.GOOS)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// isLazyName reports whether a command is an internal lazy-eval helper
// (possibly prefixed by an import namespace, e.g. "lib.__lazy_x_global").
func isLazyName(name string) bool {
	return strings.HasPrefix(name, "__lazy_") || strings.Contains(name, ".__lazy_")
}

func listCommands(data *pkg.ParsedData) {
	fmt.Println("Available commands:")
	for _, cmd := range data.Commands {
		if cmd.Name == "_" || isLazyName(cmd.Name) {
			continue
		}
		if cmd.IsDefault {
			fmt.Printf("  %s (default)\n", cmd.Name)
		} else {
			fmt.Printf("  %s\n", cmd.Name)
		}
		if cmd.Description != "" {
			for _, l := range strings.Split(cmd.Description, "\n") {
				fmt.Printf("    %s\n", l)
			}
		}
		if cmd.WorkDir != "" {
			fmt.Printf("    Working dir: %s\n", cmd.WorkDir)
		}
		if len(cmd.Produces) > 0 {
			fmt.Printf("    Produces: %s\n", strings.Join(cmd.Produces, ", "))
		}
		if len(cmd.Arguments) > 0 {
			fmt.Printf("    Arguments: ")
			for i, arg := range cmd.Arguments {
				if i > 0 {
					fmt.Printf(", ")
				}
				if arg.IsOptional {
					fmt.Printf("[%s]", arg.Name)
				} else {
					fmt.Printf("%s", arg.Name)
				}
			}
			fmt.Println()
		}
		if len(cmd.Prereqs) > 0 {
			fmt.Printf("    Depends on: %s\n", cmd.Prereqs)
		}
	}
}

func printDryRunBody(body []pkg.BodyStatement, indent int) {
	prefix := strings.Repeat("  ", indent+1)
	for _, stmt := range body {
		switch stmt.Type {
		case "if":
			fmt.Printf("%sif %s {\n", prefix, stmt.Cond)
			printDryRunBody(stmt.ThenBody, indent+1)
			elseBody := stmt.ElseBody
			for len(elseBody) == 1 && elseBody[0].Type == "if" {
				inner := elseBody[0]
				fmt.Printf("%s} else if %s {\n", prefix, inner.Cond)
				printDryRunBody(inner.ThenBody, indent+1)
				elseBody = inner.ElseBody
			}
			if len(elseBody) > 0 {
				fmt.Printf("%s} else {\n", prefix)
				printDryRunBody(elseBody, indent+1)
			}
			fmt.Printf("%s}\n", prefix)
		case "for":
			loopVar := stmt.LoopVar
			if stmt.LoopIndex != "" {
				loopVar = stmt.LoopIndex + ", " + loopVar
			}
			fmt.Printf("%sfor %s in %s {\n", prefix, loopVar, stmt.LoopItems)
			printDryRunBody(stmt.LoopBody, indent+1)
			fmt.Printf("%s}\n", prefix)
		case "continue", "break":
			fmt.Printf("%s%s\n", prefix, stmt.Type)
		case "invoke":
			fmt.Printf("%sinvoke %s\n", prefix, stmt.Shell)
		case "env":
			fmt.Printf("%senv { %s }\n", prefix, strings.Join(stmt.Env, ", "))
		default:
			fmt.Printf("%s%s\n", prefix, stmt.Shell)
		}
	}
}

func determineInputs(remaining []string) *ConstructInput {
	defaultFileName := "Constfile"
	platformFile := getPlatformConstfile()
	if fileExists(platformFile) && !fileExists(defaultFileName) {
		defaultFileName = platformFile
	}

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
	fs.IntVar(&o.jobs, "jobs", 0, "Max parallel commands (0 = unlimited)")
	fs.StringVar(&o.envFile, "env-file", "", "Load environment from file")
	fs.StringArrayVarP(&o.overrides, "env", "e", []string{}, "Override variable (key=value)")
}

// chooseTargets interactively prompts for which commands to run, accepting
// comma/space-separated numbers or names. An empty answer runs the default
// command; an invalid token re-prompts.
func chooseTargets(data *pkg.ParsedData) ([]string, error) {
	var names []string
	for _, cmd := range data.Commands {
		if cmd.Name == "_" || isLazyName(cmd.Name) {
			continue
		}
		names = append(names, cmd.Name)
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no commands to choose from")
	}

	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Println("Select targets (numbers or names; empty = default):")
		for i, n := range names {
			fmt.Printf("  %d. %s\n", i+1, n)
		}
		fmt.Print("> ")
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, err // EOF / Ctrl-D aborts
		}

		var selected []string
		invalid := false
		for _, tok := range strings.FieldsFunc(strings.TrimSpace(line), func(r rune) bool {
			return r == ',' || r == ' ' || r == '\t'
		}) {
			tok = strings.TrimSpace(tok)
			if tok == "" {
				continue
			}
			if idx, err := strconv.Atoi(tok); err == nil {
				if idx < 1 || idx > len(names) {
					fmt.Fprintf(os.Stderr, "invalid target %q (1-%d)\n", tok, len(names))
					invalid = true
					break
				}
				selected = append(selected, names[idx-1])
			} else if _, err := data.GetCommand(tok); err == nil {
				selected = append(selected, tok)
			} else {
				fmt.Fprintf(os.Stderr, "unknown target %q\n", tok)
				invalid = true
				break
			}
		}
		if invalid {
			continue
		}
		return selected, nil // empty selection => default command
	}
}

func executeBuild(inputs *ConstructInput, o *options) ([]string, error) {
	p, err := pkg.NewParser(inputs.FileName)
	if err != nil {
		return nil, err
	}
	data, err := p.Parse()
	if err != nil {
		return nil, err
	}

	flagSet := flag.NewFlagSet("construct", flag.ExitOnError)
	defineFlags(flagSet, &options{})
	executor := pkg.NewExecutor(data, o.concurrent, o.debug)
	executor.SetBaseDir(filepath.Dir(inputs.FileName))
	executor.SetJobs(o.jobs)
	executor.SetTiming(o.timing)
	executor.RegisterArgumentFlags(flagSet)
	flagSet.ParseErrorsWhitelist.UnknownFlags = false
	if err := flagSet.Parse(os.Args[1:]); err != nil {
		return nil, err
	}
	inputs = determineInputs(flagSet.Args())

	for _, ov := range o.overrides {
		eq := strings.IndexByte(ov, '=')
		if eq < 0 {
			return nil, fmt.Errorf("invalid override %q (expected key=value)", ov)
		}
		key := strings.TrimSpace(ov[:eq])
		val := ov[eq+1:]
		overridden := false
		for _, v := range data.Variables {
			if v.Name == key {
				v.Value = val
				overridden = true
				if o.debug {
					fmt.Printf("[DEBUG] Override: %s = %s\n", key, val)
				}
				break
			}
		}
		if !overridden {
			data.Variables = append(data.Variables, &pkg.Variable{Name: key, Value: val, Scope: "global"})
			if o.debug {
				fmt.Printf("[DEBUG] Override (new): %s = %s\n", key, val)
			}
		}
	}

	if o.showList {
		listCommands(data)
		return nil, nil
	}

	// Interactive picking only makes sense for a single run, not per
	// iteration of --watch.
	if o.choose && !o.dryRun && !o.watch {
		chosen, err := chooseTargets(data)
		if err != nil {
			return nil, err
		}
		inputs.Commands = chosen
	}

	if o.dryRun {
		fmt.Println("Dry run mode - commands that would be executed:")
		for _, cmd := range data.Commands {
			if isLazyName(cmd.Name) {
				continue
			}
			if len(inputs.Commands) == 0 || slices.Contains(inputs.Commands, cmd.Name) {
				fmt.Printf("  %s\n", cmd.Name)
				if cmd.WorkDir != "" {
					fmt.Printf("    (in %s)\n", cmd.WorkDir)
				}
				if len(cmd.Produces) > 0 {
					fmt.Printf("    (produces: %s)\n", strings.Join(cmd.Produces, ", "))
				}
				if len(cmd.FileDeps) > 0 {
					fmt.Printf("    (deps: %s)\n", strings.Join(cmd.FileDeps, ", "))
				}
				printDryRunBody(cmd.Body, 1)
			}
		}
		return nil, nil
	}

	if err := executor.Execute(inputs.Commands); err != nil {
		return nil, err
	}
	return collectWatchFiles(inputs.FileName, data), nil
}

func collectWatchFiles(fileName string, data *pkg.ParsedData) []string {
	baseDir := filepath.Dir(fileName)
	files := []string{fileName}
	for _, sf := range data.SourceFiles {
		if sf != fileName {
			files = append(files, sf)
		}
	}
	for _, cmd := range data.Commands {
		for _, pat := range append(append([]string{}, cmd.FileDeps...), cmd.Produces...) {
			full := filepath.Join(baseDir, pat)
			matches, _ := filepath.Glob(full)
			if len(matches) == 0 {
				files = append(files, full)
			} else {
				files = append(files, matches...)
			}
		}
	}
	return files
}

func waitForChange(files []string) bool {
	prev := fileSnapshot(files)
	for {
		time.Sleep(500 * time.Millisecond)
		if !fileSnapshotEqual(prev, fileSnapshot(files)) {
			return true
		}
	}
}

func fileSnapshot(files []string) map[string]int64 {
	m := make(map[string]int64, len(files))
	for _, f := range files {
		if info, err := os.Stat(f); err == nil {
			m[f] = info.ModTime().UnixNano()
		} else {
			m[f] = -1 // missing
		}
	}
	return m
}

func fileSnapshotEqual(a, b map[string]int64) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func exitError(err error) {
	fmt.Fprintf(os.Stderr, "Error: %v\n", err)
	if cmdErr, ok := err.(*pkg.CommandError); ok {
		os.Exit(cmdErr.ExitCode)
	}
	os.Exit(1)
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

	inputs := determineInputs(flagSet.Args())

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

	if o.watch && !o.showList && !o.dryRun {
		for {
			files, err := executeBuild(inputs, &o)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				files = []string{inputs.FileName}
			}
			if !waitForChange(files) {
				return
			}
		}
	}

	if _, err := executeBuild(inputs, &o); err != nil {
		exitError(err)
	}
}
