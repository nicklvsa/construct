package pkg

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"unicode"
)

type Parser struct {
	InputFile   string
	Data        *ParsedData
	Lines       []string
	importStack map[string]bool // recursion path, for cycle detection
	imported    map[string]bool // files already merged, for diamond dedup

	ImportReader func(path string) ([]byte, error)
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
		Lines:     strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n"),
	}
}

func (p *Parser) stateDeclLookup(name string) (string, bool) {
	for _, d := range p.Data.StateDecls {
		if d.Name == name {
			return d.Value, true
		}
	}
	return "", false
}

func (p *Parser) tryEvalExpression(expression string, varName *string, varScope *string, lineNum int) string {
	expression = strings.TrimSpace(expression)
	expression = resolveStateRefsWith(expression, p.stateDeclLookup)

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
				name := envName.String()
				def := ""
				hasDefault := false
				// @ENV:-default expands to default when the variable is unset.
				if j+1 < len(runes) && runes[j] == ':' && runes[j+1] == '-' {
					j += 2
					defStart := j
					for j < len(runes) && !isEnvDefaultEnd(runes[j]) {
						j++
					}
					def = string(runes[defStart:j])
					hasDefault = true
				}
				if val, ok := os.LookupEnv(name); ok {
					result.WriteString(val)
				} else if hasDefault {
					result.WriteString(def)
				}
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
				if variable, err := p.Data.GetVariable(refName.String(), scope); err == nil {
					if variable.IsList {
						result.WriteString(strings.Join(variable.List, ", "))
					} else {
						result.WriteString(variable.Value)
					}
				}
				i = j
				continue
			}
		}

		if char == '$' && varName != nil && varScope != nil {
			restOfLine := strings.TrimSpace(string(runes[i+1:]))
			p.Data.addCommand(&Command{
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

type parserEvalContext struct {
	p     *Parser
	scope string
}

func (c parserEvalContext) LookupVar(name string) (Value, bool) {
	return LookupVariableIndexed(c.p.Data, name, c.scope)
}

func (c parserEvalContext) LookupEnv(name string) (string, bool) {
	return os.LookupEnv(name)
}

func (c parserEvalContext) LookupState(name string) (string, bool) {
	return c.p.stateDeclLookup(name)
}

func (c parserEvalContext) BaseDir() string {
	return importBaseDir(c.p.InputFile)
}

func (p *Parser) evalVarValue(value string, varName *string, varScope *string, lineNum int) (string, bool, []string, error) {
	value = strings.TrimSpace(value)
	ctx := parserEvalContext{p: p, scope: *varScope}
	if v, ok, err := evalValueExpr(value, ctx); ok {
		if err != nil {
			return "", false, nil, err
		}
		if v.IsList {
			return v.String(), true, v.L, nil
		}
		return v.S, false, nil, nil
	}

	if strings.IndexByte(value, '&') >= 0 {
		value = resolveVarRefs(value, func(name string) (string, bool) {
			v, ok := LookupVariableIndexed(p.Data, name, *varScope)
			if !ok {
				return "", false
			}
			return v.String(), true
		})
	}
	return p.tryEvalExpression(value, varName, varScope, lineNum), false, nil, nil
}

func (p *Parser) parseVar(line string, scope string, lineNum int) error {
	pieces := strings.SplitN(line, "=", 2)

	variableName := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(pieces[0]), "var"))
	if variableName == "" {
		return fmt.Errorf("variable declaration is missing a name: %q", line)
	}

	var variableValue string
	var isList bool
	var list []string
	var refs []string
	if len(pieces) > 1 {
		var err error
		refs = VarRefNames(pieces[1])
		variableValue, isList, list, err = p.evalVarValue(pieces[1], &variableName, &scope, lineNum)
		if err != nil {
			return fmt.Errorf("variable %q: %w", variableName, err)
		}
	}

	p.Data.addVariable(&Variable{
		Name:   variableName,
		Value:  variableValue,
		Scope:  scope,
		IsList: isList,
		List:   list,
		refs:   refs,
	})

	return nil
}

func (p *Parser) parseCommand(idx int, line string, isDefault, manual bool, lineNum int, description string) (int, error) {
	trimmedLine := strings.TrimSpace(line)
	if !strings.Contains(trimmedLine, "{") {
		return 0, nil
	}

	commandName := ParseCommandName(line)
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
	container := extractContainer(line)
	timeout := extractTimeout(line)
	produces := extractProduces(line)
	onChange := extractOnChange(line)

	var commandBody []BodyStatement
	consumed := 1
	if body, ok := singleLineBody(trimmedLine); ok {
		commandBody, err = p.parseBodyStatements(atLine(splitStatements(body), lineNum), commandName)
		if err != nil {
			return 0, err
		}
	} else {
		rawBody, endIdx, err := p.parseCommandBody(idx+1, commandName)
		if err != nil {
			return 0, err
		}
		commandBody, err = p.parseBodyStatements(rawBody, commandName)
		if err != nil {
			return 0, err
		}
		consumed = endIdx - idx
	}

	if commandName != "" {
		p.Data.addCommand(&Command{
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
			Container:       container,
			Manual:          manual,
			Timeout:         timeout,
			Produces:        produces,
			OnChange:        onChange,
			Body:            commandBody,
		})
	}

	return consumed, nil
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

		if strings.HasPrefix(line, "state ") {
			inner := strings.TrimSpace(strings.TrimPrefix(line, "state"))
			name, value, ok := strings.Cut(inner, "=")
			if !ok || strings.TrimSpace(name) == "" {
				return p.parseErr(lineNum, fmt.Errorf("state declaration requires a name and a value (state name = value)"), line)
			}
			name = strings.TrimSpace(name)
			scope := "global"
			val, isList, list, err := p.evalVarValue(strings.TrimSpace(value), &name, &scope, lineNum)
			if err != nil {
				return p.parseErr(lineNum, err, line)
			}
			val = trimQuoted(val)
			p.Data.StateDecls = append(p.Data.StateDecls, &Variable{Name: name, Value: val, Scope: "global", IsList: isList, List: list})
			pendingComment = nil
			idx++
			continue
		}

		header, manual := StripManual(line)
		cmdLine := strings.TrimSpace(header)
		isDefault := strings.HasPrefix(cmdLine, "_") &&
			(len(cmdLine) == 1 || cmdLine[1] == ' ' || cmdLine[1] == '\t' ||
				cmdLine[1] == '(' || cmdLine[1] == '<' || cmdLine[1] == '{')

		consumed, err := p.parseCommand(idx, header, isDefault, manual, lineNum, strings.Join(pendingComment, "\n"))
		if err != nil {
			return p.parseErr(lineNum, err, line)
		}
		pendingComment = nil
		if consumed == 0 {
			return p.parseErr(lineNum, fmt.Errorf("unrecognized top-level statement %q (expected var, import, state, or a command)", firstWord(line)), line)
		}
		idx += consumed
	}

	return nil
}

func trimDocMarker(line string) string {
	line = strings.TrimSpace(line)
	line = strings.TrimLeft(line, "#/")
	return strings.TrimSpace(line)
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
	p.computeCacheGlobals()
	p.collectIndexedOutputRefs()

	if err := p.detectCircularDependencies(); err != nil {
		return nil, err
	}

	return p.Data, nil
}
