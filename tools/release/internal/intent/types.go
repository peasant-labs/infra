package intent

import (
	"strconv"
	"strings"
)

// This file defines every closed set the package trades in. Closed sets are
// typed, with no bare strings crossing the package boundary: callers compare
// against named constants, and membership is asserted exactly in fixtures.

// Type is a PR-title type: the verb before the optional scope list.
type Type string

const (
	TypeFeat     Type = "feat"
	TypeFix      Type = "fix"
	TypeRelease  Type = "release"
	TypeRefactor Type = "refactor"
	TypePerf     Type = "perf"
	TypeBuild    Type = "build"
	TypeRevert   Type = "revert"
	TypeDocs     Type = "docs"
	TypeTest     Type = "test"
	TypeCI       Type = "ci"
	TypeChore    Type = "chore"
	TypeStyle    Type = "style"
)

// AllTypes is the canonical closed set of title types, in grammar order.
var AllTypes = []Type{
	TypeFeat, TypeFix, TypeRelease,
	TypeRefactor, TypePerf, TypeBuild, TypeRevert,
	TypeDocs, TypeTest, TypeCI, TypeChore, TypeStyle,
}

// IsValid reports whether the type is one of the known members.
func (t Type) IsValid() bool { return isMember(AllTypes, t) }

func (t Type) String() string { return string(t) }

// ParseType returns the member spelled raw and refuses anything else.
func ParseType(raw string) (Type, error) {
	return parseMember(AllTypes, raw, "title type", "intent.ParseType")
}

// UnmarshalText parses a type through ParseType.
func (t *Type) UnmarshalText(text []byte) error {
	member, err := ParseType(string(text))
	if err != nil {
		return err
	}
	*t = member
	return nil
}

// Class groups types by what a bump bracket means on them. The zero value is
// ClassUnknown: the class of a type outside the closed set, which permits
// nothing.
type Class int

const (
	// ClassUnknown is the class of a type that is not a known member. It is
	// not a member of AllClasses and no rule accepts it.
	ClassUnknown Class = iota
	// ClassMustRelease types always release and always carry a bracket.
	ClassMustRelease
	// ClassMayRelease types release only with a bracket.
	ClassMayRelease
	// ClassNeverRelease types never carry a bracket or a breaking mark.
	ClassNeverRelease
)

// AllClasses is the canonical closed set of type classes.
var AllClasses = []Class{ClassMustRelease, ClassMayRelease, ClassNeverRelease}

// IsValid reports whether the class is one of the known members.
func (c Class) IsValid() bool { return isMember(AllClasses, c) }

// classWords spells each class once.
var classWords = map[Class]string{
	ClassMustRelease:  "must-release",
	ClassMayRelease:   "may-release",
	ClassNeverRelease: "never-release",
}

// String returns the class's word. ClassUnknown and any other value outside
// the closed set render as an invalid marker that ParseClass refuses.
func (c Class) String() string {
	if word, ok := classWords[c]; ok {
		return word
	}
	return "invalid-class(" + strconv.Itoa(int(c)) + ")"
}

// ParseClass returns the class spelled word and refuses anything else.
func ParseClass(word string) (Class, error) {
	for _, class := range AllClasses {
		if word == classWords[class] {
			return class, nil
		}
	}
	return ClassUnknown, inputError(
		"unknown type class "+strconv.Quote(word),
		"the class set is closed, and an unknown value is never read as a member",
		"intent.ParseClass",
		"parsing a type class",
		"no bracket rule can be chosen from this value",
		"use one of "+strconv.Quote(ClassMustRelease.String())+", "+strconv.Quote(ClassMayRelease.String())+", "+strconv.Quote(ClassNeverRelease.String()),
	)
}

// UnmarshalText parses a class through ParseClass.
func (c *Class) UnmarshalText(text []byte) error {
	class, err := ParseClass(string(text))
	if err != nil {
		return err
	}
	*c = class
	return nil
}

