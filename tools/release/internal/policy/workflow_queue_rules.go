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

// This file holds the last four queue rules: the title guard cannot pass
// early, range readers take the floor, each bot branch has exactly one writer,
// and token-minting jobs check the ref first.

// checkTitleGuard (title-guard-no-early-success) fails the policy when no
// required entry is marked as the guard, and otherwise requires the guard
// workflow's guard job to carry no job-level if and no continue-on-error. The
// guard workflow must never read the pull-request action or the changed-file
// list: a body edit does not change the head commit, so a run that ends green
// without checking would replace a red run. The event name may be tested in a
// step-level if and in the concurrency expression (to scope cancellation to
// pull_request); anywhere else, and any read of the event file, is rejected.
func (c *queueChecker) checkTitleGuard() {
	var guards []RequiredContext
	for _, req := range c.policy.Queue.Required {
		if req.Guard {
			guards = append(guards, req)
		}
	}
	if len(guards) == 0 {
		c.emit(QueueRuleTitleGuard, "", "",
			"the policy names no guard entry during merge-queue policy validation. Mark the title-guard required context with guard: true so one workflow runs the full check on every event with no early exit")
		return
	}
	for _, req := range guards {
		doc := c.needFile(QueueRuleTitleGuard, req.Workflow)
		if doc == nil {
			continue
		}
		job := jobNode(doc, req.Job)
		if job == nil {
			continue
		}
		if cond := mappingValue(job, "if"); cond != nil {
			c.emit(QueueRuleTitleGuard, req.Workflow, req.Job,
				"the guard job has a job-level if during merge-queue policy validation. Drop it so the full check runs on every event, including an edited PR body that changes no commit")
		}
		c.checkGuardActivityTypes(req, doc)
		if hasContinueOnError(job) {
			c.emit(QueueRuleTitleGuard, req.Workflow, req.Job,
				"the guard job sets continue-on-error during merge-queue policy validation. Drop the key so a failed check stays red instead of passing the queue; GitHub evaluates it as a value, and an expression such as ${{ fromJSON('true') }} makes it true")
		}
		for i, step := range stepsOf(job) {
			if hasContinueOnError(step) {
				c.emit(QueueRuleTitleGuard, req.Workflow, req.Job,
					stepLabel(req.Job, i, step)+" sets continue-on-error during merge-queue policy validation. Drop the key so a failed check stays red instead of passing the queue; GitHub evaluates it as a value, and an expression such as ${{ fromJSON('true') }} makes it true")
			}
		}
		for _, scalar := range fileScalars(doc) {
			if strings.Contains(scalar, "github.event.action") || strings.Contains(scalar, "github.event.changes") {
				c.emit(QueueRuleTitleGuard, req.Workflow, req.Job,
					"the guard workflow reads "+eventRead(scalar)+" during merge-queue policy validation. Never read the action or the changed-file list here: a body edit changes no commit, so a skipped run would replace a red check with green; check the title from git on every event")
				break
			}
			if strings.Contains(scalar, "GITHUB_EVENT_PATH") {
				c.emit(QueueRuleTitleGuard, req.Workflow, req.Job,
					"the guard workflow reads the event file during merge-queue policy validation. Read titles and SHAs from git through the tool's arguments instead, so a re-run of an old event cannot replace a newer result")
				break
			}
		}
		for _, name := range eventNameMisuse(doc) {
			c.emit(QueueRuleTitleGuard, req.Workflow, req.Job,
				"the guard workflow tests github.event_name in "+name+" during merge-queue policy validation. Test the event name only in a step-level if or in the concurrency expression")
		}
	}
}

// guardPullRequestActions are the activity types the guard workflow must run
// on: every default type, so a head change is checked, plus the edit, so a
// retitle re-runs the check without a push.
var guardPullRequestActions = append(slices.Clone(DefaultPullRequestActions), PullRequestActionEdited)

