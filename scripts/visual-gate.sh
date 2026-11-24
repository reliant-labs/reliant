#!/usr/bin/env bash
# shellcheck disable=SC2016 # the backticks in the reports are markdown, not command substitution
# visual-gate.sh — the visual baseline (frontends/web/e2e/visual) as a gate:
# compare only, never re-capture (owner, 2026-11-24; reliant#3446).
#
#   scripts/visual-gate.sh [--summary <file.md>]
#
# Who runs it:
#   - the merge queue's `visual` job (.github/workflows/merge-queue.yml), on the
#     merge group's own commit, when scripts/ci-lanes.sh says the group reaches
#     the lane (visual=true). The queue tests main plus the entries ahead, so a
#     baseline captured on a stale base, or a UI change pushed after the
#     capture, fails on the PR that causes it, before it lands.
#   - visual-nightly.yml, on main every night: the backstop for whatever moves
#     a surface from outside the lane's paths. It keeps the `visual:stale`
#     issue (.github/scripts/visual-stale-issue.sh).
#
# It installs frontends/web (frozen, skipped when node_modules is newer than
# the lockfile), pulls the pinned Playwright image while it runs
# `task test:web:visual -- --update-snapshots=none` (the build with the
# pinned RELEASE_VERSION, then the suite in the image: the same command a
# developer runs), and reads Playwright's JSON report (VISUAL_JSON):
#
#   exit 0  every capture matched its committed baseline
#   exit 1  captures differ: each is named, with how to re-capture
#   exit 3  the lane did not complete (install, build, docker, no report, a
#           report with errors outside a test, or one that ran no capture).
#           Never a pass: a lane that compared nothing proved nothing.
#
# The markdown report goes to the log, to --summary (the nightly's issue
# body), and to $GITHUB_STEP_SUMMARY when set. retries is 0 in
# playwright.visual.config.ts, and nothing here re-runs: a surface that differs
# on a commit whose UI did not move is nondeterministic, and is deleted with a
# `flake:removed` issue (frontends/web/e2e/visual/README.md), never retried.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

summary=""
while [ $# -gt 0 ]; do
  case "$1" in
    --summary) summary=${2:?--summary needs a file}; shift 2 ;;
    -h|--help) awk 'NR > 1 && /^set -euo pipefail/ { exit } NR > 1' "$0"; exit 0 ;;
    *) echo "visual-gate: unknown argument $1 (it takes no Playwright arguments: it compares every surface, and never re-captures)" >&2; exit 2 ;;
  esac
done

web=frontends/web
sha=$(git rev-parse HEAD)
results="$PWD/$web/visual-results.json"
rm -f "$results"

# The report (stdin) goes to the log, to --summary, and to the job summary.
report() {
  local body
  body=$(cat)
  printf '%s\n' "$body"
  if [ -n "$summary" ]; then printf '%s\n' "$body" >"$summary"; fi
  if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then printf '%s\n' "$body" >>"$GITHUB_STEP_SUMMARY"; fi
}

incomplete() {
  echo "::error title=Visual lane did not complete::$1"
  printf '### visual: the lane did not complete at `%s`\n\n%s\n\nNothing was compared, so this is not a pass. See the job log.\n' "$sha" "$1" | report
  exit 3
}

command -v jq >/dev/null 2>&1 || incomplete "jq is not installed, so the Playwright report cannot be read."
command -v task >/dev/null 2>&1 || incomplete "task is not installed: the lane runs \`task test:web:visual\`."

# The merge gate's frozen install (scripts/merge-gate.sh npm_install).
if ! [ "$web/node_modules/.modules.yaml" -nt "$web/pnpm-lock.yaml" ]; then
  pnpm install --dir "$web" --frozen-lockfile --ignore-scripts --config.confirmModulesPurge=false --reporter=silent ||
    incomplete "pnpm install --frozen-lockfile failed in $web."
fi

