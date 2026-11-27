#!/usr/bin/env bash
# release-gate.sh — reliant's SLOW tier: everything the merge gate
# (scripts/merge-gate.sh) leaves out, run on a commit of main and recorded per
# sha. scripts/release-tag.sh refuses to tag a commit without a green record.
# dev-docs/merge-and-release-gates.md is the design and the trade-offs.
#
#   release-gate.sh run [--force] [<rev>]
#       Run every step on <rev> (default origin/main, fetched) in a private
#       detached checkout of exactly that commit, so neither your checkout
#       (dirty, behind, on a branch) nor main moving during the run changes
#       what it judges. Holds one host heavy slot for the whole run. A sha with
#       a green full record is not re-run unless --force (a hotfix gate's PASS
#       is not one: `run` runs the whole gate over it). RG_ONLY=<step,...> runs a
#       subset and is never recorded.
#   release-gate.sh check [<rev>]
#       Is <rev> (default HEAD) release-ready? exit 0 green | 1 red | 3 no
#       record. A green on a first-parent ancestor (at most 50 back) also
#       passes when every change since it is release metadata: the version
#       field of electron/package.json, docs/, dev-docs/,
#       research/, *.md (a release PR's own diff).
#   release-gate.sh latest [<ref>]
#       Print the newest green-recorded sha on <ref>'s first-parent history
#       (default origin/main); exit 3 when there is none in the last 200.
#   release-gate.sh show <rev>
#       Print <rev>'s record.
#   release-gate.sh hotfix --base <base> <rev>
#       The HOTFIX gate (control-plane's rc-train hotfix lane): <rev> is <base>,
#       the reliant commit prod runs, plus the fix PRs cherry-picked onto it and
#       their version bump. It runs only what the picks can reach, the release
#       gate's whole rigor on that: the merge gate against <base> with every
#       dependent (MG_REACH=all), then the Go tests of every package with a
#       changed .go file under -race with no -short (every slow test). Recorded
#       and published like a run, as {kind: "hotfix", base}. `check` passes such
#       a record only while <base> is an ancestor of <rev> and is itself green:
#       the whole release gate on prod's commit, a narrow one on the picks.
#       <base> must be green before it runs.
#   release-gate.sh run --part <part> [--shard I/N] <rev>
#       Run one PART of the steps (below) and write a part record,
#       <records>/<sha>.part-<part>[-<I>of<N>].json. Never a release record,
#       never published: CI runs the parts as parallel jobs, then
#   release-gate.sh assemble <sha> <part.json>...
#       composes them into the record and publishes it. Fails closed, writing
#       nothing, unless every step ran on <sha>, each part exactly once (every
#       shard 1..N of a sharded part), and every go shard split the packages
#       on the same timings.
#   release-gate.sh go-timings-source [<rev>]
#       Print the newest first-parent ancestor of <rev> (default HEAD, at most
#       200 back) whose record carries go package seconds, or nothing. CI's
#       Lanes job runs it once per run and hands the sha to every go shard
#       (RG_GO_TIMINGS_FROM), so the shards balance on the latest measured
#       times and all of them split on the same ones.
#   release-gate.sh go-timings [<rev>]
#       Print those seconds as scripts/release-gate-go-timings.json's JSON
#       (the fallback a shard uses with no RG_GO_TIMINGS_FROM, or a record
#       without them): `scripts/release-gate.sh go-timings >
#       scripts/release-gate-go-timings.json` refreshes the file.
#
# Everything the merge gate (scripts/merge-gate.sh) leaves out runs here:
# golangci-lint and buf lint (lint), link errors (build), slow tests, the race
# detector and every Go package and vitest file the merge tier deferred (test),
# integration and e2e. dev-docs/merge-and-release-gates.md lists the split.
#
# Steps, in order (every step runs even after a failure; one table at the end):
#   npm          pnpm install --frozen-lockfile (npm ci for a pre-pnpm
#                checkout's package-lock.json) wherever node_modules is
#                stale (frontends/web, electron, tools/protoc-gen-es)
#   generate     task generate:check  (pinned forge generate is a no-op, + conventions)
#   docs         task generate:docs:check
#   lint         forge lint --no-fix, UNSCOPED: golangci over the module, buf
#                lint, every whole-project lane, the frontends
#   build        go build ./...
#   webbuild     task build:web:verify (lint, production renderer, artifact verification)
#   demo-size    task demo:bundle-size (the forge demo bundle against
#                frontends/web/scripts/demo/bundle-budget.json; it gates here
#                only, a pull request pastes its --report-only table)
#   typecheck    frontends/web's `npm run typecheck` (tsc -b, tests included)
#   vet          go vet ./...
#   test-go      task test -- <packages>  (Go -race -count=1 without -short:
#                every slow test; ./..., or this shard's packages)
#   test-web     task test:web  (every vitest file)
#   test-electron task test:electron
#   integration  task test:integration  (packages with integration-tagged
#                tests: none today, so it runs nothing and passes)
#   e2e          task test:e2e  (e2e/stories, ephemeral Temporal dev server)
#
# Parts (run --part; CI runs each as its own job, so the gate's wall time is
# the slowest part, not the sum: ~70 min serial on one runner, 2026-10-30):
#   static       npm generate docs build webbuild demo-size typecheck vet
#   lint         npm lint
#   go           npm test-go     (sharded: --shard I/N; packages balanced on
#                                 RG_GO_TIMINGS_FROM's record, else
#                                 scripts/release-gate-go-timings.json)
#   web          npm test-web test-electron integration e2e
# CI splits static three ways, so its slow steps (generate ~280s, webbuild
# ~220s) run side by side instead of one after another:
#   generate     npm generate docs
#   webbuild     npm webbuild demo-size
#   compile      npm build typecheck vet
# A record needs every step, not a fixed set of parts: static, or generate +
# webbuild + compile (the RC train's rl-gate.sh runs the four above).
# npm is in every part: each is a fresh runner that needs node_modules.
# The go part records each package's `go test` seconds (go_seconds); the
# assembled record carries them all, for the next run's shard split.
#
# Records: <git-common-dir>/release-gate/<sha>.{json,md} (outside every tree,
# shared by every worktree of the clone), and a git note on the commit under
# refs/notes/release-gate, pushed to origin so a release cut from another
# machine finds it (RELEASE_GATE_PUBLISH=0 keeps it local). `check` reads
# both, fetching the notes first.
#
#   RELEASE_GATE_DIR   where the private checkout lives (default
#                      ${XDG_CACHE_HOME:-~/.cache}/reliant-release-gate/<clone>);
#                      node_modules survive between runs, everything else is
#                      cleaned per run
#   FORGE_BIN          a prebuilt forge (must be the pin; check-forge-generate.sh refuses otherwise)
set -uo pipefail

