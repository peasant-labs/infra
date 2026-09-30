// Package subprocess implements vm.Driver by running one child process per
// runner VM. The host supplies the boot command; the driver passes the VM
// name, the one-shot JIT config and the shared cache directory as templated
// arguments. Every live VM is reported busy: each VM runs exactly one
// assigned job, and exited VMs are removed as soon as the process reaps.
package subprocess

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/peasant-labs/infra/runner-dispatcher/internal/vm"
)

// Config describes how to boot one runner VM.
type Config struct {
	// Command is the host-provided boot executable (for example a wrapper
	// around the hypervisor). Required.
	Command string
	// Args are argument templates. {name}, {jit-file}, {cache-dir} and
	// {job-id} are replaced per boot.
	Args []string
	// JITDir holds one per-VM JIT config file (created 0600, removed after
	// the VM exits). Required.
	JITDir string
	// CacheDir is the shared cache directory handed to every VM.
	CacheDir string
	// KillTimeout bounds the wait between SIGTERM and SIGKILL during Kill.
	// Defaults to 10 seconds.
	KillTimeout time.Duration
	Logger      *slog.Logger
}

type instance struct {
	info vm.Instance
	cmd  *exec.Cmd
	jit  string
	done chan struct{}
}

// Driver supervises one child process per runner VM.
type Driver struct {
	cfg Config

	mu        sync.Mutex
	instances map[string]*instance
}

// New validates the config and returns a driver.
func New(cfg Config) (*Driver, error) {
	if strings.TrimSpace(cfg.Command) == "" {
		return nil, fmt.Errorf("subprocess: boot command is required")
	}
	if strings.TrimSpace(cfg.JITDir) == "" {
		return nil, fmt.Errorf("subprocess: jit dir is required")
	}
	if cfg.KillTimeout <= 0 {
		cfg.KillTimeout = 10 * time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if err := os.MkdirAll(cfg.JITDir, 0o700); err != nil {
		return nil, fmt.Errorf("subprocess: create jit dir: %w", err)
	}
	return &Driver{cfg: cfg, instances: map[string]*instance{}}, nil
}

// Boot starts one VM process. The context bounds process start only; the VM
// keeps running until it exits on its own or Kill is called.
func (d *Driver) Boot(ctx context.Context, spec vm.BootSpec) (vm.Instance, error) {
	if err := ctx.Err(); err != nil {
		return vm.Instance{}, err
	}
	if spec.Name == "" {
		return vm.Instance{}, fmt.Errorf("subprocess: boot spec has no name")
	}

	jitPath := filepath.Join(d.cfg.JITDir, spec.Name+".jit")

	d.mu.Lock()
	if _, exists := d.instances[spec.Name]; exists {
		d.mu.Unlock()
		return vm.Instance{}, fmt.Errorf("subprocess: instance %s already exists", spec.Name)
	}
	d.mu.Unlock()

	if err := os.WriteFile(jitPath, []byte(spec.JITConfig), 0o600); err != nil {
		return vm.Instance{}, fmt.Errorf("subprocess: write jit config: %w", err)
	}

	repl := map[string]string{
		"name":      spec.Name,
		"jit-file":  jitPath,
		"cache-dir": d.cfg.CacheDir,
		"job-id":    spec.JobID,
	}
	args := make([]string, 0, len(d.cfg.Args))
	for _, arg := range d.cfg.Args {
		for key, value := range repl {
			arg = strings.ReplaceAll(arg, "{"+key+"}", value)
		}
		args = append(args, arg)
	}

	cmd := exec.Command(d.cfg.Command, args...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	inst := &instance{
		info: vm.Instance{ID: spec.Name, JobID: spec.JobID, State: vm.StateBusy},
		cmd:  cmd,
		jit:  jitPath,
		done: make(chan struct{}),
	}

	// Reserve the name before starting so a duplicate boot cannot orphan a
	// running VM from tracking.
	d.mu.Lock()
	if _, exists := d.instances[spec.Name]; exists {
		d.mu.Unlock()
		_ = os.Remove(jitPath)
		return vm.Instance{}, fmt.Errorf("subprocess: instance %s already exists", spec.Name)
	}
	d.instances[spec.Name] = inst
	d.mu.Unlock()

	if err := cmd.Start(); err != nil {
		d.mu.Lock()
		delete(d.instances, spec.Name)
		d.mu.Unlock()
		_ = os.Remove(jitPath)
		return vm.Instance{}, fmt.Errorf("subprocess: start %s: %w", spec.Name, err)
	}

	go func() {
		err := cmd.Wait()
		d.mu.Lock()
		if current, ok := d.instances[spec.Name]; ok && current == inst {
			delete(d.instances, spec.Name)
		}
		d.mu.Unlock()
		_ = os.Remove(jitPath)
		close(inst.done)
		if err != nil {
			d.cfg.Logger.Info("runner vm exited", "name", spec.Name, "err", err)
		} else {
			d.cfg.Logger.Info("runner vm exited", "name", spec.Name)
		}
	}()

	return inst.info, nil
}

// List returns the live VMs, all reported busy (see the package comment).
func (d *Driver) List(_ context.Context) ([]vm.Instance, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]vm.Instance, 0, len(d.instances))
	for _, inst := range d.instances {
		out = append(out, inst.info)
	}
	return out, nil
}

// Kill terminates one VM: SIGTERM to its process group, then SIGKILL if the
// kill timeout expires.
func (d *Driver) Kill(ctx context.Context, id string) error {
	d.mu.Lock()
	inst, ok := d.instances[id]
	d.mu.Unlock()
	if !ok {
		return fmt.Errorf("subprocess: unknown instance %s", id)
	}

	signalGroup := func(sig syscall.Signal) {
		if inst.cmd.Process == nil {
			return
		}
		// A negative pid signals the whole process group so hypervisor
		// children cannot outlive the VM.
		if err := syscall.Kill(-inst.cmd.Process.Pid, sig); err != nil && err != syscall.ESRCH {
			d.cfg.Logger.Warn("signal runner vm", "name", id, "signal", sig, "err", err)
		}
	}

	signalGroup(syscall.SIGTERM)
	timer := time.NewTimer(d.cfg.KillTimeout)
	defer timer.Stop()
	select {
	case <-inst.done:
		return nil
	case <-timer.C:
	}
	signalGroup(syscall.SIGKILL)
	select {
	case <-inst.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
