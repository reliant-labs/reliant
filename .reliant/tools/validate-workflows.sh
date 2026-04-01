#!/bin/bash
# Workflow Validation Helper
#
# Validates all workflow YAML files in .reliant/workflows
#
# Usage:
#   ./.reliant/tools/validate-workflows.sh           # Basic validation
#   ./.reliant/tools/validate-workflows.sh -verbose  # Detailed output
#   ./.reliant/tools/validate-workflows.sh -dir PATH # Custom directory

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

cd "$PROJECT_ROOT"

exec go run ./cmd/reliant/ workflow validate "$@"