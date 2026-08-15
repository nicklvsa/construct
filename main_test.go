package main

import (
	flag "github.com/spf13/pflag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testItems() []chooseItem {
	return []chooseItem{
		{name: "clean"},
		{name: "build", desc: "Compiles everything"},
		{name: "install"},
		{name: "test"},
	}
}

func TestChooseStateFiltering(t *testing.T) {
	s := newChooseState(testItems())
	s.typeRune('b')
	if got := s.filtered(); len(got) != 1 || got[0].name != "build" {
		t.Errorf("filter 'b' = %v", got)
	}
	s.typeRune('u')
	if got := s.filtered(); len(got) != 1 || got[0].name != "build" {
		t.Errorf("filter 'bu' = %v", got)
	}
	s.typeRune('i')
	if got := s.filtered(); len(got) != 1 || got[0].name != "build" {
		t.Errorf("filter 'bui' = %v", got)
	}
	s.backspace()
	if got := s.filtered(); len(got) != 1 || got[0].name != "build" {
		t.Errorf("after backspace = %v", got)
	}
	s.clearFilter()
	if len(s.filtered()) != 4 {
		t.Errorf("after clear = %v", s.filtered())
	}
}

func TestChooseStateToggleAndSelection(t *testing.T) {
	s := newChooseState(testItems())
	s.toggle() // selects "clean", cursor moves to "build"
	s.toggle() // selects "build"
	got := s.selectedNames()
	if len(got) != 2 || got[0] != "clean" || got[1] != "build" {
		t.Errorf("selected = %v", got)
	}
	// Toggling again deselects.
	s.move(-2)
	s.toggle()
	if got := s.selectedNames(); len(got) != 1 || got[0] != "build" {
		t.Errorf("after untoggle = %v", got)
	}
}

func TestChooseStateSelectAllAndOrder(t *testing.T) {
	s := newChooseState(testItems())
	s.typeRune('b') // filter to just build
	s.selectAll()
	// Selection applies to filtered items, returned in original order.
	got := s.selectedNames()
	if len(got) != 1 || got[0] != "build" {
		t.Errorf("selected = %v", got)
	}
}

func TestChooseStateCursorClamp(t *testing.T) {
	s := newChooseState(testItems())
	s.moveHome()
	s.move(-5)
	if s.cursor != 0 {
		t.Errorf("cursor = %d, want 0", s.cursor)
	}
	s.moveEnd()
	s.move(5)
	if s.cursor != 3 {
		t.Errorf("cursor = %d, want 3", s.cursor)
	}
	// Narrowing the filter clamps the cursor into range.
	s.typeRune('x')
	if s.cursor != 0 {
		t.Errorf("cursor after empty filter = %d, want 0", s.cursor)
	}
}

