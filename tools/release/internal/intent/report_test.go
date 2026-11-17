package intent_test

import (
	_ "embed"
	"strings"
	"testing"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
	"github.com/peasant-labs/infra/tools/release/internal/testcase"
)

//go:embed testdata/report_corpus.yaml
var reportCorpusYAML []byte

type reportInput struct {
	Config   intent.Config `yaml:"config"`
	Document string        `yaml:"document"`
}

type reportExpected struct {
	Error string `yaml:"error"`
}

func TestContractReport(t *testing.T) {
	for _, c := range loadNamedCorpus[reportInput, reportExpected](t, reportCorpusYAML).Cases {
		t.Run(c.Name, func(t *testing.T) {
			_, err := intent.ParseContractReport([]byte(c.Input.Document), c.Input.Config)
			if c.Classification == testcase.MustFail {
				requireSixLabels(t, err)
				if c.Expected.Error != "" && !strings.Contains(err.Error(), c.Expected.Error) {
					t.Fatalf("error %q does not contain %q", err, c.Expected.Error)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}
