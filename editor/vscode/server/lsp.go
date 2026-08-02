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
	Label string `json:"label"`
	Kind  int    `json:"kind"`
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

type initializeParams struct {
	RootURI string `json:"rootUri"`
}

type initializeResult struct {
	Capabilities serverCapabilities `json:"capabilities"`
}

type serverCapabilities struct {
	TextDocumentSync   int  `json:"textDocumentSync"`
	HoverProvider      bool `json:"hoverProvider"`
	DefinitionProvider bool `json:"definitionProvider"`
	CompletionProvider bool `json:"completionProvider"`
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

func (s *server) handleInitialize(params json.RawMessage) (interface{}, error) {
	return initializeResult{
		Capabilities: serverCapabilities{
			TextDocumentSync:   1, // full document sync
			HoverProvider:      true,
			DefinitionProvider: true,
			CompletionProvider: true,
		},
	}, nil
}

func (s *server) handleDidOpen(params json.RawMessage) (interface{}, error) {
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

func (s *server) handleDidChange(params json.RawMessage) (interface{}, error) {
	var p didChangeTextDocumentParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("bad params: %w", err)
	}
	if len(p.ContentChanges) > 0 {
		s.updateDoc(p.TextDocument.URI, p.ContentChanges[0].Text, p.TextDocument.Version)
	}
	return nil, nil
}

// updateDoc re-parses the document and publishes diagnostics.
func (s *server) updateDoc(uri, text string, version int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	parser := pkg.NewParserFromContent(uri, text)
	data, err := parser.Parse()
	if err != nil {
		// Keep last good data if present; diagnostics carry the error.
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
	endLine := len(lines) - 1
	if endLine < 0 {
		endLine = 0
	}
	endChar := len(lines[endLine])
	return range_{
		Start: position{Line: 0, Character: 0},
		End:   position{Line: endLine, Character: endChar},
	}
}

// duplicatePrereqWarnings scans command headers for duplicate prerequisites and
// generates warning diagnostics on the duplicate occurrences.
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

		// Split on commas and identify prereq names (tokens that are known commands).
		seen := map[string]bool{}
		searchPos := 0
		for _, part := range strings.Split(segment, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			// Skip the "in <dir>" workdir modifier and path-like tokens.
			if part == "in" || strings.Contains(part, " in ") || strings.ContainsAny(part, "/\\") {
				searchPos += len(part) + 1
				continue
			}
			// Check if this is a known command name.
			if _, err := data.GetCommand(part); err != nil {
				searchPos += len(part) + 1
				continue
			}
			if seen[part] {
				// Find the second occurrence's column in the full line.
				absCol := strings.Index(line[searchPos:], part)
				if absCol >= 0 {
					absCol += searchPos
				} else {
					absCol = strings.LastIndex(line, part)
				}
				if absCol < 0 {
					absCol = 0
				}
				diags = append(diags, diagnostic{
					Range:    refRange(lineIdx, absCol, len(part)),
					Severity: sevWarning,
					Source:   "constfile",
					Message:  fmt.Sprintf("duplicate prerequisite `%s`", part),
				})
			}
			seen[part] = true
			// Advance searchPos past this occurrence.
			idx := strings.Index(line[searchPos:], part)
			if idx >= 0 {
				searchPos += idx + len(part)
			}
		}
	}
	return diags
}

