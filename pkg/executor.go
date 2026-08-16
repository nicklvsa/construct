package pkg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/spf13/pflag"
)

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
	cacheDirty      bool
	hashMemo        map[string]string
	stateDirty      bool
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

type commandRun struct {
	done chan struct{}
	err  error
}

var (
	errLoopContinue = errors.New("continue used outside of a loop")
	errLoopBreak    = errors.New("break used outside of a loop")
)

func NewExecutor(data *ParsedData, concurrent bool, debug bool) *Executor {
	shellName, shellArgs := DefaultShell()
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

	var depFiles []string
	if len(command.FileDeps) > 0 && !isPrereq {
		depFiles = expandFileDeps(command.FileDeps, e.workDirFor(command, resolveValue, workDir))
	}

	if len(command.FileDeps) > 0 && !isPrereq && len(command.Produces) == 0 && !e.noCache {
		if skip, reason := e.shouldSkip(command, depFiles); skip {
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
		if skip, reason := e.shouldSkipProduced(command, resolveValue, workDir, depFiles); skip {
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

	if len(depFiles) > 0 {
		for _, dep := range depFiles {
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

	e.seedPrereqOutputs(command)
	body := e.bodyFor(command)

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
		produced := expandFileDeps(command.Produces, e.workDirFor(command, resolveValue, workDir))
		for _, artifact := range produced {
			if _, err := os.Stat(artifact); err != nil {
				fmt.Fprintf(os.Stderr, "warning: %s declares produces %q but it does not exist\n", command.Name, artifact)
			}
		}
		// Files this command just built must re-hash for later commands.
		e.invalidateHashes(produced)
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

func (e *Executor) Execute(commands []string) error {
	e.runs = make(map[string]*commandRun)
	e.loadState()

	for _, decl := range e.StructuredParse.StateDecls {
		e.mu.Lock()
		_, exists := e.state[decl.Name]
		e.mu.Unlock()
		if !exists {
			e.setRuntimeState(decl.Name, e.resolveGlobalValue(decl.Value))
			e.debugf("state %s=%s (initialized)\n", decl.Name, decl.Value)
		}
	}
	defer e.saveRunRecords()
	defer e.flushCache()
	defer e.flushState()

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
