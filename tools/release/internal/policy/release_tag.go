package policy

import (
	"strconv"
	"strings"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
	regexp "github.com/wasilibs/go-re2"
	"gopkg.in/yaml.v3"
)

// This file is the release-tag-checked-before-push rule. The release job is
// the one job that can push a tag, and a tag-triggered publish workflow runs
// the workflow files of the tagged commit. So a tag on an unreviewed commit
// makes the publish-side verify job run a copy of itself that may not verify
// anything. The check has to happen where the tag is written, before it
// leaves the runner, and the commit it is compared against has to be one the
// run's own condition proves is on the default branch.
//
// Three clauses establish that:
//
//  1. the workflow triggers only on push, schedule and workflow_dispatch. A
//     schedule run is on the default branch by definition; for a push or a
//     dispatch, the release job's own condition has to compare github.ref
//     with the repository's default branch, read from the event payload.
//  2. the release job's if proves the run's ref is the default branch: the
//     comparison is one conjunct of a conjunction, never behind a
//     disjunction, a negation or another comparison.
//  3. a plumbing step runs git merge-base --is-ancestor on the fetched tag
//     and ${GITHUB_SHA}, after the tag fetch and before the tag push, under
//     the tag push's own if. The tag push's if carries no status function, so
//     a failed check skips the push through GitHub's implicit success() check
//     instead of being overridden by always(), failure(), cancelled() or
//     success(). Neither the check step nor the tag push sets the
//     continue-on-error key: GitHub evaluates that key as a value, so an
//     expression such as ${{ fromJSON('true') }} would make a failed check
//     report success. The checker refuses the key, not a spelling of its
//     value; release-actions-gated refuses it on the release job itself.
//
// For a push, github.sha is the pushed branch's tip; for a schedule, the
// latest commit on the default branch; for a workflow_dispatch, the commit of
// the selected ref. Once clause 2 holds for the file that runs, each is a
// default-branch commit. The rule cannot see a run that executes another
// branch's copy of this workflow: that copy is outside the checked tree, and
// the repository ruleset that lets only the releaser App create release tags
// is the operator step that limits who can write a tag at all.

// defaultBranchComparison matches one whole conjunct: the run's ref compared
// with the fully qualified ref built from the repository's default branch.
// The literal form cannot be used here: the checker cannot know that a
// literal names the default branch, and a run on another branch executes that
// branch's copy of the workflow.
var defaultBranchComparison = regexp.MustCompile(`(?i)^` + regexp.QuoteMeta(contextRef) + `\s*==\s*format\s*\(\s*'` + regexp.QuoteMeta(intent.BranchRefPrefix) + `\{0\}'\s*,\s*` + regexp.QuoteMeta(defaultBranchExpression) + `\s*\)$`)

// releaseTagTriggers is the closed set of trigger events whose github.sha the
// release job's own condition can pin to the default branch.
var releaseTagTriggers = []GitHubEvent{GitHubEventPush, GitHubEventSchedule, GitHubEventWorkflowDispatch}

