package main

import (
	"fmt"
	"os"
	"sort"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
	"github.com/peasant-labs/infra/tools/release/internal/policy"
)

// runCheckWorkflows implements the check-workflows command.
//
// It delegates to the policy package's exported checker. The policy file
// defaults to the policy package's conventional path. The release config is
// always read: the queue rules resolve bot branches from it.
func runCheckWorkflows(flags flagValues) error {
	policyPath := flags.ValueOr(intent.FlagPolicy, policy.DefaultPath)
	dir := flags.Value(intent.FlagDir)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return fmt.Errorf("%s: cannot read workflows directory %q: %v. Fix the %s path or run from the repository root", intent.CommandCheckWorkflows, dir, err, intent.FlagDir)
	}
	queuePolicy, err := policy.LoadQueuePolicy(policyPath)
	if err != nil {
		return err
	}
	cfg, err := loadConfig(intent.CommandCheckWorkflows, intent.DefaultConfigPath)
	if err != nil {
		return err
	}
	diagnostics, required := policy.CheckWorkflows(queuePolicy, cfg, dir)
	if flags.Has(intent.FlagPrintRequired) {
		// The list feeds the queue ruleset, so it prints even when the tree
		// is rejected: an operator comparing contexts needs the names either
		// way. The exit status still carries the verdict, so reading the list
		// can never pass the gate the full run exists to be.
		printRequired(required)
		if len(diagnostics) == 0 {
			return nil
		}
		return rejectWorkflows(policyPath, diagnostics, false)
	}
	if len(diagnostics) > 0 {
		return rejectWorkflows(policyPath, diagnostics, true)
	}
	return emit(outBool(intent.OutputOK, true))
}

// printRequired writes the bare sorted context names, never a step output:
// the list is compared with the branch ruleset.
func printRequired(required []policy.RequiredContext) {
	names := make([]string, 0, len(required))
	for _, r := range required {
		names = append(names, r.Context)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Println(name)
	}
}

// rejectWorkflows reports every diagnostic on stderr and returns the typed
// rejection. machine is false for the print mode, whose stdout is the bare
// context list and must stay parseable; the human message still carries the
// rule on stderr and the exit status is the same either way.
func rejectWorkflows(policyPath string, diagnostics []policy.Diagnostic, machine bool) error {
	for _, d := range diagnostics {
		fmt.Fprintf(os.Stderr, "%s %s: rule %s in %s: %s\n", intent.ProgramName, intent.CommandCheckWorkflows, d.Rule, diagnosticWhere(d), d.Message)
	}
	first := diagnostics[0]
	where := diagnosticWhere(first) + ", checked by " + intent.ProgramName + " " + intent.CommandCheckWorkflows.String() + " against " + policyPath
	err := rejectAs(intent.CommandCheckWorkflows, intent.PolicyViolation(string(first.Rule), where, first.Message))
	if r, ok := asReject(err); ok {
		r.NoMachineLines = !machine
	}
	return err
}

// diagnosticWhere names the workflow, and the job when the rule judged one.
func diagnosticWhere(d policy.Diagnostic) string {
	where := d.Workflow
	if d.Job != "" {
		where += " job " + d.Job
	}
	return where
}
