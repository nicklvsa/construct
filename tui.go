package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/nicklvsa/construct/pkg"
	"golang.org/x/term"
)

const dashRefresh = 100 * time.Millisecond

type dashStatus int

const (
	dashPending dashStatus = iota
	dashRunning
	dashOK
	dashFailed
	dashSkipped
)

type dashRow struct {
	name   string
	status dashStatus
	start  time.Time
	dur    time.Duration
	exit   int
	errMsg string
}

type ringBuf struct {
	lines []string
	part  []byte
	max   int
}

func (r *ringBuf) write(p []byte) {
	data := append(r.part, p...)
	r.part = nil
	for {
		i := indexByte(data, '\n')
		if i < 0 {
			break
		}
		r.push(string(data[:i]))
		data = data[i+1:]
	}
	if len(data) > 0 {
		r.part = data
	}
}

func (r *ringBuf) push(line string) {
	r.lines = append(r.lines, line)
	if len(r.lines) > r.max {
		r.lines = r.lines[len(r.lines)-r.max:]
	}
}

func (r *ringBuf) tail(n int) []string {
	if len(r.lines) <= n {
		return r.lines
	}
	return r.lines[len(r.lines)-n:]
}

func indexByte(b []byte, c byte) int {
	for i := range b {
		if b[i] == c {
			return i
		}
	}
	return -1
}

type dashboard struct {
	mu          sync.Mutex
	rows        []*dashRow
	byName      map[string]*dashRow
	bufs        map[string]*ringBuf
	sel         int
	follow      bool
	began       time.Time
	frame       int
	executor    *pkg.Executor
	cancel      context.CancelFunc
	done        chan struct{}
	stopOnce    sync.Once
	restoreTerm func()
}

func newDashboard(executor *pkg.Executor, cancel context.CancelFunc) *dashboard {
	return &dashboard{
		byName:   map[string]*dashRow{},
		bufs:     map[string]*ringBuf{},
		follow:   true,
		began:    time.Now(),
		executor: executor,
		cancel:   cancel,
		done:     make(chan struct{}),
	}
}

func (d *dashboard) row(name string) *dashRow {
	if r, ok := d.byName[name]; ok {
		return r
	}
	r := &dashRow{name: name, status: dashPending}
	d.rows = append(d.rows, r)
	d.byName[name] = r
	d.bufs[name] = &ringBuf{max: 256}
	return r
}

func (d *dashboard) CommandStarted(name string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	r := d.row(name)
	r.status = dashRunning
	r.start = time.Now()
	if d.follow {
		d.sel = len(d.rows) - 1
	}
}

func (d *dashboard) CommandFinished(name string, rec pkg.RunRecord) {
	d.mu.Lock()
	defer d.mu.Unlock()
	r := d.row(name)
	switch rec.Status {
	case "ok":
		r.status = dashOK
	case "failed":
		r.status = dashFailed
		r.errMsg = rec.Error
	default:
		r.status = dashSkipped
	}
	r.exit = rec.Exit
	r.dur = time.Duration(rec.DurationMs) * time.Millisecond
}

func (d *dashboard) OutputWriter(name string) io.Writer {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.row(name)
	return bufWriter{d: d, buf: d.bufs[name]}
}

type bufWriter struct {
	d   *dashboard
	buf *ringBuf
}

func (b bufWriter) Write(p []byte) (int, error) {
	b.d.mu.Lock()
	b.buf.write(p)
	b.d.mu.Unlock()
	return len(p), nil
}

var dashSpinFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func (d *dashboard) glyph(r *dashRow) string {
	switch r.status {
	case dashPending:
		return "○"
	case dashRunning:
		return dashSpinFrames[d.frame%len(dashSpinFrames)]
	case dashOK:
		return "✔"
	case dashFailed:
		return "✘"
	default:
		return "⏭"
	}
}

func (d *dashboard) statusText(r *dashRow) string {
	switch r.status {
	case dashRunning:
		return "running " + trimDuration(time.Since(r.start))
	case dashOK, dashFailed, dashSkipped:
		if r.dur > 0 {
			s := trimDuration(r.dur)
			if r.status == dashFailed && r.exit != 0 {
				s += fmt.Sprintf(" (exit %d)", r.exit)
			}
			return s
		}
	}
	return ""
}

