package policy

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
	"gopkg.in/yaml.v3"
)

// TestMarkerExamples checks the versioned verdict-marker step set against the
// named examples that mirror it: the accepted inherit-verdict workflow, the
// release tool README, and the named API fixtures (comparing complete step
// names from structured YAML/JSON rather than regex-walking testdata).
func TestMarkerExamples(t *testing.T) {
	t.Parallel()
	want := intent.VerdictMarkerStepNames()

	t.Run("accepted-workflow", func(t *testing.T) {
		const path = "testdata/workflow_queue/overlays/accept-inherit-verdict/workflows/ci.yml"
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var doc yaml.Node
		if err := yaml.Unmarshal(data, &doc); err != nil {
			t.Fatal(err)
		}
		if len(doc.Content) == 0 {
			t.Fatalf("%s: empty YAML document", path)
		}
		root := doc.Content[0]
		gotFinal := finalStepNames(jobNode(root, "merge-gate"), len(want))
		if !slices.Equal(gotFinal, want) {
			t.Errorf("%s merge-gate final marker steps = %q, want %q", path, gotFinal, want)
		}
		var allMarkers []string
		for _, jobName := range jobNames(root) {
			for _, step := range stepsOf(jobNode(root, jobName)) {
				nameNode := mappingValue(step, "name")
				if nameNode != nil && nameNode.Kind == yaml.ScalarNode && strings.HasPrefix(nameNode.Value, intent.VerdictMarkerPrefix) {
					allMarkers = append(allMarkers, nameNode.Value)
				}
			}
		}
		if !slices.Equal(allMarkers, want) {
			t.Errorf("%s all marker steps = %q, want %q", path, allMarkers, want)
		}
	})

	t.Run("readme", func(t *testing.T) {
		const path = "../../README.md"
		readme, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(readme)
		wantVersion := fmt.Sprintf("marker step set, version %d", intent.VerdictMarkersVersion)
		if !strings.Contains(text, wantVersion) {
			t.Errorf("%s does not name %q", path, wantVersion)
		}
		matches := regexp.MustCompile("`("+regexp.QuoteMeta(intent.VerdictMarkerPrefix)+"[^`]*)`").FindAllStringSubmatch(text, -1)
		names := make([]string, 0, len(matches))
		for _, match := range matches {
			names = append(names, match[1])
		}
		if !slices.Equal(names, want) {
			t.Errorf("%s backticked marker steps = %q, want %q", path, names, want)
		}
		if count := strings.Count(text, intent.VerdictMarkerPrefix); count != len(want) {
			t.Errorf("%s contains %d occurrences of %q, want exactly %d", path, count, intent.VerdictMarkerPrefix, len(want))
		}
	})

	t.Run("inherit-verdict-api-fixture", func(t *testing.T) {
		const path = "../../cmd/release-intent/testdata/inherit-verdict.yaml"
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var fx struct {
			Routes map[string][]struct {
				Path      string `yaml:"path"`
				Responses []struct {
					Body string `yaml:"body"`
				} `yaml:"responses"`
			} `yaml:"routes"`
		}
		if err := yaml.Unmarshal(data, &fx); err != nil {
			t.Fatal(err)
		}
		routeNames := make([]string, 0, len(fx.Routes))
		for name := range fx.Routes {
			routeNames = append(routeNames, name)
		}
		sort.Strings(routeNames)
		checked := 0
		for _, routeName := range routeNames {
			for _, route := range fx.Routes[routeName] {
				for idx, resp := range route.Responses {
					if resp.Body == "" {
						continue
					}
					var job struct {
						Steps []struct {
							Name string `json:"name"`
						} `json:"steps"`
					}
					if err := json.Unmarshal([]byte(resp.Body), &job); err != nil {
						t.Fatalf("%s route %s (%s) response[%d]: invalid JSON: %v", path, routeName, route.Path, idx, err)
					}
					var markers []string
					for _, step := range job.Steps {
						if strings.HasPrefix(step.Name, intent.VerdictMarkerPrefix) {
							markers = append(markers, step.Name)
						}
					}
					if len(markers) == 0 {
						continue
					}
					checked++
					if !slices.Equal(markers, want) {
						t.Errorf("%s route %s (%s) response[%d] marker steps = %q, want %q", path, routeName, route.Path, idx, markers, want)
					}
				}
			}
		}
		if checked == 0 {
			t.Fatalf("%s: found no API job responses with %q marker steps", path, intent.VerdictMarkerPrefix)
		}
	})

	t.Run("base-verdict-corpus", func(t *testing.T) {
		const path = "../intent/testdata/base_verdict_corpus.yaml"
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var corpus struct {
			Cases []struct {
				Name  string `yaml:"name"`
				Input struct {
					Jobs []struct {
						Name  string `yaml:"name"`
						Steps []struct {
							Name string `yaml:"name"`
						} `yaml:"steps"`
					} `yaml:"jobs"`
				} `yaml:"input"`
			} `yaml:"cases"`
		}
		if err := yaml.Unmarshal(data, &corpus); err != nil {
			t.Fatal(err)
		}
		checked := 0
		for _, c := range corpus.Cases {
			for _, job := range c.Input.Jobs {
				var markers []string
				for _, step := range job.Steps {
					if strings.HasPrefix(step.Name, intent.VerdictMarkerPrefix) {
						markers = append(markers, step.Name)
					}
				}
				if len(markers) == 0 {
					continue
				}
				checked++
				if !slices.Equal(markers, want) {
					t.Errorf("%s case %s job %s marker steps = %q, want %q", path, c.Name, job.Name, markers, want)
				}
			}
		}
		if checked == 0 {
			t.Fatalf("%s: found no jobs with %q marker steps", path, intent.VerdictMarkerPrefix)
		}
	})
}
