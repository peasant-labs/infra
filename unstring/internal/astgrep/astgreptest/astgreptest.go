// Package astgreptest locates the pinned ast-grep binary for tests that run
// it. The binary is UNSTRING_AST_GREP when set, else `ast-grep` on PATH, and
// it must report exactly astgrep.RequiredVersion. A missing or different
// binary fails the test with the install command; it never skips it.
package astgreptest

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/peasant-labs/infra/unstring/internal/astgrep"
)

// Env is the variable that names the ast-grep binary for tests.
const Env = "UNSTRING_AST_GREP"

var (
	once sync.Once
	path string
	err  error
)

// Require returns the absolute path of the pinned ast-grep binary, or fails
// the test with the install command.
func Require(t testing.TB) string {
	t.Helper()
	once.Do(func() {
		b := astgrep.NewBinary(lookup())
		if cerr := astgrep.CheckVersion(context.Background(), b); cerr != nil {
			err = cerr
			return
		}
		path, err = filepath.Abs(b.Resolved())
	})
	if err != nil {
		t.Fatalf("these tests run the real ast-grep %s and found none usable: %v\n"+
			"Install it with `npm install --global @ast-grep/cli@%s` (or `npm install --global --prefix <dir> @ast-grep/cli@%s`), "+
			"then put it on PATH or set %s to the binary, and re-run go test.",
			astgrep.RequiredVersion, err, astgrep.RequiredVersion, astgrep.RequiredVersion, Env)
	}
	return path
}
