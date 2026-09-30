package dispatcher

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/peasant-labs/infra/runner-dispatcher/internal/heartbeat"
	"github.com/peasant-labs/infra/runner-dispatcher/internal/vm"
	"github.com/peasant-labs/infra/runner-dispatcher/internal/vm/dryrun"
)

// scriptedSession serves a fixed message list, then returns nextErr.
type scriptedSession struct {
	messages []*Message
	index    int
	nextErr  error
	acks     []int
}

func (s *scriptedSession) Next(ctx context.Context, lastMessageID, maxCapacity int) (*Message, error) {
	if s.index >= len(s.messages) {
		if s.nextErr != nil {
			return nil, s.nextErr
		}
		return nil, ctx.Err()
	}
	msg := s.messages[s.index]
	s.index++
	return msg, nil
}

func (s *scriptedSession) Ack(_ context.Context, messageID int) error {
	s.acks = append(s.acks, messageID)
	return nil
}

type countingJIT struct {
	mints int
	names []string
}

func (j *countingJIT) Mint(_ context.Context, name string) (string, error) {
	j.mints++
	j.names = append(j.names, name)
	return "jit-config", nil
}

func newTestDispatcher(t *testing.T, session Session, driver vm.Driver) (*Dispatcher, *countingJIT) {
	t.Helper()
	jit := &countingJIT{}
	d := New(Config{
		MaxCapacity: 4,
		Class:       "default",
		NamePrefix:  "vm",
		DrainPoll:   5 * time.Millisecond,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, session, jit, driver)
	return d, jit
}

func TestRunBootsAndAcknowledges(t *testing.T) {
	driver := dryrun.New()
	session := &scriptedSession{
		messages: []*Message{{ID: 7, Statistics: Statistics{AssignedJobs: 2}}},
		nextErr:  context.Canceled,
	}
	d, jit := newTestDispatcher(t, session, driver)

	if err := d.Run(context.Background()); err != context.Canceled {
		t.Fatalf("Run = %v, want context.Canceled", err)
	}
	instances, err := driver.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(instances) != 2 {
		t.Fatalf("instances = %+v, want 2", instances)
	}
	if jit.mints != 2 {
		t.Fatalf("jit mints = %d, want 2", jit.mints)
	}
	if len(jit.names) != 2 || jit.names[0] != "vm-1" || jit.names[1] != "vm-2" {
		t.Fatalf("jit names = %v, want the booted slot names [vm-1 vm-2]", jit.names)
	}
	if len(session.acks) != 1 || session.acks[0] != 7 {
		t.Fatalf("acks = %v, want [7]", session.acks)
	}
}

func TestStepReclaimsExited(t *testing.T) {
	driver := dryrun.New()
	if _, err := driver.Boot(context.Background(), vm.BootSpec{Name: "vm-1"}); err != nil {
		t.Fatalf("boot: %v", err)
	}
	driver.SetState("vm-1", vm.StateExited)

	d, _ := newTestDispatcher(t, &scriptedSession{}, driver)
	if err := d.Step(context.Background(), Statistics{}); err != nil {
		t.Fatalf("Step: %v", err)
	}
	instances, _ := driver.List(context.Background())
	if len(instances) != 0 {
		t.Fatalf("instances = %+v, want none", instances)
	}
}

func TestBootNamesAreReusedBelowCapacity(t *testing.T) {
	driver := dryrun.New()
	ctx := context.Background()
	if _, err := driver.Boot(ctx, vm.BootSpec{Name: "vm-1"}); err != nil {
		t.Fatalf("boot: %v", err)
	}
	driver.SetState("vm-1", vm.StateExited)

	d, _ := newTestDispatcher(t, &scriptedSession{}, driver)
	if err := d.Step(ctx, Statistics{}); err != nil {
		t.Fatalf("reclaim step: %v", err)
	}
	if err := d.Step(ctx, Statistics{AssignedJobs: 1}); err != nil {
		t.Fatalf("boot step: %v", err)
	}
	instances, _ := driver.List(ctx)
	if len(instances) != 1 || instances[0].ID != "vm-1" {
		t.Fatalf("instances = %+v, want the recycled name vm-1", instances)
	}
}

func TestStepPublishesHeartbeat(t *testing.T) {
	driver := dryrun.New()
	hb := &fakeHeartbeat{}
	d := New(Config{
		MaxCapacity: 4,
		Class:       "default",
		NamePrefix:  "vm",
		DrainPoll:   5 * time.Millisecond,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Heartbeat:   hb,
	}, &scriptedSession{}, &countingJIT{}, driver)

	if err := d.Step(context.Background(), Statistics{AssignedJobs: 2, RunningJobs: 1}); err != nil {
		t.Fatalf("Step: %v", err)
	}
	if len(hb.records) != 1 {
		t.Fatalf("heartbeat records = %d, want 1", len(hb.records))
	}
	record := hb.records[0]
	if !record.ListenerHealthy || record.AssignedJobs != 2 || record.RunningJobs != 1 || record.LiveRunners != 2 {
		t.Fatalf("record = %+v", record)
	}
	if record.Timestamp.IsZero() {
		t.Fatal("record timestamp is zero")
	}
}

type fakeHeartbeat struct {
	records []heartbeat.Record
}

func (f *fakeHeartbeat) Publish(_ context.Context, record heartbeat.Record) error {
	f.records = append(f.records, record)
	return nil
}

func TestRunPublishesHealthBeforeFirstMessage(t *testing.T) {
	driver := dryrun.New()
	hb := &fakeHeartbeat{}
	d := New(Config{
		MaxCapacity: 4,
		Class:       "default",
		NamePrefix:  "vm",
		DrainPoll:   5 * time.Millisecond,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Heartbeat:   hb,
	}, &scriptedSession{nextErr: context.Canceled}, &countingJIT{}, driver)

	if err := d.Run(context.Background()); err != context.Canceled {
		t.Fatalf("Run = %v, want context.Canceled", err)
	}
	if len(hb.records) != 1 {
		t.Fatalf("heartbeat records = %d, want 1 before the first queue message", len(hb.records))
	}
	record := hb.records[0]
	if !record.ListenerHealthy || record.AssignedJobs != 0 || record.RunningJobs != 0 || record.LiveRunners != 0 {
		t.Fatalf("record = %+v", record)
	}
	if record.Timestamp.IsZero() {
		t.Fatal("record timestamp is zero")
	}
}

func TestDrainWaitsForBusyThenReclaimsIdle(t *testing.T) {
	driver := dryrun.New()
	ctx := context.Background()
	if _, err := driver.Boot(ctx, vm.BootSpec{Name: "vm-busy"}); err != nil {
		t.Fatalf("boot: %v", err)
	}
	if _, err := driver.Boot(ctx, vm.BootSpec{Name: "vm-idle"}); err != nil {
		t.Fatalf("boot: %v", err)
	}
	driver.SetState("vm-busy", vm.StateBusy)
	driver.SetState("vm-idle", vm.StateIdle)

	d, _ := newTestDispatcher(t, &scriptedSession{}, driver)
	go func() {
		time.Sleep(20 * time.Millisecond)
		driver.SetState("vm-busy", vm.StateExited)
	}()

	drainCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := d.Drain(drainCtx); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	instances, _ := driver.List(ctx)
	if len(instances) != 0 {
		t.Fatalf("instances = %+v, want none", instances)
	}
}

func TestDrainTimesOutWhileBusy(t *testing.T) {
	driver := dryrun.New()
	if _, err := driver.Boot(context.Background(), vm.BootSpec{Name: "vm-busy"}); err != nil {
		t.Fatalf("boot: %v", err)
	}
	driver.SetState("vm-busy", vm.StateBusy)

	d, _ := newTestDispatcher(t, &scriptedSession{}, driver)
	drainCtx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := d.Drain(drainCtx); err != context.DeadlineExceeded {
		t.Fatalf("Drain = %v, want context.DeadlineExceeded", err)
	}
}
