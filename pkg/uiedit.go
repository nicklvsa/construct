package pkg

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// UIErrConflict is returned by Save when a file changed on disk since load.
var UIErrConflict = errors.New("file changed on disk")

const uiUndoLimit = 100

type UIFile struct {
	Path     string
	Text     string
	OrigText string
	CRLF     bool
}

type UIDoc struct {
	Main  string
	Files map[string]*UIFile

	undo []map[string]string
	redo []map[string]string
}

type UIHeaderPatch struct {
	Name            *string           `json:"name,omitempty"`
	IsDefault       *bool             `json:"is_default,omitempty"`
	CloudAccessible *bool             `json:"cloud_accessible,omitempty"`
	Manual          *bool             `json:"manual,omitempty"`
	Arguments       []*Argument       `json:"arguments,omitempty"`
	Prereqs         *[]string         `json:"prereqs,omitempty"`
	FileDeps        *[]string         `json:"file_deps,omitempty"`
	PrereqDirs      map[string]string `json:"prereq_dirs,omitempty"`
	Produces        *[]string         `json:"produces,omitempty"`
	OnChange        *[]string         `json:"onchange,omitempty"`
	Container       *string           `json:"container,omitempty"`
	Timeout         *string           `json:"timeout,omitempty"`
	WorkDir         *string           `json:"work_dir,omitempty"`
}

type UIEditOp struct {
	File    string         `json:"file"`
	Kind    string         `json:"kind"`
	Name    string         `json:"name,omitempty"`
	Header  *UIHeaderPatch `json:"header,omitempty"`
	Body    *string        `json:"body,omitempty"`
	Before  *string        `json:"before,omitempty"`
	Line    int            `json:"line,omitempty"`
	At      int            `json:"at,omitempty"`
	EndLine int            `json:"end_line,omitempty"`
	Text    *string        `json:"text,omitempty"`
}

type UIHeaderSummary struct {
	Arguments  []*Argument       `json:"arguments,omitempty"`
	Prereqs    []string          `json:"prereqs,omitempty"`
	FileDeps   []string          `json:"file_deps,omitempty"`
	PrereqDirs map[string]string `json:"prereq_dirs,omitempty"`
	Produces   []string          `json:"produces,omitempty"`
	OnChange   []string          `json:"onchange,omitempty"`
	Container  string            `json:"container,omitempty"`
	Timeout    string            `json:"timeout,omitempty"`
	WorkDir    string            `json:"work_dir,omitempty"`
	IsDefault  bool              `json:"is_default,omitempty"`
	Cloud      bool              `json:"cloud,omitempty"`
	Manual     bool              `json:"manual,omitempty"`
}

type UICommandState struct {
	Name        string           `json:"name"`
	Display     string           `json:"display"`
	Aliases     []string         `json:"aliases,omitempty"`
	Line        int              `json:"line"`
	DocStart    int              `json:"doc_start"`
	EndLine     int              `json:"end_line"`
	Body        string           `json:"body"`
	Description string           `json:"description,omitempty"`
	Header      *UIHeaderSummary `json:"header"`
	Stmts       []UIStmt         `json:"stmts,omitempty"`
}

type UIStmt struct {
	Type     string   `json:"type"`
	Summary  string   `json:"summary"`
	Line     int      `json:"line"`
	End      int      `json:"end"`
	Close    int      `json:"close,omitempty"` // closing-brace line for block statements
	Children []UIStmt `json:"children,omitempty"`
}

type UIVisibleState struct {
	Name string `json:"name"`
	File string `json:"file"`
	Rel  string `json:"rel"`
	Line int    `json:"line"`
}

type UIFileState struct {
	Path       string           `json:"path"`
	Rel        string           `json:"rel"`
	Main       bool             `json:"main,omitempty"`
	Dirty      bool             `json:"dirty,omitempty"`
	Text       string           `json:"text"`
	Commands   []UICommandState `json:"commands,omitempty"`
	Visible    []UIVisibleState `json:"visible,omitempty"`
	Lint       []LintIssue      `json:"lint,omitempty"`
	ParseError string           `json:"parse_error,omitempty"`
}

type UIState struct {
	Main    string         `json:"main"`
	Files   []*UIFileState `json:"files"`
	Dirty   bool           `json:"dirty"`
	CanUndo bool           `json:"can_undo"`
	CanRedo bool           `json:"can_redo"`
}

