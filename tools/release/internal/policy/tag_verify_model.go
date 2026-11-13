package policy

import (
	"path"

	"github.com/peasant-labs/infra/tools/release/internal/closedset"
	"github.com/peasant-labs/infra/tools/release/internal/intent"
)

// This file is the model of a tag-verify job: the one job without a
// credential that a tag-triggered publish job needs, and whose success is
// the only thing that lets the publish job run. Its exit status is the
// verdict on the tag, so it is confined the way a credential job is: it may
// run only the steps listed here, each in one fixed shape, and nothing in
// the job may decide what the verify command does or what its step exits
// with.

// The tool the verify job builds: the repository it is checked out from,
// the module directory inside that checkout, and the main package.
const (
	toolRepository  = "peasant-labs/infra"
	toolModuleDir   = "tools/release"
	toolMainPackage = "./cmd/" + intent.ProgramName
	toolGoMod       = "go.mod"
)

// runnerEnvTemp is the runner's per-job temporary directory. The verify job
// builds the tool there and runs it by that absolute path, so no PATH entry
// an action or a file in the checkout adds can stand in for it.
const runnerEnvTemp = "RUNNER_TEMP"

// VerifyStep is one kind of step a tag-verify job may run. The zero value is
// unknown.
type VerifyStep string

const (
	// VerifyStepRepoCheckout checks out the repository at the tag.
	VerifyStepRepoCheckout VerifyStep = "repo-checkout"
	// VerifyStepToolCheckout checks out the tool's repository at a commit.
	VerifyStepToolCheckout VerifyStep = "tool-checkout"
	// VerifyStepSetupGo installs the Go version the tool's go.mod names.
	VerifyStepSetupGo VerifyStep = "setup-go"
	// VerifyStepBuild builds the tool into the runner's temporary directory.
	VerifyStepBuild VerifyStep = "build"
	// VerifyStepVerify runs verify-tag on the triggering tag and the default
	// branch.
	VerifyStepVerify VerifyStep = "verify"
)

// AllVerifySteps is the canonical closed set of tag-verify job steps.
var AllVerifySteps = []VerifyStep{
	VerifyStepRepoCheckout,
	VerifyStepToolCheckout,
	VerifyStepSetupGo,
	VerifyStepBuild,
	VerifyStepVerify,
}

func (s VerifyStep) String() string { return string(s) }

// Shape renders the step's one accepted form for a message.
func (s VerifyStep) Shape() string {
	switch s {
	case VerifyStepRepoCheckout:
		return VerifyActionCheckout.String() + "@<40-hex> with persist-credentials: false [fetch-depth, fetch-tags]"
	case VerifyStepToolCheckout:
		return VerifyActionCheckout.String() + "@<40-hex> with repository: " + toolRepository + ", ref: <40-hex>, path: <dir>, persist-credentials: false [sparse-checkout]"
	case VerifyStepSetupGo:
		return VerifyActionSetupGo.String() + "@<40-hex> with go-version-file: <dir>/" + toolModuleDir + "/" + toolGoMod + ", cache: false"
	case VerifyStepBuild:
		return "go build -C <dir>/" + toolModuleDir + " -o \"${" + runnerEnvTemp + "}/" + intent.ProgramName + "\" " + toolMainPackage + ", with " + VerifyEnvGoWork.String() + ": " + VerifyEnvGoWork.Value()
	case VerifyStepVerify:
		return "\"${" + runnerEnvTemp + "}/" + intent.ProgramName + "\" " + intent.CommandVerifyTag.String() + " " + intent.FlagTag.String() + " \"${" + runnerEnvRefName + "}\" " + intent.FlagBranch.String() + " \"origin/${" + VerifyEnvDefaultBranch.String() + "}\""
	}
	return ""
}

// ParseVerifyStep maps a step kind's spelling to its member and refuses
// anything else.
func ParseVerifyStep(raw string) (VerifyStep, error) {
	return closedset.Parse(AllVerifySteps, raw, closedset.Context{
		Set:   "tag-verify job step",
		Where: "policy.ParseVerifyStep",
		When:  "reading a step of the job that verifies a pushed tag during merge-queue policy validation",
		Means: "the job may run only the listed steps, so the step is rejected",
	})
}

// VerifyAction is an action a tag-verify job may use, pinned to a 40-hex
// commit. The zero value is unknown.
type VerifyAction string

const (
	// VerifyActionCheckout checks out the repository or the tool.
	VerifyActionCheckout VerifyAction = "actions/checkout"
	// VerifyActionSetupGo installs Go for the build.
	VerifyActionSetupGo VerifyAction = "actions/setup-go"
)

// AllVerifyActions is the canonical closed set of tag-verify job actions.
var AllVerifyActions = []VerifyAction{VerifyActionCheckout, VerifyActionSetupGo}

func (a VerifyAction) String() string { return string(a) }

// ParseVerifyAction maps an action name (owner/repo, no ref) to its member
// and refuses every other action.
func ParseVerifyAction(raw string) (VerifyAction, error) {
	return closedset.Parse(AllVerifyActions, raw, closedset.Context{
		Set:   "tag-verify job action",
		Where: "policy.ParseVerifyAction",
		When:  "reading a uses: step in the job that verifies a pushed tag during merge-queue policy validation",
		Means: "the action runs code the checker cannot read before the verify step, so the step is rejected",
	})
}

