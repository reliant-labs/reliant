#!/usr/bin/env bash
#
# Block until proxy.golang.org (and the checksum database) actually SERVE a
# just-pushed module version.
#
# ── The failure this exists to stop ───────────────────────────────────
#
# `git push --atomic` returns the instant GitHub has the tag. The Go module
# proxy has not heard of it yet. proxy.golang.org discovers a new version by
# polling the origin, and sum.golang.org only records a hash once the proxy
# has fetched the tree — so for a window measured in MINUTES after a
# successful release push, `go mod download github.com/reliant-labs/forge@vX.Y.Z`
# 404s for everyone on earth, including our own CI.
#
# That is exactly what has been happening to E2E Scaffold on every release
# commit. The job scaffolds a project that requires the version just tagged
# and runs `go mod tidy`, which asks the proxy for a tag the proxy does not
# have. It fails, for ~6 minutes, on a release that is completely correct.
#
# The cost is not the red X. It is that a release lane which is red on every
# single release teaches everyone to ignore it, which is precisely how a tag
# that is genuinely broken ships unnoticed. A check nobody believes is worse
# than no check — the same reasoning that widened the npm window in
# verify-npm-published.sh (#341), which this script deliberately mirrors.
#
# ── Why curl and not `go mod download` ────────────────────────────────
#
# `go mod download` is the operation we are waiting to become possible, so it
# looks like the obvious probe. It is the wrong one here, for a reason that
# bites on a developer laptop rather than in CI: GOPRIVATE / GONOSUMDB /
# GOFLAGS are routinely set in this workspace (the sibling repos are private),
# and any of them can make the go command bypass the proxy entirely and answer
# from the origin or a local cache. The probe would then report "ingested"
# while the PUBLIC proxy — the one CI and every user reads — still has
# nothing.
#
# forge is a PUBLIC module. Asking the public endpoints over plain HTTP is
# both the honest question and immune to whatever the local environment
# believes. release-forge.sh's immutable-version gate already talks to the
# proxy this way; this is the same technique pointed at the opposite question.
#
# ── Both endpoints, because they fail independently ───────────────────
#
# The proxy serving the .info is necessary but not sufficient. A default
# `go mod tidy` also consults sum.golang.org, and the sumdb records a hash
# only after the proxy has fetched and hashed the tree. Waiting on the proxy
# alone leaves a smaller but real window where resolution still fails with a
# checksum-database error, which reads as a SECURITY failure rather than a
# propagation delay and sends people looking in entirely the wrong place.
#
# ── The window, and the two outcomes ──────────────────────────────────
#
# 10 minutes with capped exponential backoff. The common case resolves in
# seconds and stays fast; the slow case must not hammer the endpoints.
#
# The window covers ingestion lag alone. It used to be 35 minutes, sized for
# proxy.golang.org's NEGATIVE cache: "if someone requested the version before
# the tag was pushed, it may take up to 30 minutes for the mirror's cache to
# expire" (proxy.golang.org FAQ), and release-forge.sh's immutable-version gate
# requested exactly this version's .info moments before tagging it (v0.1.45:
# gate at 02:06Z, push at 02:07:45Z, served from 02:37Z). That gate now asks
# the proxy's cache (/cached-only), which never fetches, so no negative entry is
# planted and ingestion is the only delay left. A caller that DID ask for the
# version before the tag existed can raise GOPROXY_WAIT_MAX_SECONDS.
#
# On timeout the message must say WHICH state we are in, because the two have
# opposite remedies:
#
#   tagged but not yet ingested — the tag is pushed and visible on the origin.
#     Nothing is wrong; the proxy is late. WAIT and re-run. Do NOT delete and
#     re-cut the tag: proxy.golang.org is immutable and re-cutting at a new
#     commit burns the version permanently.
#
#   not reachable at all — the tag could not be confirmed on the origin
#     either, so the push may not have landed. That is a real problem and the
#     release is not complete.
#
# TESTING SEAMS. GOPROXY_WAIT_BASE / GOSUMDB_WAIT_BASE point the probes at a
# fixture server, and GOPROXY_WAIT_MAX_SECONDS / GOPROXY_WAIT_FIRST_DELAY /
# GOPROXY_WAIT_MAX_DELAY shrink the window so a test exercises the timeout
# path in milliseconds. Nothing else differs between CI and test.

set -euo pipefail

usage() {
  cat >&2 <<'EOF'
usage: wait-for-go-proxy.sh --module <path> --version <vX.Y.Z> [--tag-pushed]

Polls proxy.golang.org and sum.golang.org until both serve <module>@<version>,
or the window elapses.

  --module       module path, e.g. github.com/reliant-labs/forge
  --version      exact version, e.g. v0.1.30
  --tag-pushed   the tag push reported success; changes a timeout from
                 "not reachable" to "tagged but not yet ingested"
  --skip-sumdb   wait on the proxy only (for a module the sumdb never sees)

Environment (testing seams):
  GOPROXY_WAIT_BASE         proxy base URL     (default: https://proxy.golang.org)
  GOSUMDB_WAIT_BASE         sumdb base URL     (default: https://sum.golang.org)
  GOPROXY_WAIT_MAX_SECONDS  total window, seconds (default: 600)
  GOPROXY_WAIT_FIRST_DELAY  first backoff delay, seconds (default: 5)
  GOPROXY_WAIT_MAX_DELAY    cap on any single sleep, seconds (default: 30)
EOF
  exit 2
}

MODULE=""
VERSION=""
TAG_PUSHED=0
SKIP_SUMDB=0

while [ $# -gt 0 ]; do
  case "$1" in
    --module) [ $# -ge 2 ] || usage; MODULE="$2"; shift 2 ;;
    --version) [ $# -ge 2 ] || usage; VERSION="$2"; shift 2 ;;
    --tag-pushed) TAG_PUSHED=1; shift ;;
    --skip-sumdb) SKIP_SUMDB=1; shift ;;
    -h|--help) usage ;;
    *) echo "unknown argument: $1" >&2; usage ;;
  esac
