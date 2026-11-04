#!/usr/bin/env bash
# test-binary-files.sh — prove .githooks/check-binary-files.sh fails on a
# committed binary or an oversized new file, passes a normal diff, honours
# .binary-allowlist, and fails loudly when it cannot run.
#
# WHY THIS EXISTS: the guard used to classify files with file(1) and pass
# whatever it could not classify, so on a machine without file(1) it passed
# everything. control-plane#1952 merged a 5.9 MB build binary through the same
# gap, in a merge commit. A guard that cannot fail is not a guard.
#
# Hermetic: throwaway git repos, no user or system git config, no network.
#
# USAGE: scripts/test-binary-files.sh
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCRIPT="${ROOT_DIR}/.githooks/check-binary-files.sh"

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
export GIT_AUTHOR_NAME=test GIT_AUTHOR_EMAIL=test@example.invalid
export GIT_COMMITTER_NAME=test GIT_COMMITTER_EMAIL=test@example.invalid

FAILURES=0
pass() { echo "  PASS: $1"; }
fail() { echo "  FAIL: $1" >&2; FAILURES=$((FAILURES + 1)); }

# new_repo <name>: a repo on branch feature, forked from main's one commit.
new_repo() {
    REPO="${TMP_DIR}/$1"
    mkdir -p "${REPO}"
    cd "${REPO}"
    git init -q -b main
    echo hello >README.md
    git add -A
    git commit -qm base
    git checkout -q -b feature
}

# elf <path>: the start of a compiled Linux executable (magic, NUL padding).
elf() { mkdir -p "$(dirname "$1")"; printf '\177ELF\002\001\001\000\000\000\000\000\000\000\000\000payload' >"$1"; }

# check [args...]: run the guard; its output in OUT, its exit code in RC.
check() {
    RC=0
    OUT="$("${SCRIPT}" "$@" 2>&1)" || RC=$?
}

echo "check-binary-files: a text-only change passes"
new_repo text
echo 'package a' >a.go
echo world >>README.md
git add -A && git commit -qm text
check --range main HEAD
[ "${RC}" -eq 0 ] && pass "exit 0" || fail "exit ${RC}: ${OUT}"

echo "check-binary-files: a binary that arrives through a merge commit fails"
new_repo merge
git checkout -q -b side main
elf tools/x/x
git add -A && git commit -qm "side adds a build output"
git checkout -q feature
git merge -q --no-ff --no-edit side
check --range main HEAD
if [ "${RC}" -eq 1 ] && [[ "${OUT}" == *"tools/x/x: binary file"* ]]; then pass "exit 1 naming tools/x/x"; else fail "exit ${RC}: ${OUT}"; fi

echo "check-binary-files: a .gitattributes rule that diffs a binary as text does not hide it"
new_repo attrs
printf '*.bin diff\n' >.gitattributes
elf tools/x.bin
chmod +x tools/x.bin
git add -A && git commit -qm "a binary, and an attribute that makes git diff it as text"
check --range main HEAD
if [ "${RC}" -eq 1 ] && [[ "${OUT}" == *"tools/x.bin: binary file"* ]]; then pass "exit 1 naming tools/x.bin"; else fail "exit ${RC}: ${OUT}"; fi
elf tools/y.bin
git add tools/y.bin
check
if [ "${RC}" -eq 1 ] && [[ "${OUT}" == *"tools/y.bin: binary file"* ]]; then pass "staged: exit 1 naming tools/y.bin"; else fail "exit ${RC}: ${OUT}"; fi

echo "check-binary-files: a binary whose name holds a newline or a tab is caught by name"
new_repo oddnames
nl_bin="$(printf 'a\nb.bin')" tab_bin="$(printf 'c\td.bin')"
elf "${nl_bin}"
elf "${tab_bin}"
printf 'text\n' >"$(printf 'e\nf.txt')"
git add -A && git commit -qm "binaries with a newline and a tab in their names"
check --range main HEAD
if [ "${RC}" -eq 1 ] && [[ "${OUT}" == *"${nl_bin}: binary file"* ]] && [[ "${OUT}" == *"${tab_bin}: binary file"* ]] && [[ "${OUT}" != *"f.txt"* ]]; then
    pass "range: exit 1 naming both, not the text file"
