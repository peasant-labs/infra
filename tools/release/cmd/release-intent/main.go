// Command release-intent is the merge-queue release tool every repo runs from a
// pinned infra checkout. It holds every release decision: title grammar,
// version arithmetic, release config loading, Plan, Generate, QueueGate and
// the git fact collection behind them.
//
// The tool has no network access and never imports the GitHub client. The one
// impure seam is the git runner, which reads commits, tags and files from git
// through a hardened structured argv.
package main

import (
	"fmt"
	"os"

	"github.com/peasant-labs/infra/tools/release/internal/closedset"
	"github.com/peasant-labs/infra/tools/release/internal/intent"
)

func main() {
	if len(os.Args) < 2 {
		exitWith("", usageErr("usage: %s <%s> ...", intent.ProgramName, closedset.Join(intent.AllCommands, "|")))
	}
	command, err := parseCommand(os.Args[1])
	if err != nil {
		exitWith("", err)
	}
	if err := run(command, os.Args[2:]); err != nil {
		exitWith(command, err)
	}
}

// run dispatches one subcommand. The switch covers AllCommands; a member
// with no handler is a programming error, reported as one.
func run(command intent.Command, args []string) error {
	flags, err := parseFlags(command, args)
	if err != nil {
		return err
	}
	switch command {
	case intent.CommandCheckConfig:
		return runCheckConfig(flags)
	case intent.CommandCheckWorkflows:
		return runCheckWorkflows(flags)
	case intent.CommandCheckTitle:
		return runCheckTitle(flags)
	case intent.CommandClassify:
		return runClassify(flags)
	case intent.CommandGroupPR:
		return runGroupPR(flags)
	case intent.CommandPlan:
		return runPlan(flags)
	case intent.CommandReleasePR:
		return runReleasePR(flags)
	case intent.CommandQueueGate:
		return runQueueGate(flags)
	case intent.CommandVerifyTag:
		return runVerifyTag(flags)
	default:
		return fmt.Errorf("%s: subcommand %q is in the closed command set but has no handler in run (cmd/release-intent/main.go). This is a programming error in %s, not a user input issue: add the handler", intent.ProgramName, command, intent.ProgramName)
	}
}

// exitStatus is the process exit status. The zero value is acceptance.
type exitStatus int

const (
	// exitAccepted means the input was judged and accepted.
	exitAccepted exitStatus = 0
	// exitFailure means a usage or internal error: nothing was judged.
	exitFailure exitStatus = 1
	// exitRejected means the input was judged and rejected.
	exitRejected exitStatus = 2
)

// allExitStatuses is the canonical closed set of exit statuses.
var allExitStatuses = []exitStatus{exitAccepted, exitFailure, exitRejected}

// statusOf maps a handler result to its exit status. It is the one place
// the exit contract is decided.
func statusOf(err error) exitStatus {
	if err == nil {
		return exitAccepted
	}
	if _, ok := asReject(err); ok {
		return exitRejected
	}
	return exitFailure
}

// exitWith maps a handler error to the process exit status and streams.
//
// A rejection prints its machine channel (error=<code> and friends) on
// stdout and a six-part human message on stderr. A usage or internal failure
// prints an actionable message on stderr and no machine line unless the
// handler already emitted one.
func exitWith(command intent.Command, err error) {
	// A contract rejection a handler did not wrap still exits as a rejection,
	// named after the dispatched command.
	err = rejectAs(command, err)
	if r, ok := asReject(err); ok {
		// The step-output append is best effort here: the rejection already
		// decides the exit status, and stdout carries the same lines.
		_ = emit(r.machineLines()...)
		fmt.Fprintf(os.Stderr, "%s\n", r.humanMessage())
	} else {
		fmt.Fprintf(os.Stderr, "%s: %v\n", intent.ProgramName, err)
	}
	os.Exit(int(statusOf(err)))
}
