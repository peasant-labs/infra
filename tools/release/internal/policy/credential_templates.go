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

// CredentialTemplate is one command a step that receives a credential may
// run. A template is matched on the words of one command with an exact
// structure, never by a pattern over the text; its variable parts are named
// and typed: the job's own bot branch from the release config, the
// planner's tag output, a PR number from a step or job output, the
// triggering tag, and quoted title or body text. Every value that is not
// literal comes from the step's env. The zero value is unknown.
type CredentialTemplate string

const (
	// TemplatePushOwnBranch pushes the job's own bot branch.
	TemplatePushOwnBranch CredentialTemplate = "push-own-branch"
	// TemplatePushReleaseTag pushes the planner's tag; release job only.
	TemplatePushReleaseTag CredentialTemplate = "push-release-tag"
	// TemplatePRCreate opens a PR from the job's own bot branch.
	TemplatePRCreate CredentialTemplate = "pr-create"
	// TemplatePREdit retitles or rewrites the body of the job's own PR.
	TemplatePREdit CredentialTemplate = "pr-edit"
	// TemplatePRClose closes the job's own PR.
	TemplatePRClose CredentialTemplate = "pr-close"
	// TemplatePRMerge turns auto-merge on or off for the job's own PR.
	TemplatePRMerge CredentialTemplate = "pr-merge"
	// TemplateQueueRead reads the merge queue through one GraphQL query,
	// for a repository whose default token cannot read it.
	TemplateQueueRead CredentialTemplate = "merge-queue-read"
	// TemplateReleaseView reads the Release of the triggering tag.
	TemplateReleaseView CredentialTemplate = "release-view"
	// TemplateReleaseEdit edits the Release of the triggering tag.
	TemplateReleaseEdit CredentialTemplate = "release-edit"
	// TemplateReleaseCreate creates the Release of the triggering tag, which
	// must already exist.
	TemplateReleaseCreate CredentialTemplate = "release-create"
)

// AllCredentialTemplates is the canonical closed set of templates.
var AllCredentialTemplates = []CredentialTemplate{
	TemplatePushOwnBranch,
	TemplatePushReleaseTag,
	TemplatePRCreate,
	TemplatePREdit,
	TemplatePRClose,
	TemplatePRMerge,
	TemplateQueueRead,
	TemplateReleaseView,
	TemplateReleaseEdit,
	TemplateReleaseCreate,
}

func (t CredentialTemplate) String() string { return string(t) }

// ParseCredentialTemplate maps a template name to its member and refuses
// anything else.
func ParseCredentialTemplate(raw string) (CredentialTemplate, error) {
	return closedset.Parse(AllCredentialTemplates, raw, closedset.Context{
		Set:   "credential template",
		Where: "policy.ParseCredentialTemplate",
		When:  "reading a template name for a diagnostic or a test case",
		Means: "a template name selects one command shape, so an unknown name selects none",
	})
}

// Shape renders the template's exact word structure for messages.
func (t CredentialTemplate) Shape() string {
	switch t {
	case TemplatePushOwnBranch:
		return `git [-c http.extraheader="AUTHORIZATION: bearer ${` + TemplateEnvAppToken.String() + `}"] push [--force-with-lease|--force] [--no-verify] origin <own bot branch | HEAD:<own bot branch>>`
	case TemplatePushReleaseTag:
		return `git [-c http.extraheader="AUTHORIZATION: bearer ${` + TemplateEnvAppToken.String() + `}"] push origin "` + intent.TagRefPrefix + `${` + TemplateEnvTag.String() + `}:` + intent.TagRefPrefix + `${` + TemplateEnvTag.String() + `}"`
	case TemplatePRCreate:
		return `gh pr create --head <own bot branch> [--base <branch>] [--title <text>] [--body <text>]`
	case TemplatePREdit:
		return `gh pr edit [--title <text>] [--body <text>] <own PR>`
	case TemplatePRClose:
		return `gh pr close [--comment <text>] <own PR>`
	case TemplatePRMerge:
		return `gh pr merge (--auto [--squash|--merge|--rebase] | --disable-auto) <own PR>`
	case TemplateQueueRead:
		return `gh api graphql [-F <name>=<literal>]... -f query='<a mergeQueue query>' [> <file>]`
	case TemplateReleaseView:
		return `gh release view <triggering tag> [--repo <repo>]`
	case TemplateReleaseEdit:
		return `gh release edit <triggering tag> [--repo <repo>] [--title <text>] [--notes <text> | --notes-file <file>] [--prerelease[=<v>]] [--latest[=<v>]]`
	case TemplateReleaseCreate:
		return `gh release create <triggering tag> --verify-tag [--repo <repo>] [--title <text>] [--notes <text> | --notes-file <file>] [--prerelease[=<v>]] [--latest[=<v>]]`
	}
	return "<unknown template>"
}

