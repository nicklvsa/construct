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

// CommandError wraps a command execution failure with its exit code
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
	dryRun          bool
	cloudFile       string
	cloudDefs       map[string]Command
	cloudLoaded     bool
	shellName       string
	shellArgs       []string
	mu              sync.Mutex
}

func NewExecutor(data *ParsedData, concurrent bool, debug bool) *Executor {
	cloudFile := os.Getenv("CONSTRUCT_CLOUD_FILE")
	if cloudFile == "" {
		cloudFile = "fakecloud.json"
	}

	shellName := "/bin/bash"
	shellArgs := []string{"-c"}
	if runtime.GOOS == "windows" {
		shellName = "cmd"
		shellArgs = []string{"/c"}
	}

	return &Executor{
		concurrent:      concurrent,
		debug:           debug,
		dryRun:          false,
		cloudFile:       cloudFile,
		shellName:       shellName,
		shellArgs:       shellArgs,
		StructuredParse: data,
	}
}

// loadCloudDefs reads and caches cloud command definitions on first access
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
		return nil
	}

	e.cloudDefs = defs
	e.cloudLoaded = true
	return nil
}

// RegisterArgumentFlags registers pflag flags for all command arguments
// so they can be resolved during execution
func (e *Executor) RegisterArgumentFlags(flagSet *pflag.FlagSet) {
	for _, cmd := range e.StructuredParse.Commands {
		for _, arg := range cmd.Arguments {
			flagName := fmt.Sprintf("%s:%s", cmd.Name, arg.Name)
			flagSet.String(flagName, "", fmt.Sprintf("Argument %s for command %s", arg.Name, cmd.Name))
		}
	}
}

func (e *Executor) SetDryRun(dryRun bool) {
	e.dryRun = dryRun
}

// SetDebug enables or disables debug mode for verbose output
func (e *Executor) SetDebug(debug bool) {
	e.debug = debug
}

func (e *Executor) Dump(outputLoc string) error {
	data, err := json.MarshalIndent(e, "", "\t")
	if err != nil {
		return err
	}

	return os.WriteFile(outputLoc, data, 0644)
}

