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
	CommandCheckTitle    Command = "check-title"
	CommandCheckContract Command = "check-contract"
	CommandFreezePlan    Command = "freeze-plan"
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
	// CommandVerifyTag checks that a release tag is the prefix plus a version
	// and that its commit is on a branch, before anything publishes it.
	CommandVerifyTag Command = "verify-tag"
	// CommandInheritVerdict decides whether a release-only candidate inherits
	// its base commit's merge-queue verdict instead of running the full suite.
	CommandInheritVerdict Command = "inherit-verdict"
	// CommandPublishRelease creates, fills, verifies and publishes the
	// GitHub Release of a tag, by release id, from a local asset directory.
	CommandPublishRelease Command = "publish-release"
)

// AllCommands is the canonical closed set of subcommands the tool
// implements, in usage order. A command joins the set in the change that
// implements it.
var AllCommands = []Command{
	CommandCheckConfig,
	CommandCheckWorkflows,
	CommandCheckTitle,
	CommandCheckContract,
	CommandFreezePlan,
	CommandClassify,
	CommandGroupPR,
	CommandPlan,
	CommandReleasePR,
	CommandQueueGate,
	CommandVerifyTag,
	CommandInheritVerdict,
	CommandPublishRelease,
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
	// FlagNpmOnly checks only the global-npm-install rule without requiring a
	// queue policy or release config.
	FlagNpmOnly Flag = "--npm-only"
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
	// FlagFreezeFloor names the migration boundary for freeze subjects only.
	FlagFreezeFloor Flag = "--freeze-floor"
	// FlagVerdict carries the pre-tag Verdict.
	FlagVerdict Flag = "--verdict"
	// FlagWrite writes the release content into the working tree.
	FlagWrite Flag = "--write"
	// FlagCommit prepares and commits the generated release content locally.
	FlagCommit Flag = "--commit"
	// FlagCheck compares the working tree with the release content.
	FlagCheck Flag = "--check"
	// FlagCurrent names the open release branch to compare before a push.
	FlagCurrent Flag = "--current"
	// FlagQueue names the file holding the raw merge-queue response.
	FlagQueue Flag = "--queue"
	// FlagReleasePR carries the release PR number.
	FlagReleasePR Flag = "--release-pr"
	// FlagTag names the release tag to verify or publish.
	FlagTag            Flag = "--tag"
	FlagReport         Flag = "--report"
	FlagUntil          Flag = "--until"
	FlagState          Flag = "--state"
	FlagOut            Flag = "--out"
	FlagFreezePlan     Flag = "--freeze-plan"
	FlagPrintRangeBase Flag = "--print-range-base"
	// FlagRepo names the OWNER/NAME repository the command reads or
	// publishes to.
	FlagRepo Flag = "--repo"
	// FlagWait bounds how long a pending base verdict is waited for.
	FlagWait Flag = "--wait"
	// FlagAssets names the directory of files to publish as release assets.
	FlagAssets Flag = "--assets"
	// FlagNotes names the release notes file.
	FlagNotes Flag = "--notes"
	// FlagPrerelease carries whether the release is a prerelease.
	FlagPrerelease Flag = "--prerelease"
	// FlagLatest carries whether the release becomes the latest release.
	FlagLatest Flag = "--latest"
	// FlagRequireImmutable requires GitHub to report the release immutable.
	FlagRequireImmutable Flag = "--require-immutable"
	// FlagRehearse creates, fills and verifies a draft for a never-pushed
	// rehearsal tag, then deletes it. It never publishes.
	FlagRehearse Flag = "--rehearse"
)

// AllFlags is the canonical closed set of flags across every subcommand.
var AllFlags = []Flag{
	FlagConfig, FlagPolicy, FlagDir, FlagPrintRequired, FlagNpmOnly,
	FlagBase, FlagHead, FlagAuthor, FlagHeadBranch,
	FlagRev, FlagRef,
	FlagBranch, FlagMode, FlagFloor, FlagFreezeFloor, FlagVerdict,
	FlagWrite, FlagCommit, FlagCheck, FlagCurrent,
	FlagQueue, FlagReleasePR,
	FlagTag, FlagReport, FlagUntil, FlagState, FlagOut, FlagFreezePlan, FlagPrintRangeBase,
	FlagRepo, FlagWait,
	FlagAssets, FlagNotes, FlagPrerelease, FlagLatest, FlagRequireImmutable, FlagRehearse,
}

