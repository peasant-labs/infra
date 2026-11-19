package intent

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/peasant-labs/infra/tools/release/internal/closedset"
)

// This file owns the second half of verdict inheritance: whether the base
// commit carries a merge-queue verdict a release-only candidate may inherit.
// Every accepted base verdict names its source: a full suite run, an
// inherited verdict, or a path filter that found no code change. A chain of
// inherited verdicts therefore always bottoms out at a run where the full
// suite ran.

// VerdictSource is where an accepted merge-queue verdict came from. The
// aggregator job records the first three as marker steps; LegacyFull is the
// bootstrap reading of a run from before the markers existed.
type VerdictSource string

const (
	// VerdictSourceFull is a run whose full-suite job ran and passed.
	VerdictSourceFull VerdictSource = "full"
	// VerdictSourceInherited is a release-only run that inherited its own
	// base's verdict.
	VerdictSourceInherited VerdictSource = "inherited"
	// VerdictSourceUnaffected is a run whose path filter found no code
	// change, so the full suite was skipped as unaffected.
	VerdictSourceUnaffected VerdictSource = "unaffected"
	// VerdictSourceLegacyFull is a run with no marker steps whose full-suite
	// job passed: the bootstrap rule for bases built before markers.
	VerdictSourceLegacyFull VerdictSource = "legacy-full"
)

// AllVerdictSources is the canonical closed set of verdict sources.
var AllVerdictSources = []VerdictSource{
	VerdictSourceFull, VerdictSourceInherited, VerdictSourceUnaffected, VerdictSourceLegacyFull,
}

// AllRecordedVerdictSources is the sources an aggregator job records as
// marker steps, in the order the steps appear.
var AllRecordedVerdictSources = []VerdictSource{
	VerdictSourceFull, VerdictSourceInherited, VerdictSourceUnaffected,
}

func (s VerdictSource) String() string { return string(s) }

// IsValid reports whether the source is one of the known members.
func (s VerdictSource) IsValid() bool { return slices.Contains(AllVerdictSources, s) }

// UnmarshalText parses a source at a decoding boundary.
func (s *VerdictSource) UnmarshalText(text []byte) error {
	member, err := closedset.Parse(AllVerdictSources, string(text), parsing("verdict source", "intent.VerdictSource"))
	if err != nil {
		return err
	}
	*s = member
	return nil
}

// VerdictMarkerPrefix opens the name of every marker step.
const VerdictMarkerPrefix = "verdict-source: "

// VerdictMarkersVersion versions the marker step set: the exact step names
// VerdictMarkerStepNames returns, in that order. A consumer's aggregator job
// ends with these steps, and a consumer that cannot import this package
// mirrors the names together with this version in a fixture. Any change to
// the set (a name, a member, the order) bumps the version, because a base
// run recorded under the old names would otherwise read as a run without
// markers.
const VerdictMarkersVersion = 1

// MarkerStepName is the exact name of the aggregator step that records the
// source. The step name is literal so the jobs API reports it unchanged.
func (s VerdictSource) MarkerStepName() string { return VerdictMarkerPrefix + string(s) }

// VerdictMarkerStepNames is the versioned marker step set, in the order the
// aggregator's final steps carry them. It is derived from
// AllRecordedVerdictSources, the one list of recorded sources.
func VerdictMarkerStepNames() []string {
	names := make([]string, 0, len(AllRecordedVerdictSources))
	for _, source := range AllRecordedVerdictSources {
		names = append(names, source.MarkerStepName())
	}
	return names
}

// InheritRule names what a base verdict must come from: the aggregator check
// context, the repository path of the workflow that reports it, and the check
// name of the full-suite job in that workflow.
type InheritRule struct {
	Context       string `yaml:"context"`
	WorkflowPath  string `yaml:"workflow_path"`
	FullSuiteName string `yaml:"full_suite_name"`
}

// GitHub values the judgment compares against. They are wire values of the
// GitHub API, not choices of this tool.
const (
	ActionsAppSlug    = "github-actions"
	EventMergeGroup   = "merge_group"
	StatusCompleted   = "completed"
	ConclusionSuccess = "success"
	ConclusionSkipped = "skipped"
)

