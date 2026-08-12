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
	"unicode"
	"unicode/utf8"

	"github.com/nicklvsa/construct/pkg"
	flag "github.com/spf13/pflag"
	"golang.org/x/term"
)

const version = "0.1.0"

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
			fmt.Printf("%sfor %s in %s {\n", prefix, loopVar, stmt.LoopItems)
			printDryRunBody(stmt.LoopBody, indent+1)
			fmt.Printf("%s}\n", prefix)
		case pkg.StmtContinue, pkg.StmtBreak:
			fmt.Printf("%s%s\n", prefix, stmt.Type)
		case pkg.StmtInvoke:
			fmt.Printf("%sinvoke %s\n", prefix, stmt.Shell)
		case pkg.StmtEnv:
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

// toggle selects or deselects the item under the cursor and moves down.
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

// selectedNames returns the chosen commands in the picker's original order.
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
		// Keep the cursor inside the scroll window, then clamp the window
		// to the list bounds (0 when the list fits on screen).
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

// truncate shortens s to at most n runes, appending an ellipsis when cut.
func truncate(s string, n int) string {
	if n < 1 {
		return ""
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
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
		if cmd.Name == "_" || isLazyName(cmd.Name) {
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
				debugf(o.debug, "Override: %s = %s\n", key, val)
				break
			}
		}
		if !overridden {
			data.Variables = append(data.Variables, &pkg.Variable{Name: key, Value: val, Scope: "global"})
			debugf(o.debug, "Override (new): %s = %s\n", key, val)
		}
	}

	if o.showList {
		listCommands(data)
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
