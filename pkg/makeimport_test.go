package pkg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func importOK(t *testing.T, in string) ImportResult {
	t.Helper()
	res, err := ImportMakefile(in)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	p := NewParserFromContent("imported.constfile", res.Constfile)
	if _, err := p.Parse(); err != nil {
		t.Fatalf("generated Constfile does not parse: %v\n%s", err, res.Constfile)
	}
	return res
}

func TestImportSimplePhony(t *testing.T) {
	res := importOK(t, `.PHONY: build test

build:
	gcc -o app main.c

test:
	@./app --check
	-python -m pytest || true
`)
	if !strings.Contains(res.Constfile, "build {") {
		t.Errorf("missing build command:\n%s", res.Constfile)
	}
	if !strings.Contains(res.Constfile, "$ gcc -o app main.c") {
		t.Errorf("missing recipe:\n%s", res.Constfile)
	}
	if !strings.Contains(res.Constfile, "$ ./app --check") {
		t.Errorf("@ prefix not stripped:\n%s", res.Constfile)
	}
	if !strings.Contains(res.Constfile, "! $ python -m pytest || true") {
		t.Errorf("- prefix not converted:\n%s", res.Constfile)
	}
	if !strings.Contains(res.Constfile, "_ < build { }") {
		t.Errorf("missing default goal:\n%s", res.Constfile)
	}
}

func TestImportVariablesAndRefs(t *testing.T) {
	res := importOK(t, `CC := gcc
FLAGS = -Wall
build:
	$(CC) $(FLAGS) -o app main.c
	@echo done $(undefined_later)
`)
	if !strings.Contains(res.Constfile, "var CC = gcc") || !strings.Contains(res.Constfile, "var FLAGS = -Wall") {
		t.Errorf("vars not emitted:\n%s", res.Constfile)
	}
	if !strings.Contains(res.Constfile, "$ &CC &FLAGS -o app main.c") {
		t.Errorf("refs not converted:\n%s", res.Constfile)
	}
	if res.Flagged == 0 || !strings.Contains(res.Constfile, "undefined_later") {
		t.Errorf("unknown var not flagged:\n%s", res.Constfile)
	}
}

func TestImportForwardVarReference(t *testing.T) {
	res := importOK(t, `A = $(B)
B = hello
build:
	echo $(A)
`)
	if !strings.Contains(res.Constfile, "var A = &B") {
		t.Errorf("forward ref not converted:\n%s", res.Constfile)
	}
}

func TestImportFileTargets(t *testing.T) {
	res := importOK(t, `app: main.o util.o
	gcc -o app main.o util.o

main.o: main.c
	gcc -c main.c -o main.o

util.o: util.c
	gcc -c $< -o $@
`)
	if !strings.Contains(res.Constfile, "app < main-o, util-o {") {
		t.Errorf("prereqs not rewritten:\n%s", res.Constfile)
	}
	if !strings.Contains(res.Constfile, "app-o produces app") && !strings.Contains(res.Constfile, "app < main-o") {
		t.Errorf("app should stay a plain command name:\n%s", res.Constfile)
	}
	if !strings.Contains(res.Constfile, "main-o produces main.o < main.c {") {
		t.Errorf("file target not mangled with produces:\n%s", res.Constfile)
	}
	if !strings.Contains(res.Constfile, "$ gcc -c util.c -o util.o") {
		t.Errorf("automatic vars not expanded:\n%s", res.Constfile)
	}
}

func TestImportDefaultGoalDirective(t *testing.T) {
	res := importOK(t, `.DEFAULT_GOAL := test
build:
	echo build
test:
	echo test
`)
	if !strings.Contains(res.Constfile, "_ < test { }") {
		t.Errorf(".DEFAULT_GOAL not honored:\n%s", res.Constfile)
	}
}

func TestImportDocComments(t *testing.T) {
	res := importOK(t, `# Builds the thing
build:
	echo hi
`)
	if !strings.Contains(res.Constfile, "# Builds the thing\nbuild {") {
		t.Errorf("doc comment not attached:\n%s", res.Constfile)
	}
}

func TestImportPatternRuleSkipped(t *testing.T) {
	res := importOK(t, `%.o: %.c
	gcc -c $< -o $@
build:
	echo hi
`)
	if !strings.Contains(res.Constfile, "pattern rule skipped") {
		t.Errorf("pattern rule not flagged:\n%s", res.Constfile)
	}
	if res.Flagged < 1 {
		t.Errorf("flagged = %d, want >= 1", res.Flagged)
	}
}

