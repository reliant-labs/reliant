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
#   npm-*      npm ci (pnpm-* for a pnpm project) where a selected lane needs node_modules it lacks
#   generate   pinned forge generate must be a no-op; only when a forge input changed
#   docs       every docs generator must be a no-op; only when a docs input changed
#   build      compile (go list -export: no link) every package whose build
#              contains a change (the plan's go.build), or ./... on a full Go lane
#   test       tools/test-affected run -plan <the plan> (-short, cached, no
#              -race): the changed packages and their DIRECT importers, the
#              vitest files within two imports of a changed module, and the
#              electron tests when electron/ changed; deeper dependents run in
#              the release gate (MG_REACH=all for all). A change the rules cannot
#              scope (go.mod, package.json, config) runs its whole Go and
#              electron lane; a full web fallback runs tsc here and the whole
#              vitest suite in the release gate. A Go
#              test this change adds or edits that runs over the per-test budget
#              fails it (-test-budget auto)
#   typecheck  scripts/web-typecheck.sh: frontends/web's `tsc -b` (app, tests,
#              gen, node configs), incremental on state this checkout wrote,
#              when the web lane is selected or anything under
#              frontends/web/ changed; vitest strips types without checking
#              them, so this is the only phase that sees a test's type error
#   lint       forge lint's own lanes (named; no golangci-lint or buf lint, which
#              the release gate runs) --scope <changed dirs>, a pin move included;
#              like build and test it starts after the generators in --amend
#              mode, so it reads the amended tree (lint reads generated files
#              such as gen/forge_descriptor.json)
#   migsafety  forge lint --migration-safety, only when a migration or forge.yaml changed
#   migorder   every migration is versioned above origin/main's newest (fetched
#              first, fail closed), only when a migration changed
# When the gate may rewrite the tree it judges (--amend, or a dirty tree),
# generate and docs run FIRST, in place, and only then do build, test,
# typecheck, lint and migsafety run, at the same time, on the regenerated tree:
# a stale generated file (a mock the merge left a step behind) must never be
# built and tested before the regenerate that fixes it. On a clean tree without
# --amend the generators cannot change what is judged, so they run in scratch
# checkouts beside the other phases, except on a host under 32 GiB (a hosted
# runner), where forge generate runs first and alone (the memory budget below).
#
# The whole gate holds ONE host heavy slot (forge slot run), taken here; every
# phase nests inside it and never queues again. Its phases share a CPU budget
# (MG_CPUS) instead of each spawning one thread per core of a host whose other
# slots are busy too. MG_LOGDIR=<dir> keeps the
# phase logs there (default: a fresh directory under ~/.cache/reliant-merge-gate).
set -uo pipefail
cd "$(git rev-parse --show-toplevel)" || exit 2

base="" amend=0 regen=0
while [ $# -gt 0 ]; do
  case "$1" in
    --base) base=$2; shift 2 ;;
    --amend) amend=1; shift ;;
    --regen) regen=1; shift ;;
    -h|--help) sed -n '2,57p' "$0"; exit 0 ;;
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

# ── the gate's CPU budget ─────────────────────────────────────────────────
# Every Go command, go test binary, golangci and vitest otherwise sizes itself
# to the whole machine (32 threads each), and the gate runs six of them at
# once in one slot of eight: at a host load of 150 every gate was CPU-starved,
# tsc (one thread) included. So the gate gets the cores the host has free, but
# never less than half the machine (a floor of twice a slot's share measured
# slower: lint's contract lane alone ran 75s at GOMAXPROCS=2 against 41s for
# all its lanes uncapped, on a host where every other job takes all 32), and
# each concurrent phase a part of that: the test phase (the longest) half for
# Go and half for vitest, the others a quarter. MG_CPUS=<n> overrides it.
cpus=$(nproc 2>/dev/null || echo 4)
load=$(cut -d' ' -f1 /proc/loadavg 2>/dev/null | cut -d. -f1); load=${load:-0}
share=$(( cpus / 2 )); [ "$share" -ge 4 ] || share=4
free=$(( cpus - load ))
gate_cpus=${MG_CPUS:-$(( free > share ? free : share ))}
[ "$gate_cpus" -le "$cpus" ] || gate_cpus=$cpus
half=$(( gate_cpus / 2 )); [ "$half" -ge 2 ] || half=2
quarter=$(( gate_cpus / 4 )); [ "$quarter" -ge 2 ] || quarter=2