func uiNormalize(s string) string {
	return strings.ReplaceAll(s, "\r\n", "\n")
}

func NewUIDoc(path string) (*UIDoc, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("failed to read file '%s': %w", abs, err)
	}
	d := &UIDoc{Main: abs, Files: map[string]*UIFile{}}
	d.addDiskFile(abs, string(b))
	d.scanImports(abs)
	return d, nil
}

func (d *UIDoc) addDiskFile(path, raw string) {
	text := uiNormalize(raw)
	d.Files[path] = &UIFile{Path: path, Text: text, OrigText: text, CRLF: strings.Contains(raw, "\r\n")}
}

func (d *UIDoc) scanImports(path string) {
	f := d.Files[path]
	if f == nil {
		return
	}
	base := filepath.Dir(path)
	for _, line := range strings.Split(f.Text, "\n") {
		t := strings.TrimSpace(line)
		if !strings.HasPrefix(t, "import ") {
			continue
		}
		spec, _, err := parseImportSpec(t)
		if err != nil {
			continue
		}
		p := spec
		if !filepath.IsAbs(p) {
			p = filepath.Join(base, spec)
		}
		p = filepath.Clean(p)
		if _, ok := d.Files[p]; ok {
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		d.addDiskFile(p, string(b))
		d.scanImports(p)
	}
}

func (d *UIDoc) importReader(path string) ([]byte, error) {
	clean := filepath.Clean(path)
	if f, ok := d.Files[clean]; ok {
		return []byte(f.Text), nil
	}
	b, err := os.ReadFile(clean)
	if err != nil {
		return nil, err
	}
	d.addDiskFile(clean, string(b))
	return b, nil
}

func (d *UIDoc) parseFile(path string) (*ParsedData, error) {
	f := d.Files[path]
	if f == nil {
		return nil, fmt.Errorf("unknown file %q", path)
	}
	p := NewParserFromContent(path, f.Text)
	p.ImportReader = d.importReader
	return p.Parse()
}

func (d *UIDoc) parseFileSpans(path string) (*ParsedData, error) {
	f := d.Files[path]
	if f == nil {
		return nil, fmt.Errorf("unknown file %q", path)
	}
	p := NewParserFromContent(path, f.Text)
	p.ImportReader = d.importReader
	if err := p.parseLines(); err != nil {
		return nil, err
	}
	p.Data.buildIndexMaps()
	uiClassifyPrereqsLenient(p.Data)
	return p.Data, nil
}

func uiClassifyPrereqsLenient(data *ParsedData) {
	for _, cmd := range data.Commands {
		var cmdDeps, fileDeps []string
		for _, prereq := range cmd.Prereqs {
			prereq = strings.TrimSpace(prereq)
			if prereq == "" {
				continue
			}
			if _, err := data.GetCommand(prereq); err == nil {
				cmdDeps = append(cmdDeps, prereq)
			} else if IsFileDep(prereq) {
				fileDeps = append(fileDeps, prereq)
			} else {
				cmdDeps = append(cmdDeps, prereq)
			}
		}
		cmd.Prereqs = cmdDeps
		cmd.FileDeps = fileDeps
	}
}

type uiBlock struct {
	cmd      *Command
	name     string
	docStart int
	header   int
	end      int
}

func uiBlockBounds(lines []string, header int) (int, bool) {
	if _, single := singleLineBody(strings.TrimSpace(lines[header-1])); single {
		return header, true
	}
	depth := 0
	for i := header + 1; i <= len(lines); i++ {
		t := strings.TrimSpace(lines[i-1])
		if t == "" {
			continue
		}
		if strings.HasPrefix(t, "}") && strings.Contains(t, "else") {
			if depth > 0 {
				depth--
			}
			depth++
			continue
		}
		if strings.HasPrefix(t, "}") {
			if depth == 0 {
				return i, true
			}
			depth--
			continue
		}
		if isNestedBlockHeader(t) {
			depth += strings.Count(t, "{") - strings.Count(t, "}")
		}
	}
	return 0, false
}

func uiFileBlocks(data *ParsedData, path, text string) ([]uiBlock, error) {
	lines := strings.Split(text, "\n")
	var blocks []uiBlock
	for _, c := range data.Commands {
		if IsLazyName(c.Name) || c.SourceFile != path {
			continue
		}
		if c.SourceLine <= 0 || c.SourceLine > len(lines) {
			continue
		}
		end, ok := uiBlockBounds(lines, c.SourceLine)
		if !ok {
			return nil, fmt.Errorf("cannot locate the end of command %q in %s", c.Name, path)
		}
		docStart := c.SourceLine
		for docStart > 1 {
			t := strings.TrimSpace(lines[docStart-2])
			if strings.HasPrefix(t, "#") || strings.HasPrefix(t, "//") {
				docStart--
				continue
			}
			break
		}
		blocks = append(blocks, uiBlock{cmd: c, name: c.Name, docStart: docStart, header: c.SourceLine, end: end})
	}
	sort.Slice(blocks, func(i, j int) bool { return blocks[i].header < blocks[j].header })
	return blocks, nil
}

func (d *UIDoc) blocksFor(path string) (*UIFile, []string, []uiBlock, error) {
	f, ok := d.Files[path]
	if !ok {
		return nil, nil, nil, fmt.Errorf("unknown file %q", path)
	}
	data, err := d.parseFileSpans(path)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%s has parse errors: %v", filepath.Base(path), err)
	}
	lines := strings.Split(f.Text, "\n")
	blocks, err := uiFileBlocks(data, path, f.Text)
	if err != nil {
		return nil, nil, nil, err
	}
	return f, lines, blocks, nil
}

