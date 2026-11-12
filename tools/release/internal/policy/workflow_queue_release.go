package policy

import (
	"strconv"
	"strings"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
	regexp "github.com/wasilibs/go-re2"
	"gopkg.in/yaml.v3"
)

// This file is the release-actions-gated rule: every step of the release job
// that can write runs only under an `if:` on the one action the planner
// printed in the same run, the tag goes out once on the verdict's tag action,
// the verdict carries every pre-tag step in the canonical form, each pre-tag
// step is a single gated command, later steps carry the status function so a
// failed pre-tag step rebuilds instead of skipping, writes test the push the
// write step printed, nothing that writes uses continue-on-error, and the arm
// and hold steps test the gate. A tool output is not a gate by itself: a
// needs edge is satisfied by any job that succeeds, and an exit 0 in one step
// does not stop the next step.

var (
	// actionGate matches an if that compares one plan step's action output
	// with == against a literal.
	// It captures the step id and the literal, which the caller parses into
	// an intent.Action.
	actionGate = regexp.MustCompile(stepOutputPattern(intent.OutputAction) + `\s*==\s*'([^']+)'`)
	// gateTest matches an if that compares the queue gate output. The
	// alternation is built from the planner's closed gate set so a new gate
	// reaches this check with the set.
	gateTest = regexp.MustCompile(stepOutputPattern(intent.OutputGate) + `\s*==\s*'` + gateAlternatives() + `'`)
	// pushTest matches an if that tests the push the write step printed.
	pushTest = regexp.MustCompile(stepOutputPattern(intent.OutputPush) + `\b`)
	// verdictArg extracts the verdict argument: the canonical ${{ }} form
	// or the bare taggable literal for a repo with no pre-tag steps.
	verdictArg = regexp.MustCompile(regexp.QuoteMeta(intent.FlagVerdict.String()) + `\s+(\$\{\{.*?\}\}|\S+)`)
	// planRunner matches a step that runs the planner.
	planRunner = regexp.MustCompile(toolCall(intent.CommandPlan))
	// rangeReader matches a step that reads a commit range.
	rangeReader = regexp.MustCompile(toolCall(intent.CommandPlan, intent.CommandReleasePR))
	// preTagRunner matches any implemented subcommand of the release tool.
	preTagRunner = regexp.MustCompile(toolCall(intent.AllCommands...))
	// prWrite matches a run line that opens, edits, closes, merges, or
	// releases a PR, or pushes a ref.
	prWrite = regexp.MustCompile(`git\s+push\b|\bgh\s+pr\s+(create|edit|close|merge)\b|\bgh\s+release\s+create\b`)
	// tagPush matches a run line that pushes a tag: a tag refspec, an
	// explicit tag, or a release creation.
	tagPush = regexp.MustCompile(regexp.QuoteMeta(intent.TagRefPrefix) + `|\bgit\s+tag\b|\bgh\s+release\s+create\b`)
	// prOpenEdit matches a run line that opens or edits the release PR.
	prOpenEdit = regexp.MustCompile(`\bgh\s+pr\s+(create|edit)\b`)
)

// gateAlternatives renders the planner's closed gate set as a regex
// alternation, so the arm/hold check knows exactly the gates the tool prints.
func gateAlternatives() string {
	alts := make([]string, 0, len(intent.AllGates))
	for _, g := range intent.AllGates {
		alts = append(alts, regexp.QuoteMeta(g.String()))
	}
	return "(" + strings.Join(alts, "|") + ")"
}

// gatedAction parses the literal an action gate compares against into the
// planner's closed action set. A literal outside the set is no action: ok is
// false and the gate matches no real run.
func gatedAction(literal string) (intent.Action, bool) {
	action, err := intent.ParseAction(literal)
	return action, err == nil
}

// appTokenOutput is the output a GitHub App token step exposes; a step that
// reads it holds the App token.
const appTokenOutput = "outputs.token"

