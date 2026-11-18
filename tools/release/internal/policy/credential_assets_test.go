package policy

import (
	_ "embed"
	"testing"
)

//go:embed testdata/release_asset_gates.yaml
var releaseAssetGateFixtureYAML []byte

type assetGateCase struct {
	Name      string `yaml:"name"`
	Condition string `yaml:"condition"`
	First     string `yaml:"first"`
	Second    string `yaml:"second"`
	Draft     bool   `yaml:"draft"`
	Sibling   string `yaml:"sibling"`
}

type assetOutcomeCase struct {
	Name    string `yaml:"name"`
	View    string `yaml:"view"`
	Create  string `yaml:"create"`
	Asset   string `yaml:"asset"`
	Upload  bool   `yaml:"upload"`
	Publish bool   `yaml:"publish"`
}

func TestReleaseAssetGates(t *testing.T) {
	var fx struct {
		RequiredNames        []string           `yaml:"required_names"`
		Cases                []assetGateCase    `yaml:"cases"`
		OutcomeRequiredNames []string           `yaml:"outcome_required_names"`
		Outcomes             []assetOutcomeCase `yaml:"outcomes"`
	}
	if err := strictUnmarshal(releaseAssetGateFixtureYAML, &fx); err != nil {
		t.Fatal(err)
	}
	byName := make(map[string]assetGateCase, len(fx.Cases))
	var names []string
	for _, row := range fx.Cases {
		names = append(names, row.Name)
		byName[row.Name] = row
	}
	manifestNames(t, "release asset gates", fx.RequiredNames, names)
	for _, row := range fx.Cases {
		if row.Sibling != "" {
			if sibling, ok := byName[row.Sibling]; !ok || sibling.First == "" || sibling.Sibling != "" {
				t.Fatalf("reject case %q has no accepting sibling", row.Name)
			}
		} else if row.First == "" || row.Second == "" {
			t.Fatalf("case %q has neither an accepted gate nor a sibling", row.Name)
		}
		t.Run(row.Name, func(t *testing.T) {
			got, ok := parseReleaseAssetGate(row.Condition)
			wantOK := row.Sibling == ""
			want := assetGate{first: row.First, second: row.Second, draft: row.Draft}
			if ok != wantOK || got != want {
				t.Fatalf("gate = %#v, %v; want %#v, %v", got, ok, want, wantOK)
			}
		})
	}

	// This models GitHub's implicit success() guard for the fixture's create,
	// upload and edit steps; the policy corpus also checks the actual workflow
	// conditions and their order. No GitHub API calls are made.
	names = nil
	for _, row := range fx.Outcomes {
		names = append(names, row.Name)
		t.Run(row.Name, func(t *testing.T) {
			upload := row.View == "failure" && row.Create == "success"
			publish := row.Create == "success" && row.Asset != "failure"
			if upload != row.Upload || publish != row.Publish {
				t.Fatalf("upload, publish = %v, %v; want %v, %v", upload, publish, row.Upload, row.Publish)
			}
		})
	}
	manifestNames(t, "release asset outcomes", fx.OutcomeRequiredNames, names)
}
