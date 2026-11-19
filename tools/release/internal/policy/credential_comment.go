package policy

import (
	"path"
	"slices"
	"strconv"
	"strings"

	regexp "github.com/wasilibs/go-re2"
	"gopkg.in/yaml.v3"
)

// Secret and repository names are literal single identifiers, never lists or
// expressions. The names come from policy and action inputs, not shell text.
var secretName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var repositoryName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)
var prEventComparison = regexp.MustCompile(`(?i)^github\.event_name\s*==\s*'pull_request'$`)

// reviewedCheckoutDir separates every checked-out import from artifact data.
const reviewedCheckoutDir = "reviewed"

func safeRelativePath(value string) bool {
	return value != "" && pathWord.MatchString(value) && !strings.Contains(value, "..") && !strings.HasPrefix(value, "/") && path.Clean(value) != "."
}

// runPREventComparison is the gate of a workflow_run comment job: the
// producing run was a pull_request run. github.event_name is workflow_run
// there, so the pull_request comparison can never be the gate.
var runPREventComparison = regexp.MustCompile(`(?i)^github\.event\.workflow_run\.event\s*==\s*'pull_request'$`)

// workflowRunIDExpression is the only run-id a workflow_run comment job may
// download from: the run that triggered it.
const workflowRunIDExpression = "github.event.workflow_run.id"

// workflowRunTokenExpression is the only github-token a workflow_run comment
// job's download may pass: the read-only default token. The action needs a
// token to read another run's artifacts, and it is not a write credential.
const workflowRunTokenExpression = "github.token"

// triggerPullRequestTarget is spelled out because the event set does not
// contain it: a comment job's workflow may never trigger on it.
const triggerPullRequestTarget = "pull_request_target"

// workflowRunCommentRefusedEnv lists the comment-only env names a workflow_run
// comment job may not carry: the PR number is read from the evidence by the
// reviewed script, and the job mints no App token.
var workflowRunCommentRefusedEnv = []TemplateEnv{TemplateEnvPRNumber, TemplateEnvJourneyAppToken}

// hasRunGate reports whether the condition has the job class's PR event gate
// as one conjunct of a conjunction: github.event_name == 'pull_request' for a
// pull_request comment job, and github.event.workflow_run.event ==
// 'pull_request' for a workflow_run comment job.
func (j credentialJob) hasRunGate(cond string) bool {
	comparison := prEventComparison
	if j.viaWorkflowRun() {
		comparison = runPREventComparison
	}
	expr := stripIfExpression(cond)
	if strings.Contains(expr, "||") {
		return false
	}
	for _, part := range splitConjuncts(expr) {
		if comparison.MatchString(trimOuterParens(part)) {
			return true
		}
	}
	return false
}

// runGateText names the gate a comment job must carry, for messages.
func (j credentialJob) runGateText() string {
	if j.viaWorkflowRun() {
		return "github.event.workflow_run.event == 'pull_request'"
	}
	return "github.event_name == 'pull_request'"
}

// workflowRunTriggerFault says why a workflow_run comment job's workflow does
// not trigger on exactly one named producing workflow's completion, or "".
// The trigger is typed: workflow_run alone, one literal producer name, and
// types: [completed], with no other filter key. The workflow runs in the base
// repository with secrets, so a second event would be a second way in.
// pull_request_target is left out of the other-trigger list: checkCommentWorkflow
// reports it once, for every comment workflow, so the fault names it nowhere.
func workflowRunTriggerFault(doc *yaml.Node) string {
	var run trigger
	runs := 0
	var names []string
	for _, t := range workflowTriggers(doc) {
		switch {
		case t.Event == GitHubEventWorkflowRun:
			runs++
			run = t
		case t.Name != triggerPullRequestTarget:
			names = append(names, quote(t.Name))
		}
	}
	if len(names) > 0 {
		return "triggers on " + strings.Join(names, ", ") + " besides " + GitHubEventWorkflowRun.String() + "; a workflow_run comment job's workflow triggers on " + GitHubEventWorkflowRun.String() + " alone"
	}
	if runs != 1 {
		return "names " + GitHubEventWorkflowRun.String() + " " + strconv.Itoa(runs) + " times; a workflow_run comment job's workflow names it once"
	}
	if run.Config == nil {
		return GitHubEventWorkflowRun.String() + " names no " + triggerKeyWorkflows + " and no " + triggerKeyTypes
	}
	for i := 0; i+1 < len(run.Config.Content); i += 2 {
		if key := run.Config.Content[i].Value; key != triggerKeyWorkflows && key != triggerKeyTypes {
			return GitHubEventWorkflowRun.String() + " sets " + quote(key) + "; only " + triggerKeyWorkflows + " and " + triggerKeyTypes + " are accepted"
		}
	}
	producers := scalarList(mappingValue(run.Config, triggerKeyWorkflows))
	if len(producers) != 1 || producers[0] == "" || strings.ContainsAny(producers[0], "*!?[]${}") {
		return GitHubEventWorkflowRun.String() + " must name exactly one literal producing workflow in " + triggerKeyWorkflows
	}
	if types := scalarList(mappingValue(run.Config, triggerKeyTypes)); len(types) != 1 || types[0] != workflowRunCompleted {
		return GitHubEventWorkflowRun.String() + " must set " + triggerKeyTypes + ": [" + workflowRunCompleted + "], because only a completed run has its artifacts"
	}
	return ""
}

