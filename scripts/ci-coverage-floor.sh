#!/usr/bin/env bash
# Hold main's total Go test coverage to the checked-in floor.
#
#   scripts/ci-coverage-floor.sh [<runs-dir>] [<floor-file>]
#
# ci.yml's `coverage-floor` job runs it on a push to main, after the full lanes
# finish: it downloads their test-runs-* artifacts into <runs-dir> (default
# .forge/tests/runs) and runs this. Exit 1 when the total is below the number
# in <floor-file> (default .github/coverage-floor), 2 when there is nothing to
# measure or the lanes' profiles are incomplete.
#
# WHAT IS MEASURED. The union of main's full-mode lanes, each recorded as
# <runs-dir>/<run>/<lane>/cover.out: `*-race` (scripts/ci-test-full-shard.sh,
# the unit and internal/cli shards) and `integration*` (ci.yml's
# -tags=integration steps). Every lane measures the
# packages it tests with their own tests (no -coverpkg), so the total is what
# `go test -cover ./...` reports in full mode, merged. A block several lanes
# ran counts once, covered if any lane hit it: profiles are unioned by block,
# never concatenated or overwritten. The -short tier and the flake gate are
# subsets of these lanes and are left out. The guards (internal/deadcodeguard,
# removalguard, tierguard, writeguard) are recorded by no lane, so their ~1,000
# statements are in neither side of the fraction.
#
# WHAT IS NOT COUNTED. Generated code only: `*.pb.go`, anything under a `gen/`
# or `testdata/` directory, and any file whose header (before its `package`
# clause) carries Go's `// Code generated ... DO NOT EDIT.` line. A `*_gen.go`
# name alone excludes nothing: most of forge's are hand-written generators
# (internal/codegen, internal/generator), which coverage must count.
#
# THE FLOOR ONLY GOES UP. A pull request that raises coverage raises the
# number in .github/coverage-floor in the same or a following pull request;
# none lowers it. When the total clears the floor by more than half a point,
# this prints the number to raise it to. The floor sits a tenth of a point
# under a measured total: a few tests take timing-dependent branches, and a
# run's total moves by a few hundredths with them.
set -euo pipefail

runs=${1:-.forge/tests/runs}
floor_file=${2:-.github/coverage-floor}
summary=${GITHUB_STEP_SUMMARY:-/dev/null}
cd "$(git rev-parse --show-toplevel)"

floor=$(grep -v '^[[:space:]]*#' "$floor_file" | tr -d '[:space:]')
if ! [[ "$floor" =~ ^[0-9]+(\.[0-9]+)?$ ]]; then
  echo "::error::$floor_file holds no coverage floor (got '$floor'): one number, a percentage, e.g. 81.40"
  exit 2
fi

# COMPLETE OR NOTHING. COVERAGE_LANES names every full lane the run must
# have recorded and how many profiles each (`unit-race:2 cli-race:3
# integration:1 ...`; ci.yml's coverage-floor step sets it, and
# internal/ciguard derives it from ci.yml's matrices and integration steps).
# A total over some lanes is not the total: a missing low-coverage shard would
# RAISE it and pass a floor the run did not clear. So a lane with fewer
# profiles than named, an empty profile (one that holds no block), or a
# full lane the list does not name fails as unmeasured (exit 2), before any
# arithmetic.
if [[ -z "${COVERAGE_LANES:-}" ]]; then
  echo "::error::COVERAGE_LANES is not set: name every full lane and its profile count (e.g. 'unit-race:2 cli-race:3 integration:1')"
  exit 2
fi
declare -A want=() have=()
for entry in $COVERAGE_LANES; do
  if ! [[ "$entry" =~ ^([A-Za-z0-9_.-]+):([1-9][0-9]*)$ ]]; then
    echo "::error::COVERAGE_LANES entry '$entry' is not <lane>:<count>"
    exit 2
  fi
  want[${BASH_REMATCH[1]}]=${BASH_REMATCH[2]}
done

profiles=()
incomplete=()
while IFS= read -r p; do
  lane=$(basename "$(dirname "$p")")
  if ! grep -qv '^mode:' "$p" 2>/dev/null; then
    incomplete+=("$lane: $p holds no coverage block")
    continue
  fi
  have[$lane]=$((${have[$lane]:-0} + 1))
  profiles+=("$p")
