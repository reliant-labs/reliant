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
while [ \$# -gt 0 ]; do case "\$1" in --jq) f="\$2"; shift 2 ;; */labels) src="$T/labels.json"; shift ;; */events) src="$T/events.json"; shift ;; *) shift ;; esac; done
jq -r "\$f" "\$src"
# GH_EVENTS_FAIL: print the events page, then fail as a later page would.
if [ -n "\${GH_EVENTS_FAIL:-}" ] && [ "\$src" = "$T/events.json" ]; then exit 1; fi
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
check "the label applied by a bot does not count" 1 CI_PR=7
events '[{"event":"labeled","label":{"name":"migration:breaks-rollback-approved"},"actor":{"login":"reliant-01"}}]'
check "the owner's label passes" 0 CI_PR=7
check "an owner's event on a page before a failed one does not count" 1 CI_PR=7 GH_EVENTS_FAIL=1
labels '[]'
check "the owner's label, since removed, does not" 1 CI_PR=7
CMD=proto check "no buf.yaml: proto has nothing to judge" 0

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
events '[{"event":"labeled","label":{"name":"breaking:approved"},"actor":{"login":"reliant-labs[bot]"}}]'
CMD=proto check "a bot's breaking:approved does not accept it" 1 CI_PR=9
events '[{"event":"labeled","label":{"name":"breaking:approved"},"actor":{"login":"reliant-01"}}]'
CMD=proto check "the owner's breaking:approved accepts it" 0 CI_PR=9
exit "$fail"
