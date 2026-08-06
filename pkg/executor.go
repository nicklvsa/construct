package pkg

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/spf13/pflag"
)

type CommandError struct {
	Cmd      string
	ExitCode int
	Stderr   string
	File     string
	Line     int
}

func (e *CommandError) Error() string {
	loc := ""
	if e.File != "" {
		loc = fmt.Sprintf(" (%s:%d)", e.File, e.Line)
	}
	if e.Stderr != "" {
		return fmt.Sprintf("command '%s' failed (exit %d)%s: %s", e.Cmd, e.ExitCode, loc, e.Stderr)
	}
	return fmt.Sprintf("command '%s' failed (exit %d)%s", e.Cmd, e.ExitCode, loc)
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
	env             []string
	flagSet         *pflag.FlagSet
	cache           fileCache
	cacheLoaded     bool
	runs            map[string]*commandRun
	baseDir         string
	jobs            int
	sem             chan struct{}
	mu              sync.Mutex
	invokeDepth     map[string]int // in-progress invoke chains, for cycle detection
	timing          bool           // print per-command elapsed time
}

type commandRun struct {
	done chan struct{}
	err  error
}

var (
	errLoopContinue = errors.New("continue used outside of a loop")
	errLoopBreak    = errors.New("break used outside of a loop")
)

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
		return sh, nonInteractiveArgs(sh)
	}
	return "/bin/sh", []string{"-c"}
}

func nonInteractiveArgs(shell string) []string {
	switch base := path.Base(strings.ReplaceAll(shell, "\\", "/")); base {
	case "zsh":
		return []string{"-f", "-c"}
	case "bash", "bash.exe":
		return []string{"--noprofile", "--norc", "-c"}
	}
	return []string{"-c"}
}

func (e *Executor) computeChildEnv() []string {
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
			shellName, shellArgs = sh, nonInteractiveArgs(sh)
		}
	}

	executor := &Executor{
		concurrent:      concurrent,
		debug:           debug,
		cloudFile:       cloudFile,
		shellName:       shellName,
		shellArgs:       shellArgs,
		StructuredParse: data,
	}
	executor.env = executor.computeChildEnv()
	return executor
}

func (e *Executor) loadCloudDefs() error {
	e.mu.Lock()
	defer e.mu.Unlock()

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
	e.flagSet = flagSet
	for _, cmd := range e.StructuredParse.Commands {
		for _, arg := range cmd.Arguments {
			flagName := fmt.Sprintf("%s:%s", cmd.Name, arg.Name)
			flagSet.String(flagName, arg.Default, fmt.Sprintf("Argument %s for command %s", arg.Name, cmd.Name))
		}
	}
}

func (e *Executor) SetDebug(debug bool) {
	e.debug = debug
}

func (e *Executor) SetBaseDir(dir string) {
	e.baseDir = dir
}

// SetJobs caps the number of commands executed in parallel (0 = unlimited).
func (e *Executor) SetJobs(n int) {
	e.jobs = n
	if n > 0 {
		e.sem = make(chan struct{}, n)
	}
}

// SetTiming enables per-command elapsed-time output.
func (e *Executor) SetTiming(t bool) {
	e.timing = t
}

func (e *Executor) acquire() (release func()) {
	if e.sem == nil {
		return func() {}
	}
	e.sem <- struct{}{}
	return func() { <-e.sem }
}

func (e *Executor) resolveWorkDir(dir string) string {
	if dir == "" || e.baseDir == "" || filepath.IsAbs(dir) {
		return dir
	}
	return filepath.Join(e.baseDir, dir)
}

func (e *Executor) EvaluateCommand(command *Command) error {
	if e.runs == nil {
		return e.executeCommand(command)
	}

	e.mu.Lock()
	if r, ok := e.runs[command.Name]; ok {
		e.mu.Unlock()
		<-r.done
		return r.err
	}
	r := &commandRun{done: make(chan struct{})}
	e.runs[command.Name] = r
	e.mu.Unlock()

	r.err = e.executeCommand(command)
	close(r.done)
	return r.err
}

