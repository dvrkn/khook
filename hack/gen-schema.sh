#!/usr/bin/env bash
# Regenerate the committed JSON Schema artifact from the spec types.
# internal/cli/schema_test.go fails when the artifact is stale.
set -euo pipefail
cd "$(dirname "$0")/.."
mkdir -p schema/v1
go run ./cmd/khook schema > schema/v1/khook.json
echo "wrote schema/v1/khook.json"
