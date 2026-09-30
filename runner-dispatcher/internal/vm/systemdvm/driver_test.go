package systemdvm

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/peasant-labs/infra/runner-dispatcher/internal/vm"
)

// fakeSystemctl writes a shell systemctl that tracks unit state in files
// next to itself.
func fakeSystemctl(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "systemctl")
	body := `#!/bin/sh
set -eu
state_dir="$(dirname "$0")/state"
mkdir -p "$state_dir"
cmd="$1"
unit="$2"
case "$cmd" in
  start)
    touch "$state_dir/$unit"
    ;;
  stop)
    rm -f "$state_dir/$unit"
    ;;
  is-active)
    if [ -f "$state_dir/$unit" ]; then
      echo active
    else
      echo inactive
      exit 3
    fi
    ;;
  *)
    echo "unknown command $cmd" >&2
    exit 1
    ;;
esac
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write fake systemctl: %v", err)
	}
	return script
}

func newTestDriver(t *testing.T) (*Driver, string) {
	t.Helper()
	jitDir := t.TempDir()
	d, err := New(Config{
		Slots:          []string{"runner-vm-1", "runner-vm-2"},
		JITDir:         jitDir,
		Systemctl:      fakeSystemctl(t),
		CommandTimeout: 0,
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return d, jitDir
}

// Starting a slot waits for guest boot-readiness (boot plus the runner image
// load), so the default command timeout must comfortably exceed a fast boot —
// a tight bound kills `systemctl start` mid-boot and fails the dispatcher.
func TestNewDefaultCommandTimeoutFitsNotifyBoots(t *testing.T) {
	d, err := New(Config{
		Slots:     []string{"runner-vm-1"},
		JITDir:    t.TempDir(),
		Systemctl: fakeSystemctl(t),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if d.cfg.CommandTimeout < time.Minute {
		t.Fatalf("default command timeout = %s, want at least a minute", d.cfg.CommandTimeout)
	}
}

func TestBootWritesJITAndStartsUnit(t *testing.T) {
	d, jitDir := newTestDriver(t)
	ctx := context.Background()

	inst, err := d.Boot(ctx, vm.BootSpec{Name: "runner-vm-1", JITConfig: "jit-payload"})
	if err != nil {
		t.Fatalf("Boot: %v", err)
	}
	if inst.State != vm.StateBusy {
		t.Fatalf("state = %q, want busy", inst.State)
	}
	data, err := os.ReadFile(filepath.Join(jitDir, "runner-vm-1", "jit-config"))
	if err != nil {
		t.Fatalf("read jit: %v", err)
	}
	if string(data) != "jit-payload" {
		t.Fatalf("jit = %q", data)
	}
	info, err := os.Stat(filepath.Join(jitDir, "runner-vm-1", "jit-config"))
	if err != nil {
		t.Fatalf("stat jit: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o400 {
		t.Fatalf("jit mode = %o, want 400", mode)
	}

	instances, err := d.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(instances) != 1 || instances[0].ID != "runner-vm-1" {
		t.Fatalf("instances = %+v", instances)
	}
}

func TestKillStopsUnitAndRemovesJIT(t *testing.T) {
	d, jitDir := newTestDriver(t)
	ctx := context.Background()

	if _, err := d.Boot(ctx, vm.BootSpec{Name: "runner-vm-2", JITConfig: "jit"}); err != nil {
		t.Fatalf("Boot: %v", err)
	}
	if err := d.Kill(ctx, "runner-vm-2"); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	instances, err := d.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(instances) != 0 {
		t.Fatalf("instances = %+v, want none", instances)
	}
	if _, err := os.Stat(filepath.Join(jitDir, "runner-vm-2", "jit-config")); !os.IsNotExist(err) {
		t.Fatalf("jit file still present, err = %v", err)
	}
}

func TestUnknownSlotRejected(t *testing.T) {
	d, _ := newTestDriver(t)
	ctx := context.Background()
	if _, err := d.Boot(ctx, vm.BootSpec{Name: "runner-vm-9"}); err == nil {
		t.Fatal("expected error for unknown slot")
	}
	if err := d.Kill(ctx, "runner-vm-9"); err == nil {
		t.Fatal("expected error for unknown slot")
	}
}

func TestEmptyStateIsInactive(t *testing.T) {
	d, _ := newTestDriver(t)
	instances, err := d.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(instances) != 0 {
		t.Fatalf("instances = %+v, want none", instances)
	}
}
