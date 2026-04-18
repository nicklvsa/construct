package pkg

import (
	"testing"
)

func TestNewExecutor(t *testing.T) {
	data := &ParsedData{
		Variables: []*Variable{
			{Name: "test", Value: "value", Scope: "global"},
		},
		Commands: []*Command{
			{Name: "build", IsDefault: false, Body: []string{"echo hello"}},
		},
	}

	executor := NewExecutor(data, false, false)
	if executor == nil {
		t.Error("expected non-nil executor")
	}
	if executor.concurrent {
		t.Error("expected concurrent to be false")
	}
	if executor.debug {
		t.Error("expected debug to be false by default")
	}
}

func TestExecutorSetDebug(t *testing.T) {
	data := &ParsedData{}
	executor := NewExecutor(data, false, false)

	executor.SetDebug(true)
	if !executor.debug {
		t.Error("expected debug to be true")
	}

	executor.SetDebug(false)
	if executor.debug {
		t.Error("expected debug to be false")
	}
}

func TestBuildCommand(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		expectShell string
	}{
		{
			name:        "simple echo command",
			input:       "echo hello",
			expectShell: "/bin/bash",
		},
		{
			name:        "complex command",
			input:       "ls -la | grep test",
			expectShell: "/bin/bash",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			executor := NewExecutor(nil, false, false)
			if executor.shellName != tt.expectShell {
				t.Errorf("expected shell %s, got %s", tt.expectShell, executor.shellName)
			}
			args := append(executor.shellArgs, tt.input)
			if len(args) < 2 {
				t.Error("expected at least 2 args (flag and command)")
			}
		})
	}
}

func TestExecutorDryRun(t *testing.T) {
	data := &ParsedData{
		Commands: []*Command{
			{Name: "build", IsDefault: false, Body: []string{"echo hello"}},
		},
	}

	executor := NewExecutor(data, false, false)
	executor.SetDebug(true)

	cmd, err := data.GetCommand("build")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cmd.Name != "build" {
		t.Errorf("expected command name 'build', got %s", cmd.Name)
	}
}

func TestCircularDependencyDetection(t *testing.T) {
	tests := []struct {
		name        string
		commands    []*Command
		expectError bool
	}{
		{
			name: "no circular dependency",
			commands: []*Command{
				{Name: "a", Prereqs: []string{"b"}},
				{Name: "b", Prereqs: []string{}},
			},
			expectError: false,
		},
		{
			name: "direct circular dependency",
			commands: []*Command{
				{Name: "a", Prereqs: []string{"b"}},
				{Name: "b", Prereqs: []string{"a"}},
			},
			expectError: true,
		},
		{
			name: "indirect circular dependency",
			commands: []*Command{
				{Name: "a", Prereqs: []string{"b"}},
				{Name: "b", Prereqs: []string{"c"}},
				{Name: "c", Prereqs: []string{"a"}},
			},
			expectError: true,
		},
		{
			name: "self-referential dependency",
			commands: []*Command{
				{Name: "a", Prereqs: []string{"a"}},
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parser := &Parser{
				Data: &ParsedData{
					Commands: tt.commands,
				},
			}

			err := parser.detectCircularDependencies()
			if tt.expectError {
				if err == nil {
					t.Error("expected circular dependency error but got none")
				}
				_, ok := err.(*CircularDependencyError)
				if !ok {
					t.Errorf("expected CircularDependencyError, got %T", err)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestMissingPrerequisiteValidation(t *testing.T) {
	tests := []struct {
		name        string
		commands    []*Command
		expectError bool
	}{
		{
			name: "all prerequisites exist",
			commands: []*Command{
				{Name: "a", Prereqs: []string{"b"}},
				{Name: "b", Prereqs: []string{}},
			},
			expectError: false,
		},
		{
			name: "missing prerequisite",
			commands: []*Command{
				{Name: "a", Prereqs: []string{"nonexistent"}},
			},
			expectError: true,
		},
		{
			name: "empty prerequisite string",
			commands: []*Command{
				{Name: "a", Prereqs: []string{""}},
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parser := &Parser{
				Data: &ParsedData{
					Commands: tt.commands,
				},
			}

			err := parser.validatePrerequisites()
			if tt.expectError {
				if err == nil {
					t.Error("expected missing prerequisite error but got none")
				}
				_, ok := err.(*MissingDependencyError)
				if !ok {
					t.Errorf("expected MissingDependencyError, got %T", err)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}
