// Package dispatcher drives runner VMs from the GitHub scale-set queue: it
// reads statistics from a message session, plans boot/reclaim actions, mints
// one JIT registration per boot, and drains on shutdown.
package dispatcher

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/peasant-labs/infra/runner-dispatcher/internal/heartbeat"
	"github.com/peasant-labs/infra/runner-dispatcher/internal/planner"
	"github.com/peasant-labs/infra/runner-dispatcher/internal/vm"
)

// Statistics is the scale-set state observed with one message.
type Statistics struct {
	AssignedJobs int
	RunningJobs  int
}

// Message is one scale-set queue message in the shape the dispatcher needs.
type Message struct {
	ID         int
	Statistics Statistics
}

// Session is the message queue of one runner scale set.
type Session interface {
	// Next blocks until a message arrives or the long poll expires. It
	// returns (nil, nil) on expiry and ctx.Err() when the context ends.
	Next(ctx context.Context, lastMessageID int, maxCapacity int) (*Message, error)
	// Ack acknowledges a processed message so the queue does not redeliver it.
	Ack(ctx context.Context, messageID int) error
}

// JITSource mints one-shot runner registrations.
type JITSource interface {
	Mint(ctx context.Context) (string, error)
}

// Config carries the dispatcher's fixed settings.
type Config struct {
	MaxCapacity int
	Class       string
	NamePrefix  string
	DrainPoll   time.Duration
	Logger      *slog.Logger
	// Heartbeat publishes the pool-health record the router reads. Optional;
	// publish failures are logged and retried on the next cycle.
	Heartbeat heartbeat.Publisher
}

// Dispatcher owns the boot/reclaim loop for one runner scale set.
type Dispatcher struct {
	cfg     Config
	session Session
	jit     JITSource
	driver  vm.Driver
}

// New wires a dispatcher from its dependencies.
func New(cfg Config, session Session, jit JITSource, driver vm.Driver) *Dispatcher {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.DrainPoll <= 0 {
		cfg.DrainPoll = 5 * time.Second
	}
	return &Dispatcher{cfg: cfg, session: session, jit: jit, driver: driver}
}

// Run consumes the message queue until the context ends. Long-poll expiries
// reconcile against the last known statistics; a fresh dispatcher does not
// plan before it has seen its first message, but it publishes pool health
// from the first cycle so an idle pool never looks stale to the router.
func (d *Dispatcher) Run(ctx context.Context) error {
	var (
		lastID    int
		stats     Statistics
		haveStats bool
	)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Without statistics there is nothing to plan, but the pool-health
		// record still needs a fresh timestamp; otherwise the router treats a
		// live, idle pool as stale until the first job arrives. Counters read
		// zero until the first message, which the message session delivers
		// with the current statistics.
		if !haveStats {
			d.publishHealth(ctx, Statistics{})
		}
		msg, err := d.session.Next(ctx, lastID, d.cfg.MaxCapacity)
		if err != nil {
			return err
		}
		if msg == nil {
			if haveStats {
				if err := d.Step(ctx, stats); err != nil {
					return err
				}
			}
			continue
		}
		lastID = msg.ID
		stats = msg.Statistics
		haveStats = true
		if err := d.Step(ctx, stats); err != nil {
			return err
		}
		if err := d.session.Ack(ctx, msg.ID); err != nil {
			return err
		}
	}
}

