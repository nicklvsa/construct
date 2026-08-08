package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	pkg "github.com/nicklvsa/construct/pkg"
)

// ---- LSP types (subset, hand-rolled to avoid a framework dependency) ----

type position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type range_ struct {
	Start position `json:"start"`
	End   position `json:"end"`
}

type location struct {
	URI   string `json:"uri"`
	Range range_ `json:"range"`
}

type diagnostic struct {
	Range    range_ `json:"range"`
	Severity int    `json:"severity"`
	Source   string `json:"source"`
	Message  string `json:"message"`
}

type textDocumentItem struct {
	URI     string `json:"uri"`
	Version int    `json:"version"`
	Text    string `json:"text"`
}

type didChangeTextDocumentParams struct {
	TextDocument struct {
		URI     string `json:"uri"`
		Version int    `json:"version"`
	} `json:"textDocument"`
	ContentChanges []struct {
		Text string `json:"text"`
	} `json:"contentChanges"`
}

type hoverResult struct {
	Contents markupContent `json:"contents"`
}

type markupContent struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

type completionItem struct {
	Label      string `json:"label"`
	Kind       int    `json:"kind"`
	FilterText string `json:"filterText,omitempty"`
}

type completionList struct {
	IsIncomplete bool             `json:"isIncomplete"`
	Items        []completionItem `json:"items"`
}

type textDocumentPositionParams struct {
	TextDocument struct {
		URI string `json:"uri"`
	} `json:"textDocument"`
	Position position `json:"position"`
}

type initializeResult struct {
	Capabilities serverCapabilities `json:"capabilities"`
}

type serverCapabilities struct {
	TextDocumentSync   int                `json:"textDocumentSync"`
	HoverProvider      bool               `json:"hoverProvider"`
	DefinitionProvider bool               `json:"definitionProvider"`
	CompletionProvider *completionOptions `json:"completionProvider,omitempty"`
}

type completionOptions struct {
	TriggerCharacters []string `json:"triggerCharacters,omitempty"`
}

type publishDiagnosticsParams struct {
	URI         string       `json:"uri"`
	Diagnostics []diagnostic `json:"diagnostics"`
}

const (
	sevError   = 1
	sevWarning = 2
	sevInfo    = 3
)

// ---- server state ----

type docState struct {
	text    string
	version int
	data    *pkg.ParsedData
}

type server struct {
	mu   sync.Mutex
	docs map[string]*docState
	out  *os.File
}

func newServer() *server {
	return &server{
		docs: make(map[string]*docState),
		out:  os.Stdout,
	}
}

func (s *server) dispatch(method string, params json.RawMessage) (interface{}, error) {
	switch method {
	case "initialize":
		return s.handleInitialize(params)
	case "initialized":
		return nil, nil
	case "shutdown":
		return nil, nil
	case "textDocument/didOpen":
		return s.handleDidOpen(params)
	case "textDocument/didChange":
		return s.handleDidChange(params)
	case "textDocument/hover":
		return s.handleHover(params)
	case "textDocument/definition":
		return s.handleDefinition(params)
	case "textDocument/completion":
		return s.handleCompletion(params)
	default:
		return nil, nil
	}
}

func (s *server) handleInitialize(_ json.RawMessage) (any, error) {
	return initializeResult{
		Capabilities: serverCapabilities{
			TextDocumentSync:   1, // full document sync
			HoverProvider:      true,
			DefinitionProvider: true,
			CompletionProvider: &completionOptions{TriggerCharacters: []string{"&"}},
		},
	}, nil
}

