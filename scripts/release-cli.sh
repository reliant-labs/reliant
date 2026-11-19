#!/usr/bin/env bash
# release-cli.sh — the standalone `reliant` CLI release assets and the public
# daemon channel manifest (forge docs/proposals/self-upgrade.md §6, phase R0).
#
# release.yml's build-backend matrix builds `reliant-backend` once per target for
# the Electron installers. These are the same bytes, repackaged as the CLI a
# self-run daemon installs and later upgrades itself from. Nothing is built here.
# Every decision release.yml's cli-archives and cli-publish jobs make lives in this
# file, so scripts/test-release-cli.sh can prove it offline. Design, schema and
# how to verify: dev-docs/cli-release-artifacts.md.
#
#   release-cli.sh package <backend-dir> <out-dir>
#       Write reliant_<os>_<arch>.tar.gz for the six targets, and checksums.txt
#       (`<sha256>  <file>`, sha256sum format), from build-backend's artifacts as
#       download-artifact unpacks them: <backend-dir>/backend-<os>-<arch>/
#       reliant-backend[.exe]. Each archive holds `reliant` (`reliant.exe`),
#       LICENSE and LICENSES.txt at its root. With GNU tar the archives are
#       reproducible: sorted, owner 0, mtime $SOURCE_DATE_EPOCH (default 0).
#
#   release-cli.sh verify <dir>
#       Refuse unless <dir> holds exactly the six archives, checksums.txt lists
#       exactly them and each matches, and each archive holds its binary.
#
#   release-cli.sh predicate <owner/repo> <tag> <commit>
#       Print the attestation predicate, {repository, release, commit}.
#
#   release-cli.sh manifest <current-file> <channel> <tag> <commit> <commit-time-unix> <dir>
#       Print daemon-channels.json with <channel> (alpha|latest) pointing at this
#       release's archives in <dir>; every other channel is kept as it is in
#       <current-file> (absent or empty: the first manifest). Refuses a current
#       manifest of another schema, and moving a channel to an older commit.
#
#   release-cli.sh fetch-manifest <out-file>
#       Copy the published daemon-channels.json from R2 to <out-file>. No manifest
#       yet leaves <out-file> absent; any other failure is fatal (never overwrite a
#       manifest that could not be read). Needs R2_ENDPOINT (and aws creds).
#
#   release-cli.sh publish <tag> <dir>
#       Upload <dir>'s verified archives, checksums.txt, daemon-channels.json and
#       reliant-release.sigstore.json under cli/<tag>/ on R2, then the manifest to
#       the bucket root LAST, so no client reads a manifest before its files.
#       Refuses a manifest that moves any published channel backwards (re-running an
#       old release). DRY_RUN=true prints the plan and uploads nothing.
#
#   release-cli.sh verify-published <tag> <channel> [--base URL] [--wait SECONDS]
#       Read the manifest as a client does, through the CDN, until <channel> names
#       <tag>; then every URL it names returns 200 and its sha256s equal the
#       published checksums.txt.
set -euo pipefail

die() { echo "::error::$*" >&2; exit 1; }

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TARGETS="linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64"
BUCKET=downloads
MANIFEST=daemon-channels.json
SIGNATURE=reliant-release.sigstore.json
SCHEMA=1
BASE_URL="${BASE_URL:-https://downloads.reliantlabs.io}"
BASE_URL="${BASE_URL%/}"
TAG_RE='^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'

sha256_of() {
  if command -v sha256sum > /dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

words() { tr '\n' ' ' <<< "$1" | sed 's/ *$//'; }

bin_for() { if [ "$1" = windows ]; then echo reliant.exe; else echo reliant; fi; }

archives() {
  local t
  for t in $TARGETS; do echo "reliant_${t%/*}_${t#*/}.tar.gz"; done
}

check_tag() { [[ "$1" =~ $TAG_RE ]] || die "release tag must look like vX.Y.Z, got '$1'"; }
check_channel() { case "$1" in alpha | latest) ;; *) die "channel must be alpha or latest, got '$1'" ;; esac; }

