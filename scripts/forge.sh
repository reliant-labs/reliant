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
#   3. reliant forge, when reliant is on PATH. Inside a project it hands the
#      command to the pinned forge (its own cache, or a build of the pin), and
#      fails rather than run another forge when the pin cannot be had.
#   4. go run …/cmd/forge@<pin>, for a published pin: what a CI runner, which
#      has Go and neither forge nor reliant, runs.
#
# Nothing found: it says what to install and exits 127.
#
# --machine is for the commands that are about this machine, not the project
# (`slot`: the heavy slots every project and forge version share). Any forge
# answers those, and they run on every `task`, so they must cost nothing: the
# same order without the version check, and never a `go run`.
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
pin=$({ sed -n 's/^forge_version:[[:space:]]*//p' "$root/forge.yaml" 2>/dev/null || true; } |
  head -1 | sed 's/[[:space:]]#.*//' | tr -d "\"' \t\r")
published=1
case "$pin" in "" | dev | *+*) published=0 ;; esac

# is_pin: the forge at $1 reports the pinned version (any forge, for an
# unpublished pin or a machine-wide command). `forge --version` prints
# "forge version <v> …".
is_pin() {
  { [ "$machine" = 0 ] && [ "$published" = 1 ]; } || return 0
  local word
  for word in $("$1" --version 2>/dev/null); do
    [ "$word" = "$pin" ] && return 0
  done
  return 1
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
    cmd=(reliant forge)
    return
  fi
  if [ "$machine" = 0 ] && [ "$published" = 1 ] && command -v go >/dev/null 2>&1; then
    # GOPRIVATE: a fresh pseudo-version is not on proxy.golang.org yet
    # (scripts/pin-forge.sh). GOFLAGS is just -trimpath because `go run
    # pkg@version` refuses a repo-level -mod setting.
    cmd=(env GOPRIVATE="${GOPRIVATE:-github.com/reliant-labs/*}" GOFLAGS=-trimpath go run "$FORGE_MOD/cmd/forge@$pin")
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
