package pkg

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/spf13/pflag"
)

// CommandError wraps a command execution failure with its exit code.
type CommandError struct {
	Cmd      string
	ExitCode int
	Stderr   string
}

func (e *CommandError) Error() string {
	if e.Stderr != "" {
		return fmt.Sprintf("command '%s' failed (exit %d): %s", e.Cmd, e.ExitCode, e.Stderr)
	}
	return fmt.Sprintf("command '%s' failed (exit %d)", e.Cmd, e.ExitCode)
}

type Executor struct {
	StructuredParse *ParsedData
	concurrent      bool
	debug           bool
	cloudFile       string
	cloudDefs       map[string]Command
	cloudLoaded     bool
	shellName       string
	shellArgs       []string
	mu              sync.Mutex
}

func defaultShell() (string, []string) {
	if runtime.GOOS == "windows" {
		gitBashPaths := []string{
			os.Getenv("ProgramFiles") + `\Git\usr\bin\bash.exe`,
			os.Getenv("ProgramFiles(x86)") + `\Git\usr\bin\bash.exe`,
			os.Getenv("LOCALAPPDATA") + `\Programs\Git\usr\bin\bash.exe`,
		}
		for _, p := range gitBashPaths {
			if p != `\Git\usr\bin\bash.exe` {
				if _, err := os.Stat(p); err == nil {
					return p, []string{"-c"}
				}
			}
		}
		// Fall back to cmd.exe if Git Bash isn't installed.
		return "cmd", []string{"/c"}
	}
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh, []string{"-c"}
	}
	return "/bin/sh", []string{"-c"}
}

func (e *Executor) childEnv() []string {
	env := os.Environ()
	if runtime.GOOS != "windows" {
		return env
	}
	// Only adjust if we're using Git Bash.
	bashDir := filepath.Dir(e.shellName)           // ...\Git\usr\bin
	gitRoot := filepath.Dir(filepath.Dir(bashDir)) // ...\Git
	usrBin := filepath.Join(gitRoot, "usr", "bin")
	if _, err := os.Stat(usrBin); err != nil {
		return env
	}

	for i, kv := range env {
		if strings.HasPrefix(strings.ToUpper(kv), "PATH=") {
			env[i] = "PATH=" + usrBin + string(os.PathListSeparator) + kv[5:]
			break
		}
	}
	return env
}

func NewExecutor(data *ParsedData, concurrent bool, debug bool) *Executor {
	cloudFile := os.Getenv("CONSTRUCT_CLOUD_FILE")
	if cloudFile == "" {
		cloudFile = "fakecloud.json"
	}

	shellName, shellArgs := defaultShell()
	if sh := os.Getenv("CONSTRUCT_SHELL"); sh != "" {
		if runtime.GOOS == "windows" {
			shellName, shellArgs = sh, []string{"/c"}
		} else {
			shellName, shellArgs = sh, []string{"-c"}
		}
	}

	return &Executor{
		concurrent:      concurrent,
		debug:           debug,
		cloudFile:       cloudFile,
		shellName:       shellName,
		shellArgs:       shellArgs,
		StructuredParse: data,
	}
}

func (e *Executor) loadCloudDefs() error {
	if e.cloudLoaded {
		return nil
	}

	fileBytes, err := os.ReadFile(e.cloudFile)
	if err != nil {
		e.cloudDefs = make(map[string]Command)
		e.cloudLoaded = true
		return nil
	}

	var defs map[string]Command
	if err := json.Unmarshal(fileBytes, &defs); err != nil {
		e.cloudDefs = make(map[string]Command)
		e.cloudLoaded = true
		return fmt.Errorf("failed to parse cloud file %q: %w", e.cloudFile, err)
	}

	e.cloudDefs = defs
	e.cloudLoaded = true
	return nil
}

func (e *Executor) RegisterArgumentFlags(flagSet *pflag.FlagSet) {
	for _, cmd := range e.StructuredParse.Commands {
		for _, arg := range cmd.Arguments {
			flagName := fmt.Sprintf("%s:%s", cmd.Name, arg.Name)
			flagSet.String(flagName, "", fmt.Sprintf("Argument %s for command %s", arg.Name, cmd.Name))
		}
	}
}

