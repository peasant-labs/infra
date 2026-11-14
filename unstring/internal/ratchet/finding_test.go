package ratchet_test

import (
	"bytes"
	_ "embed"
	"slices"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/peasant-labs/infra/unstring/internal/ratchet"
)

//go:embed testdata/finding-kinds.yaml
var findingKindsYAML []byte

type findingKindsFixture struct {
	FindingKinds []string `yaml:"findingKinds"`
	Invalid      []string `yaml:"invalid"`
}

func loadFindingKindsFixture(t *testing.T) findingKindsFixture {
	t.Helper()
	var f findingKindsFixture
	dec := yaml.NewDecoder(bytes.NewReader(findingKindsYAML))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		t.Fatalf("decode testdata/finding-kinds.yaml: %v", err)
	}
	return f
}

func TestFindingKindMembership(t *testing.T) {
	f := loadFindingKindsFixture(t)
	var got []string
	for _, k := range ratchet.AllFindingKinds {
		if slices.Contains(got, k.String()) {
			t.Errorf("AllFindingKinds lists %q twice", k)
		}
		got = append(got, k.String())
		if !k.IsValid() {
			t.Errorf("AllFindingKinds member %q reports IsValid() == false", k)
		}
		if parsed, err := ratchet.ParseFindingKind(k.String()); err != nil || parsed != k {
			t.Errorf("ParseFindingKind(%q) = %q, %v; want the member back", k, parsed, err)
		}
	}
	for _, w := range f.FindingKinds {
		if !slices.Contains(got, w) {
			t.Errorf("AllFindingKinds is missing required member %q (testdata/finding-kinds.yaml)", w)
		}
	}
	for _, g := range got {
		if !slices.Contains(f.FindingKinds, g) {
			t.Errorf("AllFindingKinds has member %q that testdata/finding-kinds.yaml does not list", g)
		}
	}
}

func TestParseFindingKindRefusesInvalidValues(t *testing.T) {
	f := loadFindingKindsFixture(t)
	for _, s := range f.Invalid {
		if k, err := ratchet.ParseFindingKind(s); err == nil {
			t.Errorf("ParseFindingKind(%q) = %q, want an error", s, k)
		}
	}
}
