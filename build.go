package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nicklvsa/construct/pkg"
	flag "github.com/spf13/pflag"
	"golang.org/x/term"
)

func debugf(on bool, format string, args ...interface{}) {
	if on {
		fmt.Printf("[DEBUG] "+format, args...)
	}
}

func listCommands(data *pkg.ParsedData) {
	fmt.Println("Available commands:")
	for _, cmd := range data.Commands {
		if cmd.Name == "_" || pkg.IsLazyName(cmd.Name) {
			continue
		}
		if cmd.IsDefault {
			fmt.Printf("  %s (default)\n", cmd.Name)
		} else {
			fmt.Printf("  %s\n", cmd.Name)
		}
		if cmd.IsService {
			port := ""
			if cmd.Port != "" {
				port = fmt.Sprintf(", port %s", cmd.Port)
			}
			fmt.Printf("    (service%s)\n", port)
		}
		if cmd.Description != "" {
			for _, l := range strings.Split(cmd.Description, "\n") {
				fmt.Printf("    %s\n", l)
			}
		}
		if cmd.WorkDir != "" {
			fmt.Printf("    Working dir: %s\n", cmd.WorkDir)
		}
		if cmd.Timeout != "" {
			fmt.Printf("    Timeout: %s\n", cmd.Timeout)
		}
		if len(cmd.Produces) > 0 {
			fmt.Printf("    Produces: %s\n", strings.Join(cmd.Produces, ", "))
		}
		if len(cmd.Arguments) > 0 {
			fmt.Printf("    Arguments: ")
			for i, arg := range cmd.Arguments {
				if i > 0 {
					fmt.Printf(", ")
				}
				if arg.IsOptional {
					fmt.Printf("[%s]", arg.Name)
				} else {
					fmt.Printf("%s", arg.Name)
				}
			}
			fmt.Println()
		}
		if len(cmd.Prereqs) > 0 {
			fmt.Printf("    Depends on: %s\n", cmd.Prereqs)
		}
	}
}

func listCommandsJSON(data *pkg.ParsedData) {
	type cmdInfo struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Arguments   []*pkg.Argument `json:"arguments,omitempty"`
		Prereqs     []string        `json:"prereqs,omitempty"`
		WorkDir     string          `json:"work_dir,omitempty"`
		Timeout     string          `json:"timeout,omitempty"`
		Produces    []string        `json:"produces,omitempty"`
		IsDefault   bool            `json:"is_default"`
		IsService   bool            `json:"is_service,omitempty"`
		Port        string          `json:"port,omitempty"`
	}
	var out []cmdInfo
	for _, cmd := range data.Commands {
		if cmd.Name == "_" || pkg.IsLazyName(cmd.Name) {
			continue
		}
		out = append(out, cmdInfo{
			Name:        cmd.Name,
			Description: cmd.Description,
			Arguments:   cmd.Arguments,
			Prereqs:     cmd.Prereqs,
			WorkDir:     cmd.WorkDir,
			Timeout:     cmd.Timeout,
			Produces:    cmd.Produces,
			IsDefault:   cmd.IsDefault,
			IsService:   cmd.IsService,
			Port:        cmd.Port,
		})
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(b))
}

