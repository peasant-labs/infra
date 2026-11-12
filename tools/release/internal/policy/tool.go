package policy

import (
	"strings"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
	regexp "github.com/wasilibs/go-re2"
)

// This file names what the rules look for in a workflow's text: the tool's
// command line, the step outputs it prints, the repository variables it is
// passed, and the GitHub expressions that read them. Every pattern and every
// message builds its spelling from here, so a rule and the message that
// explains it cannot disagree.

// toolName is the command a consumer workflow runs after building it from
// its infra checkout.
const toolName = "release-intent"

// The subcommands and flags of the tool that the rules read in a run line.
// The command package owns the full sets as its Command and Flag types; it is
// package main and cannot be imported, so the spellings the rules read are
// named here once and nowhere else in this package.
const (
	toolCmdPlan          = "plan"
	toolCmdReleasePR     = "release-pr"
	toolCmdFreezePlan    = "freeze-plan"
	toolCmdCheckContract = "check-contract"
	toolCmdQueueGate     = "queue-gate"
	toolCmdClassify      = "classify"

	toolFlagVerdict = "--verdict"
	toolFlagFloor   = "--floor"
	toolFlagMode    = "--mode"
	toolFlagWrite   = "--write"
	toolFlagUntil   = "--until"
)

// toolCall renders a regex that matches the tool run with one of the given
// subcommands, e.g. release-intent plan.
func toolCall(commands ...string) string {
	quoted := make([]string, 0, len(commands))
	for _, c := range commands {
		quoted = append(quoted, regexp.QuoteMeta(c))
	}
	return regexp.QuoteMeta(toolName) + `\s+(` + strings.Join(quoted, "|") + `)\b`
}

// stepIDPattern matches a step id inside an expression.
const stepIDPattern = `[A-Za-z0-9_-]+`

// stepOutput renders the expression that reads one step's output, e.g.
// steps.plan.outputs.action. step is an id, or a placeholder in a message.
func stepOutput(step string, key intent.OutputKey) string {
	return "steps." + step + "." + stepOutputSuffix(key)
}

// stepOutputSuffix renders the part of a step-output expression after the
// step id, e.g. outputs.action.
func stepOutputSuffix(key intent.OutputKey) string {
	return "outputs." + key.String()
}

// stepOutputPattern renders a regex that matches a read of the given output
// on any step and captures the step id.
func stepOutputPattern(key intent.OutputKey) string {
	return `steps\.(` + stepIDPattern + `)\.` + regexp.QuoteMeta(stepOutputSuffix(key))
}

// repoVar renders the expression path that reads a repository variable,
// e.g. vars.RELEASE_BATCH_MODE.
func repoVar(v intent.RepoVariable) string {
	return "vars." + v.String()
}

// quotedLiteral renders a value as a single-quoted expression literal.
func quotedLiteral(value string) string {
	return "'" + value + "'"
}

// equals renders the canonical == comparison of an expression with a literal.
func equals(expr, value string) string {
	return expr + " == " + quotedLiteral(value)
}

// tagModeComparison is the one canonical tag-mode comparison, shared by the
// matcher and the messages: the batch-mode variable against the tag mode.
var tagModeComparison = equals(repoVar(intent.VarBatchMode), intent.ModeTag.String())