func (e *Executor) executeCommand(command *Command) error {
	start := time.Now()
	e.mu.Lock()
	workDir := command.WorkDir
	isPrereq := command.IsPrereq
	if isPrereq {
		command.PrereqOutput = []string{}
	}
	e.mu.Unlock()

	resolveValue := func(s, scope string) string {
		s = resolveVarRefs(s, func(name string) (string, bool) {
			return e.StructuredParse.LookupVariable(name, scope)
		})
		return resolveEnvRefs(s)
	}

	if len(command.FileDeps) > 0 && !isPrereq && len(command.Produces) == 0 {
		if e.shouldSkip(command, resolveValue, workDir) {
			fmt.Printf("(%s cached)\n", command.Name)
			return nil
		}
	}
	if len(command.Produces) > 0 && !isPrereq {
		if e.shouldSkipProduced(command, resolveValue, workDir) {
			fmt.Printf("(%s up to date)\n", command.Name)
			return nil
		}
	}

	var execCommandBody func(target *Command, body []BodyStatement, isPrereq bool, workDir string, srcFile string, out io.Writer, env *[]string) error
	execCommandBody = func(target *Command, body []BodyStatement, isPrereq bool, workDir string, srcFile string, out io.Writer, env *[]string) error {
		resolveBodyValue := func(s, scope string) string {
			s = resolveVarRefs(s, func(name string) (string, bool) {
				return e.StructuredParse.LookupVariable(name, scope)
			})
			return resolveEnvRefsWith(s, func(name string) string {
				if v, ok := envLookupValue(*env, name); ok {
					return v
				}
				return os.Getenv(name)
			})
		}
		envRef := func(s string) string {
			return resolveEnvRefsSetWith(s, func(name string) (string, bool) {
				if v, ok := envLookupValue(*env, name); ok {
					return v, true
				}
				return os.LookupEnv(name)
			})
		}

		condBase := e.baseDir
		if workDir != "" {
			condBase = e.resolveWorkDir(resolveBodyValue(workDir, target.Name))
		}
		for i := 0; i < len(body); i++ {
			stmt := body[i]
			if stmt.Type == "env" {
				for _, pair := range stmt.Env {
					key, value, _ := strings.Cut(pair, "=")
					value = resolveVarRefs(value, func(name string) (string, bool) {
						return e.StructuredParse.LookupVariable(name, target.Name)
					})

					resolved := envRef(value)
					*env = setEnvVar(*env, key, resolved)

					e.StructuredParse.SetVariable(key, target.Name, resolved)
					if e.debug {
						fmt.Printf("[DEBUG] env %s=%s\n", key, resolved)
					}
				}

				argFlags := make(map[string]bool, len(target.Arguments))
				for _, arg := range target.Arguments {
					argFlags[target.Name+":"+arg.Name] = true
				}
				rest := e.cleanStatements(body[i+1:], target, argFlags)
				body = append(body[:i+1], rest...)
				continue
			}

			if stmt.Type == "if" {
				cond := resolveBodyValue(stmt.Cond, target.Name)
				if e.debug {
					fmt.Printf("[DEBUG] Evaluating condition: %s\n", cond)
				}
				if evaluateConditionWithBase(cond, condBase) {
					if err := execCommandBody(target, stmt.ThenBody, isPrereq, workDir, srcFile, out, env); err != nil {
						return err
					}
				} else if stmt.ElseBody != nil {
					if err := execCommandBody(target, stmt.ElseBody, isPrereq, workDir, srcFile, out, env); err != nil {
						return err
					}
				}
				continue
			}

			if stmt.Type == "invoke" {
				invoked, err := e.StructuredParse.GetCommand(strings.TrimSpace(stmt.Shell))
				if err != nil {
					return err
				}
				if e.invokeDepth == nil {
					e.invokeDepth = make(map[string]int)
				}
				e.mu.Lock()
				if e.invokeDepth[invoked.Name] > 0 {
					e.mu.Unlock()
					return fmt.Errorf("circular invoke of command '%s'", invoked.Name)
				}
				e.invokeDepth[invoked.Name]++
				e.mu.Unlock()
				invokeErr := func() error {
					defer func() {
						e.mu.Lock()
						e.invokeDepth[invoked.Name]--
						e.mu.Unlock()
					}()
					argFlags := make(map[string]bool, len(target.Arguments))
					for _, arg := range target.Arguments {
						argFlags[target.Name+":"+arg.Name] = true
					}
					cleaned := e.cleanStatements(invoked.Body, target, argFlags)
					if stmt.OutputName != "" {
						var buf bytes.Buffer
						if err := execCommandBody(target, cleaned, isPrereq, workDir, invoked.SourceFile, &buf, env); err != nil {
							return err
						}
						e.StructuredParse.SetVariable(stmt.OutputName, target.Name, strings.TrimSpace(buf.String()))
						if e.debug {
							fmt.Printf("[DEBUG] invoke %s captured %d bytes\n", invoked.Name, buf.Len())
						}
						return nil
					}
					return execCommandBody(target, cleaned, isPrereq, workDir, invoked.SourceFile, nil, env)
				}()
				if invokeErr != nil {
					return invokeErr
				}
				continue
			}

			if stmt.Type == "for" {
				items := resolveBodyValue(stmt.LoopItems, target.Name)
				items = e.expandOutputRefs(items, target.Name)
				if items == "" {
					continue
				}
				var expanded []string
				if strings.ContainsAny(items, "*?") {
					wd := "."
					if workDir != "" {
						wd = e.resolveWorkDir(resolveBodyValue(workDir, target.Name))
					} else if e.baseDir != "" {
						wd = e.baseDir
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
				} else if rng, ok := expandRange(items); ok {
					expanded = rng
				} else {
					for item := range strings.SplitSeq(items, ",") {
						expanded = append(expanded, strings.TrimSpace(item))
					}
				}

				argFlags := make(map[string]bool)
				for _, arg := range target.Arguments {
					argFlags[target.Name+":"+arg.Name] = true
				}

			iterLoop:
				for idx, item := range expanded {
					e.StructuredParse.SetVariable(stmt.LoopVar, target.Name, item)
					if stmt.LoopIndex != "" {
						e.StructuredParse.SetVariable(stmt.LoopIndex, target.Name, strconv.Itoa(idx))
					}
					if e.debug {
						fmt.Printf("[DEBUG] For loop %s = %s\n", stmt.LoopVar, item)
					}
					cleaned := e.cleanStatements(stmt.LoopBody, target, argFlags)
					err := execCommandBody(target, cleaned, isPrereq, workDir, srcFile, out, env)
					switch {
					case errors.Is(err, errLoopContinue):
						continue
					case errors.Is(err, errLoopBreak):
						break iterLoop
					case err != nil:
						return err
					}
				}
				continue
			}

			if stmt.Type == "continue" {
				return errLoopContinue
			}
			if stmt.Type == "break" {
				return errLoopBreak
			}

			cmdLine := stmt.Shell
			if cmdLine == "" {
				continue
			}

			ignoreErr := false
			if strings.HasPrefix(cmdLine, "!") {
				ignoreErr = true
				cmdLine = strings.TrimSpace(cmdLine[1:])
			}

			cmdLine = envRef(cmdLine)

			args := append(e.shellArgs, cmdLine)
			cmd := exec.Command(e.shellName, args...)
			if workDir != "" {
				cmd.Dir = e.resolveWorkDir(resolveBodyValue(workDir, target.Name))
			}
			cmd.Env = *env

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

			release := e.acquire()
			if !e.debug && !isPrereq && target.LazyEval == nil && out == nil {
				cmd.Stdout = os.Stdout
				cmd.Stderr = os.Stderr
				err := cmd.Run()
				release()
				if err != nil {
					exitCode := 1
					if ee, ok := err.(*exec.ExitError); ok {
						exitCode = ee.ExitCode()
					}
					if !ignoreErr {
						return &CommandError{
							Cmd:      e.shellName + " " + strings.Join(args, " "),
							ExitCode: exitCode,
							File:     srcFile,
							Line:     stmt.SourceLine,
						}
					}
				}
				continue
			}

			var stdout, stderr []byte
			var err error
			if e.debug {
				stdout, stderr, err = capture(cmd)
			} else {
				stdout, err = cmd.Output()
			}

			release()

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

				if ignoreErr {
					if e.debug {
						fmt.Printf("[DEBUG] Ignoring failure (error-tolerant statement)\n")
					}
				} else {
					return &CommandError{
						Cmd:      fullCommand,
						ExitCode: exitCode,
						Stderr:   string(stderr),
						File:     srcFile,
						Line:     stmt.SourceLine,
					}
				}
			}

			strOutput := strings.TrimSpace(string(output))

			if out != nil {
				if err == nil {
					// The capture buffer cannot fail; ignore write errors.
					_, _ = out.Write(output)
				}
				continue
			}

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
				e.StructuredParse.SetVariable(target.LazyEval.VarName, target.LazyEval.Scope, strOutput)
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
		if len(cmd.PrereqCmds) > 0 {
			for _, prereq := range cmd.PrereqCmds {
				for idx, arg := range prereq.PrereqOutput {
					varName := prereq.Name + "." + fmt.Sprintf("%d", idx)
					e.StructuredParse.SetVariable(strings.TrimSpace(varName), cmd.Name, strings.TrimSpace(arg))
				}
				// Register named outputs (&prereq.name)
				for name, val := range prereq.NamedOutput {
					varName := prereq.Name + "." + name
					e.StructuredParse.SetVariable(varName, cmd.Name, strings.TrimSpace(val))
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
						Type:       "if",
						Cond:       stmt.Cond,
						ThenBody:   cleanStmts(stmt.ThenBody),
						ElseBody:   cleanStmts(stmt.ElseBody),
						SourceLine: stmt.SourceLine,
					}
					continue
				}
				if stmt.Type == "for" {
					out[i] = BodyStatement{
						Type:       "for",
						LoopVar:    stmt.LoopVar,
						LoopIndex:  stmt.LoopIndex,
						LoopItems:  e.cleanShellLine(cmd, stmt.LoopItems, argFlags),
						LoopBody:   stmt.LoopBody,
						SourceLine: stmt.SourceLine,
					}
					continue
				}
				if stmt.Type == "continue" || stmt.Type == "break" {
					out[i] = stmt
					continue
				}
				if stmt.Type == "invoke" || stmt.Type == "env" {
					out[i] = stmt
					continue
				}
				out[i] = BodyStatement{Type: "shell", Shell: e.cleanShellLine(cmd, stmt.Shell, argFlags), OutputName: stmt.OutputName, SourceLine: stmt.SourceLine}
			}
			return out
		}

		return cleanStmts(cmd.Body), nil
	}

	var prereqCmds []*Command
	for _, prereqName := range command.Prereqs {
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
		if dir := command.PrereqDirs[prereqName]; dir != "" {
			preCmd.WorkDir = dir
		}
		e.mu.Unlock()

		prereqCmds = append(prereqCmds, preCmd)
	}

	if command.PrereqCmds == nil {
		command.PrereqCmds = []*Command{}
	}

	if e.concurrent && len(prereqCmds) > 1 {
		// DAG execution: evaluate independent prerequisites in parallel.
		results := make([]*Command, len(prereqCmds))
		errs := make([]error, len(prereqCmds))
		var wg sync.WaitGroup
		for i, preCmd := range prereqCmds {
			wg.Add(1)
			go func(i int, pc *Command) {
				defer wg.Done()
				errs[i] = e.EvaluateCommand(pc)
				results[i] = pc
			}(i, preCmd)
		}
		wg.Wait()
		for _, err := range errs {
			if err != nil {
				return err
			}
		}
		command.PrereqCmds = append(command.PrereqCmds, results...)
	} else {
		for _, preCmd := range prereqCmds {
			if err := e.EvaluateCommand(preCmd); err != nil {
				return err
			}
			command.PrereqCmds = append(command.PrereqCmds, preCmd)
		}
	}

	if err := e.tryApplyCloudBody(command); err != nil {
		return err
	}

	body, err := cleanCommandBody(command)
	if err != nil {
		return err
	}

	cmdEnv := slices.Clone(e.env)

	if err := execCommandBody(command, body, isPrereq, workDir, command.SourceFile, nil, &cmdEnv); err != nil {
		return err
	}

	if len(command.FileDeps) > 0 && !isPrereq && len(command.Produces) == 0 {
		e.updateCache(command, resolveValue, workDir)
	}

	if e.timing && !isPrereq && command.LazyEval == nil {
		fmt.Printf("(%s completed in %s)\n", command.Name, time.Since(start).Round(time.Millisecond))
	}

	return nil
}

