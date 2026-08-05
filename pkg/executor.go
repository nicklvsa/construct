package pkg

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/spf13/pflag"
)

type CommandError struct {
	Cmd      string
	ExitCode int
	Stderr   string
}

func (e *CommandError) Error() string {
	if e.Stderr != "" {
		return fmt.Sprintf("command '%s' failed (exit %d): %s", e.Cmd, e.ExitCode, e.Stderr)
	}
	return fmt.Sprintf("command '%s' failed (exit %d)", e.Cmd, e.ExitCode)
}

type Executor struct {
	StructuredParse *ParsedData
	concurrent      bool
	debug           bool
	cloudFile       string
	cloudDefs       map[string]Command
	cloudLoaded     bool
	shellName       string
	shellArgs       []string
	env             []string
	flagSet         *pflag.FlagSet
	cache           fileCache
	cacheLoaded     bool
	runs            map[string]*commandRun
	mu              sync.Mutex
}

type commandRun struct {
	done chan struct{}
	err  error
}

var (
	errLoopContinue = errors.New("continue used outside of a loop")
	errLoopBreak    = errors.New("break used outside of a loop")
)

func defaultShell() (string, []string) {
	if runtime.GOOS == "windows" {
		gitBashPaths := []string{
			os.Getenv("ProgramFiles") + `\Git\usr\bin\bash.exe`,
			os.Getenv("ProgramFiles(x86)") + `\Git\usr\bin\bash.exe`,
			os.Getenv("LOCALAPPDATA") + `\Programs\Git\usr\bin\bash.exe`,
		}
		for _, p := range gitBashPaths {
			if p != `\Git\usr\bin\bash.exe` {
				if _, err := os.Stat(p); err == nil {
					return p, []string{"-c"}
				}
			}
		}
		// Fall back to cmd.exe if Git Bash isn't installed.
		return "cmd", []string{"/c"}
	}
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh, nonInteractiveArgs(sh)
	}
	return "/bin/sh", []string{"-c"}
}

func nonInteractiveArgs(shell string) []string {
	switch base := path.Base(strings.ReplaceAll(shell, "\\", "/")); base {
	case "zsh":
		return []string{"-f", "-c"}
	case "bash", "bash.exe":
		return []string{"--noprofile", "--norc", "-c"}
	}
	return []string{"-c"}
}

func (e *Executor) childEnv() []string {
	return e.env
}

func (e *Executor) computeChildEnv() []string {
	env := os.Environ()
	if runtime.GOOS != "windows" {
		return env
	}
	// Only adjust if we're using Git Bash.
	bashDir := filepath.Dir(e.shellName)           // ...\Git\usr\bin
	gitRoot := filepath.Dir(filepath.Dir(bashDir)) // ...\Git
	usrBin := filepath.Join(gitRoot, "usr", "bin")
	if _, err := os.Stat(usrBin); err != nil {
		return env
	}

	for i, kv := range env {
		if strings.HasPrefix(strings.ToUpper(kv), "PATH=") {
			env[i] = "PATH=" + usrBin + string(os.PathListSeparator) + kv[5:]
			break
		}
	}
	return env
}

func NewExecutor(data *ParsedData, concurrent bool, debug bool) *Executor {
	cloudFile := os.Getenv("CONSTRUCT_CLOUD_FILE")
	if cloudFile == "" {
		cloudFile = "fakecloud.json"
	}

	shellName, shellArgs := defaultShell()
	if sh := os.Getenv("CONSTRUCT_SHELL"); sh != "" {
		if runtime.GOOS == "windows" {
			shellName, shellArgs = sh, []string{"/c"}
		} else {
			shellName, shellArgs = sh, nonInteractiveArgs(sh)
		}
	}

	executor := &Executor{
		concurrent:      concurrent,
		debug:           debug,
		cloudFile:       cloudFile,
		shellName:       shellName,
		shellArgs:       shellArgs,
		StructuredParse: data,
	}
	executor.env = executor.computeChildEnv()
	return executor
}

