package pkg

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type LintIssue struct {
	Line     int    `json:"line"`
	Col      int    `json:"col"`
	EndCol   int    `json:"end_col"`
	Severity int    `json:"severity"`
	Message  string `json:"message"`
}

const (
	LintInfo = iota
	LintWarning
	LintError
)

func severityLabel(sev int) string {
	switch sev {
	case LintError:
		return "error"
	case LintWarning:
		return "warning"
	default:
		return "info"
	}
}

func FormatLintIssue(file string, i LintIssue) string {
	return fmt.Sprintf("%s:%d:%d: %s: %s", file, i.Line+1, i.Col+1, severityLabel(i.Severity), i.Message)
}

func Lint(lines []string, data *ParsedData, baseDir string) []LintIssue {
	var issues []LintIssue
	issues = append(issues, lintRefRules(lines, data)...)
	issues = append(issues, lintDuplicatePrereqs(lines, data)...)
	issues = append(issues, lintMissingFileDeps(data, baseDir)...)
	issues = append(issues, lintUnusedGlobals(data)...)
	issues = append(issues, lintUnreferencedCommands(data)...)
	issues = append(issues, lintHeaderKeywordMisuse(lines, data)...)
	issues = append(issues, lintStatementPrefixes(lines)...)
	issues = append(issues, lintLoopControl(data)...)
	issues = append(issues, lintRefTrailingHyphen(lines, data)...)
	issues = append(issues, lintStatementKeywordCommands(data)...)
	issues = append(issues, lintUnknownVarRefs(lines, data)...)
	issues = append(issues, lintSwitchAndOutputs(data)...)
	return issues
}

// knownRefNames unions every name a &ref can resolve to, across commands.
func knownRefNames(data *ParsedData) map[string]bool {
	known := map[string]bool{"last": true, "fail": true}
	for _, v := range data.Variables {
		known[v.Name] = true
	}
	for _, s := range data.StateDecls {
		known[s.Name] = true
	}
	var walk func(body []BodyStatement)
	walk = func(body []BodyStatement) {
		for _, stmt := range body {
			switch stmt.Type {
			case StmtFor:
				known[stmt.LoopVar] = true
				if stmt.LoopIndex != "" {
					known[stmt.LoopIndex] = true
				}
				walk(stmt.LoopBody)
			case StmtEnv:
				for _, pair := range stmt.Env {
					if k, _, ok := strings.Cut(pair, "="); ok {
						known[strings.TrimSpace(k)] = true
					}
				}
			case StmtInput:
				known[stmt.Shell] = true
			case StmtInvoke:
				if stmt.OutputName != "" {
					known[stmt.OutputName] = true
				}
			case StmtIf:
				walk(stmt.ThenBody)
				walk(stmt.ElseBody)
			case StmtOnFail:
				walk(stmt.OnFailBody)
			case StmtSwitch:
				for _, c := range stmt.Cases {
					walk(c.Body)
				}
			case StmtInDir, StmtLock:
				walk(stmt.ThenBody)
			}
		}
	}
	for _, cmd := range data.Commands {
		for _, arg := range cmd.Arguments {
			known[arg.Name] = true
		}
		walk(cmd.Body)
	}
	return known
}

func lintUnknownVarRefs(lines []string, data *ParsedData) []LintIssue {
	known := knownRefNames(data)
	var issues []LintIssue
	for lineIdx, raw := range lines {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		searchFrom := 0
		for {
			ampIdx := strings.IndexByte(raw[searchFrom:], '&')
			if ampIdx < 0 {
				break
			}
			absIdx := searchFrom + ampIdx
			searchFrom = absIdx + 1
			if absIdx > 0 && raw[absIdx-1] == '\\' {
				continue
			}
			name, nameLen := refNameAt(raw, absIdx)
			if name == "" {
				continue
			}
			searchFrom = absIdx + 1 + nameLen
			first := name
			if dot := strings.IndexByte(name, '.'); dot > 0 {
				first = name[:dot]
			}
			if known[name] || known[first] {
				continue
			}
			if _, err := data.GetCommand(first); err == nil {
				continue // prereq-output ref; other rules cover it
			}
			issues = append(issues, LintIssue{
				Line: lineIdx, Col: absIdx, EndCol: absIdx + nameLen,
				Severity: LintWarning,
				Message:  fmt.Sprintf("unknown reference `&%s` — no variable, argument, or command matches; it substitutes to empty", name),
			})
		}
	}
	return issues
}

