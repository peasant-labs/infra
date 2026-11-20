TERRAFORM ?= terraform

.PHONY: check fmt guard release-tool-check

fmt:
	$(TERRAFORM) fmt -check -recursive

guard:
	bash scripts/check-production-guards.sh

# The release tool is its own Go module; these are the steps its workflow runs.
# The module has no vendor directory, and the dev shell exports a GOFLAGS that
# selects vendor mode, so every go command here replaces the ambient GOFLAGS
# with the module mode the workflow gets by default (readonly: go.mod and
# go.sum are used as committed and never rewritten).
RELEASE_TOOL_GO = GOFLAGS=-mod=readonly go

# -count=1 bypasses go test's result cache. cmd/release-intent's process tests
# run a binary that TestMain builds with go build, which the cache cannot see;
# TestMain keeps main reachable so the test binary changes with it, and the
# gate does not rely on that alone.
release-tool-check:
	cd tools/release && { test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }; }
	cd tools/release && $(RELEASE_TOOL_GO) build ./...
	cd tools/release && $(RELEASE_TOOL_GO) vet ./...
	cd tools/release && $(RELEASE_TOOL_GO) test ./... -count=1

check: fmt guard release-tool-check
	$(TERRAFORM) -chdir=stacks/village-production init -backend=false -lockfile=readonly
	$(TERRAFORM) -chdir=stacks/village-production validate
	$(TERRAFORM) -chdir=stacks/village-production test
	$(TERRAFORM) -chdir=stacks/pkgs-production init -backend=false -lockfile=readonly
	$(TERRAFORM) -chdir=stacks/pkgs-production validate
	$(TERRAFORM) -chdir=stacks/pkgs-production test
