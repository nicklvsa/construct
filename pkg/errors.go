package pkg

import (
	"fmt"
)

// ParseError represents a parsing error with location information
type ParseError struct {
	Line    int    // Line number (1-indexed)
	Column  int    // Column number (1-indexed)
	Message string // Error message
	Context string // Line content for context
}

func (e *ParseError) Error() string {
	if e.Context != "" {
		return fmt.Sprintf("parse error at line %d:%d: %s\n\t%s", e.Line, e.Column, e.Message, e.Context)
	}
	return fmt.Sprintf("parse error at line %d:%d: %s", e.Line, e.Column, e.Message)
}

// NewParseError creates a new ParseError
func NewParseError(line, column int, message, context string) *ParseError {
	return &ParseError{
		Line:    line,
		Column:  column,
		Message: message,
		Context: context,
	}
}

// CircularDependencyError represents a circular dependency in prerequisites
type CircularDependencyError struct {
	Command string
	Path    []string // Dependency path that shows the cycle
}

func (e *CircularDependencyError) Error() string {
	return fmt.Sprintf("circular dependency detected for command '%s': %s", e.Command, e.Path)
}

// MissingDependencyError represents a missing prerequisite command
type MissingDependencyError struct {
	Command    string
	PrereqName string
}

func (e *MissingDependencyError) Error() string {
	return fmt.Sprintf("command '%s' depends on missing prerequisite '%s'", e.Command, e.PrereqName)
}
