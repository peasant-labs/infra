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
	// MaxCapacity is the scale set's slot count. The router falls back when
	// AssignedJobs (booting and running) fills it, because a job beyond it
	// would wait in the queue instead of starting.
	MaxCapacity int `json:"max_capacity"`
}

// Publisher writes a record where the router can read it.
type Publisher interface {
	Publish(ctx context.Context, record Record) error
}
