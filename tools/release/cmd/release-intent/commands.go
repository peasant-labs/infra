package main

import (
	"strconv"
	"strings"

	"github.com/peasant-labs/infra/tools/release/internal/closedset"
	"github.com/peasant-labs/infra/tools/release/internal/intent"
)

// parseCommand maps one argument word to its subcommand through the
// contract's closed set. Anything else is a usage error naming the whole set;
// no word falls back to a default command.
func parseCommand(raw string) (intent.Command, error) {
	command, err := intent.ParseCommand(raw)
	if err != nil {
		return "", usageErr("unknown subcommand %q: expected one of %s", raw, closedset.Join(intent.AllCommands, ", "))
	}
	return command, nil
}

// flagShape is what a flag takes and what it is for. An empty Placeholder
// marks a switch that takes no value.
type flagShape struct {
	Placeholder string
	Meaning     string
}

// shapeOf describes the flag. Every member of intent.AllFlags has one; a
// test asserts the set is covered.
func shapeOf(f intent.Flag) flagShape {
	switch f {
	case intent.FlagConfig:
		return flagShape{"FILE", "the release config file to validate"}
	case intent.FlagPolicy:
		return flagShape{"FILE", "the queue policy file"}
	case intent.FlagDir:
		return flagShape{"DIR", "the workflows directory"}
	case intent.FlagPrintRequired:
		return flagShape{"", "print the required check contexts, one per line"}
	case intent.FlagBase:
		return flagShape{"REV", "the PR base revision"}
	case intent.FlagHead:
		return flagShape{"REV", "the PR head revision"}
	case intent.FlagAuthor:
		return flagShape{"LOGIN", "the PR author login"}
	case intent.FlagHeadBranch:
		return flagShape{"BRANCH", "the PR head branch name"}
	case intent.FlagRev:
		return flagShape{"REV", "the base revision whose release config judges the PR"}
	case intent.FlagRef:
		return flagShape{"REF", "the merge-group ref"}
	case intent.FlagBranch:
		return flagShape{"REF", "the branch to plan, build release content from, or verify a tag against"}
	case intent.FlagMode:
		return flagShape{"MODE", "the job mode, one of " + closedset.Join(intent.AllModes, " or ")}
	case intent.FlagFloor:
		return flagShape{"SHA", "the recovery floor commit"}
	case intent.FlagVerdict:
		return flagShape{"VERDICT", "the pre-tag report, one of " + closedset.Join(reportedVerdicts(), " or ")}
	case intent.FlagWrite:
		return flagShape{"", "write the release content into the working tree"}
	case intent.FlagCheck:
		return flagShape{"", "compare the working tree with the release content"}
	case intent.FlagCurrent:
		return flagShape{"REF", "the open release branch to compare before a push"}
	case intent.FlagQueue:
		return flagShape{"FILE", "the file holding the raw merge-queue response"}
	case intent.FlagReleasePR:
		return flagShape{"N", "the release PR number, a positive integer"}
	case intent.FlagTag:
		return flagShape{"TAG", "the release tag to verify"}
	default:
		return flagShape{}
	}
}

func takesValue(f intent.Flag) bool { return shapeOf(f).Placeholder != "" }

// reportedVerdicts is the pre-tag reports a workflow may pass: every verdict
// but the absence of one, which is spelled by omitting the flag.
func reportedVerdicts() []intent.Verdict {
	out := make([]intent.Verdict, 0, len(intent.AllVerdicts))
	for _, v := range intent.AllVerdicts {
		if v != intent.VerdictNone {
			out = append(out, v)
		}
	}
	return out
}

// parseReportedVerdict maps the verdict flag's word to its verdict through
// the contract's parser. Anything but a reportable member is refused: the
// absence of a report is spelled by omitting the flag, never by a value.
func parseReportedVerdict(raw string) (intent.Verdict, bool) {
	verdict, err := intent.ParseVerdict(raw)
	if err != nil || verdict == intent.VerdictNone {
		return intent.VerdictNone, false
	}
	return verdict, true
}

// usageOf renders the command's synopsis from its flag set.
func usageOf(c intent.Command) string {
	var b strings.Builder
	b.WriteString("usage: " + intent.ProgramName + " " + c.String())
	for _, use := range c.Flags() {
		word := use.Flag.String()
		if p := shapeOf(use.Flag).Placeholder; p != "" {
			word += " " + p
		}
		if !use.Required {
			word = "[" + word + "]"
		}
		b.WriteString(" " + word)
	}
	return b.String()
}

// flagValues is one command's parsed flags: the value of every value flag
// that was passed and the presence of every flag.
type flagValues struct {
	values map[intent.Flag]string
}

// Has reports whether the flag was passed.
func (v flagValues) Has(f intent.Flag) bool {
	_, ok := v.values[f]
	return ok
}

// Value returns the flag's value, or "" for a flag that was not passed.
func (v flagValues) Value(f intent.Flag) string { return v.values[f] }

// ValueOr returns the flag's value, or fallback when the flag was not passed.
func (v flagValues) ValueOr(f intent.Flag, fallback string) string {
	if val, ok := v.values[f]; ok {
		return val
	}
	return fallback
}

// parseFlags reads the command's arguments against its flag set. It refuses
// a word outside the closed flag set, a known flag the command does not
// take, a value flag with no value or a flag-like value, and a missing
// required flag. Every refusal is a usage error carrying the synopsis.
func parseFlags(c intent.Command, args []string) (flagValues, error) {
	accepted := map[intent.Flag]bool{}
	for _, use := range c.Flags() {
		accepted[use.Flag] = true
	}
	out := flagValues{values: map[intent.Flag]string{}}
	for i := 0; i < len(args); i++ {
		flag, err := intent.ParseFlag(args[i])
		if err != nil {
			return flagValues{}, usageErr("unknown flag %q for %s: %s", args[i], c, usageOf(c))
		}
		if !accepted[flag] {
			return flagValues{}, usageErr("flag %s does not apply to %s: %s", flag, c, usageOf(c))
		}
		if !takesValue(flag) {
			out.values[flag] = ""
			continue
		}
		if i+1 >= len(args) {
			return flagValues{}, usageErr("%s requires a value (got end of arguments after %s): pass %s %s, %s. %s",
				flag, flag, flag, shapeOf(flag).Placeholder, shapeOf(flag).Meaning, usageOf(c))
		}
		val := args[i+1]
		if strings.HasPrefix(val, "-") && val != "-" {
			return flagValues{}, usageErr("%s requires a value: got flag-like %s (pass a value that does not start with '-'). %s",
				flag, strconv.Quote(val), usageOf(c))
		}
		out.values[flag] = val
		i++
	}
	for _, use := range c.Flags() {
		if use.Required && !out.Has(use.Flag) {
			return flagValues{}, usageErr("%s (missing required %s): pass %s", usageOf(c), use.Flag, shapeOf(use.Flag).Meaning)
		}
	}
	return out, nil
}
