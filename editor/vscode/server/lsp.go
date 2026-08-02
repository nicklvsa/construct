package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
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
	s.publishDiagnostics(uri, nil)
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
		if v, err := doc.data.GetVariable(name, ""); err == nil {
			return hoverResult{
				Contents: markupContent{
					Kind:  "markdown",
					Value: fmt.Sprintf("`%s` (scope: `%s`)\n\nvalue: `%s`", name, v.Scope, v.Value),
				},
			}, nil
		}
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
	if len(c.Body) > 0 {
		fmt.Fprintf(&b, "- %d body line(s)\n", len(c.Body))
	}
	return b.String()
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

	// Go-to-definition for a command's own name (clicking the header name jumps
	// nowhere useful, but clicking a reference in another command's prereq list
	// is handled above).
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

// refAtPosition finds a &varName reference overlapping the given character.
// Returns the literal (e.g. "&foo") and the bare name ("foo").
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
		if !isIdentRune(r) && r != '.' && !(r >= '0' && r <= '9') {
			break
		}
		name.WriteRune(r)
	}
	if name.Len() == 0 {
		return "", ""
	}
	// Strip a trailing ".index" if present.
	clean := name.String()
	if dot := strings.IndexByte(clean, '.'); dot >= 0 {
		clean = clean[:dot]
	}
	if clean == "" {
		return "", ""
	}
	return string(runes[idx : idx+1+len(name.String())]), clean
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

	// Regular command name followed by ( < or {
	for i, r := range trimmed {
		if r == '(' || r == '<' || r == '{' {
			return strings.TrimSpace(trimmed[:i]), true
		}
	}
	return "", false
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

// reference to silence unused warning if log is not used elsewhere
var _ = log.Printf