STEPS=(npm generate docs lint build webbuild demo-size typecheck vet test-go test-web test-electron integration e2e)
PARTS=(static lint go web)
declare -A PART_STEPS=(
  [static]="npm generate docs build webbuild demo-size typecheck vet"
  [lint]="npm lint"
  [go]="npm test-go"
  [web]="npm test-web test-electron integration e2e"
  # static, split three ways for CI (header).
  [generate]="npm generate docs"
  [webbuild]="npm webbuild demo-size"
  [compile]="npm build typecheck vet"
)
# The hotfix gate's steps (release-gate.sh hotfix).
HOTFIX_STEPS=(npm merge test-go)
NOTES_REF=refs/notes/release-gate
die() { printf 'release-gate: %s\n' "$*" >&2; exit 2; }

# This script, by absolute path: a hotfix's `check` runs it again on its base.
self="$(cd "$(dirname "$0")" && pwd)/$(basename "$0")"
cd "$(git rev-parse --show-toplevel)" || exit 2
# This gate's own forge resolver (FORGE_BIN, a forge on PATH at the pin,
# `reliant forge`, or `go run …@<pin>`), run in the private checkout: the pin
# is that commit's, the resolver this one, which a checkout older than it lacks.
forge_sh=$PWD/scripts/forge.sh
common=$(cd "$(git rev-parse --git-common-dir)" && pwd)
records="$common/release-gate"
mkdir -p "$records"
resolve() { git rev-parse --verify --quiet "$1^{commit}" || die "cannot resolve '$1'"; }

fetch_notes() {
  git fetch -q origin "+$NOTES_REF:refs/notes/release-gate-origin" 2>/dev/null || true
}
# record_of <sha>: the newest record (local file, else a note), or nothing.
record_of() {
  local sha=$1
  if [ -f "$records/$sha.json" ]; then cat "$records/$sha.json"; return; fi
  git notes --ref=release-gate show "$sha" 2>/dev/null && return
  git notes --ref=release-gate-origin show "$sha" 2>/dev/null
}
# recorded: every sha with a record anywhere, one per line (one git call per
# source, not one per commit: a walk of 200 commits stays instant).
recorded() {
  { ls "$records" 2>/dev/null | sed -n 's/^\([0-9a-f]\{40\}\)\.json$/\1/p'
    git notes --ref=release-gate list 2>/dev/null | cut -d' ' -f2
    git notes --ref=release-gate-origin list 2>/dev/null | cut -d' ' -f2
  } | sort -u
}
# A record certifies the run that wrote it. A step added to the gate since
# (webbuild, 2026-10-27) runs in the next record; an older green stays green,
# and `check` names what it predates (missing_steps).
result_of() { record_of "$1" | jq -r '.result // empty' 2>/dev/null | tail -1; }
kind_of() { record_of "$1" | jq -r '.kind // empty' 2>/dev/null | tail -1; }
# hotfix_green <sha>: a hotfix record's PASS stands only on top of a green base: its base
# is an ancestor of <sha> and passes `check` itself (a hotfix of a hotfix recurses, at most
# 10 deep). Sets HOTFIX_WHY when it does not stand.
HOTFIX_WHY=""
hotfix_green() {
  local sha=$1 base depth=${RG_HOTFIX_DEPTH:-0}
  base=$(record_of "$sha" | jq -r '.base // empty' 2>/dev/null | tail -1)
  [[ "$base" =~ ^[0-9a-f]{40}$ ]] && git merge-base --is-ancestor "$base" "$sha" 2>/dev/null \
    || { HOTFIX_WHY="its hotfix record names a base that is not its ancestor"; return 1; }
  [ "$depth" -lt 10 ] || { HOTFIX_WHY="hotfixes nest more than 10 deep"; return 1; }
  RG_HOTFIX_DEPTH=$((depth + 1)) "$self" check "$base" >/dev/null 2>&1 \
    || { HOTFIX_WHY="its hotfix base $(git rev-parse --short "$base") is not green"; return 1; }
}
# green <sha>: <sha>'s own record is a standing PASS (a hotfix record: on a green base).
green() {
  [ "$(result_of "$1")" = PASS ] || return 1
  [ "$(kind_of "$1")" != hotfix ] || hotfix_green "$1"
}
missing_steps() {
  local required
  required=$(printf '%s\n' "${STEPS[@]}" | jq -R . | jq -s .)
  record_of "$1" | jq -r --argjson required "$required" '$required - [.steps[]?.name] | join(", ")' 2>/dev/null | tail -1
}