# ── the gate's memory budget ──────────────────────────────────────────────
# Cores are not the only limit. Measured on reliant#1772 (208 files, a proto
# change): forge generate alone peaks at 7.5 GB RSS, tsc -b at 2.3 GB, each
# test link at 2.7 GB, and the phases beside one another reach 13 GB within
# five minutes. A hosted ubuntu-latest runner has 16 GiB: the merge queue's
# runner ran out of memory there three times, its agent died ("The runner has
# received a shutdown signal"), and the job sat until its timeout. So on a
# host under MG_GEN_ALONE_GIB (default 32) forge generate runs FIRST and alone,
# with the whole CPU budget, and the other phases start once it is done; on
# a bigger host it still runs beside them. MG_MEM_GIB=<n> overrides the
# detected size (the smaller of physical memory and the cgroup's memory.max).
mem_kib=$(awk '/^MemTotal:/ { print $2 }' /proc/meminfo 2>/dev/null)
[ -n "$mem_kib" ] || mem_kib=$(( $(sysctl -n hw.memsize 2>/dev/null || echo 0) / 1024 ))
cg_max=$(cat /sys/fs/cgroup/memory.max 2>/dev/null || echo max)
case "$cg_max" in ''|max|*[!0-9]*) ;; *) [ $(( cg_max / 1024 )) -lt "$mem_kib" ] && mem_kib=$(( cg_max / 1024 )) ;; esac
mem_gib=${MG_MEM_GIB:-$(( mem_kib / 1048576 ))}
gen_alone=0
# 0 means the size is unknown: keep the concurrent default.
[ "$mem_gib" -gt 0 ] && [ "$mem_gib" -lt "${MG_GEN_ALONE_GIB:-32}" ] && gen_alone=1

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
# Each phase is recorded in $L/expected BEFORE it launches and leaves its .row
# only when it finishes, so a phase that was started and cut off (queued past a
# timeout, cancelled, killed) has no row and fails the gate (gate-verify below).
phase() {
  local n=$1 s e rc; shift
  echo "$n" >>"$L/expected"
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
# mode a change confined to generated paths passes (the generators ran in the
# gate's own tree, which is amended into HEAD before build and test start);
# anything else fails the phase.
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
commit_amend() { git add -A && git -c user.name="${GIT_AUTHOR_NAME:-reliant-merge-gate}" -c user.email="${GIT_AUTHOR_EMAIL:-merge-gate@reliantlabs.io}" commit -q --amend --no-edit --no-verify; }
cleanup() { local wt; for wt in "${scratches[@]}"; do git worktree remove --force "$wt" >/dev/null 2>&1; done; }
scratches=()
trap cleanup EXIT
root=$(pwd)

# gen_forge <checkout>: task generate:forge, exactly as `task generate:check`
# runs it (the pinned forge, FORGE_BIN when set; the pinned protoc-gen-es;
# go.mod's toolchain), then the proto-convention lane it pairs with.
#
# When the plan says hooks_only (every reason is Go that a forge.yaml generate
# hook's generator compiles, and nothing forge itself reads changed), only
# those hooks can write anything different: run just their commands, under
# the same toolchain pin, instead of the whole forge generate (seconds, not
# minutes). The release gate still runs the whole one.
gen_forge() {
  local wt=$1 b h tc
  b=$(cd "$wt" && snapshot)
  if [ "$hooks_only" = true ]; then
    tc=$(sed -n 's/^toolchain //p' go.mod); [ -n "$tc" ] || tc="go$(sed -n 's/^go //p' go.mod)"
    # controlplane-client runs buf, whose 2m deadline must not cover compiling
    # its `go tool` plugins (task generate:forge gets this as a dep).
    case " $hooks " in *" controlplane-client "*) (cd "$wt" && GOMAXPROCS="$gen_procs" task deps:go-protoc-plugins) || return 1 ;; esac
    for h in $hooks; do
      echo "==> generate hook $h: $(jq -r --arg h "$h" '.forge_generate.hook_commands[$h] | join(" ")' "$L/plan.json")"
      (cd "$wt" && jq -r --arg h "$h" '.forge_generate.hook_commands[$h][]' "$L/plan.json" | tr '\n' '\0' | xargs -0 env GOMAXPROCS="$gen_procs" GOWORK=off GOTOOLCHAIN="$tc") || return 1
    done
    judge generate "$b" "$wt"
    return
  fi
  (cd "$wt" && GOMAXPROCS=$gen_procs task generate:forge) || return 1
  judge generate "$b" "$wt" || return 1
  (cd "$wt" && GOMAXPROCS=$gen_procs "${forge[@]}" lint --no-fix --conventions) || { echo "forge lint --conventions failed"; return 1; }
}
gen_docs() {
  local wt=$1 b
  b=$(cd "$wt" && snapshot)
  (cd "$wt" && GOMAXPROCS=$gen_procs task generate:docs) || return 1
  judge docs "$b" "$wt"
}