// CheckRunFact is one check run on the base commit.
type CheckRunFact struct {
	ID         int64  `yaml:"id"`
	Name       string `yaml:"name"`
	Status     string `yaml:"status"`
	Conclusion string `yaml:"conclusion"`
	App        string `yaml:"app"`
}

// StepFact is one step of an Actions job.
type StepFact struct {
	Name       string `yaml:"name"`
	Status     string `yaml:"status"`
	Conclusion string `yaml:"conclusion"`
}

// JobFact is one Actions job attempt. Its ID equals its check run's ID.
type JobFact struct {
	ID         int64      `yaml:"id"`
	RunID      int64      `yaml:"run_id"`
	RunAttempt int        `yaml:"run_attempt"`
	Name       string     `yaml:"name"`
	Status     string     `yaml:"status"`
	Conclusion string     `yaml:"conclusion"`
	Steps      []StepFact `yaml:"steps"`
}

// RunFact is one workflow run, as of its latest attempt.
type RunFact struct {
	ID         int64  `yaml:"id"`
	Event      string `yaml:"event"`
	Path       string `yaml:"path"`
	HeadSHA    string `yaml:"head_sha"`
	RunAttempt int    `yaml:"run_attempt"`
	Status     string `yaml:"status"`
	Conclusion string `yaml:"conclusion"`
}

// BaseVerdictReader reads the facts the judgment needs. The production reader
// is the GitHub REST API; a read failure is returned as is and is never a
// verdict.
type BaseVerdictReader interface {
	// CheckRuns lists every check run on the commit, every attempt included.
	CheckRuns(sha string) ([]CheckRunFact, error)
	// Job reads the Actions job whose ID is a check run's ID.
	Job(id int64) (JobFact, error)
	// Run reads one workflow run.
	Run(id int64) (RunFact, error)
	// RunJobs lists every job of every attempt of the run.
	RunJobs(id int64) ([]JobFact, error)
}

// IsPendingVerdict reports whether a rejection may still turn into a verdict
// by waiting: the base's queue run is missing (it may not have been created
// yet) or has not finished.
func IsPendingVerdict(err error) bool {
	if e, ok := err.(*Error); ok {
		return e.Code == ErrBaseVerdictPending || e.Code == ErrBaseVerdictMissing
	}
	return false
}

