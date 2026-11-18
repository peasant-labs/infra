package intent_test

import (
	_ "embed"
	"reflect"
	"testing"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
	"github.com/peasant-labs/infra/tools/release/internal/testcase"
)

//go:embed testdata/streams_corpus.yaml
var streamsCorpusYAML []byte

func TestStreams(t *testing.T) {
	type input struct {
		Document string        `yaml:"document"`
		Title    string        `yaml:"title"`
		Facts    *intent.Facts `yaml:"facts"`
	}
	type expected struct {
		Code    intent.ErrorCode `yaml:"code"`
		Tag     string           `yaml:"tag"`
		Pending []string         `yaml:"pending"`
	}
	corpus := loadNamedCorpus[input, expected](t, streamsCorpusYAML)
	for _, c := range corpus.Cases {
		t.Run(c.Name, func(t *testing.T) {
			cfg, err := intent.ParseConfig([]byte(c.Input.Document))
			if err == nil && c.Input.Title != "" {
				_, err = intent.ParseTitle(cfg, "0.1.0", c.Input.Title)
			}
			if err == nil && c.Input.Facts != nil {
				var decision intent.Decision
				decision, err = intent.Plan(cfg, *c.Input.Facts)
				if err == nil {
					var pending []string
					for _, title := range decision.Pending {
						pending = append(pending, title.Subject)
					}
					if decision.Tag != c.Expected.Tag || !reflect.DeepEqual(pending, c.Expected.Pending) {
						t.Fatalf("plan tag = %q pending = %v; want %q %v", decision.Tag, pending, c.Expected.Tag, c.Expected.Pending)
					}
				}
			}
			if c.Classification == testcase.MustFail {
				if err == nil {
					t.Fatal("expected rejection")
				}
				requireSixParts(t, err, c.Expected.Code)
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}
