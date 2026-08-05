package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/nicklvsa/construct/pkg"
	flag "github.com/spf13/pflag"
)

const version = "0.1.0"

type ConstructInput struct {
	FileName string
	Commands []string
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
  --concurrent      Execute commands concurrently
  --dry-run         Show commands without executing them
  --list            List all available commands
  -e, --env k=v     Override a variable (repeatable)

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

func main() {
	var showHelp bool
	var showVersion bool
	var showList bool
	var debug bool
	var concurrent bool
	var dryRun bool
	var overrides []string

	flagSet := flag.NewFlagSet("construct", flag.ExitOnError)
	flagSet.BoolVarP(&showHelp, "help", "h", false, "Show help message")
	flagSet.BoolVarP(&showVersion, "version", "v", false, "Show version")
	flagSet.BoolVar(&showList, "list", false, "List commands")
	flagSet.BoolVar(&debug, "debug", false, "Debug mode")
	flagSet.BoolVar(&concurrent, "concurrent", false, "Run concurrently")
	flagSet.BoolVar(&dryRun, "dry-run", false, "Dry run")
	flagSet.StringArrayVarP(&overrides, "env", "e", []string{}, "Override variable (key=value)")

	flagSet.ParseErrorsWhitelist.UnknownFlags = true
	if err := flagSet.Parse(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing flags: %v\n", err)
		os.Exit(1)
	}

	// First parse is lenient because command-argument flags (--cmd:arg=value)
	// aren't registered until the Constfile is parsed; it only discovers the
	// Constfile path. The second parse below re-runs with the full flag set.

	if showHelp {
		printUsage()
		os.Exit(0)
	}

	if showVersion {
		printVersion()
		os.Exit(0)
	}

	inputs := determineInputs(flagSet.Args())

	p, err := pkg.NewParser(inputs.FileName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	data, err := p.Parse()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	executor := pkg.NewExecutor(data, concurrent, debug)
	executor.SetBaseDir(filepath.Dir(inputs.FileName))
	executor.RegisterArgumentFlags(flagSet)

	flagSet.ParseErrorsWhitelist.UnknownFlags = false
	if err := flagSet.Parse(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing flags: %v\n", err)
		os.Exit(1)
	}
	inputs = determineInputs(flagSet.Args())

	for _, ov := range overrides {
		eq := strings.IndexByte(ov, '=')
		if eq < 0 {
			fmt.Fprintf(os.Stderr, "Error: invalid override %q (expected key=value)\n", ov)
			os.Exit(1)
		}
		key := strings.TrimSpace(ov[:eq])
		val := ov[eq+1:]
		overridden := false
		for _, v := range data.Variables {
			if v.Name == key {
				v.Value = val
				overridden = true
				if debug {
					fmt.Printf("[DEBUG] Override: %s = %s\n", key, val)
				}
				break
			}
		}
		if !overridden {
			data.Variables = append(data.Variables, &pkg.Variable{Name: key, Value: val, Scope: "global"})
			if debug {
				fmt.Printf("[DEBUG] Override (new): %s = %s\n", key, val)
			}
		}
	}

	if showList {
		listCommands(data)
		os.Exit(0)
	}

	if dryRun {
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
				if len(cmd.FileDeps) > 0 {
					fmt.Printf("    (deps: %s)\n", strings.Join(cmd.FileDeps, ", "))
				}
				printDryRunBody(cmd.Body, 1)
			}
		}
		os.Exit(0)
	}

	if err := executor.Execute(inputs.Commands); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		if cmdErr, ok := err.(*pkg.CommandError); ok {
			os.Exit(cmdErr.ExitCode)
		}
		os.Exit(1)
	}
}
