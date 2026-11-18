package intent_test

// Protected-path suite: drives intent.CheckProtectedPaths over the
// protected corpus.

import (
	_ "embed"
	"strings"
	"testing"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
	"github.com/peasant-labs/infra/tools/release/internal/testcase"
)

//go:embed testdata/protected_corpus.yaml
var protectedCorpusYAML []byte

//go:embed testdata/changelog_headings_corpus.yaml
var changelogHeadingsCorpusYAML []byte

type protectedInput struct {
	Config  intent.Config            `yaml:"config"`
	Kind    intent.PRKind            `yaml:"kind"`
	Changes []intent.ProtectedChange `yaml:"changes"`
}

type protectedExpected struct {
	Code intent.ErrorCode `yaml:"code"`
}

func TestCheckProtectedPaths(t *testing.T) {
	corpus := loadNamedCorpus[protectedInput, protectedExpected](t, protectedCorpusYAML)
	for _, c := range corpus.Cases {
		t.Run(c.Name, func(t *testing.T) {
			err := intent.CheckProtectedPaths(c.Input.Config, c.Input.Changes, c.Input.Kind)
			if c.Classification == testcase.MustFail {
				if err == nil {
					t.Fatalf("CheckProtectedPaths: expected an error, got nil")
				}
				requireSixParts(t, err, c.Expected.Code)
				return
			}
			if err != nil {
				t.Fatalf("CheckProtectedPaths: unexpected error: %v", err)
			}
		})
	}
}

type changelogEditInput struct {
	Base    string `yaml:"base"`
	Find    string `yaml:"find"`
	Replace string `yaml:"replace"`
}

type changelogEditExpected struct {
	HeadingsUnchanged bool             `yaml:"headings_unchanged"`
	Code              intent.ErrorCode `yaml:"code"`
}

// TestChangelogProtectedEdits composes the real comparison and path judgment:
// no fixture can prove the allowance merely by setting the answer itself.
func TestChangelogProtectedEdits(t *testing.T) {
	corpus := loadNamedCorpus[changelogEditInput, changelogEditExpected](t, changelogHeadingsCorpusYAML)
	for _, c := range corpus.Cases {
		t.Run(c.Name, func(t *testing.T) {
			if n := strings.Count(c.Input.Base, c.Input.Find); c.Input.Find == "" || n != 1 {
				t.Fatalf("fixture anchor %q occurs %d times, want exactly once", c.Input.Find, n)
			}
			head := strings.Replace(c.Input.Base, c.Input.Find, c.Input.Replace, 1)
			unchanged := intent.ChangelogHeadingsUnchanged(c.Input.Base, head)
			if unchanged != c.Expected.HeadingsUnchanged {
				t.Fatalf("ChangelogHeadingsUnchanged = %t, want %t", unchanged, c.Expected.HeadingsUnchanged)
			}
			cfg := intent.Config{Changelog: "CHANGELOG.md"}
			err := intent.CheckProtectedPaths(cfg, []intent.ProtectedChange{{
				Path:                       cfg.Changelog,
				ChangelogHeadingsUnchanged: unchanged,
			}}, intent.PRKindOrdinary)
			if c.Classification == testcase.MustFail {
				if err == nil {
					t.Fatal("CheckProtectedPaths: expected rejection")
				}
				requireSixParts(t, err, c.Expected.Code)
			} else if err != nil {
				t.Fatalf("CheckProtectedPaths: unexpected error: %v", err)
			}
		})
	}
}