# ── plan ──────────────────────────────────────────────────────────────────
# The merge tier's Go fan-out: direct importers only (owner, 2026-10-24).
reach=${MG_REACH:-direct}
phase plan go run ./tools/test-affected plan -json -reach "$reach" -base "$base" || { cat "$L/plan.log"; exit 1; }
sed -n '/^{/,$p' "$L/plan.log" >"$L/plan.json"
# Fail closed: without jq, or with a plan it cannot parse, every lane would read
# as "not selected" and be silently skipped.
command -v jq >/dev/null 2>&1 || { echo "merge-gate: jq is not installed, so the plan cannot be read and no selected lane could be checked" >&2; exit 1; }
jq -e . "$L/plan.json" >/dev/null 2>&1 || { echo "merge-gate: $L/plan.json is not valid JSON; refusing to guess what was selected" >&2; cat "$L/plan.log" >&2; exit 1; }
run_plan=$L/plan.json
j() { jq -r "$1" "$run_plan"; }
load_selection() {
  jq -e . "$run_plan" >/dev/null || return 1
  gofull=$(j .go.full) ngo=$(j '.go.packages|length') ndef=$(j '.go.deferred|length') webfull=$(j .web.full) nweb=$(j '.web.tests|length')
  elec=$(j .electron.full) fgen=$(j .forge_generate.full) dgen=$(j .docs_generate.full)
  hooks_only=$(j '.forge_generate.hooks_only // false') hooks=$(j '(.forge_generate.hooks // []) | join(" ")')
  [ "$regen" = 1 ] && { fgen=true; hooks_only=false; }
  files=$( { git diff --name-only "$base"; git ls-files --others --exclude-standard; } | sort -u)
  # vitest strips types: every changed web source needs tsc, tested or not.
  typecheck=false
  if [ "$webfull" = true ] || [ "$nweb" -gt 0 ] || echo "$files" | grep -q '^frontends/web/'; then typecheck=true; fi
}
load_selection || exit 1
notes+=("plan: go $([ "$gofull" = true ] && echo FULL || echo "$ngo package(s), reach $reach, $ndef transitive dependent(s) left to the release gate"); web $([ "$webfull" = true ] && echo FULL || echo "$nweb file(s)"); typecheck $typecheck; electron $elec; forge-generate $fgen$([ "$hooks_only" = true ] && echo " (hooks only: $hooks)"); docs-generate $dgen")

