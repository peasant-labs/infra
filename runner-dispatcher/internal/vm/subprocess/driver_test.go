package subprocess

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

func newTestDriver(t *testing.T, command string, args ...string) *Driver {
	t.Helper()
	d, err := New(Config{
		Command: command,
		Args:    args,
		JITDir:  t.TempDir(),
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return d
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}

func TestBootListKill(t *testing.T) {
	driver := newTestDriver(t, "sleep", "30")
	ctx := context.Background()

	inst, err := driver.Boot(ctx, vm.BootSpec{Name: "vm-1", JITConfig: "secret"})
	if err != nil {
		t.Fatalf("Boot: %v", err)
	}
	if inst.State != vm.StateBusy {
		t.Fatalf("state = %q, want %q", inst.State, vm.StateBusy)
	}

	instances, err := driver.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(instances) != 1 || instances[0].ID != "vm-1" {
		t.Fatalf("instances = %+v", instances)
	}

	if err := driver.Kill(ctx, "vm-1"); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		instances, _ := driver.List(ctx)
		return len(instances) == 0
	})
}

func TestExitedVMIsRemovedAndJITDeleted(t *testing.T) {
	driver := newTestDriver(t, "sh", "-c", "exit 0")
	ctx := context.Background()

	if _, err := driver.Boot(ctx, vm.BootSpec{Name: "vm-1", JITConfig: "secret"}); err != nil {
		t.Fatalf("Boot: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		instances, _ := driver.List(ctx)
		return len(instances) == 0
	})
	if _, err := os.Stat(filepath.Join(driver.cfg.JITDir, "vm-1.jit")); !os.IsNotExist(err) {
		t.Fatalf("jit file still present, stat err = %v", err)
	}
}

func TestArgumentsAreTemplated(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out.txt")
	driver := newTestDriver(t, "sh", "-c", "cat {jit-file} > "+out)
	ctx := context.Background()

	if _, err := driver.Boot(ctx, vm.BootSpec{Name: "vm-9", JITConfig: "jit-payload"}); err != nil {
		t.Fatalf("Boot: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		data, err := os.ReadFile(out)
		return err == nil && string(data) == "jit-payload"
	})
}

func TestKillUnknownInstance(t *testing.T) {
	driver := newTestDriver(t, "sleep", "30")
	if err := driver.Kill(context.Background(), "nope"); err == nil {
		t.Fatal("expected error for unknown instance")
	}
}

func TestDoubleBootRejected(t *testing.T) {
	driver := newTestDriver(t, "sh", "-c", "sleep 30")
	ctx := context.Background()
	if _, err := driver.Boot(ctx, vm.BootSpec{Name: "vm-1"}); err != nil {
		t.Fatalf("first Boot: %v", err)
	}
	if _, err := driver.Boot(ctx, vm.BootSpec{Name: "vm-1"}); err == nil {
		t.Fatal("expected duplicate boot to fail")
	}
	instances, _ := driver.List(ctx)
	if len(instances) != 1 {
		t.Fatalf("instances = %+v, want 1 tracked entry", instances)
	}
	if err := driver.Kill(ctx, "vm-1"); err != nil {
		t.Fatalf("Kill: %v", err)
	}
}
