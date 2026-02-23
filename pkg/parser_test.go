package pkg

import (
	"strings"
	"testing"
)

// =============================================================================
// Tests for new helper functions (Phase 1 Refactoring)
// These tests demonstrate the improved testability of the refactored parser
// =============================================================================

func TestParseCommandName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple command",
			input:    "build {",
			expected: "build",
		},
		{
			name:     "command with arguments",
			input:    "test (arg1, arg2) {",
			expected: "test",
		},
		{
			name:     "command with prerequisites",
			input:    "run < build, test {",
			expected: "run",
		},
		{
			name:     "command with args and prereqs",
			input:    "deploy (env) < build, test {",
			expected: "deploy",
		},
		{
			name:     "cloud command",
			input:    "|cloudcmd| {",
			expected: "cloudcmd",
		},
		{
			name:     "default command",
			input:    "_ {",
			expected: "_",
		},
		{
			name:     "command with all features",
			input:    "|deploy| (env, opt region) < build {",
			expected: "deploy",
		},
		{
			name:     "command with spaces before name",
			input:    "  build {",
			expected: "build",
		},
		{
			name:     "complex command name",
			input:    "docker-build (tag) < test {",
			expected: "docker-build",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseCommandName(tt.input)
			if result != tt.expected {
				t.Errorf("parseCommandName(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestExtractArgumentString(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple arguments",
			input:    "run (arg1, arg2) {",
			expected: "arg1, arg2",
		},
		{
			name:     "optional arguments",
			input:    "deploy (env, opt region) {",
			expected: "env, opt region",
		},
		{
			name:     "no arguments",
			input:    "run {",
			expected: "",
		},
		{
			name:     "arguments with prereqs",
			input:    "run (arg1) < build {",
			expected: "arg1",
		},
		{
			name:     "arguments with spaces",
			input:    "test (  arg1  ,  opt arg2  ) {",
			expected: "arg1  ,  opt arg2",
		},
		{
			name:     "cloud command with arguments",
			input:    "|deploy| (env) {",
			expected: "env",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractArgumentString(tt.input)
			if result != tt.expected {
				t.Errorf("extractArgumentString(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestExtractPrerequisiteString(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple prerequisites",
			input:    "run < build, test {",
			expected: "build, test",
		},
		{
			name:     "no prerequisites",
			input:    "run {",
			expected: "",
		},
		{
			name:     "arguments with prerequisites",
			input:    "run (arg1) < build {",
			expected: "build",
		},
		{
			name:     "arguments and prerequisites",
			input:    "deploy (env) < build, test {",
			expected: "build, test",
		},
		{
			name:     "prerequisites with spaces",
			input:    "run < build , test , lint {",
			expected: "build , test , lint",
		},
		{
			name:     "cloud command with prereqs",
			input:    "|deploy| < build {",
			expected: "build",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractPrerequisiteString(tt.input)
			if result != tt.expected {
				t.Errorf("extractPrerequisiteString(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestParseArgumentName(t *testing.T) {
	tests := []struct {
		name          string
		input         string
		expectedName  string
		expectedOptional bool
	}{
		{
			name:            "simple argument",
			input:           "arg1",
			expectedName:    "arg1",
			expectedOptional: false,
		},
		{
			name:            "optional argument",
			input:           "opt arg2",
			expectedName:    "arg2",
			expectedOptional: true,
		},
		{
			name:            "argument with spaces",
			input:           "  arg3  ",
			expectedName:    "arg3",
			expectedOptional: false,
		},
		{
			name:            "optional argument with spaces",
			input:           "  opt  arg4  ",
			expectedName:    "arg4",
			expectedOptional: true,
		},
		{
			name:            "empty string",
			input:           "",
			expectedName:    "",
			expectedOptional: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, isOptional := parseArgumentName(tt.input)
			if name != tt.expectedName {
				t.Errorf("parseArgumentName(%q) name = %q, want %q", tt.input, name, tt.expectedName)
			}
			if isOptional != tt.expectedOptional {
				t.Errorf("parseArgumentName(%q) isOptional = %v, want %v", tt.input, isOptional, tt.expectedOptional)
			}
		})
	}
}

func TestParseArgumentList(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []*Argument
		wantErr  bool
	}{
		{
			name:  "single required argument",
			input: "arg1",
			expected: []*Argument{
				{Name: "arg1", IsOptional: false},
			},
			wantErr: false,
		},
		{
			name:  "multiple required arguments",
			input: "arg1, arg2, arg3",
			expected: []*Argument{
				{Name: "arg1", IsOptional: false},
				{Name: "arg2", IsOptional: false},
				{Name: "arg3", IsOptional: false},
			},
			wantErr: false,
		},
		{
			name:  "single optional argument",
			input: "opt arg1",
			expected: []*Argument{
				{Name: "arg1", IsOptional: true},
			},
			wantErr: false,
		},
		{
			name:  "mixed required and optional",
			input: "arg1, opt arg2, arg3",
			expected: []*Argument{
				{Name: "arg1", IsOptional: false},
				{Name: "arg2", IsOptional: true},
				{Name: "arg3", IsOptional: false},
			},
			wantErr: false,
		},
		{
			name:     "empty string",
			input:    "",
			expected: nil,
			wantErr:  false,
		},
		{
			name:     "whitespace only",
			input:    "   ",
			expected: nil,
			wantErr:  false,
		},
		{
			name:     "trailing comma",
			input:    "arg1,",
			expected: []*Argument{
				{Name: "arg1", IsOptional: false},
			},
			wantErr: false,
		},
		{
			name:  "all optional",
			input: "opt arg1, opt arg2",
			expected: []*Argument{
				{Name: "arg1", IsOptional: true},
				{Name: "arg2", IsOptional: true},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseArgumentList(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseArgumentList(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}

			if len(result) != len(tt.expected) {
				t.Errorf("parseArgumentList(%q) returned %d args, want %d", tt.input, len(result), len(tt.expected))
				return
			}

			for i, arg := range result {
				if arg.Name != tt.expected[i].Name {
					t.Errorf("arg[%d].Name = %q, want %q", i, arg.Name, tt.expected[i].Name)
				}
				if arg.IsOptional != tt.expected[i].IsOptional {
					t.Errorf("arg[%d].IsOptional = %v, want %v", i, arg.IsOptional, tt.expected[i].IsOptional)
				}
			}
		})
	}
}

func TestParsePrerequisiteList(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "single prerequisite",
			input:    "build",
			expected: []string{"build"},
		},
		{
			name:     "multiple prerequisites",
			input:    "build, test, lint",
			expected: []string{"build", "test", "lint"},
		},
		{
			name:     "prerequisites with extra spaces",
			input:    "build , test , lint",
			expected: []string{"build", "test", "lint"},
		},
		{
			name:     "empty string",
			input:    "",
			expected: nil,
		},
		{
			name:     "whitespace only",
			input:    "   ",
			expected: nil,
		},
		{
			name:     "trailing comma",
			input:    "build, test,",
			expected: []string{"build", "test"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parsePrerequisiteList(tt.input)
			if err != nil {
				t.Fatalf("parsePrerequisiteList(%q) unexpected error: %v", tt.input, err)
			}

			if len(result) != len(tt.expected) {
				t.Errorf("parsePrerequisiteList(%q) returned %d prereqs, want %d", tt.input, len(result), len(tt.expected))
				return
			}

			for i, prereq := range result {
				if prereq != tt.expected[i] {
					t.Errorf("prereq[%d] = %q, want %q", i, prereq, tt.expected[i])
				}
			}
		})
	}
}

// =============================================================================
// Original tests (still passing with refactored code)
// =============================================================================

// TestNewParser tests the NewParser function
func TestNewParser(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		expectError bool
	}{
		{
			name:        "nonexistent file",
			input:       "nonexistent.txt",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parser, err := NewParser(tt.input)
			if tt.expectError {
				if err == nil {
					t.Errorf("expected error but got none")
				}
				if parser != nil {
					t.Errorf("expected nil parser on error")
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if parser == nil {
					t.Errorf("expected non-nil parser")
				}
			}
		})
	}
}

// TestParseVariable tests variable parsing
func TestParseVariable(t *testing.T) {
	tests := []struct {
		name          string
		input         string
		expectName    string
		expectValue   string
		expectScope   string
	}{
		{
			name:        "simple variable",
			input:       "var foo = bar",
			expectName:  "foo",
			expectValue: "bar",
			expectScope: "global",
		},
		{
			name:        "variable with spaces",
			input:       "var name = @COMPUTERNAME",
			expectName:  "name",
			expectValue: "", // Will be evaluated at runtime
			expectScope: "global",
		},
		{
			name:        "variable with number",
			input:       "var example = 25",
			expectName:  "example",
			expectValue: "25",
			expectScope: "global",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parser := &Parser{
				Data:  &ParsedData{},
				Lines: []string{},
			}
			err := parser.parseVar(tt.input, tt.expectScope)
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}

			// Find the variable
			var found *Variable
			for _, v := range parser.Data.Variables {
				if v.Name == tt.expectName && v.Scope == tt.expectScope {
					found = v
					break
				}
			}

			if found == nil {
				t.Errorf("variable not found")
				return
			}

			if found.Name != tt.expectName {
				t.Errorf("expected name %s, got %s", tt.expectName, found.Name)
			}

			// Only check value if it's not an environment variable
			if tt.expectValue != "" && found.Value != tt.expectValue {
				t.Errorf("expected value %s, got %s", tt.expectValue, found.Value)
			}
		})
	}
}