// appTokenAction is the action that mints a GitHub App token.
const appTokenAction = "create-github-app-token"

// statusNotCancelled is the status function that keeps a step running after
// an earlier failure.
const statusNotCancelled = "!cancelled()"

// checkReleaseActions applies release-actions-gated to the release job. A
// policy with no release job (a repo with no release PR) skips the rule.
func (c *queueChecker) checkReleaseActions() {
	rel := c.policy.Queue.ReleaseJob
	if rel == nil {
		return
	}
	doc := c.needFile(QueueRuleReleaseActions, rel.Workflow)
	if doc == nil {
		return
	}
	job := jobNode(doc, rel.Job)
	if job == nil {
		c.emit(QueueRuleReleaseActions, rel.Workflow, rel.Job,
			"jobs."+rel.Job+" is missing during merge-queue policy validation, but the policy names it as the release job. Add the job that carries out the planner's action")
		return
	}
	r := &releaseChecker{c: c, workflow: rel.Workflow, doc: doc, job: job, preTag: rel.PreTagSteps}
	r.collectSteps()
	r.checkVerdictForm()
	r.checkWriteStepsGated()
	r.checkTagPush()
	r.checkPreTagSteps()
	r.checkStatusFunctions()
	r.checkPushOutputs()
	r.checkContinueOnError()
	r.checkArmHold()
}

// releaseChecker holds one release-actions-gated evaluation: the release job's
// steps in order with their ids, and the step indexes each clause flags.
type releaseChecker struct {
	c        *queueChecker
	workflow string
	doc      *yaml.Node
	job      *yaml.Node
	preTag   []string
	steps    []*yaml.Node
	ids      map[string]int
	flagged  map[int]bool
}

// collectSteps indexes the release job's steps in order. A step without an id
// still participates (by position); clauses that need a named step report the
// missing id.
func (r *releaseChecker) collectSteps() {
	r.steps = stepsOf(r.job)
	r.ids = map[string]int{}
	r.flagged = map[int]bool{}
	for i, step := range r.steps {
		if id := stepID(step); id != "" {
			r.ids[id] = i
		}
	}
}

// stepIf returns the step's if text, or "" when it has none.
func stepIfText(step *yaml.Node) string {
	if cond := mappingValue(step, "if"); cond != nil {
		return cond.Value
	}
	return ""
}

// stepRunText returns the step's run text, or "" for a uses-only step.
func stepRunText(step *yaml.Node) string {
	if run := mappingValue(step, "run"); run != nil {
		return run.Value
	}
	return ""
}

// stepUsesText returns the step's uses target, or "" for a run step.
func stepUsesText(step *yaml.Node) string {
	if uses := mappingValue(step, "uses"); uses != nil {
		return uses.Value
	}
	return ""
}

// stepScalars returns every scalar value under the step, for content scans
// (token-output references, dequeue calls, event reads).
func stepScalars(step *yaml.Node) []string {
	var out []string
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		if n.Kind == yaml.ScalarNode {
			out = append(out, n.Value)
			return
		}
		for _, child := range n.Content {
			walk(child)
		}
	}
	walk(step)
	return out
}

// isMintStep reports whether the step mints a GitHub App token.
func isMintStep(step *yaml.Node) bool {
	return strings.Contains(stepUsesText(step), appTokenAction)
}

// isWriteStep reports whether the step can write: it mints the App token, it
// consumes a minted token output, or its run pushes a ref or writes a PR.
func isWriteStep(step *yaml.Node) bool {
	if isMintStep(step) {
		return true
	}
	for _, s := range stepScalars(step) {
		if strings.Contains(s, appTokenOutput) {
			return true
		}
	}
	return prWrite.MatchString(stepRunText(step))
}

// isArmHoldStep reports whether the step is gated on the queue gate output
// rather than on the planner's action; those steps belong to clause (8) and
// are exempt from the action-gate clauses.
func isArmHoldStep(step *yaml.Node) bool {
	return gateTest.MatchString(stepIfText(step))
}

