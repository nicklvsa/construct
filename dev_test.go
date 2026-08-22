package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nicklvsa/construct/pkg"
)

func devDir(t *testing.T, constfile string) string {
	t.Helper()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "Constfile"), []byte(constfile), 0644)
	old, _ := os.Getwd()
	os.Chdir(dir)
	t.Cleanup(func() { os.Chdir(old) })
	return dir
}

func TestDevRejectsNoServices(t *testing.T) {
	devDir(t, "build {\n  $ true\n}\n")
	if err := runDev(nil, &options{}); err == nil {
		t.Error("expected an error when no service commands exist")
	}
}

func TestDevRejectsUnknownCommand(t *testing.T) {
	devDir(t, "service a {\n  $ sleep 1\n}\n")
	if err := runDev([]string{"nope"}, &options{}); err == nil {
		t.Error("expected an error for an unknown service name")
	}
}

func TestDevRejectsServiceUnderDeepRegular(t *testing.T) {
	// deploy is a regular prereq of the aggregator and depends on a service:
	// running it to completion would block forever.
	devDir(t, "service web {\n  $ sleep 1\n}\n\ndeploy < web {\n  $ true\n}\n\ndev < deploy {\n  $ true\n}\n")
	err := runDev(nil, &options{})
	if err == nil || !strings.Contains(err.Error(), "cannot be a prerequisite") {
		t.Errorf("expected service-under-non-service error, got %v", err)
	}
}

func planFor(t *testing.T, constfile string, args []string) (*devPlan, error) {
	t.Helper()
	dir := devDir(t, constfile)
	p, err := pkg.NewParser(filepath.Join(dir, "Constfile"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := p.Parse()
	if err != nil {
		t.Fatal(err)
	}
	return planDev(data, args)
}

func TestPlanDevAggregatorMayListServices(t *testing.T) {
	plan, err := planFor(t, "service api {\n  $ s\n}\n\nservice web < api {\n  $ s\n}\n\ndev < api, web {\n  $ setup\n}\n", nil)
	if err != nil {
		t.Fatalf("aggregator with service prereqs should plan: %v", err)
	}
	if len(plan.services) != 2 || len(plan.regulars) != 0 {
		t.Errorf("plan: services=%v regulars=%v", plan.services, plan.regulars)
	}
	if plan.aggregator == nil || plan.aggregator.Name != "dev" {
		t.Errorf("aggregator not detected: %+v", plan.aggregator)
	}
}

func TestPlanDevExplicitServices(t *testing.T) {
	plan, err := planFor(t, "service a {\n  $ s\n}\n\nservice b {\n  $ s\n}\n", []string{"b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.services) != 1 || plan.services[0] != "b" {
		t.Errorf("explicit service selection: %v", plan.services)
	}
	if plan.aggregator != nil {
		t.Errorf("explicit services should not create an aggregator")
	}
}

func TestPlanDevRunsRegularsFirst(t *testing.T) {
	plan, err := planFor(t, "build {\n  $ b\n}\n\nservice web < build {\n  $ s\n}\n", []string{"web"})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.regulars) != 1 || plan.regulars[0] != "build" {
		t.Errorf("build should run before web: %v", plan.regulars)
	}
}

func TestWaitPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("cannot listen")
	}
	defer ln.Close()
	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	if !waitPort(context.Background(), port, time.Second) {
		t.Error("open listener not detected")
	}
	if waitPort(context.Background(), "1", 10*time.Millisecond) && ln.Addr().(*net.TCPAddr).Port == 1 {
		t.Log("note: something listens on port 1")
	}
}

func TestWatchOnchangeSignalsRestart(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "src"), 0755)
	os.WriteFile(filepath.Join(dir, "src", "a.txt"), []byte("v1"), 0644)

	restart := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go watchOnchange(dir, []string{"src/*.txt"}, restart, ctx)

	select {
	case <-restart:
		t.Fatal("restart signaled without a change")
	case <-time.After(600 * time.Millisecond):
	}

	os.WriteFile(filepath.Join(dir, "src", "a.txt"), []byte("v2"), 0644)
	select {
	case <-restart:
	case <-time.After(3 * time.Second):
		t.Fatal("no restart signal after a change")
	}
}

func TestDevSupervisionApplies(t *testing.T) {
	dir := devDir(t, "service a {\n  $ s\n}\n\nplain {\n  $ p\n}\n")
	if !devSupervisionApplies(filepath.Join(dir, "Constfile")) {
		t.Error("file with a service should trigger supervision dispatch")
	}
	dir2 := devDir(t, "dev {\n  $ echo plain-dev-command\n}\n")
	if devSupervisionApplies(filepath.Join(dir2, "Constfile")) {
		t.Error("no services: construct dev must fall through to the plain command")
	}
}
