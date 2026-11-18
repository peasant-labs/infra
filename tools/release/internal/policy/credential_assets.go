package policy

import (
	"slices"
	"strings"

	regexp "github.com/wasilibs/go-re2"
	"gopkg.in/yaml.v3"
)

// The release artifact is the handoff between the credential-free build and
// the publish job. These names are fixed so the action cannot upload another
// artifact or a checkout's files.
const (
	releaseArtifactName = "release-dist"
	releaseArtifactJob  = "build"
	releaseArtifactDir  = "dist"
	releaseAssetFiles   = "dist/*"
)

var (
	releaseAssetGate      = regexp.MustCompile(`^steps\.(` + stepIDPattern + `)\.outcome\s*==\s*'success'\s*\|\|\s*steps\.(` + stepIDPattern + `)\.outcome\s*==\s*'success'$`)
	draftReleaseAssetGate = regexp.MustCompile(`^steps\.(` + stepIDPattern + `)\.outcome\s*==\s*'failure'\s*&&\s*steps\.(` + stepIDPattern + `)\.outcome\s*==\s*'success'$`)
	stepOutcomeGate       = regexp.MustCompile(`^steps\.(` + stepIDPattern + `)\.outcome\s*==\s*'(success|failure)'$`)
	releaseOutputCompare  = regexp.MustCompile(`^(needs|steps)\.(` + stepIDPattern + `)\.outputs\.([A-Za-z0-9_-]+)\s*==\s*'([^']*)'$`)
)

// assetGate binds the two referenced steps. The older success-or-success
// shape permits either ordering; the draft shape always names view, then create.
type assetGate struct {
	first, second string
	draft         bool
}

func parseReleaseAssetGate(condition string) (assetGate, bool) {
	condition = trimOuterParens(stripIfExpression(condition))
	if m := draftReleaseAssetGate.FindStringSubmatch(condition); m != nil && m[1] != m[2] {
		return assetGate{first: m[1], second: m[2], draft: true}, true
	}
	if m := releaseAssetGate.FindStringSubmatch(condition); m != nil && m[1] != m[2] {
		return assetGate{first: m[1], second: m[2]}, true
	}
	return assetGate{}, false
}

func releaseAssetsInputFault(action, job *yaml.Node) string {
	with := mappingValue(action, "with")
	if got := valueOf(mappingValue(with, InputFiles.String())); got != releaseAssetFiles {
		return "passes files " + quote(got) + "; the release-assets action requires the literal " + releaseAssetFiles
	}
	for _, input := range []ActionInput{InputFailOnUnmatched, InputOverwriteFiles} {
		if got := valueOf(mappingValue(with, input.String())); got != "true" {
			return "passes " + input.String() + ": " + quote(got) + "; it must be true"
		}
	}
	if got := soleExpression(valueOf(mappingValue(with, InputTagName.String()))); got != triggeringRefName {
		return "tag_name must be the triggering tag from github.ref_name"
	}
	if got := soleExpression(valueOf(mappingValue(with, InputRepository.String()))); got != "github.repository" {
		return "repository must be the current github.repository"
	}
	needs, _ := needsList(job)
	gate, isGate := parseReleaseAssetGate(stepIfText(action))
	draft := mappingValue(with, InputDraft.String())
	if isGate {
		if gate.draft && !literalActionBoolean(draft, "true") {
			return "draft must be literal true for the view.failure && create.success draft gate"
		}
		if !gate.draft && draft != nil && !literalActionBoolean(draft, "false") {
			return "draft must be absent or literal false for the view.success || create.success published gate"
		}
	}
	for _, input := range []ActionInput{InputPrerelease, InputMakeLatest} {
		node := mappingValue(with, input.String())
		if input == InputMakeLatest && node == nil && isGate && gate.draft {
			continue
		}
		expr := soleExpression(valueOf(node))
		m := outputRead.FindStringSubmatch(expr)
		if m == nil || m[1] != "needs" || !slices.Contains(needs, m[2]) {
			return input.String() + " must read one output of a job in needs; make_latest may be omitted only for the view.failure && create.success draft gate"
		}
	}
	return ""
}

// Action inputs are strings at runtime. Accept either a YAML boolean or the
// equivalent literal string, but never an expression, alias, or collection.
func literalActionBoolean(node *yaml.Node, value string) bool {
	return node != nil && node.Kind == yaml.ScalarNode &&
		(node.Tag == "!!bool" || node.Tag == "!!str") && node.Value == value
}