// Step runs one planning cycle against the current driver state.
func (d *Dispatcher) Step(ctx context.Context, stats Statistics) error {
	instances, err := d.driver.List(ctx)
	if err != nil {
		return fmt.Errorf("list instances: %w", err)
	}
	actions, err := planner.Plan(planner.Input{
		AssignedJobs: stats.AssignedJobs,
		RunningJobs:  stats.RunningJobs,
		MaxCapacity:  d.cfg.MaxCapacity,
		Instances:    instances,
	})
	if err != nil {
		return fmt.Errorf("plan: %w", err)
	}
	free := freeNames(d.cfg.NamePrefix, d.cfg.MaxCapacity, instances)
	for _, action := range actions {
		switch action.Kind {
		case planner.ActionBoot:
			if len(free) == 0 {
				return fmt.Errorf("no free VM name below capacity %d", d.cfg.MaxCapacity)
			}
			name := free[0]
			free = free[1:]
			jit, err := d.jit.Mint(ctx)
			if err != nil {
				return fmt.Errorf("mint jit config: %w", err)
			}
			spec := vm.BootSpec{
				Name:      name,
				JobID:     action.JobID,
				JITConfig: jit,
				Class:     d.cfg.Class,
			}
			if _, err := d.driver.Boot(ctx, spec); err != nil {
				return fmt.Errorf("boot %s: %w", spec.Name, err)
			}
			d.cfg.Logger.Info("booted runner vm",
				"name", spec.Name, "job", spec.JobID,
				"assigned", stats.AssignedJobs, "running", stats.RunningJobs)
		case planner.ActionKill:
			if err := d.driver.Kill(ctx, action.InstanceID); err != nil {
				return fmt.Errorf("reclaim %s: %w", action.InstanceID, err)
			}
			d.cfg.Logger.Info("reclaimed runner vm", "name", action.InstanceID)
		default:
			return fmt.Errorf("unknown action kind %q", action.Kind)
		}
	}
	d.publishHealth(ctx, stats)
	return nil
}

// publishHealth writes one pool-health record. Planning stays gated on
// observed statistics; the health record does not, so the router can see a
// live pool before the first job. Advisory failures are logged and retried on
// the next cycle.
func (d *Dispatcher) publishHealth(ctx context.Context, stats Statistics) {
	if d.cfg.Heartbeat == nil {
		return
	}
	live, err := d.driver.List(ctx)
	if err != nil {
		d.cfg.Logger.Warn("list instances for heartbeat", "err", err)
		return
	}
	record := heartbeat.Record{
		Timestamp:       time.Now().UTC(),
		ListenerHealthy: true,
		AssignedJobs:    stats.AssignedJobs,
		RunningJobs:     stats.RunningJobs,
		LiveRunners:     len(live),
	}
	if err := d.cfg.Heartbeat.Publish(ctx, record); err != nil {
		d.cfg.Logger.Warn("publish heartbeat", "err", err)
	}
}

// freeNames returns the unused slot names in ascending order so a bounded
// pool recycles names instead of growing them without limit.
func freeNames(prefix string, capacity int, instances []vm.Instance) []string {
	used := make(map[string]struct{}, len(instances))
	for _, inst := range instances {
		used[inst.ID] = struct{}{}
	}
	free := make([]string, 0, capacity)
	for i := 1; i <= capacity; i++ {
		name := fmt.Sprintf("%s-%d", prefix, i)
		if _, ok := used[name]; !ok {
			free = append(free, name)
		}
	}
	return free
}

// Drain stops accepting new work, reclaims VMs without a job, and waits for
// in-flight jobs to finish. It returns ctx.Err() when the context expires
// first.
func (d *Dispatcher) Drain(ctx context.Context) error {
	ticker := time.NewTicker(d.cfg.DrainPoll)
	defer ticker.Stop()
	for {
		instances, err := d.driver.List(ctx)
		if err != nil {
			return fmt.Errorf("list instances: %w", err)
		}
		remaining := 0
		for _, inst := range instances {
			switch inst.State {
			case vm.StateBusy:
				remaining++
			case vm.StateBooting, vm.StateIdle, vm.StateExited:
				if err := d.driver.Kill(ctx, inst.ID); err != nil {
					return fmt.Errorf("reclaim %s during drain: %w", inst.ID, err)
				}
				d.cfg.Logger.Info("reclaimed runner vm during drain", "name", inst.ID, "state", inst.State)
			default:
				remaining++
			}
		}
		if remaining == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
