#!/usr/bin/env bash
# merge-gate.sh — reliant's MERGE tier: only what this change can affect, in
# about five minutes. `task ci:merge` runs it on your branch; the merge queue
# runs it on the squash it is about to land. What it leaves out runs in the
# release gate (scripts/release-gate.sh), on main, before a release is cut.
# dev-docs/merge-and-release-gates.md is the design and the trade-offs.
#
#   scripts/merge-gate.sh [--base <rev>] [--amend] [--regen] [--skip-lint] [--skip-golangci] [--skip-vitest] [--queue]
#
#   --base <rev>  diff base (default: the merge-base of HEAD and origin/main)
#   --amend       a regenerate that changes only generated paths is amended
#                 into HEAD instead of failing (the merge queue's mode: the
#                 merge with main can leave generated files a step behind)
#   --regen       run forge generate even if no forge input changed (the
#                 merge queue resolved a generated-file conflict)
#   --skip-lint   leave vet and the scoped forge lint to the release gate,
#                 which runs both over the whole module; nothing else changes:
#                 the diff base, golangci, buf lint and the compat checks are
#                 the merge tier's (control-plane's pr-fix gates a PR branch
#                 with it, control-plane#2189). golangci stays because the
#                 queue's golangci (changed) job ejects a PR on its findings.
#   --skip-golangci  leave golangci to a golangci (changed) job beside this
#                 gate (ci.yml's ci:run pull request runs one); nothing else
#                 changes. --queue implies it.
#   --skip-vitest leave the selected vitest files to the merge queue's `vitest`
#                 job beside this gate: 4 legs, each `scripts/vitest-shard.sh`,
#                 vitest --shard over the SAME plan's file list (#2935 reached
#                 ~1100 files, more than one runner runs in 30 minutes). The
#                 test phase keeps the Go and electron lanes; typecheck stays.
#                 Only where that job runs: `CI result` requires it.
#   --queue       the merge queue's tier (.github/workflows/merge-queue.yml),
#                 whose budget is five minutes of wall time (owner, 2026-11-01):
#                 vet is left to the release gate and golangci to the queue's
#                 golangci (changed) job, which runs the same script beside
#                 this gate on its own runner, plus queue-entry
#                 semantics: buf lint runs only when a proto or buf config
#                 changed, and check-compat.sh (CI_QUEUE_ENTRY=1) judges only
#                 the entry's own single-parent squash against HEAD^, so never
#                 pass it on a PR branch. Build, the affected tests, typecheck,
#                 the scoped forge lint, the demo build, generate, docs,
#                 migsafety and migorder stay. It used to imply --skip-lint, so
#                 the queue ran no forge lint at all, and #2696 and #2662 turned
#                 main red on findings the scoped lint names in about a minute
#                 (/home/workspace/designs/reverse-walk-backtest-2026-11-13.md).
#
# Phases, each a row of the summary (slot wait vs run, seconds):
#   plan       tools/test-affected plan: the ONE rule set (dev-docs/testing-affected.md)
#   pnpm-*     pnpm install --frozen-lockfile where a selected lane needs node_modules it lacks
#   pgtest     tools/pgtest-check: forge pgtest's shared Postgres serves a
#              scratch database, before generate or a Go test needs one; when
#              it cannot, the gate stops there (MG_PGTEST_TIMEOUT, default 150s)
#   generate   pinned forge generate must be a no-op; only when a forge input changed
#   docs       every docs generator must be a no-op; only when a docs input changed
#   build      compile (go list -export: no link) every package whose build
#              contains a change (the plan's go.build), or ./... on a full Go lane
#   testbuild  scripts/test-build.sh: compile (no link, no run) EVERY test
#              package, the build-tagged ones under their tag, in every tier,
#              so a fixture a required field or signature change broke fails
#              here wherever it is (#3489); seconds on a warm cache
#   test       tools/test-affected run -plan <the plan> (-short, cached, no
#              -race): the changed packages and their DIRECT importers, plus
#              every transitive dependent when they number at most
#              MG_REACH_CAP (default 20; 0 for direct only), the
#              vitest files within two imports of a changed module, and the
#              electron tests when electron/ changed; deeper dependents of a
#              hub run in the release gate (MG_REACH=all for all). A change the rules cannot
#              scope (go.mod, package.json, config) runs its whole Go and
#              electron lane (a forge pin move alone selects by the forge
#              packages it changes: dev-docs/testing-affected.md); a full web fallback runs tsc here, the vitest
#              files the diff's own web modules reach (-web-full reached), and
#              the whole vitest suite in the release gate. A Go
#              test this change adds or edits that runs over the per-test budget
#              fails it (-test-budget auto). Recorded (-capture) as lanes short
#              and web-affected of one forge test run, which never changes
#              the verdict (scripts/test-capture.sh)
#   typecheck  scripts/web-typecheck.sh: frontends/web's `tsc -b` (app, tests,
#              gen, node configs), incremental on state this checkout wrote,
#              when the web lane is selected or anything under
#              frontends/web/ changed; vitest strips types without checking
#              them, so this is the only phase that sees a test's type error
#   golangci   scripts/golangci-changed.sh --base <the gate's base>: golangci-lint
#              over the packages with a changed .go file, findings in the files
#              the diff changed, the root .golangci.yml, the pinned forge's
#              golangci-lint release (none on PATH, or another release, fails
#              the phase with the install command, which the summary prints). It is the very script, with the
#              same base, that the merge queue's golangci (changed) job runs, so
#              the two cannot disagree; that job ejected #2857 and #2873 on
#              findings nothing before the queue ran (owner, 2026-11-18; Reliant
#              bug 67d80734). Only when the diff adds or changes a .go file
#              (untracked ones included), and in every tier but --queue and
#              --skip-golangci, where a golangci (changed) job runs beside it
#   lint       forge lint's own lanes (named, --optional-deps-guard among them;
#              no golangci-lint, the phase above, or buf lint, which the
#              release gate runs)
#              --scope <changed dirs>, a pin move included, in every tier but
#              --skip-lint;
#              like build and test it starts after the generators in --amend
#              mode, so it reads the amended tree (lint reads generated files
#              such as gen/forge_descriptor.json)
#   demo       the forge demo bundle's build (WEB_BUILD_TARGET=demo vite build),
#              whose guard plugin fails on a Reliant-only module in the bundle,
#              when the plan's web-demo lane is selected: a change in the
#              bundle's import graph, its build config, or a FULL web lane
#   migsafety  forge lint --migration-safety, only when a migration or forge.yaml changed
#   projectlint forge lint --graph --closed-sets --proto-format, unscoped, in every
#              tier: the whole-project lanes that take seconds. --graph: every
#              provider reached by a root (#3483). --closed-sets: a CHECK declared
#              forge:closed-set lists exactly the set the pinned forge carries (a pin
#              bump that grew accesstoken.AllScopes merged without the widening
#              migration, #3382). --proto-format: buf format is a no-op (#3397,
#              #3480). It replaced the closedsets phase, which ran only on a
#              migration, go.mod or forge.yaml change.
#   migorder   every migration is versioned above origin/main's newest (fetched
#              first, fail closed), only when a migration changed; under --queue
#              also every migration the entry adds above HEAD^'s newest (main
#              plus the entries queued ahead, control-plane#3571)
# When the gate may rewrite the tree it judges (--amend, or a dirty tree),
# generate and docs run FIRST, in place, and only then do build, test,
# typecheck, golangci, lint and migsafety run, at the same time, on the regenerated tree:
# a stale generated file (a mock the merge left a step behind) must never be
# built and tested before the regenerate that fixes it. On a clean tree without
# --amend the generators cannot change what is judged, so they run in scratch
# checkouts beside the other phases, except on a host under 32 GiB (a hosted
# runner), where forge generate runs first and alone (the memory budget below).
#
# The whole gate holds ONE host heavy slot (forge machine slot run), taken here; every
# phase nests inside it and never queues again. Its phases share a CPU budget
# (MG_CPUS) instead of each spawning one thread per core of a host whose other
# slots are busy too. MG_LOGDIR=<dir> keeps the
# phase logs there (default: a fresh directory under ~/.cache/reliant-merge-gate).
set -uo pipefail
# macOS ships bash 3.2: no mapfile/readarray, no associative arrays, and under
# `set -u` an empty array expands as unbound unless written ${a[@]+"${a[@]}"}.
cd "$(git rev-parse --show-toplevel)" || exit 2

