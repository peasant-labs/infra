package intent_test

// Version arithmetic and SemVer parsing suites.

import (
	_ "embed"
	"testing"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
	"github.com/peasant-labs/infra/tools/release/internal/testcase"
)

//go:embed testdata/arithmetic_corpus.yaml
var arithmeticCorpusYAML []byte

//go:embed testdata/semver_corpus.yaml
var semverCorpusYAML []byte

type arithmeticInput struct {
	Base     string       `yaml:"base"`
	Level    intent.Level `yaml:"level"`
	Breaking bool         `yaml:"breaking"`
	// RawLevel carries a number outside the closed set straight into the
	// call, for the row that proves NextVersion refuses one.
	RawLevel int `yaml:"raw_level"`
}

type arithmeticExpected struct {
	Next intent.SemVer `yaml:"next"`
}

type semverInput struct {
	Raw string `yaml:"raw"`
}

type semverExpected struct {
	Value intent.SemVer `yaml:"value"`
}

func TestNextVersion(t *testing.T) {
	corpus := loadNamedCorpus[arithmeticInput, arithmeticExpected](t, arithmeticCorpusYAML)
	for _, c := range corpus.Cases {
		t.Run(c.Name, func(t *testing.T) {
			level := c.Input.Level
			if c.Input.RawLevel != 0 {
				level = intent.Level(c.Input.RawLevel)
			}
			got, err := intent.NextVersion(intent.SemVer(c.Input.Base), level, c.Input.Breaking)
			if c.Classification == testcase.MustFail {
				if err == nil {
					t.Fatalf("NextVersion(%q): expected an error, got %q", c.Input.Base, got)
				}
				requireSixLabels(t, err)
				return
			}
			if err != nil {
				t.Fatalf("NextVersion(%q): unexpected error: %v", c.Input.Base, err)
			}
			if got != c.Expected.Next {
				t.Errorf("NextVersion = %q, want %q", got, c.Expected.Next)
			}
		})
	}
}

func TestParseSemVer(t *testing.T) {
	corpus := loadNamedCorpus[semverInput, semverExpected](t, semverCorpusYAML)
	for _, c := range corpus.Cases {
		t.Run(c.Name, func(t *testing.T) {
			got, err := intent.ParseSemVer(c.Input.Raw)
			if c.Classification == testcase.MustFail {
				if err == nil {
					t.Fatalf("ParseSemVer(%q): expected an error, got %q", c.Input.Raw, got)
				}
				requireSixLabels(t, err)
				return
			}
			if err != nil {
				t.Fatalf("ParseSemVer(%q): unexpected error: %v", c.Input.Raw, err)
			}
			if got != c.Expected.Value {
				t.Errorf("ParseSemVer = %q, want %q", got, c.Expected.Value)
			}
		})
	}
}
