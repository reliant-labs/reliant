#!/usr/bin/env bash
# test-forge.sh — scripts/forge.sh against fake forge, reliant and go binaries
# on a PATH holding nothing else: FORGE_BIN wins; a forge on PATH or in
# .tools/bin runs only when it is the pin (any forge for a `dev` pin); then
# the pin through reliant (`reliant forge` in the project; outside it, the
# pinned build reliant's hand-off keeps, never reliant's own forge); then
# `go install …@<pin>` once into the user cache, run directly so forge's own
# exit status (2, not `go run`'s 1) reaches the caller; and with none of them
# it fails with 127. --machine takes any forge and never `go install`. The pin
# is the nearest forge.yaml above the working directory, else the script's own
# checkout's.
#
#   scripts/test-forge.sh        (seconds; needs only bash and coreutils)
set -euo pipefail
src=$(cd "$(dirname "$0")" && pwd)/forge.sh
tmp=$(mktemp -d "${TMPDIR:-/tmp}/test-forge.XXXXXX")
trap 'rm -rf "$tmp"' EXIT
# The go-install cache (step 4) goes here, never in the real user cache.
export XDG_CACHE_HOME=$tmp/xdg
fail() { echo "FAIL: $*" >&2; exit 1; }

pin=v1.2.3-0.20260101000000-abcdefabcdef
other=v1.2.2

# The tools forge.sh itself needs, and nothing that could be a forge.
mkdir -p "$tmp/tools"
for t in bash sed head tr dirname env find uname mkdir chmod mktemp mv rm cat wc; do
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
# A go that can only `go install` the pin: into $GOBIN, a forge that echoes
# its arguments and exits 2 or 3 for the args "exit2" / "exit3". Each call is
# logged to go.calls; FAKE_GO_FAIL makes the build fail.
mkdir -p "$tmp/go"
cat >"$tmp/go/go" <<EOF
#!/usr/bin/env bash
echo "\$*" >>"$tmp/go.calls"
[ "\${1:-} \${2:-}" = "install github.com/reliant-labs/forge/cmd/forge@$pin" ] || { echo "fake go: unexpected \$*" >&2; exit 2; }
[ -n "\${GOBIN:-}" ] && [ "\${GOFLAGS:-}" = -trimpath ] || { echo "fake go: GOBIN or GOFLAGS unset" >&2; exit 1; }
[ -z "\${FAKE_GO_FAIL:-}" ] || { echo "fake go: build failed" >&2; exit 1; }
cat >"\$GOBIN/forge" <<'FORGE'
#!/usr/bin/env bash
echo "go-installed \$*"
case "\${1:-}" in exit2) exit 2 ;; exit3) exit 3 ;; esac
exit 0
FORGE
chmod +x "\$GOBIN/forge"
EOF
chmod +x "$tmp/go/go"
plat=$(uname -s | tr '[:upper:]' '[:lower:]')-$(uname -m)
installed=$XDG_CACHE_HOME/reliant-forge/$pin/$plat/forge

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

check "$installed" "$(which "$proj" "$tmp/at-other:$tmp/go")" "no reliant: the pin, go installed into the user cache"
[ -x "$installed" ] || fail "no reliant: --which printed $installed but did not build it"
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
check "$installed" \
  "$(outside "$tmp/handoff:$tmp/go" --which)" "outside a project, the pinned build not found: go install, not reliant's own forge"

# Running: forge's arguments pass through, and so does its exit status.
check "$tmp/at-pin/forge lint --quiet" "$(cd "$proj" && env -u FORGE_BIN PATH="$tmp/tools:$tmp/at-pin" "$script" lint --quiet)" "arguments"
check "$tmp/rel/reliant forge generate" "$(cd "$proj" && env -u FORGE_BIN PATH="$tmp/tools:$tmp/rel" "$script" generate)" "reliant forge's arguments"
set +e
(cd "$proj" && env -u FORGE_BIN PATH="$tmp/tools:$tmp/at-pin" "$script" exit3 >/dev/null)
rc=$?
set -e
check 3 "$rc" "forge's exit status"

# Through go install: built once, then run directly, so forge's exit status
# arrives as forge's. Under `go run` every non-zero exit came back as 1 (go
# printing `exit status 2`), and test-flake.sh read forge's 2 "no new or
# edited tests" as a failure (reliant#3243's merge-queue regression).
gorun() { # forge.sh's args, run in the project with only the fake go
  (cd "$proj" && env -u FORGE_BIN PATH="$tmp/tools:$tmp/at-other:$tmp/go" "$script" "$@")
}
rm -rf "$XDG_CACHE_HOME" "$tmp/go.calls"
check "go-installed lint --quiet" "$(gorun lint --quiet)" "go install: arguments"
for want in 2 3; do
  set +e
  gorun "exit$want" >/dev/null 2>&1
  rc=$?
  set -e
  check "$want" "$rc" "go install: forge's exit status $want"
done
check 1 "$(wc -l <"$tmp/go.calls" | tr -d ' ')" "go install: built once, then reused"
check "" "$(find "$XDG_CACHE_HOME" -name '.install.*')" "go install: no scratch directory left behind"
rm -rf "$XDG_CACHE_HOME"
set +e
out=$(FAKE_GO_FAIL=1 gorun generate 2>&1)
rc=$?
set -e
[ "$rc" -ne 0 ] || fail "go install fails: exit status 0"
case "$out" in *"go install"*"failed"*) ;; *) fail "go install fails: message '$out' does not say so" ;; esac
[ ! -e "$installed" ] || fail "go install fails: a binary was cached anyway"

echo "PASS: forge.sh"