publish() {
  local sha=$1 json=$2 i
  git notes --ref=release-gate add -f -m "$json" "$sha" || return 1
  [ "${RELEASE_GATE_PUBLISH:-1}" = 0 ] && return 0
  for i in 1 2 3; do
    git push -q origin "$NOTES_REF:$NOTES_REF" 2>/dev/null && { echo "release-gate: record published to origin $NOTES_REF"; return 0; }
    fetch_notes
    git notes --ref=release-gate merge -q -s cat_sort_uniq refs/notes/release-gate-origin 2>/dev/null || true
  done
  echo "release-gate: WARNING: could not push $NOTES_REF; the record is local to this clone ($records/$sha.json)" >&2
}

# release_metadata_only <from> <to>: every change is a release PR's own.
release_metadata_only() {
  local from=$1 to=$2 f
  while IFS= read -r f; do
    [ -z "$f" ] && continue
    case "$f" in
      docs/*|dev-docs/*|research/*|*.md) continue ;;
      electron/package.json)
        # Only the version may differ: a dependency change is not metadata.
        # (electron/pnpm-lock.yaml records no app version, so a release never
        # touches it.)
        node -e '
          const [a, b] = process.argv.slice(1).map(s => JSON.parse(s || "{}"));
          const strip = j => { delete j.version; return JSON.stringify(j); };
          process.exit(strip(a) === strip(b) ? 0 : 1);
        ' "$(git show "$from:$f" 2>/dev/null)" "$(git show "$to:$f" 2>/dev/null)" || return 1 ;;
      *) return 1 ;;
    esac
  done < <(git diff --name-only "$from" "$to")
  return 0
}

cmd_check() {
  local rev sha r a n=0
  rev=${1:-HEAD}; sha=$(resolve "$rev")
  fetch_notes
  r=$(result_of "$sha")
  if [ "$r" = PASS ] && [ "$(kind_of "$sha")" = hotfix ]; then
    if hotfix_green "$sha"; then
      echo "release-gate: $(git rev-parse --short "$sha") is green: its hotfix gate passed ($(record_of "$sha" | jq -r '.finished' | tail -1)) on top of the green $(record_of "$sha" | jq -r '.base[0:9]' | tail -1)"
      return 0
    fi
    echo "release-gate: $(git rev-parse --short "$sha")'s hotfix gate passed, but $HOTFIX_WHY" >&2
    return 3
  fi
  if [ "$r" = PASS ]; then
    echo "release-gate: $(git rev-parse --short "$sha") is green ($(record_of "$sha" | jq -r '.finished'))"
    local missing; missing=$(missing_steps "$sha")
    [ -z "$missing" ] || echo "release-gate: note: that record predates step(s) $missing, which it did not run" >&2
    return 0
  fi
  if [ "$r" = FAIL ]; then echo "release-gate: $(git rev-parse --short "$sha") is RED: $(record_of "$sha" | jq -r '[.steps[] | select(.result != "PASS") | .name] | join(", ")')" >&2; return 1; fi
  local -A have; local x; while read -r x; do have[$x]=1; done < <(recorded)
  for a in $(git rev-list --first-parent -n 50 "$sha^" 2>/dev/null); do
    n=$((n+1))
    [ -n "${have[$a]:-}" ] || continue
    r=$(result_of "$a")
    [ -z "$r" ] && continue
    # A hotfix record carries only while it stands (green).
    [ "$r" != PASS ] || green "$a" || r=UNPROVEN
    if [ "$r" = PASS ] && release_metadata_only "$a" "$sha"; then
      echo "release-gate: $(git rev-parse --short "$sha") carries the green of $(git rev-parse --short "$a"), $n commit(s) back: every change since is release metadata"
      return 0
    fi
    if [ "$r" = UNPROVEN ]; then
      echo "release-gate: no record for $(git rev-parse --short "$sha"); the newest recorded ancestor $(git rev-parse --short "$a") is a hotfix whose $HOTFIX_WHY" >&2
    elif [ "$r" = PASS ]; then
      echo "release-gate: no record for $(git rev-parse --short "$sha"); the newest green ancestor $(git rev-parse --short "$a") is $n commit(s) back and the changes since are not only release metadata" >&2
    else
      echo "release-gate: no record for $(git rev-parse --short "$sha"); the newest recorded ancestor $(git rev-parse --short "$a") is RED" >&2
    fi
    return 3
  done
  echo "release-gate: no record for $(git rev-parse --short "$sha") or its last 50 first-parent ancestors" >&2
  return 3
}

cmd_latest() {
  local ref sha
  ref=${1:-origin/main}
  [ "$ref" = origin/main ] && { git fetch -q origin main 2>/dev/null || true; }
  fetch_notes
  local -A have; local x; while read -r x; do have[$x]=1; done < <(recorded)
  for sha in $(git rev-list --first-parent -n 200 "$(resolve "$ref")"); do
    [ -n "${have[$sha]:-}" ] || continue
    green "$sha" && { echo "$sha"; return 0; }
  done
  echo "release-gate: no green record in the last 200 commits of $ref" >&2
  return 3
}

cmd_run() {
  local force=0 rev sha snap t0 rc fail=0 s e step json steps_json md slot_wait part="" shard="" steps hbase=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --force) force=1; shift ;;
      --hotfix-base) hbase=${2:-}; shift 2 || die "--hotfix-base needs a commit" ;;
      --part) part=${2:-}; shift 2 || die "--part needs a name" ;;
      --shard) shard=${2:-}; shift 2 || die "--shard needs I/N" ;;
      *) break ;;
    esac
  done
  if [ -n "$part" ]; then
    [ -n "${PART_STEPS[$part]:-}" ] || die "unknown part '$part' (${!PART_STEPS[*]})"
    [ -n "${RG_ONLY:-}" ] && die "RG_ONLY and --part do not combine"
  fi
  if [ -n "$shard" ]; then
    [[ "$shard" =~ ^([1-9][0-9]*)/([1-9][0-9]*)$ ]] && [ "${BASH_REMATCH[1]}" -le "${BASH_REMATCH[2]}" ] || die "--shard must be I/N with 1 <= I <= N (got '$shard')"
    [ "$part" = go ] || die "only --part go is sharded"
  fi
  rev=${1:-}
  if [ -z "$rev" ]; then git fetch -q origin main || die "cannot fetch origin main"; rev=origin/main; fi
  sha=$(resolve "$rev")
  fetch_notes
  if [ -n "$hbase" ]; then
    [ -z "$part" ] && [ -z "${RG_ONLY:-}" ] || die "a hotfix gate is never run in parts or with RG_ONLY"
    hbase=$(resolve "$hbase")
    [ "$hbase" != "$sha" ] && git merge-base --is-ancestor "$hbase" "$sha" \
      || die "hotfix: $(git rev-parse --short "$hbase") is not a strict ancestor of $(git rev-parse --short "$sha"): a hotfix commit is prod's commit plus picks"
    "$self" check "$hbase" >/dev/null 2>&1 \
      || die "hotfix: the base $(git rev-parse --short "$hbase") is not green (release-gate.sh check); a hotfix narrows the gate only on top of a green"
  fi
  # A green record is reused only when it answers the question asked: a full
  # PASS answers both a full and a hotfix run; a hotfix PASS answers only a
  # hotfix run on the same base (whose greenness was checked just above), never
  # a full run, which then runs the whole gate rather than reporting the narrow
  # one as green.
  local rkind
  rkind=$(kind_of "$sha")
  if [ "$force" = 0 ] && [ -z "${RG_ONLY:-}" ] && [ "$(result_of "$sha")" = PASS ] \
    && { [ "$rkind" != hotfix ] || { [ -n "$hbase" ] && [ "$(record_of "$sha" | jq -r '.base // empty' | tail -1)" = "$hbase" ]; }; }; then
    echo "release-gate: $(git rev-parse --short "$sha") already has a green record (--force re-runs it)"; return 0
  fi
  # One heavy slot for the whole run; every step nests inside it.
  # Any forge will do (scripts/forge.sh --machine): slots are machine-wide.
  local slotforge
  read -r -a slotforge <<<"$("$forge_sh" --machine --which 2>/dev/null || true)"
  if [ "${#slotforge[@]}" -gt 0 ] && [ -z "${FORGE_SLOT_HELD:-}" ] && [ -z "${RG_IN_SLOT:-}" ] && "${slotforge[@]}" machine slot status --class heavy >/dev/null 2>&1; then
    RG_SLOT_T0=$(date +%s) RG_IN_SLOT=1 exec "${slotforge[@]}" machine slot run --class heavy -- "$0" run $([ "$force" = 1 ] && echo --force) ${part:+--part "$part"} ${shard:+--shard "$shard"} ${hbase:+--hotfix-base "$hbase"} "$sha"
  fi
  t0=$(date +%s); slot_wait=$(( t0 - ${RG_SLOT_T0:-$t0} ))
  snap=${RELEASE_GATE_DIR:-${XDG_CACHE_HOME:-$HOME/.cache}/reliant-release-gate/$(echo "$common" | md5sum | cut -c1-8)}/snap
  mkdir -p "$(dirname "$snap")"
  exec 9>"$(dirname "$snap")/run.lock"
  flock -n 9 || die "another release-gate run holds $(dirname "$snap")/run.lock"
  if [ ! -e "$snap/.git" ]; then
    git worktree add -q --detach "$snap" "$sha" || die "cannot create the private checkout $snap"
  fi
  (cd "$snap" && git checkout -q --detach -f "$sha" && git clean -q -ffdx -e node_modules) || die "cannot check out $sha in $snap"
  local L="$records/run-$(git rev-parse --short=12 "$sha").$(date -u +%Y%m%dT%H%M%SZ)"
  mkdir -p "$L"
  echo "release-gate: $(git log --oneline -1 "$sha" | cut -c1-100)"
  echo "release-gate: private checkout $snap, logs $L (queued ${slot_wait}s for a heavy slot)"
  steps_json="[]"
  if [ -n "$part" ]; then read -r -a steps <<<"${PART_STEPS[$part]}"; else steps=("${STEPS[@]}"); fi
  [ -z "$hbase" ] || steps=("${HOTFIX_STEPS[@]}")
  # The test steps record into ONE run of forge's test record in the private
  # checkout (.forge/tests/runs/<id>/: test-go as lane unit, test-web as
  # frontend, integration, e2e; scripts/test-capture.sh). The record never
  # changes a step's result. On main, ci.yml's test-upload job sends the
  # parts' runs to the control plane (forge test report --upload).
  local test_run=""
  if [ "${FORGE_TESTS:-}" != off ] && [[ " ${steps[*]} " =~ \ (test-go|test-web|integration|e2e|merge)\  ]]; then
    test_run=$(cd "$snap" && "$forge_sh" test begin 2>/dev/null) && [ -d "$test_run" ] || test_run=""
    [ -z "$test_run" ] || echo "release-gate: test lanes record into $test_run"
  fi
  for step in "${steps[@]}"; do
    if [ -n "${RG_ONLY:-}" ] && ! echo ",$RG_ONLY," | grep -q ",$step,"; then continue; fi
    s=$(date +%s)
    echo "release-gate: [$(date -u +%H:%M:%SZ)] $step ..."
    (cd "$snap" && export GOFLAGS="${GOFLAGS:+$GOFLAGS }-trimpath" FORGE_NO_MERGE_DRIVER=1 RG_GO_SHARD="$shard" RG_HOTFIX_BASE="$hbase" && { [ -z "$test_run" ] || export FORGE_TEST_RUN="$test_run"; } && run_step "$step") >"$L/$step.log" 2>&1
    rc=$?; e=$(date +%s)
    [ "$rc" = 0 ] || fail=1
    echo "release-gate: $step $([ "$rc" = 0 ] && echo PASS || echo "FAIL (rc $rc; $L/$step.log)") in $((e - s))s"
    steps_json=$(jq -c --arg n "$step" --arg r "$([ "$rc" = 0 ] && echo PASS || echo FAIL)" --argjson s $((e - s)) '. + [{name: $n, result: $r, seconds: $s}]' <<<"$steps_json")
  done
  e=$(date +%s)
  # What the next run's go shards balance on (go_shard_packages), and which
  # timings this shard split on, so assemble can refuse shards that disagreed.
  local go_extra='{}'
  if [ -f "$L/test-go.log" ]; then
    go_extra=$(go_package_seconds <"$L/test-go.log" | jq -c --arg split "$(cat "$L/go-split" 2>/dev/null)" '{go_seconds: .} + (if $split != "" then {go_split: $split} else {} end)')
  fi
  json=$(jq -cn --arg sha "$sha" --arg r "$([ "$fail" = 0 ] && echo PASS || echo FAIL)" --argjson steps "$steps_json" --argjson go "$go_extra" \
    --arg started "$(date -u -d "@$t0" +%Y-%m-%dT%H:%M:%SZ)" --arg finished "$(date -u -d "@$e" +%Y-%m-%dT%H:%M:%SZ)" \
    --argjson seconds $((e - t0)) --argjson slot_wait "$slot_wait" --arg forge "$(sed -n 's/^forge_version:[[:space:]]*//p' "$snap/forge.yaml" | head -1)" \
    --arg host "$(hostname)" --arg by "$(git config user.email || whoami)" \
    --arg hbase "$hbase" \
    '{sha: $sha, result: $r, started: $started, finished: $finished, seconds: $seconds, slot_wait_seconds: $slot_wait, forge: $forge, host: $host, by: $by, steps: $steps} + $go
     + (if $hbase != "" then {kind: "hotfix", base: $hbase} else {} end)')
  md=$( {
    echo "### release-gate: $(git rev-parse --short "$sha") $([ "$fail" = 0 ] && echo PASSED || echo FAILED)${hbase:+ (the hotfix gate, on top of $(git rev-parse --short "$hbase"))}"
    echo
    echo "| step | result | seconds |"
    echo "|---|---|---|"
    jq -r '.steps[] | "| \(.name) | \(.result) | \(.seconds) |"' <<<"$json"
    echo
    echo "wall: $((e - t0))s after ${slot_wait}s queued for a heavy slot; forge $(jq -r .forge <<<"$json")"
  } )
  echo; echo "$md"
  if [ -n "${RG_ONLY:-}" ]; then echo "release-gate: RG_ONLY run: not recorded"; return "$fail"; fi
  if [ -n "$part" ]; then
    local pf="$records/$sha.part-$part${shard:+-${shard%/*}of${shard#*/}}.json"
    jq -c --arg part "$part" --arg shard "$shard" '. + {part: $part, shard: $shard}' <<<"$json" >"$pf"
    echo "release-gate: part record $pf (not a release record: \`$0 assemble\` composes the parts)"
    return "$fail"
  fi
  echo "$json" >"$records/$sha.json"; echo "$md" >"$records/$sha.md"
  publish "$sha" "$json"
  return "$fail"
}