func (s *server) handleDidOpen(params json.RawMessage) (any, error) {
	var p struct {
		TextDocument textDocumentItem `json:"textDocument"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("bad params: %w", err)
	}
	item := p.TextDocument
	s.updateDoc(item.URI, item.Text, item.Version)
	return nil, nil
}

func (s *server) handleDidChange(params json.RawMessage) (any, error) {
	var p didChangeTextDocumentParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("bad params: %w", err)
	}
	if len(p.ContentChanges) > 0 {
		s.updateDoc(p.TextDocument.URI, p.ContentChanges[0].Text, p.TextDocument.Version)
	}
	return nil, nil
}

func (s *server) updateDoc(uri, text string, version int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	parser := pkg.NewParserFromContent(uri, text)
	data, err := parser.Parse()
	if err != nil {
		st := &docState{text: text, version: version}
		if old, ok := s.docs[uri]; ok {
			st.data = old.data
		}
		s.docs[uri] = st
		s.publishDiagnostics(uri, parseErrorToDiagnostics(err, text))
		return
	}

	s.docs[uri] = &docState{text: text, version: version, data: data}
	diags := namedOutputHints(text, data)
	diags = append(diags, duplicatePrereqWarnings(text, data)...)
	s.publishDiagnostics(uri, diags)
}

func (s *server) publishDiagnostics(uri string, diags []diagnostic) {
	writeNotification(s.out, "textDocument/publishDiagnostics", publishDiagnosticsParams{
		URI:         uri,
		Diagnostics: diags,
	})
}

func parseErrorToDiagnostics(err error, text string) []diagnostic {
	sev := sevError

	switch e := err.(type) {
	case *pkg.ParseError:
		line := e.Line
		if line < 1 {
			line = 1
		}
		lines := strings.Split(text, "\n")
		endChar := 0
		if line-1 < len(lines) {
			endChar = len(lines[line-1])
		}
		if endChar == 0 {
			endChar = 1
		}
		return []diagnostic{{
			Range: range_{
				Start: position{Line: line - 1, Character: 0},
				End:   position{Line: line - 1, Character: endChar},
			},
			Severity: sev,
			Source:   "constfile",
			Message:  e.Message,
		}}
	case *pkg.CircularDependencyError:
		return []diagnostic{{
			Range:    fullRange(text),
			Severity: sevError,
			Source:   "constfile",
			Message:  e.Error(),
		}}
	case *pkg.MissingDependencyError:
		return []diagnostic{{
			Range:    fullRange(text),
			Severity: sevError,
			Source:   "constfile",
			Message:  e.Error(),
		}}
	}

	return []diagnostic{{
		Range:    fullRange(text),
		Severity: sev,
		Source:   "constfile",
		Message:  err.Error(),
	}}
}

func fullRange(text string) range_ {
	lines := strings.Split(text, "\n")
	endLine := max(len(lines)-1, 0)
	endChar := len(lines[endLine])
	return range_{
		Start: position{Line: 0, Character: 0},
		End:   position{Line: endLine, Character: endChar},
	}
}

func duplicatePrereqWarnings(text string, data *pkg.ParsedData) []diagnostic {
	diags := []diagnostic{}
	lines := strings.Split(text, "\n")
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
				diags = append(diags, diagnostic{
					Range:    refRange(lineIdx, absCol, len(name)),
					Severity: sevWarning,
					Source:   "constfile",
					Message:  fmt.Sprintf("duplicate prerequisite `%s`", name),
				})
			}
			seen[name] = true
			idx := strings.Index(line[searchPos:], name)
			if idx >= 0 {
				searchPos += idx + len(name)
			} else {
				searchPos += len(part) + 1
			}
		}
	}
	return diags
}

func namedOutputHints(text string, data *pkg.ParsedData) []diagnostic {
	diags := []diagnostic{}
	lines := strings.Split(text, "\n")
	for lineIdx, line := range lines {
		searchFrom := 0
		for {
			ampIdx := strings.IndexByte(line[searchFrom:], '&')
			if ampIdx < 0 {
				break
			}
			absIdx := searchFrom + ampIdx
			name := extractRefName(line[absIdx:])
			if name == "" {
				searchFrom = absIdx + 1
				continue
			}
			searchFrom = absIdx + 1 + len(name)
			refLen := len(name) + 1

			cmdName, suffix, ok := splitCommandRef(data, name)
			if !ok {
				continue // plain &var, not a prereq output ref
			}

			cmd, err := data.GetCommand(cmdName)
			if err != nil || cmd == nil {
				continue
			}

			shellCount := countShellLines(cmd.Body)

			if idx, err := strconv.Atoi(suffix); err == nil {
				if idx < 0 || idx >= shellCount {
					diags = append(diags, diagnostic{
						Range:    refRange(lineIdx, absIdx, refLen),
						Severity: sevError,
						Source:   "constfile",
						Message:  fmt.Sprintf("index %d out of bounds: `%s` has %d output line(s)", idx, cmdName, shellCount),
					})
					continue
				}

				if hint := namedOutputAt(data, cmdName, idx); hint != "" {
					diags = append(diags, diagnostic{
						Range:    refRange(lineIdx, absIdx, refLen),
						Severity: sevWarning,
						Source:   "constfile",
						Message:  fmt.Sprintf("💡 named output available: use &%s instead of &%s", hint, name),
					})
				}
			} else {
				if !hasNamedOutput(cmd.Body, suffix) {
					diags = append(diags, diagnostic{
						Range:    refRange(lineIdx, absIdx, refLen),
						Severity: sevError,
						Source:   "constfile",
						Message:  fmt.Sprintf("unknown named output `%s` on command `%s`", suffix, cmdName),
					})
				}
			}
		}
	}
	return diags
}

func refRange(line, col, length int) range_ {
	return range_{
		Start: position{Line: line, Character: col},
		End:   position{Line: line, Character: col + length},
	}
}

func countShellLines(body []pkg.BodyStatement) int {
	count := 0
	for _, stmt := range body {
		switch stmt.Type {
		case pkg.StmtShell:
			count++
		case pkg.StmtIf:
			count += countShellLines(stmt.ThenBody)
			count += countShellLines(stmt.ElseBody)
		}
	}
	return count
}

func hasNamedOutput(body []pkg.BodyStatement, name string) bool {
	for _, stmt := range body {
		if stmt.OutputName == name {
			return true
		}
		if stmt.Type == pkg.StmtIf {
			if hasNamedOutput(stmt.ThenBody, name) || hasNamedOutput(stmt.ElseBody, name) {
				return true
			}
		}
	}
	return false
}

// namedOutputAt checks if command cmdName has a named output at the given index.
func namedOutputAt(data *pkg.ParsedData, cmdName string, idx int) string {
	cmd, err := data.GetCommand(cmdName)
	if err != nil || cmd == nil {
		return ""
	}
	shellIdx := 0
	for _, stmt := range cmd.Body {
		if stmt.Type != pkg.StmtShell {
			continue
		}
		if stmt.OutputName != "" && shellIdx == idx {
			return cmdName + "." + stmt.OutputName
		}
		shellIdx++
	}
	return ""
}

// extractRefName extracts the variable name from a &reference at the start of s.
func extractRefName(s string) string {
	if len(s) < 2 || s[0] != '&' {
		return ""
	}
	var name strings.Builder
	for _, r := range s[1:] {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '.' || r == '-' {
			name.WriteRune(r)
		} else {
			break
		}
	}
	return name.String()
}

func (s *server) handleHover(params json.RawMessage) (interface{}, error) {
	var p textDocumentPositionParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	s.mu.Lock()
	doc, ok := s.docs[p.TextDocument.URI]
	s.mu.Unlock()
	if !ok || doc.data == nil {
		return nil, nil
	}

	lines := strings.Split(doc.text, "\n")
	if p.Position.Line >= len(lines) {
		return nil, nil
	}
	line := lines[p.Position.Line]
	char := p.Position.Character

	// Hover over a &varName reference.
	if str, name := refAtPosition(line, char); str != "" {
		hoverMsg := varHoverMessage(name, doc.data)
		if hoverMsg != "" {
			return hoverResult{
				Contents: markupContent{Kind: "markdown", Value: hoverMsg},
			}, nil
		}

		if cmd := enclosingCommand(lines, p.Position.Line, doc.data); cmd != nil {
			for _, a := range cmd.Arguments {
				if a.Name == name {
					msg := fmt.Sprintf("`%s` — argument of command `%s`\n\npass with `--%s:%s=<value>`", name, cmd.Name, cmd.Name, a.Name)
					if a.IsOptional {
						msg += "\n\noptional"
					}
					return hoverResult{
						Contents: markupContent{Kind: "markdown", Value: msg},
					}, nil
				}
			}

			if invoked, ln, found := invokeCaptureHint(cmd.Body, name); found {
				loc := "unknown line"
				if ln > 0 {
					loc = fmt.Sprintf("line %d", ln)
				}
				return hoverResult{
					Contents: markupContent{
						Kind:  "markdown",
						Value: fmt.Sprintf("`%s` — output captured by `invoke %s as %s` (%s)\n\nAvailable from that statement onward.", name, invoked, name, loc),
					},
				}, nil
			}
		}
	}

	// Hover over an @ENV reference — show the environment variable's value.
	if envName, ok := envRefAtPosition(line, char); ok {
		val := os.Getenv(envName)
		var msg string
		if val != "" {
			msg = fmt.Sprintf("`@%s` (environment variable)\n\nresolved to: `%s`", envName, val)
		} else {
			msg = fmt.Sprintf("`@%s` (environment variable)\n\n⚠ not set (resolves to empty)", envName)
		}
		return hoverResult{
			Contents: markupContent{Kind: "markdown", Value: msg},
		}, nil
	}

	if target, ok := prereqNameAtPosition(line, char); ok {
		if cmd, err := doc.data.GetCommand(target); err == nil {
			return hoverResult{
				Contents: markupContent{
					Kind:  "markdown",
					Value: "prerequisite: " + commandHover(cmd),
				},
			}, nil
		}
	}

	if name, isCmd := commandNameAtLine(line); isCmd {
		if cmd, err := doc.data.GetCommand(name); err == nil {
			return hoverResult{
				Contents: markupContent{
					Kind:  "markdown",
					Value: commandHover(cmd),
				},
			}, nil
		}
	}

	// Hover over an `invoke <command>` target.
	if target, _, _, ok := invokeNameAtPosition(line, char); ok {
		if cmd, err := doc.data.GetCommand(target); err == nil {
			return hoverResult{
				Contents: markupContent{
					Kind:  "markdown",
					Value: "invokes: " + commandHover(cmd),
				},
			}, nil
		}
	}

	if hdr, isHdr := commandNameAtLine(line); isHdr {
		if _, err := doc.data.GetCommand(hdr); err == nil {
			if dir, _, _, ok := workDirAtPosition(line, char); ok {
				resolved := resolveWorkDir(dir, p.TextDocument.URI)
				return hoverResult{
					Contents: markupContent{
						Kind:  "markdown",
						Value: fmt.Sprintf("📂 working directory: `%s`\n\nresolved: `%s`", dir, resolved),
					},
				}, nil
			}
		}
	}

	return nil, nil
}

func commandHover(c *pkg.Command) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**%s**\n\n", c.Name)
	if c.CloudAccessible {
		b.WriteString("- cloud-accessible\n")
	}
	if c.IsDefault {
		b.WriteString("- default command\n")
	}
	if len(c.Arguments) > 0 {
		b.WriteString("- arguments:")
		for _, a := range c.Arguments {
			if a.IsOptional {
				fmt.Fprintf(&b, " `[opt] %s`", a.Name)
			} else {
				fmt.Fprintf(&b, " `%s`", a.Name)
			}
			if a.Default != "" {
				fmt.Fprintf(&b, " (default: %s)", a.Default)
			}
		}
		b.WriteString("\n")
	}
	if len(c.Prereqs) > 0 {
		fmt.Fprintf(&b, "- depends on: `%s`\n", strings.Join(c.Prereqs, "`, `"))
	}
	if c.WorkDir != "" {
		fmt.Fprintf(&b, "- working dir: `%s`\n", c.WorkDir)
	}
	if len(c.Body) > 0 {
		fmt.Fprintf(&b, "- %d body statement(s)\n", len(c.Body))
	}
	return b.String()
}

func splitCommandRef(data *pkg.ParsedData, name string) (cmdName, suffix string, ok bool) {
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

func varHoverMessage(name string, data *pkg.ParsedData) string {
	cmdName, suffix, ok := splitCommandRef(data, name)
	if !ok {
		// Plain &var — look it up in the variable map.
		v, err := data.GetVariable(name, "")
		if err != nil {
			for _, vv := range data.Variables {
				if vv.Name == name {
					v = vv
					break
				}
			}
		}
		if v == nil {
			return ""
		}
		return fmt.Sprintf("`%s` (scope: `%s`)\n\nvalue: `%s`", name, v.Scope, v.Value)
	}

	cmd, err := data.GetCommand(cmdName)
	if err != nil || cmd == nil {
		return ""
	}

	shellIdx := 0
	for _, stmt := range cmd.Body {
		if stmt.Type != pkg.StmtShell {
			continue
		}
		matches := false
		var namedHint string
		if idx, err := strconv.Atoi(suffix); err == nil {
			// Numeric index
			if shellIdx == idx {
				matches = true
				if stmt.OutputName != "" {
					namedHint = cmdName + "." + stmt.OutputName
				}
			}
		} else if stmt.OutputName == suffix {
			matches = true
		}
		if matches {
			msg := fmt.Sprintf("`%s` → output of `%s` line %d\n\n```\n%s\n```",
				name, cmdName, shellIdx, strings.TrimSpace(stmt.Shell))
			if namedHint != "" {
				msg += "\n\n💡 tip: this output is also available as `&" + namedHint + "`"
			}
			return msg
		}
		shellIdx++
	}

	return ""
}

func (s *server) handleDefinition(params json.RawMessage) (interface{}, error) {
	var p textDocumentPositionParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	s.mu.Lock()
	doc, ok := s.docs[p.TextDocument.URI]
	s.mu.Unlock()
	if !ok || doc.data == nil {
		return nil, nil
	}

	lines := strings.Split(doc.text, "\n")
	if p.Position.Line >= len(lines) {
		return nil, nil
	}
	line := lines[p.Position.Line]
	char := p.Position.Character

	if _, name := refAtPosition(line, char); name != "" {
		// Find the declaration line ("var name ...") anywhere in the doc.
		for i, l := range lines {
			trimmed := strings.TrimSpace(l)
			if strings.HasPrefix(trimmed, "var ") {
				declName := extractVarDeclName(trimmed)
				if declName == name {
					return location{
						URI: p.TextDocument.URI,
						Range: range_{
							Start: position{Line: i, Character: 4},
							End:   position{Line: i, Character: 4 + len(name)},
						},
					}, nil
				}
			}
		}

		if cmd := enclosingCommand(lines, p.Position.Line, doc.data); cmd != nil {
			if _, ln, found := invokeCaptureHint(cmd.Body, name); found && ln > 0 {
				invokeLine := lines[ln-1]
				col := max(strings.Index(invokeLine, "as "+name), 0)
				return location{
					URI: p.TextDocument.URI,
					Range: range_{
						Start: position{Line: ln - 1, Character: col},
						End:   position{Line: ln - 1, Character: col + len("as "+name)},
					},
				}, nil
			}
		}

		// &cmd.out / &cmd.N: jump to the shell statement producing it.
		if loc, ok := outputStmtLocation(doc, name, p.TextDocument.URI); ok {
			return loc, nil
		}
	}

	if target, ok := prereqNameAtPosition(line, char); ok {
		if _, err := doc.data.GetCommand(target); err == nil {
			if loc, ok := s.findCommandLocation(doc, target, p.TextDocument.URI); ok {
				return loc, nil
			}
		}
	}

	// invoke <command> targets resolve the same way.
	if target, _, _, ok := invokeNameAtPosition(line, char); ok {
		if _, err := doc.data.GetCommand(target); err == nil {
			if loc, ok := s.findCommandLocation(doc, target, p.TextDocument.URI); ok {
				return loc, nil
			}
		}
	}

	fd, fsc, fec, fok := fileDepAtPosition(line, char)
	if fok {
		resolved := resolveWorkDir(fd, p.TextDocument.URI)
		target := ""
		if strings.ContainsAny(resolved, "*?") {
			matches, _ := filepath.Glob(resolved)
			if len(matches) > 0 {
				target = matches[0]
			} else {
				dir := filepath.Dir(resolved)
				if _, err := os.Stat(dir); err == nil {
					target = dir
				}
			}
		} else {
			if _, err := os.Stat(resolved); err == nil {
				target = resolved
			}
		}
		if target != "" {
			uri := pathToURI(target)
			return location{
				URI: uri,
				Range: range_{
					Start: position{Line: p.Position.Line, Character: fsc},
					End:   position{Line: p.Position.Line, Character: fec},
				},
			}, nil
		}
	}

	if hdr, isHdr := commandNameAtLine(line); isHdr {
		if _, err := doc.data.GetCommand(hdr); err == nil {
			if dir, startCol, endCol, ok := workDirAtPosition(line, char); ok {
				resolved := resolveWorkDir(dir, p.TextDocument.URI)
				uri := pathToURI(resolved)
				return location{
					URI: uri,
					Range: range_{
						Start: position{Line: p.Position.Line, Character: startCol},
						End:   position{Line: p.Position.Line, Character: endCol},
					},
				}, nil
			}
		}
	}

	if name, isCmd := commandNameAtLine(line); isCmd {
		if _, err := doc.data.GetCommand(name); err == nil {
			col := max(strings.Index(line, name), 0)
			return location{
				URI: p.TextDocument.URI,
				Range: range_{
					Start: position{Line: p.Position.Line, Character: col},
					End:   position{Line: p.Position.Line, Character: col + len(name)},
				},
			}, nil
		}
	}

	return nil, nil
}

func (s *server) handleCompletion(params json.RawMessage) (any, error) {
	var p textDocumentPositionParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	s.mu.Lock()
	doc, ok := s.docs[p.TextDocument.URI]
	s.mu.Unlock()
	if !ok || doc.data == nil {
		return nil, nil
	}

	lines := strings.Split(doc.text, "\n")
	if p.Position.Line >= len(lines) {
		return nil, nil
	}

	line := lines[p.Position.Line]
	items := []completionItem{}

	if prefix, ok := completionVarPrefix(line, p.Position.Character); ok {
		return completionList{Items: completionVarItems(doc.data, lines, p.Position.Line, prefix)}, nil
	}

	// In a prereq list we complete command names.
	if isPrereqListLine(line) {
		for _, c := range doc.data.Commands {
			items = append(items, completionItem{Label: c.Name, Kind: 3}) // Function
		}
		return completionList{Items: items}, nil
	}

	leadTrim := strings.TrimLeft(line, " \t")
	if strings.HasPrefix(leadTrim, "invoke ") && p.Position.Character >= len(line)-len(leadTrim)+len("invoke ") {
		for _, c := range doc.data.Commands {
			items = append(items, completionItem{Label: c.Name, Kind: 3}) // Function
		}
		return completionList{Items: items}, nil
	}

	return completionList{Items: items}, nil
}

func commandHeaderCandidates(name string) []string {
	var out []string
	cur := name
	for {
		out = append(out, cur)
		i := strings.IndexByte(cur, '.')
		if i < 0 {
			return out
		}
		cur = cur[i+1:]
		if cur == "" {
			return out
		}
	}
}

func (s *server) findCommandLocation(doc *docState, name, docURI string) (location, bool) {
	cmd, err := doc.data.GetCommand(name)
	if err != nil || cmd == nil || cmd.SourceFile == "" {
		return location{}, false
	}

	readLoc := func(path string, isDoc bool) (location, bool) {
		var content []byte
		var readErr error
		if isDoc {
			content = []byte(doc.text)
		} else {
			content, readErr = os.ReadFile(path)
		}
		if readErr != nil && content == nil {
			return location{}, false
		}

		lines := strings.Split(string(content), "\n")
		candidates := commandHeaderCandidates(name)

		if cmd.SourceLine > 0 && cmd.SourceLine-1 < len(lines) {
			headerLine := lines[cmd.SourceLine-1]
			matched := name
			col := 0
			for _, c := range candidates {
				if idx := strings.Index(headerLine, c); idx >= 0 {
					matched = c
					col = idx
					break
				}
			}
			uri := pathToURI(path)
			if isDoc {
				uri = docURI
			}
			return location{
				URI: uri,
				Range: range_{
					Start: position{Line: cmd.SourceLine - 1, Character: col},
					End:   position{Line: cmd.SourceLine - 1, Character: col + len(matched)},
				},
			}, true
		}

		for i, l := range lines {
			if cn, ok := commandNameAtLine(l); ok {
				for _, c := range candidates {
					if cn == c {
						col := max(strings.Index(l, cn), 0)
						uri := pathToURI(path)
						if isDoc {
							uri = docURI
						}
						return location{
							URI: uri,
							Range: range_{
								Start: position{Line: i, Character: col},
								End:   position{Line: i, Character: col + len(cn)},
							},
						}, true
					}
				}
			}
		}
		return location{}, false
	}

	if strings.TrimPrefix(cmd.SourceFile, "file://") == strings.TrimPrefix(docURI, "file://") {
		return readLoc(cmd.SourceFile, true)
	}

	path := uriToPath(cmd.SourceFile)
	if path == "" {
		path = cmd.SourceFile
	}
	return readLoc(path, false)
}

func outputStmtLocation(doc *docState, ref, docURI string) (location, bool) {
	cmdName, suffix, ok := splitCommandRef(doc.data, ref)
	if !ok {
		return location{}, false
	}
	cmd, err := doc.data.GetCommand(cmdName)
	if err != nil || cmd == nil {
		return location{}, false
	}

	shellIdx := 0
	var hit *pkg.BodyStatement
	for i := range cmd.Body {
		stmt := &cmd.Body[i]
		if stmt.Type != pkg.StmtShell {
			continue
		}
		match := false
		if idx, err := strconv.Atoi(suffix); err == nil {
			match = shellIdx == idx
		} else if stmt.OutputName == suffix {
			match = true
		}
		if match {
			hit = stmt
			break
		}
		shellIdx++
	}
	if hit == nil || hit.SourceLine <= 0 {
		return location{}, false
	}

	isDoc := strings.TrimPrefix(cmd.SourceFile, "file://") == strings.TrimPrefix(docURI, "file://")
	uri := docURI
	var content []byte
	if isDoc {
		content = []byte(doc.text)
	} else {
		path := uriToPath(cmd.SourceFile)
		if path == "" {
			path = cmd.SourceFile
		}
		content, err = os.ReadFile(path)
		if err != nil {
			return location{}, false
		}
		uri = pathToURI(path)
	}

	lines := strings.Split(string(content), "\n")
	if hit.SourceLine-1 >= len(lines) {
		return location{}, false
	}
	line := lines[hit.SourceLine-1]
	col := len(line) - len(strings.TrimLeft(line, " \t"))
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "$") {
		col++ // skip the leading $
		trimmed = strings.TrimSpace(trimmed[1:])
	}
	return location{
		URI: uri,
		Range: range_{
			Start: position{Line: hit.SourceLine - 1, Character: col},
			End:   position{Line: hit.SourceLine - 1, Character: col + len(trimmed)},
		},
	}, true
}

func invokeNameAtPosition(line string, char int) (string, int, int, bool) {
	trimmed := strings.TrimSpace(line)
	lead := len(line) - len(trimmed)
	if !strings.HasPrefix(trimmed, "invoke ") {
		return "", 0, 0, false
	}
	rest := trimmed[len("invoke "):]
	j := 0
	for j < len(rest) && isPrereqIdentRune(rune(rest[j])) {
		j++
	}
	if j == 0 {
		return "", 0, 0, false
	}
	name := rest[:j]
	start := lead + len("invoke ")
	end := start + j
	if char < start || char > end {
		return "", 0, 0, false
	}
	return name, start, end, true
}

func invokeCaptureHint(body []pkg.BodyStatement, name string) (invoked string, line int, found bool) {
	for _, stmt := range body {
		switch stmt.Type {
		case pkg.StmtInvoke:
			if stmt.OutputName == name {
				return stmt.Shell, stmt.SourceLine, true
			}
		case pkg.StmtIf:
			if inv, ln, f := invokeCaptureHint(stmt.ThenBody, name); f {
				return inv, ln, f
			}
			if inv, ln, f := invokeCaptureHint(stmt.ElseBody, name); f {
				return inv, ln, f
			}
		case pkg.StmtFor:
			if inv, ln, f := invokeCaptureHint(stmt.LoopBody, name); f {
				return inv, ln, f
			}
		}
	}
	return "", 0, false
}

func refAtPosition(line string, char int) (string, string) {
	runes := []rune(line)
	idx := -1
	for i, r := range runes {
		if i <= char && r == '&' {
			idx = i
		}
	}

	if idx < 0 {
		return "", ""
	}

	var name strings.Builder
	for j := idx + 1; j < len(runes); j++ {
		r := runes[j]
		if !isIdentRune(r) && r != '.' && r != '-' && !(r >= '0' && r <= '9') {
			break
		}
		name.WriteRune(r)
	}
	if name.Len() == 0 {
		return "", ""
	}
	full := name.String()
	return string(runes[idx : idx+1+len(full)]), full
}

func envRefAtPosition(line string, char int) (string, bool) {
	runes := []rune(line)
	idx := -1
	for i, r := range runes {
		if i <= char && r == '@' {
			idx = i
		}
	}

	if idx < 0 {
		return "", false
	}

	var name strings.Builder
	for j := idx + 1; j < len(runes); j++ {
		r := runes[j]
		if !(r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			break
		}
		name.WriteRune(r)
	}
	if name.Len() == 0 {
		return "", false
	}
	return name.String(), true
}

func isIdentRune(r rune) bool {
	return r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

func commandNameAtLine(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)

	// Cloud command |name|
	if strings.HasPrefix(trimmed, "|") {
		end := strings.Index(trimmed[1:], "|")
		if end > 0 {
			return trimmed[1 : 1+end], true
		}
	}

	// Default command _
	if trimmed == "_" || strings.HasPrefix(trimmed, "_ ") || strings.HasPrefix(trimmed, "_(") {
		return "_", true
	}

	inIdx := strings.Index(trimmed, " in ")
	endIdx := len(trimmed)
	for i, r := range trimmed {
		if r == '(' || r == '<' || r == '{' {
			endIdx = i
			break
		}
	}
	if inIdx >= 0 && inIdx < endIdx {
		endIdx = inIdx
	}
	name := strings.TrimSpace(trimmed[:endIdx])
	if name == "" {
		return "", false
	}
	return name, true
}

func extractVarDeclName(line string) string {
	s := strings.TrimPrefix(line, "var ")
	s = strings.TrimSpace(s)
	if before, _, ok := strings.Cut(s, "="); ok {
		return strings.TrimSpace(before)
	}
	return s
}

func prereqNameAtPosition(line string, char int) (string, bool) {
	lt := strings.IndexByte(line, '<')
	if lt < 0 {
		return "", false
	}

	end := strings.IndexByte(line[lt:], '{')
	var region string
	if end >= 0 {
		region = line[lt+1 : lt+end]
	} else {
		region = line[lt+1:]
	}

	regionStart := lt + 1
	if char < regionStart || char > regionStart+len(region) {
		return "", false
	}

	relChar := char - regionStart
	runes := []rune(region)

	start := relChar
	for start > 0 && isPrereqIdentRune(runes[start-1]) {
		start--
	}

	endRel := relChar
	for endRel < len(runes) && isPrereqIdentRune(runes[endRel]) {
		endRel++
	}
	if start >= endRel {
		return "", false
	}
	name := string(runes[start:endRel])
	if name == "" {
		return "", false
	}
	return name, true
}

func isPrereqIdentRune(r rune) bool {
	return isIdentRune(r) || (r >= '0' && r <= '9') || r == '-' || r == '.'
}

func fileDepAtPosition(line string, char int) (token string, startCol, endCol int, ok bool) {
	lt := strings.IndexByte(line, '<')
	if lt < 0 {
		return "", 0, 0, false
	}
	brace := strings.IndexByte(line[lt:], '{')
	var region string
	if brace >= 0 {
		region = line[lt+1 : lt+brace]
	} else {
		region = line[lt+1:]
	}
	regionStart := lt + 1

	pos := char - regionStart
	if pos < 0 || pos > len(region) {
		return "", 0, 0, false
	}

	for part := range strings.SplitSeq(region, ",") {
		part = strings.TrimSpace(part)
		if part == "" || !looksLikeFileDep(part) {
			continue
		}
		idx := strings.Index(region, part)
		if pos >= idx && pos < idx+len(part) {
			return part, regionStart + idx, regionStart + idx + len(part), true
		}
	}
	return "", 0, 0, false
}

func looksLikeFileDep(token string) bool {
	if strings.ContainsAny(token, "/*\\?") {
		return true
	}
	if dot := strings.LastIndexByte(token, '.'); dot > 0 && dot < len(token)-1 {
		return true
	}
	return false
}

func workDirAtPosition(line string, char int) (dir string, startCol, endCol int, ok bool) {
	header := line
	if before, _, ok := strings.Cut(line, "<"); ok {
		header = before
	}

	inIdx := strings.LastIndex(header, " in ")
	if inIdx < 0 {
		return "", 0, 0, false
	}

	dirStart := inIdx + 4 // skip " in "
	rest := header[dirStart:]
	braceIdx := strings.IndexByte(rest, '{')

	var dirEnd int
	if braceIdx >= 0 {
		dirEnd = dirStart + braceIdx
	} else {
		dirEnd = len(header)
	}

	dir = strings.TrimSpace(header[dirStart:dirEnd])
	if dir == "" {
		return "", 0, 0, false
	}

	// Recompute the trimmed column span for the actual directory text.
	trimStart := dirStart + strings.Index(header[dirStart:dirEnd], dir)
	trimEnd := trimStart + len(dir)

	if char < trimStart || char > trimEnd {
		return "", 0, 0, false
	}

	return dir, trimStart, trimEnd, true
}

func resolveWorkDir(dir, docURI string) string {
	dir = resolveEnvRefsInString(dir)
	if filepath.IsAbs(dir) {
		return filepath.Clean(dir)
	}

	docPath := uriToPath(docURI)
	if docPath == "" {
		return dir
	}
	docDir := filepath.Dir(docPath)
	resolved := filepath.Join(docDir, dir)
	return filepath.Clean(resolved)
}

func pathToURI(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = p
	}
	slashed := filepath.ToSlash(abs)
	if !strings.HasPrefix(slashed, "/") {
		slashed = "/" + slashed
	}
	u := &url.URL{Scheme: "file", Path: slashed}
	return u.String()
}

func uriToPath(uri string) string {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" {
		return ""
	}

	p := u.Path
	if len(p) > 2 && p[0] == '/' && p[2] == ':' {
		p = p[1:]
	}
	return filepath.FromSlash(p)
}

func resolveEnvRefsInString(s string) string {
	var result strings.Builder
	runes := []rune(s)
	for i := 0; i < len(runes); {
		if runes[i] == '@' {
			var name strings.Builder
			j := i + 1
			for j < len(runes) && (isIdentRune(runes[j]) || (runes[j] >= '0' && runes[j] <= '9')) {
				name.WriteRune(runes[j])
				j++
			}
			if name.Len() > 0 {
				result.WriteString(os.Getenv(name.String()))
				i = j
				continue
			}
		}
		result.WriteRune(runes[i])
		i++
	}
	return result.String()
}

func completionVarPrefix(line string, char int) (string, bool) {
	runes := []rune(line)
	if char > len(runes) {
		char = len(runes)
	}
	j := char
	for j > 0 && isVarIdentRune(runes[j-1]) {
		j--
	}
	if j == 0 || runes[j-1] != '&' {
		return "", false
	}
	return string(runes[j:char]), true
}

func isVarIdentRune(r rune) bool {
	return isIdentRune(r) || (r >= '0' && r <= '9') || r == '.' || r == '-'
}

func completionVarItems(data *pkg.ParsedData, lines []string, lineIdx int, prefix string) []completionItem {
	items := []completionItem{}
	seen := map[string]bool{}

	add := func(label, filter string, kind int) {
		if seen[label] {
			return
		}
		seen[label] = true
		items = append(items, completionItem{Label: label, FilterText: filter, Kind: kind})
	}

	if dot := strings.LastIndexByte(prefix, '.'); dot >= 0 {
		if c := resolveCommandRef(data, prefix[:dot]); c != nil {
			named := 0
			shellIdx := 0
			for _, stmt := range c.Body {
				if stmt.Type != pkg.StmtShell {
					continue
				}
				if stmt.OutputName != "" {
					add(c.Name+"."+stmt.OutputName, stmt.OutputName, 6) // Variable
					named++
				}
				shellIdx++
			}
			if named == 0 {
				for i := 0; i < shellIdx; i++ {
					label := c.Name + "." + strconv.Itoa(i)
					add(label, strconv.Itoa(i), 6)
				}
			}
		}
	}

	for _, v := range data.Variables {
		if strings.HasPrefix(v.Name, prefix) {
			add(v.Name, v.Name, 6) // Variable
		}
	}
	if cmd := enclosingCommand(lines, lineIdx, data); cmd != nil {
		for _, a := range cmd.Arguments {
			if strings.HasPrefix(a.Name, prefix) {
				add(a.Name, a.Name, 14) // Parameter
			}
		}
	}
	return items
}

// resolveCommandRef finds the longest command that is a prefix of name
// (handles namespaced commands like "lib.gen").
func resolveCommandRef(data *pkg.ParsedData, name string) *pkg.Command {
	for i := len(name); i > 0; i = strings.LastIndexByte(name[:i], '.') {
		if cmd, err := data.GetCommand(name[:i]); err == nil {
			return cmd
		}
	}
	return nil
}

func isPrereqListLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || line != strings.TrimLeft(line, " \t") {
		return false // body lines are indented; headers start at column 0
	}
	if strings.HasPrefix(trimmed, "$") {
		return false
	}
	for _, kw := range []string{"if ", "for ", "matrix ", "env ", "invoke ", "else", "continue", "break"} {
		if strings.HasPrefix(trimmed, kw) {
			return false
		}
	}
	return strings.Contains(line, "<")
}

func enclosingCommand(lines []string, lineIdx int, data *pkg.ParsedData) *pkg.Command {
	for i := lineIdx; i >= 0; i-- {
		if name, ok := commandNameAtLine(lines[i]); ok {
			if cmd, err := data.GetCommand(name); err == nil {
				return cmd
			}
		}
	}
	return nil
}
