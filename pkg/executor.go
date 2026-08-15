package pkg

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/spf13/pflag"
	"golang.org/x/term"
)

type FailError struct {
	Message string
	File    string
	Line    int
}

func (e *FailError) Error() string {
	if e.File != "" {
		return fmt.Sprintf("fail: %s (%s:%d)", e.Message, e.File, e.Line)
	}
	return fmt.Sprintf("fail: %s", e.Message)
}

// CommandError is returned when a shell statement exits non-zero.
type CommandError struct {
	Cmd      string
	ExitCode int
	Stderr   string
	File     string
	Line     int
	TimedOut bool
	Timeout  string
}

func (e *CommandError) Error() string {
	loc := ""
	if e.File != "" {
		loc = fmt.Sprintf(" (%s:%d)", e.File, e.Line)
	}
	if e.TimedOut {
		prefix := fmt.Sprintf("command '%s' timed out after %s (exit 124)%s", e.Cmd, e.Timeout, loc)
		if e.Stderr != "" {
			return prefix + ": " + e.Stderr
		}
		return prefix
	}
	if e.Stderr != "" {
		return fmt.Sprintf("command '%s' failed (exit %d)%s: %s", e.Cmd, e.ExitCode, loc, e.Stderr)
	}
	return fmt.Sprintf("command '%s' failed (exit %d)%s", e.Cmd, e.ExitCode, loc)
}

type Executor struct {
	StructuredParse *ParsedData
	concurrent      bool
	keepGoing       bool
	noCache         bool
	quiet           bool
	explain         bool
	debug           bool
	prefixOutput    bool
	runCtx          context.Context
	cloudDefs       map[string]Command
	cloudLoaded     bool
	shellName       string
	shellArgs       []string
	env             []string
	flagSet         *pflag.FlagSet
	cache           fileCache
	cacheLoaded     bool
	runs            map[string]*commandRun
	baseDir         string
	jobs            int
	sem             chan struct{}
	mu              sync.Mutex
	invokeDepth     map[string]int // in-progress invoke chains, for cycle detection
	timing          bool           // print per-command elapsed time
	yes             bool           // --yes: auto-approve confirmations
	flame           bool           // --flame: record per-statement timing
	flameRows       []FlameRow
	ghActions       bool // GitHub Actions native output
	recordRuns      bool
	runRecords      map[string]RunRecord
	state           map[string]string
	stateLoaded     bool
	containerRT     string
	observer        RunObserver
	stdoutSink      io.Writer
	stderrSink      io.Writer
	silentStatus    bool
}

type RunObserver interface {
	CommandStarted(name string)
	CommandFinished(name string, rec RunRecord)
}

type OutputCollector interface {
	OutputWriter(name string) io.Writer
}

type FlameRow struct {
	Label  string
	Start  time.Time
	End    time.Time
	Failed bool
	Depth  int
}

type RunRecord struct {
	Status     string    `json:"status"` // ok, failed, skipped
	Exit       int       `json:"exit,omitempty"`
	DurationMs int64     `json:"duration_ms,omitempty"`
	End        time.Time `json:"end"`
	Error      string    `json:"error,omitempty"`
}

func (e *Executor) FlameRows() []FlameRow {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]FlameRow(nil), e.flameRows...)
}

func (e *Executor) RunRecords() map[string]RunRecord {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make(map[string]RunRecord, len(e.runRecords))
	maps.Copy(out, e.runRecords)
	return out
}

func (e *Executor) debugf(format string, args ...any) {
	if e.debug {
		fmt.Printf("[DEBUG] "+format, args...)
	}
}

