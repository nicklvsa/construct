package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nicklvsa/construct/pkg"
)

type devPlan struct {
	services   []string
	regulars   []string
	aggregator *pkg.Command
}

// planDev resolves which services to supervise and which regular commands to
// run first. The aggregator (a command named dev, or a non-service target
// passed to `construct dev X`) contributes its prerequisites as the service
// set and runs its body as setup.
func planDev(data *pkg.ParsedData, rest []string) (*devPlan, error) {
	isService := map[string]bool{}
	for _, c := range data.Commands {
		if c.IsService {
			isService[c.Name] = true
		}
	}

	var svcRoots []string
	var aggregator *pkg.Command
	if len(rest) > 0 {
		for _, r := range rest {
			cmd, err := data.GetCommand(r)
			if err != nil {
				return nil, exitAt(2, "dev: unknown command %q", r)
			}
			if cmd.IsService {
				svcRoots = append(svcRoots, r)
				continue
			}
			if aggregator != nil {
				return nil, exitAt(2, "dev: only one non-service command can select services (got %s and %s)", aggregator.Name, r)
			}
			aggregator = cmd
			svcRoots = append(svcRoots, cmd.Prereqs...)
		}
	} else if cmd, err := data.GetCommand("dev"); err == nil {
		aggregator = cmd
		svcRoots = cmd.Prereqs
	} else {
		for _, c := range data.Commands {
			if c.IsService {
				svcRoots = append(svcRoots, c.Name)
			}
		}
	}

	inClosure := map[string]bool{}
	var visit func(name string) error
	visit = func(name string) error {
		if inClosure[name] {
			return nil
		}
		inClosure[name] = true
		cmd, err := data.GetCommand(name)
		if err != nil {
			return exitAt(2, "dev: unknown command %q", name)
		}
		for _, pre := range cmd.Prereqs {
			if err := visit(strings.TrimSpace(pre)); err != nil {
				return err
			}
		}
		return nil
	}
	for _, r := range svcRoots {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		if err := visit(r); err != nil {
			return nil, err
		}
	}
	// The aggregator joins the closure so its own prereqs resolve, but it is
	// excluded from the regular run list — it runs as setup instead.
	if aggregator != nil {
		if err := visit(aggregator.Name); err != nil {
			return nil, err
		}
	}

	plan := &devPlan{aggregator: aggregator}
	for name := range inClosure {
		if pkg.IsLazyName(name) {
			continue
		}
		if aggregator != nil && name == aggregator.Name {
			continue
		}
		if isService[name] {
			plan.services = append(plan.services, name)
		} else {
			plan.regulars = append(plan.regulars, name)
		}
	}
	if len(plan.services) == 0 {
		return nil, exitAt(2, "dev: no service commands to run (declare one with `service name { ... }`)")
	}
	slices.Sort(plan.services)
	slices.Sort(plan.regulars)

	// A regular command that depends on a service would block on the
	// never-ending service; the aggregator is exempt by design.
	for _, name := range plan.regulars {
		cmd, _ := data.GetCommand(name)
		for _, pre := range cmd.Prereqs {
			if isService[strings.TrimSpace(pre)] {
				return nil, exitAt(2, "service %q cannot be a prerequisite of non-service %q (invert the dependency)", strings.TrimSpace(pre), name)
			}
		}
	}
	return plan, nil
}

