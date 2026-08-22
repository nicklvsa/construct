package pkg

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

type ParsedData struct {
	Variables  []*Variable `json:"variables"`
	Commands   []*Command  `json:"commands"`
	StateDecls []*Variable `json:"state,omitempty"`

	SourceFiles []string `json:"source_files,omitempty"`

	variableMap       map[string]*Variable // key: "scope.name"
	commandMap        map[string]*Command  // key: command name
	indexedOutputRefs map[string]bool      // commands referenced as &name.N / &name.*

	mu sync.RWMutex
}

func (p *ParsedData) OutputsIndexReferenced(name string) bool {
	p.mu.RLock()
	refs := p.indexedOutputRefs
	p.mu.RUnlock()
	if refs == nil {
		refs = p.computeIndexedOutputRefs()
		p.mu.Lock()
		p.indexedOutputRefs = refs
		p.mu.Unlock()
	}
	return refs[name]
}

func (p *ParsedData) computeIndexedOutputRefs() map[string]bool {
	all := map[string]bool{}
	wildcards := map[string]bool{}
	for _, cmd := range p.Commands {
		collectStmtRefs(cmd.Body, all)
		collectStmtWildcardRefs(cmd.Body, wildcards)
		for _, str := range cmd.Produces {
			for _, n := range VarRefNames(str) {
				all[n] = true
			}
			for _, n := range wildcardRefNames(str) {
				wildcards[n] = true
			}
		}
		for _, str := range cmd.OnChange {
			for _, n := range VarRefNames(str) {
				all[n] = true
			}
			for _, n := range wildcardRefNames(str) {
				wildcards[n] = true
			}
		}
	}

	refs := make(map[string]bool)
	for n := range wildcards {
		refs[n] = true
	}
	for n := range all {
		// &cmd.0 / &cmd.1 … imply the parent command's outputs are indexed.
		if dot := strings.LastIndexByte(n, '.'); dot > 0 {
			if _, err := strconv.Atoi(n[dot+1:]); err == nil {
				refs[n[:dot]] = true
			}
		}
	}
	return refs
}

// ensureIndexMapsLocked lazily builds the lookup maps on first use; it is a
// no-op once they exist. Callers must hold p.mu.
func (p *ParsedData) ensureIndexMapsLocked() {
	if p.variableMap == nil {
		p.variableMap = make(map[string]*Variable, len(p.Variables))
		for _, v := range p.Variables {
			p.variableMap[v.Scope+"."+v.Name] = v
		}
	}
	if p.commandMap == nil {
		p.commandMap = make(map[string]*Command, len(p.Commands))
		for _, cmd := range p.Commands {
			p.commandMap[cmd.Name] = cmd
		}
	}
}

// buildIndexMaps rebuilds the lookup maps from scratch (Parse, tests).
func (p *ParsedData) buildIndexMaps() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.variableMap = make(map[string]*Variable, len(p.Variables))
	for _, v := range p.Variables {
		p.variableMap[v.Scope+"."+v.Name] = v
	}
	p.commandMap = make(map[string]*Command, len(p.Commands))
	for _, cmd := range p.Commands {
		p.commandMap[cmd.Name] = cmd
	}
}

func (p *ParsedData) addVariable(v *Variable) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ensureIndexMapsLocked()
	p.Variables = append(p.Variables, v)
	p.variableMap[v.Scope+"."+v.Name] = v
}

func (p *ParsedData) SnapshotScope(scope string) []*Variable {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var out []*Variable
	for _, v := range p.Variables {
		if v.Scope == scope {
			out = append(out, &Variable{Name: v.Name, Value: v.Value, Scope: scope, IsList: v.IsList, List: v.List})
		}
	}
	return out
}

func (p *ParsedData) SeedScope(scope string, vars []*Variable) {
	for _, v := range vars {
		c := *v
		c.Scope = scope
		p.addVariable(&c)
	}
}

func (p *ParsedData) addCommand(cmd *Command) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ensureIndexMapsLocked()
	p.Commands = append(p.Commands, cmd)
	p.commandMap[cmd.Name] = cmd
}

func (p *ParsedData) SetVariable(name, scope, value string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.ensureIndexMapsLocked()
	key := scope + "." + name
	if v, ok := p.variableMap[key]; ok {
		v.Value = value
		v.IsList = false // a scalar overwrite replaces any previous list
		v.List = nil
		return
	}
	v := &Variable{Name: name, Value: value, Scope: scope}
	p.Variables = append(p.Variables, v)
	p.variableMap[key] = v
}

