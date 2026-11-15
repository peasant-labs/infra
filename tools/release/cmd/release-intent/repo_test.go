package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
	"gopkg.in/yaml.v3"
)

// This file is the one builder every temp-repository test uses. A test picks
// a release config from the repos fixture, and the builder writes the config,
// the changelog and the version documents to the paths that config names.
// Refs are built with the contract's GitRef, and commit subjects with the
// contract's title formatter from typed members.

//go:embed testdata/repos.yaml
var reposFixtureYAML []byte

// repoName names one release config in the repos fixture.
type repoName string

const (
	repoStandard            repoName = "standard"
	repoAllBots             repoName = "all-bots"
	repoReleaseOnWorkBranch repoName = "release-on-work-branch"
	repoUnprotected         repoName = "unprotected"
	repoProtectedFile       repoName = "protected-file"
)

// allRepoNames is the closed set of configs the fixture must carry, exactly.
var allRepoNames = []repoName{repoStandard, repoAllBots, repoReleaseOnWorkBranch, repoUnprotected, repoProtectedFile}

// repoFixtures is the parsed repos fixture: validated configs by name, the
// trunk ref, the seed version and the changelog every repository starts with.
type repoFixtures struct {
	Trunk          intent.GitRef
	SeedVersion    intent.SemVer
	ChangelogTitle string
	Configs        map[repoName]intent.Config
}

func loadRepoFixtures(t *testing.T) repoFixtures {
	t.Helper()
	var doc struct {
		Trunk          string   `yaml:"trunk"`
		SeedVersion    string   `yaml:"seed_version"`
		ChangelogTitle string   `yaml:"changelog_title"`
		RequiredNames  []string `yaml:"required_names"`
		Repos          []struct {
			Name    string `yaml:"name"`
			Purpose string `yaml:"purpose"`
			Config  string `yaml:"config"`
		} `yaml:"repos"`
	}
	if err := strictDecode(reposFixtureYAML, &doc); err != nil {
		t.Fatalf("repos fixture: cannot parse: %v", err)
	}
	trunk, err := intent.ParseGitRef(intent.BranchRef(doc.Trunk).String())
	if err != nil {
		t.Fatalf("repos fixture: trunk %q is not a branch name: %v", doc.Trunk, err)
	}
	seed, err := intent.ParseSemVer(doc.SeedVersion)
	if err != nil {
		t.Fatalf("repos fixture: seed_version %q: %v", doc.SeedVersion, err)
	}
	if doc.ChangelogTitle == "" {
		t.Fatalf("repos fixture: changelog_title is empty")
	}
	out := repoFixtures{Trunk: trunk, SeedVersion: seed, ChangelogTitle: doc.ChangelogTitle, Configs: map[repoName]intent.Config{}}
	seen := map[string]struct{}{}
	for _, r := range doc.Repos {
		if strings.TrimSpace(r.Purpose) == "" {
			t.Fatalf("repos fixture: repo %q states no purpose", r.Name)
		}
		name := repoName(r.Name)
		if !slices.Contains(allRepoNames, name) {
			t.Fatalf("repos fixture: repo %q is not a member of the builder's config set %v", r.Name, allRepoNames)
		}
		if _, dup := seen[r.Name]; dup {
			t.Fatalf("repos fixture: repo %q appears twice", r.Name)
		}
		seen[r.Name] = struct{}{}
		cfg, err := intent.ParseConfig([]byte(r.Config))
		if err != nil {
			t.Fatalf("repos fixture: repo %q config is invalid: %v", r.Name, err)
		}
		out.Configs[name] = cfg
	}
	checkRequiredNames(t, doc.RequiredNames, seen, "repos fixture")
	for _, name := range allRepoNames {
		if _, ok := out.Configs[name]; !ok {
			t.Fatalf("repos fixture: config %q is missing", name)
		}
	}
	return out
}

