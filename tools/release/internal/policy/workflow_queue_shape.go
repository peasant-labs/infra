package policy

import (
	"slices"
	"strconv"
	"strings"

	"github.com/peasant-labs/infra/tools/release/internal/closedset"
	"github.com/peasant-labs/infra/tools/release/internal/intent"
	regexp "github.com/wasilibs/go-re2"
	"gopkg.in/yaml.v3"
)

// This file holds the workflow-shape rules every repo shares: required
// workflows run unfiltered, the aggregator always reports, titles are never
// interpolated, the cut-release job is dispatch-only with its tag-mode writes,
// the tool checkout is a pinned SHA, and workflows call classify instead of
// comparing a bot branch literal.

// checkUnfiltered (required-workflows-unfiltered) requires every workflow that
// owns a required context to run on every pull_request and merge_group event:
// no paths, paths-ignore, or branches-ignore filter that could skip the check
// the queue waits on and stick the queue.
func (c *queueChecker) checkUnfiltered() {
	for _, req := range c.policy.Queue.Required {
		doc := c.needFile(QueueRuleUnfiltered, req.Workflow)
		if doc == nil {
			continue
		}
		for _, event := range []GitHubEvent{GitHubEventPullRequest, GitHubEventMergeGroup} {
			trigger, ok := findTrigger(doc, event)
			if !ok {
				continue
			}
			for _, key := range triggerFilterKeys {
				if mappingValue(trigger.Config, key) != nil {
					c.emit(QueueRuleUnfiltered, req.Workflow, req.Job,
						"the "+event.String()+" trigger filters with "+key+" during merge-queue policy validation, but the workflow owns required context "+req.Context+". Run on every event with no path filter, or a change that skips the check leaves the queue waiting forever")
				}
			}
			if event != GitHubEventPullRequest {
				continue
			}
			actions, listed := activityTypes(trigger)
			if !listed {
				continue
			}
			for _, want := range DefaultPullRequestActions {
				if !slices.Contains(actions, want) {
					c.emit(QueueRuleUnfiltered, req.Workflow, req.Job,
						"the "+event.String()+" trigger names "+triggerKeyTypes+" without "+want.String()+" during merge-queue policy validation, but the workflow owns required context "+req.Context+". Keep every default activity type ("+closedset.Join(DefaultPullRequestActions, ", ")+") in the list, or drop the list: a head change that skips the check leaves the queue waiting forever")
				}
			}
		}
	}
}

// checkMergeGate (merge-gate-always-reports) requires the aggregator job that
// always reports so a change touching no heavy path is never stuck in the
// queue: the required entry marked aggregator has if: always() and a needs
// list.
func (c *queueChecker) checkMergeGate() {
	for _, req := range c.policy.Queue.Required {
		if !req.Aggregator {
			continue
		}
		doc := c.needFile(QueueRuleMergeGate, req.Workflow)
		if doc == nil {
			continue
		}
		job := jobNode(doc, req.Job)
		if job == nil {
			continue
		}
		if cond := mappingValue(job, "if"); cond == nil || !isAlways(cond.Value) {
			c.emit(QueueRuleMergeGate, req.Workflow, req.Job,
				"the aggregator job has no if: always() during merge-queue policy validation. Report always so a change that skips every needed job still satisfies the queue")
		}
		needs, err := needsList(job)
		if err != nil || len(needs) == 0 {
			c.emit(QueueRuleMergeGate, req.Workflow, req.Job,
				"the aggregator job names no needs during merge-queue policy validation. List the jobs it gates so a failure upstream blocks the queue instead of passing silently")
		}
	}
}

// isAlways reports whether an if is exactly the always() status function.
func isAlways(cond string) bool {
	cond = strings.TrimSpace(cond)
	return cond == "always()" || cond == "${{ always() }}"
}

var titleInterpolation = []string{
	"pull_request.title",
	"head_commit",
	"event.commits",
}

// checkTitles (titles-never-interpolated) requires that no run or env block
// interpolates a PR title or a commit message expression. Titles are
// untrusted input: interpolating one into a shell command runs attacker
// text, and interpolating one into env smuggles it into every later run.
func (c *queueChecker) checkTitles() {
	c.eachDoc(QueueRuleTitles, func(name string, doc *yaml.Node) {
		for _, jobName := range jobNames(doc) {
			for _, step := range stepsOf(jobNode(doc, jobName)) {
				for _, field := range []struct {
					key   string
					value string
				}{
					{"run", stepRunText(step)},
					{"env", envText(step)},
				} {
					for _, pattern := range titleInterpolation {
						if strings.Contains(field.value, pattern) {
							c.emit(QueueRuleTitles, name, stepID(step),
								"a step "+field.key+" interpolates "+strconv.Quote(pattern)+" during merge-queue policy validation. Titles are untrusted input: pass the title through the tool's arguments from git instead of interpolating the event payload")
							break
						}
					}
				}
			}
		}
	})
}

