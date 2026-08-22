package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/nicklvsa/construct/pkg"
)

type learnReport struct {
	Mode       string             `json:"mode"`
	Note       string             `json:"note,omitempty"`
	Commands   []learnCommandInfo `json:"commands,omitempty"`
	Unwatched  []string           `json:"unwatched,omitempty"`
	ReadsTotal int                `json:"reads_total,omitempty"`
	ReadsRepo  int                `json:"reads_repo,omitempty"`
}

type learnCommandInfo struct {
	Name      string   `json:"name"`
	FileDeps  []string `json:"file_deps,omitempty"`
	Uncovered []string `json:"uncovered,omitempty"`
}

func runLearn(args []string, o *options) error {
	fileName, targets := splitConstfileArgs(args)
	p, err := pkg.NewParser(fileName)
	if err != nil {
		return err
	}
	data, err := p.Parse()
	if err != nil {
		return err
	}
	baseDir := filepath.Dir(fileName)
	absBase, err := filepath.Abs(baseDir)
	if err != nil {
		return err
	}

	if stracePath := findStrace(); stracePath != "" {
		return learnTraced(stracePath, exePath(), fileName, absBase, targets, data, o)
	}
	return learnStatic(absBase, data, o)
}

func findStrace() string {
	if runtime.GOOS != "linux" {
		return ""
	}
	if p, err := exec.LookPath("strace"); err == nil {
		return p
	}
	return ""
}

func exePath() string {
	exe, err := os.Executable()
	if err != nil {
		return os.Args[0]
	}
	return exe
}

func learnTraced(stracePath, exe, fileName, absBase string, targets []string, data *pkg.ParsedData, o *options) error {
	tmp, err := os.CreateTemp("", "construct-learn-*.strace")
	if err != nil {
		return err
	}
	traceFile := tmp.Name()
	tmp.Close()
	defer os.Remove(traceFile)

	childArgs := []string{}
	if fileName != defaultConstfileName() {
		childArgs = append(childArgs, fileName)
	}
	childArgs = append(childArgs, targets...)

	fmt.Printf("(learn: running under strace — slower than a normal run)\n")
	runStart := time.Now()
	cmd := exec.Command(stracePath, append([]string{"-f", "-q", "-e", "trace=%file", "-o", traceFile, exe}, childArgs...)...)
	cmd.Dir = absBase
	cmd.Env = os.Environ()
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	runErr := cmd.Run()
	fmt.Println()

	hist := pkg.LoadRunHistory(filepath.Join(absBase, pkg.CacheDirName()))
	traceData, err := os.ReadFile(traceFile)
	if err != nil {
		return fmt.Errorf("could not read strace output %s: %w", traceFile, err)
	}
	reads := parseStraceReads(traceData, absBase)
	repoReads := filterRepoReads(reads, absBase, data)

	report := learnReport{Mode: "traced", ReadsTotal: len(reads), ReadsRepo: len(repoReads)}
	for _, cmdDef := range data.Commands {
		if pkg.IsLazyName(cmdDef.Name) {
			continue
		}
		recs := hist[cmdDef.Name]
		ran := false
		for _, r := range recs {
			if r.End.After(runStart) && r.Status != "skipped" {
				ran = true
				break
			}
		}
		if !ran {
			continue
		}
		info := learnCommandInfo{Name: cmdDef.Name, FileDeps: cmdDef.FileDeps, Uncovered: uncoveredFor(cmdDef, repoReads, absBase)}
		report.Commands = append(report.Commands, info)
	}

	if o.json {
		if err := printJSON(report); err != nil {
			return err
		}

		return runErr
	}

	printTraced(report)
	return runErr
}

func printTraced(report learnReport) {
	fmt.Printf("traced %d file read(s), %d within the repo\n\n", report.ReadsTotal, report.ReadsRepo)
	for _, c := range report.Commands {
		if len(c.Uncovered) == 0 {
			fmt.Printf("%s: all reads covered by its file deps\n", c.Name)
			continue
		}

		deps := strings.Join(c.FileDeps, ", ")
		if deps == "" {
			deps = "(none declared)"
		}

		fmt.Printf("%s: reads not covered by its file deps (%s):\n", c.Name, deps)
		for _, f := range c.Uncovered {
			fmt.Printf("  %s\n", f)
		}

		fmt.Printf("  -> changing these will NOT rerun %s; add them to its deps\n\n", c.Name)
	}
}

