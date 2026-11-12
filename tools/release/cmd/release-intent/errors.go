package main

import (
	"errors"
	"fmt"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
)

// RejectError attaches the invoking subcommand to a contract rejection so the
// human message names the actual command. It wraps *intent.Error instead of
// copying its fields: the contract package owns the six parts.
type RejectError struct {
	Command intent.Command
	// Flag narrows the command to one of its modes when the rejection belongs
	// to that mode; empty otherwise.
	Flag intent.Flag
	// Lines are machine lines the command decided before the rejection; they
	// print ahead of the error line.
	Lines []outputLine
	Err   *intent.Error
}

// invocation names what the user ran: the command, plus the mode flag when
// the rejection belongs to one.
func (e *RejectError) invocation() string {
	name := intent.ProgramName
	if e.Command != "" {
		name += " " + e.Command.String()
	}
	if e.Flag != "" {
		name += " " + e.Flag.String()
	}
	return name
}

func (e *RejectError) Error() string { return e.Err.Error() }

func (e *RejectError) Unwrap() error { return e.Err }

func (e *RejectError) machineLines() []outputLine {
	return append(append([]outputLine{}, e.Lines...), out(intent.OutputError, e.Err.Code.String()))
}

func (e *RejectError) humanMessage() string {
	return fmt.Sprintf("%s: rejected %q\n  what:  %s (%s)\n  why:   %s\n  where: %s\n  when:  %s\n  means: %s\n  fix:   %s",
		e.invocation(), e.Err.Code, e.Err.What, e.Err.Code, e.Err.Why, e.Err.Where, e.Err.When, e.Err.Means, e.Err.Fix)
}

// rejectAs wraps a contract rejection with the invoking command for the
// exit-2 path. Every other error passes through unchanged for exit 1.
func rejectAs(command intent.Command, err error) error {
	return rejectAsMode(command, "", err)
}

// rejectAsMode is rejectAs for a rejection that belongs to one mode flag of
// the command. An error already wrapped keeps its first, more specific name.
func rejectAsMode(command intent.Command, mode intent.Flag, err error) error {
	if _, ok := asReject(err); ok {
		return err
	}
	var ie *intent.Error
	if errors.As(err, &ie) {
		return &RejectError{Command: command, Flag: mode, Err: ie}
	}
	return err
}

// UsageError is a CLI usage failure: exit 1 with an actionable message.
type UsageError struct{ msg string }

func (e *UsageError) Error() string { return e.msg }

func usageErr(format string, args ...any) error {
	return &UsageError{msg: fmt.Sprintf(format, args...)}
}

func asReject(err error) (*RejectError, bool) {
	var r *RejectError
	if errors.As(err, &r) {
		return r, true
	}
	return nil, false
}
