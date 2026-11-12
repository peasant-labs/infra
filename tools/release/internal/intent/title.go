package intent

import (
	regexp "github.com/wasilibs/go-re2"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The intent title grammar:
//
//	title  = type ["(" scopes ")"] ["!"] ["[" bump "]"] ": " subject
//	landed = title [" (#" digits ")"]
//
// Scopes are free-form lowercase words. A bracket is required on the
// must-release types, optional on the may-release types, and forbidden on the
// never-release types. The landed suffix is the squash-commit PR number.
//
// Every pattern is built from the syntax marks and the typed members that
// FormatTitle writes, so the parser and the formatter cannot drift apart.
var (
	oldReleasePattern = regexp.MustCompile(
		`^` + regexp.QuoteMeta(TypeRelease.String()+titleScopeOpen) + `v?[0-9]+\.[0-9]+\.[0-9]+[^)]*` + regexp.QuoteMeta(titleScopeClose))
	landedSuffix = regexp.MustCompile(
		regexp.QuoteMeta(titleLandedOpen) + `([0-9]+)` + regexp.QuoteMeta(titleLandedClose) + `$`)
	titlePattern = regexp.MustCompile(
		`^([a-z]+)` +
			`(` + regexp.QuoteMeta(titleScopeOpen) + `([^()]*)` + regexp.QuoteMeta(titleScopeClose) + `)?` +
			`(` + regexp.QuoteMeta(titleBreakingMark) + `)?` +
			`(` + regexp.QuoteMeta(titleLevelOpen) + `(` + bumpLevelAlternation() + `)` + regexp.QuoteMeta(titleLevelClose) + `)?` +
			regexp.QuoteMeta(titleSubjectSep) + `(\S.*)$`)
	scopePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
)

// bumpLevelAlternation renders the bracket levels as a pattern alternation.
func bumpLevelAlternation() string {
	words := make([]string, 0, len(bumpLevels))
	for _, level := range bumpLevels {
		words = append(words, regexp.QuoteMeta(level.String()))
	}
	return strings.Join(words, "|")
}

// typeList renders types for an error message, in canonical order.
func typeList(types []Type) string {
	words := make([]string, 0, len(types))
	for _, t := range types {
		words = append(words, t.String())
	}
	return strings.Join(words, ", ")
}

// exampleTitle renders a valid example title for an error message.
func exampleTitle(typ Type, scope string, breaking bool, level Level, subject string) string {
	t := Title{Type: typ, Breaking: breaking, Level: level, Subject: subject}
	if scope != "" {
		t.Scopes = []string{scope}
	}
	return strconv.Quote(FormatTitle(t))
}

// SplitLandedSubject splits a landed subject into its title text and PR
// number. A PR title without the squash suffix returns pr 0. It is the one
// rule for the suffix: callers never re-derive it.
func SplitLandedSubject(raw string) (text string, pr int) {
	if m := landedSuffix.FindStringSubmatch(raw); m != nil {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			return raw, 0
		}
		return raw[:len(raw)-len(m[0])], n
	}
	return raw, 0
}

