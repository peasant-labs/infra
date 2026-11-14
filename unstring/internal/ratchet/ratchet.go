package ratchet

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/peasant-labs/infra/unstring/internal/rules"
)

// Exit codes of a comparison. The command adds exit 2 for a run that could
// not happen.
const (
	ExitClean    = 0
	ExitFindings = 1
)

// Command is the closed set of commands the checker serves. It names the
// step in every finding's `where` part.
type Command string

const (
	CommandCheck  Command = "check"
	CommandUpdate Command = "update"
	CommandInit   Command = "init"
)

// String returns the command as typed on the command line.
func (c Command) String() string { return string(c) }

// Scan is what one run of the rules produced, after exempt and
// definition-site hits were dropped.
type Scan struct {
	// Hits are every tool-rule match. Two matches of one rule on one line are
	// deduplicated here.
	Hits []rules.Hit
	// Comments are the comment-rule matches: candidate allow markers and
	// ast-grep-ignore comments.
	Comments []Comment
	// Lines reads a file to place a marker that stands alone on its line.
	Lines LineReader
	// Messages returns the rule-specific parts of a hit's finding. Nil, or an
	// empty part, falls back to a generic text naming the rule.
	Messages func(rules.Hit) rules.Message
}

// Options are the settings of one comparison.
type Options struct {
	// Command is the command being run.
	Command Command
	// BaselinePath is the baseline's path relative to the config directory.
	BaselinePath string
	// Langs are the languages whose rules ran. Baseline entries of other
	// languages are neither compared nor changed.
	Langs []rules.Lang
	// LangFlag is true when `--lang` restricted the run. A restricted update
	// does not rewrite the header: it verified only part of the counts.
	LangFlag bool
	// Running is the running tool's version and the ast-grep version it ran.
	Running Versions
	// UpdateCommand is how the repository runs update, for fix lines. Empty
	// means `unstring update`.
	UpdateCommand string
}

// GitState is what git says about the working tree, for update's rename
// detection.
type GitState struct {
	// Renames maps a new path to its old path, for every rename git reports
	// between the commit that last changed the baseline and the working tree
	// (index included).
	Renames map[string]string
	// Untracked are paths git does not track: a file moved without staging
	// the move.
	Untracked map[string]bool
}

// Outcome is the result of one command.
type Outcome struct {
	// Findings are sorted: the baseline header first, then by file, line,
	// rule and kind.
	Findings []Finding
	// Exit is ExitClean or ExitFindings.
	Exit int
	// Baseline is the file to write, or nil when nothing is written.
	Baseline []byte
}

type fileKey struct {
	rule rules.RuleID
	file string
}

// evaluation is the shared part of every command: deduplicated hits after
// markers, per-file counts, and marker findings.
type evaluation struct {
	counts   map[fileKey]int
	hits     map[fileKey][]rules.Hit
	markers  []Finding
	natives  []Finding
	messages func(rules.Hit) rules.Message
}

func (o Options) selected(id rules.RuleID) bool {
	return slices.Contains(o.Langs, id.Lang())
}

func (o Options) updateCommand() string {
	if o.UpdateCommand != "" {
		return o.UpdateCommand
	}
	return "unstring update"
}

