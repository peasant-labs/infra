package intent

import (
	"slices"
	"strconv"
	"strings"

	regexp "github.com/wasilibs/go-re2"

	"github.com/peasant-labs/infra/tools/release/internal/closedset"
)

// This file owns the names the command, the policy rules and the consumer
// workflows share: the step-output keys, the default config path, the
// repository variables, and the git ref vocabulary. Each has one home here,
// so no caller spells one as a literal.

// DefaultConfigPath is where a repository keeps its release config, relative
// to the repository root. The command reads this path unless told otherwise,
// and the planner's grammar epoch is the commit that added it.
const DefaultConfigPath = ".github/release-intent.yaml"

// OutputKey is the key of one key=value line the command prints and appends
// to the step-output file. Workflows read these keys as step outputs.
type OutputKey string

const (
	// OutputAction carries the plan's Action.
	OutputAction OutputKey = "action"
	// OutputRangeBase carries the newest final tag on the branch chain.
	OutputRangeBase OutputKey = "range_base"
	// OutputVersionBase carries the highest final tag in the repository.
	OutputVersionBase OutputKey = "version_base"
	// OutputCandidate carries the candidate release commit, or nothing.
	OutputCandidate OutputKey = "candidate"
	// OutputCandidateFresh carries whether the candidate matches the generator.
	OutputCandidateFresh OutputKey = "candidate_fresh"
	// OutputLevel carries the highest pending Level.
	OutputLevel OutputKey = "level"
	// OutputBreaking carries whether any pending subject breaks.
	OutputBreaking OutputKey = "breaking"
	// OutputVersion carries the computed version.
	OutputVersion OutputKey = "version"
	// OutputTag carries the computed tag.
	OutputTag OutputKey = "tag"
	// OutputError carries the ErrorCode of a typed rejection, or nothing.
	OutputError OutputKey = "error"
	// OutputPR carries the PR number read from a merge-queue ref.
	OutputPR OutputKey = "pr"
	// OutputGate carries the queue Gate.
	OutputGate OutputKey = "gate"
	// OutputFresh carries whether the release PR content is current.
	OutputFresh OutputKey = "fresh"
	// OutputPush carries whether the release PR branch needs a push.
	OutputPush OutputKey = "push"
	// OutputKind carries the PRKind of a classified PR.
	OutputKind OutputKey = "kind"
	// OutputOK carries the pass mark of a check with no other value.
	OutputOK OutputKey = "ok"
)

// AllOutputKeys is the canonical closed set of step-output keys.
var AllOutputKeys = []OutputKey{
	OutputAction, OutputRangeBase, OutputVersionBase, OutputCandidate,
	OutputCandidateFresh, OutputLevel, OutputBreaking, OutputVersion,
	OutputTag, OutputError, OutputPR, OutputGate, OutputFresh, OutputPush,
	OutputKind, OutputOK,
}

func (k OutputKey) String() string { return string(k) }

// IsValid reports whether the key is one of the known members.
func (k OutputKey) IsValid() bool { return slices.Contains(AllOutputKeys, k) }

// ParseOutputKey returns the member spelled raw and refuses anything else.
func ParseOutputKey(raw string) (OutputKey, error) {
	return closedset.Parse(AllOutputKeys, raw, parsing("step-output key", "intent.ParseOutputKey"))
}

// outputSeparator joins a key to its value on an output line.
const outputSeparator = "="

// Line renders one key=value output line.
func (k OutputKey) Line(value string) string {
	return string(k) + outputSeparator + value
}

// ParseOutputLine splits one key=value output line into its typed key and
// its value. A line with no separator or an unknown key is an error.
func ParseOutputLine(line string) (OutputKey, string, error) {
	rawKey, value, ok := strings.Cut(line, outputSeparator)
	if !ok {
		return "", "", inputError(
			"output line "+strconv.Quote(line)+" has no "+strconv.Quote(outputSeparator),
			"every output line is a key, the separator, then the value",
			"intent.ParseOutputLine",
			"reading a step-output line",
			"the line carries no value a workflow can read",
			"print lines through OutputKey.Line",
		)
	}
	key, err := ParseOutputKey(rawKey)
	if err != nil {
		return "", "", err
	}
	return key, value, nil
}