func (e *Executor) SetDebug(debug bool) {
	e.debug = debug
}

func (e *Executor) EvaluateCommand(command *Command) error {
	resolveValue := func(s, scope string) string {
		s = resolveVarRefs(s, func(name string) (string, bool) {
			v, err := e.StructuredParse.GetVariable(name, scope)
			if err != nil || v == nil {
				return "", false
			}
			return v.Value, true
		})
		return resolveEnvRefs(s)
	}

	if len(command.FileDeps) > 0 && !command.IsPrereq {
		if e.shouldSkip(command, resolveValue) {
			fmt.Printf("(%s cached)\n", command.Name)
			return nil
		}
	}

	var execCommandBody func(target *Command, body []BodyStatement) error
	execCommandBody = func(target *Command, body []BodyStatement) error {
		isPrereq := target.IsPrereq
		for _, stmt := range body {
			if stmt.Type == "if" {
				cond := resolveValue(stmt.Cond, target.Name)
				if e.debug {
					fmt.Printf("[DEBUG] Evaluating condition: %s\n", cond)
				}
				if evaluateCondition(cond) {
					if err := execCommandBody(target, stmt.ThenBody); err != nil {
						return err
					}
				} else if stmt.ElseBody != nil {
					if err := execCommandBody(target, stmt.ElseBody); err != nil {
						return err
					}
				}
				continue
			}

			if stmt.Type == "for" {
				items := resolveValue(stmt.LoopItems, target.Name)
				var expanded []string
				if strings.ContainsAny(items, "*?") {
					wd := "."
					if target.WorkDir != "" {
						wd = resolveValue(target.WorkDir, target.Name)
					}
					for _, pattern := range strings.Split(items, ",") {
						pattern = strings.TrimSpace(pattern)
						matches, _ := filepath.Glob(filepath.Join(wd, pattern))
						if len(matches) == 0 {
							matches = []string{pattern}
						}
						for _, m := range matches {
							expanded = append(expanded, filepath.Base(m))
						}
					}
				} else {
					for _, item := range strings.Split(items, ",") {
						expanded = append(expanded, strings.TrimSpace(item))
					}
				}

				for _, item := range expanded {
					e.mu.Lock()
					e.StructuredParse.AddVariable(&Variable{
						Name:  stmt.LoopVar,
						Value: item,
						Scope: target.Name,
					})
					e.mu.Unlock()
					if e.debug {
						fmt.Printf("[DEBUG] For loop %s = %s\n", stmt.LoopVar, item)
					}
					argFlags := make(map[string]bool)
					for _, arg := range target.Arguments {
						argFlags[target.Name+":"+arg.Name] = true
					}
					cleaned := e.cleanStatements(stmt.LoopBody, target, argFlags)
					if err := execCommandBody(target, cleaned); err != nil {
						return err
					}
				}
				continue
			}

			cmdLine := stmt.Shell
			if cmdLine == "" {
				continue
			}

			args := append(e.shellArgs, cmdLine)
			cmd := exec.Command(e.shellName, args...)
			if target.WorkDir != "" {
				cmd.Dir = resolveValue(target.WorkDir, target.Name)
			}
			cmd.Env = e.childEnv()

			var fullCommand string
			if e.debug {
				fullCommand = e.shellName + " " + strings.Join(args, " ")
			}

			if e.debug {
				switch {
				case isPrereq:
					fmt.Printf("[DEBUG] Running prerequisite %s: %s\n", target.Name, fullCommand)
				case target.LazyEval != nil:
					fmt.Printf("[DEBUG] Running lazy command for variable %s: %s\n", target.LazyEval.VarName, fullCommand)
				default:
					fmt.Printf("[DEBUG] Running command %s: %s\n", target.Name, fullCommand)
				}
			}

			var stdout, stderr []byte
			var err error
			if e.debug {
				stdout, stderr, err = capture(cmd)
			} else {
				stdout, err = cmd.Output()
			}

			output := stdout
			if len(stderr) > 0 {
				if len(output) > 0 {
					output = append(output, '\n')
				}
				output = append(output, stderr...)
			}

			if err != nil {
				exitCode := 1
				if ee, ok := err.(*exec.ExitError); ok {
					exitCode = ee.ExitCode()
				}

				if e.debug {
					fmt.Printf("[DEBUG] Command failed: %v\n", err)
					if len(stderr) > 0 {
						fmt.Printf("[DEBUG] Error output: %s\n", string(stderr))
					}
				}

				if fullCommand == "" {
					fullCommand = e.shellName + " " + strings.Join(args, " ")
				}

				return &CommandError{
					Cmd:      fullCommand,
					ExitCode: exitCode,
					Stderr:   string(stderr),
				}
			}

			strOutput := strings.TrimSpace(string(output))

			switch {
			case isPrereq:
				e.mu.Lock()
				target.PrereqOutput = append(target.PrereqOutput, strOutput)
				if stmt.OutputName != "" {
					if target.NamedOutput == nil {
						target.NamedOutput = make(map[string]string)
					}
					target.NamedOutput[stmt.OutputName] = strOutput
				}
				e.mu.Unlock()
				if e.debug {
					fmt.Printf("[DEBUG] Prereq output: %s\n", strOutput)
					if stmt.OutputName != "" {
						fmt.Printf("[DEBUG] Named output %s.%s = %s\n", target.Name, stmt.OutputName, strOutput)
					}
				}
			case target.LazyEval != nil:
				e.mu.Lock()
				variable, err := e.StructuredParse.GetVariable(target.LazyEval.VarName, target.LazyEval.Scope)
				if err != nil {
					e.mu.Unlock()
					return err
				}
				variable.Value = strOutput
				e.mu.Unlock()
				if e.debug {
					fmt.Printf("[DEBUG] Set variable %s.%s = %s\n", target.LazyEval.Scope, target.LazyEval.VarName, strOutput)
				}
			default:
				fmt.Println(strOutput)
			}
		}

		return nil
	}

	cleanCommandBody := func(cmd *Command) ([]BodyStatement, error) {
		if len(cmd.Prereqs) > 0 {
			for _, prereq := range cmd.PrereqCmds {
				for idx, arg := range prereq.PrereqOutput {
					varName := prereq.Name + "." + fmt.Sprintf("%d", idx)
					e.mu.Lock()
					e.StructuredParse.AddVariable(&Variable{
						Name:  strings.TrimSpace(varName),
						Value: strings.TrimSpace(arg),
						Scope: cmd.Name,
					})
					e.mu.Unlock()
				}
				// Register named outputs (&prereq.name)
				for name, val := range prereq.NamedOutput {
					varName := prereq.Name + "." + name
					e.mu.Lock()
					e.StructuredParse.AddVariable(&Variable{
						Name:  varName,
						Value: strings.TrimSpace(val),
						Scope: cmd.Name,
					})
					e.mu.Unlock()
				}
			}
		}

		argFlags := make(map[string]bool, len(cmd.Arguments))
		for _, arg := range cmd.Arguments {
			argFlags[cmd.Name+":"+arg.Name] = true
		}

		var cleanStmts func(stmts []BodyStatement) []BodyStatement
		cleanStmts = func(stmts []BodyStatement) []BodyStatement {
			out := make([]BodyStatement, len(stmts))
			for i, stmt := range stmts {
				if stmt.Type == "if" {
					out[i] = BodyStatement{
						Type:     "if",
						Cond:     stmt.Cond,
						ThenBody: cleanStmts(stmt.ThenBody),
						ElseBody: cleanStmts(stmt.ElseBody),
					}
					continue
				}
				if stmt.Type == "for" {
					out[i] = BodyStatement{
						Type:      "for",
						LoopVar:   stmt.LoopVar,
						LoopItems: e.cleanShellLine(cmd, stmt.LoopItems, argFlags),
						LoopBody:  stmt.LoopBody,
					}
					continue
				}
				out[i] = BodyStatement{Type: "shell", Shell: e.cleanShellLine(cmd, stmt.Shell, argFlags), OutputName: stmt.OutputName}
			}
			return out
		}

		return cleanStmts(cmd.Body), nil
	}

	for _, prereqName := range command.Prereqs {
		if command.PrereqCmds == nil {
			command.PrereqCmds = []*Command{}
		}

		prereqName = strings.TrimSpace(prereqName)
		if prereqName == "" {
			continue
		}

		preCmd, err := e.StructuredParse.GetCommand(prereqName)
		if err != nil {
			return err
		}

		e.mu.Lock()
		preCmd.IsPrereq = true
		preCmd.PrereqOutput = []string{}
		e.mu.Unlock()

		if err := e.tryApplyCloudBody(preCmd); err != nil {
			return err
		}

		// If this prereq has its own prereqs, recurse into EvaluateCommand so
		// nested prereq outputs are registered before cleaning the body.
		if len(preCmd.Prereqs) > 0 {
			if err := e.EvaluateCommand(preCmd); err != nil {
				return err
			}
		} else {
			preBody, err := cleanCommandBody(preCmd)
			if err != nil {
				return err
			}
			if err := execCommandBody(preCmd, preBody); err != nil {
				return err
			}
		}

		command.PrereqCmds = append(command.PrereqCmds, preCmd)
	}

	if err := e.tryApplyCloudBody(command); err != nil {
		return err
	}

	body, err := cleanCommandBody(command)
	if err != nil {
		return err
	}

	if err := execCommandBody(command, body); err != nil {
		return err
	}

	if len(command.FileDeps) > 0 && !command.IsPrereq {
		e.updateCache(command, resolveValue)
	}

	return nil
}

