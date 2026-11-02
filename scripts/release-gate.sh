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
#       a green record is not re-run unless --force. RG_ONLY=<step,...> runs a
#       subset and is never recorded.
#   release-gate.sh check [<rev>]
#       Is <rev> (default HEAD) release-ready? exit 0 green | 1 red | 3 no
#       record. A green on a first-parent ancestor (at most 50 back) also
#       passes when every change since it is release metadata: the version
#       field of electron/package.json and its lockfile, docs/, dev-docs/,
#       research/, *.md (a release PR's own diff).
#   release-gate.sh latest [<ref>]
#       Print the newest green-recorded sha on <ref>'s first-parent history
#       (default origin/main); exit 3 when there is none in the last 200.
#   release-gate.sh show <rev>
#       Print <rev>'s record.
#   release-gate.sh run --part <part> [--shard I/N] <rev>
#       Run one PART of the steps (below) and write a part record,
#       <records>/<sha>.part-<part>[-<I>of<N>].json. Never a release record,
#       never published: CI runs the parts as parallel jobs, then
#   release-gate.sh assemble <sha> <part.json>...
#       composes them into the record and publishes it. Fails closed, writing
#       nothing, unless every part is there (every shard 1..N of a sharded
#       part, exactly once) and every step ran on <sha>.
#
# Everything the merge gate (scripts/merge-gate.sh) leaves out runs here:
# golangci-lint and buf lint (lint), link errors (build), slow tests, the race
# detector and every Go package and vitest file the merge tier deferred (test),
# integration and e2e. dev-docs/merge-and-release-gates.md lists the split.
#
# Steps, in order (every step runs even after a failure; one table at the end):
#   npm          npm ci (pnpm install --frozen-lockfile for frontends/web)
#                wherever node_modules is stale (., frontends/web, electron,
#                tools/protoc-gen-es)
#   generate     task generate:check  (pinned forge generate is a no-op, + conventions)
#   docs         task generate:docs:check
#   lint         forge lint --no-fix, UNSCOPED: golangci over the module, buf
#                lint, every whole-project lane, the frontends
#   build        go build ./...
#   webbuild     task build:web:verify (lint, production renderer, artifact verification)
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
#   static       npm generate docs build webbuild typecheck vet
#   lint         npm lint
#   go           npm test-go     (sharded: --shard I/N; packages balanced on
#                                 scripts/release-gate-go-timings.json)
#   web          npm test-web test-electron integration e2e
# npm is in every part: each is a fresh runner that needs node_modules.
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

STEPS=(npm generate docs lint build webbuild typecheck vet test-go test-web test-electron integration e2e)
PARTS=(static lint go web)
declare -A PART_STEPS=(
  [static]="npm generate docs build webbuild typecheck vet"
  [lint]="npm lint"
  [go]="npm test-go"
  [web]="npm test-web test-electron integration e2e"
)
NOTES_REF=refs/notes/release-gate
die() { printf 'release-gate: %s\n' "$*" >&2; exit 2; }

cd "$(git rev-parse --show-toplevel)" || exit 2
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
      electron/package.json|electron/package-lock.json)
        # Only the version may differ: a dependency change is not metadata.
        node -e '
          const [a, b] = process.argv.slice(1).map(s => JSON.parse(s || "{}"));
          const strip = j => { delete j.version; if (j.packages && j.packages[""]) delete j.packages[""].version; return JSON.stringify(j); };
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
    if [ "$r" = PASS ] && release_metadata_only "$a" "$sha"; then
      echo "release-gate: $(git rev-parse --short "$sha") carries the green of $(git rev-parse --short "$a"), $n commit(s) back: every change since is release metadata"
      return 0
    fi
    if [ "$r" = PASS ]; then
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
    [ "$(result_of "$sha")" = PASS ] && { echo "$sha"; return 0; }
  done
  echo "release-gate: no green record in the last 200 commits of $ref" >&2
  return 3
}

