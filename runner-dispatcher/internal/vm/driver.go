// Package vm defines the contract the dispatcher uses to boot and reclaim
// one-job runner VMs.
package vm

import "context"

// State is the lifecycle state of a runner VM as seen by a Driver.
type State string

const (
	// StateBooting means the VM exists but has not registered yet.
	StateBooting State = "booting"
	// StateIdle means the VM is registered and waiting for a job.
	StateIdle State = "idle"
	// StateBusy means the VM is running a job.
	StateBusy State = "busy"
	// StateExited means the VM has finished and can be reclaimed.
	StateExited State = "exited"
)

// Instance is one runner VM tracked by the dispatcher.
type Instance struct {
	ID    string
	JobID string
	State State
}

// BootSpec describes one VM to boot. JITConfig is a one-shot registration
// secret and must not be logged.
type BootSpec struct {
	Name      string
	JobID     string
	JITConfig string
	Class     string
}

// Driver boots, lists and reclaims runner VMs.
type Driver interface {
	Boot(ctx context.Context, spec BootSpec) (Instance, error)
	List(ctx context.Context) ([]Instance, error)
	Kill(ctx context.Context, id string) error
}
