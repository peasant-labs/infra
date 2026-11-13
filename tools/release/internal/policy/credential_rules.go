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

// This file holds the four rules that confine every credential that can
// write a ref. They replace reading every spelling of a write with an
// allowlist, so anything they do not list is rejected:
//
//   - default-token-read-only: every workflow and every job declares its
//     permissions, every scope is read or none except an OIDC token and the
//     publish workflow's contents, and every checkout keeps no credential;
//   - credentials-stay-in-listed-jobs: the App token, the secrets that mint
//     it, and a default token that can write are read only by the release
//     job, the listed bot jobs and the publish jobs, and there only by the
//     mint step and the env of a run step;
//   - credential-jobs-use-pinned-actions: a job that may hold a credential
//     uses only the allowed actions, pinned to a commit;
//   - credential-steps-run-one-template: a step that receives a credential
//     runs exactly one command from the closed template set, built from the
//     release config and the job's own bot kind;
//   - credential-jobs-run-no-general-steps: a job that may hold a credential
//     runs only allowed actions, plumbing templates and credential
//     templates, and takes everything else from the jobs it needs, through
//     job outputs and artifacts.
//
// All real work (building the tool, planning, writing and committing the
// release content, making the tag object, judging the queue) runs in an
// earlier job with no credential. A step there may run any command: checkout
// keeps no credential and the default token cannot write, so it has nothing
// to write a ref with, and it shares no runner, environment or checkout with
// the job that holds the credential.

// violation renders the parts of a diagnostic message after the location
// and rule the emitter adds: what is wrong, when it was found, why it
// matters, what it means for the caller, and how to fix it.
func violation(what, why, means, fix string) string {
	return what + " during merge-queue policy validation. Why: " + why + ". Means: " + means + ". Fix: " + fix
}

// rejected is the consequence every credential rule shares.
const rejected = "check-workflows rejects the directory, so the release job stays red until the workflow is fixed"

// stepLabel names a step in a message by its id, or its position.
func stepLabel(job string, i int, step *yaml.Node) string {
	if id := stepID(step); id != "" {
		return "jobs." + job + " step " + strconv.Quote(id)
	}
	return "jobs." + job + " step " + strconv.Itoa(i+1)
}

// checkTokenReadOnly (default-token-read-only) holds the default workflow
// token to reads: every workflow and every job declares permissions, every
// scope is read or none except the two writable scopes under their
// conditions, and every actions/checkout sets persist-credentials: false so
// no later step finds a token in the git config.
func (c *queueChecker) checkTokenReadOnly() {
	c.eachDoc(QueueRuleTokenReadOnly, func(name string, doc *yaml.Node) {
		publish := c.isTagPublishWorkflow(doc)
		c.checkPermissions(name, "", "the workflow", mappingValue(doc, "permissions"), publish)
		for _, jobName := range jobNames(doc) {
			job := jobNode(doc, jobName)
			c.checkPermissions(name, jobName, "jobs."+jobName, mappingValue(job, "permissions"), publish)
			for i, step := range stepsOf(job) {
				if !isAction(stepUsesText(step), ActionCheckout) {
					continue
				}
				persist := mappingValue(mappingValue(step, "with"), "persist-credentials")
				if persist != nil && persist.Value == "false" {
					continue
				}
				c.emit(QueueRuleTokenReadOnly, name, jobName, violation(
					stepLabel(jobName, i, step)+" runs "+ActionCheckout.String()+" without persist-credentials: false",
					"checkout otherwise writes the token into the git config, where every later step of the job can push with it",
					rejected,
					"add with: persist-credentials: false to the checkout"))
			}
		}
	})
}