base="" amend=0 regen=0 queue=0 skip_lint=0 skip_golangci=0 skip_vitest=0
while [ $# -gt 0 ]; do
  case "$1" in
    --base) base=$2; shift 2 ;;
    --amend) amend=1; shift ;;
    --regen) regen=1; shift ;;
    --skip-lint) skip_lint=1; shift ;;
    # A golangci (changed) job runs beside the gate: the queue's, or ci.yml's.
    --skip-golangci) skip_golangci=1; shift ;;
    # The merge queue's vitest job runs the selected files, in shards.
    --skip-vitest) skip_vitest=1; shift ;;
    # The queue tier skips vet (below), never forge lint.
    --queue) queue=1; shift ;;
    -h|--help) awk 'NR > 1 && /^set -uo pipefail/ { exit } NR > 1' "$0"; exit 0 ;;
    *) echo "merge-gate: unknown argument $1" >&2; exit 2 ;;
  esac
done

# vet is the release gate's under --skip-lint and in the queue, where the
# golangci (changed) job beside the gate runs the changed packages' linters.
skip_vet=0; { [ "$skip_lint" = 1 ] || [ "$queue" = 1 ]; } && skip_vet=1
[ "$queue" = 1 ] && skip_golangci=1
# GO_TEST_VET (Taskfile.yml): where the vet phase runs, it vets every package
# the test phase tests (go.build + go.packages, test files included; ./... on
# a full lane), so the test phase's go test skips its own vet subset of the
# same analyzers. Where vet is the release gate's (the queue, --skip-lint),
# go test's vet is the only vet this tier runs, and stays on. The flag is not
# in Go's test-result cache key: either way the results are task
# test:short's. An exported GO_TEST_VET (empty included) wins.
test_vet=""; [ "$skip_vet" = 1 ] || test_vet=-vet=off
test_vet=${GO_TEST_VET-$test_vet}

export GOFLAGS="${GOFLAGS:+$GOFLAGS }-trimpath" GOPRIVATE="${GOPRIVATE:-github.com/reliant-labs/*}"
# A gate must not rewrite the clone's git config (forge's merge driver).
export FORGE_NO_MERGE_DRIVER=1

# ── one heavy slot for the whole gate ─────────────────────────────────────
# Slots are machine-wide, so any forge with `forge machine slot` will do: FORGE_BIN
# (the pin) when set, else the forge on PATH, else `reliant forge`
# (scripts/forge.sh --machine; never a `go run`).
read -r -a slotforge <<<"$(scripts/forge.sh --machine --which 2>/dev/null || true)"
if [ "${#slotforge[@]}" -gt 0 ] && [ -z "${FORGE_SLOT_HELD:-}" ] && [ -z "${MG_IN_SLOT:-}" ] && "${slotforge[@]}" machine slot status --class heavy >/dev/null 2>&1; then
  MG_SLOT_T0=$(date +%s) MG_IN_SLOT=1 exec "${slotforge[@]}" machine slot run --class heavy -- "$0" ${base:+--base "$base"} $([ "$amend" = 1 ] && echo --amend) $([ "$regen" = 1 ] && echo --regen) $([ "$skip_lint" = 1 ] && echo --skip-lint) $([ "$skip_golangci" = 1 ] && echo --skip-golangci) $([ "$skip_vitest" = 1 ] && echo --skip-vitest) $([ "$queue" = 1 ] && echo --queue)
