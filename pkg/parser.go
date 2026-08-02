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

// BodyStatement is a single node in a command body: either a shell line or an
// if/else block. The executor walks this tree rather than a flat string list.
type BodyStatement struct {
	Type       string          `json:"type"` // "shell" or "if"
	Shell      string          `json:"shell,omitempty"`
	OutputName string          `json:"output_name,omitempty"`
	Cond       string          `json:"cond,omitempty"`
	ThenBody   []BodyStatement `json:"then,omitempty"`
	ElseBody   []BodyStatement `json:"else,omitempty"`
}

type Command struct {
	Name            string            `json:"name"`
	CloudAccessible bool              `json:"cloud_accessible"`
	IsDefault       bool              `json:"is_default"`
	LazyEval        *LazyOutput       `json:"lazy_output"`
	IsPrereq        bool              `json:"is_prereq"`
	PrereqOutput    []string          `json:"prereq_output"`
	NamedOutput     map[string]string `json:"named_output"`
	Arguments       []*Argument       `json:"arguments"`
	Prereqs         []string          `json:"prereqs"`
	PrereqCmds      []*Command        `json:"prereq_cmds"`
	WorkDir         string            `json:"work_dir"`
	Body            []BodyStatement   `json:"body"`
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
				Body:     []BodyStatement{{Type: "shell", Shell: fmt.Sprintf("$ %s", restOfLine)}},
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

	// Find the earliest terminator: (, <, {, or " in " workdir modifier.
	inIdx := strings.Index(line, " in ")
	terminators := []rune{'(', '<', '{'}
	endIdx := len(line)
	for i, r := range line {
		found := false
		for _, t := range terminators {
			if r == t {
				found = true
				break
			}
		}
		if found {
			endIdx = i
			break
		}
	}
	if inIdx >= 0 && inIdx < endIdx {
		endIdx = inIdx
	}
	return strings.TrimSpace(line[:endIdx])
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

	segment := line[start+1 : start+end]

	// The segment may contain prereqs, an " in <dir>" modifier, and more prereqs
	// after the workdir. Split on commas, remove the workdir token and "in"
	// keyword, and keep the rest as prereq names.
	parts := strings.Split(segment, ",")
	var prereqs []string
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" || part == "in" {
			continue
		}
		// Skip path-like tokens (the workdir directory).
		if strings.ContainsAny(part, "/\\") {
			continue
		}
		// Skip "in <path>" combined tokens.
		if strings.HasPrefix(part, "in ") {
			continue
		}
		prereqs = append(prereqs, part)
	}

	return strings.Join(prereqs, ", ")
}

