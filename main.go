package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
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

func listCommands(data *pkg.ParsedData) {
	fmt.Println("Available commands:")
	for _, cmd := range data.Commands {
		if cmd.Name == "_" || strings.HasPrefix(cmd.Name, "__lazy_") {
			continue
		}
		if cmd.IsDefault {
			fmt.Printf("  %s (default)\n", cmd.Name)
		} else {
			fmt.Printf("  %s\n", cmd.Name)
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
			if len(stmt.ElseBody) > 0 {
				fmt.Printf("%s} else {\n", prefix)
				printDryRunBody(stmt.ElseBody, indent+1)
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
	fs.IntVar(&o.jobs, "jobs", 0, "Max parallel commands (0 = unlimited)")
	fs.StringVar(&o.envFile, "env-file", "", "Load environment from file")
	fs.StringArrayVarP(&o.overrides, "env", "e", []string{}, "Override variable (key=value)")
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

	if o.dryRun {
		fmt.Println("Dry run mode - commands that would be executed:")
		for _, cmd := range data.Commands {
			if strings.HasPrefix(cmd.Name, "__lazy_") {
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