func (e *Executor) cleanStatements(stmts []BodyStatement, cmd *Command, argFlags map[string]bool) []BodyStatement {
	out := make([]BodyStatement, len(stmts))
	for i, stmt := range stmts {
		switch stmt.Type {
		case "if":
			out[i] = BodyStatement{
				Type:     "if",
				Cond:     stmt.Cond,
				ThenBody: e.cleanStatements(stmt.ThenBody, cmd, argFlags),
				ElseBody: e.cleanStatements(stmt.ElseBody, cmd, argFlags),
			}
		case "for":
			out[i] = BodyStatement{
				Type:      "for",
				LoopVar:   stmt.LoopVar,
				LoopItems: e.cleanShellLine(cmd, stmt.LoopItems, argFlags),
				LoopBody:  stmt.LoopBody,
			}
		default:
			out[i] = BodyStatement{Type: "shell", Shell: e.cleanShellLine(cmd, stmt.Shell, argFlags), OutputName: stmt.OutputName}
		}
	}
	return out
}

func (e *Executor) cleanShellLine(cmd *Command, line string, argFlags map[string]bool) string {
	line = strings.TrimSpace(line)
	if line == "" {
		return ""
	}

	if len(line) > 0 && line[0] == '$' {
		line = strings.TrimSpace(line[1:])
	}

	line = resolveVarRefs(line, func(name string) (string, bool) {
		v, err := e.StructuredParse.GetVariable(name, cmd.Name)
		if err != nil || v == nil {
			return "", false
		}
		return v.Value, true
	})

	for _, arg := range cmd.Arguments {
		lookupKey := cmd.Name + ":" + arg.Name
		if !argFlags[lookupKey] {
			continue
		}

		if !strings.Contains(line, arg.Name) {
			continue
		}
		if e.debug {
			fmt.Printf("[DEBUG] Handling argument --%s for command %s\n", arg.Name, cmd.Name)
		}
		v, err := pflag.CommandLine.GetString(lookupKey)
		if err != nil || v == "" {
			if !arg.IsOptional {
				return line
			}
			continue
		}
		line = strings.ReplaceAll(line, arg.Name, v)
	}

	return line
}

