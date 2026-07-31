#!/usr/bin/env bash
# dogfood-prep.sh — rebuild BOTH binaries and prove they came from the same tree.
#
# Run this before every dogfood/mimic phase. Paste its output into the run
# record; the phase brief should refuse to start without it.
#
# WHY THIS EXISTS
#
# `reliant` embeds forge through a `replace github.com/reliant-labs/forge =>
# ../forge` in its go.mod, so `reliant forge …` is a COPY of forge compiled
# into the reliant binary — not a call into whatever `forge` is on PATH. Two
# binaries, one source tree, independent build times.
#
# A dogfood run measured `reliant forge` while `forge` was 19 hours newer. The
# phase scaffolded against yesterday's forge, spent ~10 minutes fighting a bug
# that had been fixed the night before, and the gate passed — because the gate
# was spelled `reliant forge` too. Nothing anywhere signalled it. The run looked
# clean and was measuring the wrong tree.
#
# Rebuilding one is not enough, and neither is checking that `forge version`
# looks recent: both binaries report "dev", so the ONLY reliable signal is
# rebuilding both from the current commit and showing their timestamps.
set -euo pipefail

FORGE_DIR="${FORGE_DIR:-$HOME/src/reliant-labs/forge}"
RELIANT_DIR="${RELIANT_DIR:-$HOME/src/reliant-labs/reliant}"
CONTROL_PLANE_DIR="${CONTROL_PLANE_DIR:-$HOME/src/reliant-labs/control-plane}"
BIN="$(go env GOPATH)/bin"

echo "── dogfood prep ─────────────────────────────────────────"

for d in "$FORGE_DIR" "$RELIANT_DIR"; do
  if [ ! -d "$d" ]; then
    echo "✗ missing repo: $d" >&2
    exit 1
  fi
done

# A dirty tree is not disqualifying — this is a dev loop — but an unrecorded
# dirty tree makes the measurement unreproducible, so say so.
for d in "$FORGE_DIR" "$RELIANT_DIR"; do
  name="$(basename "$d")"
  commit="$(git -C "$d" rev-parse --short HEAD)"
  branch="$(git -C "$d" rev-parse --abbrev-ref HEAD)"
  dirty=""
  if [ -n "$(git -C "$d" status --porcelain)" ]; then
    dirty="  ⚠️  DIRTY (uncommitted changes — record this)"
  fi
  printf '%-8s %s @ %s%s\n' "$name" "$branch" "$commit" "$dirty"
done

echo
echo "building forge …"
(cd "$FORGE_DIR" && go build -o "$BIN/forge" ./cmd/forge)

echo "building reliant … (embeds forge via the go.mod replace — MUST be rebuilt too)"
(cd "$RELIANT_DIR" && go build -o "$BIN/reliant" ./cmd/reliant)

# reliant-dev carries the forensics commands that read the database directly
# (workflow ps | node | analyze | forensics). §2 and §3 are driven from it, so a
# stale one misreports the run exactly as a stale reliant does.
echo "building reliant-dev … (the forensics CLI §3 runs on)"
(cd "$RELIANT_DIR" && go build -o "$BIN/reliant-dev" ./tools/reliant-dev)

echo
echo "── binaries ─────────────────────────────────────────────"
ls -la "$BIN/forge" "$BIN/reliant" "$BIN/reliant-dev" | awk '{printf "%-10s %s %s %s\n", $NF, $6, $7, $8}'

# The check that would have caught the corrupted run: all must be newer than
# the newest source file in either tree.
for b in forge reliant reliant-dev; do
  stale=$(find "$FORGE_DIR" "$RELIANT_DIR" \
    \( -name '*.go' -o -name '*.tmpl' -o -name '*.k' \) \
    -newer "$BIN/$b" -not -path '*/.git/*' -print -quit 2>/dev/null || true)
  if [ -n "$stale" ]; then
    echo "✗ $b is OLDER than $stale — rebuild failed?" >&2
    exit 1
  fi
done

echo
echo "✅ all binaries rebuilt from the same tree, newer than all sources."
echo "   Use EITHER spelling; they are now the same forge."

# ── forge's own tests are green ──────────────────────────────────────────────
#
# A loop that starts against a red forge measures the wrong thing: every hour
# an agent then spends fighting forge's output is an hour spent on a defect
# the test suite already knew about. 33 seconds here against a plausible hour
# there is not a close call.
#
# This is a GATE, not a report — it exits non-zero. That is the point. If it
# fires, fix forge before starting a run; do not start the run and plan to
# read the failure later.
#
# forge only, deliberately: `go test ./...` here is seconds because it excludes
# the e2e tag (see below) and needs no database. reliant's suite needs Postgres
# and is minutes, so gating on it would make prep something people skip, and a
# gate people skip is worse than no gate.
echo
echo "── forge tests ──────────────────────────────────────────────────────────"
test_out="$(mktemp)"
if (cd "$FORGE_DIR" && go test ./...) >"$test_out" 2>&1; then
  echo "✅ forge unit tests green"
  rm -f "$test_out"