// JudgeBaseVerdict decides whether the base commit carries a merge-queue
// verdict a release-only candidate may inherit, and returns its source.
//
// The verdict is the aggregator check from the GitHub Actions app, in the
// rule's workflow, on a merge_group run whose head is the base. The run must
// be the only such run, completed with success on its latest attempt, and
// the aggregator's latest attempt must be completed with success. The
// aggregator must have recorded exactly one marker step; a full or legacy
// source also needs the full-suite job's latest attempt to have passed.
// Anything else is a typed rejection; nothing falls back to running tests.
func JudgeBaseVerdict(rule InheritRule, base string, reader BaseVerdictReader) (VerdictSource, error) {
	checkRuns, err := reader.CheckRuns(base)
	if err != nil {
		return "", fmt.Errorf("cannot list the check runs of base %s: %w", base, err)
	}
	var candidates []CheckRunFact
	for _, run := range checkRuns {
		if run.Name != rule.Context {
			continue
		}
		if run.App != ActionsAppSlug {
			return "", verdictError(ErrBaseVerdictProducer, base,
				"check run "+strconv.FormatInt(run.ID, 10)+" named "+strconv.Quote(rule.Context)+" comes from app "+strconv.Quote(run.App)+", not "+ActionsAppSlug,
				"only the queue workflow's own aggregator may vouch for the base; another producer reporting the same context makes the verdict untrustworthy",
				"find out which app reports "+strconv.Quote(rule.Context)+" on the default branch and stop it; then rerun this candidate's checks")
		}
		candidates = append(candidates, run)
	}
	if len(candidates) == 0 {
		return "", verdictError(ErrBaseVerdictMissing, base,
			"no "+strconv.Quote(rule.Context)+" check run exists on the base",
			"the candidate may skip the full suite only by inheriting the verdict the merge queue recorded for its exact base",
			"wait for the base commit's merge-queue run to start, or land the base through the merge queue; then rerun this candidate's checks")
	}
	type found struct {
		run  RunFact
		jobs []JobFact
	}
	byRun := map[int64]*found{}
	var order []int64
	var skipped []string
	for _, candidate := range candidates {
		job, err := reader.Job(candidate.ID)
		if err != nil {
			return "", fmt.Errorf("cannot read job %d behind check run %q on base %s: %w", candidate.ID, rule.Context, base, err)
		}
		f, ok := byRun[job.RunID]
		if !ok {
			run, err := reader.Run(job.RunID)
			if err != nil {
				return "", fmt.Errorf("cannot read workflow run %d on base %s: %w", job.RunID, base, err)
			}
			f = &found{run: run}
			byRun[job.RunID] = f
			order = append(order, job.RunID)
		}
		f.jobs = append(f.jobs, job)
	}
	var matches []*found
	for _, id := range order {
		f := byRun[id]
		if f.run.Event != EventMergeGroup || f.run.Path != rule.WorkflowPath {
			skipped = append(skipped, fmt.Sprintf("run %d (%s, %s)", f.run.ID, f.run.Event, f.run.Path))
			continue
		}
		if f.run.HeadSHA != base {
			return "", verdictError(ErrBaseVerdictAmbiguous, base,
				fmt.Sprintf("merge-group run %d reports head %s, not the base", f.run.ID, f.run.HeadSHA),
				"a check run listed on a commit must belong to a run of that commit",
				"rerun this candidate's checks; if it repeats, report the inconsistent API answer")
		}
		matches = append(matches, f)
	}
	switch len(matches) {
	case 0:
		return "", verdictError(ErrBaseVerdictMissing, base,
			"no "+EventMergeGroup+" run of "+rule.WorkflowPath+" reports "+strconv.Quote(rule.Context)+" on the base; found "+joinOrNothing(skipped),
			"only the base's merge-queue run is the authoritative verdict; a push or pull_request run is not an input to inheritance",
			"wait for or rerun the base commit's merge-queue run, or land the base through the merge queue; then rerun this candidate's checks")
	case 1:
	default:
		var ids []string
		for _, m := range matches {
			ids = append(ids, strconv.FormatInt(m.run.ID, 10))
		}
		return "", verdictError(ErrBaseVerdictAmbiguous, base,
			"more than one "+EventMergeGroup+" run reports "+strconv.Quote(rule.Context)+" on the base: runs "+strings.Join(ids, ", "),
			"inheritance needs exactly one authoritative verdict for the base",
			"let the full suite run for this candidate by landing a non-release change first, or rerun once only one queue run remains")
	}
	match := matches[0]
	latest := match.jobs[0]
	for _, job := range match.jobs[1:] {
		if job.RunAttempt > latest.RunAttempt {
			latest = job
		}
	}
	runRef := fmt.Sprintf("run %d attempt %d", match.run.ID, latest.RunAttempt)
	if match.run.Status != StatusCompleted || latest.Status != StatusCompleted {
		return "", verdictError(ErrBaseVerdictPending, base,
			fmt.Sprintf("the base's merge-queue %s has not finished (run %s, %s %s)", runRef, match.run.Status, rule.Context, latest.Status),
			"an unfinished verdict is not a verdict",
			"wait for the base commit's merge-queue run to finish, then rerun this candidate's checks")
	}
	if latest.RunAttempt != match.run.RunAttempt {
		return "", verdictError(ErrBaseVerdictAmbiguous, base,
			fmt.Sprintf("the latest %s check is from attempt %d, but run %d's latest attempt is %d", rule.Context, latest.RunAttempt, match.run.ID, match.run.RunAttempt),
			"the verdict must come from the run's latest attempt; a newer attempt without its own aggregator result does not show which verdict stands",
			"rerun the whole base commit's merge-queue run so its latest attempt reports "+strconv.Quote(rule.Context)+", then rerun this candidate's checks")
	}
	if match.run.Conclusion != ConclusionSuccess || latest.Conclusion != ConclusionSuccess {
		return "", verdictError(ErrBaseVerdictFailed, base,
			fmt.Sprintf("the base's merge-queue %s concluded %s (run attempt %d concluded %s)", runRef, conclusionOrNone(latest.Conclusion), match.run.RunAttempt, conclusionOrNone(match.run.Conclusion)),
			"only a successful latest attempt is a verdict; an older success is replaced by the newer attempt",
			"fix and rerun the base commit's merge-queue run until it passes, then rerun this candidate's checks")
	}
	source, err := recordedSource(latest, base, runRef)
	if err != nil {
		return "", err
	}
	if source == VerdictSourceFull || source == VerdictSourceLegacyFull {
		jobs, err := reader.RunJobs(match.run.ID)
		if err != nil {
			return "", fmt.Errorf("cannot list the jobs of run %d on base %s: %w", match.run.ID, base, err)
		}
		var suite *JobFact
		for i := range jobs {
			if jobs[i].Name == rule.FullSuiteName && (suite == nil || jobs[i].RunAttempt > suite.RunAttempt) {
				suite = &jobs[i]
			}
		}
		if suite == nil || suite.Status != StatusCompleted || suite.Conclusion != ConclusionSuccess {
			what := "the full-suite job " + strconv.Quote(rule.FullSuiteName) + " is absent from " + runRef
			if suite != nil {
				what = fmt.Sprintf("the full-suite job %q of %s is %s/%s", rule.FullSuiteName, runRef, suite.Status, conclusionOrNone(suite.Conclusion))
			}
			return "", verdictError(ErrBaseVerdictFailed, base, what+", but the verdict source is "+source.String(),
				"a full verdict, and the bootstrap reading of a run without markers, must rest on a passing full suite",
				"rerun the base commit's merge-queue run so the full suite passes, then rerun this candidate's checks")
		}
	}
	return source, nil
}

