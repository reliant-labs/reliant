#!/usr/bin/env bash
# test-pin-ancestry.sh — prove scripts/check-pin-ancestry.sh FAILS on a forge
# pin that is not on forge's main, and passes on one that is.
#
# WHY THIS EXISTS: the ancestry check is a detector. A detector that can pass
# vacuously is worse than none — it turns "nobody is looking" into "something
# is looking and says it's fine". So it gets a test that pins the property
# that matters: a pin off main => non-zero exit.
#
# Hermetic by default. It builds a throwaway "forge" repo with a main and a
# side branch (standing in for a PR head that was squash-merged away), points
# the check at it via PIN_ANCESTRY_FORGE_REMOTE, and runs it against fake
# go.mod files. No network, no credentials.
#
# --live also runs against the real github.com/reliant-labs/forge: forge
# main's head must pass, and 1b0f7f557663 (the dangling pin reliant main
# carried on 2026-09-25) must fail. Needs network, no auth.
#
# USAGE: scripts/test-pin-ancestry.sh [--live]
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="${ROOT_DIR}/scripts/check-pin-ancestry.sh"
LIVE=0
[ "${1:-}" = "--live" ] && LIVE=1

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

# Hermetic git: no user or system config (commit signing, url rewrites).
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
export GIT_AUTHOR_NAME=test GIT_AUTHOR_EMAIL=test@example.invalid
export GIT_COMMITTER_NAME=test GIT_COMMITTER_EMAIL=test@example.invalid

FAILURES=0
pass() { echo "  PASS: $1"; }
fail() { echo "  FAIL: $1" >&2; FAILURES=$((FAILURES + 1)); }

# ── The fake forge ───────────────────────────────────────────────────────
#   main:  m1 ─ m2 (tag v0.1.0) ─ m3
#   side:   └── s1 (tag v0.9.0)       <- a PR head squash-merged away
SIB="${TMP_DIR}/forge"
git init --quiet -b main "${SIB}"
git -C "${SIB}" config uploadpack.allowFilter true
commit() { git -C "${SIB}" commit --quiet --allow-empty -m "$1" && git -C "${SIB}" rev-parse HEAD; }
m1="$(commit m1)"
m2="$(commit m2)"
git -C "${SIB}" tag -a v0.1.0 -m v0.1.0
m3="$(commit m3)"
git -C "${SIB}" checkout --quiet -b side "${m1}"
s1="$(commit s1)"
git -C "${SIB}" tag -a v0.9.0 -m v0.9.0
git -C "${SIB}" checkout --quiet main
: "${m2}"

pseudo() { echo "v0.1.1-0.20260101000000-${1:0:12}"; }

# expect <description> <want-exit> <go.mod require block, or empty> [remote] [extra flag]
expect() {
  local desc="$1" want="$2" require="$3" remote="${4:-${SIB}}" flag="${5:-}" dir out got
  dir="$(mktemp -d "${TMP_DIR}/consumer.XXXXXX")"
  printf 'module example.com/consumer\n\ngo 1.26\n\n%s\n' "${require}" > "${dir}/go.mod"
  set +e
  out="$(PIN_ANCESTRY_FORGE_REMOTE="${remote}" "${CHECK}" --root "${dir}" ${flag} 2>&1)"
  got=$?
  set -e
  if [ "${got}" = "${want}" ]; then
    pass "${desc} (exit ${got})"
  else
    fail "${desc}: want exit ${want}, got ${got}"
    echo "${out}" | sed 's/^/      /' >&2
  fi
  LAST_OUT="${out}"
}
req() { printf 'require (\n\tgithub.com/reliant-labs/forge %s\n)' "$1"; }

echo "== pin ancestry check — detector self-test =="

echo "-- pins that are on main must PASS"
expect "pseudo-version of main's head"              0 "$(req "$(pseudo "${m3}")")"
expect "pseudo-version of an older main commit"     0 "$(req "$(pseudo "${m1}")")"
expect "tag on main"                                0 "$(req v0.1.0)"
expect "single-line require form"                   0 "require github.com/reliant-labs/forge $(pseudo "${m3}")"
expect "pseudo-version with +incompatible suffix"   0 "$(req "$(pseudo "${m3}")+incompatible")"

echo "-- THE CASES THAT MATTER: pins off main must FAIL"
expect "pseudo-version of a side-branch commit"     1 "$(req "$(pseudo "${s1}")")"
expect "pseudo-version of a commit that is gone"    1 "$(req v0.1.1-0.20260101000000-deadbeefcafe)"
expect "tag cut on a side branch"                   1 "$(req v0.9.0)"
expect "tag that does not exist"                    1 "$(req v9.9.9)"
expect "no forge require at all (vacuous)"          1 ""

echo "-- CI mode annotates the failing file"
expect "side-branch pin with --github"              1 "$(req "$(pseudo "${s1}")")" "${SIB}" --github
if printf '%s\n' "${LAST_OUT}" | grep -q '^::error file=go.mod,'; then
  pass "--github emits ::error file=go.mod"
else
  fail "--github did not emit ::error file=go.mod"
fi

echo "-- a check that cannot run must not pass"
expect "unreachable remote"                         2 "$(req "$(pseudo "${m3}")")" "${TMP_DIR}/no-such-repo"

if [ "${LIVE}" = "1" ]; then
  echo "-- live: github.com/reliant-labs/forge"
  live_remote="https://github.com/reliant-labs/forge"
  live_main="$(git ls-remote "${live_remote}" refs/heads/main | cut -f1)"
  [ -n "${live_main}" ] || { fail "could not read forge main"; exit 1; }
  expect "forge main ${live_main:0:12}"             0 "$(req "$(pseudo "${live_main}")")" "${live_remote}"
  expect "dangling 1b0f7f557663 (squashed #235 head)" 1 "$(req v0.1.18-0.20260924231801-1b0f7f557663)" "${live_remote}"
fi

echo
if [ "${FAILURES}" -gt 0 ]; then
  echo "${FAILURES} self-test case(s) FAILED — the ancestry check cannot be trusted" >&2
  exit 1
fi
echo "all self-test cases passed"