else
  # Drop the per-package `ok`/`no test files` noise; show what actually broke.
  grep -Ev '^(ok |\?[[:space:]])' "$test_out" | head -40
  rm -f "$test_out"
  echo
  echo "✗ forge's own tests are RED — fix forge before starting a loop." >&2
  echo "  Starting now means measuring agent time against a defect forge" >&2
  echo "  already knows about. Re-run: (cd $FORGE_DIR && go test ./...)" >&2
  exit 1
fi

# ── The e2e suite compiles ───────────────────────────────────────────────────
#
# forge carries 93 test functions behind `//go:build e2e`. Until they were
# wired into CI, `go test ./...` did not carry the tag and the ONE tagged CI
# invocation passed `-run 'TestE2EFixtureCorpus'` — so 92 of the 93 never ran
# and, more to the point, never COMPILED anywhere. A suite nothing builds
# rots silently: the first signal is a green PR followed by a lost hour.
#
# This is `vet`, not `test` — it type-checks the tagged tree in seconds
# without scaffolding anything. Running the suite is CI's job. Proving it
# still builds before a dogfood run is cheap enough to always do.
echo
echo "── e2e suite ────────────────────────────────────────────────────────────"
if (cd "$FORGE_DIR" && go vet -tags e2e ./... 2>&1); then
  echo "✅ forge e2e suite compiles (go vet -tags e2e ./...)"
else
  echo "✗ forge e2e suite does NOT compile — the tagged tests have rotted." >&2
  exit 1
fi

# ── Toolchain completeness ───────────────────────────────────────────────────
#
# A missing tool does not fail a gate here — it makes the gate PASS without
# running. That is the exact defect shape this exercise keeps rediscovering:
# the check reports green because it asked nothing.
#
# Concretely, forge's own e2e tests are written as
#   if toolAvailable("golangci-lint") { ...assertions... }
# with no else. Absent the binary the assertions simply do not execute and the
# test reports PASS. A dogfood run inherits the same hazard through the phase
# gates, which shell out to these tools: `forge lint` needs golangci-lint,
# `forge generate` needs buf, the frontend gate needs node/npm, and the KCL
# render needs kcl. Report presence up front so a green phase gate can be
# trusted to be green for the reason it appears to be.
#
# This is NOT a health check and must not grow into one — `forge doctor` /
# `forge env status` own "is the environment running", per the note below.
# This answers only "can the gates actually execute", which is prep's job.
echo
echo "── toolchain ────────────────────────────────────────────────────────────"
missing=""
for t in node npm golangci-lint buf kcl; do
  if p="$(command -v "$t" 2>/dev/null)"; then
    printf '  %-14s %s\n' "$t" "$p"
  else
    printf '  %-14s ⚠️  MISSING — gates that use it will pass WITHOUT running\n' "$t"
    missing="$missing $t"
  fi
done
if [ -n "$missing" ]; then
  echo "  ⚠️  missing:$missing — record this; any gate depending on them is not a signal."
fi

