package pkg

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"unicode"

	flag "github.com/spf13/pflag"
)

type Executor struct {
	StructuredParse *ParsedData
}

func NewExecutor(data *ParsedData) *Executor {
	return &Executor{
		StructuredParse: data,
	}
}

func (e *Executor) EvaluateCommand(command *Command) error {
	execCommandBody := func(body []string) error {
		for _, cmdLine := range body {
			if cmdLine == "" {
				continue
			}

			name, args := buildCommand(cmdLine)
			cmd := exec.Command(name, args...)
			output, err := cmd.Output()
			if err != nil {
				return err
			}

			strOutput := string(output)

			if command.IsPrereq {
				fmt.Printf("command %s is a prererq\n", command.Name)
				command.PrereqOutput = append(command.PrereqOutput, strOutput)
				continue
			}

			fmt.Println(strOutput)
		}

		return nil
	}

	cleanCommandBody := func(uncleanedCommand *Command) error {
		if uncleanedCommand.PrereqCmds != nil && !uncleanedCommand.IsPrereq {
			// TODO
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
					if piece[0] == '&' {
						variable, err := e.StructuredParse.GetVariable(piece[1:], uncleanedCommand.Name)
						if err == nil && variable != nil {
							linePieces[pieceIdx] = variable.Value
							continue
						}
					}

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
		command.PrereqCmds = append(command.PrereqCmds, preCmd)

		if err := cleanCommandBody(preCmd); err != nil {
			return err
		}

		if err := execCommandBody(preCmd.Body); err != nil {
			return err
		}
	}

	if err := cleanCommandBody(command); err != nil {
		return err
	}

	return execCommandBody(command.Body)
}

func (e *Executor) Exec(commands []string) error {
	defaultCommand, err := e.StructuredParse.GetDefaultCommand()
	if err == nil && defaultCommand != nil {
		if err := e.EvaluateCommand(defaultCommand); err != nil {
			return err
		}
	}

	for _, cmdName := range commands {
		if cmdName[0] == '-' {
			continue
		}

		command, err := e.StructuredParse.GetCommand(cmdName)
		if err != nil {
			return err
		}

		if err := e.EvaluateCommand(command); err != nil {
			return err
		}
	}

	return nil
}

func buildCommand(cmd string) (string, []string) {
	cmdPrefix := []string{"/bin/bash", "-c", cmd}
	if runtime.GOOS == "windows" {
		cmdPrefix = []string{"cmd", "/c", cmd}
	}

	return cmdPrefix[0], cmdPrefix[1:]
}
