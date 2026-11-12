package main

import (
	"fmt"
	"os"
	"sort"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
	"github.com/peasant-labs/infra/tools/release/internal/policy"
)

// runCheckWorkflows implements:
// release-intent check-workflows [--policy P] --dir .github/workflows [--print-required]
//
// It delegates to the policy package's exported checker. --policy defaults to
// the policy package's conventional path. The release config is always read:
// the queue rules resolve bot branches from it.
func runCheckWorkflows(args []string) error {
	policyPath := policy.DefaultPath
	dir := ""
	printRequired := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--policy":
			val, next, err := requireFlagValue(args, i, "--policy")
			if err != nil {
				return err
			}
			policyPath = val
			i = next
		case "--dir":
			val, next, err := requireFlagValue(args, i, "--dir")
			if err != nil {
				return err
			}
			dir = val
			i = next
		case "--print-required":
			printRequired = true
		default:
			return usageErr("unknown flag %q for check-workflows: expected [--policy P] --dir DIR [--print-required]", args[i])
		}
	}
	if dir == "" {
		return usageErr("usage: release-intent check-workflows [--policy P] --dir DIR [--print-required] (missing required --dir): pass the workflows directory, e.g. .github/workflows")
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return fmt.Errorf("check-workflows: cannot read workflows directory %q: %v. Fix the --dir path or run from the repository root", dir, err)
	}
	queuePolicy, err := policy.LoadQueuePolicy(policyPath)
	if err != nil {
		return err
	}
	cfg, err := loadConfig("check-workflows", DefaultConfigPath)
	if err != nil {
		return err
	}
	diagnostics, required := policy.CheckWorkflows(queuePolicy, cfg, dir)
	if printRequired {
		names := make([]string, 0, len(required))
		for _, r := range required {
			names = append(names, r.Context)
		}
		sort.Strings(names)
		for _, name := range names {
			fmt.Println(name)
		}
		return nil
	}
	if len(diagnostics) > 0 {
		for _, d := range diagnostics {
			where := d.Workflow
			if d.Job != "" {
				where += " job " + d.Job
			}
			fmt.Fprintf(os.Stderr, "release-intent check-workflows: rule %s in %s: %s\n", d.Rule, where, d.Message)
		}
		first := diagnostics[0]
		where := first.Workflow
		if first.Job != "" {
			where += " job " + first.Job
		}
		where += ", checked by release-intent check-workflows against " + policyPath
		return rejectAs("check-workflows", intent.PolicyViolation(string(first.Rule), where, first.Message))
	}
	return emitLines([]string{"ok=true"})
}
