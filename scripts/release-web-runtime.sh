#!/usr/bin/env bash
# release-web-runtime.sh — publish @reliantlabs/forge-web-runtime for a forge
# commit, when (and only when) what it ships changed since its last release.
#
# THE VERSION IS COMPUTED, NOT DECLARED IN A PULL REQUEST. It used to be: a PR
# bumped web-runtime/package.json (and forge's scaffold floor with it), and a
# maintainer was meant to tag web-runtime/vX.Y.Z afterwards. Nobody had to, so
# often nobody did — 0.12.1 and 0.13.0 were declared and never published, and
# forge#1616, #1694 and #1696 sat merged for over a week. Worse, the floor
# (webRuntimePublishedRange) then named a version npm did not have, and
# `forge generate` RAISES every consumer to that floor and refreshes its
# lockfile: every project pinning that forge commit failed to install.
#
# So the release computes the version from the commit, the way forge's own
# VERSION is written by its release and never by a PR:
#
#   previous  the highest final web-runtime/vX.Y.Z tag whose commit is the
#             commit or an ancestor of it.
#   changed   whether what the package SHIPS differs between that tag's commit
#             and this one: web-runtime/ minus its tests, and minus the
#             manifests' own version fields (the release bookkeeping syncs
#             those; a sync is not a change). Unchanged → nothing is published
#             and the answer is `previous`. npm gets no no-op versions.
#   bump      caret-preserving, so a released forge's `^X.Y.Z` floor picks up
#             every compatible runtime published after it: before 1.0 a
#             breaking change (a `type!:` title or a `BREAKING CHANGE:` footer
#             on a commit touching web-runtime/) bumps the minor and anything
#             else the patch; from 1.0, breaking → major, feat → minor, else
#             patch.
#   declared  web-runtime/package.json's version at the commit is a FLOOR: the
#             result is never below it, even when nothing else changed. A PR
#             never has to touch it; one may raise it to declare a version the
#             titles do not say.
#   range     the scaffold floor at the commit (webRuntimePublishedRange) must
#             be a caret range the result satisfies, because forge's release
#             ships the generator of exactly this commit. So the PR making a
#             breaking runtime change raises it (and package.json's version)
#             to the new minor (major from 1.0); the release refuses otherwise.
#
# After a publish, forge's release bookkeeping (scripts/release-forge.sh, the
# release/vX.Y.Z pull request) writes the version into web-runtime/package.json
# and webRuntimePublishedRange, so the floor only ever names a published
# version.
#
# WHO RUNS IT. Every forge release, before its tag: scripts/release-forge.sh
# calls it for the commit it releases and stops the release if it fails. And
# the release train (control-plane .reliant/workflows/rc-train.yaml) calls it at
# each CUT, for the forge commit F the set pins, before reliant and the control
# plane are pinned to F: their pin regenerates code from F that may need F's
# runtime, so that runtime must be on npm first. At the forge release that
# follows, this script finds the version already published (`present`).
#
# HOW IT PUBLISHES. It pushes the tag web-runtime/vX.Y.Z on the commit; that
# push runs .github/workflows/release-web-runtime.yml, which re-validates on a
# clean checkout and publishes with npm trusted publishing (OIDC; no token
# exists anywhere). This script then waits for that run and for the registry to
# serve the version (scripts/verify-npm-published.sh), and exits 0 only then.
# A tag whose publish failed is reported as a failure on every re-run until npm
# serves it: re-run the workflow, never move the tag.
#
# No release lock: the tag push is its own compare-and-swap (git refuses a tag
# origin already has), so two publishers of one version cannot both win, and
# the loser fails loudly. A caller holding forge's release lock
# (release-forge.sh) is unaffected.
#
# Usage:
#   scripts/release-web-runtime.sh [--plan | --dry-run] [--commit <sha>]
#                                  [--repo <dir>] [--branch <name>]
#
#   --plan        compute only: no worktree, no build, no tag. Cheap.
#   --dry-run     compute, then validate in a private worktree (npm ci,
#                 typecheck, test, build, pack contents); tag nothing.
#   --commit SHA  the forge commit to publish for (default: origin/<branch>).
#   --repo DIR    the forge checkout to work from (default: the enclosing one).
#                 Only its object store and origin are used: its working tree,
#                 refs/remotes and branches are never touched.
#   --branch NAME the default commit's branch (default: main).
#
# Output: progress on stderr; the LAST stdout line is always one JSON object
# (schema web-runtime-release/v1), also when the run fails:
#   {"schema":"web-runtime-release/v1","package":"@reliantlabs/forge-web-runtime",
#    "commit":"<sha>","changed":true,"previous":"0.13.1",
#    "previous_tag":"web-runtime/v0.13.1","version":"0.13.2",
#    "tag":"web-runtime/v0.13.2","bump":"patch","state":"published","detail":"…"}
# state: planned | validated | published | present | tagged | failed
#   (`tagged`: pushed to an origin that is not on github.com, where no
#   workflow publishes; fixtures only).
#
# Environment (tests):
#   FORGE_RELEASE_GH              the gh binary (default: gh)
#   FORGE_RELEASE_GITHUB_REPO     owner/name whose workflow publishes (default:
#                                 derived from origin; empty for a local origin)
#   FORGE_WEB_RUNTIME_RUN_TIMEOUT seconds to wait for the publish run (1800)
#   FORGE_WEB_RUNTIME_POLL        seconds between polls of it (15)
#   NPM_VIEW_CMD, NPM_VERIFY_*    passed through to verify-npm-published.sh
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# Everything human-readable goes to stderr; fd 3 (the real stdout) carries only
# the JSON result line.
exec 3>&1 1>&2

