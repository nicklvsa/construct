package pkg

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type importSpec struct {
	path  string
	ns    string
	cond  string
	isGit bool
}

func lastIndexOutsideQuotes(s, marker string) int {
	inQuote := false
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '"' {
			inQuote = !inQuote
			continue
		}
		if inQuote {
			continue
		}
		if strings.HasPrefix(s[i:], marker) {
			return i
		}
	}
	return -1
}

func parseImportSpec(line string) (importSpec, error) {
	spec := strings.TrimSpace(strings.TrimPrefix(line, "import"))
	out := importSpec{}

	if rest, ok := strings.CutPrefix(spec, "git "); ok {
		out.isGit = true
		spec = strings.TrimSpace(rest)
	}

	var condRaw, condKind string
	ifIdx := lastIndexOutsideQuotes(spec, " if ")
	onIdx := lastIndexOutsideQuotes(spec, " on ")
	idx, kind := ifIdx, "if"
	if onIdx > ifIdx {
		idx, kind = onIdx, "on"
	}
	if idx >= 0 {
		condRaw = strings.TrimSpace(spec[idx+4:])
		spec = strings.TrimSpace(spec[:idx])
		condKind = kind
	}

	if asIdx := strings.LastIndex(spec, " as "); asIdx >= 0 {
		out.ns = strings.TrimSpace(spec[asIdx+len(" as "):])
		spec = strings.TrimSpace(spec[:asIdx])
		if !isValidIdent(out.ns) {
			return importSpec{}, fmt.Errorf("invalid import namespace %q (expected an identifier)", out.ns)
		}
	}

	out.path = strings.Trim(spec, `"`)
	if out.path == "" {
		return importSpec{}, fmt.Errorf("import requires a file path")
	}
	if strings.ContainsAny(out.path, `"`) {
		return importSpec{}, fmt.Errorf("malformed import path %q", out.path)
	}

	switch condKind {
	case "if":
		if condRaw == "" {
			return importSpec{}, fmt.Errorf("import condition is empty (import %q if ...)", out.path)
		}
		out.cond = condRaw
	case "on":
		var checks []string
		for item := range strings.SplitSeq(condRaw, ",") {
			item = strings.TrimSpace(strings.Trim(strings.TrimSpace(item), `"`))
			switch item {
			case "":
			case "macos":
				checks = append(checks, `os("darwin")`)
			default:
				checks = append(checks, fmt.Sprintf("os(%q)", item))
			}
		}
		if len(checks) == 0 {
			return importSpec{}, fmt.Errorf("import %q: 'on' needs at least one platform (darwin, linux, windows)", out.path)
		}
		out.cond = strings.Join(checks, " || ")
	}
	return out, nil
}

func (p *Parser) processImport(line string) error {
	spec, err := parseImportSpec(line)
	if err != nil {
		return err
	}

	if spec.cond != "" {
		resolved := p.tryEvalExpression(spec.cond, nil, nil, 0)
		if !evaluateConditionWithBase(resolved, importBaseDir(p.InputFile)) {
			return nil
		}
	}

	path := spec.path
	if spec.isGit {
		resolved, defaultNS, err := ensureGitImport(spec.path, importBaseDir(p.InputFile))
		if err != nil {
			return err
		}
		path = resolved
		if spec.ns == "" {
			spec.ns = defaultNS
		}
	} else if !filepath.IsAbs(path) {
		path = filepath.Join(importBaseDir(p.InputFile), spec.path)
	}
	ns := spec.ns

	cleanPath := filepath.Clean(path)
	if p.importStack[cleanPath] {
		return fmt.Errorf("circular import of %q", spec.path)
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

	readImport := p.ImportReader
	if readImport == nil {
		readImport = os.ReadFile
	}
	content, err := readImport(cleanPath)
	if err != nil {
		return fmt.Errorf("failed to read import %q: %w", spec.path, err)
	}

	imported := NewParserFromContent(cleanPath, string(content))
	imported.importStack = p.importStack
	imported.imported = p.imported
	imported.ImportReader = p.ImportReader
	if err := imported.parseLines(); err != nil {
		return err
	}

	if ns != "" {
		renameImportNamespace(imported.Data, ns)
	}

	for _, cmd := range imported.Data.Commands {
		if existing, err := p.Data.GetCommand(cmd.Name); err == nil && existing != nil {
			return fmt.Errorf("duplicate command %q from import %q", cmd.Name, spec.path)
		}
	}

	if !slices.Contains(p.Data.SourceFiles, cleanPath) {
		p.Data.SourceFiles = append(p.Data.SourceFiles, cleanPath)
	}
	for _, sf := range imported.Data.SourceFiles {
		if !slices.Contains(p.Data.SourceFiles, sf) {
			p.Data.SourceFiles = append(p.Data.SourceFiles, sf)
		}
	}

	for _, v := range imported.Data.Variables {
		p.Data.addVariable(v)
	}
	for _, cmd := range imported.Data.Commands {
		p.Data.addCommand(cmd)
	}
	return nil
}

func importBaseDir(inputFile string) string {
	u, err := url.Parse(inputFile)
	if err == nil && u.Scheme == "file" {
		p := u.Path
		if len(p) > 2 && p[0] == '/' && p[2] == ':' {
			p = p[1:]
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
		if n, ok := globalNew[seg]; ok {
			return "&" + n + full[len(seg):], true
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
		case StmtSwitch:
			for _, c := range stmt.Cases {
				collectLoopVars(c.Body, out)
			}
		case StmtInDir, StmtLock:
			collectLoopVars(stmt.ThenBody, out)
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
		case StmtSwitch:
			stmts[i].SwitchExpr = renameVarRefs(stmts[i].SwitchExpr, rename)
			for j := range stmts[i].Cases {
				for k := range stmts[i].Cases[j].Values {
					stmts[i].Cases[j].Values[k] = renameVarRefs(stmts[i].Cases[j].Values[k], rename)
				}
				renameBodyRefs(stmts[i].Cases[j].Body, rename)
			}
		case StmtInDir, StmtLock:
			stmts[i].Shell = renameVarRefs(stmts[i].Shell, rename)
			renameBodyRefs(stmts[i].ThenBody, rename)
		case StmtBuiltin:
			stmts[i].BuiltinArgs = renameVarRefs(stmts[i].BuiltinArgs, rename)
		case StmtState, StmtConfirm, StmtPrompt:
			stmts[i].Message = renameVarRefs(stmts[i].Message, rename)
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
