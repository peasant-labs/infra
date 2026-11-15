package astgreptest

import (
	"os"

	"github.com/peasant-labs/infra/unstring/internal/astgrep"
)

func lookup() string {
	if p := os.Getenv(Env); p != "" {
		return p
	}
	return astgrep.DefaultCommand
}
