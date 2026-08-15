package main

import (
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"github.com/nicklvsa/construct/pkg"
)

func runStats(o *options, inputs *ConstructInput) error {
	dir := filepath.Join(filepath.Dir(inputs.FileName), ".construct-cache")
	hist := pkg.LoadRunHistory(dir)
	if len(hist) == 0 {
		fmt.Println("no run records yet (run a build first)")
		return nil
	}
	names := make([]string, 0, len(hist))
	for n := range hist {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		return sumMs(hist[names[i]]) > sumMs(hist[names[j]])
	})
	fmt.Printf("%-20s %5s %10s %10s %10s %s\n", "command", "runs", "avg", "last", "total", "last status")
	for _, n := range names {
		recs := hist[n]
		var total int64
		for _, r := range recs {
			total += r.DurationMs
		}
		last := recs[len(recs)-1]
		avg := total / int64(len(recs))
		fmt.Printf("%-20s %5d %10s %10s %10s %s\n", n, len(recs), durMs(avg), durMs(last.DurationMs), durMs(total), last.Status)
	}
	return nil
}

func sumMs(recs []pkg.RunRecord) int64 {
	var total int64
	for _, r := range recs {
		total += r.DurationMs
	}
	return total
}

func durMs(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return (time.Duration(ms) * time.Millisecond).Round(time.Millisecond).String()
}