func (e *Executor) cleanStatements(stmts []BodyStatement, cmd *Command, argFlags map[string]bool) []BodyStatement {
	out := make([]BodyStatement, len(stmts))
	for i, stmt := range stmts {
		switch stmt.Type {
		case "if":
			out[i] = BodyStatement{
				Type:       "if",
				Cond:       stmt.Cond,
				ThenBody:   e.cleanStatements(stmt.ThenBody, cmd, argFlags),
				ElseBody:   e.cleanStatements(stmt.ElseBody, cmd, argFlags),
				SourceLine: stmt.SourceLine,
			}
		case "for":
			out[i] = BodyStatement{
				Type:       "for",
				LoopVar:    stmt.LoopVar,
				LoopIndex:  stmt.LoopIndex,
				LoopItems:  e.cleanShellLine(cmd, stmt.LoopItems, argFlags),
				LoopBody:   stmt.LoopBody,
				SourceLine: stmt.SourceLine,
			}
		case "continue", "break":
			out[i] = stmt
		case "invoke", "env":
			out[i] = stmt
		default:
			out[i] = BodyStatement{Type: "shell", Shell: e.cleanShellLine(cmd, stmt.Shell, argFlags), OutputName: stmt.OutputName, SourceLine: stmt.SourceLine}
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
		val, ok := e.StructuredParse.LookupVariable(name, cmd.Name)
		if !ok {
			return "", false
		}
		return escapeShellValue(val), true
	})

	for _, arg := range cmd.Arguments {
		lookupKey := cmd.Name + ":" + arg.Name
		if !argFlags[lookupKey] {
			continue
		}

		if !strings.Contains(line, "&"+arg.Name) {
			continue
		}
		if e.debug {
			fmt.Printf("[DEBUG] Handling argument --%s for command %s\n", arg.Name, cmd.Name)
		}
		fs := e.flagSet
		if fs == nil {
			fs = pflag.CommandLine
		}

		v, _ := fs.GetString(lookupKey)
		line = strings.ReplaceAll(line, "&"+arg.Name, escapeShellValue(v))
	}

	// @ENV references resolve in shell lines too; unset ones stay literal.
	return resolveEnvRefsSet(line)
}