func (e *Executor) explainf(format string, args ...any) {
	if e.explain {
		fmt.Printf(format, args...)
	}
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
		var gitBashPaths []string
		if dir := os.Getenv("ProgramFiles"); dir != "" {
			gitBashPaths = append(gitBashPaths, filepath.Join(dir, `Git\usr\bin\bash.exe`))
		}
		if dir := os.Getenv("ProgramFiles(x86)"); dir != "" {
			gitBashPaths = append(gitBashPaths, filepath.Join(dir, `Git\usr\bin\bash.exe`))
		}
		if dir := os.Getenv("LOCALAPPDATA"); dir != "" {
			gitBashPaths = append(gitBashPaths, filepath.Join(dir, `Programs\Git\usr\bin\bash.exe`))
		}
		for _, p := range gitBashPaths {
			if _, err := os.Stat(p); err == nil {
				return p, []string{"-c"}
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

func DefaultShell() (string, []string) {
	return defaultShell()
}

func NewExecutor(data *ParsedData, concurrent bool, debug bool) *Executor {
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
		prefixOutput:    concurrent,
		debug:           debug,
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

	fileBytes, err := os.ReadFile(e.resolveCloudFile())
	if err != nil {
		e.cloudDefs = make(map[string]Command)
		e.cloudLoaded = true
		return nil
	}

	var defs map[string]Command
	if err := json.Unmarshal(fileBytes, &defs); err != nil {
		e.cloudDefs = make(map[string]Command)
		e.cloudLoaded = true
		return fmt.Errorf("failed to parse cloud file %q: %w", e.resolveCloudFile(), err)
	}

	e.cloudDefs = defs
	e.cloudLoaded = true
	return nil
}

func (e *Executor) RegisterArgumentFlags(flagSet *pflag.FlagSet) {
	e.flagSet = flagSet
	for _, cmd := range e.StructuredParse.Commands {
		cmd.argKey = cmd.Name
		for _, arg := range cmd.Arguments {
			flagName := fmt.Sprintf("%s:%s", cmd.Name, arg.Name)
			flagSet.String(flagName, arg.Default, fmt.Sprintf("Argument %s for command %s", arg.Name, cmd.Name))
		}
	}
}

func (e *Executor) SetDebug(debug bool) {
	e.debug = debug
}

func (e *Executor) SetParsedData(data *ParsedData) {
	e.StructuredParse = data
}

func (e *Executor) SetBaseDir(dir string) {
	e.baseDir = dir
}

func (e *Executor) SetJobs(n int) {
	e.jobs = n
	if n > 0 {
		e.sem = make(chan struct{}, n)
	}
}

func (e *Executor) SetTiming(t bool) {
	e.timing = t
}

func (e *Executor) SetObserver(o RunObserver) {
	e.observer = o
}

func (e *Executor) SetStdoutSink(w io.Writer) {
	e.stdoutSink = w
}

func (e *Executor) SetStderrSink(w io.Writer) {
	e.stderrSink = w
}

func (e *Executor) outSink() io.Writer {
	if e.stdoutSink != nil {
		return e.stdoutSink
	}
	return os.Stdout
}

func (e *Executor) errSink() io.Writer {
	if e.stderrSink != nil {
		return e.stderrSink
	}
	return os.Stderr
}

func (e *Executor) errSinkFor(ctx *execContext) io.Writer {
	if oc, ok := e.observer.(OutputCollector); ok && !e.quiet {
		return oc.OutputWriter(ctx.target.Name)
	}
	return e.errSink()
}

func (e *Executor) SetSilentStatus(v bool) {
	e.silentStatus = v
}

func (e *Executor) notifyStart(name string) {
	if e.observer != nil {
		e.observer.CommandStarted(name)
	}
}

func (e *Executor) notifyFinish(name string, rec RunRecord) {
	if e.observer != nil {
		e.observer.CommandFinished(name, rec)
	}
}

func (e *Executor) SetNoCache(v bool) {
	e.noCache = v
}

func (e *Executor) SetKeepGoing(v bool) {
	e.keepGoing = v
}

func (e *Executor) SetQuiet(v bool) {
	e.quiet = v
}

func (e *Executor) SetExplain(v bool) {
	e.explain = v
}

func (e *Executor) SetRunContext(ctx context.Context) {
	e.runCtx = ctx
}

func (e *Executor) SetYes(v bool) {
	e.yes = v
}

func (e *Executor) SetFlame(v bool) {
	e.flame = v
}

func (e *Executor) SetGithubActions(v bool) {
	e.ghActions = v
}

func (e *Executor) SetRecordRuns(v bool) {
	e.recordRuns = v
	if v {
		e.runRecords = make(map[string]RunRecord)
	}
}

func (e *Executor) statePath() string {
	return filepath.Join(e.cacheDirFor(), "state.json")
}

func LoadRunHistory(dir string) map[string][]RunRecord {
	data, err := os.ReadFile(filepath.Join(dir, "run-state.json"))
	if err != nil {
		return nil
	}
	var hist map[string][]RunRecord
	if err := json.Unmarshal(data, &hist); err != nil {
		return nil
	}
	return hist
}

func LastRecord(hist map[string][]RunRecord) map[string]RunRecord {
	out := make(map[string]RunRecord, len(hist))
	for name, recs := range hist {
		if len(recs) > 0 {
			out[name] = recs[len(recs)-1]
		}
	}
	return out
}

func SaveRunHistory(dir string, hist map[string][]RunRecord) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return
	}
	data, _ := json.MarshalIndent(hist, "", "  ")
	_ = os.WriteFile(filepath.Join(dir, "run-state.json"), data, 0644)
}

func (e *Executor) loadState() {
	if e.stateLoaded {
		return
	}
	e.stateLoaded = true
	e.state = make(map[string]string)
	data, err := os.ReadFile(e.statePath())
	if err != nil {
		return
	}
	_ = json.Unmarshal(data, &e.state)
	if e.state == nil {
		e.state = make(map[string]string)
	}
}

func (e *Executor) stateLookup(name string) (string, bool) {
	e.loadState()
	v, ok := e.state[name]
	return v, ok
}

func (e *Executor) resolveGlobalValue(s string) string {
	s = resolveStateRefsWith(s, e.stateLookup)
	s = resolveVarRefs(s, func(name string) (string, bool) {
		v, ok := LookupVariableIndexed(e.StructuredParse, name, "global")
		if !ok {
			return "", false
		}
		return v.String(), true
	})
	return resolveEnvRefsWith(s, func(name string) string { return os.Getenv(name) })
}

func (e *Executor) setRuntimeState(name, value string) {
	e.loadState()
	e.state[name] = value
	if err := os.MkdirAll(e.cacheDirFor(), 0755); err != nil {
		return
	}
	data, _ := json.MarshalIndent(e.state, "", "  ")
	_ = os.WriteFile(e.statePath(), data, 0644)
}

func (e *Executor) recordRun(name string, rec RunRecord) {
	if !e.recordRuns {
		return
	}
	e.mu.Lock()
	if e.runRecords == nil {
		e.runRecords = make(map[string]RunRecord)
	}
	e.runRecords[name] = rec
	e.mu.Unlock()
}

func (e *Executor) saveRunRecords() {
	if !e.recordRuns {
		return
	}
	e.mu.Lock()
	rec := e.runRecords
	e.mu.Unlock()
	if rec == nil {
		return
	}
	dir := e.cacheDirFor()
	hist := LoadRunHistory(dir)
	if hist == nil {
		hist = make(map[string][]RunRecord)
	}
	for name, r := range rec {
		recs := hist[name]
		recs = append(recs, r)
		if len(recs) > 50 {
			recs = recs[len(recs)-50:]
		}
		hist[name] = recs
	}
	SaveRunHistory(dir, hist)
}

func (e *Executor) command(ctx *execContext, argv []string) *exec.Cmd {
	runCtx := e.effectiveRunCtx(ctx)
	cmd := exec.CommandContext(runCtx, argv[0], argv[1:]...)
	prepareProcessGroup(cmd)
	cmd.Cancel = func() error {
		return killProcessGroup(cmd)
	}
	if ctx.workDir != "" {
		cmd.Dir = e.resolveWorkDir(e.resolveBodyValue(ctx, ctx.workDir, ctx.target.Name))
	} else if e.baseDir != "" {
		cmd.Dir = e.baseDir
	}
	cmd.Env = *ctx.env
	return cmd
}

func (e *Executor) effectiveRunCtx(ctx *execContext) context.Context {
	runCtx := e.runCtx
	if ctx.runCtx != nil {
		runCtx = ctx.runCtx
	}
	if runCtx == nil {
		runCtx = context.Background()
	}
	return runCtx
}

func (e *Executor) statementCtx(ctx *execContext, timeout string) (*execContext, context.CancelFunc) {
	if timeout == "" {
		return ctx, func() {}
	}
	d, err := time.ParseDuration(timeout)
	if err != nil {
		return ctx, func() {}
	}
	sub := *ctx
	runCtx, cancel := context.WithTimeout(e.effectiveRunCtx(ctx), d)
	sub.runCtx = runCtx
	return &sub, cancel
}

func (e *Executor) SetShell(name string) {
	if name == "" {
		return
	}
	if runtime.GOOS == "windows" {
		e.shellName, e.shellArgs = name, []string{"/c"}
	} else {
		e.shellName, e.shellArgs = name, nonInteractiveArgs(name)
	}
}

func (e *Executor) acquire() (release func()) {
	if e.sem == nil {
		return func() {}
	}
	e.sem <- struct{}{}
	return func() { <-e.sem }
}

func (e *Executor) resolveWorkDir(dir string) string {
	if dir == "" || e.baseDir == "" || filepath.IsAbs(dir) {
		return dir
	}
	return filepath.Join(e.baseDir, dir)
}

var containerEnvBlocked = map[string]bool{
	"TMPDIR": true, "TMP": true, "TEMP": true,
	"HOME": true, "PWD": true, "OLDPWD": true, "SHELL": true,
	"USER": true, "LOGNAME": true, "SHLVL": true,
	"PATH": true, "SSH_AUTH_SOCK": true, "COMMAND_MODE": true,
	"NVM_DIR": true, "NVM_CD_FLAGS": true,
	"XPC_FLAGS": true, "XPC_SERVICE_NAME": true,
	"__CFBundleIdentifier": true, "__CF_USER_TEXT_ENCODING": true,
	"MallocNanoZone": true, "OSLogRateLimit": true,
	"MACH_PORT_RENDEZVOUS_PEER_VALDATION": true,
}

func containerForwardedEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if containerEnvBlocked[name] {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func (e *Executor) containerRuntime() string {
	if e.containerRT == "" {
		e.containerRT = "docker"
		if _, err := exec.LookPath("docker"); err != nil {
			if _, err := exec.LookPath("podman"); err == nil {
				e.containerRT = "podman"
			} else {
				e.containerRT = "none"
			}
		}
	}
	return e.containerRT
}

func (e *Executor) resolveContainer(image string) string {
	if image == "" {
		return ""
	}
	switch e.containerRuntime() {
	case "none":
		return image // reported as an error at run time
	default:
		return e.containerRT + " " + image
	}
}

func (e *Executor) shellArgsFor(ctx *execContext, script string) (argv []string, display string) {
	if ctx.container == "" {
		argv = append([]string{e.shellName}, slices.Clone(e.shellArgs)...)
		argv = append(argv, script)
		return argv, e.shellName + " " + strings.Join(argv[1:], " ")
	}
	fields := strings.Fields(ctx.container)
	if len(fields) != 2 {
		return nil, ctx.container
	}
	rt, image := fields[0], fields[1]
	if rt == "none" {
		return nil, image
	}
	argv = []string{rt, "run", "--rm"}
	if ctx.envFile != "" {
		argv = append(argv, "--env-file", ctx.envFile)
	}
	if e.baseDir != "" {
		if abs, err := filepath.Abs(e.baseDir); err == nil {
			argv = append(argv, "-v", abs+":/work", "-w", "/work")
		}
	}
	if ctx.workDir != "" && !filepath.IsAbs(ctx.workDir) {
		argv = append(argv, "-w", "/work/"+filepath.ToSlash(ctx.workDir))
	}
	argv = append(argv, image, "/bin/sh", "-c", script)
	return argv, rt + " run " + image + " /bin/sh -c " + script
}

func (e *Executor) EvaluateCommand(command *Command) error {
	return e.evaluate(command, "", false)
}

func (e *Executor) evaluate(command *Command, prereqDir string, isPrereq bool) error {
	if e.runs == nil {
		return e.executeCommand(command, prereqDir, isPrereq)
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

	r.err = e.executeCommand(command, prereqDir, isPrereq)
	close(r.done)
	return r.err
}

func (e *Executor) executeCommand(command *Command, prereqDir string, isPrereq bool) error {
	start := time.Now()
	workDir := command.WorkDir
	if prereqDir != "" {
		workDir = prereqDir
	}
	e.mu.Lock()
	if isPrereq {
		command.PrereqOutput = []string{}
	}
	e.mu.Unlock()

	resolveValue := func(s, scope string) string {
		s = resolveVarRefs(s, func(name string) (string, bool) {
			return e.StructuredParse.LookupVariable(name, scope)
		})
		return ResolveEnvRefs(s)
	}

	if len(command.FileDeps) > 0 && !isPrereq && len(command.Produces) == 0 && !e.noCache {
		if skip, reason := e.shouldSkip(command, resolveValue, workDir); skip {
			e.explainf("(%s cached: %s)\n", command.Name, reason)
			if !e.explain && !e.silentStatus {
				fmt.Printf("(%s cached)\n", command.Name)
			}
			rec := RunRecord{Status: "skipped", DurationMs: time.Since(start).Milliseconds(), End: time.Now()}
			e.recordRun(command.Name, rec)
			e.notifyFinish(command.Name, rec)
			return nil
		} else if e.explain && reason != "" {
			fmt.Printf("(%s running: %s)\n", command.Name, reason)
		}
	}
	if len(command.Produces) > 0 && !isPrereq && !e.noCache {
		if skip, reason := e.shouldSkipProduced(command, resolveValue, workDir); skip {
			e.explainf("(%s up to date: %s)\n", command.Name, reason)
			if !e.explain && !e.silentStatus {
				fmt.Printf("(%s up to date)\n", command.Name)
			}
			rec := RunRecord{Status: "skipped", DurationMs: time.Since(start).Milliseconds(), End: time.Now()}
			e.recordRun(command.Name, rec)
			e.notifyFinish(command.Name, rec)
			return nil
		} else if e.explain && reason != "" {
			fmt.Printf("(%s running: %s)\n", command.Name, reason)
		}
	}

	if len(command.FileDeps) > 0 && !isPrereq {
		for _, dep := range expandFileDeps(command.FileDeps, e.workDirFor(command, resolveValue, workDir)) {
			if _, err := os.Stat(dep); err != nil {
				fmt.Fprintf(os.Stderr, "warning: %s: file dependency %q does not exist\n", command.Name, dep)
			}
		}
	}

	ctx := &execContext{
		target:    command,
		isPrereq:  isPrereq,
		workDir:   workDir,
		srcFile:   command.SourceFile,
		container: e.resolveContainer(resolveValue(command.Container, command.Name)),
	}
	var ctxCancel context.CancelFunc
	if command.Timeout != "" {
		if d, err := time.ParseDuration(command.Timeout); err == nil {
			ctx.runCtx, ctxCancel = context.WithTimeout(e.effectiveRunCtx(ctx), d)
		}
	}
	if ctxCancel != nil {
		defer ctxCancel()
	}
	cmdEnv := slices.Clone(e.env)
	ctx.env = &cmdEnv
	if ctx.container != "" {
		if f, err := os.CreateTemp("", "construct-env-"); err == nil {
			for _, kv := range containerForwardedEnv(cmdEnv) {
				fmt.Fprintln(f, kv)
			}
			f.Close()
			ctx.envFile = f.Name()
			defer os.Remove(ctx.envFile)
		}
	}

	var prereqCmds []*Command
	var prereqDirs []string
	for _, prereqName := range command.Prereqs {
		prereqName = strings.TrimSpace(prereqName)
		if prereqName == "" {
			continue
		}

		preCmd, err := e.StructuredParse.GetCommand(prereqName)
		if err != nil {
			return err
		}

		prereqDirs = append(prereqDirs, command.PrereqDirs[prereqName])
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
			go func(i int, pc *Command, dir string) {
				defer wg.Done()
				errs[i] = e.evaluate(pc, dir, true)
				results[i] = pc
			}(i, preCmd, prereqDirs[i])
		}
		wg.Wait()
		for _, err := range errs {
			if err != nil {
				return err
			}
		}
		command.PrereqCmds = append(command.PrereqCmds, results...)
	} else {
		for i, preCmd := range prereqCmds {
			if err := e.evaluate(preCmd, prereqDirs[i], true); err != nil {
				return err
			}
			command.PrereqCmds = append(command.PrereqCmds, preCmd)
		}
	}

	body := e.cleanCommandBody(command, e.bodyFor(command))

	e.notifyStart(command.Name)

	if e.ghActions && !isPrereq && command.LazyEval == nil {
		fmt.Printf("::group::%s\n", command.Name)
	}

	bodyErr := e.timed(ctx, ctx.targetLabel(), func() error {
		return e.execBody(ctx, body)
	})

	if e.ghActions && !isPrereq && command.LazyEval == nil {
		fmt.Println("::endgroup::")
	}

	if bodyErr != nil {
		if e.ghActions && !isPrereq {
			ghErrorAnnotation(bodyErr)
		}
		rec := RunRecord{Status: "failed", Exit: exitCodeOfErr(bodyErr), DurationMs: time.Since(start).Milliseconds(), End: time.Now(), Error: bodyErr.Error()}
		e.recordRun(command.Name, rec)
		e.notifyFinish(command.Name, rec)
		return bodyErr
	}

	if len(command.Produces) > 0 && !isPrereq {
		for _, artifact := range expandFileDeps(command.Produces, e.workDirFor(command, resolveValue, workDir)) {
			if _, err := os.Stat(artifact); err != nil {
				fmt.Fprintf(os.Stderr, "warning: %s declares produces %q but it does not exist\n", command.Name, artifact)
			}
		}
	}

	if len(command.FileDeps) > 0 && !isPrereq && len(command.Produces) == 0 && !e.noCache {
		e.updateCache(command, resolveValue, workDir)
	}

	if e.timing && !isPrereq && command.LazyEval == nil && !e.silentStatus {
		fmt.Printf("(%s completed in %s)\n", command.Name, time.Since(start).Round(time.Millisecond))
	}

	rec := RunRecord{Status: "ok", DurationMs: time.Since(start).Milliseconds(), End: time.Now()}
	e.recordRun(command.Name, rec)
	e.notifyFinish(command.Name, rec)
	return nil
}

func ghErrorAnnotation(err error) {
	file, line := "", 0
	switch e := err.(type) {
	case *CommandError:
		file, line = e.File, e.Line
	case *FailError:
		file, line = e.File, e.Line
	}
	loc := ""
	if file != "" {
		if line > 0 {
			loc = fmt.Sprintf(" file=%s,line=%d", file, line)
		} else {
			loc = fmt.Sprintf(" file=%s", file)
		}
	}
	fmt.Fprintf(os.Stderr, "::error%s::%s\n", loc, err.Error())
}

func exitCodeOfErr(err error) int {
	if ce, ok := err.(*CommandError); ok {
		return ce.ExitCode
	}
	return 1
}

func (e *Executor) argFlags(cmd *Command) map[string]bool {
	flags := make(map[string]bool, len(cmd.Arguments))
	scope := cmd.flagScope()
	for _, arg := range cmd.Arguments {
		flags[scope+":"+arg.Name] = true
	}
	return flags
}

func (e *Executor) cleanCommandBody(cmd *Command, body []BodyStatement) []BodyStatement {
	if len(cmd.PrereqCmds) > 0 {
		for _, prereq := range cmd.PrereqCmds {
			for idx, arg := range prereq.PrereqOutput {
				varName := prereq.Name + "." + strconv.Itoa(idx)
				e.StructuredParse.SetVariable(strings.TrimSpace(varName), cmd.Name, strings.TrimSpace(arg))
			}

			for name, val := range prereq.NamedOutput {
				varName := prereq.Name + "." + name
				e.StructuredParse.SetVariable(varName, cmd.Name, strings.TrimSpace(val))
			}
		}
	}
	return e.cleanStatements(body, cmd, e.argFlags(cmd))
}

func (e *Executor) cleanStatements(stmts []BodyStatement, cmd *Command, argFlags map[string]bool) []BodyStatement {
	out := make([]BodyStatement, len(stmts))
	for i, stmt := range stmts {
		switch stmt.Type {
		case StmtIf:
			out[i] = BodyStatement{
				Type:       StmtIf,
				Cond:       stmt.Cond,
				ThenBody:   e.cleanStatements(stmt.ThenBody, cmd, argFlags),
				ElseBody:   e.cleanStatements(stmt.ElseBody, cmd, argFlags),
				SourceLine: stmt.SourceLine,
			}
		case StmtFor:
			out[i] = BodyStatement{
				Type:         StmtFor,
				LoopVar:      stmt.LoopVar,
				LoopIndex:    stmt.LoopIndex,
				LoopItems:    e.cleanLoopItems(cmd, stmt.LoopItems),
				LoopBody:     stmt.LoopBody,
				Parallel:     stmt.Parallel,
				ParallelJobs: stmt.ParallelJobs,
				SourceLine:   stmt.SourceLine,
			}
		case StmtSwitch:
			cases := make([]SwitchCase, len(stmt.Cases))
			for j, c := range stmt.Cases {
				cases[j] = SwitchCase{
					Values:     c.Values,
					IsDefault:  c.IsDefault,
					Body:       e.cleanStatements(c.Body, cmd, argFlags),
					SourceLine: c.SourceLine,
				}
			}
			out[i] = BodyStatement{
				Type:       StmtSwitch,
				SwitchExpr: stmt.SwitchExpr,
				Modifier:   stmt.Modifier,
				Cases:      cases,
				SourceLine: stmt.SourceLine,
			}
		case StmtInDir, StmtLock:
			out[i] = BodyStatement{
				Type:       stmt.Type,
				Shell:      stmt.Shell,
				Modifier:   stmt.Modifier,
				ThenBody:   e.cleanStatements(stmt.ThenBody, cmd, argFlags),
				SourceLine: stmt.SourceLine,
			}
		case StmtContinue, StmtBreak, StmtFail, StmtRequireEnv:
			out[i] = stmt
		case StmtInvoke, StmtEnv, StmtOnFail, StmtState, StmtConfirm, StmtPrompt, StmtInput, StmtBuiltin:
			out[i] = stmt
		default:
			out[i] = BodyStatement{Type: StmtShell, Shell: e.cleanShellLine(cmd, stmt.Shell, argFlags), OutputName: stmt.OutputName, Retry: stmt.Retry, Timeout: stmt.Timeout, Modifier: stmt.Modifier, SourceLine: stmt.SourceLine}
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
		if name == "last.exit" || name == "last.output" {
			return "", false // resolved lazily at execution time
		}
		v, ok := LookupVariableIndexed(e.StructuredParse, name, cmd.Name)
		if !ok {
			return "", false
		}
		return escapeShellValue(v.Joined()), true
	})

	for _, arg := range cmd.Arguments {
		lookupKey := cmd.flagScope() + ":" + arg.Name
		if !argFlags[lookupKey] {
			continue
		}

		if !strings.Contains(line, "&"+arg.Name) {
			continue
		}
		e.debugf("Handling argument --%s for command %s\n", arg.Name, cmd.Name)
		fs := e.flagSet
		if fs == nil {
			fs = pflag.CommandLine
		}

		v, _ := fs.GetString(lookupKey)
		line = strings.ReplaceAll(line, "&"+arg.Name, escapeShellValue(v))
	}

	return resolveEnvRefsKeepUnset(line)
}

func (e *Executor) expandOutputRefs(items, scope string) string {
	if !strings.Contains(items, ".*") {
		return items
	}
	var result strings.Builder
	i := 0
	for i < len(items) {
		if items[i] == '&' && i+1 < len(items) && isVarIdentByte(items[i+1]) {
			j := i + 1
			for j < len(items) && isVarIdentByte(items[j]) {
				j++
			}

			for j < len(items) && items[j] == '.' && j+1 < len(items) && isVarIdentByte(items[j+1]) {
				j++
				for j < len(items) && isVarIdentByte(items[j]) {
					j++
				}
			}

			if j+1 < len(items) && items[j] == '.' && items[j+1] == '*' {
				name := items[i+1 : j]
				if _, err := e.StructuredParse.GetCommand(name); err == nil {
					var outs []string
					for idx := 0; ; idx++ {
						val, ok := e.StructuredParse.LookupVariable(fmt.Sprintf("%s.%d", name, idx), scope)
						if !ok {
							break
						}
						outs = append(outs, val)
					}
					result.WriteString(strings.Join(outs, ", "))
					i = j + 2
					continue
				}
			}
		}
		result.WriteByte(items[i])
		i++
	}
	return result.String()
}

func isVarIdentByte(c byte) bool {
	return c == '_' || c == '-' ||
		(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func envLookupValue(env []string, name string) (string, bool) {
	prefix := name + "="
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return kv[len(prefix):], true
		}
	}
	return "", false
}

func setEnvVar(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			continue // replaced below
		}
		out = append(out, kv)
	}
	return append(out, prefix+value)
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

func isVarIdentRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-'
}

func isPlainRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

func scanRefs(s string, marker byte, firstSeg, dotSeg func(rune) bool, lookup func(string) (string, bool), fallbackFirst bool) string {
	var result strings.Builder
	result.Grow(len(s) + 16)
	runes := []rune(s)
	i := 0
	for i < len(runes) {
		if runes[i] == '\\' && i+1 < len(runes) && runes[i+1] == rune(marker) {
			result.WriteRune(runes[i+1])
			i += 2
			continue
		}
		if runes[i] == rune(marker) {
			j := i + 1
			firstStart := j
			for j < len(runes) && firstSeg(runes[j]) {
				j++
			}
			if j > firstStart {
				firstEnd := j // first segment ends here, before any dots
				if dotSeg != nil {
					for j < len(runes) && runes[j] == '.' && j+1 < len(runes) && dotSeg(runes[j+1]) {
						j++
						for j < len(runes) && dotSeg(runes[j]) {
							j++
						}
					}
				}

				if marker == '@' && j+1 < len(runes) && runes[j] == ':' && runes[j+1] == '-' {
					j += 2
					for j < len(runes) && !isEnvDefaultEnd(runes[j]) {
						j++
					}
				}
				if val, ok := lookup(string(runes[firstStart:j])); ok {
					result.WriteString(val)
					i = j
					continue
				}
				if fallbackFirst && j > firstEnd {
					if val, ok := lookup(string(runes[firstStart:firstEnd])); ok {
						result.WriteString(val)
						i = firstEnd
						continue
					}
				}
			}
		}
		result.WriteRune(runes[i])
		i++
	}
	return result.String()
}