func runDev(args []string, o *options) error {
	fileName, rest := splitConstfileArgs(args)
	if err := rejectSubcommandFlags(rest, "dev"); err != nil {
		return err
	}
	p, err := pkg.NewParser(fileName)
	if err != nil {
		return err
	}
	data, err := p.Parse()
	if err != nil {
		return err
	}
	baseDir := filepath.Dir(fileName)

	plan, err := planDev(data, rest)
	if err != nil {
		return err
	}
	regulars, aggregator := plan.regulars, plan.aggregator

	if len(regulars) > 0 {
		fmt.Printf("(dev: running %d prerequisite command(s): %s)\n", len(regulars), strings.Join(regulars, ", "))
		ex := pkg.NewExecutor(data, false, o.debug)
		ex.SetBaseDir(baseDir)
		ex.SetShell(o.shell)
		ex.SetYes(o.yes)
		if err := ex.Execute(regulars); err != nil {
			return err
		}
	}

	if aggregator != nil && len(aggregator.Body) > 0 {
		fmt.Printf("(dev: running %s setup)\n", aggregator.Name)
		ex := pkg.NewExecutor(data, false, o.debug)
		ex.SetBaseDir(baseDir)
		ex.SetShell(o.shell)
		ex.SetYes(o.yes)
		if err := ex.RunServiceBody(aggregator); err != nil {
			return fmt.Errorf("dev setup (%s): %w", aggregator.Name, err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	go func() {
		<-sigCh
		fmt.Println("\n(dev) stopping services...")
		cancel()
		<-sigCh // a service is ignoring TERM; force
		os.Exit(130)
	}()

	services := plan.services
	ready := map[string]chan struct{}{}
	for _, s := range services {
		ready[s] = make(chan struct{})
	}
	fmt.Printf("(dev: starting %d service(s): %s — Ctrl-C stops all)\n", len(services), strings.Join(services, ", "))
	var wg sync.WaitGroup
	for _, name := range services {
		cmd, _ := data.GetCommand(name)
		wg.Add(1)
		go superviseService(&wg, data, cmd, baseDir, o, ctx, ready)
	}
	wg.Wait()
	fmt.Println("(dev) stopped")
	return nil
}

func superviseService(wg *sync.WaitGroup, data *pkg.ParsedData, cmd *pkg.Command, baseDir string, o *options, globalCtx context.Context, ready map[string]chan struct{}) {
	defer wg.Done()
	name := cmd.Name

	for _, pre := range cmd.Prereqs {
		pre = strings.TrimSpace(pre)
		if ch, ok := ready[pre]; ok {
			select {
			case <-ch:
			case <-globalCtx.Done():
				return
			}
		}
	}

	ex := pkg.NewExecutor(data, false, o.debug)
	ex.SetBaseDir(baseDir)
	ex.SetShell(o.shell)
	ex.SetYes(o.yes)

	restart := make(chan struct{}, 1)
	if len(cmd.OnChange) > 0 {
		go watchOnchange(baseDir, cmd.OnChange, restart, globalCtx)
	}

	first := true
	backoff := time.Second
	for {
		if globalCtx.Err() != nil {
			return
		}
		select { // drain stale restarts from the previous cycle
		case <-restart:
		default:
		}

		runCtx, stopRun := context.WithCancel(globalCtx)
		ex.SetRunContext(runCtx)
		done := make(chan error, 1)
		start := time.Now()
		go func() { done <- ex.RunServiceBody(cmd) }()

		if first {
			first = false
			if cmd.Port != "" {
				if waitPort(globalCtx, cmd.Port, 90*time.Second) {
					fmt.Printf("[%s] ready on port %s\n", name, cmd.Port)
				} else if globalCtx.Err() == nil {
					fmt.Fprintf(os.Stderr, "[%s] port %s not ready after 90s; starting dependents anyway\n", name, cmd.Port)
				}
			}
			close(ready[name])
		}

		var runErr error
		restarted := false
		select {
		case runErr = <-done:
		case <-restart:
			restarted = true
			fmt.Printf("[%s] inputs changed; restarting\n", name)
			stopRun()
			runErr = <-done
		}
		stopRun()

		if globalCtx.Err() != nil {
			return
		}
		if time.Since(start) > 30*time.Second {
			backoff = time.Second
		}
		if restarted {
			continue
		}
		code := 1
		if ce, ok := runErr.(interface{ ExitCode() int }); ok {
			code = ce.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "[%s] exited (code %d); restarting in %s\n", name, code, backoff)
		select {
		case <-time.After(backoff):
		case <-restart:
		case <-globalCtx.Done():
			return
		}
		backoff = min(backoff*2, 10*time.Second)
	}
}

func waitPort(ctx context.Context, port string, timeout time.Duration) bool {
	addr := net.JoinHostPort("127.0.0.1", port)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 250*time.Millisecond)
		if err == nil {
			conn.Close()
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(250 * time.Millisecond):
		}
	}
	return false
}

func watchOnchange(baseDir string, patterns []string, restart chan struct{}, ctx context.Context) {
	snapshot := func() map[string]int64 {
		files := []string{}
		for _, pat := range patterns {
			full := filepath.Join(baseDir, pat)
			matches, err := filepath.Glob(full)
			if err != nil || len(matches) == 0 {
				files = append(files, full)
				continue
			}
			files = append(files, matches...)
		}
		return fileSnapshot(files)
	}
	prev := snapshot()
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
		if next := snapshot(); !fileSnapshotEqual(prev, next) {
			prev = next
			select {
			case restart <- struct{}{}:
			default:
			}
		}
	}
}