MODE=release
COMMIT=""
REPO_ROOT=""
BRANCH=main

usage() {
  echo "usage: $0 [--plan | --dry-run] [--commit <sha>] [--repo <dir>] [--branch <name>]" >&2
  echo "       the version is computed from the commit; there is no version argument" >&2
  exit 2
}

PKG_DIR="web-runtime"
TAG_PREFIX="web-runtime/v"
WORKFLOW_FILE="release-web-runtime.yml"
FLOOR_FILE="internal/generator/frontend_webruntime.go"

# ── The result, printed as the last stdout line whatever happens ─────
R_PACKAGE="" R_COMMIT="" R_CHANGED=false R_PREVIOUS="" R_PREVIOUS_TAG=""
R_VERSION="" R_TAG="" R_BUMP=none R_STATE="" R_DETAIL=""
json_str() {
  local s="$1"
  s="${s//\\/\\\\}"; s="${s//\"/\\\"}"; s="${s//$'\n'/\\n}"; s="${s//$'\r'/}"; s="${s//$'\t'/ }"
  printf '"%s"' "$s"
}
emit_result() {
  printf >&3 '{"schema":"web-runtime-release/v1","package":%s,"commit":%s,"changed":%s,"previous":%s,"previous_tag":%s,"version":%s,"tag":%s,"bump":%s,"state":%s,"detail":%s}\n' \
    "$(json_str "$R_PACKAGE")" "$(json_str "$R_COMMIT")" "$R_CHANGED" "$(json_str "$R_PREVIOUS")" \
    "$(json_str "$R_PREVIOUS_TAG")" "$(json_str "$R_VERSION")" "$(json_str "$R_TAG")" \
    "$(json_str "$R_BUMP")" "$(json_str "$R_STATE")" "$(json_str "$R_DETAIL")"
}
EMITTED=0
# finish <state> <detail> [exit]: report and stop.
finish() {
  R_STATE="$1"; R_DETAIL="$2"
  [ "$R_STATE" = failed ] && echo "error: $R_DETAIL" >&2 || echo "→ $R_STATE: $R_DETAIL" >&2
  emit_result; EMITTED=1
  exit "${3:-0}"
}
fail() { finish failed "$1" 1; }

