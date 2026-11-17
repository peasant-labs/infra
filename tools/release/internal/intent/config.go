package intent

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/peasant-labs/infra/tools/release/internal/closedset"
)

// configInvalidError builds the typed rejection for a config file that was
// read but judges nothing: malformed YAML, an unknown key, or a failed
// value check. A file that cannot be read at all stays a plain error, so
// the caller keeps the read/parse exit split.
func configInvalidError(what, why, where, fix string) *Error {
	return &Error{
		Code:  ErrConfigInvalid,
		What:  what,
		Why:   why,
		Where: where,
		When:  "loading the release config",
		Means: "no check can run without the repo's contract",
		Fix:   fix,
	}
}

// configBotKinds is the closed set of keys a bot_branches map may carry: the
// bot PR kinds, derived once from AllPRKinds so the closed set has one home.
var configBotKinds = func() map[PRKind]bool {
	out := map[PRKind]bool{}
	for _, kind := range botKinds {
		out[kind] = true
	}
	return out
}()

// LoadConfig reads and validates one repo's release-intent file. Unknown
// keys fail closed so a typo'd key disables nothing silently. A repo that
// does not release names no surfaces, because nothing there may bump.
func LoadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, inputError(
			"cannot read release config "+strconv.Quote(path),
			"the file holds the tag prefix, the protected paths, and the bot branches every check compares against",
			"intent.LoadConfig",
			"loading the release config",
			"no title, plan, or gate can be judged without the repo's contract",
			"pass the path of a readable release config, or add the file at "+strconv.Quote(DefaultConfigPath),
		)
	}
	return ParseConfig(data)
}

// ParseConfig parses and validates one repo's release-intent file from its
// bytes, with the same strict decoding and value checks LoadConfig applies
// to a path. LoadConfig reads the file and calls this; callers holding the
// file's bytes from another revision, such as a git object, call this
// directly.
func ParseConfig(data []byte) (Config, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, configInvalidError(
			"cannot parse release config bytes: "+err.Error(),
			"the config is strict YAML with a fixed key set",
			"intent.ParseConfig",
			"fix the YAML syntax or the unknown key the message names",
		)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err != nil {
			return Config{}, configInvalidError(
				"cannot parse release config bytes: "+err.Error(),
				"the config is one YAML document",
				"intent.ParseConfig",
				"keep a single document in the config",
			)
		}
		return Config{}, configInvalidError(
			"release config bytes hold more than one document",
			"the config is one YAML document",
			"intent.ParseConfig",
			"keep a single document in the config",
		)
	}
	if err := checkConfig(cfg, "release config bytes, parsed by intent.ParseConfig"); err != nil {
		return Config{}, err
	}
	cfg.SurfaceOrder = configSurfaceOrder(data, cfg.Surfaces)
	return cfg, nil
}

// configSurfaceOrder retains YAML map order for the release title and freeze
// plan. Merged map entries not explicitly present are appended in name order.
func configSurfaceOrder(data []byte, surfaces map[string][]string) []string {
	if len(surfaces) == 0 {
		return nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil || len(doc.Content) == 0 {
		return nil // ParseConfig has already validated the same document.
	}
	root := doc.Content[0]
	var mapping *yaml.Node
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "surfaces" {
			mapping = root.Content[i+1]
			break
		}
	}
	if mapping != nil && mapping.Kind == yaml.AliasNode {
		mapping = mapping.Alias
	}
	seen := map[string]bool{}
	var names []string
	if mapping != nil && mapping.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(mapping.Content); i += 2 {
			name := mapping.Content[i].Value
			if _, ok := surfaces[name]; ok && !seen[name] {
				names = append(names, name)
				seen[name] = true
			}
		}
	}
	var remaining []string
	for name := range surfaces {
		if !seen[name] {
			remaining = append(remaining, name)
		}
	}
	sort.Strings(remaining)
	return append(names, remaining...)
}

// SurfaceNames returns config order, falling back to sorted names for a
// programmatically constructed Config that has no source document.
func (cfg Config) SurfaceNames() []string {
	names := make([]string, 0, len(cfg.Surfaces))
	seen := map[string]bool{}
	for _, name := range cfg.SurfaceOrder {
		if _, ok := cfg.Surfaces[name]; ok && !seen[name] {
			names = append(names, name)
			seen[name] = true
		}
	}
	var remaining []string
	for name := range cfg.Surfaces {
		if !seen[name] {
			remaining = append(remaining, name)
		}
	}
	sort.Strings(remaining)
	return append(names, remaining...)
}

