#!/usr/bin/env bash
# forge.sh — run the forge the project pins (forge.yaml's forge_version), from
# wherever this machine has it. Every task and script that runs forge goes
# through here (Taskfile.yml's FORGE and SLOT), and internal/forgetest resolves
# the same way for the Go tests that run forge.
#
#   scripts/forge.sh <forge args>             run the pinned forge, with this process's stdio and exit status
#   scripts/forge.sh --which                  print the command it would run, and nothing else
#   scripts/forge.sh --machine <forge args>   run any forge, for a machine-wide command (slot)
#   scripts/forge.sh --machine --which        print that command
#
# The project is the one around the working directory (the nearest forge.yaml
# above it), so a script that cd's into ../control-plane gets control-plane's
# pin; with none above, this checkout's. The pinned forge, in order:
#
#   1. $FORGE_BIN, as given: the override for forge developers and for a gate
#      that built the pin once (release-gate.sh's pinned_forge_bin).
#   2. forge on PATH, then the project's .tools/bin/forge, when its --version
#      names the pin. Any forge for a pin that names no published forge (`dev`,
#      a +dirty build): it hands the command to the pin when it can.
#   3. The pin through reliant, when reliant is on PATH. Inside the project,
#      `reliant forge`: it hands the command to the pinned forge (its own
#      cache, or a build of the pin), and fails rather than run another forge
#      when the pin cannot be had. Outside it (a script in a temp dir run with
#      this checkout's pin) `reliant forge` would run the forge built into
#      reliant instead, so it is that only when reliant's forge IS the pin, and
#      otherwise the pinned build reliant's hand-off keeps (reliant_pinned).
#   4. go install …/cmd/forge@<pin>, for a published pin: what a CI runner,
#      which has Go and neither forge nor reliant, runs. It builds the pin
#      once into <user cache>/reliant-forge/<pin>/<os>-<arch>/forge
#      (${XDG_CACHE_HOME:-$HOME/.cache}; never the repo) and execs that, so
#      forge's own exit status reaches the caller. Not `go run`: it reports
#      ANY non-zero exit of the program as its own exit 1 (printing `exit
#      status N`), so every caller that branches on forge's exit codes —
#      test-flake.sh's 2 "no new or edited tests" — read a 1 instead.
#      --which builds it too, so the path it prints runs.
#
# Nothing found: it says what to install and exits 127.
#
# --machine is for the commands that are about this machine, not the project
# (`slot`: the heavy slots every project and forge version share). Any forge
# answers those, and they run on every `task`, so they must cost nothing: the
# same order without the version check, and never a `go install`.
#
# Self-test: scripts/test-forge.sh.
set -euo pipefail

FORGE_MOD=github.com/reliant-labs/forge

machine=0
if [ "${1:-}" = --machine ]; then
  machine=1
  shift
fi
which=0
if [ "${1:-}" = --which ]; then
  which=1
  shift
fi

project_root() {
  local d=$PWD
  while :; do
    if [ -f "$d/forge.yaml" ]; then echo "$d"; return; fi
    [ "$d" != / ] || break
    d=$(dirname "$d")
  done
  cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd
}

root=$(project_root)
# in_project: the working directory is inside the project, where `reliant
# forge` hands the command to the pin.
in_project=0
case "$PWD/" in "$root"/*) in_project=1 ;; esac
pin=$({ sed -n 's/^forge_version:[[:space:]]*//p' "$root/forge.yaml" 2>/dev/null || true; } |
  head -1 | sed 's/[[:space:]]#.*//' | tr -d "\"' \t\r")
published=1
case "$pin" in "" | dev | *+*) published=0 ;; esac

# is_pin: the forge "$@" runs reports the pinned version (any forge, for an
# unpublished pin or a machine-wide command). `forge --version` prints
# "forge version <v> …".
is_pin() {
  { [ "$machine" = 0 ] && [ "$published" = 1 ]; } || return 0
  local word
  for word in $("$@" --version 2>/dev/null); do
    [ "$word" = "$pin" ] && return 0
  done
  return 1
}