NS="refs/web-runtime-release/$$"
WORKTREE_PARENT=""
cleanup() {
  local rc=$?
  if [ -n "${REPO_ROOT:-}" ] && [ -d "$REPO_ROOT" ]; then
    git -C "$REPO_ROOT" for-each-ref --format='delete %(refname)' "$NS/" 2>/dev/null | git -C "$REPO_ROOT" update-ref --stdin 2>/dev/null || true
    if [ -n "$WORKTREE_PARENT" ]; then
      git -C "$REPO_ROOT" worktree remove --force "$WORKTREE_PARENT/web-runtime-release" >/dev/null 2>&1 || true
    fi
  fi
  [ -z "$WORKTREE_PARENT" ] || rm -rf "$WORKTREE_PARENT"
  if [ "$EMITTED" = 0 ]; then
    R_STATE=failed; R_DETAIL="release-web-runtime.sh stopped unexpectedly (exit $rc); its stderr says where"
    emit_result
    [ "$rc" -ne 0 ] || rc=1
  fi
  exit "$rc"
}
trap cleanup EXIT

while [ $# -gt 0 ]; do
  case "$1" in
    --plan)    MODE=plan; shift ;;
    --dry-run) MODE=dry-run; shift ;;
    --commit)  [ $# -ge 2 ] || usage; COMMIT="$2"; shift 2 ;;
    --repo)    [ $# -ge 2 ] || usage; REPO_ROOT="$2"; shift 2 ;;
    --branch)  [ $# -ge 2 ] || usage; BRANCH="$2"; shift 2 ;;
    -h|--help) usage ;;
    -*)        echo "error: unknown flag $1" >&2; usage ;;
    *)
      echo "error: unexpected argument '$1': the web-runtime version is computed from the commit, never passed in" >&2
      echo "hint: to declare a version the commit titles do not say, raise web-runtime/package.json's version in a pull request" >&2
      exit 2 ;;
  esac
done

# ── The repository and its origin ────────────────────────────────────
[ -n "$REPO_ROOT" ] || REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null)" || fail "not inside a git checkout (pass --repo)"
REPO_ROOT="$(cd "$REPO_ROOT" && pwd)"
cd "$REPO_ROOT"
ORIGIN_URL="$(git config --get remote.origin.url 2>/dev/null || true)"
[ -n "$ORIGIN_URL" ] || fail "$REPO_ROOT has no origin remote"
if [ -n "${FORGE_RELEASE_GITHUB_REPO+set}" ]; then
  GITHUB_REPO="$FORGE_RELEASE_GITHUB_REPO"
else
  GITHUB_REPO=""
  case "$ORIGIN_URL" in
    git@github.com:*) GITHUB_REPO="${ORIGIN_URL#git@github.com:}" ;;
    https://github.com/*) GITHUB_REPO="${ORIGIN_URL#https://github.com/}" ;;
    ssh://git@github.com/*) GITHUB_REPO="${ORIGIN_URL#ssh://git@github.com/}" ;;
  esac
  GITHUB_REPO="${GITHUB_REPO%.git}"
fi
GH_BIN="${FORGE_RELEASE_GH:-gh}"

# Into a private namespace, never refs/remotes or refs/tags: the checkout is
# shared, and another agent may be fetching into those at this moment.
fetch_ok=0
for try in 1 2; do
  if git fetch -q --no-tags --refmap= origin "+refs/heads/$BRANCH:$NS/branch" "+refs/tags/${TAG_PREFIX}*:$NS/tags/${TAG_PREFIX}*"; then
    fetch_ok=1; break
  fi
  [ "$try" = 2 ] || sleep 3
done
[ "$fetch_ok" = 1 ] || fail "cannot fetch $BRANCH and the ${TAG_PREFIX}* tags from origin"

if [ -z "$COMMIT" ]; then
  COMMIT="$(git rev-parse "$NS/branch")"
elif ! git rev-parse -q --verify "$COMMIT^{commit}" >/dev/null; then
  # A commit this checkout has not fetched yet (a release/ branch head).
  git fetch -q --no-tags --refmap= origin "$COMMIT" 2>/dev/null || true
fi
COMMIT="$(git rev-parse -q --verify "$COMMIT^{commit}")" || fail "commit $COMMIT does not exist here or on origin"
R_COMMIT="$COMMIT"

