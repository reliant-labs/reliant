#!/usr/bin/env bash
# Hermetic test of scripts/check-compat.sh over a throwaway git repository and a fake gh.
set -euo pipefail
SCRIPT="$(cd "$(dirname "$0")" && pwd)/check-compat.sh"
T="$(mktemp -d)"; trap 'rm -rf "$T"' EXIT
fail=0
g() { git -C "$T/r" -c user.name=t -c user.email=t@t -c commit.gpgsign=false "$@"; }
check() { # <name> <want exit> <args...>
  local name="$1" want="$2" rc=0; shift 2
  (cd "$T/r" && env "$@" COMPAT_GH="$T/gh" COMPAT_REPO=o/r CI_BASE=base bash "$SCRIPT" "${CMD:-migrations}") >"$T/out" 2>&1 || rc=$?
  if [ "$rc" = "$want" ]; then echo "ok: $name"; else echo "FAIL: $name (exit $rc, want $want)"; cat "$T/out"; fail=1; fi
}
# The fake gh: labels and events come from files the cases write; it applies --jq with jq.
cat >"$T/gh" <<EOF
#!/usr/bin/env bash
f=""; src=""
while [ \$# -gt 0 ]; do case "\$1" in --jq) f="\$2"; shift 2 ;; */labels|*/events) src="\$1"; shift ;; *) shift ;; esac; done
# repos/o/r/issues/<n>/<kind>: $T/<kind>-<n>.json when a case wrote one, else $T/<kind>.json.
n="\${src%/*}"; n="\${n##*/}"; kind="\${src##*/}"
file="$T/\$kind-\$n.json"; [ -f "\$file" ] || file="$T/\$kind.json"
jq -r "\$f" "\$file" || exit
# FAKE_GH_EVENTS_EXIT: print the events, then fail, as gh does when a later page errors.
case "\$kind" in events) exit "\${FAKE_GH_EVENTS_EXIT:-0}" ;; esac
EOF
chmod +x "$T/gh"
labels() { printf '%s\n' "$1" >"$T/labels.json"; }
events() { printf '%s\n' "$1" >"$T/events.json"; }

mkdir -p "$T/r/db/migrations"; g init -q -b main
printf -- '-- forge:backward-compatible — additive\nCREATE TABLE a (id int);\n' >"$T/r/db/migrations/1_a.up.sql"
g add -A; g commit -qm init; g tag base
check "no migration since the base passes" 0
printf -- '-- forge:backward-compatible — additive\nALTER TABLE a ADD COLUMN b int;\n' >"$T/r/db/migrations/2_b.up.sql"
g add -A; g commit -qm add
check "a backward-compatible migration passes" 0
printf -- '-- forge:breaks-rollback drops a column the previous release reads\nALTER TABLE a DROP COLUMN b;\n' >"$T/r/db/migrations/3_c.up.sql"
g add -A; g commit -qm drop
check "breaks-rollback with no PR fails" 1
grep -q 'migration:breaks-rollback-approved' "$T/out" || { echo "FAIL: the refusal does not name the label"; fail=1; }
labels '[]'; events '[]'
check "breaks-rollback with no label fails" 1 CI_PR=7
labels '[{"name":"migration:breaks-rollback-approved"}]'
events '[{"event":"labeled","label":{"name":"migration:breaks-rollback-approved"},"actor":{"login":"reliant-labs[bot]"}}]'
# The owner and the Reliant bot (labelling on the owner's instruction) count; any other
# account does not (owner decision, 2026-11-19).
check "the Reliant bot's label passes" 0 CI_PR=7
grep -q 'applied by reliant-labs\[bot\]' "$T/out" || { echo "FAIL: the approval does not name the bot"; fail=1; }
check "COMPAT_OWNERS narrowed to the owner refuses the bot's label" 1 CI_PR=7 COMPAT_OWNERS=reliant-01
events '[{"event":"labeled","label":{"name":"migration:breaks-rollback-approved"},"actor":{"login":"some-author"}}]'
check "the label applied by an unrelated account does not count" 1 CI_PR=7
events '[{"event":"labeled","label":{"name":"migration:breaks-rollback-approved"},"actor":{"login":"github-actions[bot]"}}]'
check "the label applied by another bot does not count" 1 CI_PR=7
events '[{"event":"labeled","label":{"name":"migration:breaks-rollback-approved"},"actor":{"login":"reliant-labs[bot]"}},{"event":"labeled","label":{"name":"migration:breaks-rollback-approved"},"actor":{"login":"some-author"}}]'
check "an unrelated account re-applying the bot's label does not count" 1 CI_PR=7
events '[{"event":"labeled","label":{"name":"migration:breaks-rollback-approved"},"actor":{"login":"reliant-01"}}]'
check "the owner's label passes" 0 CI_PR=7
check "an owner event from a paginated read that then failed does not count" 1 CI_PR=7 FAKE_GH_EVENTS_EXIT=1
labels '[]'
check "the owner's label, since removed, does not" 1 CI_PR=7
CMD=proto check "no buf.yaml: proto has nothing to judge" 0