// envText renders a step's env mapping as text for interpolation scans.
func envText(step *yaml.Node) string {
	env := mappingValue(step, "env")
	if env == nil {
		return ""
	}
	return strings.Join(nodeScalars(env), "\n")
}

// cutLevels are the only levels the cut-release button may offer, from the
// intent package: a level, never a number.
var cutLevels = []intent.Level{intent.LevelPatch, intent.LevelMinor}

// checkCutRelease (cut-release-is-dispatch-only) requires the cut-release job
// to trigger only on workflow_dispatch, to take its level from a choice of
// exactly patch and minor, and to write only when tag mode is on. The button
// acts as the bot on any branch otherwise, and an unconstrained level or an
// untagged write cuts a release the plan never approved.
func (c *queueChecker) checkCutRelease() {
	for _, bot := range c.policy.Queue.BotJobs {
		if bot.Kind != intent.PRKindCutRelease {
			continue
		}
		doc := c.needFile(QueueRuleCutRelease, bot.Workflow)
		if doc == nil {
			continue
		}
		if len(workflowTriggers(doc)) == 0 {
			c.emit(QueueRuleCutRelease, bot.Workflow, bot.Job,
				"the "+intent.PRKindCutRelease.String()+" workflow has no trigger during merge-queue policy validation. Trigger only on "+GitHubEventWorkflowDispatch.String()+" so a push can never cut a release")
			continue
		}
		dispatch, only := soleTrigger(doc, GitHubEventWorkflowDispatch)
		if !only {
			c.emit(QueueRuleCutRelease, bot.Workflow, bot.Job,
				"the "+intent.PRKindCutRelease.String()+" workflow triggers on more than "+GitHubEventWorkflowDispatch.String()+" during merge-queue policy validation. Trigger only on "+GitHubEventWorkflowDispatch.String()+" so a push can never cut a release")
			dispatch, _ = findTrigger(doc, GitHubEventWorkflowDispatch)
		}
		c.checkCutLevel(bot.Workflow, bot.Job, dispatch)
		job := jobNode(doc, bot.Job)
		if job == nil {
			continue
		}
		for _, step := range stepsOf(job) {
			if !c.isWriteStep(bot.Workflow, bot.Job, step) || isMintStep(step) {
				continue
			}
			// The write step itself tests tag mode: a job-level gate can be
			// edited away from the payload, so the check reads the step if.
			if hasTagModeGate(stepIfText(step)) {
				continue
			}
			c.emit(QueueRuleCutRelease, bot.Workflow, bot.Job,
				"a "+intent.PRKindCutRelease.String()+" write step has no tag-mode gate during merge-queue policy validation. Test "+tagModeComparison+" in the step if so the button writes nothing while tag mode is off")
		}
	}
}

// checkCutLevel requires the cut-release level input to be a choice of exactly
// patch and minor.
func (c *queueChecker) checkCutLevel(workflow, job string, dispatch trigger) {
	inputs := mappingValue(dispatch.Config, "inputs")
	level := func() *yaml.Node {
		if inputs == nil || inputs.Kind != yaml.MappingNode {
			return nil
		}
		return mappingValue(inputs, "level")
	}()
	var typ string
	var options []intent.Level
	closed := true
	if level != nil && level.Kind == yaml.MappingNode {
		if t := mappingValue(level, "type"); t != nil {
			typ = t.Value
		}
		if o := mappingValue(level, "options"); o != nil && o.Kind == yaml.SequenceNode {
			for _, item := range o.Content {
				// An option that is not a level word is never read as one:
				// it makes the choice wider than the closed pair.
				option, err := intent.ParseLevel(item.Value)
				if err != nil {
					closed = false
					continue
				}
				options = append(options, option)
			}
		}
	}
	if typ != "choice" || !closed || !sameLevels(options, cutLevels) {
		c.emit(QueueRuleCutRelease, workflow, job,
			"the "+intent.PRKindCutRelease.String()+" level input is not a choice of exactly "+cutLevels[0].String()+" and "+cutLevels[1].String()+" during merge-queue policy validation. Offer a closed level choice so the button can never cut a numbered release")
	}
}

// sameLevels reports whether a and b hold the same levels, each the same
// number of times, in any order.
func sameLevels(a, b []intent.Level) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[intent.Level]int{}
	for _, s := range a {
		seen[s]++
	}
	for _, s := range b {
		seen[s]--
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}

// toolCheckoutSHA matches a 40-hex commit.
var toolCheckoutSHA = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)