// repoGitVerb is the closed set of git subcommands the builder runs to write
// a test repository. It is separate from the production runner's allowlist,
// which only reads.
type repoGitVerb string

const (
	verbInit     repoGitVerb = "init"
	verbAdd      repoGitVerb = "add"
	verbCommit   repoGitVerb = "commit"
	verbTag      repoGitVerb = "tag"
	verbCheckout repoGitVerb = "checkout"
	verbMerge    repoGitVerb = "merge"
	verbRemove   repoGitVerb = "rm"
	verbMove     repoGitVerb = "mv"
	verbRevParse repoGitVerb = "rev-parse"
	verbDiff     repoGitVerb = "diff"
)

// allRepoGitVerbs is the canonical closed set of builder git subcommands.
var allRepoGitVerbs = []repoGitVerb{
	verbInit, verbAdd, verbCommit, verbTag, verbCheckout, verbMerge,
	verbRemove, verbMove, verbRevParse, verbDiff,
}

// headRev is git's name for the checked-out commit.
const headRev = "HEAD"

// testRepo is one temp repository built from a validated release config.
type testRepo struct {
	t   *testing.T
	fix repoFixtures
	dir string
	cfg intent.Config
}

// newTestRepo initialises an empty repository on the fixture's trunk for the
// named config. It writes nothing: call Seed, or write files and Commit.
func newTestRepo(t *testing.T, name repoName) *testRepo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	fix := loadRepoFixtures(t)
	r := &testRepo{t: t, fix: fix, dir: t.TempDir(), cfg: fix.Configs[name]}
	r.git(verbInit, "-q", "-b", r.branchName(fix.Trunk))
	return r
}

// git runs one builder git subcommand in the repository and returns stdout.
func (r *testRepo) git(verb repoGitVerb, args ...string) string {
	r.t.Helper()
	return r.gitEnv(nil, verb, args...)
}

func (r *testRepo) gitEnv(extraEnv []string, verb repoGitVerb, args ...string) string {
	r.t.Helper()
	if !slices.Contains(allRepoGitVerbs, verb) {
		r.t.Fatalf("test repo: git subcommand %q is not in the builder's closed set", verb)
	}
	cmd := exec.Command("git", append([]string{string(verb)}, args...)...)
	cmd.Dir = r.dir
	cmd.Env = append(hermeticGitEnv(r.t), extraEnv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		r.t.Fatalf("test repo: git %s %v: %v\n%s", verb, args, err, stderr.String())
	}
	return stdout.String()
}

// Dir is the repository's working tree.
func (r *testRepo) Dir() string { return r.dir }

// Config is the validated release config the repository was built with.
func (r *testRepo) Config() intent.Config { return r.cfg }

// Runner returns the production git runner rooted at the repository.
func (r *testRepo) Runner() GitRunner {
	r.t.Helper()
	runner, err := newExecGitIn(r.dir)
	if err != nil {
		r.t.Fatalf("test repo: newExecGitIn: %v", err)
	}
	return runner
}

// --- refs ---

// Trunk is the branch the repository starts on.
func (r *testRepo) Trunk() intent.GitRef { return r.fix.Trunk }

// Branch builds the ref of a work branch from its short name.
func (r *testRepo) Branch(short string) intent.GitRef {
	r.t.Helper()
	ref, err := intent.ParseGitRef(intent.BranchRef(short).String())
	if err != nil {
		r.t.Fatalf("test repo: branch %q: %v", short, err)
	}
	return ref
}

// BotBranch is the head branch the config assigns to a bot PR kind.
func (r *testRepo) BotBranch(kind intent.PRKind) intent.GitRef {
	r.t.Helper()
	short, ok := r.cfg.BotBranches[kind]
	if !ok {
		r.t.Fatalf("test repo: the config names no bot branch for kind %q", kind)
	}
	return r.Branch(short)
}

