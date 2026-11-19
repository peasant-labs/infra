package intent_test

// Release-only suite: drives intent.ClassifyReleaseOnly over the release-only
// corpus.

import (
	_ "embed"
	"testing"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
	"github.com/peasant-labs/infra/tools/release/internal/testcase"
)

//go:embed testdata/release_only_corpus.yaml
var releaseOnlyCorpusYAML []byte

type releaseOnlyInput struct {
	Config  intent.Config       `yaml:"config"`
	Changes []intent.TreeChange `yaml:"changes"`
}

func TestClassifyReleaseOnly(t *testing.T) {
	corpus := loadNamedCorpus[releaseOnlyInput, intent.ReleaseOnlyResult](t, releaseOnlyCorpusYAML)
	for _, c := range corpus.Cases {
		t.Run(c.Name, func(t *testing.T) {
			got := intent.ClassifyReleaseOnly(c.Input.Config, c.Input.Changes)
			if got != c.Expected {
				t.Fatalf("ClassifyReleaseOnly = %+v, want %+v", got, c.Expected)
			}
			if got.ReleaseOnly != (c.Classification == testcase.MustPass) {
				t.Fatalf("release_only = %t contradicts classification %s", got.ReleaseOnly, c.Classification)
			}
		})
	}
}