# A migration far larger than a pipe buffer, the marker on its first line: the
# marker must be found (a `git show | grep -q` under pipefail would miss it).
g tag -f base >/dev/null
{ printf -- '-- forge:breaks-rollback rewrites a table the previous release reads\n'
  for i in $(seq 1 20000); do printf 'INSERT INTO a (id) VALUES (%d);\n' "$i"; done; } >"$T/r/db/migrations/4_big.up.sql"
g add -A; g commit -qm big
[ "$(wc -c <"$T/r/db/migrations/4_big.up.sql")" -gt 262144 ] || { echo "FAIL: the large migration is not larger than a pipe buffer"; fail=1; }
labels '[]'; events '[]'
check "a large breaks-rollback migration with no label fails" 1 CI_PR=7

# Merge-queue groups (CI_QUEUE_ENTRY=1): main squash-merges, so each entry is one
# commit on its predecessor, and each is judged alone against its own PR's labels.
g tag -f base >/dev/null
printf -- '-- forge:breaks-rollback drops table a\nDROP TABLE a;\n' >"$T/r/db/migrations/5_drop.up.sql"
g add -A; g commit -qm "entry #10"; g tag e10
printf -- '-- forge:backward-compatible — additive\nCREATE TABLE z (id int);\n' >"$T/r/db/migrations/6_z.up.sql"
g add -A; g commit -qm "entry #11"; g tag e11
printf '%s\n' '[{"name":"migration:breaks-rollback-approved"}]' >"$T/labels-10.json"
printf '%s\n' '[{"event":"labeled","label":{"name":"migration:breaks-rollback-approved"},"actor":{"login":"reliant-01"}}]' >"$T/events-10.json"
printf '%s\n' '[]' >"$T/labels-11.json"; printf '%s\n' '[]' >"$T/events-11.json"
check "queue: a compatible entry behind an approved break passes" 0 CI_QUEUE_ENTRY=1 CI_PR=11
check "queue (group diff, no entry isolation) would have blamed it" 1 CI_PR=11
g checkout -q e10
check "queue: the approved break's own entry passes" 0 CI_QUEUE_ENTRY=1 CI_PR=10
printf '%s\n' '[]' >"$T/labels-10.json"; printf '%s\n' '[]' >"$T/events-10.json"
check "queue: an unapproved break fails in its own entry's run" 1 CI_QUEUE_ENTRY=1 CI_PR=10
g checkout -q e11
check "queue: an unapproved earlier break does not fail the entry behind it" 0 CI_QUEUE_ENTRY=1 CI_PR=11
printf '%s\n' '[{"name":"migration:breaks-rollback-approved"}]' >"$T/labels-11.json"
printf '%s\n' '[{"event":"labeled","label":{"name":"migration:breaks-rollback-approved"},"actor":{"login":"reliant-01"}}]' >"$T/events-11.json"
g checkout -q e10
check "queue: a trailing entry's approval does not authorize an earlier break" 1 CI_QUEUE_ENTRY=1 CI_PR=10
g checkout -q main

