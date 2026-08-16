package pkg

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/spf13/pflag"
)

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

func (e *Executor) resolveShellLine(ctx *execContext, line string) string {
	cmd := ctx.target
	line = strings.TrimSpace(line)
	if line == "" {
		return ""
	}
	if line[0] == '$' {
		line = strings.TrimSpace(line[1:])
	}

	line = resolveVarRefs(line, func(name string) (string, bool) {
		if name == "last.exit" || name == "last.output" {
			return "", false // resolved after vars/args, below
		}
		v, ok := LookupVariableIndexed(e.StructuredParse, name, cmd.Name)
		if !ok {
			return "", false
		}
		return escapeShellValue(v.Joined()), true
	})

	for _, arg := range cmd.Arguments {
		if !strings.Contains(line, "&"+arg.Name) {
			continue
		}
		e.debugf("Handling argument --%s for command %s\n", arg.Name, cmd.Name)
		fs := e.flagSet
		if fs == nil {
			fs = pflag.CommandLine
		}
		v, _ := fs.GetString(cmd.flagScope() + ":" + arg.Name)
		line = strings.ReplaceAll(line, "&"+arg.Name, escapeShellValue(v))
	}

	line = e.resolveLastRefs(line, cmd.Name)
	return e.resolveBodyEnvRef(ctx, line)
}

var isolationRe = regexp.MustCompile(`\b(cd|pushd|popd|export|declare|typeset|readonly|local|set|unset|setopt|unsetopt|shopt|trap|umask|ulimit|alias|unalias|eval|exec|source|exit|return|break|continue|shift|read|readarray|mapfile|let|suspend|hash|caller)\b`)

func needsShellIsolation(line string) bool {
	if isolationRe.MatchString(line) {
		return true
	}
	if strings.Contains(line, "((") {
		return true
	}
	// Leading assignment (VAR=value …) defines a shell variable.
	name, _, ok := strings.Cut(line, "=")
	if !ok {
		return false
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || i > 0 && c >= '0' && c <= '9' {
			continue
		}
		return false
	}
	return true
}

func shellLineBody(line string) string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "$")
	return strings.TrimSpace(line)
}

func (e *Executor) runShellBatch(ctx *execContext, stmts []BodyStatement) error {
	type group struct {
		lines    []string
		strict   bool
		sourceLn int
	}
	var groups []*group
	cur := &group{strict: !strings.HasPrefix(shellLineBody(stmts[0].Shell), "!"), sourceLn: stmts[0].SourceLine}

	for _, stmt := range stmts {
		cmdLine := stmt.Shell
		if cmdLine == "" {
			continue
		}
		tolerant := strings.HasPrefix(shellLineBody(cmdLine), "!")
		if tolerant {
			cmdLine = strings.TrimSpace(strings.TrimPrefix(shellLineBody(cmdLine), "!"))
		}
		cmdLine = e.resolveShellLine(ctx, cmdLine)
		if cmdLine == "" {
			continue
		}
		switch {
		case needsShellIsolation(cmdLine) || strings.Contains(cmdLine, ";"):
			if tolerant {
				cmdLine = "( " + cmdLine + " ) || true"
			} else {
				cmdLine = "( " + cmdLine + " )"
			}
		case tolerant:
			cmdLine = cmdLine + " || true"
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
		if e.quiet || ctx.isPrereq {
			sink = io.Discard // prereq stdout is captured, not streamed
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
		if ctx.isPrereq {
			e.mu.Lock()
			ctx.target.PrereqOutput = append(ctx.target.PrereqOutput, strings.TrimSpace(buf.String()))
			e.mu.Unlock()
		}
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
	if body := shellLineBody(cmdLine); strings.HasPrefix(body, "!") {
		ignoreErr = true
		cmdLine = strings.TrimSpace(body[1:])
	}

	cmdLine = e.resolveShellLine(ctx, cmdLine)
	if cmdLine == "" {
		return nil
	}
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
