package intent_test

// Closed-set boundary suite: drives every Parse function that reads a
// closed-set value over the parse corpus, proves every member of every All
// list reads back as itself, and proves a type outside the closed set has no
// release class.

import (
	_ "embed"
	"fmt"
	"testing"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
	"github.com/peasant-labs/infra/tools/release/internal/testcase"
)

//go:embed testdata/parse_corpus.yaml
var parseCorpusYAML []byte

//go:embed testdata/class_corpus.yaml
var classCorpusYAML []byte

// parseSet is the closed set of closed sets the parse corpus covers.
type parseSet string

const (
	setMode         parseSet = "mode"
	setType         parseSet = "type"
	setLevel        parseSet = "level"
	setClass        parseSet = "class"
	setAction       parseSet = "action"
	setVerdict      parseSet = "verdict"
	setGate         parseSet = "gate"
	setFreshness    parseSet = "freshness"
	setPRKind       parseSet = "pr-kind"
	setErrorCode    parseSet = "error-code"
	setOutputKey    parseSet = "output-key"
	setRepoVariable parseSet = "repo-variable"
)

// stringerParser adapts one typed Parse function to a common shape.
func stringerParser[T fmt.Stringer](parse func(string) (T, error)) func(string) (fmt.Stringer, error) {
	return func(raw string) (fmt.Stringer, error) {
		member, err := parse(raw)
		if err != nil {
			return nil, err
		}
		return member, nil
	}
}

// members boxes an All list for the read-back check.
func members[T fmt.Stringer](all []T) []fmt.Stringer {
	out := make([]fmt.Stringer, 0, len(all))
	for _, member := range all {
		out = append(out, member)
	}
	return out
}

// parseSetEntry is one covered set: its parser and its All list.
type parseSetEntry struct {
	parse func(string) (fmt.Stringer, error)
	all   []fmt.Stringer
}

// parseSets maps every covered set to its production parser and All list.
var parseSets = map[parseSet]parseSetEntry{
	setMode:         {stringerParser(intent.ParseMode), members(intent.AllModes)},
	setType:         {stringerParser(intent.ParseType), members(intent.AllTypes)},
	setLevel:        {stringerParser(intent.ParseLevel), members(intent.AllLevels)},
	setClass:        {stringerParser(intent.ParseClass), members(intent.AllClasses)},
	setAction:       {stringerParser(intent.ParseAction), members(intent.AllActions)},
	setVerdict:      {stringerParser(intent.ParseVerdict), members(intent.AllVerdicts)},
	setGate:         {stringerParser(intent.ParseGate), members(intent.AllGates)},
	setFreshness:    {stringerParser(intent.ParseFreshness), members(intent.AllFreshness)},
	setPRKind:       {stringerParser(intent.ParsePRKind), members(intent.AllPRKinds)},
	setErrorCode:    {stringerParser(intent.ParseErrorCode), members(intent.AllErrorCodes)},
	setOutputKey:    {stringerParser(intent.ParseOutputKey), members(intent.AllOutputKeys)},
	setRepoVariable: {stringerParser(intent.ParseRepoVariable), members(intent.AllRepoVariables)},
}

// UnmarshalText refuses a set the suite does not cover at load time.
func (s *parseSet) UnmarshalText(text []byte) error {
	if _, ok := parseSets[parseSet(text)]; !ok {
		return fmt.Errorf("unknown parse set %q", text)
	}
	*s = parseSet(text)
	return nil
}

type parseInput struct {
	Set parseSet `yaml:"set"`
	Raw string   `yaml:"raw"`
}

type parseExpected struct {
	// Member is the wire spelling of the member the raw text selects. The
	// suite parses it through the same set's parser and compares members.
	Member *string `yaml:"member"`
}

