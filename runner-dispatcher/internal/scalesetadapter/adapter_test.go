package scalesetadapter

import (
	"strings"
	"testing"
)

// One stale registration — a boot whose runner never connected — makes GitHub
// reject every later mint that reuses the same name, so each mint must carry a
// fresh suffix while the slot name stays the readable prefix.
func TestUniqueRunnerNameKeepsPrefixAndVaries(t *testing.T) {
	first, err := uniqueRunnerName("runner-vm-1")
	if err != nil {
		t.Fatalf("uniqueRunnerName: %v", err)
	}
	second, err := uniqueRunnerName("runner-vm-1")
	if err != nil {
		t.Fatalf("uniqueRunnerName: %v", err)
	}
	if !strings.HasPrefix(first, "runner-vm-1-") {
		t.Fatalf("name %q lost the slot prefix", first)
	}
	if first == second {
		t.Fatalf("two mints produced the same name %q", first)
	}
}