else
    fail "exit ${RC}: ${OUT}"
fi
nl_staged="$(printf 'g\nh')" tab_staged="$(printf 'i\tj')"
elf "${nl_staged}"
elf "${tab_staged}"
git add -A
check
if [ "${RC}" -eq 1 ] && [[ "${OUT}" == *"${nl_staged}: binary file"* ]] && [[ "${OUT}" == *"${tab_staged}: binary file"* ]]; then
    pass "staged: exit 1 naming both"
else
    fail "exit ${RC}: ${OUT}"
fi

echo "check-binary-files: a new file over the cap fails; a grown existing one does not"
new_repo size
head -c 2048 /dev/zero | tr '\0' x >data.json
head -c 2048 /dev/zero | tr '\0' x >README.md
git add -A && git commit -qm big
MAX_NEW_FILE_BYTES=1024 check --range main HEAD
if [ "${RC}" -eq 1 ] && [[ "${OUT}" == *"data.json: new file of 2048 bytes"* ]] && [[ "${OUT}" != *README.md* ]]; then
    pass "exit 1 naming data.json only"
else
    fail "exit ${RC}: ${OUT}"
fi

echo "check-binary-files: an allowlisted glob passes"
new_repo allow
mkdir -p docs/images/a
printf '\211PNG\r\n\032\n\000\000\000\rIHDR' >docs/images/a/shot.png
printf '# screenshots\ndocs/images/*.png\n' >.binary-allowlist
git add -A && git commit -qm image
check --range main HEAD
[ "${RC}" -eq 0 ] && pass "exit 0" || fail "exit ${RC}: ${OUT}"

echo "check-binary-files: the staged mode judges the index"
new_repo staged
elf bin/tool
git add -A
check
if [ "${RC}" -eq 1 ] && [[ "${OUT}" == *"bin/tool: binary file"* ]]; then pass "exit 1 naming bin/tool"; else fail "exit ${RC}: ${OUT}"; fi

echo "check-binary-files: it fails loudly when it cannot run"
new_repo broken
check --range no-such-ref HEAD
if [ "${RC}" -eq 2 ] && [[ "${OUT}" == *"refusing to pass"* ]]; then pass "an unresolvable base is exit 2"; else fail "exit ${RC}: ${OUT}"; fi
RC=0
OUT="$(PATH="${TMP_DIR}/empty-path" "$(command -v bash)" "${SCRIPT}" --range main HEAD 2>&1)" || RC=$?
if [ "${RC}" -eq 2 ] && [[ "${OUT}" == *"git is not on PATH"* ]]; then pass "no git is exit 2"; else fail "exit ${RC}: ${OUT}"; fi

echo "check-binary-files: this repository's own binaries are all allowlisted"
cd "${ROOT_DIR}"
# --eol's i/-text: git's own binary verdict on the indexed content, the one
# `git diff --numstat` reports as -<TAB>- (and far cheaper than diffing the tree).
unlisted="$(git ls-files --eol | awk '$1 == "i/-text" { sub(/^[^\t]*\t/, ""); print }' |
    while IFS= read -r f; do
        listed=0
        while IFS= read -r pat; do
            pat="${pat%%#*}"; pat="${pat%"${pat##*[![:space:]]}"}"
            # shellcheck disable=SC2053 # the pattern is a glob on purpose
            [ -n "${pat}" ] && [[ "${f}" == ${pat} ]] && { listed=1; break; }
        done <.binary-allowlist
        [ "${listed}" -eq 1 ] || echo "${f}"
    done)"
[ -z "${unlisted}" ] && pass "every tracked binary matches .binary-allowlist" || fail "tracked binaries no .binary-allowlist line matches: ${unlisted}"

if [ "${FAILURES}" -gt 0 ]; then
    echo "test-binary-files: ${FAILURES} failure(s)" >&2
    exit 1
fi
echo "test-binary-files: all passed"