func (e *Executor) expandOutputRefs(items, scope string) string {
	if !strings.Contains(items, ".*") {
		return items
	}
	var result strings.Builder
	i := 0
	for i < len(items) {
		if items[i] == '&' && i+1 < len(items) && isVarIdentByte(items[i+1]) {
			j := i + 1
			for j < len(items) && isVarIdentByte(items[j]) {
				j++
			}

			for j < len(items) && items[j] == '.' && j+1 < len(items) && isVarIdentByte(items[j+1]) {
				j++
				for j < len(items) && isVarIdentByte(items[j]) {
					j++
				}
			}

			if j+1 < len(items) && items[j] == '.' && items[j+1] == '*' {
				name := items[i+1 : j]
				if _, err := e.StructuredParse.GetCommand(name); err == nil {
					var outs []string
					for idx := 0; ; idx++ {
						val, ok := e.StructuredParse.LookupVariable(fmt.Sprintf("%s.%d", name, idx), scope)
						if !ok {
							break
						}
						outs = append(outs, val)
					}
					result.WriteString(strings.Join(outs, ", "))
					i = j + 2
					continue
				}
			}
		}
		result.WriteByte(items[i])
		i++
	}
	return result.String()
}

func isVarIdentByte(c byte) bool {
	return c == '_' || c == '-' ||
		(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// envLookupValue finds KEY=value in an env slice.
func envLookupValue(env []string, name string) (string, bool) {
	prefix := name + "="
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return kv[len(prefix):], true
		}
	}
	return "", false
}

