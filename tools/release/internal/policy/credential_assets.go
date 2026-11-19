package policy

import (
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
	regexp "github.com/wasilibs/go-re2"
	"gopkg.in/yaml.v3"
)

// Release assets reach a Release only through the release tool's
// publish-release command (the release-publish template): one process
// creates the draft, uploads every file to it by release id, verifies the
// draft is exactly the local manifest, publishes it by id and verifies it
// again. The tool is built in the publish job itself, from a pinned infra
// checkout, before the untrusted release artifact is downloaded, the same
// way a tag-verify job builds it.
//
// The release artifact is the handoff between the credential-free build and
// the publish job. These names are fixed so the publisher cannot upload
// another artifact or a checkout's files.
const (
	releaseArtifactName = "release-dist"
	releaseArtifactJob  = "build"
	releaseArtifactDir  = "dist"
)

// releasePublishShape is the one command that publishes release assets.
const releasePublishShape = `"${` + runnerEnvTemp + `}/` + intent.ProgramName + `" publish-release --tag "$TAG" --repo "$` + runnerEnvRepository + `" --assets ` + releaseArtifactDir + `/<dir> --notes ` + releaseArtifactDir + `/<file> --prerelease "$` + "PRERELEASE" + `" --latest "$` + "LATEST" + `" [--require-immutable]`

// retiredReleaseUploadActions are actions that upload release assets. They
// find their release by listing releases, so beside another creator they
// can create a second release for the tag; no release-upload action is
// allowed in a job that may hold a credential.
var retiredReleaseUploadActions = []string{"softprops/action-gh-release"}

func isReleaseUploadAction(name string) bool {
	return slices.Contains(retiredReleaseUploadActions, name)
}

var releaseOutputCompare = regexp.MustCompile(`^(needs|steps)\.(` + stepIDPattern + `)\.outputs\.([A-Za-z0-9_-]+)\s*==\s*'([^']*)'$`)

// matchReleasePublish checks the words after the binary of a
// publish-release call: every flag once, each value in its one form, and
// PRERELEASE and LATEST bound to one needed release-kind job's outputs.
// --rehearse is refused: a rehearsal runs only from a maintainer's machine.
func (s templateStep) matchReleasePublish(words []templateWord) string {
	if literalAt(words, 0) != intent.CommandPublishRelease.String() {
		return "the command is not " + intent.ProgramName + " " + intent.CommandPublishRelease.String()
	}
	seen := map[intent.Flag]templateWord{}
	for i := 1; i < len(words); i++ {
		w := words[i]
		lit, ok := w.literal()
		flag, err := intent.ParseFlag(lit)
		if w.redirect || !ok || err != nil || !intent.CommandPublishRelease.Accepts(flag) {
			return "the call passes " + quote(w.raw) + ", which the template does not take; the template is " + releasePublishShape
		}
		if _, dup := seen[flag]; dup {
			return "the call passes " + flag.String() + " twice"
		}
		switch flag {
		case intent.FlagRehearse:
			return "the call passes " + flag.String() + ": a rehearsal runs only from a maintainer's machine for a never-pushed rehearsal tag, never in a workflow"
		case intent.FlagRequireImmutable:
			seen[flag] = w
			continue
		}
		if i+1 >= len(words) || words[i+1].redirect {
			return "the call passes " + flag.String() + " with no value"
		}
		i++
		seen[flag] = words[i]
	}
	for _, use := range intent.CommandPublishRelease.Flags() {
		if use.Required {
			if _, ok := seen[use.Flag]; !ok {
				return "the call does not pass " + use.Flag.String() + "; the template is " + releasePublishShape
			}
		}
	}
	if !s.triggeringTag(seen[intent.FlagTag]) {
		return "the tag " + quote(seen[intent.FlagTag].raw) + " is not the triggering tag: pass \"$" + TemplateEnvTag.String() + "\" with " + TemplateEnvTag.String() + " mapped from ${{ " + triggeringRefName + " }}"
	}
	if _, overridden := s.env[runnerEnvRepository]; overridden || !seen[intent.FlagRepo].params("", runnerEnvRepository, "") {
		return "the call must name the current repository as " + intent.FlagRepo.String() + " \"$" + runnerEnvRepository + "\", unoverridden"
	}
	assets, ok := seen[intent.FlagAssets].literal()
	if !ok || checkPath(seen[intent.FlagAssets]) != "" || !strings.HasPrefix(assets, releaseArtifactDir+"/") {
		return intent.FlagAssets.String() + " " + quote(seen[intent.FlagAssets].raw) + " is not a literal directory under " + releaseArtifactDir + "/, where the build's " + releaseArtifactName + " is downloaded"
	}
	notes, ok := seen[intent.FlagNotes].literal()
	if !ok || checkPath(seen[intent.FlagNotes]) != "" || !strings.HasPrefix(notes, releaseArtifactDir+"/") {
		return intent.FlagNotes.String() + " " + quote(seen[intent.FlagNotes].raw) + " is not a literal file under " + releaseArtifactDir + "/"
	}
	if a, n := path.Clean(assets), path.Clean(notes); n == a || strings.HasPrefix(n, a+"/") {
		return intent.FlagNotes.String() + " " + quote(notes) + " is inside the asset directory " + quote(assets) + ", so the notes would be published as an asset"
	}
	for flag, env := range map[intent.Flag]TemplateEnv{intent.FlagPrerelease: TemplateEnvPrerelease, intent.FlagLatest: TemplateEnvLatest} {
		if !seen[flag].params("", env.String(), "") {
			return flag.String() + " " + quote(seen[flag].raw) + " is not \"$" + env.String() + "\""
		}
	}
	return releaseKindBinding(s.doc, s.job, s.env)
}