if command -v buf >/dev/null; then
  mkdir -p "$T/r/proto/x/v1"
  printf 'version: v2\nmodules:\n  - path: proto\nbreaking:\n  use:\n    - FILE\n' >"$T/r/buf.yaml"
  printf 'syntax = "proto3";\npackage x.v1;\nmessage M {\n  string a = 1;\n  string b = 2;\n}\n' >"$T/r/proto/x/v1/x.proto"
  g add -A; g commit -qm proto; g tag -f base >/dev/null
  printf 'syntax = "proto3";\npackage x.v1;\nmessage M {\n  string a = 1;\n  string b = 2 [deprecated = true];\n  string c = 3;\n}\n' >"$T/r/proto/x/v1/x.proto"
  g commit -qam deprecate
  CMD=proto check "a deprecation plus an addition is compatible" 0
  printf 'syntax = "proto3";\npackage x.v1;\nmessage M {\n  string a = 1;\n  string c = 3;\n}\n' >"$T/r/proto/x/v1/x.proto"
  g commit -qam remove
  labels '[]'; events '[]'
  CMD=proto check "a removed field is breaking" 1 CI_PR=8
  labels '[{"name":"breaking:approved"}]'
  events '[{"event":"labeled","label":{"name":"breaking:approved"},"actor":{"login":"reliant-01"}}]'
  CMD=proto check "the owner's breaking:approved passes it" 0 CI_PR=8

  # idempotency_level: buf's RPC_SAME_IDEMPOTENCY_LEVEL reports every change of it.
  # Only IDEMPOTENCY_UNKNOWN -> NO_SIDE_EFFECTS or IDEMPOTENT is additive (the wire
  # is unchanged); every other direction still needs the owner's label.
  # svc <A's option> <B's option>: rewrite x.proto with a service of two rpcs.
  svc() {
    printf 'syntax = "proto3";\npackage x.v1;\nmessage M {\n  string a = 1;\n  string c = 3;\n}\nservice S {\n  rpc A(M) returns (M) {%s}\n  rpc B(M) returns (M) {%s}\n}\n' "$1" "$2" >"$T/r/proto/x/v1/x.proto"
  }
  NSE='option idempotency_level = NO_SIDE_EFFECTS;'
  IDEM='option idempotency_level = IDEMPOTENT;'
  labels '[]'; events '[]'
  svc "" ""; g commit -qam "rpcs, no level"; g tag -f base >/dev/null
  svc "$NSE" ""; g commit -qam "A declares NO_SIDE_EFFECTS"
  CMD=proto check "UNKNOWN -> NO_SIDE_EFFECTS is additive: passes with no label" 0 CI_PR=8
  grep -q 'additive, not breaking' "$T/out" && grep -q 'RPC "A" on service "S"' "$T/out" \
    || { echo "FAIL: the pass does not name the additive rpc"; cat "$T/out"; fail=1; }
  CMD=proto check "UNKNOWN -> NO_SIDE_EFFECTS passes with no PR at all" 0
  svc "$NSE" "$IDEM"; g commit -qam "B declares IDEMPOTENT"
  CMD=proto check "UNKNOWN -> IDEMPOTENT is additive too" 0 CI_PR=8
  PATH="$T/badjq:$PATH"; mkdir -p "$T/badjq"; printf '#!/bin/sh\nexit 3\n' >"$T/badjq/jq"; chmod +x "$T/badjq/jq"
  CMD=proto check "a jq that cannot classify the findings exempts none" 1 CI_PR=8
  PATH="${PATH#"$T/badjq:"}"
  g tag -f base >/dev/null
  svc "" "$IDEM"; g commit -qam "A drops NO_SIDE_EFFECTS"
  CMD=proto check "NO_SIDE_EFFECTS -> UNKNOWN still refuses" 1 CI_PR=8
  grep -q 'from "NO_SIDE_EFFECTS" to "IDEMPOTENCY_UNKNOWN"' "$T/out" || { echo "FAIL: the refusal does not name the finding"; cat "$T/out"; fail=1; }
  g reset -q --hard base
  svc "$IDEM" "$IDEM"; g commit -qam "A: NO_SIDE_EFFECTS -> IDEMPOTENT"
  CMD=proto check "NO_SIDE_EFFECTS -> IDEMPOTENT still refuses" 1 CI_PR=8
  g reset -q --hard base
  svc "$NSE" "$NSE"; g commit -qam "B: IDEMPOTENT -> NO_SIDE_EFFECTS"
  CMD=proto check "IDEMPOTENT -> NO_SIDE_EFFECTS still refuses" 1 CI_PR=8
  g reset -q --hard base
  svc "" ""; g commit -qam "rpcs, no level"; g tag -f base >/dev/null
  printf 'syntax = "proto3";\npackage x.v1;\nmessage M {\n  string a = 1;\n}\nservice S {\n  rpc A(M) returns (M) {%s}\n  rpc B(M) returns (M) {}\n}\n' "$NSE" >"$T/r/proto/x/v1/x.proto"
  g commit -qam "A declares NO_SIDE_EFFECTS, and a field goes"
  CMD=proto check "an additive level beside a removed field still refuses" 1 CI_PR=8
  grep -q 'deleted field "3"\|field "3"' "$T/out" || { echo "FAIL: the refusal does not name the removed field"; cat "$T/out"; fail=1; }
  if grep 'FAIL:\|^proto/' "$T/out" | grep -q 'IDEMPOTENCY_UNKNOWN'; then
    echo "FAIL: the additive finding is listed as blocking"; cat "$T/out"; fail=1
  fi
