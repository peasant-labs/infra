// Package heartbeat publishes the pool-health record the router reads in
// place of probing for online runners.
package heartbeat

import (
	"context"
	"time"
)

// Record is one pool-health observation. Timestamp is set by the publisher.
type Record struct {
	Timestamp       time.Time `json:"timestamp"`
	ListenerHealthy bool      `json:"listener_healthy"`
	AssignedJobs    int       `json:"assigned_jobs"`
	RunningJobs     int       `json:"running_jobs"`
	LiveRunners     int       `json:"live_runners"`
	// MaxCapacity is the scale set's slot count. The router routes only
	// when MaxCapacity - AssignedJobs (booting and running) leaves at
	// least the caller's required_slots free; a zero MaxCapacity skips
	// the gate.
	MaxCapacity int `json:"max_capacity"`
}

// Publisher writes a record where the router can read it.
type Publisher interface {
	Publish(ctx context.Context, record Record) error
}
