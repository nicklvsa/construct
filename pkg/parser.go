package pkg

import (
	"errors"
	"fmt"
	"os"
	"strings"

	flag "github.com/spf13/pflag"
)

type ParsedData struct {
	Variables []*Variable `json:"variables"`
	Commands  []*Command  `json:"commands"`
}

func (p *ParsedData) GetVariable(variableName, scope string) (*Variable, error) {
	variableName = strings.ReplaceAll(variableName, `"`, "")

	if scope == "" {
		scope = "global"
	}

	for _, variable := range p.Variables {
		if variable.Name == variableName && variable.Scope == scope {
			return variable, nil
		}
	}

	for _, variable := range p.Variables {
		if variable.Name == variableName && variable.Scope == "global" {
			return variable, nil
		}
	}

	return nil, fmt.Errorf("cannot find variable with name %s", variableName)
}

func (p *ParsedData) GetCommand(commandName string) (*Command, error) {
	for _, command := range p.Commands {
		if command.Name == commandName {
			return command, nil
		}
	}

	return nil, fmt.Errorf("cannot find command with name %s", commandName)
}

func (p *ParsedData) GetDefaultCommand() (*Command, error) {
	for _, command := range p.Commands {
		if command.IsDefault {
			return command, nil
		}
	}

	return nil, errors.New("no default command")
}

type Parser struct {
	InputFile string      `json:"-"`
	Data      *ParsedData `json:"data"`
	Lines     []string    `json:"-"`
}

type Argument struct {
	Name       string `json:"name"`
	IsOptional bool   `json:"is_optional"`
}

type Variable struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	Scope string `json:"scope"`
}

type LazyOutput struct {
	VarName string `json:"var_name"`
	Scope   string `json:"scope"`
}

type Command struct {
	Name            string      `json:"name"`
	CloudAccessible bool        `json:"cloud_accessible"`
	IsDefault       bool        `json:"is_default"`
	LazyEval        *LazyOutput `json:"lazy_output"`
	IsPrereq        bool        `json:"is_prereq"`
	PrereqOutput    []string    `json:"prereq_output"`
	Arguments       []Argument  `json:"arguments"`
	Prereqs         []string    `json:"prereqs"`
	PrereqCmds      []*Command  `json:"prereq_cmds"`
	Body            []string    `json:"body"`
}

func NewParser(file string) *Parser {
	data, err := os.ReadFile(file)
	if err != nil {
		panic(err)
	}

	return &Parser{
		InputFile: file,
		Data:      &ParsedData{},
		Lines:     strings.Split(string(data), "\n"),
	}
}

func (p *Parser) findVariable(varName string) (*Variable, error) {
	for _, v := range p.Data.Variables {
		if v.Name == varName {
			return v, nil
		}
	}

	return nil, fmt.Errorf("cannot find %s", varName)
}

func (p *Parser) tryEvalExpression(expression string, varName *string, varScope *string) string {
	expression = strings.TrimSpace(expression)

	var output string
	for exprIdx, expr := range expression {
		if expr == '@' {
			output += os.Getenv(GetCharsUntilEnd(exprIdx, expression))
		}

		if expr == '&' {
			name := GetCharsUntilEnd(exprIdx, expression)

			if variable, err := p.findVariable(name); err == nil {
				output += variable.Value
			}
		}

		if expr == '$' && varName != nil && varScope != nil {
			data := GetCharsUntilEnd(exprIdx, expression)
			fmt.Println(expression)

			p.Data.Commands = append(p.Data.Commands, &Command{
				Name:            fmt.Sprintf("__lazy_%s_%s", *varName, *varScope),
				LazyEval:        &LazyOutput{VarName: *varName, Scope: *varScope},
				IsDefault:       false,
				CloudAccessible: false,
				Body:            []string{fmt.Sprintf("$ %s", data)},
			})
		}
	}

	// if expression[0] == '@' {
	// 	return os.Getenv(expression[1:])
	// }

	// if expression[0] == '&' {
	// 	if variable, err := p.findVariable(expression[1:]); err == nil {
	// 		return variable.Value
	// 	}
	// }

	if len(output) <= 0 {
		return expression
	}

	return output
}

func (p *Parser) parseVar(line string, scope string) error {
	pieces := strings.Split(line, "=")

	var variableValue string
	variableName := strings.TrimSpace(strings.Split(pieces[0], "var")[1])

	if len(pieces) > 1 {
		variableValue = p.tryEvalExpression(pieces[1], &variableName, &scope)
	}

	p.Data.Variables = append(p.Data.Variables, &Variable{
		Name:  variableName,
		Value: variableValue,
		Scope: scope,
	})

	return nil
}