// templatesFor lists the templates a job class may run, in canonical order.
func templatesFor(class credentialJobClass) []CredentialTemplate {
	switch class {
	case credentialJobRelease:
		return []CredentialTemplate{TemplatePushOwnBranch, TemplatePushReleaseTag, TemplatePRCreate, TemplatePREdit, TemplatePRClose, TemplatePRMerge, TemplateQueueRead}
	case credentialJobBot:
		return []CredentialTemplate{TemplatePushOwnBranch, TemplatePRCreate, TemplatePREdit, TemplatePRClose, TemplatePRMerge, TemplateQueueRead}
	case credentialJobPublish:
		return []CredentialTemplate{TemplateReleaseView, TemplateReleaseEdit, TemplateReleaseCreate}
	}
	return nil
}

// PlumbingTemplate is one command a job that may hold a credential may run
// without a credential: it moves what an earlier job prepared from the
// downloaded artifact into the local repository, so a credential template
// can push it. Like a credential template it is matched on the exact words
// of one command. The zero value is unknown.
type PlumbingTemplate string

const (
	// PlumbingFetchBundleBranch fetches the job's own bot branch from a git
	// bundle into the local branch of the same name.
	PlumbingFetchBundleBranch PlumbingTemplate = "fetch-bundle-branch"
	// PlumbingFetchBundleTag fetches the planner's tag from a git bundle
	// into the local tag of the same name; release job only.
	PlumbingFetchBundleTag PlumbingTemplate = "fetch-bundle-tag"
)

// AllPlumbingTemplates is the canonical closed set of plumbing templates.
var AllPlumbingTemplates = []PlumbingTemplate{PlumbingFetchBundleBranch, PlumbingFetchBundleTag}

func (t PlumbingTemplate) String() string { return string(t) }

// ParsePlumbingTemplate maps a plumbing template name to its member and
// refuses anything else.
func ParsePlumbingTemplate(raw string) (PlumbingTemplate, error) {
	return closedset.Parse(AllPlumbingTemplates, raw, closedset.Context{
		Set:   "plumbing template",
		Where: "policy.ParsePlumbingTemplate",
		When:  "reading a plumbing template name for a diagnostic or a test case",
		Means: "a plumbing template name selects one command shape, so an unknown name selects none",
	})
}

// Shape renders the plumbing template's exact word structure.
func (t PlumbingTemplate) Shape() string {
	switch t {
	case PlumbingFetchBundleBranch:
		return `git fetch [--no-tags] <file>` + bundleSuffix + ` <own bot branch>:<own bot branch>`
	case PlumbingFetchBundleTag:
		return `git fetch [--no-tags] <file>` + bundleSuffix + ` "` + intent.TagRefPrefix + `${` + TemplateEnvTag.String() + `}:` + intent.TagRefPrefix + `${` + TemplateEnvTag.String() + `}"`
	}
	return "<unknown plumbing template>"
}

// plumbingFor lists the plumbing templates a job class may run.
func plumbingFor(class credentialJobClass) []PlumbingTemplate {
	switch class {
	case credentialJobRelease:
		return []PlumbingTemplate{PlumbingFetchBundleBranch, PlumbingFetchBundleTag}
	case credentialJobBot:
		return []PlumbingTemplate{PlumbingFetchBundleBranch}
	}
	return nil
}

// templateStep is what a template needs to know about the step it judges:
// the job's class and bot branch, the step's effective env, and the
// workflow and job, to resolve where an env value comes from.
type templateStep struct {
	class  credentialJobClass
	branch string
	env    map[string]string
	doc    *yaml.Node
	job    *yaml.Node
}

// The fixed words the templates are built from. These are git's and gh's,
// not the release tool's.
const (
	cmdGit            = "git"
	cmdGH             = "gh"
	gitConfigOption   = "-c"
	gitPush           = "push"
	gitFetch          = "fetch"
	gitRemote         = "origin"
	headRef           = "HEAD"
	authHeaderKey     = "http.extraheader="
	authHeaderValue   = "authorization: bearer "
	ghPR              = "pr"
	ghAPI             = "api"
	ghGraphQL         = "graphql"
	ghRelease         = "release"
	graphQLQueryField = "query"
	graphQLMergeQueue = "mergeQueue"
	ghVerbCreate      = "create"
	ghVerbEdit        = "edit"
	ghVerbClose       = "close"
	ghVerbMerge       = "merge"
	ghVerbView        = "view"
	ghFlagHead        = "--head"
	ghFlagVerifyTag   = "--verify-tag"
	ghFlagAuto        = "--auto"
	ghFlagDisableAuto = "--disable-auto"
	gitFlagForceLease = "--force-with-lease"
	gitFlagForce      = "--force"
	gitFlagNoVerify   = "--no-verify"
	gitFlagNoTags     = "--no-tags"
	endOfOptions      = "--"
	bundleSuffix      = ".bundle"
	triggeringRefName = "github.ref_name"
)