func uiFindBlock(blocks []uiBlock, name string) (uiBlock, error) {
	for _, b := range blocks {
		if b.name == name {
			return b, nil
		}
	}
	return uiBlock{}, fmt.Errorf("no command %q in this file", name)
}

func uiBlockBody(lines []string, b uiBlock) string {
	if b.end == b.header {
		line := lines[b.header-1]
		open := strings.IndexByte(line, '{')
		close := strings.LastIndexByte(line, '}')
		if open >= 0 && close > open {
			return strings.TrimSpace(line[open+1 : close])
		}
		return ""
	}
	return strings.Join(lines[b.header:b.end-1], "\n")
}

func uiBodyLines(body string) []string {
	var out []string
	for _, l := range strings.Split(body, "\n") {
		out = append(out, strings.TrimRight(l, " \t\r"))
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	if out == nil {
		out = []string{}
	}
	return out
}

func uiApplyHeaderPatch(c *Command, h *UIHeaderPatch) {
	if h == nil {
		return
	}
	if h.Name != nil {
		c.Name = *h.Name
	}
	if h.IsDefault != nil {
		c.IsDefault = *h.IsDefault
	}
	if h.CloudAccessible != nil {
		c.CloudAccessible = *h.CloudAccessible
	}
	if h.Manual != nil {
		c.Manual = *h.Manual
	}
	if h.Arguments != nil {
		c.Arguments = h.Arguments
	}
	if h.Prereqs != nil {
		c.Prereqs = *h.Prereqs
	}
	if h.FileDeps != nil {
		c.FileDeps = *h.FileDeps
	}
	if h.PrereqDirs != nil {
		c.PrereqDirs = h.PrereqDirs
	}
	if h.Produces != nil {
		c.Produces = *h.Produces
	}
	if h.OnChange != nil {
		c.OnChange = *h.OnChange
	}
	if h.Container != nil {
		c.Container = *h.Container
	}
	if h.Timeout != nil {
		c.Timeout = *h.Timeout
	}
	if h.WorkDir != nil {
		c.WorkDir = *h.WorkDir
	}
}

func uiLineOutsideBlocks(blocks []uiBlock, n int) bool {
	for _, b := range blocks {
		if n >= b.docStart && n <= b.end {
			return false
		}
	}
	return true
}

func uiSpliceBlockOut(lines []string, b uiBlock) []string {
	out := append([]string{}, lines[:b.docStart-1]...)
	out = append(out, lines[b.end:]...)
	seam := b.docStart - 1
	if seam > 0 && seam < len(out) &&
		strings.TrimSpace(out[seam-1]) == "" && strings.TrimSpace(out[seam]) == "" {
		out = append(out[:seam], out[seam+1:]...)
	}
	return out
}

func uiInsertBlock(lines []string, at int, block []string) []string {
	if at > len(lines) {
		out := lines
		if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
			out = append(out, "")
		}
		return append(out, block...)
	}
	out := append([]string{}, lines[:at-1]...)
	if at > 1 && strings.TrimSpace(lines[at-2]) != "" {
		out = append(out, "")
	}
	out = append(out, block...)
	if strings.TrimSpace(lines[at-1]) != "" {
		out = append(out, "")
	}
	return append(out, lines[at-1:]...)
}

