package schema

// TestOnly is declared in a test file, so it is never a set.
type TestOnly string

const (
	TestOnlyA TestOnly = "test-only-a"
	TestOnlyB TestOnly = "test-only-b"
)