// isTagStep reports whether the step pushes a tag.
func isTagStep(step *yaml.Node) bool {
	return tagPush.MatchString(stepRunText(step))
}

// isBranchPushStep reports whether the step pushes a branch (a push that is
// not a tag push).
func isBranchPushStep(step *yaml.Node) bool {
	run := stepRunText(step)
	return strings.Contains(run, "git push") && !isTagStep(step)
}

// isPROpenEditStep reports whether the step opens or edits the release PR.
func isPROpenEditStep(step *yaml.Node) bool {
	return prOpenEdit.MatchString(stepRunText(step))
}

// isQueueReadStep reports whether the step reads the merge queue or runs the
// queue gate.
func isQueueReadStep(step *yaml.Node) bool {
	run := stepRunText(step)
	return strings.Contains(run, "mergeQueue") || strings.Contains(run, intent.CommandQueueGate.String())
}

// canonicalVerdict builds the exact verdict expression the policy's pre-tag
// steps require: one conjunct per step in order, each reading that step's
// outcome, and nothing else. With no pre-tag steps the verdict is the
// taggable literal. The literals come from the intent package, the same
// objects the planner prints.
func canonicalVerdict(preTag []string) string {
	taggable := intent.VerdictTaggable.String()
	if len(preTag) == 0 {
		return taggable
	}
	parts := make([]string, 0, len(preTag))
	for _, name := range preTag {
		parts = append(parts, "steps."+name+".outcome == 'success'")
	}
	return "${{ (" + strings.Join(parts, " && ") + ") && '" + taggable + "' || '" + intent.VerdictRejected.String() + "' }}"
}

// closedActionList renders the closed action set for messages.
func closedActionList() string {
	vals := make([]string, 0, len(intent.AllActions))
	for _, a := range intent.AllActions {
		vals = append(vals, a.String())
	}
	return strings.Join(vals, ", ")
}

// hasDisjunction reports whether an if spreads its gate across a disjunction:
// ||, always(), or success() alongside the comparison. A gate inside a
// disjunction opens on runs the comparison never approved.
func hasDisjunction(cond string) bool {
	return strings.Contains(cond, "||") || strings.Contains(cond, "always()") || strings.Contains(cond, "success()")
}

// checkVerdictForm (clause 3) requires every verdict argument to be the
// canonical form: the taggable literal with no pre-tag steps, otherwise the
// exact expression built from the policy's pre-tag steps in order.
func (r *releaseChecker) checkVerdictForm() {
	want := canonicalVerdict(r.preTag)
	for i, step := range r.steps {
		if !strings.Contains(stepRunText(step), intent.FlagVerdict.String()) {
			continue
		}
		r.flagged[i] = true
		got := ""
		if m := verdictArg.FindStringSubmatch(stepRunText(step)); m != nil {
			got = strings.TrimSpace(m[1])
		}
		if got != want {
			r.c.emit(QueueRuleReleaseActions, r.workflow, stepID(step),
				"the "+intent.FlagVerdict.String()+" argument is "+strconv.Quote(got)+" during merge-queue policy validation, want the canonical "+strconv.Quote(want)+". A verdict that leaves out a pre-tag step or reads conclusion instead of outcome can tag a commit its checks rejected; pass the exact expression the policy's pre_tag_steps build")
		}
	}
}

