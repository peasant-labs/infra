package main

import (
	"context"
	"strconv"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
)

// runPlan implements the plan command: the one release action for a branch
// and every value the workflow needs to carry it out.
func runPlan(flags flagValues) error {
	verdict := intent.VerdictNone
	if flags.Has(intent.FlagVerdict) {
		parsed, ok := parseReportedVerdict(flags.Value(intent.FlagVerdict))
		if !ok {
			return usageErr("%s (bad %s %q): pass %s", usageOf(intent.CommandPlan), intent.FlagVerdict, flags.Value(intent.FlagVerdict), shapeOf(intent.FlagVerdict).Meaning)
		}
		verdict = parsed
	}
	// An empty mode word is the unset repository variable: plan without
	// writing. Any other word outside the closed set is refused, never read
	// as plan-only.
	mode, err := intent.ParseMode(flags.Value(intent.FlagMode))
	if err != nil {
		return usageErr("%s (bad %s %q): %v", usageOf(intent.CommandPlan), intent.FlagMode, flags.Value(intent.FlagMode), err)
	}
	ctx := context.Background()
	runner, err := newExecGit()
	if err != nil {
		return err
	}
	facts, cfg, err := Gather(ctx, runner, intent.CommandPlan, flags.Value(intent.FlagBranch), intent.DefaultConfigPath, flags.Value(intent.FlagFloor), mode, verdict)
	if err != nil {
		return rejectAs(intent.CommandPlan, err)
	}
	decision, err := intent.Plan(cfg, facts)
	if err != nil {
		return rejectAs(intent.CommandPlan, err)
	}
	return emit(
		out(intent.OutputAction, decision.Action.String()),
		out(intent.OutputRangeBase, decision.RangeBase),
		out(intent.OutputVersionBase, decision.VersionBase),
		out(intent.OutputCandidate, decision.Candidate),
		out(intent.OutputCandidateFresh, candidateFreshValue(decision)),
		out(intent.OutputLevel, decision.Level.String()),
		outBool(intent.OutputBreaking, decision.Breaking),
		out(intent.OutputVersion, decision.Version.String()),
		out(intent.OutputTag, decision.Tag),
		out(intent.OutputError, ""),
	)
}

// candidateFreshValue renders the candidate's freshness for the step output:
// a boolean word when there is a candidate with a known comparison, and an
// empty value otherwise. Unknown is never printed as stale.
func candidateFreshValue(decision intent.Decision) string {
	if decision.Candidate == "" {
		return ""
	}
	switch decision.CandidateFresh {
	case intent.FreshnessFresh:
		return strconv.FormatBool(true)
	case intent.FreshnessStale:
		return strconv.FormatBool(false)
	default:
		return ""
	}
}