func setEnvVar(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			continue // replaced below
		}
		out = append(out, kv)
	}
	return append(out, prefix+value)
}

func escapeShellValue(s string) string {
	if !strings.ContainsAny(s, "`\"\\$") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '`', '"', '\\', '$':
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func isPlainIdentByte(c byte) bool {
	return c == '_' ||
		(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

func resolveVarRefs(line string, lookup func(string) (string, bool)) string {
	if strings.IndexByte(line, '&') < 0 {
		return line
	}
	if !isASCII(line) {
		return resolveVarRefsRunes(line, lookup)
	}

	var result strings.Builder
	result.Grow(len(line))
	i := 0
	for i < len(line) {
		if line[i] == '\\' && i+1 < len(line) && line[i+1] == '&' {
			result.WriteByte('&')
			i += 2
			continue
		}
		if line[i] == '&' {
			j := i + 1
			start := j
			for j < len(line) && isVarIdentByte(line[j]) {
				j++
			}

			for j < len(line) && line[j] == '.' && j+1 < len(line) && isPlainIdentByte(line[j+1]) {
				j++
				for j < len(line) && isPlainIdentByte(line[j]) {
					j++
				}
			}
			if name := line[start:j]; name != "" {
				if val, ok := lookup(name); ok {
					result.WriteString(val)
					i = j
					continue
				}

				if dot := strings.IndexByte(name, '.'); dot > 0 {
					if val, ok := lookup(name[:dot]); ok {
						result.WriteString(val)
						i = start + dot
						continue
					}
				}
			}
		}
		result.WriteByte(line[i])
		i++
	}
	return result.String()
}

func resolveVarRefsRunes(line string, lookup func(string) (string, bool)) string {
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

			for j < len(runes) && runes[j] == '.' && j+1 < len(runes) && (unicode.IsLetter(runes[j+1]) || unicode.IsDigit(runes[j+1]) || runes[j+1] == '_') {
				name.WriteRune('.')
				j++
				for j < len(runes) && (unicode.IsLetter(runes[j]) || unicode.IsDigit(runes[j]) || runes[j] == '_') {
					name.WriteRune(runes[j])
					j++
				}
			}
			if name.Len() > 0 {
				full := name.String()
				if val, ok := lookup(full); ok {
					result.WriteString(val)
					i = j
					continue
				}

				dot := strings.IndexByte(full, '.')
				if dot > 0 {
					if val, ok := lookup(full[:dot]); ok {
						result.WriteString(val)
						i = i + 1 + dot
						continue
					}
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
	return resolveEnvRefsWith(s, os.Getenv)
}

func resolveEnvRefsWith(s string, lookup func(string) string) string {
	if strings.IndexByte(s, '@') < 0 {
		return s
	}
	if !isASCII(s) {
		return resolveEnvRefsRunesWith(s, lookup)
	}

	var result strings.Builder
	result.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == '\\' && i+1 < len(s) && s[i+1] == '@' {
			result.WriteByte('@')
			i += 2
			continue
		}
		if s[i] == '@' {
			j := i + 1
			start := j
			for j < len(s) && isPlainIdentByte(s[j]) {
				j++
			}
			if j > start {
				result.WriteString(lookup(s[start:j]))
				i = j
				continue
			}
		}
		result.WriteByte(s[i])
		i++
	}
	return result.String()
}

func resolveEnvRefsSet(s string) string {
	return resolveEnvRefsSetWith(s, func(name string) (string, bool) {
		val, ok := os.LookupEnv(name)
		return val, ok
	})
}

func resolveEnvRefsSetWith(s string, lookup func(string) (string, bool)) string {
	if strings.IndexByte(s, '@') < 0 {
		return s
	}

	var result strings.Builder
	result.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == '\\' && i+1 < len(s) && s[i+1] == '@' {
			result.WriteByte('@')
			i += 2
			continue
		}
		if s[i] == '@' {
			j := i + 1
			start := j
			for j < len(s) && isPlainIdentByte(s[j]) {
				j++
			}
			if j > start {
				if val, ok := lookup(s[start:j]); ok {
					result.WriteString(val)
					i = j
					continue
				}
			}
		}
		result.WriteByte(s[i])
		i++
	}
	return result.String()
}

func resolveEnvRefsRunes(s string) string {
	return resolveEnvRefsRunesWith(s, os.Getenv)
}

func resolveEnvRefsRunesWith(s string, lookup func(string) string) string {
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
				result.WriteString(lookup(name.String()))
				i = j
				continue
			}
		}
		result.WriteRune(runes[i])
		i++
	}
	return result.String()
}

