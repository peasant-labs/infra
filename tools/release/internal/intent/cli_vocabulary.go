package intent

import (
	"slices"

	"github.com/peasant-labs/infra/tools/release/internal/closedset"
)

// This file owns the command line's vocabulary: the program name, the
// subcommands and the flags. The command dispatches on these members and the
// policy rules read them when they check how a consumer workflow calls the
// tool, so neither side spells one as a literal.

// ProgramName is the name of the release tool's binary, as workflows call it
// and as its messages print it.
const ProgramName = "release-intent"

// Command is one subcommand of the release tool. The zero value is no
// command.
type Command string

const (
	// CommandCheckConfig validates one release config file.
	CommandCheckConfig Command = "check-config"
	// CommandCheckWorkflows checks a workflows directory against the queue policy.
	CommandCheckWorkflows Command = "check-workflows"
	// CommandCheckTitle judges a PR title and the PR's protected-path changes.
	CommandCheckTitle Command = "check-title"
	// CommandClassify prints which bot PR kind a head branch belongs to.
	CommandClassify Command = "classify"
	// CommandGroupPR prints the PR number a merge-group ref names.
	CommandGroupPR Command = "group-pr"
	// CommandPlan prints the release plan for a branch.
	CommandPlan Command = "plan"
	// CommandReleasePR writes or checks the release content.
	CommandReleasePR Command = "release-pr"
	// CommandQueueGate prints the merge-queue hold decision for the release PR.
	CommandQueueGate Command = "queue-gate"
)

// AllCommands is the canonical closed set of subcommands the tool
// implements, in usage order. A command joins the set in the change that
// implements it.
var AllCommands = []Command{
	CommandCheckConfig,
	CommandCheckWorkflows,
	CommandCheckTitle,
	CommandClassify,
	CommandGroupPR,
	CommandPlan,
	CommandReleasePR,
	CommandQueueGate,
}

func (c Command) String() string { return string(c) }

// IsValid reports whether the command is one of the known members.
func (c Command) IsValid() bool { return slices.Contains(AllCommands, c) }

// ParseCommand returns the member spelled raw and refuses anything else.
func ParseCommand(raw string) (Command, error) {
	return closedset.Parse(AllCommands, raw, parsing("subcommand", "intent.ParseCommand"))
}

// Flag is one command-line flag of the release tool, spelled as it is typed.
// The zero value is no flag.
type Flag string

const (
	// FlagConfig names the release config file to validate.
	FlagConfig Flag = "--config"
	// FlagPolicy names the queue policy file.
	FlagPolicy Flag = "--policy"
	// FlagDir names the workflows directory.
	FlagDir Flag = "--dir"
	// FlagPrintRequired prints the required check contexts.
	FlagPrintRequired Flag = "--print-required"
	// FlagBase names the PR base revision.
	FlagBase Flag = "--base"
	// FlagHead names the PR head revision.
	FlagHead Flag = "--head"
	// FlagAuthor names the PR author login.
	FlagAuthor Flag = "--author"
	// FlagHeadBranch names the PR head branch.
	FlagHeadBranch Flag = "--head-branch"
	// FlagRev names the base revision whose release config judges a PR.
	FlagRev Flag = "--rev"
	// FlagRef names a merge-group ref.
	FlagRef Flag = "--ref"
	// FlagBranch names the branch to plan or build release content from.
	FlagBranch Flag = "--branch"
	// FlagMode carries the job Mode.
	FlagMode Flag = "--mode"
	// FlagFloor names the recovery floor commit.
	FlagFloor Flag = "--floor"
	// FlagVerdict carries the pre-tag Verdict.
	FlagVerdict Flag = "--verdict"
	// FlagWrite writes the release content into the working tree.
	FlagWrite Flag = "--write"
	// FlagCheck compares the working tree with the release content.
	FlagCheck Flag = "--check"
	// FlagCurrent names the open release branch to compare before a push.
	FlagCurrent Flag = "--current"
	// FlagQueue names the file holding the raw merge-queue response.
	FlagQueue Flag = "--queue"
	// FlagReleasePR carries the release PR number.
	FlagReleasePR Flag = "--release-pr"
)

// AllFlags is the canonical closed set of flags across every subcommand.
var AllFlags = []Flag{
	FlagConfig, FlagPolicy, FlagDir, FlagPrintRequired,
	FlagBase, FlagHead, FlagAuthor, FlagHeadBranch,
	FlagRev, FlagRef,
	FlagBranch, FlagMode, FlagFloor, FlagVerdict,
	FlagWrite, FlagCheck, FlagCurrent,
	FlagQueue, FlagReleasePR,
}

func (f Flag) String() string { return string(f) }

// IsValid reports whether the flag is one of the known members.
func (f Flag) IsValid() bool { return slices.Contains(AllFlags, f) }

// ParseFlag returns the member spelled raw and refuses anything else.
func ParseFlag(raw string) (Flag, error) {
	return closedset.Parse(AllFlags, raw, parsing("flag", "intent.ParseFlag"))
}