// extractWorkDir finds the " in <dir>" modifier that sits between the end of
// the arguments/prereqs and the opening brace. Returns "" if absent.
func extractWorkDir(line string) string {
	brace := strings.Index(line, "{")
	if brace == -1 {
		return ""
	}

	segment := line[:brace]
	idx := strings.LastIndex(segment, " in ")
	if idx == -1 {
		return ""
	}

	dir := strings.TrimSpace(segment[idx+4:])
	// The workdir ends at the first comma — anything after is a trailing prereq.
	if comma := strings.IndexByte(dir, ','); comma >= 0 {
		dir = strings.TrimSpace(dir[:comma])
	}
	return dir
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

// parseCommandBody collects raw body lines until the closing brace, tracking
// nesting depth so if/else block braces are not mistaken for the command's
// closing brace.
func (p *Parser) parseCommandBody(startIdx int, commandName string) ([]string, int, error) {
	var body []string
	depth := 0

	for i := startIdx; i < len(p.Lines); i++ {
		line := p.Lines[i]
		trimmedLine := strings.TrimSpace(line)

		// Count opening braces to detect if-headers.
		opens := strings.Count(trimmedLine, "{")

		// An "if" line with a brace opens a new block.
		isIfHeader := strings.HasPrefix(trimmedLine, "if ") && opens > 0
		isElseCompound := strings.HasPrefix(trimmedLine, "}") && strings.Contains(trimmedLine, "else")

		if isElseCompound {
			// "} else {" closes the then-block (depth--) and opens else-block (depth++)
			if depth > 0 {
				depth-- // close then-block
			}
			depth++ // open else-block
			body = append(body, trimmedLine)
			continue
		}

		if strings.HasPrefix(trimmedLine, "}") {
			if depth > 0 {
				depth--
				body = append(body, trimmedLine)
				continue
			}
			// This is the command's own closing brace.
			return body, i + 1, nil
		}

		if isIfHeader {
			depth++
		}

		if trimmedLine == "" {
			continue
		}

		body = append(body, trimmedLine)
	}

	return nil, startIdx, fmt.Errorf("unclosed command body for '%s' (missing '}')", commandName)
}

// parseBodyStatements builds a statement tree from raw body lines, recognizing
// if/else blocks. Local "var" declarations are extracted out (the caller passes
// the command scope so they're registered correctly) and removed from the tree.
func (p *Parser) parseBodyStatements(raw []string, scope string) ([]BodyStatement, error) {
	var stmts []BodyStatement
	i := 0
	for i < len(raw) {
		line := raw[i]

		// Local variable declarations are extracted, not executed.
		if strings.HasPrefix(line, "var ") || strings.HasPrefix(line, "var\t") {
			if err := p.parseVar(line, scope); err != nil {
				return nil, err
			}
			i++
			continue
		}

		// if "<cond>" { ... } else { ... }
		if strings.HasPrefix(line, "if ") || line == "if{" {
			stmt, consumed, err := p.parseIfBlock(raw[i:], scope)
			if err != nil {
				return nil, err
			}
			stmts = append(stmts, stmt)
			i += consumed
			continue
		}

		// Extract "as <name>" output tag if present.
		shell, outputName := extractOutputName(line)
		stmts = append(stmts, BodyStatement{Type: "shell", Shell: shell, OutputName: outputName})
		i++
	}
	return stmts, nil
}

// extractOutputName splits a trailing " as <name>" from a shell line, returning
// the cleaned shell text and the output name ("" if none).
func extractOutputName(line string) (shell, name string) {
	// Match " as <identifier>" at end of line. Avoid matching inside quotes.
	idx := strings.LastIndex(line, " as ")
	if idx < 0 {
		return line, ""
	}
	suffix := strings.TrimSpace(line[idx+4:])
	// The name must be a bare identifier (no spaces, quotes, or special chars).
	if suffix == "" || !isValidIdent(suffix) {
		return line, ""
	}
	// Don't treat " as " inside a quoted string as an output tag.
	// Simple heuristic: if there's an odd number of quotes before the " as ",
	// we're inside a string.
	before := line[:idx]
	if strings.Count(before, `"`)%2 != 0 {
		return line, ""
	}
	return strings.TrimSpace(before), suffix
}

func isValidIdent(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}

// parseIfBlock parses an if/else block starting at lines[0]. Returns the
// IfBlock statement and the number of raw lines consumed.
func (p *Parser) parseIfBlock(raw []string, scope string) (BodyStatement, int, error) {
	header := raw[0]
	cond := extractIfCondition(header)

	// Collect the then-body until we hit a line starting with '}' or 'else'.
	var thenLines []string
	consumed := 1
	for consumed < len(raw) {
		l := raw[consumed]
		trimmed := strings.TrimSpace(l)
		if trimmed == "}" || strings.HasPrefix(trimmed, "}") {
			break
		}
		if trimmed == "else" || strings.HasPrefix(trimmed, "else ") || strings.HasPrefix(trimmed, "else{") {
			break
		}
		thenLines = append(thenLines, l)
		consumed++
	}

	if consumed >= len(raw) {
		return BodyStatement{}, 0, fmt.Errorf("unclosed if block (missing '}')")
	}

	thenStmts, err := p.parseBodyStatements(thenLines, scope)
	if err != nil {
		return BodyStatement{}, 0, err
	}

	stmt := BodyStatement{
		Type:     "if",
		Cond:     cond,
		ThenBody: thenStmts,
	}

	// Check for else. The closing line may be "}", "} else {", or "else".
	closingLine := strings.TrimSpace(raw[consumed])
	hasElse := strings.Contains(closingLine, "else")

	if hasElse {
		// The else-body starts on the same line as "} else {" — no header to
		// consume separately. Just gather lines until the closing "}".
		consumed++ // consume the "} else {" line
		var elseLines []string
		for consumed < len(raw) {
			l := raw[consumed]
			trimmed := strings.TrimSpace(l)
			if trimmed == "}" || strings.HasPrefix(trimmed, "}") {
				break
			}
			elseLines = append(elseLines, l)
			consumed++
		}
		if consumed >= len(raw) {
			return BodyStatement{}, 0, fmt.Errorf("unclosed else block (missing '}')")
		}
		elseStmts, err := p.parseBodyStatements(elseLines, scope)
		if err != nil {
			return BodyStatement{}, 0, err
		}
		stmt.ElseBody = elseStmts
		consumed++ // consume the closing '}'
	} else {
		consumed++ // consume the closing '}'
	}

	return stmt, consumed, nil
}

// extractIfCondition pulls the quoted condition out of an "if ..." header line.
// Supports:  if "a" == "b" {  →  `"a" == "b"`
func extractIfCondition(line string) string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "if")
	line = strings.TrimSpace(line)

	// Strip a trailing "{" and whitespace.
	if brace := strings.Index(line, "{"); brace >= 0 {
		line = strings.TrimSpace(line[:brace])
	}
	return line
}