// TestParseCommand tests command parsing
func TestParseCommand(t *testing.T) {
	tests := []struct {
		name          string
		input         string
		expectName    string
		expectArgs    int
		expectPrereqs int
		expectBody    int
		isDefault     bool
	}{
		{
			name:       "simple command",
			input:      "test {\n    echo hello\n}",
			expectName: "test",
			expectArgs: 0,
			expectBody: 1,
			isDefault:  false,
		},
		{
			name:       "command with arguments",
			input:      "cmdWithArgs (arg0, opt arg1) {\n    echo test\n}",
			expectName: "cmdWithArgs",
			expectArgs: 2,
			expectBody: 1,
			isDefault:  false,
		},
		{
			name:       "command with prerequisites",
			input:      "cmdWithPrereqs (arg0) < prerun, another {\n    echo test\n}",
			expectName: "cmdWithPrereqs",
			expectArgs: 1,
			expectPrereqs: 2,
			expectBody: 1,
			isDefault:  false,
		},
		{
			name:       "default command",
			input:      "_ {\n    echo hello\n}",
			expectName: "_",
			expectBody: 1,
			isDefault:  true,
		},
		{
			name:       "cloud command",
			input:      "|cloudcmd| {\n    echo test\n}",
			expectName: "cloudcmd",
			expectBody: 1,
			isDefault:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Split input into lines
			lines := strings.Split(tt.input, "\n")
			parser := &Parser{
				Data:  &ParsedData{},
				Lines: lines,
			}

			err := parser.parseCommand(0, tt.input, tt.isDefault)
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}

			if len(parser.Data.Commands) == 0 {
				t.Errorf("no commands parsed")
				return
			}

			cmd := parser.Data.Commands[0]

			if cmd.Name != tt.expectName {
				t.Errorf("expected name %s, got %s", tt.expectName, cmd.Name)
			}

			if cmd.IsDefault != tt.isDefault {
				t.Errorf("expected isDefault %v, got %v", tt.isDefault, cmd.IsDefault)
			}

			if tt.expectArgs != 0 && len(cmd.Arguments) != tt.expectArgs {
				t.Errorf("expected %d args, got %d", tt.expectArgs, len(cmd.Arguments))
			}

			if tt.expectPrereqs != 0 && len(cmd.Prereqs) != tt.expectPrereqs {
				t.Errorf("expected %d prereqs, got %d", tt.expectPrereqs, len(cmd.Prereqs))
			}

			if tt.expectBody != 0 && len(cmd.Body) != tt.expectBody {
				t.Errorf("expected %d body lines, got %d", tt.expectBody, len(cmd.Body))
			}
		})
	}
}