// literalAt returns the literal text of word i, or "".
func literalAt(words []templateWord, i int) string {
	if i >= len(words) {
		return ""
	}
	s, _ := words[i].literal()
	return s
}

// classifyTemplate names the credential template a command's leading words
// select, without checking its parts: the push form by its last word, a gh
// command by its subcommand. ok is false for any other command.
func classifyTemplate(words []templateWord) (CredentialTemplate, bool) {
	switch literalAt(words, 0) {
	case cmdGit:
		i := 1
		if literalAt(words, i) == gitConfigOption {
			i += 2
		}
		if literalAt(words, i) != gitPush {
			return "", false
		}
		last := words[len(words)-1]
		if strings.Contains(last.raw, intent.TagRefPrefix) {
			return TemplatePushReleaseTag, true
		}
		return TemplatePushOwnBranch, true
	case cmdGH:
		switch literalAt(words, 1) {
		case ghPR:
			switch literalAt(words, 2) {
			case ghVerbCreate:
				return TemplatePRCreate, true
			case ghVerbEdit:
				return TemplatePREdit, true
			case ghVerbClose:
				return TemplatePRClose, true
			case ghVerbMerge:
				return TemplatePRMerge, true
			}
		case ghAPI:
			if literalAt(words, 2) == ghGraphQL {
				return TemplateQueueRead, true
			}
		case ghRelease:
			switch literalAt(words, 2) {
			case ghVerbView:
				return TemplateReleaseView, true
			case ghVerbEdit:
				return TemplateReleaseEdit, true
			case ghVerbCreate:
				return TemplateReleaseCreate, true
			}
		}
	}
	return "", false
}

// stepTemplate classifies a step's run block, or ok false when the block is
// not one command or selects no credential template.
func stepTemplate(step *yaml.Node) (CredentialTemplate, bool) {
	words, why := tokenizeCommand(stepRunText(step))
	if why != "" {
		return "", false
	}
	return classifyTemplate(words)
}

// matchTemplate checks one credential step's command against the template
// its leading words select, with the job's class and branch. It returns the
// template and "" when the command is exactly that template, or the reason
// it is not.
func matchTemplate(words []templateWord, s templateStep) (CredentialTemplate, string) {
	tmpl, ok := classifyTemplate(words)
	if !ok {
		return "", "the command is none of the credential templates"
	}
	if !slices.Contains(templatesFor(s.class), tmpl) {
		return tmpl, "a " + s.class.String() + " may not run the " + tmpl.String() + " template"
	}
	if s.class != credentialJobPublish && s.branch == "" {
		return tmpl, "the release config names no bot branch for the job's kind, so the job owns no branch and no PR"
	}
	var why string
	switch tmpl {
	case TemplatePushOwnBranch, TemplatePushReleaseTag:
		why = s.matchPush(words, tmpl)
	case TemplatePRCreate:
		why = s.matchPRCreate(words[3:])
	case TemplatePREdit:
		why = s.matchPRSelectorCommand(words[3:], flagSpec{"--title": flagText, "-t": flagText, "--body": flagText, "-b": flagText})
	case TemplatePRClose:
		why = s.matchPRSelectorCommand(words[3:], flagSpec{"--comment": flagText, "-c": flagText})
	case TemplatePRMerge:
		why = s.matchPRMerge(words[3:])
	case TemplateQueueRead:
		why = s.matchQueueRead(words[3:])
	case TemplateReleaseView:
		why = s.matchRelease(words[3:], flagSpec{"--repo": flagRepo, "-R": flagRepo}, false)
	case TemplateReleaseEdit:
		why = s.matchRelease(words[3:], releaseFlags(), false)
	case TemplateReleaseCreate:
		spec := releaseFlags()
		spec[ghFlagVerifyTag] = flagSwitch
		why = s.matchRelease(words[3:], spec, true)
	}
	return tmpl, why
}

