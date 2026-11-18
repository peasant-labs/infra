package policy

import (
	"slices"
	"strings"

	regexp "github.com/wasilibs/go-re2"
	"gopkg.in/yaml.v3"
)

// Release assets reach a Release through one command: gh release create
// with --draft and the asset words, which creates the draft and uploads to
// that same release. A separate creator and uploader find the release by tag
// and can each create one, so the uploader can fill a release that is never
// published while the publish step publishes an empty one.
//
// The release artifact is the handoff between the credential-free build and
// the publish job. These names are fixed so the create cannot upload another
// artifact or a checkout's files.
const (
	releaseArtifactName = "release-dist"
	releaseArtifactJob  = "build"
	releaseArtifactDir  = "dist"
)

// atomicReleaseCreateShape is the one command that uploads release assets.
const atomicReleaseCreateShape = `gh release create "$TAG" --repo "$GITHUB_REPOSITORY" --draft --verify-tag --title "$TAG" --notes-file <file> --prerelease="$PRERELEASE" '` + releaseArtifactDir + `/*'`

// retiredReleaseUploadActions are actions that upload release assets. They
// find their release by listing releases, so beside a gh release create they
// can create a second release for the tag; no release-upload action is
// allowed in a job that may hold a credential.
var retiredReleaseUploadActions = []string{"softprops/action-gh-release"}

func isReleaseUploadAction(name string) bool {
	return slices.Contains(retiredReleaseUploadActions, name)
}

