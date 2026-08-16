package pkg

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

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

	readImport := p.ImportReader
	if readImport == nil {
		readImport = os.ReadFile
	}
	content, err := readImport(cleanPath)
	if err != nil {
		return fmt.Errorf("failed to read import %q: %w", spec, err)
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
		if existing, err := p.Data.GetCommand(cmd.Name); err == nil && existing != nil {
			return fmt.Errorf("duplicate command %q from import %q", cmd.Name, spec)
		}
		p.Data.addCommand(cmd)
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
