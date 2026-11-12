TERRAFORM ?= terraform

.PHONY: check fmt guard release-tool-check

fmt:
	$(TERRAFORM) fmt -check -recursive

guard:
	bash scripts/check-production-guards.sh

# The release tool is its own Go module; these are the steps its workflow runs.
release-tool-check:
	cd tools/release && { test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }; }
	cd tools/release && go build ./...
	cd tools/release && go vet ./...
	cd tools/release && go test ./... -count=1

check: fmt guard release-tool-check
	$(TERRAFORM) -chdir=stacks/village-production init -backend=false -lockfile=readonly
	$(TERRAFORM) -chdir=stacks/village-production validate
	$(TERRAFORM) -chdir=stacks/village-production test
	$(TERRAFORM) -chdir=stacks/pkgs-production init -backend=false -lockfile=readonly
	$(TERRAFORM) -chdir=stacks/pkgs-production validate
	$(TERRAFORM) -chdir=stacks/pkgs-production test
