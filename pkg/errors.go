package pkg

import (
	"errors"
	"fmt"
	"os/exec"
)

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
	var pe *ParseError
	if errors.As(err, &pe) {
		return pe
	}
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

type FailError struct {
	Message string
	File    string
	Line    int
}

func (e *FailError) Error() string {
	if e.File != "" {
		return fmt.Sprintf("fail: %s (%s:%d)", e.Message, e.File, e.Line)
	}
	return fmt.Sprintf("fail: %s", e.Message)
}

// CommandError is returned when a shell statement exits non-zero.
type CommandError struct {
	Cmd      string
	ExitCode int
	Stderr   string
	File     string
	Line     int
	TimedOut bool
	Timeout  string
}

func (e *CommandError) Error() string {
	loc := ""
	if e.File != "" {
		loc = fmt.Sprintf(" (%s:%d)", e.File, e.Line)
	}
	if e.TimedOut {
		prefix := fmt.Sprintf("command '%s' timed out after %s (exit 124)%s", e.Cmd, e.Timeout, loc)
		if e.Stderr != "" {
			return prefix + ": " + e.Stderr
		}
		return prefix
	}
	if e.Stderr != "" {
		return fmt.Sprintf("command '%s' failed (exit %d)%s: %s", e.Cmd, e.ExitCode, loc, e.Stderr)
	}
	return fmt.Sprintf("command '%s' failed (exit %d)%s", e.Cmd, e.ExitCode, loc)
}

// KeepGoingError aggregates failures from --keep-going runs.
type KeepGoingError struct {
	Errs []error
}

func (e *KeepGoingError) Error() string {
	return errors.Join(e.Errs...).Error()
}

func (e *KeepGoingError) ExitCode() int {
	for _, err := range e.Errs {
		var ce *CommandError
		if errors.As(err, &ce) {
			return ce.ExitCode
		}
	}
	return 1
}

// exitCodeOf maps a command-run error to a process exit code.
func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var ce *CommandError
	if errors.As(err, &ce) {
		return ce.ExitCode
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return 1
}