// checkReleaseTagBoundary applies the rule to the policy's release job. A
// repo with no release job pushes no tag and skips it.
func (c *queueChecker) checkReleaseTagBoundary() {
	rel := c.policy.Queue.ReleaseJob
	if rel == nil {
		return
	}
	doc := c.needFile(QueueRuleReleaseTagBoundary, rel.Workflow)
	if doc == nil {
		return
	}
	job := jobNode(doc, rel.Job)
	if job == nil {
		return
	}
	cj := c.creds[rel.Workflow][rel.Job]
	steps := stepsOf(job)
	var tagSteps, fetchSteps, guardSteps []int
	for i, step := range steps {
		switch c.plumbingTemplateOf(doc, job, step, cj) {
		case PlumbingFetchBundleTag:
			fetchSteps = append(fetchSteps, i)
		case PlumbingCheckTagAncestor:
			guardSteps = append(guardSteps, i)
		}
		if c.isTemplateStep(rel.Workflow, rel.Job, step, TemplatePushReleaseTag) {
			tagSteps = append(tagSteps, i)
		}
	}
	if len(tagSteps) == 0 {
		return
	}
	c.checkReleaseTagTriggers(rel.Workflow, rel.Job, doc)
	c.checkReleaseTagRunRef(rel.Workflow, rel.Job, job)
	if len(tagSteps) != 1 {
		// release-actions-gated owns the "more than one step pushes a tag"
		// fault, and no single tag path is left to hold to this rule.
		return
	}
	tagIdx := tagSteps[0]
	tagIf := strings.TrimSpace(stepIfText(steps[tagIdx]))
	for _, fn := range statusFunctionsIn(tagIf) {
		c.emit(QueueRuleReleaseTagBoundary, rel.Workflow, rel.Job, violation(
			"the tag push step "+stepRef(steps[tagIdx])+" has "+fn.Call()+" in its if",
			"a status function turns off GitHub's implicit success() check, so the push runs even after the step that proves the tag is on the default branch failed",
			rejected,
			"drop every status function from the tag push's if and from the check step's if, so a failed check skips the push"))
	}
	if hasContinueOnError(steps[tagIdx]) {
		c.emit(QueueRuleReleaseTagBoundary, rel.Workflow, rel.Job, violation(
			"the tag push step "+stepRef(steps[tagIdx])+" sets continue-on-error",
			"GitHub evaluates that key as a value, and an expression such as ${{ fromJSON('true') }} makes the key true; a failed push then reports success, and the key's value is not something the checker can evaluate",
			rejected,
			"drop the continue-on-error key from the tag push, whatever its value"))
	}
	fetchIdx := -1
	for _, i := range fetchSteps {
		if i < tagIdx {
			fetchIdx = i
		}
	}
	beforePush := 0
	for _, i := range guardSteps {
		step := steps[i]
		label := "the check step " + stepRef(step)
		if i >= tagIdx {
			c.emit(QueueRuleReleaseTagBoundary, rel.Workflow, rel.Job, violation(
				label+" runs after the tag push",
				"the tag has already left the runner by then, so the check cannot stop it",
				rejected,
				"run the check after the tag fetch and before the tag push"))
		} else {
			beforePush++
		}
		if fetchIdx == -1 || i < fetchIdx {
			c.emit(QueueRuleReleaseTagBoundary, rel.Workflow, rel.Job, violation(
				label+" runs before the tag fetch",
				"git merge-base reads the local tag ref, which the fetch creates",
				rejected,
				"fetch the tag from the planner's bundle first, then run the check"))
		}
		if got := strings.TrimSpace(stepIfText(step)); got != tagIf {
			c.emit(QueueRuleReleaseTagBoundary, rel.Workflow, rel.Job, violation(
				label+" runs under a different if than the tag push ("+quote(got)+" instead of "+quote(tagIf)+")",
				"the check and the push must run on exactly the same runs, or a run can push a tag the check did not gate",
				rejected,
				"give the check step the tag push step's own if, written the same way"))
		}
		if hasContinueOnError(step) {
			c.emit(QueueRuleReleaseTagBoundary, rel.Workflow, rel.Job, violation(
				label+" sets continue-on-error",
				"GitHub evaluates that key as a value, and an expression such as ${{ fromJSON('true') }} makes the key true; a failed check then reports success and the implicit success() check no longer skips the tag push",
				rejected,
				"drop the continue-on-error key from the check step, whatever its value"))
		}
	}
	if beforePush == 0 {
		c.emit(QueueRuleReleaseTagBoundary, rel.Workflow, rel.Job, violation(
			"no step proves the tag's commit is the run's commit or an ancestor of it before the tag push",
			"the tag-triggered publish workflow executes the tagged commit's own workflow files, so a tag on an unreviewed commit can publish through a copy that does not verify the tag",
			rejected,
			"add `git merge-base --is-ancestor \"refs/tags/${"+TemplateEnvTag.String()+"}\" \"${"+runnerEnvSHA+"}\"` between the tag fetch and the tag push, under the tag push's own if"))
	}
}

// checkReleaseTagTriggers holds the release job's workflow to the events
// whose github.sha the job's own condition can pin to the default branch.
func (c *queueChecker) checkReleaseTagTriggers(workflow, job string, doc *yaml.Node) {
	for _, t := range workflowTriggers(doc) {
		if t.Event == GitHubEventPush && (mappingValue(t.Config, triggerKeyTags) != nil || mappingValue(t.Config, triggerKeyTagsIgnore) != nil) {
			c.emit(QueueRuleReleaseTagBoundary, workflow, job, violation(
				"the workflow triggers on pushed tags",
				"a tag push sets github.sha to the tagged commit, which is not the default-branch commit the release job's condition proves",
				rejected,
				"trigger the release job on branches, schedule and workflow_dispatch only; publish from a separate tag-triggered workflow"))
			continue
		}
		if containsEvent(releaseTagTriggers, t.Event) {
			continue
		}
		c.emit(QueueRuleReleaseTagBoundary, workflow, job, violation(
			"the workflow triggers on "+strconv.Quote(t.Name),
			"the release job is supported on "+eventNames(releaseTagTriggers)+" only; a fourth event needs its own proof before it can hold the App credential",
			rejected,
			"drop the trigger, or move the work that does not push a tag into its own workflow"))
	}
}