// matchPlumbing checks one no-credential command of a credential job
// against the plumbing templates its class may run. It returns the template
// and "" when the command is exactly one of them, or the reason it is not.
func matchPlumbing(words []templateWord, s templateStep) (PlumbingTemplate, string) {
	if literalAt(words, 0) != cmdGit || literalAt(words, 1) != gitFetch {
		return "", "the command is none of the plumbing templates"
	}
	rest := words[2:]
	if literalAt(rest, 0) == gitFlagNoTags {
		rest = rest[1:]
	}
	if len(rest) != 2 {
		return "", "git fetch names " + strconv.Itoa(len(rest)) + " operands, and the plumbing templates take exactly one bundle file and one refspec"
	}
	if why := checkPath(rest[0]); why != "" {
		return "", why
	}
	if file, _ := rest[0].literal(); !strings.HasSuffix(file, bundleSuffix) {
		return "", "git fetch reads " + quote(file) + ", which is not a " + bundleSuffix + " file"
	}
	tmpl := PlumbingFetchBundleBranch
	if strings.Contains(rest[1].raw, intent.TagRefPrefix) {
		tmpl = PlumbingFetchBundleTag
	}
	if !slices.Contains(plumbingFor(s.class), tmpl) {
		return tmpl, "a " + s.class.String() + " may not run the " + tmpl.String() + " plumbing template"
	}
	if tmpl == PlumbingFetchBundleTag {
		return tmpl, s.releaseTagSpec(rest[1])
	}
	if s.branch == "" {
		return tmpl, "the release config names no bot branch for the job's kind, so the job owns no branch"
	}
	spec, isLit := rest[1].literal()
	if !isLit {
		return tmpl, "the refspec " + quote(rest[1].raw) + " expands, and the template takes the job's own bot branch as literal text"
	}
	spec = strings.TrimPrefix(spec, "+")
	for _, ref := range []string{s.branch, intent.BranchRef(s.branch).String()} {
		if spec == ref+":"+ref {
			return tmpl, ""
		}
	}
	return tmpl, "the refspec " + quote(spec) + " does not fetch the job's own bot branch " + quote(s.branch) + " into itself"
}

// matchPush checks a git push: the optional authorization header, the
// switches, the remote, and the one refspec.
func (s templateStep) matchPush(words []templateWord, tmpl CredentialTemplate) string {
	i := 1
	if literalAt(words, 1) == gitConfigOption {
		if why := s.authHeader(words[2]); why != "" {
			return why
		}
		i = 3
	}
	i++ // push
	var operands []templateWord
	for ; i < len(words); i++ {
		w := words[i]
		if w.redirect {
			return "the push redirects its output"
		}
		lit, isLit := w.literal()
		if isLit && strings.HasPrefix(lit, "-") {
			switch lit {
			case gitFlagForceLease, gitFlagForce, gitFlagNoVerify:
				if tmpl == TemplatePushReleaseTag {
					return "the tag push passes " + quote(lit) + ", and a release tag is pushed once and never moved"
				}
				continue
			}
			return "the push passes option " + quote(lit) + ", which the template does not allow"
		}
		operands = append(operands, w)
	}
	if len(operands) != 2 {
		return "the push names " + strconv.Itoa(len(operands)) + " operands, and the template takes exactly the remote and one refspec"
	}
	if lit, _ := operands[0].literal(); lit != gitRemote {
		return "the push names remote " + quote(operands[0].raw) + ", and the template pushes only to " + gitRemote
	}
	if tmpl == TemplatePushReleaseTag {
		if s.class != credentialJobRelease {
			return "only the release job pushes the release tag"
		}
		return s.releaseTagSpec(operands[1])
	}
	spec, isLit := operands[1].literal()
	if !isLit {
		return "the refspec " + quote(operands[1].raw) + " expands, and the template takes the job's own bot branch as literal text"
	}
	for _, want := range []string{s.branch, headRef + ":" + s.branch, intent.BranchRef(s.branch).String(), headRef + ":" + intent.BranchRef(s.branch).String()} {
		if spec == want {
			return ""
		}
	}
	return "the refspec " + quote(spec) + " is not the job's own bot branch " + quote(s.branch)
}

// authHeader checks the -c value of a push: the authorization header with
// the credential carried in one env name, double-quoted.
func (s templateStep) authHeader(w templateWord) string {
	bad := "the git -c value " + quote(w.raw) + " is not the authorization header " + `http.extraheader="AUTHORIZATION: bearer ${` + TemplateEnvAppToken.String() + `}"`
	if len(w.parts) != 2 || w.parts[0].kind != partLiteral || w.parts[1].kind != partParam {
		return bad
	}
	if !strings.EqualFold(w.parts[0].text, authHeaderKey+authHeaderValue) {
		return bad
	}
	env, err := ParseTemplateEnv(w.parts[1].text)
	if err != nil || !env.carriesCredential() {
		return bad + ": the header reads " + quote(w.parts[1].text) + ", which is not a credential carrier"
	}
	if _, set := s.env[env.String()]; !set {
		return bad + ": " + env.String() + " is not set in the step env"
	}
	return ""
}

