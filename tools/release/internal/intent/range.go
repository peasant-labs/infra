package intent

import "strconv"

func rangeError(code ErrorCode, what, why, means, fix string) *Error {
	return &Error{Code: code, What: what, Why: why,
		Where: "first-parent release history, read by intent.RangeBase and intent.RangeTitles",
		When:  "reading the landed range", Means: means, Fix: fix}
}

// RangeBaseBefore finds the nearest final tag strictly before rev on its
// first-parent chain. Tags on a side branch and on rev itself are excluded.
// When multiple final tags name the same commit, the highest version wins.
func RangeBaseBefore(tags map[string]string, prefix string, chain []Commit, rev string) (tag string, version SemVer, ok bool) {
	start := -1
	for i, commit := range chain {
		if commit.SHA == rev {
			start = i
			break
		}
	}
	if start < 0 {
		return "", "", false
	}
	type taggedVersion struct {
		tag     string
		version SemVer
	}
	bySHA := make(map[string]taggedVersion, len(tags))
	for name, sha := range tags {
		v, valid := splitReleaseTag(prefix, name)
		if !valid {
			continue
		}
		previous, exists := bySHA[sha]
		if !exists {
			bySHA[sha] = taggedVersion{name, v}
			continue
		}
		if cmp, err := compareSemVer(v, previous.version); err == nil && (cmp > 0 || cmp == 0 && name < previous.tag) {
			bySHA[sha] = taggedVersion{name, v}
		}
	}
	for _, commit := range chain[start+1:] {
		if candidate, found := bySHA[commit.SHA]; found {
			return candidate.tag, candidate.version, true
		}
	}
	return "", "", false
}

// rangeBounds applies the first-parent, epoch, recovery-floor and optional
// freeze-floor boundaries.
func rangeBounds(cfg Config, facts Facts) (string, int, error) {
	if len(facts.Commits) == 0 {
		return "", 0, rangeError(ErrNoFinalTag, "the revision has no first-parent commits",
			"a landed range needs a final tag before the revision", "no range can be checked", "fetch the complete history and its tags")
	}
	base, _, ok := RangeBaseBefore(facts.Tags, cfg.TagPrefix, facts.Commits, facts.Commits[0].SHA)
	if !ok {
		return "", 0, rangeError(ErrNoFinalTag, "no final release tag before the revision on the branch chain",
			"the range begins after the nearest earlier final tag", "no range can be checked", "fetch an earlier final tag on the first-parent chain")
	}
	end := -1
	epochIdx, floorIdx, freezeFloorIdx := -1, -1, -1
	for i, commit := range facts.Commits {
		if commit.SHA == facts.Tags[base] {
			end = i
		}
		if commit.SHA == facts.Epoch {
			epochIdx = i
		}
		if commit.SHA == facts.Floor && facts.Floor != "" {
			floorIdx = i
		}
		if commit.SHA == facts.FreezeFloor && facts.FreezeFloor != "" {
			freezeFloorIdx = i
		}
	}
	if facts.Floor != "" && floorIdx < 0 {
		return "", 0, rangeError(ErrFloorNotOnChain, "the recovery floor "+strconv.Quote(facts.Floor)+" is not on the branch chain",
			"the floor must name a chain commit", "no range can be checked", "set the floor to a first-parent commit or clear it")
	}
	if floorIdx >= 0 && floorIdx < end {
		end = floorIdx
	}
	if facts.FreezeFloor != "" && freezeFloorIdx < 0 {
		return "", 0, rangeError(ErrFloorNotOnChain, "the freeze floor "+strconv.Quote(facts.FreezeFloor)+" is not on the branch chain",
			"the freeze floor must name a chain commit", "no range can be checked", "set the freeze floor to a first-parent commit or clear it")
	}
	if freezeFloorIdx >= 0 && freezeFloorIdx < end {
		end = freezeFloorIdx
	}
	if facts.Epoch == "" || epochIdx < 0 {
		return "", 0, rangeError(ErrEpochMissing, "the grammar epoch is absent from the branch chain",
			"subjects before the config landed are not parsed", "no range can be checked", "fetch the config's adding commit")
	}
	if epochIdx < end {
		end = epochIdx + 1
	}
	return base, end, nil
}

// RangeBase validates range boundaries without parsing or judging subjects.
func RangeBase(cfg Config, facts Facts) (string, error) {
	base, _, err := rangeBounds(cfg, facts)
	return base, err
}

// RangeTitles parses every non-release subject within the shared boundaries.
func RangeTitles(cfg Config, facts Facts) (string, []Title, error) {
	base, end, err := rangeBounds(cfg, facts)
	if err != nil {
		return "", nil, err
	}
	_, version, found := HighestFinalTag(facts.Tags, cfg.TagPrefix)
	if !found {
		return "", nil, rangeError(ErrNoFinalTag, "no final release tag in the repository",
			"the title grammar needs a version base", "no range can be checked", "fetch the final release tags")
	}
	titles := []Title{}
	for _, commit := range facts.Commits[:end] {
		if commit.ChangesChangelog {
			continue
		}
		title, err := ParseTitle(cfg, version, commit.Subject)
		if err != nil {
			landed := landedError(commit, facts.Branch, err)
			if rej, ok := landed.(*Error); ok {
				rej.Where = "landed subject " + strconv.Quote(commit.SHA) + " on " + strconv.Quote(facts.Branch) + ", read by intent.RangeTitles"
				rej.When = "checking the landed range"
			}
			return "", nil, landed
		}
		titles = append(titles, title)
	}
	return base, titles, nil
}