// releaseKindBinding requires PRERELEASE and LATEST to read one needed
// job's prerelease and latest outputs, directly or through the comparison
// that job declares for them.
func releaseKindBinding(doc, job *yaml.Node, env map[string]string) string {
	const fault = "PRERELEASE and LATEST must read one needed release-kind job's prerelease and latest outputs"
	expr := soleExpression(env[TemplateEnvPrerelease.String()])
	m := outputRead.FindStringSubmatch(expr)
	if m == nil {
		m = releaseOutputCompare.FindStringSubmatch(expr)
	}
	needs, _ := needsList(job)
	if m == nil || m[1] != "needs" || !slices.Contains(needs, m[2]) {
		return fault
	}
	outputs := mappingValue(jobNode(doc, m[2]), "outputs")
	for _, b := range []struct {
		env    TemplateEnv
		output string
	}{{TemplateEnvPrerelease, "prerelease"}, {TemplateEnvLatest, "latest"}} {
		if !envReadsOutput(env[b.env.String()], m[2], b.output, outputs) {
			return fault + ": " + b.env.String() + " does not read jobs." + m[2] + "'s " + b.output + " output"
		}
	}
	return ""
}

// envReadsOutput accepts the named output directly, or a comparison of
// another output that matches the producer's own declared comparison. The
// latter admits a consumer comparing guard.kind only when that is exactly
// how the guard declared its corresponding prerelease/latest output.
func envReadsOutput(value, job, output string, outputs *yaml.Node) bool {
	declared := soleExpression(valueOf(mappingValue(outputs, output)))
	if declared == "" {
		return false
	}
	expr := soleExpression(value)
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

// publisherStep is one of the five steps of a job that publishes release
// assets, in their required order.
type publisherStep string

const (
	publisherToolCheckout publisherStep = "tool-checkout"
	publisherSetupGo      publisherStep = "setup-go"
	publisherBuild        publisherStep = "build"
	publisherDownload     publisherStep = "download"
	publisherPublish      publisherStep = "publish"
)

// publisherOrder is the one order: the tool is built before the untrusted
// release-dist bytes arrive, and nothing runs after the publish.
var publisherOrder = []publisherStep{publisherToolCheckout, publisherSetupGo, publisherBuild, publisherDownload, publisherPublish}

func publisherShapes() string {
	var out []string
	for _, s := range publisherOrder {
		switch s {
		case publisherToolCheckout:
			out = append(out, string(s)+" `"+VerifyStepToolCheckout.Shape()+"`")
		case publisherSetupGo:
			out = append(out, string(s)+" `"+VerifyStepSetupGo.Shape()+"`")
		case publisherBuild:
			out = append(out, string(s)+" `"+VerifyStepBuild.Shape()+"`")
		case publisherDownload:
			out = append(out, string(s)+" `"+ActionDownloadArtifact.String()+"@<40-hex> with name: "+releaseArtifactName+", path: "+releaseArtifactDir+"`")
		case publisherPublish:
			out = append(out, string(s)+" `"+releasePublishShape+"`")
		}
	}
	return strings.Join(out, ", then ")
}

// checkReleaseAssets (release-assets-one-atomic-create) holds every publish
// job that publishes release assets to the one publisher: exactly the five
// publisher steps in order, none with an if or a continue-on-error key, and
// no other release command beside it. A publish job that names assets in a
// gh release create runs the retired form and is refused here too.
func (c *queueChecker) checkReleaseAssets() {
	c.eachDoc(QueueRuleReleaseAssets, func(name string, doc *yaml.Node) {
		for _, jobName := range jobNames(doc) {
			if c.creds[name][jobName].class != credentialJobPublish {
				continue
			}
			job := jobNode(doc, jobName)
			why, index, node := c.publisherFault(name, doc, job)
			if why == "" {
				continue
			}
			label := "jobs." + jobName
			if node != nil {
				label = stepLabel(jobName, index, node)
			}
			c.emit(QueueRuleReleaseAssets, name, jobName, violation(
				label+" "+why,
				"release assets reach a Release only through one publish-release built in the job before the release artifact arrives; it creates, uploads, verifies and publishes by release id, so no release is published empty or partial and no leftover draft is filled",
				rejected,
				"make the publish job exactly: "+publisherShapes()+"; no if and no continue-on-error on any of them"))
		}
	})
}

// publisherFault says why a publish job that publishes assets is not the
// one publisher shape, or "" when it is or when it publishes no assets.
func (c *queueChecker) publisherFault(file string, doc, job *yaml.Node) (string, int, *yaml.Node) {
	steps := stepsOf(job)
	var publishers []int
	for i, step := range steps {
		words, why := singleCommand(doc, job, step)
		if why != "" {
			continue
		}
		tmpl, ok := classifyTemplate(words)
		switch {
		case ok && tmpl == TemplateReleasePublish:
			publishers = append(publishers, i)
		case ok && tmpl == TemplateReleaseCreate && createNamesAssets(words):
			return "names assets in gh release create, the retired form; publish with " + TemplateReleasePublish.String() + " `" + releasePublishShape + "`", i, step
		}
	}
	if len(publishers) == 0 {
		return "", 0, nil
	}
	if len(publishers) > 1 {
		return "is a second publish-release in the job; one job publishes one release once", publishers[1], steps[publishers[1]]
	}
	for i, step := range steps {
		if tmpl, ok := stepTemplate(step); ok && tmpl != TemplateReleasePublish {
			return "runs " + tmpl.String() + " beside publish-release; the publisher is the only release command of its job", i, step
		}
	}
	if len(steps) != len(publisherOrder) {
		return "has " + strconv.Itoa(len(steps)) + " steps, and a publisher job has exactly " + strconv.Itoa(len(publisherOrder)), 0, nil
	}
	toolPath, why := verifyToolCheckout(steps)
	if why != "" {
		return why, 0, nil
	}
	for i, want := range publisherOrder {
		step := steps[i]
		if mappingValue(step, "if") != nil {
			return "sets if; no publisher step may be skipped", i, step
		}
		if hasContinueOnError(step) {
			return "sets continue-on-error; a publisher step's failure must fail the job", i, step
		}
		if why := c.publisherStepFault(file, doc, job, step, want, toolPath, steps[publishers[0]]); why != "" {
			return "is not the " + string(want) + " step: " + why, i, step
		}
	}
	for i, step := range steps {
		if i != 2 {
			if _, set := effectiveEnv(doc, job, step)[TemplateEnvGoWork.String()]; set {
				return "runs with " + TemplateEnvGoWork.String() + ", which only the build step's own env may set", i, step
			}
		}
	}
	return "", 0, nil
}

// publisherStepFault says why one step is not the publisher step expected at
// its position, or "".
func (c *queueChecker) publisherStepFault(file string, doc, job, step *yaml.Node, want publisherStep, toolPath string, publish *yaml.Node) string {
	switch want {
	case publisherToolCheckout, publisherSetupGo:
		uses := stepUsesText(step)
		if uses == "" {
			return "it runs no action"
		}
		kind, why := verifyActionFault(step, uses, toolPath)
		if why != "" {
			return why
		}
		if (want == publisherToolCheckout && kind != VerifyStepToolCheckout) || (want == publisherSetupGo && kind != VerifyStepSetupGo) {
			return "it is the " + kind.String() + " step"
		}
	case publisherBuild:
		words, why := singleCommand(doc, job, step)
		if why != "" {
			return why
		}
		if matchVerifyBuild(words, toolPath) != "" {
			return "it does not run `" + VerifyStepBuild.Shape() + "`"
		}
		if env := mappingValue(step, "env"); env == nil || strings.TrimSpace(valueOf(mappingValue(env, TemplateEnvGoWork.String()))) != VerifyEnvGoWork.Value() {
			return "its own env does not set " + TemplateEnvGoWork.String() + ": " + VerifyEnvGoWork.Value()
		}
	case publisherDownload:
		if !isAction(stepUsesText(step), ActionDownloadArtifact) {
			return "it does not download " + releaseArtifactName
		}
		if why := c.releaseDistFault(file, doc, job, publish); why != "" {
			return why
		}
	case publisherPublish:
		if tmpl, ok := stepTemplate(step); !ok || tmpl != TemplateReleasePublish {
			return "it does not run `" + releasePublishShape + "`"
		}
		if !c.receivesCredential(file, jobNameOf(doc, job), step) {
			return "it receives no credential; map GH_TOKEN: ${{ github.token }} into its env"
		}
	}
	return ""
}

// createNamesAssets reports whether a gh release create names a word after
// the tag.
func createNamesAssets(words []templateWord) bool {
	positional := 0
	for i := 3; i < len(words); i++ {
		lead := ""
		if len(words[i].parts) > 0 && words[i].parts[0].kind == partLiteral {
			lead = words[i].parts[0].text
		}
		if strings.HasPrefix(lead, "-") {
			name, _, attached := strings.Cut(lead, "=")
			if kind, ok := releaseFlags()[name]; ok && !attached && kind != flagSwitch && kind != flagSwitchValue {
				i++
			}
			continue
		}
		positional++
	}
	return positional > 1
}

// jobNameOf finds a job's name in its workflow.
func jobNameOf(doc, job *yaml.Node) string {
	for _, name := range jobNames(doc) {
		if jobNode(doc, name) == job {
			return name
		}
	}
	return ""
}

// releaseDistFault requires the build's release-dist, and nothing else, to
// be downloaded into dist before the publisher, with no checkout after it.
func (c *queueChecker) releaseDistFault(file string, doc, job, publisher *yaml.Node) string {
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
	before, downloads, foundDownload := true, 0, false
	for _, step := range stepsOf(job) {
		if step == publisher {
			before = false
		}
		if isAction(stepUsesText(step), ActionDownloadArtifact) {
			downloads++
			with := mappingValue(step, "with")
			if before && valueOf(mappingValue(with, InputArtifactName.String())) == releaseArtifactName &&
				valueOf(mappingValue(with, InputArtifactPath.String())) == releaseArtifactDir &&
				mappingValue(step, "if") == nil && !hasContinueOnError(step) {
				foundDownload = true
			}
		}
		if before && foundDownload && isAction(stepUsesText(step), ActionCheckout) {
			return "checks out a repository after downloading release-dist into dist"
		}
	}
	if downloads != 1 || !foundDownload {
		return "must unconditionally download only release-dist into dist before the publisher"
	}
	return ""
}