# ── What the commit declares ─────────────────────────────────────────
MANIFEST="$(git show "$COMMIT:$PKG_DIR/package.json" 2>/dev/null)" || fail "$COMMIT has no $PKG_DIR/package.json"
# Top-level fields are the two-space-indented lines (prettier's layout, held by
# the pre-commit hook), so no JSON parser is needed to read them.
manifest_field() { sed -n "s/^  \"$1\": \"\([^\"]*\)\".*/\1/p" <<<"$MANIFEST" | head -n 1; }
R_PACKAGE="$(manifest_field name)"
DECLARED="$(manifest_field version)"
[ -n "$R_PACKAGE" ] || fail "$PKG_DIR/package.json at $COMMIT has no top-level \"name\""
SEMVER_RE='^[0-9]+\.[0-9]+\.[0-9]+$'
[[ "$DECLARED" =~ $SEMVER_RE ]] || fail "$PKG_DIR/package.json at $COMMIT has version '$DECLARED', not X.Y.Z"

ver_ge() { [ "$(printf '%s\n%s\n' "$1" "$2" | sort -V | tail -n 1)" = "$1" ]; } # $1 >= $2
ver_max() { printf '%s\n%s\n' "$1" "$2" | sort -V | tail -n 1; }

# ── The previous release: the highest final tag at or below the commit ──
PREV_VERSION="" PREV_COMMIT="" AT_COMMIT=""
while read -r ref target; do
  v="${ref#"$NS/tags/$TAG_PREFIX"}"
  [[ "$v" =~ $SEMVER_RE ]] || continue
  [ "$target" != "$COMMIT" ] || AT_COMMIT="$(ver_max "${AT_COMMIT:-0.0.0}" "$v")"
  git merge-base --is-ancestor "$target" "$COMMIT" 2>/dev/null || continue
  if [ -z "$PREV_VERSION" ] || ! ver_ge "$PREV_VERSION" "$v"; then
    PREV_VERSION="$v"; PREV_COMMIT="$target"
  fi
done < <(git for-each-ref --format='%(refname) %(*objectname)%(objectname)' "$NS/tags/" | awk '{print $1, substr($2, 1, 40)}')
# (%(*objectname) is the peeled commit of an annotated tag, empty for a
# lightweight one, so the first 40 hex characters are the commit either way.)

if [ -n "$PREV_VERSION" ]; then
  R_PREVIOUS="$PREV_VERSION"; R_PREVIOUS_TAG="$TAG_PREFIX$PREV_VERSION"
fi

# shipped_differs <from> <to>: does the package's shipped content differ? Tests
# are not shipped, and the manifests' own version fields are bookkeeping.
TEST_EXCLUDES=(
  ":(exclude,glob)$PKG_DIR/**/*.test.ts" ":(exclude,glob)$PKG_DIR/**/*.test.tsx"
  ":(exclude,glob)$PKG_DIR/**/*.test.mjs" ":(exclude,glob)$PKG_DIR/**/__tests__/**"
)
strip_versions() {
  # package.json: the top-level "version". package-lock.json: that one and the
  # root package's (the first `"version"` inside `"": {`).
  awk '
    !top && /^  "version": / { top = 1; next }
    /^    "": \{/ { inroot = 1 }
    inroot && !rootv && /^      "version": / { rootv = 1; next }
    inroot && /^    \}/ { inroot = 0 }
    { print }'
}
shipped_differs() {
  local from="$1" to="$2" f
  if ! git diff --quiet "$from" "$to" -- "$PKG_DIR" "${TEST_EXCLUDES[@]}" \
      ":(exclude)$PKG_DIR/package.json" ":(exclude)$PKG_DIR/package-lock.json"; then
    return 0
  fi
  for f in package.json package-lock.json; do
    if [ "$(git show "$from:$PKG_DIR/$f" 2>/dev/null | strip_versions)" != "$(git show "$to:$PKG_DIR/$f" 2>/dev/null | strip_versions)" ]; then
      return 0
    fi
  done
  return 1
}

