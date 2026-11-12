// Command stringly guards a repository against stringly-typed values: a
// closed-set member, a test id, an endpoint path or a data path written as a
// bare string where a typed value belongs. It scans with ast-grep, compares
// the hits with a committed per-file baseline, and fails on anything new.
//
//	stringly check   [--config PATH] [--lang go|ts] [--format text|github] [--ast-grep PATH]
//	stringly update  [--config PATH] [--lang go|ts] [--ast-grep PATH]
//	stringly init    [--config PATH] [--ast-grep PATH]
//	stringly show    catalog|rules|version [--config PATH] [--format text|json] [--ast-grep PATH]
//
// Exit 0: clean. Exit 1: findings, or a refused update or init. Exit 2: the
// tool could not run (config, ast-grep version, schema module, unreadable
// baseline).
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strings"

	"github.com/peasant-labs/infra/stringly/internal/failure"
)

// version is set at link time: -ldflags "-X main.version=v0.1.0". It overrides
// the version recorded in the build information.
var version string

// Exit codes.
const (
	exitClean       = 0
	exitFindings    = 1
	exitEnvironment = 2
)

func main() {
	built := ""
	if info, ok := debug.ReadBuildInfo(); ok {
		built = info.Main.Version
	}
	os.Exit(run(context.Background(), os.Args[1:], version, built, os.Environ(), os.Stdout, os.Stderr, wiredDeps()))
}

// process is what one invocation runs with.
type process struct {
	ctx     context.Context
	version toolVersion
	environ []string
	stdout  io.Writer
	stderr  io.Writer
	deps    deps
}

// getenv reads one variable from an os.Environ-form environment; the last
// assignment wins, as in the process environment.
func getenv(environ []string, key string) string {
	v := ""
	for _, kv := range environ {
		if k, val, ok := strings.Cut(kv, "="); ok && k == key {
			v = val
		}
	}
	return v
}

// run is the whole command: main only gathers the process inputs.
func run(ctx context.Context, args []string, linked, built string, environ []string, stdout, stderr io.Writer, d deps) int {
	v, err := resolveVersion(linked, built, getenv(environ, devEnvVar))
	if err != nil {
		why, fix := err.Error(), "report this to the infra maintainers with the command line you ran."
		var verr *versionError
		if errors.As(err, &verr) {
			why, fix = verr.why, verr.fix
		}
		return environmentFailure(stderr,
			"the tool's own version could not be determined.",
			why,
			"version check, before the command was read. No file was read.",
			fix)
	}

	if len(args) == 0 {
		return environmentFailure(stderr,
			"no command given.",
			"stringly needs one of its commands: "+joinNames(AllCommands, ", ")+".",
			"command-line parsing, before any file was read.",
			usageFix())
	}
	cmd, err := ParseCommand(args[0])
	if err != nil {
		return environmentFailure(stderr,
			err.Error()+".",
			"stringly has exactly these commands: "+joinNames(AllCommands, ", ")+".",
			"command-line parsing, before any file was read.",
			usageFix())
	}

	p := process{ctx: ctx, version: v, environ: environ, stdout: stdout, stderr: stderr, deps: d}
	switch cmd {
	case CommandShow:
		return p.runShow(args[1:])
	case CommandCheck:
		return p.runCheck(args[1:])
	case CommandUpdate:
		return p.runUpdate(args[1:])
	case CommandInit:
		return p.runInit(args[1:])
	}
	return environmentFailure(stderr,
		fmt.Sprintf("command %q has no handler.", cmd),
		"the command is in the closed set but this build does not dispatch it.",
		"command dispatch, before any file was read.",
		"report this to the infra maintainers with the command line you ran.")
}

func usageFix() string {
	runs := make([]string, len(AllCommands))
	for i, c := range AllCommands {
		runs[i] = "`stringly " + c.String()
		if c == CommandShow {
			runs[i] += " " + joinNames(AllShowTopics, "|")
		}
		runs[i] += "`"
	}
	last := len(runs) - 1
	return "run " + strings.Join(runs[:last], ", ") + " or " + runs[last] + "."
}

// environmentFailure prints a failure in the message contract's four parts and
// returns the exit code for a run that could not happen.
func environmentFailure(w io.Writer, what, why, where, fix string) int {
	return fail(w, failure.New(what, why, where, fix))
}

// fail prints a failure and returns exit 2.
func fail(w io.Writer, e *failure.Error) int {
	failure.Render(w, e)
	return exitEnvironment
}

// failErr prints err, which is a *failure.Error or is wrapped into one, and
// returns exit 2.
func failErr(w io.Writer, err error, where string) int {
	var ferr *failure.Error
	if errors.As(err, &ferr) {
		return fail(w, ferr)
	}
	return environmentFailure(w, err.Error()+".",
		"stringly could not complete the step named below.",
		where,
		"fix the cause named above and run the command again.")
}
