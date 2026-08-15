package pkg

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

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

			if stmt.Parallel {
				if err := e.execParallelFor(ctx, stmt, expanded); err != nil {
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
				err := e.execBody(ctx, stmt.LoopBody)
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
					!(ctx.isPrereq && body[end].OutputName != "") &&
					!strings.HasPrefix(shellLineBody(body[end].Shell), "!") &&
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

func (e *Executor) execParallelFor(ctx *execContext, stmt BodyStatement, items []string) error {
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
			err := e.runParallelIteration(ctx, stmt, item, idx, snapshot, dupes[item] > 1)
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

func (e *Executor) runParallelIteration(ctx *execContext, stmt BodyStatement, item string, idx int, snapshot []*Variable, qualify bool) error {
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

	return e.execBody(&sub, stmt.LoopBody)
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
		if err := e.execBody(ctx, []BodyStatement{body}); err != nil {
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

	body := e.bodyFor(invoked)

	var invokeErr error
	if stmt.OutputName != "" {
		var buf bytes.Buffer
		sub.out = &buf
		invokeErr = e.execBody(&sub, body)
		if invokeErr == nil {
			e.StructuredParse.SetVariable(stmt.OutputName, ctx.target.Name, strings.TrimSpace(buf.String()))
			e.debugf("invoke %s captured %d bytes\n", invoked.Name, buf.Len())
		}
	} else {
		invokeErr = e.execBody(&sub, body)
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

func envIsSet(ctx *execContext, name string) bool {
	if _, ok := envLookupValue(*ctx.env, name); ok {
		return true
	}
	_, ok := os.LookupEnv(name)
	return ok
}

// ---- prereq output seeding ----

// seedPrereqOutputs exposes a prerequisite's captured outputs to the calling
// command's scope as &prereq.N / &prereq.name variables. Reference resolution
// itself happens lazily at execution time (see resolveShellLine), so bodies no
// longer need to be re-cleaned after env/state/input statements or per loop
// iteration.
func (e *Executor) seedPrereqOutputs(cmd *Command) {
	if len(cmd.PrereqCmds) == 0 {
		return
	}
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

// expandOutputRefs rewrites &cmd.* into the comma-joined prereq outputs of cmd.
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

// streaming reports whether plain consecutive shell statements can run as one
// batched shell script. Prerequisite bodies qualify only when nothing
// references their per-statement outputs, since batching merges the captures.
func (e *Executor) streaming(ctx *execContext) bool {
	if e.debug || ctx.target.LazyEval != nil || ctx.out != nil {
		return false
	}
	if ctx.isPrereq {
		return !e.StructuredParse.OutputsIndexReferenced(ctx.target.Name)
	}
	return true
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
