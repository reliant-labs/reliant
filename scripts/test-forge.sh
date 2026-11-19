#!/usr/bin/env bash
# test-forge.sh — scripts/forge.sh against fake forge, reliant and go binaries
# on a PATH holding nothing else: FORGE_BIN wins; a forge on PATH or in
# .tools/bin runs only when it is the pin (any forge for a `dev` pin); then
# the pin through reliant (`reliant forge` in the project; outside it, the
# pinned build reliant's hand-off keeps, never reliant's own forge); then
# `go run …@<pin>`; and with none of them it fails with 127. --machine takes any forge and never `go run`. The pin is the nearest
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
for t in bash sed head tr dirname env find uname mkdir chmod; do
  ln -s "$(command -v "$t")" "$tmp/tools/$t"
done

# fake <dir> <name> <version>: a program that reports the version and echoes
# what it ran with (`<name> <args>`), exiting 3 for the args "exit3".
fake() {
  mkdir -p "$1"
  cat >"$1/$2" <<EOF
#!/usr/bin/env bash
if [ "\${1:-}" = --version ] || [ "\${1:-} \${2:-}" = "forge --version" ]; then echo "$2 version $3 (fake)"; exit 0; fi
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

# A reliant like the real one: its own forge is another version, and only a
# command run inside the project goes to the pin, which its hand-off builds
# once into FORGE_PINNED_CACHE (<pin>/<os-arch>/forge) and execs.
mkdir -p "$tmp/handoff"
cat >"$tmp/handoff/reliant" <<EOF
#!/usr/bin/env bash
shift # forge
if [ "\${1:-}" = --version ]; then echo "forge version v0.0.1-embedded (embedded in reliant)"; exit 0; fi
if [ -f forge.yaml ]; then
  b="\$FORGE_PINNED_CACHE/$pin/linux-amd64/forge"
  mkdir -p "\$(dirname "\$b")"
  printf '#!/usr/bin/env bash\nif [ "\${1:-}" = --version ]; then echo "forge version $pin"; exit 0; fi\necho "pinned $pin \$*"\n' >"\$b"
  chmod +x "\$b"
  exec "\$b" "\$@"
fi
echo "embedded v0.0.1 \$*"
EOF
chmod +x "$tmp/handoff/reliant"
cache=$tmp/cache
outside() { # PATH extra, then forge.sh's args: run from outside any project
  local extra=$1
  shift
  (cd "$tmp/outside" && env -u FORGE_BIN FORGE_PINNED_CACHE="$cache" PATH="$tmp/tools:$extra" "$script" "$@" 2>/dev/null)
}
check "embedded v0.0.1 whoami" "$(cd "$tmp/outside" && PATH="$tmp/tools" "$tmp/handoff/reliant" forge whoami)" \
  "the fake runs reliant's own forge outside a project, as reliant does"
check "pinned $pin whoami" "$(outside "$tmp/at-other:$tmp/handoff" whoami)" \
  "outside a project: the pin through reliant (the first run builds it), never reliant's own forge"
check "$cache/$pin/linux-amd64/forge" "$(outside "$tmp/handoff" --which)" "outside a project: reliant's cached build of the pin"
check "reliant forge" "$(cd "$proj" && env -u FORGE_BIN FORGE_PINNED_CACHE="$cache" PATH="$tmp/tools:$tmp/handoff" "$script" --which)" \
  "inside the project: reliant forge"
# reliant's own forge IS the pin: reliant forge from anywhere.
fake "$tmp/rel-at-pin" reliant "$pin"
check "reliant forge" "$(which "$tmp/outside" "$tmp/rel-at-pin")" "outside a project: a reliant whose forge is the pin"
# The pinned build is not where the cache says (its layout moved): never
# reliant's own forge, but go run the pin.
rm -rf "$cache"
sed -i.bak "s|/$pin/linux-amd64/forge|/moved/forge|" "$tmp/handoff/reliant"
check "env GOPRIVATE=github.com/reliant-labs/* GOFLAGS=-trimpath go run github.com/reliant-labs/forge/cmd/forge@$pin" \
  "$(outside "$tmp/handoff:$tmp/go" --which)" "outside a project, the pinned build not found: go run, not reliant's own forge"

# Running: forge's arguments pass through, and so does its exit status.
check "$tmp/at-pin/forge lint --quiet" "$(cd "$proj" && env -u FORGE_BIN PATH="$tmp/tools:$tmp/at-pin" "$script" lint --quiet)" "arguments"
check "$tmp/rel/reliant forge generate" "$(cd "$proj" && env -u FORGE_BIN PATH="$tmp/tools:$tmp/rel" "$script" generate)" "reliant forge's arguments"
set +e
(cd "$proj" && env -u FORGE_BIN PATH="$tmp/tools:$tmp/at-pin" "$script" exit3 >/dev/null)
rc=$?
set -e
check 3 "$rc" "forge's exit status"

echo "PASS: forge.sh"