// workflowRunCompleted is the one activity type a workflow_run comment job
// runs on.
const workflowRunCompleted = "completed"

// scalarList reads a scalar or a sequence of scalars as a list of values;
// anything else reads as empty.
func scalarList(node *yaml.Node) []string {
	switch {
	case node == nil:
		return nil
	case node.Kind == yaml.ScalarNode:
		return []string{node.Value}
	case node.Kind == yaml.SequenceNode:
		var out []string
		for _, item := range node.Content {
			if item.Kind != yaml.ScalarNode {
				return nil
			}
			out = append(out, item.Value)
		}
		return out
	}
	return nil
}

// checkCommentWorkflow holds the workflow of a listed comment job to its
// triggers, once per file. pull_request_target is refused for both comment
// modes: it runs with secrets against the base repository on a PR's behalf,
// which is the shape the comment class exists to avoid. A workflow_run
// workflow additionally triggers on workflow_run alone and holds no job that
// is not a listed comment job, so no unreviewed job runs in the privileged
// context.
//
// The workflow_run scope limit is deliberate and covers every workflow that
// names the trigger, listed comment job or not: a workflow_run workflow holds
// only listed comment jobs. A credential-free probe job would need its own
// typed class, so none is admitted today.
func (c *queueChecker) checkCommentWorkflow(file string, doc *yaml.Node) {
	first := ""
	for _, jobName := range jobNames(doc) {
		if c.creds[file][jobName].class == credentialJobComment {
			first = jobName
			break
		}
	}
	runWorkflow := hasTrigger(doc, GitHubEventWorkflowRun)
	if first == "" && !runWorkflow {
		return
	}
	emit := func(job, what, why, fix string) {
		c.emit(QueueRuleCredentialTemplates, file, job, violation(what, why, rejected, fix))
	}
	if first != "" {
		for _, t := range workflowTriggers(doc) {
			if t.Name == triggerPullRequestTarget {
				emit(first, "the workflow of the listed comment job "+quote(first)+" triggers on "+triggerPullRequestTarget,
					triggerPullRequestTarget+" runs with secrets in the base repository on behalf of a pull request, and the comment class refuses it",
					"trigger on pull_request, or on workflow_run for a fork-capable comment")
			}
		}
	}
	if !runWorkflow {
		return
	}
	if first != "" {
		if why := workflowRunTriggerFault(doc); why != "" {
			emit(first, "the workflow of the listed comment job "+quote(first)+" "+why,
				"a workflow_run comment job runs in the base repository with secrets, so its trigger names one producing workflow's completion and nothing else",
				"trigger on `workflow_run: {workflows: [<producer>], types: [completed]}` alone")
		}
	}
	// The release job's own trigger rule (release-tag-checked-before-push)
	// already refuses workflow_run in its workflow; one finding is enough.
	for _, jobName := range jobNames(doc) {
		if c.creds[file][jobName].class == credentialJobRelease {
			return
		}
	}
	for _, jobName := range jobNames(doc) {
		if c.creds[file][jobName].class != credentialJobComment {
			emit(jobName, "jobs."+jobName+" is not a listed comment job, and its workflow triggers on "+GitHubEventWorkflowRun.String(),
				"every job of a workflow_run workflow runs in the base repository with secrets and the PR author's artifacts in reach, and the checker confines only the listed comment jobs",
				"move the job to a workflow with another trigger, or list it as a comment job")
		}
	}
}