func resolveVarRefs(line string, lookup func(string) (string, bool)) string {
	var result strings.Builder
	runes := []rune(line)
	i := 0
	for i < len(runes) {
		if runes[i] == '&' {
			var name strings.Builder
			j := i + 1
			for j < len(runes) && (unicode.IsLetter(runes[j]) || unicode.IsDigit(runes[j]) || runes[j] == '_' || runes[j] == '-') {
				name.WriteRune(runes[j])
				j++
			}

			if j < len(runes) && runes[j] == '.' {
				k := j + 1
				for k < len(runes) && (unicode.IsLetter(runes[k]) || unicode.IsDigit(runes[k]) || runes[k] == '_') {
					k++
				}
				if k > j+1 {
					name.WriteRune('.')
					for m := j + 1; m < k; m++ {
						name.WriteRune(runes[m])
					}
					j = k
				}
			}
			if name.Len() > 0 {
				if val, ok := lookup(name.String()); ok {
					result.WriteString(val)
					i = j
					continue
				}
			}
		}
		result.WriteRune(runes[i])
		i++
	}
	return result.String()
}

// resolveEnvRefs replaces @ENVNAME references with the corresponding env var.
func resolveEnvRefs(s string) string {
	var result strings.Builder
	runes := []rune(s)
	for i := 0; i < len(runes); {
		if runes[i] == '@' {
			var name strings.Builder
			j := i + 1
			for j < len(runes) && (unicode.IsLetter(runes[j]) || unicode.IsDigit(runes[j]) || runes[j] == '_') {
				name.WriteRune(runes[j])
				j++
			}
			if name.Len() > 0 {
				result.WriteString(os.Getenv(name.String()))
				i = j
				continue
			}
		}
		result.WriteRune(runes[i])
		i++
	}
	return result.String()
}