func (e *Executor) loadCloudDefs() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.cloudLoaded {
		return nil
	}

	fileBytes, err := os.ReadFile(e.cloudFile)
	if err != nil {
		e.cloudDefs = make(map[string]Command)
		e.cloudLoaded = true
		return nil
	}

	var defs map[string]Command
	if err := json.Unmarshal(fileBytes, &defs); err != nil {
		e.cloudDefs = make(map[string]Command)
		e.cloudLoaded = true
		return fmt.Errorf("failed to parse cloud file %q: %w", e.cloudFile, err)
	}

	e.cloudDefs = defs
	e.cloudLoaded = true
	return nil
}

func (e *Executor) RegisterArgumentFlags(flagSet *pflag.FlagSet) {
	e.flagSet = flagSet
	for _, cmd := range e.StructuredParse.Commands {
		for _, arg := range cmd.Arguments {
			flagName := fmt.Sprintf("%s:%s", cmd.Name, arg.Name)
			flagSet.String(flagName, "", fmt.Sprintf("Argument %s for command %s", arg.Name, cmd.Name))
		}
	}
}

func (e *Executor) SetDebug(debug bool) {
	e.debug = debug
}

func (e *Executor) EvaluateCommand(command *Command) error {
	if e.runs == nil {
		return e.executeCommand(command)
	}

	e.mu.Lock()
	if r, ok := e.runs[command.Name]; ok {
		e.mu.Unlock()
		<-r.done
		return r.err
	}
	r := &commandRun{done: make(chan struct{})}
	e.runs[command.Name] = r
	e.mu.Unlock()

	r.err = e.executeCommand(command)
	close(r.done)
	return r.err
}

