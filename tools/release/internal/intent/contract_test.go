package intent_test

import (
	_ "embed"
	"reflect"
	"strings"
	"testing"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
	"github.com/peasant-labs/infra/tools/release/internal/testcase"
	"gopkg.in/yaml.v3"
)

//go:embed testdata/contract_corpus.yaml
var contractCorpusYAML []byte

//go:embed testdata/contract_closed_sets.yaml
var contractClosedSetsYAML []byte

func TestContractClosedSets(t *testing.T) {
	var want struct {
		Changes []intent.SurfaceChange    `yaml:"surface_changes"`
		States  []intent.SurfaceStateKind `yaml:"surface_states"`
	}
	if err := yaml.Unmarshal(contractClosedSetsYAML, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want.Changes, intent.AllSurfaceChanges) || !reflect.DeepEqual(want.States, intent.AllSurfaceStates) {
		t.Fatalf("closed surface sets do not match their fixture: changes=%v states=%v", intent.AllSurfaceChanges, intent.AllSurfaceStates)
	}
}

type contractInput struct {
	Config  intent.Config         `yaml:"config"`
	Current intent.SemVer         `yaml:"current"`
	Title   string                `yaml:"title"`
	Report  intent.ContractReport `yaml:"report"`
}

type contractExpected struct {
	Code          intent.ErrorCode `yaml:"code"`
	ErrorContains string           `yaml:"error_contains"`
}

func TestCheckContract(t *testing.T) {
	for _, c := range loadNamedCorpus[contractInput, contractExpected](t, contractCorpusYAML).Cases {
		t.Run(c.Name, func(t *testing.T) {
			_, err := intent.CheckContract(c.Input.Config, c.Input.Current, c.Input.Report, c.Input.Title)
			if c.Classification == testcase.MustFail {
				requireSixParts(t, err, c.Expected.Code)
				if c.Expected.ErrorContains != "" && !strings.Contains(err.Error(), c.Expected.ErrorContains) {
					t.Fatalf("error %q does not contain %q", err, c.Expected.ErrorContains)
				}
			} else if err != nil {
				t.Fatalf("unexpected rejection: %v", err)
			}
		})
	}
}
