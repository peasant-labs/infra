package policy

import (
	"slices"
	"strings"

	"github.com/peasant-labs/infra/tools/release/internal/closedset"
	regexp "github.com/wasilibs/go-re2"
	"gopkg.in/yaml.v3"
)

// This file is the credential model the four credential rules share: the
// closed sets they compare against, which jobs may hold a credential, and
// which expressions read one.
//
// A credential is anything that can write a ref: the App token a
// create-github-app-token step mints and every output of that step, the
// secrets a mint step reads, and the default workflow token
// (github.token, secrets.GITHUB_TOKEN) in a job whose permissions let it
// write. The default token is read-only everywhere else, because the
// default-token-read-only rule holds every other job to read scopes.

// PermissionLevel is the access a workflow permissions entry grants one
// scope. The zero value is unknown.
type PermissionLevel string

const (
	// PermissionRead grants read access.
	PermissionRead PermissionLevel = "read"
	// PermissionWrite grants write access.
	PermissionWrite PermissionLevel = "write"
	// PermissionNone grants no access.
	PermissionNone PermissionLevel = "none"
)

// AllPermissionLevels is the canonical closed set of permission levels.
var AllPermissionLevels = []PermissionLevel{PermissionRead, PermissionWrite, PermissionNone}

func (l PermissionLevel) String() string { return string(l) }

// ParsePermissionLevel maps a level's spelling to its member and refuses
// anything else.
func ParsePermissionLevel(raw string) (PermissionLevel, error) {
	return closedset.Parse(AllPermissionLevels, raw, closedset.Context{
		Set:   "permission level",
		Where: "policy.ParsePermissionLevel",
		When:  "reading a workflow or job permissions entry during merge-queue policy validation",
		Means: "a level the checker cannot read may grant write access, so the entry is rejected",
	})
}

// PermissionShorthand is a permissions value that names every scope at
// once. The zero value is unknown.
type PermissionShorthand string

const (
	// PermissionReadAll grants read access to every scope.
	PermissionReadAll PermissionShorthand = "read-all"
	// PermissionWriteAll grants write access to every scope.
	PermissionWriteAll PermissionShorthand = "write-all"
)

// AllPermissionShorthands is the canonical closed set of shorthands.
var AllPermissionShorthands = []PermissionShorthand{PermissionReadAll, PermissionWriteAll}

func (s PermissionShorthand) String() string { return string(s) }

// ParsePermissionShorthand maps a shorthand's spelling to its member and
// refuses anything else.
func ParsePermissionShorthand(raw string) (PermissionShorthand, error) {
	return closedset.Parse(AllPermissionShorthands, raw, closedset.Context{
		Set:   "permissions shorthand",
		Where: "policy.ParsePermissionShorthand",
		When:  "reading a workflow or job permissions value during merge-queue policy validation",
		Means: "a value the checker cannot read may grant write access, so the entry is rejected",
	})
}

// WritableScope is a permissions scope that may be granted write, each under
// one condition. Every other scope is read or none. The zero value is
// unknown.
type WritableScope string

const (
	// WritableScopeIDToken lets a job request an OIDC token, which publishes
	// to a registry and writes no ref. It may be write in any job.
	WritableScopeIDToken WritableScope = "id-token"
	// WritableScopeContents lets the default token write refs and Releases.
	// It may be write only in the tag-triggered publish workflow, whose
	// credential steps run only the publish templates.
	WritableScopeContents WritableScope = "contents"
)

// AllWritableScopes is the canonical closed set of writable scopes.
var AllWritableScopes = []WritableScope{WritableScopeIDToken, WritableScopeContents}

func (s WritableScope) String() string { return string(s) }

// ParseWritableScope maps a scope's spelling to its member and refuses every
// scope that may never be write.
func ParseWritableScope(raw string) (WritableScope, error) {
	return closedset.Parse(AllWritableScopes, raw, closedset.Context{
		Set:   "writable permissions scope",
		Where: "policy.ParseWritableScope",
		When:  "reading a write grant in a workflow or job permissions entry during merge-queue policy validation",
		Means: "only the listed scopes may be write, so the grant is rejected",
	})
}

// writeAllowed reports whether the scope may be write in a workflow:
// id-token anywhere, contents only in the tag-triggered publish workflow.
func (s WritableScope) writeAllowed(publish bool) bool {
	switch s {
	case WritableScopeIDToken:
		return true
	case WritableScopeContents:
		return publish
	}
	return false
}

// AllowedAction is an action a job that may hold a credential may use, each
// pinned to a 40-hex commit. Every other action is refused in such a job:
// it would run code the checker cannot read with the credential in reach.
// The zero value is unknown.
type AllowedAction string