// namedOutputHints scans the document for &prereq.suffix references and validates
// them. Generates: warnings for positional refs with named alternatives; errors
// for invalid indices, unknown named outputs, or references to non-existent commands.
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

			dot := strings.IndexByte(name, '.')
			if dot < 0 {
				continue // plain &var, not a prereq output ref
			}

			cmdName := name[:dot]
			suffix := name[dot+1:]

			// Look up the referenced command.
			cmd, err := data.GetCommand(cmdName)
			if err != nil || cmd == nil {
				diags = append(diags, diagnostic{
					Range:    refRange(lineIdx, absIdx, refLen),
					Severity: sevError,
					Source:   "constfile",
					Message:  fmt.Sprintf("unknown command `%s` in output reference `&%s`", cmdName, name),
				})
				continue
			}

			// Count shell lines (each produces one output).
			shellCount := countShellLines(cmd.Body)

			if idx, err := strconv.Atoi(suffix); err == nil {
				// Numeric index: validate bounds.
				if idx < 0 || idx >= shellCount {
					diags = append(diags, diagnostic{
						Range:    refRange(lineIdx, absIdx, refLen),
						Severity: sevError,
						Source:   "constfile",
						Message:  fmt.Sprintf("index %d out of bounds: `%s` has %d output line(s)", idx, cmdName, shellCount),
					})
					continue
				}
				// Check if a named output is available at this index.
				if hint := namedOutputAt(data, cmdName, idx); hint != "" {
					diags = append(diags, diagnostic{
						Range:    refRange(lineIdx, absIdx, refLen),
						Severity: sevWarning,
						Source:   "constfile",
						Message:  fmt.Sprintf("💡 named output available: use &%s instead of &%s", hint, name),
					})
				}
			} else {
				// Named output: validate it exists.
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
		case "shell":
			count++
		case "if":
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
		if stmt.Type == "if" {
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
		if stmt.Type != "shell" {
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

// ---- hover ----

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

	// Hover over a command header line.
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

	// Hover over the directory path in an "in <dir>" modifier.
	if dir, _, _, ok := workDirAtPosition(line, char); ok {
		resolved := resolveWorkDir(dir, p.TextDocument.URI)
		return hoverResult{
			Contents: markupContent{
				Kind:  "markdown",
				Value: fmt.Sprintf("📂 working directory: `%s`\n\nresolved: `%s`", dir, resolved),
			},
		}, nil
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

// varHoverMessage resolves a &reference name to a hover string. For prereq
// output references (&prereq.N or &prereq.name), it derives info from the
// referenced command's body. If a positional index has a named output, a hint
// suggests the named form.
func varHoverMessage(name string, data *pkg.ParsedData) string {
	dot := strings.IndexByte(name, '.')
	if dot < 0 {
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

	// Dotted reference: &prereq.N or &prereq.name
	cmdName := name[:dot]
	suffix := name[dot+1:]
	cmd, err := data.GetCommand(cmdName)
	if err != nil || cmd == nil {
		return ""
	}

	// Find the shell line at this index/name.
	shellIdx := 0
	for _, stmt := range cmd.Body {
		if stmt.Type != "shell" {
			continue
		}
		matches := false
		var namedHint string
		if _, err := strconv.Atoi(suffix); err == nil {
			// Numeric index
			if shellIdx == int(stringToInt(suffix)) {
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
	return fmt.Sprintf("`%s` → output of `%s`", name, cmdName)
}

func stringToInt(s string) int64 {
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int64(c-'0')
	}
	return n
}

// ---- definition ----

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

	// Go-to-definition for &varName references.
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
	}

	// Go-to-definition for a prerequisite name the cursor is on. The cursor
	// must be within the "< ... {" portion of a command header line.
	if target, ok := prereqNameAtPosition(line, char); ok {
		for i, l := range lines {
			if cn, _ := commandNameAtLine(l); cn == target {
				// Find the exact column span of the command name on its header.
				col := strings.Index(l, cn)
				if col < 0 {
					col = 0
				}
				return location{
					URI: p.TextDocument.URI,
					Range: range_{
						Start: position{Line: i, Character: col},
						End:   position{Line: i, Character: col + len(cn)},
					},
				}, nil
			}
		}
	}

	// Ctrl-click on the directory path in "in <dir>" → open the folder.
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

	// Ctrl-click on a command's own header name → jump to itself (so the click
	// registers and VSCode reveals the definition location).
	if name, isCmd := commandNameAtLine(line); isCmd {
		if _, err := doc.data.GetCommand(name); err == nil {
			col := strings.Index(line, name)
			if col < 0 {
				col = 0
			}
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

// ---- completion ----

func (s *server) handleCompletion(params json.RawMessage) (interface{}, error) {
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

	// After a '&' we complete variable names.
	if trigger := completionTriggerVar(line, p.Position.Character); trigger {
		for _, v := range doc.data.Variables {
			items = append(items, completionItem{Label: v.Name, Kind: 6}) // Variable
		}
		return completionList{Items: items}, nil
	}

	// In a prereq list we complete command names.
	if isPrereqListLine(line) {
		for _, c := range doc.data.Commands {
			items = append(items, completionItem{Label: c.Name, Kind: 3}) // Function
		}
		return completionList{Items: items}, nil
	}

	return completionList{Items: items}, nil
}

// ---- helpers ----

// refAtPosition finds a &varName or &prereq.name reference overlapping the
// given character. Returns the literal (e.g. "&foo.bar") and the full name
// ("foo.bar") including any dotted suffix.
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

// envRefAtPosition finds an @ENVNAME reference overlapping the given character.
// Returns the env var name (without the @) and whether a reference was found.
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

// commandNameAtLine extracts a command name from a header line and reports
// whether this line looks like a command declaration.
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

	// Regular command name followed by (, <, {, or " in " workdir modifier.
	// Find the earliest terminator.
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
	if eq := strings.IndexByte(s, '='); eq >= 0 {
		return strings.TrimSpace(s[:eq])
	}
	return s
}

// prereqNameAtPosition returns the prerequisite name under the cursor if the
// cursor sits within the "< ... {" portion of a command header line.
func prereqNameAtPosition(line string, char int) (string, bool) {
	lt := strings.IndexByte(line, '<')
	if lt < 0 {
		return "", false
	}
	// End of the prereq region: the opening brace (or end of line).
	end := strings.IndexByte(line[lt:], '{')
	var region string
	if end >= 0 {
		region = line[lt+1 : lt+end]
	} else {
		region = line[lt+1:]
	}
	// Absolute offset of the region start within the full line.
	regionStart := lt + 1
	if char < regionStart || char > regionStart+len(region) {
		return "", false
	}
	// Extract the identifier containing the cursor position.
	relChar := char - regionStart
	runes := []rune(region)
	// Walk left to the start of the identifier.
	start := relChar
	for start > 0 && isPrereqIdentRune(runes[start-1]) {
		start--
	}
	// Walk right to the end.
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
	return isIdentRune(r) || (r >= '0' && r <= '9') || r == '-'
}

// workDirAtPosition returns the directory path text and its column span if the
// cursor sits within the "in <dir>" portion of a command header line.
func workDirAtPosition(line string, char int) (dir string, startCol, endCol int, ok bool) {
	// Find " in " in the line (the workdir modifier keyword).
	inIdx := strings.Index(line, " in ")
	if inIdx < 0 {
		return "", 0, 0, false
	}
	dirStart := inIdx + 4 // skip " in "

	// The directory extends to the opening brace or end of line.
	rest := line[dirStart:]
	braceIdx := strings.IndexByte(rest, '{')
	var dirEnd int
	if braceIdx >= 0 {
		dirEnd = dirStart + braceIdx
	} else {
		dirEnd = len(line)
	}

	dir = strings.TrimSpace(line[dirStart:dirEnd])
	if dir == "" {
		return "", 0, 0, false
	}

	// Recompute the trimmed column span for the actual directory text.
	trimStart := dirStart + strings.Index(line[dirStart:dirEnd], dir)
	trimEnd := trimStart + len(dir)

	if char < trimStart || char > trimEnd {
		return "", 0, 0, false
	}

	return dir, trimStart, trimEnd, true
}

// resolveWorkDir resolves a workdir path (which may be relative) against the
// document's directory. Also resolves @env references.
func resolveWorkDir(dir, docURI string) string {
	// Resolve @env references in the directory.
	dir = resolveEnvRefsInString(dir)

	// If already absolute, use as-is.
	if filepath.IsAbs(dir) {
		return filepath.Clean(dir)
	}

	// Resolve relative to the document's directory.
	docPath := uriToPath(docURI)
	if docPath == "" {
		return dir
	}
	docDir := filepath.Dir(docPath)
	resolved := filepath.Join(docDir, dir)
	return filepath.Clean(resolved)
}

// pathToURI converts a native filesystem path to a file:// URI.
func pathToURI(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = p
	}
	u := &url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}
	return u.String()
}

// uriToPath converts a file:// URI to a native filesystem path.
func uriToPath(uri string) string {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" {
		return ""
	}
	p := u.Path
	// On Windows, file URIs are like /c%3A/Users/... — strip leading / and
	// the URL package already decodes %3A to ':'.
	if len(p) > 2 && p[0] == '/' && p[2] == ':' {
		p = p[1:]
	}
	return filepath.FromSlash(p)
}

// resolveEnvRefsInString replaces @ENVNAME with env values.
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

// completionTriggerVar reports whether the cursor is positioned right after
// an ampersand that begins a variable reference.
func completionTriggerVar(line string, char int) bool {
	runes := []rune(line)
	if char == 0 || char > len(runes) {
		return false
	}
	return runes[char-1] == '&'
}

func isPrereqListLine(line string) bool {
	return strings.Contains(line, "<") && !strings.Contains(line, "{")
}
