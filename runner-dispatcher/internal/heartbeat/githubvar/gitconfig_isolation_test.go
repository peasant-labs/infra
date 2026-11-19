package githubvar

import (
	"github.com/peasant-labs/infra/runner-dispatcher/internal/testutil"
	"os"
	"testing"
)

func TestMain(m *testing.M) { os.Exit(testutil.RunWithIsolatedGitConfig(m)) }
