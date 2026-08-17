package pkg

import (
	"slices"
	"strings"
)

// IsFileDep reports whether a prerequisite token looks like a file path or glob.
func IsFileDep(token string) bool {
	if strings.ContainsAny(token, "/*\\?") {
		return true
	}
	if dot := strings.LastIndexByte(token, '.'); dot > 0 && dot < len(token)-1 {
		return true
	}
	return false
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
			} else if IsFileDep(prereq) {
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

func (p *Parser) collectIndexedOutputRefs() {
	refs := p.Data.computeIndexedOutputRefs()
	p.Data.mu.Lock()
	p.Data.indexedOutputRefs = refs
	p.Data.mu.Unlock()
}

func (p *Parser) computeCacheGlobals() {
	refsByName := make(map[string][]string, len(p.Data.Variables))
	globalNames := make(map[string]bool, len(p.Data.Variables))
	for _, v := range p.Data.Variables {
		refsByName[v.Name] = append(refsByName[v.Name], v.refs...)
		if v.Scope == "global" {
			globalNames[v.Name] = true
		}
	}

	for _, cmd := range p.Data.Commands {
		seed := map[string]bool{}
		collectStmtRefs(cmd.Body, seed)
		for _, str := range cmd.Produces {
			for _, n := range VarRefNames(str) {
				seed[n] = true
			}
		}
		for _, str := range cmd.OnChange {
			for _, n := range VarRefNames(str) {
				seed[n] = true
			}
		}
		if cmd.WorkDir != "" {
			for _, n := range VarRefNames(cmd.WorkDir) {
				seed[n] = true
			}
		}

		var globals []string
		visited := make(map[string]bool, len(seed))
		queue := make([]string, 0, len(seed))
		for n := range seed {
			queue = append(queue, n)
		}
		for len(queue) > 0 {
			n := queue[len(queue)-1]
			queue = queue[:len(queue)-1]
			if visited[n] {
				continue // circular variable references terminate here
			}
			visited[n] = true
			if globalNames[n] {
				globals = append(globals, n)
			}
			queue = append(queue, refsByName[n]...)
		}
		slices.Sort(globals)
		globals = slices.Compact(globals)
		cmd.cacheGlobals, cmd.cacheGlobalsExact = globals, true
	}
}

// collectStmtRefs gathers every &name referenced by a statement tree.
func collectStmtRefs(stmts []BodyStatement, out map[string]bool) {
	collectStmtRefsWith(stmts, out, VarRefNames)
}

func collectStmtWildcardRefs(stmts []BodyStatement, out map[string]bool) {
	collectStmtRefsWith(stmts, out, wildcardRefNames)
}

func collectStmtRefsWith(stmts []BodyStatement, out map[string]bool, names func(string) []string) {
	for i := range stmts {
		stmt := &stmts[i]
		for _, str := range []string{stmt.Shell, stmt.Cond, stmt.LoopItems, stmt.SwitchExpr, stmt.Message, stmt.BuiltinArgs, stmt.Dir} {
			for _, n := range names(str) {
				out[n] = true
			}
		}
		for _, pair := range stmt.Env {
			if _, val, ok := strings.Cut(pair, "="); ok {
				for _, n := range names(val) {
					out[n] = true
				}
			}
		}
		for _, c := range stmt.Cases {
			for _, v := range c.Values {
				for _, n := range names(v) {
					out[n] = true
				}
			}
			collectStmtRefsWith(c.Body, out, names)
		}
		collectStmtRefsWith(stmt.ThenBody, out, names)
		collectStmtRefsWith(stmt.ElseBody, out, names)
		collectStmtRefsWith(stmt.LoopBody, out, names)
		collectStmtRefsWith(stmt.OnFailBody, out, names)
	}
}