cmd_run() {
  local force=0 rev sha snap t0 rc fail=0 s e step json steps_json md slot_wait part="" shard="" steps
  while [ $# -gt 0 ]; do
    case "$1" in
      --force) force=1; shift ;;
      --part) part=${2:-}; shift 2 || die "--part needs a name" ;;
      --shard) shard=${2:-}; shift 2 || die "--shard needs I/N" ;;
      *) break ;;
    esac
  done
  if [ -n "$part" ]; then
    [ -n "${PART_STEPS[$part]:-}" ] || die "unknown part '$part' (${PARTS[*]})"
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
  if [ "$force" = 0 ] && [ -z "${RG_ONLY:-}" ] && [ "$(result_of "$sha")" = PASS ]; then
    echo "release-gate: $(git rev-parse --short "$sha") already has a green record (--force re-runs it)"; return 0
  fi
  # One heavy slot for the whole run; every step nests inside it.
  local slotforge=${FORGE_BIN:-forge}
  if [ -z "${FORGE_SLOT_HELD:-}" ] && [ -z "${RG_IN_SLOT:-}" ] && "$slotforge" slot status --class heavy >/dev/null 2>&1; then
    RG_SLOT_T0=$(date +%s) RG_IN_SLOT=1 exec "$slotforge" slot run --class heavy -- "$0" run $([ "$force" = 1 ] && echo --force) ${part:+--part "$part"} ${shard:+--shard "$shard"} "$sha"
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
  for step in "${steps[@]}"; do
    if [ -n "${RG_ONLY:-}" ] && ! echo ",$RG_ONLY," | grep -q ",$step,"; then continue; fi
    s=$(date +%s)
    echo "release-gate: [$(date -u +%H:%M:%SZ)] $step ..."
    (cd "$snap" && export GOFLAGS="${GOFLAGS:+$GOFLAGS }-trimpath" FORGE_NO_MERGE_DRIVER=1 RG_GO_SHARD="$shard" && run_step "$step") >"$L/$step.log" 2>&1
    rc=$?; e=$(date +%s)
    [ "$rc" = 0 ] || fail=1
    echo "release-gate: $step $([ "$rc" = 0 ] && echo PASS || echo "FAIL (rc $rc; $L/$step.log)") in $((e - s))s"
    steps_json=$(jq -c --arg n "$step" --arg r "$([ "$rc" = 0 ] && echo PASS || echo FAIL)" --argjson s $((e - s)) '. + [{name: $n, result: $r, seconds: $s}]' <<<"$steps_json")
  done
  e=$(date +%s)
  json=$(jq -cn --arg sha "$sha" --arg r "$([ "$fail" = 0 ] && echo PASS || echo FAIL)" --argjson steps "$steps_json" \
    --arg started "$(date -u -d "@$t0" +%Y-%m-%dT%H:%M:%SZ)" --arg finished "$(date -u -d "@$e" +%Y-%m-%dT%H:%M:%SZ)" \
    --argjson seconds $((e - t0)) --argjson slot_wait "$slot_wait" --arg forge "$(sed -n 's/^forge_version:[[:space:]]*//p' "$snap/forge.yaml" | head -1)" \
    --arg host "$(hostname)" --arg by "$(git config user.email || whoami)" \
    '{sha: $sha, result: $r, started: $started, finished: $finished, seconds: $seconds, slot_wait_seconds: $slot_wait, forge: $forge, host: $host, by: $by, steps: $steps}')
  md=$( {
    echo "### release-gate: $(git rev-parse --short "$sha") $([ "$fail" = 0 ] && echo PASSED || echo FAILED)"
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
  [ $# -ge 1 ] || die "no part records for $(git rev-parse --short "$sha")"
  parts=$(jq -cs . "$@") || die "unreadable part record(s)"
  [ "$(jq --arg s "$sha" 'all(.[]; .sha == $s)' <<<"$parts")" = true ] || die "a part record is not of $sha"
  # Every part exactly once; a sharded part as one complete set 1/N..N/N.
  for p in "${PARTS[@]}"; do
    got=$(jq -r --arg p "$p" '[.[] | select(.part == $p) | .shard] | sort_by(split("/") | map(tonumber? // 0)) | join(",")' <<<"$parts")
    want=$(jq -r --arg p "$p" '[.[] | select(.part == $p) | .shard] as $s
      | if ($s | length) == 0 then "(none)"
        elif $s == [""] then ""
        else ($s[0] | split("/")[1] | tonumber) as $n | [range(1; $n + 1) | "\(.)/\($n)"] | join(",") end' <<<"$parts")
    [ "$got" = "$want" ] || die "part $p: ran [${got:-unsharded}], want [${want:-exactly one unsharded run}]; nothing recorded"
  done
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

# go_shard_packages: with RG_GO_SHARD=I/N, this shard's packages (1-based I)
# of those with test files, longest-processing-time first on the recorded
# `go test -race` package seconds in scripts/release-gate-go-timings.json (a
# package with no entry weighs the median); ties break on the path, so every
# shard computes the same split. Without it, ./... .
go_shard_packages() {
  [ -n "${RG_GO_SHARD:-}" ] || { echo ./...; return; }
  local i=${RG_GO_SHARD%/*} n=${RG_GO_SHARD#*/} timings=scripts/release-gate-go-timings.json
  [ -f "$timings" ] || timings=/dev/null
  go list -f '{{if or .TestGoFiles .XTestGoFiles}}{{.ImportPath}}{{end}}' ./... \
    | jq -rRn --slurpfile t <(cat "$timings" 2>/dev/null || echo '{}') --argjson i "$((i - 1))" --argjson n "$n" '
      ($t[0] // {}) as $w
      | [inputs | select(length > 0)] as $pkgs
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

run_step() {
  case "$1" in
    npm)
      local d scripts
      for d in . frontends/web electron tools/protoc-gen-es; do
        if [ -f "$d/pnpm-lock.yaml" ]; then
          # pnpm rewrites node_modules/.modules.yaml on every install it completes.
          [ "$d/node_modules/.modules.yaml" -nt "$d/pnpm-lock.yaml" ] && continue
          pnpm install --dir "$d" --frozen-lockfile --ignore-scripts --config.confirmModulesPurge=false --reporter=silent || return 1
          continue
        fi
        [ -f "$d/package-lock.json" ] || continue
        # Current when npm's lockfile copy is newer than the lockfile and, for
        # electron, its postinstall also fetched the binary its tests load
        # (node_modules/electron/path.txt). As scripts/merge-gate.sh does:
        # without it every electron test fails "Electron failed to install
        # correctly" (release gate a2783da3, 10 of 315).
        if [ "$d/node_modules/.package-lock.json" -nt "$d/package-lock.json" ] \
          && { [ "$d" != electron ] || [ -f "$d/node_modules/electron/path.txt" ]; }; then
          continue
        fi
        scripts=--ignore-scripts; [ "$d" = electron ] && scripts=
        npm ci --prefix "$d" $scripts --no-audit --no-fund --loglevel=error || return 1
      done ;;
    generate) task generate:check ;;
    docs) task generate:docs:check ;;
    lint)
      local pin
      pin=$(sed -n 's/^forge_version:[[:space:]]*//p' forge.yaml | head -1)
      if [ -n "${FORGE_BIN:-}" ]; then "$FORGE_BIN" lint --quiet --no-fix
      else env GOFLAGS=-trimpath go run "github.com/reliant-labs/forge/cmd/forge@$pin" lint --quiet --no-fix; fi ;;
    build) go build ./... ;;
    webbuild) task build:web:verify ;;
    typecheck) npm --prefix frontends/web run --silent typecheck ;;
    vet) go vet ./... ;;
    test-go)
      local pkgs
      pkgs=$(go_shard_packages | tr '\n' ' ')
      [ -n "${pkgs// /}" ] || { echo "test-go: shard $RG_GO_SHARD is empty; lower the shard count"; return 1; }
      echo "test-go${RG_GO_SHARD:+ shard $RG_GO_SHARD}: $(wc -w <<<"$pkgs") package pattern(s)"
      # shellcheck disable=SC2086 # one word per package
      task test -- $pkgs ;;
    test-web) task test:web ;;
    test-electron) task test:electron ;;
    integration) task test:integration ;;
    e2e) task test:e2e ;;
    *) echo "unknown step $1"; return 2 ;;
  esac
}

sub=${1:-}; [ $# -gt 0 ] && shift
case "$sub" in
  run) cmd_run "$@" ;;
  check) cmd_check "$@" ;;
  latest) cmd_latest "$@" ;;
  show) record_of "$(resolve "${1:-HEAD}")" | jq . ;;
  assemble) cmd_assemble "$@" ;;
  -h|--help|"") sed -n '2,78p' "$0" ;;
  *) die "unknown command '$sub' (run, check, latest, show, assemble)" ;;
esac
