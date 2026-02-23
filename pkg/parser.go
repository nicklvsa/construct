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
	Arguments       []*Argument `json:"arguments"`
	Prereqs         []string    `json:"prereqs"`
	PrereqCmds      []*Command  `json:"prereq_cmds"`
	Body            []string    `json:"body"`
}

func NewParser(file string) (*Parser, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("failed to read file '%s': %w", file, err)
	}

	return &Parser{
		InputFile: file,
		Data:      &ParsedData{},
		Lines:     strings.Split(string(data), "\n"),
	}, nil
}

func (p *Parser) findVariable(varName string, scope *string) (*Variable, error) {
	for _, v := range p.Data.Variables {
		if v.Name == varName {
			if scope != nil && v.Scope == *scope {
				return v, nil
			}

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

			if variable, err := p.findVariable(name, varScope); err == nil {
				output += variable.Value
			}
		}

		if expr == '$' && varName != nil && varScope != nil {
			data := GetCharsUntilEnd(exprIdx, expression)

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

// =============================================================================
// Helper Functions for Command Parsing
// These functions extract specific parts of a command declaration
// Each function is pure, testable, and has a single responsibility
// =============================================================================

// parseCommandName extracts the command name from a command declaration line
// Examples:
//   "build {" -> "build"
//   "test (arg1, arg2) {" -> "test"
//   "|cloudcmd| {" -> "cloudcmd"
func parseCommandName(line string) string {
	line = strings.TrimSpace(line)

	// Handle cloud command markers: |commandname|
	if len(line) >= 2 && line[0] == '|' {
		// Find the second pipe
		endIdx := strings.Index(line[1:], "|")
		if endIdx > 0 {
			// Extract name between pipes
			name := line[1 : endIdx+1]
			// Check if followed by command syntax
			remainder := strings.TrimSpace(line[endIdx+2:])
			if strings.HasPrefix(remainder, "(") || strings.HasPrefix(remainder, "<") || strings.HasPrefix(remainder, "{") {
				return strings.TrimSpace(name)
			}
		}
	}

	// Find end of command name (stops at '(', '<', or '{')
	for i, r := range line {
		if r == '(' || r == '<' || r == '{' {
			return strings.TrimSpace(line[:i])
		}
	}

	return strings.TrimSpace(line)
}

// extractArgumentString extracts the argument string from a command line
// Example: "run (arg1, arg2) < prereq {" -> "arg1, arg2"
func extractArgumentString(line string) string {
	start := strings.Index(line, "(")
	if start == -1 {
		return ""
	}

	end := strings.Index(line[start:], ")")
	if end == -1 {
		return ""
	}

	return strings.TrimSpace(line[start+1 : start+end])
}

// extractPrerequisiteString extracts the prerequisite string from a command line
// Example: "run (arg1) < build, test {" -> "build, test"
func extractPrerequisiteString(line string) string {
	start := strings.Index(line, "<")
	if start == -1 {
		return ""
	}

	// Find the opening brace to know where prereqs end
	end := strings.Index(line[start:], "{")
	if end == -1 {
		return ""
	}

	return strings.TrimSpace(line[start+1 : start+end])
}

// parseArgumentName parses a single argument name and determines if it's optional
// Examples:
//   "arg1" -> ("arg1", false)
//   "opt arg2" -> ("arg2", true)
func parseArgumentName(argStr string) (string, bool) {
	argStr = strings.TrimSpace(argStr)
	if argStr == "" {
		return "", false
	}

	// Check for "opt" keyword
	parts := strings.Fields(argStr)
	if len(parts) == 0 {
		return "", false
	}

	// Last part is always the argument name
	argName := parts[len(parts)-1]

	// Check if any part before the name is "opt"
	isOptional := false
	for _, part := range parts[:len(parts)-1] {
		if part == "opt" {
			isOptional = true
			break
		}
	}

	return argName, isOptional
}

// parseArgumentList parses the argument string into Argument structs
// Examples:
//   "arg1, opt arg2" -> [{arg1, false}, {arg2, true}]
//   "arg1" -> [{arg1, false}]
func parseArgumentList(argStr string) ([]*Argument, error) {
	argStr = strings.TrimSpace(argStr)
	if argStr == "" {
		return nil, nil
	}

	args := []*Argument{}
	parts := strings.Split(argStr, ",")

	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		argName, isOptional := parseArgumentName(part)
		if argName == "" {
			return nil, fmt.Errorf("invalid argument syntax: '%s'", part)
		}

		args = append(args, &Argument{
			Name:       argName,
			IsOptional: isOptional,
		})
	}

	return args, nil
}

// parsePrerequisiteList parses the prerequisite string into a list of prereq names
// Examples:
//   "build, test" -> ["build", "test"]
//   "build" -> ["build"]
func parsePrerequisiteList(prereqStr string) ([]string, error) {
	prereqStr = strings.TrimSpace(prereqStr)
	if prereqStr == "" {
		return nil, nil
	}

	parts := strings.Split(prereqStr, ",")
	prereqs := []string{}

	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			prereqs = append(prereqs, part)
		}
	}

	return prereqs, nil
}

