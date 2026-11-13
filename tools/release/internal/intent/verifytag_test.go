package intent_test

// Release-tag verification suite: drives intent.VerifyTag over the corpus.

import (
	_ "embed"
	"testing"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
	"github.com/peasant-labs/infra/tools/release/internal/testcase"
)

//go:embed testdata/verify_tag_corpus.yaml
var verifyTagCorpusYAML []byte

type verifyTagInput struct {
	Config intent.Config   `yaml:"config"`
	Check  intent.TagCheck `yaml:"check"`
}

type verifyTagExpected struct {
	Code intent.ErrorCode `yaml:"code"`
}

func TestVerifyTag(t *testing.T) {
	corpus := loadNamedCorpus[verifyTagInput, verifyTagExpected](t, verifyTagCorpusYAML)
	for _, c := range corpus.Cases {
		t.Run(c.Name, func(t *testing.T) {
			err := intent.VerifyTag(c.Input.Config, c.Input.Check)
			if c.Classification == testcase.MustFail {
				if err == nil {
					t.Fatalf("VerifyTag: expected an error, got nil")
				}
				requireSixParts(t, err, c.Expected.Code)
				return
			}
			if err != nil {
				t.Fatalf("VerifyTag: unexpected error: %v", err)
			}
		})
	}
}
