package server

type Status string

const (
	StatusReady  Status = "ready"
	StatusFailed Status = "failed"
)
