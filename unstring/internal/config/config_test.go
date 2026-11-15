package config_test

import (
	"bytes"
	_ "embed"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/peasant-labs/infra/unstring/internal/config"
)

//go:embed testdata/documented.unstring.yaml
var documentedConfig []byte

// The documented config decodes with unknown keys refused, into exactly the
// values written in the file.
func TestDocumentedConfigDecodes(t *testing.T) {
	var got config.Config
	dec := yaml.NewDecoder(bytes.NewReader(documentedConfig))
	dec.KnownFields(true)
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("decode testdata/documented.unstring.yaml: %v", err)
	}
	want := config.Config{
		Version:  config.FormatVersion,
		Baseline: config.DefaultBaselinePath,
		TS:       &config.Roots{Roots: []string{"frontend"}},
		Go:       &config.Roots{Roots: []string{"backend"}},
		Schema:   config.Schema{Module: "github.com/peasant-labs/schema"},
		Endpoints: config.Endpoints{
			Prefixes:   config.DefaultEndpointPrefixes(),
			Callees:    []string{"api"},
			MountFiles: []string{"backend/internal/router/router.go"},
		},
		DataDirs: config.DefaultDataDirs(),
		Exempt: []config.Exempt{
			{
				Rule:   "testid-selector-string",
				Paths:  []string{"frontend/scripts/visual/lib/test-id-selector.mjs"},
				Reason: "the one place a test-id selector string is built",
			},
			{
				Rule:   config.ExemptAllRules,
				Paths:  []string{"backend/internal/db/gen/**"},
				Reason: "sqlc output, regenerated from the queries",
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("decoded config\n got: %+v\nwant: %+v", got, want)
	}
}
