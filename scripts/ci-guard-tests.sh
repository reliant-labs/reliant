#!/usr/bin/env bash
# The guards' whole-repo checks the merge tier can afford, in full mode, when
# a change touches what they read. ci.yml's test-short job runs it in the merge
# tier, after the -short tier, and scripts/ci-premerge.sh (`task ci`) runs it
# locally.
#
#   scripts/ci-guard-tests.sh [--plan] [BASE]
#       BASE defaults to $CI_BASE, else the merge base of HEAD and origin/main.
#       --plan prints what changed and which tests would run, and runs nothing.
#
# WHY. Five of forge main's red episodes in the week to 2026-11-25 were a
# guard that skips its whole-repo work under -short, so the merge queue proved
# nothing and main found it: deadcodeguard's TestNoDeadClaims (control-plane
# #2960, #3055, and again inside #3103 and #3225), the skills path allowlist
# (#3007, and inside #3103), TestEveryBufTemplateEmitterIsGuarded (#3041, and
# inside #3225), TestGOFLAGSOverridesKeepTrimpath (#3067; eleven reds inside
# #3225), vacuousguard's TestNoVacuousTests (inside #3103 and #3225). Each was
# a one-line fix that then spent hours in review and the queue while main
# stayed red.
#
# WHAT IT COSTS. Every test below once, in full mode, without -race (main
# runs them under -race or -p 1 in their own jobs). The -short tier just
# compiled every package with the same flags, so this is run time: the
# deadcodeguard whole-program load is the long pole (48-71s in main's guards
# job, 2026-11-24), and the others run beside it (go test runs packages in
# parallel). The rest of the guards stay on main: internal/tierguard renders
# four whole projects (61-98s on a runner, 5.5 min on a loaded 48-core dev
# machine), and fmtguard, modguard and mockheaderguard found nothing this
# week. internal/removalguard already runs in the merge tier on its own step.
#
# TRIGGERS are git pathspecs: any Go file, the module files, and anything
# under internal/ (the skills and templates are read as files). A change that
# touches none of them (docs, workflows, scripts) runs nothing. TESTS are
# <package dir>.<TestName>; one go test runs them all with -count=1 (the
# guards list files with git and load packages with `go list`, which Go's test
# cache cannot see, so a replayed PASS could miss what the change added). A
# listed test that did not run and pass, or that skipped, fails the script
# (and internal/ciguard's -short guard checks the names), so a rename cannot
# turn this step into a vacuous pass.
set -euo pipefail

triggers=(
  ':(glob)**/*.go'
  'go.mod'
  'go.sum'
  ':(glob)internal/**'
)

tests=(
  # The dead-claims scan: type-checks the whole program once per target OS.
  internal/deadcodeguard.TestNoDeadClaims
  internal/deadcodeguard.TestQuarantineIsTight
  internal/deadcodeguard.TestRulesFireOnPlantedDefects
  internal/deadcodeguard.TestExemptionsAreLoadBearing
  # Tests that assert nothing, across the repository.
  internal/vacuousguard.TestNoVacuousTests
  internal/vacuousguard.TestQuarantineIsTight
  internal/vacuousguard.TestCapabilityProbeSetIsComplete
  # A GOFLAGS override keeps -trimpath (parses every Go file).
  internal/goexec.TestGOFLAGSOverridesKeepTrimpath
  # Every `- local:` buf template under internal/ is one the plugin check reads.
  internal/cli.TestEveryBufTemplateEmitterIsGuarded
  internal/cli.TestBufTemplatesRunEachPluginOnce
  # Every path a shipped skill names exists in a fresh scaffold.
  internal/templates.TestSkillsPathReferencesExist
)

plan=false
if [[ "${1:-}" == "--plan" ]]; then
  plan=true
  shift
fi
[[ $# -le 1 ]] || { echo "usage: $0 [--plan] [BASE]" >&2; exit 2; }

cd "$(git rev-parse --show-toplevel)"
if [[ -n "${1:-}" ]]; then
  base=$1
elif [[ -n "${CI_BASE:-}" ]]; then
  base=$CI_BASE
else
  base=$(git merge-base origin/main HEAD) || { echo "ci-guard-tests: no merge base with origin/main; pass BASE" >&2; exit 2; }
fi
git rev-parse -q --verify "$base^{commit}" >/dev/null || { echo "ci-guard-tests: cannot resolve base '$base' (fetch it)" >&2; exit 2; }

changed=$(git diff --name-only --no-renames "$base" HEAD -- "${triggers[@]}") ||
  { echo "ci-guard-tests: git diff failed; cannot tell whether the guards' inputs changed" >&2; exit 2; }
if [[ -z "$changed" ]]; then
  echo "ci-guard-tests: no Go, module or internal/ change since $(git rev-parse --short "$base"): nothing to run"
  exit 0
fi

# The packages, in the order they first appear, and one anchored -run regex
# over every name (go test takes one -run for all packages; a name two
# packages share runs in both, which is what TestQuarantineIsTight wants).
declare -A seen=()
pkgs=()
names=()
for t in "${tests[@]}"; do
  pkg=${t%.*} name=${t##*.}
  if [[ -z "${seen[$pkg]:-}" ]]; then
    pkgs+=("$pkg")
    seen[$pkg]=1
  fi
  if [[ -z "${seen[.$name]:-}" ]]; then
    names+=("$name")
    seen[.$name]=1
  fi
done
regex="^($(IFS='|'; echo "${names[*]}"))\$"

echo "ci-guard-tests: $(wc -l <<<"$changed") changed file(s) reach the guards' inputs, e.g.:"
head -n 5 <<<"$changed" | while read -r f; do echo "  $f"; done
echo "packages: ${pkgs[*]/#/./}"
echo "-run $regex"
if $plan; then
  exit 0
fi

# Full mode: no -short, no -race. -v, so each listed test's verdict is in
# the log and the check below can read it.
log=$(mktemp "${TMPDIR:-/tmp}/ci-guard-tests.XXXXXX")
trap 'rm -f "$log"' EXIT
status=0
scripts/ci-go-test.sh -count=1 -v -timeout 15m -run "$regex" "${pkgs[@]/#/./}" | tee "$log" || status=1

# Every listed test ran and passed in its own package. go test prints each
# package's output as one block that ends with its `ok`/`FAIL` line, so a
# `--- PASS: Name` line belongs to the next package named after it.
module=$(go list -m)
passed=$(awk -v mod="$module/" '
  /^--- PASS: / { pending = pending " " $3 }
  ($1 == "ok" || $1 == "FAIL") && index($2, mod) == 1 {
    pkg = substr($2, length(mod) + 1)
    n = split(pending, a, " ")
    for (i = 1; i <= n; i++) print pkg "." a[i]
    pending = ""
  }
' "$log")
for t in "${tests[@]}"; do
  if ! grep -qxF "$t" <<<"$passed"; then
    echo "ci-guard-tests: $t did not run and pass (renamed, skipped or failed): fix it, or update the tests list in $0" >&2
    status=1
  fi
done
exit "$status"