# cmd_assemble <sha> <part.json>...: the parts CI ran in parallel, as one
# record. Fails closed (exit 2, nothing written) unless every part is present,
# every shard of a sharded part exactly once, and every step ran on <sha>; a
# step's result is FAIL if any part's run of it failed.
cmd_assemble() {
  local sha parts json md p want got
  [ $# -ge 1 ] || die "usage: $0 assemble <sha> <part.json>..."
  sha=$(resolve "$1"); shift
  fetch_notes
  if [ "$(result_of "$sha")" = PASS ]; then
    echo "release-gate: $(git rev-parse --short "$sha") already has a green record"; return 0
  fi
  [ $# -ge 1 ] || die "no part records for $(git rev-parse --short "$sha") (run the parts: ${PARTS[*]})"
  parts=$(jq -cs . "$@") || die "unreadable part record(s)"
  [ "$(jq --arg s "$sha" 'all(.[]; .sha == $s)' <<<"$parts")" = true ] || die "a part record is not of $sha"
  # Every part that ran, exactly once; a sharded part as one complete set
  # 1/N..N/N. Which parts is free (static, or CI's generate + webbuild +
  # compile): the step check below is what requires the whole gate.
  for p in $(jq -r '[.[].part] | unique | .[]' <<<"$parts"); do
    [ -n "${PART_STEPS[$p]:-}" ] || die "unknown part '$p' in the part records; nothing recorded"
    got=$(jq -r --arg p "$p" '[.[] | select(.part == $p) | .shard] | sort_by(split("/") | map(tonumber? // 0)) | join(",")' <<<"$parts")
    want=$(jq -r --arg p "$p" '[.[] | select(.part == $p) | .shard] as $s
      | if ($s | length) == 0 then "(none)"
        elif $s == [""] then ""
        else ($s[0] | split("/")[1] | tonumber) as $n | [range(1; $n + 1) | "\(.)/\($n)"] | join(",") end' <<<"$parts")
    [ "$got" = "$want" ] || die "part $p: ran [${got:-unsharded}], want [${want:-exactly one unsharded run}]; nothing recorded"
  done
  # Shards that balanced on different timings split the packages differently:
  # some package may have run in no shard. Fail closed rather than record it.
  [ "$(jq '[.[] | select(.part == "go") | .go_split // ""] | unique | length <= 1' <<<"$parts")" = true ] \
    || die "the go shards split the packages on different timings ($(jq -r '[.[] | select(.part == "go") | "\(.shard): \(.go_split // "none")"] | join(", ")' <<<"$parts")); nothing recorded"
  json=$(jq -c --argjson steps "$(printf '%s\n' "${STEPS[@]}" | jq -R . | jq -s .)" \
    --arg forge "$(git show "$sha:forge.yaml" 2>/dev/null | sed -n 's/^forge_version:[[:space:]]*//p' | head -1)" \
    --arg by "$(git config user.email || whoami)" '
    . as $parts
    | ([$parts[].steps[]] | group_by(.name) | map({key: .[0].name, value: .}) | from_entries) as $ran
    | ($steps - ($ran | keys)) as $missing
    | if ($missing | length) > 0 then error("steps no part ran: \($missing | join(", "))") else . end
    | ([$parts[].started] | min) as $started | ([$parts[].finished] | max) as $finished
    | {sha: $parts[0].sha,
       result: (if all($parts[].steps[]; .result == "PASS") then "PASS" else "FAIL" end),
       started: $started, finished: $finished,
       seconds: (($finished | fromdate) - ($started | fromdate)),
       slot_wait_seconds: ([$parts[].slot_wait_seconds] | max),
       forge: $forge, host: ([$parts[].host] | unique | join(",")), by: $by,
       parts: [$parts[] | {part, shard, seconds, result}],
       go_seconds: ([$parts[].go_seconds // {}] | add),
       steps: [$steps[] as $n | $ran[$n] | {name: $n,
         result: (if all(.[]; .result == "PASS") then "PASS" else "FAIL" end),
         seconds: ([.[].seconds] | max)}]}' <<<"$parts") || die "cannot assemble the parts (above)"
  md=$( {
    echo "### release-gate: $(git rev-parse --short "$sha") $([ "$(jq -r .result <<<"$json")" = PASS ] && echo PASSED || echo FAILED) (assembled from $(jq '.parts | length' <<<"$json") parallel parts)"
    echo
    echo "| part | result | seconds |"
    echo "|---|---|---|"
    jq -r '.parts[] | "| \(.part)\(if .shard != "" then " " + .shard else "" end) | \(.result) | \(.seconds) |"' <<<"$json"
    echo
    echo "| step | result | seconds (slowest part) |"
    echo "|---|---|---|"
    jq -r '.steps[] | "| \(.name) | \(.result) | \(.seconds) |"' <<<"$json"
    echo
    echo "wall: $(jq -r .seconds <<<"$json")s from the first part's start to the last's finish; forge $(jq -r .forge <<<"$json")"
  } )
  echo "$md"
  echo "$json" >"$records/$sha.json"; echo "$md" >"$records/$sha.md"
  publish "$sha" "$json"
  [ "$(jq -r .result <<<"$json")" = PASS ]
}

# go_package_seconds: `go test` output on stdin -> {"<import path>": <whole
# seconds, at least 1>} for every package that reported a time (ok or FAIL).
go_package_seconds() {
  awk -F'\t' '$1 ~ /^(ok|FAIL)[[:space:]]*$/ && $3 ~ /^[0-9.]+s/ { s = $3 + 0; print $2 "\t" (s < 1 ? 1 : int(s + 0.5)) }' \
    | jq -Rn '[inputs | split("\t") | {key: .[0], value: (.[1] | tonumber)}] | from_entries'
}

# record_go_seconds <sha>: the go_seconds of <sha>'s record (the newest one
# that has any), or nothing.
record_go_seconds() {
  record_of "$1" | jq -cs '[.[] | .go_seconds // empty | select(type == "object" and length > 0)] | last // empty' 2>/dev/null
}

cmd_go_timings_source() {
  local sha x
  fetch_notes
  local -A have; while read -r x; do have[$x]=1; done < <(recorded)
  for sha in $(git rev-list --first-parent -n 200 "$(resolve "${1:-HEAD}")"); do
    [ -n "${have[$sha]:-}" ] || continue
    [ -n "$(record_go_seconds "$sha")" ] && { echo "$sha"; return 0; }
  done
  return 0
}

cmd_go_timings() {
  local from
  from=$(cmd_go_timings_source "${1:-HEAD}")
  [ -n "$from" ] || die "no record in the last 200 first-parent commits carries go package seconds"
  echo "release-gate: go package seconds of $(git rev-parse --short "$from")'s record" >&2
  record_go_seconds "$from" | jq -S .
}

# go_timings: the package seconds a shard balances on. RG_GO_TIMINGS_FROM's
# record (CI's Lanes job picks one sha for every shard of a run), else the
# committed scripts/release-gate-go-timings.json.
go_timings() {
  local t=""
  [ -n "${RG_GO_TIMINGS_FROM:-}" ] && t=$(record_go_seconds "$RG_GO_TIMINGS_FROM")
  if [ -n "$t" ]; then echo "$t"; return; fi
  [ -z "${RG_GO_TIMINGS_FROM:-}" ] || echo "test-go: ${RG_GO_TIMINGS_FROM:0:12}'s record has no go package seconds; balancing on scripts/release-gate-go-timings.json" >&2
  cat scripts/release-gate-go-timings.json 2>/dev/null || echo '{}'
}

# go_shard_packages: with RG_GO_SHARD=I/N, this shard's packages (1-based I)
# of those with test files, longest-processing-time first on go_timings'
# `go test -race` package seconds (a package with no entry weighs the
# median); ties break on the path, so every shard given the same timings
# computes the same split. It writes the timings' digest to $L/go-split for
# assemble to compare across shards. Without RG_GO_SHARD, ./... .
go_shard_packages() {
  [ -n "${RG_GO_SHARD:-}" ] || { echo ./...; return; }
  local i=${RG_GO_SHARD%/*} n=${RG_GO_SHARD#*/} timings
  timings=$(go_timings)
  [ -z "${L:-}" ] || jq -cS . <<<"$timings" | sha256sum | cut -c1-16 >"$L/go-split"
  go list -f '{{if or .TestGoFiles .XTestGoFiles}}{{.ImportPath}}{{end}}' ./... \
    | jq -rRn --argjson w "$timings" --argjson i "$((i - 1))" --argjson n "$n" '
      [inputs | select(length > 0)] as $pkgs
      | ([$w[]] | sort) as $known
      | (if ($known | length) > 0 then $known[($known | length) / 2 | floor] else 1 end) as $median
      | [$pkgs[] | {p: ., s: ($w[.] // $median)}]
      | sort_by(-.s, .p)
      | reduce .[] as $x ({load: [range($n) | 0], of: {}};
          (.load | to_entries | min_by(.value) | .key) as $k
          | .load[$k] += $x.s
          | .of[$x.p] = $k)
      | .of | to_entries[] | select(.value == $i) | .key'
}

# hotfix_packages: with RG_HOTFIX_BASE, every package (./<dir>) with a .go file the
# hotfix changed since its base, that still exists.
hotfix_packages() {
  git diff --name-only "$RG_HOTFIX_BASE" HEAD -- '*.go' | xargs -r -n1 dirname | sort -u \
    | while read -r d; do [ -d "$d" ] && printf './%s\n' "$d"; done \
    | xargs -r go list -f '{{.Dir}}' 2>/dev/null | sed "s|^$PWD|.|" | sort -u
}

run_step() {
  case "$1" in
    npm)
      local d scripts
      for d in frontends/web electron tools/protoc-gen-es; do
        # The install follows the checkout's own lockfile, not this runner's:
        # a hotfix gates a production commit that may predate the move to
        # pnpm (#3019), whose frontends carry package-lock.json and no
        # pnpm-lock.yaml. A frontend the checkout does not have is skipped;
        # one with a package.json but no lockfile cannot install frozen.
        [ -f "$d/package.json" ] || continue
        scripts=--ignore-scripts; [ "$d" = electron ] && scripts=
        # Current when the install's own stamp is newer than the lockfile
        # (pnpm: node_modules/.modules.yaml, touched after a no-op install,
        # which pnpm 12 leaves alone; npm: its node_modules/.package-lock.json
        # copy) and, for electron, its postinstall also fetched the binary its
        # tests load (node_modules/electron/path.txt). As scripts/merge-gate.sh
        # does: without it every electron test fails "Electron failed to
        # install correctly" (release gate a2783da3, 10 of 315).
        if [ -f "$d/pnpm-lock.yaml" ]; then
          if [ "$d/node_modules/.modules.yaml" -nt "$d/pnpm-lock.yaml" ] \
            && { [ "$d" != electron ] || [ -f "$d/node_modules/electron/path.txt" ]; }; then
            continue
          fi
          pnpm install --dir "$d" --frozen-lockfile $scripts --config.confirmModulesPurge=false --reporter=silent || return 1
          touch "$d/node_modules/.modules.yaml"
        elif [ -f "$d/package-lock.json" ]; then
          if [ "$d/node_modules/.package-lock.json" -nt "$d/package-lock.json" ] \
            && { [ "$d" != electron ] || [ -f "$d/node_modules/electron/path.txt" ]; }; then
            continue
          fi
          npm ci --prefix "$d" $scripts --no-audit --no-fund --loglevel=error || return 1
        else
          echo "npm: $d has a package.json but neither pnpm-lock.yaml nor package-lock.json"; return 1
        fi
      done ;;
    generate) task generate:check ;;
    docs) task generate:docs:check ;;
    lint) "$forge_sh" lint --quiet --no-fix ;;
    build) go build ./... ;;
    webbuild) task build:web:verify ;;
    demo-size) task demo:bundle-size ;;
    typecheck) npm --prefix frontends/web run --silent typecheck ;;
    vet) bash scripts/vet.sh ./... ;;
    merge)
      # The merge gate against the hotfix's base, every dependent of a change included.
      MG_REACH=all bash scripts/merge-gate.sh --base "$RG_HOTFIX_BASE" ;;
    test-go)
      local pkgs
      if [ -n "${RG_HOTFIX_BASE:-}" ]; then
        pkgs=$(hotfix_packages | tr '\n' ' ')
        [ -n "${pkgs// /}" ] || { echo "test-go: the hotfix changes no Go package"; return 0; }
      else
        pkgs=$(go_shard_packages | tr '\n' ' ')
      fi
      [ -n "${pkgs// /}" ] || { echo "test-go: shard $RG_GO_SHARD is empty; lower the shard count"; return 1; }
      echo "test-go${RG_GO_SHARD:+ shard $RG_GO_SHARD}: $(wc -w <<<"$pkgs") package pattern(s)"
      pinned_forge_bin || { echo "test-go: could not build the pinned forge (go.mod's github.com/reliant-labs/forge/cmd/forge)"; return 1; }
      # -vet=off (GO_TEST_VET, Taskfile.yml): the vet step's `vet.sh ./...`
      # vets every root-module package these test, test files included, and a
      # record needs every step. A hotfix gate has no vet step, but its merge
      # step's vet phase covers every package with a changed .go file. Not
      # integration or e2e: their build tags select files ./... never vets.
      # shellcheck disable=SC2086 # one word per package
      GO_TEST_VET=${GO_TEST_VET--vet=off} task test -- $pkgs ;;
    test-web) task test:web ;;
    test-electron) task test:electron ;;
    integration) task test:integration ;;
    e2e) task test:e2e ;;
    *) echo "unknown step $1"; return 2 ;;
  esac
}