func (f Flag) String() string { return string(f) }

// IsValid reports whether the flag is one of the known members.
func (f Flag) IsValid() bool { return slices.Contains(AllFlags, f) }

// ParseFlag returns the member spelled raw and refuses anything else.
func ParseFlag(raw string) (Flag, error) {
	return closedset.Parse(AllFlags, raw, parsing("flag", "intent.ParseFlag"))
}

// FlagUse is one flag a command accepts and whether the command needs it.
type FlagUse struct {
	Flag     Flag
	Required bool
}

// Flags lists the flags the command accepts, in usage order, each marked
// required or optional. It is the one place a command's flag set is decided:
// the command line parses and prints its synopsis from it, and the workflow
// policy checks every invocation in a consumer workflow against it. A value
// outside the command set accepts no flags.
func (c Command) Flags() []FlagUse {
	switch c {
	case CommandCheckConfig:
		return []FlagUse{{FlagConfig, false}}
	case CommandCheckWorkflows:
		return []FlagUse{{FlagPolicy, false}, {FlagDir, true}, {FlagPrintRequired, false}, {FlagNpmOnly, false}}
	case CommandCheckTitle:
		return []FlagUse{{FlagBase, true}, {FlagHead, true}, {FlagAuthor, true}, {FlagHeadBranch, true}}
	case CommandCheckContract:
		// A report is mandatory except in print-range-base mode.
		return []FlagUse{{FlagReport, true}, {FlagUntil, false}, {FlagFloor, false}, {FlagPrintRangeBase, false}}
	case CommandFreezePlan:
		return []FlagUse{{FlagBranch, true}, {FlagState, true}, {FlagOut, true}, {FlagUntil, false}, {FlagFloor, false}, {FlagFreezeFloor, false}}
	case CommandClassify:
		return []FlagUse{{FlagAuthor, true}, {FlagHeadBranch, true}, {FlagRev, true}}
	case CommandGroupPR:
		return []FlagUse{{FlagRef, true}}
	case CommandPlan:
		return []FlagUse{{FlagBranch, true}, {FlagMode, true}, {FlagFloor, false}, {FlagVerdict, false}}
	case CommandReleasePR:
		return []FlagUse{{FlagBranch, true}, {FlagFloor, false}, {FlagWrite, false}, {FlagCommit, false}, {FlagCheck, false}, {FlagCurrent, false}, {FlagFreezePlan, false}}
	case CommandQueueGate:
		return []FlagUse{{FlagQueue, true}, {FlagReleasePR, true}}
	case CommandVerifyTag:
		return []FlagUse{{FlagTag, true}, {FlagBranch, true}}
	case CommandInheritVerdict:
		return []FlagUse{{FlagBase, true}, {FlagHead, true}, {FlagRepo, true}, {FlagWait, false}}
	case CommandPublishRelease:
		return []FlagUse{{FlagTag, true}, {FlagRepo, true}, {FlagAssets, true}, {FlagNotes, true}, {FlagPrerelease, true}, {FlagLatest, true}, {FlagRequireImmutable, false}, {FlagRehearse, false}}
	default:
		return nil
	}
}

// Accepts reports whether the command takes the flag at all.
func (c Command) Accepts(f Flag) bool {
	for _, use := range c.Flags() {
		if use.Flag == f {
			return true
		}
	}
	return false
}

// RequiredFlags lists the flags the command cannot run without, in usage
// order. It is read from Flags, never kept as a second list.
func (c Command) RequiredFlags() []Flag {
	var out []Flag
	for _, use := range c.Flags() {
		if use.Required {
			out = append(out, use.Flag)
		}
	}
	return out
}
