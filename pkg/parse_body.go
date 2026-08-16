package pkg

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

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

		if strings.HasPrefix(line, "parallel") {
			stmt, consumed, handled, err := p.parseParallelBlock(line, raw[i:], scope, lineNum)
			if err != nil {
				return nil, err
			}
			if handled {
				stmts = append(stmts, stmt)
				i += consumed
				continue
			}
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
			name, invokeArgs := parseInvokeArgs(name)
			if name == "" {
				return nil, fmt.Errorf("invoke requires a command name")
			}
			stmts = append(stmts, BodyStatement{Type: StmtInvoke, Shell: name, OutputName: outputName, InvokeArgs: invokeArgs, SourceLine: lineNum})
			i++
			continue
		}

		if line == "fail" || strings.HasPrefix(line, "fail ") {
			msg := strings.TrimSpace(strings.TrimPrefix(line, "fail"))
			if len(msg) >= 2 && msg[0] == '"' && msg[len(msg)-1] == '"' {
				msg = msg[1 : len(msg)-1]
			}
			stmts = append(stmts, BodyStatement{
				Type:       StmtFail,
				Message:    msg,
				SourceLine: lineNum,
			})
			i++
			continue
		}

		// `global name = value` writes a global from inside a command body.
		if strings.HasPrefix(line, "global ") || strings.HasPrefix(line, "global\t") {
			if err := p.parseVar("var "+strings.TrimSpace(strings.TrimPrefix(line, "global")), "global", lineNum); err != nil {
				return nil, err
			}
			i++
			continue
		}

		// `require_env KEY` fails the command when the env var is unset.
		if strings.HasPrefix(line, "require_env ") {
			rest := strings.TrimSpace(strings.TrimPrefix(line, "require_env"))
			key, msg := rest, ""
			if idx := strings.IndexAny(rest, " \t"); idx >= 0 {
				key, msg = rest[:idx], strings.TrimSpace(rest[idx:])
			}
			key = strings.Trim(key, `"`)
			if len(msg) >= 2 && msg[0] == '"' && msg[len(msg)-1] == '"' {
				msg = msg[1 : len(msg)-1]
			}
			stmts = append(stmts, BodyStatement{Type: StmtRequireEnv, Shell: key, Message: msg, SourceLine: lineNum})
			i++
			continue
		}

		// `retry<N> $ cmd` / `retry<N, 2s> $ cmd` rerun a statement up to N extra times.
		if strings.HasPrefix(line, "retry ") || strings.HasPrefix(line, "retry\t") {
			return nil, NewParseError(p.InputFile, lineNum, 1, "the positional retry form was removed — use retry<3> $ cmd, or retry<3, 2s> to back off between attempts (prefix with $ to run a shell command)", line)
		}
		if strings.HasPrefix(line, "retry<") {
			rest := strings.TrimSpace(strings.TrimPrefix(line, "retry"))
			r, mod, _, err := peelModifier(rest)
			if err != nil {
				return nil, NewParseError(p.InputFile, lineNum, 1, fmt.Sprintf("malformed retry modifier: %v", err), line)
			}
			count, backoff, ok := parseRetryModifier(mod)
			if !ok {
				return nil, NewParseError(p.InputFile, lineNum, 1, fmt.Sprintf("invalid retry modifier %q: expected <N> or <N, duration>", mod), line)
			}
			shell, outputName := extractOutputName(strings.TrimSpace(r))
			stmts = append(stmts, BodyStatement{
				Type:       StmtShell,
				Shell:      shell,
				OutputName: outputName,
				Retry:      count,
				Modifier:   backoff,
				SourceLine: lineNum,
			})
			i++
			continue
		}

		if strings.HasPrefix(line, "onfail ") || line == "onfail{" {
			stmt, consumed, err := p.parseOnFailBlock(raw[i:], scope)
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

		if strings.HasPrefix(line, "state ") || strings.HasPrefix(line, "state\t") {
			inner := strings.TrimSpace(strings.TrimPrefix(line, "state"))
			name, value, ok := strings.Cut(inner, "=")
			if !ok || strings.TrimSpace(name) == "" {
				return nil, fmt.Errorf("state declaration requires a name and a value (state name = value)")
			}
			stmts = append(stmts, BodyStatement{
				Type:       StmtState,
				Shell:      strings.TrimSpace(name),
				Message:    strings.TrimSpace(value),
				SourceLine: lineNum,
			})
			i++
			continue
		}

		if (strings.HasPrefix(line, "switch ") || strings.HasPrefix(line, "switch<")) && strings.Contains(line, "{") {
			stmt, consumed, err := p.parseSwitchBlock(raw[i:], scope)
			if err != nil {
				return nil, err
			}
			stmts = append(stmts, stmt)
			i += consumed
			continue
		}

		if strings.HasPrefix(line, "case ") || line == "case{" ||
			strings.HasPrefix(line, "default ") || line == "default{" {
			return nil, fmt.Errorf("'case'/'default' outside of a switch statement")
		}

		if strings.HasPrefix(line, "in ") && strings.Contains(line, "{") {
			stmt, consumed, err := p.parseInDirBlock(raw[i:], scope)
			if err != nil {
				return nil, err
			}
			stmts = append(stmts, stmt)
			i += consumed
			continue
		}

		if (strings.HasPrefix(line, "lock ") || strings.HasPrefix(line, "lock<")) && strings.Contains(line, "{") {
			stmt, consumed, err := p.parseLockBlock(raw[i:], scope)
			if err != nil {
				return nil, err
			}
			stmts = append(stmts, stmt)
			i += consumed
			continue
		}

		if strings.HasPrefix(line, "confirm ") {
			stmts = append(stmts, BodyStatement{
				Type:       StmtConfirm,
				Message:    trimQuoted(strings.TrimSpace(strings.TrimPrefix(line, "confirm"))),
				SourceLine: lineNum,
			})
			i++
			continue
		}

		if strings.HasPrefix(line, "prompt ") {
			stmts = append(stmts, BodyStatement{
				Type:       StmtPrompt,
				Message:    trimQuoted(strings.TrimSpace(strings.TrimPrefix(line, "prompt"))),
				SourceLine: lineNum,
			})
			i++
			continue
		}

		if strings.HasPrefix(line, "input ") {
			rest := strings.TrimSpace(strings.TrimPrefix(line, "input"))
			name, msg := rest, ""
			if idx := strings.IndexAny(rest, " \t"); idx >= 0 {
				name, msg = rest[:idx], strings.TrimSpace(rest[idx:])
			}
			name = strings.Trim(name, `"`)
			if name == "" || !isValidIdent(name) {
				return nil, fmt.Errorf("input requires a variable name")
			}
			stmts = append(stmts, BodyStatement{
				Type:       StmtInput,
				Shell:      name,
				Message:    trimQuoted(msg),
				SourceLine: lineNum,
			})
			i++
			continue
		}

		timeoutDur := ""
		if strings.HasPrefix(line, "timeout ") {
			rest := strings.TrimSpace(strings.TrimPrefix(line, "timeout"))
			if sp := strings.IndexAny(rest, " \t"); sp > 0 {
				dur := rest[:sp]
				if _, err := time.ParseDuration(dur); err == nil {
					timeoutDur = dur
					line = strings.TrimSpace(rest[sp:])
				}
			}
		}

		if builtinName, args, mod, tolerant, ok, err := parseBuiltinLine(line); ok || err != nil {
			if err != nil {
				return nil, NewParseError(p.InputFile, lineNum, 1, err.Error(), line)
			}
			if mod != "" && (builtinName != "rm" || mod != "kill") {
				return nil, NewParseError(p.InputFile, lineNum, 1, fmt.Sprintf("unknown modifier <%s> for %s (only rm<kill> is supported)", mod, builtinName), line)
			}
			stmts = append(stmts, BodyStatement{
				Type:        StmtBuiltin,
				Shell:       builtinName,
				BuiltinArgs: args,
				Modifier:    mod,
				Tolerant:    tolerant,
				Timeout:     timeoutDur,
				SourceLine:  lineNum,
			})
			i++
			continue
		}

		if line == "else" || strings.HasPrefix(line, "else ") || strings.HasPrefix(line, "else{") {
			return nil, fmt.Errorf("'else' without a matching 'if'")
		}

		for _, kw := range headerOnlyKeywords {
			if line == kw || strings.HasPrefix(line, kw+" ") || strings.HasPrefix(line, kw+"\t") {
				return nil, NewParseError(p.InputFile, lineNum, 1, fmt.Sprintf("`%s` belongs in the command header or at the top level, not in a body — prefix the line with $ to run it in the shell", kw), line)
			}
		}

		shell, outputName := extractOutputName(line)
		stmts = append(stmts, BodyStatement{Type: StmtShell, Shell: shell, OutputName: outputName, Timeout: timeoutDur, SourceLine: lineNum})
		i++
	}
	return stmts, nil
}

