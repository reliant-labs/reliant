#!/usr/bin/env bash
# test-forge.sh — scripts/forge.sh against fake forge, reliant and go binaries
# on a PATH holding nothing else: FORGE_BIN wins; a forge on PATH or in
# .tools/bin runs only when it is the pin (any forge for a `dev` pin); then
# `reliant forge`; then `go run …@<pin>`; and with none of them it fails with
# 127. --machine takes any forge and never `go run`. The pin is the nearest
# forge.yaml above the working directory, else the script's own checkout's.
#
#   scripts/test-forge.sh        (seconds; needs only bash, sed, head, tr, env)
set -euo pipefail
src=$(cd "$(dirname "$0")" && pwd)/forge.sh
tmp=$(mktemp -d "${TMPDIR:-/tmp}/test-forge.XXXXXX")
trap 'rm -rf "$tmp"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }

pin=v1.2.3-0.20260101000000-abcdefabcdef
other=v1.2.2

# The tools forge.sh itself needs, and nothing that could be a forge.
mkdir -p "$tmp/tools"
for t in bash sed head tr dirname env; do
  ln -s "$(command -v "$t")" "$tmp/tools/$t"
done

# fake <dir> <name> <version>: a program that reports the version and echoes
# what it ran with (`<name> <args>`), exiting 3 for the args "exit3".
fake() {
  mkdir -p "$1"
  cat >"$1/$2" <<EOF
#!/usr/bin/env bash
if [ "\${1:-}" = --version ]; then echo "$2 version $3 (fake)"; exit 0; fi
echo "$1/$2 \$*"
[ "\${1:-}" = exit3 ] && exit 3
exit 0
EOF
  chmod +x "$1/$2"
}

# A project with the pin, a subdirectory, and the script in its scripts/.
proj=$tmp/proj
mkdir -p "$proj/sub/dir" "$proj/scripts"
printf 'name: x\nforge_version: %s # the pin\n' "$pin" >"$proj/forge.yaml"
cp "$src" "$proj/scripts/forge.sh"
script=$proj/scripts/forge.sh

fake "$tmp/at-pin" forge "$pin"
fake "$tmp/at-other" forge "$other"
fake "$tmp/rel" reliant "v9"
fake "$tmp/go" go "go1.99"

# which <dir> <PATH extra> [args...]: forge.sh --which run in dir.
which() {
  local dir=$1 extra=$2; shift 2
  (cd "$dir" && env -u FORGE_BIN PATH="$tmp/tools${extra:+:$extra}" "$script" "$@" --which 2>/dev/null)
}
check() { # want, got, what
  [ "$2" = "$1" ] || fail "$3: got '$2', want '$1'"
}

check "$tmp/at-pin/forge" "$(which "$proj" "$tmp/at-pin:$tmp/rel:$tmp/go")" "forge on PATH at the pin"
check "$tmp/at-pin/forge" "$(which "$proj/sub/dir" "$tmp/at-pin")" "the pin of the nearest forge.yaml above"
check "/given/forge" "$(cd "$proj" && FORGE_BIN=/given/forge PATH="$tmp/tools:$tmp/at-pin" "$script" --which)" "FORGE_BIN"
check "reliant forge" "$(which "$proj" "$tmp/at-other:$tmp/rel:$tmp/go")" "forge on PATH at another version"

mkdir -p "$proj/.tools/bin"
cp "$tmp/at-pin/forge" "$proj/.tools/bin/forge"
check "$proj/.tools/bin/forge" "$(which "$proj" "$tmp/at-other:$tmp/rel")" ".tools/bin/forge at the pin"
rm -rf "$proj/.tools"

check "env GOPRIVATE=github.com/reliant-labs/* GOFLAGS=-trimpath go run github.com/reliant-labs/forge/cmd/forge@$pin" \
  "$(which "$proj" "$tmp/at-other:$tmp/go")" "no reliant: go run the pin"
set +e
out=$(cd "$proj" && env -u FORGE_BIN PATH="$tmp/tools:$tmp/at-other" "$script" generate 2>&1)
rc=$?
set -e
check 127 "$rc" "nothing to run: exit status"
case "$out" in *"set FORGE_BIN"*"$pin"*) ;; *) fail "nothing to run: message '$out' names neither FORGE_BIN nor the pin" ;; esac

# A pin that names no published forge: any forge on PATH, never go run.
dev=$tmp/dev
mkdir -p "$dev"
printf 'forge_version: dev\n' >"$dev/forge.yaml"
check "$tmp/at-other/forge" "$(which "$dev" "$tmp/at-other:$tmp/rel")" "dev pin: any forge on PATH"
check "" "$(which "$dev" "$tmp/go" || true)" "dev pin: never go run"

# --machine: any forge, then reliant, never go run.
check "$tmp/at-other/forge" "$(which "$proj" "$tmp/at-other:$tmp/rel" --machine)" "--machine: any forge on PATH"
check "reliant forge" "$(which "$proj" "$tmp/rel:$tmp/go" --machine)" "--machine: reliant forge"
check "" "$(which "$proj" "$tmp/go" --machine || true)" "--machine: never go run"

# Outside any project: the pin of the checkout the script lives in.
mkdir -p "$tmp/outside"
check "$tmp/at-pin/forge" "$(which "$tmp/outside" "$tmp/at-pin")" "outside a project: the script's checkout's pin"
check "reliant forge" "$(which "$tmp/outside" "$tmp/at-other:$tmp/rel")" "outside a project: not another version"

# Running: forge's arguments pass through, and so does its exit status.
check "$tmp/at-pin/forge lint --quiet" "$(cd "$proj" && env -u FORGE_BIN PATH="$tmp/tools:$tmp/at-pin" "$script" lint --quiet)" "arguments"
check "$tmp/rel/reliant forge generate" "$(cd "$proj" && env -u FORGE_BIN PATH="$tmp/tools:$tmp/rel" "$script" generate)" "reliant forge's arguments"
set +e
(cd "$proj" && env -u FORGE_BIN PATH="$tmp/tools:$tmp/at-pin" "$script" exit3 >/dev/null)
rc=$?
set -e
check 3 "$rc" "forge's exit status"

echo "PASS: forge.sh"
