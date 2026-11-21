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
#
# The API's JSON is read with jq when it is on PATH, else node, else python3:
# ci.yml's Playwright jobs run setup inside Playwright's container image, which
# has node but no jq, and installing jq from apt would bring back the Ubuntu
# mirror dependency that image removes (reliant#2880).
set -euo pipefail
repo="$1" tag="$2" asset="$3" out="$4" want="${5:-}"
retry=(--retry 5 --retry-all-errors --retry-delay 5 --connect-timeout 20 --max-time 300)
auth=(-H "X-GitHub-Api-Version: 2022-11-28")
[ -z "${GH_TOKEN:-}" ] || auth+=(-H "Authorization: Bearer ${GH_TOKEN}")

# asset_field <field>: prints <field> of the release's asset named $asset, or ""
# when the asset has no such field. Fails when the release has no such asset.
asset_field() {
  local meta
  # Command substitutions run without errexit, so each failure returns explicitly.
  meta="$(curl -fsSL "${retry[@]}" "${auth[@]}" -H "Accept: application/vnd.github+json" "https://api.github.com/repos/${repo}/releases/tags/${tag}")" || return 1
  if command -v jq >/dev/null 2>&1; then
    jq -er --arg n "$asset" --arg f "$1" \
      'first(.assets[] | select(.name == $n)) | .[$f] // "" | tostring' <<<"$meta"
  elif command -v node >/dev/null 2>&1; then
    node -e '
      const [name, field] = process.argv.slice(1);
      const a = (JSON.parse(require("fs").readFileSync(0, "utf8")).assets || []).find((x) => x.name === name);
      if (!a) process.exit(4);
      console.log(a[field] == null ? "" : String(a[field]));' "$asset" "$1" <<<"$meta"
  elif command -v python3 >/dev/null 2>&1; then
    python3 -c '
import json, sys
name, field = sys.argv[1:3]
a = next((x for x in json.load(sys.stdin).get("assets") or [] if x.get("name") == name), None)
if a is None: sys.exit(4)
v = a.get(field)
print("" if v is None else v)' "$asset" "$1" <<<"$meta"
  else
    echo "::error::reading the release's API metadata needs jq, node or python3; none is on PATH" >&2
    exit 1
  fi || { echo "::error::${repo}@${tag} has no asset ${asset} in its API metadata" >&2; return 1; }
}

if ! curl -fsSL "${retry[@]}" -o "$out" "https://github.com/${repo}/releases/download/${tag}/${asset}"; then
  echo "::warning::github.com release download of ${repo}@${tag}/${asset} failed; fetching it through the API"
  id="$(asset_field id)"
  [ -n "$id" ] || { echo "::error::${repo}@${tag}/${asset}: the API records no asset id" >&2; exit 1; }
  curl -fsSL "${retry[@]}" "${auth[@]}" -H "Accept: application/octet-stream" -o "$out" \
    "https://api.github.com/repos/${repo}/releases/assets/${id}"
fi

if [ -z "$want" ]; then
  want="$(asset_field digest)"
  want="${want#sha256:}"
  [ -n "$want" ] || { echo "::error::${repo}@${tag}/${asset}: no pinned sha256 and the API records no digest" >&2; exit 1; }
fi
echo "${want}  ${out}" | sha256sum -c -
