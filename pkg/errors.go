package pkg

import "fmt"

type ParseError struct {
	Line    int
	Column  int
	Message string
	Context string
}

func (e *ParseError) Error() string {
	if e.Context != "" {
		return fmt.Sprintf("parse error at line %d:%d: %s\n\t%s", e.Line, e.Column, e.Message, e.Context)
	}
	return fmt.Sprintf("parse error at line %d:%d: %s", e.Line, e.Column, e.Message)
}

func NewParseError(line, column int, message, context string) *ParseError {
	return &ParseError{
		Line:    line,
		Column:  column,
		Message: message,
		Context: context,
	}
}

type CircularDependencyError struct {
	Command string
	Path    []string
}

func (e *CircularDependencyError) Error() string {
	return fmt.Sprintf("circular dependency detected for command '%s': %s", e.Command, e.Path)
}

type MissingDependencyError struct {
	Command    string
	PrereqName string
}

func (e *MissingDependencyError) Error() string {
	return fmt.Sprintf("command '%s' depends on missing prerequisite '%s'", e.Command, e.PrereqName)
}