// TagName is the release tag the config's prefix gives a version.
func (r *testRepo) TagName(version intent.SemVer) string {
	return r.cfg.TagPrefix + version.String()
}

// TagRef is the ref of the release tag for a version.
func (r *testRepo) TagRef(version intent.SemVer) intent.GitRef {
	return intent.TagRef(r.TagName(version))
}

func (r *testRepo) branchName(ref intent.GitRef) string {
	r.t.Helper()
	short, ok := ref.Branch()
	if !ok {
		r.t.Fatalf("test repo: ref %q is not a branch ref", ref)
	}
	return short
}

// Resolve returns the commit a ref points at, peeling an annotated tag.
func (r *testRepo) Resolve(ref intent.GitRef) string {
	r.t.Helper()
	return r.revParse(ref.String())
}

// Head returns the checked-out commit.
func (r *testRepo) Head() string {
	r.t.Helper()
	return r.revParse(headRev)
}

// Parent returns the first parent of the checked-out commit.
func (r *testRepo) Parent() string {
	r.t.Helper()
	return r.revParse(headRev + "~1")
}

func (r *testRepo) revParse(rev string) string {
	r.t.Helper()
	return strings.TrimSpace(r.git(verbRevParse, "--verify", "--end-of-options", rev+"^{commit}"))
}

// CreateBranch creates the branch at the checked-out commit and switches to it.
func (r *testRepo) CreateBranch(ref intent.GitRef) {
	r.t.Helper()
	r.git(verbCheckout, "-q", "-b", r.branchName(ref))
}

// CreateOrphan switches to a new branch with no history.
func (r *testRepo) CreateOrphan(ref intent.GitRef) {
	r.t.Helper()
	r.git(verbCheckout, "-q", "--orphan", r.branchName(ref))
}

// Checkout switches to an existing branch.
func (r *testRepo) Checkout(ref intent.GitRef) {
	r.t.Helper()
	r.git(verbCheckout, "-q", r.branchName(ref))
}

// Merge merges the branch into the checked-out one with a merge commit.
func (r *testRepo) Merge(ref intent.GitRef, title intent.Title) string {
	r.t.Helper()
	r.git(verbMerge, "-q", "--no-ff", "-m", r.subject(title), r.branchName(ref))
	return r.Head()
}

// Tag puts the lightweight release tag for the version on the checked-out
// commit.
func (r *testRepo) Tag(version intent.SemVer) {
	r.t.Helper()
	r.git(verbTag, r.TagName(version))
}

// TagAnnotated puts an annotated release tag for the version on the
// checked-out commit, so a tag object sits between the name and the commit.
func (r *testRepo) TagAnnotated(version intent.SemVer) {
	r.t.Helper()
	r.git(verbTag, "-a", r.TagName(version), "-m", r.TagName(version))
}

// TagsAt lists the tags pointing at a commit.
func (r *testRepo) TagsAt(sha string) []string {
	r.t.Helper()
	return strings.Fields(r.git(verbTag, "--points-at", sha))
}

// --- files the config names ---

// Seed writes the config, the changelog, one version document per version
// file carrying the seed version, and every whole-file protected path.
func (r *testRepo) Seed() {
	r.t.Helper()
	r.WriteConfig(r.cfg)
	r.WriteChangelog(r.fix.ChangelogTitle)
	for i := range r.cfg.VersionFiles {
		r.writeVersionDocument(i, map[string]any{r.cfg.VersionFiles[i].Field: r.fix.SeedVersion.String()})
	}
	for _, path := range r.cfg.ProtectedPaths {
		r.WriteFile(path, "guarded\n")
	}
}

// WriteConfig writes a validated config to the contract's config path. The
// rendered bytes must parse back to the same config, so the file on disk is
// the config the test holds.
func (r *testRepo) WriteConfig(cfg intent.Config) {
	r.t.Helper()
	r.WriteFile(intent.DefaultConfigPath, string(marshalConfig(r.t, cfg)))
}

