package policy

import (
	"strings"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
	regexp "github.com/wasilibs/go-re2"
	"gopkg.in/yaml.v3"
)

// This file is the tag-publish-verifies-the-tag rule. A tag-triggered
// workflow runs for any pushed tag its pattern matches, and a tag push needs
// only a write to refs/tags/: the tag can name a commit that never landed on
// the default branch. So every job of such a workflow that can publish (a
// GitHub Release through contents write, a package through an OIDC token)
// needs a job without a credential that runs `release-intent verify-tag` on
// the triggering tag and the default branch, and GitHub skips the publish
// job unless that job succeeds.

// triggerKeyTagsIgnore is the push trigger key that names tags to skip; a
// workflow that names it is tag-triggered too.
const triggerKeyTagsIgnore = "tags-ignore"

// defaultBranchExpression is the expression for the repository's default
// branch name, which the verify call compares against.
const defaultBranchExpression = "github.event.repository.default_branch"

// remoteBranchPrefixes are what may come before the default branch name in
// the --branch value: nothing, or the remote-tracking ref of origin.
var remoteBranchPrefixes = []string{"", "origin/", "refs/remotes/origin/"}

// publishStatusFunctions are the status functions that run a job after a job
// it needs failed, which would publish an unverified tag.
var publishStatusFunctions = []string{"always()", "cancelled()", "failure()"}

// expressionBody matches one ${{ }} expression alone and captures its body.
var expressionBody = regexp.MustCompile(`^\$\{\{\s*(.*?)\s*\}\}$`)

// tagTriggered reports whether the workflow runs on pushed tags.
func tagTriggered(doc *yaml.Node) bool {
	push, ok := findTrigger(doc, GitHubEventPush)
	if !ok {
		return false
	}
	return mappingValue(push.Config, triggerKeyTags) != nil || mappingValue(push.Config, triggerKeyTagsIgnore) != nil
}

// publishes reports whether a job's default token can publish: contents
// write (a GitHub Release) or id-token write (a registry through OIDC). A
// missing or unreadable permissions value can.
func publishes(doc, job *yaml.Node) bool {
	perms := effectivePermissions(doc, job)
	if perms == nil {
		return true
	}
	switch perms.Kind {
	case yaml.ScalarNode:
		s, err := ParsePermissionShorthand(perms.Value)
		return err != nil || s == PermissionWriteAll
	case yaml.MappingNode:
		for i := 0; i+1 < len(perms.Content); i += 2 {
			level, err := ParsePermissionLevel(perms.Content[i+1].Value)
			if err != nil || level == PermissionWrite {
				return true
			}
		}
		return false
	}
	return true
}

// checkTagVerified applies the rule to every tag-triggered workflow.
func (c *queueChecker) checkTagVerified() {
	c.eachDoc(QueueRuleTagVerified, func(name string, doc *yaml.Node) {
		if !tagTriggered(doc) {
			return
		}
		for _, jobName := range jobNames(doc) {
			job := jobNode(doc, jobName)
			if !publishes(doc, job) {
				continue
			}
			if why := c.publishVerifyFault(name, doc, jobName, job); why != "" {
				c.emit(QueueRuleTagVerified, name, jobName, violation(
					"jobs."+jobName+" can publish for a pushed tag, but "+why,
					"a tag push needs only a write to refs/tags/, so the tag can name a commit that never landed on the default branch; the publish job may run only after the tool verified the tag",
					rejected,
					"add a job without a credential that checks out with fetch-depth: 0 and fetch-tags: true and runs "+intent.ProgramName+" "+intent.CommandVerifyTag.String()+" "+intent.FlagTag.String()+" \"${GITHUB_REF_NAME}\" "+intent.FlagBranch.String()+" \"origin/${DEFAULT_BRANCH}\" with DEFAULT_BRANCH mapped from ${{ "+defaultBranchExpression+" }}, and list it in the publish job's needs"))
			}
		}
	})
}

