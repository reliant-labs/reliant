#!/usr/bin/env bash
# hotfix-bump.sh — the version bump of a HOTFIX release, which control-plane's rc-train
# hotfix lane cuts from the reliant commit prod runs plus fix PRs cherry-picked onto it
# (control-plane docs/pr-pipeline.md, "Hotfix lane"). Run in that checkout:
#
#   scripts/hotfix-bump.sh <vX.Y.Z>
#
# It writes what scripts/release.sh writes for a release, and nothing else, so the commit
# the caller makes of it is release metadata alone (release-gate.sh check carries the
# picks' hotfix gate to it):
#   electron/package.json and package-lock.json   the version X.Y.Z (only that field)
#   docs/data/releases/vX.Y.Z.yaml                 the notes: the commits since the newest
#                                                  final tag HEAD contains (tools/releasenotes)
#   docs/changelog.mdx                             regenerated from the notes
# It commits nothing (the caller commits, with its RC-Set trailer), refuses a version that
# is not above every final tag HEAD contains, and prints the files it changed.
set -euo pipefail
die() { printf 'hotfix-bump: %s\n' "$*" >&2; exit 1; }
v="${1:-}"
[[ "$v" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "usage: scripts/hotfix-bump.sh <vX.Y.Z>"
cd "$(git rev-parse --show-toplevel)"
[ -z "$(git status --porcelain)" ] || die "the checkout is not clean"
since="$(git tag --merged HEAD -l 'v*' | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -V | tail -n 1 || true)"
[ -n "$since" ] || die "HEAD contains no final vX.Y.Z tag to count the notes from"
[ "$since" != "$v" ] && [ "$(printf '%s\n%s\n' "$since" "$v" | sort -V | tail -n 1)" = "$v" ] \
  || die "$v is not above $since, a release HEAD already contains"
git rev-parse -q --verify "refs/tags/$v" >/dev/null && die "$v is already a tag"
[ ! -e "docs/data/releases/$v.yaml" ] || die "docs/data/releases/$v.yaml already exists"

# The version field alone, as `npm version --no-git-tag-version` sets it, without npm.
node -e '
  const fs = require("fs"), v = process.argv[1];
  for (const f of ["electron/package.json", "electron/package-lock.json"]) {
    if (!fs.existsSync(f)) continue;
    const j = JSON.parse(fs.readFileSync(f, "utf8"));
    j.version = v;
    if (j.packages && j.packages[""]) j.packages[""].version = v;
    fs.writeFileSync(f, JSON.stringify(j, null, 2) + "\n");
  }' "${v#v}"
go run ./tools/releasenotes --version "$v" --since "$since" --to HEAD --out "docs/data/releases/$v.yaml" >&2 \
  || die "tools/releasenotes could not write the notes"
go run ./tools/docgen/changelog/... docs/data/releases docs/changelog.mdx >&2 \
  || die "tools/docgen/changelog could not regenerate docs/changelog.mdx"
git status --porcelain | awk '{print $2}'