// checkWriteStepsGated (clause 1) requires every step that can write to run
// only under an if that compares one step's action output with == against one
// value of the closed action set, as a single conjunction. The arm and hold
// steps test the gate instead and belong to clause (8). The token-minting
// step is flagged for clause (7) but exempt from the action gate: it is gated
// on tag mode by token-mint-ref-check, because no single action covers every
// run that needs a token.
func (r *releaseChecker) checkWriteStepsGated() {
	for i, step := range r.steps {
		if !isWriteStep(step) || isArmHoldStep(step) {
			continue
		}
		r.flagged[i] = true
		if isMintStep(step) {
			continue
		}
		cond := stepIfText(step)
		m := actionGate.FindStringSubmatch(cond)
		if m == nil {
			r.c.emit(QueueRuleReleaseActions, r.workflow, stepID(step),
				"a step that can write has no action-gated if during merge-queue policy validation. A needs edge is satisfied by any job that succeeds and an exit 0 in an earlier step does not stop this one; gate the step on "+equals(stepOutput("<plan>", intent.OutputAction), "<one of "+closedActionList()+">"))
			continue
		}
		if hasDisjunction(cond) {
			r.c.emit(QueueRuleReleaseActions, r.workflow, stepID(step),
				"an action gate sits inside a disjunction during merge-queue policy validation. Parse the if as a conjunction: a gate with ||, always() or success() opens on runs the comparison never approved; gate on the one action alone")
			continue
		}
		if _, known := gatedAction(m[2]); !known {
			r.c.emit(QueueRuleReleaseActions, r.workflow, stepID(step),
				"the action gate compares against "+strconv.Quote(m[2])+" during merge-queue policy validation, which is not one of "+closedActionList()+". Compare against the closed action the planner prints, or the gate never matches a real run")
			continue
		}
		if _, ok := r.ids[m[1]]; !ok {
			r.c.emit(QueueRuleReleaseActions, r.workflow, stepID(step),
				"the action gate reads steps."+m[1]+" during merge-queue policy validation, but no step has that id. Point the gate at the plan step that prints the action in this run")
		}
	}
}

// tagModeGate matches the canonical tag-mode comparison. A != comparison
// never matches, so a gate that mints exactly when tag mode is off is
// rejected.
var tagModeGate = regexp.MustCompile(regexp.QuoteMeta(tagModeComparison))

// hasTagModeGate reports whether an if carries the tag-mode comparison as a
// single comparison (tagModeComparison), with no disjunction.
func hasTagModeGate(cond string) bool {
	return tagModeGate.MatchString(cond) && !strings.Contains(cond, "||")
}

// checkTagPush (clause 2) requires exactly one step to push a tag, gated on
// the tag action of a plan step that passes the verdict flag, with no other
// step or job pushing a tag.
func (r *releaseChecker) checkTagPush() {
	for _, jobName := range jobNames(r.doc) {
		job := jobNode(r.doc, jobName)
		for _, step := range stepsOf(job) {
			if !isTagStep(step) {
				continue
			}
			r.flagged[r.stepIndex(step)] = true
			cond := stepIfText(step)
			m := actionGate.FindStringSubmatch(cond)
			if m == nil || !gatesOn(m[2], intent.ActionTag) || hasDisjunction(cond) {
				r.c.emit(QueueRuleReleaseActions, r.workflow, stepID(step),
					"a step that pushes a tag is not gated on the tag action during merge-queue policy validation. Gate it on "+equals(stepOutput("<plan>", intent.OutputAction), intent.ActionTag.String())+" as a single conjunction, with no ||, always() or success(), so only the verdict's tag action pushes the tag")
				continue
			}
			idx, ok := r.ids[m[1]]
			var planRun string
			if ok {
				planRun = stepRunText(r.steps[idx])
			}
			if !ok || !strings.Contains(planRun, intent.FlagVerdict.String()) {
				r.c.emit(QueueRuleReleaseActions, r.workflow, stepID(step),
					"the tag step is gated on steps."+m[1]+" during merge-queue policy validation, which is not a plan step that passes "+intent.FlagVerdict.String()+". Gate the tag on the verdict's tag action so a stale candidate is never tagged")
			}
		}
	}
	tagCount := 0
	for _, jobName := range jobNames(r.doc) {
		for _, step := range stepsOf(jobNode(r.doc, jobName)) {
			if isTagStep(step) {
				tagCount++
			}
		}
	}
	if tagCount == 0 {
		r.c.emit(QueueRuleReleaseActions, r.workflow, "",
			"no step pushes a tag during merge-queue policy validation. One step must push the tag on the verdict's tag action, or the release job can never publish")
	} else if tagCount > 1 {
		r.c.emit(QueueRuleReleaseActions, r.workflow, "",
			"more than one step pushes a tag during merge-queue policy validation. Exactly one step pushes the tag so a failed run cannot double-publish; move every other tag push out of this workflow")
	}
}