// checkWorkflowRunComment applies the clauses only a workflow_run comment job
// has: a job-level gate on a pull_request producer, explicit read access to
// contents and actions, exactly one reviewed-script step, and none of the
// env names that stand for a PR number or an App token.
func (c *queueChecker) checkWorkflowRunComment(file, jobName string, doc, job *yaml.Node, cj credentialJob) {
	emit := func(what, why, fix string) {
		c.emit(QueueRuleCredentialTemplates, file, jobName, violation(what, why, rejected, fix))
	}
	if !cj.hasRunGate(valueOf(mappingValue(job, "if"))) {
		emit("jobs."+jobName+" has no job-level "+cj.runGateText()+" gate",
			"workflow_run fires for every completed run of the producing workflow, and only a pull_request run has the evidence artifact; a gate on a step would still run the checkout and download",
			"add `if: "+cj.runGateText()+"` (with any other conjuncts, and no disjunction) to the job")
	}
	perms := effectivePermissions(doc, job)
	for _, scope := range []string{"contents", "actions"} {
		if !permissionReads(perms, scope) {
			emit("jobs."+jobName+" does not grant "+scope+": read",
				"the default branch checkout needs contents: read and the cross-run artifact download needs actions: read, and the token stays read-only",
				"declare permissions: {contents: read, actions: read} on the job")
		}
	}
	scripts := 0
	refused := map[string]bool{}
	for _, step := range stepsOf(job) {
		if c.isTemplateStep(file, jobName, step, TemplateReviewedScript) {
			scripts++
		}
		for name := range effectiveEnv(doc, job, step) {
			for _, bad := range workflowRunCommentRefusedEnv {
				if name == bad.String() && !refused[name] {
					refused[name] = true
					emit("jobs."+jobName+" sets env "+name,
						"a workflow_run event has no pull request and the job mints no App token, so "+name+" could only carry a value from outside the reviewed script's evidence",
						"drop "+name+"; the script reads the PR number from the downloaded evidence")
				}
			}
		}
	}
	if scripts != 1 {
		emit("jobs."+jobName+" runs the reviewed script in "+strconv.Itoa(scripts)+" steps that receive the PAT",
			"a workflow_run comment job has one purpose, and the only template it may run is the policy's reviewed script, once, with the policy PAT in its step env",
			"run exactly `node "+reviewedCheckoutDir+"/<policy reviewed_script>` in one step whose env maps GH_TOKEN from the policy pat_secret")
	}
}

// permissionReads reports whether a permissions value explicitly grants read
// on the scope: the read-all shorthand, or the scope set to read.
func permissionReads(perms *yaml.Node, scope string) bool {
	switch {
	case perms == nil:
		return false
	case perms.Kind == yaml.ScalarNode:
		return perms.Value == PermissionReadAll.String()
	case perms.Kind == yaml.MappingNode:
		return valueOf(mappingValue(perms, scope)) == PermissionRead.String()
	}
	return false
}

// actionsFor derives each class's actions from the canonical action set.
func actionsFor(class credentialJobClass) []AllowedAction {
	if !slices.Contains(allCredentialJobClasses, class) {
		return nil
	}
	var out []AllowedAction
	for _, a := range AllAllowedActions {
		if a != ActionSetupGo || class == credentialJobPublish {
			out = append(out, a)
		}
	}
	return out
}

func mintInputsFor(class credentialJobClass) []ActionInput {
	// The comment class explicitly requests contents: read, never write. The
	// values are checked separately; omitting all permissions inherits the App.
	if !slices.Contains(allCredentialJobClasses, class) {
		return nil
	}
	return inputsOf(ActionCreateAppToken)
}

func (c *queueChecker) hasCommentPAT() bool {
	for _, comment := range c.policy.Queue.CommentJobs {
		if comment.PATSecret != "" {
			return true
		}
	}
	return false
}

// ambiguousCredentialYAML fails closed rather than guessing which duplicate
// value or alias expansion GitHub will use when a named personal token exists.
func ambiguousCredentialYAML(node *yaml.Node) string {
	if node == nil {
		return ""
	}
	if node.Kind == yaml.AliasNode {
		return "uses a YAML alias"
	}
	if node.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for i := 0; i+1 < len(node.Content); i += 2 {
			key := node.Content[i].Value
			if seen[key] {
				return "repeats YAML mapping key " + quote(key)
			}
			seen[key] = true
		}
	}
	for _, child := range node.Content {
		if why := ambiguousCredentialYAML(child); why != "" {
			return why
		}
	}
	return ""
}

func commentMintInputFault(with *yaml.Node) string {
	repos := valueOf(mappingValue(with, InputRepositories.String()))
	if !repositoryName.MatchString(repos) || strings.Contains(repos, "..") {
		return "passes repositories " + quote(repos) + "; a comment token requires exactly one literal repository"
	}
	for _, requirement := range []struct {
		input ActionInput
		level PermissionLevel
	}{
		{InputPermissionContents, PermissionRead},
		{InputPermissionIssues, PermissionWrite},
		{InputPermissionPullRequests, PermissionWrite},
	} {
		value := valueOf(mappingValue(with, requirement.input.String()))
		if value != requirement.level.String() {
			return "passes " + requirement.input.String() + ": " + quote(value) + "; the comment token requires explicit " + requirement.level.String()
		}
	}
	return ""
}