func findTopLevelOp(s, op string) int {
	depth := 0
	inQuote := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '"' {
			inQuote = !inQuote
			continue
		}
		if inQuote {
			continue
		}
		switch c {
		case '(':
			depth++
		case ')':
			depth--
		}
		if depth == 0 && strings.HasPrefix(s[i:], op) {
			return i
		}
	}
	return -1
}

var conditionOps = []string{"==", "!=", ">=", "<=", ">", "<"}

func evaluateCondition(cond string) bool {
	return evaluateConditionWithBase(cond, "")
}

func evaluateConditionWithBase(cond, base string) bool {
	cond = strings.TrimSpace(cond)

	// Parenthesized sub-expression: ( expr )
	if strings.HasPrefix(cond, "(") && strings.HasSuffix(cond, ")") {
		return evaluateConditionWithBase(strings.TrimSpace(cond[1:len(cond)-1]), base)
	}

	if idx := findTopLevelOp(cond, "||"); idx >= 0 {
		return evaluateConditionWithBase(cond[:idx], base) || evaluateConditionWithBase(cond[idx+2:], base)
	}
	if idx := findTopLevelOp(cond, "&&"); idx >= 0 {
		return evaluateConditionWithBase(cond[:idx], base) && evaluateConditionWithBase(cond[idx+2:], base)
	}

	// Negation: ! expr
	if strings.HasPrefix(cond, "!") {
		rest := strings.TrimSpace(cond[1:])
		return rest != "" && !evaluateConditionWithBase(rest, base)
	}

	if result, ok := evalBuiltinCondition(cond, base); ok {
		return result
	}

	if idx := strings.Index(cond, " contains "); idx > 0 {
		left := strings.TrimSpace(cond[:idx])
		right := strings.TrimSpace(cond[idx+len(" contains "):])
		left = strings.Trim(left, "\"")
		right = strings.Trim(right, "\"")
		return strings.Contains(left, right)
	}

	if idx := findTopLevelOp(cond, " in "); idx > 0 {
		left := strings.Trim(strings.TrimSpace(cond[:idx]), "\"")
		list := strings.Trim(strings.TrimSpace(cond[idx+len(" in "):]), "\"")
		for item := range strings.SplitSeq(list, ",") {
			if left == strings.TrimSpace(item) {
				return true
			}
		}
		return false
	}

	ops := conditionOps
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

func evalBuiltinCondition(cond, base string) (bool, bool) {
	open := strings.IndexByte(cond, '(')
	if open <= 0 || !strings.HasSuffix(cond, ")") {
		return false, false
	}
	name := strings.TrimSpace(cond[:open])
	arg := strings.Trim(strings.TrimSpace(cond[open+1:len(cond)-1]), `"`)
	if arg == "" {
		return false, false
	}
	if base != "" && !filepath.IsAbs(arg) {
		arg = filepath.Join(base, arg)
	}
	switch name {
	case "exists":
		_, err := os.Stat(arg)
		return err == nil, true
	case "missing":
		_, err := os.Stat(arg)
		return err != nil, true
	case "glob":
		matches, _ := filepath.Glob(arg)
		return len(matches) > 0, true
	case "require":
		_, err := exec.LookPath(arg)
		return err == nil, true
	}
	return false, false
}

func LoadEnvFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"' {
			val = val[1 : len(val)-1]
		}
		if key == "" {
			continue
		}
		if _, exists := os.LookupEnv(key); !exists {
			os.Setenv(key, val)
		}
	}
	return nil
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

	cmd.Body = append(cmd.Body, external.Body...)
	cmd.CloudAccessible = false
	return nil
}