// checkConfig validates the loaded values beyond their YAML shape. where
// names the config under check for the error message.
func checkConfig(cfg Config, where string) error {
	fail := func(what, why, fix string) error {
		return configInvalidError(what, why, where+", validating the release config", fix)
	}
	if cfg.TagPrefix == "" {
		return fail("the tag prefix is empty",
			"the prefix tells release tags apart from other tags that share the repository",
			"set tag_prefix to the text that opens this repository's release tags")
	}
	if cfg.AutomationAuthor == "" {
		return fail("the automation author is empty",
			"the author tells the release PR apart from author PRs that may not touch protected paths",
			"set automation_author to the login of the bot that opens the release PR")
	}
	if cfg.Changelog == "" {
		return fail("the changelog path is empty",
			"the changelog is protected as a whole file and only the release PR may change it",
			"set changelog to the path of this repository's changelog file")
	}
	for i, vf := range cfg.VersionFiles {
		if vf.Path == "" || vf.Field == "" {
			return fail(fmt.Sprintf("version_files[%d] names path %q and field %q", i, vf.Path, vf.Field),
				"a version file entry needs both the file and the one protected top-level field the release PR writes",
				"set both path and field to the version file and its top-level version key")
		}
	}
	for _, glob := range cfg.ProtectedPaths {
		if glob == "" {
			return fail("protected_paths contains an empty pattern", "each protected path must name a path or a glob", "remove the empty entry or name a protected path")
		}
		if _, err := path.Match(glob, ""); err != nil {
			return fail("protected_paths contains invalid pattern "+strconv.Quote(glob), "an invalid glob cannot reliably protect its paths", "correct the path glob")
		}
	}
	if len(cfg.FreezeOutputs) > 0 && !cfg.SpecFreeze {
		return fail("freeze_outputs is non-empty while spec_freeze is disabled",
			"freeze outputs are authorized only for a repository that freezes surfaces",
			"enable spec_freeze with releasing surfaces, or remove freeze_outputs")
	}
	seenOutputs := map[string]bool{}
	for _, glob := range cfg.FreezeOutputs {
		if !safeReleasePattern(glob) {
			return fail("freeze_outputs contains unsafe or invalid pattern "+strconv.Quote(glob),
				"freeze outputs must be confined repository-relative paths or valid globs",
				"use a safe path or glob outside Git metadata and workflow configuration")
		}
		if seenOutputs[glob] {
			return fail("freeze_outputs contains duplicate pattern "+strconv.Quote(glob),
				"each configured freeze output pattern must be listed once",
				"remove the duplicate pattern")
		}
		seenOutputs[glob] = true
		for _, reserved := range append([]string{cfg.Changelog}, configVersionPaths(cfg)...) {
			if matchesPathPattern(glob, reserved) {
				return fail("freeze_outputs pattern "+strconv.Quote(glob)+" matches reserved path "+strconv.Quote(reserved),
					"the changelog and version files have their own release rules",
					"narrow or remove the pattern so it excludes the changelog and every version file")
			}
		}
	}
	seenStems := map[string]string{}
	for name, stems := range cfg.Surfaces {
		if !scopePattern.MatchString(name) || len(stems) == 0 {
			return fail("surface "+strconv.Quote(name)+" has an invalid scope name or no stems",
				"each surface must be nameable in a title and own at least one generated file stem",
				"use a lowercase title scope and list its file stems")
		}
		for _, stem := range stems {
			if stem == "" || strings.ContainsAny(stem, `/\.`) || strings.IndexFunc(stem, func(r rune) bool {
				return (r < 'a' || r > 'z') && r != '-'
			}) >= 0 {
				return fail("surface "+strconv.Quote(name)+" has invalid stem "+strconv.Quote(stem),
					"file stems contain lowercase letters and hyphens, not paths, versions or extensions",
					"list only unversioned generated file stems")
			}
			if previous, ok := seenStems[stem]; ok {
				return fail("stem "+strconv.Quote(stem)+" is claimed by "+strconv.Quote(previous)+" and "+strconv.Quote(name),
					"each generated file stem belongs to exactly one surface",
					"list the stem once under its owning surface")
			}
			seenStems[stem] = name
		}
	}
	if !cfg.Releases && len(cfg.Surfaces) > 0 {
		return fail("a repo that does not release names surfaces",
			"surfaces drive bumps, and nothing bumps where releases are off",
			"either set releases to true or drop the surfaces map")
	}
	if cfg.SpecFreeze && (!cfg.Releases || len(cfg.Surfaces) == 0) {
		return fail("spec_freeze is enabled without releasing surfaces",
			"freezing needs releases and at least one configured surface",
			"enable releases and list the surfaces, or disable spec_freeze")
	}
	seenBranches := map[string]PRKind{}
	for kind, branch := range cfg.BotBranches {
		if !configBotKinds[kind] {
			return fail("bot_branches names unknown kind "+strconv.Quote(string(kind)),
				"the bot PR kinds are closed so every workflow compares against the same names",
				"use one of "+closedset.Join(botKinds, ", ")+" as the key")
		}
		if branch == "" {
			return fail("bot_branches entry "+strconv.Quote(string(kind))+" names an empty branch",
				"each bot PR kind needs the one head branch its PR uses",
				"set the head branch that bot PR is opened from")
		}
		if !strings.HasPrefix(branch, BotBranchPrefix) || branch == BotBranchPrefix {
			return fail("bot_branches entry "+strconv.Quote(string(kind))+" names branch "+strconv.Quote(branch)+", which is not under "+strconv.Quote(BotBranchPrefix),
				"the workflow checker lets each bot job push only its own bot branch and cannot see the default branch, so a bot branch outside the reserved prefix could name the default branch and let a workflow push it outside the merge queue",
				"rename the branch to "+strconv.Quote(BotBranchPrefix+"<name>")+", for example "+strconv.Quote(BotBranchPrefix+string(kind)))
		}
		if first, dup := seenBranches[branch]; dup {
			return fail("bot_branches names branch "+strconv.Quote(branch)+" for two kinds",
				"one branch belongs to one bot PR: with two kinds on one branch the classifier gives another bot PR the "+PRKindRelease.String()+" PR's exemption",
				"give "+string(first)+" and "+string(kind)+" their own head branches")
		}
		seenBranches[branch] = kind
	}
	return nil
}
