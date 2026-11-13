package policy

import (
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/peasant-labs/infra/tools/release/internal/closedset"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
	"gopkg.in/yaml.v3"
)

// This file is the tag-publish-verifies-the-tag rule. A tag-triggered
// workflow runs for any pushed tag its pattern matches, and a tag push needs
// only a write to refs/tags/: the tag can name a commit that never landed on
// the default branch. So every job of such a workflow that can publish (a
// GitHub Release through contents write, a package through an OIDC token)
// needs a tag-verify job, and GitHub skips the publish job unless that job
// succeeds.
//
// The tag-verify job's success is the verdict on the tag, so anything in it
// that can make it succeed without the tool's yes is a bypass: a masked exit
// status (|| true, ; exit 0, a pipe, a background job, set +e, a custom
// shell), a PATH entry or a stub binary that answers in the tool's place, a
// go.work in the tag's checkout that swaps the tool's modules, or a restored
// build cache. So the job is confined the way a job that holds a credential
// is: it runs only the steps of the VerifyStep set, each in one fixed shape,
// with an environment of VerifyEnv names at their fixed values, and every
// run step is read through singleCommand. It carries no job-level if, so it
// cannot be skipped into a pass. The publish job's own if calls no status
// function and reads no needs result: GitHub adds its implicit success()
// check only when the expression calls no status function, and that implicit
// check is what skips the publish job after a failed or skipped verify job.

// triggerKeyTagsIgnore is the push trigger key that names tags to skip; a
// workflow that names it is tag-triggered too.
const triggerKeyTagsIgnore = "tags-ignore"

// defaultBranchExpression is the expression for the repository's default
// branch name, which the verify call compares against.
const defaultBranchExpression = "github.event.repository.default_branch"

// remoteBranchPrefixes are what may come before the default branch name in
// the --branch value: nothing, or the remote-tracking ref of origin.
var remoteBranchPrefixes = []string{"", "origin/", "refs/remotes/origin/"}

// verifyStepKeys are the keys a tag-verify job's step may set. shell,
// working-directory, if and continue-on-error are not among them: each can
// change what the step runs or whether its failure fails the job.
var verifyStepKeys = []string{"id", "name", "uses", "with", "run", "env", "timeout-minutes"}

// verifyJobKeys are the keys a tag-verify job may set. if, defaults,
// container, services, uses, strategy and continue-on-error are not among
// them: each can change whether the job runs, the shell, the machine, the
// code that runs, or whether a failure fails the job. The job may carry no
// if because the publish job's need of it is what has to decide: a skipped
// verify job must skip the publish job.
var verifyJobKeys = []string{"name", "needs", "runs-on", "permissions", "env", "outputs", "timeout-minutes", "concurrency", "environment", "steps"}

// publishIfFault says why a publish job's if can run after the verify job
// failed or was skipped, or "". GitHub adds its implicit success() check to a
// job if only when the expression calls no status function, and it evaluates
// needs.<job>.result against the job's status rather than the check: a
// conditional that calls success(), failure(), always() or cancelled(), or
// that reads a needed job's result, decides the run on its own.
func publishIfFault(cond string) string {
	if fns := statusFunctionsIn(cond); len(fns) > 0 {
		return "its if calls the status function " + fns[0].Call() + ", which turns off GitHub's implicit success() check, so it runs even when the verify job failed or was skipped"
	}
	if needsResultRead.MatchString(cond) {
		return "its if reads a needed job's result, so it decides the run from the result instead of GitHub's implicit success() check, and a skipped verify job can let it publish"
	}
	return ""
}

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
			if why := c.publishVerifyFault(name, doc, job); why != "" {
				c.emit(QueueRuleTagVerified, name, jobName, violation(
					"jobs."+jobName+" can publish for a pushed tag, but "+why,
					"a tag push needs only a write to refs/tags/, so the tag can name a commit that never landed on the default branch; the publish job may run only after the tool verified the tag, and only a confined tag-verify job's success means it did",
					rejected,
					"list in the publish job's needs a job without a credential whose steps are only "+verifyShapes()+", with env "+verifyEnvShapes()+" and no other env, shell, working-directory, defaults, if or continue-on-error"))
			}
		}
	})
}