// WriteRawConfig writes config bytes as they are. It is for a must-fail row
// whose config is invalid on purpose.
func (r *testRepo) WriteRawConfig(raw string) {
	r.t.Helper()
	r.WriteFile(intent.DefaultConfigPath, raw)
}

// marshalConfig renders a config as the YAML document ParseConfig reads.
func marshalConfig(t *testing.T, cfg intent.Config) []byte {
	t.Helper()
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("test repo: cannot render the release config: %v", err)
	}
	back, err := intent.ParseConfig(data)
	if err != nil {
		t.Fatalf("test repo: the rendered release config does not parse: %v\n%s", err, data)
	}
	if !reflect.DeepEqual(normalConfig(back), normalConfig(cfg)) {
		t.Fatalf("test repo: the rendered release config parses to a different config:\n got %+v\nwant %+v", back, cfg)
	}
	return data
}

// normalConfig maps empty collections to nil so a round trip through YAML
// compares equal.
func normalConfig(cfg intent.Config) intent.Config {
	if len(cfg.VersionFiles) == 0 {
		cfg.VersionFiles = nil
	}
	if len(cfg.ProtectedPaths) == 0 {
		cfg.ProtectedPaths = nil
	}
	if len(cfg.Surfaces) == 0 {
		cfg.Surfaces = nil
	}
	if len(cfg.BotBranches) == 0 {
		cfg.BotBranches = nil
	}
	return cfg
}

// ChangelogPath is the changelog in the working tree.
func (r *testRepo) ChangelogPath() string { return filepath.Join(r.dir, r.cfg.Changelog) }

// WriteChangelog replaces the changelog the config names.
func (r *testRepo) WriteChangelog(text string) {
	r.t.Helper()
	r.WriteFile(r.cfg.Changelog, text)
}

// AppendChangelog appends text to the changelog the config names.
func (r *testRepo) AppendChangelog(text string) {
	r.t.Helper()
	r.WriteChangelog(r.ReadChangelog() + text)
}

// ReadChangelog reads the changelog from the working tree.
func (r *testRepo) ReadChangelog() string {
	r.t.Helper()
	return r.ReadFile(r.cfg.Changelog)
}

func (r *testRepo) versionFile(i int) intent.VersionFile {
	r.t.Helper()
	if i < 0 || i >= len(r.cfg.VersionFiles) {
		r.t.Fatalf("test repo: the config has %d version file(s), no index %d", len(r.cfg.VersionFiles), i)
	}
	return r.cfg.VersionFiles[i]
}

func (r *testRepo) versionDocument(i int) map[string]any {
	r.t.Helper()
	doc := map[string]any{}
	if err := json.Unmarshal([]byte(r.ReadFile(r.versionFile(i).Path)), &doc); err != nil {
		r.t.Fatalf("test repo: version file %q is not a JSON object: %v", r.versionFile(i).Path, err)
	}
	return doc
}

func (r *testRepo) writeVersionDocument(i int, doc map[string]any) {
	r.t.Helper()
	data, err := json.Marshal(doc)
	if err != nil {
		r.t.Fatalf("test repo: cannot render version file %q: %v", r.versionFile(i).Path, err)
	}
	r.WriteFile(r.versionFile(i).Path, string(data)+"\n")
}

// SetVersionField sets the protected field of version file i.
func (r *testRepo) SetVersionField(i int, version intent.SemVer) {
	r.t.Helper()
	doc := r.versionDocument(i)
	doc[r.versionFile(i).Field] = version.String()
	r.writeVersionDocument(i, doc)
}

// RemoveVersionField drops the protected field from version file i and
// leaves the rest of the document.
func (r *testRepo) RemoveVersionField(i int) {
	r.t.Helper()
	doc := r.versionDocument(i)
	delete(doc, r.versionFile(i).Field)
	r.writeVersionDocument(i, doc)
}