// checkToolPin (tool-pin-is-a-sha) requires every checkout of the tool to name
// a 40-hex commit, and all of them in one repo to name the same commit. A tag
// or branch pin moves under the workflows and runs code no review approved.
func (c *queueChecker) checkToolPin() {
	pins := map[string][]string{}
	c.eachDoc(QueueRuleToolPin, func(name string, doc *yaml.Node) {
		for _, jobName := range jobNames(doc) {
			for _, step := range stepsOf(jobNode(doc, jobName)) {
				uses := stepUsesText(step)
				if !strings.Contains(uses, "checkout") {
					continue
				}
				with := mappingValue(step, "with")
				if with == nil {
					continue
				}
				repo := mappingValue(with, "repository")
				if repo == nil || repo.Value == "" {
					continue
				}
				ref := ""
				if r := mappingValue(with, "ref"); r != nil {
					ref = r.Value
				}
				if !toolCheckoutSHA.MatchString(ref) {
					c.emit(QueueRuleToolPin, name, stepID(step),
						"a tool checkout pins "+strconv.Quote(ref)+" during merge-queue policy validation, which is not a 40-hex commit. Pin the tool checkout to the reviewed commit SHA so the workflows run exactly the code the review approved")
					continue
				}
				key := strings.ToLower(ref)
				pins[key] = append(pins[key], name+" jobs."+jobName)
			}
		}
	})
	if len(pins) > 1 {
		var where []string
		for _, locs := range pins {
			where = append(where, locs...)
		}
		c.emit(QueueRuleToolPin, "", "",
			"tool checkouts name different commits during merge-queue policy validation ("+strings.Join(where, ", ")+"). Pin every tool checkout to the same commit SHA so every workflow runs the same tool")
	}
}

// checkClassify (bot-branch-via-classify) requires workflows to call the
// shared classification command instead of comparing a bot branch literal:
// no if or run compares == or != against a configured bot branch, and the
// guard workflow invokes classify to tell the release PR apart. A literal in
// one workflow drifts from the config the classifier reads.
func (c *queueChecker) checkClassify() {
	branchSet := map[string]bool{}
	for _, branch := range c.cfg.BotBranches {
		if branch != "" {
			branchSet[branch] = true
		}
	}
	c.eachDoc(QueueRuleClassify, func(name string, doc *yaml.Node) {
		for _, jobName := range jobNames(doc) {
			for _, step := range stepsOf(jobNode(doc, jobName)) {
				for _, field := range []struct {
					key   string
					value string
				}{
					{"if", stepIfText(step)},
					{"run", stepRunText(step)},
				} {
					for _, literal := range branchLiterals(field.value) {
						if branchSet[literal] {
							c.emit(QueueRuleClassify, name, stepID(step),
								"a step "+field.key+" compares bot branch "+strconv.Quote(literal)+" during merge-queue policy validation. Call "+intent.ProgramName+" "+intent.CommandClassify.String()+" instead of comparing the branch: a literal in one workflow drifts from the release config the classifier reads")
						}
					}
				}
			}
		}
	})
	for _, req := range c.policy.Queue.Required {
		if !req.Guard {
			continue
		}
		doc := c.needFile(QueueRuleClassify, req.Workflow)
		if doc == nil {
			continue
		}
		if !invokesClassify(doc) {
			c.emit(QueueRuleClassify, req.Workflow, req.Job,
				"the guard workflow never calls "+intent.ProgramName+" "+intent.CommandClassify.String()+" during merge-queue policy validation. Call "+intent.CommandClassify.String()+" to tell the release PR apart instead of comparing its head branch, so the "+intent.ErrProtectedPath.String()+" exception follows the one classifier")
		}
	}
}

var (
	// branchLiteralAfterOp matches a == or != comparison with the literal
	// after the operator. A second pattern matches the literal before the
	// operator; the checker tests both sides against the configured bot
	// branches, so the patterns themselves carry no branch names.
	branchLiteralAfterOp = regexp.MustCompile(`(==|!=)\s*['"]?([A-Za-z0-9_./-]+)['"]?`)
	// branchLiteralBeforeOp matches a literal compared before the operator,
	// as in a quoted branch on the left of ==.
	branchLiteralBeforeOp = regexp.MustCompile(`['"]?([A-Za-z0-9_./-]+)['"]?\s*(==|!=)`)

	// classifyInvocation matches the shared classifier's invocation.
	classifyInvocation = regexp.MustCompile(toolCall(intent.CommandClassify))
)

// branchLiterals returns every literal compared with == or != in the text,
// on either side of the operator.
func branchLiterals(s string) []string {
	var out []string
	for _, m := range branchLiteralAfterOp.FindAllStringSubmatch(s, -1) {
		out = append(out, m[2])
	}
	for _, m := range branchLiteralBeforeOp.FindAllStringSubmatch(s, -1) {
		out = append(out, m[1])
	}
	return out
}

func invokesClassify(doc *yaml.Node) bool {
	for _, jobName := range jobNames(doc) {
		for _, step := range stepsOf(jobNode(doc, jobName)) {
			if classifyInvocation.MatchString(stepRunText(step)) {
				return true
			}
		}
	}
	return false
}