func (e *Executor) executeCommand(command *Command) error {
	isPrereq := false
	workDir := ""
	e.mu.Lock()
	if command.IsPrereq {
		isPrereq = true
		command.PrereqOutput = []string{}
	}

	workDir = command.WorkDir
	e.mu.Unlock()

	resolveValue := func(s, scope string) string {
		s = resolveVarRefs(s, func(name string) (string, bool) {
			return e.StructuredParse.LookupVariable(name, scope)
		})
		return resolveEnvRefs(s)
	}

	if len(command.FileDeps) > 0 && !isPrereq {
		if e.shouldSkip(command, resolveValue, workDir) {
			fmt.Printf("(%s cached)\n", command.Name)
			return nil
		}
	}

	var execCommandBody func(target *Command, body []BodyStatement, isPrereq bool, workDir string) error
	execCommandBody = func(target *Command, body []BodyStatement, isPrereq bool, workDir string) error {
		for _, stmt := range body {
			if stmt.Type == "if" {
				cond := resolveValue(stmt.Cond, target.Name)
				if e.debug {
					fmt.Printf("[DEBUG] Evaluating condition: %s\n", cond)
				}
				if evaluateCondition(cond) {
					if err := execCommandBody(target, stmt.ThenBody, isPrereq, workDir); err != nil {
						return err
					}
				} else if stmt.ElseBody != nil {
					if err := execCommandBody(target, stmt.ElseBody, isPrereq, workDir); err != nil {
						return err
					}
				}
				continue
			}

			if stmt.Type == "for" {
				items := resolveValue(stmt.LoopItems, target.Name)
				var expanded []string
				if strings.ContainsAny(items, "*?") {
					wd := "."
					if workDir != "" {
						wd = resolveValue(workDir, target.Name)
					}
					for _, pattern := range strings.Split(items, ",") {
						pattern = strings.TrimSpace(pattern)
						matches, _ := filepath.Glob(filepath.Join(wd, pattern))
						if len(matches) == 0 {
							matches = []string{pattern}
						}
						for _, m := range matches {
							expanded = append(expanded, filepath.Base(m))
						}
					}
				} else {
					for item := range strings.SplitSeq(items, ",") {
						expanded = append(expanded, strings.TrimSpace(item))
					}
				}

				argFlags := make(map[string]bool)
				for _, arg := range target.Arguments {
					argFlags[target.Name+":"+arg.Name] = true
				}

			iterLoop:
				for _, item := range expanded {
					e.StructuredParse.SetVariable(stmt.LoopVar, target.Name, item)
					if e.debug {
						fmt.Printf("[DEBUG] For loop %s = %s\n", stmt.LoopVar, item)
					}
					cleaned := e.cleanStatements(stmt.LoopBody, target, argFlags)
					err := execCommandBody(target, cleaned, isPrereq, workDir)
					switch {
					case errors.Is(err, errLoopContinue):
						continue
					case errors.Is(err, errLoopBreak):
						break iterLoop
					case err != nil:
						return err
					}
				}
				continue
			}

			if stmt.Type == "continue" {
				return errLoopContinue
			}
			if stmt.Type == "break" {
				return errLoopBreak
			}

			cmdLine := stmt.Shell
			if cmdLine == "" {
				continue
			}

			ignoreErr := false
			if strings.HasPrefix(cmdLine, "!") {
				ignoreErr = true
				cmdLine = strings.TrimSpace(cmdLine[1:])
			}

			args := append(e.shellArgs, cmdLine)
			cmd := exec.Command(e.shellName, args...)
			if workDir != "" {
				cmd.Dir = resolveValue(workDir, target.Name)
			}
			cmd.Env = e.childEnv()

			var fullCommand string
			if e.debug {
				fullCommand = e.shellName + " " + strings.Join(args, " ")
			}

			if e.debug {
				switch {
				case isPrereq:
					fmt.Printf("[DEBUG] Running prerequisite %s: %s\n", target.Name, fullCommand)
				case target.LazyEval != nil:
					fmt.Printf("[DEBUG] Running lazy command for variable %s: %s\n", target.LazyEval.VarName, fullCommand)
				default:
					fmt.Printf("[DEBUG] Running command %s: %s\n", target.Name, fullCommand)
				}
			}

			if !e.debug && !isPrereq && target.LazyEval == nil {
				cmd.Stdout = os.Stdout
				cmd.Stderr = os.Stderr
				if err := cmd.Run(); err != nil {
					exitCode := 1
					if ee, ok := err.(*exec.ExitError); ok {
						exitCode = ee.ExitCode()
					}
					if fullCommand == "" {
						fullCommand = e.shellName + " " + strings.Join(args, " ")
					}
					if e.debug {
						fmt.Printf("[DEBUG] Command failed: %v\n", err)
					}
					if !ignoreErr {
						return &CommandError{
							Cmd:      fullCommand,
							ExitCode: exitCode,
						}
					}
				}
				continue
			}

			var stdout, stderr []byte
			var err error
			if e.debug {
				stdout, stderr, err = capture(cmd)
			} else {
				stdout, err = cmd.Output()
			}

			output := stdout
			if len(stderr) > 0 {
				if len(output) > 0 {
					output = append(output, '\n')
				}
				output = append(output, stderr...)
			}

			if err != nil {
				exitCode := 1
				if ee, ok := err.(*exec.ExitError); ok {
					exitCode = ee.ExitCode()
				}

				if e.debug {
					fmt.Printf("[DEBUG] Command failed: %v\n", err)
					if len(stderr) > 0 {
						fmt.Printf("[DEBUG] Error output: %s\n", string(stderr))
					}
				}

				if fullCommand == "" {
					fullCommand = e.shellName + " " + strings.Join(args, " ")
				}

				if ignoreErr {
					if e.debug {
						fmt.Printf("[DEBUG] Ignoring failure (error-tolerant statement)\n")
					}
				} else {
					return &CommandError{
						Cmd:      fullCommand,
						ExitCode: exitCode,
						Stderr:   string(stderr),
					}
				}
			}

			strOutput := strings.TrimSpace(string(output))

			switch {
			case isPrereq:
				e.mu.Lock()
				target.PrereqOutput = append(target.PrereqOutput, strOutput)
				if stmt.OutputName != "" {
					if target.NamedOutput == nil {
						target.NamedOutput = make(map[string]string)
					}
					target.NamedOutput[stmt.OutputName] = strOutput
				}
				e.mu.Unlock()
				if e.debug {
					fmt.Printf("[DEBUG] Prereq output: %s\n", strOutput)
					if stmt.OutputName != "" {
						fmt.Printf("[DEBUG] Named output %s.%s = %s\n", target.Name, stmt.OutputName, strOutput)
					}
				}
			case target.LazyEval != nil:
				e.StructuredParse.SetVariable(target.LazyEval.VarName, target.LazyEval.Scope, strOutput)
				if e.debug {
					fmt.Printf("[DEBUG] Set variable %s.%s = %s\n", target.LazyEval.Scope, target.LazyEval.VarName, strOutput)
				}
			default:
				fmt.Println(strOutput)
			}
		}

		return nil
	}

	cleanCommandBody := func(cmd *Command) ([]BodyStatement, error) {
		if len(cmd.PrereqCmds) > 0 {
			for _, prereq := range cmd.PrereqCmds {
				for idx, arg := range prereq.PrereqOutput {
					varName := prereq.Name + "." + fmt.Sprintf("%d", idx)
					e.StructuredParse.SetVariable(strings.TrimSpace(varName), cmd.Name, strings.TrimSpace(arg))
				}
				// Register named outputs (&prereq.name)
				for name, val := range prereq.NamedOutput {
					varName := prereq.Name + "." + name
					e.StructuredParse.SetVariable(varName, cmd.Name, strings.TrimSpace(val))
				}
			}
		}

		argFlags := make(map[string]bool, len(cmd.Arguments))
		for _, arg := range cmd.Arguments {
			argFlags[cmd.Name+":"+arg.Name] = true
		}

		var cleanStmts func(stmts []BodyStatement) []BodyStatement
		cleanStmts = func(stmts []BodyStatement) []BodyStatement {
			out := make([]BodyStatement, len(stmts))
			for i, stmt := range stmts {
				if stmt.Type == "if" {
					out[i] = BodyStatement{
						Type:     "if",
						Cond:     stmt.Cond,
						ThenBody: cleanStmts(stmt.ThenBody),
						ElseBody: cleanStmts(stmt.ElseBody),
					}
					continue
				}
				if stmt.Type == "for" {
					out[i] = BodyStatement{
						Type:      "for",
						LoopVar:   stmt.LoopVar,
						LoopItems: e.cleanShellLine(cmd, stmt.LoopItems, argFlags),
						LoopBody:  stmt.LoopBody,
					}
					continue
				}
				if stmt.Type == "continue" || stmt.Type == "break" {
					out[i] = stmt
					continue
				}
				out[i] = BodyStatement{Type: "shell", Shell: e.cleanShellLine(cmd, stmt.Shell, argFlags), OutputName: stmt.OutputName}
			}
			return out
		}

		return cleanStmts(cmd.Body), nil
	}

	var prereqCmds []*Command
	for _, prereqName := range command.Prereqs {
		prereqName = strings.TrimSpace(prereqName)
		if prereqName == "" {
			continue
		}

		preCmd, err := e.StructuredParse.GetCommand(prereqName)
		if err != nil {
			return err
		}

		e.mu.Lock()
		preCmd.IsPrereq = true
		if dir := command.PrereqDirs[prereqName]; dir != "" {
			preCmd.WorkDir = dir
		}
		e.mu.Unlock()

		prereqCmds = append(prereqCmds, preCmd)
	}

	if command.PrereqCmds == nil {
		command.PrereqCmds = []*Command{}
	}

	if e.concurrent && len(prereqCmds) > 1 {
		// DAG execution: evaluate independent prerequisites in parallel.
		results := make([]*Command, len(prereqCmds))
		errs := make([]error, len(prereqCmds))
		var wg sync.WaitGroup
		for i, preCmd := range prereqCmds {
			wg.Add(1)
			go func(i int, pc *Command) {
				defer wg.Done()
				errs[i] = e.EvaluateCommand(pc)
				results[i] = pc
			}(i, preCmd)
		}
		wg.Wait()
		for _, err := range errs {
			if err != nil {
				return err
			}
		}
		command.PrereqCmds = append(command.PrereqCmds, results...)
	} else {
		for _, preCmd := range prereqCmds {
			if err := e.EvaluateCommand(preCmd); err != nil {
				return err
			}
			command.PrereqCmds = append(command.PrereqCmds, preCmd)
		}
	}

	if err := e.tryApplyCloudBody(command); err != nil {
		return err
	}

	body, err := cleanCommandBody(command)
	if err != nil {
		return err
	}

	if err := execCommandBody(command, body, isPrereq, workDir); err != nil {
		return err
	}

	if len(command.FileDeps) > 0 && !isPrereq {
		e.updateCache(command, resolveValue, workDir)
	}

	return nil
}

