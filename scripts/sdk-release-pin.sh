#!/usr/bin/env bash
# sdk-release-pin.sh — make a release commit's go.mod installable: drop the
# replace that points the app at this tree's Go SDK, and require the SDK at the
# commit the release is cut from instead (reliant#2824).
#
#   scripts/sdk-release-pin.sh <base-commit>
#
# Run in a reliant checkout whose tree is <base-commit> plus the release's own
# pin edits (the RC train's reliant pin commit: control-plane
# scripts/rc/rc.sh). It edits go.mod and go.sum and leaves committing to the
# caller.
#
# Why: on main, go.mod carries
#     replace github.com/reliant-labs/reliant/sdk/go => ./sdk/go
# so an SDK change lands in one PR (no pin bump, no post-squash re-pin). But
# `go install github.com/reliant-labs/reliant/cmd/reliant@<version>` refuses a
# module whose go.mod has any replace, and a consumer of this module ignores
# the replace and resolves the SDK by the require line. A release commit is
# what both of those read, so it requires the SDK at <base-commit> — whose
# sdk/go is byte-identical to the release tree's, which this script checks —
# and carries no replace.
#
# A go.mod with no SDK replace (a release cut from a commit before #2824) is
# left alone: it already requires the SDK by version. Exits non-zero, with the
# reason, on anything else. Needs git and go; resolves <base-commit> through
# the module proxy (GOPROXY), so <base-commit> must be pushed.
set -euo pipefail

SDK=github.com/reliant-labs/reliant/sdk/go

base="${1:-}"
[ -n "$base" ] || { echo "usage: scripts/sdk-release-pin.sh <base-commit>" >&2; exit 2; }
[ -f go.mod ] && [ -f sdk/go/go.mod ] || { echo "sdk-release-pin: run it in a reliant checkout (no go.mod or sdk/go/go.mod here)" >&2; exit 2; }
sha="$(git rev-parse --verify -q "${base}^{commit}")" || { echo "sdk-release-pin: $base is not a commit here" >&2; exit 2; }

replaced() { grep -Eq "^[[:space:]]*(replace[[:space:]]+)?${SDK//./\\.}([[:space:]]+v[^[:space:]]+)?[[:space:]]+=>" go.mod; }

if ! replaced; then
  echo "sdk-release-pin: go.mod has no replace for $SDK; nothing to do"
  exit 0
fi

# The SDK the release ships must be the SDK it was built and tested against.
if ! git diff --quiet "$sha" -- sdk/go || [ -n "$(git ls-files --others --exclude-standard -- sdk/go)" ]; then
  echo "sdk-release-pin: sdk/go here differs from $sha's, so requiring the SDK at $sha would ship a different SDK than this tree builds against:" >&2
  git diff --stat "$sha" -- sdk/go >&2 || true
  exit 1
fi

export GOWORK=off
go mod edit -dropreplace="$SDK"
GOFLAGS="${GOFLAGS:+$GOFLAGS }-mod=mod" go get "$SDK@$sha"
go mod tidy

if replaced; then
  echo "sdk-release-pin: go.mod still replaces $SDK after the edit" >&2
  exit 1
fi
got="$(go list -m -f '{{.Version}}' "$SDK")"
case "$got" in
  *-"${sha:0:12}") ;;
  *) echo "sdk-release-pin: go.mod requires $SDK $got, not a version of $sha" >&2; exit 1 ;;
esac
echo "sdk-release-pin: $SDK => $got (no replace)"
