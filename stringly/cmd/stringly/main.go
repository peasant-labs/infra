// Command stringly guards a repository against stringly-typed values: a
// closed-set member, a test id, an endpoint path or a data path written as a
// bare string where a typed value belongs. It scans with ast-grep, compares
// the hits with a committed per-file baseline, and fails on anything new.
//
//	stringly check   [--config PATH] [--lang go|ts] [--format text|github] [--ast-grep PATH]
//	stringly update  [--config PATH] [--lang go|ts]
//	stringly init    [--config PATH]
//	stringly show    catalog|rules|version [--config PATH] [--format text|json]
//
// Exit 0: clean. Exit 1: findings. Exit 2: the tool could not run.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"runtime/debug"

	"github.com/peasant-labs/infra/stringly/internal/astgrep"
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
	os.Exit(run(os.Args[1:], version, built, os.Getenv, os.Stdout, os.Stderr))
}

// run is the whole command: main only gathers the process inputs.
func run(args []string, linked, built string, getenv func(string) string, stdout, stderr io.Writer) int {
	v, err := resolveVersion(linked, built, getenv(devEnvVar))
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

	switch cmd {
	case CommandShow:
		return runShow(args[1:], v, stdout, stderr)
	case CommandCheck, CommandUpdate, CommandInit:
		return environmentFailure(stderr,
			fmt.Sprintf("the %s command is not part of build %s.", cmd, v),
			"this build carries only `stringly show version`; the scan, baseline and catalog are not in it.",
			"command dispatch, before any file was read.",
			"run a stringly release that includes the "+cmd.String()+" command.")
	}
	return environmentFailure(stderr,
		fmt.Sprintf("command %q has no handler.", cmd),
		"the command is in the closed set but this build does not dispatch it.",
		"command dispatch, before any file was read.",
		"report this to the infra maintainers with the command line you ran.")
}

func runShow(args []string, v toolVersion, stdout, stderr io.Writer) int {
	topicFix := "run one of: " + joinNames(AllShowTopics, ", ") + ", for example `stringly show version`."
	if len(args) == 0 {
		return environmentFailure(stderr,
			"`stringly show` needs a topic.",
			"show prints exactly one of these topics: "+joinNames(AllShowTopics, ", ")+".",
			"command-line parsing, before any file was read.",
			topicFix)
	}
	topic, err := ParseShowTopic(args[0])
	if err != nil {
		return environmentFailure(stderr,
			err.Error()+".",
			"show prints exactly one of these topics: "+joinNames(AllShowTopics, ", ")+".",
			"command-line parsing, before any file was read.",
			topicFix)
	}
	if len(args) > 1 {
		return environmentFailure(stderr,
			fmt.Sprintf("unexpected argument %q after `stringly show %s`.", args[1], topic),
			"this build accepts no flag or argument after the topic.",
			"command-line parsing, before any file was read.",
			fmt.Sprintf("run `stringly show %s` with nothing after it.", topic))
	}

	switch topic {
	case TopicVersion:
		if _, err := fmt.Fprintf(stdout, "stringly %s (requires ast-grep %s)\n", v, astgrep.RequiredVersion); err != nil {
			return environmentFailure(stderr,
				"the version could not be written.",
				err.Error(),
				"`stringly show version`, writing to standard output.",
				"check that standard output is writable.")
		}
		return exitClean
	case TopicCatalog, TopicRules:
		return environmentFailure(stderr,
			fmt.Sprintf("the %s topic is not part of build %s.", topic, v),
			"this build carries only `stringly show version`.",
			"`stringly show`, before any file was read.",
			"run a stringly release that includes `stringly show "+topic.String()+"`.")
	}
	return environmentFailure(stderr,
		fmt.Sprintf("topic %q has no handler.", topic),
		"the topic is in the closed set but this build does not dispatch it.",
		"`stringly show`, before any file was read.",
		"report this to the infra maintainers with the command line you ran.")
}

func usageFix() string {
	return "run `stringly check`, `stringly update`, `stringly init` or `stringly show " +
		joinNames(AllShowTopics, "|") + "`."
}

// environmentFailure prints a failure in the message contract's four parts and
// returns the exit code for a run that could not happen.
func environmentFailure(w io.Writer, what, why, where, fix string) int {
	fmt.Fprintf(w, "stringly: %s\n  why    %s\n  where  %s\n  fix    %s\n", what, why, where, fix)
	return exitEnvironment
}