fi
t0=$(date +%s)
slot_wait=$(( t0 - ${MG_SLOT_T0:-$t0} ))

# ── the gate's CPU budget ─────────────────────────────────────────────────
# Every Go command, go test binary, golangci and vitest otherwise sizes itself
# to the whole machine, and the gate runs six of them at once. So the gate
# gets a share of the machine, and each concurrent phase a part of that: the
# test phase (the longest) half for Go and half for vitest, the others a
# quarter. The share is forge's hostslot.DefaultShare for the heavy-slot
# holders running when the gate starts (this gate's own slot among them):
#
#   min(CPUs, max(2, min(3·CPUs / (2·busy), memory GiB / (2.5·busy))))
#
# so a gate alone keeps the machine and one among three others gets its
# part. It replaced a floor of half the machine plus the idle cores, under
# which three gates on 48 CPUs each took 24 or more (forge#3023 for the
# formula's measurements; newer forges export it as GOMAXPROCS to every slot
# holder, which then wins below).
#
# In order: MG_CPUS=<n>; a positive GOMAXPROCS already in the environment (a
# forge slot holder or the reliant daemon's heavy-command wrapper exported
# the share); else the share above, of the machine as the tightest cgroup
# cpu.max on the path sees it — a container's quota, or the daemon's
# heavy-command cgroup (internal/heavyjob): sized to nproc, a gate under a
# 4-CPU cap ran half=24 Go processes on 4 CPUs. Never more than that machine.
cpus=$(nproc 2>/dev/null || sysctl -n hw.ncpu 2>/dev/null || echo 4)
cpu_src="$cpus CPUs (nproc)"
cg=$(sed -n 's/^0:://p' /proc/self/cgroup 2>/dev/null)
while [ -n "$cg" ]; do
  # cpu.max is "<quota> <period>", or "max <period>" for none.
  read -r q p 2>/dev/null <"/sys/fs/cgroup$cg/cpu.max" || q=max
  case "$q" in ''|max|*[!0-9]*) ;; *) [ "${p:-0}" -gt 0 ] && [ $(( q / p )) -lt "$cpus" ] && cpus=$(( q / p > 0 ? q / p : 1 )) && cpu_src="$cpus CPUs (cgroup cpu.max)" ;; esac
  [ "$cg" = / ] && break
  cg=$(dirname "$cg")
done
# Memory: the smaller of physical memory and the cgroup's memory.max, in GiB
# (MG_MEM_GIB overrides it; 0 when unknown, which drops the memory term).
mem_kib=$(awk '/^MemTotal:/ { print $2 }' /proc/meminfo 2>/dev/null)
[ -n "$mem_kib" ] || mem_kib=$(( $(sysctl -n hw.memsize 2>/dev/null || echo 0) / 1024 ))
cg_max=$(cat /sys/fs/cgroup/memory.max 2>/dev/null || echo max)
case "$cg_max" in ''|max|*[!0-9]*) ;; *) [ $(( cg_max / 1024 )) -lt "$mem_kib" ] && mem_kib=$(( cg_max / 1024 )) ;; esac
mem_gib=${MG_MEM_GIB:-$(( mem_kib / 1048576 ))}
# busy: the heavy-slot holders now. Unreadable (no forge with machine slots,
# or no jq) means the gate took no slot either and cannot see the other
# gates: assume 3, which is half the machine, the old floor.
holders=$( { "${slotforge[@]}" machine slot status --class heavy --json | jq -e '.holders | length'; } 2>/dev/null) || holders=
case "$holders" in ''|*[!0-9]*) busy=3 busy_src="heavy-slot holders unreadable, assumed 3" ;; *) busy=$(( holders > 0 ? holders : 1 )) busy_src="$busy heavy holders" ;; esac
share=$(( 3 * cpus / (2 * busy) ))
[ "$mem_gib" -gt 0 ] && [ $(( mem_gib * 2 / (5 * busy) )) -lt "$share" ] && share=$(( mem_gib * 2 / (5 * busy) ))
[ "$share" -ge 2 ] || share=2
case "${GOMAXPROCS:-}" in ''|0|*[!0-9]*) gomaxprocs= ;; *) gomaxprocs=$GOMAXPROCS ;; esac
if [ -n "${MG_CPUS:-}" ]; then gate_cpus=$MG_CPUS cpu_why="MG_CPUS"
elif [ -n "$gomaxprocs" ]; then gate_cpus=$gomaxprocs cpu_why="GOMAXPROCS from the environment"
else gate_cpus=$share cpu_why="share of $cpu_src, ${mem_gib} GiB among $busy_src"
fi
[ "$gate_cpus" -le "$cpus" ] || gate_cpus=$cpus
half=$(( gate_cpus / 2 )); [ "$half" -ge 2 ] || half=2
quarter=$(( gate_cpus / 4 )); [ "$quarter" -ge 2 ] || quarter=2
echo "merge-gate: CPU budget $gate_cpus ($cpu_why)" >&2

# ── the gate's memory budget ──────────────────────────────────────────────
# Cores are not the only limit. Measured on reliant#1772 (208 files, a proto
# change): forge generate alone peaks at 7.5 GB RSS, tsc -b at 2.3 GB, each
# test link at 2.7 GB, and the phases beside one another reach 13 GB within
# five minutes. A hosted ubuntu-latest runner has 16 GiB: the merge queue's
# runner ran out of memory there three times, its agent died ("The runner has
# received a shutdown signal"), and the job sat until its timeout. So on a
# host under MG_GEN_ALONE_GIB (default 32) forge generate runs FIRST and alone,
# with the whole CPU budget, and the other phases start once it is done; on
# a bigger host it still runs beside them. mem_gib is the CPU budget's,
# above (MG_MEM_GIB=<n> overrides it).
gen_alone=0
# 0 means the size is unknown: keep the concurrent default.
[ "$mem_gib" -gt 0 ] && [ "$mem_gib" -lt "${MG_GEN_ALONE_GIB:-32}" ] && gen_alone=1