// checkGuardActivityTypes requires the guard workflow's pull_request trigger
// to list its activity types and to include every guard action. GitHub's
// default types omit the edit, so a trigger with no list, or a list without
// the edit, leaves a red title check red after the author fixes the title.
func (c *queueChecker) checkGuardActivityTypes(req RequiredContext, doc *yaml.Node) {
	want := closedset.Join(guardPullRequestActions, ", ")
	pr, ok := findTrigger(doc, GitHubEventPullRequest)
	if !ok {
		c.emit(QueueRuleTitleGuard, req.Workflow, req.Job,
			"the guard workflow has no "+GitHubEventPullRequest.String()+" trigger during merge-queue policy validation. Trigger on "+GitHubEventPullRequest.String()+" with "+triggerKeyTypes+": ["+want+"] so every head change and every retitle runs the full check")
		return
	}
	actions, listed := activityTypes(pr)
	if !listed {
		c.emit(QueueRuleTitleGuard, req.Workflow, req.Job,
			"the guard workflow's "+GitHubEventPullRequest.String()+" trigger names no "+triggerKeyTypes+" during merge-queue policy validation, and GitHub's default types ("+closedset.Join(DefaultPullRequestActions, ", ")+") omit "+PullRequestActionEdited.String()+". List "+triggerKeyTypes+": ["+want+"] so a retitle re-runs the check without a push")
		return
	}
	for _, action := range guardPullRequestActions {
		if !slices.Contains(actions, action) {
			c.emit(QueueRuleTitleGuard, req.Workflow, req.Job,
				"the guard workflow's "+GitHubEventPullRequest.String()+" trigger names "+triggerKeyTypes+" without "+action.String()+" during merge-queue policy validation. List "+triggerKeyTypes+": ["+want+"] so every head change and every retitle runs the full check")
		}
	}
}

// eventNameMisuse returns the locations outside a step-level if and the
// concurrency expression where the workflow tests the event name: job-level
// ifs, run blocks, and the workflow header. Step-level ifs and the
// concurrency subtree are the two allowed scopes.
func eventNameMisuse(doc *yaml.Node) []string {
	var out []string
	for _, scalar := range docScalarsExcept(doc, "jobs", "concurrency", workflowKeyOn) {
		if strings.Contains(scalar, "github.event_name") {
			out = append(out, "the workflow header")
			break
		}
	}
	for _, jobName := range jobNames(doc) {
		job := jobNode(doc, jobName)
		for i := 0; i+1 < len(job.Content); i += 2 {
			key, val := job.Content[i].Value, job.Content[i+1]
			switch key {
			case "concurrency", "steps":
				continue
			}
			for _, scalar := range nodeScalars(val) {
				if strings.Contains(scalar, "github.event_name") {
					out = append(out, "jobs."+jobName+"."+key)
					break
				}
			}
		}
		for _, step := range stepsOf(job) {
			for i := 0; i+1 < len(step.Content); i += 2 {
				key, val := step.Content[i].Value, step.Content[i+1]
				if key == "if" {
					continue
				}
				for _, scalar := range nodeScalars(val) {
					if strings.Contains(scalar, "github.event_name") {
						out = append(out, "a step "+key+" in jobs."+jobName)
						break
					}
				}
			}
		}
	}
	return out
}

// docScalarsExcept returns every scalar in the document outside the named
// top-level keys.
func docScalarsExcept(doc *yaml.Node, except ...string) []string {
	skip := map[string]bool{}
	for _, k := range except {
		skip[k] = true
	}
	var out []string
	for i := 0; i+1 < len(doc.Content); i += 2 {
		if skip[doc.Content[i].Value] {
			continue
		}
		out = append(out, nodeScalars(doc.Content[i+1])...)
	}
	return out
}

// nodeScalars returns every scalar value under n.
func nodeScalars(n *yaml.Node) []string {
	var out []string
	var walk func(node *yaml.Node)
	walk = func(node *yaml.Node) {
		if node.Kind == yaml.ScalarNode {
			out = append(out, node.Value)
			return
		}
		for _, child := range node.Content {
			walk(child)
		}
	}
	walk(n)
	return out
}

func eventRead(scalar string) string {
	if strings.Contains(scalar, "github.event.action") {
		return "github.event.action"
	}
	return "github.event.changes"
}

// checkRangeFloor requires every workflow step that reads a commit range to
// pass intent.FlagFloor quoted, and every planner step to pass intent.FlagMode
// quoted too. The argument can directly expand the corresponding typed
// repository variable, or expand an env variable mapped from it. An unquoted
// expansion vanishes when the variable is unset -- the normal state, set only
// for recovery -- turning the flag into a missing value or swallowing the
// next flag, so the job fails closed red on every run.
func (c *queueChecker) checkRangeFloor() {
	c.eachDoc(QueueRuleRangeFloor, func(name string, doc *yaml.Node) {
		for _, jobName := range jobNames(doc) {
			job := jobNode(doc, jobName)
			for _, step := range stepsOf(job) {
				run := stepRunText(step)
				if !rangeReader.MatchString(run) {
					continue
				}
				c.checkQuotedFlag(name, job, step, run, intent.FlagFloor, intent.VarBatchFloor)
				if planRunner.MatchString(run) {
					c.checkQuotedFlag(name, job, step, run, intent.FlagMode, intent.VarBatchMode)
				}
			}
		}
	})
}

