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
// workflow to the shape the tool reads back from a base commit's run.
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
	if _, err := fullSuiteName(doc, rule.FullSuiteJob); err != nil {
		emit(rule.FullSuiteJob, err.Error()+" during merge-queue policy validation")
	}
	needs, _ := needsList(gate)
	if !slices.Contains(needs, rule.FullSuiteJob) {
		emit(req.Job, "the aggregator does not need the full-suite job "+strconv.Quote(rule.FullSuiteJob)+" during merge-queue policy validation. A recorded full verdict must rest on the full suite the aggregator gates; add it to needs")
	}
	callers := 0
	for _, name := range needs {
		job := jobNode(doc, name)
		if !invokesInheritVerdict(job) {
			continue
		}
		callers++
		for _, scope := range []string{"checks", "actions"} {
			if permissionOf(doc, job, scope) != "read" {
				emit(name, "job "+strconv.Quote(name)+" runs "+intent.ProgramName+" "+intent.CommandInheritVerdict.String()+" without permissions "+scope+": read during merge-queue policy validation. The base verdict is read from the check runs and workflow runs; grant "+scope+": read on the job")
			}
		}
	}
	if callers == 0 {
		emit(req.Job, "no job the aggregator needs runs "+intent.ProgramName+" "+intent.CommandInheritVerdict.String()+" during merge-queue policy validation. Run it in a job the aggregator needs so an inherited verdict is proven in the same run")
	}
	steps := stepsOf(gate)
	markers := intent.AllRecordedVerdictSources
	if len(steps) < len(markers) {
		emit(req.Job, "the aggregator has fewer steps than the "+strconv.Itoa(len(markers))+" verdict marker steps during merge-queue policy validation. End it with "+markerList())
		return
	}
	tail := steps[len(steps)-len(markers):]
	for i, source := range markers {
		step := tail[i]
		name := mappingValue(step, "name")
		if name == nil || name.Value != source.MarkerStepName() {
			emit(req.Job, "the aggregator's last steps are not the verdict marker steps in order during merge-queue policy validation. End the job with "+markerList()+", each with a literal name, so a later run can read which source this run's verdict came from")
			return
		}
		if mappingValue(step, "if") == nil {
			emit(req.Job, "marker step "+strconv.Quote(source.MarkerStepName())+" has no if during merge-queue policy validation. Exactly one marker step runs per run; condition each on the source it records")
		}
		if mappingValue(step, "continue-on-error") != nil {
			emit(req.Job, "marker step "+strconv.Quote(source.MarkerStepName())+" carries continue-on-error during merge-queue policy validation. A marker step that may fail and still pass would record a source the run did not prove; remove the key")
		}
	}
	for _, step := range steps[:len(steps)-len(markers)] {
		if name := mappingValue(step, "name"); name != nil && strings.HasPrefix(name.Value, intent.VerdictMarkerPrefix) {
			emit(req.Job, "step "+strconv.Quote(name.Value)+" uses the marker prefix outside the final marker steps during merge-queue policy validation. Rename it; only the marker steps may name a verdict source")
		}
	}
	if mappingValue(gate, "continue-on-error") != nil {
		emit(req.Job, "the aggregator carries continue-on-error during merge-queue policy validation. Its conclusion is the verdict a later run inherits; remove the key")
	}
}

func markerList() string {
	names := make([]string, 0, len(intent.AllRecordedVerdictSources))
	for _, source := range intent.AllRecordedVerdictSources {
		names = append(names, strconv.Quote(source.MarkerStepName()))
	}
	return strings.Join(names, ", ")
}

// invokesInheritVerdict reports whether a step of the job calls the tool's
// inherit-verdict command.
func invokesInheritVerdict(job *yaml.Node) bool {
	for _, step := range stepsOf(job) {
		for _, call := range toolCalls(stepRunText(step)) {
			if len(call) > 1 && call[1].bare == intent.CommandInheritVerdict.String() {
				return true
			}
		}
	}
	return false
}

// permissionOf returns the job's effective access for one token scope: the
// job's permissions when it declares them, otherwise the workflow's.
func permissionOf(doc, job *yaml.Node, scope string) string {
	perms := mappingValue(job, "permissions")
	if perms == nil {
		perms = mappingValue(doc, "permissions")
	}
	if perms == nil {
		return ""
	}
	if perms.Kind == yaml.ScalarNode {
		if perms.Value == "read-all" {
			return "read"
		}
		return ""
	}
	if v := mappingValue(perms, scope); v != nil {
		return strings.TrimSpace(v.Value)
	}
	return ""
}
