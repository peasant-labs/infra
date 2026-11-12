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
// planner's tag and candidate outputs, a PR number from a step output, the
// triggering tag, and quoted title or body text. The zero value is unknown.
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
	// TemplateQueueRead reads the merge queue through one GraphQL query.
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
		return `git [-c http.extraheader="AUTHORIZATION: bearer ${` + TemplateEnvAppToken.String() + `}"] push origin "[<candidate>:]` + intent.TagRefPrefix + `<tag>"`
	case TemplatePRCreate:
		return `gh pr create --head <own bot branch> [--base <branch>] [--title <text>] [--body <text>]`
	case TemplatePREdit:
		return `gh pr edit <own PR> [--title <text>] [--body <text>]`
	case TemplatePRClose:
		return `gh pr close <own PR> [--comment <text>]`
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

// templateStep is what a template needs to know about the step it judges.
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
	cmdGit             = "git"
	cmdGH              = "gh"
	gitConfigOption    = "-c"
	gitPush            = "push"
	gitRemote          = "origin"
	headRef            = "HEAD"
	authHeaderKey      = "http.extraheader="
	authHeaderValue    = "authorization: bearer "
	ghPR               = "pr"
	ghAPI              = "api"
	ghGraphQL          = "graphql"
	ghRelease          = "release"
	graphQLQueryField  = "query"
	graphQLMergeQueue  = "mergeQueue"
	ghVerbCreate       = "create"
	ghVerbEdit         = "edit"
	ghVerbClose        = "close"
	ghVerbMerge        = "merge"
	ghVerbView         = "view"
	ghFlagHead         = "--head"
	ghFlagVerifyTag    = "--verify-tag"
	ghFlagAuto         = "--auto"
	ghFlagDisableAuto  = "--disable-auto"
	gitFlagForceLease  = "--force-with-lease"
	gitFlagForce       = "--force"
	gitFlagNoVerify    = "--no-verify"
	triggeringRefName  = "github.ref_name"
	contextRepository  = "github.repository"
	stepOutputsSegment = ".outputs."
)

