#!/bin/bash
# Validate migration naming/numbering for the Postgres migration tree.

set -euo pipefail

ROOT="internal/db/migrations"

check_dir() {
  local dir="$1"
  local label="$2"

  if [ ! -d "$dir" ]; then
    echo "❌ [$label] Migration directory not found: $dir"
    return 1
  fi

  shopt -s nullglob
  local files=("$dir"/*.sql)
  shopt -u nullglob

  if [ ${#files[@]} -eq 0 ]; then
    echo "❌ [$label] No migration files found in $dir"
    return 1
  fi

  echo "📁 [$label] Found ${#files[@]} migration files"

  declare -A seen=()
  local has_errors=0

  for file in "${files[@]}"; do
    local base
    base=$(basename "$file")

    if [[ ! "$base" =~ ^([0-9]+)_.+\.sql$ ]]; then
      echo "❌ [$label] Invalid migration filename format: $base"
      echo "   Expected: <numeric_version>_<description>.sql"
      has_errors=1
      continue
    fi

    local version="${BASH_REMATCH[1]}"
    local normalized=$((10#$version))

    if [[ -n "${seen[$normalized]:-}" ]]; then
      echo "❌ [$label] Duplicate migration version: $normalized"
      echo "   First:  ${seen[$normalized]}"
      echo "   Second: $base"
      has_errors=1
      continue
    fi

    seen[$normalized]="$base"
  done

  if [ $has_errors -ne 0 ]; then
    return 1
  fi

  echo "✅ [$label] Migration filenames and versions look good"
}

echo "🔍 Checking migration integrity..."

check_dir "$ROOT/postgres" "postgres"

echo "✅ Migration check complete"