func (e *Executor) cleanStatements(stmts []BodyStatement, cmd *Command, argFlags map[string]bool) []BodyStatement {
	out := make([]BodyStatement, len(stmts))
	for i, stmt := range stmts {
		switch stmt.Type {
		case "if":
			out[i] = BodyStatement{
				Type:     "if",
				Cond:     stmt.Cond,
				ThenBody: e.cleanStatements(stmt.ThenBody, cmd, argFlags),
				ElseBody: e.cleanStatements(stmt.ElseBody, cmd, argFlags),
			}
		case "for":
			out[i] = BodyStatement{
				Type:      "for",
				LoopVar:   stmt.LoopVar,
				LoopItems: e.cleanShellLine(cmd, stmt.LoopItems, argFlags),
				LoopBody:  stmt.LoopBody,
			}
		case "continue", "break":
			out[i] = stmt
		default:
			out[i] = BodyStatement{Type: "shell", Shell: e.cleanShellLine(cmd, stmt.Shell, argFlags), OutputName: stmt.OutputName}
		}
	}
	return out
}

func (e *Executor) cleanShellLine(cmd *Command, line string, argFlags map[string]bool) string {
	line = strings.TrimSpace(line)
	if line == "" {
		return ""
	}

	if len(line) > 0 && line[0] == '$' {
		line = strings.TrimSpace(line[1:])
	}

	line = resolveVarRefs(line, func(name string) (string, bool) {
		val, ok := e.StructuredParse.LookupVariable(name, cmd.Name)
		if !ok {
			return "", false
		}
		return escapeShellValue(val), true
	})

	for _, arg := range cmd.Arguments {
		lookupKey := cmd.Name + ":" + arg.Name
		if !argFlags[lookupKey] {
			continue
		}

		if !strings.Contains(line, "&"+arg.Name) {
			continue
		}
		if e.debug {
			fmt.Printf("[DEBUG] Handling argument --%s for command %s\n", arg.Name, cmd.Name)
		}
		fs := e.flagSet
		if fs == nil {
			fs = pflag.CommandLine
		}

		v, _ := fs.GetString(lookupKey)
		line = strings.ReplaceAll(line, "&"+arg.Name, escapeShellValue(v))
	}

	return line
}