// SetOrdinaryField sets a top-level field of version file i other than the
// protected one.
func (r *testRepo) SetOrdinaryField(i int, key string, value any) {
	r.t.Helper()
	if key == r.versionFile(i).Field {
		r.t.Fatalf("test repo: %q is the protected field of %q; use SetVersionField", key, r.versionFile(i).Path)
	}
	doc := r.versionDocument(i)
	doc[key] = value
	r.writeVersionDocument(i, doc)
}

// WriteVersionDocument replaces version file i with the given bytes. It is
// for a row whose document shape is the input under test.
func (r *testRepo) WriteVersionDocument(i int, raw string) {
	r.t.Helper()
	r.WriteFile(r.versionFile(i).Path, raw)
}

// ReadVersionField reads the protected field of version file i from the
// working tree through the contract's reader.
func (r *testRepo) ReadVersionField(i int) string {
	r.t.Helper()
	value, err := intent.ReadVersionField([]byte(r.ReadFile(r.versionFile(i).Path)), r.versionFile(i).Field)
	if err != nil {
		r.t.Fatalf("test repo: read the protected field of %q: %v", r.versionFile(i).Path, err)
	}
	return value
}

// ReadVersionDocument reads version file i from the working tree.
func (r *testRepo) ReadVersionDocument(i int) string {
	r.t.Helper()
	return r.ReadFile(r.versionFile(i).Path)
}

// RemoveVersionFile deletes version file i from the working tree and the
// index.
func (r *testRepo) RemoveVersionFile(i int) {
	r.t.Helper()
	r.git(verbRemove, "-qf", "--", r.versionFile(i).Path)
}

// ChangedLines reports the added and deleted line counts of version file i
// against the index.
func (r *testRepo) ChangedLines(i int) string {
	r.t.Helper()
	fields := strings.Fields(r.git(verbDiff, "--numstat", "--", r.versionFile(i).Path))
	if len(fields) < 2 {
		return ""
	}
	return fields[0] + " added, " + fields[1] + " deleted"
}

// --- ordinary files ---

// WriteFile writes a repo-relative file.
func (r *testRepo) WriteFile(name, content string) {
	r.t.Helper()
	writeTreeFile(r.t, r.dir, name, content)
}

// ReadFile reads a repo-relative file from the working tree.
func (r *testRepo) ReadFile(name string) string {
	r.t.Helper()
	data, err := os.ReadFile(filepath.Join(r.dir, name))
	if err != nil {
		r.t.Fatalf("test repo: read %q: %v", name, err)
	}
	return string(data)
}

// Rename moves a tracked file.
func (r *testRepo) Rename(from, to string) {
	r.t.Helper()
	r.git(verbMove, "--", from, to)
}

// Remove deletes a tracked file.
func (r *testRepo) Remove(name string) {
	r.t.Helper()
	r.git(verbRemove, "-q", "--", name)
}

// --- commits ---

// commitOption adjusts one commit.
type commitOption func(*commitSpec)

type commitSpec struct {
	date       string
	only       []string
	allowEmpty bool
}

// at pins the author and committer date, so a dated changelog heading is
// deterministic.
func at(date string) commitOption { return func(s *commitSpec) { s.date = date } }

// only stages just the named files, leaving other working-tree changes
// uncommitted.
func only(paths ...string) commitOption { return func(s *commitSpec) { s.only = paths } }

// empty allows a commit that changes no file.
func empty() commitOption { return func(s *commitSpec) { s.allowEmpty = true } }

// subject renders a typed title with the contract's formatter and proves the
// config's grammar accepts it, so a setup commit is never malformed by
// accident.
func (r *testRepo) subject(title intent.Title) string {
	r.t.Helper()
	text := intent.FormatTitle(title)
	if _, err := intent.ParseTitle(r.cfg, r.fix.SeedVersion, text); err != nil {
		r.t.Fatalf("test repo: setup title %+v renders %q, which the grammar refuses: %v", title, text, err)
	}
	return text
}