func TestImportConditionalsSkipped(t *testing.T) {
	res := importOK(t, `OS := linux
ifeq ($(OS),linux)
build:
	echo linux
else
build:
	echo other
endif
`)
	if !strings.Contains(res.Constfile, "skipped conditional directive") {
		t.Errorf("conditionals not flagged:\n%s", res.Constfile)
	}
}

func TestImportIncludeAndMake(t *testing.T) {
	res, err := ImportMakefile("include deps.mk\ndeploy:\n\t$(MAKE) install\n")
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if !strings.Contains(res.Constfile, `import "deps.mk"`) {
		t.Errorf("include not converted:\n%s", res.Constfile)
	}
	if !strings.Contains(res.Constfile, "$ construct install") {
		t.Errorf("$(MAKE) not converted:\n%s", res.Constfile)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "deps.mk"), []byte("# deps\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Constfile"), []byte(res.Constfile), 0644); err != nil {
		t.Fatal(err)
	}
	p, err := NewParser(filepath.Join(dir, "Constfile"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Parse(); err != nil {
		t.Fatalf("generated Constfile does not parse: %v", err)
	}
}

func TestImportShellFuncFlagged(t *testing.T) {
	res := importOK(t, `VERSION := $(shell git describe --tags)
build:
	echo $(VERSION)
`)
	if !strings.Contains(res.Constfile, "needs manual translation") {
		t.Errorf("$(shell) not flagged:\n%s", res.Constfile)
	}
}

func TestImportContinuations(t *testing.T) {
	res := importOK(t, `build:
	gcc -o app \
	    main.c \
	    util.c
`)
	if !strings.Contains(res.Constfile, "$ gcc -o app main.c util.c") {
		t.Errorf("recipe continuation not joined:\n%s", res.Constfile)
	}
}

func TestImportAppendAndConditionalAssign(t *testing.T) {
	res := importOK(t, `FLAGS = -Wall
FLAGS += -O2
NEW ?= yes
build:
	echo hi
`)
	if !strings.Contains(res.Constfile, "+= on an existing variable is not supported") {
		t.Errorf("+= not flagged:\n%s", res.Constfile)
	}
	if !strings.Contains(res.Constfile, "var NEW = yes") {
		t.Errorf("?= not emitted as var:\n%s", res.Constfile)
	}
}

func TestImportDoubleColonAndOrderOnly(t *testing.T) {
	res := importOK(t, `build:: main.c | order
	gcc -o build main.c
`)
	if !strings.Contains(res.Constfile, "double-colon rule treated as a normal rule") {
		t.Errorf(":: not flagged:\n%s", res.Constfile)
	}
	if !strings.Contains(res.Constfile, "order-only prerequisites skipped: order") {
		t.Errorf("order-only not flagged:\n%s", res.Constfile)
	}
	if !strings.Contains(res.Constfile, "build < main.c {") {
		t.Errorf("normal prereq lost:\n%s", res.Constfile)
	}
}

func TestImportMultipleTargets(t *testing.T) {
	res := importOK(t, `a.out b.out: src.c
	cp src.c a.out
`)
	if !strings.Contains(res.Constfile, "a-out produces a.out < src.c {") {
		t.Errorf("first target missing:\n%s", res.Constfile)
	}
	if !strings.Contains(res.Constfile, "b-out produces b.out < src.c {") {
		t.Errorf("second target missing:\n%s", res.Constfile)
	}
}

func TestImportEmptyInputFails(t *testing.T) {
	if _, err := ImportMakefile("# just a comment\n"); err == nil {
		t.Error("expected error for empty makefile")
	}
}

func TestImportDollarDollar(t *testing.T) {
	res := importOK(t, `build:
	awk '{print $$1}' data.txt
`)
	if !strings.Contains(res.Constfile, `awk '{print $1}' data.txt`) {
		t.Errorf("$$ not collapsed:\n%s", res.Constfile)
	}
}

func TestImportDuplicateVariableFromBranches(t *testing.T) {
	res := importOK(t, `OS := linux
ifeq ($(OS),linux)
PLATFORM = macos
else
PLATFORM = other
endif
build:
	echo $(PLATFORM)
`)
	if !strings.Contains(res.Constfile, "var PLATFORM = macos") {
		t.Errorf("first assignment not kept:\n%s", res.Constfile)
	}
	if got := strings.Count(res.Constfile, "var PLATFORM"); got != 1 {
		t.Errorf("PLATFORM emitted %d times, want 1:\n%s", got, res.Constfile)
	}
	if !strings.Contains(res.Constfile, "duplicate assignment to PLATFORM skipped") {
		t.Errorf("duplicate not flagged:\n%s", res.Constfile)
	}
}
