#!/usr/bin/env bash
# The merge tier, on this machine: what a merge steward runs to gate a
# forge pull request (`task ci`). Target: about five minutes.
#
#   scripts/ci-premerge.sh [BASE]     BASE defaults to $CI_BASE, else the merge
#                                     base of HEAD and origin/main (fetched)
#
# It runs what ci.yml's merge tier and pull request checks run (`test-short`,
# `checks` and `pr-checks`),
# with the same flags, concurrently, and prints one table:
#
#   test-short       go test -short over every package (scripts/ci-test-shard.sh 0 1):
#                    no -race, no -count — an unchanged package replays its cached
#                    result, which the -short tier is held to be sound for
#   flake-gate       the tests this change adds or edits, -short -count=3, no -race
#                    (scripts/ci-changed-tests.sh: only those tests, whose binaries
#                    changed anyway, so the repetition defeats no cache)
#   changelog        CHANGELOG.md is untouched (scripts/check-changelog-untouched.sh)
#   apigate          an incompatible pkg/ change is declared (internal/apigate; PR=<n>
#                    reads the pull request's labels and body, as CI does)
#   verify-generated forge generate over this checkout changes nothing
#   validate-kcl     the KCL manifests validate
#   pre-commit       the pre-commit hooks over the changed files, in a scratch worktree
#   licenses-go      the Go import graph's licenses (scripts/check-licenses.sh)
#   licenses-npm     web-runtime's, when its package.json or lock changed
#
# What it leaves to main, as ci.yml does: -race, full mode, the guards'
# whole-repo work, golangci-lint and go vet, govulncheck, Windows, e2e.
#
# RULES. Every step runs whatever the others concluded. A step that could not
# run (a missing tool) is SKIP, and a SKIP fails the gate unless CI_SKIP names
# it (CI_SKIP=pre-commit,licenses-npm); it stays in the table as WAIVED. N/A
# (nothing for the step to look at) passes. Exit status: 0 only when every
# row is PASS, N/A or WAIVED.
#
# Logs go to $CI_LOG_DIR (default: a fresh temp dir, printed). Run it as one
# heavy command — `reliant forge slot run -- task ci`, or your harness's heavy
# flag — and the forge processes inside reuse that slot (FORGE_SLOT_HELD).
set -uo pipefail

cd "$(git rev-parse --show-toplevel)" || exit 2

if [[ -n "${1:-}" ]]; then
  base=$1
elif [[ -n "${CI_BASE:-}" ]]; then
  base=$CI_BASE
else
  git fetch -q origin main 2>/dev/null || echo "warning: could not fetch origin/main; using the local ref" >&2
  base=$(git merge-base origin/main HEAD) || { echo "error: no merge base with origin/main; pass BASE" >&2; exit 2; }
fi
git rev-parse -q --verify "$base^{commit}" >/dev/null || { echo "error: cannot resolve base '$base'" >&2; exit 2; }

if [[ -n "$(git status --porcelain --untracked-files=no)" ]]; then
  echo "warning: the tree has uncommitted changes; the diff-driven steps judge $base..HEAD (committed) only" >&2
fi

logs=${CI_LOG_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/forge-premerge.XXXXXX")}
mkdir -p "$logs"
scratch=$(mktemp -d "${TMPDIR:-/tmp}/forge-premerge-scratch.XXXXXX")
cleanup() {
  if [[ -d "$scratch/pre-commit" ]]; then git worktree remove --force "$scratch/pre-commit" >/dev/null 2>&1; fi
  rm -rf "$scratch"
}
trap cleanup EXIT

declare -A waived=()
IFS=',' read -ra skips <<<"${CI_SKIP:-}"
for s in "${skips[@]}"; do [[ -n "$s" ]] && waived[$s]=1; done

order=(test-short flake-gate changelog apigate verify-generated validate-kcl pre-commit licenses-go licenses-npm)
started=$SECONDS
echo "forge pre-merge gate: $(git rev-parse --short HEAD) against $(git rev-parse --short "$base"), logs in $logs"

# step NAME CMD... — run CMD with its output in $logs/NAME.log and record
# "<status> <seconds>" in $logs/NAME.status. Exit 3 from CMD means SKIP and 4
# means N/A (see skip/na below).
step() {
  local name=$1
  shift
  local t0=$SECONDS rc=0
  echo "▶ $name"
  # A subshell: skip/na end the step with `exit`, not the gate.
  ("$@") >"$logs/$name.log" 2>&1 || rc=$?
  local status
  case $rc in
    0) status=PASS ;;
    3) status=SKIP ;;
    4) status=N/A ;;
    *) status=FAIL ;;
  esac
  if [[ $status == SKIP && -n "${waived[$name]:-}" ]]; then status=WAIVED; fi
  echo "$status $((SECONDS - t0))" >"$logs/$name.status"
  echo "■ $name: $status ($((SECONDS - t0))s)"
}
skip() { echo "SKIP: $*"; exit 3; }
na() { echo "N/A: $*"; exit 4; }
need() { command -v "$1" >/dev/null 2>&1 || skip "$1 is not on PATH"; }