[ -n "$base" ] || base=$(git merge-base HEAD origin/main) || { echo "merge-gate: no merge-base with origin/main (fetch it, or pass --base)" >&2; exit 2; }
base=$(git rev-parse "$base")
L=${MG_LOGDIR:-${XDG_CACHE_HOME:-$HOME/.cache}/reliant-merge-gate/$(date -u +%Y%m%dT%H%M%SZ)-$$}
mkdir -p "$L"
echo "$slot_wait" >"$L/slot_wait"
# gate-verify reads the tier: --skip-lint's is obliged to neither vet nor
# lint, the queue's to lint but not vet. golangci-elsewhere: a golangci
# (changed) job beside the gate runs golangci, so the gate is not obliged to.
if [ "$skip_lint" = 1 ]; then echo skip-lint >"$L/tier"; elif [ "$queue" = 1 ]; then echo queue >"$L/tier"; fi
[ "$skip_golangci" = 1 ] && echo "a golangci (changed) job beside the gate runs it" >"$L/golangci-elsewhere"
# The pinned forge of the checkout it runs in: FORGE_BIN, a forge on PATH at
# the pin, `reliant forge`, or the pin go-installed once (scripts/forge.sh).
forge=("$PWD/scripts/forge.sh")
# Paths generators write. A regenerate in --amend mode may change only these.
gen_re='^gen/|^generated/|^docs/reference/[^/]*\.mdx$|^docs/changelog\.mdx$|^frontends/web/src/gen/|^\.forge/|^deploy/observability/|_gen(_test)?\.(go|json|ndjson|ts|k)$|^frontends/web/src/store/shortcutsData\.generated\.ts$|^internal/workflow/runtime/schema/field_descriptions\.go$|^internal/skills/catalog/builtin/workflow-builder/SKILL\.md$'

rows=() notes=() fail=0
# The test phase's lanes are recorded as ONE run of forge's test record
# (scripts/test-capture.sh, via tools/test-affected run -capture): lane short
# (the affected Go tests, with coverage) and lane web-affected (the selected
# vitest files). The record never changes a verdict, and FORGE_TESTS=off (or
# a forge that cannot begin a run) runs every lane unrecorded. Begun here
# so the run carries this gate's diff base; .forge/ is gitignored, so it is
# not a change the generators' no-op judgment or the plan sees.
if [ "${FORGE_TESTS:-}" != off ] && test_run=$("${forge[@]}" test begin --base "$base" 2>/dev/null) && [ -d "$test_run" ]; then
  export FORGE_TEST_RUN=$test_run
  notes+=("tests: recorded as run $test_run (forge test report $(basename "$test_run"))")
fi
# phase <name> <cmd>...: run one phase, timed, output to $L/<name>.log.
# Each phase is recorded in $L/expected BEFORE it launches and leaves its .row
# only when it finishes, so a phase that was started and cut off (queued past a
# timeout, cancelled, killed) has no row and fails the gate (gate-verify below).
# A rise in the cgroup's oom_kill count while it ran goes to $L/<name>.oom:
# gate-report marks a failed phase the kernel's OOM killer ended (its own
# process SIGKILLed, its log naming no failure, and this rise or the gate's
# memory peak to corroborate it) as infrastructure (GATE-INFRA: oom,
# control-plane #2862), so the merge queue re-runs it once rather than
# blaming the PR. The count is the whole cgroup's, so it never qualifies a
# phase alone.
cg_dir="/sys/fs/cgroup$(cut -d: -f3 /proc/self/cgroup 2>/dev/null | head -n1)"
oom_kills() { awk '$1 == "oom_kill" { print $2 }' "$cg_dir/memory.events" 2>/dev/null || true; }
phase() {
  local n=$1 s e rc o0 o1; shift
  echo "$n" >>"$L/expected"
  o0=$(oom_kills)
  s=$(date +%s)
  "$@" >"$L/$n.log" 2>&1; rc=$?
  e=$(date +%s)
  o1=$(oom_kills)
  case "$o0,$o1" in ,*|*,|*[!0-9,]*) ;; *) [ "$o1" -gt "$o0" ] && echo $((o1 - o0)) >"$L/$n.oom" ;; esac
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
    # reach through into the gate's own tree (pnpm's links inside it are
    # relative, so the copy resolves within itself). The fresh checkout's
    # lockfile is newer than the copied install's stamp, which would make
    # deps:protoc-gen-es reinstall: give it the original's mtime.
    cp -a "$root/$d/node_modules" "$SCRATCH/$d/node_modules"
    touch -r "$root/$d/pnpm-lock.yaml" "$SCRATCH/$d/pnpm-lock.yaml"
  done
  return 0
}
commit_amend() { git add -A && git -c user.name="${GIT_AUTHOR_NAME:-reliant-merge-gate}" -c user.email="${GIT_AUTHOR_EMAIL:-merge-gate@reliantlabs.io}" commit -q --amend --no-edit --no-verify; }
cleanup() { local wt; for wt in ${scratches[@]+"${scratches[@]}"}; do git worktree remove --force "$wt" >/dev/null 2>&1; done; }
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
  # generate.log stays on the runner and the queue's generate is its longest
  # phase: scripts/generate-timings.sh puts its five slowest steps in the
  # summary (forge's FORGE_GENERATE_TIMINGS table) and keeps its exit status.
  (cd "$wt" && GOMAXPROCS=$gen_procs "$root/scripts/generate-timings.sh" "$L/notes" task generate:forge) || return 1
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
# The merge tier's Go fan-out: direct importers (owner, 2026-10-24), plus
# every transitive dependent when they number at most MG_REACH_CAP (default
# 20; 0 is direct importers only). A leaf change then runs every test binary
# it reaches; a hub change (internal/db: 46 beyond its 119 direct; a
# narrow-role package under internal/llm/tools: 151 beyond 15) keeps the
# direct cut, and testbuild still compiles every test package. 20 bounds what
# the queue's 4-CPU runner adds to some 20 test binaries.
reach=${MG_REACH:-direct} reach_cap=${MG_REACH_CAP:-20}
phase plan go run ./tools/test-affected plan -json -reach "$reach" -go-reach-cap "$reach_cap" -base "$base" || { cat "$L/plan.log"; exit 1; }
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
  elec=$(j .electron.full) fgen=$(j .forge_generate.full) dgen=$(j .docs_generate.full) webdemo=$(j '.web_demo.full // false')
  # The renderer's release build (tools/test-affected's web-build lane): a
  # dependency, lockfile or vite config change (reliant#2850).
  webbuild=$(j '.web_build.full // false')
  hooks_only=$(j '.forge_generate.hooks_only // false') hooks=$(j '(.forge_generate.hooks // []) | join(" ")')
  [ "$regen" = 1 ] && { fgen=true; hooks_only=false; }
  files=$( { git diff --name-only "$base"; git ls-files --others --exclude-standard; } | sort -u)
  # vitest strips types: every changed web source needs tsc, tested or not.
  typecheck=false
  if [ "$webfull" = true ] || [ "$nweb" -gt 0 ] || echo "$files" | grep -q '^frontends/web/'; then typecheck=true; fi
}
load_selection || exit 1
notes+=("plan: go $([ "$gofull" = true ] && echo FULL || echo "$ngo package(s), reach $(j .go.reach) (transitive budget $reach_cap), $ndef transitive dependent(s) left to the release gate"); web $([ "$webfull" = true ] && echo "FULL ($nweb reached file(s))" || echo "$nweb file(s)"); typecheck $typecheck; demo-build $webdemo; web-build $webbuild; electron $elec; forge-generate $fgen$([ "$hooks_only" = true ] && echo " (hooks only: $hooks)"); docs-generate $dgen")

