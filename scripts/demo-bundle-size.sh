#!/usr/bin/env bash
# The forge demo bundle against its size budget (task demo:bundle-size, and
# the release gate's demo-size step): builds the demo exactly as forge ships
# it (frontends/web with WEB_BUILD_TARGET=demo, vite.config.ts's demo target),
# with DEMO_BUNDLE_REPORT, then frontends/web/scripts/demo/bundle-size.mjs
# prints the sizes as a markdown table and exits 1 over
# frontends/web/scripts/demo/bundle-budget.json. Arguments go to it:
#
#   scripts/demo-bundle-size.sh                     fail over budget (the release gate)
#   scripts/demo-bundle-size.sh --report-only       never fail (a pull request body's table)
#   scripts/demo-bundle-size.sh --json-out a.json   ... then, on the change: --before a.json
#
# It builds into a temporary directory, removed after, so frontends/web/dist-demo
# stays whatever a real build left there. Only the release gate gates on it;
# the merge gate builds the demo for its guard alone (dev-docs/demo-inventory.md,
# "Bundle size").
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
out=$(mktemp -d "${TMPDIR:-/tmp}/demo-bundle-size.XXXXXX")
trap 'rm -rf "$out"' EXIT
cd "$root/frontends/web"
WEB_BUILD_TARGET=demo DEMO_BUNDLE_REPORT="$out/report.json" npx vite build --logLevel warn --outDir "$out/dist" --emptyOutDir
node scripts/demo/bundle-size.mjs --report "$out/report.json" --dist "$out/dist" "$@"
