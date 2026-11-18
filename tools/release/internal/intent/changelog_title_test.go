package intent_test

import (
	_ "embed"
	"testing"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
	"github.com/peasant-labs/infra/tools/release/internal/testcase"
)

//go:embed testdata/changelog_title_corpus.yaml
var changelogTitleCorpusYAML []byte

func TestChangelogTitle(t *testing.T) {
	type input struct {
		Title   intent.Title  `yaml:"title"`
		Kind    intent.PRKind `yaml:"kind"`
		Changed bool          `yaml:"changed"`
	}
	type expected struct {
		Code intent.ErrorCode `yaml:"code"`
	}
	for _, c := range loadNamedCorpus[input, expected](t, changelogTitleCorpusYAML).Cases {
		t.Run(c.Name, func(t *testing.T) {
			err := intent.CheckChangelogTitle(c.Input.Title, c.Input.Kind, c.Input.Changed)
			if c.Classification == testcase.MustFail {
				requireSixParts(t, err, c.Expected.Code)
			} else if err != nil {
				t.Fatalf("unexpected rejection: %v", err)
			}
		})
	}
}