const (
	// ActionCheckout checks out a repository.
	ActionCheckout AllowedAction = "actions/checkout"
	// ActionSetupGo installs Go.
	ActionSetupGo AllowedAction = "actions/setup-go"
	// ActionSetupNode installs Node.
	ActionSetupNode AllowedAction = "actions/setup-node"
	// ActionCreateAppToken mints the releaser App token.
	ActionCreateAppToken AllowedAction = "actions/create-github-app-token"
	// ActionUploadArtifact keeps a file as a run artifact.
	ActionUploadArtifact AllowedAction = "actions/upload-artifact"
	// ActionSetupPnpm installs pnpm.
	ActionSetupPnpm AllowedAction = "pnpm/action-setup"
	// ActionInstallNix installs Nix.
	ActionInstallNix AllowedAction = "cachix/install-nix-action"
)

// AllAllowedActions is the canonical closed set of allowed actions.
var AllAllowedActions = []AllowedAction{
	ActionCheckout,
	ActionSetupGo,
	ActionSetupNode,
	ActionCreateAppToken,
	ActionUploadArtifact,
	ActionSetupPnpm,
	ActionInstallNix,
}

func (a AllowedAction) String() string { return string(a) }

// ParseAllowedAction maps an action name (owner/repo, no ref) to its member
// and refuses every other action.
func ParseAllowedAction(raw string) (AllowedAction, error) {
	return closedset.Parse(AllAllowedActions, raw, closedset.Context{
		Set:   "allowed action",
		Where: "policy.ParseAllowedAction",
		When:  "reading a uses: step in a job that may hold a credential during merge-queue policy validation",
		Means: "the action runs code the checker cannot read while a credential is in reach, so the step is rejected",
	})
}

// actionPinSHA matches the 40-hex commit an allowed action is pinned to.
var actionPinSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// usesAction splits a uses: value into the action and its ref. ok is false
// for a value that names no action@ref.
func usesAction(uses string) (name, ref string, ok bool) {
	name, ref, ok = strings.Cut(uses, "@")
	return name, ref, ok && name != "" && ref != ""
}

// isAction reports whether a uses: value runs the given action at any ref.
func isAction(uses string, action AllowedAction) bool {
	name, _, ok := usesAction(uses)
	return ok && name == action.String()
}

// TemplateEnv is a name a credential step's environment may carry. A
// credential step runs one template, so its environment holds only the
// credential the template uses and the values its typed parts read; any
// other name (BASH_ENV, PATH, GIT_*, a proxy) could change what the one
// command does. The zero value is unknown.
type TemplateEnv string

const (
	// TemplateEnvGHToken carries a credential to gh.
	TemplateEnvGHToken TemplateEnv = "GH_TOKEN"
	// TemplateEnvGitHubToken carries a credential to gh.
	TemplateEnvGitHubToken TemplateEnv = "GITHUB_TOKEN"
	// TemplateEnvAppToken carries a credential to the git authorization
	// header of a push template.
	TemplateEnvAppToken TemplateEnv = "APP_TOKEN"
	// TemplateEnvTag carries the planner's tag output, or the triggering tag
	// in the publish workflow.
	TemplateEnvTag TemplateEnv = "TAG"
	// TemplateEnvCandidate carries the planner's candidate output.
	TemplateEnvCandidate TemplateEnv = "CANDIDATE"
	// TemplateEnvPRNumber carries a PR number from a step output.
	TemplateEnvPRNumber TemplateEnv = "PR_NUMBER"
	// TemplateEnvTitle carries a PR or Release title.
	TemplateEnvTitle TemplateEnv = "TITLE"
	// TemplateEnvBody carries a PR body or Release notes.
	TemplateEnvBody TemplateEnv = "BODY"
	// TemplateEnvPrerelease carries the Release prerelease flag value.
	TemplateEnvPrerelease TemplateEnv = "PRERELEASE"
	// TemplateEnvLatest carries the Release latest flag value.
	TemplateEnvLatest TemplateEnv = "LATEST"
)

// AllTemplateEnvs is the canonical closed set of credential-step env names.
var AllTemplateEnvs = []TemplateEnv{
	TemplateEnvGHToken,
	TemplateEnvGitHubToken,
	TemplateEnvAppToken,
	TemplateEnvTag,
	TemplateEnvCandidate,
	TemplateEnvPRNumber,
	TemplateEnvTitle,
	TemplateEnvBody,
	TemplateEnvPrerelease,
	TemplateEnvLatest,
}

func (e TemplateEnv) String() string { return string(e) }