# ── pnpm installs a selected lane needs ───────────────────────────────────
# npm_install <dir>: pnpm's frozen install in <dir> unless its install is
# current. Every Node project here is a pnpm project (frontends/web, electron,
# tools/protoc-gen-es), each its own workspace root.
npm_install() {
  local d=$1 n scripts
  [ -f "$d/pnpm-lock.yaml" ] || return 0
  # Current when node_modules/.modules.yaml is newer than the lockfile (the
  # touch below dates it after a no-op install, which pnpm 12 leaves alone),
  # and, for electron, its postinstall also fetched the binary: an interrupted
  # install leaves node_modules current but no node_modules/electron/path.txt,
  # and every electron test then fails "Electron failed to install correctly".
  if [ "$d/node_modules/.modules.yaml" -nt "$d/pnpm-lock.yaml" ] \
    && { [ "$d" != electron ] || [ -f "$d/node_modules/electron/path.txt" ]; }; then
    return 0
  fi
  n=pnpm-$(echo "$d" | tr '/.' '-r')
  # electron's own install script fetches the Electron binary its tests load
  # (electron/pnpm-workspace.yaml's allowBuilds names what may run).
  scripts=--ignore-scripts; [ "$d" = electron ] && scripts=
  phase "$n" pnpm install --dir "$d" --frozen-lockfile $scripts --config.confirmModulesPurge=false --reporter=silent \
    && touch "$d/node_modules/.modules.yaml"
}
prepare_dependencies() {
  local d p
  local -a need=() npm_pids=()
  if [ "$webfull" = true ] || [ "$nweb" -gt 0 ] || [ "$typecheck" = true ] || [ "$webdemo" = true ] || [ "$webbuild" = true ]; then need+=(frontends/web); fi
  if [ "$elec" = true ]; then need+=(electron); fi
  if [ "$fgen" = true ] && [ "$hooks_only" != true ]; then need+=(tools/protoc-gen-es); fi
  for d in $(printf '%s\n' ${need[@]+"${need[@]}"} | sort -u); do
    npm_install "$d" & npm_pids+=($!)
  done
  for p in ${npm_pids[@]+"${npm_pids[@]}"}; do wait "$p" || fail=1; done
}
prepare_dependencies

# ── pgtest: the shared test Postgres serves, before anything needs it ─────
# forge generate's schema read and every DB-backed Go test take a database
# from forge pgtest's shared server. When it cannot serve one, they fail only
# once they reach it, minutes in, each "connection refused" (control-plane
# #2189: 15 of 60 gates in a day). tools/pgtest-check asks pgtest itself for a
# scratch database: seconds when it can, a failure naming the shared server
# when it cannot, and then no phase starts. Only when a phase needs one: the
# whole forge generate (its hooks alone do not), or any Go test.
if [ "$fail" = 0 ] && { { [ "$fgen" = true ] && [ "$hooks_only" != true ]; } || [ "$gofull" = true ] || [ "$ngo" -gt 0 ]; }; then
  if ! phase pgtest go run ./tools/pgtest-check -timeout "${MG_PGTEST_TIMEOUT:-150s}"; then
    fail=1
    notes+=("pgtest: the shared test Postgres could not serve a database; no phase ran ($L/pgtest.log)")
    cat "$L/pgtest.log" >&2
  fi