// classifyTemplate names the template a command's leading words select,
// without checking its parts: the push form by its last word, a gh command
// by its subcommand. ok is false for any other command.
func classifyTemplate(words []templateWord) (CredentialTemplate, bool) {
	lit := func(i int) string {
		if i >= len(words) {
			return ""
		}
		s, _ := words[i].literal()
		return s
	}
	switch lit(0) {
	case cmdGit:
		i := 1
		if lit(i) == gitConfigOption {
			i += 2
		}
		if lit(i) != gitPush {
			return "", false
		}
		last := words[len(words)-1]
		if strings.Contains(last.raw, intent.TagRefPrefix) {
			return TemplatePushReleaseTag, true
		}
		return TemplatePushOwnBranch, true
	case cmdGH:
		switch lit(1) {
		case ghPR:
			switch lit(2) {
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
			if lit(2) == ghGraphQL {
				return TemplateQueueRead, true
			}
		case ghRelease:
			switch lit(2) {
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
// not one command or selects no template.
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

// matchPush checks a git push: the optional authorization header, the
// switches, the remote, and the one refspec.
func (s templateStep) matchPush(words []templateWord, tmpl CredentialTemplate) string {
	i := 1
	if lit, _ := words[1].literal(); lit == gitConfigOption {
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

// releaseTagSpec checks a tag refspec: an optional candidate source, then
// the tag ref of the planner's tag output.
func (s templateStep) releaseTagSpec(w templateWord) string {
	parts := w.parts
	bad := "the tag refspec " + quote(w.raw) + " is not [<candidate>:]" + intent.TagRefPrefix + "<tag> with both read from the planner's outputs"
	switch {
	case len(parts) == 2 && parts[0].kind == partLiteral && parts[0].text == intent.TagRefPrefix:
		return s.plannerOutput(parts[1], intent.OutputTag, TemplateEnvTag, bad)
	case len(parts) == 3 && parts[1].kind == partLiteral && parts[1].text == ":"+intent.TagRefPrefix:
		if why := s.plannerOutput(parts[0], intent.OutputCandidate, TemplateEnvCandidate, bad); why != "" {
			return why
		}
		return s.plannerOutput(parts[2], intent.OutputTag, TemplateEnvTag, bad)
	}
	return bad
}

// plannerOutputExpr matches one step output read, alone, and captures the
// step id and the output key.
var plannerOutputExpr = regexp.MustCompile(`^steps\.(` + stepIDPattern + `)\.outputs\.([A-Za-z0-9_-]+)$`)

// plannerOutput checks one part that must carry a planner output: the
// expression ${{ steps.<plan>.outputs.<key> }} directly, or the env name
// mapped from exactly that expression, where <plan> is a step of this job
// that runs the planner.
func (s templateStep) plannerOutput(p wordPart, key intent.OutputKey, env TemplateEnv, bad string) string {
	expr := ""
	switch p.kind {
	case partExpr:
		expr = p.text
	case partParam:
		if p.text != env.String() {
			return bad + ": it reads " + quote(p.text) + " where the template reads " + env.String()
		}
		expr = soleExpression(s.env[env.String()])
	default:
		return bad
	}
	m := plannerOutputExpr.FindStringSubmatch(expr)
	if m == nil || m[2] != key.String() {
		return bad + ": " + quote(expr) + " is not " + stepOutput("<plan>", key)
	}
	for _, step := range stepsOf(s.job) {
		if stepID(step) == m[1] && planRunner.MatchString(stepRunText(step)) {
			return ""
		}
	}
	return bad + ": steps." + m[1] + " is not a step of this job that runs " + intent.ProgramName + " " + intent.CommandPlan.String()
}

// soleExpression returns the body of a value that is exactly one ${{ }}
// expression, or "".
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
// leading words.
type parsedFlags struct {
	flags      map[string]templateWord
	positional []templateWord
	redirect   *templateWord
}

// parseFlags reads the words after a gh subcommand against the template's
// flag spec: each flag at most once, every value checked by its kind, no
// flag outside the spec, and a redirection only when allowed (followed by
// one path).
func (s templateStep) parseFlags(words []templateWord, spec flagSpec, allowRedirect bool) (parsedFlags, string) {
	out := parsedFlags{flags: map[string]templateWord{}}
	for i := 0; i < len(words); i++ {
		w := words[i]
		if w.redirect {
			if !allowRedirect || i+2 != len(words) {
				return out, "the command redirects its output, which the template does not allow there"
			}
			target := words[i+1]
			if why := checkPath(target); why != "" {
				return out, why
			}
			out.redirect = &target
			return out, ""
		}
		lead := ""
		if len(w.parts) > 0 && w.parts[0].kind == partLiteral {
			lead = w.parts[0].text
		}
		if !strings.HasPrefix(lead, "-") {
			out.positional = append(out.positional, w)
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
// credential. A GitHub expression is refused: it is pasted into the script
// before the shell runs, so its value could end the quote.
func (s templateStep) checkText(w templateWord, name string) string {
	for _, p := range w.parts {
		switch p.kind {
		case partExpr:
			return name + " interpolates a GitHub expression into the script; map it to " + TemplateEnvTitle.String() + " or " + TemplateEnvBody.String() + " in the step env and pass \"${NAME}\""
		case partParam:
			if why := s.textParam(p.text, name); why != "" {
				return why
			}
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
	if len(w.parts) == 1 {
		p := w.parts[0]
		if (p.kind == partParam && p.text == runnerEnvRepository) || (p.kind == partExpr && p.text == contextRepository) {
			return ""
		}
	}
	return "the repository " + quote(w.raw) + " is not owner/name, \"$" + runnerEnvRepository + "\", or ${{ " + contextRepository + " }}"
}

// prNumber matches a literal PR number.
var prNumber = regexp.MustCompile(`^[1-9][0-9]*$`)

// checkPRSelector accepts the job's own PR: its own bot branch or a PR
// number, literal, or the PR_NUMBER env name mapped from one step output.
func (s templateStep) checkPRSelector(w templateWord) string {
	if lit, ok := w.literal(); ok {
		if lit == s.branch || prNumber.MatchString(lit) {
			return ""
		}
		return "the PR " + quote(lit) + " is neither the job's own bot branch " + quote(s.branch) + " nor a PR number"
	}
	if len(w.parts) == 1 && w.parts[0].kind == partParam && w.parts[0].text == TemplateEnvPRNumber.String() {
		if plannerOutputExpr.MatchString(soleExpression(s.env[TemplateEnvPRNumber.String()])) {
			return ""
		}
		return "the PR reads " + TemplateEnvPRNumber.String() + ", which the env does not map from exactly one step output"
	}
	return "the PR " + quote(w.raw) + " is not the job's own bot branch, a PR number, or \"${" + TemplateEnvPRNumber.String() + "}\" mapped from a step output"
}

func (s templateStep) matchPRCreate(words []templateWord) string {
	f, why := s.parseFlags(words, flagSpec{
		ghFlagHead: flagLiteral, "--base": flagLiteral, "-B": flagLiteral,
		"--title": flagText, "-t": flagText, "--body": flagText, "-b": flagText,
	}, false)
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
	f, why := s.parseFlags(words, spec, false)
	if why != "" {
		return why
	}
	if len(f.positional) != 1 {
		return "the command names " + strconv.Itoa(len(f.positional)) + " PRs, and the template takes exactly the job's own PR"
	}
	return s.checkPRSelector(f.positional[0])
}

func (s templateStep) matchPRMerge(words []templateWord) string {
	f, why := s.parseFlags(words, flagSpec{
		ghFlagAuto: flagSwitch, ghFlagDisableAuto: flagSwitch,
		"--squash": flagSwitch, "-s": flagSwitch, "--merge": flagSwitch, "-m": flagSwitch, "--rebase": flagSwitch, "-r": flagSwitch,
	}, false)
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
	return s.checkPRSelector(f.positional[0])
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
	f, why := s.parseFlags(words, spec, false)
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
		return "the tag " + quote(f.positional[0].raw) + " is not the triggering tag: pass \"$" + runnerEnvRefName + "\", ${{ " + triggeringRefName + " }}, or \"${" + TemplateEnvTag.String() + "}\" with " + TemplateEnvTag.String() + " mapped from ${{ " + triggeringRefName + " }}"
	}
	return ""
}

// triggeringTag accepts the tag that triggered the workflow:
// ${{ github.ref_name }}, "$GITHUB_REF_NAME", or "$TAG" with TAG mapped from
// ${{ github.ref_name }} in the step, job or workflow env.
func (s templateStep) triggeringTag(w templateWord) bool {
	if len(w.parts) != 1 {
		return false
	}
	p := w.parts[0]
	switch p.kind {
	case partExpr:
		return p.text == triggeringRefName
	case partParam:
		if p.text == runnerEnvRefName {
			_, overridden := s.env[runnerEnvRefName]
			return !overridden
		}
		return p.text == TemplateEnvTag.String() && soleExpression(s.env[TemplateEnvTag.String()]) == triggeringRefName
	}
	return false
}