# ── Decide ────────────────────────────────────────────────────────────
if [ -n "$AT_COMMIT" ]; then
  # Released from exactly this commit already: a re-run.
  R_VERSION="$AT_COMMIT"; R_TAG="$TAG_PREFIX$AT_COMMIT"; R_CHANGED=false; R_BUMP=none
elif [ -z "$PREV_VERSION" ]; then
  # Never released below this commit: the first release is the declared one.
  R_VERSION="$DECLARED"; R_TAG="$TAG_PREFIX$DECLARED"; R_CHANGED=true; R_BUMP=declared
elif ! shipped_differs "$PREV_COMMIT" "$COMMIT"; then
  if ver_ge "$DECLARED" "$PREV_VERSION" && [ "$DECLARED" != "$PREV_VERSION" ]; then
    # Nothing shipped changed, but a pull request raised the manifest's version:
    # the declared version is a floor here too, so it is published as declared
    # (lowering it would make the bookkeeping rewrite the manifest back down).
    R_VERSION="$DECLARED"; R_TAG="$TAG_PREFIX$DECLARED"; R_CHANGED=true; R_BUMP=declared
  else
    R_VERSION="$PREV_VERSION"; R_TAG="$TAG_PREFIX$PREV_VERSION"; R_CHANGED=false; R_BUMP=none
  fi
else
  R_CHANGED=true
  breaking=0 feature=0
  BREAKING_RE='^[a-z]+(\([^)]*\))?!:'
  FEAT_RE='^feat(\([^)]*\))?!?:'
  while IFS= read -r -d $'\x1e' entry; do
    entry="${entry#$'\n'}"
    subject="${entry%%$'\x1f'*}"; body="${entry#*$'\x1f'}"
    [ -n "$subject" ] || continue
    if [[ "$subject" =~ $BREAKING_RE ]] || grep -Eq '^BREAKING[ -]CHANGE:' <<<"$body"; then breaking=1; fi
    if [[ "$subject" =~ $FEAT_RE ]]; then feature=1; fi
  done < <(git log --format='%s%x1f%b%x1e' "$PREV_COMMIT..$COMMIT" -- "$PKG_DIR" "${TEST_EXCLUDES[@]}")
  IFS=. read -r maj min pat <<<"$PREV_VERSION"
  if [ "$maj" -eq 0 ]; then
    if [ "$breaking" = 1 ]; then R_BUMP=minor; NEXT="0.$((min + 1)).0"; else R_BUMP=patch; NEXT="0.$min.$((pat + 1))"; fi
  elif [ "$breaking" = 1 ]; then R_BUMP=major; NEXT="$((maj + 1)).0.0"
  elif [ "$feature" = 1 ]; then R_BUMP=minor; NEXT="$maj.$((min + 1)).0"
  else R_BUMP=patch; NEXT="$maj.$min.$((pat + 1))"
  fi
  if [ "$DECLARED" != "$NEXT" ] && ver_ge "$DECLARED" "$NEXT"; then
    NEXT="$DECLARED"; R_BUMP=declared
  fi
  R_VERSION="$NEXT"; R_TAG="$TAG_PREFIX$NEXT"
fi

# The scaffold floor at the commit must be an npm caret range that the version
# this commit releases SATISFIES. forge's release builds its archives and tags
# this very commit, so its generator scaffolds exactly this range: a floor
# above the version names one npm lacks, and a floor in an older caret window
# (^0.13.0 for a breaking 0.14.0, ^1.x for 2.0.0) resolves only runtimes
# older than the one the generated code needs. The bookkeeping that later
# raises the floor does not reach the released binary, so refuse it here,
# before anything is published.
caret_satisfies() { # $1 version, $2 the X.Y.Z of a ^X.Y.Z range
  local vmaj vmin vpat fmaj fmin fpat
  IFS=. read -r vmaj vmin vpat <<<"$1"; IFS=. read -r fmaj fmin fpat <<<"$2"
  ver_ge "$1" "$2" || return 1
  if [ "$fmaj" -gt 0 ]; then [ "$vmaj" -eq "$fmaj" ]
  elif [ "$fmin" -gt 0 ]; then [ "$vmaj" -eq 0 ] && [ "$vmin" -eq "$fmin" ]
  else [ "$1" = "$2" ]
  fi
}
FLOOR="$(git show "$COMMIT:$FLOOR_FILE" 2>/dev/null | sed -n 's/^const webRuntimePublishedRange = "^\([0-9.]*\)".*/\1/p' | head -n 1)"
if [ -n "$FLOOR" ] && ! [[ "$FLOOR" =~ $SEMVER_RE ]]; then
  fail "$FLOOR_FILE at $COMMIT declares ^$FLOOR, not ^X.Y.Z"