// stepIndex returns the release-job step index of step, or -1 for a step in
// another job (only used for flagging release-job steps).
func (r *releaseChecker) stepIndex(step *yaml.Node) int {
	for i, s := range r.steps {
		if s == step {
			return i
		}
	}
	return -1
}

// checkPreTagSteps (clause 4) requires each policy pre-tag step to exist by
// id, to run only on the verify action, to carry the status function on every
// step after the first, and to run exactly one planner command with no
// chaining, no shell override, and no multi-line run.
func (r *releaseChecker) checkPreTagSteps() {
	for i, name := range r.preTag {
		idx, ok := r.ids[name]
		if !ok {
			r.c.emit(QueueRuleReleaseActions, r.workflow, name,
				"the policy names pre_tag_steps entry "+strconv.Quote(name)+" during merge-queue policy validation, but no release-job step has that id. Add the step so the verdict reads a real outcome")
			continue
		}
		r.flagged[idx] = true
		step := r.steps[idx]
		cond := stepIfText(step)
		m := actionGate.FindStringSubmatch(cond)
		if m == nil || !gatesOn(m[2], intent.ActionVerify) {
			r.c.emit(QueueRuleReleaseActions, r.workflow, name,
				"pre-tag step "+strconv.Quote(name)+" does not run only on the verify action during merge-queue policy validation. Gate it on "+equals(stepOutput("<plan>", intent.OutputAction), intent.ActionVerify.String())+" so a stale candidate never reaches the checks")
		} else if i > 0 && !strings.Contains(cond, statusNotCancelled) {
			r.c.emit(QueueRuleReleaseActions, r.workflow, name,
				"pre-tag step "+strconv.Quote(name)+" runs after a pre-tag step without the status function during merge-queue policy validation. Carry "+statusNotCancelled+" so a failed earlier check still rebuilds the release PR instead of skipping every later step")
		}
		run := stepRunText(step)
		switch {
		case strings.Contains(run, "\n"):
			r.c.emit(QueueRuleReleaseActions, r.workflow, name,
				"pre-tag step "+strconv.Quote(name)+" has a multi-line run during merge-queue policy validation. Run exactly one command so the step's exit status is the check's verdict and nothing else can mask it")
		case mappingValue(step, "shell") != nil:
			r.c.emit(QueueRuleReleaseActions, r.workflow, name,
				"pre-tag step "+strconv.Quote(name)+" overrides the shell during merge-queue policy validation. Drop the shell override so the step runs the one tool command with the default failure semantics")
		case !preTagRunner.MatchString(run):
			r.c.emit(QueueRuleReleaseActions, r.workflow, name,
				"pre-tag step "+strconv.Quote(name)+" runs "+strconv.Quote(firstLine(run))+" during merge-queue policy validation instead of an implemented "+intent.ProgramName+" subcommand. Run the check itself here; a wrapper script that exits zero on a break would tag a rejected commit")
		case strings.Contains(run, "||") || strings.Contains(run, "&&") || strings.Contains(run, ";") || strings.Contains(run, "|") || strings.Contains(run, "set +e"):
			r.c.emit(QueueRuleReleaseActions, r.workflow, name,
				"pre-tag step "+strconv.Quote(name)+" chains commands during merge-queue policy validation. Run exactly one command with no ||, &&, ;, pipe, or set +e so the step's exit status is the check's verdict")
		}
	}
}