# ── Stack readiness ──────────────────────────────────────────────────────────
#
# `forge doctor` answers this and it is the ONLY thing that should. Do not add
# port probes here: a bespoke prober in this script would be a fourth health
# mechanism beside `forge doctor`, KCL's `schema HealthCheck`, and serverkit's
# `FlowChecks` — and the first three would stay half-wired forever. Whatever is
# missing gets fixed IN forge, where every project gets it.
#
# Why this matters more than it looks: when a component is down the failure
# does not say so. A dead daemon leaves a run reporting `running` while nothing
# executes, and a Postgres under load surfaced to the CLI as "your API token
# was rejected — run `reliant auth token create`", which is an hour of rotating
# credentials that were never the problem.
# `forge env status` owns "is the env running": it reads the SAME resolved KCL
# ports `forge env up` uses (never a guessed default), and reports per service
# the listener state, the holding pid, whether the process is forge-owned,
# build-freshness against repo HEAD, and a loud flag when two processes serve
# one host service. Do not hand-roll port probes here — a bespoke prober would
# be a duplicate that drifts.
#
# `env status` covers HOST services only. Two builds of this tree are outside
# it: the ~/go/bin CLI binaries (covered by the rebuild check above) and the
# CLUSTER workloads — daemon-gateway above all, which serves every daemon
# stream and is a third compile of internal/grpc from this repo. On 2026-07-27
# it was five days old while the host reliant-api-server was minutes old, and
# nothing reported it: the deployed gateway predated the fix for two daemons
# evicting each other, so the flap it had already been taught not to cause
# survived two debugging sessions. `env status` should grow cluster-workload
# freshness — that belongs in forge, where every project gets it. Until then
# the image below is the only place the gateway's build is visible.
#
# Why it earns its place in prep: when a component is down the failure does not
# say so. A dead daemon leaves a run reporting `running` while nothing
# executes, and a Postgres under load surfaced to the CLI as "your API token
# was rejected — run `reliant auth token create`" — an hour of rotating
# credentials that were never the problem.
echo
echo "── env ──────────────────────────────────────────────────────────────────"
if [ -d "$CONTROL_PLANE_DIR" ]; then
  env_out="$(cd "$CONTROL_PLANE_DIR" && forge env status "${DOGFOOD_ENV:-dev}" --silence-experimental 2>&1)" || true
  printf '%s\n' "$env_out"

  # GATE on it, do not merely print it. `forge env status` is a REPORT: it exits
  # 0 whether every service is up or every one is down, and it has no --strict
  # or --gate flag. So the line above answered the question correctly and
  # nothing consumed the answer.
  #
  # Measured: a run was started while reliant-temporal-worker had been dead for
  # two hours. Temporal accepted the workflow, no worker existed to execute it,
  # and `reliant workflow status` reported PENDING — indefinitely, with nothing
  # anywhere saying why. `reliant-dev workflow ps` said "No workflows running" at the same
  # moment. The prep output above had already printed `○ reliant-temporal-worker
  # down`; prep exited 0 and the run proceeded.
  #
  # This greps forge's own output rather than probing ports — the standing rule
  # against a bespoke prober still holds, and this is not one. It consumes what
  # forge already decided. Delete it the day `forge env status` grows a real
  # gating mode, which is where this check belongs.
  if down="$(printf '%s\n' "$env_out" | grep -E '○|[[:space:]]down$|[[:space:]]down[[:space:]]')"; then
    echo
    echo "✗ a host service is DOWN — the run would sit PENDING forever:" >&2
    printf '%s\n' "$down" | sed 's/^/    /' >&2
    echo "  Fix: (cd $CONTROL_PLANE_DIR && forge env up ${DOGFOOD_ENV:-dev})" >&2
    exit 1
  fi
  echo "✅ every host service reports up"
else
  echo "  ✗ $CONTROL_PLANE_DIR not found — cannot verify the env" >&2
  exit 1
fi

# ── The DAEMON's running revision ────────────────────────────────────────────
#
# The rebuild check above proves the BINARIES on disk are current. It says
# nothing about the daemon PROCESS, which is what actually executes a run and
# carries the workflow definition compiled into it.
#
# Measured: the daemon had been up nine hours at an older revision while
# ~/go/bin/reliant was minutes old. Starting a run then would have measured the
# PREVIOUS charter — a different phase graph, different unit tooling, different
# briefs — and every timing would have been attributed to changes that were not
# in the running image. Nothing reported it; `daemon status` prints the revision
# and no one compares it.
echo
echo "── daemon ───────────────────────────────────────────────────────────────"
daemon_out="$(reliant daemon status 2>&1)" || true
printf '%s\n' "$daemon_out" | sed 's/^/  /'
daemon_rev="$(printf '%s\n' "$daemon_out" | awk '/Revision:/{print $2}')"
head_rev="$(git -C "$RELIANT_DIR" rev-parse HEAD)"
if [ -z "$daemon_rev" ]; then
  echo "✗ no daemon running — start it before the run." >&2
  exit 1
fi
if [ "$daemon_rev" != "$head_rev" ]; then
  echo "✗ the daemon is running $daemon_rev but reliant HEAD is $head_rev." >&2
  echo "  It executes the run and embeds the workflow definition, so the run" >&2
  echo "  would measure the OLD charter. Restart it." >&2
  exit 1
fi
echo "✅ daemon revision matches reliant HEAD"
echo
echo "  The daemon is NOT an env service — it dials the gateway and binds no"
echo "  port, so it never appears in env status. The daemon section above is"
echo "  what covers it."
echo
echo "  Neither is the daemon-gateway it dials. Report every pod in the env's"
echo "  namespace with its image and start time — a name filter here printed a"
echo "  bare header and exited 0 against a namespace that never held the pod it"
echo "  named, which is a false green in the one check meant to catch stale"
echo "  builds. List everything and let the reader see what is actually there."
if pods="$(kubectl -n "control-plane-${DOGFOOD_ENV:-dev}" get pods --no-headers \
  -o 'custom-columns=POD:.metadata.name,IMAGE:.spec.containers[0].image,STARTED:.status.startTime' 2>/dev/null)" &&
  [ -n "$pods" ]; then
  printf '%s\n' "$pods" | sed 's/^/    /'
else
  echo "    ⚠️  no pods in control-plane-${DOGFOOD_ENV:-dev} (or the cluster is down)"
fi