func resolveVarRefs(line string, lookup func(string) (string, bool)) string {
	if strings.IndexByte(line, '&') < 0 {
		return line
	}
	return scanRefs(line, '&', isVarIdentRune, isPlainRune, lookup, true)
}

func VarRefNames(s string) []string {
	if strings.IndexByte(s, '&') < 0 {
		return nil
	}
	var names []string
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		if runes[i] != '&' {
			continue
		}
		j := i + 1
		for j < len(runes) && isVarIdentRune(runes[j]) {
			j++
		}
		if j == i+1 {
			continue
		}
		for j < len(runes) && runes[j] == '.' && j+1 < len(runes) && isPlainRune(runes[j+1]) {
			j++
			for j < len(runes) && isPlainRune(runes[j]) {
				j++
			}
		}
		names = append(names, string(runes[i+1:j]))
		i = j - 1
	}
	return names
}

// isEnvDefaultEnd reports characters that terminate an @ENV:-default value.
func isEnvDefaultEnd(r rune) bool {
	switch r {
	case ' ', '\t', '\r', '\n', '"', '\'', ',', ';', '&', '@', '$':
		return true
	}
	return false
}

func splitEnvRefToken(token string) (name, def string, hasDefault bool) {
	if before, after, ok := strings.Cut(token, ":-"); ok {
		return before, after, true
	}
	return token, "", false
}

func ResolveEnvRefs(s string) string {
	if strings.IndexByte(s, '@') < 0 {
		return s
	}
	return scanRefs(s, '@', isPlainRune, nil, func(token string) (string, bool) {
		name, def, hasDefault := splitEnvRefToken(token)
		if val, ok := os.LookupEnv(name); ok {
			return val, true
		}
		if hasDefault {
			return def, true
		}
		return "", true
	}, false)
}

func resolveEnvRefsWith(s string, lookup func(string) string) string {
	if strings.IndexByte(s, '@') < 0 {
		return s
	}
	return scanRefs(s, '@', isPlainRune, nil, func(token string) (string, bool) {
		name, def, hasDefault := splitEnvRefToken(token)
		if val := lookup(name); val != "" {
			return val, true
		}
		if hasDefault {
			return def, true
		}
		return "", true
	}, false)
}

func resolveEnvRefsKeepUnset(s string) string {
	return resolveEnvRefsKeepUnsetWith(s, os.LookupEnv)
}

func resolveEnvRefsKeepUnsetWith(s string, lookup func(string) (string, bool)) string {
	if strings.IndexByte(s, '@') < 0 {
		return s
	}
	return scanRefs(s, '@', isPlainRune, nil, func(token string) (string, bool) {
		name, def, hasDefault := splitEnvRefToken(token)
		if val, ok := lookup(name); ok {
			return val, true
		}
		if hasDefault {
			return def, true
		}
		return "", false
	}, false)
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

var conditionOps = []string{"==", "!=", ">=", "<=", ">", "<"}

func evaluateCondition(cond string) bool {
	return evaluateConditionWithBase(cond, "")
}

func evaluateConditionWithBase(cond, base string) bool {
	cond = strings.TrimSpace(cond)

	if strings.HasPrefix(cond, "(") && strings.HasSuffix(cond, ")") {
		return evaluateConditionWithBase(strings.TrimSpace(cond[1:len(cond)-1]), base)
	}

	if idx := findTopLevelOp(cond, "||"); idx >= 0 {
		return evaluateConditionWithBase(cond[:idx], base) || evaluateConditionWithBase(cond[idx+2:], base)
	}
	if idx := findTopLevelOp(cond, "&&"); idx >= 0 {
		return evaluateConditionWithBase(cond[:idx], base) && evaluateConditionWithBase(cond[idx+2:], base)
	}

	if strings.HasPrefix(cond, "!") {
		rest := strings.TrimSpace(cond[1:])
		return rest != "" && !evaluateConditionWithBase(rest, base)
	}

	if result, ok := evalBuiltinCondition(cond, base); ok {
		return result
	}

	if idx := findTopLevelOp(cond, " contains "); idx > 0 {
		left := strings.TrimSpace(cond[:idx])
		right := strings.TrimSpace(cond[idx+len(" contains "):])
		left = strings.Trim(left, "\"")
		right = strings.Trim(right, "\"")
		return strings.Contains(left, right)
	}

	if idx := findTopLevelOp(cond, " starts_with "); idx > 0 {
		left := strings.Trim(strings.TrimSpace(cond[:idx]), "\"")
		right := strings.Trim(strings.TrimSpace(cond[idx+len(" starts_with "):]), "\"")
		return strings.HasPrefix(left, right)
	}

	if idx := findTopLevelOp(cond, " ends_with "); idx > 0 {
		left := strings.Trim(strings.TrimSpace(cond[:idx]), "\"")
		right := strings.Trim(strings.TrimSpace(cond[idx+len(" ends_with "):]), "\"")
		return strings.HasSuffix(left, right)
	}

	if idx := findTopLevelOp(cond, " matches "); idx > 0 {
		left := strings.Trim(strings.TrimSpace(cond[:idx]), "\"")
		pattern := strings.Trim(strings.TrimSpace(cond[idx+len(" matches "):]), "\"")
		matched, _ := regexp.MatchString(pattern, left)
		return matched
	}

	if idx := findTopLevelOp(cond, " in "); idx > 0 {
		left := strings.Trim(strings.TrimSpace(cond[:idx]), "\"")
		list := strings.Trim(strings.TrimSpace(cond[idx+len(" in "):]), "\"")
		for item := range strings.SplitSeq(list, ",") {
			if left == strings.TrimSpace(item) {
				return true
			}
		}
		return false
	}

	ops := conditionOps
	for _, op := range ops {
		if idx := findTopLevelOp(cond, op); idx > 0 {
			left := strings.TrimSpace(cond[:idx])
			right := strings.TrimSpace(cond[idx+len(op):])
			left = strings.Trim(left, "\"")
			right = strings.Trim(right, "\"")
			return compareValues(left, right, op)
		}
	}
	return false
}

func evalBuiltinCondition(cond, base string) (bool, bool) {
	open := strings.IndexByte(cond, '(')
	if open <= 0 || !strings.HasSuffix(cond, ")") {
		return false, false
	}
	name := strings.TrimSpace(cond[:open])
	arg := strings.Trim(strings.TrimSpace(cond[open+1:len(cond)-1]), `"`)
	if arg == "" {
		return false, false
	}
	if base != "" && !filepath.IsAbs(arg) {
		arg = filepath.Join(base, arg)
	}
	switch name {
	case "exists":
		_, err := os.Stat(arg)
		return err == nil, true
	case "missing":
		_, err := os.Stat(arg)
		return err != nil, true
	case "glob":
		matches, _ := filepath.Glob(arg)
		return len(matches) > 0, true
	case "require":
		_, err := exec.LookPath(arg)
		return err == nil, true
	}
	return false, false
}

func LoadEnvFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"' {
			val = val[1 : len(val)-1]
		}
		if key == "" {
			continue
		}
		if _, exists := os.LookupEnv(key); !exists {
			os.Setenv(key, val)
		}
	}
	return nil
}

func compareValues(left, right, op string) bool {
	if li, err := strconv.Atoi(left); err == nil {
		if ri, err := strconv.Atoi(right); err == nil {
			return compare(li, ri, op)
		}
	}
	return compare(left, right, op)
}