done < <(find "$runs" -name cover.out -type f 2>/dev/null | awk -F/ '$(NF-1) ~ /-race$|^integration/' | sort)
for lane in "${!want[@]}"; do
  if ((${have[$lane]:-0} != ${want[$lane]})); then
    incomplete+=("$lane: ${have[$lane]:-0} profile(s), want ${want[$lane]}")
  fi
done
for lane in "${!have[@]}"; do
  [[ -n "${want[$lane]:-}" ]] || incomplete+=("$lane: recorded, but COVERAGE_LANES does not name it")
done
if [[ ${#incomplete[@]} -gt 0 ]]; then
  echo "::error::the full lanes' coverage under $runs is incomplete, so the total is unmeasured (never judged on part of the suite):"
  printf '  %s\n' "${incomplete[@]}" | sort
  exit 2
fi

module=$(awk '$1 == "module" { print $2; exit }' go.mod)

# The generated files, as the import-path file names profiles use.
generated=$(mktemp)
trap 'rm -f "$generated"' EXIT
git ls-files -z -- '*.go' | xargs -0 awk -v module="$module" '
  FNR == 1 {
    skip = 0
    if (FILENAME ~ /\.pb\.go$/ || FILENAME ~ /(^|\/)(gen|testdata)\//) { print module "/" FILENAME; skip = 1; nextfile }
  }
  /^package / { nextfile }
  /^\/\/ Code generated .* DO NOT EDIT\.$/ { print module "/" FILENAME; nextfile }
' >"$generated"

report=$(awk -v floor="$floor" '
  FNR == NR { gen[$0] = 1; next }
  /^mode:/ || NF < 3 { next }
  {
    # file.go:12.3,14.5 <statements> <count>
    key = $1
    file = substr(key, 1, index(key, ":") - 1)
    if (file in gen) { next }
    if (!(key in stmts)) {
      stmts[key] = $2
      pkg = file; sub(/\/[^\/]*$/, "", pkg)
      pkgof[key] = pkg
    }
    if ($3 > 0) { hit[key] = 1 }
  }
  END {
    for (k in stmts) {
      total += stmts[k]; ptotal[pkgof[k]] += stmts[k]
      if (k in hit) { covered += stmts[k]; pcov[pkgof[k]] += stmts[k] }
    }
    if (total == 0) { print "none"; exit }
    printf "total %.2f %d %d %d\n", 100 * covered / total, covered, total, length(ptotal)
    for (p in ptotal) printf "pkg %.1f %d %s\n", 100 * pcov[p] / ptotal[p], ptotal[p], p
  }
' "$generated" "${profiles[@]}")

read -r _ pct covered total npkgs < <(grep '^total ' <<<"$report") || {
  echo "::error::the profiles under $runs hold no statements outside generated code"
  exit 2
}

echo "coverage: ${pct}% of ${total} statements (${covered} covered) in ${npkgs} packages, from ${#profiles[@]} profiles; floor ${floor}%"
printf '  %s\n' "${profiles[@]}"
{
  echo "### Go coverage: ${pct}% (floor ${floor}%)"
  echo
  echo "${covered} of ${total} statements in ${npkgs} packages, full mode, own tests, generated code excluded."
  echo
  echo "<details><summary>the 25 least-covered packages</summary>"
  echo
  echo "| % | statements | package |"
  echo "| ---: | ---: | --- |"
  grep '^pkg ' <<<"$report" | sort -k2,2n -k3,3nr | awk -v m="$module/" 'NR <= 25 { sub("^" m, "", $4); printf "| %s | %s | %s |\n", $2, $3, $4 }'
  echo
  echo "</details>"
} >>"$summary"

if awk -v p="$pct" -v f="$floor" 'BEGIN { exit !(p + 0 < f + 0) }'; then
  echo "::error title=Coverage below the floor::total Go coverage is ${pct}%, below the ${floor}% floor in ${floor_file}. A change since the floor was set removed tests or added untested code: add tests for what it changed (forge test coverage --diff origin/main lists the lines no test reached). Never lower the floor."
  exit 1
fi
if awk -v p="$pct" -v f="$floor" 'BEGIN { exit !(p - f > 0.5) }'; then
  raise=$(awk -v p="$pct" 'BEGIN { printf "%.2f", p - 0.1 }')
  echo "::notice title=Raise the coverage floor::total Go coverage is ${pct}%, over the ${floor}% floor by more than half a point: raise ${floor_file} to ${raise}"
fi
