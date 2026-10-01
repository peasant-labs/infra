package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/peasant-labs/infra/runner-dispatcher/internal/dispatcher"
	"github.com/peasant-labs/infra/runner-dispatcher/internal/vm"
	"github.com/peasant-labs/infra/runner-dispatcher/internal/vm/dryrun"
)

// idleSession long-polls forever and reports when the first poll begins, so
// the test knows the signal handler is installed before it sends SIGTERM.
type idleSession struct {
	once    sync.Once
	polling chan struct{}
}

func (s *idleSession) Next(ctx context.Context, _, _ int) (*dispatcher.Message, error) {
	s.once.Do(func() { close(s.polling) })
	<-ctx.Done()
	return nil, ctx.Err()
}

func (s *idleSession) Ack(context.Context, int) error { return nil }

type noJIT struct{}

func (noJIT) Mint(context.Context, string) (string, error) { return "jit", nil }

// SIGTERM to the service must not cancel a running job: the process stops
// polling, waits while a VM is busy, never reclaims it, and exits once the job
// ends on its own.
func TestSIGTERMDrainsWithoutCancellingARunningJob(t *testing.T) {
	driver := dryrun.New()
	if _, err := driver.Boot(context.Background(), vm.BootSpec{Name: "runner-vm-1"}); err != nil {
		t.Fatalf("boot: %v", err)
	}
	driver.SetState("runner-vm-1", vm.StateBusy)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	session := &idleSession{polling: make(chan struct{})}
	d := dispatcher.New(dispatcher.Config{
		MaxCapacity: 1,
		Class:       "default",
		NamePrefix:  "runner-vm",
		DrainPoll:   10 * time.Millisecond,
		Logger:      logger,
	}, session, noJIT{}, driver)

	done := make(chan error, 1)
	go func() { done <- serve(d, time.Minute, logger) }()

	select {
	case <-session.polling:
	case <-time.After(5 * time.Second):
		t.Fatal("dispatcher never started polling")
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM: %v", err)
	}

	select {
	case err := <-done:
		t.Fatalf("serve returned %v while a job was still running", err)
	case <-time.After(300 * time.Millisecond):
	}
	if instances, _ := driver.List(context.Background()); len(instances) != 1 || instances[0].State != vm.StateBusy {
		t.Fatalf("instances during drain = %+v, want the busy VM untouched", instances)
	}

	// The job finishes and the guest powers off.
	driver.SetState("runner-vm-1", vm.StateExited)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve = %v, want a clean drain", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not return after the job finished")
	}
}