// checkPermissions applies the permission clauses to one workflow or job
// permissions value.
func (c *queueChecker) checkPermissions(file, job, scope string, perms *yaml.Node, publish bool) {
	fail := func(what, fix string) {
		c.emit(QueueRuleTokenReadOnly, file, job, violation(what,
			"the default token can write any ref its permissions allow, the default branch included, from any step of the job",
			rejected, fix))
	}
	want := "declare permissions: with every scope " + PermissionRead.String() + " or " + PermissionNone.String() + " (" + WritableScopeIDToken.String() + ": " + PermissionWrite.String() + " is allowed for OIDC publishing, and " + WritableScopeContents.String() + ": " + PermissionWrite.String() + " only in the workflow that runs on a pushed release tag)"
	if perms == nil {
		fail(scope+" declares no permissions, so the repository default applies, which can be write-all", want)
		return
	}
	switch perms.Kind {
	case yaml.ScalarNode:
		shorthand, err := ParsePermissionShorthand(perms.Value)
		switch {
		case err != nil:
			fail(scope+" sets permissions "+strconv.Quote(perms.Value)+", which is neither a scope mapping nor "+closedset.Join(AllPermissionShorthands, " nor "), want)
		case shorthand == PermissionWriteAll:
			fail(scope+" sets permissions "+PermissionWriteAll.String(), want)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(perms.Content); i += 2 {
			name, value := perms.Content[i].Value, perms.Content[i+1].Value
			level, err := ParsePermissionLevel(value)
			if err != nil {
				fail(scope+" sets permission "+name+": "+strconv.Quote(value)+", which is not one of "+closedset.Join(AllPermissionLevels, ", "), want)
				continue
			}
			if level != PermissionWrite {
				continue
			}
			writable, err := ParseWritableScope(name)
			switch {
			case err != nil:
				fail(scope+" grants "+name+": "+PermissionWrite.String(), want)
			case !writable.writeAllowed(publish):
				fail(scope+" grants "+name+": "+PermissionWrite.String()+" in a workflow that does not run only on a pushed release tag", want)
			}
		}
	default:
		fail(scope+" sets permissions to a value that is neither a scope mapping nor a shorthand", want)
	}
}

// checkCredentialScope (credentials-stay-in-listed-jobs) keeps every
// credential inside the jobs that may hold one, and inside those jobs in the
// two places a template can use it: the mint step's inputs and the env of a
// run step. It never reaches a workflow or job env (which every step
// inherits), a job output, a reusable workflow, an action's inputs, or a run
// block's text.
func (c *queueChecker) checkCredentialScope() {
	c.eachDoc(QueueRuleCredentialScope, func(name string, doc *yaml.Node) {
		workflowReader := credentialReader{secrets: c.secrets, job: credentialJob{tokenWrites: tokenMayWrite(mappingValue(doc, "permissions"))}}
		for _, kv := range envEntries(doc) {
			if cred := workflowReader.inText(kv[1]); cred != "" {
				c.emit(QueueRuleCredentialScope, name, "", violation(
					"the workflow env "+kv[0]+" reads "+cred,
					"a workflow env reaches every step of every job, so every command of the workflow holds the credential",
					rejected,
					"move the credential into the env of the one template step that uses it"))
			}
		}
		for _, jobName := range jobNames(doc) {
			c.checkJobCredentialScope(name, jobName, jobNode(doc, jobName))
		}
	})
}

// checkJobCredentialScope applies the scope clauses to one job.
func (c *queueChecker) checkJobCredentialScope(file, jobName string, job *yaml.Node) {
	cj := c.creds[file][jobName]
	r := credentialReader{secrets: c.secrets, job: cj}
	emit := func(what, why, fix string) {
		c.emit(QueueRuleCredentialScope, file, jobName, violation(what, why, rejected, fix))
	}
	if s := mappingValue(job, "secrets"); s != nil && s.Kind == yaml.ScalarNode && s.Value == "inherit" {
		emit("jobs."+jobName+" passes secrets: inherit",
			"it hands every secret, the ones that mint the App token included, to a workflow the checker cannot read",
			"pass the reusable workflow only the secrets it needs, by name")
	}
	for i := 0; i+1 < len(job.Content); i += 2 {
		key, val := job.Content[i].Value, job.Content[i+1]
		if key == "steps" {
			continue
		}
		cred := r.inNode(val)
		if cred == "" && key == "if" {
			cred = r.in(val.Value)
		}
		if cred == "" {
			continue
		}
		emit("jobs."+jobName+"."+key+" reads "+cred,
			"a job "+key+" hands the credential to every step, to a later job, or to a workflow the checker cannot read",
			"read the credential only in the env of the one template step that uses it")
	}
	for i, step := range stepsOf(job) {
		label := stepLabel(jobName, i, step)
		mint := isMintStep(step)
		if mint && cj.class != credentialJobRelease && cj.class != credentialJobBot {
			emit(label+" mints the App token outside the release job and the listed bot jobs",
				"a job the policy does not list runs commands no template holds, so it can push the default branch with the token",
				"mint the token only in the release job or a bot job the policy lists")
			continue
		}
		c.checkStepCredentialScope(r, cj, label, step, mint, emit)
	}
}

// checkStepCredentialScope applies the scope clauses to one step.
func (c *queueChecker) checkStepCredentialScope(r credentialReader, cj credentialJob, label string, step *yaml.Node, mint bool, emit func(what, why, fix string)) {
	isRun := stepRunText(step) != ""
	for i := 0; i+1 < len(step.Content); i += 2 {
		key, val := step.Content[i].Value, step.Content[i+1]
		cred := r.inNode(val)
		if cred == "" && key == "if" {
			cred = r.in(val.Value)
		}
		if cred == "" {
			continue
		}
		switch {
		case cj.class == "":
			emit(label+" reads "+cred+" in a job that may hold no credential",
				"only the release job, the listed bot jobs and the tag-triggered publish jobs run their credential steps as templates; any other job can push the default branch with it",
				"drop the credential, or move the step into a job the policy lists")
			return
		case mint && key == "with":
		case key == "env" && isRun:
			for _, kv := range envEntries(step) {
				if r.inText(kv[1]) == "" {
					continue
				}
				if env, err := ParseTemplateEnv(kv[0]); err != nil || !env.carriesCredential() {
					emit(label+" env "+kv[0]+" reads "+cred,
						"only a carrier name ("+TemplateEnvGHToken.String()+", "+TemplateEnvGitHubToken.String()+", "+TemplateEnvAppToken.String()+") may hold a credential, so a template can never print it or write it to a file",
						"carry the credential in "+TemplateEnvGHToken.String()+" for gh or "+TemplateEnvAppToken.String()+" for the git authorization header")
				}
			}
		default:
			emit(label+" reads "+cred+" in "+key,
				"a credential reaches only the mint step's inputs and the env of a run step that runs one template; anywhere else it can be written to GITHUB_ENV, GITHUB_OUTPUT, GITHUB_PATH or a file, or handed to code the checker cannot read",
				"map the credential into the step env as "+TemplateEnvGHToken.String()+" or "+TemplateEnvAppToken.String()+" and run one template")
		}
	}
}

// checkCredentialActions (credential-jobs-use-pinned-actions) holds every
// job that may hold a credential to the allowed actions, each pinned to a
// 40-hex commit, and refuses a reusable workflow or a container there: each
// runs code the checker cannot read while the credential is in reach.
func (c *queueChecker) checkCredentialActions() {
	c.eachDoc(QueueRuleCredentialActions, func(name string, doc *yaml.Node) {
		for _, jobName := range jobNames(doc) {
			cj := c.creds[name][jobName]
			if cj.class == "" {
				continue
			}
			job := jobNode(doc, jobName)
			emit := func(what, fix string) {
				c.emit(QueueRuleCredentialActions, name, jobName, violation(what,
					"the "+cj.class.String()+" may hold a credential, and an action, a reusable workflow or a container runs code the checker cannot read with that credential in reach",
					rejected, fix))
			}
			allowed := "use only " + closedset.Join(AllAllowedActions, ", ") + ", each pinned to a 40-hex commit"
			if uses := mappingValue(job, "uses"); uses != nil {
				emit("jobs."+jobName+" calls the reusable workflow "+strconv.Quote(uses.Value), "run the job's steps inline and "+allowed)
			}
			if mappingValue(job, "container") != nil {
				emit("jobs."+jobName+" runs in a container", "run the job on the runner, so every template runs the runner's git and gh")
			}
			for i, step := range stepsOf(job) {
				uses := stepUsesText(step)
				if uses == "" {
					continue
				}
				action, ref, ok := usesAction(uses)
				if _, err := ParseAllowedAction(action); !ok || err != nil {
					emit(stepLabel(jobName, i, step)+" uses "+strconv.Quote(uses)+", which is not an allowed action", allowed)
					continue
				}
				if !actionPinSHA.MatchString(ref) {
					emit(stepLabel(jobName, i, step)+" uses "+strconv.Quote(uses)+", pinned to "+strconv.Quote(ref)+" instead of a 40-hex commit", "pin the action to the reviewed commit SHA")
				}
				if why := actionInputFault(step); why != "" {
					emit(stepLabel(jobName, i, step)+" "+why, "pass "+uses+" only the inputs it may take there")
				}
			}
		}
	})
}

// actionInputFault says why an allowed action's with: inputs are not ones
// it may take in a job that may hold a credential, or "" when they are.
// Checkout and download inputs are literal: a value from an expression could
// point them at another ref or directory. A download lands in a literal
// subdirectory of the workspace, so an artifact can never write into .git
// (its config or hooks) or beside the repository's files.
func actionInputFault(step *yaml.Node) string {
	name, _, _ := usesAction(stepUsesText(step))
	action, err := ParseAllowedAction(name)
	if err != nil {
		return ""
	}
	allowed := inputsOf(action)
	with := mappingValue(step, "with")
	for i := 0; with != nil && i+1 < len(with.Content); i += 2 {
		key, value := with.Content[i].Value, with.Content[i+1].Value
		input, err := ParseActionInput(key)
		if err != nil || !containsInput(allowed, input) {
			return "passes input " + key + ", which " + action.String() + " may not take in a job that may hold a credential (it takes " + closedset.Join(allowed, ", ") + ")"
		}
		if action != ActionCreateAppToken && strings.Contains(value, "${{") {
			return "passes input " + key + " from an expression; " + action.String() + " takes literal inputs there"
		}
	}
	if action == ActionDownloadArtifact {
		path := mappingValue(with, InputArtifactPath.String())
		if path == nil || !pathWord.MatchString(path.Value) || strings.Contains(path.Value, "..") {
			return "downloads into " + strconv.Quote(valueOf(path)) + " instead of a literal subdirectory of the workspace, so an artifact could write into .git or over the checkout"
		}
	}
	return ""
}

func containsInput(set []ActionInput, input ActionInput) bool {
	for _, member := range set {
		if member == input {
			return true
		}
	}
	return false
}

// valueOf returns a scalar node's value, or "" for a missing node.
func valueOf(n *yaml.Node) string {
	if n == nil {
		return ""
	}
	return n.Value
}

// checkCredentialJobShape (credential-jobs-run-no-general-steps) holds a job
// that may hold a credential to three kinds of step: an allowed action, a
// plumbing template, and a credential template, on one GitHub-hosted runner
// label written literally. Every other piece of work
// runs in an earlier job that holds no credential, so nothing in the
// credential job can write GITHUB_ENV or GITHUB_PATH, the git config or
// hooks, or the actions the runner already downloaded, before the step that
// holds the credential. The earlier job hands over only through job outputs
// and artifacts: every needs.<job> the job reads names a job it needs, every
// artifact it downloads is uploaded by a job it needs, and every step's
// environment holds only template env names. The job runs no service
// container and sets no working directory, so every command runs in the
// fresh checkout.
func (c *queueChecker) checkCredentialJobShape() {
	c.eachDoc(QueueRuleCredentialJobShape, func(name string, doc *yaml.Node) {
		for _, jobName := range jobNames(doc) {
			cj := c.creds[name][jobName]
			if cj.class == "" {
				continue
			}
			job := jobNode(doc, jobName)
			emit := func(what, fix string) {
				c.emit(QueueRuleCredentialJobShape, name, jobName, violation(what,
					"a "+cj.class.String()+" holds a credential, and any general step before the credential step can leave behind what makes the one template push the default branch or leak the token: an env or path file entry, a git config or hook, a rewritten downloaded action",
					rejected, fix))
			}
			c.checkJobShape(name, jobName, doc, job, cj, emit)
		}
	})
}

// checkJobShape applies the shape clauses to one credential job.
func (c *queueChecker) checkJobShape(file, jobName string, doc, job *yaml.Node, cj credentialJob, emit func(what, fix string)) {
	moveWork := "do this work in an earlier job without the credential, hand the result over as a job output or an artifact, and list that job in needs"
	if why := runnerFault(mappingValue(job, "runs-on")); why != "" {
		emit("jobs."+jobName+" "+why, "run the job on one GitHub-hosted label ("+closedset.Join(AllRunnerLabels, ", ")+"), written literally; keep the runner router for jobs without a credential")
	}
	if mappingValue(job, "services") != nil {
		emit("jobs."+jobName+" runs service containers", "drop the services; a service container can mount the runner's work directory")
	}
	if mappingValue(mappingValue(mappingValue(job, "defaults"), "run"), "working-directory") != nil {
		emit("jobs."+jobName+" sets defaults.run.working-directory", "drop it, so every command runs in the fresh checkout and never in a downloaded directory")
	}
	needs, _ := needsList(job)
	for _, ref := range needsReads(job) {
		if !slices.Contains(needs, ref) {
			emit("jobs."+jobName+" reads needs."+ref+" but does not need job "+strconv.Quote(ref), "list "+ref+" in the job's needs, so its outputs are the ones this run produced")
		}
	}
	uploads := uploadedArtifacts(doc, needs)
	for i, step := range stepsOf(job) {
		label := stepLabel(jobName, i, step)
		if mappingValue(step, "working-directory") != nil {
			emit(label+" sets working-directory", "drop it, so the command runs in the fresh checkout and never in a downloaded directory")
		}
		if uses := stepUsesText(step); uses != "" {
			if isAction(uses, ActionDownloadArtifact) {
				artifact := valueOf(mappingValue(mappingValue(step, "with"), InputArtifactName.String()))
				if artifact == "" || !uploads[artifact] {
					emit(label+" downloads artifact "+strconv.Quote(artifact)+", which no job it needs uploads", "name the artifact an earlier job uploads, and list that job in needs")
				}
			}
			if bad := badEnvName(effectiveEnv(doc, job, step)); bad != "" {
				emit(label+" runs with env "+bad+", which is not one of "+closedset.Join(AllTemplateEnvs, ", "), "drop it from the step, job and workflow env; an action reads its environment too (NODE_OPTIONS, a proxy)")
			}
			continue
		}
		if c.receivesCredential(file, jobName, step) {
			continue
		}
		if why := c.plumbingFault(doc, job, step, cj); why != "" {
			emit(label+" "+why, moveWork+"; this job runs only "+closedset.Join(AllAllowedActions, ", ")+", the plumbing templates ("+plumbingShapes(cj.class)+") and the credential templates")
		}
	}
}

// runnerFault says why a credential job's runs-on is not one literal
// GitHub-hosted label, or "" when it is. An expression lets the job without
// a credential pick the machine; self-hosted, a group or a label list can
// name a machine that keeps state between jobs.
func runnerFault(runsOn *yaml.Node) string {
	switch {
	case runsOn == nil:
		return "names no runs-on"
	case runsOn.Kind != yaml.ScalarNode:
		return "runs on a label list or a group mapping, which can select a self-hosted machine"
	case strings.Contains(runsOn.Value, "${{"):
		return "runs on " + strconv.Quote(runsOn.Value) + ", an expression, so an earlier job or an input picks the machine that holds the credential"
	}
	if _, err := ParseRunnerLabel(runsOn.Value); err != nil {
		return "runs on " + strconv.Quote(runsOn.Value) + ", which is not a GitHub-hosted label, so the machine may keep state between jobs"
	}
	return ""
}

// plumbingFault says why a no-credential run step of a credential job is
// not exactly one plumbing template its class may run, or "" when it is.
func (c *queueChecker) plumbingFault(doc, job, step *yaml.Node, cj credentialJob) string {
	if shell := effectiveShell(step, job, doc); shell != "" && shell != shellBash {
		return "runs under shell " + strconv.Quote(shell) + " instead of the runner's default bash"
	}
	env := effectiveEnv(doc, job, step)
	if bad := badEnvName(env); bad != "" {
		return "runs with env " + bad + ", which is not one of " + closedset.Join(AllTemplateEnvs, ", ") + " and can change what the command does"
	}
	words, why := tokenizeCommand(stepRunText(step))
	if why != "" {
		return "is a general step: its run block is not one command: " + why
	}
	if _, why := matchPlumbing(words, templateStep{class: cj.class, branch: cj.branch, env: env, doc: doc, job: job}); why != "" {
		return "is a general step: " + why
	}
	return ""
}

// badEnvName returns the first env name that is not a template env name, or
// "".
func badEnvName(env map[string]string) string {
	for _, name := range sortedKeys(env) {
		if _, err := ParseTemplateEnv(name); err != nil {
			return name
		}
	}
	return ""
}

// plumbingShapes lists a job class's plumbing templates for a message.
func plumbingShapes(class credentialJobClass) string {
	var out []string
	for _, t := range plumbingFor(class) {
		out = append(out, t.String()+" `"+t.Shape()+"`")
	}
	if len(out) == 0 {
		return "none for this job"
	}
	return strings.Join(out, "; ")
}

// needsJobRead matches a read of one needed job's outputs or result and
// captures the job id.
var needsJobRead = regexp.MustCompile(`\bneeds\.(` + stepIDPattern + `)\.`)

// needsReads returns every job id the job reads through the needs context,
// in order of first appearance.
func needsReads(job *yaml.Node) []string {
	var out []string
	for _, scalar := range nodeScalars(job) {
		for _, m := range needsJobRead.FindAllStringSubmatch(scalar, -1) {
			if !slices.Contains(out, m[1]) {
				out = append(out, m[1])
			}
		}
	}
	return out
}

// uploadedArtifacts returns the artifact names the given jobs upload with a
// literal name.
func uploadedArtifacts(doc *yaml.Node, jobs []string) map[string]bool {
	out := map[string]bool{}
	for _, name := range jobs {
		for _, step := range stepsOf(jobNode(doc, name)) {
			if !isUploadArtifact(stepUsesText(step)) {
				continue
			}
			if artifact := valueOf(mappingValue(mappingValue(step, "with"), InputArtifactName.String())); artifact != "" && !strings.Contains(artifact, "${{") {
				out[artifact] = true
			}
		}
	}
	return out
}

// uploadArtifactAction is the action an earlier job hands files over with.
// It is not an allowed action in a credential job; it only names where an
// artifact comes from.
const uploadArtifactAction = "actions/upload-artifact"

func isUploadArtifact(uses string) bool {
	name, _, ok := usesAction(uses)
	return ok && name == uploadArtifactAction
}

// checkCredentialTemplates (credential-steps-run-one-template) holds every
// step that receives a credential to exactly one command from the closed
// template set its job class may run, with the job's own bot branch, an
// environment of template names only, and the runner's default shell.
func (c *queueChecker) checkCredentialTemplates() {
	c.eachDoc(QueueRuleCredentialTemplates, func(name string, doc *yaml.Node) {
		for _, jobName := range jobNames(doc) {
			cj := c.creds[name][jobName]
			if cj.class == "" {
				continue
			}
			job := jobNode(doc, jobName)
			for i, step := range stepsOf(job) {
				if stepRunText(step) == "" || !c.receivesCredential(name, jobName, step) {
					continue
				}
				if why := c.templateFault(doc, job, step, cj); why != "" {
					c.emit(QueueRuleCredentialTemplates, name, jobName, violation(
						stepLabel(jobName, i, step)+" receives a credential but "+why,
						"a step that holds a credential runs exactly one command the checker can read, so it can write only the job's own bot branch, the planner's tag, its own PR or the triggering tag's Release",
						rejected,
						"run one of the templates a "+cj.class.String()+" may run: "+templateShapes(cj.class)+"; do any other work in a step without the credential"))
				}
			}
		}
	})
}

// templateShapes lists a job class's templates for a message.
func templateShapes(class credentialJobClass) string {
	var out []string
	for _, t := range templatesFor(class) {
		out = append(out, t.String()+" `"+t.Shape()+"`")
	}
	return strings.Join(out, "; ")
}

// templateFault says why a credential step is not exactly one template its
// job may run, or "" when it is.
func (c *queueChecker) templateFault(doc, job, step *yaml.Node, cj credentialJob) string {
	if shell := effectiveShell(step, job, doc); shell != "" && shell != shellBash {
		return "runs under shell " + strconv.Quote(shell) + " instead of the runner's default bash"
	}
	env := effectiveEnv(doc, job, step)
	for _, name := range sortedKeys(env) {
		if _, err := ParseTemplateEnv(name); err != nil {
			return "its env sets " + name + ", which is not one of " + closedset.Join(AllTemplateEnvs, ", ") + " and can change what the command does"
		}
	}
	words, why := tokenizeCommand(stepRunText(step))
	if why != "" {
		return "its run block is not one command: " + why
	}
	tmpl, why := matchTemplate(words, templateStep{class: cj.class, branch: cj.branch, env: env, doc: doc, job: job})
	if why != "" {
		return why
	}
	if tmpl == TemplatePRMerge && cj.class == credentialJobRelease && !isArmHoldStep(doc, job, step) {
		return "merges a PR outside an arm or hold step; only the steps gated on the " + intent.CommandQueueGate.String() + " output arm or hold the release PR"
	}
	return ""
}

// shellBash is the one shell a credential step may name: the runner's
// default.
const shellBash = "bash"

// effectiveShell returns the shell a run step uses: its own shell, or the
// job's or the workflow's defaults.run.shell, or "" for the runner default.
func effectiveShell(step, job, doc *yaml.Node) string {
	if s := mappingValue(step, "shell"); s != nil {
		return s.Value
	}
	for _, scope := range []*yaml.Node{job, doc} {
		if s := mappingValue(mappingValue(mappingValue(scope, "defaults"), "run"), "shell"); s != nil {
			return s.Value
		}
	}
	return ""
}

// reader returns the credential reader of one job.
func (c *queueChecker) reader(file, job string) credentialReader {
	return credentialReader{secrets: c.secrets, job: c.creds[file][job]}
}

// receivesCredential reports whether a step can reach a credential: through
// its own, its job's or its workflow's env, or anywhere in the step.
func (c *queueChecker) receivesCredential(file, jobName string, step *yaml.Node) bool {
	f := c.files[file]
	if f == nil || f.doc == nil {
		return false
	}
	r := c.reader(file, jobName)
	for _, v := range effectiveEnv(f.doc, jobNode(f.doc, jobName), step) {
		if r.inText(v) != "" {
			return true
		}
	}
	return r.inNode(step) != "" || r.in(stepIfText(step)) != ""
}

// isWriteStep reports whether a step can write: it mints the App token or
// it receives a credential. A step without a credential has nothing to
// write with.
func (c *queueChecker) isWriteStep(file, jobName string, step *yaml.Node) bool {
	return isMintStep(step) || c.receivesCredential(file, jobName, step)
}

// isTemplateStep reports whether a step receives a credential and its
// command selects one of the given templates.
func (c *queueChecker) isTemplateStep(file, jobName string, step *yaml.Node, templates ...CredentialTemplate) bool {
	if !c.receivesCredential(file, jobName, step) {
		return false
	}
	tmpl, ok := stepTemplate(step)
	return ok && containsTemplate(templates, tmpl)
}

func containsTemplate(set []CredentialTemplate, t CredentialTemplate) bool {
	for _, member := range set {
		if member == t {
			return true
		}
	}
	return false
}

// allowedPush is one policy job that may hold the App token: its class and
// the bot branch it owns, resolved from the release config's bot_branches,
// the one source. branch is empty when the config names none for its kind.
type allowedPush struct {
	workflow, job, branch string
	class                 credentialJobClass
}

// resolvePushes maps every policy job to its bot branch. A policy kind the
// config does not name, or a kind two jobs claim, is a policy-level
// diagnostic under the caller's rule: nothing may push for a branch that
// does not exist, and two writers of one branch race. Such a job stays
// listed, so the credential rules still hold it to its templates, and with
// no branch none of its pushes matches.
func (c *queueChecker) resolvePushes(rule QueueRule) []allowedPush {
	var allowed []allowedPush
	seen := map[intent.PRKind]string{}
	if rel := c.policy.Queue.ReleaseJob; rel != nil {
		branch, ok := c.cfg.BotBranches[intent.PRKindRelease]
		if !ok {
			c.emit(rule, "", rel.Job, violation(
				"the policy names a release job, but the release config names no branch for the "+intent.PRKindRelease.String()+" kind",
				"the release job's push template takes its own bot branch from the release config",
				rejected,
				"set bot_branches "+intent.PRKindRelease.String()+" in "+intent.DefaultConfigPath))
		}
		allowed = append(allowed, allowedPush{rel.Workflow, rel.Job, branch, credentialJobRelease})
	}
	for _, bot := range c.policy.Queue.BotJobs {
		branch, ok := c.cfg.BotBranches[bot.Kind]
		if !ok {
			c.emit(rule, "", bot.Job, violation(
				"the policy names bot kind "+strconv.Quote(bot.Kind.String())+" for job "+strconv.Quote(bot.Job)+", but the release config names no branch for that kind",
				"a bot job's push template takes its own bot branch from the release config",
				rejected,
				"add the bot_branches entry for the kind in "+intent.DefaultConfigPath))
		} else if owner, dup := seen[bot.Kind]; dup {
			c.emit(rule, "", bot.Job, violation(
				"bot kind "+strconv.Quote(bot.Kind.String())+" is written by "+owner+" and by job "+strconv.Quote(bot.Job),
				"two jobs pushing one bot branch race and overwrite each other, so the policy gives each bot kind exactly one writer",
				rejected,
				"give each bot kind exactly one writer job"))
		}
		seen[bot.Kind] = "job " + strconv.Quote(bot.Job)
		allowed = append(allowed, allowedPush{bot.Workflow, bot.Job, branch, credentialJobBot})
	}
	return allowed
}
