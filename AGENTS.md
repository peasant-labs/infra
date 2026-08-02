# Agent instructions

This repository owns Terraform control-plane resources and the workflows that
plan and apply them.

## Boundaries

- Keep `village-production` and `pkgs-production` as separate Terraform roots,
  HCP Terraform workspaces, credentials, GitHub environments, and concurrency
  groups.
- Never commit Terraform state, plans, credentials, account IDs, zone IDs,
  signing material, R2 S3 keys, Railway variables, or Village transcript KEKs.
- Never create secret-bearing R2 credentials through Terraform. Provider secret
  values would be retained in state.
- Never add secret-bearing Terraform attributes. Saved deployment plans are
  short-lived GitHub artifacts and can contain complete resource values.
- Railway is externally managed. Do not add the community Railway provider or
  import the working production environment without a separately reviewed
  decision.
- Preserve `prevent_destroy` on production buckets and the package custom domain.
- Do not add an automated destroy workflow.

## Versions

- Terraform CLI is exactly `1.15.8` in roots and shared modules.
- Cloudflare provider is exactly `5.22.0` in every root and shared module.
- GitHub Actions use immutable commit SHAs with a version comment.
- Regenerate and commit every root's `.terraform.lock.hcl` when a provider pin
  changes.

## Validation

Run `make check` before committing. Tests must use Terraform mock providers and
must not contact production services. Add separate `.tftest.hcl` fixtures for
new stack contracts rather than embedding test-only resources in production
configuration.