# ── npm installs a selected lane needs ────────────────────────────────────
# npm_install <dir>: npm ci in <dir> unless its install is current; pnpm's
# frozen install for a pnpm project (frontends/web).
npm_install() {
  local d=$1 n scripts
  if [ -f "$d/pnpm-lock.yaml" ]; then
    # pnpm rewrites node_modules/.modules.yaml on every install it completes.
    [ "$d/node_modules/.modules.yaml" -nt "$d/pnpm-lock.yaml" ] && return 0
    n=pnpm-$(echo "$d" | tr '/.' '-r')
    phase "$n" pnpm install --dir "$d" --frozen-lockfile --ignore-scripts --config.confirmModulesPurge=false --reporter=silent
    return
  fi
  [ -f "$d/package-lock.json" ] || return 0
  # An install is current when npm's lockfile copy is newer than the lockfile,
  # and, for electron, its postinstall also fetched the binary: an interrupted
  # install leaves node_modules current but no node_modules/electron/path.txt,
  # and every electron test then fails "Electron failed to install correctly".
  if [ "$d/node_modules/.package-lock.json" -nt "$d/package-lock.json" ] \
    && { [ "$d" != electron ] || [ -f "$d/node_modules/electron/path.txt" ]; }; then
    return 0
  fi
  n=npm-$(echo "$d" | tr '/.' '-r')
  # electron's own install script fetches the Electron binary its tests load.
  scripts=--ignore-scripts; [ "$d" = electron ] && scripts=
  phase "$n" npm ci --prefix "$d" $scripts --no-audit --no-fund --loglevel=error
}
prepare_dependencies() {
  local d p
  local -a need=() npm_pids=()
  if [ "$webfull" = true ] || [ "$nweb" -gt 0 ] || [ "$typecheck" = true ]; then need+=(. frontends/web); fi
  if [ "$elec" = true ]; then need+=(. electron); fi
  if [ "$fgen" = true ] && [ "$hooks_only" != true ]; then need+=(tools/protoc-gen-es); fi
  for d in $(printf '%s\n' "${need[@]}" | sort -u); do
    npm_install "$d" & npm_pids+=($!)
  done
  for p in "${npm_pids[@]}"; do wait "$p" || fail=1; done
}
prepare_dependencies

# ── generate → docs, then build ‖ test ‖ typecheck ‖ lint ‖ migsafety ──────
# Where a generator's output becomes the tree the other phases judge, it runs
# first, in place: in --amend mode (the merge queue's: a regenerate is folded
# into the squash, so build and test must judge the regenerated tree, never
# the stale one the merge left), and in a dirty tree (a scratch checkout of
# HEAD would not see your edits). The phases that read the tree start only
# once the generators are done and, with --amend, their output is committed.
# This costs generate's wall time only when generate is selected at all.
#
# Otherwise (a clean tree without --amend) a regenerate cannot change what is
# judged — any change fails the generate phase — so the generators run in
# scratch checkouts and every phase runs at once.
inplace=0; { [ "$amend" = 1 ] || [ -n "$(git status --porcelain)" ]; } && inplace=1
pids=()
# A generator running first and alone gets the whole budget; one beside the
# other phases (in a scratch checkout), a quarter.
gen_procs=$quarter; [ "$inplace" = 1 ] && gen_procs=$gate_cpus
# Every consumer, including gate-verify, uses the regenerated-tree plan.
if [ "$fail" = 0 ] && [ "$inplace" = 1 ]; then
  [ "$fgen" = true ] && { phase generate gen_forge "$root" || fail=1; }
  [ "$fail" = 0 ] && [ "$dgen" = true ] && { phase docs gen_docs "$root" || fail=1; }
  # --amend: fold what the generators rewrote (judge allowed generated paths
  # only) into HEAD before anything builds or tests it.
  if [ "$amend" = 1 ] && [ "$fail" = 0 ] && [ -n "$(git status --porcelain)" ]; then
    commit_amend || fail=1
    notes+=("regenerated before build and test: they judged the amended tree")
  fi
  # Dirty-tree generation can expand reach too, even without --amend.
  if [ "$fail" = 0 ]; then
    phase plan-2 go run ./tools/test-affected plan -json -reach "$reach" -base "$base" || fail=1
    if [ "$fail" = 0 ]; then
      sed -n '/^{/,$p' "$L/plan-2.log" >"$L/plan-2.json"
      run_plan=$L/plan-2.json
      old_fgen=$fgen old_dgen=$dgen old_hooks_only=$hooks_only old_hooks=$hooks
      load_selection || fail=1
      # A prior hook-only result cannot satisfy a newly full generation lane.
      # Refuse rather than credit old rows for newly introduced obligations.
      if { [ "$fgen" = true ] && [ "$old_fgen" != true ]; } ||
         { [ "$dgen" = true ] && [ "$old_dgen" != true ]; } ||
         { [ "$old_hooks_only" = true ] && { [ "$hooks_only" != true ] || [ "$hooks" != "$old_hooks" ]; }; }; then
        echo "merge-gate: regeneration expanded generator obligations; rerun on the regenerated tree" >&2
        fail=1
      fi
      prepare_dependencies
    fi
  fi