// verifyShapes lists the tag-verify steps for a message.
func verifyShapes() string {
	var out []string
	for _, s := range AllVerifySteps {
		out = append(out, s.String()+" `"+s.Shape()+"`")
	}
	return strings.Join(out, "; ")
}

// verifyEnvShapes lists the tag-verify env names and values for a message.
func verifyEnvShapes() string {
	var out []string
	for _, e := range AllVerifyEnvs {
		out = append(out, e.String()+": "+e.Value())
	}
	return strings.Join(out, ", ")
}

// publishVerifyFault says why a publish job does not run behind a verified
// tag, or "" when it does: its if carries no status function that runs it
// after a failure, and one job it needs is a confined tag-verify job. When
// none is, it names each candidate's first fault.
func (c *queueChecker) publishVerifyFault(file string, doc, job *yaml.Node) string {
	if cond := mappingValue(job, "if"); cond != nil {
		if why := publishIfFault(cond.Value); why != "" {
			return why
		}
	}
	needs, _ := needsList(job)
	var faults []string
	for _, need := range needs {
		verifier := jobNode(doc, need)
		if verifier == nil || publishes(doc, verifier) {
			continue
		}
		why := c.tagVerifyJobFault(file, doc, need, verifier)
		if why == "" {
			return ""
		}
		faults = append(faults, "jobs."+need+" "+why)
	}
	if len(faults) == 0 {
		return "it needs no job without a credential, so nothing verifies the tag before it publishes"
	}
	return "no job it needs is a confined tag-verify job (" + strings.Join(faults, "; ") + ")"
}

// tagVerifyJobFault says why a job is not a confined tag-verify job, or ""
// when it is.
func (c *queueChecker) tagVerifyJobFault(file string, doc *yaml.Node, jobName string, job *yaml.Node) string {
	if cj := c.creds[file][jobName]; cj.class != "" || len(cj.mints) > 0 {
		return "may hold a credential"
	}
	if cond := mappingValue(job, "if"); cond != nil {
		return "sets a job-level if (" + quote(cond.Value) + "), and a skipped verify job must skip every job that needs it: drop the if so the job runs and its exit status decides"
	}
	if key := keyOutside(job, verifyJobKeys); key != "" {
		return "sets " + key + ", which a tag-verify job may not set"
	}
	if mappingValue(doc, "defaults") != nil {
		return "runs in a workflow that sets defaults, which can change the shell of every run step"
	}
	if why := runnerFault(mappingValue(job, "runs-on")); why != "" {
		return why
	}
	steps := stepsOf(job)
	toolPath, why := verifyToolCheckout(steps)
	if why != "" {
		return why
	}
	verified := false
	for i, step := range steps {
		kind, why := c.verifyStepFault(doc, job, step, toolPath)
		if why != "" {
			return "has " + strings.TrimPrefix(stepLabel(jobName, i, step), "jobs."+jobName+" ") + ", which " + why
		}
		verified = verified || kind == VerifyStepVerify
	}
	if !verified {
		return "runs no " + VerifyStepVerify.String() + " step `" + VerifyStepVerify.Shape() + "`"
	}
	return ""
}

// keyOutside returns the first key of a mapping outside the allowed set, or
// "".
func keyOutside(node *yaml.Node, allowed []string) string {
	for i := 0; node != nil && i+1 < len(node.Content); i += 2 {
		if key := node.Content[i].Value; !slices.Contains(allowed, key) {
			return key
		}
	}
	return ""
}

