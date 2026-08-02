# Peasant Labs infrastructure

Public Terraform configurations and deployment workflows for Peasant Labs.

## Managed infrastructure

| Stack | HCP Terraform workspace | GitHub environment | Resources |
|---|---|---|---|
| `stacks/village-production` | `village-production` | `village-infra` | Existing private `village-transcripts` R2 bucket |
| `stacks/pkgs-production` | `pkgs-production` | `pkgs-infra` | Public `pkgs` R2 bucket and `pkgs.peasantlabs.org` custom domain |

The stacks share source code only through `modules/r2-bucket`. They have separate
state, Cloudflare credentials, GitHub approvals, and apply concurrency groups.

Railway is intentionally outside this Terraform boundary. The working Village
Railway environment remains dashboard-managed. This repository must not import,
mutate, or store its services, database, variables, runtime credentials, or
transcript key-encryption keys.

## Fixed versions

The initial baseline records the versions tested on 2026-08-01:

| Dependency | Version |
|---|---|
| Terraform CLI | `1.15.8` |
| Cloudflare Terraform provider | `5.22.0` |
| `actions/checkout` | `7.0.1`, pinned to commit `3d3c42e5aac5ba805825da76410c181273ba90b1` |
| `hashicorp/setup-terraform` | `4.0.1`, pinned to commit `dfe3c3f87815947d99a8997f908cb6525fc44e9e` |
| `actions/upload-artifact` | `7.0.1`, pinned to commit `043fb46d1a93c77aae656e7c1c64a875d1fc6a0a` |
| `actions/download-artifact` | `8.0.1`, pinned to commit `3e5f45b2cfb9172054b4087a40e8e0b5a5461e7c` |

Provider checksums are committed in each stack's `.terraform.lock.hcl`.
Dependency updates arrive as reviewable Dependabot pull requests.

## Local validation

Install Terraform `1.15.8`, then run:

```sh
make check
```

This checks formatting, initializes each stack without its remote backend,
validates it, and runs the stack's fixture-backed Terraform tests. It does not
contact Cloudflare or HCP Terraform.

## Deployment setup

Create the two named HCP Terraform workspaces before the first remote operation.
Configure them for local execution so GitHub Actions runs Terraform while HCP
Terraform supplies encrypted state and locking.

The repository has these protected GitHub environments. Configure their
variables and secrets before the first deployment:

| Environment | Variables | Secrets |
|---|---|---|
| `village-infra` | `TF_CLOUD_ORGANIZATION`, `CLOUDFLARE_ACCOUNT_ID` | `TF_API_TOKEN`, `CLOUDFLARE_API_TOKEN` |
| `pkgs-infra` | `TF_CLOUD_ORGANIZATION`, `CLOUDFLARE_ACCOUNT_ID`, `CLOUDFLARE_ZONE_ID` | `TF_API_TOKEN`, `CLOUDFLARE_API_TOKEN` |

Require maintainer approval on both environments. The Cloudflare token in each
environment should have only the permissions needed by that stack.

Use the `Terraform deployment` workflow to plan or apply one stack. An apply run
creates and prints an exact plan first, retains its binary plan artifact for one
day, and applies only that artifact after the apply job passes the protected
environment gate. State drift invalidates the saved plan rather than causing a
new unreviewed plan. Apply is accepted only from `main`; no destroy operation
exists.

### Existing Village bucket

The first `village-production` operation must import the existing private bucket
before any apply:

```sh
export TF_CLOUD_ORGANIZATION='<hcp-terraform-organization>'
export TF_WORKSPACE='village-production'
export TF_TOKEN_app_terraform_io='<hcp-terraform-token>'
export CLOUDFLARE_API_TOKEN='<village-infrastructure-token>'
export TF_VAR_cloudflare_account_id='<cloudflare-account-id>'

terraform -chdir=stacks/village-production init
terraform -chdir=stacks/village-production import \
  module.transcript_bucket.cloudflare_r2_bucket.this \
  '<cloudflare-account-id>/village-transcripts/default'
terraform -chdir=stacks/village-production plan
```

Cloudflare provider `5.22.0` requires all three import-ID segments, including the
`default` jurisdiction.

The first post-import plan may add only the explicit disabled `r2.dev` control.
Stop if it proposes bucket replacement, public access, a custom domain, or a
location change.

## Secret boundary

Terraform manages control-plane resources only. It must never create or store:

- R2 S3 access keys.
- Village transcript key-encryption keys.
- Package archive signing keys.
- Railway variables or credentials.
- Terraform or Cloudflare API tokens.

State files, plan files, variable files, and credentials are ignored locally and
must not be committed. Account and zone IDs are supplied through protected
environment variables rather than source files.

GitHub temporarily stores a saved plan for each deployment run. Terraform plan
files can contain complete resource values, so the no-secret-in-Terraform rule is
also a plan-artifact requirement, not only a source and state requirement.
