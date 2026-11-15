//go:build !unix

package main

import (
	"fmt"
	"os"
)

// Preparation requires descriptor-relative, no-symlink traversal. Other
// commands, including the legacy write mode, remain available on this host.
func openConfined(name string) (*os.File, error) {
	return nil, fmt.Errorf("safe release-commit preparation is unavailable on this platform for %q; run it on a Unix runner", name)
}