// ParseTemplateEnv maps an env name to its member and refuses every other
// name.
func ParseTemplateEnv(raw string) (TemplateEnv, error) {
	return closedset.Parse(AllTemplateEnvs, raw, closedset.Context{
		Set:   "credential step env name",
		Where: "policy.ParseTemplateEnv",
		When:  "reading the environment of a step that receives a credential during merge-queue policy validation",
		Means: "a name outside the set can change what the step's one command does, so the step is rejected",
	})
}

// carriesCredential reports whether the name may carry a credential.
func (e TemplateEnv) carriesCredential() bool {
	return e == TemplateEnvGHToken || e == TemplateEnvGitHubToken || e == TemplateEnvAppToken
}

// The environment names the runner sets that a template's typed parts may
// read: the repository and the triggering ref's short name.
const (
	runnerEnvRepository = "GITHUB_REPOSITORY"
	runnerEnvRefName    = "GITHUB_REF_NAME"
)

// credentialJobClass is why a job may hold a credential. The zero value is
// a job that may hold none.
type credentialJobClass string

const (
	// credentialJobRelease is the policy's release job.
	credentialJobRelease credentialJobClass = "release job"
	// credentialJobBot is a bot job the policy lists.
	credentialJobBot credentialJobClass = "bot job"
	// credentialJobPublish is a job of the tag-triggered publish workflow
	// whose default token may write contents.
	credentialJobPublish credentialJobClass = "tag-triggered publish job"
)

// allCredentialJobClasses is the canonical closed set of job classes.
var allCredentialJobClasses = []credentialJobClass{credentialJobRelease, credentialJobBot, credentialJobPublish}

func (k credentialJobClass) String() string { return string(k) }

// credentialJob is one job's place in the credential model.
type credentialJob struct {
	class credentialJobClass
	// branch is the bot branch a release or bot job owns, from the release
	// config.
	branch string
	// tokenWrites tells whether the job's default token may write, so that
	// github.token and secrets.GITHUB_TOKEN are credentials in it.
	tokenWrites bool
	// mints holds the ids of the job's mint steps.
	mints []string
}

// mintSecrets returns the secret names every mint step in the directory
// reads, upper-cased. Such a secret mints a token, so it is a credential
// wherever it is read.
func (c *queueChecker) mintSecrets() map[string]bool {
	out := map[string]bool{}
	for _, name := range c.sortedNames() {
		f := c.files[name]
		if f.doc == nil {
			continue
		}
		for _, jobName := range jobNames(f.doc) {
			for _, step := range stepsOf(jobNode(f.doc, jobName)) {
				if !isMintStep(step) {
					continue
				}
				for _, s := range nodeScalars(mappingValueOrEmpty(step, "with")) {
					for _, m := range secretRead.FindAllStringSubmatch(s, -1) {
						out[strings.ToUpper(m[1])] = true
					}
				}
			}
		}
	}
	return out
}

// secretRead matches a read of one named secret.
var secretRead = regexp.MustCompile(`(?i)\bsecrets\.([A-Za-z_][A-Za-z0-9_]*)`)

// mappingValueOrEmpty returns the value under key, or an empty mapping.
func mappingValueOrEmpty(node *yaml.Node, key string) *yaml.Node {
	if v := mappingValue(node, key); v != nil {
		return v
	}
	return &yaml.Node{Kind: yaml.MappingNode}
}

// isTagPublishWorkflow reports whether the workflow runs only on a pushed
// release tag (see tagOnlyTriggerFault).
func (c *queueChecker) isTagPublishWorkflow(doc *yaml.Node) bool {
	return tagOnlyTriggerFault(doc, c.cfg.TagPrefix) == ""
}

// effectivePermissions returns the permissions node that applies to a job:
// its own, or the workflow's.
func effectivePermissions(doc, job *yaml.Node) *yaml.Node {
	if p := mappingValue(job, "permissions"); p != nil {
		return p
	}
	return mappingValue(doc, "permissions")
}

// tokenMayWrite reports whether a permissions value lets the default token
// write anything but an OIDC token. A missing or unreadable value may: the
// repository default can be write.
func tokenMayWrite(perms *yaml.Node) bool {
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
			if err != nil {
				return true
			}
			if level == PermissionWrite && perms.Content[i].Value != WritableScopeIDToken.String() {
				return true
			}
		}
		return false
	}
	return true
}

