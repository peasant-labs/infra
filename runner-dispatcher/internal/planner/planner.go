// Package planner turns scale-set statistics plus tracked VM state into the
// minimal set of boot and reclaim actions for one dispatcher cycle.
package planner

import (
	"fmt"

	"github.com/peasant-labs/infra/runner-dispatcher/internal/vm"
)

// ActionKind is the kind of action a plan can contain.
type ActionKind string

const (
	// ActionBoot creates one new runner VM.
	ActionBoot ActionKind = "boot"
	// ActionKill reclaims one existing runner VM.
	ActionKill ActionKind = "kill"
)

// Action is one planned boot or reclaim.
type Action struct {
	Kind       ActionKind
	InstanceID string
	JobID      string
}

// Input is the state a single planning cycle sees.
//
// AssignedJobs and RunningJobs come from the scale-set statistics
// (TotalAssignedJobs and TotalRunningJobs): assigned jobs include both jobs
// waiting for a runner and jobs already running.
type Input struct {
	AssignedJobs int
	RunningJobs  int
	MaxCapacity  int
	Instances    []vm.Instance
}

// Plan returns the boot/reclaim actions for one cycle. Demand is capped by
// MaxCapacity; busy and booting instances always stay, exited instances are
// always reclaimed, and surplus idle instances are reclaimed down to demand.
func Plan(in Input) ([]Action, error) {
	if in.MaxCapacity <= 0 {
		return nil, fmt.Errorf("max capacity must be positive, got %d", in.MaxCapacity)
	}
	if in.AssignedJobs < 0 || in.RunningJobs < 0 || in.RunningJobs > in.AssignedJobs {
		return nil, fmt.Errorf("invalid statistics: assigned=%d running=%d", in.AssignedJobs, in.RunningJobs)
	}

	desired := in.AssignedJobs
	if desired > in.MaxCapacity {
		desired = in.MaxCapacity
	}

	var actions []Action
	covering := 0
	var idle []string
	for _, inst := range in.Instances {
		switch inst.State {
		case vm.StateExited:
			actions = append(actions, Action{Kind: ActionKill, InstanceID: inst.ID})
		case vm.StateIdle:
			covering++
			idle = append(idle, inst.ID)
		case vm.StateBooting, vm.StateBusy:
			covering++
		default:
			return nil, fmt.Errorf("unknown instance state %q for instance %s", inst.State, inst.ID)
		}
	}

	surplus := covering - desired
	for _, id := range idle {
		if surplus <= 0 {
			break
		}
		actions = append(actions, Action{Kind: ActionKill, InstanceID: id})
		surplus--
		covering--
	}

	boots := desired - covering
	for ; boots > 0; boots-- {
		actions = append(actions, Action{Kind: ActionBoot})
	}
	return actions, nil
}
