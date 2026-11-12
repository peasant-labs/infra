package policy

import (
	"path"
	"slices"
	"strings"

	"github.com/peasant-labs/infra/tools/release/internal/closedset"
	"github.com/peasant-labs/infra/tools/release/internal/intent"
	regexp "github.com/wasilibs/go-re2"
	"gopkg.in/yaml.v3"
)

// This file finds every ref write in a step: each git push refspec in every
// form git accepts, and each gh command or API call that writes a ref. The
// push rule then holds every write to what its job may write. A write whose
// target the reader cannot prove (a variable, a pattern, a push that names no
// refspec, an option that pushes every branch or tag, an API call it cannot
// read) is reported as unproven, so the rule fails closed.

// refWriteKind is what one ref write targets. The zero value is unknown.
type refWriteKind string

const (
	// refWriteBranch pushes one branch named literally in the refspec.
	refWriteBranch refWriteKind = "branch"
	// refWriteTag pushes one ref under refs/tags/.
	refWriteTag refWriteKind = "tag"
	// refWriteUnproven writes a ref the reader cannot pin to one literal
	// branch or one tag.
	refWriteUnproven refWriteKind = "unproven"
)

// allRefWriteKinds is the canonical closed set of ref write kinds.
var allRefWriteKinds = []refWriteKind{refWriteBranch, refWriteTag, refWriteUnproven}

func (k refWriteKind) String() string { return string(k) }

// parseRefWriteKind maps a kind's spelling to its member and refuses
// anything else.
func parseRefWriteKind(raw string) (refWriteKind, error) {
	return closedset.Parse(allRefWriteKinds, raw, closedset.Context{
		Set:   "ref write kind",
		Where: "policy.parseRefWriteKind",
		When:  "reading the kind of a ref write",
		Means: "a kind decides which allowance a write is held to, so an unknown kind matches none",
	})
}

// refWrite is one ref write found in a run block.
type refWrite struct {
	kind refWriteKind
	// branch is the branch a branch write targets.
	branch string
	// tag is the part of a tag write's destination after refs/tags/, as
	// written, and tagExpands tells whether the shell expands it.
	tag        string
	tagExpands bool
	// raw is the refspec or command as written, for messages.
	raw string
	// why says what the reader could not prove about an unproven write.
	why string
}

func unproven(raw, why string) refWrite {
	return refWrite{kind: refWriteUnproven, raw: raw, why: why}
}

// refWriteMarkers are API names that write a ref (or arm a merge) whatever
// client calls them: the GraphQL ref and merge mutations, the REST ref and
// merge endpoints, and the environment variables that inject git config. A
// run block naming one is an unproven write, so a curl call or a script
// string cannot route around the command readers.
var refWriteMarkers = []string{
	"createRef", "updateRef", "deleteRef", "mergeBranch", "createCommitOnBranch",
	"mergePullRequest", "enablePullRequestAutoMerge", "enqueuePullRequest",
	"git/refs", "/merges", "merge-upstream",
	"GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT",
}

// pushWord matches the word push anywhere, for run text the lexer cannot
// read.
var pushWord = regexp.MustCompile(`(^|[^A-Za-z0-9_-])push([^A-Za-z0-9_-]|$)`)

// stepRefWrites returns every ref write in the step's run block. job and doc
// supply the default shell and the env maps; either may be nil. A step that
// runs under a shell the lexer cannot read is an unproven write whenever it
// mentions a push or a marker.
func stepRefWrites(step, job, doc *yaml.Node) []refWrite {
	run := stepRunText(step)
	var out []refWrite
	for _, scope := range []*yaml.Node{step, job, doc} {
		out = append(out, envRefWrites(scope)...)
	}
	if run == "" {
		return out
	}
	if shell := effectiveShell(step, job, doc); shell != "" && !posixShell(shell) {
		if pushWord.MatchString(run) || markerIn(run) != "" {
			out = append(out, unproven(firstLine(run), "the step runs under shell "+quote(shell)+", which the checker cannot read, and it mentions a push or a ref-writing API"))
		}
		return out
	}
	return append(out, runRefWrites(run)...)
}

