#!/usr/bin/env bash
# merge-gate.sh — reliant's MERGE tier: only what this change can affect, in
# about five minutes. `task ci:merge` runs it on your branch; the merge queue
# runs it on the squash it is about to land. What it leaves out runs in the
# release gate (scripts/release-gate.sh), on main, before a release is cut.
# dev-docs/merge-and-release-gates.md is the design and the trade-offs.
#
#   scripts/merge-gate.sh [--base <rev>] [--amend] [--regen]
#
#   --base <rev>  diff base (default: the merge-base of HEAD and origin/main)
#   --amend       a regenerate that changes only generated paths is amended
#                 into HEAD instead of failing (the merge queue's mode: the
#                 merge with main can leave generated files a step behind)
#   --regen       run forge generate even if no forge input changed (the
#                 merge queue resolved a generated-file conflict)
#
# Phases, each a row of the summary (slot wait vs run, seconds):
#   plan       tools/test-affected plan: the ONE rule set (dev-docs/testing-affected.md)
#   npm-*      npm ci where a selected lane needs node_modules it lacks
#   generate   pinned forge generate must be a no-op; only when a forge input changed
#   docs       every docs generator must be a no-op; only when a docs input changed
#   build      go build ./...
#   test       tools/test-affected run -lanes go,web,electron (-short, cached, no -race)
#   lint       forge lint --scope <changed dirs> (unscoped when a pin moved)
#   migsafety  forge lint --migration-safety, only when a migration changed
# generate and docs run first, because they can change the tree the others
# judge; build, test, lint and migsafety then run at the same time.
#
# The whole gate holds ONE host heavy slot (forge slot run), taken here; every
# phase nests inside it and never queues again. MG_LOGDIR=<dir> keeps the
# phase logs there (default: a fresh directory under ~/.cache/reliant-merge-gate).
set -uo pipefail
cd "$(git rev-parse --show-toplevel)" || exit 2

base="" amend=0 regen=0
while [ $# -gt 0 ]; do
  case "$1" in
    --base) base=$2; shift 2 ;;
    --amend) amend=1; shift ;;
    --regen) regen=1; shift ;;
    -h|--help) sed -n '2,32p' "$0"; exit 0 ;;
    *) echo "merge-gate: unknown argument $1" >&2; exit 2 ;;
  esac
done

export GOFLAGS="${GOFLAGS:+$GOFLAGS }-trimpath" GOPRIVATE="${GOPRIVATE:-github.com/reliant-labs/*}"
# A gate must not rewrite the clone's git config (forge's merge driver).
export FORGE_NO_MERGE_DRIVER=1

# ── one heavy slot for the whole gate ─────────────────────────────────────
# Slots are machine-wide, so any forge with `forge slot` will do: FORGE_BIN
# (the pin) when set, else the forge on PATH.
slotforge=${FORGE_BIN:-forge}
if [ -z "${FORGE_SLOT_HELD:-}" ] && [ -z "${MG_IN_SLOT:-}" ] && "$slotforge" slot status --class heavy >/dev/null 2>&1; then
  MG_SLOT_T0=$(date +%s) MG_IN_SLOT=1 exec "$slotforge" slot run --class heavy -- "$0" ${base:+--base "$base"} $([ "$amend" = 1 ] && echo --amend) $([ "$regen" = 1 ] && echo --regen)
fi
t0=$(date +%s)
slot_wait=$(( t0 - ${MG_SLOT_T0:-$t0} ))

[ -n "$base" ] || base=$(git merge-base HEAD origin/main) || { echo "merge-gate: no merge-base with origin/main (fetch it, or pass --base)" >&2; exit 2; }
base=$(git rev-parse "$base")
L=${MG_LOGDIR:-${XDG_CACHE_HOME:-$HOME/.cache}/reliant-merge-gate/$(date -u +%Y%m%dT%H%M%SZ)-$$}
mkdir -p "$L"
echo "$slot_wait" >"$L/slot_wait"
pin=$(sed -n 's/^forge_version:[[:space:]]*//p' forge.yaml | head -1)
if [ -n "${FORGE_BIN:-}" ]; then forge=("$FORGE_BIN"); else forge=(env GOFLAGS=-trimpath go run "github.com/reliant-labs/forge/cmd/forge@$pin"); fi
# Paths generators write. A regenerate in --amend mode may change only these.
gen_re='^gen/|^generated/|^docs/reference/[^/]*\.mdx$|^docs/changelog\.mdx$|^frontends/web/src/gen/|^\.forge/|^deploy/observability/|_gen(_test)?\.(go|json|ndjson|ts|k)$|^frontends/web/src/store/shortcutsData\.generated\.ts$|^internal/workflow/runtime/schema/field_descriptions\.go$|^internal/skills/catalog/builtin/workflow-builder/SKILL\.md$'

