package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/nicklvsa/construct/pkg"
)

var doctorRequireRe = regexp.MustCompile(`require\(\s*("([^"]+)"|'([^']+)')`)
var errDoctorFailed = errors.New("doctor found problems (see [FAIL] entries above)")

func runDoctor(o *options, inputs *ConstructInput) error {
	problems := 0
	fail := func(format string, args ...any) {
		problems++
		fmt.Printf("[FAIL] "+format+"\n", args...)
	}
	pass := func(format string, args ...any) {
		fmt.Printf("[ ok ] "+format+"\n", args...)
	}

	pass("construct %s (%s/%s)", version, runtime.GOOS, runtime.GOARCH)

	shell, args := pkg.DefaultShell()
	if _, err := exec.LookPath(shell); err != nil {
		fail("shell %q not found on PATH", shell)
	} else {
		pass("shell: %s %s", shell, strings.Join(args, " "))
	}

	cloudPath := os.Getenv("CONSTRUCT_CLOUD_FILE")
	if cloudPath == "" {
		cloudPath = filepath.Join(filepath.Dir(inputs.FileName), "construct-cloud.json")
	}
	if _, err := pkg.LoadCloudDefsFile(cloudPath); err != nil {
		fail("cloud file %s: %v", cloudPath, err)
	} else if fileExists(cloudPath) {
		pass("cloud file: %s", cloudPath)
	} else {
		pass("cloud file: %s (not present)", cloudPath)
	}

	envPath := o.envFile
	if envPath == "" {
		candidate := filepath.Join(filepath.Dir(inputs.FileName), ".env")
		if fileExists(candidate) {
			envPath = candidate
		}
	}
	if envPath != "" {
		if err := pkg.LoadEnvFile(envPath); err != nil {
			fail("env file %s: %v", envPath, err)
		} else {
			pass("env file: %s", envPath)
		}
	}

	data, err := parseConstfileOptional(inputs.FileName)
	if err != nil {
		fail("Constfile: %v", err)
		return errDoctorFailed
	}
	if data == nil {
		fmt.Println("no Constfile found — run `construct init` to scaffold one")
		return nil
	}
	pass("Constfile: %d command(s), %d variable(s)", len(data.Commands), len(data.Variables))

	for _, cmd := range data.Commands {
		for _, stmt := range collectAllStatements(cmd.Body) {
			for _, m := range doctorRequireRe.FindAllStringSubmatch(stmt.Cond, -1) {
				tool := strings.Trim(m[1], `"'`) // group 1 is the quoted name (double or single)
				if _, err := exec.LookPath(tool); err != nil {
					fail("command %q requires tool %q, which is not on PATH", cmd.Name, tool)
				} else {
					pass("tool %q found (required by %q)", tool, cmd.Name)
				}
			}
		}
	}

	used := make(map[string]bool)
	for _, cmd := range data.Commands {
		for _, stmt := range collectAllStatements(cmd.Body) {
			markRefs(used, stmt.Shell)
			markRefs(used, stmt.Cond)
			markRefs(used, stmt.LoopItems)
			markRefs(used, stmt.SwitchExpr)
			markRefs(used, stmt.Message)
			markRefs(used, stmt.BuiltinArgs)
			for _, c := range stmt.Cases {
				for _, v := range c.Values {
					markRefs(used, v)
				}
			}
		}
	}
	referenced := make(map[string]bool)
	for _, cmd := range data.Commands {
		if cmd.IsDefault {
			referenced[cmd.Name] = true
		}
		for _, p := range cmd.Prereqs {
			referenced[p] = true
		}
	}
	hadWarning := false
	for _, v := range data.Variables {
		if v.Scope == "global" && !used[v.Name] {
			fmt.Printf("[warn] global variable %q is never referenced\n", v.Name)
			hadWarning = true
		}
	}
	for _, cmd := range data.Commands {
		if cmd.Name == "_" || cmd.Manual || pkg.IsLazyName(cmd.Name) {
			continue
		}
		if !referenced[cmd.Name] {
			fmt.Printf("[info] command %q is never referenced (not a prerequisite, invoke target, or default)\n", cmd.Name)
			hadWarning = true
		}
	}
	if hadWarning && problems == 0 {
		fmt.Println("(warnings above are informational)")
	}
	if problems > 0 {
		return errDoctorFailed
	}
	fmt.Println("no problems found")
	return nil
}

func collectAllStatements(body []pkg.BodyStatement) []pkg.BodyStatement {
	var out []pkg.BodyStatement
	for _, stmt := range body {
		out = append(out, stmt)
		switch stmt.Type {
		case pkg.StmtIf:
			out = append(out, collectAllStatements(stmt.ThenBody)...)
			out = append(out, collectAllStatements(stmt.ElseBody)...)
		case pkg.StmtFor:
			out = append(out, collectAllStatements(stmt.LoopBody)...)
		case pkg.StmtOnFail:
			out = append(out, collectAllStatements(stmt.OnFailBody)...)
		case pkg.StmtSwitch:
			for _, c := range stmt.Cases {
				out = append(out, collectAllStatements(c.Body)...)
			}
		case pkg.StmtInDir, pkg.StmtLock:
			out = append(out, collectAllStatements(stmt.ThenBody)...)
		}
	}
	return out
}

func markRefs(used map[string]bool, s string) {
	for _, name := range pkg.VarRefNames(s) {
		used[name] = true
	}
}
