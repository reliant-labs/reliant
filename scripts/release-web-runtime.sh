#!/usr/bin/env bash
# release-web-runtime.sh — tag a release of the @reliantlabs/forge-web-runtime
# npm package.
#
# The npm twin of scripts/release-pkg.sh, and it exists for the same reason:
# minting a release by hand is easy to get subtly wrong, and the failure is
# only visible downstream, after a scaffolded project can no longer install.
#
# What it validates, in order:
#   1. the version shape (vX.Y.Z, optional prerelease);
#   2. a CLEAN web-runtime/ tree — a dirty tree means the tarball would not
#      match the tag;
#   3. that the tag does not already exist;
#   4. that package.json's `version` agrees with the requested version;
#   5. that the package BUILDS, TYPECHECKS and its tests PASS;
#   6. that `npm pack` contains exactly the declared `files` surface — the
#      check that catches a build which silently emitted nothing, which is
#      indistinguishable from a healthy publish until an install fails;
#   7. that forge's `webRuntimePublishedRange` tracks this version, so a
#      released forge cannot scaffold a range that does not exist yet.
#
# Usage:
#   scripts/release-web-runtime.sh [--dry-run] vX.Y.Z
#
# After a real (non-dry-run) invocation ONE step remains:
#
#   git push origin web-runtime/vX.Y.Z
#
# That push IS the release. .github/workflows/release-web-runtime.yml triggers
# on web-runtime/v*, re-runs these validations on a clean checkout, and
# publishes. There is no `npm publish` to type.
#
# This script used to end by asking for a hand-typed publish, on the reasoning
# that an irreversible act should not be automated. That did not hold up: we
# already publish a notarized Electron app from CI on the same trigger shape,
# which is more irreversible and more credential-sensitive than a scoped npm
# publish. And the manual step had a cost — v0.3.0 was tagged but never
# published, so its devlog module existed in the source and in forge's
# templates while the registry copy lacked it, and every scaffold built
# against the registry failed to typecheck.
#
# The script keeps its value as the LOCAL gate: it runs the checks before the
# tag exists, which is where a failure costs nothing. CI repeats them on a
# clean checkout, because a maintainer's node_modules cannot establish what a
# stranger downloads.
set -euo pipefail

DRY_RUN=0
VERSION=""

usage() {
  echo "usage: $0 [--dry-run] vX.Y.Z" >&2
  exit 2
}

while [ $# -gt 0 ]; do
  case "$1" in
    --dry-run) DRY_RUN=1; shift ;;
    -h|--help) usage ;;
    -*)        echo "error: unknown flag $1" >&2; usage ;;
    *)
      [ -z "$VERSION" ] || usage
      VERSION="$1"; shift ;;
  esac
done

[ -n "$VERSION" ] || usage

# ── 1. Version shape ────────────────────────────────────────────────
# Canonical semver with optional prerelease. Reject the tag-prefixed form
# early — users habitually paste `web-runtime/v1.2.3` back in.
if ! echo "$VERSION" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'; then
  echo "error: version must look like vX.Y.Z (got: $VERSION)" >&2
  echo "hint: pass the bare version; the script adds the web-runtime/ tag prefix itself." >&2
  exit 1
fi
TAG="web-runtime/$VERSION"
BARE="${VERSION#v}"

REPO_ROOT="$(git rev-parse --show-toplevel)"
cd "$REPO_ROOT"

PKG_DIR="web-runtime"
if [ ! -f "$PKG_DIR/package.json" ]; then
  echo "error: $PKG_DIR/package.json not found (run from the forge repo)" >&2
  exit 1
fi

# ── 2. Clean tree ───────────────────────────────────────────────────
# Scoped to web-runtime/: unrelated work elsewhere in the repo must not block
# a release, but an uncommitted change to the package itself means the tag
# would not describe the bytes that get published.
if [ -n "$(git status --porcelain -- "$PKG_DIR")" ]; then
  echo "error: $PKG_DIR/ has uncommitted changes — commit or stash them first." >&2
  git status --short -- "$PKG_DIR" >&2
  exit 1
