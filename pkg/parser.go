package pkg

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"unicode"
)

type ParsedData struct {
	Variables []*Variable `json:"variables"`
	Commands  []*Command  `json:"commands"`

	variableMap map[string]*Variable // key: "scope.name"
	commandMap  map[string]*Command  // key: command name

	mu sync.RWMutex
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

func (p *ParsedData) SetVariable(name, scope, value string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.variableMap != nil {
		key := scope + "." + name
		if v, ok := p.variableMap[key]; ok {
			v.Value = value
			return
		}
		v := &Variable{Name: name, Value: value, Scope: scope}
		p.Variables = append(p.Variables, v)
		p.variableMap[key] = v
		return
	}

	for _, v := range p.Variables {
		if v.Name == name && v.Scope == scope {
			v.Value = value
			return
		}
	}
	p.Variables = append(p.Variables, &Variable{Name: name, Value: value, Scope: scope})
}

func (p *ParsedData) LookupVariable(name, scope string) (string, bool) {
	if strings.IndexByte(name, '"') >= 0 {
		name = strings.ReplaceAll(name, `"`, "")
	}
	if scope == "" {
		scope = "global"
	}

	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.variableMap != nil {
		if v, ok := p.variableMap[scope+"."+name]; ok {
			return v.Value, true
		}
		if scope != "global" {
			if v, ok := p.variableMap["global."+name]; ok {
				return v.Value, true
			}
		}
		return "", false
	}

	for _, v := range p.Variables {
		if v.Name == name && v.Scope == scope {
			return v.Value, true
		}
	}
	for _, v := range p.Variables {
		if v.Name == name && v.Scope == "global" {
			return v.Value, true
		}
	}
	return "", false
}

func (p *ParsedData) GetVariable(variableName, scope string) (*Variable, error) {
	if strings.IndexByte(variableName, '"') >= 0 {
		variableName = strings.ReplaceAll(variableName, `"`, "")
	}

	if scope == "" {
		scope = "global"
	}

	p.mu.RLock()
	defer p.mu.RUnlock()

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

func (p *ParsedData) GlobalVariableSnapshot() map[string]string {
	p.mu.RLock()
	defer p.mu.RUnlock()

	out := make(map[string]string)
	for _, v := range p.Variables {
		if v.Scope == "global" {
			out[v.Name] = v.Value
		}
	}
	return out
}

type Parser struct {
	InputFile   string
	Data        *ParsedData
	Lines       []string
	importStack map[string]bool // recursion path, for cycle detection
	imported    map[string]bool // files already merged, for diamond dedup
}

type Argument struct {
	Name       string `json:"name"`
	IsOptional bool   `json:"is_optional"`
	Default    string `json:"default,omitempty"`
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

type BodyStatement struct {
	Type       string          `json:"type"` // "shell", "if", or "for"
	Shell      string          `json:"shell,omitempty"`
	OutputName string          `json:"output_name,omitempty"`
	Cond       string          `json:"cond,omitempty"`
	ThenBody   []BodyStatement `json:"then,omitempty"`
	ElseBody   []BodyStatement `json:"else,omitempty"`
	LoopVar    string          `json:"loop_var,omitempty"`
	LoopIndex  string          `json:"loop_index,omitempty"`
	LoopItems  string          `json:"loop_items,omitempty"`
	LoopBody   []BodyStatement `json:"loop_body,omitempty"`
}

type Command struct {
	Name            string            `json:"name"`
	SourceFile      string            `json:"source_file,omitempty"`
	CloudAccessible bool              `json:"cloud_accessible"`
	IsDefault       bool              `json:"is_default"`
	LazyEval        *LazyOutput       `json:"lazy_output"`
	IsPrereq        bool              `json:"is_prereq"`
	PrereqOutput    []string          `json:"prereq_output"`
	NamedOutput     map[string]string `json:"named_output"`
	Arguments       []*Argument       `json:"arguments"`
	Prereqs         []string          `json:"prereqs"`
	PrereqDirs      map[string]string `json:"prereq_dirs,omitempty"`
	FileDeps        []string          `json:"file_deps"`
	Produces        []string          `json:"produces,omitempty"`
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

		if char == '\\' && i+1 < len(runes) && (runes[i+1] == '&' || runes[i+1] == '@' || runes[i+1] == '$') {
			result.WriteRune(runes[i+1])
			i += 2
			continue
		}

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
			if strings.HasPrefix(remainder, "(") || strings.HasPrefix(remainder, "<") || strings.HasPrefix(remainder, "{") || strings.HasPrefix(remainder, "in ") {
				return strings.TrimSpace(name)
			}
		}
	}

	// Find the earliest terminator: (, <, {, " in " workdir modifier, or
	// " produces " output clause.
	inIdx := strings.Index(line, " in ")
	prodIdx := findProducesIdx(line)
	terminators := []rune{'(', '<', '{'}
	endIdx := len(line)
	for i, r := range line {
		found := slices.Contains(terminators, r)
		if found {
			endIdx = i
			break
		}
	}
	if inIdx >= 0 && inIdx < endIdx {
		endIdx = inIdx
	}
	if prodIdx >= 0 && prodIdx < endIdx {
		endIdx = prodIdx
	}
	return strings.TrimSpace(line[:endIdx])
}

func findProducesIdx(line string) int {
	depth := 0
	inQuote := false
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '"':
			inQuote = !inQuote
		case '(':
			if !inQuote {
				depth++
			}
		case ')':
			if !inQuote {
				depth--
			}
		}
		if depth == 0 && !inQuote && strings.HasPrefix(line[i:], " produces ") {
			return i
		}
	}
	return -1
}