func lintSwitchAndOutputs(data *ParsedData) []LintIssue {
	var issues []LintIssue
	var walk func(body []BodyStatement)
	walk = func(body []BodyStatement) {
		seen := map[string]bool{}
		for _, stmt := range body {
			switch stmt.Type {
			case StmtShell:
				if stmt.OutputName != "" {
					if seen[stmt.OutputName] {
						issues = append(issues, LintIssue{
							Line: max(stmt.SourceLine-1, 0), Col: 0, EndCol: 0,
							Severity: LintError,
							Message:  fmt.Sprintf("duplicate named output %q — `&cmd.%s` resolves to only one of them", stmt.OutputName, stmt.OutputName),
						})
					}
					seen[stmt.OutputName] = true
				}
			case StmtInvoke:
				if stmt.OutputName != "" {
					seen[stmt.OutputName] = true
				}
			case StmtSwitch:
				for _, c := range stmt.Cases {
					if !c.IsDefault && len(c.Values) == 0 {
						issues = append(issues, LintIssue{
							Line: max(c.SourceLine-1, 0), Col: 0, EndCol: 0,
							Severity: LintWarning,
							Message:  "case without values never matches",
						})
					}
					walk(c.Body)
				}
			case StmtIf:
				walk(stmt.ThenBody)
				walk(stmt.ElseBody)
			case StmtFor:
				walk(stmt.LoopBody)
			case StmtOnFail:
				walk(stmt.OnFailBody)
			case StmtInDir, StmtLock:
				walk(stmt.ThenBody)
			}
		}
	}
	for _, cmd := range data.Commands {
		walk(cmd.Body)
	}
	return issues
}

var statementKeywordNames = map[string]bool{
	"if": true, "else": true, "for": true, "matrix": true, "parallel": true,
	"in": true, "switch": true, "case": true, "default": true, "env": true,
	"invoke": true, "fail": true, "onfail": true, "require_env": true,
	"retry": true, "confirm": true, "prompt": true, "input": true,
	"global": true, "var": true, "state": true, "lock": true,
	"continue": true, "break": true, "manual": true, "produces": true,
	"container": true, "onchange": true, "import": true,
}

func lintStatementKeywordCommands(data *ParsedData) []LintIssue {
	var issues []LintIssue
	for _, cmd := range data.Commands {
		if !statementKeywordNames[cmd.Name] {
			continue
		}
		issues = append(issues, LintIssue{
			Line: max(cmd.SourceLine-1, 0), Col: 0, EndCol: len(cmd.Name),
			Severity: LintError,
			Message:  fmt.Sprintf("`%s` is a statement keyword — this line defines a command literally named `%s`; the statement belongs inside a command body", cmd.Name, cmd.Name),
		})
	}
	return issues
}