// Class reports the release class of the type. A type outside the closed set
// has ClassUnknown: it is never read as a type that may skip a release.
func (t Type) Class() Class {
	switch t {
	case TypeFeat, TypeFix, TypeRelease:
		return ClassMustRelease
	case TypeRefactor, TypePerf, TypeBuild, TypeRevert:
		return ClassMayRelease
	case TypeDocs, TypeTest, TypeCI, TypeChore, TypeStyle:
		return ClassNeverRelease
	default:
		return ClassUnknown
	}
}

// typesOf lists the types of one class, in canonical order.
func typesOf(class Class) []Type {
	out := []Type{}
	for _, t := range AllTypes {
		if t.Class() == class {
			out = append(out, t)
		}
	}
	return out
}

// Level is a release bump level. The zero value means no release.
type Level int

const (
	// LevelNone marks a title that releases nothing.
	LevelNone Level = iota
	// LevelPatch advances the last number.
	LevelPatch
	// LevelMinor advances the middle number.
	LevelMinor
	// LevelMajor advances the first number.
	LevelMajor
)

// AllLevels is the canonical closed set of bump levels, in increasing order.
var AllLevels = []Level{LevelNone, LevelPatch, LevelMinor, LevelMajor}

// levelWords spells each level once: the title bracket, the output line and
// the fixtures all use these words.
var levelWords = map[Level]string{
	LevelNone:  "none",
	LevelPatch: "patch",
	LevelMinor: "minor",
	LevelMajor: "major",
}

// IsValid reports whether the level is one of the known members.
func (l Level) IsValid() bool { return isMember(AllLevels, l) }

// String returns the level's word. A value outside the closed set renders as
// an invalid marker that ParseLevel refuses, never as a member's word.
func (l Level) String() string {
	if word, ok := levelWords[l]; ok {
		return word
	}
	return "invalid-level(" + strconv.Itoa(int(l)) + ")"
}

// bumpLevels lists the levels a title bracket may carry: every level that
// releases, in increasing order.
var bumpLevels = func() []Level {
	out := []Level{}
	for _, level := range AllLevels {
		if level != LevelNone {
			out = append(out, level)
		}
	}
	return out
}()

// levelList renders levels for an error message.
func levelList(levels []Level) string {
	words := make([]string, 0, len(levels))
	for _, level := range levels {
		words = append(words, strconv.Quote(level.String()))
	}
	return strings.Join(words, ", ")
}

// ParseLevel returns the level spelled word and refuses anything else,
// including the empty string: no release is spelled "none".
func ParseLevel(word string) (Level, error) {
	for _, level := range AllLevels {
		if word == levelWords[level] {
			return level, nil
		}
	}
	return LevelNone, inputError(
		"unknown level "+strconv.Quote(word),
		"the level set is closed, and an unknown value is never read as a member",
		"intent.ParseLevel",
		"parsing a level",
		"no version can be computed from this value",
		"use one of "+levelList(AllLevels),
	)
}

// UnmarshalText parses a level through ParseLevel.
func (l *Level) UnmarshalText(text []byte) error {
	level, err := ParseLevel(string(text))
	if err != nil {
		return err
	}
	*l = level
	return nil
}

// Mode selects whether the release job may write. The zero value is not a
// member: it is refused wherever a mode is used.
type Mode string

const (
	// ModePlanOnly prints the plan and writes nothing.
	ModePlanOnly Mode = "plan-only"
	// ModeTag arms the job to push a tag and keep the release PR current.
	ModeTag Mode = "tag"
)

// AllModes is the canonical closed set of release-job modes.
var AllModes = []Mode{ModePlanOnly, ModeTag}

func (m Mode) String() string { return string(m) }

// IsValid reports whether the mode is one of the known members.
func (m Mode) IsValid() bool { return isMember(AllModes, m) }

// modeUnset is the value of an unset repository variable.
const modeUnset = ""