func (d *UIDoc) texts() map[string]string {
	out := make(map[string]string, len(d.Files))
	for p, f := range d.Files {
		out[p] = f.Text
	}
	return out
}

func (d *UIDoc) setTexts(snap map[string]string) {
	for p, t := range snap {
		if f, ok := d.Files[p]; ok {
			f.Text = t
		}
	}
}

func (d *UIDoc) parseStatus() (map[string]bool, map[string]string) {
	ok := make(map[string]bool, len(d.Files))
	errs := make(map[string]string, len(d.Files))
	for _, p := range d.fileList() {
		_, err := d.parseFile(p)
		ok[p] = err == nil
		if err != nil {
			errs[p] = err.Error()
		}
	}
	return ok, errs
}

func (d *UIDoc) fileList() []string {
	list := make([]string, 0, len(d.Files))
	for p := range d.Files {
		list = append(list, p)
	}
	sort.Strings(list)
	return list
}

func (d *UIDoc) pushUndo(snap map[string]string) {
	d.undo = append(d.undo, snap)
	if len(d.undo) > uiUndoLimit {
		d.undo = d.undo[len(d.undo)-uiUndoLimit:]
	}
	d.redo = nil
}

func (d *UIDoc) Apply(ops []UIEditOp) error {
	before, _ := d.parseStatus()
	snap := d.texts()
	for i := range ops {
		if err := d.applyOp(&ops[i]); err != nil {
			d.setTexts(snap)
			return err
		}
	}
	after, afterErrs := d.parseStatus()
	for p, wasOK := range before {
		if wasOK && !after[p] {
			d.setTexts(snap)
			return fmt.Errorf("change rejected: %s", afterErrs[p])
		}
	}
	d.pushUndo(snap)
	return nil
}

func (d *UIDoc) Undo() bool {
	if len(d.undo) == 0 {
		return false
	}
	d.redo = append(d.redo, d.texts())
	snap := d.undo[len(d.undo)-1]
	d.undo = d.undo[:len(d.undo)-1]
	d.setTexts(snap)
	return true
}

func (d *UIDoc) Redo() bool {
	if len(d.redo) == 0 {
		return false
	}
	d.undo = append(d.undo, d.texts())
	snap := d.redo[len(d.redo)-1]
	d.redo = d.redo[:len(d.redo)-1]
	d.setTexts(snap)
	return true
}

func (d *UIDoc) applyOp(op *UIEditOp) error {
	switch op.Kind {
	case "setHeader":
		return d.opSetHeader(op)
	case "setBody":
		return d.opSetBody(op)
	case "moveCommand":
		return d.opMoveCommand(op)
	case "deleteCommand":
		return d.opDeleteCommand(op)
	case "insertCommand":
		return d.opInsertCommand(op)
	case "duplicateCommand":
		return d.opDuplicateCommand(op)
	case "insertLines", "setLine", "deleteLines":
		return d.opLines(op)
	case "moveStmt":
		return d.opMoveStmt(op)
	case "deleteStmt":
		return d.opDeleteStmt(op)
	case "setFile":
		if op.Text == nil {
			return fmt.Errorf("setFile requires text")
		}
		f, ok := d.Files[op.File]
		if !ok {
			return fmt.Errorf("unknown file %q", op.File)
		}
		f.Text = *op.Text
		return nil
	default:
		return fmt.Errorf("unknown op %q", op.Kind)
	}
}

func (d *UIDoc) opSetHeader(op *UIEditOp) error {
	f, lines, blocks, err := d.blocksFor(op.File)
	if err != nil {
		return err
	}
	b, err := uiFindBlock(blocks, op.Name)
	if err != nil {
		return err
	}
	merged := *b.cmd
	uiApplyHeaderPatch(&merged, op.Header)
	header := EmitHeader(&merged)
	if b.end == b.header {
		if body := uiBlockBody(lines, b); body != "" {
			header = header + " " + body + " }"
		} else {
			header = header + " }"
		}
	}
	lines[b.header-1] = header
	f.Text = strings.Join(lines, "\n")
	return nil
}

