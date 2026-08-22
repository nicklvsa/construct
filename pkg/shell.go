package pkg

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

func (e *Executor) InteractiveShell(name, containerOverride string) (int, error) {
	var cmd *Command
	if name != "" {
		var err error
		cmd, err = e.StructuredParse.GetCommand(name)
		if err != nil {
			return 2, err
		}
	} else {
		cmd = &Command{Name: ""}
	}

	env := slices.Clone(e.env)
	ctx := &execContext{target: cmd, env: &env, srcFile: cmd.SourceFile}

	resolveValue := func(s, scope string) string {
		s = resolveVarRefs(s, func(name string) (string, bool) {
			return e.StructuredParse.LookupVariable(name, scope)
		})
		return ResolveEnvRefs(s)
	}
	if containerOverride != "" {
		ctx.container = e.resolveContainer(resolveValue(containerOverride, cmd.Name))
	} else if cmd.Container != "" {
		ctx.container = e.resolveContainer(resolveValue(cmd.Container, cmd.Name))
	}

	for _, stmt := range cmd.Body {
		if stmt.Type != StmtEnv {
			continue
		}
		for _, pair := range stmt.Env {
			key, value, _ := strings.Cut(pair, "=")
			value = resolveVarRefs(value, func(name string) (string, bool) {
				return e.StructuredParse.LookupVariable(name, cmd.Name)
			})
			resolved := e.resolveBodyEnvRef(ctx, value)
			env = setEnvVar(env, key, resolved)
		}
	}

	var proc *exec.Cmd
	if ctx.container != "" {
		fields := strings.Fields(ctx.container)
		if len(fields) != 2 {
			return 1, fmt.Errorf("invalid container spec %q", ctx.container)
		}
		rt, image := fields[0], fields[1]
		if rt == "none" {
			return 1, fmt.Errorf("neither docker nor podman is installed")
		}
		argv := []string{rt, "run", "--rm", "-it"}
		f, err := os.CreateTemp("", "construct-env-")
		if err != nil {
			return 1, err
		}
		defer os.Remove(f.Name())
		for _, kv := range containerForwardedEnv(env) {
			fmt.Fprintln(f, kv)
		}
		f.Close()
		argv = append(argv, "--env-file", f.Name())
		if e.baseDir != "" {
			abs, err := filepath.Abs(e.baseDir)
			if err != nil {
				return 1, err
			}
			argv = append(argv, "-v", filepath.ToSlash(abs)+":/work", "-w", "/work")
		}
		if cmd.WorkDir != "" {
			if wd, ok := containerWorkDir(cmd.WorkDir); ok {
				argv = append(argv, "-w", wd)
			}
		}
		argv = append(argv, image, "/bin/sh", "-c", "command -v bash >/dev/null 2>&1 && exec bash || exec sh")
		proc = exec.Command(argv[0], argv[1:]...)
	} else {
		proc = exec.Command(e.shellName)
		if cmd.WorkDir != "" {
			proc.Dir = e.resolveWorkDir(cmd.WorkDir)
			if e.baseDir != "" {
				os.MkdirAll(proc.Dir, 0o755)
			}
		} else if e.baseDir != "" {
			proc.Dir = e.baseDir
		}
	}

	proc.Env = env
	proc.Stdin = os.Stdin
	proc.Stdout = os.Stdout
	proc.Stderr = os.Stderr

	if err := proc.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode(), nil
		}
		return 1, err
	}
	return 0, nil
}

func containerWorkDir(workDir string) (string, bool) {
	wd := filepath.ToSlash(workDir)
	switch {
	case strings.HasPrefix(wd, "/"):
		return wd, true
	case len(wd) >= 2 && wd[1] == ':':
		return "", false
	default:
		return "/work/" + wd, true
	}
}
