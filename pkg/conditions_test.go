package pkg

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestEvalBuiltinConditionBaseOnlyJoinsPaths(t *testing.T) {
	plat := `os("` + runtime.GOOS + `")`
	if !evaluateConditionWithBase(plat, filepath.Join("some", "base")) {
		t.Error("os() broken by base-dir joining")
	}
	arch := `arch("` + runtime.GOARCH + `")`
	if !evaluateConditionWithBase(arch, "base") {
		t.Error("arch() broken by base-dir joining")
	}
	if evaluateConditionWithBase(`exists("definitely-not-here.txt")`, ".") {
		t.Error("exists() should still resolve against the base")
	}
}