// parseCommandBody extracts the command body starting from the given line index
// Returns the body lines and the index of the line after the closing brace
func (p *Parser) parseCommandBody(startIdx int, commandName string) ([]string, int, error) {
	var body []string

	for i := startIdx; i < len(p.Lines); i++ {
		line := p.Lines[i]

		// Check for closing brace BEFORE trimming
		if strings.Contains(line, "}") {
			// Found the end of the body - return what we have so far
			return body, i + 1, nil
		}

		// Trim and check if empty
		trimmedLine := strings.TrimSpace(line)
		if trimmedLine == "" {
			continue
		}

		body = append(body, trimmedLine)
	}

	return nil, startIdx, fmt.Errorf("unclosed command body for '%s' (missing '}')", commandName)
}

// =============================================================================
// Refactored parseCommand - Clean and readable!
// =============================================================================

func (p *Parser) parseCommand(idx int, line string, isDefault bool) error {
	// Check if this line actually contains a command body
	// If not, just skip it (it might be a line with just a command name)
	trimmedLine := strings.TrimSpace(line)
	if !strings.Contains(trimmedLine, "{") {
		// No command body, skip
		return nil
	}

	// Step 1: Extract the command name (parseCommandName handles cloud markers)
	commandName := parseCommandName(line)

	// Determine if this is a cloud command
	// We check the original line for pipe markers
	cloudAccessible := false
	if len(trimmedLine) >= 2 && trimmedLine[0] == '|' {
		// This is a cloud command
		cloudAccessible = true
	}

	// Step 2: Parse arguments
	argStr := extractArgumentString(line)
	commandArgs, err := parseArgumentList(argStr)
	if err != nil {
		return fmt.Errorf("failed to parse arguments for '%s': %w", commandName, err)
	}

	// Step 3: Parse prerequisites
	prereqStr := extractPrerequisiteString(line)
	prereqs, err := parsePrerequisiteList(prereqStr)
	if err != nil {
		return fmt.Errorf("failed to parse prerequisites for '%s': %w", commandName, err)
	}

	// Step 4: Parse body
	rawBody, _, err := p.parseCommandBody(idx+1, commandName)
	if err != nil {
		return err
	}

	// Step 5: Process body (handle local variables)
	var commandBody []string
	for _, cmdLine := range rawBody {
		cmdLine = strings.TrimSpace(cmdLine)
		if strings.HasPrefix(cmdLine, "var") {
			if err := p.parseVar(cmdLine, commandName); err != nil {
				return err
			}
			continue
		}
		commandBody = append(commandBody, cmdLine)
	}

	// Step 6: Register flags for arguments
	for _, arg := range commandArgs {
		flagName := fmt.Sprintf("%s:%s", commandName, arg.Name)
		flag.String(flagName, "", flagName)
	}

	// Step 7: Create and register the command
	if commandName != "" && len(commandBody) > 0 {
		p.Data.Commands = append(p.Data.Commands, &Command{
			Name:            commandName,
			CloudAccessible: cloudAccessible,
			IsDefault:       isDefault,
			IsPrereq:        false,
			Arguments:       commandArgs,
			Prereqs:         prereqs,
			Body:            commandBody,
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
