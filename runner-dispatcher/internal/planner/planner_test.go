package planner

import (
	"os"
	"testing"

	"github.com/peasant-labs/infra/runner-dispatcher/internal/vm"
	"gopkg.in/yaml.v3"
)

type fixtureInstance struct {
	ID    string `yaml:"id"`
	State string `yaml:"state"`
}

type fixtureAction struct {
	Kind       string `yaml:"kind"`
	InstanceID string `yaml:"instanceId"`
}

type fixtureCase struct {
	Name        string            `yaml:"name"`
	Assigned    int               `yaml:"assigned"`
	Running     int               `yaml:"running"`
	MaxCapacity int               `yaml:"maxCapacity"`
	Instances   []fixtureInstance `yaml:"instances"`
	Want        []fixtureAction   `yaml:"want"`
	WantErr     bool              `yaml:"wantErr"`
}

func loadFixture(t *testing.T) []fixtureCase {
	t.Helper()
	data, err := os.ReadFile("../../testdata/planner.yaml")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var cases []fixtureCase
	if err := yaml.Unmarshal(data, &cases); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	if len(cases) == 0 {
		t.Fatal("fixture contains no cases")
	}
	return cases
}

func TestPlan(t *testing.T) {
	for _, tc := range loadFixture(t) {
		t.Run(tc.Name, func(t *testing.T) {
			instances := make([]vm.Instance, 0, len(tc.Instances))
			for _, inst := range tc.Instances {
				instances = append(instances, vm.Instance{ID: inst.ID, State: vm.State(inst.State)})
			}
			actions, err := Plan(Input{
				AssignedJobs: tc.Assigned,
				RunningJobs:  tc.Running,
				MaxCapacity:  tc.MaxCapacity,
				Instances:    instances,
			})
			if tc.WantErr {
				if err == nil {
					t.Fatalf("expected error, got actions %+v", actions)
				}
				return
			}
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			if len(actions) != len(tc.Want) {
				t.Fatalf("actions = %+v, want %+v", actions, tc.Want)
			}
			for i, want := range tc.Want {
				if got := actions[i]; got.Kind != ActionKind(want.Kind) || got.InstanceID != want.InstanceID {
					t.Fatalf("action[%d] = %+v, want %+v", i, got, want)
				}
			}
		})
	}
}