func extractProduces(line string) []string {
	idx := findProducesIdx(line)
	if idx < 0 {
		return nil
	}
	segment := line[idx+len(" produces "):]
	if lt := strings.IndexByte(segment, '<'); lt >= 0 {
		segment = segment[:lt]
	}
	if brace := strings.IndexByte(segment, '{'); brace >= 0 {
		segment = segment[:brace]
	}
	if inIdx := strings.Index(segment, " in "); inIdx >= 0 {
		segment = segment[:inIdx]
	}
	var out []string
	for _, p := range strings.Split(segment, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
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

func extractPrerequisites(line string) ([]string, map[string]string, error) {
	start := strings.Index(line, "<")
	if start == -1 {
		return nil, nil, nil
	}

	end := strings.Index(line[start:], "{")
	if end == -1 {
		return nil, nil, nil
	}

	segment := line[start+1 : start+end]

	dirs := make(map[string]string)
	var result []string
	for _, part := range strings.Split(segment, ",") {
		part = strings.TrimSpace(part)
		if part == "" || part == "in" {
			continue
		}
		if strings.HasPrefix(part, "in ") {
			continue
		}
		dir := ""
		if inIdx := strings.Index(part, " in "); inIdx >= 0 {
			dir = strings.TrimSpace(part[inIdx+4:])
			part = strings.TrimSpace(part[:inIdx])
		}
		if part == "" {
			continue
		}
		result = append(result, part)
		if dir != "" {
			dirs[part] = dir
		}
	}

	if len(dirs) == 0 {
		dirs = nil
	}
	return result, dirs, nil
}

func extractWorkDir(line string) string {
	before, _, ok := strings.Cut(line, "{")
	if !ok {
		return ""
	}

	if lt := strings.Index(before, "<"); lt >= 0 {
		before = before[:lt]
	}

	segment := before
	idx := strings.LastIndex(segment, " in ")
	if idx == -1 {
		return ""
	}

	dir := strings.TrimSpace(segment[idx+4:])
	if comma := strings.IndexByte(dir, ','); comma >= 0 {
		dir = strings.TrimSpace(dir[:comma])
	}
	return dir
}

func parseArgumentName(argStr string) (string, bool, string) {
	argStr = strings.TrimSpace(argStr)
	if argStr == "" {
		return "", false, ""
	}

	parts := strings.Fields(argStr)
	if len(parts) == 0 {
		return "", false, ""
	}

	argName := parts[len(parts)-1]
	isOptional := slices.Contains(parts[:len(parts)-1], "opt")

	defaultVal := ""
	if eq := strings.IndexByte(argName, '='); eq >= 0 {
		defaultVal = argName[eq+1:]
		argName = argName[:eq]
		isOptional = true
	}

	return argName, isOptional, defaultVal
}

func parseArgumentList(argStr string) ([]*Argument, error) {
	argStr = strings.TrimSpace(argStr)
	if argStr == "" {
		return nil, nil
	}

	args := []*Argument{}
	seen := make(map[string]bool)
	parts := strings.SplitSeq(argStr, ",")

	for part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		argName, isOptional, defaultVal := parseArgumentName(part)
		if argName == "" {
			return nil, fmt.Errorf("invalid argument syntax: '%s'", part)
		}
		if seen[argName] {
			return nil, fmt.Errorf("duplicate argument '%s'", argName)
		}
		seen[argName] = true

		args = append(args, &Argument{
			Name:       argName,
			IsOptional: isOptional,
			Default:    defaultVal,
		})
	}

	return args, nil
}

func isFileDep(token string) bool {
	if strings.ContainsAny(token, "/*\\") {
		return true
	}
	if dot := strings.LastIndexByte(token, '.'); dot > 0 && dot < len(token)-1 {
		return true
	}
	return false
}

func (p *Parser) parseCommandBody(startIdx int, commandName string) ([]string, int, error) {
	var body []string
	depth := 0

	for i := startIdx; i < len(p.Lines); i++ {
		line := p.Lines[i]
		trimmedLine := strings.TrimSpace(line)

		opens := strings.Count(trimmedLine, "{")
		closes := strings.Count(trimmedLine, "}")

		isIfHeader := strings.HasPrefix(trimmedLine, "if ") && opens > 0
		isForHeader := strings.HasPrefix(trimmedLine, "for ") && opens > 0
		isMatrixHeader := strings.HasPrefix(trimmedLine, "matrix ") && opens > 0
		isElseCompound := strings.HasPrefix(trimmedLine, "}") && strings.Contains(trimmedLine, "else")

		if isElseCompound {
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

			return body, i + 1, nil
		}

		if isIfHeader || isForHeader || isMatrixHeader {
			depth += opens - closes
		}

		if trimmedLine == "" {
			continue
		}

		body = append(body, trimmedLine)
	}

	return nil, startIdx, fmt.Errorf("unclosed command body for '%s' (missing '}')", commandName)
}

func (p *Parser) parseBodyStatements(raw []string, scope string) ([]BodyStatement, error) {
	var stmts []BodyStatement
	i := 0
	for i < len(raw) {
		line := raw[i]

		line = stripInlineComment(line)
		if line == "" {
			i++
			continue
		}

		if strings.HasPrefix(line, "var ") || strings.HasPrefix(line, "var\t") {
			if err := p.parseVar(line, scope); err != nil {
				return nil, err
			}
			i++
			continue
		}

		if strings.HasPrefix(line, "if ") || line == "if{" {
			stmt, consumed, err := p.parseIfBlock(raw[i:], scope)
			if err != nil {
				return nil, err
			}
			stmts = append(stmts, stmt)
			i += consumed
			continue
		}

		if strings.HasPrefix(line, "for ") && strings.Contains(line, "{") {
			stmt, consumed, err := p.parseForBlock(raw[i:], scope)
			if err != nil {
				return nil, err
			}
			stmts = append(stmts, stmt)
			i += consumed
			continue
		}

		// matrix <var> in <items>; <var> in <items>; ... { body }
		if strings.HasPrefix(line, "matrix ") && strings.Contains(line, "{") {
			stmt, consumed, err := p.parseMatrixBlock(raw[i:], scope)
			if err != nil {
				return nil, err
			}
			stmts = append(stmts, stmt)
			i += consumed
			continue
		}

		if strings.HasPrefix(line, "continue if ") {
			cond := strings.TrimSpace(line[len("continue if "):])
			stmts = append(stmts, BodyStatement{
				Type:     "if",
				Cond:     cond,
				ThenBody: []BodyStatement{{Type: "continue"}},
			})
			i++
			continue
		}
		if strings.HasPrefix(line, "break if ") {
			cond := strings.TrimSpace(line[len("break if "):])
			stmts = append(stmts, BodyStatement{
				Type:     "if",
				Cond:     cond,
				ThenBody: []BodyStatement{{Type: "break"}},
			})
			i++
			continue
		}
		if line == "continue" || line == "break" {
			stmts = append(stmts, BodyStatement{Type: line})
			i++
			continue
		}

		// A dangling "else" can't be a shell statement; report it clearly.
		if line == "else" || strings.HasPrefix(line, "else ") || strings.HasPrefix(line, "else{") {
			return nil, fmt.Errorf("'else' without a matching 'if'")
		}

		shell, outputName := extractOutputName(line)
		stmts = append(stmts, BodyStatement{Type: "shell", Shell: shell, OutputName: outputName})
		i++
	}
	return stmts, nil
}

func extractOutputName(line string) (shell, name string) {
	idx := strings.LastIndex(line, " as ")
	if idx < 0 {
		return line, ""
	}

	suffix := strings.TrimSpace(line[idx+4:])
	if suffix == "" || !isValidIdent(suffix) {
		return line, ""
	}

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

// findBlockBounds finds the quote-aware first '{' and the first '}' after it.
func findBlockBounds(line string) (open, close int, ok bool) {
	inQuote := false
	open = -1
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '"':
			inQuote = !inQuote
		case '{':
			if !inQuote && open < 0 {
				open = i
			}
		case '}':
			if !inQuote && open >= 0 {
				return open, i, true
			}
		}
	}
	return -1, -1, false
}

