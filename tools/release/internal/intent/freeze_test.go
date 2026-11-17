package intent_test

import (
	_ "embed"
	"slices"
	"strings"
	"testing"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
	"github.com/peasant-labs/infra/tools/release/internal/testcase"
)

//go:embed testdata/freeze_corpus.yaml
var freezeCorpusYAML []byte

//go:embed testdata/freeze_documents.yaml
var freezeDocumentsYAML []byte

//go:embed testdata/freeze_outputs.yaml
var freezeOutputCorpusYAML []byte

type freezeOutputInput struct {
	Config intent.Config     `yaml:"config"`
	Plan   intent.FreezePlan `yaml:"plan"`
	Dirty  []string          `yaml:"dirty"`
}

type freezeOutputExpected struct {
	Code    intent.ErrorCode `yaml:"code"`
	Surface string           `yaml:"surface"`
	Prefix  string           `yaml:"prefix"`
}

func TestFrozenPrimaryOutputs(t *testing.T) {
	for _, c := range loadNamedCorpus[freezeOutputInput, freezeOutputExpected](t, freezeOutputCorpusYAML).Cases {
		t.Run(c.Name, func(t *testing.T) {
			err := intent.CheckFrozenPrimaryOutputs(c.Input.Config, c.Input.Plan, c.Input.Dirty)
			if c.Classification == testcase.MustFail {
				requireSixParts(t, err, c.Expected.Code)
				if !strings.Contains(err.Error(), c.Expected.Surface) || !strings.Contains(err.Error(), c.Expected.Prefix) {
					t.Fatalf("error %q does not name surface %q and prefix %q", err, c.Expected.Surface, c.Expected.Prefix)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

type freezeDocumentInput struct {
	Kind     string `yaml:"kind"`
	Document string `yaml:"document"`
}

type freezeDocumentExpected struct {
	Code intent.ErrorCode `yaml:"code"`
}

func TestFreezeDocuments(t *testing.T) {
	cfg := intent.Config{Surfaces: map[string][]string{"local-api": {"local"}}}
	for _, c := range loadNamedCorpus[freezeDocumentInput, freezeDocumentExpected](t, freezeDocumentsYAML).Cases {
		t.Run(c.Name, func(t *testing.T) {
			var err error
			switch c.Input.Kind {
			case "status":
				_, err = intent.ParseSurfaceStates([]byte(c.Input.Document), cfg)
			case "plan":
				_, err = intent.ParseFreezePlan([]byte(c.Input.Document), cfg)
			default:
				t.Fatalf("unknown document kind %q", c.Input.Kind)
			}
			if c.Classification == testcase.MustFail {
				if c.Expected.Code != "" {
					requireSixParts(t, err, c.Expected.Code)
				} else {
					requireSixLabels(t, err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

type freezeInput struct {
	Config         intent.Config        `yaml:"config"`
	ConfigDocument string               `yaml:"config_document"`
	States         intent.SurfaceStates `yaml:"states"`
	Titles         []string             `yaml:"titles"`
}

type freezeExpected struct {
	Code     intent.ErrorCode `yaml:"code"`
	Surface  string           `yaml:"surface"`
	To       intent.SemVer    `yaml:"to"`
	Level    string           `yaml:"level"`
	Breaking bool             `yaml:"breaking"`
	Freeze   bool             `yaml:"freeze"`
	Order    []string         `yaml:"order"`
}

func TestPlanFreeze(t *testing.T) {
	for _, c := range loadNamedCorpus[freezeInput, freezeExpected](t, freezeCorpusYAML).Cases {
		t.Run(c.Name, func(t *testing.T) {
			if c.Input.ConfigDocument != "" {
				cfg, err := intent.ParseConfig([]byte(c.Input.ConfigDocument))
				if err != nil {
					t.Fatal(err)
				}
				c.Input.Config = cfg
			}
			var titles []intent.Title
			for _, raw := range c.Input.Titles {
				title, err := intent.ParseTitle(c.Input.Config, "0.1.0", raw)
				if err != nil {
					t.Fatal(err)
				}
				titles = append(titles, title)
			}
			plan, err := intent.PlanFreeze(c.Input.Config, c.Input.States, titles)
			if c.Classification == testcase.MustFail {
				requireSixParts(t, err, c.Expected.Code)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.Expected.Order != nil {
				var names []string
				for _, row := range plan.Surfaces {
					names = append(names, row.Name)
				}
				if !slices.Equal(names, c.Expected.Order) {
					t.Fatalf("plan surface order = %v, want %v", names, c.Expected.Order)
				}
			}
			for _, row := range plan.Surfaces {
				if row.Name == c.Expected.Surface {
					if row.To != c.Expected.To || row.Level.String() != c.Expected.Level || row.Breaking != c.Expected.Breaking || row.Freeze != c.Expected.Freeze {
						t.Fatalf("surface = %+v; want %+v", row, c.Expected)
					}
					return
				}
			}
			t.Fatalf("missing surface %q", c.Expected.Surface)
		})
	}
}
