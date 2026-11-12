package policy

import (
	"strings"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
	regexp "github.com/wasilibs/go-re2"
)

// These helpers build workflow patterns and messages from the shared tool
// vocabulary in intent, so the policy cannot restate command or flag names.

// toolCall renders a regex that matches the tool run with one of the given
// subcommands, e.g. release-intent plan.
func toolCall(commands ...intent.Command) string {
	quoted := make([]string, 0, len(commands))
	for _, c := range commands {
		quoted = append(quoted, regexp.QuoteMeta(c.String()))
	}
	return regexp.QuoteMeta(intent.ProgramName) + `\s+(` + strings.Join(quoted, "|") + `)(?:\s|$|[;&|])`
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