// ParseTitle parses one intent title against the release config and the
// repo's current version, which decides what a breaking mark may pair with.
// An empty current reads as below 1.0. It accepts both PR titles and landed
// subjects; a landed subject sets Title.PR to its PR number.
func ParseTitle(cfg Config, current SemVer, raw string) (Title, error) {
	if !utf8.ValidString(raw) {
		return Title{}, rejection(ErrMalformed, raw,
			"the title is not valid UTF-8",
			"a title is text, and bytes that are not text match no part of the grammar",
			"the title-guard check is red, so the merge queue will not accept this PR",
			"retitle in plain text, for example "+exampleTitle(TypeFeat, "graph", false, LevelMinor, "add a facet"))
	}
	trimmed := strings.TrimSpace(raw)
	if oldReleasePattern.MatchString(trimmed) {
		return Title{}, rejection(ErrOldReleaseTitle, raw,
			"the title uses the old release form",
			"release titles carry a level, never a number, since the release job computes every version",
			"the title-guard check is red, so the merge queue will not accept this PR",
			"retitle with a level, for example "+exampleTitle(TypeRelease, "", false, LevelMinor, "publish the pending changes")+", and drop any version or changelog edit from the PR")
	}

	text, pr := SplitLandedSubject(trimmed)

	m := titlePattern.FindStringSubmatch(text)
	if m == nil {
		return Title{}, rejection(ErrMalformed, raw,
			"the title does not match the intent grammar",
			"a title is a type, an optional scope list, an optional breaking mark, an optional level, then \": \" and a subject",
			"the title-guard check is red, so the merge queue will not accept this PR",
			"retitle in the grammar, for example "+exampleTitle(TypeFeat, "graph", false, LevelMinor, "add a facet")+"; a GitHub Revert title needs a fresh title in this form")
	}

	typ, typeErr := ParseType(m[1])
	if typeErr != nil {
		return Title{}, rejection(ErrUnknownType, raw,
			"type "+strconv.Quote(m[1])+" is not a known title type",
			"the type set is closed so every repo judges the same verbs",
			"the title-guard check is red, so the merge queue will not accept this PR",
			"pick a type from the closed set: "+typeList(AllTypes))
	}

	scopes, ok := parseScopes(m[3], m[2] != "")
	if !ok {
		return Title{}, rejection(ErrMalformed, raw,
			"the scope list "+strconv.Quote(m[2])+" does not parse",
			"scopes are comma-separated lowercase words with no spaces, for example \"(graph)\" or \"(graph,table)\"",
			"the title-guard check is red, so the merge queue will not accept this PR",
			"rename each scope to lowercase letters, digits and hyphens with no spaces, or drop the list")
	}

	breaking := m[4] == titleBreakingMark
	level := LevelNone
	if m[6] != "" {
		parsed, levelErr := ParseLevel(m[6])
		if levelErr != nil {
			return Title{}, levelErr
		}
		level = parsed
	}

	if !cfg.Releases && (level != LevelNone || breaking) {
		return Title{}, rejection(ErrReleasesDisabled, raw,
			"the title carries a release marker in a repo that does not release",
			"this repo has releases off, so no title may carry a level or a breaking mark",
			"the title-guard check is red, so the merge queue will not accept this PR",
			"drop the bracket and the breaking mark, for example "+exampleTitle(TypeFeat, "graph", false, LevelNone, "add a facet"))
	}

	switch typ.Class() {
	case ClassMustRelease:
		if cfg.Releases && level == LevelNone {
			return Title{}, rejection(ErrMissingBump, raw,
				"type "+typ.String()+" releases but the title has no level",
				"the types "+typeList(typesOf(ClassMustRelease))+" always release, and nothing is inferred from the type",
				"the title-guard check is red, so the merge queue will not accept this PR",
				"add a level, for example "+exampleTitle(typ, "", false, LevelMinor, "...")+" (or "+titleLevelOpen+LevelPatch.String()+titleLevelClose+"); if the change ships nothing, use a type that need not release with no bracket: "+typeList(append(typesOf(ClassMayRelease), typesOf(ClassNeverRelease)...)))
		}
	case ClassNeverRelease:
		if level != LevelNone || breaking {
			return Title{}, rejection(ErrBumpOnNonReleasing, raw,
				"type "+typ.String()+" never releases but the title carries a release marker",
				"the types "+typeList(typesOf(ClassNeverRelease))+" ship no release, so a bracket or a breaking mark on them contradicts the type",
				"the title-guard check is red, so the merge queue will not accept this PR",
				"drop the bracket and the breaking mark, or change the type to one that releases")
		}
	case ClassMayRelease:
		// A bracket is optional: with one the title releases, without one
		// it does not.
	default:
		return Title{}, inputError(
			"type "+strconv.Quote(typ.String())+" has no release class",
			"every known type belongs to exactly one class, and an unclassed type permits nothing",
			"intent.ParseTitle",
			"checking the title's level against its type",
			"the title cannot be judged",
			"give the type a class in Type.Class before adding it to the closed set",
		)
	}

	if breaking {
		below10 := true
		if current != "" {
			major, _, _, splitErr := splitSemVer(current)
			if splitErr != nil {
				return Title{}, inputError(
					"invalid current version "+strconv.Quote(string(current)),
					"the current version is the version base and must be a bare X.Y.Z value",
					"intent.ParseTitle",
					"checking what a breaking mark may pair with",
					"the breaking rule cannot be judged from this version",
					"pass the highest final release tag without its prefix, for example \"0.1.0\"",
				)
			}
			below10 = major == 0
		}
		want := LevelMinor
		if !below10 {
			want = LevelMajor
		}
		if level != want {
			return Title{}, rejection(ErrBangBumpMismatch, raw,
				"the breaking mark pairs with "+bracket(level)+" but this repo needs "+bracket(want),
				bangWhy(below10),
				"the title-guard check is red, so the merge queue will not accept this PR",
				"write the break as "+strconv.Quote(titleBreakingMark+bracket(want))+", for example "+exampleTitle(TypeFeat, "graph", true, want, "drop the legacy field"))
		}
	} else if level == LevelMajor {
		return Title{}, rejection(ErrBangBumpMismatch, raw,
			"the title carries "+bracket(LevelMajor)+" with no breaking mark",
			"a major level only exists alongside a declared break; no title mints the next major by itself",
			"the title-guard check is red, so the merge queue will not accept this PR",
			"add the breaking mark for the repo's form, or lower the level to "+bracket(LevelMinor))
	}

	return Title{
		Type:     typ,
		Scopes:   scopes,
		Breaking: breaking,
		Level:    level,
		Subject:  m[7],
		PR:       pr,
	}, nil
}

// bangWhy states the breaking rule for the repo's side of 1.0.
func bangWhy(below10 bool) string {
	if below10 {
		return "below 1.0 a break is written " + titleBreakingMark + bracket(LevelMinor) + " and moves the middle number; " +
			titleBreakingMark + bracket(LevelPatch) + ", " + titleBreakingMark + bracket(LevelMajor) + " and " + bracket(LevelMajor) + " are rejected"
	}
	return "from 1.0 a break is written " + titleBreakingMark + bracket(LevelMajor) + "; " + titleBreakingMark + " with " +
		bracket(LevelMinor) + " or " + bracket(LevelPatch) + ", and " + bracket(LevelMajor) + " with no " + titleBreakingMark + ", are rejected"
}

// bracket renders a level in its title bracket.
func bracket(level Level) string {
	return titleLevelOpen + level.String() + titleLevelClose
}

// parseScopes validates one scope list. present reports whether the title
// carried parentheses at all; empty parentheses are malformed.
func parseScopes(inner string, present bool) ([]string, bool) {
	if !present {
		return []string{}, true
	}
	parts := strings.Split(inner, titleScopeSep)
	scopes := make([]string, 0, len(parts))
	for _, part := range parts {
		if !scopePattern.MatchString(part) {
			return nil, false
		}
		scopes = append(scopes, part)
	}
	return scopes, true
}