func (e *Executor) Execute(commands []string) error {
	e.runs = make(map[string]*commandRun)

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
	var addPrereqs func(name string)
	addPrereqs = func(name string) {
		if neededScopes[name] {
			return
		}
		neededScopes[name] = true
		if cmd, err := e.StructuredParse.GetCommand(name); err == nil {
			for _, prereq := range cmd.Prereqs {
				addPrereqs(strings.TrimSpace(prereq))
			}
		}
	}
	for _, name := range targets {
		addPrereqs(name)
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

func expandRange(s string) ([]string, bool) {
	a, b, ok := strings.Cut(strings.TrimSpace(s), "..")
	if !ok {
		return nil, false
	}
	start, err1 := strconv.Atoi(strings.TrimSpace(a))
	end, err2 := strconv.Atoi(strings.TrimSpace(b))
	if err1 != nil || err2 != nil {
		return nil, false
	}
	var out []string
	if start <= end {
		for i := start; i <= end; i++ {
			out = append(out, strconv.Itoa(i))
		}
	} else {
		for i := start; i >= end; i-- {
			out = append(out, strconv.Itoa(i))
		}
	}
	return out, true
}

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

func (e *Executor) cacheDirFor() string {
	if e.baseDir != "" {
		return filepath.Join(e.baseDir, cacheDir)
	}
	return cacheDir
}

func loadFileCache(dir string) fileCache {
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return fileCache{}
	}
	var fc fileCache
	if err := json.Unmarshal(data, &fc); err != nil {
		return fileCache{}
	}
	return fc
}

func (fc fileCache) save(dir string) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return
	}
	data, _ := json.MarshalIndent(fc, "", "  ")
	// A failed manifest write is not fatal; the build result stands, and the
	// cache is simply rebuilt on the next run.
	_ = os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0644)
}