func printDryRunBody(body []pkg.BodyStatement, indent int) {
	prefix := strings.Repeat("  ", indent+1)
	for _, stmt := range body {
		switch stmt.Type {
		case pkg.StmtIf:
			fmt.Printf("%sif %s {\n", prefix, stmt.Cond)
			printDryRunBody(stmt.ThenBody, indent+1)
			elseBody := stmt.ElseBody
			for len(elseBody) == 1 && elseBody[0].Type == pkg.StmtIf {
				inner := elseBody[0]
				fmt.Printf("%s} else if %s {\n", prefix, inner.Cond)
				printDryRunBody(inner.ThenBody, indent+1)
				elseBody = inner.ElseBody
			}
			if len(elseBody) > 0 {
				fmt.Printf("%s} else {\n", prefix)
				printDryRunBody(elseBody, indent+1)
			}
			fmt.Printf("%s}\n", prefix)
		case pkg.StmtFor:
			loopVar := stmt.LoopVar
			if stmt.LoopIndex != "" {
				loopVar = stmt.LoopIndex + ", " + loopVar
			}
			keyword := "for"
			if stmt.Parallel {
				keyword = "parallel"
				if stmt.ParallelJobs > 0 {
					keyword = fmt.Sprintf("parallel<%d>", stmt.ParallelJobs)
				}
			}
			fmt.Printf("%s%s %s in %s {\n", prefix, keyword, loopVar, stmt.LoopItems)
			printDryRunBody(stmt.LoopBody, indent+1)
			fmt.Printf("%s}\n", prefix)
		case pkg.StmtSwitch:
			mod := ""
			if stmt.Modifier != "" {
				mod = "<" + stmt.Modifier + ">"
			}
			fmt.Printf("%sswitch%s %s {\n", prefix, mod, stmt.SwitchExpr)
			for _, c := range stmt.Cases {
				if c.IsDefault {
					fmt.Printf("%s  default {\n", prefix)
				} else {
					fmt.Printf("%s  case %s {\n", prefix, strings.Join(c.Values, ", "))
				}
				printDryRunBody(c.Body, indent+2)
				fmt.Printf("%s  }\n", prefix)
			}
			fmt.Printf("%s}\n", prefix)
		case pkg.StmtInDir:
			fmt.Printf("%sin %s {\n", prefix, stmt.Shell)
			printDryRunBody(stmt.ThenBody, indent+1)
			fmt.Printf("%s}\n", prefix)
		case pkg.StmtLock:
			mod := ""
			if stmt.Modifier != "" {
				mod = "<" + stmt.Modifier + ">"
			}
			fmt.Printf("%slock%s %q {\n", prefix, mod, stmt.Shell)
			printDryRunBody(stmt.ThenBody, indent+1)
			fmt.Printf("%s}\n", prefix)
		case pkg.StmtState:
			fmt.Printf("%sstate %s = %s\n", prefix, stmt.Shell, stmt.Message)
		case pkg.StmtBuiltin:
			args := stmt.BuiltinArgs
			if stmt.Tolerant {
				args = "! " + args
			}
			if stmt.Timeout != "" {
				args = fmt.Sprintf("timeout %s %s", stmt.Timeout, args)
			}
			name := stmt.Shell
			if stmt.Modifier != "" {
				name = fmt.Sprintf("%s<%s>", name, stmt.Modifier)
			}
			fmt.Printf("%s%s %s\n", prefix, name, args)
		case pkg.StmtConfirm:
			fmt.Printf("%sconfirm %q\n", prefix, stmt.Message)
		case pkg.StmtPrompt:
			fmt.Printf("%sprompt %q\n", prefix, stmt.Message)
		case pkg.StmtInput:
			fmt.Printf("%sinput %s %q\n", prefix, stmt.Shell, stmt.Message)
		case pkg.StmtContinue, pkg.StmtBreak:
			fmt.Printf("%s%s\n", prefix, stmt.Type)
		case pkg.StmtPort:
			fmt.Printf("%sport %s\n", prefix, stmt.Shell)
		case pkg.StmtInvoke:
			fmt.Printf("%sinvoke %s\n", prefix, stmt.Shell)
		case pkg.StmtEnv:
			fmt.Printf("%senv { %s }\n", prefix, strings.Join(stmt.Env, ", "))
		case pkg.StmtFail:
			fmt.Printf("%sfail %q\n", prefix, stmt.Message)
		case pkg.StmtOnFail:
			fmt.Printf("%sonfail {\n", prefix)
			printDryRunBody(stmt.OnFailBody, indent+1)
			fmt.Printf("%s}\n", prefix)
		default:
			shell := stmt.Shell
			if stmt.Timeout != "" {
				shell = fmt.Sprintf("timeout %s %s", stmt.Timeout, shell)
			}
			fmt.Printf("%s%s\n", prefix, shell)
		}
	}
}

func tuiEligible(inputs *ConstructInput, o *options) bool {
	if o.watch || o.repeat > 0 || o.dryRun || o.showList || o.choose || o.debug || o.explain || o.ghActions || o.quiet || o.resume {
		fmt.Fprintln(os.Stderr, "(--tui disabled: incompatible with the selected options)")
		return false
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		fmt.Fprintln(os.Stderr, "(--tui requires a terminal; running without the dashboard)")
		return false
	}
	if p, err := pkg.NewParser(inputs.FileName); err == nil {
		if data, err := p.Parse(); err == nil {
			var all []pkg.BodyStatement
			for _, cmd := range data.Commands {
				all = append(all, collectAllStatements(cmd.Body)...)
			}
			for _, stmt := range all {
				switch stmt.Type {
				case pkg.StmtConfirm, pkg.StmtPrompt, pkg.StmtInput:
					fmt.Fprintln(os.Stderr, "(--tui disabled: interactive statements need a plain terminal)")
					return false
				}
			}
		}
	}
	return true
}