func (d *UIDoc) opSetBody(op *UIEditOp) error {
	if op.Body == nil {
		return fmt.Errorf("setBody requires body")
	}
	f, lines, blocks, err := d.blocksFor(op.File)
	if err != nil {
		return err
	}
	b, err := uiFindBlock(blocks, op.Name)
	if err != nil {
		return err
	}
	body := uiBodyLines(*op.Body)
	if b.end == b.header {
		merged := *b.cmd
		out := append([]string{}, lines[:b.header-1]...)
		out = append(out, EmitHeader(&merged))
		out = append(out, body...)
		out = append(out, "}")
		out = append(out, lines[b.header:]...)
		lines = out
	} else {
		out := append([]string{}, lines[:b.header]...)
		out = append(out, body...)
		out = append(out, lines[b.end-1:]...)
		lines = out
	}
	f.Text = strings.Join(lines, "\n")
	return nil
}

func (d *UIDoc) opMoveCommand(op *UIEditOp) error {
	f, lines, blocks, err := d.blocksFor(op.File)
	if err != nil {
		return err
	}
	b, err := uiFindBlock(blocks, op.Name)
	if err != nil {
		return err
	}
	insertAt := len(lines) + 1
	if op.Before != nil {
		target, err := uiFindBlock(blocks, *op.Before)
		if err != nil {
			return err
		}
		if target.name == b.name {
			return fmt.Errorf("cannot move %q before itself", b.name)
		}
		insertAt = target.docStart
		if insertAt > b.docStart {
			insertAt -= b.end - b.docStart + 1
		}
	}
	blockLines := append([]string{}, lines[b.docStart-1:b.end]...)
	lines = uiSpliceBlockOut(lines, b)
	lines = uiInsertBlock(lines, insertAt, blockLines)
	f.Text = strings.Join(lines, "\n")
	return nil
}

func (d *UIDoc) opDeleteCommand(op *UIEditOp) error {
	f, lines, blocks, err := d.blocksFor(op.File)
	if err != nil {
		return err
	}
	b, err := uiFindBlock(blocks, op.Name)
	if err != nil {
		return err
	}
	lines = uiSpliceBlockOut(lines, b)
	f.Text = strings.Join(lines, "\n")
	return nil
}

func (d *UIDoc) opInsertCommand(op *UIEditOp) error {
	if op.Header == nil || op.Header.Name == nil || *op.Header.Name == "" {
		return fmt.Errorf("insertCommand requires a header with a name")
	}
	f, lines, blocks, err := d.blocksFor(op.File)
	if err != nil {
		return err
	}
	c := &Command{Name: *op.Header.Name}
	uiApplyHeaderPatch(c, op.Header)
	block := []string{EmitHeader(c)}
	block = append(block, uiBodyLines(deref(op.Body))...)
	block = append(block, "}")

	insertAt := len(lines) + 1
	if op.Before != nil {
		target, err := uiFindBlock(blocks, *op.Before)
		if err != nil {
			return err
		}
		insertAt = target.docStart
	}
	lines = uiInsertBlock(lines, insertAt, block)
	f.Text = strings.Join(lines, "\n")
	return nil
}