func (p *ParsedData) LookupVariable(name, scope string) (string, bool) {
	v, err := p.GetVariable(name, scope)
	if err != nil {
		return "", false
	}
	return v.Value, true
}

// GetVariable returns a copy: callers must not see concurrent SetVariable
// writes through a shared pointer.
func (p *ParsedData) GetVariable(variableName, scope string) (*Variable, error) {
	if strings.IndexByte(variableName, '"') >= 0 {
		variableName = strings.ReplaceAll(variableName, `"`, "")
	}

	if scope == "" {
		scope = "global"
	}

	if p.variableMap == nil {
		p.mu.Lock()
		p.ensureIndexMapsLocked()
		p.mu.Unlock()
	}

	p.mu.RLock()
	defer p.mu.RUnlock()

	if v, ok := p.variableMap[scope+"."+variableName]; ok {
		c := *v
		return &c, nil
	}
	if scope != "global" {
		if v, ok := p.variableMap["global."+variableName]; ok {
			c := *v
			return &c, nil
		}
	}
	return nil, fmt.Errorf("cannot find variable with name %s", variableName)
}

func (p *ParsedData) GetCommand(commandName string) (*Command, error) {
	if p.commandMap == nil {
		p.mu.Lock()
		p.ensureIndexMapsLocked()
		p.mu.Unlock()
	}
	p.mu.RLock()
	cmd, ok := p.commandMap[commandName]
	p.mu.RUnlock()
	if ok {
		return cmd, nil
	}
	return nil, fmt.Errorf("cannot find command with name %s", commandName)
}

func (p *ParsedData) GetDefaultCommand() (*Command, error) {
	for _, command := range p.Commands {
		if command.IsDefault {
			return command, nil
		}
	}

	return nil, errors.New("no default command")
}

func (p *ParsedData) GlobalVariableSnapshot() map[string]string {
	p.mu.RLock()
	defer p.mu.RUnlock()

	out := make(map[string]string)
	for _, v := range p.Variables {
		if v.Scope == "global" {
			out[v.Name] = v.Value
		}
	}
	return out
}

type Argument struct {
	Name       string `json:"name"`
	IsOptional bool   `json:"is_optional"`
	Default    string `json:"default,omitempty"`
}

type Variable struct {
	Name   string   `json:"name"`
	Value  string   `json:"value"`
	Scope  string   `json:"scope"`
	IsList bool     `json:"is_list,omitempty"`
	List   []string `json:"list,omitempty"`

	refs []string // &names in the raw value, for cache-key scoping
}

func (p *ParsedData) SetVariableValue(name, scope string, v Value) {
	if v.IsList {
		p.SetVariableList(name, scope, v.L)
		return
	}
	p.SetVariable(name, scope, v.S)
}

// SetVariableList stores a list; Value becomes the comma-joined form.
func (p *ParsedData) SetVariableList(name, scope string, items []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	value := strings.Join(items, ", ")
	p.ensureIndexMapsLocked()
	key := scope + "." + name
	if v, ok := p.variableMap[key]; ok {
		v.Value = value
		v.IsList = true
		v.List = items
		return
	}
	v := &Variable{Name: name, Value: value, Scope: scope, IsList: true, List: items}
	p.Variables = append(p.Variables, v)
	p.variableMap[key] = v
}

func (p *ParsedData) LookupVariableValue(name, scope string) (Value, bool) {
	v, err := p.GetVariable(name, scope)
	if err != nil || v == nil {
		return Value{}, false
	}
	if v.IsList {
		return ListValue(v.List), true
	}
	return StringValue(v.Value), true
}

func StripManual(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	rest, ok := strings.CutPrefix(trimmed, "manual ")
	if !ok {
		return line, false
	}
	rest = strings.TrimLeft(rest, " 	")
	if rest == "" || !isCommandNameStart(rest[0]) {
		return line, false
	}
	return rest, true
}

func StripService(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	rest, ok := strings.CutPrefix(trimmed, "service ")
	if !ok {
		return line, false
	}
	rest = strings.TrimLeft(rest, " 	")
	if rest == "" || !isCommandNameStart(rest[0]) {
		return line, false
	}
	return rest, true
}

