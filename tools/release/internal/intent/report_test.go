package intent_test

import (
	_ "embed"
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

func TestContractReport(t *testing.T) {
	for _, c := range loadNamedCorpus[reportInput, struct{}](t, reportCorpusYAML).Cases {
		t.Run(c.Name, func(t *testing.T) {
			_, err := intent.ParseContractReport([]byte(c.Input.Document), c.Input.Config)
			if c.Classification == testcase.MustFail {
				requireSixLabels(t, err)
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}