func (d *UIDoc) opDuplicateCommand(op *UIEditOp) error {
	f, lines, blocks, err := d.blocksFor(op.File)
	if err != nil {
		return err
	}
	b, err := uiFindBlock(blocks, op.Name)
	if err != nil {
		return err
	}
	data, err := d.parseFile(op.File)
	if err != nil {
		return err
	}
	taken := map[string]bool{}
	for _, c := range data.Commands {
		taken[c.Name] = true
	}
	base := b.name + "-copy"
	newName := base
	for n := 2; taken[newName]; n++ {
		newName = base + strconv.Itoa(n)
	}
	blockLines := append([]string{}, lines[b.docStart-1:b.end]...)
	merged := *b.cmd
	merged.Name = newName
	blockLines[b.header-b.docStart] = EmitHeader(&merged)
	lines = uiInsertBlock(lines, b.end+1, blockLines)
	f.Text = strings.Join(lines, "\n")
	return nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func uiStmtTree(stmts []BodyStatement, bodyEnd int, lines []string) []UIStmt {
	var out []UIStmt
	for _, s := range stmts {
		if s.SourceLine <= 0 {
			continue
		}
		st := UIStmt{Type: s.Type, Summary: uiStmtSummary(&s), Line: s.SourceLine, End: s.SourceLine}
		switch s.Type {
		case StmtIf:
			st.Children = append(uiStmtTree(s.ThenBody, 0, lines), uiStmtTree(s.ElseBody, 0, lines)...)
		case StmtFor:
			st.Children = uiStmtTree(s.LoopBody, 0, lines)
		case StmtSwitch:
			for _, c := range s.Cases {
				st.Children = append(st.Children, uiStmtTree(c.Body, 0, lines)...)
			}
		case StmtOnFail:
			st.Children = uiStmtTree(s.OnFailBody, 0, lines)
		case StmtInDir, StmtLock:
			st.Children = uiStmtTree(s.ThenBody, 0, lines)
		}
		for _, k := range st.Children {
			if k.End > st.End {
				st.End = k.End
			}
		}
		if len(st.Children) > 0 && st.Line-1 < len(lines) {
			if close, ok := uiStmtBlockBounds(lines, st.Line, s.Type == StmtIf); ok && close > st.End {
				st.End = close
				st.Close = close
			}
		}
		out = append(out, st)
	}
	for i := 0; i+1 < len(out); i++ {
		if limit := out[i+1].Line - 1; out[i].End < limit {
			out[i].End = limit
		}
	}
	if n := len(out); n > 0 && bodyEnd > 0 && out[n-1].End < bodyEnd {
		out[n-1].End = bodyEnd
	}
	return out
}

func uiStmtBlockBounds(lines []string, header int, isIf bool) (int, bool) {
	if _, single := singleLineBody(strings.TrimSpace(lines[header-1])); single {
		return header, true
	}
	depth := 0
	for i := header + 1; i <= len(lines); i++ {
		t := strings.TrimSpace(lines[i-1])
		if t == "" {
			continue
		}
		if strings.HasPrefix(t, "}") {
			if depth == 0 {
				if isIf && strings.Contains(t, "else") {
					continue
				}
				return i, true
			}
			depth--
			continue
		}
		if isNestedBlockHeader(t) {
			depth += strings.Count(t, "{") - strings.Count(t, "}")
		}
	}
	return 0, false
}

func uiStmtSummary(s *BodyStatement) string {
	prefix := ""
	if s.Timeout != "" {
		prefix += fmt.Sprintf("timeout<%s> ", s.Timeout)
	}
	if s.Retry > 0 {
		prefix += fmt.Sprintf("retry<%d> ", s.Retry)
	}
	switch s.Type {
	case StmtShell:
		return prefix + strings.TrimSpace(s.Shell)
	case StmtIf:
		return "if " + s.Cond
	case StmtFor:
		p := ""
		if s.Parallel {
			p = "parallel "
		}
		return p + "for " + s.LoopVar + " in " + s.LoopItems
	case StmtInvoke:
		return prefix + "invoke " + s.Shell
	case StmtEnv:
		return "env { " + strings.Join(s.Env, ", ") + " }"
	case StmtSwitch:
		return "switch " + s.SwitchExpr
	case StmtInDir:
		return "in " + s.Shell
	case StmtLock:
		return "lock " + s.Shell
	case StmtOnFail:
		return "onfail { ... }"
	case StmtFail:
		return "fail " + s.Message
	case StmtContinue:
		return "continue"
	case StmtBreak:
		return "break"
	case StmtConfirm:
		return "confirm " + s.Message
	case StmtPrompt:
		return "prompt " + s.Message
	case StmtInput:
		return "input " + s.Message
	case StmtState:
		return "state " + s.Message
	case StmtBuiltin:
		return prefix + s.Shell + " " + s.BuiltinArgs
	default:
		return s.Type
	}
}

func uiFindStmtByLine(stmts []UIStmt, line int) *UIStmt {
	for i := range stmts {
		if line >= stmts[i].Line && line <= stmts[i].End {
			if hit := uiFindStmtByLine(stmts[i].Children, line); hit != nil {
				return hit
			}
			return &stmts[i]
		}
	}
	return nil
}

func (d *UIDoc) stmtsFor(path, name string) (*UIFile, []string, uiBlock, []UIStmt, error) {
	f, lines, blocks, err := d.blocksFor(path)
	if err != nil {
		return nil, nil, uiBlock{}, nil, err
	}
	b, err := uiFindBlock(blocks, name)
	if err != nil {
		return nil, nil, uiBlock{}, nil, err
	}
	if b.end == b.header {
		return nil, nil, uiBlock{}, nil, fmt.Errorf("command %q has a single-line body", name)
	}
	return f, lines, b, uiStmtTree(b.cmd.Body, b.end-1, lines), nil
}

func (d *UIDoc) opMoveStmt(op *UIEditOp) error {
	f, lines, b, stmts, err := d.stmtsFor(op.File, op.Name)
	if err != nil {
		return err
	}
	st := uiFindStmtByLine(stmts, op.Line)
	if st == nil {
		return fmt.Errorf("no statement at line %d", op.Line)
	}
	at := op.At
	if at <= 0 {
		at = b.end // append as the last statement, before the closing brace
	}
	if at > st.Line && at <= st.End {
		return fmt.Errorf("cannot move a statement into itself")
	}
	if at < b.header+1 || at > b.end {
		return fmt.Errorf("insertion line %d outside the command body", at)
	}
	block := append([]string{}, lines[st.Line-1:st.End]...)
	out := append([]string{}, lines[:st.Line-1]...)
	out = append(out, lines[st.End:]...)
	if at > st.End {
		at -= st.End - st.Line + 1
	}
	out = append(out[:at-1], append(block, out[at-1:]...)...)
	f.Text = strings.Join(out, "\n")
	return nil
}

func (d *UIDoc) opDeleteStmt(op *UIEditOp) error {
	f, lines, _, stmts, err := d.stmtsFor(op.File, op.Name)
	if err != nil {
		return err
	}
	st := uiFindStmtByLine(stmts, op.Line)
	if st == nil {
		return fmt.Errorf("no statement at line %d", op.Line)
	}
	out := append([]string{}, lines[:st.Line-1]...)
	out = append(out, lines[st.End:]...)
	f.Text = strings.Join(out, "\n")
	return nil
}

func (d *UIDoc) opLines(op *UIEditOp) error {
	f, lines, blocks, err := d.blocksFor(op.File)
	if err != nil {
		return err
	}
	switch op.Kind {
	case "insertLines":
		if op.Text == nil {
			return fmt.Errorf("insertLines requires text")
		}
		if op.Line < 0 || op.Line > len(lines) {
			return fmt.Errorf("line %d out of range", op.Line)
		}
		if !uiLineOutsideBlocks(blocks, op.Line) {
			return fmt.Errorf("line %d is inside a command; edit the command instead", op.Line)
		}
		out := append([]string{}, lines[:op.Line]...)
		out = append(out, strings.Split(*op.Text, "\n")...)
		out = append(out, lines[op.Line:]...)
		lines = out
	case "setLine":
		if op.Text == nil {
			return fmt.Errorf("setLine requires text")
		}
		if op.Line < 1 || op.Line > len(lines) {
			return fmt.Errorf("line %d out of range", op.Line)
		}
		if !uiLineOutsideBlocks(blocks, op.Line) {
			return fmt.Errorf("line %d is inside a command; edit the command instead", op.Line)
		}
		lines[op.Line-1] = *op.Text
	case "deleteLines":
		if op.Line < 1 || op.EndLine < op.Line || op.EndLine > len(lines) {
			return fmt.Errorf("lines %d-%d out of range", op.Line, op.EndLine)
		}
		for n := op.Line; n <= op.EndLine; n++ {
			if !uiLineOutsideBlocks(blocks, n) {
				return fmt.Errorf("line %d is inside a command; edit the command instead", n)
			}
		}
		lines = append(append([]string{}, lines[:op.Line-1]...), lines[op.EndLine:]...)
	}
	f.Text = strings.Join(lines, "\n")
	return nil
}

func (d *UIDoc) DirtyFiles() []string {
	var out []string
	for _, p := range d.fileList() {
		if f := d.Files[p]; f.Text != f.OrigText {
			out = append(out, p)
		}
	}
	return out
}

func (d *UIDoc) Save() (saved, conflicts []string, err error) {
	for _, p := range d.fileList() {
		f := d.Files[p]
		if f.Text == f.OrigText {
			continue
		}
		disk, rerr := os.ReadFile(p)
		if rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
			// An unreadable file must not silently pass the conflict check
			// and get overwritten below.
			return saved, nil, fmt.Errorf("conflict check %s: %w", p, rerr)
		}
		if rerr == nil && uiNormalize(string(disk)) != f.OrigText {
			conflicts = append(conflicts, p)
		}
	}
	if len(conflicts) > 0 {
		return nil, conflicts, UIErrConflict
	}
	for _, p := range d.fileList() {
		f := d.Files[p]
		if f.Text == f.OrigText {
			continue
		}
		formatted := FormatConstfile(f.Text)
		out := formatted
		if f.CRLF {
			out = strings.ReplaceAll(formatted, "\n", "\r\n")
		}
		mode := os.FileMode(0644)
		if info, serr := os.Stat(p); serr == nil {
			mode = info.Mode().Perm()
		}
		tmp := p + ".construct-ui-tmp"
		if werr := os.WriteFile(tmp, []byte(out), mode); werr != nil {
			return saved, nil, werr
		}
		if rerr := os.Rename(tmp, p); rerr != nil {
			os.Remove(tmp)
			return saved, nil, rerr
		}
		f.Text = formatted
		f.OrigText = formatted
		saved = append(saved, p)
	}
	return saved, nil, nil
}

