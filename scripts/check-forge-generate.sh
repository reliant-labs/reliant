#!/usr/bin/env bash
# Fail when the pinned `forge generate` changes the tree.
#
# ── Why this exists ──────────────────────────────────────────────────────
#
# forge projects code from reliant's protos, contract.go interfaces,
# migrations and forge.yaml: the generated mocks, gen/forge_descriptor.json,
# pkg/middleware/procedures_gen.go, the per-service test harnesses, the
# observability files, .forge/hashes.json. All of it is committed. A change to
# an input that is not followed by the pinned `forge generate` leaves main with
# output that no longer matches its inputs, and the next contributor to run
# generate inherits someone else's diff (or a mock that no longer compiles).
# `make generate-all` does not run forge, so CI's "Generated code is up to
# date" job cannot see this; this check does.
#
# ── What it judges ───────────────────────────────────────────────────────
#
# The CHANGE generate makes, not the cleanliness of the tree: every dirty or
# untracked file is hashed before and after, so it runs as well on a working
# tree with your uncommitted edits (the local gate) as on a clean checkout
# (CI). Any file generate creates, rewrites or deletes is named and fails it.
#
# ── Which forge ──────────────────────────────────────────────────────────
#
# Exactly forge.yaml's forge_version: `go run .../cmd/forge@<version>`, which
# the Go build cache keeps linked after the first run. FORGE=<binary> uses a
# prebuilt one instead (CI caches it), and is refused unless it reports that
# same version. go.mod must require the same forge, or the forge that lints
# and the forge that generates are two different programs.
#
# Run it through `make forge-generate-check`, which supplies the pinned buf
# ($BUF), protoc-gen-es and Go toolchain, exactly as for `make generate-all`.
#
# Usage:
#   scripts/check-forge-generate.sh            # human-readable
#   scripts/check-forge-generate.sh --github   # also ::error annotations for CI
set -euo pipefail

github=0
if [ "${1:-}" = "--github" ]; then github=1; fi

cd "$(git rev-parse --show-toplevel)"

fail() {
  if [ "${github}" = 1 ]; then echo "::error title=$1::$2"; fi
  echo "error: $1" >&2
  echo "$2" >&2
  exit 1
}

pin="$(sed -n 's/^forge_version:[[:space:]]*//p' forge.yaml | head -1)"
[ -n "${pin}" ] || fail "no forge_version in forge.yaml" "forge.yaml must pin the forge that generates this repo."
mod="$(sed -n 's|^[[:space:]]*github.com/reliant-labs/forge[[:space:]]\{1,\}\(v[^[:space:]]*\).*|\1|p' go.mod | head -1)"
if [ "${mod}" != "${pin}" ]; then
  fail "forge.yaml and go.mod pin different forges" "forge.yaml forge_version is ${pin}, go.mod requires ${mod:-nothing}. Re-pin both with scripts/pin-forge.sh."
fi

if [ -n "${FORGE:-}" ]; then
  got="$("${FORGE}" --version 2>/dev/null || true)"
  case "${got}" in
    *" ${pin}") ;;
    *) fail "FORGE is not the pinned forge" "${FORGE} reports '${got}', forge.yaml pins ${pin}." ;;
  esac
  forge=("${FORGE}")
else
  # GOPRIVATE: a fresh pseudo-version is not on proxy.golang.org yet
  # (scripts/pin-forge.sh). GOFLAGS is reset to just -trimpath because `go run
  # pkg@version` refuses a repo-level -mod setting, and an empty one would drop
  # -trimpath under a toolchain switched by go.mod (see the Makefile's GOFLAGS).
  forge=(env GOPRIVATE="${GOPRIVATE:-github.com/reliant-labs/*}" GOFLAGS=-trimpath go run "github.com/reliant-labs/forge/cmd/forge@${pin}")
fi

# buf is resolved from PATH by forge; BUF names the Makefile's pinned build.
if [ -n "${BUF:-}" ]; then
  bufdir="$(mktemp -d)"
  trap 'rm -rf "${bufdir}"' EXIT
  ln -s "$(cd "$(dirname "${BUF}")" && pwd)/$(basename "${BUF}")" "${bufdir}/buf"
  export PATH="${bufdir}:${PATH}"
fi

# A check must not rewrite the clone's git config (forge's merge driver).
export FORGE_NO_MERGE_DRIVER="${FORGE_NO_MERGE_DRIVER:-1}"

# One line per dirty or untracked path: "<path>\t<blob hash, or 'deleted'>".
snapshot() {
  git ls-files -z --modified --others --exclude-standard | sort -zu |
    while IFS= read -r -d '' f; do
      if [ -e "${f}" ] || [ -L "${f}" ]; then
        printf '%s\t%s\n' "${f}" "$(git hash-object --no-filters -- "${f}")"
      else
        printf '%s\tdeleted\n' "${f}"
      fi
    done
}

before="$(snapshot)"
echo "==> forge generate (forge ${pin})"
"${forge[@]}" generate
after="$(snapshot)"

changed=()
# An empty snapshot is one blank line, which diff reports too: skip it.
while IFS= read -r f; do if [ -n "${f}" ]; then changed+=("${f}"); fi; done < <(
  diff <(printf '%s\n' "${before}") <(printf '%s\n' "${after}") |
    awk -F'\t' '/^[<>] / { print substr($1, 3) }' | sort -u
)
if [ "${#changed[@]}" = 0 ]; then
  echo "forge generate is a no-op: the generated code matches its inputs."
  exit 0
fi

echo "forge generate changed ${#changed[@]} file(s):" >&2
printf '  %s\n' "${changed[@]}" >&2
if [ "${github}" = 1 ]; then
  for f in "${changed[@]}"; do echo "::error file=${f},title=Stale forge output::forge generate changes ${f}"; done
  echo "--- diff (tracked files) ---"
  git --no-pager diff -- "${changed[@]}" || true
fi
fail "forge generate is not a no-op" "Run the pinned forge generate (make forge-generate-check, or: go run github.com/reliant-labs/forge/cmd/forge@${pin} generate) and commit what it writes, in the change that caused it."
