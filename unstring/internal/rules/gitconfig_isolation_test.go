package rules_test

import (
	"github.com/peasant-labs/infra/unstring/internal/testutil"
	"os"
	"testing"
)

func TestMain(m *testing.M) { os.Exit(testutil.RunWithIsolatedGitConfig(m)) }