func TestParseClosedSets(t *testing.T) {
	corpus := loadNamedCorpus[parseInput, parseExpected](t, parseCorpusYAML)
	covered := map[parseSet]map[testcase.Classification]bool{}
	for _, c := range corpus.Cases {
		if covered[c.Input.Set] == nil {
			covered[c.Input.Set] = map[testcase.Classification]bool{}
		}
		covered[c.Input.Set][c.Classification] = true
		t.Run(c.Name, func(t *testing.T) {
			entry := parseSets[c.Input.Set]
			got, err := entry.parse(c.Input.Raw)
			if c.Classification == testcase.MustFail {
				if err == nil {
					t.Fatalf("parse %s %q: expected an error, got %q", c.Input.Set, c.Input.Raw, got)
				}
				requireSixLabels(t, err)
				return
			}
			if err != nil {
				t.Fatalf("parse %s %q: unexpected error: %v", c.Input.Set, c.Input.Raw, err)
			}
			if c.Expected.Member == nil {
				t.Fatalf("an accept row must name the member it selects")
			}
			want, err := entry.parse(*c.Expected.Member)
			if err != nil {
				t.Fatalf("expected member %q is not a member of %s: %v", *c.Expected.Member, c.Input.Set, err)
			}
			if got != want {
				t.Errorf("parse %s %q = %q, want %q", c.Input.Set, c.Input.Raw, got, want)
			}
		})
	}
	// Every covered set has an accept row and a reject row.
	for set := range parseSets {
		for _, class := range testcase.AllClassifications {
			if !covered[set][class] {
				t.Errorf("set %q has no %s row in the parse corpus", set, class)
			}
		}
	}
}

// TestClosedSetMembersReadBack proves every member of every All list is
// spelled uniquely and reads back as itself through its parser.
func TestClosedSetMembersReadBack(t *testing.T) {
	for set, entry := range parseSets {
		seen := map[string]bool{}
		for _, member := range entry.all {
			word := member.String()
			if seen[word] {
				t.Errorf("%s: two members are spelled %q", set, word)
			}
			seen[word] = true
			got, err := entry.parse(word)
			if err != nil {
				t.Errorf("%s: member %q does not parse: %v", set, word, err)
				continue
			}
			if got != member {
				t.Errorf("%s: member %q reads back as %q", set, word, got)
			}
		}
	}
}

type classInput struct {
	// Type is the raw type word. Reject rows carry a word outside the
	// closed set, so the field stays raw text.
	Type string `yaml:"type"`
}

type classExpected struct {
	Class intent.Class `yaml:"class"`
}

// TestTypeClass drives Type.Class over the class corpus and proves every
// known type has a known class. A reject row carries a type outside the
// closed set: it must not parse, and its class must be ClassUnknown.
func TestTypeClass(t *testing.T) {
	corpus := loadNamedCorpus[classInput, classExpected](t, classCorpusYAML)
	covered := map[intent.Type]bool{}
	for _, c := range corpus.Cases {
		t.Run(c.Name, func(t *testing.T) {
			typ, err := intent.ParseType(c.Input.Type)
			if c.Classification == testcase.MustFail {
				if err == nil {
					t.Fatalf("ParseType(%q): expected an error, got %q", c.Input.Type, typ)
				}
				if got := intent.Type(c.Input.Type).Class(); got != intent.ClassUnknown {
					t.Errorf("Type(%q).Class() = %q, want the unknown class", c.Input.Type, got)
				}
				if intent.Type(c.Input.Type).Class().IsValid() {
					t.Errorf("Type(%q).Class() is a member of AllClasses", c.Input.Type)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseType(%q): unexpected error: %v", c.Input.Type, err)
			}
			covered[typ] = true
			if got := typ.Class(); got != c.Expected.Class {
				t.Errorf("%s.Class() = %q, want %q", typ, got, c.Expected.Class)
			}
		})
	}
	for _, typ := range intent.AllTypes {
		if !covered[typ] {
			t.Errorf("type %q has no row in the class corpus", typ)
		}
		if !typ.Class().IsValid() {
			t.Errorf("type %q has no known class", typ)
		}
	}
}