func (p *Parser) parseCommand(idx int, line string, isDefault bool) error {
	var commandName string
	var prereqNames string
	var commandBody []string
	var commandArgs []Argument

	parseArgName := func(name string) (string, bool) {
		name = strings.TrimSpace(name)
		namePieces := strings.Split(name, " ")

		isOptional := false
		argumentName := namePieces[len(namePieces)-1]

		for _, arg := range namePieces[:len(namePieces)-1] {
			if arg == "opt" {
				isOptional = true
			}
		}

		return argumentName, isOptional
	}

	parseCmdName := func(cmdName string) string {
		var outputCmdName string

		for _, cmdChar := range cmdName {
			if cmdChar == '(' || cmdChar == '*' {
				break
			}

			outputCmdName += string(cmdChar)
		}

		return strings.TrimSpace(outputCmdName)
	}

	for chIdx, char := range line {
		if char == '{' {
			for _, nameChar := range line[:chIdx-1] {
				commandName += string(nameChar)
			}

			commandName = parseCmdName(commandName)

			start := idx + 1
			for !strings.ContainsRune(p.Lines[start], '}') {
				cmdLine := strings.TrimSpace(p.Lines[start])
				commandBody = append(commandBody, cmdLine)
				start++
			}

			break
		}

		if char == '(' {
			var argName string

			for argCharIdx, argChar := range line[chIdx+1:] {
				if argChar == ')' {
					// TODO: support non-argument constructs with prereqs
					for nextCharIdx, nextChar := range line[chIdx+1:][argCharIdx+1:] {
						if nextChar == '<' {
							updatedLine := line[chIdx+1:][argCharIdx+1:][nextCharIdx+1:]
							for _, namedNextChar := range updatedLine {
								if namedNextChar == '{' {
									break
								}

								prereqNames += string(namedNextChar)
							}
						}
					}

					argumentName, optional := parseArgName(argName)
					commandArgs = append(commandArgs, Argument{
						Name:       argumentName,
						IsOptional: optional,
					})
					break
				}

				if argChar == ',' {
					argumentName, optional := parseArgName(argName)
					commandArgs = append(commandArgs, Argument{
						Name:       argumentName,
						IsOptional: optional,
					})

					argName = ""
					continue
				}

				argName += string(argChar)
			}

			continue
		}
	}

	if commandName != "" && len(commandBody) > 0 {
		var updatedCommandBody []string
		prereqList := strings.Split(strings.TrimSpace(prereqNames), ",")

		for _, cmdLine := range commandBody {
			cmdLine = strings.TrimSpace(cmdLine)
			if strings.HasPrefix(cmdLine, "var") {
				if err := p.parseVar(cmdLine, commandName); err != nil {
					return err
				}

				continue
			}

			updatedCommandBody = append(updatedCommandBody, cmdLine)
		}

		for _, arg := range commandArgs {
			flagName := fmt.Sprintf("%s:%s", commandName, arg.Name)
			flag.String(flagName, "", flagName)
		}

		cloudAccessible := false
		if commandName[0] == '|' && commandName[len(commandName)-1] == '|' {
			cloudAccessible = true
			commandName = commandName[1 : len(commandName)-1]
		}

		p.Data.Commands = append(p.Data.Commands, &Command{
			IsDefault:       isDefault,
			IsPrereq:        false,
			PrereqOutput:    nil,
			PrereqCmds:      nil,
			Arguments:       commandArgs,
			Prereqs:         prereqList,
			Name:            commandName,
			CloudAccessible: cloudAccessible,
			Body:            updatedCommandBody,
		})
	}

	return nil
}

func (p *Parser) Parse() (*ParsedData, error) {
	for idx, line := range p.Lines {
		if strings.HasPrefix(line, "//") {
			continue
		}

		if strings.HasPrefix(line, "var") {
			if err := p.parseVar(line, "global"); err != nil {
				return nil, err
			}

			continue
		}

		if strings.HasPrefix(line, "_") {
			if err := p.parseCommand(idx, line, true); err != nil {
				return nil, err
			}

			continue
		}

		if err := p.parseCommand(idx, line, false); err != nil {
			return nil, err
		}
	}

	writeChan := make(chan error)

	debugWriter := func(signal chan<- error) {
		mermaid := DebugToMermaid(p.Data)
		if err := os.WriteFile("diagram.md", []byte(mermaid), 0644); err != nil {
			signal <- err
		}

		signal <- nil
	}

	go debugWriter(writeChan)
	if err := <-writeChan; err != nil {
		fmt.Println(err.Error())
	}

	return p.Data, nil
}
