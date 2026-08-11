package pkg

import (
	"errors"
	"fmt"
	"net/url"
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

	SourceFiles []string `json:"source_files,omitempty"`

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

// BodyStatement statement types.
const (
	StmtShell    = "shell"
	StmtIf       = "if"
	StmtFor      = "for"
	StmtInvoke   = "invoke"
	StmtEnv      = "env"
	StmtContinue = "continue"
	StmtBreak    = "break"
)

type BodyStatement struct {
	Type       string          `json:"type"` // one of the Stmt* constants
	Shell      string          `json:"shell,omitempty"`
	OutputName string          `json:"output_name,omitempty"`
	Cond       string          `json:"cond,omitempty"`
	ThenBody   []BodyStatement `json:"then,omitempty"`
	ElseBody   []BodyStatement `json:"else,omitempty"`
	LoopVar    string          `json:"loop_var,omitempty"`
	LoopIndex  string          `json:"loop_index,omitempty"`
	LoopItems  string          `json:"loop_items,omitempty"`
	LoopBody   []BodyStatement `json:"loop_body,omitempty"`
	Env        []string        `json:"env,omitempty"`
	SourceLine int             `json:"source_line,omitempty"`
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
	SourceLine      int               `json:"source_line,omitempty"`
	Description     string            `json:"description,omitempty"`
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

func (p *Parser) tryEvalExpression(expression string, varName *string, varScope *string, lineNum int) string {
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
				Name:       fmt.Sprintf("__lazy_%s_%s", *varName, *varScope),
				LazyEval:   &LazyOutput{VarName: *varName, Scope: *varScope},
				Body:       []BodyStatement{{Type: StmtShell, Shell: fmt.Sprintf("$ %s", restOfLine), SourceLine: lineNum}},
				SourceLine: lineNum,
				SourceFile: p.InputFile,
			})
			i = len(runes)
			continue
		}

		result.WriteRune(char)
		i++
	}

	return result.String()
}

