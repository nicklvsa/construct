package main

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nicklvsa/construct/pkg"
	"golang.org/x/term"
)

const flameBlocks = " ▏▎▍▌▋▊▉█"

var flameBlockRunes = []rune(flameBlocks)

// flameHeatRamp is a cool-to-hot 256-color gradient; longer spans land hotter.
var flameHeatRamp = []int{61, 67, 68, 74, 73, 79, 78, 84, 83, 82, 118, 154, 148, 190, 184, 226, 220, 214, 208, 202, 196}

func heatColor(share float64) int {
	idx := int(share*float64(len(flameHeatRamp)-1) + 0.5)
	return flameHeatRamp[min(max(idx, 0), len(flameHeatRamp)-1)]
}

func flameBar(offset, share float64, width int, failed, color bool) string {
	cells := make([]rune, width)
	for i := range cells {
		cells[i] = ' '
	}
	startCell := min(int(offset*float64(width)), width-1)
	if startCell < 0 {
		startCell = 0
	}
	fill := share * float64(width)
	full := int(fill)
	for i := 0; i < full && startCell+i < width; i++ {
		cells[startCell+i] = '█'
	}
	if frac := fill - float64(full); frac >= 0.125 && startCell+full < width {
		cells[startCell+full] = flameBlockRunes[int(frac*8+0.5)]
	} else if full == 0 && fill > 0 && startCell < width {
		cells[startCell] = flameBlockRunes[1] // tiny span: keep a visible sliver
	}
	bar := string(cells)
	if !color {
		return bar
	}
	if failed {
		return "\x1b[1;91m" + bar + "\x1b[0m"
	}
	return fmt.Sprintf("\x1b[38;5;%dm%s\x1b[0m", heatColor(share), bar)
}

func flameDur(d time.Duration) string {
	switch {
	case d >= time.Second:
		return d.Round(10 * time.Millisecond).String()
	case d >= time.Millisecond:
		return d.Round(100 * time.Microsecond).String()
	default:
		return d.Round(10 * time.Microsecond).String()
	}
}

func renderFlame(rows []pkg.FlameRow) {
	if len(rows) == 0 {
		return
	}

	sorted := slices.Clone(rows)
	slices.SortStableFunc(sorted, func(a, b pkg.FlameRow) int { return a.Start.Compare(b.Start) })

	start, end := sorted[0].Start, sorted[0].End
	for _, r := range sorted {
		if r.Start.Before(start) {
			start = r.Start
		}
		if r.End.After(end) {
			end = r.End
		}
	}
	total := end.Sub(start)
	if total <= 0 {
		total = time.Millisecond
	}

	width := 100
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 80 {
		width = w - 4
	}

	labelW := min(width/3, 34)
	barW := max(width-labelW-19, 20)
	rowW := labelW + barW + 19
	useColor := os.Getenv("NO_COLOR") == "" && term.IsTerminal(int(os.Stdout.Fd()))

	fmt.Printf("\n flame · %d statement(s) · %s total\n", len(sorted), flameDur(total))
	sep := strings.Repeat("─", rowW)
	if useColor {
		sep = "\x1b[2m" + sep + "\x1b[0m"
	}
	fmt.Println(sep)
	for _, r := range sorted {
		dur := r.End.Sub(r.Start)
		share := float64(dur) / float64(total)

		label := strings.Repeat("  ", r.Depth) + r.Label
		if utf8.RuneCountInString(label) > labelW {
			label = string([]rune(label)[:labelW-1]) + "…"
		}
		mark := " "
		labelColor, labelReset := "", ""
		if r.Failed {
			mark = "✗"
			if useColor {
				labelColor, labelReset = "\x1b[1;91m", "\x1b[0m"
			}
		}

		pad := strings.Repeat(" ", max(labelW-utf8.RuneCountInString(label), 0))
		bar := flameBar(float64(r.Start.Sub(start))/float64(total), share, barW, r.Failed, useColor)

		fmt.Printf("%s%s%s%s%s %s %5.1f%% %9s\n",
			labelColor, mark, labelReset, label, pad, bar, share*100, flameDur(dur))
	}
}
