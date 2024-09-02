package pkg

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"unicode"

	flag "github.com/spf13/pflag"
)

type Executor struct {
	StructuredParse *ParsedData
	concurrent      bool
}

func NewExecutor(data *ParsedData, concurrent bool) *Executor {
	return &Executor{
		concurrent:      concurrent,
		StructuredParse: data,
	}
}

func (e *Executor) EvaluateCommand(command *Command) error {
	execCommandBody := func(execCmd *Command) error {
		for _, cmdLine := range execCmd.Body {
			if cmdLine == "" {
				continue
			}

			name, args := buildCommand(cmdLine)
			cmd := exec.Command(name, args...)
			output, err := cmd.Output()
			if err != nil {
				output = []byte(err.Error())
			}

			strOutput := string(output)

			if execCmd.IsPrereq {
				execCmd.PrereqOutput = append(execCmd.PrereqOutput, strOutput)
				continue
			}

			fmt.Println(strOutput)
		}

		return nil
	}

	cleanCommandBody := func(uncleanedCommand *Command) error {
		if len(uncleanedCommand.Prereqs) > 0 {
			for _, prereq := range uncleanedCommand.PrereqCmds {
				for idx, arg := range prereq.PrereqOutput {
					varName := fmt.Sprintf("%s.%d", prereq.Name, idx)
					e.StructuredParse.Variables = append(
						e.StructuredParse.Variables,
						Variable{
							Name:  strings.TrimSpace(varName),
							Value: strings.TrimSpace(arg),
							Scope: uncleanedCommand.Name,
						},
					)
				}
			}
		}

		for lineIdx, line := range uncleanedCommand.Body {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}

			if line[0] == '$' {
				executionLine := strings.TrimSpace(line[1:])
				linePieces := strings.Split(executionLine, " ")

				for pieceIdx, piece := range linePieces {
					piece = strings.TrimSpace(piece)
					for pieceCharIdx, pieceChar := range piece {
						if pieceChar == '&' {
							variable, err := e.StructuredParse.GetVariable(piece[pieceCharIdx+1:], uncleanedCommand.Name)
							if err == nil && variable != nil {
								linePieces[pieceIdx] = variable.Value
								continue
							}
						}
					}

					// for now, arguments can ONLY be used in $ expressions
					for _, arg := range uncleanedCommand.Arguments {
						pieceFlagName := fmt.Sprintf("%s:%s", uncleanedCommand.Name, piece)
						argFlagName := fmt.Sprintf("%s:%s", uncleanedCommand.Name, arg.Name)
						if pieceFlagName == argFlagName {
							v, err := flag.CommandLine.GetString(argFlagName)
							if err != nil || v == "" {
								if !arg.IsOptional {
									return fmt.Errorf("%s is not optional", argFlagName)
								}
							}

							linePieces[pieceIdx] = v
						}
					}
				}

				uncleanedCommand.Body[lineIdx] = strings.Join(linePieces, " ")
			}

			for bodyIdx, bodyChar := range line {
				if bodyChar == '&' {
					var varName string
					for _, varRef := range line[bodyIdx+1:] {
						if !unicode.IsLetter(varRef) {
							break
						}

						varName += string(varRef)
					}

					varDef, err := e.StructuredParse.GetVariable(varName, uncleanedCommand.Name)
					if err == nil && varDef != nil {
						uncleanedCommand.Body[lineIdx] = strings.Replace(uncleanedCommand.Body[lineIdx], fmt.Sprintf("&%s", varName), varDef.Value, 1)
					}
				}
			}
		}

		return nil
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

		preCmd.IsPrereq = true
		preCmd.PrereqOutput = []string{}

		if err := e.tryApplyCloudBody(preCmd); err != nil {
			return err
		}

		if err := cleanCommandBody(preCmd); err != nil {
			return err
		}

		if err := execCommandBody(preCmd); err != nil {
			return err
		}

		command.PrereqCmds = append(command.PrereqCmds, preCmd)
	}

	if err := e.tryApplyCloudBody(command); err != nil {
		return err
	}

	if err := cleanCommandBody(command); err != nil {
		return err
	}

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
	defaultCommand, err := e.StructuredParse.GetDefaultCommand()
	if err == nil && defaultCommand != nil {
		if err := e.EvaluateCommand(defaultCommand); err != nil {
			return err
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

func (e Executor) getCloudDefinition(name string) (*Command, error) {
	fileBytes, err := os.ReadFile("fakecloud.json")
	if err != nil {
		return nil, err
	}

	var commands map[string]Command
	if err := json.Unmarshal(fileBytes, &commands); err != nil {
		return nil, err
	}

	if c, ok := commands[name]; ok {
		return &c, nil
	}

	return nil, fmt.Errorf("%s command not found in cloud", name)
}

func buildCommand(cmd string) (string, []string) {
	cmdPrefix := [3]string{"/bin/bash", "-c", cmd}
	if runtime.GOOS == "windows" {
		cmdPrefix = [3]string{"cmd", "/c", cmd}
	}

	return cmdPrefix[0], cmdPrefix[1:]
}