rows=() notes=() fail=0
# phase <name> <cmd>...: run one phase, timed, output to $L/<name>.log.
phase() {
  local n=$1 s e rc; shift
  s=$(date +%s)
  "$@" >"$L/$n.log" 2>&1; rc=$?
  e=$(date +%s)
  echo "$n $rc $((e - s)) $((s - t0))" >"$L/$n.row"
  return $rc
}
snapshot() {
  git ls-files -z --modified --others --exclude-standard | sort -zu |
    while IFS= read -r -d '' f; do
      if [ -e "$f" ] || [ -L "$f" ]; then printf '%s\t%s\n' "$f" "$(git hash-object --no-filters -- "$f")"; else printf '%s\tdeleted\n' "$f"; fi
    done
}
# judge <phase> <snapshot before> <checkout>: did the generator change <checkout>?
# Writes $L/<phase>.changed (the paths) and $L/<phase>.wt (where). In --amend
# mode a change confined to generated paths passes, and is copied into the
# gate's own tree and amended once every phase has finished (never while build
# and test are reading it); anything else fails the phase.
judge() {
  local n=$1 before=$2 wt=$3 changed bad
  changed=$(cd "$wt" && diff <(printf '%s\n' "$before") <(snapshot) | awk -F'\t' '/^[<>] / { print substr($1, 3) }' | sort -u | grep -v '^$' | grep -v '\(^\|/\)node_modules$' || true)
  [ -z "$changed" ] && return 0
  echo "$changed" >"$L/$n.changed"; echo "$wt" >"$L/$n.wt"
  bad=$(echo "$changed" | grep -Ev "$gen_re" || true)
  if [ "$amend" = 1 ] && [ -z "$bad" ]; then
    echo "$n: regenerated $(echo "$changed" | wc -l | tr -d ' ') generated file(s); amended into HEAD" >>"$L/notes"
    return 0
  fi
  {
    echo "$n is not a no-op: it changed $(echo "$changed" | wc -l | tr -d ' ') file(s):"
    echo "$changed" | sed 's/^/  /'
    [ -n "$bad" ] && [ "$amend" = 1 ] && { echo "of which NOT generated paths (the branch must commit them):"; echo "$bad" | sed 's/^/  /'; }
    echo "Run it and commit what it writes, in the change that caused it."
  } >>"$L/$n.log"
  return 1
}

# scratch <name>: a private detached checkout of HEAD under $L, for a
# generator that writes the tree, so it can run beside the phases that read it.
# node_modules a generator needs is copied from the gate's own tree.
# Sets SCRATCH (not a subshell: cleanup must see every checkout made).
scratch() {
  local d
  SCRATCH="$L/wt-$1"
  git worktree add -q --detach "$SCRATCH" HEAD >/dev/null 2>&1 || return 1
  scratches+=("$SCRATCH")
  for d in tools/protoc-gen-es; do
    [ -d "$root/$d/node_modules" ] || continue
    # A copy, not a link: an install in the scratch checkout must never
    # reach through into the gate's own tree. The fresh checkout's lockfile
    # is newer than the copied install's stamp, which would make
    # deps:protoc-gen-es reinstall: give it the original's mtime.
    cp -a "$root/$d/node_modules" "$SCRATCH/$d/node_modules"
    touch -r "$root/$d/package-lock.json" "$SCRATCH/$d/package-lock.json"
  done
  return 0
}
cleanup() { local wt; for wt in "${scratches[@]}"; do git worktree remove --force "$wt" >/dev/null 2>&1; done; }
scratches=()
trap cleanup EXIT
root=$(pwd)