// ParseMode reads the mode a workflow passes from the VarBatchMode
// repository variable. The empty string is an unset variable and plans
// without writing; a member's own spelling selects that member. Anything
// else is an error: a misspelled value never arms the job and never passes
// for a deliberate plan-only run.
func ParseMode(raw string) (Mode, error) {
	if raw == modeUnset {
		return ModePlanOnly, nil
	}
	if mode := Mode(raw); mode.IsValid() {
		return mode, nil
	}
	return "", inputError(
		"unknown mode "+strconv.Quote(raw),
		"the mode set is closed, and an unknown value neither arms the release job nor counts as a plan-only run",
		"intent.ParseMode",
		"reading the release-job mode",
		"no plan can be made from this value",
		"set the repository variable "+VarBatchMode.String()+" to one of "+memberList(AllModes)+", or leave it unset to plan without writing",
	)
}

// UnmarshalText parses a mode through ParseMode.
func (m *Mode) UnmarshalText(text []byte) error {
	mode, err := ParseMode(string(text))
	if err != nil {
		return err
	}
	*m = mode
	return nil
}

// Action is the one thing a plan tells the release workflow to do.
type Action string

const (
	// ActionNone writes nothing; printed whenever tag mode is off.
	ActionNone Action = "none"
	// ActionVerify runs the pre-tag steps on the candidate.
	ActionVerify Action = "verify"
	// ActionTag pushes the printed tag at the candidate.
	ActionTag Action = "tag"
	// ActionOpenPR writes the release content and opens or updates the release PR.
	ActionOpenPR Action = "open-pr"
	// ActionClosePR closes the open release PR, if there is one.
	ActionClosePR Action = "close-pr"
)

// AllActions is the canonical closed set of plan actions.
var AllActions = []Action{ActionNone, ActionVerify, ActionTag, ActionOpenPR, ActionClosePR}

func (a Action) String() string { return string(a) }

// IsValid reports whether the value is one of the known members.
func (a Action) IsValid() bool { return isMember(AllActions, a) }

// ParseAction returns the member spelled raw and refuses anything else.
func ParseAction(raw string) (Action, error) {
	return parseMember(AllActions, raw, "plan action", "intent.ParseAction")
}

// UnmarshalText parses the value through ParseAction.
func (a *Action) UnmarshalText(text []byte) error {
	member, err := ParseAction(string(text))
	if err != nil {
		return err
	}
	*a = member
	return nil
}

// Verdict is the workflow's report on its pre-tag steps, and nothing else.
type Verdict string

const (
	// VerdictNone means no pre-tag report was given. It is the zero value:
	// nothing is proven.
	VerdictNone Verdict = ""
	// VerdictTaggable means every pre-tag step passed.
	VerdictTaggable Verdict = "taggable"
	// VerdictRejected means a pre-tag step failed.
	VerdictRejected Verdict = "rejected"
)

// AllVerdicts is the canonical closed set of pre-tag verdicts.
var AllVerdicts = []Verdict{VerdictNone, VerdictTaggable, VerdictRejected}

func (v Verdict) String() string { return string(v) }

// IsValid reports whether the value is one of the known members.
func (v Verdict) IsValid() bool { return isMember(AllVerdicts, v) }

// ParseVerdict returns the member spelled raw and refuses anything else.
func ParseVerdict(raw string) (Verdict, error) {
	return parseMember(AllVerdicts, raw, "pre-tag verdict", "intent.ParseVerdict")
}

// UnmarshalText parses the value through ParseVerdict.
func (v *Verdict) UnmarshalText(text []byte) error {
	member, err := ParseVerdict(string(text))
	if err != nil {
		return err
	}
	*v = member
	return nil
}

// Gate is the merge-queue hold decision for the release PR.
type Gate string

const (
	// GateArm lets the job turn auto-merge on for the release PR.
	GateArm Gate = "arm"
	// GateHold keeps the release PR unarmed while a releasing PR is ahead.
	GateHold Gate = "hold"
)

// AllGates is the canonical closed set of queue-gate outcomes.
var AllGates = []Gate{GateArm, GateHold}

func (g Gate) String() string { return string(g) }