// envRefWrites reports env entries that inject git config: they can define
// an alias for push or turn on pushing tags, so no push in their scope can be
// read.
func envRefWrites(scope *yaml.Node) []refWrite {
	if scope == nil {
		return nil
	}
	env := mappingValue(scope, "env")
	if env == nil || env.Kind != yaml.MappingNode {
		return nil
	}
	var out []refWrite
	for i := 0; i+1 < len(env.Content); i += 2 {
		name := env.Content[i].Value
		if name == "GIT_CONFIG_PARAMETERS" || name == "GIT_CONFIG_COUNT" || strings.HasPrefix(name, "GIT_CONFIG_KEY_") {
			out = append(out, unproven(name, "env sets "+name+", which injects git config (a push alias or push settings) that the checker cannot read"))
		}
	}
	return out
}

// effectiveShell returns the shell a run step uses: its own shell, or the
// job's or the workflow's defaults.run.shell, or "" for the runner default.
func effectiveShell(step, job, doc *yaml.Node) string {
	if s := mappingValue(step, "shell"); s != nil {
		return s.Value
	}
	for _, scope := range []*yaml.Node{job, doc} {
		if scope == nil {
			continue
		}
		if d := mappingValue(scope, "defaults"); d != nil {
			if r := mappingValue(d, "run"); r != nil {
				if s := mappingValue(r, "shell"); s != nil {
					return s.Value
				}
			}
		}
	}
	return ""
}

// posixShell reports whether a shell setting runs bash or sh, whose scripts
// the lexer reads.
func posixShell(shell string) bool {
	fields := strings.Fields(shell)
	return len(fields) > 0 && (path.Base(fields[0]) == "bash" || path.Base(fields[0]) == "sh")
}

func markerIn(run string) string {
	for _, marker := range refWriteMarkers {
		if strings.Contains(run, marker) {
			return marker
		}
	}
	return ""
}

func quote(s string) string { return `"` + s + `"` }

// runRefWrites returns every ref write in a run block, in order: the writes
// of every git and gh command the lexer finds, then one unproven write per
// marker the text names, and one when the text does not lex completely but
// mentions a push or gh.
func runRefWrites(run string) []refWrite {
	cmds, complete := shellCommands(run)
	var out []refWrite
	for _, cmd := range cmds {
		out = append(out, commandRefWrites(cmd)...)
	}
	for _, marker := range refWriteMarkers {
		if strings.Contains(run, marker) {
			out = append(out, unproven(marker, "the run names "+marker+", which writes a ref or arms a merge through the GitHub API or injects git config; the checker cannot hold such a call to one branch"))
		}
	}
	if !complete && (pushWord.MatchString(run) || strings.Contains(run, "gh")) {
		out = append(out, unproven(firstLine(run), "the run block does not lex as a complete shell script (an unterminated quote, substitution, or here-document), so its pushes cannot be read"))
	}
	return out
}

// outputCommands only print their arguments, so a git word among them is
// text, not a command.
var outputCommands = []string{"echo", "printf"}

// commandRefWrites returns the ref writes of one simple command: the first
// git or gh word at or after the command word, with the words after it.
func commandRefWrites(cmd shellCommand) []refWrite {
	start := commandWord(cmd)
	if start < 0 || containsString(outputCommands, path.Base(cmd[start].bare)) {
		return nil
	}
	raw := strings.Join(cmd.bareWords(), " ")
	for i := start; i < len(cmd); i++ {
		switch path.Base(cmd[i].bare) {
		case "git":
			return gitRefWrites(cmd[i+1:], raw)
		case "gh":
			return ghRefWrites(cmd[i+1:], raw)
		}
	}
	return nil
}

// gitGlobalValueOptions are the git options before the subcommand that take
// the next word as their value.
var gitGlobalValueOptions = []string{"-C", "--git-dir", "--work-tree", "--exec-path", "--super-prefix", "--list-cmds"}

// gitGlobalSwitches are the git options before the subcommand that take no
// value and change no ref name.
var gitGlobalSwitches = []string{
	"--no-pager", "-P", "-p", "--paginate", "--bare", "--no-replace-objects",
	"--no-optional-locks", "--literal-pathspecs", "--glob-pathspecs",
	"--noglob-pathspecs", "--icase-pathspecs", "--no-lazy-fetch", "--no-advice",
}