// verifyToolCheckout returns the path of the job's one tool checkout, the
// directory the build step changes into.
func verifyToolCheckout(steps []*yaml.Node) (string, string) {
	var paths []string
	for _, step := range steps {
		with := mappingValue(step, "with")
		if isVerifyAction(step, VerifyActionCheckout) && mappingValue(with, VerifyInputRepository.String()) != nil {
			paths = append(paths, path.Clean(valueOf(mappingValue(with, VerifyInputPath.String()))))
		}
	}
	if len(paths) != 1 {
		return "", "checks out the tool " + strconv.Itoa(len(paths)) + " times, and a tag-verify job builds exactly one tool checkout"
	}
	return paths[0], ""
}

// isVerifyAction reports whether a step uses the given action at any ref.
func isVerifyAction(step *yaml.Node, action VerifyAction) bool {
	name, _, ok := usesAction(stepUsesText(step))
	return ok && name == action.String()
}

// verifyStepFault classifies one step of a tag-verify job and says why it is
// not that kind's one shape, or "" when it is.
func (c *queueChecker) verifyStepFault(doc, job, step *yaml.Node, toolPath string) (VerifyStep, string) {
	if key := keyOutside(step, verifyStepKeys); key != "" {
		return "", "sets " + key + ", which a tag-verify step may not set"
	}
	if why := verifyEnvFault(effectiveEnv(doc, job, step)); why != "" {
		return "", why
	}
	if uses := stepUsesText(step); uses != "" {
		return verifyActionFault(step, uses, toolPath)
	}
	words, why := singleCommand(doc, job, step)
	if why != "" {
		return "", "is not one command: " + why
	}
	if why := matchVerifyBuild(words, toolPath); why == "" {
		if env := effectiveEnv(doc, job, step); env[VerifyEnvGoWork.String()] != VerifyEnvGoWork.Value() {
			return VerifyStepBuild, "builds the tool without " + VerifyEnvGoWork.String() + ": " + VerifyEnvGoWork.Value() + ", so a go.work file in the tag's checkout could replace the tool's modules"
		}
		return VerifyStepBuild, ""
	}
	if why := matchVerifyTag(words); why != "" {
		return "", why
	}
	return VerifyStepVerify, ""
}

// verifyEnvFault says why an effective env is not made only of VerifyEnv
// names at their values, or "".
func verifyEnvFault(env map[string]string) string {
	for _, name := range sortedKeys(env) {
		member, err := ParseVerifyEnv(name)
		if err != nil {
			return "runs with env " + name + ", which is not one of " + closedset.Join(AllVerifyEnvs, ", ") + " and can change which binary runs or what the build compiles"
		}
		if strings.TrimSpace(env[name]) != member.Value() {
			return "runs with env " + name + " set to " + strconv.Quote(env[name]) + " instead of " + strconv.Quote(member.Value())
		}
	}
	return ""
}

// verifyActionFault classifies an action step of a tag-verify job and says
// why it is not one of the three action shapes, or "".
func verifyActionFault(step *yaml.Node, uses, toolPath string) (VerifyStep, string) {
	name, ref, ok := usesAction(uses)
	action, err := ParseVerifyAction(name)
	if !ok || err != nil {
		return "", "uses " + strconv.Quote(uses) + ", which is not one of " + closedset.Join(AllVerifyActions, ", ")
	}
	if !actionPinSHA.MatchString(ref) {
		return "", "uses " + strconv.Quote(uses) + ", pinned to " + strconv.Quote(ref) + " instead of a 40-hex commit"
	}
	with := mappingValue(step, "with")
	kind := VerifyStepSetupGo
	if action == VerifyActionCheckout {
		kind = VerifyStepRepoCheckout
		if mappingValue(with, VerifyInputRepository.String()) != nil {
			kind = VerifyStepToolCheckout
		}
	}
	allowed, required := verifyInputs(kind)
	for i := 0; with != nil && i+1 < len(with.Content); i += 2 {
		key, value := with.Content[i].Value, with.Content[i+1].Value
		input, err := ParseVerifyInput(key)
		if err != nil || !slices.Contains(allowed, input) {
			return kind, "passes input " + key + ", which the " + kind.String() + " step may not take (it takes " + closedset.Join(allowed, ", ") + ")"
		}
		if strings.Contains(value, "${{") {
			return kind, "passes input " + key + " from an expression; the " + kind.String() + " step takes literal inputs"
		}
		if why := verifyInputValueFault(input, strings.TrimSpace(value), toolPath); why != "" {
			return kind, "passes " + key + ": " + strconv.Quote(value) + ", but " + why
		}
	}
	for _, input := range required {
		if mappingValue(with, input.String()) == nil {
			return kind, "does not pass " + input.String() + ", which the " + kind.String() + " step must pass: " + kind.Shape()
		}
	}
	return kind, ""
}