func (p *Parser) parseVar(line string, scope string, lineNum int) error {
	pieces := strings.SplitN(line, "=", 2)

	variableName := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(pieces[0]), "var"))
	if variableName == "" {
		return fmt.Errorf("variable declaration is missing a name: %q", line)
	}

	var variableValue string
	if len(pieces) > 1 {
		variableValue = p.tryEvalExpression(pieces[1], &variableName, &scope, lineNum)
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
			dir = strings.TrimSpace(part[inIdx+len(" in "):])
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

	dir := strings.TrimSpace(segment[idx+len(" in "):])
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

type rawLine struct {
	text string
	num  int
}

func (p *Parser) parseCommandBody(startIdx int, commandName string) ([]rawLine, int, error) {
	var body []rawLine
	depth := 0

	for i := startIdx; i < len(p.Lines); i++ {
		line := p.Lines[i]
		trimmedLine := strings.TrimSpace(line)
		lineNum := i + 1

		isElseCompound := strings.HasPrefix(trimmedLine, "}") && strings.Contains(trimmedLine, "else")

		if isElseCompound {
			if depth > 0 {
				depth-- // close then-block
			}
			depth++ // open else-block
			body = append(body, rawLine{text: trimmedLine, num: lineNum})
			continue
		}

		if strings.HasPrefix(trimmedLine, "}") {
			if depth > 0 {
				depth--
				body = append(body, rawLine{text: trimmedLine, num: lineNum})
				continue
			}

			return body, i + 1, nil
		}

		if isNestedBlockHeader(trimmedLine) {
			depth += strings.Count(trimmedLine, "{") - strings.Count(trimmedLine, "}")
		}

		if trimmedLine == "" {
			continue
		}

		body = append(body, rawLine{text: trimmedLine, num: lineNum})
	}

	return nil, startIdx, fmt.Errorf("unclosed command body for '%s' (missing '}')", commandName)
}

func (p *Parser) parseBodyStatements(raw []rawLine, scope string) ([]BodyStatement, error) {
	var stmts []BodyStatement
	i := 0
	for i < len(raw) {
		line := raw[i].text
		lineNum := raw[i].num

		line = stripInlineComment(line)
		if line == "" {
			i++
			continue
		}

		if strings.HasPrefix(line, "var ") || strings.HasPrefix(line, "var\t") {
			if err := p.parseVar(line, scope, lineNum); err != nil {
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

		if strings.HasPrefix(line, "env ") && strings.Contains(line, "{") {
			stmt, consumed, err := p.parseEnvBlock(raw[i:])
			if err != nil {
				return nil, err
			}
			stmts = append(stmts, stmt)
			i += consumed
			continue
		}

		if strings.HasPrefix(line, "invoke ") {
			rest := strings.TrimSpace(line[len("invoke "):])
			name, outputName := extractOutputName(rest)
			name = strings.TrimSpace(name)
			if name == "" {
				return nil, fmt.Errorf("invoke requires a command name")
			}
			stmts = append(stmts, BodyStatement{Type: StmtInvoke, Shell: name, OutputName: outputName, SourceLine: lineNum})
			i++
			continue
		}

		if strings.HasPrefix(line, "continue if ") {
			cond := strings.TrimSpace(line[len("continue if "):])
			stmts = append(stmts, BodyStatement{
				Type:       StmtIf,
				Cond:       cond,
				ThenBody:   []BodyStatement{{Type: StmtContinue, SourceLine: lineNum}},
				SourceLine: lineNum,
			})
			i++
			continue
		}

		if strings.HasPrefix(line, "break if ") {
			cond := strings.TrimSpace(line[len("break if "):])
			stmts = append(stmts, BodyStatement{
				Type:       StmtIf,
				Cond:       cond,
				ThenBody:   []BodyStatement{{Type: StmtBreak, SourceLine: lineNum}},
				SourceLine: lineNum,
			})
			i++
			continue
		}

		if line == "continue" || line == "break" {
			stmtType := StmtContinue
			if line == "break" {
				stmtType = StmtBreak
			}
			stmts = append(stmts, BodyStatement{Type: stmtType, SourceLine: lineNum})
			i++
			continue
		}

		// A dangling "else" can't be a shell statement; report it clearly.
		if line == "else" || strings.HasPrefix(line, "else ") || strings.HasPrefix(line, "else{") {
			return nil, fmt.Errorf("'else' without a matching 'if'")
		}

		shell, outputName := extractOutputName(line)
		stmts = append(stmts, BodyStatement{Type: StmtShell, Shell: shell, OutputName: outputName, SourceLine: lineNum})
		i++
	}
	return stmts, nil
}

func extractOutputName(line string) (shell, name string) {
	idx := strings.LastIndex(line, " as ")
	if idx < 0 {
		return line, ""
	}

	suffix := strings.TrimSpace(line[idx+len(" as "):])
	if suffix == "" || !isValidIdent(suffix) {
		return line, ""
	}

	before := line[:idx]
	if strings.Count(before, `"`)%2 != 0 {
		return line, ""
	}
	return strings.TrimSpace(before), suffix
}

// isValidIdent reports whether s is a plain word (letters, digits,
// underscore, no dash) — used for `as <name>` outputs and import namespaces.
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

// atLine tags each statement string with the source line it came from.
func atLine(texts []string, num int) []rawLine {
	out := make([]rawLine, len(texts))
	for i, t := range texts {
		out[i] = rawLine{text: t, num: num}
	}
	return out
}

func (p *Parser) parseIfBlock(raw []rawLine, scope string) (BodyStatement, int, error) {
	header := raw[0].text
	headerNum := raw[0].num
	cond := extractIfCondition(header)

	if then, elsePart, ok := splitSingleLineIf(header); ok {
		thenStmts, err := p.parseBodyStatements(atLine(splitStatements(then), headerNum), scope)
		if err != nil {
			return BodyStatement{}, 0, err
		}
		stmt := BodyStatement{Type: StmtIf, Cond: cond, ThenBody: thenStmts, SourceLine: headerNum}
		switch {
		case elsePart == "":
		case strings.HasPrefix(elsePart, "else if "):
			inner, _, err := p.parseIfBlock(atLine([]string{elsePart}, headerNum), scope)
			if err != nil {
				return BodyStatement{}, 0, err
			}
			stmt.ElseBody = []BodyStatement{inner}
		default:
			body, ok := singleLineBody(elsePart)
			if !ok {
				return BodyStatement{}, 0, fmt.Errorf("malformed single-line else: %q", elsePart)
			}
			elseStmts, err := p.parseBodyStatements(atLine(splitStatements(body), headerNum), scope)
			if err != nil {
				return BodyStatement{}, 0, err
			}
			stmt.ElseBody = elseStmts
		}
		return stmt, 1, nil
	}

	var thenLines []rawLine
	consumed := 1
	depth := 0
	for consumed < len(raw) {
		l := raw[consumed]
		trimmed := strings.TrimSpace(l.text)

		isNested := isNestedBlockHeader(trimmed)
		isElseCompound := strings.HasPrefix(trimmed, "}") && strings.Contains(trimmed, "else")

		if isNested {
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
		Type:       StmtIf,
		Cond:       cond,
		ThenBody:   thenStmts,
		SourceLine: headerNum,
	}

	closing := raw[consumed]
	closingLine := strings.TrimSpace(closing.text)
	hasElse := strings.Contains(closingLine, "else")

	if hasElse {
		consumed++ // consume the "} else {" line
		rest := strings.TrimSpace(strings.TrimPrefix(closingLine, "}"))

		if strings.HasPrefix(rest, "else if ") || rest == "else if" {
			nestedRaw := append(atLine([]string{rest}, closing.num), raw[consumed:]...)
			elseIfStmt, elseConsumed, err := p.parseIfBlock(nestedRaw, scope)
			if err != nil {
				return BodyStatement{}, 0, err
			}
			stmt.ElseBody = []BodyStatement{elseIfStmt}
			consumed += elseConsumed - 1
			return stmt, consumed, nil
		}

		var elseLines []rawLine
		depth := 0
		for consumed < len(raw) {
			l := raw[consumed]
			trimmed := strings.TrimSpace(l.text)

			if isNestedBlockHeader(trimmed) {
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

func splitEnvPairs(s string) []string {
	var out []string
	inQuote := false
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			inQuote = !inQuote
		case ',':
			if !inQuote {
				out = append(out, strings.TrimSpace(s[start:i]))
				start = i + 1
			}
		}
	}
	out = append(out, strings.TrimSpace(s[start:]))
	return out
}

func parseEnvPairs(pairs []string) ([]string, error) {
	var out []string
	for _, pair := range pairs {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		if !strings.Contains(pair, "=") {
			return nil, fmt.Errorf("invalid env declaration %q (expected KEY=VALUE)", pair)
		}
		out = append(out, pair)
	}
	return out, nil
}

func (p *Parser) parseEnvBlock(raw []rawLine) (BodyStatement, int, error) {
	headerLine := raw[0]
	stmt := BodyStatement{Type: StmtEnv, SourceLine: headerLine.num}

	if body, ok := singleLineBody(headerLine.text); ok {
		pairs, err := parseEnvPairs(splitEnvPairs(body))
		if err != nil {
			return BodyStatement{}, 0, err
		}
		stmt.Env = pairs
		return stmt, 1, nil
	}

	lines, endIdx, err := collectBodyLines(raw, 1)
	if err != nil {
		return BodyStatement{}, 0, fmt.Errorf("unclosed env block (missing '}')")
	}
	var pairs []string
	for _, l := range lines {
		l.text = stripInlineComment(l.text)
		if strings.TrimSpace(l.text) == "" {
			continue
		}
		pairs = append(pairs, l.text)
	}
	parsed, err := parseEnvPairs(pairs)
	if err != nil {
		return BodyStatement{}, 0, err
	}
	stmt.Env = parsed
	return stmt, endIdx, nil
}

func (p *Parser) parseForBlock(raw []rawLine, scope string) (BodyStatement, int, error) {
	headerLine := raw[0]
	header := strings.TrimSpace(headerLine.text)
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
	loopItems := strings.TrimSpace(headerPart[inIdx+len(" in "):])

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
	if body, ok := singleLineBody(raw[0].text); ok {
		bodyStmts, err := p.parseBodyStatements(atLine(splitStatements(body), headerLine.num), scope)
		if err != nil {
			return BodyStatement{}, 0, err
		}
		return BodyStatement{
			Type:       StmtFor,
			LoopVar:    loopVar,
			LoopIndex:  loopIndex,
			LoopItems:  loopItems,
			LoopBody:   bodyStmts,
			SourceLine: headerLine.num,
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
		Type:       StmtFor,
		LoopVar:    loopVar,
		LoopIndex:  loopIndex,
		LoopItems:  loopItems,
		LoopBody:   bodyStmts,
		SourceLine: headerLine.num,
	}
	return stmt, endIdx, nil
}

func (p *Parser) parseMatrixBlock(raw []rawLine, scope string) (BodyStatement, int, error) {
	headerLine := raw[0]
	header := strings.TrimSpace(headerLine.text)
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
		it := strings.TrimSpace(clause[inIdx+len(" in "):])
		if v == "" || it == "" {
			return BodyStatement{}, 0, fmt.Errorf("malformed matrix clause %q", clause)
		}
		vars = append(vars, v)
		items = append(items, it)
	}

	if body, ok := singleLineBody(raw[0].text); ok {
		bodyStmts, err := p.parseBodyStatements(atLine(splitStatements(body), headerLine.num), scope)
		if err != nil {
			return BodyStatement{}, 0, err
		}
		stmt := BodyStatement{
			Type:       StmtFor,
			LoopVar:    vars[len(vars)-1],
			LoopItems:  items[len(items)-1],
			LoopBody:   bodyStmts,
			SourceLine: headerLine.num,
		}
		for i := len(vars) - 2; i >= 0; i-- {
			stmt = BodyStatement{
				Type:       StmtFor,
				LoopVar:    vars[i],
				LoopItems:  items[i],
				LoopBody:   []BodyStatement{stmt},
				SourceLine: headerLine.num,
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
		Type:       StmtFor,
		LoopVar:    vars[len(vars)-1],
		LoopItems:  items[len(items)-1],
		LoopBody:   bodyStmts,
		SourceLine: headerLine.num,
	}

	for i := len(vars) - 2; i >= 0; i-- {
		stmt = BodyStatement{
			Type:       StmtFor,
			LoopVar:    vars[i],
			LoopItems:  items[i],
			LoopBody:   []BodyStatement{stmt},
			SourceLine: headerLine.num,
		}
	}
	return stmt, endIdx, nil
}

func collectBodyLines(raw []rawLine, start int) ([]rawLine, int, error) {
	var lines []rawLine
	depth := 0
	for start < len(raw) {
		l := raw[start]
		trimmed := strings.TrimSpace(l.text)

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
	return (strings.HasPrefix(t, "if ") || strings.HasPrefix(t, "for ") || strings.HasPrefix(t, "matrix ") || strings.HasPrefix(t, "env ")) && strings.Contains(t, "{")
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

func (p *Parser) parseCommand(idx int, line string, isDefault bool, lineNum int, description string) (int, error) {
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
			SourceLine:      lineNum,
			Description:     description,
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
			file := ""
			if cmd, err := p.Data.GetCommand(cmdName); err == nil {
				file = cmd.SourceFile
			}
			return &CircularDependencyError{
				Command: cmdName,
				Path:    append(path, cmdName),
				File:    file,
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
	var pendingComment []string // doc comment lines before the next command
	for idx < len(p.Lines) {
		line := p.Lines[idx]
		lineNum := idx + 1

		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "//") {
			pendingComment = append(pendingComment, trimDocMarker(trimmed))
			idx++
			continue
		}

		line = stripInlineComment(line)
		if line == "" {
			idx++
			continue
		}

		if strings.HasPrefix(line, "import ") || line == "import" {
			if err := p.processImport(line); err != nil {
				return p.parseErr(lineNum, err, line)
			}
			pendingComment = nil
			idx++
			continue
		}

		if strings.HasPrefix(line, "var ") {
			if err := p.parseVar(line, "global", lineNum); err != nil {
				return p.parseErr(lineNum, err, line)
			}
			pendingComment = nil
			idx++
			continue
		}

		isDefault := len(line) > 0 && line[0] == '_' &&
			(len(line) == 1 || line[1] == ' ' || line[1] == '(' || line[1] == '<' || line[1] == '{')

		consumed, err := p.parseCommand(idx, line, isDefault, lineNum, strings.Join(pendingComment, "\n"))
		if err != nil {
			return p.parseErr(lineNum, err, line)
		}
		pendingComment = nil
		idx += consumed
		if consumed == 0 {
			idx++
		}
	}

	return nil
}

func trimDocMarker(line string) string {
	line = strings.TrimSpace(line)
	line = strings.TrimLeft(line, "#/")
	return strings.TrimSpace(line)
}

func parseImportSpec(line string) (path, ns string, err error) {
	spec := strings.TrimSpace(strings.TrimPrefix(line, "import"))

	if asIdx := strings.LastIndex(spec, " as "); asIdx >= 0 {
		ns = strings.TrimSpace(spec[asIdx+len(" as "):])
		spec = strings.TrimSpace(spec[:asIdx])
		if !isValidIdent(ns) {
			return "", "", fmt.Errorf("invalid import namespace %q (expected an identifier)", ns)
		}
	}

	spec = strings.Trim(spec, `"`)
	if spec == "" {
		return "", "", fmt.Errorf("import requires a file path")
	}
	return spec, ns, nil
}

func (p *Parser) processImport(line string) error {
	spec, ns, err := parseImportSpec(line)
	if err != nil {
		return err
	}

	path := spec
	if !filepath.IsAbs(path) {
		path = filepath.Join(importBaseDir(p.InputFile), spec)
	}

	cleanPath := filepath.Clean(path)
	if p.importStack[cleanPath] {
		return fmt.Errorf("circular import of %q", spec)
	}

	dedupKey := cleanPath
	if ns != "" {
		dedupKey = cleanPath + "|" + ns
	}

	if p.imported[dedupKey] {
		return nil
	}

	p.importStack[cleanPath] = true
	defer delete(p.importStack, cleanPath)
	p.imported[dedupKey] = true

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

	if ns != "" {
		renameImportNamespace(imported.Data, ns)
	}

	if !slices.Contains(p.Data.SourceFiles, cleanPath) {
		p.Data.SourceFiles = append(p.Data.SourceFiles, cleanPath)
	}
	for _, sf := range imported.Data.SourceFiles {
		if !slices.Contains(p.Data.SourceFiles, sf) {
			p.Data.SourceFiles = append(p.Data.SourceFiles, sf)
		}
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

func importBaseDir(inputFile string) string {
	u, err := url.Parse(inputFile)
	if err == nil && u.Scheme == "file" {
		p := u.Path
		if len(p) > 2 && p[0] == '/' && p[2] == ':' {
			p = p[1:] // strip the leading slash before the drive letter: /c:/ -> c:/
		}
		return filepath.Dir(filepath.FromSlash(p))
	}
	return filepath.Dir(inputFile)
}

func renameImportNamespace(data *ParsedData, ns string) {
	commandNew, globalNew := importRenameMaps(data, ns)
	shadows := commandShadowSets(data)

	for _, c := range data.Commands {
		oldName := c.Name
		c.Name = commandNew[oldName]
		renameCommandRefs(c, commandNew, globalNew, shadows[oldName])
	}

	for _, v := range data.Variables {
		if v.Scope == "global" {
			if n, ok := globalNew[v.Name]; ok {
				v.Name = n
			}
		} else if n, ok := commandNew[v.Scope]; ok {
			v.Scope = n
		}
	}
}

// importRenameMaps builds the old->new name tables for commands and globals.
func importRenameMaps(data *ParsedData, ns string) (commandNew, globalNew map[string]string) {
	prefix := ns + "."
	commandNew = make(map[string]string, len(data.Commands))
	for _, c := range data.Commands {
		commandNew[c.Name] = prefix + c.Name
	}
	globalNew = make(map[string]string)
	for _, v := range data.Variables {
		if v.Scope == "global" {
			globalNew[v.Name] = prefix + v.Name
		}
	}
	return commandNew, globalNew
}

func commandShadowSets(data *ParsedData) map[string]map[string]bool {
	shadows := make(map[string]map[string]bool, len(data.Commands))
	for _, c := range data.Commands {
		shadow := make(map[string]bool)
		for _, v := range data.Variables {
			if v.Scope == c.Name {
				shadow[v.Name] = true
			}
		}
		for _, arg := range c.Arguments {
			shadow[arg.Name] = true
		}
		if c.LazyEval != nil && c.LazyEval.Scope != "global" {
			// Lazy bodies resolve in their scope command's context.
			if scopeCmd, err := data.GetCommand(c.LazyEval.Scope); err == nil {
				for _, v := range data.Variables {
					if v.Scope == scopeCmd.Name {
						shadow[v.Name] = true
					}
				}
				for _, arg := range scopeCmd.Arguments {
					shadow[arg.Name] = true
				}
			}
		}
		collectLoopVars(c.Body, shadow)
		shadows[c.Name] = shadow
	}
	return shadows
}

func renameCommandRefs(c *Command, commandNew, globalNew map[string]string, shadow map[string]bool) {
	for i, prereq := range c.Prereqs {
		if n, ok := commandNew[strings.TrimSpace(prereq)]; ok {
			c.Prereqs[i] = n
		}
	}

	if len(c.PrereqDirs) > 0 {
		newDirs := make(map[string]string, len(c.PrereqDirs))
		for prereq, dir := range c.PrereqDirs {
			if n, ok := commandNew[prereq]; ok {
				newDirs[n] = dir
			} else {
				newDirs[prereq] = dir
			}
		}
		c.PrereqDirs = newDirs
	}

	if c.LazyEval != nil {
		if c.LazyEval.Scope == "global" {
			if n, ok := globalNew[c.LazyEval.VarName]; ok {
				c.LazyEval.VarName = n
			}
		} else if n, ok := commandNew[c.LazyEval.Scope]; ok {
			c.LazyEval.Scope = n
		}
	}

	rename := func(full string) (string, bool) {
		seg := firstIdent(full)
		if seg == "" || shadow[seg] {
			return "", false
		}
		if n, ok := globalNew[full]; ok {
			return "&" + n, true
		}
		if n, ok := commandNew[seg]; ok {
			return "&" + n + full[len(seg):], true
		}
		return "", false
	}
	renameBodyRefs(c.Body, rename)
}

func collectLoopVars(stmts []BodyStatement, out map[string]bool) {
	for _, stmt := range stmts {
		switch stmt.Type {
		case StmtFor:
			out[stmt.LoopVar] = true
			if stmt.LoopIndex != "" {
				out[stmt.LoopIndex] = true
			}
			collectLoopVars(stmt.LoopBody, out)
		case StmtIf:
			collectLoopVars(stmt.ThenBody, out)
			collectLoopVars(stmt.ElseBody, out)
		}
	}
}

func renameBodyRefs(stmts []BodyStatement, rename func(string) (string, bool)) {
	for i := range stmts {
		switch stmts[i].Type {
		case StmtIf:
			stmts[i].Cond = renameVarRefs(stmts[i].Cond, rename)
			renameBodyRefs(stmts[i].ThenBody, rename)
			renameBodyRefs(stmts[i].ElseBody, rename)
		case StmtFor:
			stmts[i].LoopItems = renameVarRefs(stmts[i].LoopItems, rename)
			renameBodyRefs(stmts[i].LoopBody, rename)
		case StmtInvoke:
			if s := stmts[i].Shell; s != "" {
				rewritten := renameVarRefs("&"+s, rename)
				if rewritten != "&"+s {
					stmts[i].Shell = strings.TrimPrefix(rewritten, "&")
				}
			}
		default:
			stmts[i].Shell = renameVarRefs(stmts[i].Shell, rename)
		}
	}
}

func firstIdent(name string) string {
	for i := 0; i < len(name); i++ {
		if !isVarIdentByte(name[i]) {
			return name[:i]
		}
	}
	return name
}
func renameVarRefs(s string, rename func(string) (string, bool)) string {
	if strings.IndexByte(s, '&') < 0 {
		return s
	}
	return scanRefs(s, '&', isVarIdentRune, isVarIdentRune, rename, false)
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
					File:       cmd.SourceFile,
					Line:       cmd.SourceLine,
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

	if !slices.Contains(p.Data.SourceFiles, p.InputFile) {
		p.Data.SourceFiles = append(p.Data.SourceFiles, p.InputFile)
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
