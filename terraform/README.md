# Terraform

Three modules. `aws-bootstrap` and `gcp-bootstrap` create the access; `aws` creates the static
infrastructure that test runs attach to. Databases and Fargate tasks are not
here: the worker creates and deletes them on every run.

| Module | Creates | Applied by | How often |
|---|---|---|---|
| `aws-bootstrap/` | Policies `dbtest-infra`, `dbtest-run` and the role `dbtest` | An account admin (`AdministratorAccess` or `IAMFullAccess`) | Once, and when a policy or the user list changes |
| `aws/` | ECR repos, ECS cluster and task definitions, log group, security groups, RDS subnet group, ECS execution role | `dbtest` | Once, and when the infrastructure changes |
| `gcp-bootstrap/` | Cloud SQL, IAM and IAM Credentials APIs; service account `dbtest` with the custom role `dbtest` (`dbtest-role.yaml`); who may impersonate it | A project owner | Once, and when the role or the user list changes |

Applying `aws/`, pushing images and running the worker all use the `dbtest`
role. Its two policies split the permissions: `dbtest-infra` for the
infrastructure and images, `dbtest-run` for what the worker calls. Nobody needs
admin access after the bootstrap.

## Credentials

Neither module names a profile. Terraform, the AWS CLI and the Go worker all
take credentials from `AWS_PROFILE` (or any other source in the AWS default
credential chain). Check who you are before applying:

```sh
aws sts get-caller-identity --query Arn --output text
```

## 1. Bootstrap (admin, once)

List who may assume the roles:

```sh
cp terraform/aws-bootstrap/terraform.tfvars.example terraform/aws-bootstrap/terraform.tfvars
# edit trusted_principals: one ARN per user or role
```

Each user finds their ARN with `aws sts get-caller-identity`. For an IAM
Identity Center user, list the role ARN with its path:
`arn:aws:iam::<account>:role/aws-reserved/sso.amazonaws.com/<region>/AWSReservedSSO_<set>_<id>`.

```sh
AWS_PROFILE=<admin> terraform -chdir=terraform/aws-bootstrap init
AWS_PROFILE=<admin> terraform -chdir=terraform/aws-bootstrap apply
AWS_PROFILE=<admin> terraform -chdir=terraform/aws-bootstrap output
```

Hand the role ARN to the users. An admin who does not use Terraform can
create the same policies from `aws-bootstrap/dbtest-infra.json` and
`aws-bootstrap/dbtest-run.json`, or attach them to an Identity Center
permission set.

## 2. Profile (each user, once)

Add to `~/.aws/config`, with `source_profile` naming the user's own login:

```ini
[profile dbtest]
role_arn       = <role_arn>
source_profile = <your profile>
region         = us-west-2
```

`aws sts get-caller-identity --profile dbtest` should print
`assumed-role/dbtest/...`.

## 3. Infrastructure

```sh
export AWS_PROFILE=dbtest
make aws-cidr     # writes this machine's public IP to terraform/aws/terraform.tfvars and plans
make aws-apply
terraform -chdir=terraform/aws output -raw worker_env > .env
make push         # needs config.mk; see the Makefile
```

Regenerate `.env` after every apply that replaces a security group; the worker
reads their IDs from it.

## 4. Runs

```sh
export AWS_PROFILE=dbtest
source .env
STATE_DSN='postgres://postgres:test@localhost:5433/postgres' ./worker
```

Fargate runs take task definition names as images:
`-probe-image dbtest-probe -bench-image dbtest-bench`.

## Changes

- **Public IP changed.** The worker's connections to the database time out.
  Run `make aws-cidr` and `make aws-apply`.
- **A policy JSON changed.** Re-apply `aws-bootstrap` as the admin. IAM takes a few
  seconds to pick it up.
- **AccessDenied.** The message names the missing action. Add it to the
  matching JSON file and re-apply `aws-bootstrap`.

## Teardown

```sh
AWS_PROFILE=dbtest terraform -chdir=terraform/aws destroy
AWS_PROFILE=<admin> terraform -chdir=terraform/aws-bootstrap destroy
```

ECR repositories that still hold images are not deleted; empty them first.
Destroy `aws` before `aws-bootstrap`, which removes the `dbtest` role.

## Notes

- The policies match resources by the `dbtest` prefix and the
  `Project = dbtest` tag. Changing `var.project` in `aws/` needs the same
  change in both JSON files.
- The `dbtest` role covers what Terraform, `make push` and the worker call,
  not the AWS console. Use an admin login to browse.
- `terraform.tfvars` and state files are git-ignored. Keep account IDs out of
  committed files.

## GCP

```sh
cp terraform/gcp-bootstrap/terraform.tfvars.example terraform/gcp-bootstrap/terraform.tfvars
# edit project and trusted_principals ("user:<email>")
gcloud auth application-default login      # as a project owner
terraform -chdir=terraform/gcp-bootstrap init
terraform -chdir=terraform/gcp-bootstrap apply
```

Each user then logs in as the service account. The worker and Terraform pick
it up through Application Default Credentials:

```sh
gcloud auth application-default login \
  --impersonate-service-account=$(terraform -chdir=terraform/gcp-bootstrap output -raw service_account_email)
```

A permission is missing: the 403 names it. Add it to `dbtest-role.yaml` and
re-apply `gcp-bootstrap` as a project owner.

Teardown: `terraform -chdir=terraform/gcp-bootstrap destroy`. The APIs stay
enabled, and the custom role ID stays reserved for 7 days.
