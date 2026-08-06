package pkg

import "fmt"

type ParseError struct {
	File    string
	Line    int
	Column  int
	Message string
	Context string
}

func (e *ParseError) Error() string {
	loc := fmt.Sprintf("line %d:%d", e.Line, e.Column)
	if e.File != "" {
		loc = fmt.Sprintf("%s:%d:%d", e.File, e.Line, e.Column)
	}
	if e.Context != "" {
		return fmt.Sprintf("parse error at %s: %s\n\t%s", loc, e.Message, e.Context)
	}
	return fmt.Sprintf("parse error at %s: %s", loc, e.Message)
}

func NewParseError(file string, line, column int, message, context string) *ParseError {
	return &ParseError{
		File:    file,
		Line:    line,
		Column:  column,
		Message: message,
		Context: context,
	}
}

func (p *Parser) parseErr(lineNum int, err error, context string) *ParseError {
	return NewParseError(p.InputFile, lineNum, 1, err.Error(), context)
}

type CircularDependencyError struct {
	Command string
	Path    []string
	File    string
}

func (e *CircularDependencyError) Error() string {
	if e.File != "" {
		return fmt.Sprintf("circular dependency detected for command '%s' (%s): %s", e.Command, e.File, e.Path)
	}
	return fmt.Sprintf("circular dependency detected for command '%s': %s", e.Command, e.Path)
}

type MissingDependencyError struct {
	Command    string
	PrereqName string
	File       string
	Line       int
}

func (e *MissingDependencyError) Error() string {
	if e.File != "" {
		return fmt.Sprintf("command '%s' depends on missing prerequisite '%s' (%s:%d)", e.Command, e.PrereqName, e.File, e.Line)
	}
	return fmt.Sprintf("command '%s' depends on missing prerequisite '%s'", e.Command, e.PrereqName)
}