func evaluate(s Scan, o Options) (evaluation, error) {
	ev := evaluation{counts: map[fileKey]int{}, hits: map[fileKey][]rules.Hit{}, messages: s.Messages}

	// Dedupe: a hit is (rule, file, line).
	seen := map[rules.HitKey]bool{}
	var hits []rules.Hit
	for _, h := range s.Hits {
		if !o.selected(h.Rule) || seen[h.Key()] {
			continue
		}
		seen[h.Key()] = true
		hits = append(hits, h)
	}

	// Markers.
	type target struct {
		rule rules.RuleID
		file string
		line int
	}
	cache := map[string][]string{}
	valid := map[target][]*marker{}
	used := map[*marker]bool{}
	var parsed []*marker
	for _, c := range s.Comments {
		m, native := parseComment(c)
		if native != nil {
			ev.natives = append(ev.natives, nativeFinding(*native, o))
			continue
		}
		if m == nil {
			continue
		}
		if m.state == markerValid || m.state == markerNotAccepted {
			line, err := resolveTarget(c, s.Lines, cache)
			if err != nil {
				return evaluation{}, fmt.Errorf("read %s to place the marker on line %d: %w", c.File, c.Line, err)
			}
			m.target = line
		}
		parsed = append(parsed, m)
		if m.state == markerValid {
			k := target{m.rule, c.File, m.target}
			valid[k] = append(valid[k], m)
		}
	}
	for _, h := range hits {
		k := target{h.Rule, h.File, h.Line}
		if ms := valid[k]; len(ms) > 0 {
			for _, m := range ms {
				used[m] = true
			}
			continue
		}
		fk := fileKey{h.Rule, h.File}
		ev.counts[fk]++
		ev.hits[fk] = append(ev.hits[fk], h)
	}
	for _, m := range parsed {
		if m.state == markerValid && used[m] {
			continue
		}
		ev.markers = append(ev.markers, markerFinding(m, o, ev))
	}
	for k := range ev.hits {
		slices.SortFunc(ev.hits[k], func(a, b rules.Hit) int { return cmp.Compare(a.Line, b.Line) })
	}
	return ev, nil
}

// Check compares the scan with the baseline. Stale entries, raised counts,
// new files, marker problems and a header from another tool version are all
// findings; the header difference does not stop the comparison.
func Check(s Scan, base Baseline, o Options) (Outcome, error) {
	ev, err := evaluate(s, o)
	if err != nil {
		return Outcome{}, err
	}
	var out []Finding
	if base.Header != o.Running {
		out = append(out, headerFinding(base.Header, o))
	}
	out = append(out, ev.natives...)
	out = append(out, ev.markers...)
	for _, k := range ev.sortedKeys() {
		n := ev.counts[k]
		allowed, ok := base.Count(k.rule, k.file)
		switch {
		case !ok:
			out = append(out, ev.hitFindings(KindNewFile, k, 0, o)...)
		case n > allowed:
			out = append(out, ev.hitFindings(KindRaised, k, allowed, o)...)
		case n < allowed:
			out = append(out, staleFinding(KindStaleLower, k, allowed, n, o))
		}
	}
	for _, k := range baselineKeys(base, o) {
		if ev.counts[k] == 0 {
			allowed, _ := base.Count(k.rule, k.file)
			out = append(out, staleFinding(KindStaleMissingFile, k, allowed, 0, o))
		}
	}
	return finish(out, nil), nil
}

// Update lowers counts, removes entries with no hit left, moves an entry on a
// git rename whose count does not rise, and writes the running tool's
// header. If any count would rise or any entry would be added, it writes
// nothing and returns those hits.
func Update(s Scan, base Baseline, git GitState, o Options) (Outcome, error) {
	ev, err := evaluate(s, o)
	if err != nil {
		return Outcome{}, err
	}
	if len(ev.natives) > 0 {
		return finish(ev.natives, nil), nil
	}
	next := base.clone()
	// Drop every entry of the selected languages; they are rebuilt from the
	// scan below.
	for id := range next.Entries {
		if o.selected(id) {
			delete(next.Entries, id)
		}
	}
	var refused []Finding
	for _, k := range ev.sortedKeys() {
		n := ev.counts[k]
		if allowed, ok := base.Count(k.rule, k.file); ok {
			if n > allowed {
				refused = append(refused, ev.hitFindings(KindRaised, k, allowed, o)...)
				continue
			}
			setEntry(next, k, n)
			continue
		}
		if old, ok := git.Renames[k.file]; ok {
			if allowed, ok := base.Count(k.rule, old); ok {
				if n <= allowed {
					setEntry(next, k, n)
					continue
				}
				refused = append(refused, ev.renameRaisedFindings(k, old, allowed, o)...)
				continue
			}
		}
		fs := ev.hitFindings(KindNewFile, k, 0, o)
		if git.Untracked[k.file] && missingEntryForRule(base, k.rule, ev) {
			for i := range fs {
				fs[i].Fix = fmt.Sprintf("if %s was moved from a file the baseline lists, stage the rename so git can confirm it "+
					"(git add -A <old path> %s), then run %s again. Otherwise: %s",
					k.file, k.file, o.updateCommand(), fs[i].Fix)
			}
		}
		refused = append(refused, fs...)
	}
	if len(refused) > 0 {
		return finish(refused, nil), nil
	}
	if !o.LangFlag {
		next.Header = o.Running
	}
	return finish(nil, next.Format()), nil
}

