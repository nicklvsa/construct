//go:build !windows

package pkg

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestLockBoundedWait(t *testing.T) {
	dir := t.TempDir()
	locks := filepath.Join(dir, ".construct-cache", "locks")
	if err := os.MkdirAll(locks, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(locks, "release"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)

	p := NewParserFromContent("t.constfile", "build {\n    lock<100ms> \"release\" {\n        $ echo hi\n    }\n}")
	data, err := p.Parse()
	if err != nil {
		t.Fatal(err)
	}
	data.buildIndexMaps()
	executor := NewExecutor(data, false, false)
	executor.SetBaseDir(dir)

	start := time.Now()
	err = executor.Execute([]string{"build"})
	elapsed := time.Since(start)
	if err == nil || !strings.Contains(err.Error(), "timed out after 100ms") {
		t.Fatalf("expected lock timeout, got %v", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("bounded lock waited %v", elapsed)
	}
}