func (c *queueChecker) releaseAssetsFault(file, jobName string, doc, job, action *yaml.Node) string {
	if hasContinueOnError(job) || hasContinueOnError(action) {
		return "sets continue-on-error on the publish job or assets action, which could hide an incomplete upload"
	}
	needs, _ := needsList(job)
	build := jobNode(doc, releaseArtifactJob)
	if !slices.Contains(needs, releaseArtifactJob) || build == nil || publishes(doc, build) ||
		c.creds[file][releaseArtifactJob].class != "" || !uploadedArtifacts(doc, []string{releaseArtifactJob})[releaseArtifactName] {
		return "does not need a credential-free build job that uploads release-dist"
	}
	uploads := 0
	for _, producer := range jobNames(doc) {
		for _, step := range stepsOf(jobNode(doc, producer)) {
			if !isUploadArtifact(stepUsesText(step)) {
				continue
			}
			with := mappingValue(step, "with")
			name := valueOf(mappingValue(with, InputArtifactName.String()))
			if strings.Contains(name, "${{") {
				return "cannot prove release-dist provenance while an artifact name is an expression"
			}
			if name != releaseArtifactName {
				continue
			}
			uploads++
			if producer != releaseArtifactJob || valueOf(mappingValue(with, InputArtifactPath.String())) != releaseArtifactDir ||
				mappingValue(step, "if") != nil || hasContinueOnError(step) {
				return "requires release-dist to be uploaded unconditionally from build's dist directory"
			}
		}
	}
	if uploads != 1 {
		return "requires exactly one release-dist upload from build"
	}
	beforeAction, downloads, foundDownload := true, 0, false
	preceding := map[string]*yaml.Node{}
	positions := map[string]int{}
	for index, step := range stepsOf(job) {
		if step == action {
			beforeAction = false
		}
		if isAction(stepUsesText(step), ActionDownloadArtifact) {
			downloads++
			with := mappingValue(step, "with")
			if beforeAction && valueOf(mappingValue(with, InputArtifactName.String())) == releaseArtifactName &&
				valueOf(mappingValue(with, InputArtifactPath.String())) == releaseArtifactDir &&
				mappingValue(step, "if") == nil && !hasContinueOnError(step) {
				foundDownload = true
			}
		}
		if beforeAction && stepID(step) != "" {
			preceding[stepID(step)] = step
			positions[stepID(step)] = index
		}
		if beforeAction && foundDownload && isAction(stepUsesText(step), ActionCheckout) {
			return "checks out the repository after downloading release-dist into dist"
		}
	}
	if downloads != 1 || !foundDownload {
		return "must unconditionally download only release-dist into dist before uploading"
	}
	gate, ok := parseReleaseAssetGate(stepIfText(action))
	if !ok {
		return "must gate the action on view.success || create.success or view.failure && create.success, using earlier release-view and release-create steps"
	}
	if gate.draft && preceding[gate.first] != nil && preceding[gate.second] != nil && positions[gate.first] >= positions[gate.second] {
		return "requires the release-view step before the draft create step"
	}
	cj := c.creds[file][jobName]
	foundView, foundCreate := false, false
	for i, id := range []string{gate.first, gate.second} {
		step := preceding[id]
		if step == nil || !c.receivesCredential(file, jobName, step) {
			return "gates on a missing or non-credential release step"
		}
		words, why := singleCommand(doc, job, step)
		if why != "" {
			return "gates on a step that is not one release template"
		}
		env := effectiveEnv(doc, job, step)
		reader := templateStep{class: cj.class, env: env, doc: doc, job: job}
		tmpl, why := matchTemplate(words, reader)
		if why != "" {
			return "gates on an invalid release template: " + why
		}
		spec := releaseFlags()
		spec[ghFlagVerifyTag] = flagSwitch
		flags, why := reader.parseFlags(words[3:], spec)
		if why != "" {
			return "gates on an invalid release command: " + why
		}
		if !namesCurrentReleaseRepository(flags, env) {
			return "gates on a release command that does not name the current repository"
		}
		if gate.draft {
			if (i == 0 && tmpl != TemplateReleaseView) || (i == 1 && tmpl != TemplateReleaseCreate) {
				return "must gate the draft action on view.failure && create.success, in that order"
			}
			if i == 0 && (mappingValue(step, "if") != nil || valueOf(mappingValue(step, "continue-on-error")) != "true") {
				return "requires the release-view step to run unconditionally with continue-on-error: true"
			}
			if i == 1 {
				draft, present := flags.flags[ghFlagDraft]
				lit, literal := draft.literal()
				if !present || !literal || (lit != ghFlagDraft && lit != ghFlagDraft+"=true") {
					return "requires the release-create step to create a draft with --draft or --draft=true"
				}
				if !matchesStepOutcome(stepIfText(step), gate.first, "failure") || hasContinueOnError(step) {
					return "the draft create step must run only on view.failure and must not set continue-on-error"
				}
			}
		}
		foundView = foundView || tmpl == TemplateReleaseView
		foundCreate = foundCreate || tmpl == TemplateReleaseCreate
	}
	if !foundView || !foundCreate {
		return "must gate on one release-view and one release-create --verify-tag step"
	}
	if gate.draft {
		return c.draftPublishFault(file, jobName, doc, job, action, gate)
	}
	return ""
}

// namesCurrentReleaseRepository requires exactly one repo flag and the
// runner's unmodified repository value, for an asset gate or release-state view.
func namesCurrentReleaseRepository(flags parsedFlags, env map[string]string) bool {
	repo, long := flags.flags["--repo"]
	shortRepo, short := flags.flags["-R"]
	if short {
		repo = shortRepo
	}
	_, overridden := env[runnerEnvRepository]
	return long != short && repo.params("", runnerEnvRepository, "") && !overridden
}