func (d *UIDoc) rel(path string) string {
	rel, err := filepath.Rel(filepath.Dir(d.Main), path)
	if err != nil {
		return path
	}
	return rel
}

func uiHeaderSummary(c *Command) *UIHeaderSummary {
	return &UIHeaderSummary{
		Arguments:  c.Arguments,
		Prereqs:    c.Prereqs,
		FileDeps:   c.FileDeps,
		PrereqDirs: c.PrereqDirs,
		Produces:   c.Produces,
		OnChange:   c.OnChange,
		Container:  c.Container,
		Timeout:    c.Timeout,
		WorkDir:    c.WorkDir,
		IsDefault:  c.IsDefault,
		Cloud:      c.CloudAccessible,
		Manual:     c.Manual,
	}
}

func (d *UIDoc) State() *UIState {
	merged, _ := d.parseFileSpans(d.Main)
	mergedNames := map[string][]string{}
	if merged != nil {
		for _, c := range merged.Commands {
			if IsLazyName(c.Name) {
				continue
			}
			key := c.SourceFile + ":" + strconv.Itoa(c.SourceLine)
			mergedNames[key] = append(mergedNames[key], c.Name)
		}
	}

	st := &UIState{Main: d.Main, Files: []*UIFileState{}}
	st.CanUndo = len(d.undo) > 0
	st.CanRedo = len(d.redo) > 0
	st.Dirty = len(d.DirtyFiles()) > 0

	all := append([]string{d.Main}, filterPath(d.fileList(), d.Main)...)
	seen := map[string]bool{}
	for _, p := range all {
		if seen[p] {
			continue
		}
		seen[p] = true
		f := d.Files[p]
		fs := &UIFileState{Path: p, Rel: d.rel(p), Main: p == d.Main, Dirty: f.Text != f.OrigText, Text: f.Text}
		if full, err := d.parseFile(p); err != nil {
			fs.ParseError = err.Error()
		} else {
			fs.Lint = Lint(strings.Split(f.Text, "\n"), full, filepath.Dir(p))
		}
		data, err := d.parseFileSpans(p)
		if err != nil {
			st.Files = append(st.Files, fs)
			continue
		}
		lines := strings.Split(f.Text, "\n")
		blocks, berr := uiFileBlocks(data, p, f.Text)
		if berr == nil {
			for _, b := range blocks {
				key := p + ":" + strconv.Itoa(b.header)
				cs := UICommandState{
					Name:        b.name,
					Line:        b.header,
					DocStart:    b.docStart,
					EndLine:     b.end,
					Body:        uiBlockBody(lines, b),
					Description: b.cmd.Description,
					Header:      uiHeaderSummary(b.cmd),
				}
				if b.end > b.header {
					cs.Stmts = uiStmtTree(b.cmd.Body, b.end-1, lines)
				}
				if names := mergedNames[key]; len(names) > 0 && p != d.Main {
					cs.Display = names[0]
					cs.Aliases = names[1:]
				} else {
					cs.Display = b.name
				}
				fs.Commands = append(fs.Commands, cs)
			}
		}
		for _, c := range data.Commands {
			if IsLazyName(c.Name) || c.SourceFile == p {
				continue
			}
			fs.Visible = append(fs.Visible, UIVisibleState{
				Name: c.Name,
				File: c.SourceFile,
				Rel:  d.rel(c.SourceFile),
				Line: c.SourceLine,
			})
		}
		st.Files = append(st.Files, fs)
	}
	return st
}

func filterPath(list []string, skip string) []string {
	out := make([]string, 0, len(list))
	for _, p := range list {
		if p != skip {
			out = append(out, p)
		}
	}
	return out
}
