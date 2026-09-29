// Package heartbeat publishes the pool-health record the router reads in
// place of probing for online runners.
package heartbeat

import (
	"context"
	"time"
)

// Record is one pool-health observation. Timestamp is set by the publisher.
type Record struct {
	Timestamp       time.Time
	ListenerHealthy bool
	AssignedJobs    int
	RunningJobs     int
	LiveRunners     int
}

// Publisher writes a record where the router can read it.
type Publisher interface {
	Publish(ctx context.Context, record Record) error
}
