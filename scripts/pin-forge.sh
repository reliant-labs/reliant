#!/usr/bin/env bash
# Pin github.com/reliant-labs/forge (both modules) to a sibling COMMIT.
#
# ── Why a commit and not a tag ────────────────────────────────────────────
#
# Pre-launch, prod is the only real environment and every bug we chase has
# only ever appeared there. A tag-per-fix costs three tags, three CI cycles
# and three merges before a one-line forge change reaches prod.
#
# A commit pin costs one `go get`. Measured, not estimated: resolving forge
# at a commit took 2.065s, while a freshly pushed TAG took ~20 MINUTES to
# become usable because proxy.golang.org had not ingested it yet. GOPRIVATE
# (github.com/reliant-labs/*) is what makes the 2s number real — it routes
# our own modules straight at GitHub instead of the proxy.
#
# What lands in go.mod is a Go PSEUDO-VERSION, e.g.
#   v0.1.15-0.20260910160302-c195f15386be
# That is a real, resolvable, checksummed module version — NOT a `replace`.
# The distinction matters: a replace applies in every build mode, GOWORK=off
# does not disable it, and it breaks the container build. See
# internal/buildmode/forge_replace_guard_test.go, which stays untouched and
# passes on a pseudo-version.
#
# This is not novel here either: forge.yaml already carried the pseudo-version
# v0.1.12-0.20260903044854-14d84353e34b in September and the tooling handled it.
#
# ── This is a MODE, and it ends at launch ────────────────────────────────
#
# At launch we go back to tag pins. docs/pinning.md records the mode, why,
# and the exact command to reverse it. Read that before "fixing" a
# pseudo-version you find in go.mod.
#
# Usage:
#   scripts/pin-forge.sh              # pin forge's current origin/main
#   scripts/pin-forge.sh <sha|ref>    # pin an explicit commit, branch or tag

set -euo pipefail

FORGE_REMOTE="https://github.com/reliant-labs/forge"
FORGE_MOD="github.com/reliant-labs/forge"
PKG_MOD="github.com/reliant-labs/forge/pkg"

# GOPRIVATE is the whole performance story (see header). Set it here rather
# than relying on the caller's environment so the script is fast on a fresh
# machine and in CI, not only on a laptop that happens to export it.
export GOPRIVATE="${GOPRIVATE:-github.com/reliant-labs/*}"
# `go get` needs -mod=mod to write go.mod; a repo-level -mod=readonly in
# GOFLAGS would otherwise make this fail with a confusing "updates to go.mod
# needed" instead of doing the one thing it exists to do.
export GOFLAGS="${GOFLAGS:-} -mod=mod"

cd "$(git rev-parse --show-toplevel)"

require_line() { sed -n "s|^[[:space:]]*$1[[:space:]]\{1,\}\(v[^[:space:]]*\).*|\1|p" go.mod | head -1; }

before_forge="$(require_line "$FORGE_MOD")"
before_pkg="$(require_line "$PKG_MOD")"

# ── Read the REMOTE, never a local checkout ──────────────────────────────
# A sibling checkout on this machine is routinely on someone else's branch,
# mid-rebase, or simply days stale. `git ls-remote` is authoritative and
# costs well under a second, so there is no reason to trust local state.
target="${1:-}"
if [ -z "${target}" ]; then
  sha="$(git ls-remote "${FORGE_REMOTE}" refs/heads/main | cut -f1)"
  if [ -z "${sha}" ]; then
    echo "error: could not read refs/heads/main from ${FORGE_REMOTE}" >&2
    exit 1
  fi
  source_desc="origin/main"
else
  # Resolve whatever was handed in (branch, tag or sha) against the remote so
  # the pin is always a concrete commit. A ref that does not resolve remotely
  # is passed through unchanged and `go get` reports it.
  sha="$(git ls-remote "${FORGE_REMOTE}" "${target}" | cut -f1 | head -1)"
  if [ -z "${sha}" ]; then sha="${target}"; fi
  source_desc="${target}"
fi

echo "==> pinning forge to ${source_desc} (${sha})"

# ── Both modules, one `go get` ───────────────────────────────────────────
# forge and forge/pkg are tagged from a SINGLE commit and are expected to be
# the same source vintage. Pinning them separately compiles today and skews
# the moment one moves without the other, which is a failure that surfaces as
# an inscrutable type error in a consumer months later. One command, one sha.
go get "${FORGE_MOD}@${sha}" "${PKG_MOD}@${sha}"
go mod tidy

after_forge="$(require_line "$FORGE_MOD")"
after_pkg="$(require_line "$PKG_MOD")"

# ── Verify the pin SURVIVED tidy ─────────────────────────────────────────
# This is not ceremony. A pseudo-version derives its numeric prefix from the
# last tag REACHABLE from that commit, so it can sort BELOW a release tag —
# reliant's own main resolves to v1.7.8-0.… while v1.7.12 exists. Go picks
# the maximum required version, so if anything in the graph still requires a
# higher tag, MVS silently restores it and the pin you asked for is not the
# pin you got. Fail loudly instead of shipping the wrong forge.
short="${sha:0:12}"
for pair in "${FORGE_MOD}:${after_forge}" "${PKG_MOD}:${after_pkg}"; do
  mod="${pair%%:*}"; got="${pair#*:}"
  if [ -z "${got}" ]; then
    echo "error: ${mod} is not required in go.mod after tidy" >&2
    exit 1
  fi
  # Two acceptable outcomes. A commit pin records a pseudo-version CONTAINING
  # the sha. An explicit TAG argument records the tag itself — we resolved it
  # to a sha above only to have something concrete to report, and the tag
  # never contains that sha, so it must be matched separately or the tag path
  # (which still has to work after launch) false-fails.
  if [ "${got}" = "${target}" ]; then continue; fi
  case "${got}" in
    *"${short}"*) : ;;
    *)
      echo "error: ${mod} resolved to ${got}, which is neither the requested ref (${source_desc}) nor a pseudo-version containing ${short}." >&2
      echo "       Minimum version selection kept a higher version from the module graph." >&2
      echo "       A pseudo-version can sort BELOW an existing release tag — see docs/pinning.md." >&2
      exit 1
      ;;
  esac
done

if [ "${after_forge}" != "${after_pkg}" ]; then
  echo "error: forge (${after_forge}) and forge/pkg (${after_pkg}) disagree — they are tagged from one commit and must match" >&2
  exit 1
fi

# ── Prove it against the CONSUMER's view ─────────────────────────────────
# GOWORK=off is the only local signal that sees what CI sees. With go.work
# active, forge resolves from ../forge on disk, so code calling an API that
# exists only in an unreleased sibling checkout builds perfectly here and
# fails every CI job. This repo has a go.work; without this line the script
# would "verify" nothing.
echo "==> GOWORK=off go build ./..."
GOWORK=off go build ./...

echo
echo "pinned forge -> ${source_desc} (${sha})"
printf '  %-40s %s -> %s\n' "${FORGE_MOD}" "${before_forge:-<absent>}" "${after_forge}"
printf '  %-40s %s -> %s\n' "${PKG_MOD}" "${before_pkg:-<absent>}" "${after_pkg}"
echo
echo "Files changed: go.mod, go.sum. Review and commit them yourself —"
echo "this script deliberately does not commit (see docs/pinning.md)."
