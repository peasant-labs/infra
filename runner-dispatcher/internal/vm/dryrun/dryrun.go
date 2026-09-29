// Package dryrun provides an in-memory Driver used to run the dispatcher
// without a hypervisor. Instances stay in the booting state until a test or
// operator advances them.
package dryrun

import (
	"context"
	"fmt"
	"sync"

	"github.com/peasant-labs/infra/runner-dispatcher/internal/vm"
)

// Driver is an in-memory vm.Driver.
type Driver struct {
	mu        sync.Mutex
	instances map[string]vm.Instance
	order     []string
}

// New returns an empty dry-run driver.
func New() *Driver {
	return &Driver{instances: map[string]vm.Instance{}}
}

// Boot records an instance in the booting state.
func (d *Driver) Boot(_ context.Context, spec vm.BootSpec) (vm.Instance, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if spec.Name == "" {
		return vm.Instance{}, fmt.Errorf("dryrun: boot spec has no name")
	}
	if _, exists := d.instances[spec.Name]; exists {
		return vm.Instance{}, fmt.Errorf("dryrun: instance %s already exists", spec.Name)
	}
	inst := vm.Instance{ID: spec.Name, JobID: spec.JobID, State: vm.StateBooting}
	d.instances[spec.Name] = inst
	d.order = append(d.order, spec.Name)
	return inst, nil
}

// List returns tracked instances in boot order.
func (d *Driver) List(_ context.Context) ([]vm.Instance, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]vm.Instance, 0, len(d.order))
	for _, id := range d.order {
		out = append(out, d.instances[id])
	}
	return out, nil
}

// Kill removes a tracked instance.
func (d *Driver) Kill(_ context.Context, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.instances[id]; !ok {
		return fmt.Errorf("dryrun: unknown instance %s", id)
	}
	delete(d.instances, id)
	for i, name := range d.order {
		if name == id {
			d.order = append(d.order[:i], d.order[i+1:]...)
			break
		}
	}
	return nil
}

// SetState advances a tracked instance to another lifecycle state.
func (d *Driver) SetState(id string, state vm.State) {
	d.mu.Lock()
	defer d.mu.Unlock()
	inst, ok := d.instances[id]
	if !ok {
		return
	}
	inst.State = state
	d.instances[id] = inst
}