func learnStatic(absBase string, data *pkg.ParsedData, o *options) error {
	unwatched, err := pkg.FilesNotWatched(data, absBase)
	if err != nil {
		return err
	}

	for i, f := range unwatched {
		if rel, err := filepath.Rel(absBase, f); err == nil {
			unwatched[i] = rel
		}
	}

	sort.Strings(unwatched)

	report := learnReport{
		Mode:      "static",
		Note:      "no tracer available; showing files no command watches (on Linux with strace, learn traces actual reads)",
		Unwatched: unwatched,
	}

	if o.json {
		return printJSON(report)
	}

	fmt.Printf("(learn: %s)\n", report.Note)
	if len(unwatched) == 0 {
		fmt.Println("every file in the repo is watched by some command's deps")
		return nil
	}

	fmt.Printf("%d file(s) no command watches (editing them triggers nothing):\n", len(unwatched))
	limit := 50

	for _, f := range unwatched {
		if limit == 0 {
			fmt.Printf("  ... and %d more\n", len(unwatched)-50)
			break
		}
		limit--
		fmt.Printf("  %s\n", f)
	}

	return nil
}

func printJSON(v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}

func uncoveredFor(cmd *pkg.Command, repoReads []string, absBase string) []string {
	covered := map[string]bool{}
	for _, f := range expandDepsIn(cmd.FileDeps, absBase) {
		covered[f] = true
	}
	var out []string
	for _, f := range repoReads {
		if !covered[f] {
			out = append(out, f)
		}
	}
	return out
}

func expandDepsIn(patterns []string, absBase string) []string {
	var out []string
	for _, pattern := range patterns {
		full := filepath.Join(absBase, pattern)
		matches, err := filepath.Glob(full)
		if err != nil || len(matches) == 0 {
			matches = []string{full}
		}
		for _, m := range matches {
			if rel, err := filepath.Rel(absBase, m); err == nil {
				out = append(out, rel)
			}
		}
	}
	return out
}

func filterRepoReads(reads map[string]bool, absBase string, data *pkg.ParsedData) []string {
	produced := map[string]bool{}
	for _, cmd := range data.Commands {
		for _, f := range expandDepsIn(cmd.Produces, absBase) {
			produced[f] = true
		}
	}
	var out []string
	for p := range reads {
		rel, err := filepath.Rel(absBase, p)
		if err != nil || strings.HasPrefix(rel, "..") {
			continue
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, ".git/") || strings.HasPrefix(rel, pkg.CacheDirName()+"/") || produced[rel] {
			continue
		}
		out = append(out, rel)
	}
	sort.Strings(out)
	return out
}

func parseStraceReads(data []byte, absBase string) map[string]bool {
	reads := map[string]bool{}
	for line := range strings.SplitSeq(string(data), "\n") {
		rest := strings.TrimSpace(line)
		if sp := strings.IndexByte(rest, ' '); sp > 0 {
			rest = strings.TrimSpace(rest[sp+1:])
		} else {
			continue
		}

		var path string
		switch {
		case strings.HasPrefix(rest, "openat(") || strings.HasPrefix(rest, "open("):
			if !strings.Contains(rest, "O_RDONLY") && !strings.Contains(rest, "O_RDWR") {
				continue
			}
			path = firstQuoted(rest)
		case strings.HasPrefix(rest, "execve("):
			path = firstQuoted(rest)
		default:
			continue
		}

		if path == "" {
			continue
		}

		if !strings.HasPrefix(path, "/") && filepath.VolumeName(path) == "" {
			path = filepath.Join(absBase, path)
		}

		eq := strings.LastIndex(rest, ") = ")
		if eq < 0 || strings.HasPrefix(rest[eq+4:], "-1") {
			continue
		}

		reads[path] = true
	}

	return reads
}

func firstQuoted(s string) string {
	_, after, ok := strings.Cut(s, "\"")
	if !ok {
		return ""
	}
	rest := after
	before, _, ok := strings.Cut(rest, "\"")
	if !ok {
		return ""
	}
	return before
}