// IsValid reports whether the value is one of the known members.
func (g Gate) IsValid() bool { return isMember(AllGates, g) }

// ParseGate returns the member spelled raw and refuses anything else.
func ParseGate(raw string) (Gate, error) {
	return parseMember(AllGates, raw, "queue gate", "intent.ParseGate")
}

// UnmarshalText parses the value through ParseGate.
func (g *Gate) UnmarshalText(text []byte) error {
	member, err := ParseGate(string(text))
	if err != nil {
		return err
	}
	*g = member
	return nil
}

// Freshness is whether a landed release candidate still matches what the
// generator produces now. Unknown means there was nothing to compare, never
// that a read failed: a read failure is an error, not a stale verdict.
type Freshness string

const (
	// FreshnessUnknown marks the absence of a candidate.
	FreshnessUnknown Freshness = "unknown"
	// FreshnessFresh marks content equal to what the generator produces now.
	FreshnessFresh Freshness = "fresh"
	// FreshnessStale marks content that no longer matches the generator.
	FreshnessStale Freshness = "stale"
)

// AllFreshness is the canonical closed set of freshness outcomes.
var AllFreshness = []Freshness{FreshnessUnknown, FreshnessFresh, FreshnessStale}

func (f Freshness) String() string { return string(f) }

// IsValid reports whether the value is one of the known members.
func (f Freshness) IsValid() bool { return isMember(AllFreshness, f) }

// ParseFreshness returns the member spelled raw and refuses anything else.
func ParseFreshness(raw string) (Freshness, error) {
	return parseMember(AllFreshness, raw, "freshness", "intent.ParseFreshness")
}

// UnmarshalText parses the value through ParseFreshness.
func (f *Freshness) UnmarshalText(text []byte) error {
	member, err := ParseFreshness(string(text))
	if err != nil {
		return err
	}
	*f = member
	return nil
}

// PRKind tells the workflows which bot PR a head branch belongs to, if any.
type PRKind string

const (
	// PRKindOrdinary is any author PR or any bot PR on an unlisted branch.
	PRKindOrdinary PRKind = "ordinary"
	// PRKindRelease is the one standing release PR.
	PRKindRelease PRKind = "release"
	// PRKindVendorHash is the dependency-hash fix PR.
	PRKindVendorHash PRKind = "vendor-hash"
	// PRKindCutRelease is the maintainer-triggered forced-release PR.
	PRKindCutRelease PRKind = "cut-release"
)

// AllPRKinds is the canonical closed set of PR kinds.
var AllPRKinds = []PRKind{PRKindOrdinary, PRKindRelease, PRKindVendorHash, PRKindCutRelease}

func (k PRKind) String() string { return string(k) }

// IsValid reports whether the value is one of the known members.
func (k PRKind) IsValid() bool { return isMember(AllPRKinds, k) }

// ParsePRKind returns the member spelled raw and refuses anything else.
func ParsePRKind(raw string) (PRKind, error) {
	return parseMember(AllPRKinds, raw, "PR kind", "intent.ParsePRKind")
}

// UnmarshalText parses the value through ParsePRKind.
func (k *PRKind) UnmarshalText(text []byte) error {
	member, err := ParsePRKind(string(text))
	if err != nil {
		return err
	}
	*k = member
	return nil
}

// botKinds lists the bot PR kinds in canonical order, derived once from
// AllPRKinds so the closed set has one home.
var botKinds = func() []PRKind {
	out := []PRKind{}
	for _, k := range AllPRKinds {
		if k != PRKindOrdinary {
			out = append(out, k)
		}
	}
	return out
}()

// ErrorCode is the closed set of typed rejections. Every member has a reject
// fixture whose message carries all six parts; codes used only by later
// work join this set with that work.
type ErrorCode string