# reliant_pinned: the pinned build reliant's hand-off keeps, for a run outside
# the project. A no-op project command run in the project root (`lint --help`;
# `version` and `help` are never handed off) makes reliant obtain the pin from
# its cache, or build it once; then the executable under that cache that
# reports the pin is the forge. The cache ($FORGE_PINNED_CACHE, else <user
# cache dir>/forge/pinned) is forge's hand-off detail, not an interface:
# neither reliant nor forge prints the pinned forge's path. The version check
# means a moved cache fails over to the next step, never runs another forge.
reliant_pinned() {
  local err cache=${FORGE_PINNED_CACHE:-} f
  if ! err=$( (cd "$root" && reliant forge lint --help 2>&1 >/dev/null) ); then
    printf '%s\n' "$err" >&2
    return 1
  fi
  if [ -z "$cache" ]; then
    case "$(uname -s)" in
      Darwin) cache=$HOME/Library/Caches/forge/pinned ;;
      *) cache=${XDG_CACHE_HOME:-$HOME/.cache}/forge/pinned ;;
    esac
  fi
  while IFS= read -r f; do
    if is_pin "$f"; then
      cmd=("$f")
      return 0
    fi
  done < <(find "$cache" -maxdepth 3 -type f \( -name forge -o -name forge.exe \) -path "*$pin*" 2>/dev/null)
  return 1
}

# go_installed: the pin, built by `go install` once per machine into a cache
# directory keyed by the pin and platform, then run directly (step 4 above).
# The build lands in a fresh directory beside the final path and is renamed
# into place, so a concurrent run sees either no binary or a whole one.
go_installed() {
  local dir bin tmp
  dir=${XDG_CACHE_HOME:-$HOME/.cache}/reliant-forge/$pin/$(uname -s | tr '[:upper:]' '[:lower:]')-$(uname -m)
  bin=$dir/forge
  if [ ! -x "$bin" ]; then
    mkdir -p "$dir" || exit 1
    tmp=$(mktemp -d "$dir/.install.XXXXXX") || exit 1
    # GOPRIVATE: a fresh pseudo-version is not on proxy.golang.org yet
    # (scripts/pin-forge.sh). GOFLAGS is just -trimpath because `go install
    # pkg@version` refuses a repo-level -mod setting. Its output goes to
    # stderr: stdout is forge's (and --which's).
    if ! env GOBIN="$tmp" GOPRIVATE="${GOPRIVATE:-github.com/reliant-labs/*}" GOFLAGS=-trimpath \
      go install "$FORGE_MOD/cmd/forge@$pin" >&2; then
      rm -rf "$tmp"
      echo "forge.sh: go install $FORGE_MOD/cmd/forge@$pin failed" >&2
      exit 1
    fi
    mv -f "$tmp/forge" "$bin" || { rm -rf "$tmp"; exit 1; }
    rm -rf "$tmp"
  fi
  cmd=("$bin")
}

resolve() {
  if [ -n "${FORGE_BIN:-}" ]; then
    cmd=("$FORGE_BIN")
    return
  fi
  local f
  for f in "$(command -v forge 2>/dev/null || true)" "$root/.tools/bin/forge"; do
    if [ -n "$f" ] && [ -x "$f" ] && is_pin "$f"; then
      cmd=("$f")
      return
    fi
  done
  if command -v reliant >/dev/null 2>&1; then
    if [ "$machine" = 1 ] || [ "$published" = 0 ] || [ "$in_project" = 1 ] || is_pin reliant forge; then
      cmd=(reliant forge)
      return
    fi
    if reliant_pinned; then
      return
    fi
    echo "forge.sh: reliant has no build of forge $pin to run outside $root; trying go install" >&2
  fi
  if [ "$machine" = 0 ] && [ "$published" = 1 ] && command -v go >/dev/null 2>&1; then
    go_installed
    return
  fi
  if [ "$machine" = 1 ]; then
    echo "forge.sh: no forge on this machine: set FORGE_BIN, or put forge or reliant on PATH" >&2
  else
    echo "forge.sh: no forge to run for $root (pin ${pin:-none}): set FORGE_BIN," \
      "put forge ${pin:-<any>} on PATH, or install reliant (its \`reliant forge\` runs the pin)" >&2
  fi
  exit 127
}

cmd=()
resolve
if [ "$which" = 1 ]; then
  echo "${cmd[*]}"
  exit 0
fi
exec "${cmd[@]}" "$@"