// releaseTagSpec checks a tag refspec: exactly
// "refs/tags/${TAG}:refs/tags/${TAG}", with TAG mapped from the planner's
// tag output. Both sides are written out because git splits a refspec at
// its last ':': a ':' smuggled into TAG then leaves a source that names no
// ref, and the command fails, instead of retargeting a single-sided
// refspec at another ref.
func (s templateStep) releaseTagSpec(w templateWord) string {
	p := w.parts
	tag := TemplateEnvTag.String()
	if len(p) != 4 ||
		p[0].kind != partLiteral || p[0].text != intent.TagRefPrefix ||
		p[1].kind != partParam || p[1].text != tag ||
		p[2].kind != partLiteral || p[2].text != ":"+intent.TagRefPrefix ||
		p[3].kind != partParam || p[3].text != tag {
		return "the tag refspec " + quote(w.raw) + " is not \"" + intent.TagRefPrefix + "${" + tag + "}:" + intent.TagRefPrefix + "${" + tag + "}\""
	}
	return s.plannerOutput(TemplateEnvTag, intent.OutputTag)
}

// plannerOutput checks that an env name carries exactly one planner output:
// ${{ steps.<plan>.outputs.<key> }}, or ${{ needs.<job>.outputs.<name> }}
// where that job maps <name> from exactly that step output, and <plan> runs
// the planner.
func (s templateStep) plannerOutput(env TemplateEnv, key intent.OutputKey) string {
	expr := soleExpression(s.env[env.String()])
	step, gotKey, ok := resolveOutput(s.doc, s.job, expr)
	if !ok || gotKey != key.String() {
		return env.String() + " is " + quote(s.env[env.String()]) + ", which is not " + stepOutput("<plan>", key) + " or a needs output mapped from it"
	}
	if !planRunner.MatchString(stepRunText(step)) {
		return env.String() + " reads steps." + stepID(step) + ", which does not run " + intent.ProgramName + " " + intent.CommandPlan.String()
	}
	return ""
}

// outputRead matches one step output read or one needs output read, alone,
// and captures the context, the step or job id, and the output name.
var outputRead = regexp.MustCompile(`^(steps|needs)\.(` + stepIDPattern + `)\.outputs\.([A-Za-z0-9_-]+)$`)

// resolveOutput follows an expression body to the step output it reads:
// steps.<id>.outputs.<key> in the job itself, or needs.<job>.outputs.<name>
// where <job> is one of the job's needs and maps <name> from exactly one of
// its own step outputs. It returns that step and the output key.
func resolveOutput(doc, job *yaml.Node, expr string) (*yaml.Node, string, bool) {
	m := outputRead.FindStringSubmatch(expr)
	if m == nil {
		return nil, "", false
	}
	if m[1] == "steps" {
		for _, step := range stepsOf(job) {
			if stepID(step) == m[2] {
				return step, m[3], true
			}
		}
		return nil, "", false
	}
	needs, err := needsList(job)
	if err != nil || !slices.Contains(needs, m[2]) {
		return nil, "", false
	}
	producer := jobNode(doc, m[2])
	mapped := mappingValue(mappingValue(producer, "outputs"), m[3])
	if mapped == nil {
		return nil, "", false
	}
	n := outputRead.FindStringSubmatch(soleExpression(mapped.Value))
	if n == nil || n[1] != "steps" {
		return nil, "", false
	}
	for _, step := range stepsOf(producer) {
		if stepID(step) == n[2] {
			return step, n[3], true
		}
	}
	return nil, "", false
}

// soleExpression returns the body of a value that is exactly one ${{ }}
// expression, trimmed, or "".
func soleExpression(value string) string {
	v := strings.TrimSpace(value)
	if !strings.HasPrefix(v, "${{") || !strings.HasSuffix(v, "}}") {
		return ""
	}
	body := v[3 : len(v)-2]
	if strings.Contains(body, "}}") || strings.Contains(body, "${{") {
		return ""
	}
	return strings.TrimSpace(body)
}

// flagKind is what one gh flag takes.
type flagKind int

const (
	// flagSwitch takes no value.
	flagSwitch flagKind = iota + 1
	// flagText takes quoted text: literals and non-credential env names.
	flagText
	// flagLiteral takes one literal word.
	flagLiteral
	// flagPath takes one literal relative file path.
	flagPath
	// flagRepo takes the repository.
	flagRepo
	// flagSwitchValue is a switch that may carry =<value>.
	flagSwitchValue
)

