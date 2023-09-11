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
	for lineIdx, line := range command.Body {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		if line[0] == '$' {
			executionLine := strings.TrimSpace(line[1:])
			linePieces := strings.Split(executionLine, " ")

			for pieceIdx, piece := range linePieces {
				piece = strings.TrimSpace(piece)
				variable, err := e.StructuredParse.GetVariable(piece, command.Name)
				if err == nil && variable != nil {
					linePieces[pieceIdx] = variable.Value
					continue
				}

				for _, arg := range command.Arguments {
					pieceFlagName := fmt.Sprintf("%s:%s", command.Name, piece)
					argFlagName := fmt.Sprintf("%s:%s", command.Name, arg.Name)
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

			command.Body[lineIdx] = strings.Join(linePieces, " ")
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

				varDef, err := e.StructuredParse.GetVariable(varName, command.Name)
				if err == nil && varDef != nil {
					command.Body[lineIdx] = strings.Replace(command.Body[lineIdx], fmt.Sprintf("&%s", varName), varDef.Value, 1)
				}
			}
		}
	}

	for _, cmdLine := range command.Body {
		if cmdLine == "" {
			continue
		}

		name, args := buildCommand(cmdLine)
		cmd := exec.Command(name, args...)
		output, err := cmd.Output()
		if err != nil {
			return err
		}

		fmt.Println(string(output))
	}

	return nil
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
