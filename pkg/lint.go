package pkg

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
		if cmd.Name == "_" || IsLazyName(cmd.Name) || referenced[cmd.Name] {
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