func matchesStepOutcome(condition, id, outcome string) bool {
	condition = trimOuterParens(stripIfExpression(condition))
	m := stepOutcomeGate.FindStringSubmatch(condition)
	return m != nil && m[1] == id && m[2] == outcome
}

// draftPublishFault binds the final write to the same successful creation and
// leaves the default success() guard intact, so a failed upload cannot publish.
func (c *queueChecker) draftPublishFault(file, jobName string, doc, job, action *yaml.Node, gate assetGate) string {
	prerelease := soleExpression(valueOf(mappingValue(mappingValue(action, "with"), InputPrerelease.String())))
	source := outputRead.FindStringSubmatch(prerelease)
	if source == nil || source[1] != "needs" || source[3] != "prerelease" {
		return "the draft action prerelease must read a needed job's prerelease output"
	}
	producer := jobNode(doc, source[2])
	outputs := mappingValue(producer, "outputs")
	if soleExpression(valueOf(mappingValue(outputs, "prerelease"))) == "" {
		return "the draft action prerelease output must be declared by the needed job"
	}
	afterAction, edits := false, 0
	for _, step := range stepsOf(job) {
		if step == action {
			afterAction = true
			continue
		}
		if isAction(stepUsesText(step), ActionReleaseAssets) {
			return "requires the draft assets action to be the only release-assets action"
		}
		tmpl, ok := stepTemplate(step)
		if !ok {
			continue
		}
		if tmpl == TemplateReleaseCreate && stepID(step) != gate.second {
			return "requires the gated draft create to be the only release-create step"
		}
		if tmpl != TemplateReleaseEdit {
			continue
		}
		edits++
		if !afterAction {
			return "requires the publish edit after the assets action"
		}
		condition, dual := parseReleaseAssetGate(stepIfText(step))
		if !matchesStepOutcome(stepIfText(step), gate.second, "success") &&
			(!dual || !condition.draft || condition.first != gate.first || condition.second != gate.second) {
			return "the publish edit must be gated on create.success, optionally with view.failure, without a status function"
		}
		if hasContinueOnError(step) || !c.receivesCredential(file, jobName, step) {
			return "the publish edit must receive the credential and must not set continue-on-error"
		}
		words, why := singleCommand(doc, job, step)
		if why != "" {
			return "the publish edit must run one release-edit template"
		}
		reader := templateStep{class: c.creds[file][jobName].class, env: effectiveEnv(doc, job, step), doc: doc, job: job}
		if got, why := matchTemplate(words, reader); why != "" || got != TemplateReleaseEdit {
			return "the publish edit must run a valid release-edit template for the triggering tag"
		}
		flags, _ := reader.parseFlags(words[3:], releaseFlags())
		if !namesCurrentReleaseRepository(flags, reader.env) {
			return "the publish edit must name the current repository with exactly one --repo or -R flag"
		}
		draft, present := flags.flags[ghFlagDraft]
		lit, literal := draft.literal()
		if !present || !literal || lit != ghFlagDraft+"=false" {
			return "the publish edit must pass --draft=false"
		}
		for _, name := range []string{"prerelease", "latest"} {
			flag := "--" + name
			word, present := flags.flags[flag]
			if !present || !publishFlagReadsOutput(word, flag, reader.env, source[2], name, outputs) {
				return "the publish edit " + flag + " must read the needed release-kind job's " + name + " output"
			}
		}
	}
	if edits != 1 {
		return "requires exactly one publish edit after the assets action"
	}
	return ""
}

// publishFlagReadsOutput accepts the named output directly, or a comparison
// of another output that matches the producer's own declared comparison.
// The latter admits a consumer comparing guard.kind only when that is exactly
// how the guard declared its corresponding prerelease/latest output.
func publishFlagReadsOutput(word templateWord, flag string, env map[string]string, job, output string, outputs *yaml.Node) bool {
	if len(word.parts) != 2 || word.parts[0] != (wordPart{partLiteral, flag + "="}) || word.parts[1].kind != partParam {
		return false
	}
	declared := soleExpression(valueOf(mappingValue(outputs, output)))
	if declared == "" {
		return false
	}
	expr := soleExpression(env[word.parts[1].text])
	if m := outputRead.FindStringSubmatch(expr); m != nil {
		return m[1] == "needs" && m[2] == job && m[3] == output
	}
	actual := releaseOutputCompare.FindStringSubmatch(expr)
	expected := releaseOutputCompare.FindStringSubmatch(declared)
	if actual == nil || expected == nil || actual[1] != "needs" || actual[2] != job || expected[1] != "steps" || actual[4] != expected[4] {
		return false
	}
	other := soleExpression(valueOf(mappingValue(outputs, actual[3])))
	m := outputRead.FindStringSubmatch(other)
	return m != nil && m[1] == "steps" && m[2] == expected[2] && m[3] == expected[3]
}