func lintHeaderKeywordMisuse(lines []string, data *ParsedData) []LintIssue {
	var issues []LintIssue
	for _, cmd := range data.Commands {
		lineIdx := cmd.SourceLine - 1
		if lineIdx < 0 || lineIdx >= len(lines) {
			continue
		}
		line := lines[lineIdx]
		lt := strings.IndexByte(line, '<')
		brace := strings.IndexByte(line, '{')
		if brace < 0 {
			brace = len(line)
		}

		if lt >= 0 && brace > lt {
			segment := line[lt+1 : brace]
			for _, kw := range []string{"produces", "container", "timeout"} {
				for _, tok := range strings.FieldsFunc(segment, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' }) {
					if tok != kw {
						continue
					}
					if _, err := data.GetCommand(kw); err == nil {
						continue
					}
					col := strings.Index(line[lt:], " "+kw)
					if col < 0 {
						col = 0
					} else {
						col += lt + 1
					}
					issues = append(issues, LintIssue{
						Line: lineIdx, Col: col, EndCol: col + len(kw),
						Severity: LintError,
						Message:  fmt.Sprintf("`%s` after the prerequisite list is not a modifier — write `%s produces <artifact> < prereqs`-style headers (here `%s` is treated as a prerequisite)", kw, cmd.Name, kw),
					})
				}
			}
		}

		if ti := findTopLevelKeyword(line, " timeout "); ti >= 0 && ti < brace {
			seg := line[ti+len(" timeout "):]
			for _, stop := range []string{"<", "{", " produces ", " onchange ", " container ", " in "} {
				if c := strings.Index(seg, stop); c >= 0 {
					seg = seg[:c]
				}
			}
			seg = strings.TrimSpace(seg)
			if seg != "" {
				if _, err := time.ParseDuration(seg); err != nil {
					issues = append(issues, LintIssue{
						Line: lineIdx, Col: ti, EndCol: ti + len(" timeout "),
						Severity: LintError,
						Message:  fmt.Sprintf("invalid timeout duration %q (expected e.g. 30s, 5m)", seg),
					})
				}
			}
		}
	}
	return issues
}

func lintStatementPrefixes(lines []string) []LintIssue {
	var issues []LintIssue
	for lineIdx, raw := range lines {
		line := strings.TrimSpace(raw)
		if !strings.HasPrefix(line, "timeout ") {
			continue
		}
		rest := strings.TrimSpace(line[len("timeout"):])
		sp := strings.IndexAny(rest, " \t")
		if sp <= 0 {
			continue
		}
		value, tail := rest[:sp], strings.TrimSpace(rest[sp:])
		if !strings.HasPrefix(tail, "$") && !strings.HasPrefix(tail, "! $") {
			continue // likely a shell command that happens to start with the keyword
		}
		col := strings.Index(raw, value)
		if col < 0 {
			col = 0
		}
		if _, err := time.ParseDuration(value); err != nil {
			issues = append(issues, LintIssue{
				Line: lineIdx, Col: col, EndCol: col + len(value),
				Severity: LintError,
				Message:  fmt.Sprintf("invalid timeout duration %q — the statement runs as a plain shell line (expected e.g. `timeout 30s $ cmd`)", value),
			})
		}
	}
	return issues
}

func lintLoopControl(data *ParsedData) []LintIssue {
	var issues []LintIssue
	var walk func(body []BodyStatement, inLoop, parallel bool)
	walk = func(body []BodyStatement, inLoop, parallel bool) {
		for _, stmt := range body {
			switch stmt.Type {
			case StmtContinue, StmtBreak:
				if !inLoop {
					issues = append(issues, LintIssue{
						Line: max(stmt.SourceLine-1, 0), Col: 0, EndCol: 0,
						Severity: LintError,
						Message:  fmt.Sprintf("`%s` outside a loop", stmt.Type),
					})
				} else if stmt.Type == StmtBreak && parallel {
					issues = append(issues, LintIssue{
						Line: max(stmt.SourceLine-1, 0), Col: 0, EndCol: 0,
						Severity: LintError,
						Message:  "`break` cannot stop the concurrent iterations of a parallel loop — use `continue if` to skip items instead",
					})
				}
			case StmtIf:
				walk(stmt.ThenBody, inLoop, parallel)
				walk(stmt.ElseBody, inLoop, parallel)
			case StmtFor:
				walk(stmt.LoopBody, true, stmt.Parallel)
			case StmtOnFail:
				walk(stmt.OnFailBody, inLoop, parallel)
			case StmtSwitch:
				for _, c := range stmt.Cases {
					walk(c.Body, inLoop, parallel)
				}
			case StmtInDir, StmtLock:
				walk(stmt.ThenBody, inLoop, parallel)
			}
		}
	}
	for _, cmd := range data.Commands {
		walk(cmd.Body, false, false)
	}
	return issues
}

