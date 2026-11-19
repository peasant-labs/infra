# Testing

## Tests never read your git configuration

Each Go module has an `internal/testutil` copy of `RunWithIsolatedGitConfig`.
Keep their environment-variable lists in sync. A test package that starts a
process must call it from an unconstrained `TestMain`, even when the process is
not itself git: production code or a script may start git later. It suppresses
system and global config and environment-injected config and repository paths;
repository-local configuration remains available. Git 2.32 or newer is needed.
Tests that intentionally exercise hostile config may override the baseline with
`t.Setenv`. Children that construct their own environment must preserve the
isolation or establish an equivalent one themselves.

`tools/release/internal/testutil` runs `TestGitIsolationCopiesMatch` to check
the helper, guard, behavioural tests and scanner fixtures are byte-identical
across the three modules. Run it from a complete repository checkout.

Each module scans its test sources for `os/exec` imports. Their package must
have an unconstrained `TestMain` ending in
`os.Exit(testutil.RunWithIsolatedGitConfig(...))`, with no other `m.Run`,
`goleak.VerifyTestMain`, `os.Exit`, or isolation-helper call. Setup before the
exit is allowed. The helper's own package instead calls unexported
`isolateGitConfig` in a top-level `if err := isolateGitConfig(); err != nil`
that exits nonzero, then ends with `os.Exit(m.Run())`, without a competing
runner, exit, or isolation call. It runs independently so a broken helper
cannot skip its own tests. Its fixture corpus tests the scanner. This is a
conservative source guard, not a proof of process isolation: it cannot see
processes started through other packages, dynamically generated code, or a
child environment that discards the baseline. It recognizes the helper by
name, not by type resolution or control flow. Platform-specific filenames are
not detected without explicit build constraints; do not put the TestMain in one.