// checkStatusFunctions (clause 5) requires every step after the first pre-tag
// step that runs the planner or reads an action to carry !cancelled(). Without
// the status function GitHub adds success() to every later if, so a failed
// pre-tag step would skip the rebuild instead of running it.
func (r *releaseChecker) checkStatusFunctions() {
	first := -1
	for i, step := range r.steps {
		if _, ok := r.ids[stepID(step)]; ok && isPreTag(r.preTag, stepID(step)) && (first == -1 || i < first) {
			first = i
		}
	}
	if first == -1 {
		return
	}
	for i, step := range r.steps {
		if i <= first {
			continue
		}
		if !planRunner.MatchString(stepRunText(step)) && !strings.Contains(stepIfText(step), stepOutputSuffix(intent.OutputAction)) {
			continue
		}
		r.flagged[i] = true
		if !strings.Contains(stepIfText(step), statusNotCancelled) {
			r.c.emit(QueueRuleReleaseActions, r.workflow, stepID(step),
				"a step after the first pre-tag step has no status function during merge-queue policy validation. Carry "+statusNotCancelled+" so a failed check rebuilds the release PR instead of skipping every step after it")
		}
	}
}

// checkPushOutputs (clause 6) requires each step that pushes the release
// branch, opens or edits the release PR, or reads the merge queue to also test
// the push output the write step printed in the same run. A step with
// !cancelled() runs after any failure, so without the push test it would push
// or touch the PR when the write step never ran.
func (r *releaseChecker) checkPushOutputs() {
	write := -1
	for i, step := range r.steps {
		if strings.Contains(stepRunText(step), intent.CommandReleasePR.String()) && strings.Contains(stepRunText(step), intent.FlagWrite.String()) {
			write = i
			break
		}
	}
	for i, step := range r.steps {
		if !isBranchPushStep(step) && !isPROpenEditStep(step) && !isQueueReadStep(step) {
			continue
		}
		r.flagged[i] = true
		if write == -1 {
			r.c.emit(QueueRuleReleaseActions, r.workflow, stepID(step),
				"a push, PR, or queue-read step has no "+intent.CommandReleasePR.String()+" "+intent.FlagWrite.String()+" step in this run during merge-queue policy validation. Write the release content first so these steps can test the push it printed")
			continue
		}
		m := pushTest.FindStringSubmatch(stepIfText(step))
		writeID := stepID(r.steps[write])
		if m == nil || (writeID != "" && m[1] != writeID) {
			r.c.emit(QueueRuleReleaseActions, r.workflow, stepID(step),
				"a push, PR, or queue-read step does not test the write step's push output during merge-queue policy validation. Test "+stepOutput(displayID(writeID), intent.OutputPush)+" in the if so the step runs only when this run wrote the release content")
		}
	}
}

// checkContinueOnError (clause 7) forbids continue-on-error on every step the
// earlier clauses flag: a masked failure would make outcome lie to the
// verdict.
func (r *releaseChecker) checkContinueOnError() {
	for i, step := range r.steps {
		if !r.flagged[i] {
			continue
		}
		if cont := mappingValue(step, "continue-on-error"); cont != nil && cont.Value == "true" {
			r.c.emit(QueueRuleReleaseActions, r.workflow, stepID(step),
				"a gated step uses continue-on-error during merge-queue policy validation. Drop it so a failed write, check, or tag fails the run and its outcome stays honest for the verdict")
		}
	}
}