func (p *Parser) parseCommand(idx int, line string, isDefault bool) (int, error) {
	trimmedLine := strings.TrimSpace(line)
	if !strings.Contains(trimmedLine, "{") {
		return 0, nil
	}

	commandName := parseCommandName(line)

	cloudAccessible := len(trimmedLine) >= 2 && trimmedLine[0] == '|'

	commandArgs, err := parseArgumentList(extractArgumentString(line))
	if err != nil {
		return 0, fmt.Errorf("failed to parse arguments for '%s': %w", commandName, err)
	}

	prereqs, err := parsePrerequisiteList(extractPrerequisiteString(line))
	if err != nil {
		return 0, fmt.Errorf("failed to parse prerequisites for '%s': %w", commandName, err)
	}

	workDir := extractWorkDir(line)

	rawBody, endIdx, err := p.parseCommandBody(idx+1, commandName)
	if err != nil {
		return 0, err
	}

	commandBody, err := p.parseBodyStatements(rawBody, commandName)
	if err != nil {
		return 0, err
	}

	if commandName != "" && len(commandBody) > 0 {
		p.Data.Commands = append(p.Data.Commands, &Command{
			Name:            commandName,
			CloudAccessible: cloudAccessible,
			IsDefault:       isDefault,
			Arguments:       commandArgs,
			Prereqs:         prereqs,
			WorkDir:         workDir,
			Body:            commandBody,
		})
	}

	// Return how many lines were consumed (header + body lines).
	return endIdx - idx, nil
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
	idx := 0
	for idx < len(p.Lines) {
		line := p.Lines[idx]
		lineNum := idx + 1

		line = stripInlineComment(line)
		if line == "" {
			idx++
			continue
		}

		if strings.HasPrefix(line, "//") || strings.HasPrefix(line, "#") {
			idx++
			continue
		}

		if strings.HasPrefix(line, "var ") {
			if err := p.parseVar(line, "global"); err != nil {
				return nil, NewParseError(lineNum, 1, err.Error(), line)
			}
			idx++
			continue
		}

		if len(line) > 0 && line[0] == '_' && (len(line) == 1 || line[1] == ' ' || line[1] == '(' || line[1] == '<' || line[1] == '{') {
			consumed, err := p.parseCommand(idx, line, true)
			if err != nil {
				return nil, NewParseError(lineNum, 1, err.Error(), line)
			}
			idx += consumed
			if consumed == 0 {
				idx++
			}
			continue
		}

		consumed, err := p.parseCommand(idx, line, false)
		if err != nil {
			return nil, NewParseError(lineNum, 1, err.Error(), line)
		}
		idx += consumed
		if consumed == 0 {
			idx++
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