fi
if [ -n "$FLOOR" ] && ! ver_ge "$R_VERSION" "$FLOOR"; then
  fail "$FLOOR_FILE at $COMMIT declares ^$FLOOR, above the $R_VERSION this commit releases; lower it, or raise $PKG_DIR/package.json's version to declare $FLOOR"
fi
if [ -n "$FLOOR" ] && ! caret_satisfies "$R_VERSION" "$FLOOR"; then
  fail "$FLOOR_FILE at $COMMIT declares ^$FLOOR, which does not admit the $R_VERSION this commit releases (a ${R_BUMP} bump): a forge released from it would scaffold a range that cannot install the runtime its generated code needs. In the pull request that makes the breaking change, raise webRuntimePublishedRange to ^$R_VERSION and $PKG_DIR/package.json's version to $R_VERSION"
fi

# ── Superseded: a LATER commit already released a version at least this one ──
# The release train publishes for the forge commit a set pins (F), which can be older than
# a commit the runtime was already released from by hand or by an earlier set: v1.17.0-rc.1
# computed 0.13.14 for f389bc3008c7 while web-runtime/v0.13.14 was on db0e90cdd, a
# descendant, and the cut failed ("that version is taken"), stalling every set until the
# pin moved. A release from a descendant ships everything this commit's runtime does, so
# when one is at or above the computed version and inside this commit's caret floor, it
# IS this commit's runtime: nothing is published (npm's latest never moves back), and in
# a release npm must serve it.
verify_served_version() { bash "$SCRIPT_DIR/verify-npm-published.sh" --name "$R_PACKAGE" --version "$1"; }
SUPER_VERSION="" SUPER_COMMIT=""
if [ "$R_CHANGED" = true ]; then
  while read -r ref target; do
    v="${ref#"$NS/tags/$TAG_PREFIX"}"
    [[ "$v" =~ $SEMVER_RE ]] || continue
    [ "$target" != "$COMMIT" ] || continue
    git merge-base --is-ancestor "$COMMIT" "$target" 2>/dev/null || continue
    ver_ge "$v" "$R_VERSION" || continue
    caret_satisfies "$v" "${FLOOR:-$R_VERSION}" || continue
    if [ -z "$SUPER_VERSION" ] || ! ver_ge "$SUPER_VERSION" "$v"; then SUPER_VERSION="$v"; SUPER_COMMIT="$target"; fi
  done < <(git for-each-ref --format='%(refname) %(*objectname)%(objectname)' "$NS/tags/" | awk '{print $1, substr($2, 1, 40)}')
fi
if [ -n "$SUPER_VERSION" ]; then
  R_VERSION="$SUPER_VERSION"; R_TAG="$TAG_PREFIX$SUPER_VERSION"; R_CHANGED=false; R_BUMP=none
  why="$R_TAG was released from ${SUPER_COMMIT:0:12}, which descends from ${COMMIT:0:12} and ships all of its runtime"
  echo "→ $R_PACKAGE: superseded: $why; nothing to publish"
  [ "$MODE" = release ] || finish "$([ "$MODE" = plan ] && echo planned || echo validated)" "superseded: $why"
  if [ -z "$GITHUB_REPO" ]; then finish present "superseded: $why (origin is not on github.com, so npm is not asked)"; fi
  if verify_served_version "$SUPER_VERSION"; then finish present "superseded: $why; npm serves $R_PACKAGE@$SUPER_VERSION"; fi
  fail "superseded by $R_TAG, but npm does not serve $R_PACKAGE@$SUPER_VERSION: its publish failed. Re-run that tag's $WORKFLOW_FILE run; never move the tag"
