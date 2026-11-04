#!/usr/bin/env bash
# test-fmt-staged.sh — prove .githooks/fmt-staged.sh formats exactly the
# staged files, by language, and nothing else.
#
# WHY THIS EXISTS: the pre-commit hook used to format the whole module on every
# commit (`go fmt ./...`, over a minute on a TypeScript-only commit). The
# property that matters is the selection: a commit with no Go file runs no Go
# formatter, an unstaged file is never touched, and what gets re-staged is only
# what the commit already carried.
#
# Hermetic: a throwaway git repo, GOFMT pointed at a recorder that wraps the
# real gofmt, and a fake prettier. No network.
#
# USAGE: scripts/test-fmt-staged.sh
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCRIPT="${ROOT_DIR}/.githooks/fmt-staged.sh"
REAL_GOFMT="$(command -v gofmt)"

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

# Hermetic git: no user or system config (commit signing, hooksPath).
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
export GIT_AUTHOR_NAME=test GIT_AUTHOR_EMAIL=test@example.invalid
export GIT_COMMITTER_NAME=test GIT_COMMITTER_EMAIL=test@example.invalid

FAILURES=0
pass() { echo "  PASS: $1"; }
fail() { echo "  FAIL: $1" >&2; FAILURES=$((FAILURES + 1)); }

# The recorder logs each gofmt invocation's file arguments, one per line.
LOG="${TMP_DIR}/gofmt.log"
cat >"${TMP_DIR}/gofmt" <<EOF
#!/usr/bin/env bash
for a in "\$@"; do case "\$a" in -*) ;; *) echo "\$a" >>"${LOG}" ;; esac; done
exec "${REAL_GOFMT}" "\$@"
EOF
chmod +x "${TMP_DIR}/gofmt"
export GOFMT="${TMP_DIR}/gofmt"

UNFORMATTED=$'package a\nfunc  F( ) {   }\n'
FORMATTED=$'package a\n\nfunc F() {}\n'

# new_repo: a fresh repo with an unformatted committed Go file and a web app
# whose package.json declares no prettier (reliant's shape today).
new_repo() {
    REPO="${TMP_DIR}/repo-$1"
    git init -q "${REPO}"
    cd "${REPO}"
    mkdir -p internal/a frontends/web/src
    printf '%s' "${UNFORMATTED}" >internal/a/untouched.go
    echo '{"name":"web"}' >frontends/web/package.json
    echo 'export const x=1' >frontends/web/src/x.ts
    git add -A && git commit -qm init --no-verify
    : >"${LOG}"
}

run_hook() { "${SCRIPT}" >"${TMP_DIR}/out.txt" 2>&1; }

echo "fmt-staged: a TypeScript-only commit runs no Go formatter"
new_repo ts-only
echo 'export const y=2' >>frontends/web/src/x.ts
git add frontends/web/src/x.ts
if run_hook && [ ! -s "${LOG}" ]; then pass "gofmt not run"; else fail "gofmt ran: $(cat "${LOG}" "${TMP_DIR}/out.txt")"; fi
if [ "$(cat internal/a/untouched.go)" = "$(printf '%s' "${UNFORMATTED}")" ]; then
    pass "an unstaged Go file is left alone"
else
    fail "an unstaged Go file was formatted"
fi
if git diff --quiet; then pass "nothing outside the commit changed"; else fail "work tree changed: $(git diff --stat)"; fi

echo "fmt-staged: only the staged Go files are formatted, and re-staged"
new_repo go
printf '%s' "${UNFORMATTED}" >internal/a/staged.go
printf '%s' "${UNFORMATTED}" >internal/a/unstaged.go
git add internal/a/staged.go
if run_hook; then pass "exits 0"; else fail "exit non-zero: $(cat "${TMP_DIR}/out.txt")"; fi
if [ "$(cat "${LOG}")" = "internal/a/staged.go" ]; then
    pass "gofmt saw exactly the staged file"
else
    fail "gofmt saw: $(tr '\n' ' ' <"${LOG}")"
fi
if [ "$(git show :internal/a/staged.go)" = "$(printf '%s' "${FORMATTED}")" ]; then
    pass "the staged file's formatted content is staged"
else
    fail "index holds: $(git show :internal/a/staged.go)"
fi
if [ "$(cat internal/a/unstaged.go)" = "$(printf '%s' "${UNFORMATTED}")" ] && [ "$(cat internal/a/untouched.go)" = "$(printf '%s' "${UNFORMATTED}")" ]; then
    pass "unstaged and untouched Go files are left alone"
else
    fail "a Go file outside the commit was formatted"
fi

echo "fmt-staged: a partially staged file is never re-staged"
new_repo partial
printf '%s' "${UNFORMATTED}" >internal/a/p.go
git add internal/a/p.go
printf '%s\n// unstaged edit\n' "${UNFORMATTED}" >internal/a/p.go
staged_before="$(git show :internal/a/p.go)"
if run_hook; then
    fail "a partially staged file with unformatted staged content must refuse the commit"
else
    pass "refuses, naming the fix"
fi
grep -q 'git add -p -- internal/a/p.go' "${TMP_DIR}/out.txt" && pass "says how to stage it" || fail "no fix named: $(cat "${TMP_DIR}/out.txt")"
if [ "$(git show :internal/a/p.go)" = "${staged_before}" ]; then
    pass "the index is untouched (no unstaged hunk swept in)"
else
    fail "the partially staged file was re-staged"
fi

echo "fmt-staged: a web package that pins prettier gets it on its staged files only"
new_repo prettier
echo '{"name":"web","devDependencies":{"prettier":"3.3.3"}}' >frontends/web/package.json
mkdir -p frontends/web/node_modules/.bin
PLOG="${TMP_DIR}/prettier.log"
: >"${PLOG}"
cat >frontends/web/node_modules/.bin/prettier <<EOF
#!/usr/bin/env bash
for a in "\$@"; do case "\$a" in -*) ;; *) echo "\$a" >>"${PLOG}" ;; esac; done
EOF
chmod +x frontends/web/node_modules/.bin/prettier
echo 'export const y=2' >>frontends/web/src/x.ts
echo 'export const z=3' >frontends/web/src/z.ts
git add frontends/web/src/x.ts
if run_hook && [ "$(cat "${PLOG}")" = "frontends/web/src/x.ts" ]; then
    pass "prettier saw exactly the staged web file"
else
    fail "prettier saw: $(tr '\n' ' ' <"${PLOG}") $(cat "${TMP_DIR}/out.txt")"
fi
[ ! -s "${LOG}" ] && pass "gofmt not run" || fail "gofmt ran on a web-only commit"

echo "fmt-staged: an empty commit formats nothing"
new_repo empty
if run_hook && [ ! -s "${LOG}" ]; then pass "nothing run"; else fail "ran: $(cat "${LOG}" "${TMP_DIR}/out.txt")"; fi

if [ "${FAILURES}" -gt 0 ]; then
    echo "test-fmt-staged: ${FAILURES} failure(s)" >&2
    exit 1
fi
echo "test-fmt-staged: all passed"