func singleLineBody(line string) (string, bool) {
	open, close, ok := findBlockBounds(line)
	if !ok || strings.TrimSpace(line[close+1:]) != "" {
		return "", false
	}
	return strings.TrimSpace(line[open+1 : close]), true
}

func splitStatements(s string) []string {
	var out []string
	inQuote := false
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			inQuote = !inQuote
		case ';':
			if !inQuote {
				out = append(out, strings.TrimSpace(s[start:i]))
				start = i + 1
			}
		}
	}
	out = append(out, strings.TrimSpace(s[start:]))
	return out
}

func splitSingleLineIf(line string) (string, string, bool) {
	open, close, ok := findBlockBounds(line)
	if !ok {
		return "", "", false
	}
	then := strings.TrimSpace(line[open+1 : close])
	remainder := strings.TrimSpace(line[close+1:])
	if remainder == "" {
		return then, "", true
	}
	if strings.HasPrefix(remainder, "else") {
		return then, remainder, true
	}
	return "", "", false
}

func (p *Parser) parseIfBlock(raw []string, scope string) (BodyStatement, int, error) {
	header := raw[0]
	cond := extractIfCondition(header)

	if then, elsePart, ok := splitSingleLineIf(header); ok {
		thenStmts, err := p.parseBodyStatements(splitStatements(then), scope)
		if err != nil {
			return BodyStatement{}, 0, err
		}
		stmt := BodyStatement{Type: "if", Cond: cond, ThenBody: thenStmts}
		switch {
		case elsePart == "":
		case strings.HasPrefix(elsePart, "else if "):
			inner, _, err := p.parseIfBlock([]string{elsePart}, scope)
			if err != nil {
				return BodyStatement{}, 0, err
			}
			stmt.ElseBody = []BodyStatement{inner}
		default:
			body, ok := singleLineBody(elsePart)
			if !ok {
				return BodyStatement{}, 0, fmt.Errorf("malformed single-line else: %q", elsePart)
			}
			elseStmts, err := p.parseBodyStatements(splitStatements(body), scope)
			if err != nil {
				return BodyStatement{}, 0, err
			}
			stmt.ElseBody = elseStmts
		}
		return stmt, 1, nil
	}

	var thenLines []string
	consumed := 1
	depth := 0
	for consumed < len(raw) {
		l := raw[consumed]
		trimmed := strings.TrimSpace(l)

		isInnerIf := strings.HasPrefix(trimmed, "if ") && strings.Contains(trimmed, "{")
		isInnerFor := strings.HasPrefix(trimmed, "for ") && strings.Contains(trimmed, "{")
		isElseCompound := strings.HasPrefix(trimmed, "}") && strings.Contains(trimmed, "else")

		if isInnerIf || isInnerFor {
			depth += strings.Count(trimmed, "{") - strings.Count(trimmed, "}")
		}

		if isElseCompound && depth == 0 {
			break
		}

		if strings.HasPrefix(trimmed, "}") {
			if depth > 0 {
				depth--
				thenLines = append(thenLines, l)
				consumed++
				continue
			}
			break
		}

		if (trimmed == "else" || strings.HasPrefix(trimmed, "else ")) && depth == 0 {
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

	closingLine := strings.TrimSpace(raw[consumed])
	hasElse := strings.Contains(closingLine, "else")

	if hasElse {
		consumed++ // consume the "} else {" line
		rest := strings.TrimSpace(strings.TrimPrefix(closingLine, "}"))

		if strings.HasPrefix(rest, "else if ") || rest == "else if" {
			nestedRaw := append([]string{rest}, raw[consumed:]...)
			elseIfStmt, elseConsumed, err := p.parseIfBlock(nestedRaw, scope)
			if err != nil {
				return BodyStatement{}, 0, err
			}
			stmt.ElseBody = []BodyStatement{elseIfStmt}
			consumed += elseConsumed - 1
			return stmt, consumed, nil
		}

		var elseLines []string
		depth := 0
		for consumed < len(raw) {
			l := raw[consumed]
			trimmed := strings.TrimSpace(l)

			isInnerIf := strings.HasPrefix(trimmed, "if ") && strings.Contains(trimmed, "{")
			isInnerFor := strings.HasPrefix(trimmed, "for ") && strings.Contains(trimmed, "{")

			if isInnerIf || isInnerFor {
				depth += strings.Count(trimmed, "{") - strings.Count(trimmed, "}")
			}

			if strings.HasPrefix(trimmed, "}") {
				if depth > 0 {
					depth--
					elseLines = append(elseLines, l)
					consumed++
					continue
				}
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

func (p *Parser) parseForBlock(raw []string, scope string) (BodyStatement, int, error) {
	header := strings.TrimSpace(raw[0])
	header = strings.TrimPrefix(header, "for")

	before, _, ok := strings.Cut(header, "{")
	if !ok {
		return BodyStatement{}, 0, fmt.Errorf("malformed for loop: missing '{'")
	}
	headerPart := strings.TrimSpace(before)

	inIdx := strings.Index(headerPart, " in ")
	if inIdx < 0 {
		return BodyStatement{}, 0, fmt.Errorf("malformed for loop: missing 'in'")
	}

	loopVar := strings.TrimSpace(headerPart[:inIdx])
	loopItems := strings.TrimSpace(headerPart[inIdx+4:])

	// "for i, f in ..." binds an index variable in addition to the value.
	loopIndex := ""
	if comma := strings.IndexByte(loopVar, ','); comma >= 0 {
		loopIndex = strings.TrimSpace(loopVar[:comma])
		loopVar = strings.TrimSpace(loopVar[comma+1:])
		if loopVar == "" || loopIndex == "" {
			return BodyStatement{}, 0, fmt.Errorf("malformed for loop: bad index syntax in %q", headerPart)
		}
	}

	// Single-line block: "for x in a, b { stmt }".
	if body, ok := singleLineBody(raw[0]); ok {
		bodyStmts, err := p.parseBodyStatements(splitStatements(body), scope)
		if err != nil {
			return BodyStatement{}, 0, err
		}
		return BodyStatement{
			Type:      "for",
			LoopVar:   loopVar,
			LoopIndex: loopIndex,
			LoopItems: loopItems,
			LoopBody:  bodyStmts,
		}, 1, nil
	}

	bodyLines, endIdx, err := collectBodyLines(raw, 1)
	if err != nil {
		return BodyStatement{}, 0, fmt.Errorf("unclosed for block (missing '}')")
	}

	bodyStmts, err := p.parseBodyStatements(bodyLines, scope)
	if err != nil {
		return BodyStatement{}, 0, err
	}

	stmt := BodyStatement{
		Type:      "for",
		LoopVar:   loopVar,
		LoopIndex: loopIndex,
		LoopItems: loopItems,
		LoopBody:  bodyStmts,
	}
	return stmt, endIdx, nil
}

func (p *Parser) parseMatrixBlock(raw []string, scope string) (BodyStatement, int, error) {
	header := strings.TrimSpace(raw[0])
	header = strings.TrimPrefix(header, "matrix")

	before, _, ok := strings.Cut(header, "{")
	if !ok {
		return BodyStatement{}, 0, fmt.Errorf("malformed matrix: missing '{'")
	}
	headerPart := strings.TrimSpace(before)

	var vars, items []string
	for _, clause := range strings.Split(headerPart, ";") {
		clause = strings.TrimSpace(clause)
		inIdx := strings.Index(clause, " in ")
		if inIdx < 0 {
			return BodyStatement{}, 0, fmt.Errorf("malformed matrix clause %q: missing 'in'", clause)
		}
		v := strings.TrimSpace(clause[:inIdx])
		it := strings.TrimSpace(clause[inIdx+4:])
		if v == "" || it == "" {
			return BodyStatement{}, 0, fmt.Errorf("malformed matrix clause %q", clause)
		}
		vars = append(vars, v)
		items = append(items, it)
	}

	// Single-line block: "matrix ... { stmt }".
	if body, ok := singleLineBody(raw[0]); ok {
		bodyStmts, err := p.parseBodyStatements(splitStatements(body), scope)
		if err != nil {
			return BodyStatement{}, 0, err
		}
		stmt := BodyStatement{
			Type:      "for",
			LoopVar:   vars[len(vars)-1],
			LoopItems: items[len(items)-1],
			LoopBody:  bodyStmts,
		}
		for i := len(vars) - 2; i >= 0; i-- {
			stmt = BodyStatement{
				Type:      "for",
				LoopVar:   vars[i],
				LoopItems: items[i],
				LoopBody:  []BodyStatement{stmt},
			}
		}
		return stmt, 1, nil
	}

	bodyLines, endIdx, err := collectBodyLines(raw, 1)
	if err != nil {
		return BodyStatement{}, 0, fmt.Errorf("unclosed matrix block (missing '}')")
	}

	bodyStmts, err := p.parseBodyStatements(bodyLines, scope)
	if err != nil {
		return BodyStatement{}, 0, err
	}

	stmt := BodyStatement{
		Type:      "for",
		LoopVar:   vars[len(vars)-1],
		LoopItems: items[len(items)-1],
		LoopBody:  bodyStmts,
	}
	for i := len(vars) - 2; i >= 0; i-- {
		stmt = BodyStatement{
			Type:      "for",
			LoopVar:   vars[i],
			LoopItems: items[i],
			LoopBody:  []BodyStatement{stmt},
		}
	}
	return stmt, endIdx, nil
}

func collectBodyLines(raw []string, start int) ([]string, int, error) {
	var lines []string
	depth := 0
	for start < len(raw) {
		l := raw[start]
		trimmed := strings.TrimSpace(l)

		// Net-brace accounting: "if x { stmt }" opens and closes on one line.
		if isNestedBlockHeader(trimmed) {
			depth += strings.Count(trimmed, "{") - strings.Count(trimmed, "}")
		}

		if strings.HasPrefix(trimmed, "}") {
			if depth > 0 {
				if !strings.Contains(trimmed, "else") {
					depth--
				}
				lines = append(lines, l)
				start++
				continue
			}
			return lines, start + 1, nil
		}

		lines = append(lines, l)
		start++
	}
	return nil, start, fmt.Errorf("unclosed block (missing '}')")
}

func isNestedBlockHeader(t string) bool {
	return (strings.HasPrefix(t, "if ") || strings.HasPrefix(t, "for ") || strings.HasPrefix(t, "matrix ")) && strings.Contains(t, "{")
}

func extractIfCondition(line string) string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "if")
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "else if")
	line = strings.TrimSpace(line)

	brace := -1
	inQuote := false
	for i := 0; i < len(line); i++ {
		if line[i] == '"' {
			inQuote = !inQuote
		}
		if line[i] == '{' && !inQuote {
			brace = i
			break
		}
	}
	if brace >= 0 {
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

	prereqs, prereqDirs, err := extractPrerequisites(line)
	if err != nil {
		return 0, fmt.Errorf("failed to parse prerequisites for '%s': %w", commandName, err)
	}

	workDir := extractWorkDir(line)
	produces := extractProduces(line)

	rawBody, endIdx, err := p.parseCommandBody(idx+1, commandName)
	if err != nil {
		return 0, err
	}

	commandBody, err := p.parseBodyStatements(rawBody, commandName)
	if err != nil {
		return 0, err
	}

	if commandName != "" {
		p.Data.Commands = append(p.Data.Commands, &Command{
			Name:            commandName,
			SourceFile:      p.InputFile,
			CloudAccessible: cloudAccessible,
			IsDefault:       isDefault,
			Arguments:       commandArgs,
			Prereqs:         prereqs,
			PrereqDirs:      prereqDirs,
			WorkDir:         workDir,
			Produces:        produces,
			Body:            commandBody,
		})
	}

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
		// A comment marker only starts a comment when preceded by whitespace
		// (or line start), matching shell conventions. This keeps URLs and
		// anchors intact: https://x and a#b are not comments.
		if !inQuote && i+1 < len(line) && c == '/' && line[i+1] == '/' {
			if i == 0 || line[i-1] == ' ' || line[i-1] == '\t' {
				return strings.TrimSpace(line[:i])
			}
		}
		if !inQuote && c == '#' {
			if i == 0 || line[i-1] == ' ' || line[i-1] == '\t' {
				return strings.TrimSpace(line[:i])
			}
		}
	}
	return line
}

func (p *Parser) parseLines() error {
	if p.importStack == nil {
		p.importStack = make(map[string]bool)
	}
	if p.imported == nil {
		p.imported = make(map[string]bool)
	}

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

		if strings.HasPrefix(line, "import ") || line == "import" {
			if err := p.processImport(line); err != nil {
				return NewParseError(lineNum, 1, err.Error(), line)
			}
			idx++
			continue
		}

		if strings.HasPrefix(line, "var ") {
			if err := p.parseVar(line, "global"); err != nil {
				return NewParseError(lineNum, 1, err.Error(), line)
			}
			idx++
			continue
		}

		if len(line) > 0 && line[0] == '_' && (len(line) == 1 || line[1] == ' ' || line[1] == '(' || line[1] == '<' || line[1] == '{') {
			consumed, err := p.parseCommand(idx, line, true)
			if err != nil {
				return NewParseError(lineNum, 1, err.Error(), line)
			}
			idx += consumed
			if consumed == 0 {
				idx++
			}
			continue
		}

		consumed, err := p.parseCommand(idx, line, false)
		if err != nil {
			return NewParseError(lineNum, 1, err.Error(), line)
		}
		idx += consumed
		if consumed == 0 {
			idx++
		}
	}

	return nil
}

func (p *Parser) processImport(line string) error {
	spec := strings.TrimSpace(strings.TrimPrefix(line, "import"))
	spec = strings.Trim(spec, `"`)
	if spec == "" {
		return fmt.Errorf("import requires a file path")
	}

	path := spec
	if !filepath.IsAbs(path) {
		dir := filepath.Dir(strings.TrimPrefix(p.InputFile, "file://"))
		path = filepath.Join(dir, spec)
	}
	cleanPath := filepath.Clean(path)
	if p.importStack[cleanPath] {
		return fmt.Errorf("circular import of %q", spec)
	}
	if p.imported[cleanPath] {
		// Already merged (e.g. reached through a different import path).
		return nil
	}
	p.importStack[cleanPath] = true
	defer delete(p.importStack, cleanPath)
	p.imported[cleanPath] = true

	content, err := os.ReadFile(cleanPath)
	if err != nil {
		return fmt.Errorf("failed to read import %q: %w", spec, err)
	}

	imported := NewParserFromContent(cleanPath, string(content))
	imported.importStack = p.importStack
	imported.imported = p.imported
	if err := imported.parseLines(); err != nil {
		return err
	}

	p.Data.Variables = append(p.Data.Variables, imported.Data.Variables...)
	for _, cmd := range imported.Data.Commands {
		if existing, err := p.Data.GetCommand(cmd.Name); err == nil && existing != nil {
			return fmt.Errorf("duplicate command %q from import %q", cmd.Name, spec)
		}
		p.Data.Commands = append(p.Data.Commands, cmd)
	}
	return nil
}

func (p *Parser) classifyPrereqs() error {
	for _, cmd := range p.Data.Commands {
		var cmdDeps, fileDeps []string
		for _, prereq := range cmd.Prereqs {
			prereq = strings.TrimSpace(prereq)
			if prereq == "" {
				continue
			}
			if _, err := p.Data.GetCommand(prereq); err == nil {
				cmdDeps = append(cmdDeps, prereq)
			} else if isFileDep(prereq) {
				fileDeps = append(fileDeps, prereq)
			} else {
				return &MissingDependencyError{
					Command:    cmd.Name,
					PrereqName: prereq,
				}
			}
		}
		cmd.Prereqs = cmdDeps
		cmd.FileDeps = fileDeps
	}
	return nil
}

func (p *Parser) Parse() (*ParsedData, error) {
	if err := p.parseLines(); err != nil {
		return nil, err
	}

	p.Data.buildIndexMaps()

	seen := make(map[string]bool)
	for _, cmd := range p.Data.Commands {
		if seen[cmd.Name] {
			return nil, fmt.Errorf("duplicate command %q", cmd.Name)
		}
		seen[cmd.Name] = true
	}

	seenVars := make(map[string]bool)
	for _, v := range p.Data.Variables {
		key := v.Scope + "." + v.Name
		if seenVars[key] {
			return nil, fmt.Errorf("duplicate variable %q in scope %q", v.Name, v.Scope)
		}
		seenVars[key] = true
	}

	if err := p.classifyPrereqs(); err != nil {
		return nil, err
	}

	if err := p.detectCircularDependencies(); err != nil {
		return nil, err
	}

	return p.Data, nil
}