// flagSpec maps each flag a template takes to its kind.
type flagSpec map[string]flagKind

func releaseFlags() flagSpec {
	return flagSpec{
		"--repo": flagRepo, "-R": flagRepo,
		"--title": flagText, "-t": flagText,
		"--notes": flagText, "-n": flagText,
		"--notes-file": flagPath, "-F": flagPath,
		"--prerelease": flagSwitchValue, "--latest": flagSwitchValue,
	}
}

// parsedFlags is one command's flags and positional words after the fixed
// leading words. guarded tells, per positional word, whether it follows
// "--", so gh can never read its value as a flag.
type parsedFlags struct {
	flags      map[string]templateWord
	positional []templateWord
	guarded    []bool
}

// parseFlags reads the words after a gh subcommand against the template's
// flag spec: each flag at most once, every value checked by its kind, no
// flag outside the spec, and no redirection. After "--" every word is
// positional.
func (s templateStep) parseFlags(words []templateWord, spec flagSpec) (parsedFlags, string) {
	out := parsedFlags{flags: map[string]templateWord{}}
	ended := false
	for i := 0; i < len(words); i++ {
		w := words[i]
		if w.redirect {
			return out, "the command redirects its output, which the template does not allow there"
		}
		lead := ""
		if len(w.parts) > 0 && w.parts[0].kind == partLiteral {
			lead = w.parts[0].text
		}
		if !ended {
			if lit, ok := w.literal(); ok && lit == endOfOptions {
				ended = true
				continue
			}
		}
		if ended || !strings.HasPrefix(lead, "-") {
			out.positional = append(out.positional, w)
			out.guarded = append(out.guarded, ended)
			continue
		}
		_, isLit := w.literal()
		name, _, attached := strings.Cut(lead, "=")
		if !isLit && !attached {
			return out, "the flag word " + quote(w.raw) + " expands"
		}
		kind, ok := spec[name]
		if !ok {
			return out, "the command passes " + quote(w.raw) + ", which the template does not take"
		}
		if _, dup := out.flags[name]; dup {
			return out, "the command passes " + name + " twice"
		}
		switch kind {
		case flagSwitch:
			if attached {
				return out, "the command passes " + name + " with a value"
			}
			out.flags[name] = w
		case flagSwitchValue:
			if attached {
				if why := s.checkSwitchValue(w, name); why != "" {
					return out, why
				}
			}
			out.flags[name] = w
		default:
			if attached {
				return out, "the command attaches a value to " + name + " with '='; pass the flag and its value as separate words"
			}
			if i+1 >= len(words) || words[i+1].redirect {
				return out, "the command passes " + name + " with no value"
			}
			i++
			if why := s.checkValue(words[i], kind, name); why != "" {
				return out, why
			}
			out.flags[name] = words[i]
		}
	}
	return out, ""
}

// checkSwitchValue checks the value after '=' of a switch that may carry
// one: true, false, or one double-quoted env name that carries no
// credential.
func (s templateStep) checkSwitchValue(w templateWord, name string) string {
	if lit, ok := w.literal(); ok {
		_, value, _ := strings.Cut(lit, "=")
		if value == "true" || value == "false" {
			return ""
		}
		return name + " carries " + quote(value) + ", which is neither true nor false"
	}
	if len(w.parts) == 2 && w.parts[0].text == name+"=" && w.parts[1].kind == partParam {
		return s.textParam(w.parts[1].text, name)
	}
	return name + " carries " + quote(w.raw) + ", which is neither true, false, nor one env name"
}

// checkValue checks one flag value by its kind.
func (s templateStep) checkValue(w templateWord, kind flagKind, name string) string {
	switch kind {
	case flagText:
		return s.checkText(w, name)
	case flagLiteral:
		if _, ok := w.literal(); !ok {
			return name + " takes literal text, and " + quote(w.raw) + " expands"
		}
	case flagPath:
		return checkPath(w)
	case flagRepo:
		return checkRepo(w)
	}
	return ""
}

// checkText accepts literal text and double-quoted env names that carry no
// credential.
func (s templateStep) checkText(w templateWord, name string) string {
	for _, p := range w.parts {
		if p.kind != partParam {
			continue
		}
		if why := s.textParam(p.text, name); why != "" {
			return why
		}
	}
	return ""
}