// Commit stages the working tree and commits it under a typed title. It
// returns the new commit.
func (r *testRepo) Commit(title intent.Title, opts ...commitOption) string {
	r.t.Helper()
	return r.commit(r.subject(title), opts)
}

// CommitRaw commits under subject text as it is. It is for a must-fail row
// whose subject is malformed on purpose.
func (r *testRepo) CommitRaw(subject string, opts ...commitOption) string {
	r.t.Helper()
	return r.commit(subject, opts)
}

func (r *testRepo) commit(subject string, opts []commitOption) string {
	r.t.Helper()
	var spec commitSpec
	for _, opt := range opts {
		opt(&spec)
	}
	if len(spec.only) > 0 {
		r.git(verbAdd, append([]string{"--"}, spec.only...)...)
	} else {
		r.git(verbAdd, "-A")
	}
	args := []string{"-q", "-m", subject}
	if spec.allowEmpty {
		args = append(args, "--allow-empty")
	}
	var env []string
	if spec.date != "" {
		env = []string{"GIT_AUTHOR_DATE=" + spec.date, "GIT_COMMITTER_DATE=" + spec.date}
	}
	r.gitEnv(env, verbCommit, args...)
	return r.Head()
}

// Run runs the built binary in the repository.
func (r *testRepo) Run(argv ...string) processResult {
	r.t.Helper()
	return runBinary(r.t, r.dir, argv, "", nil)
}

// --- shared helpers ---

func writeTreeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir for %q: %v", name, err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write %q: %v", name, err)
	}
}

// hermeticGitEnv is the environment for every git and binary child process:
// no ambient git variables or user config, a throwaway home, and a pinned
// identity.
func hermeticGitEnv(t *testing.T) []string {
	t.Helper()
	home := t.TempDir()
	env := make([]string, 0, len(os.Environ())+8)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "GIT_") || key == "HOME" || key == "XDG_CONFIG_HOME" || key == githubOutputEnv {
			continue
		}
		env = append(env, entry)
	}
	return append(env,
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, "xdg"),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
}

// --- typed titles for setup commits ---

// titled builds a setup title from typed members.
func titled(typ intent.Type, scope string, level intent.Level, subject string) intent.Title {
	return intent.Title{Type: typ, Scopes: []string{scope}, Level: level, Subject: subject}
}

// releaseTitled builds a scope-less release title at a level.
func releaseTitled(level intent.Level, subject string) intent.Title {
	return intent.Title{Type: intent.TypeRelease, Level: level, Subject: subject}
}

var (
	// titleSeed is the first commit of a seeded repository.
	titleSeed = titled(intent.TypeFeat, "init", intent.LevelMinor, "seed")
	// titleAddFacet is one pending minor.
	titleAddFacet = titled(intent.TypeFeat, "graph", intent.LevelMinor, "add facet")
	// titleDocs is a commit that releases nothing.
	titleDocs = titled(intent.TypeDocs, "readme", intent.LevelNone, "touch docs")
)

// --- common shapes ---

// newSeededRepo is a repository whose one commit holds the seeded files.
func newSeededRepo(t *testing.T, name repoName) *testRepo {
	t.Helper()
	r := newTestRepo(t, name)
	r.Seed()
	r.Commit(titleSeed)
	return r
}

// newReleasedRepo is a seeded repository whose seed commit carries the
// release tag for the seed version.
func newReleasedRepo(t *testing.T, name repoName) *testRepo {
	t.Helper()
	r := newSeededRepo(t, name)
	r.Tag(r.fix.SeedVersion)
	return r
}

// newPendingRepo is a released repository with one pending minor on top.
func newPendingRepo(t *testing.T, name repoName) *testRepo {
	t.Helper()
	r := newReleasedRepo(t, name)
	r.WriteFile("work.txt", "more\n")
	r.Commit(titleAddFacet)
	return r
}