fi

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
# prewarm_tests: compile, never link, the test packages the plan selected, so
# a test phase that has to wait for a generate running alone (a small host,
# the memory budget above) finds them in the build cache and only links and
# runs. It runs in the generate's shadow: niced, so it takes only the CPU the
# generate leaves idle, and at a quarter of the budget, so its compiles (each
# well under 1 GiB; no 2.7 GiB link) fit beside generate's 7.5 GiB on a
# 16 GiB runner. It judges nothing: a package that does not compile is the
# test phase's to report, so it never fails the gate. The tree it compiles is
# the one test judges: generate runs in a scratch checkout here.
prewarm_tests() {
  local pkgs p
  if [ "$gofull" = true ]; then pkgs=(./...); else pkgs=(); while IFS= read -r p; do pkgs+=("$p"); done < <(jq -r '.go.packages // [] | .[]' "$run_plan"); fi
  if [ ${#pkgs[@]} -eq 0 ]; then echo "prewarm: no Go test package selected"; return 0; fi
  echo "prewarm: compiling the tests of ${#pkgs[@]} package selection(s) beside forge generate"
  GOMAXPROCS=$quarter nice -n 10 go list -buildvcs=false -e -test -export -p "$quarter" "${pkgs[@]}" >/dev/null 2>&1 ||
    echo "prewarm: some packages did not compile; the test phase reports them"
  return 0
}
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
    phase plan-2 go run ./tools/test-affected plan -json -reach "$reach" -go-reach-cap "$reach_cap" -base "$base" || fail=1
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
      # A small host (the memory budget above): generate first, alone, but
      # with the test packages compiling beside it (prewarm_tests), so the
      # test phase that follows only links and runs.
      notes+=("memory: ${mem_gib} GiB < ${MG_GEN_ALONE_GIB:-32}; forge generate ran before the other phases, test packages compiled beside it")
      phase prewarm prewarm_tests & prewarm_pid=$!
      gen_procs=$gate_cpus; phase generate gen_forge "$SCRATCH" || fail=1; gen_procs=$quarter
      wait "$prewarm_pid" || fail=1
    else phase generate gen_forge "$SCRATCH" & pids+=($!); fi
  fi
  # A generate that already failed alone leaves nothing to run docs beside.
  if [ "$dgen" = true ] && [ "$fail" = 0 ]; then
    if scratch docs; then phase docs gen_docs "$SCRATCH" & pids+=($!); else fail=1; fi
  fi
fi
# build_check: compile, without linking, every package whose build contains a
# change. `go build ./...` also linked every main package of the module (the
# server, the CLI, a dozen generators): 41s with every package already
# compiled, against 6s for this; the link check runs in the release gate's
# build. A full Go lane (a pin moved) compiles ./... ; a forge pin move alone
# is in go.build as every package whose build contains a changed forge
# package (tools/test-affected/forgepin.go). go list -export compiles
# each package for its export data and exits non-zero naming any that does not
# compile, main packages included.
build_check() {
  local pkgs p
  if [ "$gofull" = true ]; then pkgs=(./...); else pkgs=(); while IFS= read -r p; do pkgs+=("$p"); done < <(jq -r '.go.build // [] | .[]' "$run_plan"); fi
  if [ ${#pkgs[@]} -eq 0 ]; then echo "build: no Go package's build contains a change"; return 0; fi
  echo "build: compiling ${pkgs[*]:0:20}$([ ${#pkgs[@]} -gt 20 ] && echo " ... (${#pkgs[@]} packages)")"
  # Errors go to stderr; the package list on stdout is noise.
  GOMAXPROCS=$quarter go list -export "${pkgs[@]}" >/dev/null
}
# The merge tier runs forge lint's own lanes, named, over the changed
# directories; golangci-lint over the changed packages is the golangci phase
# (below), and buf lint, golangci over the rest of the module and the
# whole-project and deploy lanes run in the release gate's unscoped `forge lint`. Measured on one loaded host,
# the same two-directory scope: every default lane 331s (golangci the bulk;
# 405s average, 2735s unscoped on a pin move, in the merge queue), these lanes
# 41s. A pin move no longer unscopes it: what a new forge or module version
# breaks across the module is the release gate's to find; the regenerated
# files a pin bump commits are in the diff, so they are in scope here.
# --optional-deps-guard: forge.yaml makes forge-optional-deps-guard an error,
# and #2696 reached main on one (an unguarded optional dep in
# rpc_move_project_to_org.go); one more AST lane over the same scope.
lint_lanes=(--contract --conventions --generated-drift --go-markers --proto-markers
  --create-nullability --computed-fields --read-only-fields --db-grants
  --column-markers --closed-sets --tracing --guarded-fields --optional-deps-guard)
# The whole vitest suite (1033 files, ~20 min on a loaded host) is a release
# blocker, not a merge blocker (owner, 2026-10-28: slow suites gate releases;
# the merge gate aims at five minutes). A full web fallback (a package.json or
# build-config change) still type-checks every web source here, and runs the
# vitest files the diff's own web modules reach, as a scoped web lane would
# (-web-full reached): before, it ran none, and #2669's 13 failures merged
# behind the queue entries of #2677 and #2693, whose proto-vendor/ and
# package.json changes made the lane FULL. The rest run in the release gate's
# `task test` (dev-docs/testing-affected.md, "A FULL web lane in the merge gate").
test_lanes=go,web,electron
if [ "$skip_vitest" = 1 ]; then
  # --skip-vitest: the merge queue's vitest job (scripts/vitest-shard.sh)
  # runs the same plan's files, split across its legs.
  test_lanes=go,electron
  notes+=("web: the $nweb vitest file(s) the plan selects run in the merge queue's vitest job (vitest --shard, scripts/vitest-shard.sh); tsc -b runs here")
elif [ "$webfull" = true ]; then
  notes+=("web: FULL fallback; tsc -b and the $nweb vitest file(s) the diff's modules reach run here, the whole vitest suite in the release gate")
fi
# demo_build: the forge demo bundle (frontends/web/vite.config.ts's demo
# target), as tools/test-affected's web-demo lane runs it but into the log
# directory, removed after: its guard plugin is the check, not the output.
# tsc -b is the typecheck phase's.
demo_build() {
  local out=$L/dist-demo rc
  (cd frontends/web && WEB_BUILD_TARGET=demo npx vite build --logLevel warn --outDir "$out" --emptyOutDir); rc=$?
  rm -rf "$out"
  return "$rc"
}
if [ "$fail" = 0 ]; then
  # Every test package compiles, in every tier (scripts/test-build.sh says
  # why): a fixture broken by a required field or a signature change in a
  # package no lane selected (#3489). After build, whose compiles it reuses,
  # and never the packages the test phase runs (it compiles those itself):
  # side by side the two would compile the same packages twice. Seconds on a
  # warm cache.
  if [ "$gofull" = true ]; then echo ./... >"$L/tested.txt"; else jq -r '.go.packages // [] | .[]' "$run_plan" >"$L/tested.txt"; fi
  { phase build build_check; rc=$?
    phase testbuild env GOMAXPROCS="$quarter" scripts/test-build.sh -p "$quarter" --exclude "$L/tested.txt" || rc=1
    exit "$rc"; } & pids+=($!)
  phase test env GOMAXPROCS="$half" VITEST_MAX_WORKERS="$half" GO_TEST_VET="$test_vet" \
    go run ./tools/test-affected run -plan "$run_plan" -base "$base" -lanes "$test_lanes" -web-full reached -p "$half" -test-budget "${MG_TEST_BUDGET:-auto}" -capture & pids+=($!)
  if [ "$typecheck" = true ]; then
    phase typecheck scripts/web-typecheck.sh & pids+=($!)
  fi
  # The two vite builds are the gate's largest phases: on reliant#2935's merge
  # with main the demo bundle peaked at 9.1 GB RSS and the renderer's release
  # bundle at 11.8 GB, each on its own. Beside test, tsc -b and lint on the
  # queue's 16 GiB runner the demo build alone took the gate to 14.6 GiB
  # (cgroup memory.peak); with both, the runner ran out (#2935 twice, #3009
  # twice: "The runner has received a shutdown signal", or a job with no log
  # at its 30-minute timeout). So on a small host (gen_alone, the memory
  # budget above) they run after every other phase, one at a time
  # (run_bundles, below); on a bigger host, beside the rest.
  if [ "$gen_alone" = 1 ]; then
    bundles_after=1
  else
    if [ "$webdemo" = true ]; then
      phase demo demo_build & pids+=($!)
    fi
    # The renderer's release bundle: what vitest and tsc -b cannot judge, a
    # dependency the bundler refuses (#2829's `node:module`, reliant#2850).
    if [ "$webbuild" = true ]; then
      phase webbuild scripts/web-release-build.sh & pids+=($!)
    fi
  fi
  # The changed files' directories, minus nested Go modules (sdk/go): see
  # scripts/lint-scope.sh and its self-test, scripts/test-lint-scope.sh.
  scope=$(echo "$files" | scripts/lint-scope.sh)
  if [ "$queue" = 1 ]; then
    notes+=("queue tier: golangci-lint on the changed packages is the golangci (changed) job beside this one; vet runs in the release gate$(echo "$files" | grep -qE '^proto/|^proto-vendor/|(^|/)buf[^/]*\.yaml$' || echo '; buf lint too (no proto changed)')")
  fi
  if [ "$skip_lint" = 1 ]; then
    notes+=("--skip-lint: vet and forge lint run in the release gate; golangci runs here")
  elif [ -n "$scope" ]; then
    notes+=("lint: forge's own lanes over $(echo "$scope" | tr ',' '\n' | wc -l | tr -d ' ') changed dir(s); golangci-lint is its own phase, buf lint runs in the release gate")
    phase lint env GOMAXPROCS="$quarter" "${forge[@]}" lint --quiet --no-fix --skip-frontends --scope "$scope" "${lint_lanes[@]}" & pids+=($!)
  else
    notes+=("lint: not run (only root files changed; the release gate lints the module)")
  fi
  # golangci: the merge queue's golangci (changed) job, here, so a finding
  # fails this gate instead of ejecting the PR from the queue (#2857, #2873;
  # Reliant bug 67d80734). The same script with the same base, so the same
  # packages, config, release and verdict. Only when the diff adds or changes
  # a .go file, as that job's "Changed Go files" step decides (plus untracked
  # files, which golangci-lint's --new-from-rev reads too). Not in --queue,
  # nor under --skip-golangci (ci.yml's ci:run gate): a golangci (changed)
  # job runs beside the gate on its own runner, so it would lint twice. Under --skip-lint too:
  # that tier leaves vet and forge lint to the release gate, which ejects
  # nothing, but the queue's golangci job ejects the PR. In the gate's own
  # heavy slot it drops golangci-lint's machine lock (--in-slot), as forge
  # lint does in a slot of its own; GOMAXPROCS is a quarter, like lint's.
  gofiles=$( { git diff --name-only --diff-filter=ACMR "$base" -- '*.go'; git ls-files --others --exclude-standard -- '*.go'; } | sort -u)
  if [ "$queue" = 1 ]; then :
  elif [ "$skip_golangci" = 1 ]; then
    notes+=("golangci: --skip-golangci; the golangci (changed) job beside this gate runs it")
  elif [ -n "$gofiles" ]; then
    phase golangci env GOMAXPROCS="$quarter" scripts/golangci-changed.sh --base "$base" ${MG_IN_SLOT:+--in-slot} & pids+=($!)
  else
    notes+=("golangci: not run (no .go file changed)")
  fi
  if [ "$queue" != 1 ] || echo "$files" | grep -qE '^proto/|^proto-vendor/|(^|/)buf[^/]*\.yaml$'; then
    # buf lint, then buf breaking against the diff base: semver at major 1 (owner,
    # 2026-11-05), so a wire break fails unless the owner accepted it with
    # breaking:approved, applied by the owner or by the Reliant bot on the owner's
    # instruction (scripts/check-compat.sh; CI_PR names the pull request). In the queue,
    # CI_QUEUE_ENTRY judges only this entry's squash, against its own PR's labels.
    phase proto env CI_BASE="$base" CI_QUEUE_ENTRY="$queue" bash -c 'task proto:lint && scripts/check-compat.sh proto' & pids+=($!)
  fi
  # Build reach includes ordinary production packages with no test binary.
  # Keep test-only changes too: vet checks their tests even when Build is empty.
  if [ "$gofull" = true ]; then vet_pkgs=(./...); else
    vet_pkgs=(); while IFS= read -r p; do vet_pkgs+=("$p"); done < <(jq -r '((.go.build // []) + (.go.packages // [])) | unique | .[]' "$run_plan")
  fi
  if [ ${#vet_pkgs[@]} -gt 0 ] && [ "$skip_vet" != 1 ]; then
    phase vet env GOMAXPROCS="$quarter" bash scripts/vet.sh "${vet_pkgs[@]}" & pids+=($!)
  fi
  if echo "$files" | grep -qE '^db/migrations/|^forge\.yaml$'; then
    # A migration declaring forge:breaks-rollback also needs the owner's
    # migration:breaks-rollback-approved label, applied by the owner or by the
    # Reliant bot on the owner's instruction (scripts/check-compat.sh).
    phase migsafety env CI_BASE="$base" CI_QUEUE_ENTRY="$queue" bash -c '"$@" && scripts/check-compat.sh migrations' _ "${forge[@]}" lint --migration-safety --no-fix & pids+=($!)
  fi
  # forge lint's whole-project lanes that take seconds, in every tier: the
  # scoped lint above skips a lane that cannot be scoped, and each of these
  # judges files the diff need not touch. --graph: every provider reached by
  # a root (#3483's unreachable internal/planning). --closed-sets: a
  # forge:closed-set CHECK lists exactly the pinned forge's set (#3382's pin
  # bump). --proto-format: buf format leaves every .proto alone (#3397,
  # #3480). 12-24s together at load 270 on 48 CPUs; the other whole-project
  # lanes (golangci over the module, the frontends) stay in the release gate.
  phase projectlint env GOMAXPROCS="$quarter" "${forge[@]}" lint --quiet --no-fix --skip-frontends --graph --closed-sets --proto-format & pids+=($!)
  # A migration below main's newest is never applied and reliant's migrator
  # refuses to start on it (reliant#1399). The check fetches origin/main
  # itself, so it judges main as it is now, not as this clone last saw it. In
  # the queue it also judges this entry against HEAD^, main plus the entries
  # queued ahead, none of them on origin/main yet: #3483 merged a migration
  # below #3482's that way (control-plane#3571).
  if echo "$files" | grep -qE '^db/migrations/'; then
    also=(); [ "$queue" = 1 ] && also=(-also HEAD^)
    phase migorder go run ./tools/test-affected migration-order ${also[@]+"${also[@]}"} & pids+=($!)
  fi
fi
# Every launched phase is waited for, the generators too when the gate failed
# before build: cleanup must not remove a checkout a generator still writes.
for p in ${pids[@]+"${pids[@]}"}; do wait "$p" || fail=1; done
# run_bundles: a small host's vite builds, deferred above, one at a time now
# that nothing else runs. They run even when another phase failed, so the
# summary names every failure, and before an --amend failure resets the tree.
run_bundles() {
  local rc=0
  [ "$webdemo" = true ] || [ "$webbuild" = true ] || return 0
  notes+=("memory: ${mem_gib} GiB < ${MG_GEN_ALONE_GIB:-32}; the vite builds (demo, release) ran one at a time after the other phases")
  if [ "$webdemo" = true ]; then phase demo demo_build || rc=1; fi
  if [ "$webbuild" = true ]; then phase webbuild scripts/web-release-build.sh || rc=1; fi
  return "$rc"
}
if [ "${bundles_after:-0}" = 1 ]; then run_bundles || fail=1; fi
# --amend: a failed gate leaves no half-regenerated tree behind.
if [ "$amend" = 1 ] && [ "$fail" != 0 ] && [ -n "$(git status --porcelain)" ]; then
  git reset -q --hard; git clean -fdq
fi
[ -f "$L/notes" ] && while IFS= read -r n; do notes+=("$n"); done <"$L/notes"
# The peak memory of the gate's cgroup (cgroup v2 memory.peak, where the host
# has it), so the summary shows how close a small host came to its limit: the
# rollout check of the prewarm beside a generate running alone reads it.
cg_peak=$(cat "$cg_dir/memory.peak" 2>/dev/null || true)
case "$cg_peak" in ''|*[!0-9]*) cg_peak=0 ;; *) notes+=("memory peak: $(awk -v b="$cg_peak" 'BEGIN { printf "%.1f", b / 1073741824 }') GiB of ${mem_gib} GiB (cgroup memory.peak)") ;; esac
# The limit that peak is judged against for GATE-INFRA: oom (gate-report): the
# detected size above, or the gate's own cgroup's memory.max when smaller.
mem_limit=$(( mem_kib * 1024 ))
cg_own_max=$(cat "$cg_dir/memory.max" 2>/dev/null || echo max)
case "$cg_own_max" in ''|max|*[!0-9]*) ;; *) [ "$cg_own_max" -lt "$mem_limit" ] && mem_limit=$cg_own_max ;; esac

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
  go run ./tools/test-affected gate-report -dir "$L" -wall "$((end - t0))" -mem-peak "$cg_peak" -mem-limit "$mem_limit" || { echo 'GATE-HEALTH: report failed'; fail=1; }
  echo "### merge-gate: checks $([ "$fail" = 0 ] && echo PASSED || echo FAILED) (timing reported separately)"
  echo
  echo "| phase | result | seconds | started +s |"
  echo "|---|---|---|---|"
  echo "| heavy slot (queue) | - | $slot_wait | - |"
  for f in plan pnpm-frontends-web pnpm-electron pnpm-tools-protoc-gen-es pgtest generate docs plan-2 build testbuild test typecheck demo webbuild golangci lint projectlint proto vet migsafety migorder; do
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