// credentialJobs places every job of every workflow in the credential model,
// keyed by file then job. Branches come from resolvePushes; a job the policy
// lists in a file that does not exist places nothing.
func (c *queueChecker) credentialJobs(branches []allowedPush) map[string]map[string]credentialJob {
	listed := map[string]map[string]allowedPush{}
	for _, a := range branches {
		if listed[a.workflow] == nil {
			listed[a.workflow] = map[string]allowedPush{}
		}
		listed[a.workflow][a.job] = a
	}
	out := map[string]map[string]credentialJob{}
	for _, name := range c.sortedNames() {
		f := c.files[name]
		if f.doc == nil {
			continue
		}
		out[name] = map[string]credentialJob{}
		publish := c.isTagPublishWorkflow(f.doc)
		for _, jobName := range jobNames(f.doc) {
			job := jobNode(f.doc, jobName)
			cj := credentialJob{tokenWrites: tokenMayWrite(effectivePermissions(f.doc, job))}
			for _, step := range stepsOf(job) {
				if isMintStep(step) {
					cj.mints = append(cj.mints, stepID(step))
				}
			}
			if a, ok := listed[name][jobName]; ok {
				cj.class, cj.branch = a.class, a.branch
			} else if publish && cj.tokenWrites {
				cj.class = credentialJobPublish
			}
			out[name][jobName] = cj
		}
	}
	return out
}

// credentialReader finds the credentials an expression reads in one job.
type credentialReader struct {
	secrets map[string]bool
	job     credentialJob
}

// contextRead matches a context name at an expression word boundary and
// what follows it: a property name, or nothing a property read would have
// (a bracket, a call argument, the end), which reads the whole object.
var contextRead = regexp.MustCompile(`(?:^|[^a-z0-9_.-])(github|secrets|steps)(\.[a-z0-9_-]+)?`)

// in returns the credential the expression reads, or "" when it reads none.
// Expressions are case-insensitive, so the text is lowered and its spaces
// dropped first. A whole-object read (a bracket index, toJSON(secrets))
// counts as reading every credential the object holds.
func (r credentialReader) in(expr string) string {
	e := strings.ToLower(strings.Join(strings.Fields(expr), ""))
	for _, m := range contextRead.FindAllStringSubmatchIndex(e, -1) {
		ctx := e[m[2]:m[3]]
		prop := ""
		if m[4] >= 0 {
			prop = e[m[4]+1 : m[5]]
		}
		switch ctx {
		case "github":
			if r.job.tokenWrites && (prop == "" || prop == "token") {
				return "github.token"
			}
		case "secrets":
			switch {
			case prop == "":
				return "the whole secrets context"
			case r.secrets[strings.ToUpper(prop)]:
				return "secrets." + strings.ToUpper(prop)
			case prop == "github_token" && r.job.tokenWrites:
				return "secrets.GITHUB_TOKEN"
			}
		case "steps":
			if len(r.job.mints) == 0 {
				continue
			}
			if prop == "" {
				return "the whole steps context, which holds the minted App token"
			}
			for _, id := range r.job.mints {
				if strings.ToLower(id) == prop {
					return "steps." + id + " (the minted App token)"
				}
			}
		}
	}
	return ""
}

// inText returns the first credential any expression in the text reads.
func (r credentialReader) inText(text string) string {
	for _, expr := range expressionBodies(text) {
		if cred := r.in(expr); cred != "" {
			return cred
		}
	}
	return ""
}

// inNode returns the first credential any scalar under the node reads.
func (r credentialReader) inNode(n *yaml.Node) string {
	if n == nil {
		return ""
	}
	for _, s := range nodeScalars(n) {
		if cred := r.inText(s); cred != "" {
			return cred
		}
	}
	return ""
}

// expressionBodies returns the body of every ${{ }} in the text. An
// unterminated expression runs to the end of the text.
func expressionBodies(text string) []string {
	var out []string
	for {
		start := strings.Index(text, "${{")
		if start < 0 {
			return out
		}
		rest := text[start+3:]
		end := strings.Index(rest, "}}")
		if end < 0 {
			return append(out, rest)
		}
		out = append(out, rest[:end])
		text = rest[end+2:]
	}
}

// envEntries returns a node's env mapping as ordered name/value pairs.
func envEntries(node *yaml.Node) [][2]string {
	env := mappingValue(node, "env")
	if env == nil || env.Kind != yaml.MappingNode {
		return nil
	}
	var out [][2]string
	for i := 0; i+1 < len(env.Content); i += 2 {
		out = append(out, [2]string{env.Content[i].Value, env.Content[i+1].Value})
	}
	return out
}

// effectiveEnv merges the workflow, job and step env, the step winning, as
// the runner does.
func effectiveEnv(doc, job, step *yaml.Node) map[string]string {
	out := map[string]string{}
	for _, scope := range []*yaml.Node{doc, job, step} {
		for _, kv := range envEntries(scope) {
			out[kv[0]] = kv[1]
		}
	}
	return out
}

// sortedKeys returns a map's keys in order.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