// recordedSource reads the marker steps of the aggregator's latest attempt.
// No marker step at all is a run from before markers: the bootstrap reading.
func recordedSource(job JobFact, base, runRef string) (VerdictSource, error) {
	var markers, succeeded []string
	for _, step := range job.Steps {
		if !strings.HasPrefix(step.Name, VerdictMarkerPrefix) {
			continue
		}
		markers = append(markers, step.Name)
		if step.Conclusion == ConclusionSuccess {
			succeeded = append(succeeded, step.Name)
		}
	}
	if len(markers) == 0 {
		return VerdictSourceLegacyFull, nil
	}
	if len(succeeded) == 1 {
		for _, source := range AllRecordedVerdictSources {
			if succeeded[0] == source.MarkerStepName() {
				return source, nil
			}
		}
	}
	return "", verdictError(ErrBaseVerdictAmbiguous, base,
		fmt.Sprintf("the aggregator of %s recorded %d successful marker steps (%s) among %s", runRef, len(succeeded), joinOrNothing(succeeded), strings.Join(markers, ", ")),
		"exactly one marker step names the source of a verdict, so a chain of inherited verdicts bottoms out at a full suite",
		"restore exactly one successful "+strconv.Quote(VerdictMarkerPrefix+"<source>")+" step per run in the aggregator job, then rerun the base commit's merge-queue run")
}

func verdictError(code ErrorCode, base, what, why, fix string) *Error {
	return &Error{
		Code:  code,
		What:  what + " (base " + base + ")",
		Why:   why,
		Where: "the base commit's merge-queue checks, judged by intent.JudgeBaseVerdict",
		When:  "deciding whether a release-only candidate inherits its base's verdict",
		Means: "the release gate fails closed: it neither inherits a verdict nor falls back to running the suite",
		Fix:   fix,
	}
}

func conclusionOrNone(conclusion string) string {
	if conclusion == "" {
		return "no conclusion"
	}
	return conclusion
}

func joinOrNothing(items []string) string {
	if len(items) == 0 {
		return "nothing"
	}
	return strings.Join(items, ", ")
}