// RepoVariable is the name of a repository variable the release workflows
// pass to the command.
type RepoVariable string

const (
	// VarBatchMode arms the release job: its value is a Mode, and an unset
	// variable plans without writing.
	VarBatchMode RepoVariable = "RELEASE_BATCH_MODE"
	// VarBatchFloor names the commit that drops a bad landed subject, and
	// everything before it, from the release range.
	VarBatchFloor RepoVariable = "RELEASE_BATCH_FLOOR"
)

// AllRepoVariables is the canonical closed set of repository variables.
var AllRepoVariables = []RepoVariable{VarBatchMode, VarBatchFloor}

func (v RepoVariable) String() string { return string(v) }

// IsValid reports whether the variable is one of the known members.
func (v RepoVariable) IsValid() bool { return slices.Contains(AllRepoVariables, v) }

// ParseRepoVariable returns the member spelled raw and refuses anything else.
func ParseRepoVariable(raw string) (RepoVariable, error) {
	return closedset.Parse(AllRepoVariables, raw, parsing("repository variable", "intent.ParseRepoVariable"))
}

// GitRef is a fully qualified git ref name, such as refs/heads/main. The
// zero value names no ref. Build one with BranchRef, TagRef or
// MergeQueueRef.Ref, or validate outside text with ParseGitRef.
type GitRef string

const (
	// refRoot opens every fully qualified ref.
	refRoot = "refs/"
	// BranchRefPrefix opens every branch ref.
	BranchRefPrefix = refRoot + "heads/"
	// TagRefPrefix opens every tag ref.
	TagRefPrefix = refRoot + "tags/"
	// MergeQueueRefPrefix opens the temporary branches GitHub's merge queue
	// creates for a merge group.
	MergeQueueRefPrefix = BranchRefPrefix + "gh-readonly-queue/"
	// refGlobAny is the for-each-ref wildcard that closes a ref pattern.
	refGlobAny = "*"
)

func (r GitRef) String() string { return string(r) }

// BranchRef builds the ref of a branch from its short name.
func BranchRef(branch string) GitRef { return GitRef(BranchRefPrefix + branch) }

// TagRef builds the ref of a tag from its short name.
func TagRef(tag string) GitRef { return GitRef(TagRefPrefix + tag) }

// TagRefGlob builds the ref pattern that matches every tag the given tag
// prefix opens, for listing a repository's release tags.
func TagRefGlob(tagPrefix string) string { return TagRefPrefix + tagPrefix + refGlobAny }

// Branch returns the short branch name when the ref is a branch ref.
func (r GitRef) Branch() (string, bool) { return r.under(BranchRefPrefix) }

// Tag returns the short tag name when the ref is a tag ref.
func (r GitRef) Tag() (string, bool) { return r.under(TagRefPrefix) }

func (r GitRef) under(prefix string) (string, bool) {
	rest, ok := strings.CutPrefix(string(r), prefix)
	if !ok || rest == "" {
		return "", false
	}
	return rest, true
}

// refForbidden lists the characters git refuses anywhere in a ref name.
const refForbidden = " ~^:?*[\\"

// ParseGitRef validates raw as a fully qualified ref name and returns it
// typed. It refuses a short name, an empty path part, and the characters and
// sequences git itself refuses, so a hostile value never reaches a command
// line as a ref.
func ParseGitRef(raw string) (GitRef, error) {
	fail := func(what string) (GitRef, error) {
		return "", inputError(
			"invalid git ref "+strconv.Quote(raw)+": "+what,
			"a ref is a fully qualified name under "+strconv.Quote(refRoot)+" with no empty part and none of the characters git refuses",
			"intent.ParseGitRef",
			"validating a git ref",
			"no branch, tag or merge group can be read from this value",
			"pass a full ref, for example "+strconv.Quote(BranchRef("main").String())+" or "+strconv.Quote(TagRef("v0.1.0").String()),
		)
	}
	rest, ok := strings.CutPrefix(raw, refRoot)
	if !ok {
		return fail("it does not start with " + strconv.Quote(refRoot))
	}
	parts := strings.Split(rest, "/")
	if len(parts) < 2 {
		return fail("it names no ref under a namespace")
	}
	for _, part := range parts {
		if part == "" {
			return fail("it has an empty path part")
		}
		if strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".") || strings.HasSuffix(part, ".lock") {
			return fail("path part " + strconv.Quote(part) + " starts or ends with a dot or ends with .lock")
		}
	}
	if strings.Contains(raw, "..") || strings.Contains(raw, "@{") {
		return fail("it contains a sequence git refuses")
	}
	for _, c := range raw {
		if c < 0x20 || c == 0x7f || c == '\uFFFD' || strings.ContainsRune(refForbidden, c) {
			return fail("it contains a character git refuses")
		}
	}
	return GitRef(raw), nil
}