test_short() { CI_TEST_JSON="$logs/test-short.json" GITHUB_STEP_SUMMARY="$logs/test-short.md" scripts/ci-test-shard.sh 0 1; }
flake_gate() { GITHUB_STEP_SUMMARY="$logs/flake-gate.md" scripts/ci-changed-tests.sh "$base"; }
changelog() { scripts/check-changelog-untouched.sh "$base"; }
apigate() {
  local args=(-base "$base")
  if [[ -n "${PR:-}" ]]; then args+=(-pr "$PR"); fi
  go run ./internal/apigate "${args[@]}"
}

# forge from this checkout, and the codegen tools at go.mod's versions, in a
# bin dir of this run's own, first on PATH: the steps below must judge this
# checkout's forge, and a tool on the machine's PATH at another version must
# not stand in for the pinned one (ci.yml's checks job does the same).
bin="$scratch/bin"
forge_tools() {
  mkdir -p "$bin" &&
    go build -o "$bin/forge" ./cmd/forge &&
    GOBIN="$bin" PATH="$bin:$PATH" "$bin/forge" tools install --force
}
verify_generated() { PATH="$bin:$PATH" "$bin/forge" ci verify-generated; }
validate_kcl() {
  need kcl
  PATH="$bin:$PATH" "$bin/forge" ci validate-kcl
}
pre_commit() {
  local pc=()
  if command -v pre-commit >/dev/null 2>&1; then
    pc=(pre-commit)
  elif command -v uvx >/dev/null 2>&1; then
    pc=(uvx pre-commit)
  else
    skip "neither pre-commit nor uvx is on PATH"
  fi
  # A scratch worktree: the hooks include fixers, and this checkout must not
  # change under the tests running beside them.
  git worktree add -q --detach "$scratch/pre-commit" HEAD || return 1
  (cd "$scratch/pre-commit" && SKIP=go-vet-mod,proto-generated "${pc[@]}" run --show-diff-on-failure --from-ref "$base" --to-ref HEAD)
}
licenses_go() { go list -deps ./... >/dev/null && scripts/check-licenses.sh; }
licenses_npm() {
  if git diff --quiet "$base" HEAD -- web-runtime/package.json web-runtime/package-lock.json; then
    na "web-runtime's dependencies did not change"
  fi
  need npm
  (cd web-runtime && npm install --no-audit --no-fund) && scripts/check-npm-licenses.sh web-runtime
}

step test-short test_short &
step flake-gate flake_gate &
step changelog changelog &
step apigate apigate &
{
  step forge-tools forge_tools
  if [[ $(cut -d' ' -f1 "$logs/forge-tools.status") == PASS ]]; then
    step verify-generated verify_generated &
    step validate-kcl validate_kcl &
    wait
  else
    for s in verify-generated validate-kcl; do
      echo "FAIL 0" >"$logs/$s.status"
      echo "could not build forge or install its codegen tools: see forge-tools.log" >"$logs/$s.log"
    done
  fi
} &
step pre-commit pre_commit &
step licenses-go licenses_go &
step licenses-npm licenses_npm &
wait

failed=0
printf '\n%-17s %-7s %6s  %s\n' STEP RESULT SECONDS LOG
for s in forge-tools "${order[@]}"; do
  read -r status secs <"$logs/$s.status" 2>/dev/null || { status=FAIL secs=0; }
  printf '%-17s %-7s %6s  %s\n' "$s" "$status" "$secs" "$logs/$s.log"
  case $status in
    PASS | N/A | WAIVED) ;;
    *) failed=1 ;;
  esac
done
echo
echo "wall: $((SECONDS - started))s"
for s in forge-tools "${order[@]}"; do
  read -r status _ <"$logs/$s.status" 2>/dev/null || status=FAIL
  case $status in
    FAIL | SKIP)
      echo
      echo "── $s ($status): last 40 lines of $logs/$s.log"
      tail -n 40 "$logs/$s.log"
      ;;
  esac
done
if ((failed)); then
  echo
  echo "pre-merge gate: FAILED (a SKIP fails too; CI_SKIP=<step> waives one on purpose)"
  exit 1
fi
echo "pre-merge gate: passed"
