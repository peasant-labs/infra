package intent_test

// Publish suite: drives the publish-release judgments over facts recorded
// from GitHub (see the corpus header for the recordings).

import (
	_ "embed"
	"strings"
	"testing"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
	"github.com/peasant-labs/infra/tools/release/internal/testcase"
)

//go:embed testdata/publish_corpus.yaml
var publishCorpusYAML []byte

// publishOp selects which judgment a row drives.
type publishOp string

const (
	publishOpLocal     publishOp = "local"
	publishOpSelect    publishOp = "select"
	publishOpRehearsal publishOp = "rehearsal"
	publishOpTag       publishOp = "tag"
	publishOpDraft     publishOp = "draft"
	publishOpPublished publishOp = "published"
)

const publishRepo = "peasant-labs/schema"

type publishInput struct {
	Op               publishOp                 `yaml:"op"`
	Tag              string                    `yaml:"tag"`
	Entries          []intent.LocalEntry       `yaml:"entries"`
	Releases         []intent.ReleaseFact      `yaml:"releases"`
	Truncated        bool                      `yaml:"truncated"`
	TagExists        bool                      `yaml:"tag_exists"`
	Release          intent.ReleaseFact        `yaml:"release"`
	Local            []intent.LocalAsset       `yaml:"local"`
	Remote           []intent.ReleaseAssetFact `yaml:"remote"`
	RequireImmutable bool                      `yaml:"require_immutable"`
	Rerun            bool                      `yaml:"rerun"`
}

type publishExpected struct {
	Code      intent.ErrorCode `yaml:"code"`
	Published int64            `yaml:"published"`
	Contains  []string         `yaml:"contains"`
}

func runPublishOp(t *testing.T, in publishInput) (int64, error) {
	t.Helper()
	switch in.Op {
	case publishOpLocal:
		return 0, intent.CheckLocalAssets("dist/assets", in.Entries)
	case publishOpSelect:
		r, err := intent.SelectRelease(publishRepo, in.Tag, in.Releases, in.Truncated)
		if r != nil {
			return r.ID, err
		}
		return 0, err
	case publishOpRehearsal:
		return 0, intent.CheckRehearsal(in.Tag, in.TagExists)
	case publishOpTag:
		return 0, intent.CheckTagExists(publishRepo, in.Tag, in.TagExists)
	case publishOpDraft:
		return 0, intent.JudgeDraft(publishRepo, in.Release, in.Local, in.Remote)
	case publishOpPublished:
		return 0, intent.JudgePublished(publishRepo, in.Release, in.Local, in.Remote, in.RequireImmutable, in.Rerun)
	}
	t.Fatalf("unknown op %q", in.Op)
	return 0, nil
}

func TestPublishJudgments(t *testing.T) {
	corpus := loadNamedCorpus[publishInput, publishExpected](t, publishCorpusYAML)
	for _, c := range corpus.Cases {
		t.Run(c.Name, func(t *testing.T) {
			id, err := runPublishOp(t, c.Input)
			if c.Classification == testcase.MustFail {
				if err == nil {
					t.Fatalf("accepted, want rejection %s", c.Expected.Code)
				}
				requireSixParts(t, err, c.Expected.Code)
				for _, frag := range c.Expected.Contains {
					if !strings.Contains(err.Error(), frag) {
						t.Fatalf("rejection does not mention %q: %v", frag, err)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected rejection: %v", err)
			}
			if id != c.Expected.Published {
				t.Fatalf("selected release %d, want %d", id, c.Expected.Published)
			}
		})
	}
}
