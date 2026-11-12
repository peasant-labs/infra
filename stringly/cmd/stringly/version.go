package main

import (
	"fmt"
	"strings"
)

// devEnvVar is the environment variable that lets a build with no version run.
const devEnvVar = "STRINGLY_DEV"

// develBuildVersion is what the Go toolchain records as the main module's
// version when it knows none.
const develBuildVersion = "(devel)"

// toolVersion is the running tool's version, as written to the baseline's
// `tool:` header line.
type toolVersion struct {
	// Value is the version, for example "v0.1.0". It is empty for a
	// development build.
	Value string
	// Dev is true for a local build with no version, allowed by STRINGLY_DEV=1.
	// A development build refuses init and update.
	Dev bool
}

// String returns the version for display.
func (v toolVersion) String() string {
	if v.Dev {
		return "(development build)"
	}
	return v.Value
}

// resolveVersion decides the running tool's version.
//
// linked is the value set with `-ldflags "-X main.version=…"`; it wins.
// built is the main module version from the build information, which
// `go run …@vX.Y.Z` records. A local build records "(devel)", nothing, or a
// "+dirty" version; none of those identifies a released tool, so such a build
// runs only when devEnv is "1".
func resolveVersion(linked, built, devEnv string) (toolVersion, error) {
	if linked != "" {
		if !strings.HasPrefix(linked, "v") || strings.ContainsAny(linked, " \t\n") {
			return toolVersion{}, &versionError{
				why: fmt.Sprintf("the linker-set version %q is not of the form vMAJOR.MINOR.PATCH, "+
					"and the baseline header must name a version.", linked),
				fix: "rebuild with -ldflags \"-X main.version=vX.Y.Z\" naming a real version, or drop the flag.",
			}
		}
		return toolVersion{Value: linked}, nil
	}
	if built != "" && built != develBuildVersion && !strings.Contains(built, "+dirty") {
		return toolVersion{Value: built}, nil
	}
	if devEnv == "1" {
		return toolVersion{Dev: true}, nil
	}
	recorded := "no version"
	if built != "" {
		recorded = fmt.Sprintf("the development version %q", built)
	}
	return toolVersion{}, &versionError{
		why: fmt.Sprintf("this build carries %s, which names no release, so the baseline header "+
			"could not say which tool verified the counts.", recorded),
		fix: "run a released version (go run github.com/peasant-labs/infra/stringly/cmd/stringly@vX.Y.Z), " +
			"build with -ldflags \"-X main.version=vX.Y.Z\", or set " + devEnvVar + "=1 for local development.",
	}
}

// versionError is a version failure, split into the message contract's why
// and fix parts.
type versionError struct {
	why string
	fix string
}

func (e *versionError) Error() string { return e.why + " " + e.fix }