elif [ "$fail" = 0 ]; then
  if [ "$fgen" = true ]; then
    if ! scratch gen; then fail=1
    elif [ "$gen_alone" = 1 ]; then
      # A small host (the memory budget above): generate first, alone.
      notes+=("memory: ${mem_gib} GiB < ${MG_GEN_ALONE_GIB:-32}; forge generate ran alone before the other phases")
      gen_procs=$gate_cpus; phase generate gen_forge "$SCRATCH" || fail=1; gen_procs=$quarter
    else phase generate gen_forge "$SCRATCH" & pids+=($!); fi
  fi
  if [ "$dgen" = true ]; then
    if scratch docs; then phase docs gen_docs "$SCRATCH" & pids+=($!); else fail=1; fi
  fi
fi
# build_check: compile, without linking, every package whose build contains a
# change. `go build ./...` also linked every main package of the module (the
# server, the CLI, a dozen generators): 41s with every package already
# compiled, against 6s for this; the link check runs in the release gate's
# build. A full Go lane (a pin moved) compiles ./... . go list -export compiles
# each package for its export data and exits non-zero naming any that does not
# compile, main packages included.
build_check() {
  local pkgs
  if [ "$gofull" = true ]; then pkgs=(./...); else mapfile -t pkgs < <(jq -r '.go.build // [] | .[]' "$run_plan"); fi
  if [ ${#pkgs[@]} -eq 0 ]; then echo "build: no Go package's build contains a change"; return 0; fi
  echo "build: compiling ${pkgs[*]:0:20}$([ ${#pkgs[@]} -gt 20 ] && echo " ... (${#pkgs[@]} packages)")"
  # Errors go to stderr; the package list on stdout is noise.
  GOMAXPROCS=$quarter go list -export "${pkgs[@]}" >/dev/null
}
# The merge tier runs forge lint's own lanes, named, over the changed
# directories; golangci-lint, buf lint and the whole-project and deploy lanes
# run in the release gate's unscoped `forge lint`. Measured on one loaded host,
# the same two-directory scope: every default lane 331s (golangci the bulk;
# 405s average, 2735s unscoped on a pin move, in the merge queue), these lanes
# 41s. A pin move no longer unscopes it: what a new forge or module version
# breaks across the module is the release gate's to find; the regenerated
# files a pin bump commits are in the diff, so they are in scope here.
lint_lanes=(--contract --conventions --generated-drift --go-markers --proto-markers
  --create-nullability --computed-fields --read-only-fields --db-grants
  --column-markers --closed-sets --tracing --guarded-fields)
# The whole vitest suite (1033 files, ~20 min on a loaded host) is a release
# blocker, not a merge blocker (owner, 2026-10-28: slow suites gate releases;
# the merge gate aims at five minutes). A full web fallback still type-checks
# every web source here; its tests run in the release gate's `task test`.
test_lanes=go,web,electron
if [ "$webfull" = true ]; then
  test_lanes=go,electron
  notes+=("web: FULL fallback; tsc -b runs here, the whole vitest suite in the release gate")
fi
if [ "$fail" = 0 ]; then
  phase build build_check & pids+=($!)
  phase test env GOMAXPROCS="$half" VITEST_MAX_WORKERS="$half" \
    go run ./tools/test-affected run -plan "$run_plan" -base "$base" -lanes "$test_lanes" -p "$half" -test-budget "${MG_TEST_BUDGET:-auto}" & pids+=($!)
  if [ "$typecheck" = true ]; then
    phase typecheck scripts/web-typecheck.sh & pids+=($!)
  fi
  # One awk, not a dirname per file: forking hundreds of processes on a
  # loaded host delayed lint's start by over a minute.
  scope=$(echo "$files" | awk -F/ 'NF > 1 { NF--; print }' OFS=/ | sort -u | while read -r d; do [ -d "$d" ] && echo "$d"; done | paste -sd, -)
  if [ -n "$scope" ]; then
    notes+=("lint: forge's own lanes over $(echo "$scope" | tr ',' '\n' | wc -l | tr -d ' ') changed dir(s); golangci-lint and buf lint run in the release gate")
    phase lint env GOMAXPROCS="$quarter" "${forge[@]}" lint --quiet --no-fix --skip-frontends --scope "$scope" "${lint_lanes[@]}" & pids+=($!)
  else
    notes+=("lint: not run (only root files changed; the release gate lints the module)")
  fi
  phase proto task proto:lint & pids+=($!)
  # Build reach includes ordinary production packages with no test binary.
  # Keep test-only changes too: vet checks their tests even when Build is empty.
  if [ "$gofull" = true ]; then vet_pkgs=(./...); else
    mapfile -t vet_pkgs < <(jq -r '((.go.build // []) + (.go.packages // [])) | unique | .[]' "$run_plan")
  fi
  if [ ${#vet_pkgs[@]} -gt 0 ]; then
    phase vet env GOMAXPROCS="$quarter" go vet "${vet_pkgs[@]}" & pids+=($!)
  fi
  if echo "$files" | grep -qE '^db/migrations/|^forge\.yaml$'; then
    phase migsafety "${forge[@]}" lint --migration-safety --no-fix & pids+=($!)
  fi
  # A migration below main's newest is never applied and reliant's migrator
  # refuses to start on it (reliant#1399). The check fetches origin/main
  # itself, so it judges main as it is now, not as this clone last saw it.
  if echo "$files" | grep -qE '^db/migrations/'; then
    phase migorder go run ./tools/test-affected migration-order & pids+=($!)
  fi
  for p in "${pids[@]}"; do wait "$p" || fail=1; done
fi
# --amend: a failed gate leaves no half-regenerated tree behind.
if [ "$amend" = 1 ] && [ "$fail" != 0 ] && [ -n "$(git status --porcelain)" ]; then
  git reset -q --hard; git clean -fdq
fi
[ -f "$L/notes" ] && while IFS= read -r n; do notes+=("$n"); done <"$L/notes"

# ── fail closed: every phase the plan selected must have COMPLETED ────────
# A selected lane that never ran (queued for a slot, cancelled, timed out, tool
# missing) leaves no result row; build/test green around it would otherwise
# pass (reliant#1393 merged with docs-generate still queued). gate-verify fails
# it by name. Its own failure to run is a failure too.
if go run ./tools/test-affected gate-verify -plan "$run_plan" -base "$base" -dir "$L" >"$L/verify.log" 2>&1; then :; else
  fail=1
  [ -f "$L/verdict" ] || echo "FAILED: gate-verify could not run" >"$L/verdict"
  notes+=("$(head -c 300 "$L/verdict")")
fi

# ── summary ───────────────────────────────────────────────────────────────
end=$(date +%s)
{
  go run ./tools/test-affected gate-report -dir "$L" -wall "$((end - t0))" || { echo 'GATE-HEALTH: report failed'; fail=1; }
  echo "### merge-gate: checks $([ "$fail" = 0 ] && echo PASSED || echo FAILED) (timing reported separately)"
  echo
  echo "| phase | result | seconds | started +s |"
  echo "|---|---|---|---|"
  echo "| heavy slot (queue) | - | $slot_wait | - |"
  for f in plan npm-r pnpm-frontends-web npm-electron npm-tools-protoc-gen-es generate docs plan-2 build test typecheck lint proto vet migsafety migorder; do
    [ -f "$L/$f.row" ] || continue
    read -r n rc s st <"$L/$f.row"
    echo "| $n | $([ "$rc" = 0 ] && echo PASS || echo "FAIL ($rc)") | $s | $st |"
  done
  echo
  echo "wall: $((end - t0))s in the slot, after ${slot_wait}s queued for it; diff base $(git rev-parse --short "$base")"
  for n in "${notes[@]}"; do echo "- $n"; done
  echo "head: $(git rev-parse --short HEAD)  logs: $L"
} >"$L/summary.md"
cat "$L/summary.md"
exit "$fail"