func hashFile(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
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

func (e *Executor) cacheManifest() fileCache {
	e.mu.Lock()
	defer e.mu.Unlock()

	if !e.cacheLoaded {
		e.cache = loadFileCache(e.cacheDirFor())
		e.cacheLoaded = true
	}
	return e.cache
}

func (e *Executor) cacheKey(cmd *Command) string {
	parts := []string{cmd.Name}
	for _, arg := range cmd.Arguments {
		v, _ := e.flagSet.GetString(cmd.Name + ":" + arg.Name)
		parts = append(parts, arg.Name+"="+v)
	}
	for name, val := range e.StructuredParse.GlobalVariableSnapshot() {
		parts = append(parts, "var:"+name+"="+val)
	}
	sort.Strings(parts[1:])
	return strings.Join(parts, "|")
}

func (e *Executor) shouldSkip(cmd *Command, resolve func(string, string) string, workDir string) bool {
	wd := e.resolveWorkDir(resolve(workDir, cmd.Name))
	if wd == "" {
		wd = e.baseDir
	}
	files := expandFileDeps(cmd.FileDeps, wd)
	if len(files) == 0 {
		return false
	}

	fc := e.cacheManifest()
	key := e.cacheKey(cmd)
	cached, exists := fc[key]
	if !exists {
		return false
	}

	hashes := parallelHash(files)
	for i, f := range files {
		if cached[f] != hashes[i] {
			if e.debug {
				fmt.Printf("[DEBUG] %s: file changed: %s\n", cmd.Name, f)
			}
			return false
		}
	}
	return true
}

// parallelHash computes sha256 hashes of the given files concurrently.
func parallelHash(files []string) []string {
	if len(files) < 2 {
		out := make([]string, len(files))
		for i, f := range files {
			out[i] = hashFile(f)
		}
		return out
	}
	out := make([]string, len(files))
	var wg sync.WaitGroup
	for i, f := range files {
		wg.Add(1)
		go func(i int, f string) {
			defer wg.Done()
			out[i] = hashFile(f)
		}(i, f)
	}
	wg.Wait()
	return out
}

func (e *Executor) shouldSkipProduced(cmd *Command, resolve func(string, string) string, workDir string) bool {
	wd := e.resolveWorkDir(resolve(workDir, cmd.Name))
	if wd == "" {
		wd = e.baseDir
	}
	artifacts := expandFileDeps(cmd.Produces, wd)
	if len(artifacts) == 0 {
		return false
	}
	var newest time.Time
	for _, a := range artifacts {
		info, err := os.Stat(a)
		if err != nil || info.IsDir() {
			return false // missing artifact => must build
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	for _, d := range expandFileDeps(cmd.FileDeps, wd) {
		info, err := os.Stat(d)
		if err != nil {
			return false
		}
		if info.ModTime().After(newest) {
			return false
		}
	}
	return true
}

func (e *Executor) updateCache(cmd *Command, resolve func(string, string) string, workDir string) {
	wd := e.resolveWorkDir(resolve(workDir, cmd.Name))
	if wd == "" {
		wd = e.baseDir
	}
	files := expandFileDeps(cmd.FileDeps, wd)

	e.mu.Lock()
	defer e.mu.Unlock()

	if !e.cacheLoaded {
		e.cache = loadFileCache(e.cacheDirFor())
		e.cacheLoaded = true
	}
	fc := e.cache
	key := e.cacheKey(cmd)
	if fc[key] == nil {
		fc[key] = make(map[string]string)
	}
	hashes := parallelHash(files)
	for i, f := range files {
		fc[key][f] = hashes[i]
	}
	fc.save(e.cacheDirFor())
}
