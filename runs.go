package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/nicklvsa/construct/pkg"
)

func runRuns(args []string, o *options) error {
	fileName, rest := splitConstfileArgs(args)
	hist := pkg.LoadRunHistory(filepath.Join(filepath.Dir(fileName), pkg.CacheDirName()))

	if len(rest) == 0 {
		return runsList(hist, o)
	}
	switch rest[0] {
	case "show":
		if len(rest) < 2 {
			return exitAt(2, "usage: construct runs [Constfile] show <command> [n]")
		}
		return runsShow(hist, rest[1], rest[2:], o)
	case "diff":
		if len(rest) < 2 {
			return exitAt(2, "usage: construct runs [Constfile] diff <command> [a] [b]")
		}
		return runsDiff(hist, rest[1], rest[2:])
	default:
		return exitAt(2, "usage: construct runs [Constfile] [show <command> [n] | diff <command> [a] [b]]")
	}
}

type runEntry struct {
	Name   string        `json:"name"`
	Index  int           `json:"index"` // 1 = most recent
	Record pkg.RunRecord `json:"record"`
}

func runsList(hist map[string][]pkg.RunRecord, o *options) error {
	if len(hist) == 0 {
		fmt.Println("no run records yet (run a build first)")
		return nil
	}
	var entries []runEntry
	for name, recs := range hist {
		if pkg.IsLazyName(name) {
			continue
		}
		for i, rec := range recs {
			entries = append(entries, runEntry{Name: name, Index: len(recs) - i, Record: rec})
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Record.End.After(entries[j].Record.End) })
	if len(entries) > 25 {
		entries = entries[:25]
	}

	if o.json {
		b, err := json.MarshalIndent(entries, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(b))
		return nil
	}

	fmt.Printf("%-20s %5s %8s %10s %10s  %s\n", "command", "run", "status", "exit", "duration", "finished")
	for _, e := range entries {
		exit := "-"
		if e.Record.Exit != 0 {
			exit = strconv.Itoa(e.Record.Exit)
		}
		when := e.Record.End.Format("2006-01-02 15:04:05")
		fmt.Printf("%-20s %5d %8s %10s %10s  %s\n", e.Name, e.Index, e.Record.Status, exit, durMs(e.Record.DurationMs), when)
	}
	return nil
}

// recordByIndex returns the nth-most-recent record (1 = latest).
func recordByIndex(recs []pkg.RunRecord, n int) (pkg.RunRecord, bool) {
	if n < 1 || n > len(recs) {
		return pkg.RunRecord{}, false
	}
	return recs[len(recs)-n], true
}

func recordIndexArg(args []string, pos int, def int) (int, error) {
	if len(args) <= pos || args[pos] == "" {
		return def, nil
	}
	n, err := strconv.Atoi(args[pos])
	if err != nil || n < 1 {
		return 0, exitAt(2, "invalid record index %q (1 = most recent)", args[pos])
	}
	return n, nil
}

func runsShow(hist map[string][]pkg.RunRecord, name string, args []string, o *options) error {
	recs := hist[name]
	if len(recs) == 0 {
		return exitAt(1, "no run records for %q", name)
	}
	n, err := recordIndexArg(args, 0, 1)
	if err != nil {
		return err
	}
	rec, ok := recordByIndex(recs, n)
	if !ok {
		return exitAt(1, "only %d record(s) for %q (1 = most recent)", len(recs), name)
	}

	if o.json {
		b, err := json.MarshalIndent(runEntry{Name: name, Index: n, Record: rec}, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(b))
		return nil
	}

	exit := ""
	if rec.Exit != 0 {
		exit = fmt.Sprintf(" (exit %d)", rec.Exit)
	}
	fmt.Printf("%s run %d: %s%s, %s, finished %s\n", name, n, rec.Status, exit, durMs(rec.DurationMs), rec.End.Format("2006-01-02 15:04:05"))
	if rec.Error != "" {
		fmt.Printf("error: %s\n", rec.Error)
	}
	if rec.Log == "" {
		fmt.Println("(no output captured)")
		return nil
	}
	fmt.Println()
	fmt.Print(rec.Log)
	if !strings.HasSuffix(rec.Log, "\n") {
		fmt.Println()
	}
	return nil
}

func runsDiff(hist map[string][]pkg.RunRecord, name string, args []string) error {
	recs := hist[name]
	if len(recs) == 0 {
		return exitAt(1, "no run records for %q", name)
	}
	aIdx, err := recordIndexArg(args, 0, 1)
	if err != nil {
		return err
	}
	bIdx, err := recordIndexArg(args, 1, 2)
	if err != nil {
		return err
	}
	a, ok := recordByIndex(recs, aIdx)
	if !ok {
		return exitAt(1, "only %d record(s) for %q", len(recs), name)
	}
	b, ok := recordByIndex(recs, bIdx)
	if !ok {
		return exitAt(1, "only %d record(s) for %q", len(recs), name)
	}
	if a.Log == "" && b.Log == "" {
		return exitAt(1, "neither record captured output (runs before log capture have none)")
	}

	fmt.Printf("diff of %s run %d (newer) vs run %d (older)\n", name, aIdx, bIdx)
	lines := diffLines(strings.Split(b.Log, "\n"), strings.Split(a.Log, "\n"))
	same := true
	for _, l := range lines {
		if strings.HasPrefix(l, "+") || strings.HasPrefix(l, "-") {
			same = false
		}
		fmt.Println(l)
	}
	if same {
		fmt.Println("(logs identical)")
	}
	return nil
}

// diffLines is a small LCS line diff; inputs are expected to be log-sized,
// and pathological cases fall back to a coarse whole-file replacement.
func diffLines(old, new []string) []string {
	if len(old)*len(new) > 4_000_000 {
		return []string{
			"- " + strings.Join(old, "\n"),
			"+ " + strings.Join(new, "\n"),
		}
	}
	n, m := len(old), len(new)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if old[i] == new[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var out []string
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case old[i] == new[j]:
			out = append(out, "  "+old[i])
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			out = append(out, "- "+old[i])
			i++
		default:
			out = append(out, "+ "+new[j])
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, "- "+old[i])
	}
	for ; j < m; j++ {
		out = append(out, "+ "+new[j])
	}
	return out
}