var builtinCommands = []string{"cp", "rm", "mkdir", "touch", "download", "extract"}

var headerOnlyKeywords = []string{"manual", "produces", "container", "onchange", "import"}

func parseBuiltinLine(line string) (name, args, mod string, tolerant, ok bool, err error) {
	rest := line
	if strings.HasPrefix(rest, "!") {
		tolerant = true
		rest = strings.TrimSpace(rest[1:])
	}
	for _, b := range builtinCommands {
		if strings.HasPrefix(rest, b+" ") || rest == b {
			return b, strings.TrimSpace(rest[len(b):]), "", tolerant, true, nil
		}
		if strings.HasPrefix(rest, b+"<") {
			r, m, _, perr := peelModifier(rest[len(b):])
			if perr != nil {
				return "", "", "", false, true, fmt.Errorf("%s modifier: %v", b, perr)
			}
			return b, strings.TrimSpace(r), m, tolerant, true, nil
		}
	}
	return "", "", "", false, false, nil
}

func trimQuoted(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
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

func findBlockBounds(line string) (open, close int, ok bool) {
	inQuote := false
	depth := 0
	open = -1
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '"':
			inQuote = !inQuote
		case '{':
			if !inQuote {
				if open < 0 {
					open = i
				}
				depth++
			}
		case '}':
			if !inQuote && open >= 0 {
				depth--
				if depth == 0 {
					return open, i, true
				}
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

func (p *Parser) parseOnFailBlock(raw []rawLine, scope string) (BodyStatement, int, error) {
	headerLine := raw[0]
	stmt := BodyStatement{Type: StmtOnFail, SourceLine: headerLine.num}

	if body, ok := singleLineBody(headerLine.text); ok {
		bodyStmts, err := p.parseBodyStatements(atLine(splitStatements(body), headerLine.num), scope)
		if err != nil {
			return BodyStatement{}, 0, err
		}
		stmt.OnFailBody = bodyStmts
		return stmt, 1, nil
	}

	lines, endIdx, err := collectBodyLines(raw, 1)
	if err != nil {
		return BodyStatement{}, 0, fmt.Errorf("unclosed onfail block (missing '}')")
	}
	bodyStmts, err := p.parseBodyStatements(lines, scope)
	if err != nil {
		return BodyStatement{}, 0, err
	}
	stmt.OnFailBody = bodyStmts
	return stmt, endIdx, nil
}

func (p *Parser) parseSwitchBlock(raw []rawLine, scope string) (BodyStatement, int, error) {
	headerLine := raw[0]
	header := strings.TrimSpace(headerLine.text)
	header = strings.TrimPrefix(header, "switch")

	modifier := ""
	if rest, mod, has, err := peelModifier(header); has || err != nil {
		if err != nil {
			return BodyStatement{}, 0, fmt.Errorf("malformed switch modifier: %v", err)
		}
		if mod != "strict" {
			return BodyStatement{}, 0, fmt.Errorf("unknown switch modifier %q (only \"strict\" is allowed)", mod)
		}
		modifier = mod
		header = rest
	}

	before, _, ok := strings.Cut(header, "{")
	if !ok {
		return BodyStatement{}, 0, fmt.Errorf("malformed switch: missing '{'")
	}
	expr := strings.TrimSpace(before)
	if expr == "" {
		return BodyStatement{}, 0, fmt.Errorf("switch requires an expression")
	}

	lines, endIdx, err := collectBodyLines(raw, 1)
	if err != nil {
		return BodyStatement{}, 0, fmt.Errorf("unclosed switch block (missing '}')")
	}

	stmt := BodyStatement{Type: StmtSwitch, SwitchExpr: expr, Modifier: modifier, SourceLine: headerLine.num}
	seen := make(map[string]bool)
	j := 0
	for j < len(lines) {
		l := lines[j]
		trimmed := strings.TrimSpace(l.text)
		var caseStmt SwitchCase
		switch {
		case strings.HasPrefix(trimmed, "case "):
			rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "case"))
			before, _, ok := strings.Cut(rest, "{")
			if !ok {
				return BodyStatement{}, 0, fmt.Errorf("malformed case: missing '{'")
			}
			for _, v := range strings.Split(before, ",") {
				v = trimQuoted(strings.TrimSpace(v))
				if v == "" {
					continue
				}
				if seen[v] {
					return BodyStatement{}, 0, fmt.Errorf("duplicate case value %q", v)
				}
				seen[v] = true
				caseStmt.Values = append(caseStmt.Values, v)
			}
			caseStmt.SourceLine = l.num
		case strings.HasPrefix(trimmed, "default"):
			if !strings.Contains(trimmed, "{") {
				return BodyStatement{}, 0, fmt.Errorf("malformed default: missing '{'")
			}
			caseStmt.IsDefault = true
			caseStmt.SourceLine = l.num
		default:
			return BodyStatement{}, 0, fmt.Errorf("expected 'case' or 'default' in switch, got %q", trimmed)
		}

		var bodyLines []rawLine
		if body, ok := singleLineBody(l.text); ok {
			bodyLines = atLine(splitStatements(body), l.num)
			j++
		} else {
			var err error
			bodyLines, j, err = collectBodyLines(lines, j+1)
			if err != nil {
				return BodyStatement{}, 0, fmt.Errorf("unclosed case body (missing '}')")
			}
		}
		bodyStmts, err := p.parseBodyStatements(bodyLines, scope)
		if err != nil {
			return BodyStatement{}, 0, err
		}
		caseStmt.Body = bodyStmts
		stmt.Cases = append(stmt.Cases, caseStmt)
	}

	if len(stmt.Cases) == 0 {
		return BodyStatement{}, 0, fmt.Errorf("switch requires at least one case")
	}
	return stmt, endIdx, nil
}

func (p *Parser) parseInDirBlock(raw []rawLine, scope string) (BodyStatement, int, error) {
	headerLine := raw[0]
	header := strings.TrimSpace(headerLine.text)
	header = strings.TrimPrefix(header, "in")

	before, _, ok := strings.Cut(header, "{")
	if !ok {
		return BodyStatement{}, 0, fmt.Errorf("malformed 'in' block: missing '{'")
	}
	dir := strings.TrimSpace(before)
	if dir == "" {
		return BodyStatement{}, 0, fmt.Errorf("'in' block requires a directory")
	}

	if body, ok := singleLineBody(raw[0].text); ok {
		bodyStmts, err := p.parseBodyStatements(atLine(splitStatements(body), headerLine.num), scope)
		if err != nil {
			return BodyStatement{}, 0, err
		}
		return BodyStatement{Type: StmtInDir, Shell: dir, ThenBody: bodyStmts, SourceLine: headerLine.num}, 1, nil
	}

	bodyLines, endIdx, err := collectBodyLines(raw, 1)
	if err != nil {
		return BodyStatement{}, 0, fmt.Errorf("unclosed 'in' block (missing '}')")
	}
	bodyStmts, err := p.parseBodyStatements(bodyLines, scope)
	if err != nil {
		return BodyStatement{}, 0, err
	}
	return BodyStatement{Type: StmtInDir, Shell: dir, ThenBody: bodyStmts, SourceLine: headerLine.num}, endIdx, nil
}

func (p *Parser) parseLockBlock(raw []rawLine, scope string) (BodyStatement, int, error) {
	headerLine := raw[0]
	header := strings.TrimSpace(headerLine.text)
	header = strings.TrimPrefix(header, "lock")

	modifier := ""
	if rest, mod, has, err := peelModifier(header); has || err != nil {
		if err != nil {
			return BodyStatement{}, 0, fmt.Errorf("malformed lock modifier: %v", err)
		}
		if _, derr := parseModifierDuration("lock", mod); derr != nil {
			return BodyStatement{}, 0, derr
		}
		modifier = mod
		header = rest
	}

	before, _, ok := strings.Cut(header, "{")
	if !ok {
		return BodyStatement{}, 0, fmt.Errorf("malformed lock: missing '{'")
	}
	name := trimQuoted(strings.TrimSpace(before))
	if name == "" {
		return BodyStatement{}, 0, fmt.Errorf("lock requires a name")
	}

	if body, ok := singleLineBody(raw[0].text); ok {
		bodyStmts, err := p.parseBodyStatements(atLine(splitStatements(body), headerLine.num), scope)
		if err != nil {
			return BodyStatement{}, 0, err
		}
		return BodyStatement{Type: StmtLock, Shell: name, Modifier: modifier, ThenBody: bodyStmts, SourceLine: headerLine.num}, 1, nil
	}

	bodyLines, endIdx, err := collectBodyLines(raw, 1)
	if err != nil {
		return BodyStatement{}, 0, fmt.Errorf("unclosed lock block (missing '}')")
	}
	bodyStmts, err := p.parseBodyStatements(bodyLines, scope)
	if err != nil {
		return BodyStatement{}, 0, err
	}
	return BodyStatement{Type: StmtLock, Shell: name, Modifier: modifier, ThenBody: bodyStmts, SourceLine: headerLine.num}, endIdx, nil
}

func parseInvokeArgs(s string) (name string, args []string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	name = s
	if idx := strings.IndexAny(s, " \t"); idx >= 0 {
		name, s = s[:idx], strings.TrimSpace(s[idx:])
	}
	if s == "" {
		return name, nil
	}

	var pairs []string
	var cur strings.Builder
	inQuote := false
	flush := func() {
		if p := strings.TrimSpace(cur.String()); p != "" {
			if strings.Contains(p, "=") {
				pairs = append(pairs, p)
			}
		}
		cur.Reset()
	}
	for _, r := range s {
		switch r {
		case '"':
			inQuote = !inQuote
			cur.WriteRune(r)
		case ',':
			if !inQuote {
				flush()
			} else {
				cur.WriteRune(r)
			}
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return name, pairs
}

func (p *Parser) parseParallelBlock(line string, raw []rawLine, scope string, lineNum int) (BodyStatement, int, bool, error) {
	rest := strings.TrimLeft(line[len("parallel"):], " \t")
	if rest == "" {
		return BodyStatement{}, 0, false, nil
	}
	jobs := 0
	hasModifier := false
	if rest[0] == '<' {
		end := strings.IndexByte(rest, '>')
		if end < 0 {
			return BodyStatement{}, 0, true, fmt.Errorf("malformed parallel modifier: missing '>'")
		}
		n, err := strconv.Atoi(rest[1:end])
		if err != nil || n <= 0 {
			return BodyStatement{}, 0, true, fmt.Errorf("malformed parallel modifier %q: expected a positive integer", rest[:end+1])
		}
		jobs = n
		rest = rest[end+1:]
		hasModifier = true
	}
	rest = strings.TrimLeft(rest, " \t")
	if !strings.HasPrefix(rest, "for ") && !strings.HasPrefix(rest, "matrix ") {
		if hasModifier {
			return BodyStatement{}, 0, true, fmt.Errorf("the <N> modifier only applies to parallel for/matrix loops (got %q)", firstWord(rest))
		}
		return BodyStatement{}, 0, false, nil
	}

	saved := raw[0].text
	raw[0].text = rest
	var (
		stmt     BodyStatement
		consumed int
		err      error
	)
	if strings.HasPrefix(rest, "for ") {
		stmt, consumed, err = p.parseForBlock(raw, scope)
	} else {
		stmt, consumed, err = p.parseMatrixBlock(raw, scope)
	}
	raw[0].text = saved
	if err != nil {
		return BodyStatement{}, 0, true, err
	}
	stmt.Parallel = true
	stmt.ParallelJobs = jobs
	return stmt, consumed, true, nil
}

// peelModifier strips a leading "<...>" keyword modifier.
func peelModifier(rest string) (rest2, mod string, ok bool, err error) {
	if !strings.HasPrefix(rest, "<") {
		return rest, "", false, nil
	}
	end := strings.IndexByte(rest, '>')
	if end < 0 {
		return "", "", true, fmt.Errorf("missing '>'")
	}
	mod = rest[1:end]
	if mod == "" {
		return "", "", true, fmt.Errorf("empty modifier")
	}
	return rest[end+1:], mod, true, nil
}

func parseRetryModifier(mod string) (int, string, bool) {
	countPart, backoff, hasBackoff := strings.Cut(mod, ",")
	n, err := strconv.Atoi(strings.TrimSpace(countPart))
	if err != nil || n <= 0 {
		return 0, "", false
	}
	if !hasBackoff {
		return n, "", true
	}
	backoff = strings.TrimSpace(backoff)
	if d, derr := time.ParseDuration(backoff); derr != nil || d <= 0 {
		return 0, "", false
	}
	return n, backoff, true
}

func parseModifierDuration(kw, mod string) (time.Duration, error) {
	d, err := time.ParseDuration(mod)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("invalid %s<%s> modifier: expected a positive duration", kw, mod)
	}
	return d, nil
}

func firstWord(s string) string {
	s = strings.TrimSpace(s)
	if sp := strings.IndexAny(s, " \t"); sp >= 0 {
		return s[:sp]
	}
	return s
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
		before, after, ok := strings.Cut(clause, " in ")
		if !ok {
			return BodyStatement{}, 0, fmt.Errorf("malformed matrix clause %q: missing 'in'", clause)
		}
		v := strings.TrimSpace(before)
		it := strings.TrimSpace(after)
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
		return nestMatrixLoops(vars, items, bodyStmts, headerLine.num), 1, nil
	}

	bodyLines, endIdx, err := collectBodyLines(raw, 1)
	if err != nil {
		return BodyStatement{}, 0, fmt.Errorf("unclosed matrix block (missing '}')")
	}

	bodyStmts, err := p.parseBodyStatements(bodyLines, scope)
	if err != nil {
		return BodyStatement{}, 0, err
	}
	return nestMatrixLoops(vars, items, bodyStmts, headerLine.num), endIdx, nil
}

func nestMatrixLoops(vars, items []string, body []BodyStatement, lineNum int) BodyStatement {
	stmt := BodyStatement{
		Type:       StmtFor,
		LoopVar:    vars[len(vars)-1],
		LoopItems:  items[len(items)-1],
		LoopBody:   body,
		SourceLine: lineNum,
	}
	for i := len(vars) - 2; i >= 0; i-- {
		stmt = BodyStatement{
			Type:       StmtFor,
			LoopVar:    vars[i],
			LoopItems:  items[i],
			LoopBody:   []BodyStatement{stmt},
			SourceLine: lineNum,
		}
	}
	return stmt
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
	return (strings.HasPrefix(t, "if ") || strings.HasPrefix(t, "for ") || strings.HasPrefix(t, "matrix ") ||
		strings.HasPrefix(t, "env ") || strings.HasPrefix(t, "onfail ") ||
		strings.HasPrefix(t, "switch ") || strings.HasPrefix(t, "switch<") || strings.HasPrefix(t, "case ") ||
		strings.HasPrefix(t, "default") || strings.HasPrefix(t, "in ") ||
		strings.HasPrefix(t, "lock ") || strings.HasPrefix(t, "lock<") || strings.HasPrefix(t, "case{") ||
		strings.HasPrefix(t, "parallel")) && strings.Contains(t, "{")
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