else
  echo "skip: buf is not on PATH, so the proto cases did not run"
fi

# Deleting buf.yaml must not switch the proto check off (refused before buf runs).
mkdir -p "$T/r/proto/x/v1"
printf 'version: v2\nmodules:\n  - path: proto\nbreaking:\n  use:\n    - FILE\n' >"$T/r/buf.yaml"
printf 'syntax = "proto3";\npackage x.v1;\nmessage M {\n  string a = 1;\n  string b = 2;\n}\n' >"$T/r/proto/x/v1/x.proto"
g add -A; g commit -q --allow-empty -m "proto base"; g tag -f base >/dev/null
g rm -q buf.yaml
printf 'syntax = "proto3";\npackage x.v1;\nmessage M {\n  string a = 1;\n}\n' >"$T/r/proto/x/v1/x.proto"
g add -A; g commit -qm "drop buf.yaml and a field"
labels '[]'; events '[]'
CMD=proto check "deleting buf.yaml beside a breaking change fails" 1 CI_PR=9
grep -q 'buf.yaml' "$T/out" || { echo "FAIL: the refusal does not name buf.yaml"; fail=1; }
labels '[{"name":"breaking:approved"}]'
events '[{"event":"labeled","label":{"name":"breaking:approved"},"actor":{"login":"some-author"}}]'
CMD=proto check "an unrelated account's breaking:approved does not accept it" 1 CI_PR=9
events '[{"event":"labeled","label":{"name":"breaking:approved"},"actor":{"login":"reliant-labs[bot]"}}]'
CMD=proto check "the Reliant bot's breaking:approved accepts it" 0 CI_PR=9
events '[{"event":"labeled","label":{"name":"breaking:approved"},"actor":{"login":"reliant-01"}}]'
CMD=proto check "the owner's breaking:approved accepts it" 0 CI_PR=9
exit "$fail"