// textParam accepts an env name a text value may read: a template env name
// that carries no credential.
func (s templateStep) textParam(param, name string) string {
	env, err := ParseTemplateEnv(param)
	if err != nil {
		return name + " reads " + quote(param) + ", which is not one of the credential step env names (" + closedset.Join(AllTemplateEnvs, ", ") + ")"
	}
	if env.carriesCredential() {
		return name + " reads the credential carrier " + env.String() + ", which would publish the credential"
	}
	return ""
}

// pathWord matches a literal relative file path.
var pathWord = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_./-]*$`)

func checkPath(w templateWord) string {
	lit, ok := w.literal()
	if !ok || !pathWord.MatchString(lit) || strings.Contains(lit, "..") {
		return "the file " + quote(w.raw) + " is not a literal relative path"
	}
	return ""
}

// repoWord matches a literal owner/name.
var repoWord = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

func checkRepo(w templateWord) string {
	if lit, ok := w.literal(); ok && repoWord.MatchString(lit) {
		return ""
	}
	if len(w.parts) == 1 && w.parts[0].kind == partParam && w.parts[0].text == runnerEnvRepository {
		return ""
	}
	return "the repository " + quote(w.raw) + " is not owner/name or \"$" + runnerEnvRepository + "\""
}

// prNumber matches a literal PR number.
var prNumber = regexp.MustCompile(`^[1-9][0-9]*$`)

// checkPRSelector accepts the job's own PR: its own bot branch or a PR
// number, literal, or the PR_NUMBER env name mapped from one step or job
// output. A value from env comes after "--", so gh never reads it as a
// flag.
func (s templateStep) checkPRSelector(w templateWord, guarded bool) string {
	if lit, ok := w.literal(); ok {
		if lit == s.branch || prNumber.MatchString(lit) {
			return ""
		}
		return "the PR " + quote(lit) + " is neither the job's own bot branch " + quote(s.branch) + " nor a PR number"
	}
	if len(w.parts) == 1 && w.parts[0].kind == partParam && w.parts[0].text == TemplateEnvPRNumber.String() {
		if _, _, ok := resolveOutput(s.doc, s.job, soleExpression(s.env[TemplateEnvPRNumber.String()])); !ok {
			return "the PR reads " + TemplateEnvPRNumber.String() + ", which the env does not map from exactly one step output or one output of a job this job needs"
		}
		if !guarded {
			return "the PR reads " + TemplateEnvPRNumber.String() + " before \"" + endOfOptions + "\", so a value that starts with '-' would be read as a flag; pass " + endOfOptions + " \"${" + TemplateEnvPRNumber.String() + "}\" last"
		}
		return ""
	}
	return "the PR " + quote(w.raw) + " is not the job's own bot branch, a PR number, or \"${" + TemplateEnvPRNumber.String() + "}\" mapped from an output"
}

func (s templateStep) matchPRCreate(words []templateWord) string {
	f, why := s.parseFlags(words, flagSpec{
		ghFlagHead: flagLiteral, "--base": flagLiteral, "-B": flagLiteral,
		"--title": flagText, "-t": flagText, "--body": flagText, "-b": flagText,
	})
	if why != "" {
		return why
	}
	if len(f.positional) != 0 {
		return "gh pr create takes no positional words"
	}
	head, ok := f.flags[ghFlagHead]
	if !ok {
		return "gh pr create names no " + ghFlagHead + ", so the head branch is whatever is checked out"
	}
	if lit, _ := head.literal(); lit != s.branch {
		return "gh pr create opens from " + quote(head.raw) + ", not the job's own bot branch " + quote(s.branch)
	}
	return ""
}

func (s templateStep) matchPRSelectorCommand(words []templateWord, spec flagSpec) string {
	f, why := s.parseFlags(words, spec)
	if why != "" {
		return why
	}
	if len(f.positional) != 1 {
		return "the command names " + strconv.Itoa(len(f.positional)) + " PRs, and the template takes exactly the job's own PR"
	}
	return s.checkPRSelector(f.positional[0], f.guarded[0])
}

func (s templateStep) matchPRMerge(words []templateWord) string {
	f, why := s.parseFlags(words, flagSpec{
		ghFlagAuto: flagSwitch, ghFlagDisableAuto: flagSwitch,
		"--squash": flagSwitch, "-s": flagSwitch, "--merge": flagSwitch, "-m": flagSwitch, "--rebase": flagSwitch, "-r": flagSwitch,
	})
	if why != "" {
		return why
	}
	_, auto := f.flags[ghFlagAuto]
	_, disable := f.flags[ghFlagDisableAuto]
	methods := len(f.flags) - btoi(auto) - btoi(disable)
	switch {
	case auto == disable:
		return "gh pr merge passes neither or both of " + ghFlagAuto + " and " + ghFlagDisableAuto + "; a merge without " + ghFlagAuto + " would land the PR outside the merge queue"
	case disable && methods > 0:
		return "gh pr merge " + ghFlagDisableAuto + " passes a merge method"
	case methods > 1:
		return "gh pr merge passes more than one merge method"
	}
	if len(f.positional) != 1 {
		return "gh pr merge names " + strconv.Itoa(len(f.positional)) + " PRs, and the template takes exactly the job's own PR"
	}
	return s.checkPRSelector(f.positional[0], f.guarded[0])
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// graphQLFieldName matches a GraphQL variable name.
var graphQLFieldName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// graphQLOperation matches the leading keyword of a GraphQL document.
var graphQLOperation = regexp.MustCompile(`^\s*query\b`)

// graphQLWrite matches an operation keyword that writes or streams.
var graphQLWrite = regexp.MustCompile(`(?i)\b(mutation|subscription)\b`)

func (s templateStep) matchQueueRead(words []templateWord) string {
	query := ""
	for i := 0; i < len(words); i++ {
		w := words[i]
		if w.redirect {
			if i+2 != len(words) {
				return "the queue read redirects to more than one file"
			}
			if why := checkPath(words[i+1]); why != "" {
				return why
			}
			break
		}
		lit, _ := w.literal()
		raw := false
		switch lit {
		case "-f", "--raw-field":
			raw = true
		case "-F", "--field":
		default:
			return "the queue read passes " + quote(w.raw) + "; it takes only -F/-f name=value fields and one stdout redirection"
		}
		if i+1 >= len(words) {
			return "the queue read passes " + lit + " with no field"
		}
		i++
		field, ok := words[i].literal()
		if !ok {
			return "the queue read field " + quote(words[i].raw) + " expands; GraphQL fields are literal text (gh fills {owner} and {repo})"
		}
		name, value, hasValue := strings.Cut(field, "=")
		if !hasValue || !graphQLFieldName.MatchString(name) {
			return "the queue read field " + quote(field) + " is not name=value"
		}
		if !raw && strings.HasPrefix(value, "@") {
			return "the queue read field " + name + " reads a file"
		}
		if name == graphQLQueryField {
			if !raw {
				return "the queue read passes its query with -F, which can read it from a file; pass -f query='...'"
			}
			if query != "" {
				return "the queue read passes two queries"
			}
			query = value
		}
	}
	switch {
	case query == "":
		return "the queue read passes no -f query='...'"
	case !graphQLOperation.MatchString(query) || graphQLWrite.MatchString(query):
		return "the GraphQL document is not one query: it must open with 'query' and name no mutation or subscription"
	case !strings.Contains(query, graphQLMergeQueue):
		return "the GraphQL query does not read " + graphQLMergeQueue
	}
	return ""
}

// matchRelease checks a gh release view, edit, or create on the triggering
// tag. create additionally requires --verify-tag, so gh refuses to create a
// tag that does not exist.
func (s templateStep) matchRelease(words []templateWord, spec flagSpec, create bool) string {
	f, why := s.parseFlags(words, spec)
	if why != "" {
		return why
	}
	if len(f.positional) != 1 {
		return "the command names " + strconv.Itoa(len(f.positional)) + " tags, and the template takes exactly the triggering tag"
	}
	if create {
		if _, ok := f.flags[ghFlagVerifyTag]; !ok {
			return "gh release create does not pass " + ghFlagVerifyTag + ", so it would create the tag when it is missing"
		}
	}
	if !s.triggeringTag(f.positional[0]) {
		return "the tag " + quote(f.positional[0].raw) + " is not the triggering tag: pass \"$" + runnerEnvRefName + "\", or \"${" + TemplateEnvTag.String() + "}\" with " + TemplateEnvTag.String() + " mapped from ${{ " + triggeringRefName + " }}"
	}
	return ""
}

// triggeringTag accepts the tag that triggered the workflow:
// "$GITHUB_REF_NAME" when no env overrides it, or "$TAG" with TAG mapped
// from ${{ github.ref_name }} in the step, job or workflow env. The
// workflow runs only on a tag under the release prefix, so the value never
// starts with '-'.
func (s templateStep) triggeringTag(w templateWord) bool {
	if len(w.parts) != 1 || w.parts[0].kind != partParam {
		return false
	}
	switch name := w.parts[0].text; name {
	case runnerEnvRefName:
		_, overridden := s.env[runnerEnvRefName]
		return !overridden
	case TemplateEnvTag.String():
		return soleExpression(s.env[name]) == triggeringRefName
	}
	return false
}
