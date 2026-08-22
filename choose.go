package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nicklvsa/construct/pkg"
	"golang.org/x/term"
)

type chooseItem struct {
	name string
	desc string
	def  bool
}

type chooseState struct {
	all      []chooseItem
	filter   []rune
	cursor   int
	offset   int
	selected map[string]bool
}

func newChooseState(all []chooseItem) *chooseState {
	return &chooseState{all: all, selected: make(map[string]bool)}
}

func (s *chooseState) filtered() []chooseItem {
	if len(s.filter) == 0 {
		return s.all
	}
	prefix := strings.ToLower(string(s.filter))
	var out []chooseItem
	for _, it := range s.all {
		if strings.HasPrefix(strings.ToLower(it.name), prefix) {
			out = append(out, it)
		}
	}
	return out
}

func (s *chooseState) move(delta int) {
	if n := len(s.filtered()); n > 0 {
		s.cursor += delta
		if s.cursor < 0 {
			s.cursor = 0
		} else if s.cursor >= n {
			s.cursor = n - 1
		}
	}
}

func (s *chooseState) moveHome() { s.cursor = 0 }
func (s *chooseState) moveEnd() {
	if n := len(s.filtered()); n > 0 {
		s.cursor = n - 1
	}
}

func (s *chooseState) toggle() {
	vis := s.filtered()
	if len(vis) == 0 {
		return
	}
	name := vis[s.cursor].name
	if s.selected[name] {
		delete(s.selected, name)
	} else {
		s.selected[name] = true
	}
	s.move(1)
}

func (s *chooseState) selectAll() {
	for _, it := range s.filtered() {
		s.selected[it.name] = true
	}
}

func (s *chooseState) typeRune(r rune) {
	s.filter = append(s.filter, r)
	s.cursor = 0
}

func (s *chooseState) backspace() {
	if len(s.filter) > 0 {
		s.filter = s.filter[:len(s.filter)-1]
		s.cursor = 0
	}
}

func (s *chooseState) clearFilter() {
	s.filter = nil
	s.cursor = 0
}

func (s *chooseState) selectedNames() []string {
	var out []string
	for _, it := range s.all {
		if s.selected[it.name] {
			out = append(out, it.name)
		}
	}
	return out
}

type chooserKey int

const (
	keyNone chooserKey = iota
	keyUp
	keyDown
	keyHome
	keyEnd
	keySpace
	keyEnter
	keyBackspace
	keyQuit
	keyClear
	keySelectAll
	keyRune
)

func decodeChooserKey(buf []byte) (chooserKey, rune) {
	if len(buf) == 0 {
		return keyNone, 0
	}

	if len(buf) >= 3 && buf[0] == '\x1b' && buf[1] == '[' {
		switch buf[2] {
		case 'A':
			return keyUp, 0
		case 'B':
			return keyDown, 0
		case 'H':
			return keyHome, 0
		case 'F':
			return keyEnd, 0
		}
		return keyNone, 0
	}
	switch buf[0] {
	case 0x03, 0x1b: // ctrl-c, escape
		return keyQuit, 0
	case '\r', '\n':
		return keyEnter, 0
	case ' ':
		return keySpace, 0
	case 0x7f, 0x08: // backspace
		return keyBackspace, 0
	case 0x15: // ctrl-u: clear filter
		return keyClear, 0
	case 0x01: // ctrl-a: select all visible
		return keySelectAll, 0
	}
	r, _ := utf8.DecodeRune(buf)
	if unicode.IsPrint(r) {
		return keyRune, r
	}
	return keyNone, 0
}

