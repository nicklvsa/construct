package pkg

import (
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

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
			result := ParseCommandName(tt.input)
			if result != tt.expected {
				t.Errorf("ParseCommandName(%q) = %q, want %q", tt.input, result, tt.expected)
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

func TestExtractPrerequisites(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
		dirs     map[string]string
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
			expected: "build, test, lint",
		},
		{
			name:     "cloud command with prereqs",
			input:    "|deploy| < build {",
			expected: "build",
		},
		{
			name:     "prereq with in dir modifier",
			input:    "run < build in subdir {",
			expected: "build",
			dirs:     map[string]string{"build": "subdir"},
		},
		{
			name:     "multiple prereqs with trailing in dir",
			input:    "deploy < build, test in deep/dir {",
			expected: "build, test",
			dirs:     map[string]string{"test": "deep/dir"},
		},
		{
			name:     "each prereq with its own in dir",
			input:    "deploy < build in a, test in b {",
			expected: "build, test",
			dirs:     map[string]string{"build": "a", "test": "b"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			names, dirs, err := extractPrerequisites(tt.input)
			if err != nil {
				t.Fatalf("extractPrerequisites(%q) unexpected error: %v", tt.input, err)
			}
			if got := strings.Join(names, ", "); got != tt.expected {
				t.Errorf("extractPrerequisites(%q) = %q, want %q", tt.input, got, tt.expected)
			}
			if !reflect.DeepEqual(dirs, tt.dirs) {
				t.Errorf("extractPrerequisites(%q) dirs = %v, want %v", tt.input, dirs, tt.dirs)
			}
		})
	}
}