done

[ -n "$MODULE" ] || { echo "--module is required" >&2; usage; }
[ -n "$VERSION" ] || { echo "--version is required" >&2; usage; }

PROXY_BASE="${GOPROXY_WAIT_BASE:-https://proxy.golang.org}"
SUMDB_BASE="${GOSUMDB_WAIT_BASE:-https://sum.golang.org}"
MAX_SECONDS="${GOPROXY_WAIT_MAX_SECONDS:-600}"
DELAY="${GOPROXY_WAIT_FIRST_DELAY:-5}"
MAX_DELAY="${GOPROXY_WAIT_MAX_DELAY:-30}"

# http_status echoes the status code for a URL, or "000" when curl could not
# reach it at all. An unreachable endpoint is NOT treated as a 404: "the proxy
# says no such version" and "I could not ask the proxy" are different facts,
# and only the first one means "keep waiting, it will appear."
http_status() {
  local url="$1" code
  code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 30 "$url" 2>/dev/null)" || code="000"
  [ -n "$code" ] || code="000"
  printf '%s\n' "$code"
}

proxy_url="$PROXY_BASE/$MODULE/@v/$VERSION.info"
sumdb_url="$SUMDB_BASE/lookup/$MODULE@$VERSION"

if [ "$SKIP_SUMDB" -eq 1 ]; then
  echo "waiting for $MODULE@$VERSION on the module proxy (up to ${MAX_SECONDS}s)..."
else
  echo "waiting for $MODULE@$VERSION on the module proxy and checksum db (up to ${MAX_SECONDS}s)..."
fi

elapsed=0
attempt=0
proxy_ok=0
sumdb_ok=0

while :; do
  attempt=$((attempt + 1))

  if [ "$proxy_ok" -eq 0 ]; then
    code="$(http_status "$proxy_url")"
    if [ "$code" = "200" ]; then
      proxy_ok=1
      echo "  proxy.golang.org serves $MODULE@$VERSION (after ${elapsed}s)"
    fi
  fi

  # The sumdb is only asked once the proxy has the version: the sumdb records a
  # hash only after the proxy fetched the tree, so probing it earlier is a
  # guaranteed miss and only burns requests.
  if [ "$SKIP_SUMDB" -eq 1 ]; then
    sumdb_ok=1
  elif [ "$proxy_ok" -eq 1 ] && [ "$sumdb_ok" -eq 0 ]; then
    code="$(http_status "$sumdb_url")"
    if [ "$code" = "200" ]; then
      sumdb_ok=1
      echo "  sum.golang.org has a checksum for $MODULE@$VERSION (after ${elapsed}s)"
    fi
  fi

  if [ "$proxy_ok" -eq 1 ] && [ "$sumdb_ok" -eq 1 ]; then
    echo "✅ $MODULE@$VERSION is ingested and resolvable (after ${elapsed}s, ${attempt} attempt(s))"
    exit 0
  fi

  if [ "$elapsed" -ge "$MAX_SECONDS" ]; then
    break
  fi

  # Never sleep past the end of the window.
  remaining=$((MAX_SECONDS - elapsed))
  if [ "$DELAY" -gt "$remaining" ]; then
    DELAY="$remaining"
  fi
  if [ "$proxy_ok" -eq 0 ]; then
    pending="proxy"
  else
    pending="checksum db"
  fi
  echo "  attempt ${attempt}: ${pending} has not ingested it yet, waiting ${DELAY}s (${elapsed}s elapsed)"
  sleep "$DELAY"
  elapsed=$((elapsed + DELAY))

  DELAY=$((DELAY * 2))
  if [ "$DELAY" -gt "$MAX_DELAY" ]; then
    DELAY="$MAX_DELAY"
  fi
done

if [ "$TAG_PUSHED" -eq 1 ]; then
  echo "::error::$MODULE@$VERSION is TAGGED BUT NOT YET INGESTED. The tag push reported success, so the release itself is fine — proxy.golang.org (and/or sum.golang.org) is still catching up, which has been observed to take longer than ${MAX_SECONDS}s. Do NOT delete and re-cut the tag: the proxy is immutable and re-cutting at a different commit burns $VERSION permanently. Wait, then re-run this check or confirm by hand with 'GOPROXY=proxy.golang.org GOFLAGS=-mod=mod go mod download $MODULE@$VERSION'. Anything that resolves $VERSION (E2E Scaffold, a consumer bump) will keep failing until it lands." >&2
else
  echo "::error::$MODULE@$VERSION IS NOT RESOLVABLE and the tag push did not report success, so the tag may never have landed on the origin. This is not propagation delay — confirm the tag exists ('git ls-remote --tags origin $VERSION') before cutting anything that depends on it." >&2
fi
exit 1