fi

if [ "$R_CHANGED" = true ]; then
  echo "→ $R_PACKAGE: $R_VERSION (${R_BUMP} over ${R_PREVIOUS:-<no release>}), from $(git log -1 --format='%h %s' "$COMMIT")"
else
  echo "→ $R_PACKAGE: unchanged since $R_TAG; nothing to publish"
fi

if [ "$MODE" = plan ]; then
  if [ "$R_CHANGED" = true ]; then finish planned "would publish $R_VERSION from ${COMMIT:0:12}"; fi
  finish planned "$R_VERSION is current at ${COMMIT:0:12}; nothing to publish"
fi

# verify_served <publish-reported-success: 0|1>: the registry serves R_VERSION.
verify_served() {
  local flag=()
  [ "$1" = 1 ] && flag=(--publish-reported-success)
  bash "$SCRIPT_DIR/verify-npm-published.sh" --name "$R_PACKAGE" --version "$R_VERSION" "${flag[@]}"
}

# ── Already released: prove npm has it ───────────────────────────────
if [ "$R_CHANGED" = false ]; then
  [ "$MODE" = release ] || finish validated "$R_VERSION is already released at $R_TAG; nothing to publish"
  if [ -z "$GITHUB_REPO" ]; then finish present "$R_TAG exists (origin is not on github.com, so npm is not asked)"; fi
  if verify_served 0; then finish present "npm serves $R_PACKAGE@$R_VERSION ($R_TAG)"; fi
  fail "$R_TAG exists but npm does not serve $R_PACKAGE@$R_VERSION: its publish failed. Re-run the tag's $WORKFLOW_FILE run (gh run list -R ${GITHUB_REPO:-reliant-labs/forge} --workflow $WORKFLOW_FILE --branch $R_TAG); never move the tag"
fi

# A version tag elsewhere means the version is taken; a version is immutable.
if TAKEN="$(git rev-parse -q --verify "$NS/tags/$R_TAG^{commit}")"; then
  [ "$TAKEN" = "$COMMIT" ] || fail "$R_TAG already exists at ${TAKEN:0:12}, not ${COMMIT:0:12}: that version is taken. Raise $PKG_DIR/package.json's version past it in a pull request"
fi

# ── Validate the exact bytes in a private worktree ───────────────────
# The workflow repeats every check on a clean runner; doing them here first
# means a broken package fails before a tag exists, where failing costs nothing.
WORKTREE_PARENT="$(mktemp -d)"
WT="$WORKTREE_PARENT/web-runtime-release"
git worktree add -q --detach "$WT" "$COMMIT" || fail "cannot make a worktree at $COMMIT"
# Every step is checked by hand: `set -e` does not apply inside a function or
# subshell whose status is tested (`|| fail`), so a failing step would
# otherwise be skipped over.
validate_package() {
  local pack required
  cd "$WT/$PKG_DIR" || return 1
  echo "→ npm ci"; npm ci --no-audit --no-fund || return 1
  # The version the workflow will publish: it sets it from the tag the same way.
  npm pkg set version="$R_VERSION" || return 1
  echo "→ typecheck"; npm run typecheck || return 1
  echo "→ test"; npm test || return 1
  echo "→ build"; npm run build || return 1
  echo "→ pack contents"
  pack="$(npm pack --dry-run --json)" || return 1
  for required in dist/index.js dist/index.d.ts LICENSE NOTICE; do
    grep -q "\"$required\"" <<<"$pack" || { echo "error: the tarball is missing $required" >&2; return 1; }
  done
}
( validate_package ) || fail "$R_PACKAGE@$R_VERSION does not validate at ${COMMIT:0:12} (npm ci / typecheck / test / build / pack; the output is above)"