// gitRefWrites reads one git command from its arguments: the options before
// the subcommand (a -c or --config-env that sets forbidden config is an
// unproven write), then a push, a ref-writing plumbing command, or a config
// write that changes how later pushes behave.
func gitRefWrites(args []shellWord, raw string) []refWrite {
	var out []refWrite
	unknownGlobal := ""
	i := 0
options:
	for ; i < len(args); i++ {
		w := args[i].bare
		name, value, attached := strings.Cut(w, "=")
		switch {
		case w == "-c" || w == "--config-env":
			if i+1 < len(args) {
				i++
				out = append(out, gitConfigWrites(args[i], raw)...)
			}
		case attached && name == "--config-env":
			out = append(out, gitConfigWrites(shellWord{raw: value, bare: value, expands: args[i].expands}, raw)...)
		case containsString(gitGlobalValueOptions, w):
			i++
		case attached && containsString(gitGlobalValueOptions, name):
		case containsString(gitGlobalSwitches, w):
		case strings.HasPrefix(w, "-"):
			unknownGlobal = w
		default:
			break options
		}
	}
	if i >= len(args) {
		return out
	}
	sub := args[i]
	rest := args[i+1:]
	if sub.expands {
		return append(out, unproven(raw, "the git subcommand "+quote(sub.bare)+" is a variable, so it may be a push"))
	}
	switch sub.bare {
	case "push":
		if unknownGlobal != "" {
			return append(out, unproven(raw, "git option "+quote(unknownGlobal)+" before push is not one the checker reads, and it may change which ref the push writes"))
		}
		return append(out, pushRefWrites(rest, raw)...)
	case "send-pack", "http-push":
		return append(out, unproven(raw, "git "+sub.bare+" pushes refs with no refspec the checker reads; use git push with an explicit refspec"))
	case "subtree":
		for _, w := range rest {
			if w.bare == "push" {
				return append(out, unproven(raw, "git subtree push writes a ref the checker cannot read; use git push with an explicit refspec"))
			}
		}
	case "config":
		for _, w := range rest {
			out = append(out, gitConfigWrites(w, raw)...)
		}
		if key := firstOperand(rest); key != nil && key.expands {
			out = append(out, unproven(raw, "git config sets key "+quote(key.bare)+", a variable, which may be an alias or a push setting"))
		}
	}
	return out
}

// firstOperand returns the first word that is not an option, or nil.
func firstOperand(words []shellWord) *shellWord {
	for i := range words {
		if !strings.HasPrefix(words[i].bare, "-") {
			return &words[i]
		}
	}
	return nil
}

// gitConfigWrites reports a config key that changes what a push writes: an
// alias (which can rename push), a push.* setting (followTags pushes tags),
// or a remote's push refspecs or mirror flag. The key is the word up to its
// first '='; a key that expands cannot be read.
func gitConfigWrites(w shellWord, raw string) []refWrite {
	key, _, _ := strings.Cut(w.bare, "=")
	if strings.ContainsAny(key, "$`") {
		return []refWrite{unproven(raw, "git config key "+quote(key)+" is a variable, so it may be an alias or a push setting")}
	}
	lower := strings.ToLower(key)
	switch {
	case strings.HasPrefix(lower, "alias."):
		return []refWrite{unproven(raw, "git config "+quote(key)+" defines an alias, which can rename push so no push it runs can be read")}
	case strings.HasPrefix(lower, "push."):
		return []refWrite{unproven(raw, "git config "+quote(key)+" changes what every later push writes (push.followTags pushes tags)")}
	case strings.HasPrefix(lower, "remote.") && (strings.HasSuffix(lower, ".push") || strings.HasSuffix(lower, ".mirror")):
		return []refWrite{unproven(raw, "git config "+quote(key)+" sets the refs a push to that remote writes")}
	}
	return nil
}

// pushForbidden are the git push options that write refs no refspec names,
// with what each does.
var pushForbidden = map[string]string{
	"--all":         "pushes every local branch, the default branch included",
	"--branches":    "pushes every local branch, the default branch included",
	"--mirror":      "mirrors every local ref onto the remote, the default branch included",
	"--tags":        "pushes every local tag",
	"--follow-tags": "pushes every annotated tag reachable from the pushed commits",
	"--delete":      "deletes the named remote refs",
	"-d":            "deletes the named remote refs",
	"--prune":       "deletes every remote branch with no local counterpart",
	"--repo":        "names the remote through --repo, so the checker cannot tell the remote from the refspecs",
}

// pushValueOptions take a value, attached with '=' or as the next word.
var pushValueOptions = []string{"--push-option", "-o", "--receive-pack", "--exec", "--recurse-submodules"}

