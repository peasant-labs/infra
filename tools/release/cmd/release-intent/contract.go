package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
	"gopkg.in/yaml.v3"
)

// rangeFacts collects the same first-parent, tag, epoch and floor facts used
// by the planner, without invoking its candidate freshness machinery.
func rangeFacts(ctx context.Context, r GitRunner, rev, floor string) (intent.Config, intent.Facts, error) {
	data, err := r.ReadFile(ctx, rev, intent.DefaultConfigPath)
	if err != nil {
		return intent.Config{}, intent.Facts{}, fmt.Errorf("cannot read release config at %q: %w", rev, err)
	}
	cfg, err := intent.ParseConfig(data)
	if err != nil {
		return intent.Config{}, intent.Facts{}, err
	}
	commits, err := r.FirstParent(ctx, rev, cfg.Changelog)
	if err != nil {
		return intent.Config{}, intent.Facts{}, err
	}
	tags, err := r.Tags(ctx, cfg.TagPrefix)
	if err != nil {
		return intent.Config{}, intent.Facts{}, err
	}
	epoch, err := r.FirstCommitAdding(ctx, rev, intent.DefaultConfigPath)
	if err != nil {
		return intent.Config{}, intent.Facts{}, err
	}
	return cfg, intent.Facts{Branch: rev, Commits: commits, Tags: tags, Epoch: epoch, Floor: floor}, nil
}

func optionalStdin() (string, error) {
	info, err := os.Stdin.Stat()
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeCharDevice != 0 {
		return "", nil
	}
	data, err := io.ReadAll(io.LimitReader(os.Stdin, 65537))
	if err != nil {
		return "", err
	}
	if len(data) > 65536 {
		return "", usageErr("stdin exceeds the title size limit")
	}
	text := strings.TrimSpace(string(data))
	if strings.ContainsAny(text, "\r\n") {
		return "", usageErr("stdin must contain one title, not multiple lines")
	}
	return text, nil
}

func runCheckContract(flags flagValues) error {
	until := flags.Value(intent.FlagUntil)
	printBase := flags.Has(intent.FlagPrintRangeBase)
	if printBase && (until == "" || flags.Has(intent.FlagReport)) {
		return usageErr("--print-range-base requires --until and takes no --report")
	}
	if !printBase && (!flags.Has(intent.FlagReport) || flags.Value(intent.FlagReport) == "") {
		return usageErr("--report FILE is required unless --print-range-base is used")
	}
	if until == "" && flags.Has(intent.FlagFloor) {
		return usageErr("--floor applies only with --until")
	}
	title, err := optionalStdin()
	if err != nil {
		return err
	}
	if until != "" && title != "" {
		return usageErr("--until cannot be combined with a title on stdin")
	}
	if until == "" && title == "" {
		return usageErr("check-contract without --until requires one PR title on stdin")
	}
	ctx := context.Background()
	r, err := newExecGit()
	if err != nil {
		return err
	}
	if until != "" {
		cfg, facts, err := rangeFacts(ctx, r, until, flags.Value(intent.FlagFloor))
		if err != nil {
			return err
		}
		if printBase {
			base, err := intent.RangeBase(cfg, facts)
			if err != nil {
				return err
			}
			return emit(out(intent.OutputRangeBase, base))
		}
		base, subjects, err := intent.RangeTitles(cfg, facts)
		if err != nil {
			return err
		}
		rep, err := readContractReport(flags.Value(intent.FlagReport), cfg)
		if err != nil {
			return err
		}
		if rep.Head != facts.Commits[0].SHA || rep.Base != facts.Tags[base] {
			return handoffError("check-contract", fmt.Sprintf("report base/head %q/%q do not match range base/head %q/%q", rep.Base, rep.Head, facts.Tags[base], facts.Commits[0].SHA), "the report must describe exactly the range being judged", "regenerate the report for this range")
		}
		if err := intent.CheckContractRange(cfg, rep, subjects); err != nil {
			return err
		}
		return emit(out(intent.OutputSurfaces, reportSurfaces(rep)), out(intent.OutputError, ""))
	}
	// Read only the base hint first; the full strict decoder validates it
	// after the base revision's config has been fetched.
	data, err := os.ReadFile(flags.Value(intent.FlagReport))
	if err != nil {
		return handoffError("check-contract", fmt.Sprintf("cannot read report: %v", err), "the report is the evidence being judged", "pass a readable report file")
	}
	var hint struct {
		Base string `yaml:"base"`
	}
	if err := yaml.Unmarshal(data, &hint); err != nil || hint.Base == "" {
		return handoffError("check-contract", fmt.Sprintf("report has no readable base: %v", err), "the base selects the trusted release configuration", "regenerate the report")
	}
	cfgBytes, err := r.ReadFile(ctx, hint.Base, intent.DefaultConfigPath)
	if err != nil {
		var absent *NotFoundError
		if !errors.As(err, &absent) {
			return fmt.Errorf("check-contract: cannot read the base release config at %q: %w", hint.Base, err)
		}
		// The adopting PR has no base config. Like check-title, use HEAD only
		// for that absent-file case; a failed fetch never triggers fallback.
		cfgBytes, err = r.ReadFile(ctx, "HEAD", intent.DefaultConfigPath)
		if err != nil {
			return fmt.Errorf("check-contract: the base has no release config and HEAD cannot supply an adopting config: %w", err)
		}
	}
	cfg, err := intent.ParseConfig(cfgBytes)
	if err != nil {
		return err
	}
	rep, err := intent.ParseContractReport(data, cfg)
	if err != nil {
		return err
	}
	head, err := r.FirstParent(ctx, "HEAD", cfg.Changelog)
	if err != nil || len(head) == 0 {
		return fmt.Errorf("check-contract: cannot resolve checkout HEAD: %v", err)
	}
	if rep.Head != head[0].SHA {
		return handoffError("check-contract", fmt.Sprintf("report head %q does not match checkout HEAD %q", rep.Head, head[0].SHA), "the report must describe the checked-out tree", "regenerate the report for HEAD")
	}
	tags, err := r.Tags(ctx, cfg.TagPrefix)
	if err != nil {
		return err
	}
	_, version, _ := intent.HighestFinalTag(tags, cfg.TagPrefix)
	if _, err := intent.CheckContract(cfg, version, rep, title); err != nil {
		return err
	}
	return emit(out(intent.OutputSurfaces, reportSurfaces(rep)), out(intent.OutputError, ""))
}

