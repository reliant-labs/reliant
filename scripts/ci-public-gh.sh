#!/usr/bin/env bash
# A deliberately narrow `gh api` adapter for the public Forge PR compatibility
# checks: it reads this repository's PR metadata and nothing else. Local
# callers can still use gh with their own auth.
#
# GITHUB_TOKEN, when set, authenticates the read. ci.yml's pr-checks job hands
# the workflow's read-only token (contents: read, on a public repository) to
# the two steps that use this adapter, because an anonymous read shares one
# 60-an-hour limit per runner IP, which a shared runner exhausts (forge#2459:
# a 403 turned a PR with no API break red). The token reaches curl on stdin,
# never argv, and goes only to api.github.com. Without it the read is anonymous.
# GH_TOKEN and the user's curl configuration are never read.
#
# A rate limit (HTTP 429, or a 403 carrying the rate-limit headers or message),
# a 5xx or a transport failure is retried: after Retry-After, or until
# X-RateLimit-Reset, when that fits in the wait budget (60s per invocation),
# else after 2s, 4s, 8s. When it still fails the adapter exits 75 (EX_TEMPFAIL)
# with a line saying it is infrastructure, so a caller can tell "GitHub could
# not be read" from a verdict. Any other HTTP error exits 1, naming the status.
set -euo pipefail

[[ ${1:-} == api ]] || exit 2
shift
paginate=false
if [[ ${1:-} == --paginate ]]; then paginate=true; shift; fi
endpoint=${1:-}
shift || exit 2
filter='.'
if [[ $# == 2 && $1 == --jq ]]; then filter=$2; shift 2; fi
[[ $# == 0 ]] || exit 2
case "$endpoint" in
  repos/reliant-labs/forge/*) ;;
  *) echo 'ci-public-gh: public Forge PR metadata only' >&2; exit 2 ;;
esac
[[ $endpoint =~ ^repos/reliant-labs/forge/(pulls/[1-9][0-9]*|issues/[1-9][0-9]*/(labels|events))$ ]] || exit 2
if $paginate; then [[ $endpoint == */events ]] || exit 2; fi

attempts=4 # per request
budget=60  # seconds of waiting, across every retry of this invocation
waited=0
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# get URL: one request, no redirects. Sets $status (000 on a transport
# failure) and leaves the headers, body and curl's error in $tmp.
get() {
  local rc=0
  rm -f "$tmp/headers" "$tmp/body"
  status=$(
    if [[ -n ${GITHUB_TOKEN:-} ]]; then printf 'Authorization: Bearer %s\n' "$GITHUB_TOKEN"; fi |
      curl -q --silent --show-error --connect-timeout 10 --max-time 25 -H @- \
        -H 'Accept: application/vnd.github+json' -H 'X-GitHub-Api-Version: 2022-11-28' \
        -D "$tmp/headers" -o "$tmp/body" -w '%{http_code}' "$1" 2>"$tmp/err"
  ) || rc=$?
  if ((rc != 0)); then status=000; fi
}

# header NAME: the last response's header value, or nothing.
header() {
  [[ -f $tmp/headers ]] || return 0
  grep -i "^$1:" "$tmp/headers" | tail -n 1 | cut -d: -f2- | tr -d ' \r' || true
}

rate_limited() {
  [[ $(header x-ratelimit-remaining) == 0 || -n $(header retry-after) ]] ||
    grep -qi 'rate limit' "$tmp/body" 2>/dev/null
}

# retry_wait ATTEMPT: seconds to wait before the next attempt; nothing when
# GitHub's own retry time does not fit the remaining budget.
retry_wait() {
  local after reset left=$((budget - waited))
  after=$(header retry-after)
  reset=$(header x-ratelimit-reset)
  if [[ $after =~ ^[0-9]+$ ]]; then
    ((after <= left)) && echo "$after"
  elif [[ $(header x-ratelimit-remaining) == 0 && $reset =~ ^[0-9]+$ ]]; then
    after=$((reset - $(date +%s) + 1))
    ((after < 1)) && after=1
    ((after <= left)) && echo "$after"
  else
    echo $((2 ** $1))
  fi
  return 0
}

reset_note() {
  local reset when
  reset=$(header x-ratelimit-reset)
  [[ $reset =~ ^[0-9]+$ ]] || return 0
  when=$(date -u -d "@$reset" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || echo "epoch $reset")
  echo "; the limit resets at $when"
}

# fetch URL: the body into $tmp/body, retrying what a later attempt may pass.
fetch() {
  local url=$1 what=${1%%\?*} attempt reason delay msg
  what=${what#https://api.github.com/}
  for ((attempt = 1; ; attempt++)); do
    get "$url"
    case $status in
      2??) return 0 ;;
      000) reason="GitHub API unreachable ($(head -n 1 "$tmp/err"))" ;;
      429) reason='GitHub API rate limit (HTTP 429)' ;;
      5??) reason="GitHub API error (HTTP $status)" ;;
      *)
        if [[ $status == 403 ]] && rate_limited; then
          reason='GitHub API rate limit (HTTP 403)'
        else
          msg=$(jq -r '.message? // empty' "$tmp/body" 2>/dev/null || true)
          echo "ci-public-gh: GitHub API returned HTTP $status reading $what${msg:+: $msg}" >&2
          exit 1
        fi
        ;;
    esac
    delay=$(retry_wait "$attempt")
    if ((attempt >= attempts)) || [[ -z $delay ]] || ((waited + delay > budget)); then
      echo "ci-public-gh: infrastructure: $reason reading $what after $attempt attempt(s)$(reset_note); this is not a verdict on the pull request" >&2
      exit 75
    fi
    echo "ci-public-gh: $reason reading $what; retrying in ${delay}s" >&2
    sleep "$delay"
    waited=$((waited + delay))
  done
}

# No redirects, and no token but GITHUB_TOKEN. API/parse failures must
# propagate, including a later page: partial history cannot approve a PR.
for ((page = 1; page <= 30; page++)); do
  fetch "https://api.github.com/$endpoint?per_page=100&page=$page"
  response=$(<"$tmp/body")
  if [[ $endpoint == */labels || $endpoint == */events ]]; then
    jq -e 'type == "array"' <<<"$response" >/dev/null
  else
    jq -e 'type == "object" and (.labels | type == "array") and (.body == null or (.body | type == "string"))' <<<"$response" >/dev/null
  fi
  jq -cr "$filter" <<<"$response"
  if ! $paginate || [[ $(jq length <<<"$response") -lt 100 ]]; then exit 0; fi
done
echo 'ci-public-gh: too many label-event pages; cannot establish approval' >&2
exit 1