// pushSwitches take no value (or only an attached one) and change no target.
var pushSwitches = []string{
	"--force", "-f", "--force-with-lease", "--no-force-with-lease",
	"--force-if-includes", "--no-force-if-includes", "--atomic", "--no-atomic",
	"--verify", "--no-verify", "--quiet", "-q", "--verbose", "-v",
	"--porcelain", "--set-upstream", "-u", "--progress", "--no-progress",
	"--dry-run", "-n", "--thin", "--no-thin", "--no-follow-tags",
	"--signed", "--no-signed", "--ipv4", "-4", "--ipv6", "-6",
	"--no-recurse-submodules",
}

// pushShortSwitches are the one-letter switches a cluster such as -fu may
// combine.
const pushShortSwitches = "fqvnu46"

// pushRefWrites reads a git push's arguments: options anywhere before --,
// then the remote, then the refspecs. Every refspec is one write; an option
// that writes refs no refspec names, an option the reader does not know, and
// a push with no refspec are unproven writes.
func pushRefWrites(args []shellWord, raw string) []refWrite {
	var out []refWrite
	var operands []shellWord
	endOfOptions := false
	for i := 0; i < len(args); i++ {
		w := args[i]
		b := w.bare
		if !endOfOptions && b == "--" {
			endOfOptions = true
			continue
		}
		if endOfOptions || !strings.HasPrefix(b, "-") || b == "-" {
			operands = append(operands, w)
			continue
		}
		if w.expands {
			out = append(out, unproven(raw, "push option "+quote(b)+" is a variable, so it may push every branch or delete one"))
			continue
		}
		name, _, attached := strings.Cut(b, "=")
		if why, bad := pushForbidden[name]; bad {
			out = append(out, unproven(raw, "git push "+name+" "+why))
			continue
		}
		switch {
		case containsString(pushValueOptions, name):
			if !attached {
				i++
			}
		case containsString(pushSwitches, name):
		case !strings.HasPrefix(b, "--") && len(b) > 2:
			out = append(out, pushShortCluster(b, raw)...)
			if strings.HasSuffix(b, "o") {
				i++
			}
		default:
			out = append(out, unproven(raw, "git push option "+quote(b)+" is not one the checker reads, and it may change which refs the push writes"))
		}
	}
	if len(operands) < 2 {
		return append(out, unproven(raw, "git push names no refspec, so push.default and the remote's push config decide which branch it writes"))
	}
	for _, spec := range operands[1:] {
		out = append(out, refspecWrite(spec))
	}
	return out
}

// pushShortCluster reads a cluster of one-letter push options such as -fu.
// -d deletes; -o takes the rest of the cluster (or the next word) as its
// value; any other letter must be a known switch.
func pushShortCluster(cluster, raw string) []refWrite {
	for _, letter := range cluster[1:] {
		switch {
		case letter == 'd':
			return []refWrite{unproven(raw, "git push -d "+pushForbidden["--delete"])}
		case letter == 'o':
			return nil
		case !strings.ContainsRune(pushShortSwitches, letter):
			return []refWrite{unproven(raw, "git push option "+quote(cluster)+" is not one the checker reads, and it may change which refs the push writes")}
		}
	}
	return nil
}

// refspecWrite classifies one push refspec by its destination: the part
// after the last ':' outside a GitHub expression, or the whole refspec when
// it has none. A leading '+' only forces the update.
func refspecWrite(w shellWord) refWrite {
	spec := strings.TrimPrefix(w.bare, "+")
	src, dst, colon := cutDestination(spec)
	if !colon {
		src, dst = spec, spec
	}
	switch {
	case strings.ContainsAny(spec, "*"):
		return unproven(w.bare, "refspec "+quote(w.bare)+" is a pattern, so it may write any ref it matches")
	case colon && src == "":
		return unproven(w.bare, "refspec "+quote(w.bare)+" has no source, so it deletes the remote ref")
	case dst == "":
		return unproven(w.bare, "refspec "+quote(w.bare)+" names no destination")
	case strings.HasPrefix(dst, intent.TagRefPrefix):
		return refWrite{kind: refWriteTag, tag: strings.TrimPrefix(dst, intent.TagRefPrefix), tagExpands: w.expands, raw: w.bare}
	case strings.ContainsAny(dst, "$`"):
		return unproven(w.bare, "refspec "+quote(w.bare)+" writes "+quote(dst)+", which is a variable the checker cannot prove")
	case dst == "HEAD" || dst == "@":
		return unproven(w.bare, "refspec "+quote(w.bare)+" writes the current branch, whatever it is")
	}
	branch := dst
	if b, ok := strings.CutPrefix(dst, intent.BranchRefPrefix); ok {
		branch = b
	} else if strings.HasPrefix(dst, refRootPrefix) {
		return unproven(w.bare, "refspec "+quote(w.bare)+" writes "+quote(dst)+", outside "+intent.BranchRefPrefix+" and "+intent.TagRefPrefix)
	}
	if branch == "" {
		return unproven(w.bare, "refspec "+quote(w.bare)+" names no branch")
	}
	return refWrite{kind: refWriteBranch, branch: branch, raw: w.bare}
}

