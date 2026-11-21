#!/usr/bin/env bash
# The renderer's release build, for the merge gate (reliant#2850): the
# production `vite build` and bundle verification that the release gate's
# webbuild step (task build:web:verify) and release.yml's prepare job run,
# without their eslint and the `tsc -b` the merge gate's typecheck phase
# already runs. vitest and tsc never bundle, so a dependency that imports what
# only the bundler refuses (#2829's supabase-js importing `node:module`) passes
# both and fails here.
#
# It builds into a temporary directory, removed after: the bundle is the
# check, not an output, and frontends/web/dist stays whatever a real build
# left there. NODE_ENV=production is on the build command only, as in the
# Taskfile: npm reads it too, and on an install it would skip devDependencies.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
out=$(mktemp -d "${TMPDIR:-/tmp}/web-release-build.XXXXXX")
trap 'rm -rf "$out"' EXIT
cd "$root/frontends/web"
NODE_ENV=production npx vite build --logLevel warn --outDir "$out/dist" --emptyOutDir
node "$root/.github/scripts/verify-bundle.mjs" --dist "$out/dist"