func lintRefTrailingHyphen(lines []string, data *ParsedData) []LintIssue {
	known := func(name string) bool {
		if _, err := data.GetCommand(name); err == nil {
			return true
		}
		for _, v := range data.Variables {
			if v.Name == name {
				return true
			}
		}
		return false
	}
	var issues []LintIssue
	for lineIdx, line := range lines {
		searchFrom := 0
		for {
			ampIdx := strings.IndexByte(line[searchFrom:], '&')
			if ampIdx < 0 {
				break
			}
			absIdx := searchFrom + ampIdx
			name, nameLen := refNameAt(line, absIdx)
			searchFrom = absIdx + 1
			if name == "" || !strings.HasSuffix(name, "-") {
				continue
			}
			searchFrom = absIdx + 1 + nameLen
			base := strings.TrimSuffix(name, "-")
			if base == "" || known(name) || !known(base) {
				continue
			}
			issues = append(issues, LintIssue{
				Line: lineIdx, Col: absIdx, EndCol: absIdx + nameLen,
				Severity: LintWarning,
				Message:  fmt.Sprintf("`&%s` includes the trailing `-` in the reference name — did you mean `&%s -`?", name, base),
			})
		}
	}
	return issues
}

func lintRefRules(lines []string, data *ParsedData) []LintIssue {
	var issues []LintIssue
	for lineIdx, line := range lines {
		searchFrom := 0
		for {
			ampIdx := strings.IndexByte(line[searchFrom:], '&')
			if ampIdx < 0 {
				break
			}
			absIdx := searchFrom + ampIdx
			name, nameLen := refNameAt(line, absIdx)
			if name == "" {
				searchFrom = absIdx + 1
				continue
			}
			searchFrom = absIdx + 1 + len(name)

			cmdName, suffix, ok := SplitCommandRef(data, name)
			if !ok {
				continue // plain &var, not a prereq output ref
			}
			cmd, err := data.GetCommand(cmdName)
			if err != nil || cmd == nil {
				continue
			}

			shellCount := len(ShellStatements(cmd.Body))
			if idx, err := strconv.Atoi(suffix); err == nil {
				if idx < 0 || idx >= shellCount {
					issues = append(issues, LintIssue{
						Line: lineIdx, Col: absIdx, EndCol: absIdx + nameLen,
						Severity: LintError,
						Message:  fmt.Sprintf("index %d out of bounds: `%s` has %d output line(s)", idx, cmdName, shellCount),
					})
					continue
				}
				for shellIdx, stmt := range ShellStatements(cmd.Body) {
					if stmt.OutputName != "" && shellIdx == idx {
						issues = append(issues, LintIssue{
							Line: lineIdx, Col: absIdx, EndCol: absIdx + nameLen,
							Severity: LintInfo,
							Message:  fmt.Sprintf("💡 named output available: use &%s.%s instead of &%s", cmdName, stmt.OutputName, name),
						})
						break
					}
				}
			} else if invoked, _, found := InvokeCaptureHint(cmd.Body, suffix); found {
				issues = append(issues, LintIssue{
					Line: lineIdx, Col: absIdx, EndCol: absIdx + nameLen,
					Severity: LintWarning,
					Message:  fmt.Sprintf("`%s` is captured by `invoke %s as %s` — visible only inside `%s`", suffix, invoked, suffix, cmdName),
				})
			} else if !HasNamedOutput(cmd.Body, suffix) {
				issues = append(issues, LintIssue{
					Line: lineIdx, Col: absIdx, EndCol: absIdx + nameLen,
					Severity: LintError,
					Message:  fmt.Sprintf("unknown named output `%s` on command `%s`", suffix, cmdName),
				})
			}
		}
	}
	return issues
}

func refNameAt(s string, i int) (string, int) {
	runes := []rune(s[i:])
	j := 1
	for j < len(runes) && isVarIdentRune(runes[j]) {
		j++
	}
	if j == 1 {
		return "", 0
	}
	for j < len(runes) && runes[j] == '.' && j+1 < len(runes) && isPlainRune(runes[j+1]) {
		j++
		for j < len(runes) && isPlainRune(runes[j]) {
			j++
		}
	}
	name := string(runes[1:j])
	return name, len(name)
}