func evaluateCondition(cond string) bool {
	cond = strings.TrimSpace(cond)

	// Keyword operators (checked before symbol operators so the words are
	// claimed as operators rather than scanned as operands).
	if idx := strings.Index(cond, " contains "); idx > 0 {
		left := strings.TrimSpace(cond[:idx])
		right := strings.TrimSpace(cond[idx+len(" contains "):])
		left = strings.Trim(left, "\"")
		right = strings.Trim(right, "\"")
		return strings.Contains(left, right)
	}

	ops := []string{"==", "!=", ">=", "<=", ">", "<"}
	for _, op := range ops {
		if idx := strings.Index(cond, op); idx > 0 {
			left := strings.TrimSpace(cond[:idx])
			right := strings.TrimSpace(cond[idx+len(op):])
			left = strings.Trim(left, "\"")
			right = strings.Trim(right, "\"")
			return compareValues(left, right, op)
		}
	}
	return false
}

func compareValues(left, right, op string) bool {
	if li, err := strconv.Atoi(left); err == nil {
		if ri, err := strconv.Atoi(right); err == nil {
			return compareInt(li, ri, op)
		}
	}
	return compareString(left, right, op)
}

func compareInt(l, r int, op string) bool {
	switch op {
	case "==":
		return l == r
	case "!=":
		return l != r
	case "<":
		return l < r
	case ">":
		return l > r
	case "<=":
		return l <= r
	case ">=":
		return l >= r
	}
	return false
}

func compareString(l, r, op string) bool {
	switch op {
	case "==":
		return l == r
	case "!=":
		return l != r
	case "<":
		return l < r
	case ">":
		return l > r
	case "<=":
		return l <= r
	case ">=":
		return l >= r
	}
	return false
}

func (e *Executor) tryApplyCloudBody(cmd *Command) error {
	if !cmd.CloudAccessible {
		return nil
	}

	external, err := e.getCloudDefinition(cmd.Name)
	if err != nil || external == nil {
		return nil
	}

	for _, stmt := range external.Body {
		cmd.Body = append(cmd.Body, stmt)
	}
	cmd.CloudAccessible = false
	return nil
}

func (e *Executor) Exec(commands []string) error {
	targets := make([]string, 0, len(commands))
	for _, cmdName := range commands {
		if cmdName == "" || cmdName[0] == '-' {
			continue
		}
		targets = append(targets, cmdName)
	}

	if len(targets) == 0 {
		if defaultCommand, err := e.StructuredParse.GetDefaultCommand(); err == nil && defaultCommand != nil {
			targets = []string{defaultCommand.Name}
		}
	}

	neededScopes := make(map[string]bool)
	for _, name := range targets {
		neededScopes[name] = true
	}
	neededScopes["global"] = true

	for _, cmd := range e.StructuredParse.Commands {
		if cmd.LazyEval == nil {
			continue
		}
		if !neededScopes[cmd.LazyEval.Scope] {
			continue
		}
		if prevCmd, err := e.StructuredParse.GetCommand(cmd.LazyEval.Scope); err == nil && prevCmd != nil {
			cmd.Arguments = append(cmd.Arguments, prevCmd.Arguments...)
		}
		if err := e.EvaluateCommand(cmd); err != nil {
			return err
		}
	}

	if e.concurrent {
		return e.execConcurrent(targets)
	}

	for _, cmdName := range targets {
		if err := e.processCommand(cmdName); err != nil {
			return err
		}
	}

	return nil
}