func renderChooser(s *chooseState, width, height int) string {
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}
	vis := s.filtered()

	listH := max(height-3, 1)
	n := len(vis)
	if n == 0 {
		s.offset = 0
	} else {
		switch {
		case s.cursor < s.offset:
			s.offset = s.cursor
		case s.cursor >= s.offset+listH:
			s.offset = s.cursor - listH + 1
		}
		s.offset = min(max(s.offset, 0), max(n-listH, 0))
	}

	var b strings.Builder
	b.WriteString("\x1b[H\x1b[2J") // home + clear screen
	b.WriteString("Select targets — ↑/↓ move, space toggle, type to filter, enter run, esc quit:\r\n")

	for i := s.offset; i < n && i < s.offset+listH; i++ {
		it := vis[i]
		cur, mark := "  ", "  "
		if i == s.cursor {
			cur = "> "
		}
		if s.selected[it.name] {
			mark = "[x]"
		} else {
			mark = "[ ]"
		}
		name := truncate(it.name, max(width-10, 1))
		line := fmt.Sprintf("%s %s %s", cur, mark, name)
		if it.def {
			line += " (default)"
		}
		if it.desc != "" {
			if avail := width - utf8.RuneCountInString(line) - 5; avail > 4 {
				line += " — " + truncate(it.desc, avail)
			}
		}
		b.WriteString(line)
		b.WriteString("\r\n")
	}
	fmt.Fprintf(&b, "filter: %s\r\n[%d/%d selected]", string(s.filter), len(s.selected), len(s.all))
	return b.String()
}

func truncate(s string, n int) string {
	if n < 1 {
		return ""
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "..."
}

func termSize(lastW, lastH int) (int, int) {
	w, h, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || w <= 0 || h <= 0 {
		return lastW, lastH
	}
	return w, h
}

func chooseTargets(data *pkg.ParsedData) ([]string, error) {
	var items []chooseItem
	for _, cmd := range data.Commands {
		if cmd.Name == "_" || pkg.IsLazyName(cmd.Name) {
			continue
		}
		desc := ""
		if cmd.Description != "" {
			desc = strings.Split(cmd.Description, "\n")[0]
		}
		items = append(items, chooseItem{name: cmd.Name, desc: desc, def: cmd.IsDefault})
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("no commands to choose from")
	}

	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return chooseTargetsLine(items, data)
	}

	state := newChooseState(items)
	width, height := termSize(80, 24)

	old, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return nil, fmt.Errorf("failed to enter raw mode: %w", err)
	}

	enableANSI(os.Stdout)
	fmt.Print("\x1b[?1049h")
	defer func() {
		fmt.Print("\x1b[?1049l")
		term.Restore(int(os.Stdin.Fd()), old)
	}()

	render := func() {
		width, height = termSize(width, height)
		fmt.Print(renderChooser(state, width, height))
	}

	render()
	for {
		var buf [8]byte
		n, err := os.Stdin.Read(buf[:])
		if err != nil {
			return nil, err
		}
		key, r := decodeChooserKey(buf[:n])
		switch key {
		case keyUp:
			state.move(-1)
		case keyDown:
			state.move(1)
		case keyHome:
			state.moveHome()
		case keyEnd:
			state.moveEnd()
		case keySpace:
			state.toggle()
		case keyClear:
			state.clearFilter()
		case keySelectAll:
			state.selectAll()
		case keyBackspace:
			state.backspace()
		case keyQuit:
			return nil, fmt.Errorf("selection aborted")
		case keyEnter:
			return state.selectedNames(), nil // empty selection => default command
		case keyRune:
			if r == 'q' && len(state.filter) == 0 {
				return nil, fmt.Errorf("selection aborted")
			}
			state.typeRune(r)
		}
		render()
	}
}

func chooseTargetsLine(items []chooseItem, data *pkg.ParsedData) ([]string, error) {
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Println("Select targets (numbers or names, comma/space separated; empty = default):")
		for i, it := range items {
			suffix := ""
			if it.def {
				suffix = " (default)"
			}
			if it.desc != "" {
				suffix += " — " + it.desc
			}
			fmt.Printf("  %d. %s%s\n", i+1, it.name, suffix)
		}
		fmt.Print("> ")
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}

		var selected []string
		invalid := false
		for _, tok := range strings.FieldsFunc(strings.TrimSpace(line), func(r rune) bool {
			return r == ',' || r == ' ' || r == '\t'
		}) {
			tok = strings.TrimSpace(tok)
			if tok == "" {
				continue
			}
			if idx, err := strconv.Atoi(tok); err == nil {
				if idx < 1 || idx > len(items) {
					fmt.Fprintf(os.Stderr, "invalid target %q (1-%d)\n", tok, len(items))
					invalid = true
					break
				}
				selected = append(selected, items[idx-1].name)
			} else if _, err := data.GetCommand(tok); err == nil {
				selected = append(selected, tok)
			} else {
				fmt.Fprintf(os.Stderr, "unknown target %q\n", tok)
				invalid = true
				break
			}
		}
		if invalid {
			continue
		}
		return selected, nil
	}
}