// Init writes the first baseline: every counted hit, with the running
// tool's header. The caller runs it only when no baseline file exists.
func Init(s Scan, o Options) (Outcome, error) {
	ev, err := evaluate(s, o)
	if err != nil {
		return Outcome{}, err
	}
	if len(ev.natives) > 0 {
		return finish(ev.natives, nil), nil
	}
	b := Baseline{Header: o.Running, Entries: map[rules.RuleID]map[string]int{}}
	for k, n := range ev.counts {
		setEntry(b, k, n)
	}
	return finish(nil, b.Format()), nil
}

func setEntry(b Baseline, k fileKey, n int) {
	if n <= 0 {
		return
	}
	if b.Entries[k.rule] == nil {
		b.Entries[k.rule] = map[string]int{}
	}
	b.Entries[k.rule][k.file] = n
}

// missingEntryForRule reports whether the baseline lists a file for the rule
// that has no hit left: the old side of a move git did not see.
func missingEntryForRule(base Baseline, id rules.RuleID, ev evaluation) bool {
	for f := range base.Entries[id] {
		if ev.counts[fileKey{id, f}] == 0 {
			return true
		}
	}
	return false
}

func finish(findings []Finding, baseline []byte) Outcome {
	sortFindings(findings)
	exit := ExitClean
	if len(findings) > 0 {
		exit = ExitFindings
	}
	return Outcome{Findings: findings, Exit: exit, Baseline: baseline}
}

func sortFindings(fs []Finding) {
	slices.SortStableFunc(fs, func(a, b Finding) int {
		ah, bh := a.Kind == KindBaselineHeader, b.Kind == KindBaselineHeader
		if ah != bh {
			if ah {
				return -1
			}
			return 1
		}
		return cmp.Or(
			cmp.Compare(a.File, b.File),
			cmp.Compare(a.Line, b.Line),
			cmp.Compare(a.Rule, b.Rule),
			cmp.Compare(a.Kind, b.Kind),
		)
	})
}

func (ev evaluation) sortedKeys() []fileKey {
	keys := make([]fileKey, 0, len(ev.counts))
	for k := range ev.counts {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, compareKeys)
	return keys
}

func compareKeys(a, b fileKey) int {
	return cmp.Or(cmp.Compare(a.rule, b.rule), cmp.Compare(a.file, b.file))
}

// baselineKeys returns the entries of the selected languages, sorted.
func baselineKeys(base Baseline, o Options) []fileKey {
	var keys []fileKey
	for id, files := range base.Entries {
		if !o.selected(id) {
			continue
		}
		for f := range files {
			keys = append(keys, fileKey{id, f})
		}
	}
	slices.SortFunc(keys, compareKeys)
	return keys
}

