package pkg

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
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
		return "cmd", []string{"/c"}
	}
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh, []string{"-c"}
	}
	return "/bin/sh", []string{"-c"}
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
		// A missing cloud file is fine (cloud features are opt-in); a parse
		// error is not, so only swallow not-found-style errors.
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
	execCommandBody := func(target *Command, body []string) error {
		isPrereq := target.IsPrereq
		for lineIdx, cmdLine := range body {
			if cmdLine == "" {
				continue
			}

			args := append(e.shellArgs, cmdLine)
			cmd := exec.Command(e.shellName, args...)

			var fullCommand string
			if e.debug {
				fullCommand = e.shellName + " " + strings.Join(args, " ")
			}

			if e.debug {
				switch {
				case isPrereq:
					fmt.Printf("[DEBUG] Running prerequisite %s[%d]: %s\n", target.Name, lineIdx, fullCommand)
				case target.LazyEval != nil:
					fmt.Printf("[DEBUG] Running lazy command for variable %s: %s\n", target.LazyEval.VarName, fullCommand)
				default:
					fmt.Printf("[DEBUG] Running command %s[%d]: %s\n", target.Name, lineIdx, fullCommand)
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
				e.mu.Unlock()
				if e.debug {
					fmt.Printf("[DEBUG] Prereq output: %s\n", strOutput)
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

	// cleanCommandBody resolves &variable references and arguments, returning a
	// new body slice. It does not mutate the command's stored Body.
	cleanCommandBody := func(cmd *Command) ([]string, error) {
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
			}
		}

		cleanedBody := make([]string, len(cmd.Body))
		copy(cleanedBody, cmd.Body)

		argFlags := make(map[string]bool, len(cmd.Arguments))
		for _, arg := range cmd.Arguments {
			argFlags[cmd.Name+":"+arg.Name] = true
		}

		for lineIdx, line := range cleanedBody {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}

			// `$` execution lines may contain argument placeholders and &vars.
			if line[0] == '$' {
				executionLine := strings.TrimSpace(line[1:])
				linePieces := strings.Fields(executionLine)

				for pieceIdx, piece := range linePieces {
					if idx := strings.IndexByte(piece, '&'); idx >= 0 {
						varName := piece[idx+1:]
						if variable, err := e.StructuredParse.GetVariable(varName, cmd.Name); err == nil && variable != nil {
							linePieces[pieceIdx] = strings.ReplaceAll(piece, "&"+varName, variable.Value)
						}
					}

					lookupKey := cmd.Name + ":" + piece
					if argFlags[lookupKey] {
						if e.debug {
							fmt.Printf("[DEBUG] Handling argument --%s for command %s\n", piece, cmd.Name)
						}

						v, err := pflag.CommandLine.GetString(lookupKey)
						if err != nil || v == "" {
							isOptional := false
							for _, arg := range cmd.Arguments {
								if arg.Name == piece {
									isOptional = arg.IsOptional
									break
								}
							}
							if !isOptional {
								return nil, fmt.Errorf("%s is not optional", lookupKey)
							}
						}

						linePieces[pieceIdx] = v
					}
				}

				cleanedBody[lineIdx] = strings.Join(linePieces, " ")
				continue
			}

			// Non-$ lines: resolve any &variable references inline.
			cleanedBody[lineIdx] = resolveVarRefs(line, func(name string) (string, bool) {
				v, err := e.StructuredParse.GetVariable(name, cmd.Name)
				if err != nil || v == nil {
					return "", false
				}
				return v.Value, true
			})
		}

		return cleanedBody, nil
	}

	// Run prerequisites first. Each prereq is executed with a copy of its body
	// so repeated invocations don't accumulate substitutions.
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

		preBody, err := cleanCommandBody(preCmd)
		if err != nil {
			return err
		}

		if err := execCommandBody(preCmd, preBody); err != nil {
			return err
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

	return execCommandBody(command, body)
}

// resolveVarRefs replaces &name references in a line using lookup, leaving
// unknown references untouched.
func resolveVarRefs(line string, lookup func(string) (string, bool)) string {
	var result strings.Builder
	runes := []rune(line)
	i := 0
	for i < len(runes) {
		if runes[i] == '&' {
			var name strings.Builder
			j := i + 1
			for j < len(runes) && (unicode.IsLetter(runes[j]) || runes[j] == '_') {
				name.WriteRune(runes[j])
				j++
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

func (e *Executor) tryApplyCloudBody(cmd *Command) error {
	if !cmd.CloudAccessible {
		return nil
	}

	external, err := e.getCloudDefinition(cmd.Name)
	if err != nil {
		return nil
	}

	cmd.Body = append(cmd.Body, external.Body...)
	return nil
}

func (e *Executor) Exec(commands []string) error {
	if len(commands) == 0 {
		defaultCommand, err := e.StructuredParse.GetDefaultCommand()
		if err == nil && defaultCommand != nil {
			if err := e.EvaluateCommand(defaultCommand); err != nil {
				return err
			}
		}
	}

	for _, cmd := range e.StructuredParse.Commands {
		if cmd.LazyEval != nil {
			prevCmd, err := e.StructuredParse.GetCommand(cmd.LazyEval.Scope)
			if err == nil && prevCmd != nil {
				cmd.Arguments = append(cmd.Arguments, prevCmd.Arguments...)
			}

			if err := e.EvaluateCommand(cmd); err != nil {
				return err
			}
		}
	}

	targets := make([]string, 0, len(commands))
	for _, cmdName := range commands {
		if cmdName == "" || cmdName[0] == '-' {
			continue
		}
		targets = append(targets, cmdName)
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

// execConcurrent runs the named commands concurrently, returning the first
// non-nil error. All goroutines are awaited before returning.
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