# pinned_forge_bin builds the forge go.mod pins ONCE, before the Go tests, and
# exports it as FORGE_BIN, unless FORGE_BIN is already given. A test that runs
# forge (cmd/reliant/cmd's pinnedForge) otherwise builds it inside its own
# package's 5m budget: on a cold runner that build took 4m23s on main at
# 922d5ed89, and the package timed out (#2005). Building here spends the
# step's time, never a test's.
pinned_forge_bin() {
  [ -z "${FORGE_BIN:-}" ] || return 0
  local dir
  dir=$(mktemp -d "${TMPDIR:-/tmp}/release-gate-forge.XXXXXXXX") || return 1
  GOFLAGS=-trimpath go build -o "$dir/forge" github.com/reliant-labs/forge/cmd/forge || return 1
  export FORGE_BIN="$dir/forge"
}

sub=${1:-}; [ $# -gt 0 ] && shift
case "$sub" in
  run) cmd_run "$@" ;;
  check) cmd_check "$@" ;;
  latest) cmd_latest "$@" ;;
  show) record_of "$(resolve "${1:-HEAD}")" | jq . ;;
  assemble) cmd_assemble "$@" ;;
  hotfix)
    [ "${1:-}" = --base ] && [ -n "${2:-}" ] && [ -n "${3:-}" ] && [ $# -eq 3 ] || die "usage: $0 hotfix --base <base> <rev>"
    cmd_run --hotfix-base "$2" "$3" ;;
  go-timings-source) cmd_go_timings_source "$@" ;;
  go-timings) cmd_go_timings "$@" ;;
  -h|--help|"") sed -n '2,/^set -uo pipefail$/p' "$0" | sed '$d' ;;
  *) die "unknown command '$sub' (run, check, latest, show, assemble, go-timings-source, go-timings, hotfix)" ;;
esac