// checkArmHold (clause 8) requires the auto-merge arm and hold steps to test
// their respective intent.Gate values on the queue gate output of the same
// run. On hold the job only turns auto-merge off; no step may call a dequeue.
func (r *releaseChecker) checkArmHold() {
	seen := map[intent.Gate]bool{}
	for i, step := range r.steps {
		m := gateTest.FindStringSubmatch(stepIfText(step))
		if m == nil {
			continue
		}
		r.flagged[i] = true
		// The pattern's alternation is the closed gate set, so the capture
		// parses; a capture that did not would mark no gate as seen.
		if gate, err := intent.ParseGate(m[2]); err == nil {
			seen[gate] = true
		}
		idx, ok := r.ids[m[1]]
		var gateRun string
		if ok {
			gateRun = stepRunText(r.steps[idx])
		}
		if !ok || !strings.Contains(gateRun, intent.CommandQueueGate.String()) {
			r.c.emit(QueueRuleReleaseActions, r.workflow, stepID(step),
				"the arm/hold step tests steps."+m[1]+" during merge-queue policy validation, which is not the "+intent.CommandQueueGate.String()+" output of this run. Test the gate the "+intent.CommandQueueGate.String()+" command printed so arming follows the hold rule")
		}
	}
	for _, want := range intent.AllGates {
		if !seen[want] {
			r.c.emit(QueueRuleReleaseActions, r.workflow, "",
				"no step tests "+equals(intent.OutputGate.String(), want.String())+" during merge-queue policy validation. Arm auto-merge only when the gate says "+intent.GateArm.String()+", and take the arming back when it says "+intent.GateHold.String())
		}
	}
	for _, scalar := range fileScalars(r.doc) {
		if strings.Contains(scalar, "dequeuePullRequest") {
			r.c.emit(QueueRuleReleaseActions, r.workflow, "",
				"the workflow calls a dequeue during merge-queue policy validation. On hold the job only turns auto-merge off; a queued release PR is never taken out of the queue")
			break
		}
	}
}

func isPreTag(preTag []string, id string) bool {
	for _, name := range preTag {
		if name == id {
			return true
		}
	}
	return false
}

func displayID(id string) string {
	if id == "" {
		return "<the write step>"
	}
	return id
}

// gatesOn reports whether the literal an action gate compares against is
// the wanted member of the closed action set.
func gatesOn(literal string, want intent.Action) bool {
	action, ok := gatedAction(literal)
	return ok && action == want
}

func firstLine(s string) string {
	if i := strings.Index(s, "\n"); i >= 0 {
		return s[:i]
	}
	return s
}

// jobNames returns every job name in the workflow in mapping order.
func jobNames(doc *yaml.Node) []string {
	jobs := mappingValue(doc, "jobs")
	if jobs == nil || jobs.Kind != yaml.MappingNode {
		return nil
	}
	var names []string
	for i := 0; i+1 < len(jobs.Content); i += 2 {
		names = append(names, jobs.Content[i].Value)
	}
	return names
}

// jobNode returns the named job's mapping node, or nil.
func jobNode(doc *yaml.Node, name string) *yaml.Node {
	jobs := mappingValue(doc, "jobs")
	if jobs == nil || jobs.Kind != yaml.MappingNode {
		return nil
	}
	job := mappingValue(jobs, name)
	if job == nil || job.Kind != yaml.MappingNode {
		return nil
	}
	return job
}

// stepsOf returns the job's steps in order, or nil when it has none.
func stepsOf(job *yaml.Node) []*yaml.Node {
	if job == nil {
		return nil
	}
	steps := mappingValue(job, "steps")
	if steps == nil || steps.Kind != yaml.SequenceNode {
		return nil
	}
	var out []*yaml.Node
	for _, step := range steps.Content {
		if step.Kind == yaml.MappingNode {
			out = append(out, step)
		}
	}
	return out
}

// stepID returns the step's id, or "" for an unnamed step.
func stepID(step *yaml.Node) string {
	if id := mappingValue(step, "id"); id != nil {
		return id.Value
	}
	return ""
}

// fileScalars returns every scalar value in the document, for content scans.
func fileScalars(doc *yaml.Node) []string {
	var out []string
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		if n.Kind == yaml.ScalarNode {
			out = append(out, n.Value)
			return
		}
		for _, child := range n.Content {
			walk(child)
		}
	}
	walk(doc)
	return out
}
