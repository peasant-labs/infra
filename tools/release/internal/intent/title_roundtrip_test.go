package intent_test

// Title round-trip suite: proves intent.FormatTitle and intent.ParseTitle
// are exact inverses over the round-trip corpus, whose titles are typed
// members.

import (
	_ "embed"
	"reflect"
	"testing"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
	"github.com/peasant-labs/infra/tools/release/internal/testcase"
)

//go:embed testdata/title_roundtrip_corpus.yaml
var titleRoundTripCorpusYAML []byte

type roundTripInput struct {
	Title    intent.Title `yaml:"title"`
	Current  string       `yaml:"current"`
	Releases bool         `yaml:"releases"`
}

type roundTripExpected struct {
	Text string           `yaml:"text"`
	Code intent.ErrorCode `yaml:"code"`
}

func TestTitleRoundTrip(t *testing.T) {
	corpus := loadNamedCorpus[roundTripInput, roundTripExpected](t, titleRoundTripCorpusYAML)
	typesSeen := map[intent.Type]bool{}
	levelsSeen := map[intent.Level]bool{}
	for _, c := range corpus.Cases {
		if c.Classification == testcase.MustPass {
			typesSeen[c.Input.Title.Type] = true
			levelsSeen[c.Input.Title.Level] = true
		}
		t.Run(c.Name, func(t *testing.T) {
			cfg := intent.Config{Releases: c.Input.Releases}
			current := intent.SemVer(c.Input.Current)
			text := intent.FormatTitle(c.Input.Title)
			if text != c.Expected.Text {
				t.Fatalf("FormatTitle = %q, want %q", text, c.Expected.Text)
			}
			parsed, err := intent.ParseTitle(cfg, current, text)
			if c.Classification == testcase.MustFail {
				if c.Expected.Code != "" {
					if err == nil {
						t.Fatalf("ParseTitle(%q): expected an error, got %+v", text, parsed)
					}
					requireSixParts(t, err, c.Expected.Code)
					return
				}
				if err != nil {
					t.Fatalf("ParseTitle(%q): the row names no code, so the text must parse to a different title: %v", text, err)
				}
				if reflect.DeepEqual(parsed, c.Input.Title) {
					t.Fatalf("ParseTitle(%q) returned the input title; the row claims it cannot round-trip", text)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseTitle(%q): unexpected error: %v", text, err)
			}
			if !reflect.DeepEqual(parsed, c.Input.Title) {
				t.Errorf("ParseTitle(FormatTitle(title)) = %+v, want %+v", parsed, c.Input.Title)
			}
			if again := intent.FormatTitle(parsed); again != text {
				t.Errorf("FormatTitle(ParseTitle(text)) = %q, want %q", again, text)
			}
		})
	}
	for _, typ := range intent.AllTypes {
		if !typesSeen[typ] {
			t.Errorf("type %q has no accept row in the round-trip corpus", typ)
		}
	}
	for _, level := range intent.AllLevels {
		if !levelsSeen[level] {
			t.Errorf("level %q has no accept row in the round-trip corpus", level)
		}
	}
}