// ruleMessage returns the hit's rule-specific parts, with generic fallbacks
// so that no part is ever empty.
func (ev evaluation) ruleMessage(h rules.Hit) rules.Message {
	var m rules.Message
	if ev.messages != nil {
		m = ev.messages(h)
	}
	if strings.TrimSpace(m.What) == "" {
		m.What = fmt.Sprintf("a hit of the rule %s.", h.Rule)
	}
	if strings.TrimSpace(m.Why) == "" {
		m.Why = "a value that belongs to a closed set, a contract or a file layout is written as a bare string; a rename would make it match nothing instead of failing the build."
	}
	if strings.TrimSpace(m.Fix) == "" {
		m.Fix = "use the typed value; `unstring show rules` prints this rule's fix."
	}
	return m
}

func (ev evaluation) hitFindings(kind FindingKind, k fileKey, allowed int, o Options) []Finding {
	hs := ev.hits[k]
	found := len(hs)
	var where string
	if kind == KindNewFile {
		where = fmt.Sprintf("unstring %s, comparing with %s: this file has no entry for this rule (allowed 0, found %d).",
			o.Command, o.BaselinePath, found)
	} else {
		where = fmt.Sprintf("unstring %s, comparing with %s: the count of this rule in this file rose (allowed %d, found %d). Every hit of the rule in the file is listed.",
			o.Command, o.BaselinePath, allowed, found)
	}
	if o.Command == CommandUpdate {
		where += " Nothing was written: update never raises a count or adds a file."
	}
	out := make([]Finding, 0, len(hs))
	for _, h := range hs {
		msg := ev.ruleMessage(h)
		out = append(out, Finding{
			Kind: kind, Rule: h.Rule, File: h.File, Line: h.Line, Text: h.Text,
			What: msg.What, Why: msg.Why, Where: where, Fix: msg.Fix,
		})
	}
	return out
}

func (ev evaluation) renameRaisedFindings(k fileKey, old string, allowed int, o Options) []Finding {
	fs := ev.hitFindings(KindRaised, k, allowed, o)
	for i := range fs {
		fs[i].Where = fmt.Sprintf("unstring update, comparing with %s: %s was renamed from %s, and the count of this rule rose across the rename (allowed %d, found %d). Nothing was written.",
			o.BaselinePath, k.file, old, allowed, len(ev.hits[k]))
	}
	return fs
}

func staleFinding(kind FindingKind, k fileKey, allowed, found int, o Options) Finding {
	what := fmt.Sprintf("the baseline allows %d hits of %s in %s, and the tree now has %d.", allowed, k.rule, k.file, found)
	if found == 0 {
		what = fmt.Sprintf("the baseline allows %d hits of %s in %s, and the file has none left or is gone.", allowed, k.rule, k.file)
	}
	return Finding{
		Kind: kind, Rule: k.rule, File: k.file,
		What:  what,
		Why:   "unused allowance would let a new hit in this file pass unnoticed; the baseline only ever goes down.",
		Where: fmt.Sprintf("unstring %s, comparing with %s (allowed %d, found %d).", o.Command, o.BaselinePath, allowed, found),
		Fix:   fmt.Sprintf("run the update command (%s) and commit the lowered baseline with the change that removed the hits.", o.updateCommand()),
	}
}

func headerFinding(h Versions, o Options) Finding {
	fix := fmt.Sprintf("run the update command (%s). It rewrites the header and lowers what was fixed, and refuses while any count would rise: "+
		"fix every new hit listed below first, or mark it where its rule accepts a marker.", o.updateCommand())
	if o.LangFlag {
		fix += " Run it without --lang: only a run over every language rewrites the header."
	}
	return Finding{
		Kind: KindBaselineHeader,
		File: o.BaselinePath,
		What: fmt.Sprintf("the baseline header records tool %s and ast-grep %s; the running tool is %s with ast-grep %s.",
			h.Tool, h.AstGrep, o.Running.Tool, o.Running.AstGrep),
		Why: "the header says which tool last verified the counts. They must be verified by the pinned tool, " +
			"and a different header never allows a count to rise.",
		Where: fmt.Sprintf("unstring %s, reading %s. The counts were still compared; any count finding is listed with this one.",
			o.Command, o.BaselinePath),
		Fix: fix,
	}
}