// publishVerifyFault says why a publish job does not run behind a verified
// tag, or "" when it does.
func (c *queueChecker) publishVerifyFault(file string, doc *yaml.Node, jobName string, job *yaml.Node) string {
	if cond := mappingValue(job, "if"); cond != nil {
		for _, fn := range publishStatusFunctions {
			if strings.Contains(cond.Value, fn) {
				return "its if carries " + fn + ", which runs it even when the verify job failed"
			}
		}
	}
	needs, _ := needsList(job)
	for _, need := range needs {
		verifier := jobNode(doc, need)
		if verifier == nil || publishes(doc, verifier) {
			continue
		}
		cj := c.creds[file][need]
		if cj.class != "" || len(cj.mints) > 0 {
			continue
		}
		if cont := mappingValue(verifier, "continue-on-error"); cont != nil && cont.Value != "false" {
			continue
		}
		for _, step := range stepsOf(verifier) {
			if c.verifiesTag(doc, verifier, step) {
				return ""
			}
		}
	}
	return "no job it needs is a job without a credential whose step runs " + intent.ProgramName + " " + intent.CommandVerifyTag.String() + " on the triggering tag and the default branch, with no if and no continue-on-error"
}

// verifiesTag reports whether a step runs the verify command on the
// triggering tag and the default branch, unconditionally.
func (c *queueChecker) verifiesTag(doc, job, step *yaml.Node) bool {
	if mappingValue(step, "if") != nil {
		return false
	}
	if cont := mappingValue(step, "continue-on-error"); cont != nil && cont.Value != "false" {
		return false
	}
	env := effectiveEnv(doc, job, step)
	for _, call := range toolCalls(stepRunText(step)) {
		if len(call) < 2 || call[1].bare != intent.CommandVerifyTag.String() {
			continue
		}
		tag, branch := flagWord(call, intent.FlagTag), flagWord(call, intent.FlagBranch)
		if isTriggeringTag(tag, env) && isDefaultBranch(branch, env) {
			return true
		}
	}
	return false
}

// flagWord returns the bare word after a flag in one tool call, or "".
func flagWord(call shellCommand, flag intent.Flag) string {
	for i := 2; i+1 < len(call); i++ {
		if call[i].bare == flag.String() {
			return call[i+1].bare
		}
	}
	return ""
}

// isTriggeringTag reports whether a word carries the triggering tag:
// ${{ github.ref_name }}, $GITHUB_REF_NAME, or an env name mapped from
// ${{ github.ref_name }}.
func isTriggeringTag(word string, env map[string]string) bool {
	if expressionOf(word) == triggeringRefName {
		return true
	}
	name, ok := shellParameter(word)
	if !ok {
		return false
	}
	if value, set := env[name]; set {
		return expressionOf(value) == triggeringRefName
	}
	return name == runnerEnvRefName
}

// isDefaultBranch reports whether a word names the default branch, as a
// branch or origin's remote-tracking ref: ${{ github.event.repository.
// default_branch }} or an env name mapped from it, after one of the
// remote prefixes.
func isDefaultBranch(word string, env map[string]string) bool {
	for _, prefix := range remoteBranchPrefixes {
		rest, ok := strings.CutPrefix(word, prefix)
		if !ok {
			continue
		}
		if expressionOf(rest) == defaultBranchExpression {
			return true
		}
		if name, ok := shellParameter(rest); ok && expressionOf(env[name]) == defaultBranchExpression {
			return true
		}
	}
	return false
}

// expressionOf returns the body of a value that is one ${{ }} expression, or
// "".
func expressionOf(value string) string {
	if m := expressionBody.FindStringSubmatch(strings.TrimSpace(value)); m != nil {
		return m[1]
	}
	return ""
}

// shellParameter returns the name of a lone $NAME or ${NAME}.
func shellParameter(word string) (string, bool) {
	name, ok := strings.CutPrefix(word, "$")
	if !ok {
		return "", false
	}
	if inner, braced := strings.CutPrefix(name, "{"); braced {
		name, ok = strings.CutSuffix(inner, "}")
		if !ok {
			return "", false
		}
	}
	if name == "" {
		return "", false
	}
	for i := 0; i < len(name); i++ {
		if !isNameChar(name[i], i == 0) {
			return "", false
		}
	}
	return name, true
}