# The image (~1 GB) downloads while the renderer builds; the suite's own
# `docker run` waits for nothing once it is local. A failed pull is not
# fatal here: `docker run` pulls it again and fails loudly if it cannot.
pulllog=$(mktemp)
"$web/scripts/visual-docker.sh" --pull >"$pulllog" 2>&1 &
pull=$!

set +e
VISUAL_JSON="$results" task test:web:visual -- --update-snapshots=none
rc=$?
set -e
wait "$pull" || { echo "visual-gate: the background image pull failed (the suite pulled it itself):"; cat "$pulllog"; }
rm -f "$pulllog"

[ -s "$results" ] || incomplete "task test:web:visual exited $rc and wrote no Playwright report: the build, docker or the image failed before any surface was compared."
jq -e '.stats | type == "object"' "$results" >/dev/null 2>&1 || incomplete "the Playwright report $results is unreadable."

# Errors outside any test (a config that would not load, a web server that
# never came up) leave the tests unrun.
global=$(jq -r '[.errors[]? | (.message // "error") | split("\n")[0]] | join("; ")' "$results")
[ -z "$global" ] || incomplete "Playwright failed outside any test: $global"

ran=$(jq -r '.stats | (.expected // 0) + (.unexpected // 0) + (.flaky // 0)' "$results")
[ "$ran" -gt 0 ] || incomplete "the report ran no capture (every test skipped or none collected)."

# One row per capture that did not pass: the surface (the test title,
# "<surface> (<viewport>)"), the project, and what differed.
fails=$(jq -r '
  [.. | objects | select(has("specs")) | .specs[] | .title as $title | .tests[]
   | select(.status == "unexpected" or .status == "flaky")
   | ((.results | last | (.error.message // ([.errors[]?.message] | first) // "failed"))
       | gsub("\u001b\\[[0-9;]*m"; "")) as $msg
   | { surface: $title, project: .projectName,
       what: ( ($msg | capture("(?<n>[0-9]+) pixels \\(ratio (?<r>[0-9.]+)")? | "\(.n) pixels differ (ratio \(.r))")
               // ($msg | capture("(?<line>Expected an image[^\n]*)")? | .line)
               // ($msg | split("\n") | map(select(test("\\S"))) | first // "failed") ) } ]
  | sort_by(.surface, .project)[] | [.surface, .project, .what] | @tsv' "$results")

if [ "$rc" -eq 0 ] && [ -z "$fails" ]; then
  printf '### visual: all %s captures match the committed baselines at `%s`\n' "$ran" "$sha" | report
  exit 0
fi
[ -n "$fails" ] || incomplete "task test:web:visual exited $rc, but the report names no failed capture."

n=$(wc -l <<<"$fails")
echo "::error title=Visual baselines differ::$n of $ran captures differ from the committed baselines at $sha"
{
  printf '### visual: %s of %s captures differ from the committed baselines at `%s`\n\n' "$n" "$ran" "$sha"
  printf '| surface | project | difference |\n|---|---|---|\n'
  while IFS=$'\t' read -r s p w; do printf '| `%s` | %s | %s |\n' "$s" "$p" "${w//|/\\|}"; done <<<"$fails"
  cat <<'EOF'

The committed baselines in `frontends/web/e2e/visual/__screenshots__/` do not
match this commit's UI. In the merge queue this commit is main plus the
entries ahead of the PR, so either the PR moved these surfaces without
re-capturing them, or it captured them on a base that lacked a UI change
already on main or ahead of it in the queue. Re-capture on the current base:

```sh
git fetch origin && git rebase origin/main
task test:web:visual -- --update-snapshots
git add frontends/web/e2e/visual/__screenshots__ && git commit -m "test(web): re-capture visual baselines"
```

Then list the moved surfaces in the PR body (README.md, "Before and after in
a pull request"). The expected, actual and diff images are in the run's
`visual-results` artifact (`test-results-visual/`, `playwright-report-visual/`).

A surface that differs on a commit whose UI did not change is
nondeterministic: delete it from `SURFACES` with a `flake:removed` issue.
Never re-run the lane to green.
EOF
} | report
exit 1