// checkQuotedFlag requires one range-reader argument in its quoted form: the
// flag followed by "${{ vars.VAR }}" directly, or by "$ENV" with ENV mapped
// from the variable in the step or job env.
func (c *queueChecker) checkQuotedFlag(name string, job, step *yaml.Node, run string, flag intent.Flag, variable intent.RepoVariable) {
	value, ok := flagValue(run, flag.String())
	if !ok || !c.quotedVariable(step, job, value, variable) {
		c.emit(QueueRuleRangeFloor, name, stepID(step),
			"a step reads a commit range without a quoted "+flag.String()+" during merge-queue policy validation. Pass "+flag.String()+" \"${{ "+repoVar(variable)+" }}\" (or "+flag.String()+" \"$VAR\" with VAR mapped from the variable in env): an unquoted expansion vanishes when the variable is unset and the flag eats the next argument")
	}
}

// quotedVariable reports whether value carries the variable: the double-quoted
// ${{ vars.VAR }} form directly, or a double-quoted $ENV mapped from the
// variable in the step or job env. Single quotes and bare words never expand.
func (c *queueChecker) quotedVariable(step, job *yaml.Node, value string, variable intent.RepoVariable) bool {
	if len(value) >= 2 && strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`) {
		inner := value[1 : len(value)-1]
		if strings.Contains(inner, repoVar(variable)) {
			return true
		}
		if strings.HasPrefix(inner, "$") {
			if v := envValue(step, inner[1:]); strings.Contains(v, repoVar(variable)) {
				return true
			}
			if v := envValue(job, inner[1:]); strings.Contains(v, repoVar(variable)) {
				return true
			}
		}
	}
	return false
}

// envValue returns the value of the named env entry on a step or job, or "".
func envValue(node *yaml.Node, name string) string {
	if env := mappingValue(node, "env"); env != nil {
		if v := mappingValue(env, name); v != nil {
			return v.Value
		}
	}
	return ""
}

// flagValue returns the shell word following the flag in the run line, keeping
// quoted spans whole, or false when the flag is absent or valueless.
func flagValue(run, flag string) (string, bool) {
	words := shellWords(run)
	for i, w := range words {
		if w == flag && i+1 < len(words) {
			return words[i+1], true
		}
		if rest, ok := strings.CutPrefix(w, flag+"="); ok {
			return rest, true
		}
	}
	return "", false
}

// shellWords is the checker's one shell-word splitter. It splits a run line
// on unquoted whitespace and keeps three spans whole: a single-quoted span, a
// double-quoted span, and an unquoted GitHub expression (${{ ... }}), which
// may hold spaces. Quotes stay in the word so a caller can tell quoted from
// bare; bareWord strips them where the bare value is needed.
func shellWords(s string) []string {
	var out []string
	var cur strings.Builder
	var quote byte
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case quote != 0:
			cur.WriteByte(ch)
			if ch == quote {
				quote = 0
			}
		case ch == '"' || ch == '\'':
			quote = ch
			cur.WriteByte(ch)
		case strings.HasPrefix(s[i:], "${{"):
			end := strings.Index(s[i:], "}}")
			if end < 0 {
				cur.WriteString(s[i:])
				i = len(s)
				break
			}
			cur.WriteString(s[i : i+end+2])
			i += end + 1
		case ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r':
			flush()
		default:
			cur.WriteByte(ch)
		}
	}
	flush()
	return out
}

// bareWord returns a shell word with its delimiting quotes removed, the value
// the shell would pass to the command (expansions left as written).
func bareWord(word string) string {
	var out strings.Builder
	var quote byte
	for i := 0; i < len(word); i++ {
		ch := word[i]
		switch {
		case quote != 0 && ch == quote:
			quote = 0
		case quote == 0 && (ch == '"' || ch == '\''):
			quote = ch
		default:
			out.WriteByte(ch)
		}
	}
	return out.String()
}

// tagOnlyTriggerFault says why the workflow's on: is not exactly push: tags:
// with every pattern under the tag prefix, or "" when it is.
func tagOnlyTriggerFault(doc *yaml.Node, prefix string) string {
	push, only := soleTrigger(doc, GitHubEventPush)
	if !only {
		return "the workflow's only trigger is not " + GitHubEventPush.String() + ": " + triggerKeyTags + ":"
	}
	if push.Config == nil || len(push.Config.Content) != 2 || push.Config.Content[0].Value != triggerKeyTags {
		return "the workflow's " + GitHubEventPush.String() + " trigger is not limited to " + triggerKeyTags + ": alone"
	}
	var patterns []string
	switch tags := push.Config.Content[1]; tags.Kind {
	case yaml.ScalarNode:
		patterns = []string{tags.Value}
	case yaml.SequenceNode:
		for _, item := range tags.Content {
			patterns = append(patterns, item.Value)
		}
	}
	if len(patterns) == 0 {
		return "the workflow's " + GitHubEventPush.String() + ": " + triggerKeyTags + ": names no pattern"
	}
	if prefix == "" {
		return "the release config sets no tag_prefix to hold the tag patterns to"
	}
	for _, pattern := range patterns {
		if !strings.HasPrefix(pattern, prefix) {
			return "the tag pattern " + strconv.Quote(pattern) + " does not start with the release config's tag_prefix " + strconv.Quote(prefix)
		}
	}
	return ""
}

func (c *queueChecker) checkTokenMint() {
	type target struct {
		workflow, job string
		kind          intent.PRKind
		isRelease     bool
	}
	var jobs []target
	if rel := c.policy.Queue.ReleaseJob; rel != nil {
		jobs = append(jobs, target{rel.Workflow, rel.Job, intent.PRKindRelease, true})
	}
	for _, bot := range c.policy.Queue.BotJobs {
		jobs = append(jobs, target{bot.Workflow, bot.Job, bot.Kind, false})
	}
	for _, target := range jobs {
		doc := c.needFile(QueueRuleTokenMint, target.workflow)
		if doc == nil {
			continue
		}
		job := jobNode(doc, target.job)
		if job == nil {
			continue
		}
		var mint *yaml.Node
		for _, step := range stepsOf(job) {
			if isMintStep(step) {
				mint = step
				break
			}
		}
		if mint == nil {
			continue
		}
		jobCond := ""
		if cond := mappingValue(job, "if"); cond != nil {
			jobCond = cond.Value
		}
		if !hasRefCheck(stepIfText(mint)) && !hasRefCheck(jobCond) {
			c.emit(QueueRuleTokenMint, target.workflow, target.job,
				"job "+strconv.Quote(target.job)+" mints the App token without checking github.ref during merge-queue policy validation. Compare "+equals(contextRef, intent.BranchRef("<default>").String())+" in the mint step's own if or the job if, or a dispatch on any branch runs that branch's file with the App key")
		}
		// Only the release job and the cut-release job mint behind tag mode:
		// the vendor-hash fix lands whenever its tree is stale, tag mode or
		// not.
		if (target.isRelease || target.kind == intent.PRKindCutRelease) &&
			!hasTagModeGate(stepIfText(mint)) && !hasTagModeGate(jobCond) {
			c.emit(QueueRuleTokenMint, target.workflow, target.job,
				"job "+strconv.Quote(target.job)+" mints the App token without a tag-mode gate during merge-queue policy validation. Mint only when tag mode is on, with "+tagModeComparison+" in the mint step's if or the job if, so a tag-mode-off run mints no token")
		}
	}
}

// refComparison matches the canonical default-branch comparison: the
// GitHub ref against a fully qualified branch ref, or the format form against
// the repository's default branch.
var refComparison = regexp.MustCompile(
	regexp.QuoteMeta(contextRef) + `\s*==\s*'` + regexp.QuoteMeta(intent.BranchRefPrefix) + `[^']+'|` +
		regexp.QuoteMeta(contextRef) + `\s*==\s*format\('` + regexp.QuoteMeta(intent.BranchRefPrefix) + `\{0\}'[^)]*default_branch`)

// contextRef is the expression that reads the ref a run was triggered for.
const contextRef = "github.ref"

// hasRefCheck reports whether an if carries the canonical default-branch
// comparison as a single comparison: the ref comparison, with no disjunction.
// A run-block test does not stop the job, so only an if counts.
func hasRefCheck(cond string) bool {
	return refComparison.MatchString(cond) && !strings.Contains(cond, "||")
}
