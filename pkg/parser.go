package pkg

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode"
)

type ParsedData struct {
	Variables []*Variable `json:"variables"`
	Commands  []*Command  `json:"commands"`

	variableMap map[string]*Variable // key: "scope.name"
	commandMap  map[string]*Command  // key: command name
}

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

	if p.variableMap != nil {
		if v, ok := p.variableMap[scope+"."+variableName]; ok {
			return v, nil
		}
		if scope != "global" {
			if v, ok := p.variableMap["global."+variableName]; ok {
				return v, nil
			}
		}
		return nil, fmt.Errorf("cannot find variable with name %s", variableName)
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
	InputFile string
	Data      *ParsedData
	Lines     []string
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

	return NewParserFromContent(file, string(data)), nil
}

// NewParserFromContent builds a Parser from in-memory content. Used by the
// language server to parse live document state without touching disk.
func NewParserFromContent(file, content string) *Parser {
	return &Parser{
		InputFile: file,
		Data:      &ParsedData{},
		Lines:     strings.Split(content, "\n"),
	}
}

func (p *Parser) findVariable(varName string, scope *string) (*Variable, error) {
	if scope != nil && *scope != "" {
		for _, v := range p.Data.Variables {
			if v.Name == varName && v.Scope == *scope {
				return v, nil
			}
		}
	}

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

		// A `$` in a variable definition creates a lazy command: the variable's
		// value is computed by running the command at execution time.
		if char == '$' && varName != nil && varScope != nil {
			restOfLine := strings.TrimSpace(string(runes[i+1:]))
			p.Data.Commands = append(p.Data.Commands, &Command{
				Name:     fmt.Sprintf("__lazy_%s_%s", *varName, *varScope),
				LazyEval: &LazyOutput{VarName: *varName, Scope: *varScope},
				Body:     []string{fmt.Sprintf("$ %s", restOfLine)},
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
	if len(pieces) == 0 {
		return fmt.Errorf("invalid variable declaration: %q", line)
	}

	// Name is everything after the "var" keyword. TrimPrefix is robust against
	// names that happen to contain "var" (e.g. "var var_name = x").
	variableName := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(pieces[0]), "var"))
	if variableName == "" {
		return fmt.Errorf("variable declaration is missing a name: %q", line)
	}

	var variableValue string
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

func parseCommandName(line string) string {
	line = strings.TrimSpace(line)

	// Cloud command markers: |commandname|
	if len(line) >= 2 && line[0] == '|' {
		endIdx := strings.Index(line[1:], "|")
		if endIdx > 0 {
			name := line[1 : endIdx+1]
			remainder := strings.TrimSpace(line[endIdx+2:])
			if strings.HasPrefix(remainder, "(") || strings.HasPrefix(remainder, "<") || strings.HasPrefix(remainder, "{") {
				return strings.TrimSpace(name)
			}
		}
	}

	for i, r := range line {
		if r == '(' || r == '<' || r == '{' {
			return strings.TrimSpace(line[:i])
		}
	}

	return strings.TrimSpace(line)
}

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

func extractPrerequisiteString(line string) string {
	start := strings.Index(line, "<")
	if start == -1 {
		return ""
	}

	end := strings.Index(line[start:], "{")
	if end == -1 {
		return ""
	}

	return strings.TrimSpace(line[start+1 : start+end])
}

func parseArgumentName(argStr string) (string, bool) {
	argStr = strings.TrimSpace(argStr)
	if argStr == "" {
		return "", false
	}

	parts := strings.Fields(argStr)
	if len(parts) == 0 {
		return "", false
	}

	argName := parts[len(parts)-1]

	isOptional := false
	for _, part := range parts[:len(parts)-1] {
		if part == "opt" {
			isOptional = true
			break
		}
	}

	return argName, isOptional
}

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

func (p *Parser) parseCommandBody(startIdx int, commandName string) ([]string, int, error) {
	var body []string

	for i := startIdx; i < len(p.Lines); i++ {
		line := p.Lines[i]
		trimmedLine := strings.TrimSpace(line)

		// A closing brace only ends the block when it begins the trimmed line,
		// so braces inside shell commands (e.g. `awk '{print $1}'`) are preserved.
		if strings.HasPrefix(trimmedLine, "}") {
			return body, i + 1, nil
		}

		if trimmedLine == "" {
			continue
		}

		body = append(body, trimmedLine)
	}

	return nil, startIdx, fmt.Errorf("unclosed command body for '%s' (missing '}')", commandName)
}

func (p *Parser) parseCommand(idx int, line string, isDefault bool) error {
	trimmedLine := strings.TrimSpace(line)
	if !strings.Contains(trimmedLine, "{") {
		return nil
	}

	commandName := parseCommandName(line)

	cloudAccessible := len(trimmedLine) >= 2 && trimmedLine[0] == '|'

	commandArgs, err := parseArgumentList(extractArgumentString(line))
	if err != nil {
		return fmt.Errorf("failed to parse arguments for '%s': %w", commandName, err)
	}

	prereqs, err := parsePrerequisiteList(extractPrerequisiteString(line))
	if err != nil {
		return fmt.Errorf("failed to parse prerequisites for '%s': %w", commandName, err)
	}

	rawBody, _, err := p.parseCommandBody(idx+1, commandName)
	if err != nil {
		return err
	}

	// Local variable declarations ("var ...") inside a body are extracted into
	// command scope and removed from the executable body.
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

	if commandName != "" && len(commandBody) > 0 {
		p.Data.Commands = append(p.Data.Commands, &Command{
			Name:            commandName,
			CloudAccessible: cloudAccessible,
			IsDefault:       isDefault,
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

// stripInlineComment removes inline comments (// or #), preserving comment
// markers inside quoted strings and honouring backslash escapes.
func stripInlineComment(line string) string {
	inQuote := false
	for i := 0; i < len(line); i++ {
		c := line[i]

		if c == '\\' && inQuote && i+1 < len(line) {
			i++ // skip the escaped character
			continue
		}

		if c == '"' {
			inQuote = !inQuote
			continue
		}
		if !inQuote && i+1 < len(line) && c == '/' && line[i+1] == '/' {
			return strings.TrimSpace(line[:i])
		}
		if !inQuote && c == '#' {
			return strings.TrimSpace(line[:i])
		}
	}
	return line
}

func (p *Parser) Parse() (*ParsedData, error) {
	for idx, line := range p.Lines {
		lineNum := idx + 1

		line = stripInlineComment(line)
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "//") || strings.HasPrefix(line, "#") {
			continue
		}

		// "var " (with the trailing space) marks a global variable declaration.
		if strings.HasPrefix(line, "var ") {
			if err := p.parseVar(line, "global"); err != nil {
				return nil, NewParseError(lineNum, 1, err.Error(), line)
			}
			continue
		}

		// "_" marks the default command.
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