func (e *Executor) execConcurrent(targets []string) error {
	var waiter sync.WaitGroup
	errCh := make(chan error, len(targets))

	for _, cmdName := range targets {
		waiter.Add(1)
		go func(name string) {
			defer waiter.Done()
			errCh <- e.processCommand(name)
		}(cmdName)
	}

	go func() {
		waiter.Wait()
		close(errCh)
	}()

	var firstErr error
	for err := range errCh {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (e *Executor) Execute(commands []string) error {
	return e.Exec(commands)
}

func (e *Executor) processCommand(name string) error {
	command, err := e.StructuredParse.GetCommand(name)
	if err != nil {
		return err
	}
	return e.EvaluateCommand(command)
}

func (e *Executor) getCloudDefinition(name string) (*Command, error) {
	if err := e.loadCloudDefs(); err != nil {
		return nil, err
	}

	if c, ok := e.cloudDefs[name]; ok {
		return &c, nil
	}

	return nil, fmt.Errorf("%s command not found in cloud", name)
}

// capture reads stdout and stderr concurrently to avoid pipe-buffer deadlock.
func capture(cmd *exec.Cmd) (stdout, stderr []byte, err error) {
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("failed to start command: %w", err)
	}

	var stdoutBytes, stderrBytes []byte
	var stdoutErr, stderrErr error
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		stdoutBytes, stdoutErr = io.ReadAll(stdoutPipe)
	}()

	go func() {
		defer wg.Done()
		stderrBytes, stderrErr = io.ReadAll(stderrPipe)
	}()

	wg.Wait()

	if stdoutErr != nil {
		return nil, nil, fmt.Errorf("failed to read stdout: %w", stdoutErr)
	}
	if stderrErr != nil {
		return nil, nil, fmt.Errorf("failed to read stderr: %w", stderrErr)
	}

	if err := cmd.Wait(); err != nil {
		return stdoutBytes, stderrBytes, err
	}

	return stdoutBytes, stderrBytes, nil
}

const cacheDir = ".construct-cache"

type fileCache map[string]map[string]string

func loadFileCache() fileCache {
	data, err := os.ReadFile(filepath.Join(cacheDir, "manifest.json"))
	if err != nil {
		return fileCache{}
	}
	var fc fileCache
	if err := json.Unmarshal(data, &fc); err != nil {
		return fileCache{}
	}
	return fc
}

func (fc fileCache) save() {
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return
	}
	data, _ := json.MarshalIndent(fc, "", "  ")
	os.WriteFile(filepath.Join(cacheDir, "manifest.json"), data, 0644)
}

func hashFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func expandFileDeps(patterns []string, workDir string) []string {
	wd := workDir
	if wd == "" {
		wd = "."
	}
	var files []string
	for _, pattern := range patterns {
		full := filepath.Join(wd, pattern)
		matches, err := filepath.Glob(full)
		if err != nil || len(matches) == 0 {
			files = append(files, full)
			continue
		}
		files = append(files, matches...)
	}
	return files
}

func (e *Executor) shouldSkip(cmd *Command, resolve func(string, string) string) bool {
	wd := resolve(cmd.WorkDir, cmd.Name)
	files := expandFileDeps(cmd.FileDeps, wd)
	if len(files) == 0 {
		return false
	}

	fc := loadFileCache()
	cached, exists := fc[cmd.Name]
	if !exists {
		return false
	}

	for _, f := range files {
		current := hashFile(f)
		if cached[f] != current {
			if e.debug {
				fmt.Printf("[DEBUG] %s: file changed: %s\n", cmd.Name, f)
			}
			return false
		}
	}
	return true
}

func (e *Executor) updateCache(cmd *Command, resolve func(string, string) string) {
	wd := resolve(cmd.WorkDir, cmd.Name)
	files := expandFileDeps(cmd.FileDeps, wd)
	fc := loadFileCache()
	if fc[cmd.Name] == nil {
		fc[cmd.Name] = make(map[string]string)
	}
	for _, f := range files {
		fc[cmd.Name][f] = hashFile(f)
	}
	fc.save()
}
