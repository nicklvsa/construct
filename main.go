package main

import (
	"bufio"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nicklvsa/construct/pkg"
	flag "github.com/spf13/pflag"
	"golang.org/x/term"
)

const version = "0.2.0"

func debugf(on bool, format string, args ...interface{}) {
	if on {
		fmt.Printf("[DEBUG] "+format, args...)
	}
}

type ConstructInput struct {
	FileName string
	Commands []string
}

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
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `construct - A Make-like build tool

Usage:
  construct [options] [Constfile] [commands...]
  construct <init|import|shell|doctor|stats|cloud|clean|lint|graph|fmt|completion> [args...]

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
  --repo OWNER/REPO GitHub repository for cloud jobs (default: git remote)
  --ref BRANCH      Git ref to dispatch cloud jobs on (default: current branch)
  --wait            Follow a cloud job and stream its logs
  --notify          Desktop notification when the run finishes

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
`)
}

func printVersion() {
	fmt.Printf("construct version %s %s/%s\n", version, runtime.GOOS, runtime.GOARCH)
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
func listCommands(data *pkg.ParsedData) {
	fmt.Println("Available commands:")
	for _, cmd := range data.Commands {
		if cmd.Name == "_" || pkg.IsLazyName(cmd.Name) {
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
		if cmd.Timeout != "" {
			fmt.Printf("    Timeout: %s\n", cmd.Timeout)
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

func listCommandsJSON(data *pkg.ParsedData) {
	type cmdInfo struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Arguments   []*pkg.Argument `json:"arguments,omitempty"`
		Prereqs     []string        `json:"prereqs,omitempty"`
		WorkDir     string          `json:"work_dir,omitempty"`
		Timeout     string          `json:"timeout,omitempty"`
		Produces    []string        `json:"produces,omitempty"`
		IsDefault   bool            `json:"is_default"`
	}
	var out []cmdInfo
	for _, cmd := range data.Commands {
		if cmd.Name == "_" || pkg.IsLazyName(cmd.Name) {
			continue
		}
		out = append(out, cmdInfo{
			Name:        cmd.Name,
			Description: cmd.Description,
			Arguments:   cmd.Arguments,
			Prereqs:     cmd.Prereqs,
			WorkDir:     cmd.WorkDir,
			Timeout:     cmd.Timeout,
			Produces:    cmd.Produces,
			IsDefault:   cmd.IsDefault,
		})
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(b))
}

func printDryRunBody(body []pkg.BodyStatement, indent int) {
	prefix := strings.Repeat("  ", indent+1)
	for _, stmt := range body {
		switch stmt.Type {
		case pkg.StmtIf:
			fmt.Printf("%sif %s {\n", prefix, stmt.Cond)
			printDryRunBody(stmt.ThenBody, indent+1)
			elseBody := stmt.ElseBody
			for len(elseBody) == 1 && elseBody[0].Type == pkg.StmtIf {
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
		case pkg.StmtFor:
			loopVar := stmt.LoopVar
			if stmt.LoopIndex != "" {
				loopVar = stmt.LoopIndex + ", " + loopVar
			}
			keyword := "for"
			if stmt.Parallel {
				keyword = "parallel"
				if stmt.ParallelJobs > 0 {
					keyword = fmt.Sprintf("parallel<%d>", stmt.ParallelJobs)
				}
			}
			fmt.Printf("%s%s %s in %s {\n", prefix, keyword, loopVar, stmt.LoopItems)
			printDryRunBody(stmt.LoopBody, indent+1)
			fmt.Printf("%s}\n", prefix)
		case pkg.StmtSwitch:
			fmt.Printf("%sswitch %s {\n", prefix, stmt.SwitchExpr)
			for _, c := range stmt.Cases {
				if c.IsDefault {
					fmt.Printf("%s  default {\n", prefix)
				} else {
					fmt.Printf("%s  case %s {\n", prefix, strings.Join(c.Values, ", "))
				}
				printDryRunBody(c.Body, indent+2)
				fmt.Printf("%s  }\n", prefix)
			}
			fmt.Printf("%s}\n", prefix)
		case pkg.StmtInDir:
			fmt.Printf("%sin %s {\n", prefix, stmt.Shell)
			printDryRunBody(stmt.ThenBody, indent+1)
			fmt.Printf("%s}\n", prefix)
		case pkg.StmtLock:
			fmt.Printf("%slock %q {\n", prefix, stmt.Shell)
			printDryRunBody(stmt.ThenBody, indent+1)
			fmt.Printf("%s}\n", prefix)
		case pkg.StmtState:
			fmt.Printf("%sstate %s = %s\n", prefix, stmt.Shell, stmt.Message)
		case pkg.StmtBuiltin:
			args := stmt.BuiltinArgs
			if stmt.Tolerant {
				args = "! " + args
			}
			if stmt.Timeout != "" {
				args = fmt.Sprintf("timeout %s %s", stmt.Timeout, args)
			}
			fmt.Printf("%s%s %s\n", prefix, stmt.Shell, args)
		case pkg.StmtConfirm:
			fmt.Printf("%sconfirm %q\n", prefix, stmt.Message)
		case pkg.StmtPrompt:
			fmt.Printf("%sprompt %q\n", prefix, stmt.Message)
		case pkg.StmtInput:
			fmt.Printf("%sinput %s %q\n", prefix, stmt.Shell, stmt.Message)
		case pkg.StmtContinue, pkg.StmtBreak:
			fmt.Printf("%s%s\n", prefix, stmt.Type)
		case pkg.StmtInvoke:
			fmt.Printf("%sinvoke %s\n", prefix, stmt.Shell)
		case pkg.StmtEnv:
			fmt.Printf("%senv { %s }\n", prefix, strings.Join(stmt.Env, ", "))
		case pkg.StmtFail:
			fmt.Printf("%sfail %q\n", prefix, stmt.Message)
		case pkg.StmtOnFail:
			fmt.Printf("%sonfail {\n", prefix)
			printDryRunBody(stmt.OnFailBody, indent+1)
			fmt.Printf("%s}\n", prefix)
		default:
			shell := stmt.Shell
			if stmt.Timeout != "" {
				shell = fmt.Sprintf("timeout %s %s", stmt.Timeout, shell)
			}
			fmt.Printf("%s%s\n", prefix, shell)
		}
	}
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
	fs.StringVar(&o.shell, "shell", "", "Shell to run statements with (default: $SHELL)")
	fs.StringArrayVarP(&o.overrides, "env", "e", []string{}, "Override variable (key=value)")
}

type chooseItem struct {
	name string
	desc string
	def  bool
}

type chooseState struct {
	all      []chooseItem
	filter   []rune
	cursor   int
	offset   int
	selected map[string]bool
}

func newChooseState(all []chooseItem) *chooseState {
	return &chooseState{all: all, selected: make(map[string]bool)}
}

func (s *chooseState) filtered() []chooseItem {
	if len(s.filter) == 0 {
		return s.all
	}
	prefix := strings.ToLower(string(s.filter))
	var out []chooseItem
	for _, it := range s.all {
		if strings.HasPrefix(strings.ToLower(it.name), prefix) {
			out = append(out, it)
		}
	}
	return out
}

func (s *chooseState) move(delta int) {
	if n := len(s.filtered()); n > 0 {
		s.cursor += delta
		if s.cursor < 0 {
			s.cursor = 0
		} else if s.cursor >= n {
			s.cursor = n - 1
		}
	}
}

func (s *chooseState) moveHome() { s.cursor = 0 }
func (s *chooseState) moveEnd() {
	if n := len(s.filtered()); n > 0 {
		s.cursor = n - 1
	}
}

func (s *chooseState) toggle() {
	vis := s.filtered()
	if len(vis) == 0 {
		return
	}
	name := vis[s.cursor].name
	if s.selected[name] {
		delete(s.selected, name)
	} else {
		s.selected[name] = true
	}
	s.move(1)
}

func (s *chooseState) selectAll() {
	for _, it := range s.filtered() {
		s.selected[it.name] = true
	}
}

func (s *chooseState) typeRune(r rune) {
	s.filter = append(s.filter, r)
	s.cursor = 0
}

func (s *chooseState) backspace() {
	if len(s.filter) > 0 {
		s.filter = s.filter[:len(s.filter)-1]
		s.cursor = 0
	}
}

func (s *chooseState) clearFilter() {
	s.filter = nil
	s.cursor = 0
}

func (s *chooseState) selectedNames() []string {
	var out []string
	for _, it := range s.all {
		if s.selected[it.name] {
			out = append(out, it.name)
		}
	}
	return out
}

type chooserKey int

const (
	keyNone chooserKey = iota
	keyUp
	keyDown
	keyHome
	keyEnd
	keySpace
	keyEnter
	keyBackspace
	keyQuit
	keyClear
	keySelectAll
	keyRune
)

func decodeChooserKey(buf []byte) (chooserKey, rune) {
	if len(buf) == 0 {
		return keyNone, 0
	}

	if len(buf) >= 3 && buf[0] == '\x1b' && buf[1] == '[' {
		switch buf[2] {
		case 'A':
			return keyUp, 0
		case 'B':
			return keyDown, 0
		case 'H':
			return keyHome, 0
		case 'F':
			return keyEnd, 0
		}
		return keyNone, 0
	}
	switch buf[0] {
	case 0x03, 0x1b: // ctrl-c, escape
		return keyQuit, 0
	case '\r', '\n':
		return keyEnter, 0
	case ' ':
		return keySpace, 0
	case 0x7f, 0x08: // backspace
		return keyBackspace, 0
	case 0x15: // ctrl-u: clear filter
		return keyClear, 0
	case 0x01: // ctrl-a: select all visible
		return keySelectAll, 0
	}
	r, _ := utf8.DecodeRune(buf)
	if unicode.IsPrint(r) {
		return keyRune, r
	}
	return keyNone, 0
}

func renderChooser(s *chooseState, width, height int) string {
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}
	vis := s.filtered()

	listH := max(height-3, 1)
	n := len(vis)
	if n == 0 {
		s.offset = 0
	} else {
		switch {
		case s.cursor < s.offset:
			s.offset = s.cursor
		case s.cursor >= s.offset+listH:
			s.offset = s.cursor - listH + 1
		}
		s.offset = min(max(s.offset, 0), max(n-listH, 0))
	}

	var b strings.Builder
	b.WriteString("\x1b[H\x1b[2J") // home + clear screen
	b.WriteString("Select targets — ↑/↓ move, space toggle, type to filter, enter run, esc quit:\r\n")

	for i := s.offset; i < n && i < s.offset+listH; i++ {
		it := vis[i]
		cur, mark := "  ", "  "
		if i == s.cursor {
			cur = "> "
		}
		if s.selected[it.name] {
			mark = "[x]"
		} else {
			mark = "[ ]"
		}
		name := truncate(it.name, max(width-10, 1))
		line := fmt.Sprintf("%s %s %s", cur, mark, name)
		if it.def {
			line += " (default)"
		}
		if it.desc != "" {
			if avail := width - len(line) - 5; avail > 4 {
				line += " — " + truncate(it.desc, avail)
			}
		}
		b.WriteString(line)
		b.WriteString("\r\n")
	}
	fmt.Fprintf(&b, "filter: %s\r\n[%d/%d selected]", string(s.filter), len(s.selected), len(s.all))
	return b.String()
}

func truncate(s string, n int) string {
	if n < 1 {
		return ""
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "..."
}

func termSize(lastW, lastH int) (int, int) {
	w, h, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || w <= 0 || h <= 0 {
		return lastW, lastH
	}
	return w, h
}

func chooseTargets(data *pkg.ParsedData) ([]string, error) {
	var items []chooseItem
	for _, cmd := range data.Commands {
		if cmd.Name == "_" || pkg.IsLazyName(cmd.Name) {
			continue
		}
		desc := ""
		if cmd.Description != "" {
			desc = strings.Split(cmd.Description, "\n")[0]
		}
		items = append(items, chooseItem{name: cmd.Name, desc: desc, def: cmd.IsDefault})
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("no commands to choose from")
	}

	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return chooseTargetsLine(items, data)
	}

	state := newChooseState(items)
	width, height := termSize(80, 24)

	old, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return nil, fmt.Errorf("failed to enter raw mode: %w", err)
	}

	fmt.Print("\x1b[?1049h")
	defer func() {
		fmt.Print("\x1b[?1049l")
		term.Restore(int(os.Stdin.Fd()), old)
	}()

	render := func() {
		width, height = termSize(width, height)
		fmt.Print(renderChooser(state, width, height))
	}

	render()
	for {
		var buf [8]byte
		n, err := os.Stdin.Read(buf[:])
		if err != nil {
			return nil, err
		}
		key, r := decodeChooserKey(buf[:n])
		switch key {
		case keyUp:
			state.move(-1)
		case keyDown:
			state.move(1)
		case keyHome:
			state.moveHome()
		case keyEnd:
			state.moveEnd()
		case keySpace:
			state.toggle()
		case keyClear:
			state.clearFilter()
		case keySelectAll:
			state.selectAll()
		case keyBackspace:
			state.backspace()
		case keyQuit:
			return nil, fmt.Errorf("selection aborted")
		case keyEnter:
			return state.selectedNames(), nil // empty selection => default command
		case keyRune:
			if r == 'q' && len(state.filter) == 0 {
				return nil, fmt.Errorf("selection aborted")
			}
			state.typeRune(r)
		}
		render()
	}
}

func chooseTargetsLine(items []chooseItem, data *pkg.ParsedData) ([]string, error) {
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Println("Select targets (numbers or names, comma/space separated; empty = default):")
		for i, it := range items {
			suffix := ""
			if it.def {
				suffix = " (default)"
			}
			if it.desc != "" {
				suffix += " — " + it.desc
			}
			fmt.Printf("  %d. %s%s\n", i+1, it.name, suffix)
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
				if idx < 1 || idx > len(items) {
					fmt.Fprintf(os.Stderr, "invalid target %q (1-%d)\n", tok, len(items))
					invalid = true
					break
				}
				selected = append(selected, items[idx-1].name)
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

func tuiEligible(inputs *ConstructInput, o *options) bool {
	if o.watch || o.repeat > 0 || o.dryRun || o.showList || o.choose || o.debug || o.explain || o.ghActions || o.quiet || o.resume {
		fmt.Fprintln(os.Stderr, "(--tui disabled: incompatible with the selected options)")
		return false
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		fmt.Fprintln(os.Stderr, "(--tui requires a terminal; running without the dashboard)")
		return false
	}
	if p, err := pkg.NewParser(inputs.FileName); err == nil {
		if data, err := p.Parse(); err == nil {
			var all []pkg.BodyStatement
			for _, cmd := range data.Commands {
				all = append(all, collectAllStatements(cmd.Body)...)
			}
			for _, stmt := range all {
				switch stmt.Type {
				case pkg.StmtConfirm, pkg.StmtPrompt, pkg.StmtInput:
					fmt.Fprintln(os.Stderr, "(--tui disabled: interactive statements need a plain terminal)")
					return false
				}
			}
		}
	}
	return true
}

func executeBuild(inputs *ConstructInput, o *options, runCtx context.Context) ([]string, error) {
	p, err := pkg.NewParser(inputs.FileName)
	if err != nil {
		return nil, err
	}
	data, err := p.Parse()
	if err != nil {
		return nil, err
	}

	// Strict second parse so per-command argument flags (--cmd:arg) are recognized.
	flagSet := flag.NewFlagSet("construct", flag.ContinueOnError)
	flagSet.SetOutput(io.Discard)
	defineFlags(flagSet, &options{})
	executor := pkg.NewExecutor(data, o.concurrent, o.debug)
	if o.tui {
		dashCtx, dashCancel := context.WithCancel(runCtx)
		defer dashCancel()
		runCtx = dashCtx
		o.dash = newDashboard(executor, dashCancel)
		executor.SetObserver(o.dash)
		executor.SetSilentStatus(true)
		go o.dash.start()
	}
	executor.SetBaseDir(filepath.Dir(inputs.FileName))
	executor.SetJobs(o.jobs)
	executor.SetTiming(o.timing)
	executor.SetNoCache(o.noCache)
	executor.SetKeepGoing(o.keepGoing)
	executor.SetQuiet(o.quiet)
	executor.SetExplain(o.explain)
	executor.SetShell(o.shell)
	executor.SetRunContext(runCtx)
	executor.SetYes(o.yes)
	executor.SetFlame(o.flame)
	executor.SetGithubActions(o.ghActions)
	executor.SetRecordRuns(true)
	executor.RegisterArgumentFlags(flagSet)

	flagSet.ParseErrorsWhitelist.UnknownFlags = false
	if err := flagSet.Parse(os.Args[1:]); err != nil {
		return nil, err
	}

	for _, ov := range o.overrides {
		before, after, ok := strings.Cut(ov, "=")
		if !ok {
			return nil, fmt.Errorf("invalid override %q (expected key=value)", ov)
		}
		key := strings.TrimSpace(before)
		val := after
		overridden := false
		for _, v := range data.Variables {
			if v.Name == key {
				overridden = true
				break
			}
		}

		data.SetVariable(key, "global", val)
		if o.debug {
			if overridden {
				debugf(o.debug, "Override: %s = %s\n", key, val)
			} else {
				debugf(o.debug, "Override (new): %s = %s\n", key, val)
			}
		}
	}

	if o.showList {
		if o.json {
			listCommandsJSON(data)
		} else {
			listCommands(data)
		}
		return nil, nil
	}

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
			if pkg.IsLazyName(cmd.Name) {
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

	execErr := executor.Execute(inputs.Commands)
	if o.flame {
		// Render even on failure — the flame graph is most useful then.
		renderFlame(executor.FlameRows())
	}
	if execErr != nil {
		return nil, execErr
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
	patterns := func(pats []string) {
		for _, pat := range pats {
			full := filepath.Join(baseDir, pat)
			matches, _ := filepath.Glob(full)
			if len(matches) == 0 {
				files = append(files, full)
			} else {
				files = append(files, matches...)
			}
		}
	}
	for _, cmd := range data.Commands {
		patterns(cmd.FileDeps)
		patterns(cmd.Produces)
		patterns(cmd.OnChange)
	}
	return files
}

func waitForChange(files []string, interrupted *atomic.Bool) bool {
	prev := fileSnapshot(files)
	for {
		if interrupted.Load() {
			return false
		}
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

// exitCodeError carries a process exit code; an empty message means the
// caller already printed its diagnostics.
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

func notifySummary(inputs *ConstructInput, err error, d time.Duration) {
	summary := fmt.Sprintf("%s in %s", targetLabelFor(inputs), trimDuration(d))
	if err != nil {
		first := strings.SplitN(err.Error(), "\n", 2)[0]
		if len(first) > 120 {
			first = first[:120] + "…"
		}
		summary = fmt.Sprintf("%s failed in %s: %s", targetLabelFor(inputs), trimDuration(d), first)
	}
	notifyResult(err == nil, summary)
}

func targetLabelFor(inputs *ConstructInput) string {
	if len(inputs.Commands) == 0 {
		return "default target"
	}
	return strings.Join(inputs.Commands, " ")
}

func trimDuration(d time.Duration) string {
	return d.Round(100 * time.Millisecond).String()
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

//go:embed init-templates/*
var initTemplates embed.FS

var subcommandNames = []string{"init", "import", "shell", "doctor", "stats", "cloud", "clean", "lint", "graph", "completion", "fmt"}

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

func runInit(args []string, o *options) error {
	template := o.template
	fileName := o.fileName
	force := o.force
	if template == "" {
		template = "minimal"
	}
	if fileName == "" {
		fileName = "Constfile"
	}
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			return exitAt(2, "unknown init option %q", a)
		}
		template = a
	}
	if _, err := os.Stat(fileName); err == nil && !force {
		return fmt.Errorf("%s already exists (use --force to overwrite)", fileName)
	}
	content, err := initTemplates.ReadFile("init-templates/" + template + ".constfile")
	if err != nil {
		return fmt.Errorf("unknown template %q (available: minimal, go, python, node, rust, monorepo)", template)
	}
	if err := os.WriteFile(fileName, content, 0644); err != nil {
		return err
	}
	fmt.Printf("created %s from the %q template\n", fileName, template)
	fmt.Println("next: construct --list")
	return nil
}

var doctorRequireRe = regexp.MustCompile(`require\(\s*"([^"]+)"`)

func runDoctor(o *options, inputs *ConstructInput) error {
	problems := 0
	fail := func(format string, args ...any) {
		problems++
		fmt.Printf("[FAIL] "+format+"\n", args...)
	}
	pass := func(format string, args ...any) {
		fmt.Printf("[ ok ] "+format+"\n", args...)
	}

	pass("construct %s (%s/%s)", version, runtime.GOOS, runtime.GOARCH)

	shell, args := pkg.DefaultShell()
	if _, err := exec.LookPath(shell); err != nil {
		fail("shell %q not found on PATH", shell)
	} else {
		pass("shell: %s %s", shell, strings.Join(args, " "))
	}

	cloudPath := os.Getenv("CONSTRUCT_CLOUD_FILE")
	if cloudPath == "" {
		candidate := filepath.Join(filepath.Dir(inputs.FileName), "construct-cloud.json")
		if fileExists(candidate) {
			cloudPath = candidate
		} else {
			cloudPath = "construct-cloud.json"
		}
	}
	if _, err := pkg.LoadCloudDefsFile(cloudPath); err != nil {
		fail("cloud file %s: %v", cloudPath, err)
	} else if fileExists(cloudPath) {
		pass("cloud file: %s", cloudPath)
	} else {
		pass("cloud file: %s (not present)", cloudPath)
	}

	envPath := o.envFile
	if envPath == "" {
		candidate := filepath.Join(filepath.Dir(inputs.FileName), ".env")
		if fileExists(candidate) {
			envPath = candidate
		}
	}
	if envPath != "" {
		if err := pkg.LoadEnvFile(envPath); err != nil {
			fail("env file %s: %v", envPath, err)
		} else {
			pass("env file: %s", envPath)
		}
	}

	data, err := parseConstfileOptional(inputs.FileName)
	if err != nil {
		fail("Constfile: %v", err)
		return errDoctorFailed
	}
	if data == nil {
		fmt.Println("no Constfile found — run `construct init` to scaffold one")
		return nil
	}
	pass("Constfile: %d command(s), %d variable(s)", len(data.Commands), len(data.Variables))

	for _, cmd := range data.Commands {
		for _, stmt := range collectAllStatements(cmd.Body) {
			for _, m := range doctorRequireRe.FindAllStringSubmatch(stmt.Cond, -1) {
				tool := m[1]
				if _, err := exec.LookPath(tool); err != nil {
					fail("command %q requires tool %q, which is not on PATH", cmd.Name, tool)
				} else {
					pass("tool %q found (required by %q)", tool, cmd.Name)
				}
			}
		}
	}

	used := make(map[string]bool)
	for _, cmd := range data.Commands {
		for _, stmt := range collectAllStatements(cmd.Body) {
			markRefs(used, stmt.Shell)
			markRefs(used, stmt.Cond)
			markRefs(used, stmt.LoopItems)
			markRefs(used, stmt.SwitchExpr)
			markRefs(used, stmt.Message)
			markRefs(used, stmt.BuiltinArgs)
			markRefs(used, stmt.Dir)
			for _, c := range stmt.Cases {
				for _, v := range c.Values {
					markRefs(used, v)
				}
			}
		}
	}
	referenced := make(map[string]bool)
	for _, cmd := range data.Commands {
		if cmd.IsDefault {
			referenced[cmd.Name] = true
		}
		for _, p := range cmd.Prereqs {
			referenced[p] = true
		}
	}
	hadWarning := false
	for _, v := range data.Variables {
		if v.Scope == "global" && !used[v.Name] {
			fmt.Printf("[warn] global variable %q is never referenced\n", v.Name)
			hadWarning = true
		}
	}
	for _, cmd := range data.Commands {
		if cmd.Name == "_" || cmd.Manual || pkg.IsLazyName(cmd.Name) {
			continue
		}
		if !referenced[cmd.Name] {
			fmt.Printf("[info] command %q is never referenced (not a prerequisite, invoke target, or default)\n", cmd.Name)
			hadWarning = true
		}
	}
	if hadWarning && problems == 0 {
		fmt.Println("(warnings above are informational)")
	}
	if problems > 0 {
		return errDoctorFailed
	}
	fmt.Println("no problems found")
	return nil
}

var errDoctorFailed = errors.New("doctor found problems (see [FAIL] entries above)")

func collectAllStatements(body []pkg.BodyStatement) []pkg.BodyStatement {
	var out []pkg.BodyStatement
	for _, stmt := range body {
		out = append(out, stmt)
		switch stmt.Type {
		case pkg.StmtIf:
			out = append(out, collectAllStatements(stmt.ThenBody)...)
			out = append(out, collectAllStatements(stmt.ElseBody)...)
		case pkg.StmtFor:
			out = append(out, collectAllStatements(stmt.LoopBody)...)
		case pkg.StmtOnFail:
			out = append(out, collectAllStatements(stmt.OnFailBody)...)
		case pkg.StmtSwitch:
			for _, c := range stmt.Cases {
				out = append(out, collectAllStatements(c.Body)...)
			}
		case pkg.StmtInDir, pkg.StmtLock:
			out = append(out, collectAllStatements(stmt.ThenBody)...)
		}
	}
	return out
}

func markRefs(used map[string]bool, s string) {
	for _, name := range pkg.VarRefNames(s) {
		used[name] = true
	}
}

func runStats(o *options, inputs *ConstructInput) error {
	dir := filepath.Join(filepath.Dir(inputs.FileName), ".construct-cache")
	hist := pkg.LoadRunHistory(dir)
	if len(hist) == 0 {
		fmt.Println("no run records yet (run a build first)")
		return nil
	}
	names := make([]string, 0, len(hist))
	for n := range hist {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		return sumMs(hist[names[i]]) > sumMs(hist[names[j]])
	})
	fmt.Printf("%-20s %5s %10s %10s %10s %s\n", "command", "runs", "avg", "last", "total", "last status")
	for _, n := range names {
		recs := hist[n]
		var total int64
		for _, r := range recs {
			total += r.DurationMs
		}
		last := recs[len(recs)-1]
		avg := total / int64(len(recs))
		fmt.Printf("%-20s %5d %10s %10s %10s %s\n", n, len(recs), durMs(avg), durMs(last.DurationMs), durMs(total), last.Status)
	}
	return nil
}

func sumMs(recs []pkg.RunRecord) int64 {
	var total int64
	for _, r := range recs {
		total += r.DurationMs
	}
	return total
}

func durMs(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return (time.Duration(ms) * time.Millisecond).Round(time.Millisecond).String()
}

func runCloud(args []string, o *options, inputs *ConstructInput) error {
	if len(args) == 0 {
		return cloudUsage()
	}
	sub := args[0]
	rest := args[1:]

	baseDir := filepath.Dir(inputs.FileName)
	exec := pkg.NewExecutor(&pkg.ParsedData{}, o.debug, false)
	exec.SetBaseDir(baseDir)
	if data, err := parseConstfileOptional(inputs.FileName); err != nil {
		return err
	} else if data != nil {
		exec.SetParsedData(data)
	}

	switch sub {
	case "list":
		entries, err := exec.CloudList()
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
		n, err := exec.CloudPull(rest, o.output)
		if err != nil {
			return err
		}
		target := o.output
		if target == "" {
			target = filepath.Join(baseDir, "construct-cloud.json")
		}
		fmt.Printf("pulled %d cloud command(s) into %s\n", n, target)
	case "push":
		n, err := exec.CloudPush(rest, o.fileName)
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

// flameBlocks are partial-fill runes for sub-cell bar precision (eighths).
const flameBlocks = " ▏▎▍▌▋▊▉█"

var flameBlockRunes = []rune(flameBlocks)

// flameHeatRamp is a cool-to-hot 256-color gradient; longer spans land hotter.
var flameHeatRamp = []int{61, 67, 68, 74, 73, 79, 78, 84, 83, 82, 118, 154, 148, 190, 184, 226, 220, 214, 208, 202, 196}

func heatColor(share float64) int {
	idx := int(share*float64(len(flameHeatRamp)-1) + 0.5)
	return flameHeatRamp[min(max(idx, 0), len(flameHeatRamp)-1)]
}

// flameBar renders one timeline bar from start/duration fractions of the run.
func flameBar(offset, share float64, width int, failed, color bool) string {
	cells := make([]rune, width)
	for i := range cells {
		cells[i] = ' '
	}
	startCell := min(int(offset*float64(width)), width-1)
	if startCell < 0 {
		startCell = 0
	}
	fill := share * float64(width)
	full := int(fill)
	for i := 0; i < full && startCell+i < width; i++ {
		cells[startCell+i] = '█'
	}
	if frac := fill - float64(full); frac >= 0.125 && startCell+full < width {
		cells[startCell+full] = flameBlockRunes[int(frac*8+0.5)]
	} else if full == 0 && fill > 0 && startCell < width {
		cells[startCell] = flameBlockRunes[1] // tiny span: keep a visible sliver
	}
	bar := string(cells)
	if !color {
		return bar
	}
	if failed {
		return "\x1b[1;91m" + bar + "\x1b[0m"
	}
	return fmt.Sprintf("\x1b[38;5;%dm%s\x1b[0m", heatColor(share), bar)
}

// flameDur formats a duration compactly: 1.24s, 412.3ms, 87µs.
func flameDur(d time.Duration) string {
	switch {
	case d >= time.Second:
		return d.Round(10 * time.Millisecond).String()
	case d >= time.Millisecond:
		return d.Round(100 * time.Microsecond).String()
	default:
		return d.Round(10 * time.Microsecond).String()
	}
}

func renderFlame(rows []pkg.FlameRow) {
	if len(rows) == 0 {
		return
	}
	// Chronological order: parents start before their children.
	sorted := slices.Clone(rows)
	slices.SortStableFunc(sorted, func(a, b pkg.FlameRow) int { return a.Start.Compare(b.Start) })

	start, end := sorted[0].Start, sorted[0].End
	for _, r := range sorted {
		if r.Start.Before(start) {
			start = r.Start
		}
		if r.End.After(end) {
			end = r.End
		}
	}
	total := end.Sub(start)
	if total <= 0 {
		total = time.Millisecond
	}

	width := 100
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 80 {
		width = w - 4
	}
	labelW := min(width/3, 34)
	// Row layout: mark(1) label(labelW) sp(1) bar(barW) sp(1) pct(6) sp(1) dur(9).
	barW := width - labelW - 19
	if barW < 20 {
		barW = 20
	}
	rowW := labelW + barW + 19
	useColor := os.Getenv("NO_COLOR") == "" && term.IsTerminal(int(os.Stdout.Fd()))

	fmt.Printf("\n flame · %d statement(s) · %s total\n", len(sorted), flameDur(total))
	sep := strings.Repeat("─", rowW)
	if useColor {
		sep = "\x1b[2m" + sep + "\x1b[0m"
	}
	fmt.Println(sep)
	for _, r := range sorted {
		dur := r.End.Sub(r.Start)
		share := float64(dur) / float64(total)

		label := strings.Repeat("  ", r.Depth) + r.Label
		if utf8.RuneCountInString(label) > labelW {
			label = string([]rune(label)[:labelW-1]) + "…"
		}
		mark := " "
		labelColor, labelReset := "", ""
		if r.Failed {
			mark = "✗"
			if useColor {
				labelColor, labelReset = "\x1b[1;91m", "\x1b[0m"
			}
		}
		// Pad by visible runes: printf would pad by bytes and the label can
		// contain multibyte characters.
		pad := strings.Repeat(" ", max(labelW-utf8.RuneCountInString(label), 0))

		bar := flameBar(float64(r.Start.Sub(start))/float64(total), share, barW, r.Failed, useColor)

		fmt.Printf("%s%s%s%s%s %s %5.1f%% %9s\n",
			labelColor, mark, labelReset, label, pad, bar, share*100, flameDur(dur))
	}
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
			err = runStats(&o, inputs)
		case "cloud":
			err = runCloud(positionals[1:], &o, inputs)
		case "clean":
			err = runClean(positionals[1:], &o)
		case "lint":
			err = runLint(positionals[1:], &o)
		case "graph":
			err = runGraph(positionals[1:], &o)
		case "completion":
			err = runCompletion(positionals[1:], &o)
		case "fmt":
			err = runFmt(positionals[1:], &o)
		}
		if err != nil {
			exitError(err)
		}
		return
	}

	inputs := determineInputs(positionals)

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
			files, err := executeBuild(inputs, &o, runCtx)
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
			_, err := executeBuild(inputs, &o, runCtx)
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

	if o.tui && !tuiEligible(inputs, &o) {
		o.tui = false
	}

	runStart := time.Now()
	files, err := executeBuild(inputs, &o, runCtx)
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