func compare[T ~int | ~string](l, r T, op string) bool {
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

func (e *Executor) bodyFor(cmd *Command) []BodyStatement {
	if !cmd.CloudAccessible {
		return cmd.Body
	}
	external, err := e.getCloudDefinition(cmd.Name)
	if err != nil || external == nil {
		return cmd.Body
	}
	return append(slices.Clone(cmd.Body), external.Body...)
}

type execContext struct {
	target      *Command
	isPrereq    bool
	workDir     string
	container   string
	envFile     string
	srcFile     string
	out         io.Writer
	env         *[]string
	runCtx      context.Context
	depth       int // nesting depth for the --flame report
	onFails     []BodyStatement
	onFailRun   bool
	forcePrefix bool // per-iteration output prefixing for parallel loops
}

func (ctx *execContext) targetLabel() string {
	if ctx.target != nil && ctx.target.LazyEval != nil {
		return ctx.target.LazyEval.VarName + " (lazy)"
	}
	return ctx.target.Name
}

func (e *Executor) timed(ctx *execContext, label string, fn func() error) error {
	if !e.flame {
		return fn()
	}
	start := time.Now()
	err := fn()
	e.mu.Lock()
	e.flameRows = append(e.flameRows, FlameRow{
		Label:  label,
		Start:  start,
		End:    time.Now(),
		Failed: err != nil,
		Depth:  ctx.depth,
	})
	e.mu.Unlock()
	return err
}

func (e *Executor) setLastResult(ctx *execContext, exit int, output string) {
	e.StructuredParse.SetVariable("last.exit", ctx.target.Name, strconv.Itoa(exit))
	e.StructuredParse.SetVariable("last.output", ctx.target.Name, strings.TrimSpace(output))
}

func (e *Executor) resolveLastRefs(s, scope string) string {
	if !strings.Contains(s, "&last.") {
		return s
	}
	for _, n := range []string{"last.exit", "last.output"} {
		if v, ok := e.StructuredParse.LookupVariable(n, scope); ok {
			s = strings.ReplaceAll(s, "&"+n, escapeShellValue(v))
		}
	}
	return s
}

func termIsTTY(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}

func supportsPipefail(shell string) bool {
	switch path.Base(strings.ReplaceAll(shell, "\\", "/")) {
	case "bash", "zsh", "bash.exe":
		return true
	}
	return false
}

func (e *Executor) resolveBodyValue(ctx *execContext, s, scope string) string {
	s = resolveVarRefs(s, func(name string) (string, bool) {
		v, ok := LookupVariableIndexed(e.StructuredParse, name, scope)
		if !ok {
			return "", false
		}
		if v.IsList {
			return v.String(), true
		}
		return v.S, true
	})
	s = resolveStateRefsWith(s, e.stateLookup)
	return resolveEnvRefsWith(s, func(name string) string {
		if v, ok := envLookupValue(*ctx.env, name); ok {
			return v
		}
		return os.Getenv(name)
	})
}

func (e *Executor) resolveBodyEnvRef(ctx *execContext, s string) string {
	s = resolveStateRefsWith(s, e.stateLookup)
	return resolveEnvRefsKeepUnsetWith(s, func(name string) (string, bool) {
		if v, ok := envLookupValue(*ctx.env, name); ok {
			return v, true
		}
		return os.LookupEnv(name)
	})
}

type executorEvalContext struct {
	e     *Executor
	ctx   *execContext
	scope string
}

func (c executorEvalContext) LookupVar(name string) (Value, bool) {
	return LookupVariableIndexed(c.e.StructuredParse, name, c.scope)
}

func (c executorEvalContext) LookupEnv(name string) (string, bool) {
	if v, ok := envLookupValue(*c.ctx.env, name); ok {
		return v, true
	}
	return os.LookupEnv(name)
}

func (c executorEvalContext) LookupState(name string) (string, bool) {
	return c.e.stateLookup(name)
}

func (c executorEvalContext) BaseDir() string {
	if c.e.baseDir != "" {
		return c.e.baseDir
	}
	return "."
}

func (e *Executor) execBody(ctx *execContext, body []BodyStatement) (err error) {
	defer func() {
		if err != nil && !ctx.onFailRun &&
			!errors.Is(err, errLoopContinue) && !errors.Is(err, errLoopBreak) {
			ctx.onFailRun = true
			err = e.runOnFails(ctx, err)
		}
	}()

	condBase := e.baseDir
	if ctx.workDir != "" {
		condBase = e.resolveWorkDir(e.resolveBodyValue(ctx, ctx.workDir, ctx.target.Name))
	}
	for i := 0; i < len(body); i++ {
		stmt := body[i]
		switch stmt.Type {
		case StmtEnv:
			for _, pair := range stmt.Env {
				key, value, _ := strings.Cut(pair, "=")
				value = resolveVarRefs(value, func(name string) (string, bool) {
					return e.StructuredParse.LookupVariable(name, ctx.target.Name)
				})

				resolved := e.resolveBodyEnvRef(ctx, value)
				*ctx.env = setEnvVar(*ctx.env, key, resolved)
				e.StructuredParse.SetVariable(key, ctx.target.Name, resolved)
				e.debugf("env %s=%s\n", key, resolved)
			}

			rest := e.cleanStatements(body[i+1:], ctx.target, e.argFlags(ctx.target))
			body = append(body[:i+1], rest...)

		case StmtIf:
			cond := e.resolveBodyValue(ctx, stmt.Cond, ctx.target.Name)
			e.debugf("Evaluating condition: %s\n", cond)
			err := e.timed(ctx, truncateLabel(ctx.targetLabel()+": if "+stmt.Cond), func() error {
				if evaluateConditionWithBase(cond, condBase) {
					if err := e.execBody(ctx, stmt.ThenBody); err != nil {
						return err
					}
				} else if stmt.ElseBody != nil {
					if err := e.execBody(ctx, stmt.ElseBody); err != nil {
						return err
					}
				}
				return nil
			})
			if err != nil {
				return err
			}

		case StmtSwitch:
			expr := strings.Trim(e.resolveBodyValue(ctx, stmt.SwitchExpr, ctx.target.Name), `"`)
			err := e.timed(ctx, truncateLabel(ctx.targetLabel()+": switch "+stmt.SwitchExpr), func() error {
				for _, c := range stmt.Cases {
					if c.IsDefault {
						continue
					}
					for _, v := range c.Values {
						if e.resolveBodyValue(ctx, v, ctx.target.Name) == expr {
							return e.execBody(ctx, c.Body)
						}
					}
				}
				for _, c := range stmt.Cases {
					if c.IsDefault {
						return e.execBody(ctx, c.Body)
					}
				}
				if stmt.Modifier == "strict" {
					return &FailError{Message: fmt.Sprintf("strict switch: no case matched %q", expr), File: ctx.srcFile, Line: stmt.SourceLine}
				}
				return nil
			})
			if err != nil {
				return err
			}

		case StmtInDir:
			dir := e.resolveBodyValue(ctx, stmt.Shell, ctx.target.Name)
			err := e.timed(ctx, truncateLabel(ctx.targetLabel()+": in "+dir), func() error {
				sub := *ctx
				sub.workDir = dir
				sub.depth = ctx.depth + 1
				if e.baseDir != "" {
					if full := e.resolveWorkDir(dir); full != "" {
						_ = os.MkdirAll(full, 0755)
					}
				}
				return e.execBody(&sub, stmt.ThenBody)
			})
			if err != nil {
				return err
			}

		case StmtLock:
			name := e.resolveBodyValue(ctx, stmt.Shell, ctx.target.Name)
			var maxWait time.Duration
			if stmt.Modifier != "" {
				maxWait, _ = time.ParseDuration(stmt.Modifier)
			}
			err := e.timed(ctx, truncateLabel(ctx.targetLabel()+": lock "+name), func() error {
				return e.withLock(ctx, name, maxWait, func() error {
					return e.execBody(ctx, stmt.ThenBody)
				})
			})
			if err != nil {
				return err
			}

		case StmtState:
			raw := e.resolveBodyValue(ctx, stmt.Message, ctx.target.Name)
			value := raw
			if v, ok, err := evalValueExpr(raw, executorEvalContext{e: e, ctx: ctx, scope: ctx.target.Name}); ok && err == nil {
				value = v.S
			}
			value = trimQuoted(value)
			e.setRuntimeState(stmt.Shell, value)
			e.StructuredParse.SetVariable(stmt.Shell, ctx.target.Name, value)
			e.debugf("state %s=%s\n", stmt.Shell, value)
			// Re-clean the remainder so &name refs pick up the new value.
			rest := e.cleanStatements(body[i+1:], ctx.target, e.argFlags(ctx.target))
			body = append(body[:i+1], rest...)

		case StmtConfirm:
			if !e.yes {
				if !termIsTTY(os.Stdin) {
					return &FailError{Message: fmt.Sprintf("confirm \"%s\" aborted (stdin is not a terminal; pass --yes to approve)", stmt.Message), File: ctx.srcFile, Line: stmt.SourceLine}
				}
				fmt.Printf("%s [y/N]: ", stmt.Message)
				line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
				line = strings.TrimSpace(line)
				if !strings.EqualFold(line, "y") && !strings.EqualFold(line, "yes") {
					return &FailError{Message: "aborted by user: " + stmt.Message, File: ctx.srcFile, Line: stmt.SourceLine}
				}
			} else {
				e.debugf("confirm %q auto-approved (--yes)\n", stmt.Message)
			}

		case StmtPrompt:
			if !e.yes && termIsTTY(os.Stdin) {
				fmt.Printf("%s [press Enter to continue]: ", stmt.Message)
				_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
			} else {
				fmt.Println(stmt.Message)
			}

		case StmtInput:
			fmt.Printf("%s", stmt.Message)
			if stmt.Message != "" {
				fmt.Print(" ")
			}
			line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
			line = strings.TrimSpace(line)
			e.StructuredParse.SetVariable(stmt.Shell, ctx.target.Name, line)
			e.debugf("input %s=%q\n", stmt.Shell, line)
			// Re-clean the remainder so &name refs pick up the input.
			rest := e.cleanStatements(body[i+1:], ctx.target, e.argFlags(ctx.target))
			body = append(body[:i+1], rest...)

		case StmtBuiltin:
			if err := e.timed(ctx, truncateLabel(ctx.targetLabel()+": "+stmt.Shell+" "+stmt.BuiltinArgs), func() error {
				return e.runBuiltin(ctx, stmt)
			}); err != nil {
				return err
			}

		case StmtInvoke:
			if err := e.timed(ctx, truncateLabel(ctx.targetLabel()+": invoke "+stmt.Shell), func() error {
				return e.invokeCommand(ctx, stmt)
			}); err != nil {
				return err
			}

			if stmt.OutputName != "" {
				rest := e.cleanStatements(body[i+1:], ctx.target, e.argFlags(ctx.target))
				body = append(body[:i+1], rest...)
			}

		case StmtFor:
			items := e.resolveBodyValue(ctx, stmt.LoopItems, ctx.target.Name)
			items = e.expandOutputRefs(items, ctx.target.Name)
			if items == "" {
				continue
			}
			var expanded []string
			if v, ok, err := evalValueExpr(items, executorEvalContext{e: e, ctx: ctx, scope: ctx.target.Name}); ok && err == nil && v.IsList {
				expanded = v.L
			} else {
				expanded = e.expandLoopItems(ctx, items)
			}
			argFlags := e.argFlags(ctx.target)

			if stmt.Parallel {
				if err := e.execParallelFor(ctx, stmt, expanded, argFlags); err != nil {
					return err
				}
				continue
			}

		iterLoop:
			for idx, item := range expanded {
				e.StructuredParse.SetVariable(stmt.LoopVar, ctx.target.Name, item)
				if stmt.LoopIndex != "" {
					e.StructuredParse.SetVariable(stmt.LoopIndex, ctx.target.Name, strconv.Itoa(idx))
				}
				e.debugf("For loop %s = %s\n", stmt.LoopVar, item)
				cleaned := e.cleanStatements(stmt.LoopBody, ctx.target, argFlags)
				err := e.execBody(ctx, cleaned)
				switch {
				case errors.Is(err, errLoopContinue):
					continue
				case errors.Is(err, errLoopBreak):
					break iterLoop
				case err != nil:
					return err
				}
			}

		case StmtContinue:
			return errLoopContinue
		case StmtBreak:
			return errLoopBreak

		case StmtFail:
			return &FailError{Message: stmt.Message, File: ctx.srcFile, Line: stmt.SourceLine}

		case StmtRequireEnv:
			if !envIsSet(ctx, stmt.Shell) {
				msg := fmt.Sprintf("required environment variable %s is not set", stmt.Shell)
				if stmt.Message != "" {
					msg += ": " + stmt.Message
				}
				return &FailError{Message: msg, File: ctx.srcFile, Line: stmt.SourceLine}
			}

		case StmtOnFail:
			ctx.onFails = append(ctx.onFails, stmt.OnFailBody...)

		default:
			if e.streaming(ctx) {
				end := i
				for end < len(body) && body[end].Type == StmtShell && body[end].Retry == 0 && body[end].Timeout == "" &&
					!strings.HasPrefix(body[end].Shell, "!") &&
					!strings.Contains(body[end].Shell, "&last.") {
					end++
				}
				if end > i {
					if err := e.timed(ctx, truncateLabel(ctx.targetLabel()+": batch"), func() error {
						return e.runShellBatch(ctx, body[i:end])
					}); err != nil {
						return err
					}
					i = end - 1
					continue
				}
			}
			if err := e.timed(ctx, truncateLabel(ctx.targetLabel()+": "+stmt.Shell), func() error {
				return e.runShell(ctx, stmt)
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (e *Executor) execParallelFor(ctx *execContext, stmt BodyStatement, items []string, argFlags map[string]bool) error {
	limit := stmt.ParallelJobs
	if limit <= 0 {
		limit = e.jobs
	}
	if limit <= 0 {
		limit = runtime.NumCPU()
	}
	limit = min(limit, len(items))
	if limit < 1 {
		return nil
	}

	snapshot := e.StructuredParse.SnapshotScope(ctx.target.Name)
	dupes := make(map[string]int, len(items))
	for _, it := range items {
		dupes[it]++
	}

	errs := make([]error, len(items))
	gate := make(chan struct{}, limit)
	var wg sync.WaitGroup
	var stop atomic.Bool

	for idx, item := range items {
		if stop.Load() {
			break
		}
		gate <- struct{}{}
		wg.Add(1)
		go func(idx int, item string) {
			defer wg.Done()
			defer func() { <-gate }()
			err := e.runParallelIteration(ctx, stmt, item, idx, snapshot, argFlags, dupes[item] > 1)
			switch {
			case errors.Is(err, errLoopContinue):
			case errors.Is(err, errLoopBreak):
				errs[idx] = fmt.Errorf("break is not supported inside a parallel loop (%s:%d)", ctx.srcFile, stmt.SourceLine)
				stop.Store(true)
			case err != nil:
				errs[idx] = err
				stop.Store(true)
			}
		}(idx, item)
	}
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func (e *Executor) runParallelIteration(ctx *execContext, stmt BodyStatement, item string, idx int, snapshot []*Variable, argFlags map[string]bool, qualify bool) error {
	scope := ctx.target.Name + "/" + item
	if qualify {
		scope = fmt.Sprintf("%s/%s#%d", ctx.target.Name, item, idx)
	}
	iterCmd := *ctx.target
	iterCmd.Name = scope
	e.StructuredParse.SeedScope(scope, snapshot)
	e.StructuredParse.SetVariable(stmt.LoopVar, scope, item)
	if stmt.LoopIndex != "" {
		e.StructuredParse.SetVariable(stmt.LoopIndex, scope, strconv.Itoa(idx))
	}
	e.debugf("Parallel loop %s = %s\n", stmt.LoopVar, item)

	sub := *ctx
	sub.target = &iterCmd
	envCopy := slices.Clone(*ctx.env)
	sub.env = &envCopy
	sub.onFails = nil
	sub.forcePrefix = true

	cleaned := e.cleanStatements(stmt.LoopBody, &iterCmd, argFlags)
	return e.execBody(&sub, cleaned)
}

func truncateLabel(s string) string {
	if utf8.RuneCountInString(s) > 60 {
		return string([]rune(s)[:57]) + "..."
	}
	return s
}

func (e *Executor) runOnFails(ctx *execContext, cause error) error {
	snapshot := ctx.onFails
	ctx.onFails = nil
	savedCtx := ctx.runCtx
	ctx.runCtx = context.Background()
	defer func() { ctx.runCtx = savedCtx }()

	e.StructuredParse.SetVariable("fail.message", ctx.target.Name, cause.Error())
	e.StructuredParse.SetVariable("fail.line", ctx.target.Name, strconv.Itoa(failLine(cause)))
	if cmdErr, ok := cause.(*CommandError); ok {
		e.StructuredParse.SetVariable("fail.exit", ctx.target.Name, strconv.Itoa(cmdErr.ExitCode))
	}

	for _, body := range snapshot {
		cleaned := e.cleanStatements([]BodyStatement{body}, ctx.target, e.argFlags(ctx.target))
		if err := e.execBody(ctx, cleaned); err != nil {
			fmt.Fprintf(os.Stderr, "onfail error: %v\n", err)
		}
	}
	return cause
}

func failLine(err error) int {
	switch e := err.(type) {
	case *CommandError:
		return e.Line
	case *FailError:
		return e.Line
	}
	return 0
}

func (e *Executor) invokeCommand(ctx *execContext, stmt BodyStatement) error {
	invoked, err := e.StructuredParse.GetCommand(strings.TrimSpace(stmt.Shell))
	if err != nil {
		if def, cerr := e.getCloudDefinition(strings.TrimSpace(stmt.Shell)); cerr == nil {
			invoked = &Command{Name: def.Name, Body: def.Body, Arguments: def.Arguments}
		} else {
			return err
		}
	}
	if e.invokeDepth == nil {
		e.invokeDepth = make(map[string]int)
	}
	e.mu.Lock()
	if e.invokeDepth[invoked.Name] > 0 {
		e.mu.Unlock()
		return fmt.Errorf("circular invoke of command '%s'", invoked.Name)
	}
	e.invokeDepth[invoked.Name]++
	e.mu.Unlock()

	sub := *ctx // invoke bodies run in the caller's context
	sub.srcFile = invoked.SourceFile
	sub.out = nil
	sub.depth = ctx.depth + 1 // one level deeper in the --flame report

	passed := make(map[string]bool)
	for _, pair := range stmt.InvokeArgs {
		key, val, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.Trim(strings.TrimSpace(val), `"`)
		e.StructuredParse.SetVariable(key, ctx.target.Name, val)
		passed[key] = true
	}
	for _, arg := range invoked.Arguments {
		if !passed[arg.Name] {
			e.StructuredParse.SetVariable(arg.Name, ctx.target.Name, strings.Trim(arg.Default, `"`))
		}
	}

	cleaned := e.cleanStatements(e.bodyFor(invoked), ctx.target, e.argFlags(ctx.target))

	var invokeErr error
	if stmt.OutputName != "" {
		var buf bytes.Buffer
		sub.out = &buf
		invokeErr = e.execBody(&sub, cleaned)
		if invokeErr == nil {
			e.StructuredParse.SetVariable(stmt.OutputName, ctx.target.Name, strings.TrimSpace(buf.String()))
			e.debugf("invoke %s captured %d bytes\n", invoked.Name, buf.Len())
		}
	} else {
		invokeErr = e.execBody(&sub, cleaned)
	}

	e.mu.Lock()
	e.invokeDepth[invoked.Name]--
	e.mu.Unlock()
	return invokeErr
}

func (e *Executor) expandLoopItems(ctx *execContext, items string) []string {
	var expanded []string
	if strings.ContainsAny(items, "*?") {
		wd := "."
		if ctx.workDir != "" {
			wd = e.resolveWorkDir(e.resolveBodyValue(ctx, ctx.workDir, ctx.target.Name))
		} else if e.baseDir != "" {
			wd = e.baseDir
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
		return expanded
	}
	if rng, ok := expandRange(items); ok {
		return rng
	}
	for item := range strings.SplitSeq(items, ",") {
		expanded = append(expanded, strings.TrimSpace(item))
	}
	return expanded
}

func envIsSet(ctx *execContext, name string) bool {
	if _, ok := envLookupValue(*ctx.env, name); ok {
		return true
	}
	_, ok := os.LookupEnv(name)
	return ok
}

func (e *Executor) cleanLoopItems(cmd *Command, line string) string {
	line = strings.TrimSpace(line)
	if line == "" {
		return ""
	}
	if len(line) > 0 && line[0] == '$' {
		line = strings.TrimSpace(line[1:])
	}
	line = resolveVarRefs(line, func(name string) (string, bool) {
		v, ok := LookupVariableIndexed(e.StructuredParse, name, cmd.Name)
		if !ok {
			return "", false
		}
		return v.String(), true
	})
	return resolveEnvRefsKeepUnset(line)
}

func (e *Executor) streaming(ctx *execContext) bool {
	return !e.debug && !ctx.isPrereq && ctx.target.LazyEval == nil && ctx.out == nil
}

func (e *Executor) runShellBatch(ctx *execContext, stmts []BodyStatement) error {
	type group struct {
		lines    []string
		strict   bool
		sourceLn int
	}
	var groups []*group
	cur := &group{strict: !strings.HasPrefix(stmts[0].Shell, "!"), sourceLn: stmts[0].SourceLine}

	for _, stmt := range stmts {
		cmdLine := stmt.Shell
		if cmdLine == "" {
			continue
		}
		tolerant := strings.HasPrefix(cmdLine, "!")
		if tolerant {
			cmdLine = strings.TrimSpace(cmdLine[1:])
		}
		cmdLine = e.resolveBodyEnvRef(ctx, cmdLine)
		cmdLine = strings.TrimSpace(strings.TrimPrefix(cmdLine, "$"))
		if supportsPipefail(e.shellName) {
			cmdLine = "set -o pipefail; " + cmdLine
		}
		if tolerant {
			cmdLine = "( " + cmdLine + " ) || true"
		} else {
			cmdLine = "( " + cmdLine + " )"
		}
		if cur.strict == tolerant { // tolerance changed: start a new group
			groups = append(groups, cur)
			cur = &group{strict: !tolerant, sourceLn: stmt.SourceLine}
		}
		cur.lines = append(cur.lines, cmdLine)
	}
	groups = append(groups, cur)

	for _, g := range groups {
		if len(g.lines) == 0 {
			continue
		}
		script := strings.Join(g.lines, "\n")
		if g.strict {
			script = "set -e\n" + script
		}
		if supportsPipefail(e.shellName) {
			script = "set -o pipefail\n" + script
		}
		argv, fullCommand := e.shellArgsFor(ctx, script)
		if argv == nil {
			return fmt.Errorf("command %q needs container runtime but neither docker nor podman was found", ctx.target.Name)
		}
		cmd := e.command(ctx, argv)

		var buf bytes.Buffer
		sink := io.Writer(e.outSink())
		if e.quiet {
			sink = io.Discard
		} else if oc, ok := e.observer.(OutputCollector); ok {
			sink = oc.OutputWriter(ctx.target.Name)
		} else if e.prefixOutput || ctx.forcePrefix {
			sink = &linePrefixWriter{w: e.outSink(), prefix: "[" + ctx.target.Name + "] "}
		}
		cmd.Stdout = io.MultiWriter(sink, &buf)
		cmd.Stderr = e.errSinkFor(ctx)

		e.debugf("Running command %s (batched): %s\n", ctx.target.Name, fullCommand)

		release := e.acquire()
		err := cmd.Run()
		release()
		if pw, ok := sink.(*linePrefixWriter); ok {
			pw.flush()
		}
		e.setLastResult(ctx, exitCodeOf(err), buf.String())
		if err != nil {
			return e.commandError(fullCommand, ctx, BodyStatement{SourceLine: g.sourceLn}, err, "")
		}
	}
	return nil
}

type linePrefixWriter struct {
	mu     sync.Mutex
	w      io.Writer
	prefix string
	buf    []byte
}

func (p *linePrefixWriter) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.buf = append(p.buf, b...)
	for {
		idx := bytes.IndexByte(p.buf, '\n')
		if idx < 0 {
			break
		}
		line := p.buf[:idx+1]
		p.w.Write(append([]byte(p.prefix), line...))
		p.buf = p.buf[idx+1:]
	}
	return len(b), nil
}

func (p *linePrefixWriter) flush() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.buf) > 0 {
		p.w.Write(append([]byte(p.prefix), p.buf...))
		p.buf = nil
	}
}

func (e *Executor) runShell(ctx *execContext, stmt BodyStatement) error {
	backoff, _ := time.ParseDuration(stmt.Modifier)
	var lastErr error
	for attempt := 0; ; attempt++ {
		lastErr = e.runShellOnce(ctx, stmt)
		if lastErr == nil || attempt >= stmt.Retry {
			return lastErr
		}
		if backoff > 0 {
			wait := backoff * time.Duration(1<<attempt)
			fmt.Fprintf(os.Stderr, "(!) %s failed, retrying in %s (attempt %d/%d)\n", stmt.Shell, wait, attempt+1, stmt.Retry+1)
			select {
			case <-e.effectiveRunCtx(ctx).Done():
				return e.effectiveRunCtx(ctx).Err()
			case <-time.After(wait):
			}
		} else {
			fmt.Fprintf(os.Stderr, "(!) %s failed, retrying (attempt %d/%d)\n", stmt.Shell, attempt+1, stmt.Retry+1)
		}
	}
}

func (e *Executor) runShellOnce(ctx *execContext, stmt BodyStatement) error {
	cmdLine := stmt.Shell
	if cmdLine == "" {
		return nil
	}

	ignoreErr := false
	if strings.HasPrefix(cmdLine, "!") {
		ignoreErr = true
		cmdLine = strings.TrimSpace(cmdLine[1:])
	}

	cmdLine = e.resolveLastRefs(cmdLine, ctx.target.Name)
	cmdLine = e.resolveBodyEnvRef(ctx, cmdLine)
	cmdLine = strings.TrimSpace(strings.TrimPrefix(cmdLine, "$"))
	if supportsPipefail(e.shellName) {
		cmdLine = "set -o pipefail\n" + cmdLine
	}

	stmtCtx, cancel := e.statementCtx(ctx, stmt.Timeout)
	defer cancel()
	argv, fullCommand := e.shellArgsFor(ctx, cmdLine)
	if argv == nil {
		return fmt.Errorf("command %q needs container runtime but neither docker nor podman was found", ctx.target.Name)
	}
	cmd := e.command(stmtCtx, argv)
	if e.debug {
		switch {
		case ctx.isPrereq:
			e.debugf("Running prerequisite %s: %s\n", ctx.target.Name, fullCommand)
		case ctx.target.LazyEval != nil:
			e.debugf("Running lazy command for variable %s: %s\n", ctx.target.LazyEval.VarName, fullCommand)
		default:
			e.debugf("Running command %s: %s\n", ctx.target.Name, fullCommand)
		}
	}

	release := e.acquire()
	stream := !e.debug && !ctx.isPrereq && ctx.target.LazyEval == nil && ctx.out == nil
	if stream {
		var buf bytes.Buffer
		sink := io.Writer(e.outSink())
		if e.quiet {
			sink = io.Discard
		} else if oc, ok := e.observer.(OutputCollector); ok {
			sink = oc.OutputWriter(ctx.target.Name)
		} else if e.prefixOutput || ctx.forcePrefix {
			sink = &linePrefixWriter{w: e.outSink(), prefix: "[" + ctx.target.Name + "] "}
		}
		cmd.Stdout = io.MultiWriter(sink, &buf)
		cmd.Stderr = e.errSinkFor(ctx)
		err := cmd.Run()
		release()
		if pw, ok := sink.(*linePrefixWriter); ok {
			pw.flush()
		}
		e.setLastResult(ctx, exitCodeOf(err), buf.String())
		if err != nil && !ignoreErr {
			return e.commandError(fullCommand, stmtCtx, stmt, err, "")
		}
		return nil
	}

	var stdout, stderr []byte
	var err error
	if e.debug {
		stdout, stderr, err = capture(cmd)
	} else {
		stdout, err = cmd.Output()
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			stderr = ee.Stderr
		}
	}
	release()

	output := stdout
	if len(stderr) > 0 {
		if len(output) > 0 {
			output = append(output, '\n')
		}
		output = append(output, stderr...)
	}

	e.setLastResult(ctx, exitCodeOf(err), string(output))

	if err != nil {
		e.debugf("Command failed: %v\n", err)
		if len(stderr) > 0 {
			e.debugf("Error output: %s\n", string(stderr))
		}
		if !ignoreErr {
			return e.commandError(fullCommand, stmtCtx, stmt, err, string(stderr))
		}
		e.debugf("Ignoring failure (error-tolerant statement)\n")
	}

	strOutput := strings.TrimSpace(string(output))

	if ctx.out != nil {
		if err == nil {
			_, _ = ctx.out.Write(output)
		}
		return nil
	}

	switch {
	case ctx.isPrereq:
		e.mu.Lock()
		ctx.target.PrereqOutput = append(ctx.target.PrereqOutput, strOutput)
		if stmt.OutputName != "" {
			if ctx.target.NamedOutput == nil {
				ctx.target.NamedOutput = make(map[string]string)
			}
			ctx.target.NamedOutput[stmt.OutputName] = strOutput
		}
		e.mu.Unlock()
		e.debugf("Prereq output: %s\n", strOutput)
		if stmt.OutputName != "" {
			e.debugf("Named output %s.%s = %s\n", ctx.target.Name, stmt.OutputName, strOutput)
		}
	case ctx.target.LazyEval != nil:
		e.StructuredParse.SetVariable(ctx.target.LazyEval.VarName, ctx.target.LazyEval.Scope, strOutput)
		e.debugf("Set variable %s.%s = %s\n", ctx.target.LazyEval.Scope, ctx.target.LazyEval.VarName, strOutput)
	default:
		if !e.quiet {
			fmt.Fprintln(e.outSink(), strOutput)
		}
	}
	return nil
}

func (e *Executor) commandError(fullCommand string, stmtCtx *execContext, stmt BodyStatement, err error, stderr string) error {
	ce := &CommandError{
		Cmd:      fullCommand,
		ExitCode: exitCodeOf(err),
		Stderr:   stderr,
		File:     stmtCtx.srcFile,
		Line:     stmt.SourceLine,
	}
	timedOut := errors.Is(err, context.DeadlineExceeded)
	if !timedOut && e.effectiveRunCtx(stmtCtx).Err() == context.DeadlineExceeded {
		timedOut = true
	}
	if timedOut {
		ce.TimedOut = true
		ce.Timeout = stmt.Timeout
		if ce.Timeout == "" {
			ce.Timeout = stmtCtx.target.Timeout
		}
		ce.ExitCode = 124
	}
	return ce
}

func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	return 1
}

func (e *Executor) Execute(commands []string) error {
	e.runs = make(map[string]*commandRun)
	e.loadState()

	for _, decl := range e.StructuredParse.StateDecls {
		if _, exists := e.state[decl.Name]; !exists {
			e.setRuntimeState(decl.Name, e.resolveGlobalValue(decl.Value))
			e.debugf("state %s=%s (initialized)\n", decl.Name, decl.Value)
		}
	}
	defer e.saveRunRecords()

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
	var addPrereqs func(name string)
	addPrereqs = func(name string) {
		if neededScopes[name] {
			return
		}
		neededScopes[name] = true
		if cmd, err := e.StructuredParse.GetCommand(name); err == nil {
			for _, prereq := range cmd.Prereqs {
				addPrereqs(strings.TrimSpace(prereq))
			}
		}
	}
	for _, name := range targets {
		addPrereqs(name)
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

	if len(targets) == 0 {
		return errors.New("no commands requested and no default ('_') command defined (run `construct --list` to see available commands)")
	}

	if e.concurrent {
		return e.execConcurrent(targets)
	}

	var errs []error
	for _, cmdName := range targets {
		if err := e.processCommand(cmdName); err != nil {
			if !e.keepGoing {
				return err
			}
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return &KeepGoingError{Errs: errs}
	}
	return nil
}

// KeepGoingError aggregates failures from --keep-going runs.
type KeepGoingError struct {
	Errs []error
}

func (e *KeepGoingError) Error() string {
	return errors.Join(e.Errs...).Error()
}

func (e *KeepGoingError) ExitCode() int {
	for _, err := range e.Errs {
		if ce, ok := err.(*CommandError); ok {
			return ce.ExitCode
		}
	}
	return 1
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

	var errs []error
	for err := range errCh {
		if err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		if e.keepGoing {
			return &KeepGoingError{Errs: errs}
		}
		return errs[0]
	}
	return nil
}

func (e *Executor) processCommand(name string) error {
	command, err := e.StructuredParse.GetCommand(name)
	if err != nil {
		if def, cerr := e.getCloudDefinition(name); cerr == nil {
			e.debugf("Running cloud command %s (no local definition)\n", name)
			command = &Command{Name: def.Name, Body: def.Body, Arguments: def.Arguments}
			return e.EvaluateCommand(command)
		}
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

func expandRange(s string) ([]string, bool) {
	a, b, ok := strings.Cut(strings.TrimSpace(s), "..")
	if !ok {
		return nil, false
	}
	start, err1 := strconv.Atoi(strings.TrimSpace(a))
	end, err2 := strconv.Atoi(strings.TrimSpace(b))
	if err1 != nil || err2 != nil {
		return nil, false
	}
	var out []string
	if start <= end {
		for i := start; i <= end; i++ {
			out = append(out, strconv.Itoa(i))
		}
	} else {
		for i := start; i >= end; i-- {
			out = append(out, strconv.Itoa(i))
		}
	}
	return out, true
}

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

func CacheDirName() string {
	return cacheDir
}

type fileCache map[string]map[string]string

func (e *Executor) cacheDirFor() string {
	if e.baseDir != "" {
		return filepath.Join(e.baseDir, cacheDir)
	}
	return cacheDir
}

func loadFileCache(dir string) fileCache {
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return fileCache{}
	}
	var fc fileCache
	if err := json.Unmarshal(data, &fc); err != nil {
		return fileCache{}
	}
	return fc
}

func (fc fileCache) save(dir string) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return
	}

	data, _ := json.MarshalIndent(fc, "", "  ")
	_ = os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0644)
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

func (e *Executor) loadedCacheLocked() fileCache {
	if !e.cacheLoaded {
		e.cache = loadFileCache(e.cacheDirFor())
		e.cacheLoaded = true
	}
	return e.cache
}

func (e *Executor) cacheManifest() fileCache {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.loadedCacheLocked()
}

func (e *Executor) cacheKey(cmd *Command) string {
	name := cmd.Name
	if cmd.SourceFile != "" {
		name += "@" + filepath.Base(cmd.SourceFile)
	}
	parts := []string{name}
	for _, arg := range cmd.Arguments {
		v := arg.Default
		if e.flagSet != nil {
			v, _ = e.flagSet.GetString(cmd.Name + ":" + arg.Name)
		}
		parts = append(parts, arg.Name+"="+v)
	}
	snapshot := e.StructuredParse.GlobalVariableSnapshot()
	if cmd.cacheGlobalsExact {
		for _, g := range cmd.cacheGlobals {
			parts = append(parts, "var:"+g+"="+snapshot[g])
		}
	} else {
		for name, val := range snapshot {
			parts = append(parts, "var:"+name+"="+val)
		}
	}
	sort.Strings(parts[1:])
	return strings.Join(parts, "|")
}

func (e *Executor) workDirFor(cmd *Command, resolve func(string, string) string, workDir string) string {
	wd := e.resolveWorkDir(resolve(workDir, cmd.Name))
	if wd == "" {
		wd = e.baseDir
	}
	return wd
}

func (e *Executor) shouldSkip(cmd *Command, resolve func(string, string) string, workDir string) (bool, string) {
	files := expandFileDeps(cmd.FileDeps, e.workDirFor(cmd, resolve, workDir))
	if len(files) == 0 {
		return false, ""
	}

	fc := e.cacheManifest()
	key := e.cacheKey(cmd)
	cached, exists := fc[key]
	if !exists {
		return false, "no cached result"
	}

	hashes := parallelHash(files)
	for i, f := range files {
		if cached[f] != hashes[i] {
			e.debugf("%s: file changed: %s\n", cmd.Name, f)
			return false, fmt.Sprintf("%s changed", f)
		}
	}
	return true, fmt.Sprintf("%d dep(s) unchanged", len(files))
}

func parallelHash(files []string) []string {
	if len(files) < 2 {
		out := make([]string, len(files))
		for i, f := range files {
			out[i] = hashFile(f)
		}
		return out
	}
	out := make([]string, len(files))
	var wg sync.WaitGroup
	for i, f := range files {
		wg.Add(1)
		go func(i int, f string) {
			defer wg.Done()
			out[i] = hashFile(f)
		}(i, f)
	}
	wg.Wait()
	return out
}

func (e *Executor) shouldSkipProduced(cmd *Command, resolve func(string, string) string, workDir string) (bool, string) {
	artifacts := expandFileDeps(cmd.Produces, e.workDirFor(cmd, resolve, workDir))
	if len(artifacts) == 0 {
		return false, ""
	}
	var newest time.Time
	for _, a := range artifacts {
		info, err := os.Stat(a)
		if err != nil || info.IsDir() {
			return false, fmt.Sprintf("missing artifact %s", a) // must build
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	for _, d := range expandFileDeps(cmd.FileDeps, e.workDirFor(cmd, resolve, workDir)) {
		info, err := os.Stat(d)
		if err != nil {
			return false, fmt.Sprintf("missing dep %s", d)
		}
		if info.ModTime().After(newest) {
			return false, fmt.Sprintf("%s is newer than the artifacts", d)
		}
	}
	return true, "artifacts up to date"
}

func (e *Executor) updateCache(cmd *Command, resolve func(string, string) string, workDir string) {
	files := expandFileDeps(cmd.FileDeps, e.workDirFor(cmd, resolve, workDir))

	e.mu.Lock()
	defer e.mu.Unlock()

	fc := e.loadedCacheLocked()
	key := e.cacheKey(cmd)
	if fc[key] == nil {
		fc[key] = make(map[string]string)
	}
	hashes := parallelHash(files)
	for i, f := range files {
		fc[key][f] = hashes[i]
	}
	fc.save(e.cacheDirFor())
}