// checkReleaseTagRunRef requires the release job's if to prove the run is on
// the default branch. The comparison has to be one conjunct of a conjunction:
// a disjunction, a negation or a second comparison around it opens the job on
// another ref while the text still contains the comparison.
func (c *queueChecker) checkReleaseTagRunRef(workflow, jobName string, job *yaml.Node) {
	cond := mappingValue(job, "if")
	if cond == nil {
		c.emit(QueueRuleReleaseTagBoundary, workflow, jobName, violation(
			"jobs."+jobName+" has no job-level if",
			"nothing then restricts the job to the default branch, so a dispatch on another branch runs that branch's workflow file with the credential",
			rejected,
			"add `if: github.ref == "+defaultBranchTest()+"` (with any other conjuncts) to the job"))
		return
	}
	expr := stripIfExpression(cond.Value)
	if strings.Contains(expr, "||") {
		c.emit(QueueRuleReleaseTagBoundary, workflow, jobName, violation(
			"jobs."+jobName+" if holds a disjunction",
			"a disjunction can be true on another ref while the default-branch comparison is false",
			rejected,
			"write the job if as a conjunction and keep github.ref == "+defaultBranchTest()+" as one conjunct"))
		return
	}
	if !hasDefaultBranchConjunct(expr) {
		c.emit(QueueRuleReleaseTagBoundary, workflow, jobName, violation(
			"jobs."+jobName+" if does not prove the run is on the default branch (compare github.ref with "+defaultBranchTest()+")",
			"a push to another branch or a dispatch on another branch would otherwise run this file with the credential, and the tag check would compare against an untrusted commit",
			rejected,
			"keep github.ref == "+defaultBranchTest()+" as one conjunct of the job if; a literal branch name cannot be checked against the repository's actual default branch"))
	}
}

// defaultBranchTest renders the format() side of the one comparison the rule
// accepts.
func defaultBranchTest() string {
	return "format('" + intent.BranchRefPrefix + "{0}', " + defaultBranchExpression + ")"
}

// stripIfExpression removes one optional ${{ }} wrapper from an if value.
func stripIfExpression(cond string) string {
	s := strings.TrimSpace(cond)
	if strings.HasPrefix(s, "${{") && strings.HasSuffix(s, "}}") {
		s = strings.TrimSpace(s[3 : len(s)-2])
	}
	return s
}

// hasDefaultBranchConjunct reports whether the expression has the comparison
// as one top-level conjunct. Splitting on top-level && and matching one
// conjunct exactly is a semantic reading, not a substring test: a comparison
// under !, inside a larger comparison, or inside a nested group is not one.
func hasDefaultBranchConjunct(expr string) bool {
	for _, conjunct := range splitConjuncts(expr) {
		if defaultBranchComparison.MatchString(trimOuterParens(conjunct)) {
			return true
		}
	}
	return false
}

// splitConjuncts splits an expression on its top-level && operators, keeping
// quoted text and parenthesized groups whole.
func splitConjuncts(expr string) []string {
	var out []string
	depth := 0
	inQuote := false
	start := 0
	for i := 0; i < len(expr); i++ {
		c := expr[i]
		switch {
		case inQuote:
			if c == '\'' {
				inQuote = false
			}
		case c == '\'':
			inQuote = true
		case c == '(':
			depth++
		case c == ')':
			if depth > 0 {
				depth--
			}
		case c == '&' && depth == 0 && i+1 < len(expr) && expr[i+1] == '&':
			out = append(out, strings.TrimSpace(expr[start:i]))
			i++
			start = i + 1
		}
	}
	return append(out, strings.TrimSpace(expr[start:]))
}

// trimOuterParens removes balanced parentheses that wrap the whole conjunct.
func trimOuterParens(conjunct string) string {
	s := strings.TrimSpace(conjunct)
	for strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") && closesAtEnd(s) {
		s = strings.TrimSpace(s[1 : len(s)-1])
	}
	return s
}

// closesAtEnd reports whether the first '(' closes at the last character.
func closesAtEnd(s string) bool {
	depth := 0
	inQuote := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inQuote:
			if c == '\'' {
				inQuote = false
			}
		case c == '\'':
			inQuote = true
		case c == '(':
			depth++
		case c == ')':
			depth--
			if depth == 0 {
				return i == len(s)-1
			}
		}
	}
	return false
}

// stepRef names a step by its id, or its position in the job.
func stepRef(step *yaml.Node) string {
	if id := stepID(step); id != "" {
		return strconv.Quote(id)
	}
	return "without an id"
}

// plumbingTemplateOf names the plumbing template a step's run block is, if
// it is exactly one.
func (c *queueChecker) plumbingTemplateOf(doc, job, step *yaml.Node, cj credentialJob) PlumbingTemplate {
	if stepRunText(step) == "" {
		return ""
	}
	words, why := singleCommand(doc, job, step)
	if why != "" {
		return ""
	}
	tmpl, why := matchPlumbing(words, templateStep{class: cj.class, branch: cj.branch, env: effectiveEnv(doc, job, step), doc: doc, job: job})
	if why != "" {
		return ""
	}
	return tmpl
}

func containsEvent(events []GitHubEvent, event GitHubEvent) bool {
	for _, member := range events {
		if member == event {
			return true
		}
	}
	return false
}

func eventNames(events []GitHubEvent) string {
	names := make([]string, 0, len(events))
	for _, e := range events {
		names = append(names, e.String())
	}
	return strings.Join(names, ", ")
}