// refRootPrefix opens every fully qualified ref name.
var refRootPrefix = strings.TrimSuffix(intent.TagRefPrefix, "tags/")

// cutDestination splits a refspec at its last ':' outside a ${{ }}
// expression.
func cutDestination(spec string) (src, dst string, ok bool) {
	last := -1
	for i := 0; i < len(spec); i++ {
		if strings.HasPrefix(spec[i:], "${{") {
			end := strings.Index(spec[i:], "}}")
			if end < 0 {
				break
			}
			i += end + 1
			continue
		}
		if spec[i] == ':' {
			last = i
		}
	}
	if last < 0 {
		return "", "", false
	}
	return spec[:last], spec[last+1:], true
}

// ghAPIValueOptions are the gh api options that take the next word as their
// value and carry no request body.
var ghAPIValueOptions = []string{"-H", "--header", "-q", "--jq", "-t", "--template", "--hostname", "--cache", "-p", "--preview"}

// ghBodyFields are the gh api options that add a request field.
var ghBodyFields = []string{"-f", "--raw-field", "-F", "--field"}

// ghRefWrites reads one gh command: gh repo sync writes a branch, and a gh
// api call writes a ref when it calls a ref-writing REST endpoint with a
// writing method or a GraphQL query the reader cannot see. gh pr merge and
// gh release create have their own allowances in the push rule.
func ghRefWrites(args []shellWord, raw string) []refWrite {
	if len(args) == 0 {
		return nil
	}
	if args[0].expands {
		return []refWrite{unproven(raw, "the gh subcommand "+quote(args[0].bare)+" is a variable, so it may write a ref")}
	}
	switch args[0].bare {
	case "repo":
		if len(args) > 1 && args[1].bare == "sync" {
			return []refWrite{unproven(raw, "gh repo sync writes a branch with no refspec the checker reads")}
		}
	case "api":
		return ghAPIWrites(args[1:], raw)
	}
	return nil
}

// restRefWriteSegments are the REST path segments whose writing methods
// write a ref: a merge into a branch, a branch sync or rename, a file commit,
// a PR branch update, or a ref.
var restRefWriteSegments = []string{"merges", "merge-upstream", "contents", "update-branch", "rename", "refs"}

