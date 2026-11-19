package policy

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
	"gopkg.in/yaml.v3"
)

// workflowsDir is where GitHub reads workflow files, relative to the
// repository root. A run's path field names its file under it.
const workflowsDir = ".github/workflows/"

// WorkflowPath is the repository path of a workflow file the policy names.
func WorkflowPath(workflow string) string { return workflowsDir + workflow }

// inheritAggregator returns the required aggregator entry the rule names.
func inheritAggregator(queue QueueSection, rule *InheritVerdictRule) (RequiredContext, bool) {
	for _, req := range queue.Required {
		if req.Aggregator && req.Context == rule.Context {
			return req, true
		}
	}
	return RequiredContext{}, false
}

// InheritWorkflowPath names the workflow file ResolveInheritRule needs, or
// "" when the policy does not opt into inheritance.
func InheritWorkflowPath(p QueuePolicy) string {
	rule := p.Queue.InheritVerdict
	if rule == nil {
		return ""
	}
	req, ok := inheritAggregator(p.Queue, rule)
	if !ok {
		return ""
	}
	return WorkflowPath(req.Workflow)
}

// ResolveInheritRule builds the tool's inheritance rule from the policy and
// the bytes of the aggregator's workflow: the aggregator context, the
// workflow path, and the check name of the full-suite job, which is the
// job's literal name or, without one, its id.
func ResolveInheritRule(p QueuePolicy, workflow []byte) (intent.InheritRule, error) {
	rule := p.Queue.InheritVerdict
	if rule == nil {
		return intent.InheritRule{}, fmt.Errorf("the queue policy (%s) has no inherit_verdict section, so no base verdict can be inherited. Add inherit_verdict: {context: <aggregator context>, full_suite_job: <job id>} or stop calling %s %s", DefaultPath, intent.ProgramName, intent.CommandInheritVerdict)
	}
	req, ok := inheritAggregator(p.Queue, rule)
	if !ok {
		return intent.InheritRule{}, fmt.Errorf("inherit_verdict names context %q, which is no required aggregator in %s. Name the aggregator context", rule.Context, DefaultPath)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(workflow, &doc); err != nil || len(doc.Content) == 0 {
		return intent.InheritRule{}, fmt.Errorf("cannot parse workflow %s to resolve the full-suite job %q: %v. Fix the workflow syntax", WorkflowPath(req.Workflow), rule.FullSuiteJob, err)
	}
	name, err := fullSuiteName(doc.Content[0], rule.FullSuiteJob)
	if err != nil {
		return intent.InheritRule{}, fmt.Errorf("workflow %s: %v", WorkflowPath(req.Workflow), err)
	}
	return intent.InheritRule{Context: rule.Context, WorkflowPath: WorkflowPath(req.Workflow), FullSuiteName: name}, nil
}

// fullSuiteName returns the check name the full-suite job reports under.
func fullSuiteName(doc *yaml.Node, jobID string) (string, error) {
	job := jobNode(doc, jobID)
	if job == nil {
		return "", fmt.Errorf("jobs.%s, the inherit_verdict full_suite_job, is missing. Name the job that runs the full suite", jobID)
	}
	name := mappingValue(job, "name")
	if name == nil {
		return jobID, nil
	}
	if name.Kind != yaml.ScalarNode || name.Value == "" || strings.Contains(name.Value, "${{") {
		return "", fmt.Errorf("jobs.%s name is not one literal string. The full-suite check name is read back from the jobs API, so it must not be an expression", jobID)
	}
	return name.Value, nil
}

// checkInheritVerdict (inherited-verdicts-are-recorded) holds an opted-in
// workflow to what the tool reads back from a base commit's run, and no
// more: the full-suite job exists and the aggregator needs it, the
// aggregator ends with the versioned marker step set and cannot fail softly,
// and the step that runs inherit-verdict can neither be skipped by its own if
// nor fail softly. The
// full-suite job's check name and the token scopes are checked where they are
// used, at run time, where a fault fails closed.
func (c *queueChecker) checkInheritVerdict() {
	rule := c.policy.Queue.InheritVerdict
	if rule == nil {
		return
	}
	req, ok := inheritAggregator(c.policy.Queue, rule)
	if !ok {
		return
	}
	doc := c.needFile(QueueRuleInheritVerdict, req.Workflow)
	if doc == nil {
		return
	}
	gate := jobNode(doc, req.Job)
	if gate == nil {
		return
	}
	emit := func(job, what string) { c.emit(QueueRuleInheritVerdict, req.Workflow, job, what) }
	needs, _ := needsList(gate)
	if jobNode(doc, rule.FullSuiteJob) == nil {
		emit(rule.FullSuiteJob, "jobs."+rule.FullSuiteJob+", the inherit_verdict full_suite_job, is missing during merge-queue policy validation. Name the job that runs the full suite")
	} else if !slices.Contains(needs, rule.FullSuiteJob) {
		emit(req.Job, "the aggregator does not need the full-suite job "+strconv.Quote(rule.FullSuiteJob)+" during merge-queue policy validation. A recorded full verdict must rest on the full suite the aggregator gates; add it to needs")
	}
	if got, want := finalStepNames(gate, len(intent.VerdictMarkerStepNames())), intent.VerdictMarkerStepNames(); !slices.Equal(got, want) {
		emit(req.Job, fmt.Sprintf("the aggregator's last steps are %q, not the verdict marker steps (set version %d) during merge-queue policy validation. End the job with %s, so a later run can read which source this run's verdict came from", got, intent.VerdictMarkersVersion, markerList()))
	}
	if mappingValue(gate, "continue-on-error") != nil {
		emit(req.Job, "the aggregator carries continue-on-error during merge-queue policy validation. A failed aggregator would then report success, and a later run would inherit it with no way to tell; remove the key")
	}
	callers := 0
	for _, name := range needs {
		for _, step := range stepsOf(jobNode(doc, name)) {
			if !stepInvokesInheritVerdict(step) {
				continue
			}
			callers++
			if mappingValue(step, "if") != nil {
				emit(name, "the step that runs "+intent.ProgramName+" "+intent.CommandInheritVerdict.String()+" in job "+strconv.Quote(name)+" has a step-level if during merge-queue policy validation. A skipped step would leave inherit unset for a release candidate; run it unconditionally and let its script decide")
			}
			if mappingValue(step, "continue-on-error") != nil {
				emit(name, "the step that runs "+intent.ProgramName+" "+intent.CommandInheritVerdict.String()+" in job "+strconv.Quote(name)+" carries continue-on-error during merge-queue policy validation. A failed proof must fail its job; remove the key")
			}
		}
	}
	if callers == 0 {
		emit(req.Job, "no job the aggregator needs runs "+intent.ProgramName+" "+intent.CommandInheritVerdict.String()+" during merge-queue policy validation. Run it in a job the aggregator needs so an inherited verdict is proven in the same run")
	}
}

// finalStepNames returns the names of the job's last n steps; an unnamed
// step reads as "".
func finalStepNames(job *yaml.Node, n int) []string {
	steps := stepsOf(job)
	if len(steps) > n {
		steps = steps[len(steps)-n:]
	}
	names := make([]string, 0, len(steps))
	for _, step := range steps {
		name := ""
		if v := mappingValue(step, "name"); v != nil {
			name = v.Value
		}
		names = append(names, name)
	}
	return names
}

func markerList() string {
	names := intent.VerdictMarkerStepNames()
	for i := range names {
		names[i] = strconv.Quote(names[i])
	}
	return strings.Join(names, ", ")
}

// stepInvokesInheritVerdict reports whether the step calls the tool's
// inherit-verdict command.
func stepInvokesInheritVerdict(step *yaml.Node) bool {
	for _, call := range toolCalls(stepRunText(step)) {
		if len(call) > 1 && call[1].bare == intent.CommandInheritVerdict.String() {
			return true
		}
	}
	return false
}
