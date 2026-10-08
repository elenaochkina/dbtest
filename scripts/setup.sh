#!/usr/bin/env bash
# Writes config.mk, the local registry settings `make push` reads.
#
# Not implemented yet. To add:
# - REGISTRY from `terraform -chdir=terraform/aws output -raw ecr_registry`.
# - The region from AWS_REGION, defaulting to the terraform region.
# - REGISTRY_LOGIN as an `aws ecr get-login-password | docker login` line,
#   using AWS_PROFILE or a profile the script asks for.
# - Ask before overwriting an existing config.mk.
# - Delete config.mk.example and point the Makefile's errors at this script.
set -euo pipefail

echo "scripts/setup.sh is not implemented yet" >&2
exit 1
