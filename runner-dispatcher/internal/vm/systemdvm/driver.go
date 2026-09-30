// Package systemdvm boots runner VMs through the microvm.nix systemd units.
// Each slot maps to one `microvm@<slot>.service`; the guest is a Nix-declared
// VM whose JIT config is written into the slot's shared directory before the
// unit starts.
package systemdvm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/peasant-labs/infra/runner-dispatcher/internal/vm"
)

// Config describes the systemd unit slots backing the runner pool.
type Config struct {
	// Slots are the VM instance names, for example runner-vm-1.
	Slots []string
	// UnitTemplate renders one slot name into a systemd unit; %s is the slot.
	UnitTemplate string
	// JITDir holds one subdirectory per slot; Boot writes <JITDir>/<slot>/jit-config.
	JITDir string
	// Systemctl is the systemctl binary; tests point this at a fake.
	Systemctl string
	// CommandTimeout bounds each systemctl invocation.
	CommandTimeout time.Duration
	Logger         *slog.Logger
}

// Driver controls VMs through their systemd units.
type Driver struct {
	cfg Config
}

// New validates the config and returns a driver.
func New(cfg Config) (*Driver, error) {
	if len(cfg.Slots) == 0 {
		return nil, errors.New("systemdvm: at least one slot is required")
	}
	if strings.TrimSpace(cfg.JITDir) == "" {
		return nil, errors.New("systemdvm: jit dir is required")
	}
	if cfg.UnitTemplate == "" {
		cfg.UnitTemplate = "microvm@%s.service"
	}
	if cfg.Systemctl == "" {
		cfg.Systemctl = "systemctl"
	}
	if cfg.CommandTimeout <= 0 {
		cfg.CommandTimeout = 30 * time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Driver{cfg: cfg}, nil
}

func (d *Driver) unit(slot string) string {
	return fmt.Sprintf(d.cfg.UnitTemplate, slot)
}

func (d *Driver) jitPath(slot string) string {
	return filepath.Join(d.cfg.JITDir, slot, "jit-config")
}

// Boot writes the JIT config for one slot and starts its unit. The context
// bounds the start command only; the VM runs until it powers off or Kill is
// called.
func (d *Driver) Boot(ctx context.Context, spec vm.BootSpec) (vm.Instance, error) {
	if !slices.Contains(d.cfg.Slots, spec.Name) {
		return vm.Instance{}, fmt.Errorf("systemdvm: %q is not a configured slot", spec.Name)
	}
	jitPath := d.jitPath(spec.Name)
	if err := os.MkdirAll(filepath.Dir(jitPath), 0o700); err != nil {
		return vm.Instance{}, fmt.Errorf("systemdvm: create slot dir: %w", err)
	}
	if err := os.WriteFile(jitPath, []byte(spec.JITConfig), 0o400); err != nil {
		return vm.Instance{}, fmt.Errorf("systemdvm: write jit config: %w", err)
	}
	if _, err := d.run(ctx, "start", d.unit(spec.Name)); err != nil {
		_ = os.Remove(jitPath)
		return vm.Instance{}, fmt.Errorf("systemdvm: start %s: %w", spec.Name, err)
	}
	d.cfg.Logger.Info("started runner vm unit", "slot", spec.Name, "unit", d.unit(spec.Name), "job", spec.JobID)
	return vm.Instance{ID: spec.Name, JobID: spec.JobID, State: vm.StateBusy}, nil
}

// List reports every slot whose unit is active or activating.
func (d *Driver) List(ctx context.Context) ([]vm.Instance, error) {
	out := make([]vm.Instance, 0, len(d.cfg.Slots))
	for _, slot := range d.cfg.Slots {
		state, err := d.activeState(ctx, d.unit(slot))
		if err != nil {
			return nil, err
		}
		switch state {
		case "active", "activating", "reloading":
			out = append(out, vm.Instance{ID: slot, State: vm.StateBusy})
		}
	}
	return out, nil
}

// Kill stops one slot's unit and removes its JIT config.
func (d *Driver) Kill(ctx context.Context, id string) error {
	if !slices.Contains(d.cfg.Slots, id) {
		return fmt.Errorf("systemdvm: %q is not a configured slot", id)
	}
	if _, err := d.run(ctx, "stop", d.unit(id)); err != nil {
		return fmt.Errorf("systemdvm: stop %s: %w", id, err)
	}
	if err := os.Remove(d.jitPath(id)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("systemdvm: remove jit config: %w", err)
	}
	d.cfg.Logger.Info("stopped runner vm unit", "slot", id, "unit", d.unit(id))
	return nil
}

// activeState runs `systemctl is-active` and returns its first output line.
// An inactive unit exits non-zero but is not an error.
func (d *Driver) activeState(ctx context.Context, unit string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, d.cfg.CommandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, d.cfg.Systemctl, "is-active", unit)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return "", fmt.Errorf("systemdvm: is-active %s: %w", unit, err)
		}
		// Non-zero is expected for inactive units; the state is on stdout.
	}
	state := strings.TrimSpace(strings.SplitN(stdout.String(), "\n", 2)[0])
	if state == "" {
		state = "inactive"
	}
	return state, nil
}

func (d *Driver) run(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, d.cfg.CommandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, d.cfg.Systemctl, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}
