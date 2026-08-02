TERRAFORM ?= terraform

.PHONY: check fmt guard

fmt:
	$(TERRAFORM) fmt -check -recursive

guard:
	bash scripts/check-production-guards.sh

check: fmt guard
	$(TERRAFORM) -chdir=stacks/village-production init -backend=false -lockfile=readonly
	$(TERRAFORM) -chdir=stacks/village-production validate
	$(TERRAFORM) -chdir=stacks/village-production test
	$(TERRAFORM) -chdir=stacks/pkgs-production init -backend=false -lockfile=readonly
	$(TERRAFORM) -chdir=stacks/pkgs-production validate
	$(TERRAFORM) -chdir=stacks/pkgs-production test
