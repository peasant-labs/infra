package config

import (
	"path"
	"strings"
)

// MatchPath reports whether file matches an exempt path. Both are relative to
// the config's directory with `/` separators.
//
// A path is a glob: `*`, `?` and `[…]` match within one segment, and a `**`
// segment matches any number of segments, none included. A path without any
// wildcard also matches every file below it, so `backend/gen` exempts the
// directory.
func MatchPath(pattern, file string) bool {
	if !strings.ContainsAny(pattern, "*?[") {
		return file == pattern || strings.HasPrefix(file, pattern+"/")
	}
	return matchSegments(strings.Split(pattern, "/"), strings.Split(file, "/"))
}

func matchSegments(pat, name []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			rest := pat[1:]
			for i := 0; i <= len(name); i++ {
				if matchSegments(rest, name[i:]) {
					return true
				}
			}
			return false
		}
		if len(name) == 0 {
			return false
		}
		ok, err := path.Match(pat[0], name[0])
		if err != nil || !ok {
			return false
		}
		pat, name = pat[1:], name[1:]
	}
	return len(name) == 0
}

// globBase returns the leading segments of a pattern that hold no wildcard:
// the directory a walk for its matches starts from.
func globBase(pattern string) string {
	segs := strings.Split(pattern, "/")
	var base []string
	for _, s := range segs {
		if strings.ContainsAny(s, "*?[") {
			break
		}
		base = append(base, s)
	}
	if len(base) == 0 {
		return "."
	}
	return strings.Join(base, "/")
}

// globSegments rewrites `**` to `*` so the whole pattern can be checked for
// syntax with path.Match.
func globSegments(pattern string) string {
	return strings.ReplaceAll(pattern, "**", "*")
}