func (d *dashboard) render(w, h int) string {
	d.frame++
	var b strings.Builder

	done := 0
	for _, r := range d.rows {
		if r.status != dashPending && r.status != dashRunning {
			done++
		}
	}
	b.WriteString(fmt.Sprintf("construct — %d/%d done — %s", done, len(d.rows), trimDuration(time.Since(d.began))))
	b.WriteString("\r\n")

	listH := h - 6
	if listH < 1 {
		listH = 1
	}
	visible := d.rows
	offset := 0
	if len(visible) > listH {
		offset = d.sel - listH + 1
		if offset < 0 {
			offset = 0
		}
		if offset > len(visible)-listH {
			offset = len(visible) - listH
		}
		visible = visible[offset : offset+listH]
	}
	for i, r := range visible {
		marker := " "
		if offset+i == d.sel {
			marker = ">"
		}
		name := r.name
		if len(name) > 24 {
			name = name[:21] + "..."
		}
		line := fmt.Sprintf("%s %s %-24s", marker, d.glyph(r), name)
		if st := d.statusText(r); st != "" {
			line += st
		}
		b.WriteString(line + "\r\n")
	}

	if w > 0 {
		b.WriteString(strings.Repeat("─", minInt(w, 72)) + "\r\n")
	}

	logH := h - len(visible) - 4
	if logH < 1 {
		logH = 1
	}
	var lines []string
	if d.sel >= 0 && d.sel < len(d.rows) {
		if buf := d.bufs[d.rows[d.sel].name]; buf != nil {
			lines = buf.tail(logH)
		}
	}
	for _, l := range lines {
		l = strings.ReplaceAll(l, "\r", "")
		if w > 2 && len(l) > w-2 {
			l = l[:w-2]
		}
		b.WriteString(l + "\r\n")
	}
	for i := len(lines); i < logH; i++ {
		b.WriteString("\r\n")
	}
	b.WriteString("j/k select · f follow · q detach · Ctrl-C cancel")
	return b.String()
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// start takes over the terminal until the dashboard is detached or stopped.
func (d *dashboard) start() {
	old, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		d.mu.Lock()
		d.detachLocked()
		d.mu.Unlock()
		return
	}
	fmt.Print("\x1b[?1049h\x1b[?25l")
	d.mu.Lock()
	d.restoreTerm = func() {
		fmt.Print("\x1b[?25h\x1b[?1049l")
		term.Restore(int(os.Stdin.Fd()), old)
	}
	d.mu.Unlock()

	keys := make(chan byte, 32)
	go func() {
		buf := make([]byte, 8)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				keys <- buf[0]
			}
			if err != nil {
				close(keys)
				return
			}
		}
	}()

	ticker := time.NewTicker(dashRefresh)
	defer ticker.Stop()
	lastW, lastH := termSize(80, 24)
	for {
		select {
		case <-d.done:
			return
		case k, ok := <-keys:
			if !ok {
				return
			}
			d.key(k)
		case <-ticker.C:
			w, h := termSize(lastW, lastH)
			lastW, lastH = w, h
			d.mu.Lock()
			frame := d.render(w, h)
			d.mu.Unlock()
			fmt.Print("\x1b[H\x1b[2J" + frame)
		}
	}
}

func (d *dashboard) key(k byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	switch k {
	case 'j':
		d.follow = false
		if d.sel < len(d.rows)-1 {
			d.sel++
		}
	case 'k':
		d.follow = false
		if d.sel > 0 {
			d.sel--
		}
	case 'f':
		d.follow = true
		if len(d.rows) > 0 {
			d.sel = len(d.rows) - 1
		}
	case 'q':
		d.detachLocked()
	case 0x03:
		d.cancel()
	}
}

// detachLocked restores normal output while the build keeps running.
func (d *dashboard) detachLocked() {
	d.stopOnce.Do(func() {
		if d.restoreTerm != nil {
			d.restoreTerm()
			d.restoreTerm = nil
		}
		d.executor.SetObserver(nil)
		close(d.done)
	})
}

func (d *dashboard) stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.detachLocked()

	for _, r := range d.rows {
		if r.status != dashFailed {
			continue
		}
		fmt.Println()
		fmt.Printf("--- %s failed ---", r.name)
		if r.errMsg != "" {
			fmt.Println()
			fmt.Println(r.errMsg)
		}
		if buf := d.bufs[r.name]; buf != nil && len(buf.lines) > 0 {
			fmt.Println()
			for _, l := range buf.tail(10) {
				fmt.Println(l)
			}
		}
		fmt.Println()
	}
}
