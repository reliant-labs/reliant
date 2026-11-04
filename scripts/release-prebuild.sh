#!/usr/bin/env bash
# release-prebuild.sh — the decisions behind release.yml's Electron prebuild reuse.
#
# rc-train dispatches release.yml with prebuild_version=vX.Y.Z (and dry_run) on the RC
# commit, so the ~45m Electron build overlaps the rest of the release. With the
# repository variable ELECTRON_REUSE_PREBUILD=true, the tag push of vX.Y.Z on that
# same commit publishes the prebuild's signed artifacts instead of building again.
# Everything that decides WHETHER a prebuild is reused and WHAT is published lives
# here, so scripts/test-release-prebuild.sh can prove it without a runner.
#
#   release-prebuild.sh marker <version> <sha> <web-dist> <out-file>
#       Write the prebuild marker: the version, the commit, and the identity the
#       renderer was built with (<web-dist>/version.json). Fails when the renderer
#       does not report <version>: a prebuild ships unchanged inside the release.
#
#   release-prebuild.sh find <owner/repo> <tag> <sha>
#       Print the id of a successful prebuild run of <tag> on exactly <sha> whose
#       marker matches (version, commit, renderer identity) and whose three platform
#       artifacts are still there. Prints nothing when there is none: build instead.
#
#   release-prebuild.sh publish <mac|linux|win> <channel> <version> <dist-dir>
#       Upload a platform's prebuilt artifacts to R2 exactly where electron-builder
#       --publish puts them (the s3 provider of electron-builder*.js: the bucket
#       root of `downloads`), every update feed LAST so no client reads a feed
#       before its files. Refuses, uploading nothing, unless every feed that
#       platform's clients poll is present and names <version>. DRY_RUN=true
#       prints the plan and uploads nothing. Needs R2_ENDPOINT (and aws creds).
set -euo pipefail

die() { echo "::error::$*" >&2; exit 1; }

# The update feeds electron-builder writes per platform, which clients poll. The
# Windows feed is `<channel>.yml`, not `<channel>-win.yml`; Linux arm64 has its own
# (release.yml's "Purge Cloudflare CDN cache" step lists the same set).
feeds_for() {
  case "$1" in
    mac) echo "$2-mac.yml" ;;
    linux) echo "$2-linux.yml $2-linux-arm64.yml" ;;
    win) echo "$2.yml" ;;
    *) die "unknown platform '$1' (mac, linux, win)" ;;
  esac
}

files_for() {
  case "$1" in
    mac) echo "*.dmg *.zip *.blockmap" ;;
    linux) echo "*.AppImage *.deb *.blockmap" ;;
    win) echo "*.exe *.blockmap" ;;
    *) die "unknown platform '$1' (mac, linux, win)" ;;
  esac
}

cmd_marker() {
  [ $# -eq 4 ] || die "usage: marker <version> <sha> <web-dist> <out-file>"
  local version="$1" sha="$2" dist="$3" out="$4" renderer
  [[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "prebuild version must be vX.Y.Z, got '$version'"
  renderer="$(jq -r '.version // empty' "$dist/version.json" 2>/dev/null)" || true
  [ "$renderer" = "${version#v}" ] || die "the renderer in $dist reports version '${renderer:-<none>}', not ${version#v}: build it with RELEASE_VERSION=$version"
  mkdir -p "$(dirname "$out")"
  jq -n --arg v "$version" --arg s "$sha" --arg r "$renderer" '{version:$v, sha:$s, renderer:$r}' > "$out"
}

cmd_find() {
  [ $# -eq 3 ] || die "usage: find <owner/repo> <tag> <sha>"
  local repo="$1" tag="$2" sha="$3" ids id names tmp m
  ids="$(gh run list -R "$repo" --workflow release.yml --event workflow_dispatch \
    --commit "$sha" --status success --limit 20 --json databaseId --jq '.[].databaseId')" ||
    { echo "Cannot list prebuild runs of $tag on $sha: building" >&2; return 0; }
  tmp="$(mktemp -d)"
  # shellcheck disable=SC2064 # expand now: tmp is local
  trap "rm -rf '$tmp'" RETURN
  for id in $ids; do
    names="$(gh api "repos/$repo/actions/runs/$id/artifacts?per_page=100" \
      --jq '.artifacts[] | select(.expired | not) | .name')" || continue
    grep -qx "prebuild-$tag" <<< "$names" || continue
    local missing=""
    for a in macos-builds linux-builds windows-builds; do
      grep -qx "$a" <<< "$names" || missing="$missing $a"
    done
    if [ -n "$missing" ]; then
      echo "Prebuild run $id of $tag lacks${missing}: not reusing it" >&2
      continue
    fi
    if ! gh run download "$id" -R "$repo" -n "prebuild-$tag" -D "$tmp/$id" > /dev/null; then
      echo "Cannot download the marker of prebuild run $id: not reusing it" >&2
      continue
    fi
    m="$tmp/$id/prebuild.json"
    if [ "$(jq -r .version "$m" 2>/dev/null)" != "$tag" ] ||
      [ "$(jq -r .sha "$m" 2>/dev/null)" != "$sha" ] ||
      [ "$(jq -r .renderer "$m" 2>/dev/null)" != "${tag#v}" ]; then
      echo "Prebuild run $id's marker ($(tr -d '\n' < "$m" 2>/dev/null)) is not $tag on $sha: not reusing it" >&2
      continue
    fi
    echo "Publishing prebuild run $id of $tag ($sha) instead of building" >&2
    echo "$id"
    return 0
  done
  echo "No prebuild of $tag on $sha: building" >&2
}

cmd_publish() {
  [ $# -eq 4 ] || die "usage: publish <mac|linux|win> <channel> <version> <dist-dir>"
  local platform="$1" channel="$2" version="$3" dist="$4" f got pat
  local -a files=() feeds=() pats=()
  [ -d "$dist" ] || die "no prebuild directory $dist"
  read -r -a pats <<< "$(files_for "$platform")"
  read -r -a feeds <<< "$(feeds_for "$platform" "$channel")"
  cd "$dist"
  shopt -s nullglob
  for pat in "${pats[@]}"; do
    # shellcheck disable=SC2206 # the unquoted glob is the point
    files+=($pat)
  done
  shopt -u nullglob
  [ ${#files[@]} -gt 0 ] || die "the $platform prebuild has no artifacts"
  for f in "${feeds[@]}"; do
    [ -f "$f" ] || die "the $platform prebuild has no update feed $f: its clients would stay on the previous release"
    got="$(sed -n 's/^version:[[:space:]]*//p' "$f" | head -1 | tr -d "\"' \r")"
    [ "$got" = "${version#v}" ] || die "update feed $f names version '${got:-<none>}', not ${version#v}"
  done
  [ "${DRY_RUN:-}" = true ] || [ -n "${R2_ENDPOINT:-}" ] || die "R2_ENDPOINT is not set"
  for f in "${files[@]}" "${feeds[@]}"; do
    echo "publish $f"
    [ "${DRY_RUN:-}" = true ] ||
      aws s3 cp "$f" "s3://downloads/$f" --endpoint-url "$R2_ENDPOINT" --acl public-read --only-show-errors
  done
}

sub="${1:-}"
[ $# -gt 0 ] && shift
case "$sub" in
  marker) cmd_marker "$@" ;;
  find) cmd_find "$@" ;;
  publish) cmd_publish "$@" ;;
  *) die "usage: release-prebuild.sh marker|find|publish ... (see the header)" ;;
esac
