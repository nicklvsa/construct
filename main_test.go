package main

import (
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