func TestDecodeChooserKey(t *testing.T) {
	cases := []struct {
		in   []byte
		want chooserKey
	}{
		{[]byte{0x1b, '[', 'A'}, keyUp},
		{[]byte{0x1b, '[', 'B'}, keyDown},
		{[]byte{0x1b, '[', 'H'}, keyHome},
		{[]byte{0x1b, '[', 'F'}, keyEnd},
		{[]byte{' '}, keySpace},
		{[]byte{'\r'}, keyEnter},
		{[]byte{'\n'}, keyEnter},
		{[]byte{0x7f}, keyBackspace},
		{[]byte{0x08}, keyBackspace},
		{[]byte{0x1b}, keyQuit},
		{[]byte{0x03}, keyQuit},
		{[]byte{0x15}, keyClear},
		{[]byte{0x01}, keySelectAll},
		{[]byte{'g'}, keyRune},
		{[]byte{'é'}, keyRune},
		{[]byte{0x1b, '[', 'C'}, keyNone},
	}
	for _, tc := range cases {
		got, _ := decodeChooserKey(tc.in)
		if got != tc.want {
			t.Errorf("decode(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
	if _, r := decodeChooserKey([]byte("é")); r != 'é' {
		t.Errorf("rune decode = %q", r)
	}
}

func TestRenderChooser(t *testing.T) {
	s := newChooseState(testItems())
	s.typeRune('b')
	s.toggle() // select "build"
	out := renderChooser(s, 60, 10)
	if !strings.Contains(out, "[x] build") {
		t.Errorf("render missing selection state:\n%s", out)
	}
	if !strings.Contains(out, "[1/4 selected]") {
		t.Errorf("render missing count:\n%s", out)
	}
	if !strings.Contains(out, "Compiles everything") {
		t.Errorf("render missing description:\n%s", out)
	}
	if strings.Contains(out, "install") {
		t.Errorf("filter 'b' should hide install:\n%s", out)
	}
}

func TestRenderChooserNoBareLF(t *testing.T) {
	s := newChooseState(testItems())
	for i := range 3 {
		out := renderChooser(s, 60, 10)
		prev := byte(0)
		for j := 0; j < len(out); j++ {
			if out[j] == '\n' && prev != '\r' {
				t.Fatalf("bare \\n at byte %d in frame %d", j, i)
			}
			prev = out[j]
		}
		s.toggle()
	}
}

func TestNewFlagsParsing(t *testing.T) {
	var o options
	fs := flag.NewFlagSet("construct", flag.ContinueOnError)
	defineFlags(fs, &o)
	if err := fs.Parse([]string{"--no-cache", "-k", "--shell", "/bin/bash", "--env", "X=1"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !o.noCache || !o.keepGoing || o.shell != "/bin/bash" {
		t.Errorf("flags parsed wrong: %+v", o)
	}
	if len(o.overrides) != 1 || o.overrides[0] != "X=1" {
		t.Errorf("overrides = %v", o.overrides)
	}
}

func TestRunClean(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "src.txt"), []byte("x"), 0644)
	os.WriteFile(filepath.Join(dir, "out.txt"), []byte("y"), 0644)
	os.WriteFile(filepath.Join(dir, "Constfile"), []byte("build produces out.txt < src.txt {\n  cp src.txt out.txt\n}\n"), 0644)
	old, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(old)

	if err := runClean(nil, &options{}); err != nil {
		t.Fatalf("clean: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "out.txt")); !os.IsNotExist(err) {
		t.Error("produced file still exists after clean")
	}
	if _, err := os.Stat(filepath.Join(dir, "src.txt")); err != nil {
		t.Error("source dependency was removed by clean")
	}
}

func TestRunCleanRefusesOutsidePaths(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "precious.txt")
	os.WriteFile(outside, []byte("keep me"), 0644)
	os.WriteFile(filepath.Join(dir, "Constfile"), []byte("build produces ../"+filepath.Base(filepath.Dir(outside))+"/"+filepath.Base(outside)+" {\n  $ echo hi\n}\n"), 0644)
	old, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(old)

	if err := runClean(nil, &options{}); err != nil {
		t.Fatalf("clean: %v", err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Error("clean removed a file outside the Constfile directory")
	}
}

func TestRunGraph(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "Constfile"), []byte("gen {\n  $ echo hi\n}\nmain < gen, src.txt {\n  $ echo done\n}\n"), 0644)
	old, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(old)

	out := captureMainStdout(t, func() {
		if err := runGraph(nil, &options{}); err != nil {
			t.Errorf("graph: %v", err)
		}
	})
	for _, want := range []string{"main", "├── gen", "└── src.txt (file)"} {
		if !strings.Contains(out, want) {
			t.Errorf("graph output missing %q:\n%s", want, out)
		}
	}
}

func TestRunFmtCheck(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "Constfile")
	os.WriteFile(file, []byte("cmd {\n  $ echo hi\n}\n"), 0644)
	if err := runFmt([]string{file}, &options{checkFormat: true}); err == nil {
		t.Error("fmt --check should fail on unformatted file")
	}
	if err := runFmt([]string{file}, &options{}); err != nil {
		t.Fatalf("fmt: %v", err)
	}
	content, _ := os.ReadFile(file)
	if string(content) != "cmd {\n    $ echo hi\n}\n" {
		t.Errorf("formatted content = %q", content)
	}
	if err := runFmt([]string{file}, &options{checkFormat: true}); err != nil {
		t.Errorf("fmt --check should pass after formatting: %v", err)
	}
}

func TestRunCompletion(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish"} {
		out := captureMainStdout(t, func() {
			if err := runCompletion([]string{shell}, &options{}); err != nil {
				t.Errorf("completion %s: %v", shell, err)
			}
		})
		if !strings.Contains(out, "construct") || !strings.Contains(out, "__targets") {
			t.Errorf("completion %s missing dynamic command hook:\n%s", shell, out)
		}
	}
	if err := runCompletion([]string{"tcsh"}, &options{}); err == nil {
		t.Error("unknown shell should fail")
	}
}

func captureMainStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	return string(out)
}