// TestParseUnclosedBrace tests the infinite loop fix
func TestParseUnclosedBrace(t *testing.T) {
	input := "test {\n    echo hello\n"
	lines := strings.Split(input, "\n")
	parser := &Parser{
		Data:  &ParsedData{},
		Lines: lines,
	}

	err := parser.parseCommand(0, input, false)
	if err == nil {
		t.Errorf("expected error for unclosed brace, got nil")
	}

	// Check error message contains useful information
	if err != nil {
		errMsg := err.Error()
		if errMsg == "" {
			t.Errorf("error message should not be empty")
		}
	}
}

// TestParseMalformedSyntax tests various malformed syntax cases
func TestParseMalformedSyntax(t *testing.T) {
	tests := []struct {
		name        string
		input       []string
		expectError bool
	}{
		{
			name:        "empty file",
			input:       []string{""},
			expectError: false,
		},
		{
			name:        "only comments",
			input:       []string{"// comment", "// another comment"},
			expectError: false,
		},
		{
			name:        "unclosed brace",
			input:       []string{"test {\n    echo hello"},
			expectError: true,
		},
		{
			name:        "command without body",
			input:       []string{"test"},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parser := &Parser{
				Data:  &ParsedData{},
				Lines: tt.input,
			}

			_, err := parser.Parse()
			if tt.expectError && err == nil {
				t.Errorf("expected error but got none")
			}
			if !tt.expectError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// TestGetVariable tests variable retrieval
func TestGetVariable(t *testing.T) {
	data := &ParsedData{
		Variables: []*Variable{
			{Name: "foo", Value: "bar", Scope: "global"},
			{Name: "local", Value: "value", Scope: "myscope"},
		},
	}

	tests := []struct {
		name        string
		varName     string
		scope       string
		expectError bool
		expectValue string
	}{
		{
			name:        "existing global variable",
			varName:     "foo",
			scope:       "",
			expectError: false,
			expectValue: "bar",
		},
		{
			name:        "existing scoped variable",
			varName:     "local",
			scope:       "myscope",
			expectError: false,
			expectValue: "value",
		},
		{
			name:        "nonexistent variable",
			varName:     "nonexistent",
			scope:       "",
			expectError: true,
		},
		{
			name:        "scoped variable fallback to global",
			varName:     "foo",
			scope:       "otherscope",
			expectError: false,
			expectValue: "bar",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			variable, err := data.GetVariable(tt.varName, tt.scope)
			if tt.expectError {
				if err == nil {
					t.Errorf("expected error but got none")
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if variable.Value != tt.expectValue {
					t.Errorf("expected value %s, got %s", tt.expectValue, variable.Value)
				}
			}
		})
	}
}

// TestGetCommand tests command retrieval
func TestGetCommand(t *testing.T) {
	data := &ParsedData{
		Commands: []*Command{
			{Name: "build"},
			{Name: "test"},
		},
	}

	tests := []struct {
		name        string
		commandName string
		expectError bool
	}{
		{
			name:        "existing command",
			commandName: "build",
			expectError: false,
		},
		{
			name:        "nonexistent command",
			commandName: "nonexistent",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, err := data.GetCommand(tt.commandName)
			if tt.expectError {
				if err == nil {
					t.Errorf("expected error but got none")
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if cmd.Name != tt.commandName {
					t.Errorf("expected command name %s, got %s", tt.commandName, cmd.Name)
				}
			}
		})
	}
}

// TestParseCommentLine tests comment handling
func TestParseCommentLine(t *testing.T) {
	input := []string{
		"// this is a comment",
		"var test = value",
		"// another comment",
		"test {",
		"    echo hello",
		"}",
	}

	parser := &Parser{
		Data:  &ParsedData{},
		Lines: input,
	}

	_, err := parser.Parse()
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	// Should have 1 variable and 1 command (comments ignored)
	if len(parser.Data.Variables) != 1 {
		t.Errorf("expected 1 variable, got %d", len(parser.Data.Variables))
	}
	if len(parser.Data.Commands) != 1 {
		t.Errorf("expected 1 command, got %d", len(parser.Data.Commands))
	}
}

// TestPrereqWhitespace tests the prereq whitespace fix
func TestPrereqWhitespace(t *testing.T) {
	input := "cmdPrereqTest (arg0) < prerun, another, test {\n    echo hello\n}"
	lines := strings.Split(input, "\n")
	parser := &Parser{
		Data:  &ParsedData{},
		Lines: lines,
	}

	err := parser.parseCommand(0, input, false)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	if len(parser.Data.Commands) == 0 {
		t.Errorf("no commands parsed")
		return
	}

	cmd := parser.Data.Commands[0]

	// Check that prereqs are trimmed
	for i, prereq := range cmd.Prereqs {
		if prereq != strings.TrimSpace(prereq) {
			t.Errorf("prereq %d not trimmed: '%s'", i, prereq)
		}
	}

	// Should have 3 prereqs
	if len(cmd.Prereqs) != 3 {
		t.Errorf("expected 3 prereqs, got %d", len(cmd.Prereqs))
	}
}

// TestGetDefaultCommand tests default command retrieval
func TestGetDefaultCommand(t *testing.T) {
	data := &ParsedData{
		Commands: []*Command{
			{Name: "build", IsDefault: false},
			{Name: "_", IsDefault: true},
		},
	}

	cmd, err := data.GetDefaultCommand()
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if cmd.Name != "_" {
		t.Errorf("expected default command name '_', got %s", cmd.Name)
	}

	// Test no default command
	data2 := &ParsedData{
		Commands: []*Command{
			{Name: "build", IsDefault: false},
		},
	}

	_, err = data2.GetDefaultCommand()
	if err == nil {
		t.Errorf("expected error when no default command")
	}
}

// TestArgumentParsing tests argument parsing
func TestArgumentParsing(t *testing.T) {
	input := "cmdArgsTest (arg0, opt arg1, opt arg2) {\n    echo hello\n}"
	lines := strings.Split(input, "\n")
	parser := &Parser{
		Data:  &ParsedData{},
		Lines: lines,
	}

	err := parser.parseCommand(0, input, false)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	if len(parser.Data.Commands) == 0 {
		t.Errorf("no commands parsed")
		return
	}

	cmd := parser.Data.Commands[0]

	// Should have 3 arguments
	if len(cmd.Arguments) != 3 {
		t.Errorf("expected 3 arguments, got %d", len(cmd.Arguments))
	}

	// Check first argument (required)
	if cmd.Arguments[0].Name != "arg0" || cmd.Arguments[0].IsOptional {
		t.Errorf("first argument should be required 'arg0'")
	}

	// Check second argument (optional)
	if cmd.Arguments[1].Name != "arg1" || !cmd.Arguments[1].IsOptional {
		t.Errorf("second argument should be optional 'arg1'")
	}

	// Check third argument (optional)
	if cmd.Arguments[2].Name != "arg2" || !cmd.Arguments[2].IsOptional {
		t.Errorf("third argument should be optional 'arg2'")
	}
}

// TestScopedVariableParsing tests scoped variable parsing
func TestScopedVariableParsing(t *testing.T) {
	input := "test {\n    var local = value\n    echo hello\n}"
	lines := strings.Split(input, "\n")
	parser := &Parser{
		Data:  &ParsedData{},
		Lines: lines,
	}

	err := parser.parseCommand(0, input, false)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	if len(parser.Data.Commands) == 0 {
		t.Errorf("no commands parsed")
		return
	}

	// Should have a scoped variable
	var found *Variable
	for _, v := range parser.Data.Variables {
		if v.Name == "local" && v.Scope == "test" {
			found = v
			break
		}
	}

	if found == nil {
		t.Errorf("scoped variable not found")
	}
}
