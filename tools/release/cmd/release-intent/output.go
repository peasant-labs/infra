package main

import (
	"fmt"
	"os"
	"strconv"

	"github.com/peasant-labs/infra/tools/release/internal/intent"
)

// githubOutputEnv names the environment variable holding the step-output
// file path a GitHub Actions job provides.
const githubOutputEnv = "GITHUB_OUTPUT"

// outputLine is one key=value line of the machine channel. The key is a
// member of the contract's closed step-output set; no line is built from a
// bare key string.
type outputLine struct {
	Key   intent.OutputKey
	Value string
}

func out(key intent.OutputKey, value string) outputLine {
	return outputLine{Key: key, Value: value}
}

func outBool(key intent.OutputKey, value bool) outputLine {
	return outputLine{Key: key, Value: strconv.FormatBool(value)}
}

func (l outputLine) String() string { return l.Key.Line(l.Value) }

// emit writes the lines to stdout and appends them to the file named by
// $GITHUB_OUTPUT when set, so workflow steps can consume them as step
// outputs. The file is opened append-only and never truncated: it
// accumulates outputs across a job's steps.
func emit(lines ...outputLine) error {
	for _, line := range lines {
		fmt.Println(line.String())
	}
	if path := os.Getenv(githubOutputEnv); path != "" {
		if err := appendOutputFile(path, lines); err != nil {
			return fmt.Errorf("cannot write to $%s (%s): %w. Unset $%s to print to stdout only, or fix the path permissions", githubOutputEnv, path, err, githubOutputEnv)
		}
	}
	return nil
}

func appendOutputFile(path string, lines []outputLine) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, line := range lines {
		if _, err := f.WriteString(line.String() + "\n"); err != nil {
			return err
		}
	}
	return nil
}