const (
	ErrMalformed           ErrorCode = "malformed"
	ErrUnknownType         ErrorCode = "unknown-type"
	ErrMissingBump         ErrorCode = "missing-bump"
	ErrBumpOnNonReleasing  ErrorCode = "bump-on-non-releasing"
	ErrBangBumpMismatch    ErrorCode = "bang-bump-mismatch"
	ErrOldReleaseTitle     ErrorCode = "old-release-title"
	ErrReleasesDisabled    ErrorCode = "releases-disabled"
	ErrProtectedPath       ErrorCode = "protected-path"
	ErrNoFinalTag          ErrorCode = "no-final-tag"
	ErrEpochMissing        ErrorCode = "epoch-missing"
	ErrFloorNotOnChain     ErrorCode = "floor-not-on-chain"
	ErrReleaseContentStale ErrorCode = "release-content-stale"
	ErrConfigInvalid       ErrorCode = "config-invalid"
	ErrPolicyViolation     ErrorCode = "policy-violation"
)

// AllErrorCodes is the canonical closed set of typed error codes.
var AllErrorCodes = []ErrorCode{
	ErrMalformed,
	ErrUnknownType,
	ErrMissingBump,
	ErrBumpOnNonReleasing,
	ErrBangBumpMismatch,
	ErrOldReleaseTitle,
	ErrReleasesDisabled,
	ErrProtectedPath,
	ErrNoFinalTag,
	ErrEpochMissing,
	ErrFloorNotOnChain,
	ErrReleaseContentStale,
	ErrConfigInvalid,
	ErrPolicyViolation,
}

func (c ErrorCode) String() string { return string(c) }

// IsValid reports whether the value is one of the known members.
func (c ErrorCode) IsValid() bool { return isMember(AllErrorCodes, c) }

// ParseErrorCode returns the member spelled raw and refuses anything else.
func ParseErrorCode(raw string) (ErrorCode, error) {
	return parseMember(AllErrorCodes, raw, "error code", "intent.ParseErrorCode")
}

// UnmarshalText parses the value through ParseErrorCode.
func (c *ErrorCode) UnmarshalText(text []byte) error {
	member, err := ParseErrorCode(string(text))
	if err != nil {
		return err
	}
	*c = member
	return nil
}

// SemVer is a bare X.Y.Z version value, without any tag prefix. A tag is
// always TagPrefix plus a SemVer.
type SemVer string

func (s SemVer) String() string { return string(s) }

// UnmarshalText parses a version through ParseSemVer. An empty value stays
// the zero value, which means no version: a plan with nothing pending
// computes none.
func (s *SemVer) UnmarshalText(text []byte) error {
	if len(text) == 0 {
		*s = ""
		return nil
	}
	version, err := ParseSemVer(string(text))
	if err != nil {
		return err
	}
	*s = version
	return nil
}

// Title is one parsed intent title: a PR title, or a landed subject with its
// PR number attached. PR is 0 when the text is a PR title, not a landed
// subject.
type Title struct {
	Type     Type     `yaml:"type"`
	Scopes   []string `yaml:"scopes"`
	Breaking bool     `yaml:"breaking"`
	Level    Level    `yaml:"level"`
	Subject  string   `yaml:"subject"`
	PR       int      `yaml:"pr"`
}

// VersionFile names one version file and the single top-level field the
// release PR writes and protects. The rest of the file stays ordinary.
type VersionFile struct {
	Path  string `yaml:"path"`
	Field string `yaml:"field"`
}

// Config is one repo's release configuration, read from its release-intent
// file. BotBranches maps a bot PR kind to the one head branch that PR uses,
// so every workflow compares against the same names.
type Config struct {
	Releases         bool              `yaml:"releases"`
	TagPrefix        string            `yaml:"tag_prefix"`
	AutomationAuthor string            `yaml:"automation_author"`
	Changelog        string            `yaml:"changelog"`
	VersionFiles     []VersionFile     `yaml:"version_files"`
	ProtectedPaths   []string          `yaml:"protected_paths"`
	Surfaces         map[string]string `yaml:"surfaces"`
	BotBranches      map[PRKind]string `yaml:"bot_branches"`
}

// Error is an actionable rejection: what went wrong, why, where it failed,
// when it failed, what it means for the caller, and how to fix it.
type Error struct {
	Code        ErrorCode
	What, Why   string
	Where, When string
	Means, Fix  string
}