# gen_forge <checkout>: task generate:forge, exactly as `task generate:check`
# runs it (the pinned forge, FORGE_BIN when set; the pinned protoc-gen-es;
# go.mod's toolchain), then the proto-convention lane it pairs with.
gen_forge() {
  local wt=$1 b
  b=$(cd "$wt" && snapshot)
  (cd "$wt" && task generate:forge) || return 1
  judge generate "$b" "$wt" || return 1
  (cd "$wt" && "${forge[@]}" lint --no-fix --conventions) || { echo "forge lint --conventions failed"; return 1; }
}
gen_docs() {
  local wt=$1 b
  b=$(cd "$wt" && snapshot)
  (cd "$wt" && task generate:docs) || return 1
  judge docs "$b" "$wt"
}

# ── plan ──────────────────────────────────────────────────────────────────
phase plan go run ./tools/test-affected plan -json -base "$base" || { cat "$L/plan.log"; exit 1; }
sed -n '/^{/,$p' "$L/plan.log" >"$L/plan.json"
j() { jq -r "$1" "$L/plan.json"; }
gofull=$(j .go.full) ngo=$(j '.go.packages|length') webfull=$(j .web.full) nweb=$(j '.web.tests|length')
elec=$(j .electron.full) fgen=$(j .forge_generate.full) dgen=$(j .docs_generate.full)
[ "$regen" = 1 ] && fgen=true
notes+=("plan: go $([ "$gofull" = true ] && echo FULL || echo "$ngo package(s)"); web $([ "$webfull" = true ] && echo FULL || echo "$nweb file(s)"); electron $elec; forge-generate $fgen; docs-generate $dgen")

# ── npm installs a selected lane needs ────────────────────────────────────
need=()
if [ "$webfull" = true ] || [ "$nweb" -gt 0 ]; then need+=(. frontends/web); fi
if [ "$elec" = true ]; then need+=(. electron); fi
if [ "$fgen" = true ]; then need+=(tools/protoc-gen-es); fi
for d in $(printf '%s\n' "${need[@]}" | sort -u); do
  [ -f "$d/package-lock.json" ] || continue
  [ "$d/node_modules/.package-lock.json" -nt "$d/package-lock.json" ] && continue
  n=npm-$(echo "$d" | tr '/.' '-r')
  # electron's own install script fetches the Electron binary its tests load.
  scripts=--ignore-scripts; [ "$d" = electron ] && scripts=
  phase "$n" npm ci --prefix "$d" $scripts --no-audit --no-fund --loglevel=error || fail=1
done

# ── generate ‖ docs ‖ build ‖ test ‖ lint ‖ migsafety ──────────────────────
# A clean tree (the merge queue's case) runs the generators in scratch
# checkouts, so every phase runs at once. A dirty one runs them in place,
# first, because a scratch checkout of HEAD would not see your edits.
inplace=0; [ -n "$(git status --porcelain)" ] && inplace=1
files=$( { git diff --name-only "$base"; git ls-files --others --exclude-standard; } | sort -u)
pids=()
if [ "$fail" = 0 ] && [ "$inplace" = 1 ]; then
  [ "$fgen" = true ] && { phase generate gen_forge "$root" || fail=1; }
  [ "$fail" = 0 ] && [ "$dgen" = true ] && { phase docs gen_docs "$root" || fail=1; }
elif [ "$fail" = 0 ]; then
  if [ "$fgen" = true ]; then
    if scratch gen; then phase generate gen_forge "$SCRATCH" & pids+=($!); else fail=1; fi
  fi
  if [ "$dgen" = true ]; then
    if scratch docs; then phase docs gen_docs "$SCRATCH" & pids+=($!); else fail=1; fi
  fi
fi
run_checks() {
  local sfx=${1:-}
  phase "build$sfx" go build ./... & pids+=($!)
  phase "test$sfx" go run ./tools/test-affected run -base "$base" -lanes go,web,electron & pids+=($!)
}
if [ "$fail" = 0 ]; then
  run_checks
  if echo "$files" | grep -qE '^(go\.(mod|sum)|forge\.yaml)$'; then
    notes+=("lint: unscoped (a pin moved)")
    phase lint "${forge[@]}" lint --quiet --no-fix --skip-frontends & pids+=($!)
  else
    scope=$(echo "$files" | grep / | xargs -r -n1 dirname | sort -u | while read -r d; do [ -d "$d" ] && echo "$d"; done | paste -sd, -)
    if [ -n "$scope" ]; then
      notes+=("lint: --scope over $(echo "$scope" | tr ',' '\n' | wc -l | tr -d ' ') changed dir(s)")
      phase lint "${forge[@]}" lint --quiet --no-fix --skip-frontends --scope "$scope" & pids+=($!)
    else
      notes+=("lint: not run (only root files changed)")
    fi
  fi
  if echo "$files" | grep -qE '^db/migrations/|^forge\.yaml$'; then
    phase migsafety "${forge[@]}" lint --migration-safety --no-fix & pids+=($!)
  fi
  for p in "${pids[@]}"; do wait "$p" || fail=1; done