func lintDuplicatePrereqs(lines []string, data *ParsedData) []LintIssue {
	var issues []LintIssue
	for lineIdx, line := range lines {
		lt := strings.IndexByte(line, '<')
		if lt < 0 {
			continue
		}
		brace := strings.IndexByte(line[lt:], '{')
		if brace < 0 {
			continue
		}
		segment := line[lt+1 : lt+brace]

		seen := map[string]bool{}
		searchPos := 0
		for part := range strings.SplitSeq(segment, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if part == "in" || strings.HasPrefix(part, "in ") {
				searchPos += len(part) + 1
				continue
			}
			name := part
			if inIdx := strings.Index(name, " in "); inIdx >= 0 {
				name = strings.TrimSpace(name[:inIdx])
			}
			if name == "" || strings.ContainsAny(name, "/\\") {
				searchPos += len(part) + 1
				continue
			}
			if _, err := data.GetCommand(name); err != nil {
				searchPos += len(part) + 1
				continue
			}
			if seen[name] {
				absCol := strings.Index(line[searchPos:], name)
				if absCol >= 0 {
					absCol += searchPos
				} else {
					absCol = strings.LastIndex(line, name)
				}
				if absCol < 0 {
					absCol = 0
				}
				issues = append(issues, LintIssue{
					Line: lineIdx, Col: absCol, EndCol: absCol + len(name),
					Severity: LintWarning,
					Message:  fmt.Sprintf("duplicate prerequisite `%s`", name),
				})
			}
			seen[name] = true
			if idx := strings.Index(line[searchPos:], name); idx >= 0 {
				searchPos += idx + len(name)
			} else {
				searchPos += len(part) + 1
			}
		}
	}
	return issues
}

func lintMissingFileDeps(data *ParsedData, baseDir string) []LintIssue {
	var issues []LintIssue
	for _, cmd := range data.Commands {
		for _, dep := range expandFileDeps(cmd.FileDeps, workDirBase(baseDir, cmd.WorkDir)) {
			if _, err := os.Stat(dep); err != nil {
				issues = append(issues, LintIssue{
					Line: max(cmd.SourceLine-1, 0), Col: 0, EndCol: 0,
					Severity: LintWarning,
					Message:  fmt.Sprintf("command `%s`: file dependency %q does not exist", cmd.Name, dep),
				})
			}
		}
	}
	return issues
}

func workDirBase(baseDir, workDir string) string {
	if workDir == "" || filepath.IsAbs(workDir) {
		return baseDir
	}
	return filepath.Join(baseDir, workDir)
}

func lintUnusedGlobals(data *ParsedData) []LintIssue {
	used := map[string]bool{}
	for _, cmd := range data.Commands {
		collectStmtRefs(cmd.Body, used)
		for _, str := range append(append([]string{}, cmd.Produces...), cmd.OnChange...) {
			for _, n := range VarRefNames(str) {
				used[n] = true
			}
		}
		if cmd.WorkDir != "" {
			for _, n := range VarRefNames(cmd.WorkDir) {
				used[n] = true
			}
		}
	}
	var issues []LintIssue
	for _, v := range data.Variables {
		if v.Scope == "global" && !used[v.Name] {
			issues = append(issues, LintIssue{
				Severity: LintInfo,
				Message:  fmt.Sprintf("global variable %q is never referenced", v.Name),
			})
		}
	}
	return issues
}

