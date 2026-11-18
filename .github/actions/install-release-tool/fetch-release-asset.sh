#!/usr/bin/env bash
# fetch-release-asset.sh <owner/repo> <tag> <asset> <out> [sha256]
#
# Downloads one GitHub release asset to <out> and checks it.
#
# It tries github.com's release URL first, with retries. If that keeps failing,
# it falls back to the same asset through the REST API (api.github.com ... /assets/<id>).
# The fallback is not a duplicate: on 2026-11-08 from ~08:46Z, github.com/<o>/<r>/releases/download
# answered 504 for every asset (Task, ripgrep), failing every job at setup, while the API's
# asset endpoint served the same bytes.
#
# The checksum is the pinned [sha256] when given. Otherwise it's the digest the API
# records for the asset. An asset with neither is refused: nothing unverified is installed.
# GH_TOKEN (the job's github.token) authenticates the API calls.
set -euo pipefail
repo="$1" tag="$2" asset="$3" out="$4" want="${5:-}"
retry=(--retry 5 --retry-all-errors --retry-delay 5 --connect-timeout 20 --max-time 300)
auth=(-H "X-GitHub-Api-Version: 2022-11-28")
[ -z "${GH_TOKEN:-}" ] || auth+=(-H "Authorization: Bearer ${GH_TOKEN}")

meta=""
release_meta() {
  [ -n "$meta" ] || meta="$(curl -fsSL "${retry[@]}" "${auth[@]}" -H "Accept: application/vnd.github+json" "https://api.github.com/repos/${repo}/releases/tags/${tag}")"
  jq -ce --arg n "$asset" '.assets[] | select(.name == $n)' <<<"$meta"
}

if ! curl -fsSL "${retry[@]}" -o "$out" "https://github.com/${repo}/releases/download/${tag}/${asset}"; then
  echo "::warning::github.com release download of ${repo}@${tag}/${asset} failed; fetching it through the API"
  id="$(release_meta | jq -r '.id')"
  curl -fsSL "${retry[@]}" "${auth[@]}" -H "Accept: application/octet-stream" -o "$out" \
    "https://api.github.com/repos/${repo}/releases/assets/${id}"
fi

if [ -z "$want" ]; then
  want="$(release_meta | jq -r '.digest // "" | sub("^sha256:"; "")')"
  [ -n "$want" ] || { echo "::error::${repo}@${tag}/${asset}: no pinned sha256 and the API records no digest" >&2; exit 1; }
fi
echo "${want}  ${out}" | sha256sum -c -