fi

# ── 3. Tag must not exist ───────────────────────────────────────────
if git rev-parse -q --verify "refs/tags/$TAG" >/dev/null; then
  echo "error: tag $TAG already exists." >&2
  exit 1
fi

# ── 4. package.json agrees ──────────────────────────────────────────
# The tag and the manifest must name the same version, or `npm publish` ships
# something the tag does not describe.
PKG_VERSION="$(node -p "require('./$PKG_DIR/package.json').version")"
if [ "$PKG_VERSION" != "$BARE" ]; then
  echo "error: $PKG_DIR/package.json says version $PKG_VERSION, but you asked for $BARE." >&2
  echo "hint: bump the version in package.json (and commit) before tagging." >&2
  exit 1
fi

PKG_NAME="$(node -p "require('./$PKG_DIR/package.json').name")"

# ── 5. Build, typecheck, test ───────────────────────────────────────
echo "==> building $PKG_NAME@$BARE"
( cd "$PKG_DIR" && npm run build )
echo "==> typechecking"
( cd "$PKG_DIR" && npm run typecheck )
echo "==> testing"
( cd "$PKG_DIR" && npm test )

# ── 6. The tarball actually contains the package ────────────────────
# `files` in package.json declares dist/ + interceptors/ + README. A build
# that emitted nothing still packs "successfully" — with no dist/ — and the
# breakage only surfaces when a consumer imports the barrel and gets a module
# resolution error. Assert the entry point is really in there.
echo "==> verifying pack contents"
PACK_LIST="$(cd "$PKG_DIR" && npm pack --dry-run --json)"
for required in "dist/index.js" "dist/index.d.ts"; do
  if ! echo "$PACK_LIST" | grep -q "\"$required\""; then
    echo "error: tarball is missing $required — did the build emit dist/?" >&2
    exit 1
  fi
done

# ── 7. forge's scaffold range tracks this version ───────────────────
# A released forge writes webRuntimePublishedRange into every scaffolded
# frontend's package.json. If it lags the version being released, forge
# scaffolds a range the registry cannot satisfy for the newest features —
# and the failure lands on a user, not here. (TestWebRuntimePublishedRange-
# TracksPackage enforces the same invariant in CI; this is the release-time
# copy so the check runs at the moment it matters.)
RANGE_FILE="internal/generator/frontend_webruntime.go"
DECLARED_RANGE="$(grep -oE 'webRuntimePublishedRange = "\^[0-9]+\.[0-9]+\.[0-9]+"' "$RANGE_FILE" | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' || true)"
RELEASE_MINOR="$(echo "$BARE" | cut -d. -f1-2)"
DECLARED_MINOR="$(echo "$DECLARED_RANGE" | cut -d. -f1-2)"
if [ "$RELEASE_MINOR" != "$DECLARED_MINOR" ]; then
  echo "error: $RANGE_FILE declares ^$DECLARED_RANGE but you are releasing $BARE." >&2
  echo "hint: update webRuntimePublishedRange to ^$BARE and commit before tagging." >&2
  exit 1
fi

# ── 8. Tag ──────────────────────────────────────────────────────────
if [ "$DRY_RUN" -eq 1 ]; then
  echo
  echo "DRY RUN — every validation passed. Would create tag: $TAG"
  echo "Then: git push origin $TAG   (that push publishes, via .github/workflows/release-web-runtime.yml)"
  exit 0
fi

git tag -a "$TAG" -m "$PKG_NAME $VERSION"
echo
echo "Created tag $TAG."
echo
echo "Remaining step — pushing the tag IS the release:"
echo "  git push origin $TAG"
echo
echo "That triggers .github/workflows/release-web-runtime.yml, which re-runs these"
echo "validations on a clean checkout and publishes to npm. Watch it with:"
echo "  gh run watch \$(gh run list --workflow 'Release web-runtime' --limit 1 --json databaseId -q '.[0].databaseId')"