// patReads identifies named policy PATs or a whole-secrets read. It is used in
// addition to the general credential reader to confine this particular token.
func (c *queueChecker) patReads(node *yaml.Node) []string {
	var found []string
	for _, scalar := range nodeScalars(node) {
		for _, expr := range expressionBodies(scalar) {
			e := strings.ToLower(strings.Join(strings.Fields(expr), ""))
			for _, m := range contextRead.FindAllStringSubmatchIndex(e, -1) {
				if e[m[2]:m[3]] != "secrets" {
					continue
				}
				if m[4] < 0 {
					if !slices.Contains(found, "the whole secrets context") {
						found = append(found, "the whole secrets context")
					}
					continue
				}
				name := e[m[4]+1 : m[5]]
				for _, comment := range c.policy.Queue.CommentJobs {
					if comment.PATSecret != "" && strings.EqualFold(name, comment.PATSecret) && !slices.Contains(found, comment.PATSecret) {
						found = append(found, comment.PATSecret)
					}
				}
			}
		}
	}
	return found
}

// directoriesOverlap rejects either directory containing the other. The
// evidence artifact must not replace the reviewed script or its sibling
// imports, even when the artifact itself is controlled by a PR.
func directoriesOverlap(a, b string) bool {
	a, b = path.Clean(a), path.Clean(b)
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

func (c *queueChecker) reviewedScriptFault(doc, job, step *yaml.Node, cj credentialJob) string {
	if cj.reviewedScript == "" || cj.patSecret == "" {
		return "the policy must name reviewed_script and pat_secret for the reviewed script"
	}
	dir := effectiveEnv(doc, job, step)[TemplateEnvEvidenceDir.String()]
	if !safeRelativePath(dir) || directoriesOverlap(dir, reviewedCheckoutDir) {
		return "EVIDENCE_DIR must be a literal directory disjoint from the reviewed checkout"
	}
	foundScript, foundDownload, checkouts := false, false, 0
	for _, candidate := range stepsOf(job) {
		if candidate == step {
			foundScript = true
		}
		uses := stepUsesText(candidate)
		if isAction(uses, ActionCheckout) {
			checkouts++
			if foundScript || valueOf(mappingValue(mappingValue(candidate, "with"), InputRef.String())) == "" ||
				soleExpression(valueOf(mappingValue(mappingValue(candidate, "with"), InputRef.String()))) != defaultBranchExpression ||
				valueOf(mappingValue(mappingValue(candidate, "with"), InputArtifactPath.String())) != reviewedCheckoutDir ||
				mappingValue(candidate, "if") != nil || hasContinueOnError(candidate) {
				return "the reviewed script requires one unconditional default-branch checkout before it runs"
			}
		}
		if isAction(uses, ActionDownloadArtifact) {
			dest := valueOf(mappingValue(mappingValue(candidate, "with"), InputArtifactPath.String()))
			if !safeRelativePath(dest) || directoriesOverlap(dest, reviewedCheckoutDir) {
				return "an artifact download can overwrite the reviewed script or its imports"
			}
			runID := valueOf(mappingValue(mappingValue(candidate, "with"), InputRunID.String()))
			token := valueOf(mappingValue(mappingValue(candidate, "with"), InputGitHubToken.String()))
			fromTriggeringRun := !cj.viaWorkflowRun() ||
				(soleExpression(runID) == workflowRunIDExpression && soleExpression(token) == workflowRunTokenExpression)
			if !foundScript && fromTriggeringRun && path.Clean(dest) == path.Clean(dir) && mappingValue(candidate, "if") == nil && !hasContinueOnError(candidate) {
				foundDownload = true
			}
		}
	}
	if checkouts != 1 {
		return "the reviewed script requires exactly one default-branch checkout"
	}
	if !foundDownload {
		if cj.viaWorkflowRun() {
			return "the reviewed script requires an unconditional earlier artifact download into EVIDENCE_DIR from the triggering run (run-id: ${{ " + workflowRunIDExpression + " }} and github-token: ${{ " + workflowRunTokenExpression + " }}); a missing artifact fails the job closed"
		}
		return "the reviewed script requires an unconditional earlier artifact download into EVIDENCE_DIR"
	}
	if len(statusFunctionsIn(stepIfText(step))) != 0 {
		return "the reviewed script if must not bypass failed checkout or download steps"
	}
	return ""
}
