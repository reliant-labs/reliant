#!/usr/bin/env bash
# ci-lanes.sh — which of CI's path-selected lanes a change reaches.
#
#   scripts/ci-lanes.sh --base <rev>          the change <rev>..HEAD (a pull request)
#   scripts/ci-lanes.sh --full [--base <rev>] every lane (main, nightly, dispatch)
#
# Prints one `<lane>=true|false` line per lane, the form $GITHUB_OUTPUT takes.
# `task ci:lanes` runs it; .github/workflows/ci.yml's `lanes` job is a thin
# wrapper over that task.
#
# These are the lanes NEITHER gate covers: the merge gate (scripts/merge-gate.sh)
# and the release gate (scripts/release-gate.sh) decide every Go, vitest,
# electron-unit, lint and codegen check themselves, through tools/test-affected.
# What is left is what a gate cannot run on a developer's machine cheaply or at
# all, selected by path here:
#
#   web          the release's renderer build + verify-bundle (task build:web:verify)
#   playwright   Playwright against the built renderer (task test:web:e2e)
#   electron     the electron-builder package smoke (task build:electron:smoke)
#   cp_contract  proto-vendor/controlplane/ is exactly the export of the
#                control-plane commit its SOURCE declares, and that commit is on
#                control-plane main (task contract:controlplane:check; needs the
#                private sibling). It judges a change made HERE, so even under
#                --full it runs only when <rev>..HEAD touches what it checks.
#                control-plane moving ahead is not a change here:
#                .github/workflows/cp-contract-drift.yml reports it.
#
# Fails in the safe direction: an empty diff, a diff that cannot be computed,
# or a change to CI itself (.github/workflows/ci.yml, .github/actions/, this
# script, the Taskfile) turns every lane on (cp_contract: see above).
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

base="" full=false
while [ $# -gt 0 ]; do
  case "$1" in
    --base) base=${2:?--base needs a rev}; shift 2 ;;
    --full) full=true; shift ;;
    -h|--help) sed -n '2,26p' "$0"; exit 0 ;;
    *) echo "ci-lanes: unknown argument $1" >&2; exit 2 ;;
  esac
done
[ "$full" = true ] || [ -n "$base" ] || { echo "ci-lanes: pass --base <rev> or --full" >&2; exit 2; }

changed="" diffed=false
if [ -n "$base" ]; then
  if changed=$(git diff --name-only "$base" HEAD 2>/dev/null); then
    diffed=true
  else
    echo "ci-lanes: cannot diff $base..HEAD; running every lane" >&2
    full=true
  fi
fi
if [ "$full" = false ]; then
  if [ -z "$changed" ]; then
    echo "ci-lanes: empty diff; running every lane" >&2
    full=true
  elif grep -qE '^(\.github/workflows/ci\.yml|\.github/actions/.*|scripts/ci-lanes\.sh|Taskfile\.yml)$' <<< "$changed"; then
    echo "ci-lanes: this change edits CI itself; running every lane" >&2
    full=true
  fi
fi

# What can break the vendored control-plane contract from this side: the
# vendored tree, its sync script, or CI itself. Without a diff (a nightly, a
# dispatch, a diff that failed), nothing here changed it.
cp_contract=false
if [ "$diffed" = true ] && grep -qE '^(proto-vendor/controlplane/|\.github/scripts/sync-controlplane-proto\.mjs$|\.github/workflows/ci\.yml$|\.github/actions/|scripts/ci-lanes\.sh$|Taskfile\.yml$)' <<< "$changed"; then
  cp_contract=true
fi

web=true playwright=true electron=true
if [ "$full" = false ]; then
  web=false playwright=false electron=false
  # The web build (lint, typecheck, vite build, verify-bundle).
  if grep -qE '^(frontends/web/|package(-lock)?\.json$|electron/release\.config\.json$|\.github/scripts/verify-bundle\.mjs$)' <<< "$changed"; then web=true; fi
  # Playwright drives the built renderer: any web change but unit tests and
  # their fixtures.
  if grep -E '^(frontends/web/|package(-lock)?\.json$|electron/release\.config\.json$)' <<< "$changed" \
    | grep -qvE '(/__tests__/|\.test\.tsx?$|^frontends/web/README\.md$)'; then playwright=true; fi
  # The electron package: electron itself, or what it bundles the renderer
  # with. Renderer source is the web lane's to build.
  if grep -qE '^(electron/|package(-lock)?\.json$|frontends/web/package(-lock)?\.json$|frontends/web/vite\.config\.ts$)' <<< "$changed"; then electron=true; fi
fi

echo "web=$web"
echo "playwright=$playwright"
echo "electron=$electron"
echo "cp_contract=$cp_contract"
