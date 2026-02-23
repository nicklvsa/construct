package main

import (
	"fmt"
	"os"
	"runtime"

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

func handleArgs() *ConstructInput {
	defaultFileName := "Constfile"

	platformFile := getPlatformConstfile()
	if fileExists(platformFile) && !fileExists(defaultFileName) {
		defaultFileName = platformFile
	}

	args := os.Args[1:]
	if len(args) <= 0 {
		return &ConstructInput{
			FileName: defaultFileName,
		}
	}

	info, err := os.Stat(args[0])
	if err != nil {
		return &ConstructInput{
			FileName: defaultFileName,
			Commands: args,
		}
	}

	if info.IsDir() {
		return &ConstructInput{
			FileName: defaultFileName,
			Commands: args,
		}
	}

	return &ConstructInput{
		FileName: args[0],
		Commands: args[1:],
	}
}

func listCommands(data *pkg.ParsedData) {
	fmt.Println("Available commands:")
	for _, cmd := range data.Commands {
		if cmd.Name == "_" {
			continue
		}
		if cmd.IsDefault {
			fmt.Printf("  %s (default)\n", cmd.Name)
		} else {
			fmt.Printf("  %s\n", cmd.Name)
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

func main() {
	var showHelp bool
	var showVersion bool
	var showList bool
	var debug bool
	var concurrent bool
	var dryRun bool

	flagSet := flag.NewFlagSet("construct", flag.ExitOnError)
	flagSet.BoolVarP(&showHelp, "help", "h", false, "Show help message")
	flagSet.BoolVarP(&showVersion, "version", "v", false, "Show version")
	flagSet.BoolVar(&showList, "list", false, "List commands")
	flagSet.BoolVar(&debug, "debug", false, "Debug mode")
	flagSet.BoolVar(&concurrent, "concurrent", false, "Run concurrently")
	flagSet.BoolVar(&dryRun, "dry-run", false, "Dry run")

	if err := flagSet.Parse(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing flags: %v\n", err)
		os.Exit(1)
	}

	if showHelp {
		printUsage()
		os.Exit(0)
	}

	if showVersion {
		printVersion()
		os.Exit(0)
	}

	remaining := flagSet.Args()
	defaultFileName := "Constfile"
	platformFile := getPlatformConstfile()
	if fileExists(platformFile) && !fileExists(defaultFileName) {
		defaultFileName = platformFile
	}

	var inputs *ConstructInput
	if len(remaining) > 0 {
		info, err := os.Stat(remaining[0])
		if err == nil && !info.IsDir() {
			inputs = &ConstructInput{
				FileName: remaining[0],
				Commands: remaining[1:],
			}
		} else {
			inputs = &ConstructInput{
				FileName: defaultFileName,
				Commands: remaining,
			}
		}
	} else {
		inputs = handleArgs()
	}

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

	if showList {
		listCommands(data)
		os.Exit(0)
	}

	if dryRun {
		fmt.Println("Dry run mode - commands that would be executed:")
		for _, cmd := range data.Commands {
			if len(inputs.Commands) == 0 || contains(inputs.Commands, cmd.Name) {
				fmt.Printf("  %s\n", cmd.Name)
				for _, line := range cmd.Body {
					fmt.Printf("    %s\n", line)
				}
			}
		}
		os.Exit(0)
	}

	executor := pkg.NewExecutor(data, concurrent, debug)
	if err := executor.Execute(inputs.Commands); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}