func isCommandNameStart(c byte) bool {
	return c == '_' || c == '|' ||
		(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// IsLazyName reports a synthetic lazy-evaluation command, not a user command.
func IsLazyName(name string) bool {
	return strings.HasPrefix(name, "__lazy_") || strings.Contains(name, ".__lazy_")
}

type LazyOutput struct {
	VarName string `json:"var_name"`
	Scope   string `json:"scope"`
}

const (
	StmtShell      = "shell"
	StmtIf         = "if"
	StmtFor        = "for"
	StmtInvoke     = "invoke"
	StmtEnv        = "env"
	StmtContinue   = "continue"
	StmtBreak      = "break"
	StmtFail       = "fail"
	StmtOnFail     = "onfail"
	StmtRequireEnv = "require_env"
	StmtSwitch     = "switch"
	StmtInDir      = "in"
	StmtLock       = "lock"
	StmtState      = "state"
	StmtBuiltin    = "builtin"
	StmtConfirm    = "confirm"
	StmtPrompt     = "prompt"
	StmtInput      = "input"
	StmtPort       = "port"
)

type SwitchCase struct {
	Values     []string        `json:"values,omitempty"`
	IsDefault  bool            `json:"is_default,omitempty"`
	Body       []BodyStatement `json:"body,omitempty"`
	SourceLine int             `json:"source_line,omitempty"`
}

type BodyStatement struct {
	Type         string          `json:"type"` // one of the Stmt* constants
	Shell        string          `json:"shell,omitempty"`
	OutputName   string          `json:"output_name,omitempty"`
	Cond         string          `json:"cond,omitempty"`
	ThenBody     []BodyStatement `json:"then,omitempty"`
	ElseBody     []BodyStatement `json:"else,omitempty"`
	LoopVar      string          `json:"loop_var,omitempty"`
	LoopIndex    string          `json:"loop_index,omitempty"`
	LoopItems    string          `json:"loop_items,omitempty"`
	LoopBody     []BodyStatement `json:"loop_body,omitempty"`
	Env          []string        `json:"env,omitempty"`
	Message      string          `json:"message,omitempty"`
	OnFailBody   []BodyStatement `json:"onfail,omitempty"`
	InvokeArgs   []string        `json:"invoke_args,omitempty"`
	Retry        int             `json:"retry,omitempty"`
	Timeout      string          `json:"timeout,omitempty"`
	SwitchExpr   string          `json:"switch_expr,omitempty"`
	Cases        []SwitchCase    `json:"cases,omitempty"`
	Dir          string          `json:"dir,omitempty"`
	BuiltinArgs  string          `json:"builtin_args,omitempty"`
	Tolerant     bool            `json:"tolerant,omitempty"`
	Parallel     bool            `json:"parallel,omitempty"`
	ParallelJobs int             `json:"parallel_jobs,omitempty"`
	Modifier     string          `json:"modifier,omitempty"`
	SourceLine   int             `json:"source_line,omitempty"`
}

type Command struct {
	Name            string      `json:"name"`
	SourceFile      string      `json:"source_file,omitempty"`
	CloudAccessible bool        `json:"cloud_accessible"`
	IsDefault       bool        `json:"is_default"`
	IsService       bool        `json:"is_service,omitempty"`
	Port            string      `json:"port,omitempty"`
	LazyEval        *LazyOutput `json:"lazy_output"`
	IsPrereq        bool        `json:"is_prereq"`

	cacheGlobals      []string          // globals the command's refs reach, for cache keys
	cacheGlobalsExact bool              // false (hand-built data) keys on every global
	argKey            string            // flag-set scope, fixed at registration so renames keep args resolvable
	PrereqOutput      []string          `json:"prereq_output"`
	NamedOutput       map[string]string `json:"named_output"`
	Arguments         []*Argument       `json:"arguments"`
	Prereqs           []string          `json:"prereqs"`
	PrereqDirs        map[string]string `json:"prereq_dirs,omitempty"`
	FileDeps          []string          `json:"file_deps"`
	Produces          []string          `json:"produces,omitempty"`
	OnChange          []string          `json:"onchange,omitempty"`
	PrereqCmds        []*Command        `json:"prereq_cmds"`
	WorkDir           string            `json:"work_dir"`
	Container         string            `json:"container,omitempty"`
	Manual            bool              `json:"manual,omitempty"`
	Timeout           string            `json:"timeout,omitempty"`
	Body              []BodyStatement   `json:"body"`
	SourceLine        int               `json:"source_line,omitempty"`
	Description       string            `json:"description,omitempty"`
}

func (c *Command) flagScope() string {
	if c.argKey != "" {
		return c.argKey
	}
	return c.Name
}
