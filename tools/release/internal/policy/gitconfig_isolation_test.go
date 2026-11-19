package policy

import (
	"github.com/peasant-labs/infra/tools/release/internal/testutil"
	"os"
	"testing"
)

func TestMain(m *testing.M) { os.Exit(testutil.RunWithIsolatedGitConfig(m)) }