// VerifyInput is one with: input a tag-verify job's action may take. Every
// value is a literal. The zero value is unknown.
type VerifyInput string

const (
	// VerifyInputPersistCredentials is checkout's persist-credentials, which
	// must be false.
	VerifyInputPersistCredentials VerifyInput = "persist-credentials"
	// VerifyInputFetchDepth is the repository checkout's fetch-depth.
	VerifyInputFetchDepth VerifyInput = "fetch-depth"
	// VerifyInputFetchTags is the repository checkout's fetch-tags.
	VerifyInputFetchTags VerifyInput = "fetch-tags"
	// VerifyInputRepository is the tool checkout's repository, which must be
	// the tool's.
	VerifyInputRepository VerifyInput = "repository"
	// VerifyInputRef is the tool checkout's commit.
	VerifyInputRef VerifyInput = "ref"
	// VerifyInputPath is the tool checkout's directory in the workspace.
	VerifyInputPath VerifyInput = "path"
	// VerifyInputSparseCheckout narrows the tool checkout.
	VerifyInputSparseCheckout VerifyInput = "sparse-checkout"
	// VerifyInputGoVersionFile is setup-go's version file, which must be the
	// tool's go.mod.
	VerifyInputGoVersionFile VerifyInput = "go-version-file"
	// VerifyInputCache is setup-go's cache switch, which must be false: a
	// restored build cache comes from another run and could hold any object
	// for the tool's packages.
	VerifyInputCache VerifyInput = "cache"
)

// AllVerifyInputs is the canonical closed set of tag-verify action inputs.
var AllVerifyInputs = []VerifyInput{
	VerifyInputPersistCredentials,
	VerifyInputFetchDepth,
	VerifyInputFetchTags,
	VerifyInputRepository,
	VerifyInputRef,
	VerifyInputPath,
	VerifyInputSparseCheckout,
	VerifyInputGoVersionFile,
	VerifyInputCache,
}

func (i VerifyInput) String() string { return string(i) }

// ParseVerifyInput maps an input name to its member and refuses anything
// else.
func ParseVerifyInput(raw string) (VerifyInput, error) {
	return closedset.Parse(AllVerifyInputs, raw, closedset.Context{
		Set:   "tag-verify action input",
		Where: "policy.ParseVerifyInput",
		When:  "reading the with: inputs of an action in the job that verifies a pushed tag during merge-queue policy validation",
		Means: "an input outside the set can change what the action checks out or installs, so the step is rejected",
	})
}

// verifyInputs lists the inputs a step kind may take and, of those, the ones
// it must take.
func verifyInputs(kind VerifyStep) (allowed, required []VerifyInput) {
	switch kind {
	case VerifyStepRepoCheckout:
		return []VerifyInput{VerifyInputPersistCredentials, VerifyInputFetchDepth, VerifyInputFetchTags},
			[]VerifyInput{VerifyInputPersistCredentials}
	case VerifyStepToolCheckout:
		return []VerifyInput{VerifyInputRepository, VerifyInputRef, VerifyInputPath, VerifyInputSparseCheckout, VerifyInputPersistCredentials},
			[]VerifyInput{VerifyInputRepository, VerifyInputRef, VerifyInputPath, VerifyInputPersistCredentials}
	case VerifyStepSetupGo:
		return []VerifyInput{VerifyInputGoVersionFile, VerifyInputCache},
			[]VerifyInput{VerifyInputGoVersionFile, VerifyInputCache}
	}
	return nil, nil
}

// VerifyEnv is a name the environment of a tag-verify job may carry, each
// with the one value it must have. Any other name (PATH, BASH_ENV, GOFLAGS,
// GOTOOLCHAIN, GOPROXY) could change which binary runs or what the build
// compiles. The zero value is unknown.
type VerifyEnv string

const (
	// VerifyEnvDefaultBranch carries the default branch name to the verify
	// command's --branch.
	VerifyEnvDefaultBranch VerifyEnv = "DEFAULT_BRANCH"
	// VerifyEnvGoWork turns workspace mode off for the build. The checkout of
	// the unverified tag sits in a parent directory of the tool, so a go.work
	// file there would otherwise replace the tool's modules with its own.
	VerifyEnvGoWork VerifyEnv = "GOWORK"
)

// AllVerifyEnvs is the canonical closed set of tag-verify env names.
var AllVerifyEnvs = []VerifyEnv{VerifyEnvDefaultBranch, VerifyEnvGoWork}

func (e VerifyEnv) String() string { return string(e) }

// Value is the one value the name may carry.
func (e VerifyEnv) Value() string {
	switch e {
	case VerifyEnvDefaultBranch:
		return "${{ " + defaultBranchExpression + " }}"
	case VerifyEnvGoWork:
		return "off"
	}
	return ""
}

// ParseVerifyEnv maps an env name to its member and refuses every other
// name.
func ParseVerifyEnv(raw string) (VerifyEnv, error) {
	return closedset.Parse(AllVerifyEnvs, raw, closedset.Context{
		Set:   "tag-verify env name",
		Where: "policy.ParseVerifyEnv",
		When:  "reading the environment of the job that verifies a pushed tag during merge-queue policy validation",
		Means: "a name outside the set can change which binary runs or what the build compiles, so the job is rejected",
	})
}

// toolBuildDir is the directory the build template changes to: the tool
// checkout's path joined with the module directory.
func toolBuildDir(checkoutPath string) string {
	return path.Join(checkoutPath, toolModuleDir)
}
