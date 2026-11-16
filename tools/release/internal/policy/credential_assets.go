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

var releaseAssetGate = regexp.MustCompile(`^steps\.(` + stepIDPattern + `)\.outcome\s*==\s*'success'\s*\|\|\s*steps\.(` + stepIDPattern + `)\.outcome\s*==\s*'success'$`)

func releaseAssetsInputFault(with, job *yaml.Node) string {
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
	for _, input := range []ActionInput{InputPrerelease, InputMakeLatest} {
		expr := soleExpression(valueOf(mappingValue(with, input.String())))
		m := outputRead.FindStringSubmatch(expr)
		if m == nil || m[1] != "needs" || !slices.Contains(needs, m[2]) {
			return input.String() + " must read one output of a job in needs"
		}
	}
	return ""
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
	for _, step := range stepsOf(job) {
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
		}
		if beforeAction && foundDownload && isAction(stepUsesText(step), ActionCheckout) {
			return "checks out the repository after downloading release-dist into dist"
		}
	}
	if downloads != 1 || !foundDownload {
		return "must unconditionally download only release-dist into dist before uploading"
	}
	condition := trimOuterParens(stripIfExpression(stepIfText(action)))
	gate := releaseAssetGate.FindStringSubmatch(condition)
	if gate == nil || gate[1] == gate[2] {
		return "must gate the action on the success of its earlier release-view or release-create step"
	}
	cj := c.creds[file][jobName]
	foundView, foundCreate := false, false
	for _, id := range gate[1:] {
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
		repo, long := flags.flags["--repo"]
		shortRepo, short := flags.flags["-R"]
		if short {
			repo = shortRepo
		}
		_, overridden := env[runnerEnvRepository]
		if long == short || !repo.params("", runnerEnvRepository, "") || overridden {
			return "gates on a release command that does not name the current repository"
		}
		foundView = foundView || tmpl == TemplateReleaseView
		foundCreate = foundCreate || tmpl == TemplateReleaseCreate
	}
	if !foundView || !foundCreate {
		return "must gate on one release-view and one release-create --verify-tag step"
	}
	return ""
}