func readContractReport(path string, cfg intent.Config) (intent.ContractReport, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return intent.ContractReport{}, handoffError("check-contract", fmt.Sprintf("cannot read report %q: %v", path, err), "the report is the evidence being judged", "pass a readable report file")
	}
	return intent.ParseContractReport(data, cfg)
}

func reportSurfaces(rep intent.ContractReport) string {
	var names []string
	for _, s := range rep.Surfaces {
		if s.Change != intent.SurfaceChangeNone {
			names = append(names, s.Name)
		}
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

func handoffError(command, what, why, fix string) error {
	return fmt.Errorf("%s: %s [why: %s; where: %s; when: reading release evidence; means: no decision can be made; fix: %s]", command, what, why, command, fix)
}

func runFreezePlan(flags flagValues) error {
	ctx := context.Background()
	r, err := newExecGit()
	if err != nil {
		return err
	}
	branch := flags.Value(intent.FlagBranch)
	rev := flags.ValueOr(intent.FlagUntil, branch)
	cfg, facts, err := rangeFacts(ctx, r, rev, flags.Value(intent.FlagFloor))
	if err != nil {
		return err
	}
	facts.FreezeFloor = flags.Value(intent.FlagFreezeFloor)
	if rev != branch {
		chain, err := r.FirstParent(ctx, branch, cfg.Changelog)
		if err != nil || len(facts.Commits) == 0 {
			return fmt.Errorf("freeze-plan: cannot read branch %q or revision %q: %v", branch, rev, err)
		}
		found := false
		for _, c := range chain {
			found = found || c.SHA == facts.Commits[0].SHA
		}
		if !found {
			return fmt.Errorf("freeze-plan: revision %q is not on the first-parent chain of %q; select a candidate on the branch", rev, branch)
		}
	}
	_, subjects, err := intent.RangeTitles(cfg, facts)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(flags.Value(intent.FlagState))
	if err != nil {
		return handoffError("freeze-plan", fmt.Sprintf("cannot read surface state: %v", err), "the surface state is required for every freeze decision", "pass a readable state file generated from the source revision")
	}
	states, err := intent.ParseSurfaceStates(data, cfg)
	if err != nil {
		return err
	}
	plan, err := intent.PlanFreeze(cfg, states, subjects)
	if err != nil {
		return err
	}
	plan.FreezeFloor = flags.Value(intent.FlagFreezeFloor)
	doc, err := yaml.Marshal(plan)
	if err != nil {
		return fmt.Errorf("freeze-plan: cannot encode plan: %w", err)
	}
	if err := os.WriteFile(flags.Value(intent.FlagOut), doc, 0o600); err != nil {
		return fmt.Errorf("freeze-plan: cannot write plan: %w", err)
	}
	var names []string
	for _, s := range plan.Surfaces {
		if s.Freeze {
			names = append(names, s.Name)
		}
	}
	return emit(out(intent.OutputFrozen, strings.Join(names, ",")), out(intent.OutputError, ""))
}