func isVarIdentByte(c byte) bool {
	return c == '_' || c == '-' ||
		(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func escapeShellValue(s string) string {
	if !strings.ContainsAny(s, "`\"\\$") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '`', '"', '\\', '$':
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func isPlainIdentByte(c byte) bool {
	return c == '_' ||
		(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

func resolveVarRefs(line string, lookup func(string) (string, bool)) string {
	if strings.IndexByte(line, '&') < 0 {
		return line
	}
	if !isASCII(line) {
		return resolveVarRefsRunes(line, lookup)
	}

	var result strings.Builder
	result.Grow(len(line))
	i := 0
	for i < len(line) {
		if line[i] == '&' {
			j := i + 1
			start := j
			for j < len(line) && isVarIdentByte(line[j]) {
				j++
			}
			if j < len(line) && line[j] == '.' {
				k := j + 1
				ks := k
				for k < len(line) && isPlainIdentByte(line[k]) {
					k++
				}
				if k > ks {
					j = k
				}
			}
			if name := line[start:j]; name != "" {
				if val, ok := lookup(name); ok {
					result.WriteString(val)
					i = j
					continue
				}
			}
		}
		result.WriteByte(line[i])
		i++
	}
	return result.String()
}

func resolveVarRefsRunes(line string, lookup func(string) (string, bool)) string {
	var result strings.Builder
	runes := []rune(line)
	i := 0
	for i < len(runes) {
		if runes[i] == '&' {
			var name strings.Builder
			j := i + 1
			for j < len(runes) && (unicode.IsLetter(runes[j]) || unicode.IsDigit(runes[j]) || runes[j] == '_' || runes[j] == '-') {
				name.WriteRune(runes[j])
				j++
			}

			if j < len(runes) && runes[j] == '.' {
				k := j + 1
				for k < len(runes) && (unicode.IsLetter(runes[k]) || unicode.IsDigit(runes[k]) || runes[k] == '_') {
					k++
				}
				if k > j+1 {
					name.WriteRune('.')
					for m := j + 1; m < k; m++ {
						name.WriteRune(runes[m])
					}
					j = k
				}
			}
			if name.Len() > 0 {
				if val, ok := lookup(name.String()); ok {
					result.WriteString(val)
					i = j
					continue
				}
			}
		}
		result.WriteRune(runes[i])
		i++
	}
	return result.String()
}

// resolveEnvRefs replaces @ENVNAME references with the corresponding env var.
func resolveEnvRefs(s string) string {
	if strings.IndexByte(s, '@') < 0 {
		return s
	}
	if !isASCII(s) {
		return resolveEnvRefsRunes(s)
	}

	var result strings.Builder
	result.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == '@' {
			j := i + 1
			start := j
			for j < len(s) && isPlainIdentByte(s[j]) {
				j++
			}
			if j > start {
				result.WriteString(os.Getenv(s[start:j]))
				i = j
				continue
			}
		}
		result.WriteByte(s[i])
		i++
	}
	return result.String()
}

func resolveEnvRefsRunes(s string) string {
	var result strings.Builder
	runes := []rune(s)
	for i := 0; i < len(runes); {
		if runes[i] == '@' {
			var name strings.Builder
			j := i + 1
			for j < len(runes) && (unicode.IsLetter(runes[j]) || unicode.IsDigit(runes[j]) || runes[j] == '_') {
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

func findTopLevelOp(s, op string) int {
	depth := 0
	inQuote := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '"' {
			inQuote = !inQuote
			continue
		}
		if inQuote {
			continue
		}
		switch c {
		case '(':
			depth++
		case ')':
			depth--
		}
		if depth == 0 && strings.HasPrefix(s[i:], op) {
			return i
		}
	}
	return -1
}

func evaluateCondition(cond string) bool {
	cond = strings.TrimSpace(cond)

	// Parenthesized sub-expression: ( expr )
	if strings.HasPrefix(cond, "(") && strings.HasSuffix(cond, ")") {
		return evaluateCondition(strings.TrimSpace(cond[1 : len(cond)-1]))
	}

	// Logical OR / AND, lowest precedence first.
	if idx := findTopLevelOp(cond, "||"); idx >= 0 {
		return evaluateCondition(cond[:idx]) || evaluateCondition(cond[idx+2:])
	}
	if idx := findTopLevelOp(cond, "&&"); idx >= 0 {
		return evaluateCondition(cond[:idx]) && evaluateCondition(cond[idx+2:])
	}

	// Negation: ! expr
	if strings.HasPrefix(cond, "!") {
		rest := strings.TrimSpace(cond[1:])
		return rest != "" && !evaluateCondition(rest)
	}

	if idx := strings.Index(cond, " contains "); idx > 0 {
		left := strings.TrimSpace(cond[:idx])
		right := strings.TrimSpace(cond[idx+len(" contains "):])
		left = strings.Trim(left, "\"")
		right = strings.Trim(right, "\"")
		return strings.Contains(left, right)
	}

	ops := []string{"==", "!=", ">=", "<=", ">", "<"}
	for _, op := range ops {
		if idx := strings.Index(cond, op); idx > 0 {
			left := strings.TrimSpace(cond[:idx])
			right := strings.TrimSpace(cond[idx+len(op):])
			left = strings.Trim(left, "\"")
			right = strings.Trim(right, "\"")
			return compareValues(left, right, op)
		}
	}
	return false
}

func compareValues(left, right, op string) bool {
	if li, err := strconv.Atoi(left); err == nil {
		if ri, err := strconv.Atoi(right); err == nil {
			return compareInt(li, ri, op)
		}
	}
	return compareString(left, right, op)
}

func compareInt(l, r int, op string) bool {
	switch op {
	case "==":
		return l == r
	case "!=":
		return l != r
	case "<":
		return l < r
	case ">":
		return l > r
	case "<=":
		return l <= r
	case ">=":
		return l >= r
	}
	return false
}

func compareString(l, r, op string) bool {
	switch op {
	case "==":
		return l == r
	case "!=":
		return l != r
	case "<":
		return l < r
	case ">":
		return l > r
	case "<=":
		return l <= r
	case ">=":
		return l >= r
	}
	return false
}

func (e *Executor) tryApplyCloudBody(cmd *Command) error {
	if !cmd.CloudAccessible {
		return nil
	}

	external, err := e.getCloudDefinition(cmd.Name)
	if err != nil || external == nil {
		return nil
	}

	for _, stmt := range external.Body {
		cmd.Body = append(cmd.Body, stmt)
	}
	cmd.CloudAccessible = false
	return nil
}

func (e *Executor) Exec(commands []string) error {
	e.runs = make(map[string]*commandRun)

	targets := make([]string, 0, len(commands))
	for _, cmdName := range commands {
		if cmdName == "" || cmdName[0] == '-' {
			continue
		}
		targets = append(targets, cmdName)
	}

	if len(targets) == 0 {
		if defaultCommand, err := e.StructuredParse.GetDefaultCommand(); err == nil && defaultCommand != nil {
			targets = []string{defaultCommand.Name}
		}
	}

	neededScopes := make(map[string]bool)
	for _, name := range targets {
		neededScopes[name] = true
	}
	neededScopes["global"] = true

	for _, cmd := range e.StructuredParse.Commands {
		if cmd.LazyEval == nil {
			continue
		}
		if !neededScopes[cmd.LazyEval.Scope] {
			continue
		}
		if prevCmd, err := e.StructuredParse.GetCommand(cmd.LazyEval.Scope); err == nil && prevCmd != nil {
			cmd.Arguments = append(cmd.Arguments, prevCmd.Arguments...)
		}
		if err := e.EvaluateCommand(cmd); err != nil {
			return err
		}
	}

	if e.concurrent {
		return e.execConcurrent(targets)
	}

	for _, cmdName := range targets {
		if err := e.processCommand(cmdName); err != nil {
			return err
		}
	}

	return nil
}

func (e *Executor) execConcurrent(targets []string) error {
	var waiter sync.WaitGroup
	errCh := make(chan error, len(targets))

	for _, cmdName := range targets {
		waiter.Add(1)
		go func(name string) {
			defer waiter.Done()
			errCh <- e.processCommand(name)
		}(cmdName)
	}

	go func() {
		waiter.Wait()
		close(errCh)
	}()

	var firstErr error
	for err := range errCh {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (e *Executor) Execute(commands []string) error {
	return e.Exec(commands)
}

func (e *Executor) processCommand(name string) error {
	command, err := e.StructuredParse.GetCommand(name)
	if err != nil {
		return err
	}
	return e.EvaluateCommand(command)
}

func (e *Executor) getCloudDefinition(name string) (*Command, error) {
	if err := e.loadCloudDefs(); err != nil {
		return nil, err
	}

	if c, ok := e.cloudDefs[name]; ok {
		return &c, nil
	}

	return nil, fmt.Errorf("%s command not found in cloud", name)
}

// capture reads stdout and stderr concurrently to avoid pipe-buffer deadlock.
func capture(cmd *exec.Cmd) (stdout, stderr []byte, err error) {
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("failed to start command: %w", err)
	}

	var stdoutBytes, stderrBytes []byte
	var stdoutErr, stderrErr error
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		stdoutBytes, stdoutErr = io.ReadAll(stdoutPipe)
	}()

	go func() {
		defer wg.Done()
		stderrBytes, stderrErr = io.ReadAll(stderrPipe)
	}()

	wg.Wait()

	if stdoutErr != nil {
		return nil, nil, fmt.Errorf("failed to read stdout: %w", stdoutErr)
	}
	if stderrErr != nil {
		return nil, nil, fmt.Errorf("failed to read stderr: %w", stderrErr)
	}

	if err := cmd.Wait(); err != nil {
		return stdoutBytes, stderrBytes, err
	}

	return stdoutBytes, stderrBytes, nil
}

const cacheDir = ".construct-cache"

type fileCache map[string]map[string]string

func loadFileCache() fileCache {
	data, err := os.ReadFile(filepath.Join(cacheDir, "manifest.json"))
	if err != nil {
		return fileCache{}
	}
	var fc fileCache
	if err := json.Unmarshal(data, &fc); err != nil {
		return fileCache{}
	}
	return fc
}

func (fc fileCache) save() {
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return
	}
	data, _ := json.MarshalIndent(fc, "", "  ")
	os.WriteFile(filepath.Join(cacheDir, "manifest.json"), data, 0644)
}

func hashFile(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

func expandFileDeps(patterns []string, workDir string) []string {
	wd := workDir
	if wd == "" {
		wd = "."
	}
	var files []string
	for _, pattern := range patterns {
		full := filepath.Join(wd, pattern)
		matches, err := filepath.Glob(full)
		if err != nil || len(matches) == 0 {
			files = append(files, full)
			continue
		}
		files = append(files, matches...)
	}
	return files
}

func (e *Executor) cacheManifest() fileCache {
	e.mu.Lock()
	defer e.mu.Unlock()

	if !e.cacheLoaded {
		e.cache = loadFileCache()
		e.cacheLoaded = true
	}
	return e.cache
}

func (e *Executor) shouldSkip(cmd *Command, resolve func(string, string) string, workDir string) bool {
	wd := resolve(workDir, cmd.Name)
	files := expandFileDeps(cmd.FileDeps, wd)
	if len(files) == 0 {
		return false
	}

	fc := e.cacheManifest()
	cached, exists := fc[cmd.Name]
	if !exists {
		return false
	}

	for _, f := range files {
		current := hashFile(f)
		if cached[f] != current {
			if e.debug {
				fmt.Printf("[DEBUG] %s: file changed: %s\n", cmd.Name, f)
			}
			return false
		}
	}
	return true
}

func (e *Executor) updateCache(cmd *Command, resolve func(string, string) string, workDir string) {
	wd := resolve(workDir, cmd.Name)
	files := expandFileDeps(cmd.FileDeps, wd)

	e.mu.Lock()
	defer e.mu.Unlock()

	if !e.cacheLoaded {
		e.cache = loadFileCache()
		e.cacheLoaded = true
	}
	fc := e.cache
	if fc[cmd.Name] == nil {
		fc[cmd.Name] = make(map[string]string)
	}
	for _, f := range files {
		fc[cmd.Name][f] = hashFile(f)
	}
	fc.save()
}