// MergeQueueRef is the parsed form of a merge-group branch ref: the branch
// the group merges into, the PR at the head of the group, and the commit the
// group was built on.
type MergeQueueRef struct {
	Base string `yaml:"base"`
	PR   int    `yaml:"pr"`
	SHA  string `yaml:"sha"`
}

// mergeQueueRefPattern matches a merge-group ref: the queue prefix, the base
// branch (which may hold slashes), then pr-<number>-<hex sha>.
var mergeQueueRefPattern = regexp.MustCompile(
	`^` + regexp.QuoteMeta(MergeQueueRefPrefix) + `(.+)/pr-([0-9]+)-([0-9a-fA-F]+)$`)

// Ref builds the merge-group ref for the parsed parts.
func (q MergeQueueRef) Ref() GitRef {
	return GitRef(MergeQueueRefPrefix + q.Base + "/pr-" + strconv.Itoa(q.PR) + "-" + q.SHA)
}

// ParseMergeQueueRef reads a merge-group ref. Any other ref, and a group ref
// whose PR number is not a positive integer, reports false with no error:
// the PR-time check stays authoritative, so a ref GitHub reshapes must not
// block the queue.
func ParseMergeQueueRef(ref GitRef) (MergeQueueRef, bool) {
	m := mergeQueueRefPattern.FindStringSubmatch(string(ref))
	if m == nil {
		return MergeQueueRef{}, false
	}
	pr, err := strconv.Atoi(m[2])
	if err != nil || pr <= 0 {
		return MergeQueueRef{}, false
	}
	return MergeQueueRef{Base: m[1], PR: pr, SHA: m[3]}, true
}

// Title syntax marks, shared by ParseTitle's grammar and FormatTitle.
const (
	titleScopeOpen    = "("
	titleScopeClose   = ")"
	titleScopeSep     = ","
	titleBreakingMark = "!"
	titleLevelOpen    = "["
	titleLevelClose   = "]"
	titleSubjectSep   = ": "
	titleLandedOpen   = " (#"
	titleLandedClose  = ")"
)

// FormatTitle renders a title in the intent grammar. It is the inverse of
// ParseTitle: for every Title that ParseTitle returns, parsing the rendered
// text under the same config and version returns the same Title, and for
// every text ParseTitle accepts without surrounding space, rendering the
// parsed Title returns that text. A PR number other than 0 renders the
// landed-subject suffix. It validates nothing: a Title that ParseTitle would
// refuse renders text that ParseTitle refuses.
func FormatTitle(t Title) string {
	var b strings.Builder
	b.WriteString(t.Type.String())
	if len(t.Scopes) > 0 {
		b.WriteString(titleScopeOpen)
		b.WriteString(strings.Join(t.Scopes, titleScopeSep))
		b.WriteString(titleScopeClose)
	}
	if t.Breaking {
		b.WriteString(titleBreakingMark)
	}
	if t.Level != LevelNone {
		b.WriteString(titleLevelOpen)
		b.WriteString(t.Level.String())
		b.WriteString(titleLevelClose)
	}
	b.WriteString(titleSubjectSep)
	b.WriteString(t.Subject)
	if t.PR != 0 {
		b.WriteString(titleLandedOpen)
		b.WriteString(strconv.Itoa(t.PR))
		b.WriteString(titleLandedClose)
	}
	return b.String()
}

// parsing is the context of a refusal at one of this package's exported
// closed-set parsers: set names the closed set and where names the parser.
func parsing(set, where string) closedset.Context {
	return closedset.Context{
		Set:   set,
		Where: where,
		When:  "parsing a " + set,
		Means: "no decision can be made from this value",
	}
}