func markerFinding(m *marker, o Options, ev evaluation) Finding {
	c := m.comment
	where := fmt.Sprintf("unstring %s, reading the allow markers in %s.", o.Command, c.File)
	f := Finding{Rule: m.rule, File: c.File, Line: c.Line, Text: c.Text, Where: where}
	switch m.state {
	case markerUnknownRule:
		f.Kind = KindMarkerUnknownRule
		f.What = fmt.Sprintf("an unstring-allow marker names %q, which is not a rule.", m.ruleText)
		f.Why = "a marker suppresses the hits of one named rule; an id that is no rule suppresses nothing and hides a typo."
		f.Fix = "correct the id (`unstring show rules` lists them), or remove the marker. The form is " +
			formatMarker("<rule-id>", "<a reason of three or more words>", false) + "."
	case markerNotAccepted:
		f.Kind = KindMarkerNotAccepted
		f.What = fmt.Sprintf("an unstring-allow marker names %s, which accepts no marker.", m.rule)
		f.Why = fmt.Sprintf("a value that a declared set owns is never kept as a literal by annotation, so the hits of %s on line %d are still counted.", m.rule, m.target)
		fix := "remove the marker and compare with the typed value: the set's member, or a typed set for that vocabulary next to its owner."
		if hs := ev.hits[fileKey{m.rule, c.File}]; len(hs) > 0 {
			for _, h := range hs {
				if h.Line == m.target {
					fix = "remove the marker. " + ev.ruleMessage(h).Fix
					break
				}
			}
		}
		f.Fix = fix
	case markerNoReason:
		f.Kind = KindMarkerNoReason
		f.What = fmt.Sprintf("the unstring-allow marker for %s has no reason of at least %d words after an em dash (%s).", m.rule, MinReasonWords, MarkerSeparator)
		f.Why = "a marker removes a hit from the guard; its reason is what a reviewer reads to accept it, and there is exactly one form so markers can be found and audited."
		f.Fix = "write the marker exactly as: " + m.suggestion
	case markerValid:
		f.Kind = KindMarkerUnused
		f.What = fmt.Sprintf("the unstring-allow marker for %s applies to line %d, which has no hit of that rule.", m.rule, m.target)
		f.Why = "a marker that suppresses nothing would silently hide a future hit on that line."
		f.Fix = "remove the marker, or move it to the line with the hit: at the end of that line, or alone on the line directly above it."
	}
	return f
}

func nativeFinding(n nativeIgnoreComment, o Options) Finding {
	c := n.comment
	f := Finding{
		Kind: KindNativeIgnoreOnGuardRule, Rule: n.rule, File: c.File, Line: c.Line, Text: c.Text,
		Where: fmt.Sprintf("unstring %s, reading the comments in %s.", o.Command, c.File),
	}
	if n.rule == "" {
		f.What = "a bare ast-grep-ignore comment suppresses every rule, the guard's rules included."
		f.Why = "ast-grep hides the guard's hits on the next line from the count, so the baseline cannot see them."
		f.Fix = "name the strict rule ids the comment is meant for (// ast-grep-ignore: <rule-id>); for a guard rule, use " +
			formatMarker("<rule-id>", "<a reason of three or more words>", false) + " where the rule accepts a marker."
		return f
	}
	f.What = fmt.Sprintf("an ast-grep-ignore comment names the guard rule %s.", n.rule)
	f.Why = "ast-grep hides the hit from the guard's count, and a strict scan run with --error=unused-suppression reports the comment as unused."
	if n.rule.AcceptsMarker() {
		f.Fix = "replace it with " + formatMarker(n.rule, "<a reason of three or more words>", false) + ", or fix the literal."
	} else {
		f.Fix = fmt.Sprintf("remove it and fix the literal: %s accepts no marker, so the fix is the typed value.", n.rule)
	}
	return f
}
