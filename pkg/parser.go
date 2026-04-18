package pkg

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode"
)

type ParsedData struct {
	Variables []*Variable      `json:"variables"`
	Commands  []*Command       `json:"commands"`

	// Index maps for O(1) lookups, built after parsing
	variableMap map[string]*Variable // key: "scope.name"
	commandMap  map[string]*Command  // key: command name
}

// buildIndexMaps populates the lookup maps for O(1) access
func (p *ParsedData) buildIndexMaps() {
	p.variableMap = make(map[string]*Variable, len(p.Variables))
	p.commandMap = make(map[string]*Command, len(p.Commands))

	for _, v := range p.Variables {
		p.variableMap[v.Scope+"."+v.Name] = v
	}
	for _, cmd := range p.Commands {
		p.commandMap[cmd.Name] = cmd
	}
}

// AddVariable appends a variable and updates the index map
func (p *ParsedData) AddVariable(v *Variable) {
	p.Variables = append(p.Variables, v)
	if p.variableMap != nil {
		p.variableMap[v.Scope+"."+v.Name] = v
	}
}

func (p *ParsedData) GetVariable(variableName, scope string) (*Variable, error) {
	variableName = strings.ReplaceAll(variableName, `"`, "")

	if scope == "" {
		scope = "global"
	}

	// O(1) scoped lookup
	if p.variableMap != nil {
		if v, ok := p.variableMap[scope+"."+variableName]; ok {
			return v, nil
		}
		// Fallback to global scope
		if scope != "global" {
			if v, ok := p.variableMap["global."+variableName]; ok {
				return v, nil
			}
		}
		return nil, fmt.Errorf("cannot find variable with name %s", variableName)
	}

	// Fallback for when maps aren't built yet (during parsing)
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
	if p.commandMap != nil {
		if cmd, ok := p.commandMap[commandName]; ok {
			return cmd, nil
		}
		return nil, fmt.Errorf("cannot find command with name %s", commandName)
	}

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
	// First try to find a variable matching the requested scope
	if scope != nil && *scope != "" {
		for _, v := range p.Data.Variables {
			if v.Name == varName && v.Scope == *scope {
				return v, nil
			}
		}
	}

	// Fall back to global scope
	for _, v := range p.Data.Variables {
		if v.Name == varName && v.Scope == "global" {
			return v, nil
		}
	}

	return nil, fmt.Errorf("cannot find %s", varName)
}

func (p *Parser) tryEvalExpression(expression string, varName *string, varScope *string) string {
	expression = strings.TrimSpace(expression)

	var result strings.Builder
	runes := []rune(expression)
	i := 0
	for i < len(runes) {
		char := runes[i]

		if char == '@' {
			var envName strings.Builder
			j := i + 1
			for j < len(runes) {
				c := runes[j]
				if !unicode.IsLetter(c) && !unicode.IsDigit(c) && c != '_' {
					break
				}
				envName.WriteRune(c)
				j++
			}
			if envName.Len() > 0 {
				result.WriteString(os.Getenv(envName.String()))
				i = j
				continue
			}
		}

		if char == '&' {
			var refName strings.Builder
			j := i + 1
			for j < len(runes) {
				c := runes[j]
				if !unicode.IsLetter(c) && !unicode.IsDigit(c) && c != '_' {
					break
				}
				refName.WriteRune(c)
				j++
			}
			if refName.Len() > 0 {
				scope := "global"
				if varScope != nil && *varScope != "" {
					scope = *varScope
				}
				if variable, err := p.findVariable(refName.String(), &scope); err == nil {
					result.WriteString(variable.Value)
				}
				i = j
				continue
			}
		}

		if char == '$' && varName != nil && varScope != nil {
			restOfLine := strings.TrimSpace(string(runes[i+1:]))
			p.Data.Commands = append(p.Data.Commands, &Command{
				Name:            fmt.Sprintf("__lazy_%s_%s", *varName, *varScope),
				LazyEval:        &LazyOutput{VarName: *varName, Scope: *varScope},
				IsDefault:       false,
				CloudAccessible: false,
				Body:            []string{fmt.Sprintf("$ %s", restOfLine)},
			})
			i = len(runes)
			continue
		}

		result.WriteRune(char)
		i++
	}

	return result.String()
}

