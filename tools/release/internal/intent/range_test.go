package intent_test

import (
	_ "embed"
	"slices"
	"strings"
	"testing"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
	"github.com/peasant-labs/infra/tools/release/internal/testcase"
)

//go:embed testdata/contract_range_corpus.yaml
var contractRangeCorpusYAML []byte

type contractRangeInput struct {
	Config intent.Config         `yaml:"config"`
	Facts  intent.Facts          `yaml:"facts"`
	Report intent.ContractReport `yaml:"report"`
}

type contractRangeExpected struct {
	Base     string           `yaml:"base"`
	Titles   int              `yaml:"titles"`
	Subjects []string         `yaml:"subjects"`
	Excluded []string         `yaml:"excluded"`
	Code     intent.ErrorCode `yaml:"code"`
	Error    string           `yaml:"error"`
}

func TestCheckContractRange(t *testing.T) {
	for _, c := range loadNamedCorpus[contractRangeInput, contractRangeExpected](t, contractRangeCorpusYAML).Cases {
		t.Run(c.Name, func(t *testing.T) {
			base, titles, err := intent.RangeTitles(c.Input.Config, c.Input.Facts)
			if err == nil {
				err = intent.CheckContractRange(c.Input.Config, c.Input.Report, titles)
			}
			if c.Classification == testcase.MustFail {
				requireSixParts(t, err, c.Expected.Code)
				if c.Expected.Error != "" && !strings.Contains(err.Error(), c.Expected.Error) {
					t.Fatalf("error %q does not contain %q", err, c.Expected.Error)
				}
				return
			}
			if err != nil || base != c.Expected.Base || len(titles) != c.Expected.Titles {
				t.Fatalf("base=%q titles=%v error=%v", base, titles, err)
			}
			var subjects []string
			for _, title := range titles {
				subjects = append(subjects, intent.FormatTitle(title))
			}
			if c.Expected.Subjects != nil && !slices.Equal(subjects, c.Expected.Subjects) {
				t.Fatalf("subjects = %v, want %v", subjects, c.Expected.Subjects)
			}
			for _, subject := range c.Expected.Excluded {
				if slices.Contains(subjects, subject) {
					t.Fatalf("excluded subject %q appeared in %v", subject, subjects)
				}
			}
		})
	}
}