// ghAPIWrites reads one gh api call. GraphQL needs a literal query (the
// markers then catch a ref mutation); REST is a write when the method writes
// and the path names a ref-writing endpoint, including a PR merge.
func ghAPIWrites(args []shellWord, raw string) []refWrite {
	method := ""
	methodExpands := false
	body := false
	var endpoint *shellWord
	var query *shellWord
	queryFromFile := false
	for i := 0; i < len(args); i++ {
		w := args[i]
		b := w.bare
		name, value, attached := strings.Cut(b, "=")
		switch {
		case b == "-X" || b == "--method":
			if i+1 < len(args) {
				i++
				method, methodExpands = args[i].bare, args[i].expands
			}
		case attached && name == "--method":
			method, methodExpands = value, w.expands
		case strings.HasPrefix(b, "-X") && len(b) > 2:
			method, methodExpands = b[2:], w.expands
		case containsString(ghBodyFields, b) || (attached && containsString(ghBodyFields, name)):
			body = true
			field := shellWord{raw: value, bare: value, expands: w.expands}
			if !attached {
				if i+1 >= len(args) {
					continue
				}
				i++
				field = args[i]
			}
			key, fieldValue, _ := strings.Cut(field.bare, "=")
			if key == "query" {
				f := field
				query = &f
				queryFromFile = (name == "-F" || name == "--field") && strings.HasPrefix(fieldValue, "@")
			}
		case b == "--input":
			body = true
			i++
		case attached && name == "--input":
			body = true
		case containsString(ghAPIValueOptions, b):
			i++
		case strings.HasPrefix(b, "-"):
		default:
			if endpoint == nil {
				e := w
				endpoint = &e
			}
		}
	}
	if endpoint == nil {
		return nil
	}
	if strings.Trim(endpoint.bare, "/") == "graphql" {
		switch {
		case query == nil:
			return []refWrite{unproven(raw, "gh api graphql sends no literal query field, so the checker cannot see whether it is a ref mutation")}
		case query.expands || queryFromFile:
			return []refWrite{unproven(raw, "gh api graphql reads its query from a variable or a file, so the checker cannot see whether it is a ref mutation")}
		}
		return nil
	}
	if methodExpands {
		return []refWrite{unproven(raw, "gh api method "+quote(method)+" is a variable, so the call may write")}
	}
	if method == "" {
		method = "GET"
		if body {
			method = "POST"
		}
	}
	if m := strings.ToUpper(method); m == "GET" || m == "HEAD" {
		return nil
	}
	segments := strings.Split(strings.Trim(endpoint.bare, "/"), "/")
	for i, seg := range segments {
		if containsString(restRefWriteSegments, seg) || (seg == "merge" && i >= 2 && segments[i-2] == "pulls") {
			return []refWrite{unproven(raw, "gh api "+strings.ToUpper(method)+" "+endpoint.bare+" writes a ref through the REST API, which the checker cannot hold to one branch")}
		}
	}
	return nil
}

// hasRefWrite reports whether the step writes any ref.
func hasRefWrite(step *yaml.Node) bool {
	return len(stepRefWrites(step, nil, nil)) > 0
}

// hasBranchOrUnprovenWrite reports whether the step writes a ref other than a
// tag.
func hasBranchOrUnprovenWrite(step *yaml.Node) bool {
	return slices.ContainsFunc(stepRefWrites(step, nil, nil), func(w refWrite) bool { return w.kind != refWriteTag })
}

// tagOutputExpression matches the planner's tag output alone,
// ${{ steps.<id>.outputs.tag }}, and captures the step id.
var tagOutputExpression = regexp.MustCompile(`^\$\{\{\s*` + stepOutputPattern(intent.OutputTag) + `\s*\}\}$`)

// provenTagFault says why a tag write's tag is not the planner's tag output,
// or "" when it is. The tag must be ${{ steps.<id>.outputs.tag }} directly,
// or a shell variable ($TAG or ${TAG}, outside single quotes) that the step,
// the job, or the workflow maps from exactly that expression; and <id> must
// be a step in the same job that runs the planner. The planner prints a tag
// built from the release config's prefix and a validated version, so such a
// tag can hold no ':' that would turn the refspec into a branch write.
func provenTagFault(w refWrite, step, job, doc *yaml.Node) string {
	expr := w.tag
	if !tagOutputExpression.MatchString(expr) {
		name, ok := shellVariable(expr)
		if !ok {
			return "the tag " + quote(w.tag) + " is not the planner's tag output or a variable mapped from it"
		}
		if !w.tagExpands {
			return "the variable " + quote(w.tag) + " sits in single quotes, so the shell never expands it"
		}
		expr = ""
		for _, scope := range []*yaml.Node{step, job, doc} {
			if scope == nil {
				continue
			}
			if v := envValue(scope, name); v != "" {
				expr = strings.TrimSpace(v)
				break
			}
		}
		if !tagOutputExpression.MatchString(expr) {
			return "the variable " + name + " is not mapped from the planner's tag output in env"
		}
	}
	id := tagOutputExpression.FindStringSubmatch(expr)[1]
	for _, s := range stepsOf(job) {
		if stepID(s) == id && planRunner.MatchString(stepRunText(s)) {
			return ""
		}
	}
	return "steps." + id + " is not a step of this job that runs " + intent.ProgramName + " " + intent.CommandPlan.String()
}

// shellVariable returns the name of a lone shell variable reference, $NAME
// or ${NAME}.
func shellVariable(word string) (string, bool) {
	name, ok := strings.CutPrefix(word, "$")
	if !ok {
		return "", false
	}
	if inner, braced := strings.CutPrefix(name, "{"); braced {
		name, ok = strings.CutSuffix(inner, "}")
		if !ok {
			return "", false
		}
	}
	return name, shellName.MatchString(name)
}
