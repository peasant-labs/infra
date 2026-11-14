package catalog_test

import (
	"bytes"
	_ "embed"
	"slices"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/peasant-labs/infra/unstring/internal/catalog"
)

//go:embed testdata/source-kinds.yaml
var sourceKindsYAML []byte

type sourceKindsFixture struct {
	SourceKinds []string `yaml:"sourceKinds"`
	Invalid     []string `yaml:"invalid"`
}

func loadSourceKindsFixture(t *testing.T) sourceKindsFixture {
	t.Helper()
	var f sourceKindsFixture
	dec := yaml.NewDecoder(bytes.NewReader(sourceKindsYAML))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		t.Fatalf("decode testdata/source-kinds.yaml: %v", err)
	}
	return f
}

func TestSourceKindMembership(t *testing.T) {
	f := loadSourceKindsFixture(t)
	var got []string
	for _, k := range catalog.AllSourceKinds {
		if slices.Contains(got, k.String()) {
			t.Errorf("AllSourceKinds lists %q twice", k)
		}
		got = append(got, k.String())
		if !k.IsValid() {
			t.Errorf("AllSourceKinds member %q reports IsValid() == false", k)
		}
		if parsed, err := catalog.ParseSourceKind(k.String()); err != nil || parsed != k {
			t.Errorf("ParseSourceKind(%q) = %q, %v; want the member back", k, parsed, err)
		}
	}
	for _, w := range f.SourceKinds {
		if !slices.Contains(got, w) {
			t.Errorf("AllSourceKinds is missing required member %q (testdata/source-kinds.yaml)", w)
		}
	}
	for _, g := range got {
		if !slices.Contains(f.SourceKinds, g) {
			t.Errorf("AllSourceKinds has member %q that testdata/source-kinds.yaml does not list", g)
		}
	}
}

func TestParseSourceKindRefusesInvalidValues(t *testing.T) {
	f := loadSourceKindsFixture(t)
	for _, s := range f.Invalid {
		if k, err := catalog.ParseSourceKind(s); err == nil {
			t.Errorf("ParseSourceKind(%q) = %q, want an error", s, k)
		}
	}
}