fi
# --amend: fold what the generators rewrote (generated paths only) into HEAD.
# build and test judged the tree before it; when the regenerated files include
# code, run them again on the amended tree.
if [ "$amend" = 1 ] && [ "$fail" = 0 ]; then
  for n in generate docs; do
    [ -f "$L/$n.changed" ] || continue
    wt=$(cat "$L/$n.wt"); [ "$wt" = "$root" ] && continue
    while IFS= read -r f; do
      if [ -e "$wt/$f" ]; then mkdir -p "$(dirname "$f")" && cp -p "$wt/$f" "$f"; else rm -f "$f"; fi
    done <"$L/$n.changed"
  done
fi
commit_amend() { git add -A && git -c user.name="${GIT_AUTHOR_NAME:-reliant-merge-gate}" -c user.email="${GIT_AUTHOR_EMAIL:-merge-gate@reliantlabs.io}" commit -q --amend --no-edit --no-verify; }
if [ "$amend" = 1 ] && [ -n "$(git status --porcelain)" ]; then
  if [ "$fail" = 0 ]; then
    # The docs generators compile what generate just rewrote: re-judge them,
    # in place and before the re-check (they may rewrite code too).
    if [ "$dgen" = true ] && grep -qE '\.(go|proto)$' "$L/generate.changed" 2>/dev/null; then
      rm -f "$L/docs.changed"; b=$(snapshot)
      phase docs-2 bash -c "task generate:docs" || fail=1
      [ "$fail" = 0 ] && { judge docs "$b" "$root" || fail=1; }
    fi
    if [ "$fail" = 0 ]; then
      commit_amend
      if cat "$L"/*.changed 2>/dev/null | grep -qE '\.(go|ts|tsx|proto)$'; then
        notes+=("regenerated code: build and test ran again on the amended tree")
        pids=(); run_checks -2; for p in "${pids[@]}"; do wait "$p" || fail=1; done
      fi
    fi
  fi
  [ "$fail" = 0 ] || { git reset -q --hard; git clean -fdq; }
fi
[ -f "$L/notes" ] && while IFS= read -r n; do notes+=("$n"); done <"$L/notes"

# ── summary ───────────────────────────────────────────────────────────────
end=$(date +%s)
{
  echo "### merge-gate: $([ "$fail" = 0 ] && echo PASSED || echo FAILED)"
  echo
  echo "| phase | result | seconds | started +s |"
  echo "|---|---|---|---|"
  echo "| heavy slot (queue) | - | $slot_wait | - |"
  for f in plan npm-r npm-frontends-web npm-electron npm-tools-protoc-gen-es generate docs build test lint migsafety build-2 test-2 docs-2; do
    [ -f "$L/$f.row" ] || continue
    read -r n rc s st <"$L/$f.row"
    echo "| $n | $([ "$rc" = 0 ] && echo PASS || echo "FAIL ($rc)") | $s | $st |"
  done
  echo
  echo "wall: $((end - t0))s in the slot, after ${slot_wait}s queued for it; diff base $(git rev-parse --short "$base")"
  for n in "${notes[@]}"; do echo "- $n"; done
  echo "head: $(git rev-parse --short HEAD)  logs: $L"
} >"$L/summary.md"
if [ "$fail" != 0 ]; then
  for f in "$L"/*.row; do
    read -r n rc _ _ <"$f"
    [ "$rc" = 0 ] && continue
    echo "── $n failed (rc $rc), last lines of $L/$n.log:"
    grep -v '^ok \|no test files\|^[[:space:]]*$' "$L/$n.log" | tail -25 | sed 's/^/   /'
  done
fi
cat "$L/summary.md"
exit "$fail"