func (e *Executor) EvaluateCommand(command *Command) error {
	execCommandBody := func(execCmd *Command) error {
		for lineIdx, cmdLine := range execCmd.Body {
			if cmdLine == "" {
				continue
			}

			args := append(e.shellArgs, cmdLine)
			cmd := exec.Command(e.shellName, args...)

			var fullCommand string
			if e.debug {
				fullCommand = e.shellName + " " + strings.Join(args, " ")
			}

			// Debug output showing what command is being executed
			if e.debug {
				if execCmd.IsPrereq {
					fmt.Printf("[DEBUG] Running prerequisite %s[%d]: %s\n", execCmd.Name, lineIdx, fullCommand)
				} else if command.LazyEval != nil {
					fmt.Printf("[DEBUG] Running lazy command for variable %s: %s\n", command.LazyEval.VarName, fullCommand)
				} else {
					fmt.Printf("[DEBUG] Running command %s[%d]: %s\n", execCmd.Name, lineIdx, fullCommand)
				}
			}

			// Capture both stdout and stderr
			var stdout, stderr []byte
			var err error
			if e.debug {
				stdout, stderr, err = captureOutputWithDebug(cmd)
			} else {
				stdout, err = cmd.Output()
			}

			// Combine output for display
			output := stdout
			if len(stderr) > 0 {
				if len(output) > 0 {
					output = append(output, '\n')
				}
				output = append(output, stderr...)
			}

			// Handle command errors with exit code propagation
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

				// Build fullCommand lazily for the error (only needed if not already built for debug)
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

			if execCmd.IsPrereq {
				e.mu.Lock()
				execCmd.PrereqOutput = append(execCmd.PrereqOutput, strOutput)
				e.mu.Unlock()
				if e.debug {
					fmt.Printf("[DEBUG] Prereq output: %s\n", strOutput)
				}
				continue
			}

			if command.LazyEval != nil {
				e.mu.Lock()
				variable, err := e.StructuredParse.GetVariable(command.LazyEval.VarName, command.LazyEval.Scope)
				if err != nil {
					e.mu.Unlock()
					return err
				}

				variable.Value = strOutput
				e.mu.Unlock()
				if e.debug {
					fmt.Printf("[DEBUG] Set variable %s.%s = %s\n", command.LazyEval.Scope, command.LazyEval.VarName, strOutput)
				}
			} else {
				fmt.Println(strOutput)
			}
		}

		return nil
	}

	// cleanCommandBody resolves variable references and arguments in the
	// command body, returning a new body slice without mutating the original.
	cleanCommandBody := func(uncleanedCommand *Command) ([]string, error) {
		if len(uncleanedCommand.Prereqs) > 0 {
			for _, prereq := range uncleanedCommand.PrereqCmds {
				for idx, arg := range prereq.PrereqOutput {
					var varName strings.Builder
					varName.WriteString(prereq.Name)
					varName.WriteByte('.')
					varName.WriteString(fmt.Sprintf("%d", idx))
					e.mu.Lock()
					e.StructuredParse.AddVariable(&Variable{
						Name:  strings.TrimSpace(varName.String()),
						Value: strings.TrimSpace(arg),
						Scope: uncleanedCommand.Name,
					})
					e.mu.Unlock()
				}
			}
		}

		cleanedBody := make([]string, len(uncleanedCommand.Body))
		copy(cleanedBody, uncleanedCommand.Body)

		// Pre-build argument set for O(1) lookup: piece -> flag lookup key
		argFlags := make(map[string]string, len(uncleanedCommand.Arguments))
		for _, arg := range uncleanedCommand.Arguments {
			key := uncleanedCommand.Name + ":" + arg.Name
			argFlags[key] = key
		}

		for lineIdx, line := range cleanedBody {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}

			if line[0] == '$' {
				executionLine := strings.TrimSpace(line[1:])
				linePieces := strings.Split(executionLine, " ")

				for pieceIdx, piece := range linePieces {
					piece = strings.TrimSpace(piece)

					// Substitute variable references (&varName)
					if idx := strings.IndexByte(piece, '&'); idx >= 0 {
						varName := piece[idx+1:]
						if variable, err := e.StructuredParse.GetVariable(varName, uncleanedCommand.Name); err == nil && variable != nil {
							linePieces[pieceIdx] = strings.ReplaceAll(piece, "&"+varName, variable.Value)
						}
					}

					// O(1) argument lookup instead of iterating all args
					lookupKey := uncleanedCommand.Name + ":" + piece
					if _, ok := argFlags[lookupKey]; ok {
						if e.debug {
							fmt.Printf("[DEBUG] Handling argument --%s for command %s\n", piece, uncleanedCommand.Name)
						}

						v, err := pflag.CommandLine.GetString(lookupKey)
						if err != nil || v == "" {
							isOptional := false
							for _, arg := range uncleanedCommand.Arguments {
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
			}

			for bodyIdx, bodyChar := range line {
				if bodyChar == '&' {
					var varName strings.Builder
					for _, varRef := range line[bodyIdx+1:] {
						if !unicode.IsLetter(varRef) && varRef != '_' {
							break
						}

						varName.WriteRune(varRef)
					}

					if varName.Len() == 0 {
						continue
					}

					name := varName.String()
					varDef, err := e.StructuredParse.GetVariable(name, uncleanedCommand.Name)
					if err == nil && varDef != nil {
						// Build "&name" without fmt.Sprintf
						var pattern strings.Builder
						pattern.WriteByte('&')
						pattern.WriteString(name)
						cleanedBody[lineIdx] = strings.ReplaceAll(cleanedBody[lineIdx], pattern.String(), varDef.Value)
					}
				}
			}
		}

		return cleanedBody, nil
	}

	for _, prereq := range command.Prereqs {
		if command.PrereqCmds == nil {
			command.PrereqCmds = []*Command{}
		}

		prereq = strings.TrimSpace(prereq)
		if len(prereq) <= 0 {
			continue
		}

		preCmd, err := e.StructuredParse.GetCommand(prereq)
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

		cleanedBody, err := cleanCommandBody(preCmd)
		if err != nil {
			return err
		}
		preCmd.Body = cleanedBody

		if err := execCommandBody(preCmd); err != nil {
			return err
		}

		command.PrereqCmds = append(command.PrereqCmds, preCmd)
	}

	if err := e.tryApplyCloudBody(command); err != nil {
		return err
	}

	cleanedBody, err := cleanCommandBody(command)
	if err != nil {
		return err
	}
	command.Body = cleanedBody

	return execCommandBody(command)
}

func (e *Executor) tryApplyCloudBody(cmd *Command) error {
	if !cmd.CloudAccessible {
		return nil
	}

	external, err := e.getCloudDefinition(cmd.Name)
	if err != nil {
		return nil
	}

	// cmd.Prereqs = external.Prereqs
	// cmd.PrereqCmds = external.PrereqCmds
	// cmd.PrereqOutput = external.PrereqOutput

	// Append mode by default. Maybe make this configurable?
	cmd.Body = append(cmd.Body, external.Body...)

	return nil
}

func (e *Executor) Exec(commands []string) error {
	// Only run the default command when no specific commands are provided
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

	var waiter sync.WaitGroup
	errData := make(chan error)

	for _, cmdName := range commands {
		if cmdName[0] == '-' {
			continue
		}

		if e.concurrent {
			waiter.Add(1)
			go e.processCommand(cmdName, errData, &waiter)

			continue
		}

		if err := e.processCommand(cmdName, nil, nil); err != nil {
			return err
		}
	}

	if e.concurrent {
		go func() {
			waiter.Wait()
			close(errData)
		}()

		for data := range errData {
			if data != nil {
				return data
			}
		}
	}

	return nil
}

func (e *Executor) Execute(commands []string) error {
	return e.Exec(commands)
}

func (e *Executor) processCommand(name string, resp chan<- error, wg *sync.WaitGroup) error {
	if wg != nil {
		defer wg.Done()
	}

	command, err := e.StructuredParse.GetCommand(name)
	if err != nil {
		if e.concurrent {
			resp <- err
		} else {
			return err
		}
	}

	if err := e.EvaluateCommand(command); err != nil {
		if e.concurrent {
			resp <- err
		} else {
			return err
		}
	}

	return nil
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

// captureOutputWithDebug captures both stdout and stderr from a command.
// Both streams are read concurrently to prevent pipe buffer deadlocks.
func captureOutputWithDebug(cmd *exec.Cmd) (stdout, stderr []byte, err error) {
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

	// Read both streams concurrently to avoid deadlock
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