var (
	stepOutcomeGate      = regexp.MustCompile(`^steps\.(` + stepIDPattern + `)\.outcome\s*==\s*'(success|failure)'$`)
	releaseOutputCompare = regexp.MustCompile(`^(needs|steps)\.(` + stepIDPattern + `)\.outputs\.([A-Za-z0-9_-]+)\s*==\s*'([^']*)'$`)
	viewFailureAndCreate = regexp.MustCompile(`^steps\.(` + stepIDPattern + `)\.outcome\s*==\s*'failure'\s*&&\s*steps\.(` + stepIDPattern + `)\.outcome\s*==\s*'success'$`)
	// assetGlobWord is one literal directory glob: a relative directory and
	// a final "/*". gh expands it itself and fails when it matches nothing.
	assetGlobWord = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*(/[A-Za-z0-9_][A-Za-z0-9_.-]*)*/\*$`)
)

// releaseAssetWordsFault checks the asset words after the tag of a gh release
// create: exactly one literal directory glob, or literal relative file paths.
// The shell never sees a glob, because a glob outside quotes is refused by the
// command reader; gh expands the quoted word itself.
func releaseAssetWordsFault(assets []templateWord) string {
	for _, w := range assets {
		lit, ok := w.literal()
		if !ok {
			return "the asset " + quote(w.raw) + " expands; name the assets as one quoted '<dir>/*' or as literal file paths"
		}
		if strings.HasPrefix(lit, "-") {
			return "the asset " + quote(w.raw) + " starts with '-'"
		}
		if strings.Contains(lit, "*") {
			if len(assets) != 1 || !assetGlobWord.MatchString(lit) || slices.Contains(strings.Split(lit, "/"), "..") {
				return "the asset glob " + quote(w.raw) + " is not the one asset word, as a literal '<dir>/*'"
			}
			continue
		}
		if why := checkPath(w); why != "" {
			return "the asset " + quote(w.raw) + " is not a literal relative path or one quoted '<dir>/*'"
		}
	}
	return ""
}

func draftFlagTrue(f parsedFlags) bool {
	draft, present := f.flags[ghFlagDraft]
	lit, literal := draft.literal()
	return present && literal && (lit == ghFlagDraft || lit == ghFlagDraft+"=true")
}

// releaseStep is one release template step of a job, read once.
type releaseStep struct {
	index int
	node  *yaml.Node
	tmpl  CredentialTemplate
	env   map[string]string
	flags parsedFlags
}

func (c *queueChecker) releaseSteps(file, jobName string, doc, job *yaml.Node) []releaseStep {
	var out []releaseStep
	for i, step := range stepsOf(job) {
		words, why := singleCommand(doc, job, step)
		if why != "" {
			continue
		}
		tmpl, ok := classifyTemplate(words)
		if !ok || (tmpl != TemplateReleaseCreate && tmpl != TemplateReleaseEdit && tmpl != TemplateReleaseView && tmpl != TemplateReleaseList) {
			continue
		}
		spec := releaseFlags()
		if tmpl == TemplateReleaseCreate {
			spec[ghFlagVerifyTag] = flagSwitch
		}
		if tmpl == TemplateReleaseView {
			spec = flagSpec{"--repo": flagRepo, "-R": flagRepo, "--json": flagLiteral, "--jq": flagLiteral}
		}
		if tmpl == TemplateReleaseList {
			spec = releaseListSpec()
		}
		env := effectiveEnv(doc, job, step)
		reader := templateStep{class: c.creds[file][jobName].class, env: env, doc: doc, job: job}
		flags, why := reader.parseFlags(words[3:], spec)
		if why != "" {
			continue
		}
		out = append(out, releaseStep{index: i, node: step, tmpl: tmpl, env: env, flags: flags})
	}
	return out
}

// checkReleaseAssets (release-assets-one-atomic-create) holds every
// publish job whose gh release create names assets to the one atomic shape:
// one creator, assets only from the build's release-dist, created as a draft
// only when the unconditional view found no release, and one publish edit
// gated on that create.
func (c *queueChecker) checkReleaseAssets() {
	c.eachDoc(QueueRuleReleaseAssets, func(name string, doc *yaml.Node) {
		for _, jobName := range jobNames(doc) {
			if c.creds[name][jobName].class != credentialJobPublish {
				continue
			}
			job := jobNode(doc, jobName)
			steps := c.releaseSteps(name, jobName, doc, job)
			var create *releaseStep
			for i := range steps {
				if steps[i].tmpl == TemplateReleaseCreate && len(steps[i].flags.positional) > 1 {
					create = &steps[i]
					break
				}
			}
			// A create without assets uploads nothing, so it cannot fill one
			// release while another is published; the upload-action ban in
			// credential-jobs-use-pinned-actions covers the other uploader.
			if create == nil {
				continue
			}
			if why := c.atomicReleaseFault(name, jobName, doc, job, steps, *create); why != "" {
				c.emit(QueueRuleReleaseAssets, name, jobName, violation(
					stepLabel(jobName, create.index, create.node)+" "+why,
					"release assets reach a Release only through one atomic gh release create --draft; a second creator or uploader finds the release by tag and can fill a release that is never published",
					rejected,
					"run "+atomicReleaseCreateShape+" gated on steps.<view>.outcome == 'failure', after an unconditional gh release view with continue-on-error: true, then one gh release edit --draft=false gated on steps.<create>.outcome == 'success'"))
			}
		}
	})
}

func (c *queueChecker) atomicReleaseFault(file, jobName string, doc, job *yaml.Node, steps []releaseStep, create releaseStep) string {
	for _, step := range stepsOf(job) {
		if name, _, ok := usesAction(stepUsesText(step)); ok && isReleaseUploadAction(name) {
			return "shares the job with the release-upload action " + name + ", so two tools would write the Release"
		}
	}
	for _, s := range steps {
		if s.tmpl == TemplateReleaseCreate && s.node != create.node {
			return "is not the only gh release create in the job"
		}
	}
	for _, asset := range create.flags.positional[1:] {
		lit, _ := asset.literal()
		if !strings.HasPrefix(lit, releaseArtifactDir+"/") {
			return "uploads " + quote(asset.raw) + ", which is outside the " + releaseArtifactDir + " directory the build's " + releaseArtifactName + " is downloaded into"
		}
	}
	if why := c.releaseDistFault(file, doc, job, create.node); why != "" {
		return why
	}
	if hasContinueOnError(create.node) {
		return "sets continue-on-error, which could hide a failed upload"
	}
	if !namesCurrentReleaseRepository(create.flags, create.env) {
		return "must name the current repository with exactly one --repo or -R flag"
	}
	condition := trimOuterParens(stripIfExpression(stepIfText(create.node)))
	m := viewFailureAndCreate.FindStringSubmatch(condition)
	if m == nil || m[1] == m[2] {
		return "must run only on steps.<view>.outcome == 'failure' && steps.<list>.outcome == 'success', so it never runs beside an existing release or a leftover draft"
	}
	viewID, listID := m[1], m[2]
	var view *releaseStep
	for i := range steps {
		if stepID(steps[i].node) == viewID && steps[i].index < create.index {
			view = &steps[i]
		}
	}
	if view == nil || view.tmpl != TemplateReleaseView || !c.receivesCredential(file, jobName, view.node) {
		return "is gated on " + quote(viewID) + ", which is not an earlier gh release view of the triggering tag"
	}
	if mappingValue(view.node, "if") != nil || valueOf(mappingValue(view.node, "continue-on-error")) != "true" {
		return "requires the release-view step to run unconditionally with continue-on-error: true"
	}
	if !namesCurrentReleaseRepository(view.flags, view.env) {
		return "requires the release-view step to name the current repository with exactly one --repo or -R flag"
	}
	if why := c.releaseListFault(file, jobName, steps, *view, create, listID); why != "" {
		return why
	}
	source, outputs, why := releaseKindSource(doc, job, create)
	if why != "" {
		return why
	}
	return c.publishEditFault(file, jobName, doc, job, steps, create, viewID, source, outputs)
}

// releaseListFault requires the one release-list check that refuses a
// create when any release or draft already names the tag: a failed view may
// be a rate limit or an outage rather than an absent release, and GitHub
// keeps several drafts for one tag.
func (c *queueChecker) releaseListFault(file, jobName string, steps []releaseStep, view, create releaseStep, listID string) string {
	var list *releaseStep
	for i := range steps {
		if steps[i].tmpl != TemplateReleaseList {
			continue
		}
		if list != nil {
			return "requires exactly one gh release list check in the job"
		}
		list = &steps[i]
	}
	if list == nil || stepID(list.node) != listID {
		return "is gated on " + quote(listID) + ", which is not the job's gh release list check; run " + releaseListShape + " between the view and the create"
	}
	if list.index < view.index || list.index > create.index {
		return "requires the gh release list check after the view and before the create"
	}
	if !matchesStepOutcome(stepIfText(list.node), view.id(), "failure") {
		return "requires the gh release list check to run only on steps." + view.id() + ".outcome == 'failure'"
	}
	if hasContinueOnError(list.node) || !c.receivesCredential(file, jobName, list.node) {
		return "requires the gh release list check to receive the credential and not set continue-on-error, so a failed list stops the create"
	}
	return ""
}

func (s releaseStep) id() string { return stepID(s.node) }

// releaseDistFault requires the build's release-dist, and nothing else, to
// be downloaded into dist before the create, with no checkout after it.
func (c *queueChecker) releaseDistFault(file string, doc, job, create *yaml.Node) string {
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
	beforeCreate, downloads, foundDownload := true, 0, false
	for _, step := range stepsOf(job) {
		if step == create {
			beforeCreate = false
		}
		if isAction(stepUsesText(step), ActionDownloadArtifact) {
			downloads++
			with := mappingValue(step, "with")
			if beforeCreate && valueOf(mappingValue(with, InputArtifactName.String())) == releaseArtifactName &&
				valueOf(mappingValue(with, InputArtifactPath.String())) == releaseArtifactDir &&
				mappingValue(step, "if") == nil && !hasContinueOnError(step) {
				foundDownload = true
			}
		}
		if beforeCreate && foundDownload && isAction(stepUsesText(step), ActionCheckout) {
			return "checks out the repository after downloading release-dist into dist"
		}
	}
	if downloads != 1 || !foundDownload {
		return "must unconditionally download only release-dist into dist before the create"
	}
	return ""
}

// releaseKindSource finds the needed job whose prerelease output the create
// reads, directly or through the comparison that job declares.
func releaseKindSource(doc, job *yaml.Node, create releaseStep) (string, *yaml.Node, string) {
	const fault = "the create --prerelease must read a needed job's prerelease output"
	word, present := create.flags.flags["--prerelease"]
	if !present || len(word.parts) != 2 || word.parts[1].kind != partParam {
		return "", nil, fault
	}
	expr := soleExpression(create.env[word.parts[1].text])
	m := outputRead.FindStringSubmatch(expr)
	if m == nil {
		m = releaseOutputCompare.FindStringSubmatch(expr)
	}
	needs, _ := needsList(job)
	if m == nil || m[1] != "needs" || !slices.Contains(needs, m[2]) {
		return "", nil, fault
	}
	outputs := mappingValue(jobNode(doc, m[2]), "outputs")
	if !publishFlagReadsOutput(word, "--prerelease", create.env, m[2], "prerelease", outputs) {
		return "", nil, fault
	}
	return m[2], outputs, ""
}

// publishEditFault binds the one publish to the create and leaves the default
// success() guard intact, so a failed create or upload cannot publish.
func (c *queueChecker) publishEditFault(file, jobName string, doc, job *yaml.Node, steps []releaseStep, create releaseStep, viewID, source string, outputs *yaml.Node) string {
	createID := stepID(create.node)
	edits := 0
	for _, s := range steps {
		if s.tmpl != TemplateReleaseEdit {
			continue
		}
		edits++
		if s.index < create.index {
			return "requires the publish edit after the create"
		}
		condition := trimOuterParens(stripIfExpression(stepIfText(s.node)))
		dual := viewFailureAndCreate.FindStringSubmatch(condition)
		if createID == "" || (!matchesStepOutcome(stepIfText(s.node), createID, "success") &&
			(dual == nil || dual[1] != viewID || dual[2] != createID)) {
			return "the publish edit must be gated on create.success, optionally with view.failure, without a status function"
		}
		if hasContinueOnError(s.node) || !c.receivesCredential(file, jobName, s.node) {
			return "the publish edit must receive the credential and must not set continue-on-error"
		}
		if !namesCurrentReleaseRepository(s.flags, s.env) {
			return "the publish edit must name the current repository with exactly one --repo or -R flag"
		}
		draft, present := s.flags.flags[ghFlagDraft]
		lit, literal := draft.literal()
		if !present || !literal || lit != ghFlagDraft+"=false" {
			return "the publish edit must pass --draft=false"
		}
		for _, name := range []string{"prerelease", "latest"} {
			flag := "--" + name
			word, present := s.flags.flags[flag]
			if !present || !publishFlagReadsOutput(word, flag, s.env, source, name, outputs) {
				return "the publish edit " + flag + " must read the needed release-kind job's " + name + " output"
			}
		}
	}
	if edits != 1 {
		return "requires exactly one publish edit after the create"
	}
	return ""
}

// namesCurrentReleaseRepository requires exactly one repo flag and the
// runner's unmodified repository value, for a release command or view.
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