func lintUnreferencedCommands(data *ParsedData) []LintIssue {
	referenced := map[string]bool{}
	for _, cmd := range data.Commands {
		if cmd.IsDefault {
			referenced[cmd.Name] = true
		}
		for _, p := range cmd.Prereqs {
			referenced[p] = true
		}
		for _, stmt := range ShellStatements(cmd.Body) {
			if stmt.Type == StmtInvoke {
				referenced[strings.TrimSpace(stmt.Shell)] = true
			}
		}
	}
	var issues []LintIssue
	for _, cmd := range data.Commands {
		if cmd.Name == "_" || cmd.Manual || IsLazyName(cmd.Name) || referenced[cmd.Name] {
			continue
		}
		issues = append(issues, LintIssue{
			Line: max(cmd.SourceLine-1, 0), Col: 0, EndCol: 0,
			Severity: LintInfo,
			Message:  fmt.Sprintf("command %q is never referenced (not a prerequisite, invoke target, or default)", cmd.Name),
		})
	}
	return issues
}

func ShellStatements(body []BodyStatement) []BodyStatement {
	var out []BodyStatement
	for _, stmt := range body {
		switch stmt.Type {
		case StmtShell:
			out = append(out, stmt)
		case StmtIf:
			out = append(out, ShellStatements(stmt.ThenBody)...)
			out = append(out, ShellStatements(stmt.ElseBody)...)
		case StmtFor:
			out = append(out, ShellStatements(stmt.LoopBody)...)
		case StmtOnFail:
			out = append(out, ShellStatements(stmt.OnFailBody)...)
		case StmtSwitch:
			for _, c := range stmt.Cases {
				out = append(out, ShellStatements(c.Body)...)
			}
		case StmtInDir, StmtLock:
			out = append(out, ShellStatements(stmt.ThenBody)...)
		}
	}
	return out
}

func InvokeCaptureHint(body []BodyStatement, name string) (invoked string, line int, found bool) {
	for _, stmt := range body {
		switch stmt.Type {
		case StmtInvoke:
			if stmt.OutputName == name {
				return stmt.Shell, stmt.SourceLine, true
			}
		case StmtIf:
			if inv, ln, f := InvokeCaptureHint(stmt.ThenBody, name); f {
				return inv, ln, f
			}
			if inv, ln, f := InvokeCaptureHint(stmt.ElseBody, name); f {
				return inv, ln, f
			}
		case StmtFor:
			if inv, ln, f := InvokeCaptureHint(stmt.LoopBody, name); f {
				return inv, ln, f
			}
		case StmtOnFail:
			if inv, ln, f := InvokeCaptureHint(stmt.OnFailBody, name); f {
				return inv, ln, f
			}
		case StmtSwitch:
			for _, c := range stmt.Cases {
				if inv, ln, f := InvokeCaptureHint(c.Body, name); f {
					return inv, ln, f
				}
			}
		case StmtInDir, StmtLock:
			if inv, ln, f := InvokeCaptureHint(stmt.ThenBody, name); f {
				return inv, ln, f
			}
		}
	}
	return "", 0, false
}

func SplitCommandRef(data *ParsedData, name string) (cmdName, suffix string, ok bool) {
	parts := strings.Split(name, ".")
	for i := len(parts); i >= 1; i-- {
		candidate := strings.Join(parts[:i], ".")
		if _, err := data.GetCommand(candidate); err != nil {
			continue
		}
		suffix := strings.Join(parts[i:], ".")
		if suffix == "" {
			continue // whole-output ref (&cmd) is not a command+suffix ref
		}
		return candidate, suffix, true
	}
	return "", "", false
}

func HasNamedOutput(body []BodyStatement, name string) bool {
	for _, stmt := range body {
		if stmt.OutputName == name {
			return true
		}
		switch stmt.Type {
		case StmtIf:
			if HasNamedOutput(stmt.ThenBody, name) || HasNamedOutput(stmt.ElseBody, name) {
				return true
			}
		case StmtFor:
			if HasNamedOutput(stmt.LoopBody, name) {
				return true
			}
		case StmtOnFail:
			if HasNamedOutput(stmt.OnFailBody, name) {
				return true
			}
		case StmtSwitch:
			for _, c := range stmt.Cases {
				if HasNamedOutput(c.Body, name) {
					return true
				}
			}
		case StmtInDir, StmtLock:
			if HasNamedOutput(stmt.ThenBody, name) {
				return true
			}
		}
	}
	return false
}