func (e *Error) Error() string {
	return "release-intent: " + e.Code.String() + ": " + e.What +
		" [why: " + e.Why +
		"; where: " + e.Where +
		"; when: " + e.When +
		"; means: " + e.Means +
		"; fix: " + e.Fix + "]"
}

// Commit is one first-parent commit on the release branch: its SHA, its
// subject, whether it touches the changelog file, and its committer date as
// YYYY-MM-DD for a dated changelog heading.
type Commit struct {
	SHA              string `yaml:"sha"`
	Subject          string `yaml:"subject"`
	ChangesChangelog bool   `yaml:"changes_changelog"`
	Date             string `yaml:"date"`
}

// Facts is everything Plan reads: the branch under plan, its tip's
// first-parent history, every tag in the repository, the grammar epoch and
// the recovery floor, the job mode and verdict, and per-commit freshness for
// the changelog-changing commits in range. The command layer fills Fresh by
// comparing each candidate through Generate; Plan only reads the entry for
// the commit it picks.
type Facts struct {
	Branch  string            `yaml:"branch"`
	Commits []Commit          `yaml:"commits"`
	Tags    map[string]string `yaml:"tags"`
	Epoch   string            `yaml:"epoch"`
	Floor   string            `yaml:"floor"`
	Mode    Mode              `yaml:"mode"`
	Verdict Verdict           `yaml:"verdict"`
	Fresh   map[string]bool   `yaml:"fresh"`
}

// PendingSet is one set of releasing subjects with what it computes: the
// subjects, the version and tag they produce, and the committer date of their
// newest commit for a dated heading. A Decision carries two: Before, the
// subjects older than the candidate release commit (the freshness check
// renders exactly this set, so the caller never re-plans a sub-range), and
// Full, the whole range.
type PendingSet struct {
	Pending []Title `yaml:"pending"`
	Version SemVer  `yaml:"version"`
	Tag     string  `yaml:"tag"`
	Date    string  `yaml:"date"`
}

// Decision is the whole plan: the one action plus every value the workflow
// needs to carry it out. For verify and tag, Pending, Level, Breaking,
// Version and Tag come from the subjects older than the candidate, and Before
// repeats that same set with its date; the subjects after the candidate stay
// pending for the next release. Full holds the whole range's set in the same
// shape, always filled when anything is pending, whatever the verdict; the
// release-pr write path renders Full. Date is the committer date of the
// newest commit in Pending, for the dated heading of the set being rendered.
type Decision struct {
	Action         Action     `yaml:"action"`
	RangeBase      string     `yaml:"range_base"`
	VersionBase    string     `yaml:"version_base"`
	Candidate      string     `yaml:"candidate"`
	CandidateFresh Freshness  `yaml:"candidate_fresh"`
	Pending        []Title    `yaml:"pending"`
	Level          Level      `yaml:"level"`
	Breaking       bool       `yaml:"breaking"`
	Version        SemVer     `yaml:"version"`
	Tag            string     `yaml:"tag"`
	Before         PendingSet `yaml:"before"`
	Full           PendingSet `yaml:"full"`
	Date           string     `yaml:"date"`
}

// ContentInput is what the release-content generator renders.
type ContentInput struct {
	Pending       []Title `yaml:"pending"`
	BaseChangelog string  `yaml:"base_changelog"`
	Version       SemVer  `yaml:"version"`
	Tag           string  `yaml:"tag"`
	Date          string  `yaml:"date"`
}

// Content is the generated release content: the full new changelog text, the
// new section on its own, and one value per version file path.
type Content struct {
	Changelog string            `yaml:"changelog"`
	Section   string            `yaml:"section"`
	Versions  map[string]string `yaml:"versions"`
}

// QueueEntry is one merge-queue entry the gate judges.
type QueueEntry struct {
	Position int    `yaml:"position"`
	Number   int    `yaml:"number"`
	Title    string `yaml:"title"`
}
