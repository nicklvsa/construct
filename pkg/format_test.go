package pkg

import "testing"

func TestFormatConstfileIndentation(t *testing.T) {
	in := "var x = 1\ncmd {\n  if \"&x\" {\n    $ echo a\n  } else {\n    $ echo b\n  }\n}\n"
	want := "var x = 1\ncmd {\n    if \"&x\" {\n        $ echo a\n    } else {\n        $ echo b\n    }\n}\n"
	if got := FormatConstfile(in); got != want {
		t.Errorf("FormatConstfile:\n got: %q\nwant: %q", got, want)
	}
}

func TestFormatConstfileIdempotent(t *testing.T) {
	in := "cmd {\n\t$ echo tab-indented\n\tfor f in a, b {\n\t\t$ echo &f\n\t}\n}\n"
	once := FormatConstfile(in)
	twice := FormatConstfile(once)
	if once != twice {
		t.Errorf("not idempotent:\n once: %q\ntwice: %q", once, twice)
	}
	if once != "cmd {\n    $ echo tab-indented\n    for f in a, b {\n        $ echo &f\n    }\n}\n" {
		t.Errorf("unexpected output: %q", once)
	}
}

func TestFormatConstfilePreservesShellText(t *testing.T) {
	in := "cmd {\n      $ echo \"  spaced  ${var}  awk '{print}' \"\n}\n"
	got := FormatConstfile(in)
	want := "cmd {\n    $ echo \"  spaced  ${var}  awk '{print}' \"\n}\n"
	if got != want {
		t.Errorf("shell text modified:\n got: %q\nwant: %q", got, want)
	}
}

func TestFormatConstfileTrailingNewline(t *testing.T) {
	if got := FormatConstfile("cmd {\n  $ x\n}"); got != "cmd {\n    $ x\n}\n" {
		t.Errorf("missing trailing newline: %q", got)
	}
	if got := FormatConstfile("cmd {\n    $ x\n}\n\n\n"); got != "cmd {\n    $ x\n}\n" {
		t.Errorf("trailing blank lines not collapsed: %q", got)
	}
}

func TestNetBracesSkipsShellLines(t *testing.T) {
	if n := NetBraces(`$ awk '{print}'`); n != 0 {
		t.Errorf("shell line braces counted: %d", n)
	}
	if n := NetBraces(`if "x" {`); n != 1 {
		t.Errorf("if header = %d, want 1", n)
	}
	if n := NetBraces(`} else {`); n != 0 {
		t.Errorf("else line = %d, want 0", n)
	}
	if n := NetBraces(`$ echo "}"`); n != 0 {
		t.Errorf("brace in shell string counted: %d", n)
	}
}
