package pkg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestModifierParsing(t *testing.T) {
	_, cmd := parseBuild(t, "build {\n    lock<5m> \"release\" {\n        $ echo hi\n    }\n}")
	lock := cmd.Body[0]
	if lock.Type != StmtLock || lock.Modifier != "5m" || lock.Shell != "release" {
		t.Errorf("lock stmt = %+v", lock)
	}

	_, cmd = parseBuild(t, "build {\n    switch<strict> \"&env\" {\n        case \"prod\" {\n            $ echo hi\n        }\n    }\n}")
	sw := cmd.Body[0]
	if sw.Type != StmtSwitch || sw.Modifier != "strict" {
		t.Errorf("switch stmt = %+v", sw)
	}

	_, cmd = parseBuild(t, "build {\n    retry<3, 2s> $ flaky.sh\n}")
	rs := cmd.Body[0]
	if rs.Type != StmtShell || rs.Retry != 3 || rs.Modifier != "2s" {
		t.Errorf("retry stmt = %+v", rs)
	}

	_, cmd = parseBuild(t, "build {\n    retry<3> $ plain.sh\n}")
	if rs := cmd.Body[0]; rs.Modifier != "" || rs.Retry != 3 {
		t.Errorf("plain retry = %+v", rs)
	}
}

func TestModifierMalformed(t *testing.T) {
	for _, in := range []string{
		"build {\n    lock<5m \"x\" {\n        $ echo hi\n    }\n}",
		"build {\n    lock<x> \"x\" {\n        $ echo hi\n    }\n}",
		"build {\n    lock<0s> \"x\" {\n        $ echo hi\n    }\n}",
		"build {\n    switch<foo> \"x\" {\n        case \"a\" {\n            $ echo hi\n        }\n    }\n}",
		"build {\n    retry<x> $ cmd\n}",
		"build {\n    retry<0> $ cmd\n}",
		"build {\n    retry<3, x> $ cmd\n}",
		"build {\n    retry<2s> $ cmd\n}",
		"build {\n    retry 3 $ cmd\n}",
	} {
		if _, err := NewParserFromContent("t.constfile", in).Parse(); err == nil {
			t.Errorf("expected error for %q", in)
		}
	}
}

func TestModifierNestedInIf(t *testing.T) {
	_, cmd := parseBuild(t, `build {
    if "&env" == "prod" {
        lock<5m> "release" {
            $ echo hi
        }
        switch<strict> "&env" {
            case "prod" {
                $ echo ok
            }
        }
    }
}`)
	then := cmd.Body[0].ThenBody
	if len(then) != 2 || then[0].Type != StmtLock || then[0].Modifier != "5m" {
		t.Fatalf("nested statements = %#v", then)
	}
	if then[1].Type != StmtSwitch || then[1].Modifier != "strict" {
		t.Fatalf("nested switch = %#v", then)
	}
}

func TestRetryBackoff(t *testing.T) {
	_, err := runParsed(t, "build {\n    retry<2, 50ms> $ false\n}")
	if err == nil {
		t.Fatal("expected failure after retries")
	}
}

func TestRetryBackoffRecovers(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.ToSlash(filepath.Join(dir, "m"))
	in := "build {\n    retry<3, 20ms> $ test -f " + marker + " || { touch " + marker + "; exit 1; }\n}"
	if _, err := runParsed(t, in); err != nil {
		t.Fatalf("second attempt should succeed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "m")); err != nil {
		t.Errorf("marker missing: %v", err)
	}
}

func TestSwitchStrict(t *testing.T) {
	_, err := runParsed(t, `var env = prod
build {
    switch<strict> "&env" {
        case "staging" {
            $ echo staging
        }
    }
}`)
	if err == nil || !strings.Contains(err.Error(), "no case matched") {
		t.Fatalf("expected strict failure, got %v", err)
	}

	_, err = runParsed(t, `build {
    switch<strict> "&env" {
        case "staging" {
            $ echo staging
        }
        default {
            $ echo fallback
        }
    }
}`)
	if err != nil {
		t.Fatalf("default should satisfy strict: %v", err)
	}

	_, err = runParsed(t, `build {
    switch "&env" {
        case "staging" {
            $ echo staging
        }
    }
}`)
	if err != nil {
		t.Fatalf("non-strict no-match should pass: %v", err)
	}
}