func executeBuild(inputs *ConstructInput, o *options, runCtx context.Context) ([]string, error) {
	p, err := pkg.NewParser(inputs.FileName)
	if err != nil {
		return nil, err
	}
	data, err := p.Parse()
	if err != nil {
		return nil, err
	}

	// Strict second parse so per-command argument flags (--cmd:arg) are recognized.
	flagSet := flag.NewFlagSet("construct", flag.ContinueOnError)
	flagSet.SetOutput(io.Discard)
	defineFlags(flagSet, &options{})
	executor := pkg.NewExecutor(data, o.concurrent, o.debug)
	if o.tui {
		dashCtx, dashCancel := context.WithCancel(runCtx)
		defer dashCancel()
		runCtx = dashCtx
		o.dash = newDashboard(executor, dashCancel)
		executor.SetObserver(o.dash)
		executor.SetSilentStatus(true)
		go o.dash.start()
	}

	executor.SetBaseDir(filepath.Dir(inputs.FileName))
	executor.SetJobs(o.jobs)
	executor.SetTiming(o.timing)
	executor.SetNoCache(o.noCache)
	executor.SetKeepGoing(o.keepGoing)
	executor.SetQuiet(o.quiet)
	executor.SetExplain(o.explain)
	executor.SetShell(o.shell)
	executor.SetRunContext(runCtx)
	executor.SetYes(o.yes)
	executor.SetFlame(o.flame)
	executor.SetGithubActions(o.ghActions)
	executor.SetRecordRuns(true)
	executor.RegisterArgumentFlags(flagSet)

	flagSet.ParseErrorsWhitelist.UnknownFlags = false
	if err := flagSet.Parse(os.Args[1:]); err != nil {
		return nil, err
	}

	for _, ov := range o.overrides {
		before, after, ok := strings.Cut(ov, "=")
		if !ok {
			return nil, fmt.Errorf("invalid override %q (expected key=value)", ov)
		}
		key := strings.TrimSpace(before)
		val := after
		overridden := false
		for _, v := range data.Variables {
			if v.Name == key {
				overridden = true
				break
			}
		}

		data.SetVariable(key, "global", val)
		if o.debug {
			if overridden {
				debugf(o.debug, "Override: %s = %s\n", key, val)
			} else {
				debugf(o.debug, "Override (new): %s = %s\n", key, val)
			}
		}
	}

	if o.showList {
		if o.json {
			listCommandsJSON(data)
		} else {
			listCommands(data)
		}
		return nil, nil
	}

	if o.choose && !o.dryRun && !o.watch {
		chosen, err := chooseTargets(data)
		if err != nil {
			return nil, err
		}
		inputs.Commands = chosen
	}

	if o.since != "" {
		proceed, err := applySince(inputs, o, data)
		if err != nil {
			return nil, err
		}
		if !proceed {
			return collectWatchFiles(inputs.FileName, data), nil
		}
	}

	if o.dryRun {
		fmt.Println("Dry run mode - commands that would be executed:")
		for _, cmd := range data.Commands {
			if pkg.IsLazyName(cmd.Name) {
				continue
			}
			if len(inputs.Commands) == 0 || slices.Contains(inputs.Commands, cmd.Name) {
				fmt.Printf("  %s\n", cmd.Name)
				if cmd.WorkDir != "" {
					fmt.Printf("    (in %s)\n", cmd.WorkDir)
				}
				if len(cmd.Produces) > 0 {
					fmt.Printf("    (produces: %s)\n", strings.Join(cmd.Produces, ", "))
				}
				if len(cmd.FileDeps) > 0 {
					fmt.Printf("    (deps: %s)\n", strings.Join(cmd.FileDeps, ", "))
				}
				printDryRunBody(cmd.Body, 1)
			}
		}
		return nil, nil
	}

	execErr := executor.Execute(inputs.Commands)
	if o.flame {
		// Render even on failure — the flame graph is most useful then.
		renderFlame(executor.FlameRows())
	}
	if execErr != nil {
		return nil, execErr
	}
	return collectWatchFiles(inputs.FileName, data), nil
}

func collectWatchFiles(fileName string, data *pkg.ParsedData) []string {
	baseDir := filepath.Dir(fileName)
	files := []string{fileName}
	for _, sf := range data.SourceFiles {
		if sf != fileName {
			files = append(files, sf)
		}
	}
	patterns := func(pats []string) {
		for _, pat := range pats {
			full := filepath.Join(baseDir, pat)
			matches, _ := filepath.Glob(full)
			if len(matches) == 0 {
				files = append(files, full)
			} else {
				files = append(files, matches...)
			}
		}
	}
	for _, cmd := range data.Commands {
		patterns(cmd.FileDeps)
		patterns(cmd.Produces)
		patterns(cmd.OnChange)
	}
	return files
}

func waitForChange(files []string, interrupted *atomic.Bool) bool {
	prev := fileSnapshot(files)
	for {
		if interrupted.Load() {
			return false
		}
		time.Sleep(500 * time.Millisecond)
		if !fileSnapshotEqual(prev, fileSnapshot(files)) {
			return true
		}
	}
}

func fileSnapshot(files []string) map[string]int64 {
	m := make(map[string]int64, len(files))
	for _, f := range files {
		if info, err := os.Stat(f); err == nil {
			m[f] = info.ModTime().UnixNano()
		} else {
			m[f] = -1 // missing
		}
	}
	return m
}

func fileSnapshotEqual(a, b map[string]int64) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func notifySummary(inputs *ConstructInput, err error, d time.Duration) {
	summary := fmt.Sprintf("%s in %s", targetLabelFor(inputs), trimDuration(d))
	if err != nil {
		first := strings.SplitN(err.Error(), "\n", 2)[0]
		if len(first) > 120 {
			first = first[:120] + "…"
		}
		summary = fmt.Sprintf("%s failed in %s: %s", targetLabelFor(inputs), trimDuration(d), first)
	}
	notifyResult(err == nil, summary)
}

func targetLabelFor(inputs *ConstructInput) string {
	if len(inputs.Commands) == 0 {
		return "default target"
	}
	return strings.Join(inputs.Commands, " ")
}

func trimDuration(d time.Duration) string {
	return d.Round(100 * time.Millisecond).String()
}