cmd_package() {
  [ $# -eq 2 ] || die "usage: package <backend-dir> <out-dir>"
  local src="$1" out="$2" epoch="${SOURCE_DATE_EPOCH:-0}" t os arch bin from dir name stage
  [ -d "$src" ] || die "no backend directory $src"
  [[ "$epoch" =~ ^[0-9]+$ ]] || die "SOURCE_DATE_EPOCH must be unix seconds, got '$epoch'"
  mkdir -p "$out"
  rm -f "$out"/reliant_*.tar.gz "$out/checksums.txt"
  stage="$(mktemp -d)"
  # shellcheck disable=SC2064 # expand now: stage is local
  trap "rm -rf '$stage'" RETURN
  : > "$stage/checksums.txt"
  for t in $TARGETS; do
    os="${t%/*}" arch="${t#*/}"
    bin="$(bin_for "$os")"
    from="$src/backend-$os-$arch/reliant-backend"
    [ "$os" = windows ] && from="$from.exe"
    [ -f "$from" ] || die "no $os/$arch backend at $from (build-backend's artifact backend-$os-$arch)"
    dir="$stage/$os-$arch"
    mkdir -p "$dir"
    cp "$from" "$dir/$bin"
    cp "$ROOT/LICENSE" "$ROOT/LICENSES.txt" "$dir/"
    chmod 0755 "$dir/$bin"
    chmod 0644 "$dir/LICENSE" "$dir/LICENSES.txt"
    name="reliant_${os}_${arch}.tar.gz"
    if tar --version 2> /dev/null | grep -q 'GNU tar'; then
      tar -C "$dir" --sort=name --owner=0 --group=0 --numeric-owner --mtime="@$epoch" \
        -cf - "$bin" LICENSE LICENSES.txt | gzip -n -9 > "$out/$name"
    else
      COPYFILE_DISABLE=1 tar -C "$dir" -cf - "$bin" LICENSE LICENSES.txt | gzip -n -9 > "$out/$name"
    fi
    printf '%s  %s\n' "$(sha256_of "$out/$name")" "$name" >> "$stage/checksums.txt"
    echo "packaged $name" >&2
  done
  # checksums.txt appears only once every archive exists.
  mv "$stage/checksums.txt" "$out/checksums.txt"
}

cmd_verify() {
  [ $# -eq 1 ] || die "usage: verify <dir>"
  local dir="$1" want listed present line sum name t bin
  [ -f "$dir/checksums.txt" ] || die "$dir has no checksums.txt"
  want="$(archives | sort)"
  present="$(cd "$dir" && find . -maxdepth 1 -name 'reliant_*.tar.gz' | sed 's|^\./||' | sort)"
  [ "$present" = "$want" ] || die "$dir holds [$(words "$present")], want exactly [$(words "$want")]"
  listed="$(awk '{ sub(/^\*/, "", $2); print $2 }' "$dir/checksums.txt" | sort)"
  [ "$listed" = "$want" ] || die "checksums.txt lists [$(words "$listed")], want exactly [$(words "$want")]"
  while read -r line; do
    sum="${line%% *}" name="${line##* }"
    name="${name#\*}"
    [ "$(sha256_of "$dir/$name")" = "$sum" ] || die "$name does not match checksums.txt"
  done < "$dir/checksums.txt"
  for t in $TARGETS; do
    bin="$(bin_for "${t%/*}")"
    tar -tzf "$dir/reliant_${t%/*}_${t#*/}.tar.gz" | grep -qx "$bin" ||
      die "reliant_${t%/*}_${t#*/}.tar.gz does not hold $bin at its root"
  done
}

cmd_predicate() {
  [ $# -eq 3 ] || die "usage: predicate <owner/repo> <tag> <commit>"
  local repo="$1" tag="$2" commit="$3"
  [[ "$repo" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || die "repository must be owner/name, got '$repo'"
  check_tag "$tag"
  [[ "$commit" =~ ^[0-9a-f]{40}$ ]] || die "commit must be a full sha, got '$commit'"
  jq -cn --arg repo "$repo" --arg release "$tag" --arg commit "$commit" \
    '{repository: $repo, release: $release, commit: $commit}'
}

# assert_not_backwards <current-manifest> <new-manifest>: no channel the current
# manifest publishes may name an older commit in the new one, unless it is the
# same release (a re-run).
assert_not_backwards() {
  local back
  [ -s "$1" ] || return 0
  back="$(jq -r --slurpfile new "$2" '
    .channels // {} | to_entries[]
    | select(($new[0].channels[.key] // null) == null
        or (($new[0].channels[.key].tag != .value.tag)
            and ($new[0].channels[.key].commit_time_unix < .value.commit_time_unix)))
    | "\(.key) (\(.value.tag) -> \($new[0].channels[.key].tag // "nothing"))"' "$1")" ||
    die "cannot compare $1 with $2"
  [ -z "$back" ] || die "the manifest would move a channel backwards: $(words "$back"); a release never replaces a newer one"
}

cmd_manifest() {
  [ $# -eq 6 ] || die "usage: manifest <current-file> <channel> <tag> <commit> <commit-time-unix> <dir>"
  local current="$1" channel="$2" tag="$3" commit="$4" ctime="$5" dir="$6"
  local repo="${GITHUB_REPOSITORY:-reliant-labs/reliant}" prefix platforms entry base new
  check_channel "$channel"
  check_tag "$tag"
  [[ "$commit" =~ ^[0-9a-f]{40}$ ]] || die "commit must be a full sha, got '$commit'"
  # release-identity.sh --commit-time prints 0 when it cannot read the commit:
  # an unordered build must never become a channel's target.
  [[ "$ctime" =~ ^[1-9][0-9]*$ ]] || die "commit time must be positive unix seconds, got '$ctime'"
  cmd_verify "$dir"
  base='{}'
  if [ -s "$current" ]; then
    base="$(jq -c . "$current")" || die "$current is not JSON"
    [ "$(jq -r '.schema' <<< "$base")" = "$SCHEMA" ] ||
      die "$current is schema $(jq -r '.schema' <<< "$base"), this script writes $SCHEMA: update it before publishing"
  fi
  prefix="$BASE_URL/cli/$tag"
  platforms="$(awk '{ sub(/^\*/, "", $2); print $1, $2 }' "$dir/checksums.txt" | jq -Rn --arg p "$prefix" '
    [inputs | split(" ") | {
      key: (.[1] | capture("^reliant_(?<os>[a-z]+)_(?<arch>[a-z0-9]+)\\.tar\\.gz$") | "\(.os)/\(.arch)"),
      value: {url: "\($p)/\(.[1])", sha256: .[0]}}]
    | from_entries')"
  entry="$(jq -cn --arg tag "$tag" --arg commit "$commit" --argjson ctime "$ctime" --arg p "$prefix" \
    --arg sums "$(sha256_of "$dir/checksums.txt")" --arg sig "$SIGNATURE" --argjson platforms "$platforms" '
    {version: ($tag | ltrimstr("v")), tag: $tag, commit: $commit, commit_time_unix: $ctime,
     checksums: {url: "\($p)/checksums.txt", sha256: $sums},
     bundle: {url: "\($p)/\($sig)"},
     platforms: $platforms}')"
  new="$(jq -S --argjson schema "$SCHEMA" --arg repo "$repo" --arg ch "$channel" --argjson e "$entry" '
    .schema = $schema | .repository = $repo
    | .written_by = {tag: $e.tag, commit: $e.commit, bundle: $e.bundle}
    | .channels = ((.channels // {}) + {($ch): $e})' <<< "$base")"
  local tmp
  tmp="$(mktemp)"
  printf '%s\n' "$base" > "$tmp.cur"
  printf '%s\n' "$new" > "$tmp"
  assert_not_backwards "$tmp.cur" "$tmp"
  rm -f "$tmp" "$tmp.cur"
  printf '%s\n' "$new"
}

cmd_fetch_manifest() {
  [ $# -eq 1 ] || die "usage: fetch-manifest <out-file>"
  local out="$1" err
  rm -f "$out"
  [ -n "${R2_ENDPOINT:-}" ] || die "R2_ENDPOINT is not set"
  if err="$(aws s3api head-object --bucket "$BUCKET" --key "$MANIFEST" --endpoint-url "$R2_ENDPOINT" 2>&1 > /dev/null)"; then
    aws s3 cp "s3://$BUCKET/$MANIFEST" "$out" --endpoint-url "$R2_ENDPOINT" --only-show-errors ||
      die "cannot read s3://$BUCKET/$MANIFEST"
    jq -e . "$out" > /dev/null || die "s3://$BUCKET/$MANIFEST is not JSON"
    echo "read the published $MANIFEST" >&2
  elif grep -q '(404)\|Not Found' <<< "$err"; then
    echo "no $MANIFEST is published yet: this release writes the first" >&2
  else
    die "cannot tell whether s3://$BUCKET/$MANIFEST exists: $err"
  fi
}

upload() { # upload <file> <key> [aws s3 cp flags...]
  local f="$1" key="$2"
  shift 2
  echo "publish $key"
  [ "${DRY_RUN:-}" = true ] ||
    aws s3 cp "$f" "s3://$BUCKET/$key" --endpoint-url "$R2_ENDPOINT" --acl public-read --only-show-errors "$@"
}

cmd_publish() {
  [ $# -eq 2 ] || die "usage: publish <tag> <dir>"
  local tag="$1" dir="$2" f current
  check_tag "$tag"
  cmd_verify "$dir"
  for f in "$MANIFEST" "$SIGNATURE"; do
    [ -s "$dir/$f" ] || die "$dir has no $f"
  done
  [ "$(jq -r '.written_by.tag' "$dir/$MANIFEST")" = "$tag" ] ||
    die "$dir/$MANIFEST was written by $(jq -r '.written_by.tag' "$dir/$MANIFEST"), not $tag"
  if [ "${DRY_RUN:-}" != true ]; then
    [ -n "${R2_ENDPOINT:-}" ] || die "R2_ENDPOINT is not set"
    current="$(mktemp)"
    cmd_fetch_manifest "$current"
    assert_not_backwards "$current" "$dir/$MANIFEST"
    rm -f "$current"
  fi
  while read -r f; do
    upload "$dir/$f" "cli/$tag/$f"
  done < <(archives)
  upload "$dir/checksums.txt" "cli/$tag/checksums.txt" --content-type text/plain
  upload "$dir/$SIGNATURE" "cli/$tag/$SIGNATURE" --content-type application/json
  upload "$dir/$MANIFEST" "cli/$tag/$MANIFEST" --content-type application/json
  # The one mutable object, last.
  upload "$dir/$MANIFEST" "$MANIFEST" --content-type application/json --cache-control "public, max-age=60"
}

http_code() { curl -sI -o /dev/null -w '%{http_code}' --max-time 30 "$1" 2> /dev/null || echo 000; }

cmd_verify_published() {
  [ $# -ge 2 ] || die "usage: verify-published <tag> <channel> [--base URL] [--wait SECONDS]"
  local tag="$1" channel="$2" wait=600 base="$BASE_URL" body="" got deadline url fail=0 sums name sum tmp
  shift 2
  while [ $# -gt 0 ]; do
    case "$1" in
      --base) base="${2:?--base needs a URL}"; shift 2 ;;
      --wait) wait="${2:?--wait needs seconds}"; shift 2 ;;
      *) die "verify-published: unknown argument $1" ;;
    esac
  done
  base="${base%/}"
  check_tag "$tag"
  check_channel "$channel"
  deadline=$(($(date +%s) + wait))
  while :; do
    body="$(curl -fsS --max-time 30 "$base/$MANIFEST" 2> /dev/null || true)"
    got="$(jq -r --arg ch "$channel" '.channels[$ch].tag // empty' <<< "$body" 2> /dev/null || true)"
    [ "$got" = "$tag" ] && break
    if [ "$(date +%s)" -ge "$deadline" ]; then
      die "$base/$MANIFEST names ${got:-nothing} for $channel, not $tag"
    fi
    sleep 20
  done
  echo "ok    $base/$MANIFEST names $tag for $channel"
  [ "$(jq -r '.schema' <<< "$body")" = "$SCHEMA" ] || die "$base/$MANIFEST is not schema $SCHEMA"
  while read -r url; do
    if [ "$(http_code "$url")" = 200 ]; then echo "ok    200 $url"; else echo "FAIL  $url"; fail=1; fi
  done < <(jq -r --arg ch "$channel" '.channels[$ch] | (.platforms[].url), .checksums.url, .bundle.url' <<< "$body")
  tmp="$(mktemp)"
  curl -fsS --max-time 30 -o "$tmp" "$(jq -r --arg ch "$channel" '.channels[$ch].checksums.url' <<< "$body")" 2> /dev/null ||
    die "cannot read the channel's checksums.txt"
  sums="$(cat "$tmp")"
  if [ "$(sha256_of "$tmp")" = "$(jq -r --arg ch "$channel" '.channels[$ch].checksums.sha256' <<< "$body")" ]; then
    echo "ok    sha256 checksums.txt"
  else
    echo "FAIL  sha256 of checksums.txt differs from the manifest"
    fail=1
  fi
  rm -f "$tmp"
  while read -r name sum; do
    if grep -Fqx "$sum  $name" <<< "$sums"; then echo "ok    sha256 $name"; else echo "FAIL  sha256 of $name differs from checksums.txt"; fail=1; fi
  done < <(jq -r --arg ch "$channel" '.channels[$ch].platforms[] | "\(.url | split("/") | last) \(.sha256)"' <<< "$body")
  [ "$fail" = 0 ] || die "the $channel channel's CLI assets are not all published"
}

sub="${1:-}"
[ $# -gt 0 ] && shift
case "$sub" in
  package) cmd_package "$@" ;;
  verify) cmd_verify "$@" ;;
  predicate) cmd_predicate "$@" ;;
  manifest) cmd_manifest "$@" ;;
  fetch-manifest) cmd_fetch_manifest "$@" ;;
  publish) cmd_publish "$@" ;;
  verify-published) cmd_verify_published "$@" ;;
  *) die "usage: release-cli.sh package|verify|predicate|manifest|fetch-manifest|publish|verify-published ... (see the header)" ;;
esac