// verifyInputValueFault says why a literal input value is not one the input
// may carry, or "".
func verifyInputValueFault(input VerifyInput, value, toolPath string) string {
	switch input {
	case VerifyInputPersistCredentials, VerifyInputCache:
		if value != "false" {
			return input.String() + " must be false"
		}
	case VerifyInputRepository:
		if value != toolRepository {
			return "the tool comes only from " + toolRepository
		}
	case VerifyInputRef:
		if !actionPinSHA.MatchString(value) {
			return "the tool checkout names a 40-hex commit"
		}
	case VerifyInputPath:
		if !pathWord.MatchString(value) || strings.Contains(value, "..") {
			return "the tool checkout lands in a literal subdirectory of the workspace"
		}
	case VerifyInputGoVersionFile:
		if want := path.Join(toolBuildDir(toolPath), toolGoMod); value != want {
			return "the Go version comes from the tool's own " + strconv.Quote(want)
		}
	}
	return ""
}

// matchVerifyBuild checks one command against the build template: the tool
// checkout's module built into the runner's temporary directory.
func matchVerifyBuild(words []templateWord, toolPath string) string {
	want := []string{"go", "build", "-C", toolBuildDir(toolPath), "-o", "", toolMainPackage}
	if len(words) != len(want) {
		return "is not the build template"
	}
	for i, lit := range want {
		if lit == "" {
			if !words[i].params("", runnerEnvTemp, "/"+intent.ProgramName) {
				return "is not the build template"
			}
			continue
		}
		if got, ok := words[i].literal(); !ok || got != lit {
			return "is not the build template"
		}
	}
	return ""
}

// matchVerifyTag checks one command against the verify template: the tool
// built in this job, run by its absolute path on the triggering tag and the
// default branch.
func matchVerifyTag(words []templateWord) string {
	notOne := "runs neither the " + VerifyStepBuild.String() + " template `" + VerifyStepBuild.Shape() + "` nor the " + VerifyStepVerify.String() + " template `" + VerifyStepVerify.Shape() + "`"
	if len(words) != 6 || !words[0].params("", runnerEnvTemp, "/"+intent.ProgramName) {
		return notOne
	}
	for i, lit := range []string{intent.CommandVerifyTag.String(), intent.FlagTag.String()} {
		if got, ok := words[i+1].literal(); !ok || got != lit {
			return notOne
		}
	}
	if !words[3].params("", runnerEnvRefName, "") {
		return "verifies the tag " + quote(words[3].raw) + ", not the triggering tag \"${" + runnerEnvRefName + "}\""
	}
	if got, ok := words[4].literal(); !ok || got != intent.FlagBranch.String() {
		return notOne
	}
	for _, prefix := range remoteBranchPrefixes {
		if words[5].params(prefix, VerifyEnvDefaultBranch.String(), "") {
			return ""
		}
	}
	return "verifies against " + quote(words[5].raw) + ", not the default branch \"origin/${" + VerifyEnvDefaultBranch.String() + "}\""
}