func (p *Parser) parseVar(line string, scope string) error {
	pieces := strings.SplitN(line, "=", 2)

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
//
//	"build {" -> "build"
//	"test (arg1, arg2) {" -> "test"
//	"|cloudcmd| {" -> "cloudcmd"
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
//
//	"arg1" -> ("arg1", false)
//	"opt arg2" -> ("arg2", true)
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
//
//	"arg1, opt arg2" -> [{arg1, false}, {arg2, true}]
//	"arg1" -> [{arg1, false}]
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
//
//	"build, test" -> ["build", "test"]
//	"build" -> ["build"]
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

	// Step 6: Create and register the command
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

func (p *Parser) detectCircularDependencies() error {
	visited := make(map[string]bool)
	inStack := make(map[string]bool)

	var visit func(cmdName string, path []string) error
	visit = func(cmdName string, path []string) error {
		if inStack[cmdName] {
			return &CircularDependencyError{
				Command: cmdName,
				Path:    append(path, cmdName),
			}
		}

		if visited[cmdName] {
			return nil
		}

		cmd, err := p.Data.GetCommand(cmdName)
		if err != nil {
			return nil
		}

		inStack[cmdName] = true
		path = append(path, cmdName)

		for _, prereq := range cmd.Prereqs {
			prereq = strings.TrimSpace(prereq)
			if prereq == "" {
				continue
			}
			if err := visit(prereq, path); err != nil {
				return err
			}
		}

		inStack[cmdName] = false
		visited[cmdName] = true
		return nil
	}

	for _, cmd := range p.Data.Commands {
		if err := visit(cmd.Name, []string{}); err != nil {
			return err
		}
	}

	return nil
}

func (p *Parser) validatePrerequisites() error {
	for _, cmd := range p.Data.Commands {
		for _, prereq := range cmd.Prereqs {
			prereq = strings.TrimSpace(prereq)
			if prereq == "" {
				continue
			}
			_, err := p.Data.GetCommand(prereq)
			if err != nil {
				return &MissingDependencyError{
					Command:    cmd.Name,
					PrereqName: prereq,
				}
			}
		}
	}
	return nil
}

// stripInlineComment removes inline comments (// or #) from a line,
// preserving comment markers inside quoted strings.
func stripInlineComment(line string) string {
	inQuote := false
	for i := 0; i < len(line); i++ {
		if line[i] == '"' {
			inQuote = !inQuote
			continue
		}
		if !inQuote && i+1 < len(line) && line[i] == '/' && line[i+1] == '/' {
			return strings.TrimSpace(line[:i])
		}
		if !inQuote && line[i] == '#' {
			return strings.TrimSpace(line[:i])
		}
	}
	return line
}

func (p *Parser) Parse() (*ParsedData, error) {
	for idx, line := range p.Lines {
		lineNum := idx + 1 // 1-indexed for error reporting

		line = stripInlineComment(line)
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "//") || strings.HasPrefix(line, "#") {
			continue
		}

		// "var" must be followed by a space to be a variable declaration
		// (avoids matching "variable", "var_x", etc.)
		if strings.HasPrefix(line, "var ") {
			if err := p.parseVar(line, "global"); err != nil {
				return nil, NewParseError(lineNum, 1, err.Error(), line)
			}

			continue
		}

		// "_" is the default command marker — must be followed by space, '(' , '<', or '{'
		if len(line) > 0 && line[0] == '_' && (len(line) == 1 || line[1] == ' ' || line[1] == '(' || line[1] == '<' || line[1] == '{') {
			if err := p.parseCommand(idx, line, true); err != nil {
				return nil, NewParseError(lineNum, 1, err.Error(), line)
			}

			continue
		}

		if err := p.parseCommand(idx, line, false); err != nil {
			return nil, NewParseError(lineNum, 1, err.Error(), line)
		}
	}

	if err := p.validatePrerequisites(); err != nil {
		return nil, err
	}

	if err := p.detectCircularDependencies(); err != nil {
		return nil, err
	}

	p.Data.buildIndexMaps()

	return p.Data, nil
}