[ "$MODE" = release ] || finish validated "would tag $R_TAG at ${COMMIT:0:12} and publish $R_PACKAGE@$R_VERSION"

# ── Tag and push: the push IS the publication ─────────────────────────
# Tags are made by the Reliant bot, whichever machine cuts them (owner,
# 2026-11-03): its identity is set per command, never in git config.
RELEASE_GIT_NAME='reliant-labs[bot]'
RELEASE_GIT_EMAIL='186876276+reliant-labs[bot]@users.noreply.github.com'
if [ -z "${TAKEN:-}" ]; then
  if git rev-parse -q --verify "refs/tags/$R_TAG" >/dev/null; then
    [ "$(git rev-parse "refs/tags/$R_TAG^{commit}")" = "$COMMIT" ] || fail "a local tag $R_TAG names another commit than ${COMMIT:0:12}; it was never pushed (origin has none). Delete it (git tag -d $R_TAG) and re-run"
  else
    GIT_AUTHOR_NAME="$RELEASE_GIT_NAME" GIT_AUTHOR_EMAIL="$RELEASE_GIT_EMAIL" \
      GIT_COMMITTER_NAME="$RELEASE_GIT_NAME" GIT_COMMITTER_EMAIL="$RELEASE_GIT_EMAIL" \
      git tag -a "$R_TAG" -m "$R_PACKAGE v$R_VERSION" "$COMMIT"
  fi
  if ! git push -q origin "refs/tags/$R_TAG"; then
    git tag -d "$R_TAG" >/dev/null 2>&1 || true
    if [ -n "$(git ls-remote --tags origin "refs/tags/$R_TAG" 2>/dev/null)" ]; then
      fail "origin already has $R_TAG: another release took $R_VERSION first. Re-run: the next run computes from it"
    fi
    fail "origin refused the push of $R_TAG (git's reason is above); nothing was published"
  fi
  echo "→ pushed $R_TAG at ${COMMIT:0:12}"
fi

if [ -z "$GITHUB_REPO" ]; then
  finish tagged "pushed $R_TAG; origin is not on github.com, so no workflow publishes it"
fi

# ── Wait for the publish run, then for the registry ──────────────────
run_ok=0
if command -v "$GH_BIN" >/dev/null 2>&1; then
  deadline=$(( $(date +%s) + ${FORGE_WEB_RUNTIME_RUN_TIMEOUT:-1800} ))
  echo "→ waiting for $WORKFLOW_FILE on $R_TAG"
  run=""
  while :; do
    run="$("$GH_BIN" run list -R "$GITHUB_REPO" --workflow "$WORKFLOW_FILE" --branch "$R_TAG" --limit 1 \
      --json databaseId,status,conclusion,url --jq '.[0] | select(. != null) | "\(.databaseId) \(.status) \(.conclusion) \(.url)"' 2>/dev/null || true)"
    read -r run_id run_status run_conclusion run_url <<<"${run:-- - - -}"
    if [ "$run_status" = completed ]; then
      [ "$run_conclusion" = success ] || fail "the publish run for $R_TAG ended $run_conclusion: $run_url. $R_TAG is pushed and npm does not have $R_VERSION: fix the cause, then re-run that run (gh run rerun $run_id -R $GITHUB_REPO); never move the tag"
      run_ok=1; echo "  published by $run_url"; break
    fi
    if [ "$(date +%s)" -ge "$deadline" ]; then
      echo "  note: no finished $WORKFLOW_FILE run for $R_TAG yet (${run_url:-none seen}); asking the registry directly"
      break
    fi
    sleep "${FORGE_WEB_RUNTIME_POLL:-15}"
  done
else
  echo "  note: $GH_BIN is not installed, so the publish run cannot be watched; asking the registry directly"
fi

if verify_served "$run_ok"; then
  finish published "npm serves $R_PACKAGE@$R_VERSION ($R_TAG at ${COMMIT:0:12})"
fi
fail "$R_TAG is pushed but npm does not serve $R_PACKAGE@$R_VERSION (see above). Re-run its $WORKFLOW_FILE run; never move the tag"
