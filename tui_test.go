package main

import (
	"strings"
	"testing"

	"github.com/nicklvsa/construct/pkg"
)

func TestRingBuf(t *testing.T) {
	r := &ringBuf{max: 3}
	r.write([]byte("alpha\nbeta\n"))
	if got := strings.Join(r.tail(10), "|"); got != "alpha|beta" {
		t.Errorf("lines = %q", got)
	}
	r.write([]byte("gam"))
	r.write([]byte("ma\n"))
	if got := strings.Join(r.tail(10), "|"); got != "alpha|beta|gamma" {
		t.Errorf("lines = %q", got)
	}
	r.write([]byte("delta\nepsilon\nzeta\n"))
	if got := strings.Join(r.tail(10), "|"); got != "delta|epsilon|zeta" {
		t.Errorf("cap not applied: %q", got)
	}
	if got := strings.Join(r.tail(2), "|"); got != "epsilon|zeta" {
		t.Errorf("tail(2) = %q", got)
	}
}

func TestDashboardObserverTransitions(t *testing.T) {
	d := newDashboard(nil, func() {})
	d.CommandStarted("gen")
	d.CommandFinished("gen", pkg.RunRecord{Status: "ok", DurationMs: 42})
	d.CommandStarted("build")
	d.CommandFinished("build", pkg.RunRecord{Status: "failed", Exit: 3, Error: "boom"})

	if len(d.rows) != 2 {
		t.Fatalf("rows = %d", len(d.rows))
	}
	if d.rows[0].status != dashOK || d.rows[0].dur != 42*1e6 {
		t.Errorf("gen row = %+v", d.rows[0])
	}
	if d.rows[1].status != dashFailed || d.rows[1].exit != 3 || d.rows[1].errMsg != "boom" {
		t.Errorf("build row = %+v", d.rows[1])
	}
}

func TestDashboardOutputWriter(t *testing.T) {
	d := newDashboard(nil, func() {})
	w := d.OutputWriter("gen")
	w.Write([]byte("line-one\nline-"))
	w.Write([]byte("two\n"))
	buf := d.bufs["gen"]
	if buf == nil {
		t.Fatal("no buffer for gen")
	}
	if got := strings.Join(buf.tail(10), "|"); got != "line-one|line-two" {
		t.Errorf("captured = %q", got)
	}
}

func TestDashboardRender(t *testing.T) {
	d := newDashboard(nil, func() {})
	d.CommandStarted("gen")
	d.CommandFinished("gen", pkg.RunRecord{Status: "ok", DurationMs: 5})
	d.CommandStarted("build")
	w := d.OutputWriter("build")
	w.Write([]byte("compiling main.go\n"))

	frame := d.render(80, 24)
	for _, want := range []string{"construct — ", "gen", "build", "compiling main.go", "q detach"} {
		if !strings.Contains(frame, want) {
			t.Errorf("frame missing %q:\n%s", want, frame)
		}
	}

	small := d.render(20, 6)
	if small == "" {
		t.Error("small render empty")
	}
}

func TestDashboardKeys(t *testing.T) {
	d := newDashboard(nil, func() {})
	d.CommandStarted("a")
	d.CommandStarted("b")
	if d.sel != 1 {
		t.Fatalf("sel = %d, want 1 (follow)", d.sel)
	}
	d.key('k')
	if d.sel != 0 || d.follow {
		t.Errorf("sel = %d follow = %v", d.sel, d.follow)
	}
	d.key('j')
	if d.sel != 1 {
		t.Errorf("sel = %d after j", d.sel)
	}
	d.key('f')
	if !d.follow {
		t.Error("f should re-enable follow")
	}
}
