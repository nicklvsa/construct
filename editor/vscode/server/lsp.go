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

// LSP types, hand-rolled to avoid a framework dependency.

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
	TextDocumentSync       int                `json:"textDocumentSync"`
	HoverProvider          bool               `json:"hoverProvider"`
	DefinitionProvider     bool               `json:"definitionProvider"`
	DocumentSymbolProvider bool               `json:"documentSymbolProvider"`
	CompletionProvider     *completionOptions `json:"completionProvider,omitempty"`
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
)

type docState struct {
	text  string
	lines []string
	data  *pkg.ParsedData
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
	case "textDocument/documentSymbol":
		return s.handleDocumentSymbol(params)
	case "textDocument/didClose":
		return s.handleDidClose(params)
	default:
		return nil, errMethodNotFound
	}
}

var errMethodNotFound = fmt.Errorf("method not found")

func (s *server) handleDidClose(params json.RawMessage) (any, error) {
	var p struct {
		TextDocument struct {
			URI string `json:"uri"`
		} `json:"textDocument"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("bad params: %w", err)
	}
	s.mu.Lock()
	delete(s.docs, p.TextDocument.URI)
	s.mu.Unlock()
	s.publishDiagnostics(p.TextDocument.URI, nil)
	return nil, nil
}

func (s *server) handleInitialize(_ json.RawMessage) (any, error) {
	return initializeResult{
		Capabilities: serverCapabilities{
			TextDocumentSync:       1, // full document sync
			HoverProvider:          true,
			DefinitionProvider:     true,
			DocumentSymbolProvider: true,
			CompletionProvider:     &completionOptions{TriggerCharacters: []string{"&"}},
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
	s.updateDoc(item.URI, item.Text)
	return nil, nil
}

func (s *server) handleDidChange(params json.RawMessage) (any, error) {
	var p didChangeTextDocumentParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("bad params: %w", err)
	}
	if len(p.ContentChanges) > 0 {
		s.updateDoc(p.TextDocument.URI, p.ContentChanges[0].Text)
	}
	return nil, nil
}

func (s *server) updateDoc(uri, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	lines := strings.Split(text, "\n")
	parser := pkg.NewParserFromContent(uri, text)
	data, err := parser.Parse()
	if err != nil {
		st := &docState{text: text, lines: lines}
		if old, ok := s.docs[uri]; ok {
			st.data = old.data
		}
		s.docs[uri] = st
		s.publishDiagnostics(uri, parseErrorToDiagnostics(err, uri, lines))
		return
	}

	s.docs[uri] = &docState{text: text, lines: lines, data: data}
	s.publishDiagnostics(uri, lintDiagnostics(pkg.Lint(lines, data, uriToPathDir(uri)), lines))
}

func (s *server) publishDiagnostics(uri string, diags []diagnostic) {
	writeNotification(s.out, "textDocument/publishDiagnostics", publishDiagnosticsParams{
		URI:         uri,
		Diagnostics: diags,
	})
}

func parseErrorToDiagnostics(err error, uri string, lines []string) []diagnostic {
	sev := sevError

	switch e := err.(type) {
	case *pkg.ParseError:
		if docPath := uriToPath(uri); docPath != "" && e.File != "" && e.File != uri && e.File != docPath {
			// Imported-file error: its lines don't map onto this document.
			return []diagnostic{{
				Range:    fullRange(lines),
				Severity: sev,
				Source:   "constfile",
				Message:  e.Error(),
			}}
		}
		line := e.Line
		if line < 1 {
			line = 1
		}
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
			Range:    fullRange(lines),
			Severity: sevError,
			Source:   "constfile",
			Message:  e.Error(),
		}}
	case *pkg.MissingDependencyError:
		return []diagnostic{{
			Range:    fullRange(lines),
			Severity: sevError,
			Source:   "constfile",
			Message:  e.Error(),
		}}
	}

	return []diagnostic{{
		Range:    fullRange(lines),
		Severity: sev,
		Source:   "constfile",
		Message:  err.Error(),
	}}
}

// lintDiagnostics renders pkg.Lint issues as LSP diagnostics. Token-range
// issues (EndCol > 0) underline the reference; others cover their line;
// file-wide issues cover the whole document.
func lintDiagnostics(issues []pkg.LintIssue, lines []string) []diagnostic {
	sevMap := map[int]int{pkg.LintError: sevError, pkg.LintWarning: sevWarning, pkg.LintInfo: sevWarning}
	diags := []diagnostic{}
	for _, is := range issues {
		rng := fullRange(lines)
		if is.EndCol > 0 {
			rng = range_{
				Start: position{Line: is.Line, Character: is.Col},
				End:   position{Line: is.Line, Character: is.EndCol},
			}
		} else if is.Line >= 0 {
			end := 0
			if is.Line < len(lines) {
				end = len(lines[is.Line])
			}
			rng = range_{
				Start: position{Line: is.Line, Character: 0},
				End:   position{Line: is.Line, Character: end},
			}
		}
		diags = append(diags, diagnostic{
			Range:    rng,
			Severity: sevMap[is.Severity],
			Source:   "constfile",
			Message:  is.Message,
		})
	}
	return diags
}

func uriToPathDir(uri string) string {
	if p := uriToPath(uri); p != "" {
		return filepath.Dir(p)
	}
	return "."
}

func fullRange(lines []string) range_ {
	endLine := max(len(lines)-1, 0)
	endChar := len(lines[endLine])
	return range_{
		Start: position{Line: 0, Character: 0},
		End:   position{Line: endLine, Character: endChar},
	}
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

	lines := doc.lines
	if p.Position.Line >= len(lines) {
		return nil, nil
	}
	line := lines[p.Position.Line]
	char := p.Position.Character

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

			if invoked, ln, found := pkg.InvokeCaptureHint(cmd.Body, name); found {
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

		if msg, ok := failContextHover(lines, p.Position.Line, name); ok {
			return hoverResult{
				Contents: markupContent{Kind: "markdown", Value: msg},
			}, nil
		}

		if msg, ok := lastResultHover(name); ok {
			return hoverResult{
				Contents: markupContent{Kind: "markdown", Value: msg},
			}, nil
		}
	}

	if prefixHover, ok := linePrefixHover(line, char); ok {
		return hoverResult{
			Contents: markupContent{Kind: "markdown", Value: prefixHover},
		}, nil
	}

	// Argument declarations in a command header: `deploy (env, opt region)`.
	if arg, ok := headerArgAtPosition(line, char); ok {
		if arg == "opt" {
			return hoverResult{
				Contents: markupContent{Kind: "markdown", Value: "`opt` — marks the preceding argument declaration as optional."},
			}, nil
		}
		if cmd := enclosingCommand(lines, p.Position.Line, doc.data); cmd != nil {
			for _, a := range cmd.Arguments {
				if a.Name == arg {
					msg := fmt.Sprintf("`%s` — argument of command `%s`\n\npass with `--%s:%s=<value>`", arg, cmd.Name, cmd.Name, a.Name)
					if a.IsOptional {
						msg += "\n\noptional"
					}
					if a.Default != "" {
						msg += fmt.Sprintf("\n\ndefault: `%s`", a.Default)
					}
					return hoverResult{
						Contents: markupContent{Kind: "markdown", Value: msg},
					}, nil
				}
			}
		}
	}

	// Keywords/builtins — never in shell text after a `$`.
	if word, ok := wordAtPosition(line, char); ok && !shellLineContentAt(line, char) {
		// A word used as a call (`env("X")`) is a function, not a keyword.
		if strings.Contains(line, word+"(") {
			if msg, found := functionHover(word); found {
				return hoverResult{
					Contents: markupContent{Kind: "markdown", Value: msg},
				}, nil
			}
		}
		if msg, found := keywordHover(word); found {
			return hoverResult{
				Contents: markupContent{Kind: "markdown", Value: msg},
			}, nil
		}
		if msg, found := functionHover(word); found {
			return hoverResult{
				Contents: markupContent{Kind: "markdown", Value: msg},
			}, nil
		}
	}

	if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "onfail ") || trimmed == "onfail{" {
		return hoverResult{
			Contents: markupContent{
				Kind:  "markdown",
				Value: "runs once when any later statement in this command fails\n\nfailure context available: `&fail.message`, `&fail.line`, `&fail.exit`",
			},
		}, nil
	}

	// Must precede the @ENV hover, which would misread @state as an env var.
	if name, ok := stateRefAtPosition(line, char); ok {
		msg := fmt.Sprintf("`state(\"%s\")` — persisted state", name)
		for _, d := range doc.data.StateDecls {
			if d.Name == name {
				msg += fmt.Sprintf("\n\ndeclared value: `%s`", d.Value)
			}
		}
		msg += "\n\npersists across runs in `.construct-cache/state.json`"
		return hoverResult{
			Contents: markupContent{Kind: "markdown", Value: msg},
		}, nil
	}

	if envName, ok := envRefAtPosition(line, char); ok {
		val := os.Getenv(envName)
		var msg string
		switch {
		case val != "":
			msg = fmt.Sprintf("`@%s` (environment variable)\n\nresolved to: `%s`", envName, val)
		case envDefaultOnLine(line, envName) != "":
			msg = fmt.Sprintf("`@%s` (environment variable)\n\n⚠ not set — default: `%s`", envName, envDefaultOnLine(line, envName))
		default:
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

	if fd, _, _, ok := fileDepAtPosition(line, char); ok {
		resolved := resolveWorkDir(fd, p.TextDocument.URI)
		msg := fmt.Sprintf("`%s` — file dependency\n\nresolved: `%s`", fd, resolved)
		if info, err := os.Stat(resolved); err == nil {
			if info.IsDir() {
				msg += fmt.Sprintf("\n\ndirectory — modified %s", info.ModTime().Format("2006-01-02 15:04"))
			} else {
				msg += fmt.Sprintf("\n\n%s — modified %s", humanSize(info.Size()), info.ModTime().Format("2006-01-02 15:04"))
			}
		} else if !strings.ContainsAny(resolved, "*?") {
			msg += "\n\n⚠ missing (the command always runs)"
		}
		return hoverResult{
			Contents: markupContent{Kind: "markdown", Value: msg},
		}, nil
	}

	if name, isCmd := commandNameAtLine(line); isCmd {
		if _, err := doc.data.GetCommand(name); err == nil {
			if dir, _, _, ok := workDirAtPosition(line, char); ok {
				resolved := resolveWorkDir(dir, p.TextDocument.URI)
				msg := fmt.Sprintf("📂 working directory: `%s`\n\nresolved: `%s`", dir, resolved)
				if info, err := os.Stat(resolved); err == nil && info.IsDir() {
					msg += "\n\nexists ✓"
				}
				return hoverResult{
					Contents: markupContent{Kind: "markdown", Value: msg},
				}, nil
			}
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

	return nil, nil
}

func commandHover(c *pkg.Command) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**%s**\n\n", c.Name)
	if c.Description != "" {
		fmt.Fprintf(&b, "%s\n\n", c.Description)
	}
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
	if c.Container != "" {
		fmt.Fprintf(&b, "- container: `%s`\n", c.Container)
	}
	if c.Manual {
		b.WriteString("- manual entry point (excluded from unreferenced checks)\n")
	}
	if c.Timeout != "" {
		fmt.Fprintf(&b, "- timeout: `%s`\n", c.Timeout)
	}
	if len(c.Produces) > 0 {
		fmt.Fprintf(&b, "- produces: `%s`\n", strings.Join(c.Produces, "`, `"))
	}
	if len(c.Body) > 0 {
		fmt.Fprintf(&b, "- %d body statement(s)\n", len(c.Body))
	}
	return b.String()
}

func varHoverMessage(name string, data *pkg.ParsedData) string {
	cmdName, suffix, ok := pkg.SplitCommandRef(data, name)
	if !ok {
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

	for shellIdx, stmt := range pkg.ShellStatements(cmd.Body) {
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

	lines := doc.lines
	if p.Position.Line >= len(lines) {
		return nil, nil
	}
	line := lines[p.Position.Line]
	char := p.Position.Character

	if name, ok := stateRefAtPosition(line, char); ok {
		for i, l := range lines {
			trimmed := strings.TrimSpace(l)
			if !strings.HasPrefix(trimmed, "state ") {
				continue
			}
			s := strings.TrimSpace(strings.TrimPrefix(trimmed, "state"))
			declName, _, _ := strings.Cut(s, "=")
			declName = strings.TrimSpace(declName)
			if declName == name {
				col := strings.Index(l, declName)
				return location{
					URI: p.TextDocument.URI,
					Range: range_{
						Start: position{Line: i, Character: max(col, 0)},
						End:   position{Line: i, Character: max(col, 0) + len(declName)},
					},
				}, nil
			}
		}
	}

	if _, name := refAtPosition(line, char); name != "" {
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
			if _, ln, found := pkg.InvokeCaptureHint(cmd.Body, name); found && ln > 0 {
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

	lines := doc.lines
	if p.Position.Line >= len(lines) {
		return nil, nil
	}

	line := lines[p.Position.Line]
	items := []completionItem{}

	if prefix, ok := completionVarPrefix(line, p.Position.Character); ok {
		return completionList{Items: completionVarItems(doc.data, lines, p.Position.Line, prefix)}, nil
	}

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

	if word, atStart := lineStartWord(line, p.Position.Character); atStart {
		for _, kw := range statementKeywords {
			if strings.HasPrefix(kw, word) {
				items = append(items, completionItem{Label: kw, Kind: 14}) // Keyword
			}
		}
		return completionList{Items: items}, nil
	}

	if strings.HasPrefix(leadTrim, "var ") || strings.HasPrefix(leadTrim, "state ") ||
		strings.HasPrefix(leadTrim, "global ") || strings.HasPrefix(leadTrim, "env ") {
		if word, _ := wordAtPosition(line, p.Position.Character); word != "" {
			for _, fn := range builtinFunctions {
				if strings.HasPrefix(fn, word) {
					items = append(items, completionItem{Label: fn + "()", FilterText: fn, Kind: 3}) // Function
				}
			}
		}
	}

	return completionList{Items: items}, nil
}

var statementKeywords = []string{
	"var", "import", "switch", "case", "default", "in", "lock", "state",
	"confirm", "prompt", "input", "timeout",
	"cp", "rm", "mkdir", "touch", "download", "extract",
	"for", "if", "matrix", "env", "invoke", "fail", "global",
	"require_env", "retry", "onfail", "continue", "break",
}

var builtinFunctions = []string{
	"exists", "missing", "glob", "require", "file", "lines", "sha256",
	"basename", "dirname", "ext", "stem", "upper", "lower", "trim",
	"replace", "sprintf", "length", "abs", "min", "max", "date", "uuid",
	"len", "sort", "uniq", "join", "split", "env", "state",
}

// lineStartWord returns the word at the start of a line (only whitespace before).
func lineStartWord(line string, char int) (string, bool) {
	if char > len(line) {
		char = len(line)
	}
	prefix := line[:char]
	trimmed := strings.TrimLeft(prefix, " \t")
	if trimmed == "" {
		return "", true
	}
	for _, r := range trimmed {
		if !isIdentRune(r) {
			return "", false
		}
	}
	return trimmed, true
}

// headerArgAtPosition reports the word under char inside a header's argument
// list, e.g. `deploy (env, opt region) {`.
func headerArgAtPosition(line string, char int) (string, bool) {
	open := strings.IndexByte(line, '(')
	if open < 0 {
		return "", false
	}
	closeIdx := strings.IndexByte(line[open:], ')')
	if closeIdx < 0 {
		return "", false
	}
	closeIdx += open
	if char <= open || char >= closeIdx {
		return "", false
	}
	return wordAtPosition(line, char)
}

// envDefaultOnLine extracts the :-default of an @ENV reference on the line.
func envDefaultOnLine(line, envName string) string {
	idx := strings.Index(line, "@"+envName+":-")
	if idx < 0 {
		return ""
	}
	rest := line[idx+1+len(envName)+2:]
	end := strings.IndexAny(rest, " \t\r\n\"',;&$")
	if end < 0 {
		end = len(rest)
	}
	return rest[:end]
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func wordAtPosition(line string, char int) (string, bool) {
	runes := []rune(line)
	if char > len(runes) {
		char = len(runes)
	}
	start := char
	for start > 0 && isIdentRune(runes[start-1]) {
		start--
	}
	end := char
	for end < len(runes) && isIdentRune(runes[end]) {
		end++
	}
	if start == end {
		return "", false
	}
	return string(runes[start:end]), true
}

type documentSymbol struct {
	Name           string `json:"name"`
	Kind           int    `json:"kind"`
	Detail         string `json:"detail,omitempty"`
	Range          range_ `json:"range"`
	SelectionRange range_ `json:"selectionRange"`
}

func (s *server) handleDocumentSymbol(params json.RawMessage) (interface{}, error) {
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
	var symbols []documentSymbol
	for _, cmd := range doc.data.Commands {
		if pkg.IsLazyName(cmd.Name) {
			continue
		}
		// Imported commands' SourceLines belong to other files.
		if strings.TrimPrefix(cmd.SourceFile, "file://") != strings.TrimPrefix(p.TextDocument.URI, "file://") {
			continue
		}
		if cmd.SourceLine <= 0 || cmd.SourceLine-1 >= len(lines) {
			continue
		}
		headerLine := lines[cmd.SourceLine-1]
		nameCol := strings.Index(headerLine, cmd.Name)
		if nameCol < 0 {
			continue
		}
		selection := range_{
			Start: position{Line: cmd.SourceLine - 1, Character: nameCol},
			End:   position{Line: cmd.SourceLine - 1, Character: nameCol + len(cmd.Name)},
		}

		endLine := cmd.SourceLine - 1
		depth := netBraces(headerLine)
		for l := cmd.SourceLine; l < len(lines) && depth > 0; l++ {
			depth += netBraces(lines[l])
			endLine = l
		}
		detail := ""
		if cmd.IsDefault {
			detail = "default command"
		} else if len(cmd.Prereqs) > 0 {
			detail = "depends on " + strings.Join(cmd.Prereqs, ", ")
		}
		symbols = append(symbols, documentSymbol{
			Name:           cmd.Name,
			Kind:           12, // Function
			Detail:         detail,
			Range:          range_{Start: position{Line: cmd.SourceLine - 1, Character: 0}, End: position{Line: endLine, Character: len(lines[endLine])}},
			SelectionRange: selection,
		})
	}

	for i, l := range lines {
		trimmed := strings.TrimSpace(l)
		if !strings.HasPrefix(trimmed, "state ") {
			continue
		}
		s := strings.TrimSpace(strings.TrimPrefix(trimmed, "state"))
		name, _, ok := strings.Cut(s, "=")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		if !isIdentRune(rune(name[0])) {
			continue
		}
		col := strings.Index(l, name)
		if col < 0 {
			continue
		}
		symbols = append(symbols, documentSymbol{
			Name:           name,
			Kind:           13, // Variable
			Detail:         "persisted state",
			Range:          range_{Start: position{Line: i, Character: 0}, End: position{Line: i, Character: len(l)}},
			SelectionRange: range_{Start: position{Line: i, Character: col}, End: position{Line: i, Character: col + len(name)}},
		})
	}
	if symbols == nil {
		symbols = []documentSymbol{}
	}
	return symbols, nil
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
	cmdName, suffix, ok := pkg.SplitCommandRef(doc.data, ref)
	if !ok {
		return location{}, false
	}
	cmd, err := doc.data.GetCommand(cmdName)
	if err != nil || cmd == nil {
		return location{}, false
	}

	stmts := pkg.ShellStatements(cmd.Body)
	var hit *pkg.BodyStatement
	for i := range stmts {
		stmt := &stmts[i]
		match := false
		if idx, err := strconv.Atoi(suffix); err == nil {
			match = i == idx
		} else if stmt.OutputName == suffix {
			match = true
		}
		if match {
			hit = stmt
			break
		}
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

// stateRefAtPosition finds a state("name")/@state("name") reference covering char.
func stateRefAtPosition(line string, char int) (string, bool) {
	for _, marker := range []string{"@state(", "state("} {
		idx := 0
		for {
			rel := strings.Index(line[idx:], marker)
			if rel < 0 {
				break
			}
			start := idx + rel
			quote := strings.IndexByte(line[start+len(marker):], '"')
			if quote < 0 {
				break
			}
			open := start + len(marker) + quote + 1
			closeQ := strings.IndexByte(line[open:], '"')
			if closeQ < 0 {
				break
			}
			name := line[open : open+closeQ]
			end := open + closeQ + 1
			if char >= start && char <= end {
				return name, true
			}
			idx = end + 1
		}
	}
	return "", false
}

func isIdentRune(r rune) bool {
	return r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

// commandNameAtLine guesses the header's command name via the parser's own
// header logic. Callers must validate with GetCommand; shell lines match nothing.
func commandNameAtLine(line string) (string, bool) {
	line, _ = pkg.StripManual(line)
	name := pkg.ParseCommandName(line)
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
		if part == "" || !pkg.IsFileDep(part) {
			continue
		}
		idx := strings.Index(region, part)
		if pos >= idx && pos < idx+len(part) {
			return part, regionStart + idx, regionStart + idx + len(part), true
		}
	}
	return "", 0, 0, false
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
	dir = pkg.ResolveEnvRefs(dir)
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
			for _, stmt := range pkg.ShellStatements(c.Body) {
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

	// &last.* context refs are set by every shell statement.
	if strings.HasPrefix(prefix, "last.") {
		for _, l := range []string{"last.exit", "last.output"} {
			if strings.HasPrefix(l, prefix) {
				add(l, strings.TrimPrefix(l, "last."), 6) // Variable
			}
		}
	}

	// Inside an onfail block, &fail.* context refs are available.
	if strings.HasPrefix(prefix, "fail.") && inOnFailBlock(lines, lineIdx) {
		for _, f := range []string{"fail.message", "fail.line", "fail.exit"} {
			if strings.HasPrefix(f, prefix) {
				add(f, strings.TrimPrefix(f, "fail."), 6) // Variable
			}
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

// resolveCommandRef finds name itself or its longest command prefix.
func resolveCommandRef(data *pkg.ParsedData, name string) *pkg.Command {
	if cmdName, _, ok := pkg.SplitCommandRef(data, name); ok {
		name = cmdName
	}
	if cmd, err := data.GetCommand(name); err == nil {
		return cmd
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
	for _, kw := range []string{"var ", "import ", "if ", "for ", "matrix ", "env ", "invoke ", "onfail ", "fail ", "global ", "require_env ", "retry ", "else", "continue", "break", "switch ", "case ", "default", "in ", "lock ", "state ", "confirm ", "prompt ", "input ", "timeout ", "cp ", "rm ", "mkdir ", "touch ", "download ", "extract "} {
		if strings.HasPrefix(trimmed, kw) {
			return false
		}
	}
	return strings.Contains(line, "<")
}

// netBraces counts braces on a line, ignoring shell lines (starting with `$`
// or `!`) so `awk '{print}'` and `${var}` don't skew the depth.
func netBraces(line string) int {
	trimmed := strings.TrimLeft(line, " \t")
	if strings.HasPrefix(trimmed, "$") || strings.HasPrefix(trimmed, "!") {
		return 0
	}
	return strings.Count(line, "{") - strings.Count(line, "}")
}

// inOnFailBlock reports whether lineIdx is inside an `onfail` block, walking
// up counting braces until an unmatched closer appears.
func inOnFailBlock(lines []string, lineIdx int) bool {
	depth := 0
	for i := lineIdx; i >= 0; i-- {
		line := lines[i]
		depth -= netBraces(line)
		if depth < 0 && (strings.HasPrefix(strings.TrimSpace(line), "onfail ") || strings.TrimSpace(line) == "onfail{") {
			return true
		}
	}
	return false
}

func failContextHover(lines []string, lineIdx int, name string) (string, bool) {
	if !inOnFailBlock(lines, lineIdx) {
		return "", false
	}
	switch name {
	case "fail.message":
		return "`&fail.message` — the error message of the failure that triggered this `onfail` block", true
	case "fail.line":
		return "`&fail.line` — the source line of the failing statement (empty when unknown)", true
	case "fail.exit":
		return "`&fail.exit` — the exit code of the failed command (only set for non-zero exits)", true
	}
	return "", false
}

func lastResultHover(name string) (string, bool) {
	switch name {
	case "last.exit":
		return "`&last.exit` — the exit code of the most recently executed shell statement (0 on success)\n\nSet after every statement; most useful after an `!` error-tolerant statement.", true
	case "last.output":
		return "`&last.output` — the captured output of the most recently executed shell statement", true
	}
	return "", false
}

func linePrefixHover(line string, char int) (string, bool) {
	trimmed := strings.TrimLeft(line, " \t")
	lead := len(line) - len(trimmed)
	if char != lead || trimmed == "" {
		return "", false
	}
	switch trimmed[0] {
	case '$':
		return "`$` prefix — runs this line through the shell.\n\nA **bare** line (no `$`) starting with a builtin name (`cp`, `rm`, `mkdir`, `touch`, `download`, `extract`) runs as a cross-platform builtin instead; use `$ cp ...` to force the shell's version.", true
	case '!':
		return "`!` prefix — error-tolerant: a non-zero exit does not abort the command.\n\nThe outcome is available to later statements via `&last.exit` and `&last.output`.", true
	}
	return "", false
}

// shellLineContentAt reports whether char sits after the `$` shell marker.
func shellLineContentAt(line string, char int) bool {
	if idx := strings.IndexByte(line, '$'); idx >= 0 && char > idx {
		return true
	}
	return false
}

func keywordHover(word string) (string, bool) {
	switch word {
	case "switch":
		return "`switch <expr> { case v { } ... default { } }`\n\nRuns the first case whose value equals the expression; `default` runs when nothing matches.", true
	case "case":
		return "`case v1, v2 { ... }`\n\nRuns its body when the switch expression equals one of the comma-separated values.", true
	case "default":
		return "`default { ... }`\n\nRuns when no case value matched the switch expression.", true
	case "in":
		return "`in <dir> { ... }`\n\nRuns the block with the working directory set to `<dir>`, resolved against the Constfile directory.", true
	case "lock":
		return "`lock \"name\" { ... }`\n\nHolds an exclusive lock (in `.construct-cache/locks/`) while the block runs; other construct processes wait for it.", true
	case "state":
		return "`state name = value`\n\nPersists a variable across runs in `.construct-cache/state.json`. Read it back with `state(\"name\")` or `@state(\"name\")`.", true
	case "confirm":
		return "`confirm \"message\"`\n\nAsks for y/N confirmation and aborts the command when declined. `--yes` auto-approves.", true
	case "prompt":
		return "`prompt \"message\"`\n\nPrints the message and waits for Enter (skips the wait when stdin is not a TTY or `--yes` is set).", true
	case "input":
		return "`input name \"question\"`\n\nReads a line from stdin into the variable `name`.", true
	case "timeout":
		return "`timeout 30s $ cmd` or `cmd timeout 30s { ... }`\n\nCaps the statement (or whole command) at the duration; a hit is reported as exit 124.", true
	case "cp":
		return "builtin: `cp <src> <dst>`\n\nCopies a file or directory recursively, cross-platform.\n\nBare `cp` runs the builtin; `$ cp` runs the shell's cp.", true
	case "rm":
		return "builtin: `rm <path>`\n\nRemoves a file or directory recursively; refuses to remove the base directory or its ancestors.\n\nBare `rm` runs the builtin; `$ rm` runs the shell's rm.", true
	case "mkdir":
		return "builtin: `mkdir <path>`\n\nCreates a directory (and its parents).\n\nBare `mkdir` runs the builtin; `$ mkdir` runs the shell's mkdir.", true
	case "touch":
		return "builtin: `touch <path>`\n\nCreates the file if missing, otherwise updates its mtime.\n\nBare `touch` runs the builtin; `$ touch` runs the shell's touch.", true
	case "download":
		return "builtin: `download <url> <dst>`\n\nDownloads a URL to a file with a progress bar on TTYs.\n\nBare `download` runs the builtin; `$ download` runs the shell's download.", true
	case "extract":
		return "builtin: `extract <archive> <dir>`\n\nExtracts `.zip`, `.tar`, `.tar.gz`/`.tgz` or `.tar.bz2` archives; entries escaping the destination are refused.\n\nBare `extract` runs the builtin; `$ extract` runs the shell's extract.", true
	case "for":
		return "`for x in a, b, c { ... }`\n\nIterates over comma-separated items. `for i, x in ...` also binds the index; globs (`*.go`) and ranges (`1..5`) expand too.", true
	case "if":
		return "`if <cond> { ... } else if ... { ... } else { ... }`\n\nConditions support `==`, `!=`, `<`, `>`, `<=`, `>=`, `&&`, `||`, `!`, `contains`, `starts_with`, `ends_with`, `matches`, `in`, and the helpers `exists()`, `missing()`, `glob()`, `require()`.", true
	case "matrix":
		return "`matrix os in linux, mac; arch in amd64, arm64 { ... }`\n\nRuns the body once per combination of the `;`-separated clauses (nested loops).", true
	case "env":
		return "`env KEY=VALUE { ... }` or `env KEY=VALUE, ... { ... }`\n\nExports variables to the rest of the command's statements; reference them as `@KEY`.", true
	case "invoke":
		return "`invoke <cmd> k=v, ...`\n\nRuns another command's body inline in the current one. `invoke <cmd> as out` captures its combined output into `&out`.", true
	case "fail":
		return "`fail \"message\"`\n\nAborts the command with a failure (onfail blocks run).", true
	case "global":
		return "`global name = value`\n\nWrites a global variable from inside a command body; visible to every command.", true
	case "require_env":
		return "`require_env KEY \"message\"`\n\nFails the command when the environment variable KEY is not set.", true
	case "retry":
		return "`retry N $ cmd`\n\nReruns the statement up to N extra times when it fails.", true
	case "onfail":
		return "runs once when any later statement in this command fails\n\nfailure context available: `&fail.message`, `&fail.line`, `&fail.exit`", true
	case "continue":
		return "`continue` — skip to the next loop iteration; `continue if <cond>` is the conditional form.", true
	case "break":
		return "`break` — exit the loop; `break if <cond>` is the conditional form.", true
	case "var":
		return "`var name = value`\n\nDeclares a variable; reference it as `&name`. Values support expressions (`[a, b]`, `1 + 2`), `@ENV` refs, and `state(\"name\")`.", true
	case "import":
		return "`import \"lib.constfile\" as lib`\n\nMerges another file's commands and variables, optionally under a namespace (`lib.cmd`, `&lib.var`).", true
	case "produces":
		return "`produces <files>`\n\nDeclares the command's outputs. While the artifacts exist and are newer than the command's file dependencies, the command is skipped as up to date.", true
	case "manual":
		return "`manual <command>`\n\nMarks the command as an intentionally unreferenced entry point — meant to be invoked by name or via `--choose`, so lint and doctor stay quiet about it.", true
	case "container":
		return "`container \"image\"`\n\nRuns the command's shell statements inside the image via docker/podman, with the workspace mounted at `/work`. Builtins (`cp`, `rm`, …) still run on the host.", true
	case "onchange":
		return "`onchange <globs>`\n\nExtra file patterns that rerun this command in `--watch` mode.", true
	}
	return "", false
}

func functionHover(word string) (string, bool) {
	switch word {
	case "basename", "dirname", "ext", "stem":
		return fmt.Sprintf("`%s(path)` — path helper returning %s", word, map[string]string{
			"basename": "the final path element",
			"dirname":  "the parent directory",
			"ext":      "the file extension (with dot)",
			"stem":     "the basename without its extension",
		}[word]), true
	case "upper", "lower", "trim":
		return fmt.Sprintf("`%s(s)` — string helper: %s", word, map[string]string{
			"upper": "uppercases",
			"lower": "lowercases",
			"trim":  "strips surrounding whitespace",
		}[word]), true
	case "replace":
		return "`replace(s, old, new)` — replaces every occurrence of `old` with `new`", true
	case "sprintf":
		return "`sprintf(format, args...)` — printf-style formatting", true
	case "length", "len":
		return "`len(value)` — the number of list items, or the rune length of a string", true
	case "abs":
		return "`abs(n)` — the absolute value of a number", true
	case "min", "max":
		return fmt.Sprintf("`%s(a, b, ...)` — the %s of the arguments", word, word), true
	case "date":
		return "`date(\"2006-01-02\")` — the current time formatted (Go layout); defaults to YYYY-MM-DD", true
	case "uuid":
		return "`uuid()` — a random v4 UUID", true
	case "file":
		return "`file(path)` — the file's contents as a string", true
	case "lines":
		return "`lines(path)` — the file's non-empty lines as a list", true
	case "sha256":
		return "`sha256(path)` — the SHA-256 hex digest of a file", true
	case "glob":
		return "`glob(pattern)` — files matching a glob as a list", true
	case "sort":
		return "`sort(list)` — the list sorted lexicographically", true
	case "uniq":
		return "`uniq(list)` — the list with duplicates removed, order preserved", true
	case "join":
		return "`join(list, sep)` — the list joined into a string", true
	case "split":
		return "`split(s, sep)` — the string split into a list", true
	case "exists":
		return "`exists(path)` — \"true\" when the file exists (usable in conditions)", true
	case "missing":
		return "`missing(path)` — \"true\" when the file does not exist (usable in conditions)", true
	case "require":
		return "`require(tool)` — \"true\" when the tool is on PATH (usable in conditions)", true
	case "env":
		return "`env(name)` — the environment variable's value", true
	case "state":
		return "`state(\"name\")` or `@state(\"name\")` — a value persisted by a `state` declaration", true
	}
	return "", false
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
