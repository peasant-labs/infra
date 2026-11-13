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
		names := make([]string, 0, len(required))
		for _, r := range required {
			names = append(names, r.Context)
		}
		sort.Strings(names)
		// Bare context names, not key=value lines: this list feeds branch
		// protection, never a step output.
		for _, name := range names {
			fmt.Println(name)
		}
		return nil
	}
	if len(diagnostics) > 0 {
		for _, d := range diagnostics {
			fmt.Fprintf(os.Stderr, "%s %s: rule %s in %s: %s\n", intent.ProgramName, intent.CommandCheckWorkflows, d.Rule, diagnosticWhere(d), d.Message)
		}
		first := diagnostics[0]
		where := diagnosticWhere(first) + ", checked by " + intent.ProgramName + " " + intent.CommandCheckWorkflows.String() + " against " + policyPath
		return rejectAs(intent.CommandCheckWorkflows, intent.PolicyViolation(string(first.Rule), where, first.Message))
	}
	return emit(outBool(intent.OutputOK, true))
}

// diagnosticWhere names the workflow, and the job when the rule judged one.
func diagnosticWhere(d policy.Diagnostic) string {
	where := d.Workflow
	if d.Job != "" {
		where += " job " + d.Job
	}
	return where
}