func TestParseArgumentName(t *testing.T) {
	tests := []struct {
		name             string
		input            string
		expectedName     string
		expectedOptional bool
	}{
		{
			name:             "simple argument",
			input:            "arg1",
			expectedName:     "arg1",
			expectedOptional: false,
		},
		{
			name:             "optional argument",
			input:            "opt arg2",
			expectedName:     "arg2",
			expectedOptional: true,
		},
		{
			name:             "argument with spaces",
			input:            "  arg3  ",
			expectedName:     "arg3",
			expectedOptional: false,
		},
		{
			name:             "optional argument with spaces",
			input:            "  opt  arg4  ",
			expectedName:     "arg4",
			expectedOptional: true,
		},
		{
			name:             "empty string",
			input:            "",
			expectedName:     "",
			expectedOptional: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, isOptional, _ := parseArgumentName(tt.input)
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
			name:  "trailing comma",
			input: "arg1,",
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
		name        string
		input       string
		expectName  string
		expectValue string
		expectScope string
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
			err := parser.parseVar(tt.input, tt.expectScope, 1)
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
			name:          "command with prerequisites",
			input:         "cmdWithPrereqs (arg0) < prerun, another {\n    echo test\n}",
			expectName:    "cmdWithPrereqs",
			expectArgs:    1,
			expectPrereqs: 2,
			expectBody:    1,
			isDefault:     false,
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

			_, err := parser.parseCommand(0, tt.input, tt.isDefault, 1, "")
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

	_, err := parser.parseCommand(0, input, false, 1, "")
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

	_, err := parser.parseCommand(0, input, false, 1, "")
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

	_, err := parser.parseCommand(0, input, false, 1, "")
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

	_, err := parser.parseCommand(0, input, false, 1, "")
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

func TestTryEvalExpression(t *testing.T) {
	t.Run("plain text passes through unchanged", func(t *testing.T) {
		p := &Parser{Data: &ParsedData{}}
		name, scope := "x", "global"
		got := p.tryEvalExpression("hello world", &name, &scope, 1)
		if got != "hello world" {
			t.Errorf("got %q, want %q", got, "hello world")
		}
	})

	t.Run("@env expands environment variable", func(t *testing.T) {
		t.Setenv("CONSTRUCT_TEST_ENV", "envvalue")
		p := &Parser{Data: &ParsedData{}}
		name, scope := "x", "global"
		got := p.tryEvalExpression("v=@CONSTRUCT_TEST_ENV", &name, &scope, 1)
		if got != "v=envvalue" {
			t.Errorf("got %q, want %q", got, "v=envvalue")
		}
	})

	t.Run("@env undefined expands to empty", func(t *testing.T) {
		p := &Parser{Data: &ParsedData{}}
		name, scope := "x", "global"
		got := p.tryEvalExpression("[@CONSTRUCT_NOPE_NOT_SET]", &name, &scope, 1)
		if got != "[]" {
			t.Errorf("got %q, want %q", got, "[]")
		}
	})

	t.Run("&ref resolves global variable", func(t *testing.T) {
		p := &Parser{Data: &ParsedData{}}
		p.Data.Variables = append(p.Data.Variables, &Variable{Name: "g", Value: "GVAL", Scope: "global"})
		name, scope := "x", "global"
		got := p.tryEvalExpression("&g!", &name, &scope, 1)
		if got != "GVAL!" {
			t.Errorf("got %q, want %q", got, "GVAL!")
		}
	})

	t.Run("&ref resolves scoped variable", func(t *testing.T) {
		p := &Parser{Data: &ParsedData{}}
		p.Data.Variables = append(p.Data.Variables, &Variable{Name: "loc", Value: "LOCVAL", Scope: "mycmd"})
		name, scope := "x", "mycmd"
		got := p.tryEvalExpression("&loc", &name, &scope, 1)
		if got != "LOCVAL" {
			t.Errorf("got %q, want %q", got, "LOCVAL")
		}
	})

	t.Run("unknown &ref leaves no text (findVariable fails silently)", func(t *testing.T) {
		p := &Parser{Data: &ParsedData{}}
		name, scope := "x", "global"
		// The reference is consumed but resolves to nothing.
		got := p.tryEvalExpression("a&missing b", &name, &scope, 1)
		if got != "a b" {
			t.Errorf("got %q, want %q", got, "a b")
		}
	})

	t.Run("$ creates a lazy command and stops evaluation", func(t *testing.T) {
		p := &Parser{Data: &ParsedData{}}
		name, scope := "myvar", "global"
		_ = p.tryEvalExpression("$ echo hi", &name, &scope, 1)
		if len(p.Data.Commands) != 1 {
			t.Fatalf("expected 1 lazy command, got %d", len(p.Data.Commands))
		}
		lc := p.Data.Commands[0]
		if lc.LazyEval == nil {
			t.Fatal("expected LazyEval to be set")
		}
		if lc.LazyEval.VarName != "myvar" || lc.LazyEval.Scope != "global" {
			t.Errorf("lazy eval = %+v", lc.LazyEval)
		}
		wantBody := "$ echo hi"
		if len(lc.Body) != 1 || lc.Body[0].Shell != wantBody {
			t.Errorf("body = %#v, want [%q]", lc.Body, wantBody)
		}
	})

	t.Run("$ without varName/context is treated as literal", func(t *testing.T) {
		p := &Parser{Data: &ParsedData{}}
		// No varName/scope => the $ branch is skipped, $ stays literal.
		got := p.tryEvalExpression("$5", nil, nil, 1)
		if got != "$5" {
			t.Errorf("got %q, want %q", got, "$5")
		}
	})
}

// TestParseVarValidation covers the new validation paths in parseVar.
func TestParseVarValidation(t *testing.T) {
	t.Run("name containing var is extracted correctly", func(t *testing.T) {
		p := &Parser{Data: &ParsedData{}}
		if err := p.parseVar("var var_name = x", "global", 1); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(p.Data.Variables) != 1 {
			t.Fatalf("expected 1 variable, got %d", len(p.Data.Variables))
		}
		if p.Data.Variables[0].Name != "var_name" {
			t.Errorf("name = %q, want %q", p.Data.Variables[0].Name, "var_name")
		}
	})

	t.Run("empty name returns an error", func(t *testing.T) {
		p := &Parser{Data: &ParsedData{}}
		err := p.parseVar("var = x", "global", 1)
		if err == nil {
			t.Fatal("expected error for empty variable name, got nil")
		}
		if len(p.Data.Variables) != 0 {
			t.Errorf("no variable should be created on error, got %d", len(p.Data.Variables))
		}
	})

	t.Run("bare var with no equals creates empty-valued variable", func(t *testing.T) {
		p := &Parser{Data: &ParsedData{}}
		if err := p.parseVar("var onlyname", "global", 1); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(p.Data.Variables) != 1 || p.Data.Variables[0].Name != "onlyname" {
			t.Fatalf("expected single variable 'onlyname', got %#v", p.Data.Variables)
		}
		if p.Data.Variables[0].Value != "" {
			t.Errorf("expected empty value, got %q", p.Data.Variables[0].Value)
		}
	})
}

// TestExtractWorkDir covers the "in <dir>" modifier extraction.
func TestExtractWorkDir(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "no workdir", input: "build {", want: ""},
		{name: "no workdir with args", input: "build (arg1) {", want: ""},
		{name: "no workdir with prereqs", input: "build < test {", want: ""},
		{name: "simple workdir", input: "build in subdir {", want: "subdir"},
		{name: "workdir with args", input: "build (arg1) in subdir {", want: "subdir"},
		// "in <dir>" after "<" belongs to the prerequisites, not the command.
		{name: "workdir before prereqs", input: "build in subdir < test {", want: "subdir"},
		{name: "prereq in dir does not set command workdir", input: "build < test in subdir {", want: ""},
		{name: "workdir with args and prereqs", input: "deploy (env) in dir < build in deep/dir {", want: "dir"},
		{name: "cloud command with workdir", input: "|deploy| in cloud/dir {", want: "cloud/dir"},
		{name: "workdir with dot path", input: "build in ./local {", want: "./local"},
		{name: "workdir with env ref", input: "build in @HOMEDIR/projects {", want: "@HOMEDIR/projects"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractWorkDir(tt.input)
			if got != tt.want {
				t.Errorf("extractWorkDir(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestParseCommandWorkDir verifies the WorkDir field is populated on parsed commands.
func TestParseCommandWorkDir(t *testing.T) {
	input := "build in subdir {\n    echo hi\n}"
	lines := strings.Split(input, "\n")
	parser := &Parser{Data: &ParsedData{}, Lines: lines}

	if _, err := parser.parseCommand(0, input, false, 1, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(parser.Data.Commands) != 1 {
		t.Fatalf("expected 1 command, got %d", len(parser.Data.Commands))
	}
	if parser.Data.Commands[0].WorkDir != "subdir" {
		t.Errorf("WorkDir = %q, want %q", parser.Data.Commands[0].WorkDir, "subdir")
	}
}

// TestParseIfBlock verifies that if/else blocks are parsed into the body tree.
func TestParseIfBlock(t *testing.T) {
	input := `build {
    $ echo before
    if "1" == "1" {
        $ echo then-branch
    } else {
        $ echo else-branch
    }
    $ echo after
}`
	lines := strings.Split(input, "\n")
	parser := &Parser{Data: &ParsedData{}, Lines: lines}

	if _, err := parser.parseCommand(0, "build {", false, 1, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(parser.Data.Commands) != 1 {
		t.Fatalf("expected 1 command, got %d", len(parser.Data.Commands))
	}
	cmd := parser.Data.Commands[0]
	// Expected body: [shell:before, if, shell:after]
	if len(cmd.Body) != 3 {
		t.Fatalf("expected 3 body statements, got %d: %#v", len(cmd.Body), cmd.Body)
	}
	if cmd.Body[0].Type != "shell" || cmd.Body[0].Shell != "$ echo before" {
		t.Errorf("stmt[0] = %#v, want shell '$ echo before'", cmd.Body[0])
	}
	if cmd.Body[1].Type != "if" {
		t.Errorf("stmt[1] type = %q, want 'if'", cmd.Body[1].Type)
	}
	if cmd.Body[1].Cond != `"1" == "1"` {
		t.Errorf("stmt[1] cond = %q, want %q", cmd.Body[1].Cond, `"1" == "1"`)
	}
	if len(cmd.Body[1].ThenBody) != 1 || cmd.Body[1].ThenBody[0].Shell != "$ echo then-branch" {
		t.Errorf("then body = %#v", cmd.Body[1].ThenBody)
	}
	if len(cmd.Body[1].ElseBody) != 1 || cmd.Body[1].ElseBody[0].Shell != "$ echo else-branch" {
		t.Errorf("else body = %#v", cmd.Body[1].ElseBody)
	}
	if cmd.Body[2].Type != "shell" || cmd.Body[2].Shell != "$ echo after" {
		t.Errorf("stmt[2] = %#v, want shell '$ echo after'", cmd.Body[2])
	}
}

// TestParseIfBlockNoElse verifies if without else.
func TestParseIfBlockNoElse(t *testing.T) {
	input := `build {
    if "1" == "1" {
        $ echo only-then
    }
}`
	lines := strings.Split(input, "\n")
	parser := &Parser{Data: &ParsedData{}, Lines: lines}

	if _, err := parser.parseCommand(0, "build {", false, 1, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cmd := parser.Data.Commands[0]
	if len(cmd.Body) != 1 || cmd.Body[0].Type != "if" {
		t.Fatalf("expected single if statement, got %#v", cmd.Body)
	}
	if len(cmd.Body[0].ElseBody) != 0 {
		t.Errorf("expected empty else body, got %#v", cmd.Body[0].ElseBody)
	}
}

// TestParseForBlock verifies a basic for loop parses to the right structure.
func TestParseForBlock(t *testing.T) {
	input := `build {
    $ echo before
    for item in a, b, c {
        $ echo &item
    }
    $ echo after
}`
	lines := strings.Split(input, "\n")
	parser := &Parser{Data: &ParsedData{}, Lines: lines}

	if _, err := parser.parseCommand(0, "build {", false, 1, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(parser.Data.Commands) != 1 {
		t.Fatalf("expected 1 command, got %d", len(parser.Data.Commands))
	}
	cmd := parser.Data.Commands[0]
	// Expected body: [shell:before, for, shell:after]
	if len(cmd.Body) != 3 {
		t.Fatalf("expected 3 body statements, got %d: %#v", len(cmd.Body), cmd.Body)
	}
	if cmd.Body[0].Type != "shell" || cmd.Body[0].Shell != "$ echo before" {
		t.Errorf("stmt[0] = %#v, want shell '$ echo before'", cmd.Body[0])
	}
	if cmd.Body[1].Type != "for" {
		t.Fatalf("stmt[1] type = %q, want 'for'", cmd.Body[1].Type)
	}
	if cmd.Body[1].LoopVar != "item" {
		t.Errorf("loop var = %q, want 'item'", cmd.Body[1].LoopVar)
	}
	if cmd.Body[1].LoopItems != "a, b, c" {
		t.Errorf("loop items = %q, want 'a, b, c'", cmd.Body[1].LoopItems)
	}
	if len(cmd.Body[1].LoopBody) != 1 || cmd.Body[1].LoopBody[0].Shell != "$ echo &item" {
		t.Errorf("loop body = %#v", cmd.Body[1].LoopBody)
	}
	if cmd.Body[2].Type != "shell" || cmd.Body[2].Shell != "$ echo after" {
		t.Errorf("stmt[2] = %#v, want shell '$ echo after'", cmd.Body[2])
	}
}

func TestParseForBlockWithIf(t *testing.T) {
	input := `build {
    for item in a, b {
        if "&item" == "a" {
            $ echo yes
        } else {
            $ echo no
        }
        $ echo tail
    }
    $ echo after
}`
	lines := strings.Split(input, "\n")
	parser := &Parser{Data: &ParsedData{}, Lines: lines}

	if _, err := parser.parseCommand(0, "build {", false, 1, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cmd := parser.Data.Commands[0]
	// Expected body: [for, shell:after]
	if len(cmd.Body) != 2 {
		t.Fatalf("expected 2 body statements, got %d: %#v", len(cmd.Body), cmd.Body)
	}
	if cmd.Body[0].Type != "for" {
		t.Fatalf("stmt[0] type = %q, want 'for'", cmd.Body[0].Type)
	}

	loopBody := cmd.Body[0].LoopBody

	if len(loopBody) != 2 {
		t.Fatalf("expected 2 statements in loop body, got %d: %#v", len(loopBody), loopBody)
	}
	if loopBody[0].Type != "if" {
		t.Errorf("loop body[0] type = %q, want 'if'", loopBody[0].Type)
	}
	if len(loopBody[0].ThenBody) != 1 || loopBody[0].ThenBody[0].Shell != "$ echo yes" {
		t.Errorf("then body = %#v", loopBody[0].ThenBody)
	}
	if len(loopBody[0].ElseBody) != 1 || loopBody[0].ElseBody[0].Shell != "$ echo no" {
		t.Errorf("else body = %#v", loopBody[0].ElseBody)
	}
	if loopBody[1].Type != "shell" || loopBody[1].Shell != "$ echo tail" {
		t.Errorf("loop body[1] = %#v, want shell '$ echo tail'", loopBody[1])
	}
	if cmd.Body[1].Type != "shell" || cmd.Body[1].Shell != "$ echo after" {
		t.Errorf("stmt[1] = %#v, want shell '$ echo after'", cmd.Body[1])
	}
}

// TestParseForBlockNestedFor verifies a for loop nests inside another for.
func TestParseForBlockNestedFor(t *testing.T) {
	input := `build {
    for outer in x, y {
        for inner in 1, 2 {
            $ echo &outer &inner
        }
    }
    $ echo done
}`
	lines := strings.Split(input, "\n")
	parser := &Parser{Data: &ParsedData{}, Lines: lines}

	if _, err := parser.parseCommand(0, "build {", false, 1, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cmd := parser.Data.Commands[0]
	if len(cmd.Body) != 2 || cmd.Body[0].Type != "for" {
		t.Fatalf("expected [for, shell], got %#v", cmd.Body)
	}
	outer := cmd.Body[0].LoopBody
	if len(outer) != 1 || outer[0].Type != "for" {
		t.Fatalf("expected nested for in outer loop body, got %#v", outer)
	}
	if len(outer[0].LoopBody) != 1 || outer[0].LoopBody[0].Shell != "$ echo &outer &inner" {
		t.Errorf("inner loop body = %#v", outer[0].LoopBody)
	}
	if cmd.Body[1].Shell != "$ echo done" {
		t.Errorf("stmt after loops = %#v", cmd.Body[1])
	}
}

// TestEvaluateCondition covers the condition evaluator.
func TestEvaluateCondition(t *testing.T) {
	tests := []struct {
		name string
		cond string
		want bool
	}{
		{name: "string equal true", cond: `"hello" == "hello"`, want: true},
		{name: "string equal false", cond: `"hello" == "world"`, want: false},
		{name: "string not equal true", cond: `"a" != "b"`, want: true},
		{name: "numeric equal true", cond: `"18" == "18"`, want: true},
		{name: "numeric gt true", cond: `"20" > "18"`, want: true},
		{name: "numeric gt false", cond: `"18" > "20"`, want: false},
		{name: "numeric gte equal", cond: `"18" >= "18"`, want: true},
		{name: "numeric lt true", cond: `"2" < "10"`, want: true},
		{name: "numeric lte equal", cond: `"5" <= "5"`, want: true},
		{name: "lexicographic when non-numeric", cond: `"abc" < "abd"`, want: true},
		{name: "mixed numeric and string", cond: `"18" > "abc"`, want: false}, // falls back to string
		{name: "contains true", cond: `"windows/amd64" contains "windows"`, want: true},
		{name: "contains false", cond: `"darwin/arm64" contains "windows"`, want: false},
		{name: "contains substring mid", cond: `"darwin-arm64" contains "arm"`, want: true},
		{name: "contains exact match", cond: `"abc" contains "abc"`, want: true},
		{name: "contains empty needle", cond: `"abc" contains ""`, want: true},
		{name: "contains unquoted left operand", cond: `windows/amd64 contains "windows"`, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := evaluateCondition(tt.cond); got != tt.want {
				t.Errorf("evaluateCondition(%q) = %v, want %v", tt.cond, got, tt.want)
			}
		})
	}
}

// TestExtractOutputName covers the "as <name>" suffix extraction.
func TestExtractOutputName(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantShell string
		wantName  string
	}{
		{name: "no tag", input: `$ echo hello`, wantShell: `$ echo hello`, wantName: ""},
		{name: "simple tag", input: `$ echo hello as greeting`, wantShell: `$ echo hello`, wantName: "greeting"},
		{name: "tag with underscore", input: `$ echo hi as my_output`, wantShell: `$ echo hi`, wantName: "my_output"},
		{name: "tag with number", input: `$ echo v as version2`, wantShell: `$ echo v`, wantName: "version2"},
		{name: "as inside quotes not a tag", input: `$ echo "hello as world"`, wantShell: `$ echo "hello as world"`, wantName: ""},
		{name: "non-identifier suffix ignored", input: `$ echo hello as hello world`, wantShell: `$ echo hello as hello world`, wantName: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			shell, name := extractOutputName(tt.input)
			if shell != tt.wantShell {
				t.Errorf("shell = %q, want %q", shell, tt.wantShell)
			}
			if name != tt.wantName {
				t.Errorf("name = %q, want %q", name, tt.wantName)
			}
		})
	}
}

func TestParseCommandNameHeaderModifiers(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"|deploy| produces dist {", "deploy"},
		{"|deploy| onchange src/** {", "deploy"},
		{"|deploy| timeout 5s {", "deploy"},
		{"build produces dist/app.js, dist/lib.js < src/main.go {", "build"},
		{"build onchange src/** < src/main.go {", "build"},
		{"build timeout 5s {", "build"},
		{"build produces dist timeout 5s in dir {", "build"},
		{"build < dep.txt produces dist {", "build"},
	}
	for _, tc := range tests {
		if got := ParseCommandName(tc.input); got != tc.expected {
			t.Errorf("ParseCommandName(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

func TestExtractProducesCutAtTimeout(t *testing.T) {
	// `produces` used to swallow a following `timeout` modifier.
	line := "build produces dist timeout 5s {"
	got := extractProduces(line)
	if len(got) != 1 || got[0] != "dist" {
		t.Errorf("extractProduces = %v, want [dist]", got)
	}
	if timeout := extractTimeout("build timeout 5s produces dist {"); timeout != "5s" {
		t.Errorf("extractTimeout = %q, want 5s", timeout)
	}
}

func TestIndentedDefaultCommand(t *testing.T) {
	p := NewParserFromContent("Constfile", "build {\n  $ echo hi\n}\n\n  _ < build {\n    $ echo default\n  }\n")
	data, err := p.Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	def, err := data.GetDefaultCommand()
	if err != nil {
		t.Fatalf("indented `_` not detected as default: %v", err)
	}
	if def.Name != "_" {
		t.Errorf("default command name = %q, want _", def.Name)
	}
}

func TestExecuteNoDefaultCommandErrors(t *testing.T) {
	data := &ParsedData{
		Commands: []*Command{
			{Name: "build", Body: shellBody("echo hi")},
		},
	}
	data.buildIndexMaps()
	executor := NewExecutor(data, false, false)
	if err := executor.Execute(nil); err == nil {
		t.Fatal("Execute with no targets and no default should error")
	}
}

func TestCacheKeyWithoutRegisteredFlags(t *testing.T) {
	// An executor built without RegisterArgumentFlags used to panic in
	// cacheKey when a command had file deps and arguments.
	cmd := &Command{
		Name:      "c",
		FileDeps:  []string{"dep.txt"},
		Arguments: []*Argument{{Name: "a"}},
	}
	data := &ParsedData{Commands: []*Command{cmd}}
	data.buildIndexMaps()
	e := NewExecutor(data, false, false)
	e.SetBaseDir(t.TempDir())
	if err := e.Execute([]string{"c"}); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
}

func TestResolveCloudFileDefaultName(t *testing.T) {
	dir := t.TempDir()
	e := NewExecutor(&ParsedData{}, false, false)
	e.SetBaseDir(dir)
	if got := e.resolveCloudFile(); got != filepath.Join(dir, "construct-cloud.json") {
		t.Errorf("resolveCloudFile = %q, want %q", got, filepath.Join(dir, "construct-cloud.json"))
	}
	t.Setenv("CONSTRUCT_CLOUD_FILE", "custom.json")
	if got := e.resolveCloudFile(); got != "custom.json" {
		t.Errorf("resolveCloudFile with env override = %q, want custom.json", got)
	}
}

func BenchmarkParse(b *testing.B) {
	var sb strings.Builder
	sb.WriteString("var os = linux\nvar arch = arm64\n\n")
	for i := 0; i < 250; i++ {
		fmt.Fprintf(&sb, "build%d (env, opt region) produces dist/app%d < src/main.go in cmd%d {\n", i, i, i%10)
		sb.WriteString("    if \"&os\" == \"linux\" && \"&arch\" == \"arm64\" {\n")
		sb.WriteString("        for f in src/*.go {\n            $ echo building &f\n        }\n    }\n")
		sb.WriteString("    switch \"&os\" {\n        case \"linux\" { $ echo lnx }\n        default { $ echo other }\n    }\n}\n\n")
	}
	content := sb.String()
	b.SetBytes(int64(len(content)))

	for b.Loop() {
		if _, err := NewParserFromContent("Constfile", content).Parse(); err != nil {
			b.Fatal(err)
		}
	}
}
